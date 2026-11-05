package feederhandlers

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/opensvc/oc3/cachekeys"
	"github.com/opensvc/oc3/feeder"
	"github.com/opensvc/oc3/util/logkey"
)

type (
	// arrayReport is the posted array configuration, its sections kept as
	// they were sent: the worker decodes each with the parser of the type.
	arrayReport struct {
		Name string                     `json:"name"`
		Type feeder.ArrayType           `json:"type"`
		Data map[string]json.RawMessage `json:"data"`
	}
)

// PostArray stores the posted array configuration, compressed, in
// FeedArrayH <type>@<name>, adds the node to the proxies of the array, and
// queues its parsing.
//
// An array is keyed by its type and name, not by the node reporting it: the
// nodes of a cluster all push the arrays the cluster declares, and the
// latest report is the one to parse, whichever node sent it. Each is a proxy
// of the array.
func (a *Api) PostArray(c echo.Context) error {
	nodeID, log := getNodeIDAndLogger(c, "PostArray")
	if nodeID == "" {
		return JSONNodeAuthProblem(c)
	}

	// The body may come compressed, and is decompressed here rather than
	// by a middleware: the size is limited once decompressed, so that a
	// small body can not expand without bound.
	var r io.Reader = c.Request().Body
	if strings.EqualFold(c.Request().Header.Get(echo.HeaderContentEncoding), "gzip") {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return JSONProblemf(c, http.StatusBadRequest, "invalid gzip body: %s", err)
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}
	limited := &io.LimitedReader{R: r, N: a.ArrayMaxSize + 1}
	var payload arrayReport
	if err := json.NewDecoder(limited).Decode(&payload); err != nil {
		if limited.N <= 0 {
			return JSONProblemf(c, http.StatusRequestEntityTooLarge, "the array configuration is larger than %d bytes", a.ArrayMaxSize)
		}
		return JSONProblem(c, http.StatusBadRequest, err.Error())
	}
	required, parsed := feeder.ArrayRequiredSections[payload.Type]
	switch {
	case payload.Name == "", strings.Contains(payload.Name, "@"):
		return JSONProblemf(c, http.StatusBadRequest, "invalid array name: %q", payload.Name)
	case !payload.Type.Valid():
		return JSONProblemf(c, http.StatusBadRequest, "unsupported array type: %q", payload.Type)
	case !parsed:
		return JSONProblemf(c, http.StatusBadRequest, "the collector does not parse %s arrays yet", payload.Type)
	}
	for _, section := range required {
		if _, ok := payload.Data[section]; !ok {
			return JSONProblemf(c, http.StatusBadRequest, "missing section %s of the %s array %s", section, payload.Type, payload.Name)
		}
	}

	b, err := gzipJSON(payload)
	if err != nil {
		log.Warn("gzipJSON", logkey.Error, err)
		return JSONError(c)
	}
	ctx := c.Request().Context()
	idx := string(payload.Type) + "@" + payload.Name
	if err := a.Redis.HSet(ctx, cachekeys.FeedArrayH, idx, b).Err(); err != nil {
		log.Error("HSet FeedArrayH", logkey.Error, err)
		return JSONError(c)
	}
	if err := a.Redis.SAdd(ctx, cachekeys.FeedArrayProxyS+idx, nodeID).Err(); err != nil {
		log.Error("SAdd FeedArrayProxyS", logkey.Error, err)
		return JSONError(c)
	}
	if err := a.pushNotPending(ctx, log, cachekeys.FeedArrayPendingH, cachekeys.FeedArrayQ, idx); err != nil {
		log.Error("pushNotPending", logkey.Error, err)
		return JSONError(c)
	}
	return c.JSON(http.StatusAccepted, nil)
}

// gzipJSON returns v encoded in json and compressed: the configuration of an
// array stays in the cache until a worker parses it.
func gzipJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
