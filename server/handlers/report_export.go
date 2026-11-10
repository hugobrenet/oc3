package serverhandlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"gopkg.in/yaml.v3"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/util/echolog"
)

// The export of a report holds its definition, and those of the charts and the
// metrics it shows, the ids replaced by the names, so that another collector
// imports them: the format of the historical GET /reports/<id>/export and POST
// /reports/import.

type exportedReport struct {
	Name       string         `json:"report_name"`
	Definition map[string]any `json:"report_definition"`
}

type exportedChart struct {
	Name       string         `json:"chart_name"`
	Definition map[string]any `json:"chart_definition"`
}

type reportExport struct {
	Reports []exportedReport     `json:"reports"`
	Charts  []exportedChart      `json:"charts"`
	Metrics []cdb.ExportedMetric `json:"metrics"`
}

// refID reads an id of a definition, written as a number or a string.
func refID(v any) (int, bool) {
	switch id := v.(type) {
	case int:
		return id, true
	case int64:
		return int(id), true
	case float64:
		return int(id), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(id))
		return n, err == nil
	}
	return 0, false
}

// parseDefinition reads a YAML definition as a map: empty when there is none.
func parseDefinition(text string) (map[string]any, error) {
	definition := map[string]any{}
	if strings.TrimSpace(text) == "" {
		return definition, nil
	}
	var parsed any
	if err := yaml.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, err
	}
	if m, ok := parsed.(map[string]any); ok {
		return m, nil
	}
	return definition, nil
}

func asMaps(v any) []map[string]any {
	list, _ := v.([]any)
	var maps []map[string]any
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			maps = append(maps, m)
		}
	}
	return maps
}

func toAny(maps []map[string]any) []any {
	list := make([]any, len(maps))
	for i, m := range maps {
		list[i] = m
	}
	return list
}

// sectionChildren gathers the entries of a section, its Charts, its Metrics and
// its children, in the single children list of the exported format.
func sectionChildren(section map[string]any) []map[string]any {
	var children []map[string]any
	for _, key := range []string{"Charts", "Metrics", "children"} {
		children = append(children, asMaps(section[key])...)
		delete(section, key)
	}
	return children
}

func sortedIDs(set map[int]bool) []int {
	ids := make([]int, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// exportReport builds the export of a report.
func (a *Api) exportReport(ctx context.Context, row *cdb.ReportObjectRow) (*reportExport, error) {
	definition, err := parseDefinition(row.Definition)
	if err != nil {
		return nil, fmt.Errorf("the report definition is not valid YAML: %w", err)
	}
	sections := asMaps(definition["Sections"])
	chartIDs := map[int]bool{}
	metricIDs := map[int]bool{}
	for _, section := range sections {
		children := sectionChildren(section)
		for _, child := range children {
			if id, ok := refID(child["chart_id"]); ok {
				chartIDs[id] = true
			}
			if id, ok := refID(child["metric_id"]); ok {
				metricIDs[id] = true
			}
		}
		section["children"] = toAny(children)
	}
	if sections != nil {
		definition["Sections"] = toAny(sections)
	}

	// The charts, and the metrics they draw.
	charts := []exportedChart{}
	chartNames := map[int]string{}
	chartDefinitions := map[int]map[string]any{}
	for _, id := range sortedIDs(chartIDs) {
		chart, err := a.ODB.ReportObjectRowByID(ctx, cdb.ReportObjectChart, id)
		if err != nil {
			return nil, err
		}
		if chart == nil {
			continue
		}
		chartDefinition, err := parseDefinition(chart.Definition)
		if err != nil {
			return nil, fmt.Errorf("the definition of the chart %s is not valid YAML: %w", chart.Name, err)
		}
		for _, m := range asMaps(chartDefinition["Metrics"]) {
			if id, ok := refID(m["metric_id"]); ok {
				metricIDs[id] = true
			}
		}
		chartNames[id] = chart.Name
		chartDefinitions[id] = chartDefinition
	}
	metrics, err := a.ODB.ExportedMetrics(ctx, sortedIDs(metricIDs))
	if err != nil {
		return nil, err
	}

	// The ids replaced by the names, the entries of a missing object left out.
	named := func(entries []map[string]any) []map[string]any {
		var kept []map[string]any
		for _, e := range entries {
			if v, set := e["metric_id"]; set {
				id, _ := refID(v)
				m, found := metrics[id]
				if !found {
					continue
				}
				delete(e, "metric_id")
				e["metric_name"] = m.Name
			} else if v, set := e["chart_id"]; set {
				id, _ := refID(v)
				name, found := chartNames[id]
				if !found {
					continue
				}
				delete(e, "chart_id")
				e["chart_name"] = name
			}
			kept = append(kept, e)
		}
		return kept
	}
	for _, id := range sortedIDs(chartIDs) {
		chartDefinition, found := chartDefinitions[id]
		if !found {
			continue
		}
		if _, set := chartDefinition["Metrics"]; set {
			chartDefinition["Metrics"] = toAny(named(asMaps(chartDefinition["Metrics"])))
		}
		charts = append(charts, exportedChart{Name: chartNames[id], Definition: chartDefinition})
	}
	for _, section := range sections {
		section["children"] = toAny(named(asMaps(section["children"])))
	}
	exportedMetrics := []cdb.ExportedMetric{}
	for _, id := range sortedIDs(metricIDs) {
		if m, found := metrics[id]; found {
			exportedMetrics = append(exportedMetrics, m)
		}
	}
	return &reportExport{
		Reports: []exportedReport{{Name: row.Name, Definition: definition}},
		Charts:  charts,
		Metrics: exportedMetrics,
	}, nil
}

// GetReportExport handles GET /reports/{report_id}/export.
func (a *Api) GetReportExport(c echo.Context, reportId string) error {
	log := echolog.GetLogHandler(c, "GetReportExport")
	ctx := c.Request().Context()
	id, err := a.reportObjectID(ctx, log, reportKindReport, reportId)
	if err != nil {
		return httpProblem(c, err)
	}
	if err := a.requireReportObjectVisible(ctx, c, log, reportKindReport, id); err != nil {
		return httpProblem(c, err)
	}
	row, err := a.ODB.ReportObjectRowByID(ctx, cdb.ReportObjectReport, id)
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot read the report", err))
	}
	if row == nil {
		return JSONProblemf(c, http.StatusNotFound, "Report %d not found", id)
	}
	export, err := a.exportReport(ctx, row)
	if err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, export)
}

// reportImport is the body of POST /reports/import: the export format, the
// definitions as objects.
type reportImport struct {
	Reports []map[string]any `json:"reports"`
	Charts  []map[string]any `json:"charts"`
	Metrics []map[string]any `json:"metrics"`
}

type importResult struct {
	Info  []string `json:"info"`
	Error []string `json:"error"`
}

func (r *importResult) infof(format string, args ...any) {
	r.Info = append(r.Info, fmt.Sprintf(format, args...))
}

func (r *importResult) errorf(format string, args ...any) {
	r.Error = append(r.Error, fmt.Sprintf(format, args...))
}

func mapString(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func mapInt(m map[string]any, key string) *int {
	if id, ok := refID(m[key]); ok {
		return &id
	}
	return nil
}

func mapDefinition(m map[string]any, key string) map[string]any {
	if d, ok := m[key].(map[string]any); ok {
		return d
	}
	return map[string]any{}
}

// importTeams publishes a new object to the primary team of the caller, also
// made responsible for it, as a creation does.
func (a *Api) importTeams(ctx context.Context, c echo.Context, log *slog.Logger, k reportKind, id int) {
	userID := authUserID(c)
	if userID == nil {
		return
	}
	groupID, found, err := a.ODB.UserPrimaryGroupID(ctx, *userID)
	if err != nil {
		log.Error("cannot read the primary group", "error", err)
		return
	}
	if !found {
		return
	}
	for _, responsibles := range []bool{false, true} {
		table := k.obj.TeamTable(responsibles)
		if table == "" {
			continue
		}
		if err := a.ODB.InsertReportObjectTeam(ctx, k.obj, table, id, groupID); err != nil {
			log.Error("cannot attach the team", "kind", k.obj.Kind, "id", id, "error", err)
		}
	}
}

// PostReportsImport handles POST /reports/import: create the metrics, the charts
// and the reports of an export, those of an existing name kept and used as they
// are. The historical collector let a ReportsManager create the metrics too; the
// metrics being SQL requests, creating one requires the Manager privilege, as
// POST /metrics does.
func (a *Api) PostReportsImport(c echo.Context) error {
	log := echolog.GetLogHandler(c, "PostReportsImport")
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout*5)
	defer cancel()
	if err := reportKindReport.requirePrivilege(c); err != nil {
		return httpProblem(c, err)
	}
	raw, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return JSONProblemf(c, http.StatusBadRequest, "cannot read the request body: %s", err)
	}
	var body reportImport
	if err := json.Unmarshal(raw, &body); err != nil {
		return JSONProblemf(c, http.StatusBadRequest, "invalid request body: %s", err)
	}
	result := importResult{Info: []string{}, Error: []string{}}
	userEmail, _ := c.Get(XUserEmail).(string)
	changed := false

	metricIDs := map[string]int{}
	for i, m := range body.Metrics {
		name := mapString(m, "metric_name")
		if name == "" {
			result.errorf("Missing 'metric_name' key in metric %d", i)
			continue
		}
		if id, found, err := a.ODB.ReportObjectByName(ctx, cdb.ReportObjectMetric, name); err != nil {
			return httpProblem(c, httpInternal(log, "cannot check the metric name", err))
		} else if found {
			metricIDs[name] = id
			result.infof("Skip metric %s: already exists", name)
			continue
		}
		if !IsManager(c) {
			result.errorf("Skip metric %s: creating a metric requires the Manager privilege", name)
			continue
		}
		fields := cdb.MetricFields{Name: &name, ValueIndex: mapInt(m, "metric_col_value_index"), InstanceIndex: mapInt(m, "metric_col_instance_index")}
		sqlText := mapString(m, "metric_sql")
		fields.SQL = &sqlText
		if label := mapString(m, "metric_col_instance_label"); label != "" {
			fields.InstanceLabel = &label
		}
		if h := mapString(m, "metric_historize"); h == "T" || h == "F" {
			fields.Historize = &h
		}
		id, err := a.ODB.InsertMetric(ctx, fields, userEmail)
		if err != nil {
			return httpProblem(c, httpInternal(log, "cannot create the metric "+name, err))
		}
		metricIDs[name] = id
		a.importTeams(ctx, c, log, reportKindMetric, id)
		a.reportCommit(ctx, c, log, reportKindMetric, id, sqlText)
		a.reportLog(ctx, c, log, "metric.add", "Metric %(metric_name)s added", map[string]any{"metric_name": name})
		result.infof("Added metric %s", name)
		changed = true
	}

	// resolve replaces the metric_name and chart_name of the entries by the ids
	// of the objects, and leaves out those it cannot resolve.
	chartIDs := map[string]int{}
	resolve := func(entries []map[string]any, where string, charts bool) []map[string]any {
		var kept []map[string]any
		for j, e := range entries {
			if name := mapString(e, "metric_name"); name != "" {
				id, found := metricIDs[name]
				if !found {
					if existing, ok, err := a.ODB.ReportObjectByName(ctx, cdb.ReportObjectMetric, name); err == nil && ok {
						id, found = existing, true
					}
				}
				if !found {
					result.errorf("Id of metric %s not found, in %s", name, where)
					continue
				}
				delete(e, "metric_name")
				e["metric_id"] = id
			} else if name := mapString(e, "chart_name"); charts && name != "" {
				id, found := chartIDs[name]
				if !found {
					if existing, ok, err := a.ODB.ReportObjectByName(ctx, cdb.ReportObjectChart, name); err == nil && ok {
						id, found = existing, true
					}
				}
				if !found {
					result.errorf("Id of chart %s not found, in %s", name, where)
					continue
				}
				delete(e, "chart_name")
				e["chart_id"] = id
			} else {
				result.errorf("Missing 'metric_name' or 'chart_name' key in entry %d of %s", j, where)
				continue
			}
			kept = append(kept, e)
		}
		return kept
	}

	for i, m := range body.Charts {
		name := mapString(m, "chart_name")
		if name == "" {
			result.errorf("Missing 'chart_name' key in chart %d", i)
			continue
		}
		if id, found, err := a.ODB.ReportObjectByName(ctx, cdb.ReportObjectChart, name); err != nil {
			return httpProblem(c, httpInternal(log, "cannot check the chart name", err))
		} else if found {
			chartIDs[name] = id
			result.infof("Skip chart %s: already exists", name)
			continue
		}
		definition := mapDefinition(m, "chart_definition")
		if _, set := definition["Metrics"]; set {
			definition["Metrics"] = toAny(resolve(asMaps(definition["Metrics"]), "chart "+name, false))
		}
		text, err := yaml.Marshal(definition)
		if err != nil {
			result.errorf("Error converting the chart %s to yaml: %s", name, err)
			continue
		}
		id, err := a.ODB.InsertChart(ctx, name, string(text))
		if err != nil {
			return httpProblem(c, httpInternal(log, "cannot create the chart "+name, err))
		}
		chartIDs[name] = id
		a.importTeams(ctx, c, log, reportKindChart, id)
		a.reportCommit(ctx, c, log, reportKindChart, id, string(text))
		a.reportLog(ctx, c, log, "chart.add", "Chart %(chart_name)s added", map[string]any{"chart_name": name})
		result.infof("Added chart %s", name)
		changed = true
	}

	for i, m := range body.Reports {
		name := mapString(m, "report_name")
		if name == "" {
			result.errorf("Missing 'report_name' key in report %d", i)
			continue
		}
		if _, found, err := a.ODB.ReportObjectByName(ctx, cdb.ReportObjectReport, name); err != nil {
			return httpProblem(c, httpInternal(log, "cannot check the report name", err))
		} else if found {
			result.infof("Skip report %s: already exists", name)
			continue
		}
		definition := mapDefinition(m, "report_definition")
		sections := asMaps(definition["Sections"])
		for j, section := range sections {
			section["children"] = toAny(resolve(sectionChildren(section), fmt.Sprintf("section %d of report %s", j, name), true))
		}
		if sections != nil {
			definition["Sections"] = toAny(sections)
		}
		text, err := yaml.Marshal(definition)
		if err != nil {
			result.errorf("Error converting the report %s to yaml: %s", name, err)
			continue
		}
		id, err := a.ODB.InsertReport(ctx, name, string(text))
		if err != nil {
			return httpProblem(c, httpInternal(log, "cannot create the report "+name, err))
		}
		a.importTeams(ctx, c, log, reportKindReport, id)
		a.reportCommit(ctx, c, log, reportKindReport, id, string(text))
		a.reportLog(ctx, c, log, "report.add", "Report %(report_name)s added", map[string]any{"report_name": name})
		result.infof("Added report %s", name)
		changed = true
	}
	if changed {
		a.reportNotify(ctx, log)
	}
	return c.JSON(http.StatusOK, result)
}
