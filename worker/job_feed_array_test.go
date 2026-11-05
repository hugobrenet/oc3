package worker

import (
	"bytes"
	"compress/gzip"
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"

	"github.com/opensvc/oc3/cachekeys"
)

// The job takes the report and the proxies out of the cache, and a job
// whose report a former one took has nothing to do.
func TestArrayGetData(t *testing.T) {
	ctx := context.Background()
	c := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = c.Close() })

	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write([]byte(`{"name":"pure1","type":"pure","data":{"arrays":[{"name":"fa1"}]}}`))
	_ = w.Close()
	c.HSet(ctx, cachekeys.FeedArrayH, "pure@pure1", buf.Bytes())
	c.SAdd(ctx, cachekeys.FeedArrayProxyS+"pure@pure1", "n1", "n2")

	d := newArray("pure@pure1")
	d.SetRedis(c)
	if err := d.getData(ctx); err != nil {
		t.Fatal(err)
	}
	if !d.found || d.data.Name != "pure1" || string(d.data.Data["arrays"]) != `[{"name":"fa1"}]` {
		t.Errorf("found %v, data %+v", d.found, d.data)
	}
	if len(d.proxies) != 2 {
		t.Errorf("proxies %v", d.proxies)
	}
	if n, _ := c.HLen(ctx, cachekeys.FeedArrayH).Result(); n != 0 {
		t.Error("report left in the cache")
	}
	if n, _ := c.Exists(ctx, cachekeys.FeedArrayProxyS+"pure@pure1").Result(); n != 0 {
		t.Error("proxies left in the cache")
	}

	d = newArray("pure@pure1")
	d.SetRedis(c)
	if err := d.getData(ctx); err != nil {
		t.Fatal(err)
	}
	if d.found {
		t.Error("a report found twice")
	}
}
