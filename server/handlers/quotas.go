package serverhandlers

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/server"
	"github.com/opensvc/oc3/util/echolog"
)

// GetQuotas handles GET /quotas: the quotas of the application codes in the
// disk groups of the storage arrays, as the historical quota view lists them.
func (a *Api) GetQuotas(c echo.Context, params server.GetQuotasParams) error {
	return a.handleList(c, "GetQuotas", "disk_quota", listEndpointParams{
		props: params.Props, limit: params.Limit, offset: params.Offset,
		meta: params.Meta, stats: params.Stats, orderby: params.Orderby, groupby: params.Groupby,
		filter: params.Filter,
	}, func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
		return a.ODB.GetQuotas(ctx, p, 0)
	})
}

// GetArrayDiskgroupQuotas handles GET /arrays/{array_id}/diskgroups/{dg_id}/quotas.
func (a *Api) GetArrayDiskgroupQuotas(c echo.Context, arrayId string, dgId int64, params server.GetArrayDiskgroupQuotasParams) error {
	dg, err := a.ODB.DiskGroupOfArray(c.Request().Context(), arrayId, dgId)
	if err != nil {
		return httpProblem(c, httpInternal(echolog.GetLogHandler(c, "GetArrayDiskgroupQuotas"), "cannot find the disk group", err))
	}
	if dg == nil {
		return JSONProblemf(c, http.StatusNotFound, "disk group %d not found in array %s", dgId, arrayId)
	}
	return a.handleList(c, "GetArrayDiskgroupQuotas", "disk_quota", listEndpointParams{
		props: params.Props, limit: params.Limit, offset: params.Offset,
		meta: params.Meta, stats: params.Stats, orderby: params.Orderby, groupby: params.Groupby,
		filter: params.Filter,
	}, func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
		return a.ODB.GetQuotas(ctx, p, dg.ID)
	})
}

// PostArrayDiskgroupQuotas handles POST /arrays/{array_id}/diskgroups/{dg_id}/quotas:
// a quota for an application code in the disk group, added or, when it has
// one, changed, as rest_post_array_diskgroup_quotas does.
func (a *Api) PostArrayDiskgroupQuotas(c echo.Context, arrayId string, dgId int64) error {
	log := echolog.GetLogHandler(c, "PostArrayDiskgroupQuotas")
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	if err := requireStorageManager(c); err != nil {
		return err
	}
	var body server.QuotaInput
	if err := c.Bind(&body); err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	if body.Quota == nil {
		return JSONProblemf(c, http.StatusBadRequest, "the 'quota' key is mandatory")
	}
	if err := checkQuotaSize(*body.Quota); err != nil {
		return httpProblem(c, err)
	}
	dg, err := a.quotaDiskGroup(ctx, log, arrayId, dgId)
	if err != nil {
		return httpProblem(c, err)
	}
	app, err := a.quotaApp(ctx, log, body)
	if err != nil {
		return httpProblem(c, err)
	}
	if app == nil {
		return JSONProblemf(c, http.StatusBadRequest, "either 'app' or 'app_id' is mandatory")
	}
	current, err := a.ODB.QuotaOfApp(ctx, dg.ID, app.ID)
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot find the quota", err))
	}
	d := map[string]any{"quota": strconv.FormatInt(*body.Quota, 10), "app": app.App, "dg": dg.Name}
	var id int64
	var action, format string
	if current == nil {
		if id, err = a.ODB.InsertQuota(ctx, dg.ID, app.ID, *body.Quota); err != nil {
			return httpProblem(c, httpInternal(log, "cannot add the quota", err))
		}
		action, format = "quota.add", "%(quota)s quota added for app %(app)s in dg %(dg)s"
	} else {
		id = current.ID
		if err := a.ODB.UpdateQuota(ctx, id, app.ID, *body.Quota); err != nil {
			return httpProblem(c, httpInternal(log, "cannot update the quota", err))
		}
		action, format = "quota.change", "%(quota)s quota updated for app %(app)s in dg %(dg)s"
	}
	return a.quotaDone(c, ctx, log, dg.ID, id, action, format, d)
}

// PostArrayDiskgroupQuota handles POST /arrays/{array_id}/diskgroups/{dg_id}/quotas/{quota_id}:
// a quota changed, its size or the application code it is for, as
// rest_post_array_diskgroup_quota does.
func (a *Api) PostArrayDiskgroupQuota(c echo.Context, arrayId string, dgId int64, quotaId int64) error {
	log := echolog.GetLogHandler(c, "PostArrayDiskgroupQuota")
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	if err := requireStorageManager(c); err != nil {
		return err
	}
	var body server.QuotaInput
	if err := c.Bind(&body); err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	dg, quota, err := a.quotaInDiskGroup(ctx, log, arrayId, dgId, quotaId)
	if err != nil {
		return httpProblem(c, err)
	}
	size, appID, appName := quota.Quota, quota.AppID, quota.App
	if body.Quota != nil {
		if err := checkQuotaSize(*body.Quota); err != nil {
			return httpProblem(c, err)
		}
		size = *body.Quota
	}
	app, err := a.quotaApp(ctx, log, body)
	if err != nil {
		return httpProblem(c, err)
	}
	if app != nil && app.ID != quota.AppID {
		// One quota per application code in a disk group.
		if other, err := a.ODB.QuotaOfApp(ctx, dg.ID, app.ID); err != nil {
			return httpProblem(c, httpInternal(log, "cannot find the quota", err))
		} else if other != nil {
			return JSONProblemf(c, http.StatusConflict, "app %s already has a quota in dg %s", app.App, dg.Name)
		}
		appID, appName = app.ID, app.App
	}
	if err := a.ODB.UpdateQuota(ctx, quota.ID, appID, size); err != nil {
		return httpProblem(c, httpInternal(log, "cannot update the quota", err))
	}
	// What changed, in words, as beautify_change() writes it.
	var changes []string
	if size != quota.Quota {
		changes = append(changes, fmt.Sprintf("quota: %d => %d", quota.Quota, size))
	}
	if appID != quota.AppID {
		changes = append(changes, fmt.Sprintf("app: %s => %s", quota.App, appName))
	}
	if len(changes) == 0 {
		changes = append(changes, "nothing")
	}
	d := map[string]any{"quota": strconv.FormatInt(size, 10), "app": appName, "dg": dg.Name, "data": strings.Join(changes, ", ")}
	return a.quotaDone(c, ctx, log, dg.ID, quota.ID, "quota.change",
		"%(quota)s quota change for app %(app)s in dg %(dg)s: %(data)s", d)
}

// DeleteArrayDiskgroupQuota handles DELETE /arrays/{array_id}/diskgroups/{dg_id}/quotas/{quota_id},
// as rest_delete_array_diskgroup_quota does.
func (a *Api) DeleteArrayDiskgroupQuota(c echo.Context, arrayId string, dgId int64, quotaId int64) error {
	log := echolog.GetLogHandler(c, "DeleteArrayDiskgroupQuota")
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	if err := requireStorageManager(c); err != nil {
		return err
	}
	result, err := a.deleteQuota(c, ctx, log, arrayId, dgId, quotaId)
	if err != nil {
		return httpProblem(c, err)
	}
	if err := a.ODB.Session.NotifyChanges(ctx); err != nil {
		log.Error("cannot notify changes", "error", err)
	}
	return c.JSON(http.StatusOK, result)
}

// deleteQuota deletes a quota of a disk group, makes the space reserved in the
// disk group the sum of its quotas again and logs it (quota.del).
func (a *Api) deleteQuota(c echo.Context, ctx context.Context, log *slog.Logger, arrayID string, dgID, quotaID int64) (server.QuotaChangeResponse, error) {
	dg, quota, err := a.quotaInDiskGroup(ctx, log, arrayID, dgID, quotaID)
	if err != nil {
		return server.QuotaChangeResponse{}, err
	}
	if err := a.ODB.DeleteQuota(ctx, quota.ID); err != nil {
		return server.QuotaChangeResponse{}, httpInternal(log, "cannot delete the quota", err)
	}
	if err := a.ODB.RefreshDiskGroupReserved(ctx, dg.ID); err != nil {
		log.Error("cannot refresh the space reserved in the disk group", "dg_id", dg.ID, "error", err)
	}
	format := "%(quota)s quota deleted for app %(app)s in dg %(dg)s"
	d := map[string]any{"quota": strconv.FormatInt(quota.Quota, 10), "app": quota.App, "dg": dg.Name}
	a.auditLog(c, ctx, log, cdb.LogEntry{Action: "quota.del", Fmt: format, Dict: d})
	return server.QuotaChangeResponse{Info: pyFormat(format, d), Id: quota.ID}, nil
}

// requireStorageManager refuses a caller without the StorageManager privilege,
// a Manager holding it.
func requireStorageManager(c echo.Context) error {
	if !IsAuthByUser(c) {
		return denyRequest(c, http.StatusUnauthorized, "user authentication required")
	}
	if !IsManager(c) && !HasGroup(c, "StorageManager") {
		return denyRequest(c, http.StatusForbidden, "StorageManager privilege required")
	}
	return nil
}

// checkQuotaSize refuses a size the quota column cannot hold, an int(11) in MB.
func checkQuotaSize(size int64) error {
	if size < 0 || size > math.MaxInt32 {
		return httpErrorf(http.StatusBadRequest, "quota: %d MB is out of 0..%d", size, math.MaxInt32)
	}
	return nil
}

// quotaDiskGroup returns the disk group of the path, a 404 when the array has none
// of that id.
func (a *Api) quotaDiskGroup(ctx context.Context, log *slog.Logger, arrayID string, dgID int64) (*cdb.DiskGroup, error) {
	dg, err := a.ODB.DiskGroupOfArray(ctx, arrayID, dgID)
	if err != nil {
		return nil, httpInternal(log, "cannot find the disk group", err)
	}
	if dg == nil {
		return nil, httpErrorf(http.StatusNotFound, "dg %d not found in array %s", dgID, arrayID)
	}
	return dg, nil
}

// quotaInDiskGroup returns the disk group and the quota of the path, a 404
// when the quota is not one of that disk group.
func (a *Api) quotaInDiskGroup(ctx context.Context, log *slog.Logger, arrayID string, dgID, quotaID int64) (*cdb.DiskGroup, *cdb.Quota, error) {
	dg, err := a.quotaDiskGroup(ctx, log, arrayID, dgID)
	if err != nil {
		return nil, nil, err
	}
	quota, err := a.ODB.QuotaByID(ctx, quotaID)
	if err != nil {
		return nil, nil, httpInternal(log, "cannot find the quota", err)
	}
	if quota == nil || quota.DgID != dg.ID {
		return nil, nil, httpErrorf(http.StatusNotFound, "quota %d not found in dg %s", quotaID, dg.Name)
	}
	return dg, quota, nil
}

// quotaApp returns the application code the body names, by app_id or app; nil
// when it names none, a 404 when it does not exist.
func (a *Api) quotaApp(ctx context.Context, log *slog.Logger, body server.QuotaInput) (*cdb.App, error) {
	var ident string
	switch {
	case body.AppId != nil:
		ident = strconv.FormatInt(*body.AppId, 10)
	case body.App != nil && *body.App != "":
		ident = *body.App
	default:
		return nil, nil
	}
	app, err := a.ODB.GetApp(ctx, ident, nil, true)
	if err != nil {
		return nil, httpInternal(log, "cannot find the app", err)
	}
	if app == nil {
		return nil, httpErrorf(http.StatusNotFound, "app %s not found", ident)
	}
	return app, nil
}

// quotaDone ends a change of a quota: the space reserved in the disk group
// made the sum of its quotas again, the change logged, the pages told.
func (a *Api) quotaDone(c echo.Context, ctx context.Context, log *slog.Logger, dgID, quotaID int64, action, format string, d map[string]any) error {
	if err := a.ODB.RefreshDiskGroupReserved(ctx, dgID); err != nil {
		log.Error("cannot refresh the space reserved in the disk group", "dg_id", dgID, "error", err)
	}
	a.auditLog(c, ctx, log, cdb.LogEntry{Action: action, Fmt: format, Dict: d})
	if err := a.ODB.Session.NotifyChanges(ctx); err != nil {
		log.Error("cannot notify changes", "error", err)
	}
	return c.JSON(http.StatusOK, server.QuotaChangeResponse{Info: pyFormat(format, d), Id: quotaID})
}
