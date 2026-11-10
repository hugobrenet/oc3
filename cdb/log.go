package cdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type (
	LogEntry struct {
		ID     int64  `json:"id"`
		Action string `json:"log_action"`
		User   string `json:"log_user"`
		// Impersonator is the user who really signed in when the action is made as
		// User; empty otherwise. Log fills it from the context (WithImpersonator)
		// when it is not set.
		Impersonator string         `json:"log_impersonator"`
		Fmt          string         `json:"log_fmt"`
		Dict         map[string]any `json:"log_dict"`
		Date         time.Time      `json:"log_date"`
		SvcID        *uuid.UUID     `json:"svc_id"`
		IsGtalkSent  bool           `json:"log_gtalk_sent"`
		IsEmailSent  bool           `json:"log_email_sent"`
		EntryID      string         `json:"log_entry_id"`
		Level        string         `json:"log_level"`
		NodeID       *uuid.UUID     `json:"node_id"`
		// Origin and RequestID name the client the request came through, as it
		// declares it; Log fills them from the context (WithOrigin) when Origin
		// is not set.
		Origin    string `json:"log_origin"`
		RequestID string `json:"log_request_id"`
	}
)

type LogsFiltersetFilter struct {
	Active  bool
	NodeIDs []string
	SvcIDs  []string
}

// GetLog returns a single log row by id.
func (oDb *DB) GetLog(ctx context.Context, id string, p ListParams) ([]map[string]any, error) {
	if len(p.SelectExprs) == 0 {
		return nil, fmt.Errorf("getLog: no select expressions")
	}
	query := "SELECT " + strings.Join(p.SelectExprs, ", ") +
		" FROM log" + logJoins + " WHERE log.id = ?"
	args := []any{id}
	query, args = appendLimitOffset(query, args, p.Limit, p.Offset)
	rows, err := oDb.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getLog: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRowsToMaps(rows, p.Props, p.TypeHints)
}

// logJoins makes the "nodes." and "services." props selectable: a log event only
// carries the ids, and most events name no node nor service (empty ids), hence the
// LEFT JOINs.
const logJoins = " LEFT JOIN nodes ON nodes.node_id = log.node_id" +
	" LEFT JOIN services ON services.svc_id = log.svc_id"

// GetLogs returns rows from the collector log table.
func (oDb *DB) GetLogs(ctx context.Context, p ListParams, fset LogsFiltersetFilter) ([]map[string]any, error) {
	if len(p.SelectExprs) == 0 {
		return nil, fmt.Errorf("getLogs: no select expressions")
	}
	query := "SELECT " + strings.Join(p.SelectExprs, ", ") +
		" FROM log" + logJoins + " WHERE log.id > 0"
	args := []any{}
	if fset.Active {
		query, args = appendLogsFiltersetClause(query, args, fset.NodeIDs, fset.SvcIDs)
	}
	// Column filters of the request, the joined names included: the joins are
	// always there.
	conds, filterArgs := p.FilterConditions()
	for _, cond := range conds {
		query += " AND " + cond
	}
	args = append(args, filterArgs...)
	if gb := p.GroupByClause(""); gb != "" {
		query += " " + gb
	}
	query += " " + p.OrderByClause("log.id DESC")
	query, args = appendLimitOffset(query, args, p.Limit, p.Offset)
	rows, err := oDb.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getLogs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRowsToMaps(rows, p.Props, p.TypeHints)
}

func appendLogsFiltersetClause(query string, args []any, nodeIDs, svcIDs []string) (string, []any) {
	nN := len(nodeIDs)
	nS := len(svcIDs)
	switch {
	case nN > 0 && nS > 0:
		query += " AND (log.node_id = '' OR log.node_id IN (" + Placeholders(nN) + "))"
		for _, id := range nodeIDs {
			args = append(args, id)
		}
		query += " AND (log.svc_id = '' OR log.svc_id IN (" + Placeholders(nS) + "))"
		for _, id := range svcIDs {
			args = append(args, id)
		}
	case nN == 1:
		query += " AND log.node_id = ?"
		args = append(args, nodeIDs[0])
	case nN > 0:
		query += " AND log.node_id IN (" + Placeholders(nN) + ")"
		for _, id := range nodeIDs {
			args = append(args, id)
		}
	case nS == 1:
		query += " AND log.svc_id = ?"
		args = append(args, svcIDs[0])
	case nS > 0:
		query += " AND log.svc_id IN (" + Placeholders(nS) + ")"
		for _, id := range svcIDs {
			args = append(args, id)
		}
	default:
		query += " AND log.node_id IS NULL"
	}
	return query, args
}

type impersonatorKey struct{}

// WithImpersonator marks a context as the one of a request made as another user
// by the user named impersonator: the audit entries written with it say so.
func WithImpersonator(ctx context.Context, impersonator string) context.Context {
	return context.WithValue(ctx, impersonatorKey{}, impersonator)
}

// ImpersonatorFrom returns the user who really signed in, when the context is the
// one of a request made as another user; empty otherwise.
func ImpersonatorFrom(ctx context.Context) string {
	s, _ := ctx.Value(impersonatorKey{}).(string)
	return s
}

// The origins a client may declare (WithOrigin): the AI assistant of the
// collector, through its MCP and its agent, and the other clients of that MCP.
const (
	OriginAssistant = "assistant"
	OriginMCP       = "mcp"
)

// Origin is the client a request came through, as it declares it, and the
// request ID it gave, which ties an audit entry to the audit lines of that
// client.
type Origin struct {
	Name      string
	RequestID string
}

type originKey struct{}

// WithOrigin marks a context as the one of a request made through the client
// named by origin: the audit entries written with it say so.
func WithOrigin(ctx context.Context, origin Origin) context.Context {
	return context.WithValue(ctx, originKey{}, origin)
}

// OriginFrom returns the client a request came through, the zero Origin for a
// request that declares none.
func OriginFrom(ctx context.Context) Origin {
	o, _ := ctx.Value(originKey{}).(Origin)
	return o
}

// nullable gives NULL for an empty text, as the optional columns of the log
// hold it.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Log writes audit entries. An entry written for a request made as another user
// names both: log_user, the user the action is made as, and log_impersonator,
// the user who really signed in (LogEntry.Impersonator, or the context's). An
// entry written for a request made through a client that declares itself
// names it in log_origin and log_request_id (LogEntry.Origin and RequestID,
// or the context's).
func (oDb *DB) Log(ctx context.Context, entries ...LogEntry) error {
	toDict := func(d map[string]any) string {
		if d == nil {
			return "{}"
		}
		s, err := json.Marshal(d)
		if err != nil {
			return "{}"
		}
		return string(s)
	}
	cols := "(log_action, log_user, log_impersonator, log_origin, log_request_id, log_fmt, log_dict, log_level, svc_id, node_id, log_date)"
	fromCtx := ImpersonatorFrom(ctx)
	originCtx := OriginFrom(ctx)
	lines := make([]string, 0)
	args := make([]any, 0)

	for _, entry := range entries {
		impersonator := entry.Impersonator
		if impersonator == "" {
			impersonator = fromCtx
		}
		var impersonatorArg any
		if impersonator != "" {
			impersonatorArg = impersonator
		}
		origin := Origin{Name: entry.Origin, RequestID: entry.RequestID}
		if origin.Name == "" {
			origin = originCtx
		}
		args = append(args, entry.Action, entry.User, impersonatorArg, nullable(origin.Name), nullable(origin.RequestID),
			entry.Fmt, toDict(entry.Dict), entry.Level, entry.SvcID, entry.NodeID)
		lines = append(lines, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NOW())")

	}
	sql := fmt.Sprintf("INSERT INTO log %s VALUES %s", cols, strings.Join(lines, ","))
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, err := oDb.ExecContext(ctx, sql, args...)
	return err
}

type LogMessage struct {
	User string
	// Impersonator is the user who really signed in when User is impersonated;
	// InsertLogMessage reads it from the context when empty, as Log does.
	Impersonator string
	Fmt          string
	Dict         string // JSON-encoded log_dict
	Level        string
	SvcID        *string
	NodeID       *string
}

// InsertLogMessage inserts a single 'message' log event and returns its id.
func (oDb *DB) InsertLogMessage(ctx context.Context, m LogMessage) (int64, error) {
	const query = "INSERT INTO log (log_action, log_user, log_impersonator, log_origin, log_request_id, log_fmt, log_dict, log_level, svc_id, node_id, log_date)" +
		" VALUES ('message', ?, ?, ?, ?, ?, ?, ?, ?, ?, NOW())"
	var svcID, nodeID sql.NullString
	if m.SvcID != nil {
		svcID = sql.NullString{String: *m.SvcID, Valid: true}
	}
	if m.NodeID != nil {
		nodeID = sql.NullString{String: *m.NodeID, Valid: true}
	}
	dict := m.Dict
	if dict == "" {
		dict = "{}"
	}
	impersonator := m.Impersonator
	if impersonator == "" {
		impersonator = ImpersonatorFrom(ctx)
	}
	var impersonatorArg sql.NullString
	if impersonator != "" {
		impersonatorArg = sql.NullString{String: impersonator, Valid: true}
	}
	// The origin of the request, as Log writes it.
	origin := OriginFrom(ctx)
	res, err := oDb.ExecContext(ctx, query, m.User, impersonatorArg, nullable(origin.Name), nullable(origin.RequestID),
		m.Fmt, dict, m.Level, svcID, nodeID)
	if err != nil {
		return 0, fmt.Errorf("InsertLogMessage: %w", err)
	}
	oDb.SetChange("log")
	id, _ := res.LastInsertId()
	return id, nil
}
