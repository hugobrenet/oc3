package serverhandlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/shaj13/go-guardian/v2/auth"

	"github.com/opensvc/oc3/xauth"
)

// runImpersonate runs the middleware, without database, for a user signed in
// as user 7 with the given mode and groups, and a request carrying header.
func runImpersonate(t *testing.T, mode string, groups []string, header string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if header != "" {
		req.Header.Set(ImpersonateHeader, header)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	ext := make(auth.Extensions)
	ext.Set(xauth.XUserID, "7")
	c.Set("user", auth.NewUserInfo("admin@example.com", "7", groups, ext))
	c.Set("groups", groups)
	if mode != "" {
		c.Set(XAuthMode, mode)
	}
	called := false
	err := ImpersonateMiddleware(nil)(func(c echo.Context) error {
		called = true
		if IsImpersonating(c) {
			t.Error("the request should not be impersonated")
		}
		return nil
	})(c)
	if err != nil {
		t.Fatal(err)
	}
	return rec, called
}

func TestImpersonateMiddleware(t *testing.T) {
	privileged := []string{"Manager"}
	for _, c := range []struct {
		name   string
		mode   string
		groups []string
		header string
		code   int
	}{
		{"no header", AuthModeUser, nil, "", 0},
		{"public route", "", nil, "8", 0},
		{"own id", AuthModeUser, privileged, "7", 0},
		{"no privilege", AuthModeUser, []string{"Impersonate", "UserManager"}, "8", http.StatusForbidden},
		{"node", AuthModeNode, privileged, "8", http.StatusForbidden},
		{"not an id", AuthModeUser, privileged, "someone", http.StatusBadRequest},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec, called := runImpersonate(t, c.mode, c.groups, c.header)
			if c.code == 0 {
				if !called {
					t.Error("the handler was not called")
				}
				return
			}
			if called {
				t.Error("the handler should not be called")
			}
			if rec.Code != c.code {
				t.Errorf("got %d, want %d", rec.Code, c.code)
			}
			if rec.Header().Get(ImpersonationRefusedHeader) == "" {
				t.Errorf("missing %s", ImpersonationRefusedHeader)
			}
		})
	}
}
