package serverhandlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/labstack/echo/v4"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/migrate"
	"github.com/opensvc/oc3/server"
)

// aiContext is an echo context of a request by a user of groups.
func aiContext(method, target, body string, groups ...string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set(XAuthMode, AuthModeUser)
	c.Set(XUserEmail, "user@example.com")
	c.Set("groups", groups)
	return c, rec
}

// TestAIListETag checks a list answers its ETag, a 304 to a caller giving it
// back, and a 200 to a caller giving another.
func TestAIListETag(t *testing.T) {
	list := map[string]any{"data": []string{"naming"}}
	c, rec := aiContext(http.MethodGet, "/ai/resources", "")
	if err := writeAIList(c, list); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("first read: %d, %v", rec.Code, err)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" || rec.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("headers %v", rec.Header())
	}
	c, rec = aiContext(http.MethodGet, "/ai/resources", "")
	c.Request().Header.Set("If-None-Match", `"other", `+etag)
	if err := writeAIList(c, list); err != nil || rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("same list: %d %q, %v", rec.Code, rec.Body.String(), err)
	}
	c, rec = aiContext(http.MethodGet, "/ai/resources", "")
	c.Request().Header.Set("If-None-Match", etag)
	if err := writeAIList(c, map[string]any{"data": []string{"naming", "runbook"}}); err != nil || rec.Code != http.StatusOK {
		t.Errorf("changed list: %d, %v", rec.Code, err)
	}
}

// TestAIBodyLimit checks a write larger than aiBodyLimit is refused before it
// is decoded.
func TestAIBodyLimit(t *testing.T) {
	body := `{"name":"big","body":"` + strings.Repeat("x", aiBodyLimit) + `"}`
	c, rec := aiContext(http.MethodPost, "/ai/resources", body, "AIManager")
	var in server.PostAIResourcesJSONRequestBody
	if err := bindAI(c, &in); err == nil || rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("answer %d, %v", rec.Code, err)
	}
}

// TestAIPublicationsToManagers checks the groups an object is published to
// are given to an AIManager only.
func TestAIPublicationsToManagers(t *testing.T) {
	item := cdb.AIContent{Name: "runbook", Backends: []string{"oc3"}, Publications: []int{4}}
	c, _ := aiContext(http.MethodGet, "/ai/resources", "", "Ops")
	if meta := aiResourceMeta(c, item); meta.Publications != nil {
		t.Errorf("a reader sees the publications %v", *meta.Publications)
	}
	c, _ = aiContext(http.MethodGet, "/ai/resources", "", "AIManager")
	if meta := aiSkillMeta(c, item); meta.Publications == nil || !slices.Equal(*meta.Publications, []int{4}) {
		t.Errorf("an AIManager sees the publications %v", meta.Publications)
	}
	item.Publications = nil
	if meta := aiResourceMeta(c, item); meta.Publications == nil || len(*meta.Publications) != 0 {
		t.Errorf("an object published to everybody: %v", meta.Publications)
	}
}

// initMetrics registers the metrics of the database once for the tests.
var initMetrics sync.Once

// noEvents publishes nothing.
type noEvents struct{}

func (noEvents) EventPublish(string, map[string]any) error { return nil }

// aiTestAPI is an Api on a new database of the MariaDB server
// OC3_TEST_MYSQL_DSN names, holding the tables of the AI content and those
// they need, dropped at the end of the test.
func aiTestAPI(t *testing.T) (*Api, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("OC3_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("OC3_TEST_MYSQL_DSN is not set, as root:pass@tcp(127.0.0.1:3306)/: no MariaDB to test against")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ParseTime = true
	cfg.DBName = ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("oc3_test_ai_%d", time.Now().UnixNano())
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
	statements := []string{
		"CREATE TABLE auth_group (id INT AUTO_INCREMENT PRIMARY KEY, role VARCHAR(255) NOT NULL, description LONGTEXT, privilege CHAR(1) DEFAULT 'F')",
		"CREATE TABLE log (id INT AUTO_INCREMENT PRIMARY KEY, log_action VARCHAR(100) NOT NULL, log_user VARCHAR(100) NOT NULL," +
			" log_impersonator VARCHAR(100), log_origin VARCHAR(16), log_request_id VARCHAR(64), log_fmt VARCHAR(200), log_dict TEXT, log_date TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP," +
			" svc_id CHAR(36) DEFAULT '', log_level ENUM('debug','info','warning','error','critical') DEFAULT 'info', node_id CHAR(36) DEFAULT '')",
		"INSERT INTO auth_group (role, privilege) VALUES ('Ops', 'F'), ('Other', 'F')",
	}
	all, err := migrate.All()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.Name == "ai_content" {
			statements = append(statements, m.Statements...)
		}
	}
	for _, s := range statements {
		if _, err := pool.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	initMetrics.Do(cdb.InitMetrics)
	odb := cdb.New(pool)
	odb.CreateSession(noEvents{})
	return &Api{ODB: odb, SyncTimeout: 10 * time.Second}, pool
}

// aiNames are the names of a list answer, and whether any gives its
// publications.
func aiNames(t *testing.T, rec *httptest.ResponseRecorder) ([]string, bool) {
	t.Helper()
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("%d %s: %v", rec.Code, rec.Body.String(), err)
	}
	var names []string
	publications := false
	for _, item := range list.Data {
		names = append(names, item["name"].(string))
		_, has := item["publications"]
		publications = publications || has
	}
	return names, publications
}

// TestAIContentAccess writes resources and skills as an AIManager and reads
// them as users of other groups, on a MariaDB: an object published to a group
// is listed and read by its members only, a 404 for the others as for an
// object that does not exist; a list answers a 304 to a caller having it; a
// backend holds at most aiMaxPerBackend objects, a replace adding one
// included; a deletion takes the publications with the object; each write is
// logged.
func TestAIContentAccess(t *testing.T) {
	a, pool := aiTestAPI(t)
	var ops int
	if err := pool.QueryRow("SELECT id FROM auth_group WHERE role = 'Ops'").Scan(&ops); err != nil {
		t.Fatal(err)
	}
	oc3, om3 := server.Oc3, server.Om3
	call := func(groups []string, method, target, body string, h func(echo.Context) error) *httptest.ResponseRecorder {
		t.Helper()
		c, rec := aiContext(method, target, body, groups...)
		_ = h(c)
		return rec
	}
	manager := []string{"AIManager"}
	expect := func(rec *httptest.ResponseRecorder, code int, what string) {
		t.Helper()
		if rec.Code != code {
			t.Fatalf("%s: %d %s, want %d", what, rec.Code, rec.Body.String(), code)
		}
	}

	runbook := fmt.Sprintf(`{"name":"runbook","description":"The runbook of the checks.","backends":["oc3"],"publications":[%d],"body":"# Runbook\n"}`, ops)
	expect(call(manager, http.MethodPost, "/ai/resources", runbook, a.PostAIResources), http.StatusCreated, "create runbook")
	expect(call(manager, http.MethodPost, "/ai/resources", runbook, a.PostAIResources), http.StatusConflict, "create runbook again")
	naming := `{"name":"naming","description":"The naming rules.","backends":["oc3","om3"],"body":"# Naming\n"}`
	expect(call(manager, http.MethodPost, "/ai/resources", naming, a.PostAIResources), http.StatusCreated, "create naming")
	expect(call([]string{"Ops"}, http.MethodPost, "/ai/resources", naming, a.PostAIResources), http.StatusForbidden, "create as a reader")
	skill := fmt.Sprintf(`{"name":"maintenance","backends":["oc3"],"publications":[%d],"body":%q}`, ops, skillMD)
	expect(call(manager, http.MethodPost, "/ai/skills", skill, a.PostAISkills), http.StatusCreated, "create the skill")

	list := func(groups []string, backend *server.AIBackend, etag string) *httptest.ResponseRecorder {
		t.Helper()
		c, rec := aiContext(http.MethodGet, "/ai/resources", "", groups...)
		if etag != "" {
			c.Request().Header.Set("If-None-Match", etag)
		}
		_ = a.GetAIResources(c, server.GetAIResourcesParams{Backend: backend})
		return rec
	}
	read := func(groups []string, name string, backend *server.AIBackend) *httptest.ResponseRecorder {
		return call(groups, http.MethodGet, "/ai/resources/"+name, "", func(c echo.Context) error {
			return a.GetAIResource(c, name, server.GetAIResourceParams{Backend: backend})
		})
	}

	// A member of Ops reads the runbook published to Ops, the others do not.
	rec := list([]string{"Ops"}, &oc3, "")
	if names, publications := aiNames(t, rec); !slices.Equal(names, []string{"naming", "runbook"}) || publications {
		t.Errorf("Ops lists %v, publications %v", names, publications)
	}
	opsETag := rec.Header().Get("ETag")
	rec = list([]string{"Other"}, &oc3, "")
	if names, _ := aiNames(t, rec); !slices.Equal(names, []string{"naming"}) {
		t.Errorf("Other lists %v", names)
	}
	if rec.Header().Get("ETag") == opsETag {
		t.Errorf("Ops and Other have the same ETag")
	}
	if names, publications := aiNames(t, list(manager, nil, "")); !slices.Equal(names, []string{"naming", "runbook"}) || !publications {
		t.Errorf("the AIManager lists %v, publications %v", names, publications)
	}
	if names, _ := aiNames(t, list([]string{"Ops"}, &om3, "")); !slices.Equal(names, []string{"naming"}) {
		t.Errorf("Ops lists for om3 %v", names)
	}
	expect(read([]string{"Ops"}, "runbook", &oc3), http.StatusOK, "Ops reads the runbook")
	notPublished := read([]string{"Other"}, "runbook", &oc3)
	expect(notPublished, http.StatusNotFound, "Other reads the runbook")
	missing := read([]string{"Other"}, "missing", &oc3)
	expect(missing, http.StatusNotFound, "Other reads a missing resource")
	if strings.ReplaceAll(notPublished.Body.String(), "runbook", "missing") != missing.Body.String() {
		t.Errorf("an unpublished resource answers %s, a missing one %s", notPublished.Body.String(), missing.Body.String())
	}
	expect(read([]string{"Ops"}, "runbook", &om3), http.StatusNotFound, "Ops reads the runbook for om3")
	expect(call([]string{"Other"}, http.MethodGet, "/ai/skills/maintenance", "", func(c echo.Context) error {
		return a.GetAISkill(c, "maintenance", server.GetAISkillParams{Backend: &oc3})
	}), http.StatusNotFound, "Other reads the skill")

	// The same list answers a 304, a changed one a 200.
	expect(list([]string{"Ops"}, &oc3, opsETag), http.StatusNotModified, "Ops lists again")
	public := `{"description":"The runbook of the checks, for everybody.","backends":["oc3"],"publications":[],"body":"# Runbook\n"}`
	put := func(name, body string) *httptest.ResponseRecorder {
		return call(manager, http.MethodPut, "/ai/resources/"+name, body, func(c echo.Context) error { return a.PutAIResource(c, name) })
	}
	expect(put("runbook", public), http.StatusOK, "publish the runbook to everybody")
	expect(list([]string{"Ops"}, &oc3, opsETag), http.StatusOK, "Ops lists after a change")
	expect(read([]string{"Other"}, "runbook", &oc3), http.StatusOK, "Other reads the public runbook")

	// om3 holds naming and enough others to be full: naming is replaced, the
	// runbook cannot be added to it.
	for i := 1; i < aiMaxPerBackend; i++ {
		if _, err := pool.Exec("INSERT INTO ai_resources (name, title, description, mime_type, backends, body, size, digest, updated, updated_by)"+
			" VALUES (?, '', 'd', 'text/markdown', 'om3', 'x', 1, 'sha256:x', NOW(), '')", fmt.Sprintf("filler-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	expect(put("naming", naming), http.StatusOK, "replace naming in a full backend")
	expect(put("runbook", strings.Replace(public, `["oc3"]`, `["oc3","om3"]`, 1)), http.StatusBadRequest, "add the runbook to a full backend")

	// A deletion takes the publications with the object.
	expect(call(manager, http.MethodDelete, "/ai/skills/maintenance", "", func(c echo.Context) error { return a.DeleteAISkill(c, "maintenance") }),
		http.StatusNoContent, "delete the skill")
	expect(call(manager, http.MethodDelete, "/ai/skills/maintenance", "", func(c echo.Context) error { return a.DeleteAISkill(c, "maintenance") }),
		http.StatusNotFound, "delete the skill again")
	var publications, logged int
	if err := pool.QueryRow("SELECT COUNT(*) FROM ai_skills_publications").Scan(&publications); err != nil || publications != 0 {
		t.Errorf("publications of the deleted skill: %d, %v", publications, err)
	}
	// 3 creations, 2 replaces and a deletion; the refused writes are not logged.
	if err := pool.QueryRow("SELECT COUNT(*) FROM log WHERE log_action LIKE 'ai.%'").Scan(&logged); err != nil || logged != 6 {
		t.Errorf("logged %d writes, %v", logged, err)
	}
}
