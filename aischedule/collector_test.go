package aischedule

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeCollector answers the skills it knows.
func fakeCollector(t *testing.T) Collector {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/ai/skills/morning-check":
			if r.URL.Query().Has("backend") {
				t.Error("the check counted as a read of the skill")
			}
			_, _ = w.Write([]byte(`{"name":"morning-check","backends":["oc3"]}`))
		case "/api/ai/skills/om3-only":
			_, _ = w.Write([]byte(`{"name":"om3-only","backends":["om3"]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return Collector{URL: srv.URL + "/api/", Client: srv.Client()}
}

func TestCheckSkill(t *testing.T) {
	c := fakeCollector(t)
	if err := c.CheckSkill(t.Context(), "tok", "morning-check"); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckSkill(t.Context(), "tok", "gone"); err == nil || !strings.Contains(err.Error(), "not published to the account") {
		t.Fatalf("unreadable skill: %v", err)
	}
	if err := c.CheckSkill(t.Context(), "tok", "om3-only"); err == nil || !strings.Contains(err.Error(), "oc3 backend") {
		t.Fatalf("om3 skill: %v", err)
	}
}
