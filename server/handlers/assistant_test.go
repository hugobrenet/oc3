package serverhandlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/shaj13/go-guardian/v2/auth"

	"github.com/opensvc/oc3/server"
	"github.com/opensvc/oc3/xauth"
)

// fakeAgent records what reaches it and answers as the agent does.
type fakeAgent struct {
	method, path, query, auth, contentType, cookie, extra, body string
}

func newFakeAgent(t *testing.T, seen *fakeAgent) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*seen = fakeAgent{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization"),
			contentType: r.Header.Get("Content-Type"), cookie: r.Header.Get("Cookie"), extra: r.Header.Get("X-Extra"), body: string(b)}
		switch {
		case strings.HasSuffix(r.URL.Path, "/runs"):
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			_, _ = io.WriteString(w, "data: {\"type\":\"TEXT_MESSAGE_CONTENT\",\"messageId\":\"t:2\",\"delta\":\"Hello\"}\n\n")
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, "data: {\"type\":\"RUN_FINISHED\",\"outcome\":{\"type\":\"success\"}}\n\n")
		case strings.HasSuffix(r.URL.Path, "/missing"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"conversation_not_found","message":"no such conversation"}}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"conversations":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func assistantAPI(t *testing.T, agentURL string) *Api {
	t.Helper()
	u, _ := url.Parse(agentURL + "/base")
	return &Api{Assistant: &AssistantConfig{BaseURL: u, Path: "/v1/oc3", Client: http.DefaultClient, Stream: http.DefaultClient}}
}

// assistantRequest builds a request of a user signed in with source, carrying a
// bearer token, a cookie and another header, none of which but the token's
// value may reach the agent.
func assistantRequest(method, target, body, source string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer user-access-token")
	req.Header.Set("Cookie", "oc3_session=secret-session")
	req.Header.Set("X-Extra", "do-not-forward")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	ext := make(auth.Extensions)
	ext.Set(xauth.XAuthSource, source)
	c.Set("user", auth.NewUserInfo("alice@example.com", "7", nil, ext))
	return c, rec
}

func TestAssistantRelay(t *testing.T) {
	var seen fakeAgent
	agent := newFakeAgent(t, &seen)
	a := assistantAPI(t, agent.URL)

	c, rec := assistantRequest(http.MethodGet, "/api/assistant/conversations/c1/messages?limit=20", "", xauth.AuthSourceBearer)
	limit := 20
	if err := a.GetAssistantMessages(c, "c1", server.GetAssistantMessagesParams{Limit: &limit}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || seen.path != "/base/v1/oc3/conversations/c1/messages" || seen.query != "limit=20" {
		t.Fatalf("relayed to %s?%s, answered %d", seen.path, seen.query, rec.Code)
	}
	if seen.auth != "Bearer user-access-token" || seen.cookie != "" || seen.extra != "" {
		t.Fatalf("forwarded headers: auth %q cookie %q extra %q", seen.auth, seen.cookie, seen.extra)
	}

	// The agent's errors come back as they are.
	c, rec = assistantRequest(http.MethodGet, "/", "", xauth.AuthSourceBearer)
	if err := a.GetAssistantConversation(c, "missing"); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "conversation_not_found") {
		t.Fatalf("error: %d %s", rec.Code, rec.Body.String())
	}

	// An id that is not one never reaches the agent.
	for _, bad := range []string{"../../admin", "a/b", "a%2Fb", "", "a?x=1"} {
		seen = fakeAgent{}
		c, rec = assistantRequest(http.MethodGet, "/", "", xauth.AuthSourceBearer)
		_ = a.GetAssistantConversation(c, bad)
		if rec.Code != http.StatusBadRequest || seen.method != "" {
			t.Errorf("id %q: %d, agent called: %v", bad, rec.Code, seen.method != "")
		}
	}

	// A run streams, its AG-UI input relayed as it is.
	input := `{"threadId":"c1","runId":"r1","messages":[{"id":"u1","role":"user","content":"hi"}]}`
	c, rec = assistantRequest(http.MethodPost, "/", input, xauth.AuthSourceBearer)
	if err := a.PostAssistantRun(c, "c1"); err != nil {
		t.Fatal(err)
	}
	if seen.path != "/base/v1/oc3/conversations/c1/runs" || seen.body != input || seen.contentType != "application/json" || seen.method != http.MethodPost {
		t.Fatalf("run request: %+v", seen)
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(rec.Body.String(), `"type":"RUN_FINISHED"`) ||
		!strings.Contains(rec.Body.String(), `"delta":"Hello"`) {
		t.Fatalf("stream: %q %q", rec.Header().Get("Content-Type"), rec.Body.String())
	}

	// The input of a run may weigh more than the 64 KiB of the other routes, up to 4 MiB.
	big := `{"threadId":"c1","runId":"r2","messages":[{"id":"u2","role":"user","content":"` + strings.Repeat("x", 100<<10) + `"}]}`
	c, rec = assistantRequest(http.MethodPost, "/", big, xauth.AuthSourceBearer)
	if err := a.PostAssistantRun(c, "c1"); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(seen.body) != len(big) {
		t.Fatalf("run of %d bytes: %d, relayed %d", len(big), rec.Code, len(seen.body))
	}
	c, rec = assistantRequest(http.MethodPost, "/", strings.Repeat("x", 4<<20+1), xauth.AuthSourceBearer)
	seen = fakeAgent{}
	if err := a.PostAssistantRun(c, "c1"); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusRequestEntityTooLarge || seen.method != "" {
		t.Fatalf("run over 4 MiB: %d, agent called: %v", rec.Code, seen.method != "")
	}
	c, rec = assistantRequest(http.MethodPatch, "/", `{"title":"`+strings.Repeat("x", 64<<10)+`"}`, xauth.AuthSourceBearer)
	seen = fakeAgent{}
	if err := a.PatchAssistantConversation(c, "c1"); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusRequestEntityTooLarge || seen.method != "" {
		t.Fatalf("rename over 64 KiB: %d, agent called: %v", rec.Code, seen.method != "")
	}

	// The capabilities are relayed.
	c, rec = assistantRequest(http.MethodGet, "/", "", xauth.AuthSourceBearer)
	if err := a.GetAssistantCapabilities(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || seen.path != "/base/v1/oc3/capabilities" {
		t.Fatalf("capabilities relayed to %s: %d", seen.path, rec.Code)
	}

	// The tools are relayed, with the token only.
	c, rec = assistantRequest(http.MethodGet, "/", "", xauth.AuthSourceBearer)
	if err := a.GetAssistantTools(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || seen.method != http.MethodGet || seen.path != "/base/v1/oc3/tools" ||
		seen.auth != "Bearer user-access-token" || seen.cookie != "" || seen.extra != "" {
		t.Fatalf("tools relayed: %+v, %d", seen, rec.Code)
	}
}

func TestAssistantRefusals(t *testing.T) {
	var seen fakeAgent
	agent := newFakeAgent(t, &seen)

	check := func(name string, a *Api, c echo.Context, rec *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		seen = fakeAgent{}
		if err := a.GetAssistantConversations(c); err != nil {
			t.Fatal(err)
		}
		if rec.Code != status || !strings.Contains(rec.Body.String(), code) || seen.method != "" {
			t.Errorf("%s: %d %s, agent called: %v", name, rec.Code, rec.Body.String(), seen.method != "")
		}
	}
	c, rec := assistantRequest(http.MethodGet, "/", "", xauth.AuthSourceBearer)
	check("not configured", &Api{}, c, rec, http.StatusNotFound, "assistant_disabled")
	c, rec = assistantRequest(http.MethodGet, "/", "", xauth.AuthSourceBasic)
	check("password sign-in", assistantAPI(t, agent.URL), c, rec, http.StatusUnauthorized, "assistant_token_unavailable")
	c, rec = assistantRequest(http.MethodGet, "/", "", xauth.AuthSourceBearer)
	c.Set(XImpersonator, auth.NewUserInfo("manager@example.com", "1", nil, nil))
	check("impersonating", assistantAPI(t, agent.URL), c, rec, http.StatusForbidden, "assistant_forbidden")
}

// The feedback on a turn is relayed with its body, and withdrawn; a turn id that
// is not one never reaches the agent.
func TestAssistantFeedbackRelay(t *testing.T) {
	var seen fakeAgent
	agent := newFakeAgent(t, &seen)
	a := assistantAPI(t, agent.URL)

	body := `{"rating":"down","reasons":["missing_information"],"expected":"the quota"}`
	c, rec := assistantRequest(http.MethodPut, "/", body, xauth.AuthSourceBearer)
	if err := a.PutAssistantFeedback(c, "c1", "t1"); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || seen.method != http.MethodPut || seen.path != "/base/v1/oc3/conversations/c1/turns/t1/feedback" ||
		seen.body != body || seen.contentType != "application/json" {
		t.Fatalf("put: %d %+v", rec.Code, seen)
	}

	c, _ = assistantRequest(http.MethodDelete, "/", "", xauth.AuthSourceBearer)
	if err := a.DeleteAssistantFeedback(c, "c1", "t1"); err != nil {
		t.Fatal(err)
	}
	if seen.method != http.MethodDelete || seen.path != "/base/v1/oc3/conversations/c1/turns/t1/feedback" || seen.body != "" {
		t.Fatalf("delete: %+v", seen)
	}

	for _, bad := range []string{"../t", "t/x", ""} {
		seen = fakeAgent{}
		c, rec = assistantRequest(http.MethodPut, "/", body, xauth.AuthSourceBearer)
		_ = a.PutAssistantFeedback(c, "c1", bad)
		if rec.Code != http.StatusBadRequest || seen.method != "" {
			t.Errorf("turn %q: %d, agent called: %v", bad, rec.Code, seen.method != "")
		}
	}
}
