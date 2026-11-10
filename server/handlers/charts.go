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

// GetCharts handles GET /charts
func (a *Api) GetCharts(c echo.Context, params server.GetChartsParams) error {
	return a.handleList(c, "GetCharts", "chart", listEndpointParams{
		props: params.Props, limit: params.Limit, offset: params.Offset,
		meta: params.Meta, stats: params.Stats, orderby: params.Orderby, groupby: params.Groupby,
		filter: params.Filter,
	}, func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
		return a.ODB.GetCharts(ctx, p)
	})
}

// GetChart handles GET /charts/{chart_id}
func (a *Api) GetChart(c echo.Context, chartId string, params server.GetChartParams) error {
	return a.handleItem(c, "GetChart", "chart", "id", chartId, listEndpointParams{props: params.Props},
		func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
			return a.ODB.GetChart(ctx, chartId, p)
		})
}

// checkChart checks the name and the definition of a new chart, and returns the
// name trimmed.
func checkChart(name string, definition *string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("the chart_name property is mandatory")
	}
	if utf8.RuneCountInString(name) > 100 {
		return "", fmt.Errorf("chart_name is longer than 100 characters")
	}
	if definition != nil && strings.TrimSpace(*definition) != "" {
		var parsed any
		if err := yaml.Unmarshal([]byte(*definition), &parsed); err != nil {
			return "", fmt.Errorf("chart_yaml is not valid YAML: %w", err)
		}
	}
	return name, nil
}

// PostCharts handles POST /charts: create a chart, published to the primary
// team of its author, which is also made responsible for it, as the historical
// collector did.
func (a *Api) PostCharts(c echo.Context) error {
	log := echolog.GetLogHandler(c, "PostCharts")
	odb := a.ODB
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()

	if !IsAuthByUser(c) {
		return denyRequest(c, http.StatusUnauthorized, "user authentication required")
	}
	if !IsReportsManager(c) {
		return denyRequest(c, http.StatusForbidden, "ReportsManager privilege required")
	}
	var body server.PostChartsJSONRequestBody
	if err := c.Bind(&body); err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	name, err := checkChart(body.ChartName, body.ChartYaml)
	if err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	log.Info("called", "chart_name", name)

	if otherID, taken, err := odb.ChartByName(ctx, name); err != nil {
		log.Error("cannot check chart name", logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot check chart name")
	} else if taken {
		return JSONProblemf(c, http.StatusConflict, "a chart named %s already exists: %d", name, otherID)
	}

	definition := ""
	if body.ChartYaml != nil {
		definition = *body.ChartYaml
	}
	id, err := odb.InsertChart(ctx, name, definition)
	if err != nil {
		log.Error("cannot create chart", logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot create chart")
	}

	if userID := authUserID(c); userID != nil {
		groupID, found, err := odb.UserPrimaryGroupID(ctx, *userID)
		if err != nil {
			log.Error("cannot read the primary group", logkey.Error, err)
		} else if found {
			if err := odb.AddChartTeams(ctx, id, groupID); err != nil {
				log.Error("cannot attach the chart to its team", logkey.Error, err)
			}
		}
	}

	a.reportCommit(ctx, c, log, reportKindChart, id, definition)

	userEmail, _ := c.Get(XUserEmail).(string)
	if logErr := odb.Log(ctx, cdb.LogEntry{
		Action: "chart.add",
		User:   userEmail,
		Fmt:    "Chart %(chart_name)s added",
		Dict:   map[string]any{"chart_name": name},
		Level:  "info",
	}); logErr != nil {
		log.Error("cannot write audit log", logkey.Error, logErr)
	}
	if err := odb.Session.NotifyChanges(ctx); err != nil {
		log.Error("cannot notify changes", logkey.Error, err)
	}

	return a.handleItem(c, "PostCharts", "chart", "id", strconv.Itoa(id), listEndpointParams{},
		func(ctx context.Context, p cdb.ListParams) ([]map[string]any, error) {
			return odb.GetChart(ctx, strconv.Itoa(id), p)
		})
}
