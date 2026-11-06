package serverhandlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/server"
	"github.com/opensvc/oc3/util/echolog"
	"github.com/opensvc/oc3/util/git"
	"github.com/opensvc/oc3/util/logkey"
	"github.com/opensvc/oc3/xauth"
)

// reportKind is one of the objects of the reports, as the API handles them: the
// reports and the charts, changed by a ReportsManager responsible for them, and
// the metrics, changed by the managers only, as the historical collector did.
type reportKind struct {
	obj cdb.ReportObject
	// label starts the log messages: "Report", "Chart", "Metric".
	label string
	// dirKey is the setting of the directory of the git histories, one
	// repository per object holding its definition in a file named gitFile, as
	// the historical collector keeps them under private/<gitFile>.
	dirKey  string
	gitFile string
	// managerOnly: the Manager privilege is required to change the object,
	// rather than ReportsManager and the responsibility for it.
	managerOnly bool
}

var (
	reportKindReport = reportKind{obj: cdb.ReportObjectReport, label: "Report",
		dirKey: "server.directories.reports", gitFile: "reports"}
	reportKindChart = reportKind{obj: cdb.ReportObjectChart, label: "Chart",
		dirKey: "server.directories.charts", gitFile: "charts"}
	reportKindMetric = reportKind{obj: cdb.ReportObjectMetric, label: "Metric",
		dirKey: "server.directories.metrics", gitFile: "metrics", managerOnly: true}
)

func (k reportKind) track() git.Track {
	return git.Track{Dir: viper.GetString(k.dirKey), File: k.gitFile}
}

// requirePrivilege refuses a caller who may not change this kind of objects.
func (k reportKind) requirePrivilege(c echo.Context) error {
	if !IsAuthByUser(c) {
		return httpErrorf(http.StatusUnauthorized, "user authentication required")
	}
	if k.managerOnly {
		if !IsManager(c) {
			return httpErrorf(http.StatusForbidden, "Manager privilege required")
		}
		return nil
	}
	if !IsReportsManager(c) {
		return httpErrorf(http.StatusForbidden, "ReportsManager privilege required")
	}
	return nil
}

// reportCommit records a new definition in the history of the object. As the
// historical collector, a failed commit does not fail the change: it is logged.
func (a *Api) reportCommit(ctx context.Context, c echo.Context, log *slog.Logger, k reportKind, id int, content string) {
	caller, err := a.formCaller(ctx, c)
	if err != nil {
		log.Error("cannot read the caller", logkey.Error, err)
	}
	if err := k.track().Commit(strconv.Itoa(id), content, caller.author()); err != nil {
		log.Error("cannot record the revision", "kind", k.obj.Kind, "id", id, logkey.Error, err)
	}
}

// reportLog writes an audit log entry of a change of the reports.
func (a *Api) reportLog(ctx context.Context, c echo.Context, log *slog.Logger, action, format string, dict map[string]any) {
	userEmail, _ := c.Get(XUserEmail).(string)
	if err := a.ODB.Log(ctx, cdb.LogEntry{Action: action, User: userEmail, Fmt: format, Dict: dict, Level: "info"}); err != nil {
		log.Error("cannot write audit log", logkey.Error, err)
	}
}

func (a *Api) reportNotify(ctx context.Context, log *slog.Logger) {
	if err := a.ODB.Session.NotifyChanges(ctx); err != nil {
		log.Error("cannot notify changes", logkey.Error, err)
	}
}

// reportObjectID resolves the id of an object, given as an id or, as the
// historical get_report_id() and alike accept, as a name.
func (a *Api) reportObjectID(ctx context.Context, log *slog.Logger, k reportKind, s string) (int, error) {
	if id, err := strconv.Atoi(s); err == nil {
		return id, nil
	}
	id, found, err := a.ODB.ReportObjectByName(ctx, k.obj, s)
	if err != nil {
		return 0, httpInternal(log, "cannot resolve the "+k.obj.Kind, err)
	}
	if !found {
		return 0, httpErrorf(http.StatusNotFound, "%s %s not found", k.label, s)
	}
	return id, nil
}

// requireVisible refuses a caller the object is not published to, unless a
// manager.
func (a *Api) requireReportObjectVisible(ctx context.Context, c echo.Context, log *slog.Logger, k reportKind, id int) error {
	ok, err := a.ODB.ReportObjectVisible(ctx, k.obj, id, UserGroupsFromContext(c), IsManager(c))
	if err != nil {
		return httpInternal(log, "cannot check the "+k.obj.Kind+" publication", err)
	}
	if !ok {
		return httpErrorf(http.StatusNotFound, "%s %d not found or not published to you", k.label, id)
	}
	return nil
}

// requireChange refuses a caller who may not change the object: the privilege
// of its kind, and, but for a manager, the responsibility for it.
func (a *Api) requireReportObjectChange(ctx context.Context, c echo.Context, log *slog.Logger, k reportKind, id int) (*cdb.ReportObjectRow, error) {
	if err := k.requirePrivilege(c); err != nil {
		return nil, err
	}
	row, err := a.ODB.ReportObjectRowByID(ctx, k.obj, id)
	if err != nil {
		return nil, httpInternal(log, "cannot read the "+k.obj.Kind, err)
	}
	if row == nil {
		return nil, httpErrorf(http.StatusNotFound, "%s %d not found", k.label, id)
	}
	ok, err := a.ODB.ReportObjectResponsible(ctx, k.obj, id, UserGroupsFromContext(c), IsManager(c))
	if err != nil {
		return nil, httpInternal(log, "cannot check the "+k.obj.Kind+" responsibility", err)
	}
	if !ok {
		return nil, httpErrorf(http.StatusForbidden, "You are not responsible for the %s %d", k.obj.Kind, id)
	}
	return row, nil
}

// checkReportObjectFields checks a new name and definition of a report or a
// chart, and returns the name trimmed.
func checkReportObjectFields(k reportKind, name, definition *string) (*string, error) {
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if trimmed == "" {
			return nil, fmt.Errorf("%s cannot be empty", k.obj.NameCol)
		}
		if utf8.RuneCountInString(trimmed) > 100 {
			return nil, fmt.Errorf("%s is longer than 100 characters", k.obj.NameCol)
		}
		name = &trimmed
	}
	if definition != nil && strings.TrimSpace(*definition) != "" {
		var parsed any
		if err := yaml.Unmarshal([]byte(*definition), &parsed); err != nil {
			return nil, fmt.Errorf("%s is not valid YAML: %w", k.obj.DefCol, err)
		}
	}
	return name, nil
}

// updateReportObject changes the name and the definition of a report or a
// chart, logs the change and records the definition in its history.
func (a *Api) updateReportObject(c echo.Context, handlerName string, k reportKind, idStr string, name, definition *string, get func(ctx context.Context, id string, p cdb.ListParams) ([]map[string]any, error)) error {
	log := echolog.GetLogHandler(c, handlerName)
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	id, err := a.reportObjectID(ctx, log, k, idStr)
	if err != nil {
		return httpProblem(c, err)
	}
	row, err := a.requireReportObjectChange(ctx, c, log, k, id)
	if err != nil {
		return httpProblem(c, err)
	}
	name, err = checkReportObjectFields(k, name, definition)
	if err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	var changes []string
	if name != nil && *name != row.Name {
		otherID, taken, err := a.ODB.ReportObjectByName(ctx, k.obj, *name)
		if err != nil {
			return httpProblem(c, httpInternal(log, "cannot check the name", err))
		}
		if taken && otherID != id {
			return JSONProblemf(c, http.StatusConflict, "a %s named %s already exists: %d", k.obj.Kind, *name, otherID)
		}
		changes = append(changes, fmt.Sprintf("%s: %s => %s", k.obj.NameCol, row.Name, *name))
	} else {
		name = nil
	}
	if definition != nil && *definition != row.Definition {
		// The definition may be long: the log says it changed, the history holds it.
		changes = append(changes, k.obj.DefCol+" changed")
	} else {
		definition = nil
	}
	if len(changes) > 0 {
		if err := a.ODB.UpdateReportObject(ctx, k.obj, id, name, definition); err != nil {
			return httpProblem(c, httpInternal(log, "cannot update the "+k.obj.Kind, err))
		}
		a.reportLog(ctx, c, log, k.obj.Kind+".change", k.label+" %("+k.obj.NameCol+")s change: %(data)s",
			map[string]any{k.obj.NameCol: row.Name, "data": strings.Join(changes, ", ")})
		if definition != nil {
			a.reportCommit(ctx, c, log, k, id, *definition)
		}
		a.reportNotify(ctx, log)
	}
	return a.handleItem(c, handlerName, k.obj.Kind, "id", strconv.Itoa(id), listEndpointParams{},
		func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
			return get(ctx, strconv.Itoa(id), p)
		})
}

// deleteReportObject deletes an object with its publications and responsibles,
// and, for a metric, its historized series. Its history is kept.
func (a *Api) deleteReportObject(ctx context.Context, c echo.Context, log *slog.Logger, k reportKind, idStr string) (map[string]any, error) {
	id, err := a.reportObjectID(ctx, log, k, idStr)
	if err != nil {
		return nil, err
	}
	row, err := a.requireReportObjectChange(ctx, c, log, k, id)
	if err != nil {
		return nil, err
	}
	if err := a.ODB.DeleteReportObject(ctx, k.obj, id); err != nil {
		return nil, httpInternal(log, "cannot delete the "+k.obj.Kind, err)
	}
	if k.obj.Kind == "metric" {
		if dir := statsDirectory(); dir != "" {
			if err := os.RemoveAll(filepath.Join(dir, "metrics", strconv.Itoa(id))); err != nil {
				log.Error("cannot remove the series of the metric", "metric_id", id, logkey.Error, err)
			}
		}
	}
	format := k.label + " %(" + k.obj.NameCol + ")s deleted"
	d := map[string]any{k.obj.NameCol: row.Name}
	a.reportLog(ctx, c, log, k.obj.Kind+".del", format, d)
	return map[string]any{"info": pyFormat(format, d)}, nil
}

func (a *Api) deleteReportObjectOne(c echo.Context, handlerName string, k reportKind, idStr string) error {
	log := echolog.GetLogHandler(c, handlerName)
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	result, err := a.deleteReportObject(ctx, c, log, k, idStr)
	if err != nil {
		return httpProblem(c, err)
	}
	a.reportNotify(ctx, log)
	return c.JSON(http.StatusOK, result)
}

// deleteReportObjectBulk takes the ids from the body key id, one entry or a
// list, as the historical DELETE /reports and alike do.
func (a *Api) deleteReportObjectBulk(c echo.Context, handlerName string, k reportKind) error {
	log := echolog.GetLogHandler(c, handlerName)
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	entries, isList, err := decodeEntries(c)
	if err != nil {
		return httpProblem(c, err)
	}
	defer a.reportNotify(ctx, log)
	return runEntries(c, entries, isList, func(entry map[string]any) (map[string]any, error) {
		id, ok := entryString(entry, "id")
		if !ok {
			return nil, httpErrorf(http.StatusBadRequest, "The 'id' key is mandatory")
		}
		return a.deleteReportObject(ctx, c, log, k, id)
	})
}

// reportObjectAmIResponsible answers whether the caller may change the object.
func (a *Api) reportObjectAmIResponsible(c echo.Context, handlerName string, k reportKind, idStr string) error {
	log := echolog.GetLogHandler(c, handlerName)
	ctx := c.Request().Context()
	id, err := a.reportObjectID(ctx, log, k, idStr)
	if err != nil {
		if isNotFound(err) {
			return c.JSON(http.StatusOK, map[string]any{"data": false})
		}
		return httpProblem(c, err)
	}
	if k.requirePrivilege(c) != nil {
		return c.JSON(http.StatusOK, map[string]any{"data": false})
	}
	ok, err := a.ODB.ReportObjectResponsible(ctx, k.obj, id, UserGroupsFromContext(c), IsManager(c))
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot check the "+k.obj.Kind+" responsibility", err))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": ok})
}

// getReportObjectTeam lists the groups the object is published to, or
// responsible for it, for a caller it is published to.
func (a *Api) getReportObjectTeam(c echo.Context, handlerName string, k reportKind, responsibles bool, idStr string, p listEndpointParams) error {
	log := echolog.GetLogHandler(c, handlerName)
	ctx := c.Request().Context()
	id, err := a.reportObjectID(ctx, log, k, idStr)
	if err != nil {
		return httpProblem(c, err)
	}
	if err := a.requireReportObjectVisible(ctx, c, log, k, id); err != nil {
		return httpProblem(c, err)
	}
	table := k.obj.TeamTable(responsibles)
	return a.handleList(c, handlerName, "auth_group", p, func(ctx context.Context, lp cdb.ListParams) ([]map[string]any, error) {
		return a.ODB.GetReportObjectTeam(ctx, k.obj, table, id, lp)
	})
}

// isNotFound tells whether err is a request refused with a 404.
func isNotFound(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.status == http.StatusNotFound
}

// reportTeamWord is how the log messages name a team link.
func reportTeamWord(responsibles bool) string {
	if responsibles {
		return "responsibility"
	}
	return "publication"
}

// changeReportObjectTeam links or unlinks an object and a group. Adding resolves
// the group by id or role, among the caller's groups for a non-manager, and
// refuses privilege groups, as the forms do. An existing link, or a missing one,
// answers with an info message rather than an error.
func (a *Api) changeReportObjectTeam(ctx context.Context, c echo.Context, log *slog.Logger, k reportKind, responsibles, add bool, idStr, groupIDStr string) (map[string]any, error) {
	id, err := a.reportObjectID(ctx, log, k, idStr)
	if err != nil {
		return nil, err
	}
	if _, err := a.requireReportObjectChange(ctx, c, log, k, id); err != nil {
		return nil, err
	}
	table := k.obj.TeamTable(responsibles)
	action := k.obj.Kind + "." + map[bool]string{true: "responsible", false: "publication"}[responsibles] +
		"." + map[bool]string{true: "add", false: "delete"}[add]
	prefix := k.label + " %(" + k.obj.LinkCol + ")s " + reportTeamWord(responsibles) + " to group %(group_id)s"

	if !add {
		groupID, err := strconv.ParseInt(groupIDStr, 10, 64)
		if err != nil {
			return nil, httpErrorf(http.StatusBadRequest, "invalid group id %q", groupIDStr)
		}
		d := map[string]any{k.obj.LinkCol: strconv.Itoa(id), "group_id": groupIDStr}
		exists, err := a.ODB.ReportObjectTeamExists(ctx, k.obj, table, id, groupID)
		if err != nil {
			return nil, httpInternal(log, "cannot check the group link", err)
		}
		if !exists {
			return map[string]any{"info": pyFormat(prefix+" already removed", d)}, nil
		}
		if err := a.ODB.DeleteReportObjectTeam(ctx, k.obj, table, id, groupID); err != nil {
			return nil, httpInternal(log, "cannot unlink the group", err)
		}
		a.reportLog(ctx, c, log, action, prefix+" removed", d)
		return map[string]any{"info": pyFormat(prefix+" removed", d)}, nil
	}

	var userGroupIDs []int64
	isManager := IsManager(c)
	if !isManager {
		user := UserInfoFromContext(c)
		if user == nil {
			return nil, httpErrorf(http.StatusUnauthorized, "missing user context")
		}
		userID, err := strconv.ParseInt(user.GetExtensions().Get(xauth.XUserID), 10, 64)
		if err != nil {
			return nil, httpErrorf(http.StatusBadRequest, "invalid user id")
		}
		if userGroupIDs, err = a.ODB.UserGroupIDs(ctx, userID); err != nil {
			return nil, httpInternal(log, "cannot list user groups", err)
		}
	}
	group, status, err := a.ODB.OrgGroup(ctx, groupIDStr, userGroupIDs, isManager)
	if err != nil {
		return nil, httpInternal(log, "cannot resolve the group", err)
	}
	switch status {
	case cdb.OrgGroupNotFound:
		return nil, httpErrorf(http.StatusNotFound, "Group not found: %s", groupIDStr)
	case cdb.OrgGroupAmbiguous:
		return nil, httpErrorf(http.StatusBadRequest, "Ambiguous group id: %s", groupIDStr)
	case cdb.OrgGroupPrivileged:
		return nil, httpErrorf(http.StatusForbidden, "Operation not allowed on privilege group: %s", group.Role)
	}
	d := map[string]any{k.obj.LinkCol: strconv.Itoa(id), "group_id": group.Role}
	exists, err := a.ODB.ReportObjectTeamExists(ctx, k.obj, table, id, group.ID)
	if err != nil {
		return nil, httpInternal(log, "cannot check the group link", err)
	}
	if exists {
		return map[string]any{"info": pyFormat(prefix+" already added", d)}, nil
	}
	if err := a.ODB.InsertReportObjectTeam(ctx, k.obj, table, id, group.ID); err != nil {
		return nil, httpInternal(log, "cannot link the group", err)
	}
	a.reportLog(ctx, c, log, action, prefix+" added", d)
	return map[string]any{"info": pyFormat(prefix+" added", d)}, nil
}

func (a *Api) reportObjectTeamOne(c echo.Context, handlerName string, k reportKind, responsibles, add bool, idStr, groupID string) error {
	log := echolog.GetLogHandler(c, handlerName)
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	result, err := a.changeReportObjectTeam(ctx, c, log, k, responsibles, add, idStr, groupID)
	if err != nil {
		return httpProblem(c, err)
	}
	a.reportNotify(ctx, log)
	return c.JSON(http.StatusOK, result)
}

// reportObjectTeamBulk takes the object and the group from the body keys
// <kind>_id and group_id, one entry or a list, as the historical
// /reports_publications and alike do.
func (a *Api) reportObjectTeamBulk(c echo.Context, handlerName string, k reportKind, responsibles, add bool) error {
	log := echolog.GetLogHandler(c, handlerName)
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	entries, isList, err := decodeEntries(c)
	if err != nil {
		return httpProblem(c, err)
	}
	defer a.reportNotify(ctx, log)
	return runEntries(c, entries, isList, func(entry map[string]any) (map[string]any, error) {
		id, ok := entryString(entry, k.obj.LinkCol)
		if !ok {
			return nil, httpErrorf(http.StatusBadRequest, "The '%s' key is mandatory", k.obj.LinkCol)
		}
		groupID, ok := entryString(entry, "group_id")
		if !ok {
			return nil, httpErrorf(http.StatusBadRequest, "The 'group_id' key is mandatory")
		}
		return a.changeReportObjectTeam(ctx, c, log, k, responsibles, add, id, groupID)
	})
}

// reportObjectHistoryID resolves the object of a history request, and tells
// whether the caller may read it: the historical handlers answer empty rather
// than an error otherwise.
func (a *Api) reportObjectHistoryID(ctx context.Context, c echo.Context, log *slog.Logger, k reportKind, idStr string) (int, bool, error) {
	id, err := a.reportObjectID(ctx, log, k, idStr)
	if err != nil {
		if isNotFound(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	ok, err := a.ODB.ReportObjectVisible(ctx, k.obj, id, UserGroupsFromContext(c), IsManager(c))
	if err != nil {
		return 0, false, httpInternal(log, "cannot check the "+k.obj.Kind+" publication", err)
	}
	return id, ok, nil
}

func (a *Api) getReportObjectRevisions(c echo.Context, handlerName string, k reportKind, idStr string) error {
	log := echolog.GetLogHandler(c, handlerName)
	ctx := c.Request().Context()
	id, allowed, err := a.reportObjectHistoryID(ctx, c, log, k, idStr)
	if err != nil {
		return httpProblem(c, err)
	}
	if !allowed {
		return c.JSON(http.StatusOK, map[string]any{"data": []git.Revision{}})
	}
	revisions, err := k.track().Timeline(strconv.Itoa(id))
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot read the history", err))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": revisions})
}

func (a *Api) getReportObjectRevision(c echo.Context, handlerName string, k reportKind, idStr, cid string) error {
	log := echolog.GetLogHandler(c, handlerName)
	ctx := c.Request().Context()
	if !git.ValidRev(cid) {
		return JSONProblemf(c, http.StatusBadRequest, "invalid revision %q", cid)
	}
	id, allowed, err := a.reportObjectHistoryID(ctx, c, log, k, idStr)
	if err != nil {
		return httpProblem(c, err)
	}
	if !allowed {
		return c.JSON(http.StatusOK, map[string]any{"data": git.Blob{}})
	}
	blob, err := k.track().At(strconv.Itoa(id), cid)
	if err != nil {
		return httpProblem(c, httpErrorf(http.StatusNotFound, "revision %s not found", cid))
	}
	if blob == nil {
		return c.JSON(http.StatusOK, map[string]any{"data": git.Blob{}})
	}
	return c.JSON(http.StatusOK, map[string]any{"data": blob})
}

func (a *Api) getReportObjectDiff(c echo.Context, handlerName string, k reportKind, idStr, cid string, other *string) error {
	log := echolog.GetLogHandler(c, handlerName)
	ctx := c.Request().Context()
	if !git.ValidRev(cid) || (other != nil && *other != "" && !git.ValidRev(*other)) {
		return JSONProblemf(c, http.StatusBadRequest, "invalid revision")
	}
	id, allowed, err := a.reportObjectHistoryID(ctx, c, log, k, idStr)
	if err != nil {
		return httpProblem(c, err)
	}
	if !allowed {
		return c.JSON(http.StatusOK, map[string]any{"data": ""})
	}
	var out string
	if other != nil && *other != "" {
		out, err = k.track().Diff(strconv.Itoa(id), cid, *other)
	} else {
		out, err = k.track().Show(strconv.Itoa(id), cid)
	}
	if err != nil {
		return httpProblem(c, httpErrorf(http.StatusNotFound, "revision %s not found", cid))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

// postReportObjectRollback restores the definition of a revision, recorded as a
// new revision, stores it in the object and logs it.
func (a *Api) postReportObjectRollback(c echo.Context, handlerName string, k reportKind, idStr, cid string) error {
	log := echolog.GetLogHandler(c, handlerName)
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	if !git.ValidRev(cid) {
		return JSONProblemf(c, http.StatusBadRequest, "invalid revision %q", cid)
	}
	id, err := a.reportObjectID(ctx, log, k, idStr)
	if err != nil {
		return httpProblem(c, err)
	}
	row, err := a.requireReportObjectChange(ctx, c, log, k, id)
	if err != nil {
		return httpProblem(c, err)
	}
	caller, err := a.formCaller(ctx, c)
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot read the caller", err))
	}
	track := k.track()
	key := strconv.Itoa(id)
	if err := track.Rollback(key, cid, caller.author()); err != nil {
		log.Error("cannot roll back", "kind", k.obj.Kind, "id", id, "cid", cid, logkey.Error, err)
		return httpProblem(c, httpErrorf(http.StatusNotFound, "cannot roll %s %d back to %s", k.obj.Kind, id, cid))
	}
	content, err := track.Read(key)
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot read the restored definition", err))
	}
	if err := a.ODB.UpdateReportObject(ctx, k.obj, id, nil, &content); err != nil {
		return httpProblem(c, httpInternal(log, "cannot store the restored definition", err))
	}
	// The historical handler logs nothing; a rollback changes the definition as
	// an edit does, which is logged.
	a.reportLog(ctx, c, log, k.obj.Kind+".rollback", k.label+" %("+k.obj.NameCol+")s rolled back to revision %(cid)s",
		map[string]any{k.obj.NameCol: row.Name, "cid": cid})
	a.reportNotify(ctx, log)
	return c.JSON(http.StatusOK, map[string]string{"info": fmt.Sprintf("%s %d rolled back to %s", k.obj.Kind, id, cid)})
}

// The handlers of the generated interface.

// PostReport handles POST /reports/{report_id}.
func (a *Api) PostReport(c echo.Context, reportId string) error {
	var body server.PostReportJSONRequestBody
	if err := c.Bind(&body); err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	return a.updateReportObject(c, "PostReport", reportKindReport, reportId, body.ReportName, body.ReportYaml, a.ODB.GetReport)
}

// PostChart handles POST /charts/{chart_id}.
func (a *Api) PostChart(c echo.Context, chartId string) error {
	var body server.PostChartJSONRequestBody
	if err := c.Bind(&body); err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	return a.updateReportObject(c, "PostChart", reportKindChart, chartId, body.ChartName, body.ChartYaml, a.ODB.GetChart)
}

// DeleteReport handles DELETE /reports/{report_id}.
func (a *Api) DeleteReport(c echo.Context, reportId string) error {
	return a.deleteReportObjectOne(c, "DeleteReport", reportKindReport, reportId)
}

// DeleteReports handles DELETE /reports.
func (a *Api) DeleteReports(c echo.Context) error {
	return a.deleteReportObjectBulk(c, "DeleteReports", reportKindReport)
}

// DeleteChart handles DELETE /charts/{chart_id}.
func (a *Api) DeleteChart(c echo.Context, chartId string) error {
	return a.deleteReportObjectOne(c, "DeleteChart", reportKindChart, chartId)
}

// DeleteCharts handles DELETE /charts.
func (a *Api) DeleteCharts(c echo.Context) error {
	return a.deleteReportObjectBulk(c, "DeleteCharts", reportKindChart)
}

// DeleteMetric handles DELETE /metrics/{metric_id}.
func (a *Api) DeleteMetric(c echo.Context, metricId string) error {
	return a.deleteReportObjectOne(c, "DeleteMetric", reportKindMetric, metricId)
}

// DeleteMetrics handles DELETE /metrics.
func (a *Api) DeleteMetrics(c echo.Context) error {
	return a.deleteReportObjectBulk(c, "DeleteMetrics", reportKindMetric)
}

// GetReportAmIResponsible handles GET /reports/{report_id}/am_i_responsible.
func (a *Api) GetReportAmIResponsible(c echo.Context, reportId string) error {
	return a.reportObjectAmIResponsible(c, "GetReportAmIResponsible", reportKindReport, reportId)
}

// GetChartAmIResponsible handles GET /charts/{chart_id}/am_i_responsible.
func (a *Api) GetChartAmIResponsible(c echo.Context, chartId string) error {
	return a.reportObjectAmIResponsible(c, "GetChartAmIResponsible", reportKindChart, chartId)
}

// GetMetricAmIResponsible handles GET /metrics/{metric_id}/am_i_responsible.
func (a *Api) GetMetricAmIResponsible(c echo.Context, metricId string) error {
	return a.reportObjectAmIResponsible(c, "GetMetricAmIResponsible", reportKindMetric, metricId)
}

func teamListParams(props *server.InQueryProps, limit *server.InQueryLimit, offset *server.InQueryOffset,
	meta *server.InQueryMeta, stats *server.InQueryStats, orderby *server.InQueryOrderby,
	groupby *server.InQueryGroupby, filter *server.InQueryFilter) listEndpointParams {
	return listEndpointParams{props: props, limit: limit, offset: offset, meta: meta, stats: stats,
		orderby: orderby, groupby: groupby, filter: filter}
}

// GetReportPublications handles GET /reports/{report_id}/publications.
func (a *Api) GetReportPublications(c echo.Context, reportId string, p server.GetReportPublicationsParams) error {
	return a.getReportObjectTeam(c, "GetReportPublications", reportKindReport, false, reportId,
		teamListParams(p.Props, p.Limit, p.Offset, p.Meta, p.Stats, p.Orderby, p.Groupby, p.Filter))
}

// GetReportResponsibles handles GET /reports/{report_id}/responsibles.
func (a *Api) GetReportResponsibles(c echo.Context, reportId string, p server.GetReportResponsiblesParams) error {
	return a.getReportObjectTeam(c, "GetReportResponsibles", reportKindReport, true, reportId,
		teamListParams(p.Props, p.Limit, p.Offset, p.Meta, p.Stats, p.Orderby, p.Groupby, p.Filter))
}

// GetChartPublications handles GET /charts/{chart_id}/publications.
func (a *Api) GetChartPublications(c echo.Context, chartId string, p server.GetChartPublicationsParams) error {
	return a.getReportObjectTeam(c, "GetChartPublications", reportKindChart, false, chartId,
		teamListParams(p.Props, p.Limit, p.Offset, p.Meta, p.Stats, p.Orderby, p.Groupby, p.Filter))
}

// GetChartResponsibles handles GET /charts/{chart_id}/responsibles.
func (a *Api) GetChartResponsibles(c echo.Context, chartId string, p server.GetChartResponsiblesParams) error {
	return a.getReportObjectTeam(c, "GetChartResponsibles", reportKindChart, true, chartId,
		teamListParams(p.Props, p.Limit, p.Offset, p.Meta, p.Stats, p.Orderby, p.Groupby, p.Filter))
}

// GetMetricPublications handles GET /metrics/{metric_id}/publications.
func (a *Api) GetMetricPublications(c echo.Context, metricId string, p server.GetMetricPublicationsParams) error {
	return a.getReportObjectTeam(c, "GetMetricPublications", reportKindMetric, false, metricId,
		teamListParams(p.Props, p.Limit, p.Offset, p.Meta, p.Stats, p.Orderby, p.Groupby, p.Filter))
}

// PostReportPublication handles POST /reports/{report_id}/publications/{group_id}.
func (a *Api) PostReportPublication(c echo.Context, reportId string, groupId string) error {
	return a.reportObjectTeamOne(c, "PostReportPublication", reportKindReport, false, true, reportId, groupId)
}

// DeleteReportPublication handles DELETE /reports/{report_id}/publications/{group_id}.
func (a *Api) DeleteReportPublication(c echo.Context, reportId string, groupId string) error {
	return a.reportObjectTeamOne(c, "DeleteReportPublication", reportKindReport, false, false, reportId, groupId)
}

// PostReportResponsible handles POST /reports/{report_id}/responsibles/{group_id}.
func (a *Api) PostReportResponsible(c echo.Context, reportId string, groupId string) error {
	return a.reportObjectTeamOne(c, "PostReportResponsible", reportKindReport, true, true, reportId, groupId)
}

// DeleteReportResponsible handles DELETE /reports/{report_id}/responsibles/{group_id}.
func (a *Api) DeleteReportResponsible(c echo.Context, reportId string, groupId string) error {
	return a.reportObjectTeamOne(c, "DeleteReportResponsible", reportKindReport, true, false, reportId, groupId)
}

// PostChartPublication handles POST /charts/{chart_id}/publications/{group_id}.
func (a *Api) PostChartPublication(c echo.Context, chartId string, groupId string) error {
	return a.reportObjectTeamOne(c, "PostChartPublication", reportKindChart, false, true, chartId, groupId)
}

// DeleteChartPublication handles DELETE /charts/{chart_id}/publications/{group_id}.
func (a *Api) DeleteChartPublication(c echo.Context, chartId string, groupId string) error {
	return a.reportObjectTeamOne(c, "DeleteChartPublication", reportKindChart, false, false, chartId, groupId)
}

// PostChartResponsible handles POST /charts/{chart_id}/responsibles/{group_id}.
func (a *Api) PostChartResponsible(c echo.Context, chartId string, groupId string) error {
	return a.reportObjectTeamOne(c, "PostChartResponsible", reportKindChart, true, true, chartId, groupId)
}

// DeleteChartResponsible handles DELETE /charts/{chart_id}/responsibles/{group_id}.
func (a *Api) DeleteChartResponsible(c echo.Context, chartId string, groupId string) error {
	return a.reportObjectTeamOne(c, "DeleteChartResponsible", reportKindChart, true, false, chartId, groupId)
}

// PostMetricPublication handles POST /metrics/{metric_id}/publications/{group_id}.
func (a *Api) PostMetricPublication(c echo.Context, metricId string, groupId string) error {
	return a.reportObjectTeamOne(c, "PostMetricPublication", reportKindMetric, false, true, metricId, groupId)
}

// DeleteMetricPublication handles DELETE /metrics/{metric_id}/publications/{group_id}.
func (a *Api) DeleteMetricPublication(c echo.Context, metricId string, groupId string) error {
	return a.reportObjectTeamOne(c, "DeleteMetricPublication", reportKindMetric, false, false, metricId, groupId)
}

// PostReportsPublications handles POST /reports_publications.
func (a *Api) PostReportsPublications(c echo.Context) error {
	return a.reportObjectTeamBulk(c, "PostReportsPublications", reportKindReport, false, true)
}

// DeleteReportsPublications handles DELETE /reports_publications.
func (a *Api) DeleteReportsPublications(c echo.Context) error {
	return a.reportObjectTeamBulk(c, "DeleteReportsPublications", reportKindReport, false, false)
}

// PostReportsResponsibles handles POST /reports_responsibles.
func (a *Api) PostReportsResponsibles(c echo.Context) error {
	return a.reportObjectTeamBulk(c, "PostReportsResponsibles", reportKindReport, true, true)
}

// DeleteReportsResponsibles handles DELETE /reports_responsibles.
func (a *Api) DeleteReportsResponsibles(c echo.Context) error {
	return a.reportObjectTeamBulk(c, "DeleteReportsResponsibles", reportKindReport, true, false)
}

// PostChartsPublications handles POST /charts_publications.
func (a *Api) PostChartsPublications(c echo.Context) error {
	return a.reportObjectTeamBulk(c, "PostChartsPublications", reportKindChart, false, true)
}

// DeleteChartsPublications handles DELETE /charts_publications.
func (a *Api) DeleteChartsPublications(c echo.Context) error {
	return a.reportObjectTeamBulk(c, "DeleteChartsPublications", reportKindChart, false, false)
}

// PostChartsResponsibles handles POST /charts_responsibles.
func (a *Api) PostChartsResponsibles(c echo.Context) error {
	return a.reportObjectTeamBulk(c, "PostChartsResponsibles", reportKindChart, true, true)
}

// DeleteChartsResponsibles handles DELETE /charts_responsibles.
func (a *Api) DeleteChartsResponsibles(c echo.Context) error {
	return a.reportObjectTeamBulk(c, "DeleteChartsResponsibles", reportKindChart, true, false)
}

// PostMetricsPublications handles POST /metrics_publications.
func (a *Api) PostMetricsPublications(c echo.Context) error {
	return a.reportObjectTeamBulk(c, "PostMetricsPublications", reportKindMetric, false, true)
}

// DeleteMetricsPublications handles DELETE /metrics_publications.
func (a *Api) DeleteMetricsPublications(c echo.Context) error {
	return a.reportObjectTeamBulk(c, "DeleteMetricsPublications", reportKindMetric, false, false)
}

// GetReportRevisions handles GET /reports/{report_id}/revisions.
func (a *Api) GetReportRevisions(c echo.Context, reportId string) error {
	return a.getReportObjectRevisions(c, "GetReportRevisions", reportKindReport, reportId)
}

// GetChartRevisions handles GET /charts/{chart_id}/revisions.
func (a *Api) GetChartRevisions(c echo.Context, chartId string) error {
	return a.getReportObjectRevisions(c, "GetChartRevisions", reportKindChart, chartId)
}

// GetMetricRevisions handles GET /metrics/{metric_id}/revisions.
func (a *Api) GetMetricRevisions(c echo.Context, metricId string) error {
	return a.getReportObjectRevisions(c, "GetMetricRevisions", reportKindMetric, metricId)
}

// GetReportRevision handles GET /reports/{report_id}/revisions/{cid}.
func (a *Api) GetReportRevision(c echo.Context, reportId string, cid string) error {
	return a.getReportObjectRevision(c, "GetReportRevision", reportKindReport, reportId, cid)
}

// GetChartRevision handles GET /charts/{chart_id}/revisions/{cid}.
func (a *Api) GetChartRevision(c echo.Context, chartId string, cid string) error {
	return a.getReportObjectRevision(c, "GetChartRevision", reportKindChart, chartId, cid)
}

// GetMetricRevision handles GET /metrics/{metric_id}/revisions/{cid}.
func (a *Api) GetMetricRevision(c echo.Context, metricId string, cid string) error {
	return a.getReportObjectRevision(c, "GetMetricRevision", reportKindMetric, metricId, cid)
}

// GetReportDiff handles GET /reports/{report_id}/diff/{cid}.
func (a *Api) GetReportDiff(c echo.Context, reportId string, cid string, params server.GetReportDiffParams) error {
	return a.getReportObjectDiff(c, "GetReportDiff", reportKindReport, reportId, cid, params.Other)
}

// GetChartDiff handles GET /charts/{chart_id}/diff/{cid}.
func (a *Api) GetChartDiff(c echo.Context, chartId string, cid string, params server.GetChartDiffParams) error {
	return a.getReportObjectDiff(c, "GetChartDiff", reportKindChart, chartId, cid, params.Other)
}

// GetMetricDiff handles GET /metrics/{metric_id}/diff/{cid}.
func (a *Api) GetMetricDiff(c echo.Context, metricId string, cid string, params server.GetMetricDiffParams) error {
	return a.getReportObjectDiff(c, "GetMetricDiff", reportKindMetric, metricId, cid, params.Other)
}

// PostReportRollback handles POST /reports/{report_id}/rollback/{cid}.
func (a *Api) PostReportRollback(c echo.Context, reportId string, cid string) error {
	return a.postReportObjectRollback(c, "PostReportRollback", reportKindReport, reportId, cid)
}

// PostChartRollback handles POST /charts/{chart_id}/rollback/{cid}.
func (a *Api) PostChartRollback(c echo.Context, chartId string, cid string) error {
	return a.postReportObjectRollback(c, "PostChartRollback", reportKindChart, chartId, cid)
}

// PostMetricRollback handles POST /metrics/{metric_id}/rollback/{cid}.
func (a *Api) PostMetricRollback(c echo.Context, metricId string, cid string) error {
	return a.postReportObjectRollback(c, "PostMetricRollback", reportKindMetric, metricId, cid)
}
