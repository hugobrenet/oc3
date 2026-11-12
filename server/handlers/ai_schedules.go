package serverhandlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v4"

	"github.com/opensvc/oc3/aischedule"
	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/server"
	"github.com/opensvc/oc3/util/echolog"
)

// The schedules of the skills of the AI assistant. The scheduler of the
// collector runs them as its technical account, which reads the whole fleet:
// their reports tell what it reads, so only an AIManager reads and writes
// them, as the skills.

var aiScheduleName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

const (
	aiScheduleMaxPrompt   = 4000
	aiScheduleMaxChannels = 10
	aiScheduleRunsLimit   = 20
	aiScheduleRunsMax     = 100
)

// requireAIManager refuses anyone but an AIManager or a Manager.
func requireAIManager(c echo.Context) error {
	if !IsAuthByUser(c) {
		return httpErrorf(http.StatusUnauthorized, "user authentication required")
	}
	if !IsAIManager(c) {
		return httpErrorf(http.StatusForbidden, "AIManager privilege required")
	}
	return nil
}

func aiScheduleAPI(s cdb.AISchedule) server.AISchedule {
	days := make([]server.AIScheduleDay, len(s.Days))
	for i, d := range s.Days {
		days[i] = server.AIScheduleDay(d)
	}
	channels := s.ChannelIDs
	if channels == nil {
		channels = []int64{}
	}
	out := server.AISchedule{Id: s.ID, Name: s.Name, Skill: s.Skill, Prompt: s.Prompt, Days: days, At: s.AtTime,
		Timezone: s.Timezone, Channels: channels, SendFailures: s.SendFailures, Enabled: s.Enabled,
		Created: s.Created.UTC(), Updated: s.Updated.UTC(), UpdatedBy: s.UpdatedBy}
	if s.NextRun != nil {
		t := s.NextRun.UTC()
		out.NextRun = &t
	}
	return out
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func aiScheduleRunAPI(r cdb.AIScheduleRun) server.AIScheduleRun {
	out := server.AIScheduleRun{Id: r.ID, ScheduleId: r.ScheduleID, Trigger: server.AIScheduleRunTrigger(r.Trigger),
		State: server.AIScheduleRunState(r.State), Queued: r.Queued.UTC(), Started: utcPtr(r.Started), Ended: utcPtr(r.Ended),
		ToolCalls: r.ToolCalls, RefusedActions: r.RefusedActions, Report: r.Report, Error: r.Error, QueuedBy: r.QueuedBy}
	if r.RequestID != "" {
		out.RequestId = &r.RequestID
	}
	if len(r.Calls) > 0 {
		calls := make([]server.AIScheduleRunCall, len(r.Calls))
		for i, c := range r.Calls {
			calls[i] = server.AIScheduleRunCall{Name: c.Name, DurationMs: c.DurationMs, Error: c.Error}
			if c.NotRun != "" {
				notRun := server.AIScheduleRunCallNotRun(c.NotRun)
				calls[i].NotRun = &notRun
			}
		}
		out.Calls = &calls
	}
	return out
}

// checkAISchedule checks a schedule as given, against the skills and the
// channels of the collector, and returns it with its next run.
func (a *Api) checkAISchedule(ctx context.Context, in server.AIScheduleInput, now time.Time) (cdb.AISchedule, error) {
	s := cdb.AISchedule{Name: in.Name, Skill: in.Skill, AtTime: in.At, Timezone: in.Timezone, ChannelIDs: in.Channels,
		SendFailures: in.SendFailures == nil || *in.SendFailures, Enabled: in.Enabled == nil || *in.Enabled}
	if !aiScheduleName.MatchString(s.Name) {
		return s, httpErrorf(http.StatusBadRequest, "name: %q is not 1 to 64 lowercase letters, digits and dashes", s.Name)
	}
	if in.Prompt != nil {
		s.Prompt = *in.Prompt
	}
	if utf8.RuneCountInString(s.Prompt) > aiScheduleMaxPrompt {
		return s, httpErrorf(http.StatusBadRequest, "prompt: longer than %d characters", aiScheduleMaxPrompt)
	}
	days := make([]string, len(in.Days))
	for i, d := range in.Days {
		days[i] = string(d)
	}
	var err error
	if s.Days, err = aischedule.Validate(days, s.AtTime, s.Timezone); err != nil {
		return s, httpErrorf(http.StatusBadRequest, "%s", err)
	}
	if len(s.ChannelIDs) == 0 || len(s.ChannelIDs) > aiScheduleMaxChannels {
		return s, httpErrorf(http.StatusBadRequest, "channels: 1 to %d channels", aiScheduleMaxChannels)
	}
	slices.Sort(s.ChannelIDs)
	s.ChannelIDs = slices.Compact(s.ChannelIDs)
	for _, id := range s.ChannelIDs {
		if ch, err := a.ODB.NotifyChannelByID(ctx, id); err != nil {
			return s, err
		} else if ch == nil {
			return s, httpErrorf(http.StatusBadRequest, "channels: no channel %d", id)
		}
	}
	// The assistant of the collector runs the skill: one for the oc3 backend.
	skills, err := a.ODB.AIContents(ctx, cdb.AISkills, cdb.AIContentFilter{Name: s.Skill, Backend: "oc3", All: true})
	if err != nil {
		return s, err
	}
	if len(skills) == 0 {
		return s, httpErrorf(http.StatusBadRequest, "skill: no skill %q for the oc3 backend", s.Skill)
	}
	if s.Enabled {
		next, err := aischedule.NextRun(s.Days, s.AtTime, s.Timezone, now)
		if err != nil {
			return s, httpErrorf(http.StatusBadRequest, "%s", err)
		}
		s.NextRun = &next
	}
	return s, nil
}

// GetAISchedules handles GET /ai/schedules.
func (a *Api) GetAISchedules(c echo.Context, params server.GetAISchedulesParams) error {
	if err := requireAIManager(c); err != nil {
		return httpProblem(c, err)
	}
	skill := ""
	if params.Skill != nil {
		skill = *params.Skill
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	items, err := a.ODB.AISchedules(ctx, skill)
	if err != nil {
		return httpProblem(c, httpInternal(echolog.GetLogHandler(c, "GetAISchedules"), "cannot read the schedules", err))
	}
	out := server.AIScheduleListResponse{Data: make([]server.AISchedule, 0, len(items))}
	for _, s := range items {
		out.Data = append(out.Data, aiScheduleAPI(s))
	}
	return c.JSON(http.StatusOK, out)
}

// readAISchedule reads the schedule id for an AIManager, a 404 when there is
// none.
func (a *Api) readAISchedule(c echo.Context, operation string, id int64) (*cdb.AISchedule, error) {
	if err := requireAIManager(c); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	s, err := a.ODB.AIScheduleByID(ctx, id)
	if err != nil {
		return nil, httpInternal(echolog.GetLogHandler(c, operation), "cannot read the schedule", err)
	}
	if s == nil {
		return nil, httpErrorf(http.StatusNotFound, "no schedule %d", id)
	}
	return s, nil
}

// GetAISchedule handles GET /ai/schedules/{schedule_id}.
func (a *Api) GetAISchedule(c echo.Context, id server.InPathAIScheduleId) error {
	s, err := a.readAISchedule(c, "GetAISchedule", id)
	if err != nil {
		return httpProblem(c, err)
	}
	return c.JSON(http.StatusOK, aiScheduleAPI(*s))
}

// PostAISchedules handles POST /ai/schedules.
func (a *Api) PostAISchedules(c echo.Context) error {
	return a.saveAISchedule(c, "PostAISchedules", 0)
}

// PutAISchedule handles PUT /ai/schedules/{schedule_id}.
func (a *Api) PutAISchedule(c echo.Context, id server.InPathAIScheduleId) error {
	return a.saveAISchedule(c, "PutAISchedule", id)
}

// saveAISchedule creates a schedule, id 0, or replaces the schedule id.
func (a *Api) saveAISchedule(c echo.Context, operation string, id int64) error {
	log := echolog.GetLogHandler(c, operation)
	if err := requireAIManager(c); err != nil {
		return httpProblem(c, err)
	}
	var in server.AIScheduleInput
	if err := bindAI(c, &in); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	s, err := a.checkAISchedule(ctx, in, time.Now())
	if err != nil {
		var he *httpError
		if !errors.As(err, &he) {
			err = httpInternal(log, "cannot check the schedule", err)
		}
		return httpProblem(c, err)
	}
	s.ID = id
	s.UpdatedBy, _ = c.Get(XUserEmail).(string)
	if id != 0 && s.Enabled {
		// A change keeping the days, the time and the time zone of an enabled
		// schedule keeps its next run: computed again from now, a change in
		// the minute it is due, before the task queues it, would skip it.
		current, err := a.ODB.AIScheduleByID(ctx, id)
		if err != nil {
			return httpProblem(c, httpInternal(log, "cannot read the schedule", err))
		}
		if current != nil && current.Enabled && current.NextRun != nil && current.AtTime == s.AtTime &&
			current.Timezone == s.Timezone && slices.Equal(current.Days, s.Days) {
			s.NextRun = current.NextRun
		}
	}
	action, verb, status := "change", "changed", http.StatusOK
	if id == 0 {
		action, verb, status = "add", "added", http.StatusCreated
		s.ID, err = a.ODB.InsertAISchedule(ctx, s)
	} else {
		var found bool
		if found, err = a.ODB.UpdateAISchedule(ctx, s); err == nil && !found {
			return httpProblem(c, httpErrorf(http.StatusNotFound, "no schedule %d", id))
		}
	}
	if errors.Is(err, cdb.ErrAIScheduleNameTaken) {
		return httpProblem(c, httpErrorf(http.StatusConflict, "a schedule named %s already exists", s.Name))
	} else if err != nil {
		return httpProblem(c, httpInternal(log, "cannot write the schedule", err))
	}
	a.aiScheduleAudit(c, action, "AI schedule %(name)s of the skill %(skill)s "+verb, s)
	saved, err := a.ODB.AIScheduleByID(ctx, s.ID)
	if err != nil || saved == nil {
		return httpProblem(c, httpInternal(log, "cannot read the schedule", err))
	}
	return c.JSON(status, aiScheduleAPI(*saved))
}

// DeleteAISchedule handles DELETE /ai/schedules/{schedule_id}.
func (a *Api) DeleteAISchedule(c echo.Context, id server.InPathAIScheduleId) error {
	s, err := a.readAISchedule(c, "DeleteAISchedule", id)
	if err != nil {
		return httpProblem(c, err)
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	if _, err := a.ODB.DeleteAISchedule(ctx, id); err != nil {
		return httpProblem(c, httpInternal(echolog.GetLogHandler(c, "DeleteAISchedule"), "cannot delete the schedule", err))
	}
	a.aiScheduleAudit(c, "delete", "AI schedule %(name)s of the skill %(skill)s deleted", *s)
	return c.NoContent(http.StatusNoContent)
}

// GetAIScheduleRuns handles GET /ai/schedules/{schedule_id}/runs.
func (a *Api) GetAIScheduleRuns(c echo.Context, id server.InPathAIScheduleId, params server.GetAIScheduleRunsParams) error {
	if _, err := a.readAISchedule(c, "GetAIScheduleRuns", id); err != nil {
		return httpProblem(c, err)
	}
	limit := aiScheduleRunsLimit
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > aiScheduleRunsMax {
			return httpProblem(c, httpErrorf(http.StatusBadRequest, "limit: 1 to %d", aiScheduleRunsMax))
		}
		limit = *params.Limit
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	runs, err := a.ODB.AIScheduleRuns(ctx, id, limit)
	if err != nil {
		return httpProblem(c, httpInternal(echolog.GetLogHandler(c, "GetAIScheduleRuns"), "cannot read the runs", err))
	}
	out := server.AIScheduleRunListResponse{Data: make([]server.AIScheduleRun, 0, len(runs))}
	for _, r := range runs {
		out.Data = append(out.Data, aiScheduleRunAPI(r))
	}
	return c.JSON(http.StatusOK, out)
}

// PostAIScheduleRuns handles POST /ai/schedules/{schedule_id}/runs.
func (a *Api) PostAIScheduleRuns(c echo.Context, id server.InPathAIScheduleId) error {
	log := echolog.GetLogHandler(c, "PostAIScheduleRuns")
	s, err := a.readAISchedule(c, "PostAIScheduleRuns", id)
	if err != nil {
		return httpProblem(c, err)
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	by, _ := c.Get(XUserEmail).(string)
	runID, err := a.ODB.QueueAIScheduleRun(ctx, id, "manual", by)
	if errors.Is(err, cdb.ErrAIRunPending) {
		return httpProblem(c, httpErrorf(http.StatusConflict, "%s", err))
	} else if err != nil {
		return httpProblem(c, httpInternal(log, "cannot queue the run", err))
	}
	a.aiScheduleAudit(c, "run", "AI schedule %(name)s of the skill %(skill)s run on demand", *s)
	run, err := a.ODB.AIScheduleRunByID(ctx, runID)
	if err != nil || run == nil {
		return httpProblem(c, httpInternal(log, "cannot read the run", err))
	}
	return c.JSON(http.StatusAccepted, aiScheduleRunAPI(*run))
}

func (a *Api) aiScheduleAudit(c echo.Context, action, format string, s cdb.AISchedule) {
	userEmail, _ := c.Get(XUserEmail).(string)
	entry := cdb.LogEntry{Action: "ai.schedule." + action, User: userEmail, Fmt: format,
		Dict: map[string]any{"name": s.Name, "skill": s.Skill}, Level: "info"}
	if err := a.ODB.Log(c.Request().Context(), entry); err != nil {
		echolog.GetLogHandler(c, "aiScheduleAudit").Error("cannot write audit log", "error", fmt.Sprint(err))
	}
}
