package serverhandlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/labstack/echo/v4"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/server"
	"github.com/opensvc/oc3/util/echolog"
	"github.com/opensvc/oc3/util/logkey"
)

// GetMetrics handles GET /metrics
func (a *Api) GetMetrics(c echo.Context, params server.GetMetricsParams) error {
	return a.handleList(c, "GetMetrics", "metric", listEndpointParams{
		props: params.Props, limit: params.Limit, offset: params.Offset,
		meta: params.Meta, stats: params.Stats, orderby: params.Orderby, groupby: params.Groupby,
		filter: params.Filter,
	}, func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
		return a.ODB.GetMetrics(ctx, p)
	})
}

// GetMetric handles GET /metrics/{metric_id}
func (a *Api) GetMetric(c echo.Context, metricId string, params server.GetMetricParams) error {
	return a.handleItem(c, "GetMetric", "metric", "id", metricId, listEndpointParams{props: params.Props},
		func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
			return a.ODB.GetMetric(ctx, metricId, p)
		})
}

// requireMetricsManager allows the changes to the metrics to the managers only, as
// the historical collector's API did (check_privilege("Manager")).
func requireMetricsManager(c echo.Context) error {
	if !IsAuthByUser(c) {
		return denyRequest(c, http.StatusUnauthorized, "user authentication required")
	}
	if !IsManager(c) {
		return denyRequest(c, http.StatusForbidden, "Manager privilege required")
	}
	return nil
}

// metricBody is the body of the metric create and update requests. It is decoded
// here rather than by the generated types: a null metric_col_instance_index clears
// the column, which an absent one does not.
type metricBody struct {
	MetricName    *string `json:"metric_name"`
	MetricSQL     *string `json:"metric_sql"`
	ValueIndex    *int    `json:"metric_col_value_index"`
	InstanceIndex *int    `json:"metric_col_instance_index"`
	InstanceLabel *string `json:"metric_col_instance_label"`
	Historize     *string `json:"metric_historize"`
}

// readMetricFields decodes and checks the body of a metric request.
func readMetricFields(c echo.Context) (cdb.MetricFields, error) {
	var f cdb.MetricFields
	raw, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return f, fmt.Errorf("cannot read the request body: %w", err)
	}
	var body metricBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return f, fmt.Errorf("invalid request body: %w", err)
	}
	var present map[string]json.RawMessage
	_ = json.Unmarshal(raw, &present)

	if body.MetricName != nil {
		name := strings.TrimSpace(*body.MetricName)
		if name == "" {
			return f, fmt.Errorf("metric_name cannot be empty")
		}
		if utf8.RuneCountInString(name) > 100 {
			return f, fmt.Errorf("metric_name is longer than 100 characters")
		}
		f.Name = &name
	}
	f.SQL = body.MetricSQL
	if body.ValueIndex != nil && *body.ValueIndex < 0 {
		return f, fmt.Errorf("metric_col_value_index cannot be negative")
	}
	f.ValueIndex = body.ValueIndex
	if body.InstanceIndex != nil && *body.InstanceIndex < 0 {
		return f, fmt.Errorf("metric_col_instance_index cannot be negative")
	}
	f.InstanceIndex = body.InstanceIndex
	if value, ok := present["metric_col_instance_index"]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		f.ClearInstanceIndex = true
	}
	if body.InstanceLabel != nil && utf8.RuneCountInString(*body.InstanceLabel) > 100 {
		return f, fmt.Errorf("metric_col_instance_label is longer than 100 characters")
	}
	f.InstanceLabel = body.InstanceLabel
	if body.Historize != nil && *body.Historize != "T" && *body.Historize != "F" {
		return f, fmt.Errorf("metric_historize must be one of T, F")
	}
	f.Historize = body.Historize
	return f, nil
}

// PostMetrics handles POST /metrics: create a metric, published to the primary
// team of its author, as the historical collector did.
func (a *Api) PostMetrics(c echo.Context) error {
	log := echolog.GetLogHandler(c, "PostMetrics")
	odb := a.ODB
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()

	if err := requireMetricsManager(c); err != nil {
		return err
	}
	fields, err := readMetricFields(c)
	if err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	if fields.Name == nil {
		return JSONProblemf(c, http.StatusBadRequest, "the metric_name property is mandatory")
	}
	log.Info("called", "metric_name", *fields.Name)

	if otherID, taken, err := odb.MetricByName(ctx, *fields.Name); err != nil {
		log.Error("cannot check metric name", logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot check metric name")
	} else if taken {
		return JSONProblemf(c, http.StatusConflict, "a metric named %s already exists: %d", *fields.Name, otherID)
	}

	userEmail, _ := c.Get(XUserEmail).(string)
	id, err := odb.InsertMetric(ctx, fields, userEmail)
	if err != nil {
		log.Error("cannot create metric", logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot create metric")
	}

	// Published to the author's team, so that someone not a Manager would see it.
	if userID := authUserID(c); userID != nil {
		groupID, found, err := odb.UserPrimaryGroupID(ctx, *userID)
		if err != nil {
			log.Error("cannot read the primary group", logkey.Error, err)
		} else if found {
			if err := odb.AddMetricPublication(ctx, id, groupID); err != nil {
				log.Error("cannot publish the metric", logkey.Error, err)
			}
		}
	}

	sqlText := ""
	if fields.SQL != nil {
		sqlText = *fields.SQL
	}
	a.reportCommit(ctx, c, log, reportKindMetric, id, sqlText)

	if logErr := odb.Log(ctx, cdb.LogEntry{
		Action: "metric.add",
		User:   userEmail,
		Fmt:    "Metric %(metric_name)s added",
		Dict:   map[string]any{"metric_name": *fields.Name},
		Level:  "info",
	}); logErr != nil {
		log.Error("cannot write audit log", logkey.Error, logErr)
	}
	if err := odb.Session.NotifyChanges(ctx); err != nil {
		log.Error("cannot notify changes", logkey.Error, err)
	}

	return a.handleItem(c, "PostMetrics", "metric", "id", strconv.Itoa(id), listEndpointParams{},
		func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
			return odb.GetMetric(ctx, strconv.Itoa(id), p)
		})
}

// PostMetric handles POST /metrics/{metric_id}: change the properties given.
func (a *Api) PostMetric(c echo.Context, metricId string) error {
	log := echolog.GetLogHandler(c, "PostMetric")
	odb := a.ODB
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()

	if err := requireMetricsManager(c); err != nil {
		return err
	}
	id, err := strconv.Atoi(metricId)
	if err != nil {
		return JSONProblemf(c, http.StatusNotFound, "metric %s not found", metricId)
	}
	row, err := odb.MetricRowByID(ctx, id)
	if err != nil {
		log.Error("cannot get metric", "metric_id", id, logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot get metric")
	}
	if row == nil {
		return JSONProblemf(c, http.StatusNotFound, "metric %s not found", metricId)
	}
	fields, err := readMetricFields(c)
	if err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	log.Info("called", "metric_id", id)

	if fields.Name != nil && *fields.Name != row.Name {
		if otherID, taken, err := odb.MetricByName(ctx, *fields.Name); err != nil {
			log.Error("cannot check metric name", logkey.Error, err)
			return JSONProblemf(c, http.StatusInternalServerError, "cannot check metric name")
		} else if taken && otherID != id {
			return JSONProblemf(c, http.StatusConflict, "a metric named %s already exists: %d", *fields.Name, otherID)
		}
	}

	changes := metricChanges(row, fields)
	if len(changes) > 0 {
		if err := odb.UpdateMetric(ctx, id, fields); err != nil {
			log.Error("cannot update metric", "metric_id", id, logkey.Error, err)
			return JSONProblemf(c, http.StatusInternalServerError, "cannot update metric")
		}
		if fields.SQL != nil && *fields.SQL != row.SQL {
			a.reportCommit(ctx, c, log, reportKindMetric, id, *fields.SQL)
		}
		userEmail, _ := c.Get(XUserEmail).(string)
		if logErr := odb.Log(ctx, cdb.LogEntry{
			Action: "metric.change",
			User:   userEmail,
			Fmt:    "Metric %(metric_name)s change: %(data)s",
			Dict:   map[string]any{"metric_name": row.Name, "data": strings.Join(changes, ", ")},
			Level:  "info",
		}); logErr != nil {
			log.Error("cannot write audit log", logkey.Error, logErr)
		}
		if err := odb.Session.NotifyChanges(ctx); err != nil {
			log.Error("cannot notify changes", logkey.Error, err)
		}
	}

	return a.handleItem(c, "PostMetric", "metric", "id", strconv.Itoa(id), listEndpointParams{},
		func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
			return odb.GetMetric(ctx, strconv.Itoa(id), p)
		})
}

// metricChanges describes what the fields change, for the log; nothing when the
// request repeats the current values.
func metricChanges(row *cdb.MetricRow, f cdb.MetricFields) []string {
	var changes []string
	text := func(name, before string, after *string) {
		if after != nil && *after != before {
			changes = append(changes, fmt.Sprintf("%s: %s => %s", name, before, *after))
		}
	}
	index := func(v sql.NullInt64) string {
		if !v.Valid {
			return ""
		}
		return strconv.FormatInt(v.Int64, 10)
	}
	text("metric_name", row.Name, f.Name)
	if f.SQL != nil && *f.SQL != row.SQL {
		// The request may be long: the log says it changed, the metric holds it.
		changes = append(changes, "metric_sql changed")
	}
	if f.ValueIndex != nil && index(row.ValueIndex) != strconv.Itoa(*f.ValueIndex) {
		changes = append(changes, fmt.Sprintf("metric_col_value_index: %s => %d", index(row.ValueIndex), *f.ValueIndex))
	}
	if f.ClearInstanceIndex && row.InstanceIndex.Valid {
		changes = append(changes, fmt.Sprintf("metric_col_instance_index: %s => ", index(row.InstanceIndex)))
	} else if f.InstanceIndex != nil && index(row.InstanceIndex) != strconv.Itoa(*f.InstanceIndex) {
		changes = append(changes, fmt.Sprintf("metric_col_instance_index: %s => %d", index(row.InstanceIndex), *f.InstanceIndex))
	}
	text("metric_col_instance_label", row.InstanceLabel, f.InstanceLabel)
	text("metric_historize", row.Historize, f.Historize)
	return changes
}
