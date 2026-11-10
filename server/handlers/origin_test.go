package serverhandlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/opensvc/oc3/cdb"
)

func TestOriginMiddleware(t *testing.T) {
	for _, c := range []struct {
		name      string
		origin    string
		requestID string
		want      cdb.Origin
	}{
		{"no header", "", "", cdb.Origin{}},
		{"request id alone", "", "abc", cdb.Origin{}},
		{"assistant", "assistant", "0123456789abcdef0123456789abcdef", cdb.Origin{Name: "assistant", RequestID: "0123456789abcdef0123456789abcdef"}},
		{"mcp without request id", "mcp", "", cdb.Origin{Name: "mcp"}},
		{"unknown origin", "browser", "abc", cdb.Origin{}},
		{"origin in another case", "Assistant", "abc", cdb.Origin{}},
		{"malformed request id", "assistant", "a b", cdb.Origin{Name: "assistant"}},
		{"request id too long", "assistant", string(make([]byte, 65)), cdb.Origin{Name: "assistant"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if c.origin != "" {
				req.Header.Set(OriginHeader, c.origin)
			}
			if c.requestID != "" {
				req.Header.Set(RequestIDHeader, c.requestID)
			}
			ctx := echo.New().NewContext(req, httptest.NewRecorder())
			var got cdb.Origin
			err := OriginMiddleware()(func(c echo.Context) error {
				got = cdb.OriginFrom(c.Request().Context())
				return nil
			})(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("origin %+v, want %+v", got, c.want)
			}
		})
	}
}
