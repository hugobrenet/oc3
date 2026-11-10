package serverhandlers

import (
	"database/sql"
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

// TestOriginIsLogged writes a resource through OriginMiddleware, on a
// MariaDB: the audit entry of the write names the origin and the request ID
// the request declared, and an entry of a request declaring none, neither.
func TestOriginIsLogged(t *testing.T) {
	a, pool := aiTestAPI(t)
	write := func(name string, header map[string]string) {
		t.Helper()
		body := `{"name":"` + name + `","description":"A resource.","backends":["oc3"],"body":"# Text\n"}`
		c, rec := aiContext(http.MethodPost, "/ai/resources", body, "AIManager")
		for k, v := range header {
			c.Request().Header.Set(k, v)
		}
		if err := OriginMiddleware()(a.PostAIResources)(c); err != nil || rec.Code != http.StatusCreated {
			t.Fatalf("create %s: %v %d %s", name, err, rec.Code, rec.Body.String())
		}
	}
	const id = "0123456789abcdef0123456789abcdef"
	write("through-assistant", map[string]string{OriginHeader: "assistant", RequestIDHeader: id})
	write("by-hand", nil)
	rows, err := pool.Query("SELECT log_origin, log_request_id FROM log ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got [][2]sql.NullString
	for rows.Next() {
		var origin, requestID sql.NullString
		if err := rows.Scan(&origin, &requestID); err != nil {
			t.Fatal(err)
		}
		got = append(got, [2]sql.NullString{origin, requestID})
	}
	want := [][2]sql.NullString{
		{{String: "assistant", Valid: true}, {String: id, Valid: true}},
		{{}, {}},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("log entries %+v, want %+v", got, want)
	}
}
