package feederhandlers

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/labstack/echo/v4"

	"github.com/opensvc/oc3/cachekeys"
)

func postArray(t *testing.T, a *Api, body []byte, gz bool) *httptest.ResponseRecorder {
	t.Helper()
	if gz {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		_, _ = w.Write(body)
		_ = w.Close()
		body = buf.Bytes()
	}
	req := httptest.NewRequest(http.MethodPost, "/api/array", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if gz {
		req.Header.Set(echo.HeaderContentEncoding, "gzip")
	}
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set(XNodeID, "node-1")
	if err := a.PostArray(c); err != nil {
		t.Fatal(err)
	}
	return rec
}

func newArrayAPI(t *testing.T, maxSize int64) (*Api, *redis.Client) {
	t.Helper()
	r := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = r.Close() })
	return &Api{Redis: r, ArrayMaxSize: maxSize}, r
}

// An array report, compressed or not, is stored compressed under
// <type>@<name>, its node added to the proxies of the array, and queued
// once.
func TestPostArray(t *testing.T) {
	ctx := context.Background()
	body := []byte(`{"name":"pure1","type":"pure","data":{"arrays":[{"name":"fa1"}],"volumes":[],"ports":[],"pods":[]}}`)
	for _, gz := range []bool{false, true} {
		a, r := newArrayAPI(t, 1<<20)
		if rec := postArray(t, a, body, gz); rec.Code != http.StatusAccepted {
			t.Fatalf("gzip %v: got %d %s", gz, rec.Code, rec.Body)
		}
		_ = postArray(t, a, body, gz)

		b, err := r.HGet(ctx, cachekeys.FeedArrayH, "pure@pure1").Bytes()
		if err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("stored compressed: %s", err)
		}
		plain, _ := io.ReadAll(zr)
		var got arrayReport
		if err := json.Unmarshal(plain, &got); err != nil {
			t.Fatal(err)
		}
		if got.Name != "pure1" || string(got.Data["arrays"]) != `[{"name":"fa1"}]` {
			t.Errorf("stored %s", plain)
		}
		if l, _ := r.SMembers(ctx, cachekeys.FeedArrayProxyS+"pure@pure1").Result(); len(l) != 1 || l[0] != "node-1" {
			t.Errorf("proxies %v", l)
		}
		if n, _ := r.LLen(ctx, cachekeys.FeedArrayQ).Result(); n != 1 {
			t.Errorf("queued %d times", n)
		}
	}
}

// A report the worker could not parse is refused, and nothing is stored.
func TestPostArrayRefused(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		body string
		code int
	}{
		"no name":         {`{"name":"","type":"pure","data":{"arrays":[],"volumes":[],"ports":[]}}`, http.StatusBadRequest},
		"name with @":     {`{"name":"a@b","type":"pure","data":{"arrays":[],"volumes":[],"ports":[]}}`, http.StatusBadRequest},
		"unknown type":    {`{"name":"a","type":"eva","data":{}}`, http.StatusBadRequest},
		"type not parsed": {`{"name":"a","type":"symmetrix","data":{}}`, http.StatusBadRequest},
		"missing section": {`{"name":"a","type":"pure","data":{"arrays":[],"volumes":[]}}`, http.StatusBadRequest},
		"invalid json":    {`{"name":`, http.StatusBadRequest},
		"larger than max": {`{"name":"a","type":"pure","data":{"arrays":[],"volumes":[],"ports":[],"pods":"` + strings.Repeat("x", 300) + `"}}`, http.StatusRequestEntityTooLarge},
	}
	for name, tc := range cases {
		a, r := newArrayAPI(t, 256)
		rec := postArray(t, a, []byte(tc.body), true)
		if rec.Code != tc.code {
			t.Errorf("%s: got %d %s, want %d", name, rec.Code, rec.Body, tc.code)
		}
		if n, _ := r.HLen(ctx, cachekeys.FeedArrayH).Result(); n != 0 {
			t.Errorf("%s: stored", name)
		}
	}
}
