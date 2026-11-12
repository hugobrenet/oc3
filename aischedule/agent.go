package aischedule

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Grant gets the access tokens of the technical account the schedules run as,
// from the OpenID provider of the collector: a client_credentials grant of the
// collector's client, with the username and the app password of the account,
// as Authentik gives them to its service accounts.
type Grant struct {
	TokenURL string
	ClientID string
	Username string
	Password string
	Scope    string
	Client   *http.Client
}

// Token returns a new access token.
func (g Grant) Token(ctx context.Context) (string, error) {
	form := url.Values{
		"grant_type": {"client_credentials"},
		"client_id":  {g.ClientID},
		"username":   {g.Username},
		"password":   {g.Password},
	}
	if g.Scope != "" {
		form.Set("scope", g.Scope)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var answer struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&answer)
	if resp.StatusCode != http.StatusOK || answer.AccessToken == "" {
		return "", fmt.Errorf("token: the provider answered %s: %s %s", resp.Status, answer.Error, answer.ErrorDescription)
	}
	return answer.AccessToken, nil
}

// Agent runs prompts on the AI agent of the collector, on the unattended
// route of its oc3 backend, as the account of the token given: the agent
// offers the model the read-only tools only, waits on nobody and stores
// nothing.
type Agent struct {
	// URL is the base URL of the backend, such as https://agent:8090/v1/oc3.
	URL string
	// Client calls the agent; the context of a run bounds it.
	Client *http.Client
}

// Call is a call of a tool a run made, as the agent summarized it.
type Call struct {
	Name       string
	DurationMs int64
	Error      bool
	// NotRun tells why the call did not run: unoffered, such as a write the
	// unattended run does not offer, or needs_confirmation.
	NotRun string
}

// Result is what a run gave.
type Result struct {
	// Report is the last message of the assistant.
	Report string
	// ToolCalls counts the calls of tools the run made, those that did not
	// run included.
	ToolCalls int
	// NotRun counts the calls that did not run: the writes the model asked
	// for.
	NotRun int
	// Refused counts the calls the bounds of the question refused.
	Refused int
	// Calls are the calls, the first 256.
	Calls []Call
	// RequestID is the X-Request-ID the agent gave the run.
	RequestID string
}

// maxEvent bounds an event of the stream: a tool result is at most 1 MiB,
// escaped in JSON.
const maxEvent = 8 << 20

// summary is the result of the RUN_FINISHED of an unattended run.
type summary struct {
	Unattended bool `json:"unattended"`
	ToolCalls  []struct {
		Name       string `json:"name"`
		DurationMs int64  `json:"durationMs"`
		Error      bool   `json:"error"`
		NotRun     string `json:"notRun"`
	} `json:"toolCalls"`
	ToolCallCount int `json:"toolCallCount"`
	NotRunCalls   int `json:"notRunCalls"`
	RefusedCalls  int `json:"refusedCalls"`
}

// Run asks prompt in one unattended run, named runID in the audit of the
// agent and threadID for the stream, and returns its last message.
func (a Agent) Run(ctx context.Context, token, threadID, runID, prompt string) (Result, error) {
	var res Result
	input := map[string]any{
		"threadId": threadID,
		"runId":    runID,
		"messages": []map[string]string{{"id": runID + "-prompt", "role": "user", "content": prompt}},
	}
	var done *summary
	err := a.call(ctx, token, "/runs", input, &res.RequestID, func(r io.Reader) error {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 64<<10), maxEvent)
		var text strings.Builder
		for scanner.Scan() {
			line, ok := strings.CutPrefix(scanner.Text(), "data:")
			if !ok {
				continue
			}
			var ev struct {
				Type    string          `json:"type"`
				Delta   string          `json:"delta"`
				Code    string          `json:"code"`
				Message string          `json:"message"`
				Result  json.RawMessage `json:"result"`
				Outcome struct {
					Type string `json:"type"`
				} `json:"outcome"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &ev); err != nil {
				return fmt.Errorf("event: %w", err)
			}
			switch ev.Type {
			case "TEXT_MESSAGE_START":
				text.Reset()
			case "TEXT_MESSAGE_CONTENT":
				text.WriteString(ev.Delta)
			case "TEXT_MESSAGE_END":
				if s := strings.TrimSpace(text.String()); s != "" {
					res.Report = s
				}
			case "RUN_ERROR":
				return fmt.Errorf("the assistant failed: %s %s", ev.Code, ev.Message)
			case "RUN_FINISHED":
				if ev.Outcome.Type != "success" {
					return fmt.Errorf("the agent ended the run with the outcome %q", ev.Outcome.Type)
				}
				var s summary
				if json.Unmarshal(ev.Result, &s) != nil || !s.Unattended {
					return errors.New("the agent did not end the run as an unattended one")
				}
				done = &s
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		if done == nil {
			return errors.New("the stream ended before the run")
		}
		return nil
	})
	if done != nil {
		res.ToolCalls, res.NotRun, res.Refused = done.ToolCallCount, done.NotRunCalls, done.RefusedCalls
		for _, c := range done.ToolCalls {
			res.Calls = append(res.Calls, Call{Name: c.Name, DurationMs: c.DurationMs, Error: c.Error, NotRun: c.NotRun})
		}
	}
	if err != nil {
		return res, fmt.Errorf("run: %w", err)
	}
	if strings.TrimSpace(res.Report) == "" {
		return res, errors.New("the assistant gave no answer")
	}
	return res, nil
}

// call posts body, none when nil, to path and reads a 2xx answer with read;
// requestID, when set, takes the X-Request-ID of the answer, whatever it is.
func (a Agent) call(ctx context.Context, token, path string, body any, requestID *string, read func(io.Reader) error) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.URL, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := a.Client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if requestID != nil {
		*requestID = resp.Header.Get("X-Request-ID")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		answer, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("the agent answered %s: %s", resp.Status, strings.TrimSpace(string(answer)))
	}
	return read(resp.Body)
}

// Prompt is what the scheduler asks the assistant: the skill, the prompt of
// the schedule, and that nobody is there to answer nor to confirm.
func Prompt(skill, prompt string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Use the skill %q.\n", skill)
	if p := strings.TrimSpace(prompt); p != "" {
		b.WriteString("\n" + p + "\n")
	}
	b.WriteString("\nThis is a scheduled run: its answer is sent as a report to notification channels. " +
		"Answer with the report alone, in Markdown.")
	return b.String()
}
