package serverhandlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/labstack/echo/v4"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/server"
	"github.com/opensvc/oc3/util/echolog"
)

// The administration of the storage arrays, as the historical /arrays API: an
// array, its comment, its disk groups, its target ports, the nodes inventorying
// it (its proxies), and the quotas of its disk groups. The changes require the
// StorageManager privilege.

// arrayID resolves an array given by its name or its id: a 404 when none is.
func (a *Api) arrayID(ctx context.Context, log *slog.Logger, ident string) (int64, error) {
	id, ok, err := a.ODB.ArrayIDByIdent(ctx, ident)
	if err != nil {
		return 0, httpInternal(log, "cannot resolve the array", err)
	}
	if !ok {
		return 0, httpErrorf(http.StatusNotFound, "array %s not found", ident)
	}
	return id, nil
}

func (a *Api) arrayNotify(ctx context.Context, log *slog.Logger) {
	if err := a.ODB.Session.NotifyChanges(ctx); err != nil {
		log.Error("cannot notify changes", "error", err)
	}
}

// GetArray handles GET /arrays/{array_id}.
func (a *Api) GetArray(c echo.Context, arrayId string, params server.GetArrayParams) error {
	log := echolog.GetLogHandler(c, "GetArray")
	id, err := a.arrayID(c.Request().Context(), log, arrayId)
	if err != nil {
		return httpProblem(c, err)
	}
	return a.handleItem(c, "GetArray", "array", "id", arrayId, listEndpointParams{props: params.Props},
		func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
			return a.ODB.GetArray(ctx, id, p)
		})
}

// PostArray handles POST /arrays/{array_id}: change the comment of an array,
// the only property the agents do not feed, as the historical handler was used
// for.
func (a *Api) PostArray(c echo.Context, arrayId string) error {
	log := echolog.GetLogHandler(c, "PostArray")
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	if err := requireStorageManager(c); err != nil {
		return err
	}
	var body server.PostArrayJSONRequestBody
	if err := c.Bind(&body); err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	if body.ArrayComment == nil {
		return JSONProblemf(c, http.StatusBadRequest, "nothing to change: array_comment is the property to set")
	}
	if utf8.RuneCountInString(*body.ArrayComment) > 255 {
		return JSONProblemf(c, http.StatusBadRequest, "array_comment is longer than 255 characters")
	}
	id, err := a.arrayID(ctx, log, arrayId)
	if err != nil {
		return httpProblem(c, err)
	}
	row, err := a.ODB.ArrayRecordByID(ctx, id)
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot read the array", err))
	}
	if row == nil {
		return JSONProblemf(c, http.StatusNotFound, "array %s not found", arrayId)
	}
	if *body.ArrayComment != row.Comment {
		if err := a.ODB.UpdateArrayComment(ctx, id, *body.ArrayComment); err != nil {
			return httpProblem(c, httpInternal(log, "cannot change the array", err))
		}
		a.auditLog(c, ctx, log, cdb.LogEntry{
			Action: "array.change",
			Fmt:    "array %(name)s changed: %(data)s",
			Dict: map[string]any{"name": row.Name,
				"data": fmt.Sprintf("array_comment: %s => %s", row.Comment, *body.ArrayComment)},
		})
		a.arrayNotify(ctx, log)
	}
	return a.handleItem(c, "PostArray", "array", "id", strconv.FormatInt(id, 10), listEndpointParams{},
		func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
			return a.ODB.GetArray(ctx, id, p)
		})
}

// GetArraysDiskgroups handles GET /arrays_diskgroups.
func (a *Api) GetArraysDiskgroups(c echo.Context, params server.GetArraysDiskgroupsParams) error {
	return a.handleList(c, "GetArraysDiskgroups", "array_dg", listEndpointParams{
		props: params.Props, limit: params.Limit, offset: params.Offset,
		meta: params.Meta, stats: params.Stats, orderby: params.Orderby, groupby: params.Groupby,
		filter: params.Filter,
	}, func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
		return a.ODB.GetArraysDiskgroups(ctx, p)
	})
}

// GetArrayDiskgroup handles GET /arrays/{array_id}/diskgroups/{dg_id}: the disk
// group of an id or a name, in the array, or in any array for the array 0.
func (a *Api) GetArrayDiskgroup(c echo.Context, arrayId string, dgId string, params server.GetArrayDiskgroupParams) error {
	log := echolog.GetLogHandler(c, "GetArrayDiskgroup")
	var id int64
	if arrayId != "0" {
		var err error
		if id, err = a.arrayID(c.Request().Context(), log, arrayId); err != nil {
			return httpProblem(c, err)
		}
	}
	return a.handleItem(c, "GetArrayDiskgroup", "array_dg", "id", dgId, listEndpointParams{props: params.Props},
		func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
			return a.ODB.GetArrayDiskgroup(ctx, id, dgId, p)
		})
}

// GetArrayDiskgroupQuota handles GET /arrays/{array_id}/diskgroups/{dg_id}/quotas/{quota_id}.
func (a *Api) GetArrayDiskgroupQuota(c echo.Context, arrayId string, dgId int64, quotaId int64, params server.GetArrayDiskgroupQuotaParams) error {
	log := echolog.GetLogHandler(c, "GetArrayDiskgroupQuota")
	dg, err := a.quotaDiskGroup(c.Request().Context(), log, arrayId, dgId)
	if err != nil {
		return httpProblem(c, err)
	}
	return a.handleItem(c, "GetArrayDiskgroupQuota", "disk_quota", "id", strconv.FormatInt(quotaId, 10),
		listEndpointParams{props: params.Props},
		func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
			return a.ODB.GetQuota(ctx, p, dg.ID, quotaId)
		})
}

// DeleteArrayDiskgroupQuotas handles DELETE /arrays/{array_id}/diskgroups/{dg_id}/quotas:
// the quotas given by the id key of the body, one entry or a list.
func (a *Api) DeleteArrayDiskgroupQuotas(c echo.Context, arrayId string, dgId int64) error {
	log := echolog.GetLogHandler(c, "DeleteArrayDiskgroupQuotas")
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	if err := requireStorageManager(c); err != nil {
		return err
	}
	entries, isList, err := decodeEntries(c)
	if err != nil {
		return httpProblem(c, err)
	}
	defer a.arrayNotify(ctx, log)
	return runEntries(c, entries, isList, func(entry map[string]any) (map[string]any, error) {
		s, ok := entryString(entry, "id")
		if !ok {
			return nil, httpErrorf(http.StatusBadRequest, "The 'id' key is mandatory")
		}
		quotaID, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, httpErrorf(http.StatusBadRequest, "invalid quota id %q", s)
		}
		result, err := a.deleteQuota(c, ctx, log, arrayId, dgId, quotaID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"info": result.Info, "id": result.Id}, nil
	})
}

// GetArrayProxies handles GET /arrays/{array_id}/proxies.
func (a *Api) GetArrayProxies(c echo.Context, arrayId string, params server.GetArrayProxiesParams) error {
	log := echolog.GetLogHandler(c, "GetArrayProxies")
	id, err := a.arrayID(c.Request().Context(), log, arrayId)
	if err != nil {
		return httpProblem(c, err)
	}
	return a.handleList(c, "GetArrayProxies", "array_proxy", listEndpointParams{
		props: params.Props, limit: params.Limit, offset: params.Offset,
		meta: params.Meta, stats: params.Stats, orderby: params.Orderby, groupby: params.Groupby,
		filter: params.Filter,
	}, func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
		return a.ODB.GetArrayProxies(ctx, id, p)
	})
}

// changeArrayProxy sets or unsets a node, by its id or its name, as a proxy of
// an array. An unset accepts the id of a node deleted since. A proxy already
// set, or already unset, answers with an info message.
func (a *Api) changeArrayProxy(c echo.Context, ctx context.Context, log *slog.Logger, arrayIdent, nodeIdent string, add bool) (map[string]any, error) {
	arrayID, err := a.arrayID(ctx, log, arrayIdent)
	if err != nil {
		return nil, err
	}
	node, err := a.ODB.NodeByNodeIDOrNodename(ctx, nodeIdent)
	if err != nil {
		return nil, httpInternal(log, "cannot resolve the node", err)
	}
	nodeID := nodeIdent
	if node != nil {
		nodeID = node.NodeID
	} else if add {
		return nil, httpErrorf(http.StatusNotFound, "node %s not found", nodeIdent)
	}
	d := map[string]any{"node_id": nodeID, "array_id": strconv.FormatInt(arrayID, 10)}
	exists, err := a.ODB.ArrayProxyExists(ctx, arrayID, nodeID)
	if err != nil {
		return nil, httpInternal(log, "cannot check the proxy", err)
	}
	if add {
		if exists {
			return map[string]any{"info": pyFormat("node %(node_id)s is already proxy for array %(array_id)s", d)}, nil
		}
		if err := a.ODB.InsertArrayProxy(ctx, arrayID, nodeID); err != nil {
			return nil, httpInternal(log, "cannot set the proxy", err)
		}
		format := "node %(node_id)s set as proxy for array %(array_id)s"
		a.auditLog(c, ctx, log, cdb.LogEntry{Action: "storage.array.proxy.add", Fmt: format, Dict: d})
		return map[string]any{"info": pyFormat(format, d)}, nil
	}
	if !exists {
		return map[string]any{"info": pyFormat("node %(node_id)s is not proxy for array %(array_id)s", d)}, nil
	}
	if err := a.ODB.DeleteArrayProxy(ctx, arrayID, nodeID); err != nil {
		return nil, httpInternal(log, "cannot unset the proxy", err)
	}
	format := "node %(node_id)s unset as proxy for array %(array_id)s"
	a.auditLog(c, ctx, log, cdb.LogEntry{Action: "storage.array.proxy.del", Fmt: format, Dict: d})
	return map[string]any{"info": pyFormat(format, d)}, nil
}

func (a *Api) arrayProxyOne(c echo.Context, handlerName, arrayId, nodeId string, add bool) error {
	log := echolog.GetLogHandler(c, handlerName)
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	if err := requireStorageManager(c); err != nil {
		return err
	}
	result, err := a.changeArrayProxy(c, ctx, log, arrayId, nodeId, add)
	if err != nil {
		return httpProblem(c, err)
	}
	a.arrayNotify(ctx, log)
	return c.JSON(http.StatusOK, result)
}

// arrayProxyBulk takes the nodes from the node_id key of the body, one entry
// or a list, as the historical /arrays/<id>/proxies handlers do.
func (a *Api) arrayProxyBulk(c echo.Context, handlerName, arrayId string, add bool) error {
	log := echolog.GetLogHandler(c, handlerName)
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	if err := requireStorageManager(c); err != nil {
		return err
	}
	entries, isList, err := decodeEntries(c)
	if err != nil {
		return httpProblem(c, err)
	}
	defer a.arrayNotify(ctx, log)
	return runEntries(c, entries, isList, func(entry map[string]any) (map[string]any, error) {
		nodeID, ok := entryString(entry, "node_id")
		if !ok {
			return nil, httpErrorf(http.StatusBadRequest, "The 'node_id' key is mandatory")
		}
		return a.changeArrayProxy(c, ctx, log, arrayId, nodeID, add)
	})
}

// PostArrayProxy handles POST /arrays/{array_id}/proxies/{node_id}.
func (a *Api) PostArrayProxy(c echo.Context, arrayId string, nodeId string) error {
	return a.arrayProxyOne(c, "PostArrayProxy", arrayId, nodeId, true)
}

// DeleteArrayProxy handles DELETE /arrays/{array_id}/proxies/{node_id}.
func (a *Api) DeleteArrayProxy(c echo.Context, arrayId string, nodeId string) error {
	return a.arrayProxyOne(c, "DeleteArrayProxy", arrayId, nodeId, false)
}

// PostArrayProxies handles POST /arrays/{array_id}/proxies.
func (a *Api) PostArrayProxies(c echo.Context, arrayId string) error {
	return a.arrayProxyBulk(c, "PostArrayProxies", arrayId, true)
}

// DeleteArrayProxies handles DELETE /arrays/{array_id}/proxies.
func (a *Api) DeleteArrayProxies(c echo.Context, arrayId string) error {
	return a.arrayProxyBulk(c, "DeleteArrayProxies", arrayId, false)
}

// GetArrayTargets handles GET /arrays/{array_id}/targets.
func (a *Api) GetArrayTargets(c echo.Context, arrayId string, params server.GetArrayTargetsParams) error {
	log := echolog.GetLogHandler(c, "GetArrayTargets")
	id, err := a.arrayID(c.Request().Context(), log, arrayId)
	if err != nil {
		return httpProblem(c, err)
	}
	return a.handleList(c, "GetArrayTargets", "array_tgtid", listEndpointParams{
		props: params.Props, limit: params.Limit, offset: params.Offset,
		meta: params.Meta, stats: params.Stats, orderby: params.Orderby, groupby: params.Groupby,
		filter: params.Filter,
	}, func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
		return a.ODB.GetArrayTargets(ctx, id, p)
	})
}
