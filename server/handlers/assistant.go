package serverhandlers

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/opensvc/oc3/server"
	"github.com/opensvc/oc3/util/echolog"
	"github.com/opensvc/oc3/util/logkey"
	"github.com/opensvc/oc3/xauth"
)

// AssistantConfig is the AI agent oc3 relays the assistant routes to: its base
// URL and the path of the collector's backend, both from the configuration
// (server.assistant), and the HTTP client calling it.
type AssistantConfig struct {
	BaseURL *url.URL
	Path    string
	// Client calls the agent for the answers in one piece; Stream for the streamed
	// runs, without an overall timeout, ended with the request of the browser.
	Client *http.Client
	Stream *http.Client
}

const (
	// assistantBodyLimit is the size of a request body relayed to the routes of
	// the conversations: the agent refuses more than 64 KiB.
	assistantBodyLimit = 64 << 10
	// assistantRunBodyLimit is the size of the AG-UI input of a run relayed: the
	// agent refuses more than 4 MiB.
	assistantRunBodyLimit = 4 << 20
	// assistantAnswerLimit is the size of an answer relayed in one piece: the
	// agent's message pages are at most 1 MiB.
	assistantAnswerLimit = 2 << 20
)

// assistantError writes an error of the relay in the agent's error shape, so that
// the view reads them alike.
func assistantError(c echo.Context, status int, code, message string) error {
	var resp server.AssistantError
	resp.Error.Code = code
	resp.Error.Message = message
	return c.JSON(status, resp)
}

// assistantToken returns the access token to call the agent with: that of the
// single sign-on session of the request, refreshed when needed, or the bearer
// token of the request itself. A password sign-in has none; while impersonating,
// the token would be the Manager's, so none either.
func (a *Api) assistantToken(c echo.Context) (string, string, error) {
	if c.Get(XImpersonator) != nil {
		return "", "impersonating", nil
	}
	user := UserInfoFromContext(c)
	if user == nil {
		return "", "token_unavailable", nil
	}
	switch user.GetExtensions().Get(xauth.XAuthSource) {
	case xauth.AuthSourceBearer:
		_, raw, _ := strings.Cut(c.Request().Header.Get("Authorization"), " ")
		return strings.TrimSpace(raw), "", nil
	case xauth.AuthSourceSession:
		if a.OIDC == nil {
			return "", "token_unavailable", nil
		}
		cookie, err := c.Cookie(a.OIDC.SessionCookieName())
		if err != nil {
			return "", "token_unavailable", nil
		}
		token, err := a.OIDC.AccessToken(c.Request().Context(), cookie.Value)
		if errors.Is(err, xauth.ErrNoToken) {
			return "", "token_unavailable", nil
		}
		if err != nil {
			return "", "", err
		}
		return token, "", nil
	}
	return "", "password_sign_in", nil
}

// GetAssistantStatus handles GET /assistant/status.
func (a *Api) GetAssistantStatus(c echo.Context) error {
	resp := server.AssistantStatus{Enabled: a.Assistant != nil}
	reason := "disabled"
	if a.Assistant != nil {
		token, why, err := a.assistantToken(c)
		if err != nil {
			echolog.GetLogHandler(c, "GetAssistantStatus").Error("cannot read the access token", logkey.Error, err)
			why = "token_unavailable"
		}
		resp.Available = token != ""
		reason = why
	}
	if !resp.Available {
		r := server.AssistantStatusReason(reason)
		resp.Reason = &r
	}
	return c.JSON(http.StatusOK, resp)
}

// assistantRelay forwards a request to the agent: the method, the path under
// the configured backend path, the query, a JSON body of at most bodyLimit
// bytes (none when 0), and the access token of the caller. Nothing else of the
// browser's request goes: no cookie, no other header. The answer comes back as
// it is, a stream of events as it arrives.
func (a *Api) assistantRelay(c echo.Context, method, subpath string, query url.Values, bodyLimit int, stream bool) error {
	log := echolog.GetLogHandler(c, "assistant")
	if a.Assistant == nil {
		return assistantError(c, http.StatusNotFound, "assistant_disabled", "the AI assistant is not configured")
	}
	if subpath == "" {
		return assistantError(c, http.StatusBadRequest, "invalid_request", "invalid conversation or turn id")
	}
	token, why, err := a.assistantToken(c)
	if err != nil {
		log.Error("cannot read the access token", logkey.Error, err)
		return assistantError(c, http.StatusServiceUnavailable, "assistant_unavailable", "the access token cannot be read; retry later")
	}
	switch why {
	case "impersonating":
		return assistantError(c, http.StatusForbidden, "assistant_forbidden", "the assistant cannot be used while impersonating")
	case "password_sign_in", "token_unavailable":
		return assistantError(c, http.StatusUnauthorized, "assistant_token_unavailable", "sign in with single sign-on to use the assistant")
	}

	var body io.Reader
	withBody := bodyLimit > 0
	if withBody {
		b, err := io.ReadAll(io.LimitReader(c.Request().Body, int64(bodyLimit)+1))
		if err != nil {
			return assistantError(c, http.StatusBadRequest, "invalid_request", "the request body cannot be read")
		}
		if len(b) > bodyLimit {
			return assistantError(c, http.StatusRequestEntityTooLarge, "request_too_large",
				fmt.Sprintf("the request body exceeds %d KiB", bodyLimit>>10))
		}
		body = bytes.NewReader(b)
	}
	target := *a.Assistant.BaseURL
	target.Path = strings.TrimRight(target.Path, "/") + "/" + strings.Trim(a.Assistant.Path, "/") + subpath
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(c.Request().Context(), method, target.String(), body)
	if err != nil {
		return assistantError(c, http.StatusInternalServerError, "assistant_unreachable", "the agent request cannot be built")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	if withBody {
		req.Header.Set("Content-Type", "application/json")
	}
	client := a.Assistant.Client
	if stream {
		client = a.Assistant.Stream
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Error("cannot reach the agent", "url", target.Redacted(), logkey.Error, err)
		return assistantError(c, http.StatusBadGateway, "assistant_unreachable", "the AI agent cannot be reached")
	}
	defer func() { _ = resp.Body.Close() }()

	h := c.Response().Header()
	h.Set("Cache-Control", "no-store")
	contentType := resp.Header.Get("Content-Type")
	if stream && resp.StatusCode == http.StatusOK && strings.HasPrefix(contentType, "text/event-stream") {
		h.Set("Content-Type", "text/event-stream")
		// No buffering by a proxy in front: each event goes out as it comes.
		h.Set("X-Accel-Buffering", "no")
		c.Response().WriteHeader(http.StatusOK)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := c.Response().Write(buf[:n]); werr != nil {
					return nil
				}
				c.Response().Flush()
			}
			if err != nil {
				return nil
			}
		}
	}
	answer, err := io.ReadAll(io.LimitReader(resp.Body, assistantAnswerLimit+1))
	if err != nil || len(answer) > assistantAnswerLimit {
		return assistantError(c, http.StatusBadGateway, "assistant_unreachable", "the agent answer cannot be read")
	}
	if resp.StatusCode == http.StatusNoContent || len(answer) == 0 {
		return c.NoContent(resp.StatusCode)
	}
	if !strings.HasPrefix(contentType, "application/json") {
		log.Error("unexpected agent answer", "status", resp.StatusCode, "content_type", contentType)
		return assistantError(c, http.StatusBadGateway, "assistant_unreachable", "the AI agent answered unexpectedly")
	}
	return c.Blob(resp.StatusCode, "application/json", answer)
}

// regexpAssistantID is an id of the agent: a conversation or a turn. Anything
// else is refused before a path is built, so that no id can reach another route
// of the agent.
var regexpAssistantID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// conversationPath is the path of a conversation under the backend, "" when an
// id is not one.
func conversationPath(id string, rest ...string) string {
	if !regexpAssistantID.MatchString(id) {
		return ""
	}
	return strings.Join(append([]string{"/conversations", id}, rest...), "/")
}

// GetAssistantConversations handles GET /assistant/conversations.
func (a *Api) GetAssistantConversations(c echo.Context) error {
	return a.assistantRelay(c, http.MethodGet, "/conversations", nil, 0, false)
}

// PostAssistantConversation handles POST /assistant/conversations.
func (a *Api) PostAssistantConversation(c echo.Context) error {
	return a.assistantRelay(c, http.MethodPost, "/conversations", nil, 0, false)
}

// GetAssistantConversation handles GET /assistant/conversations/{conversation_id}.
func (a *Api) GetAssistantConversation(c echo.Context, conversationId string) error {
	return a.assistantRelay(c, http.MethodGet, conversationPath(conversationId), nil, 0, false)
}

// PatchAssistantConversation handles PATCH /assistant/conversations/{conversation_id}.
func (a *Api) PatchAssistantConversation(c echo.Context, conversationId string) error {
	return a.assistantRelay(c, http.MethodPatch, conversationPath(conversationId), nil, assistantBodyLimit, false)
}

// DeleteAssistantConversation handles DELETE /assistant/conversations/{conversation_id}.
func (a *Api) DeleteAssistantConversation(c echo.Context, conversationId string) error {
	return a.assistantRelay(c, http.MethodDelete, conversationPath(conversationId), nil, 0, false)
}

// GetAssistantMessages handles GET /assistant/conversations/{conversation_id}/messages.
func (a *Api) GetAssistantMessages(c echo.Context, conversationId string, params server.GetAssistantMessagesParams) error {
	query := url.Values{}
	if params.Limit != nil {
		query.Set("limit", strconv.Itoa(*params.Limit))
	}
	if params.Before != nil && *params.Before != "" {
		query.Set("before", *params.Before)
	}
	return a.assistantRelay(c, http.MethodGet, conversationPath(conversationId, "messages"), query, 0, false)
}

// PostAssistantRun handles POST /assistant/conversations/{conversation_id}/runs:
// an AG-UI run, its input relayed as it is and its events streamed back.
func (a *Api) PostAssistantRun(c echo.Context, conversationId string) error {
	return a.assistantRelay(c, http.MethodPost, conversationPath(conversationId, "runs"), nil, assistantRunBodyLimit, true)
}

// GetAssistantCapabilities handles GET /assistant/capabilities.
func (a *Api) GetAssistantCapabilities(c echo.Context) error {
	return a.assistantRelay(c, http.MethodGet, "/capabilities", nil, 0, false)
}

// GetAssistantTools handles GET /assistant/tools: the tools of the MCP the
// model may call for the caller, as the agent lists them.
func (a *Api) GetAssistantTools(c echo.Context) error {
	return a.assistantRelay(c, http.MethodGet, "/tools", nil, 0, false)
}

// PutAssistantFeedback handles PUT /assistant/conversations/{conversation_id}/turns/{turn_id}/feedback.
func (a *Api) PutAssistantFeedback(c echo.Context, conversationId string, turnId string) error {
	if !regexpAssistantID.MatchString(turnId) {
		return assistantError(c, http.StatusBadRequest, "invalid_request", "invalid conversation or turn id")
	}
	return a.assistantRelay(c, http.MethodPut, conversationPath(conversationId, "turns", turnId, "feedback"), nil, assistantBodyLimit, false)
}

// DeleteAssistantFeedback handles DELETE /assistant/conversations/{conversation_id}/turns/{turn_id}/feedback.
func (a *Api) DeleteAssistantFeedback(c echo.Context, conversationId string, turnId string) error {
	if !regexpAssistantID.MatchString(turnId) {
		return assistantError(c, http.StatusBadRequest, "invalid_request", "invalid conversation or turn id")
	}
	return a.assistantRelay(c, http.MethodDelete, conversationPath(conversationId, "turns", turnId, "feedback"), nil, 0, false)
}
