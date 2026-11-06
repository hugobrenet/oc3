package serverhandlers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
	"gopkg.in/yaml.v3"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/server"
	"github.com/opensvc/oc3/util/echolog"
	"github.com/opensvc/oc3/util/logkey"
)

// GetReports handles GET /reports
func (a *Api) GetReports(c echo.Context, params server.GetReportsParams) error {
	return a.handleList(c, "GetReports", "report", listEndpointParams{
		props: params.Props, limit: params.Limit, offset: params.Offset,
		meta: params.Meta, stats: params.Stats, orderby: params.Orderby, groupby: params.Groupby,
		filter: params.Filter,
	}, func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
		return a.ODB.GetReports(ctx, p)
	})
}

// GetReport handles GET /reports/{report_id}
func (a *Api) GetReport(c echo.Context, reportId string, params server.GetReportParams) error {
	return a.handleItem(c, "GetReport", "report", "id", reportId, listEndpointParams{props: params.Props},
		func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
			return a.ODB.GetReport(ctx, reportId, p)
		})
}

// checkReport checks the name and the definition of a new report, and returns the
// name trimmed.
func checkReport(name string, definition *string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("the report_name property is mandatory")
	}
	if utf8.RuneCountInString(name) > 100 {
		return "", fmt.Errorf("report_name is longer than 100 characters")
	}
	if definition != nil && strings.TrimSpace(*definition) != "" {
		var parsed any
		if err := yaml.Unmarshal([]byte(*definition), &parsed); err != nil {
			return "", fmt.Errorf("report_yaml is not valid YAML: %w", err)
		}
	}
	return name, nil
}

// PostReports handles POST /reports: create a report, published to the primary
// team of its author, which is also made responsible for it, as the historical
// collector did.
func (a *Api) PostReports(c echo.Context) error {
	log := echolog.GetLogHandler(c, "PostReports")
	odb := a.ODB
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()

	if !IsAuthByUser(c) {
		return denyRequest(c, http.StatusUnauthorized, "user authentication required")
	}
	if !IsReportsManager(c) {
		return denyRequest(c, http.StatusForbidden, "ReportsManager privilege required")
	}
	var body server.PostReportsJSONRequestBody
	if err := c.Bind(&body); err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	name, err := checkReport(body.ReportName, body.ReportYaml)
	if err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	log.Info("called", "report_name", name)

	if otherID, taken, err := odb.ReportByName(ctx, name); err != nil {
		log.Error("cannot check report name", logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot check report name")
	} else if taken {
		return JSONProblemf(c, http.StatusConflict, "a report named %s already exists: %d", name, otherID)
	}

	definition := ""
	if body.ReportYaml != nil {
		definition = *body.ReportYaml
	}
	id, err := odb.InsertReport(ctx, name, definition)
	if err != nil {
		log.Error("cannot create report", logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot create report")
	}

	if userID := authUserID(c); userID != nil {
		groupID, found, err := odb.UserPrimaryGroupID(ctx, *userID)
		if err != nil {
			log.Error("cannot read the primary group", logkey.Error, err)
		} else if found {
			if err := odb.AddReportTeams(ctx, id, groupID); err != nil {
				log.Error("cannot attach the report to its team", logkey.Error, err)
			}
		}
	}

	a.reportCommit(ctx, c, log, reportKindReport, id, definition)

	userEmail, _ := c.Get(XUserEmail).(string)
	if logErr := odb.Log(ctx, cdb.LogEntry{
		Action: "report.add",
		User:   userEmail,
		Fmt:    "Report %(report_name)s added",
		Dict:   map[string]any{"report_name": name},
		Level:  "info",
	}); logErr != nil {
		log.Error("cannot write audit log", logkey.Error, logErr)
	}
	if err := odb.Session.NotifyChanges(ctx); err != nil {
		log.Error("cannot notify changes", logkey.Error, err)
	}

	return a.handleItem(c, "PostReports", "report", "id", strconv.Itoa(id), listEndpointParams{},
		func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
			return odb.GetReport(ctx, strconv.Itoa(id), p)
		})
}
