package cdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ReportObject describes one of the objects of the reports: the reports, the
// charts and the metrics. They share their layout: a table holding the name and
// the definition, the publications to groups and, but for the metrics, the
// groups responsible for them. The names are constants, never user input.
type ReportObject struct {
	Kind      string // report, chart, metric: the log actions and messages
	Table     string
	NameCol   string
	DefCol    string // the definition, recorded in the git history
	LinkCol   string // the object id column of the publication and responsible tables
	PubTable  string
	RespTable string // empty: no responsibles, the managers only change the object
}

var (
	ReportObjectReport = ReportObject{
		Kind: "report", Table: "reports", NameCol: "report_name", DefCol: "report_yaml",
		LinkCol: "report_id", PubTable: "report_team_publication", RespTable: "report_team_responsible",
	}
	ReportObjectChart = ReportObject{
		Kind: "chart", Table: "charts", NameCol: "chart_name", DefCol: "chart_yaml",
		LinkCol: "chart_id", PubTable: "chart_team_publication", RespTable: "chart_team_responsible",
	}
	ReportObjectMetric = ReportObject{
		Kind: "metric", Table: "metrics", NameCol: "metric_name", DefCol: "metric_sql",
		LinkCol: "metric_id", PubTable: "metric_team_publication",
	}
)

// ReportObjectTeamTable names the table of an object/group link: the
// publications or the responsibles of the object.
func (o ReportObject) TeamTable(responsibles bool) string {
	if responsibles {
		return o.RespTable
	}
	return o.PubTable
}

// ReportObjectRow is the name and the definition of a report, chart or metric.
type ReportObjectRow struct {
	ID         int
	Name       string
	Definition string
}

// teamRolesCond restricts the rows of a team table aliased t to the groups of these
// roles.
func teamRolesCond(groups []string) (string, []any) {
	cleanGroups := cleanGroups(groups)
	if len(cleanGroups) == 0 {
		return "", nil
	}
	args := make([]any, len(cleanGroups))
	for i, g := range cleanGroups {
		args[i] = g
	}
	return "t.group_id IN (SELECT ag.id FROM auth_group ag WHERE ag.role IN (" + Placeholders(len(cleanGroups)) + "))", args
}

// ReportObjectRowByID returns the object, nil when it does not exist.
func (oDb *DB) ReportObjectRowByID(ctx context.Context, o ReportObject, id int) (*ReportObjectRow, error) {
	var (
		row        = ReportObjectRow{ID: id}
		definition sql.NullString
	)
	err := oDb.DB.QueryRowContext(ctx,
		"SELECT "+o.NameCol+", "+o.DefCol+" FROM "+o.Table+" WHERE id = ?", id).Scan(&row.Name, &definition)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reportObjectRowByID %s: %w", o.Table, err)
	}
	row.Definition = definition.String
	return &row, nil
}

// ReportObjectByName returns the id of the object of this name.
func (oDb *DB) ReportObjectByName(ctx context.Context, o ReportObject, name string) (int, bool, error) {
	var id int
	err := oDb.DB.QueryRowContext(ctx, "SELECT id FROM "+o.Table+" WHERE "+o.NameCol+" = ? LIMIT 1", name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("reportObjectByName %s: %w", o.Table, err)
	}
	return id, true, nil
}

// ReportObjectVisible tells whether the object exists and is published to one
// of the groups, or exists for a manager.
func (oDb *DB) ReportObjectVisible(ctx context.Context, o ReportObject, id int, groups []string, isManager bool) (bool, error) {
	query := "SELECT 1 FROM " + o.Table + " WHERE id = ?"
	args := []any{id}
	if !isManager {
		cond, condArgs := teamRolesCond(groups)
		if cond == "" {
			return false, nil
		}
		query += " AND id IN (SELECT t." + o.LinkCol + " FROM " + o.PubTable + " t WHERE " + cond + ")"
		args = append(args, condArgs...)
	}
	var one int
	err := oDb.DB.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reportObjectVisible %s: %w", o.Table, err)
	}
	return true, nil
}

// ReportObjectResponsible tells whether the object exists and one of the groups
// is responsible for it, or it exists for a manager. An object without
// responsibles is the managers' only.
func (oDb *DB) ReportObjectResponsible(ctx context.Context, o ReportObject, id int, groups []string, isManager bool) (bool, error) {
	if !isManager && o.RespTable == "" {
		return false, nil
	}
	query := "SELECT 1 FROM " + o.Table + " WHERE id = ?"
	args := []any{id}
	if !isManager {
		cond, condArgs := teamRolesCond(groups)
		if cond == "" {
			return false, nil
		}
		query += " AND id IN (SELECT t." + o.LinkCol + " FROM " + o.RespTable + " t WHERE " + cond + ")"
		args = append(args, condArgs...)
	}
	var one int
	err := oDb.DB.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reportObjectResponsible %s: %w", o.Table, err)
	}
	return true, nil
}

// UpdateReportObject changes the name and the definition given.
func (oDb *DB) UpdateReportObject(ctx context.Context, o ReportObject, id int, name, definition *string) error {
	var (
		sets []string
		args []any
	)
	if name != nil {
		sets = append(sets, o.NameCol+" = ?")
		args = append(args, *name)
	}
	if definition != nil {
		sets = append(sets, o.DefCol+" = ?")
		args = append(args, *definition)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	if _, err := oDb.ExecContext(ctx, "UPDATE "+o.Table+" SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
		return fmt.Errorf("updateReportObject %s: %w", o.Table, err)
	}
	oDb.SetChange(o.Table)
	return nil
}

// DeleteReportObject deletes the object with its publications and responsibles.
func (oDb *DB) DeleteReportObject(ctx context.Context, o ReportObject, id int) error {
	for _, table := range []string{o.PubTable, o.RespTable} {
		if table == "" {
			continue
		}
		if _, err := oDb.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+o.LinkCol+" = ?", id); err != nil {
			return fmt.Errorf("deleteReportObject %s: %w", table, err)
		}
		oDb.SetChange(table)
	}
	if _, err := oDb.ExecContext(ctx, "DELETE FROM "+o.Table+" WHERE id = ?", id); err != nil {
		return fmt.Errorf("deleteReportObject %s: %w", o.Table, err)
	}
	oDb.SetChange(o.Table)
	return nil
}

// ReportObjectTeamExists reports whether the object is linked to the group in
// the team table.
func (oDb *DB) ReportObjectTeamExists(ctx context.Context, o ReportObject, table string, id int, groupID int64) (bool, error) {
	var one int
	err := oDb.DB.QueryRowContext(ctx, "SELECT 1 FROM "+table+" WHERE "+o.LinkCol+" = ? AND group_id = ? LIMIT 1",
		id, groupID).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("reportObjectTeamExists %s: %w", table, err)
	}
	return true, nil
}

// InsertReportObjectTeam links the object to the group in the team table.
func (oDb *DB) InsertReportObjectTeam(ctx context.Context, o ReportObject, table string, id int, groupID int64) error {
	if _, err := oDb.ExecContext(ctx, "INSERT INTO "+table+" ("+o.LinkCol+", group_id) VALUES (?, ?)", id, groupID); err != nil {
		return fmt.Errorf("insertReportObjectTeam %s: %w", table, err)
	}
	oDb.SetChange(table)
	return nil
}

// DeleteReportObjectTeam unlinks the object from the group in the team table.
func (oDb *DB) DeleteReportObjectTeam(ctx context.Context, o ReportObject, table string, id int, groupID int64) error {
	if _, err := oDb.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+o.LinkCol+" = ? AND group_id = ?", id, groupID); err != nil {
		return fmt.Errorf("deleteReportObjectTeam %s: %w", table, err)
	}
	oDb.SetChange(table)
	return nil
}

// GetReportObjectTeam lists the groups linked to the object in the team table,
// as auth_group rows.
func (oDb *DB) GetReportObjectTeam(ctx context.Context, o ReportObject, table string, id int, p ListParams) ([]map[string]any, error) {
	if len(p.SelectExprs) == 0 {
		return nil, fmt.Errorf("getReportObjectTeam: no select expressions")
	}
	conds := []string{"t." + o.LinkCol + " = ?"}
	args := []any{id}
	filterConds, filterArgs := p.FilterConditions()
	conds = append(conds, filterConds...)
	args = append(args, filterArgs...)
	query := "SELECT " + strings.Join(p.SelectExprs, ", ") + " FROM auth_group JOIN " + table +
		" t ON t.group_id = auth_group.id WHERE " + strings.Join(conds, " AND ")
	if gb := p.GroupByClause(""); gb != "" {
		query += " " + gb
	}
	query += " " + p.OrderByClause("auth_group.role")
	query, args = appendLimitOffset(query, args, p.Limit, p.Offset)
	rows, err := oDb.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getReportObjectTeam %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	return scanRowsToMaps(rows, p.Props, p.TypeHints)
}

// ReportObjectNames returns the names of the objects of these ids, by id.
func (oDb *DB) ReportObjectNames(ctx context.Context, o ReportObject, ids []int) (map[int]string, error) {
	names := make(map[int]string)
	if len(ids) == 0 {
		return names, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := oDb.DB.QueryContext(ctx,
		"SELECT id, "+o.NameCol+" FROM "+o.Table+" WHERE id IN ("+Placeholders(len(ids))+")", args...)
	if err != nil {
		return nil, fmt.Errorf("reportObjectNames %s: %w", o.Table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			id   int
			name string
		)
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("reportObjectNames %s: %w", o.Table, err)
		}
		names[id] = name
	}
	return names, rows.Err()
}

// ExportedMetric is a metric as GET /reports/{report_id}/export writes it.
type ExportedMetric struct {
	Name          string `json:"metric_name"`
	SQL           string `json:"metric_sql"`
	ValueIndex    *int   `json:"metric_col_value_index"`
	InstanceIndex *int   `json:"metric_col_instance_index"`
	InstanceLabel string `json:"metric_col_instance_label"`
	Historize     string `json:"metric_historize"`
}

// ExportedMetrics returns the metrics of these ids, by id.
func (oDb *DB) ExportedMetrics(ctx context.Context, ids []int) (map[int]ExportedMetric, error) {
	metrics := make(map[int]ExportedMetric)
	if len(ids) == 0 {
		return metrics, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := oDb.DB.QueryContext(ctx,
		"SELECT id, metric_name, COALESCE(metric_sql, ''), metric_col_value_index, metric_col_instance_index,"+
			" COALESCE(metric_col_instance_label, ''), COALESCE(metric_historize, 'F')"+
			" FROM metrics WHERE id IN ("+Placeholders(len(ids))+")", args...)
	if err != nil {
		return nil, fmt.Errorf("exportedMetrics: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			id            int
			m             ExportedMetric
			valueIndex    sql.NullInt64
			instanceIndex sql.NullInt64
		)
		if err := rows.Scan(&id, &m.Name, &m.SQL, &valueIndex, &instanceIndex, &m.InstanceLabel, &m.Historize); err != nil {
			return nil, fmt.Errorf("exportedMetrics: %w", err)
		}
		if valueIndex.Valid {
			v := int(valueIndex.Int64)
			m.ValueIndex = &v
		}
		if instanceIndex.Valid {
			v := int(instanceIndex.Int64)
			m.InstanceIndex = &v
		}
		metrics[id] = m
	}
	return metrics, rows.Err()
}
