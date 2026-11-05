package serverhandlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/server"
	"github.com/opensvc/oc3/util/echolog"
	"github.com/opensvc/oc3/util/logkey"
)

// The resources and the skills of the AI assistant. An AIManager writes them;
// the MCP servers read them for the agent, as the user, through the routes
// GET /ai/resources and /ai/skills: the reads apply the publications of the
// objects to the groups of the caller, the writes need the privilege.

// IsAIManager reports whether the caller may write the resources and the
// skills of the AI assistant.
func IsAIManager(c echo.Context) bool {
	return IsManager(c) || HasGroup(c, "AIManager")
}

// aiFilter is the filter of a read by the caller: of backend when set, every
// object for an AIManager, the objects published to one of their groups or to
// none for the others.
func aiFilter(c echo.Context, backend *server.AIBackend, name string, withBody bool) cdb.AIContentFilter {
	f := cdb.AIContentFilter{Name: name, Groups: UserGroupsFromContext(c), All: IsAIManager(c), WithBody: withBody}
	if backend != nil {
		f.Backend = string(*backend)
	}
	return f
}

// aiBackendArg checks the backend asked by a read.
func aiBackendArg(backend *server.AIBackend) error {
	if backend != nil && *backend != server.Om3 && *backend != server.Oc3 {
		return fmt.Errorf("backend: %q is not om3 nor oc3", *backend)
	}
	return nil
}

// writeAIList answers a list with its ETag, a hash of the answer, and a 304
// when the caller has it already. The answer is private to the caller, whose
// groups decide what it holds.
func writeAIList(c echo.Context, list any) error {
	data, err := json.Marshal(list)
	if err != nil {
		return JSONProblemf(c, http.StatusInternalServerError, "cannot encode the list")
	}
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	header := c.Response().Header()
	header.Set("ETag", etag)
	header.Set("Cache-Control", "private, no-cache")
	for _, tag := range strings.Split(c.Request().Header.Get("If-None-Match"), ",") {
		if strings.TrimSpace(tag) == etag {
			return c.NoContent(http.StatusNotModified)
		}
	}
	return c.JSONBlob(http.StatusOK, data)
}

func aiBackends(backends []string) []server.AIBackend {
	out := make([]server.AIBackend, len(backends))
	for i, b := range backends {
		out[i] = server.AIBackend(b)
	}
	return out
}

func aiResourceMeta(c cdb.AIContent) server.AIResourceMeta {
	return server.AIResourceMeta{Name: c.Name, Title: c.Title, Description: c.Description, MimeType: c.MimeType,
		Backends: aiBackends(c.Backends), Publications: c.Publications, Size: c.Size, Digest: c.Digest, Updated: c.Updated.UTC()}
}

func aiSkillMeta(c cdb.AIContent) server.AISkillMeta {
	frontmatter := map[string]any{}
	_ = json.Unmarshal([]byte(c.Frontmatter), &frontmatter)
	return server.AISkillMeta{Name: c.Name, Backends: aiBackends(c.Backends), Publications: c.Publications,
		Frontmatter: frontmatter, Size: c.Size, Digest: c.Digest, Updated: c.Updated.UTC()}
}

// GetAIResources handles GET /ai/resources.
func (a *Api) GetAIResources(c echo.Context, params server.GetAIResourcesParams) error {
	items, err := a.readAI(c, "GetAIResources", cdb.AIResources, params.Backend, "", false)
	if err != nil {
		return err
	}
	list := server.AIResourceListResponse{Data: []server.AIResourceMeta{}}
	for _, item := range items {
		list.Data = append(list.Data, aiResourceMeta(item))
	}
	return writeAIList(c, list)
}

// GetAISkills handles GET /ai/skills.
func (a *Api) GetAISkills(c echo.Context, params server.GetAISkillsParams) error {
	items, err := a.readAI(c, "GetAISkills", cdb.AISkills, params.Backend, "", false)
	if err != nil {
		return err
	}
	list := server.AISkillListResponse{Data: []server.AISkillMeta{}}
	for _, item := range items {
		list.Data = append(list.Data, aiSkillMeta(item))
	}
	return writeAIList(c, list)
}

// GetAIResource handles GET /ai/resources/{name}.
func (a *Api) GetAIResource(c echo.Context, name string, params server.GetAIResourceParams) error {
	items, err := a.readAI(c, "GetAIResource", cdb.AIResources, params.Backend, name, true)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return JSONProblemf(c, http.StatusNotFound, "no resource %s", name)
	}
	meta := aiResourceMeta(items[0])
	return c.JSON(http.StatusOK, server.AIResource{Name: meta.Name, Title: meta.Title, Description: meta.Description,
		MimeType: meta.MimeType, Backends: meta.Backends, Publications: meta.Publications, Size: meta.Size,
		Digest: meta.Digest, Updated: meta.Updated, Body: items[0].Body})
}

// GetAISkill handles GET /ai/skills/{name}.
func (a *Api) GetAISkill(c echo.Context, name string, params server.GetAISkillParams) error {
	items, err := a.readAI(c, "GetAISkill", cdb.AISkills, params.Backend, name, true)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return JSONProblemf(c, http.StatusNotFound, "no skill %s", name)
	}
	meta := aiSkillMeta(items[0])
	return c.JSON(http.StatusOK, server.AISkill{Name: meta.Name, Backends: meta.Backends, Publications: meta.Publications,
		Frontmatter: meta.Frontmatter, Size: meta.Size, Digest: meta.Digest, Updated: meta.Updated, Body: items[0].Body})
}

// readAI reads the objects of kind the caller may read; the error is the
// answer already written.
func (a *Api) readAI(c echo.Context, operation string, kind cdb.AIContentKind, backend *server.AIBackend, name string, withBody bool) ([]cdb.AIContent, error) {
	log := echolog.GetLogHandler(c, operation)
	if !IsAuthByUser(c) {
		return nil, denyRequest(c, http.StatusUnauthorized, "user authentication required")
	}
	if err := aiBackendArg(backend); err != nil {
		return nil, JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	items, err := a.ODB.AIContents(ctx, kind, aiFilter(c, backend, name, withBody))
	if err != nil {
		log.Error("cannot read", logkey.Error, err)
		return nil, JSONProblemf(c, http.StatusInternalServerError, "cannot read the AI content")
	}
	return items, nil
}

// PostAIResources handles POST /ai/resources.
func (a *Api) PostAIResources(c echo.Context) error {
	var body server.PostAIResourcesJSONRequestBody
	if err := c.Bind(&body); err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	name := ""
	if body.Name != nil {
		name = *body.Name
	}
	return a.writeAI(c, "PostAIResources", cdb.AIResources, "resource", name, true, func() (cdb.AIContent, error) {
		return checkAIResource(body, name)
	})
}

// PostAISkills handles POST /ai/skills.
func (a *Api) PostAISkills(c echo.Context) error {
	var body server.PostAISkillsJSONRequestBody
	if err := c.Bind(&body); err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	name := ""
	if body.Name != nil {
		name = *body.Name
	}
	return a.writeAI(c, "PostAISkills", cdb.AISkills, "skill", name, true, func() (cdb.AIContent, error) {
		return checkAISkill(body, name)
	})
}

// PutAIResource handles PUT /ai/resources/{name}.
func (a *Api) PutAIResource(c echo.Context, name string) error {
	var body server.PutAIResourceJSONRequestBody
	if err := c.Bind(&body); err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	return a.writeAI(c, "PutAIResource", cdb.AIResources, "resource", name, false, func() (cdb.AIContent, error) {
		return checkAIResource(body, name)
	})
}

// PutAISkill handles PUT /ai/skills/{name}.
func (a *Api) PutAISkill(c echo.Context, name string) error {
	var body server.PutAISkillJSONRequestBody
	if err := c.Bind(&body); err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	return a.writeAI(c, "PutAISkill", cdb.AISkills, "skill", name, false, func() (cdb.AIContent, error) {
		return checkAISkill(body, name)
	})
}

// writeAI creates (create) or replaces an object of kind named name, checked
// by check, and answers its metadata.
func (a *Api) writeAI(c echo.Context, operation string, kind cdb.AIContentKind, what, name string, create bool, check func() (cdb.AIContent, error)) error {
	log := echolog.GetLogHandler(c, operation)
	if !IsAuthByUser(c) {
		return denyRequest(c, http.StatusUnauthorized, "user authentication required")
	}
	if !IsAIManager(c) {
		return denyRequest(c, http.StatusForbidden, "AIManager privilege required")
	}
	item, err := check()
	if err != nil {
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	odb := a.ODB
	if missing, err := odb.MissingGroups(ctx, item.Publications); err != nil {
		log.Error("cannot check the groups", logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot check the groups")
	} else if len(missing) > 0 {
		return JSONProblemf(c, http.StatusBadRequest, "publications: no group %v", missing)
	}
	item.Updated = time.Now().UTC().Truncate(time.Second)
	item.UpdatedBy, _ = c.Get(XUserEmail).(string)
	action := "change"
	if create {
		action = "add"
		if _, taken, err := odb.AIContentID(ctx, kind, name); err != nil {
			log.Error("cannot check the name", logkey.Error, err)
			return JSONProblemf(c, http.StatusInternalServerError, "cannot check the name")
		} else if taken {
			return JSONProblemf(c, http.StatusConflict, "a %s named %s already exists", what, name)
		}
		for _, backend := range item.Backends {
			if n, err := odb.AIContentCount(ctx, kind, backend); err != nil {
				log.Error("cannot count", logkey.Error, err)
				return JSONProblemf(c, http.StatusInternalServerError, "cannot count the %ss", what)
			} else if n >= aiMaxPerBackend {
				return JSONProblemf(c, http.StatusBadRequest, "backends: %s has %d %ss already, the most it may have", backend, n, what)
			}
		}
		if err := odb.InsertAIContent(ctx, kind, item); err != nil {
			log.Error("cannot create", logkey.Error, err)
			return JSONProblemf(c, http.StatusInternalServerError, "cannot create the %s", what)
		}
	} else if found, err := odb.UpdateAIContent(ctx, kind, name, item); err != nil {
		log.Error("cannot update", logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot update the %s", what)
	} else if !found {
		return JSONProblemf(c, http.StatusNotFound, "no %s %s", what, name)
	}
	a.logAI(ctx, c, log, what, action, name)
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	if kind == cdb.AISkills {
		return c.JSON(status, aiSkillMeta(item))
	}
	return c.JSON(status, aiResourceMeta(item))
}

// DeleteAIResource handles DELETE /ai/resources/{name}.
func (a *Api) DeleteAIResource(c echo.Context, name string) error {
	return a.deleteAI(c, "DeleteAIResource", cdb.AIResources, "resource", name)
}

// DeleteAISkill handles DELETE /ai/skills/{name}.
func (a *Api) DeleteAISkill(c echo.Context, name string) error {
	return a.deleteAI(c, "DeleteAISkill", cdb.AISkills, "skill", name)
}

func (a *Api) deleteAI(c echo.Context, operation string, kind cdb.AIContentKind, what, name string) error {
	log := echolog.GetLogHandler(c, operation)
	if !IsAuthByUser(c) {
		return denyRequest(c, http.StatusUnauthorized, "user authentication required")
	}
	if !IsAIManager(c) {
		return denyRequest(c, http.StatusForbidden, "AIManager privilege required")
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), a.SyncTimeout)
	defer cancel()
	if found, err := a.ODB.DeleteAIContent(ctx, kind, name); err != nil {
		log.Error("cannot delete", logkey.Error, err)
		return JSONProblemf(c, http.StatusInternalServerError, "cannot delete the %s", what)
	} else if !found {
		return JSONProblemf(c, http.StatusNotFound, "no %s %s", what, name)
	}
	a.logAI(ctx, c, log, what, "delete", name)
	return c.NoContent(http.StatusNoContent)
}

// logAI writes a write in the collector log, as the other writes are.
func (a *Api) logAI(ctx context.Context, c echo.Context, log interface{ Error(string, ...any) }, what, action, name string) {
	userEmail, _ := c.Get(XUserEmail).(string)
	verb := map[string]string{"add": "added", "change": "changed", "delete": "deleted"}[action]
	if err := a.ODB.Log(ctx, cdb.LogEntry{
		Action: "ai." + what + "." + action,
		User:   userEmail,
		Fmt:    "AI " + what + " %(name)s " + verb,
		Dict:   map[string]any{"name": name},
		Level:  "info",
	}); err != nil {
		log.Error("cannot write audit log", logkey.Error, err)
	}
	if err := a.ODB.Session.NotifyChanges(ctx); err != nil {
		log.Error("cannot notify changes", logkey.Error, err)
	}
}
