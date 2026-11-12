package cdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type (
	// AISchedule runs a skill of the AI assistant at its time, on the days of
	// the week it lists, in its time zone, and sends the answer to the
	// notification channels it names.
	AISchedule struct {
		ID           int64
		Name         string
		Skill        string
		Prompt       string
		Days         []string
		AtTime       string
		Timezone     string
		ChannelIDs   []int64
		SendFailures bool
		Enabled      bool
		// NextRun is when it runs next; nil when disabled.
		NextRun   *time.Time
		Created   time.Time
		Updated   time.Time
		UpdatedBy string
	}

	// AIScheduleRun is one run of a schedule, at its time or asked at once.
	AIScheduleRun struct {
		ID             int64
		ScheduleID     int64
		Trigger        string
		State          string
		Queued         time.Time
		Started        *time.Time
		Ended          *time.Time
		ToolCalls      int
		RefusedActions int
		Report         string
		Error          string
		QueuedBy       string
		// RequestID is the X-Request-ID the agent gave the run.
		RequestID string
		// Calls are the calls of the tools the run made, as the agent
		// summarized them.
		Calls []AIRunCall
	}

	// AIRunCall is a call of a tool a run made, by identifiers only.
	AIRunCall struct {
		Name       string `json:"name"`
		DurationMs int64  `json:"duration_ms"`
		Error      bool   `json:"error"`
		// NotRun tells why the call did not run: unoffered, such as a
		// write, or needs_confirmation; empty for a call that ran.
		NotRun string `json:"not_run,omitempty"`
	}
)

// The states of a run.
const (
	AIRunQueued  = "queued"
	AIRunRunning = "running"
	AIRunSent    = "sent"
	AIRunFailed  = "failed"
	AIRunUnsent  = "unsent"
)

// ErrAIScheduleNameTaken is the error of a schedule named as another one.
var ErrAIScheduleNameTaken = errors.New("a schedule already has this name")

// ErrAIRunPending is the error of a run asked while one of the same schedule
// is queued or running.
var ErrAIRunPending = errors.New("a run of the schedule is queued or running already")

const aiScheduleColumns = "id, name, skill, prompt, days, at_time, timezone, channel_ids, send_failures, enabled, next_run, created, updated, updated_by"

func scanAISchedule(scan func(...any) error) (AISchedule, error) {
	var (
		s        AISchedule
		days     string
		channels string
		nextRun  sql.NullTime
	)
	if err := scan(&s.ID, &s.Name, &s.Skill, &s.Prompt, &days, &s.AtTime, &s.Timezone, &channels, &s.SendFailures,
		&s.Enabled, &nextRun, &s.Created, &s.Updated, &s.UpdatedBy); err != nil {
		return s, err
	}
	if days != "" {
		s.Days = strings.Split(days, ",")
	}
	if err := json.Unmarshal([]byte(channels), &s.ChannelIDs); err != nil {
		return s, fmt.Errorf("schedule %d channel_ids: %w", s.ID, err)
	}
	if nextRun.Valid {
		t := nextRun.Time
		s.NextRun = &t
	}
	return s, nil
}

// AISchedules reads the schedules, by name; of one skill when skill is set.
func (oDb *DB) AISchedules(ctx context.Context, skill string) ([]AISchedule, error) {
	query, args := "SELECT "+aiScheduleColumns+" FROM ai_schedules", []any{}
	if skill != "" {
		query += " WHERE skill = ?"
		args = append(args, skill)
	}
	rows, err := oDb.DB.QueryContext(ctx, query+" ORDER BY name", args...)
	if err != nil {
		return nil, fmt.Errorf("aiSchedules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []AISchedule{}
	for rows.Next() {
		s, err := scanAISchedule(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("aiSchedules: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AISchedulesByChannel reads the schedules sending to each channel, by name.
func (oDb *DB) AISchedulesByChannel(ctx context.Context) (map[int64][]AISchedule, error) {
	all, err := oDb.AISchedules(ctx, "")
	if err != nil {
		return nil, err
	}
	out := map[int64][]AISchedule{}
	for _, s := range all {
		for _, id := range s.ChannelIDs {
			out[id] = append(out[id], s)
		}
	}
	return out, nil
}

// AIScheduleByID reads one schedule, nil when there is none.
func (oDb *DB) AIScheduleByID(ctx context.Context, id int64) (*AISchedule, error) {
	s, err := scanAISchedule(oDb.DB.QueryRowContext(ctx, "SELECT "+aiScheduleColumns+" FROM ai_schedules WHERE id = ?", id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("aiScheduleByID: %w", err)
	}
	return &s, nil
}

func aiScheduleArgs(s AISchedule) ([]any, error) {
	channels, err := json.Marshal(s.ChannelIDs)
	if err != nil {
		return nil, err
	}
	var nextRun any
	if s.NextRun != nil {
		nextRun = s.NextRun.UTC()
	}
	return []any{s.Name, s.Skill, s.Prompt, strings.Join(s.Days, ","), s.AtTime, s.Timezone, string(channels),
		s.SendFailures, s.Enabled, nextRun}, nil
}

// InsertAISchedule creates a schedule, its id given back.
func (oDb *DB) InsertAISchedule(ctx context.Context, s AISchedule) (int64, error) {
	args, err := aiScheduleArgs(s)
	if err != nil {
		return 0, fmt.Errorf("insertAISchedule: %w", err)
	}
	result, err := oDb.DB.ExecContext(ctx, "INSERT INTO ai_schedules (name, skill, prompt, days, at_time, timezone, channel_ids,"+
		" send_failures, enabled, next_run, created, updated, updated_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NOW(), NOW(), ?)",
		append(args, s.UpdatedBy)...)
	if isDuplicate(err) {
		return 0, ErrAIScheduleNameTaken
	}
	if err != nil {
		return 0, fmt.Errorf("insertAISchedule: %w", err)
	}
	oDb.SetChange("ai_schedules")
	return result.LastInsertId()
}

// UpdateAISchedule replaces the writable fields of a schedule; found false
// when there is none.
func (oDb *DB) UpdateAISchedule(ctx context.Context, s AISchedule) (bool, error) {
	args, err := aiScheduleArgs(s)
	if err != nil {
		return false, fmt.Errorf("updateAISchedule: %w", err)
	}
	result, err := oDb.DB.ExecContext(ctx, "UPDATE ai_schedules SET name = ?, skill = ?, prompt = ?, days = ?, at_time = ?,"+
		" timezone = ?, channel_ids = ?, send_failures = ?, enabled = ?, next_run = ?, updated = NOW(), updated_by = ? WHERE id = ?",
		append(args, s.UpdatedBy, s.ID)...)
	if isDuplicate(err) {
		return false, ErrAIScheduleNameTaken
	}
	if err != nil {
		return false, fmt.Errorf("updateAISchedule: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		found, err := oDb.AIScheduleByID(ctx, s.ID)
		return found != nil, err
	}
	oDb.SetChange("ai_schedules")
	return true, nil
}

// DeleteAISchedule deletes a schedule with its runs; found false when there is
// none.
func (oDb *DB) DeleteAISchedule(ctx context.Context, id int64) (bool, error) {
	result, err := oDb.DB.ExecContext(ctx, "DELETE FROM ai_schedules WHERE id = ?", id)
	if err != nil {
		return false, fmt.Errorf("deleteAISchedule: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return false, nil
	}
	if _, err := oDb.DB.ExecContext(ctx, "DELETE FROM ai_schedule_runs WHERE schedule_id = ?", id); err != nil {
		return true, fmt.Errorf("deleteAISchedule runs: %w", err)
	}
	oDb.SetChange("ai_schedules")
	return true, nil
}

// DueAISchedules reads the enabled schedules whose next run is due at now.
func (oDb *DB) DueAISchedules(ctx context.Context, now time.Time) ([]AISchedule, error) {
	rows, err := oDb.DB.QueryContext(ctx, "SELECT "+aiScheduleColumns+" FROM ai_schedules"+
		" WHERE enabled = 1 AND next_run IS NOT NULL AND next_run <= ? ORDER BY next_run", now.UTC())
	if err != nil {
		return nil, fmt.Errorf("dueAISchedules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []AISchedule
	for rows.Next() {
		s, err := scanAISchedule(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("dueAISchedules: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetAIScheduleNextRun records when a schedule runs next, if it still runs at
// previous: a schedule changed meanwhile keeps the next run its change set.
func (oDb *DB) SetAIScheduleNextRun(ctx context.Context, id int64, previous time.Time, next *time.Time) (bool, error) {
	var value any
	if next != nil {
		value = next.UTC()
	}
	result, err := oDb.DB.ExecContext(ctx, "UPDATE ai_schedules SET next_run = ? WHERE id = ? AND next_run = ?", value, id, previous.UTC())
	if err != nil {
		return false, fmt.Errorf("setAIScheduleNextRun: %w", err)
	}
	n, _ := result.RowsAffected()
	return n == 1, nil
}

// QueueAIScheduleRun queues a run of a schedule, its id given back; none,
// ErrAIRunPending, when a run of the schedule is queued or running already.
func (oDb *DB) QueueAIScheduleRun(ctx context.Context, scheduleID int64, trigger, by string) (int64, error) {
	result, err := oDb.DB.ExecContext(ctx, "INSERT INTO ai_schedule_runs (schedule_id, run_trigger, state, queued, report, queued_by)"+
		" SELECT ?, ?, 'queued', NOW(), '', ? FROM DUAL WHERE NOT EXISTS"+
		" (SELECT 1 FROM ai_schedule_runs WHERE schedule_id = ? AND state IN ('queued', 'running'))", scheduleID, trigger, by, scheduleID)
	if isDuplicate(err) {
		// Queued meanwhile by another request: the unique key of the
		// pending runs refused the second.
		return 0, ErrAIRunPending
	}
	if err != nil {
		return 0, fmt.Errorf("queueAIScheduleRun: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return 0, ErrAIRunPending
	}
	oDb.SetChange("ai_schedule_runs")
	return result.LastInsertId()
}

// AIScheduleRunByID reads one run, nil when there is none.
func (oDb *DB) AIScheduleRunByID(ctx context.Context, id int64) (*AIScheduleRun, error) {
	r, err := scanAIRun(oDb.DB.QueryRowContext(ctx, "SELECT "+aiRunColumns+" FROM ai_schedule_runs WHERE id = ?", id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("aiScheduleRunByID: %w", err)
	}
	return &r, nil
}

const aiRunColumns = "id, schedule_id, run_trigger, state, queued, started, ended, tool_calls, refused_actions, report, error, queued_by," +
	" request_id, COALESCE(calls, '')"

func scanAIRun(scan func(...any) error) (AIScheduleRun, error) {
	var (
		r              AIScheduleRun
		started, ended sql.NullTime
		calls          string
	)
	err := scan(&r.ID, &r.ScheduleID, &r.Trigger, &r.State, &r.Queued, &started, &ended, &r.ToolCalls, &r.RefusedActions,
		&r.Report, &r.Error, &r.QueuedBy, &r.RequestID, &calls)
	if err == nil && calls != "" {
		if jsonErr := json.Unmarshal([]byte(calls), &r.Calls); jsonErr != nil {
			err = fmt.Errorf("run %d calls: %w", r.ID, jsonErr)
		}
	}
	if started.Valid {
		t := started.Time
		r.Started = &t
	}
	if ended.Valid {
		t := ended.Time
		r.Ended = &t
	}
	return r, err
}

// AIScheduleRuns reads the latest runs of a schedule, the most recent first.
func (oDb *DB) AIScheduleRuns(ctx context.Context, scheduleID int64, limit int) ([]AIScheduleRun, error) {
	rows, err := oDb.DB.QueryContext(ctx, "SELECT "+aiRunColumns+" FROM ai_schedule_runs WHERE schedule_id = ? ORDER BY id DESC LIMIT ?",
		scheduleID, limit)
	if err != nil {
		return nil, fmt.Errorf("aiScheduleRuns: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []AIScheduleRun{}
	for rows.Next() {
		r, err := scanAIRun(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("aiScheduleRuns: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PreviousAIScheduleRun reads the last run of a schedule ended before the
// run before, nil when there is none.
func (oDb *DB) PreviousAIScheduleRun(ctx context.Context, scheduleID, before int64) (*AIScheduleRun, error) {
	r, err := scanAIRun(oDb.DB.QueryRowContext(ctx, "SELECT "+aiRunColumns+" FROM ai_schedule_runs"+
		" WHERE schedule_id = ? AND id < ? AND ended IS NOT NULL ORDER BY id DESC LIMIT 1", scheduleID, before).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("previousAIScheduleRun: %w", err)
	}
	return &r, nil
}

// TakeAIScheduleRun takes the oldest queued run, now running; nil when none
// waits. Taken by one scheduler only: the state changes only if it was queued.
func (oDb *DB) TakeAIScheduleRun(ctx context.Context) (*AIScheduleRun, error) {
	for {
		r, err := scanAIRun(oDb.DB.QueryRowContext(ctx, "SELECT "+aiRunColumns+" FROM ai_schedule_runs WHERE state = 'queued' ORDER BY id LIMIT 1").Scan)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("takeAIScheduleRun: %w", err)
		}
		result, err := oDb.DB.ExecContext(ctx, "UPDATE ai_schedule_runs SET state = 'running', started = NOW() WHERE id = ? AND state = 'queued'", r.ID)
		if err != nil {
			return nil, fmt.Errorf("takeAIScheduleRun: %w", err)
		}
		if n, _ := result.RowsAffected(); n == 1 {
			r.State = AIRunRunning
			return &r, nil
		}
		// Taken by another scheduler meanwhile: the next one.
	}
}

// EndAIScheduleRun records how a run ended.
func (oDb *DB) EndAIScheduleRun(ctx context.Context, r AIScheduleRun) error {
	if len(r.Error) > 1024 {
		r.Error = r.Error[:1024]
	}
	if len(r.RequestID) > 64 {
		r.RequestID = r.RequestID[:64]
	}
	var calls any
	if len(r.Calls) > 0 {
		data, err := json.Marshal(r.Calls)
		if err != nil {
			return fmt.Errorf("endAIScheduleRun: %w", err)
		}
		calls = string(data)
	}
	_, err := oDb.DB.ExecContext(ctx, "UPDATE ai_schedule_runs SET state = ?, ended = NOW(), tool_calls = ?, refused_actions = ?,"+
		" report = ?, error = ?, request_id = ?, calls = ? WHERE id = ?", r.State, r.ToolCalls, r.RefusedActions, r.Report, r.Error,
		r.RequestID, calls, r.ID)
	if err != nil {
		return fmt.Errorf("endAIScheduleRun: %w", err)
	}
	oDb.SetChange("ai_schedule_runs")
	return nil
}

// FailStaleAIScheduleRuns ends the runs left running longer than maxAge, by a
// scheduler stopped meanwhile.
func (oDb *DB) FailStaleAIScheduleRuns(ctx context.Context, maxAge time.Duration) (int64, error) {
	result, err := oDb.DB.ExecContext(ctx, "UPDATE ai_schedule_runs SET state = 'failed', ended = NOW(),"+
		" error = 'the scheduler stopped during the run' WHERE state = 'running' AND started < NOW() - INTERVAL ? SECOND",
		int64(maxAge/time.Second))
	if err != nil {
		return 0, fmt.Errorf("failStaleAIScheduleRuns: %w", err)
	}
	return result.RowsAffected()
}
