package aischedule

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "oc3" || r.Form.Get("username") != "bot" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Form.Get("password") != "app-password" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"bad credentials"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":1800}`))
	}))
	defer srv.Close()
	g := Grant{TokenURL: srv.URL, ClientID: "oc3", Username: "bot", Password: "app-password", Client: srv.Client()}
	if tok, err := g.Token(t.Context()); err != nil || tok != "tok" {
		t.Fatalf("token %q, %v", tok, err)
	}
	g.Password = "wrong"
	if _, err := g.Token(t.Context()); err == nil || !strings.Contains(err.Error(), "invalid_grant") || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("err %v", err)
	}
}

// fakeAgent serves the unattended route: it checks the input, a single
// message of the user and no tools, then streams a report and the summary.
func fakeAgent(t *testing.T, finished string) (*httptest.Server, *map[string]any) {
	var input map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/oc3/runs" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		w.Header().Set("X-Request-ID", "req-1")
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"type":"RUN_STARTED"}`,
			`{"type":"TEXT_MESSAGE_START","messageId":"m1"}`,
			`{"type":"TEXT_MESSAGE_CONTENT","delta":"Let me check."}`,
			`{"type":"TEXT_MESSAGE_END"}`,
			`{"type":"TOOL_CALL_START","toolCallId":"t1","toolCallName":"list_alerts"}`,
			`{"type":"TEXT_MESSAGE_START","messageId":"m2"}`,
			`{"type":"TEXT_MESSAGE_CONTENT","delta":"# Report\n"}`,
			`{"type":"TEXT_MESSAGE_CONTENT","delta":"all good"}`,
			`{"type":"TEXT_MESSAGE_END"}`,
			finished,
		} {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &input
}

const finishedUnattended = `{"type":"RUN_FINISHED","outcome":{"type":"success"},"result":{"unattended":true,"finishReason":"completed",` +
	`"toolCalls":[{"id":"t1","name":"list_alerts","durationMs":120,"error":false},{"id":"t2","name":"attach_node_tag","durationMs":0,"error":true,"notRun":"unoffered"}],` +
	`"toolCallCount":2,"notRunCalls":1,"refusedCalls":0}}`

func TestAgentRunsUnattended(t *testing.T) {
	srv, input := fakeAgent(t, finishedUnattended)
	res, err := Agent{URL: srv.URL + "/v1/oc3/", Client: srv.Client()}.Run(t.Context(), "tok", "oc3-schedule-3", "oc3-schedule-run-42", Prompt("morning-check", ""))
	if err != nil {
		t.Fatal(err)
	}
	if res.Report != "# Report\nall good" || res.ToolCalls != 2 || res.NotRun != 1 || res.RequestID != "req-1" ||
		len(res.Calls) != 2 || res.Calls[1] != (Call{Name: "attach_node_tag", Error: true, NotRun: "unoffered"}) {
		t.Fatalf("result %+v", res)
	}
	messages, _ := (*input)["messages"].([]any)
	if (*input)["threadId"] != "oc3-schedule-3" || (*input)["runId"] != "oc3-schedule-run-42" || len(messages) != 1 || (*input)["tools"] != nil {
		t.Fatalf("input %+v", *input)
	}
	if content := messages[0].(map[string]any)["content"].(string); !strings.Contains(content, `"morning-check"`) {
		t.Fatalf("prompt %q", content)
	}
}

func TestAgentRunErrors(t *testing.T) {
	srv, _ := fakeAgent(t, finishedUnattended)
	if _, err := (Agent{URL: srv.URL + "/v1/oc3", Client: srv.Client()}).Run(t.Context(), "expired", "t", "r", "x"); err == nil ||
		!strings.Contains(err.Error(), "401") {
		t.Fatalf("err %v", err)
	}
	// An agent ending the run otherwise than unattended, such as one of
	// before the route: no report taken.
	attended, _ := fakeAgent(t, `{"type":"RUN_FINISHED","outcome":{"type":"success"},"result":{"turnId":"x"}}`)
	if _, err := (Agent{URL: attended.URL + "/v1/oc3", Client: attended.Client()}).Run(t.Context(), "tok", "t", "r", "x"); err == nil ||
		!strings.Contains(err.Error(), "unattended") {
		t.Fatalf("err %v", err)
	}
	interrupted, _ := fakeAgent(t, `{"type":"RUN_FINISHED","outcome":{"type":"interrupt"}}`)
	if _, err := (Agent{URL: interrupted.URL + "/v1/oc3", Client: interrupted.Client()}).Run(t.Context(), "tok", "t", "r", "x"); err == nil ||
		!strings.Contains(err.Error(), `outcome "interrupt"`) {
		t.Fatalf("err %v", err)
	}
	failing, _ := fakeAgent(t, `{"type":"RUN_ERROR","code":"request_timeout","message":"the agent request timed out"}`)
	if _, err := (Agent{URL: failing.URL + "/v1/oc3", Client: failing.Client()}).Run(t.Context(), "tok", "t", "r", "x"); err == nil ||
		!strings.Contains(err.Error(), "request_timeout") {
		t.Fatalf("err %v", err)
	}
}
