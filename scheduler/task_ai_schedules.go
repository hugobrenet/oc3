package scheduler

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"

	"github.com/opensvc/oc3/aischedule"
	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/notify"
)

// The schedules of the skills of the AI assistant: ai_schedules_queue queues
// the runs of the schedules due, at once; ai_schedules_run runs them, as long
// as they take, on the agent, as the technical account of the collector, and
// sends their reports to the channels of their schedules. Both do nothing
// without ai_schedules.enable.
var (
	TaskAISchedulesQueue = Task{
		name:    "ai_schedules_queue",
		desc:    "queue the runs of the schedules of the AI assistant skills due, and fail those a stopped scheduler left running (ai_schedules.enable)",
		period:  time.Minute,
		fn:      taskAISchedulesQueue,
		timeout: time.Minute,
	}

	TaskAISchedulesRun = Task{
		name:    "ai_schedules_run",
		desc:    "run the queued runs of the schedules of the AI assistant skills, and send their reports to their channels (ai_schedules.enable)",
		period:  time.Minute,
		fn:      taskAISchedulesRun,
		timeout: aiSchedulesRunTask,
	}
)

// aiSchedulesRunTask bounds a run of the task ai_schedules_run.
const aiSchedulesRunTask = time.Hour

// aiRunRecordTimeout bounds the recording of the end of a run.
var aiRunRecordTimeout = 10 * time.Second

// aiScheduleConfig is the configuration of the schedules, under ai_schedules
// in the configuration file.
type aiScheduleConfig struct {
	agent      aischedule.Agent
	grant      aischedule.Grant
	collector  aischedule.Collector
	runTimeout time.Duration
	workers    int
	// maxRuns bounds the runs of a task run: the others wait for the next.
	maxRuns int
}

func readAIScheduleConfig() (aiScheduleConfig, error) {
	const k = "ai_schedules."
	c := aiScheduleConfig{
		runTimeout: viper.GetDuration(k + "run_timeout"),
		workers:    max(viper.GetInt(k+"workers"), 1),
	}
	if c.runTimeout <= 0 {
		c.runTimeout = 10 * time.Minute
	}
	c.maxRuns = c.workers * max(int(aiSchedulesRunTask/c.runTimeout)-1, 1)
	base := strings.TrimSpace(viper.GetString(k + "agent.base_url"))
	path := strings.TrimSpace(viper.GetString(k + "agent.path"))
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || !strings.HasPrefix(path, "/") {
		return c, fmt.Errorf("%sagent.base_url and %sagent.path: an http(s) URL and an absolute path, such as /v1/oc3", k, k)
	}
	if !secureOrLoopback(u) {
		return c, fmt.Errorf("%sagent.base_url: https, or http on the loopback only", k)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile := viper.GetString(k + "agent.ca_file"); caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return c, fmt.Errorf("%sagent.ca_file: %w", k, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return c, fmt.Errorf("%sagent.ca_file: no PEM certificate", k)
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.agent = aischedule.Agent{URL: strings.TrimRight(u.String(), "/") + path, Client: &http.Client{Transport: transport, CheckRedirect: noRedirect}}

	// The API of the collector, which the check of the skill calls with the
	// token of the run.
	collector, err := url.Parse(strings.TrimSpace(viper.GetString(k + "collector.url")))
	if err != nil || collector.Host == "" || collector.User != nil || collector.RawQuery != "" {
		return c, fmt.Errorf("%scollector.url: the URL of the API of the collector, such as http://127.0.0.1:8081/api", k)
	}
	if !secureOrLoopback(collector) {
		return c, fmt.Errorf("%scollector.url: https, or http on the loopback only", k)
	}
	c.collector = aischedule.Collector{URL: strings.TrimRight(collector.String(), "/"),
		Client: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: noRedirect}}

	c.grant = aischedule.Grant{
		TokenURL: viper.GetString(k + "auth.token_url"),
		ClientID: viper.GetString(k + "auth.client_id"),
		Username: viper.GetString(k + "auth.username"),
		Password: viper.GetString(k + "auth.password"),
		Scope:    viper.GetString(k + "auth.scope"),
		Client:   &http.Client{Timeout: 30 * time.Second, CheckRedirect: noRedirect},
	}
	if c.grant.Password == "" {
		if file := viper.GetString(k + "auth.password_file"); file != "" {
			b, err := os.ReadFile(file)
			if err != nil {
				return c, fmt.Errorf("%sauth.password_file: %w", k, err)
			}
			c.grant.Password = strings.TrimSpace(string(b))
		}
	}
	if !strings.HasPrefix(c.grant.TokenURL, "https://") || c.grant.ClientID == "" || c.grant.Username == "" || c.grant.Password == "" {
		return c, fmt.Errorf("%sauth: token_url (https), client_id, username and password (or password_file, or OC3_AI_SCHEDULES_AUTH_PASSWORD) are required", k)
	}
	return c, nil
}

// secureOrLoopback tells whether the tokens of the technical account may go
// to u: over https, or over http to the host itself.
func secureOrLoopback(u *url.URL) bool {
	return u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))
}

func taskAISchedulesQueue(ctx context.Context, task *Task) error {
	if !viper.GetBool("ai_schedules.enable") {
		return nil
	}
	odb := task.DB()
	var errs []error
	runTimeout := viper.GetDuration("ai_schedules.run_timeout")
	if runTimeout <= 0 {
		runTimeout = 10 * time.Minute
	}
	// A run is bounded by run_timeout: one running much longer was left by a
	// scheduler stopped meanwhile.
	if n, err := odb.FailStaleAIScheduleRuns(ctx, runTimeout+5*time.Minute); err != nil {
		errs = append(errs, err)
	} else if n > 0 {
		task.Warnf("%d runs left running by a stopped scheduler failed", n)
	}
	now := time.Now()
	due, err := odb.DueAISchedules(ctx, now)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, s := range due {
		// The next run first: a schedule failing to queue is not queued again
		// at each run of the task. A scheduler down for a while runs it once.
		next, err := aischedule.NextRun(s.Days, s.AtTime, s.Timezone, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("schedule %s: %w", s.Name, err))
			continue
		}
		if moved, err := odb.SetAIScheduleNextRun(ctx, s.ID, *s.NextRun, &next); err != nil {
			errs = append(errs, err)
			continue
		} else if !moved {
			// Changed meanwhile, or queued by another scheduler.
			continue
		}
		if _, err := odb.QueueAIScheduleRun(ctx, s.ID, "schedule", ""); errors.Is(err, cdb.ErrAIRunPending) {
			task.Warnf("schedule %s: skipped, its previous run is not over", s.Name)
		} else if err != nil {
			errs = append(errs, err)
		} else {
			task.Infof("schedule %s: run queued, next at %s", s.Name, next.Format(time.RFC3339))
		}
	}
	return errors.Join(errs...)
}

func taskAISchedulesRun(ctx context.Context, task *Task) error {
	if !viper.GetBool("ai_schedules.enable") {
		return nil
	}
	// A configuration that cannot run them fails the runs queued, the
	// reason told in each, rather than leave them queued without one.
	cfg, cfgErr := readAIScheduleConfig()
	ncfg, ncfgErr := readNotifyConfig()
	if ncfgErr != nil {
		cfgErr = errors.Join(cfgErr, ncfgErr)
	}
	execute := cfg.execute
	if cfgErr != nil {
		cfg.workers, cfg.maxRuns = 1, 100
		execute = func(context.Context, cdb.AISchedule, cdb.AIScheduleRun) (aischedule.Result, error) {
			return aischedule.Result{}, fmt.Errorf("the scheduler cannot run the schedules: %w", cfgErr)
		}
	}
	odb := task.DB()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
		runs = make(chan cdb.AIScheduleRun)
	)
	if cfgErr != nil {
		errs = append(errs, cfgErr)
	}
	for range cfg.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range runs {
				if err := runAISchedule(ctx, odb, ncfg, r, cfg.runTimeout, execute); err != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("run %d: %w", r.ID, err))
					mu.Unlock()
				}
			}
		}()
	}
	for range cfg.maxRuns {
		r, err := odb.TakeAIScheduleRun(ctx)
		if err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
			break
		}
		if r == nil {
			break
		}
		runs <- *r
	}
	close(runs)
	wg.Wait()
	return errors.Join(errs...)
}

// execute runs the skill of s on the agent, unattended, as the technical
// account: its token first, then the check that the account reads the skill,
// without which the agent would run the prompt alone.
func (cfg aiScheduleConfig) execute(ctx context.Context, s cdb.AISchedule, r cdb.AIScheduleRun) (aischedule.Result, error) {
	token, err := cfg.grant.Token(ctx)
	if err != nil {
		return aischedule.Result{}, err
	}
	if err := cfg.collector.CheckSkill(ctx, token, s.Skill); err != nil {
		return aischedule.Result{}, err
	}
	// The run is named in the audit of the agent, with its request ID.
	threadID := "oc3-schedule-" + strconv.FormatInt(s.ID, 10)
	runID := "oc3-schedule-run-" + strconv.FormatInt(r.ID, 10)
	return cfg.agent.Run(ctx, token, threadID, runID, aischedule.Prompt(s.Skill, s.Prompt))
}

// runAISchedule runs r with execute, sends its report, and records how it
// ended; it returns the errors of the collector, not those of the run,
// recorded in it.
func runAISchedule(ctx context.Context, odb *cdb.DB, ncfg notifyConfig, r cdb.AIScheduleRun, timeout time.Duration,
	execute func(context.Context, cdb.AISchedule, cdb.AIScheduleRun) (aischedule.Result, error)) error {
	// Recorded even when the task is over, once the run is: a context of its
	// own, its time counted from then.
	end := func(r cdb.AIScheduleRun) error {
		endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), aiRunRecordTimeout)
		defer cancel()
		return odb.EndAIScheduleRun(endCtx, r)
	}
	s, err := odb.AIScheduleByID(ctx, r.ScheduleID)
	if err != nil {
		r.State, r.Error = cdb.AIRunFailed, "cannot read the schedule"
		return errors.Join(err, end(r))
	}
	if s == nil {
		// Deleted meanwhile, with its runs.
		return nil
	}

	runCtx, cancelRun := context.WithTimeout(ctx, timeout)
	res, runErr := execute(runCtx, *s, r)
	cancelRun()
	r.ToolCalls, r.RefusedActions, r.Report, r.RequestID = res.ToolCalls, res.NotRun, res.Report, res.RequestID
	for _, c := range res.Calls {
		r.Calls = append(r.Calls, cdb.AIRunCall{Name: c.Name, DurationMs: c.DurationMs, Error: c.Error, NotRun: c.NotRun})
	}
	report := notify.Report{Title: ncfg.title, PublicURL: ncfg.publicURL, Name: s.Name, Skill: s.Skill, Body: res.Report, Path: "/ai-skills",
		Reason: "Sent by the schedule " + s.Name + " of the AI assistant, run as the technical account of the collector."}
	if runErr != nil {
		r.State, r.Error = cdb.AIRunFailed, runErr.Error()
		if !s.SendFailures {
			return end(r)
		}
		// A failure is told once per cause: the run before failed the same
		// way, such as with a configuration unreadable for a while, and the
		// channels heard of it. A success, or another cause, tells again.
		if previous, err := odb.PreviousAIScheduleRun(ctx, s.ID, r.ID); err == nil && previous != nil &&
			previous.State == cdb.AIRunFailed && strings.HasPrefix(previous.Error, r.Error) {
			r.Error += " (already told to the channels)"
			return end(r)
		}
		report.Failed = true
		report.Body = "The run gave no report: " + runErr.Error()
	} else if res.NotRun > 0 {
		report.Body += fmt.Sprintf("\n\n---\n_The assistant asked for %d change(s), not made: the scheduled runs make no change._", res.NotRun)
	}

	sent, sendErrs := 0, []string{}
	for _, id := range s.ChannelIDs {
		if err := sendAIReport(ctx, odb, ncfg, id, report); err != nil {
			sendErrs = append(sendErrs, "channel "+strconv.FormatInt(id, 10)+": "+err.Error())
		} else {
			sent++
		}
	}
	if runErr == nil {
		r.State = cdb.AIRunSent
		if sent == 0 {
			r.State = cdb.AIRunUnsent
		}
	}
	if len(sendErrs) > 0 {
		r.Error = strings.TrimPrefix(r.Error+"; "+strings.Join(sendErrs, "; "), "; ")
	}
	return end(r)
}

// sendAIReport sends the report to the channel id, as the alerts are sent.
func sendAIReport(ctx context.Context, odb *cdb.DB, ncfg notifyConfig, id int64, report notify.Report) error {
	ch, err := odb.NotifyChannelByID(ctx, id)
	if err != nil {
		return errors.New("cannot read the channel")
	}
	if ch == nil {
		return errors.New("no such channel")
	}
	if !ch.Enabled {
		return errors.New("the channel is disabled")
	}
	dest, err := notify.DestinationOf(ncfg.ring, ch)
	if err != nil {
		return err
	}
	sender, err := channelSender(ncfg, ch)
	if err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return sender.SendReport(sendCtx, dest, report)
}
