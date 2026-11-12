package serverhandlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opensvc/oc3/migrate"
	"github.com/opensvc/oc3/server"
)

// TestAISchedulesRequireAIManager checks the schedules are refused to the
// users who are not AIManager: their reports tell what the technical account
// of the collector reads.
func TestAISchedulesRequireAIManager(t *testing.T) {
	a := &Api{}
	c, rec := aiContext(http.MethodGet, "/ai/schedules", "", "Everybody", "AlertsManager")
	if err := a.GetAISchedules(c, server.GetAISchedulesParams{}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AIManager") {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
	c, rec = aiContext(http.MethodPost, "/ai/schedules/1/runs", "", "Everybody")
	if err := a.PostAIScheduleRuns(c, 1); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("run: got %d %s", rec.Code, rec.Body.String())
	}
}

// TestAISchedules writes a schedule of a skill as an AIManager, on a MariaDB:
// it is checked against the skills and the channels, gets its next run, runs
// on demand once at a time, and goes with its runs.
func TestAISchedules(t *testing.T) {
	a, pool := aiTestAPI(t)
	all, err := migrate.All()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.Name == "notification_channels" || m.Name == "ai_schedules" || m.Name == "ai_schedule_runs" || m.Name == "ai_schedule_runs_unattended" {
			for _, s := range m.Statements {
				if _, err := pool.Exec(s); err != nil {
					t.Fatalf("%s: %v", s, err)
				}
			}
		}
	}
	for _, s := range []string{
		"INSERT INTO notification_channels (name, type, description, settings_json, created, updated) VALUES ('ops-mail', 'email', '', '{}', NOW(), NOW())",
		"INSERT INTO ai_skills (name, backends, body, frontmatter, size, digest, updated, updated_by) VALUES" +
			" ('morning-check', 'oc3', '---\nname: morning-check\n---\n', '{}', 30, 'sha256:x', NOW(), 'x')," +
			" ('om3-only', 'om3', '---\nname: om3-only\n---\n', '{}', 25, 'sha256:y', NOW(), 'x')",
	} {
		if _, err := pool.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	post := func(body string) *http.Response {
		c, rec := aiContext(http.MethodPost, "/ai/schedules", body, "AIManager")
		if err := a.PostAISchedules(c); err != nil {
			t.Fatal(err)
		}
		return rec.Result()
	}
	valid := `{"name":"morning","skill":"morning-check","days":["fri","mon"],"at":"08:00","timezone":"Europe/Paris","channels":[1]}`
	for name, body := range map[string]string{
		"skill of om3":    strings.Replace(valid, "morning-check", "om3-only", 1),
		"no skill":        strings.Replace(valid, "morning-check", "none", 1),
		"no channel":      strings.Replace(valid, "[1]", "[]", 1),
		"unknown channel": strings.Replace(valid, "[1]", "[9]", 1),
		"bad time":        strings.Replace(valid, "08:00", "8h", 1),
		"bad zone":        strings.Replace(valid, "Europe/Paris", "Paris", 1),
		"bad name":        strings.Replace(valid, `"morning"`, `"Morning check"`, 1),
	} {
		if r := post(body); r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", name, r.StatusCode)
		}
	}
	r := post(valid)
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", r.StatusCode)
	}
	var s server.AISchedule
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	if s.NextRun == nil || !s.NextRun.After(time.Now()) || len(s.Days) != 2 || s.Days[0] != "mon" || !s.Enabled || !s.SendFailures {
		t.Fatalf("schedule %+v", s)
	}
	if r := post(valid); r.StatusCode != http.StatusConflict {
		t.Errorf("same name: %d", r.StatusCode)
	}

	// A change keeping the cadence keeps the next run, due in a minute here:
	// computed again, it would move to the next day.
	if _, err := pool.Exec("UPDATE ai_schedules SET next_run = '2030-01-07 07:00:00' WHERE id = ?", s.Id); err != nil {
		t.Fatal(err)
	}
	c, rec := aiContext(http.MethodPut, "/ai/schedules/1", strings.Replace(valid, "}", `,"prompt":"in English"}`, 1), "AIManager")
	if err := a.PutAISchedule(c, s.Id); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	var kept server.AISchedule
	if err := json.Unmarshal(rec.Body.Bytes(), &kept); err != nil || kept.NextRun == nil || kept.NextRun.Year() != 2030 || kept.Prompt != "in English" {
		t.Fatalf("next run not kept: %s", rec.Body.String())
	}
	c, rec = aiContext(http.MethodPut, "/ai/schedules/1", strings.Replace(valid, "08:00", "09:00", 1), "AIManager")
	if err := a.PutAISchedule(c, s.Id); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &kept); err != nil || kept.NextRun == nil || kept.NextRun.Year() == 2030 {
		t.Fatalf("next run kept across a change of time: %s", rec.Body.String())
	}

	// Disabled: no next run.
	c, rec = aiContext(http.MethodPut, "/ai/schedules/1", strings.Replace(valid, "}", `,"enabled":false}`, 1), "AIManager")
	if err := a.PutAISchedule(c, s.Id); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "next_run") {
		t.Errorf("a disabled schedule has a next run: %s", rec.Body.String())
	}

	// One run on demand at a time.
	run := func() int {
		c, rec := aiContext(http.MethodPost, "/ai/schedules/1/runs", "", "AIManager")
		if err := a.PostAIScheduleRuns(c, s.Id); err != nil {
			t.Fatal(err)
		}
		return rec.Code
	}
	if code := run(); code != http.StatusAccepted {
		t.Fatalf("run: %d", code)
	}
	if code := run(); code != http.StatusConflict {
		t.Errorf("second run: %d", code)
	}
	c, rec = aiContext(http.MethodGet, "/ai/schedules/1/runs", "", "AIManager")
	if err := a.GetAIScheduleRuns(c, s.Id, server.GetAIScheduleRunsParams{}); err != nil {
		t.Fatal(err)
	}
	var runs server.AIScheduleRunListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil || len(runs.Data) != 1 || runs.Data[0].State != "queued" ||
		runs.Data[0].Trigger != "manual" || runs.Data[0].QueuedBy != "user@example.com" {
		t.Fatalf("runs %s, %v", rec.Body.String(), err)
	}

	// The channel names the schedule sending to it, and is not deleted.
	c, rec = aiContext(http.MethodGet, "/notifications/channels", "", "AlertsManager")
	if err := a.GetNotificationChannels(c); err != nil {
		t.Fatal(err)
	}
	var channels struct {
		Data []server.NotificationChannel `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &channels); err != nil || len(channels.Data) != 1 ||
		len(channels.Data[0].AiSchedules) != 1 || channels.Data[0].AiSchedules[0].Name != "morning" ||
		channels.Data[0].AiSchedules[0].Skill != "morning-check" || channels.Data[0].AiSchedules[0].Enabled {
		t.Fatalf("channels %s, %v", rec.Body.String(), err)
	}
	c, rec = aiContext(http.MethodDelete, "/notifications/channels/1", "", "AlertsManager")
	if err := a.DeleteNotificationChannel(c, 1); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "morning") {
		t.Fatalf("delete of a channel in use: %d %s", rec.Code, rec.Body.String())
	}

	c, rec = aiContext(http.MethodDelete, "/ai/schedules/1", "", "AIManager")
	if err := a.DeleteAISchedule(c, s.Id); err != nil || rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	c, rec = aiContext(http.MethodDelete, "/notifications/channels/1", "", "AlertsManager")
	if err := a.DeleteNotificationChannel(c, 1); err != nil || rec.Code != http.StatusNoContent {
		t.Fatalf("delete of a channel no schedule uses: %d %s", rec.Code, rec.Body.String())
	}
	var left int
	if err := pool.QueryRow("SELECT COUNT(*) FROM ai_schedule_runs").Scan(&left); err != nil || left != 0 {
		t.Errorf("runs left %d, %v", left, err)
	}
	c, rec = aiContext(http.MethodGet, "/ai/schedules/"+strconv.FormatInt(s.Id, 10), "", "AIManager")
	if err := a.GetAISchedule(c, s.Id); err != nil || rec.Code != http.StatusNotFound {
		t.Errorf("deleted schedule read: %d", rec.Code)
	}
}
