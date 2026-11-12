package scheduler

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/spf13/viper"

	"github.com/opensvc/oc3/aischedule"
	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/migrate"
)

// aiScheduleDB is a new database of the MariaDB server OC3_TEST_MYSQL_DSN
// names, holding the tables of the schedules and of the channels.
func aiScheduleDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("OC3_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("OC3_TEST_MYSQL_DSN is not set, as root:pass@tcp(127.0.0.1:3306)/: no MariaDB to test against")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ParseTime, cfg.Loc, cfg.DBName = true, time.Local, ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("oc3_test_sched_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE " + name) })
	cfg.DBName = name
	pool, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
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
	return pool
}

func TestAISchedulesQueueAndRun(t *testing.T) {
	pool := aiScheduleDB(t)
	viper.Set("ai_schedules.enable", true)
	t.Cleanup(func() { viper.Set("ai_schedules.enable", false) })
	for _, s := range []string{
		"INSERT INTO notification_channels (name, type, description, settings_json, enabled, created, updated)" +
			" VALUES ('off', 'email', '', '{\"addresses\":[\"ops@example.com\"]}', 0, NOW(), NOW())",
		// Due a minute ago, and not due.
		"INSERT INTO ai_schedules (name, skill, prompt, days, at_time, timezone, channel_ids, next_run, created, updated, updated_by)" +
			" VALUES ('due', 'morning-check', '', 'mon,tue,wed,thu,fri,sat,sun', '08:00', 'Europe/Paris', '[1]', NOW() - INTERVAL 1 MINUTE, NOW(), NOW(), 'x')," +
			" ('later', 'morning-check', '', 'mon', '08:00', 'Europe/Paris', '[1]', NOW() + INTERVAL 1 DAY, NOW(), NOW(), 'x')",
	} {
		if _, err := pool.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	task := &Task{name: "test", db: pool}
	for range 2 {
		// Queued once: the second run of the task finds it not due.
		if err := taskAISchedulesQueue(t.Context(), task); err != nil {
			t.Fatal(err)
		}
	}
	var queued int
	var next time.Time
	if err := pool.QueryRow("SELECT COUNT(*) FROM ai_schedule_runs WHERE schedule_id = 1 AND state = 'queued' AND run_trigger = 'schedule'").Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("queued %d, %v", queued, err)
	}
	if err := pool.QueryRow("SELECT next_run FROM ai_schedules WHERE id = 1").Scan(&next); err != nil || !next.After(time.Now()) {
		t.Fatalf("next run %v, %v", next, err)
	}

	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/oc3/runs" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// The run outlasts the recording timeout: the end of the run is
		// recorded all the same.
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("X-Request-ID", "req-7")
		fmt.Fprint(w, "data: {\"type\":\"TEXT_MESSAGE_START\"}\n\n"+
			"data: {\"type\":\"TEXT_MESSAGE_CONTENT\",\"delta\":\"# All good\"}\n\ndata: {\"type\":\"TEXT_MESSAGE_END\"}\n\n"+
			"data: {\"type\":\"RUN_FINISHED\",\"outcome\":{\"type\":\"success\"},\"result\":{\"unattended\":true,"+
			"\"toolCalls\":[{\"name\":\"list_alerts\",\"durationMs\":40},{\"name\":\"attach_node_tag\",\"error\":true,\"notRun\":\"unoffered\"}],"+
			"\"toolCallCount\":2,\"notRunCalls\":1}}\n\n")
	}))
	defer agent.Close()
	sso := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok"}`))
	}))
	defer sso.Close()
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ai/skills/morning-check":
			_, _ = w.Write([]byte(`{"name":"morning-check","backends":["oc3"]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer collector.Close()
	cfg := aiScheduleConfig{
		agent:      aischedule.Agent{URL: agent.URL + "/v1/oc3", Client: agent.Client()},
		grant:      aischedule.Grant{TokenURL: sso.URL, ClientID: "oc3", Username: "bot", Password: "p", Client: sso.Client()},
		collector:  aischedule.Collector{URL: collector.URL + "/api", Client: collector.Client()},
		runTimeout: time.Minute, workers: 1, maxRuns: 5,
	}
	recordTimeout := aiRunRecordTimeout
	aiRunRecordTimeout = 100 * time.Millisecond
	t.Cleanup(func() { aiRunRecordTimeout = recordTimeout })
	odb := task.DB()
	run := func() cdb.AIScheduleRun {
		t.Helper()
		r, err := odb.TakeAIScheduleRun(t.Context())
		if err != nil || r == nil {
			t.Fatalf("take %v, %v", r, err)
		}
		if err := runAISchedule(t.Context(), odb, notifyConfig{}, *r, cfg.runTimeout, cfg.execute); err != nil {
			t.Fatal(err)
		}
		runs, err := odb.AIScheduleRuns(t.Context(), 1, 5)
		if err != nil || len(runs) == 0 {
			t.Fatalf("runs %+v, %v", runs, err)
		}
		return runs[0]
	}
	queue := func() {
		t.Helper()
		if _, err := odb.QueueAIScheduleRun(t.Context(), 1, "manual", "ops@example.com"); err != nil {
			t.Fatal(err)
		}
	}
	// The report given, but no channel took it: the only one is disabled.
	// The write the assistant asked for is told, with the calls and the
	// request ID of the agent.
	if got := run(); got.State != "unsent" || got.Report != "# All good" || got.ToolCalls != 2 || got.RefusedActions != 1 ||
		got.RequestID != "req-7" || len(got.Calls) != 2 || got.Calls[1] != (cdb.AIRunCall{Name: "attach_node_tag", Error: true, NotRun: "unoffered"}) ||
		got.Ended == nil || !strings.Contains(got.Error, "channel 1: the channel is disabled") {
		t.Fatalf("run %+v", got)
	}

	// The agent down: the run fails, and says why.
	agent.Close()
	queue()
	if got := run(); got.State != "failed" || !strings.Contains(got.Error, "run:") || got.QueuedBy != "ops@example.com" {
		t.Fatalf("run %+v", got)
	}

	// Two runs asked at once: one only is queued.
	queue()
	if _, err := odb.QueueAIScheduleRun(t.Context(), 1, "manual", "ops@example.com"); !errors.Is(err, cdb.ErrAIRunPending) {
		t.Fatalf("second run: %v", err)
	}

	// A scheduler that cannot run the schedules fails the run queued, and
	// says why, rather than leave it queued.
	viper.Set("ai_schedules.agent.base_url", "")
	if err := taskAISchedulesRun(t.Context(), task); err == nil {
		t.Fatal("the task did not report the configuration")
	}
	runs, _ := odb.AIScheduleRuns(t.Context(), 1, 1)
	if got := runs[0]; got.State != "failed" || !strings.Contains(got.Error, "the scheduler cannot run the schedules") ||
		strings.Contains(got.Error, "already told") {
		t.Fatalf("run without configuration %+v", got)
	}
	// The same cause again: recorded, not told to the channels again.
	queue()
	if err := taskAISchedulesRun(t.Context(), task); err == nil {
		t.Fatal("the task did not report the configuration")
	}
	runs, _ = odb.AIScheduleRuns(t.Context(), 1, 1)
	if got := runs[0]; got.State != "failed" || !strings.HasSuffix(got.Error, "(already told to the channels)") {
		t.Fatalf("second run without configuration %+v", got)
	}
}

func TestReadAIScheduleConfig(t *testing.T) {
	set := func(kv map[string]any) {
		for k, v := range kv {
			viper.Set("ai_schedules."+k, v)
		}
	}
	t.Cleanup(func() {
		set(map[string]any{"agent.base_url": "", "agent.path": "", "auth.token_url": "", "auth.client_id": "", "auth.username": "", "auth.password": ""})
	})
	set(map[string]any{"collector.url": "http://127.0.0.1:8081/api", "agent.base_url": "https://agent:8090", "agent.path": "/v1/oc3", "auth.token_url": "https://sso/application/o/token/",
		"auth.client_id": "oc3", "auth.username": "bot", "auth.password": "p", "run_timeout": "10m", "workers": 2})
	cfg, err := readAIScheduleConfig()
	if err != nil || cfg.agent.URL != "https://agent:8090/v1/oc3" || cfg.maxRuns != 10 {
		t.Fatalf("cfg %+v, %v", cfg, err)
	}
	for name, kv := range map[string]map[string]any{
		"plain http":     {"agent.base_url": "http://agent:8090"},
		"no password":    {"auth.password": ""},
		"http provider":  {"auth.token_url": "http://sso/token"},
		"http collector": {"collector.url": "http://collector.example.com/api"},
	} {
		set(kv)
		if _, err := readAIScheduleConfig(); err == nil {
			t.Errorf("%s accepted", name)
		}
		set(map[string]any{"agent.base_url": "https://agent:8090", "auth.password": "p", "auth.token_url": "https://sso/application/o/token/",
			"collector.url": "http://127.0.0.1:8081/api"})
	}
}
