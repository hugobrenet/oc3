package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

type (
	// runSpec is the activity of the agents.
	runSpec struct {
		FeederURL string
		Version   string
		Timeout   time.Duration
		Duration  time.Duration
		Ramp      time.Duration
		// StatusDelay is the least time between two daemon status posts of a
		// speaker, and the period it looks for changes at.
		StatusDelay  time.Duration
		PingInterval time.Duration
		// InfoInterval is the period of the resource info of each instance
		// (the info schedule of the objects, @60m by default).
		InfoInterval  time.Duration
		StatsInterval time.Duration
		// DailyInterval is the period of the inventory pushes (asset,
		// packages, checks, disks), daily on a real node.
		DailyInterval time.Duration
		// ChangesPerHour and ActionsPerDay are by service.
		ChangesPerHour float64
		ActionsPerDay  float64
		ErrorRatio     float64
		// InventorySpread is the time to send the first inventory pushes of
		// all the nodes, the daily interval when zero.
		InventorySpread time.Duration
		// NoInventory leaves out the node pushes, for a load of the status
		// path alone.
		NoInventory bool
	}

	sim struct {
		spec   fleetSpec
		run    runSpec
		fleet  *fleet
		client *http.Client
		rec    *recorder
	}

	speaker struct {
		*sim
		st       *clusterState
		rand     *rand.Rand
		dequeue  map[string]chan struct{}
		lastPost time.Time
		lastAt   time.Time
		resync   bool
		pending  []string
		infoNext [][]time.Time
		actions  sync.WaitGroup
	}
)

func newSim(spec fleetSpec, run runSpec, f *fleet, rec *recorder) *sim {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 0
	transport.MaxIdleConnsPerHost = f.nodeCount() + len(f.clusters)
	return &sim{
		spec:   spec,
		run:    run,
		fleet:  f,
		client: &http.Client{Transport: transport, Timeout: run.Timeout},
		rec:    rec,
	}
}

// probe checks the feeder answers at all, whatever its answer: an agent on
// the collector host reaches it on the loopback, the default of --feeder, but
// from another host or network namespace it has the address the agents use.
func (s *sim) probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.run.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.run.FeederURL+"/api/version", nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("feeder %s unreachable: %w (from outside the collector host or its network namespace, give --feeder the url the agents use)", s.run.FeederURL, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return nil
}

// do sends a request as node n, records it, and decodes the answer into out
// when given.
func (s *sim) do(ctx context.Context, n *node, method, endpoint string, body any, out any) (int, error) {
	var b []byte
	var reader io.Reader
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.run.FeederURL+"/api"+endpoint, reader)
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth(n.name, n.uuid)
	req.Header.Set("Content-Type", "application/json")
	begin := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			s.rec.add(method+" "+endpoint, 0, time.Since(begin), len(b), err)
		}
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	rb, err := io.ReadAll(resp.Body)
	elapsed := time.Since(begin)
	if err == nil && resp.StatusCode >= 300 {
		err = fmt.Errorf("%s %s: %d %.200s", method, endpoint, resp.StatusCode, bytes.TrimSpace(rb))
	}
	if ctx.Err() == nil {
		s.rec.add(method+" "+endpoint, resp.StatusCode, elapsed, len(b), err)
	}
	if err == nil && out != nil && len(rb) > 0 {
		_ = json.Unmarshal(rb, out)
	}
	return resp.StatusCode, err
}

// Run starts the clusters along the ramp and returns at the end of the run.
func (s *sim) Run(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, s.run.Duration)
	defer cancel()
	var wg sync.WaitGroup
	n := len(s.fleet.clusters)
	for i, c := range s.fleet.clusters {
		delay := time.Duration(0)
		if n > 1 {
			delay = s.run.Ramp * time.Duration(i) / time.Duration(n)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			s.startCluster(ctx, c)
		}()
	}
	wg.Wait()
}

func (s *sim) startCluster(ctx context.Context, c *cluster) {
	now := time.Now()
	sp := &speaker{
		sim:     s,
		st:      newClusterState(s.spec, c, s.run.Version, now),
		rand:    s.spec.newRand("speaker", c.idx),
		dequeue: map[string]chan struct{}{},
		resync:  true,
	}
	// The first resource info of each instance is spread over an interval, as
	// the agents of a fleet are not started together.
	for range c.services {
		row := make([]time.Time, len(c.nodes))
		for ni := range row {
			row[ni] = now.Add(time.Duration(sp.rand.Int64N(int64(s.run.InfoInterval))))
		}
		sp.infoNext = append(sp.infoNext, row)
	}
	var wg sync.WaitGroup
	for _, n := range c.nodes {
		ch := make(chan struct{}, 1)
		sp.dequeue[n.name] = ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.nodeLoop(ctx, n, ch)
		}()
	}
	sp.loop(ctx)
	sp.actions.Wait()
	wg.Wait()
}

func (sp *speaker) loop(ctx context.Context) {
	speakerNode := sp.st.c.nodes[0]
	ticker := time.NewTicker(sp.run.StatusDelay)
	defer ticker.Stop()
	pChange := sp.run.ChangesPerHour * sp.run.StatusDelay.Hours()
	pAction := sp.run.ActionsPerDay * sp.run.StatusDelay.Hours() / 24
	for {
		now := time.Now()
		for si := range sp.st.c.services {
			if sp.rand.Float64() < pChange {
				sp.pending = append(sp.pending, sp.st.event(si, now)...)
			}
			if sp.rand.Float64() < pAction {
				sp.startAction(ctx, si, now)
			}
		}
		switch {
		case sp.resync:
			sp.postStatus(ctx, speakerNode, now, sp.st.allChanges())
		case len(sp.pending) > 0:
			sp.postStatus(ctx, speakerNode, now, sp.pending)
		case now.Sub(sp.lastPost) >= sp.run.PingInterval:
			sp.postPing(ctx, speakerNode, now)
		}
		sp.sendResinfo(ctx, speakerNode, now)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (sp *speaker) postStatus(ctx context.Context, n *node, now time.Time, changes []string) {
	var resp pingStatusResponse
	_, err := sp.do(ctx, n, http.MethodPost, "/daemon/status", sp.st.daemonStatusBody(sp.lastAt, now, uniq(changes)), &resp)
	sp.lastPost = now
	if err != nil {
		// The agent keeps its changes and sends them again.
		return
	}
	sp.lastAt = now
	sp.resync = false
	sp.pending = nil
	sp.handleResponse(ctx, n, resp)
}

func (sp *speaker) postPing(ctx context.Context, n *node, now time.Time) {
	var resp pingStatusResponse
	code, err := sp.do(ctx, n, http.MethodPost, "/daemon/ping", sp.st.pingBody(), &resp)
	sp.lastPost = now
	if err != nil {
		return
	}
	if code == http.StatusNoContent {
		// The collector has lost the status: send it whole again.
		sp.resync = true
		return
	}
	sp.handleResponse(ctx, n, resp)
}

type pingStatusResponse struct {
	ObjectWithoutConfig  *[]string `json:"object_without_config"`
	NodeWithActionQueued *[]string `json:"node_with_action_queued"`
}

func (sp *speaker) handleResponse(ctx context.Context, n *node, resp pingStatusResponse) {
	if resp.NodeWithActionQueued != nil {
		for _, name := range *resp.NodeWithActionQueued {
			if ch, ok := sp.dequeue[name]; ok {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
	}
	if resp.ObjectWithoutConfig != nil {
		for _, path := range *resp.ObjectWithoutConfig {
			if si, ok := sp.st.serviceIndex(path); ok {
				_, _ = sp.do(ctx, n, http.MethodPost, "/object/config", sp.st.objectConfigBody(si), nil)
			}
		}
	}
}

func (sp *speaker) sendResinfo(ctx context.Context, n *node, now time.Time) {
	for si, row := range sp.infoNext {
		for ni, next := range row {
			if now.Before(next) {
				continue
			}
			row[ni] = next.Add(sp.run.InfoInterval)
			if ctx.Err() != nil {
				return
			}
			_, _ = sp.do(ctx, n, http.MethodPost, "/instance/resource_info", sp.st.resinfoBody(si, ni), nil)
		}
	}
}

// startAction runs an action of a service on a node it runs on: its begin
// now, its end a few seconds later, as the speaker relays them.
func (sp *speaker) startAction(ctx context.Context, si int, now time.Time) {
	c := sp.st.c
	s := c.services[si]
	ni := s.primary
	if s.topology == "flex" {
		ni = sp.rand.IntN(len(c.nodes))
	}
	action := []string{"start", "stop", "restart", "sync_all", "run", "push_resinfo"}[sp.rand.IntN(6)]
	status := "ok"
	if sp.rand.Float64() < sp.run.ErrorRatio {
		status = "err"
	}
	duration := time.Duration(2+sp.rand.IntN(40)) * time.Second
	pid := 1000 + sp.rand.IntN(1<<20)
	sessionID := sp.spec.uuid("session", c.idx, si, now.UnixNano())
	nLines := 1 + sp.rand.IntN(len(s.resources))
	speakerNode := c.nodes[0]
	sp.actions.Add(1)
	go func() {
		defer sp.actions.Done()
		var accepted struct {
			UUID string `json:"uuid"`
		}
		_, err := sp.do(ctx, speakerNode, http.MethodPost, "/instance/action", actionBody(sp.st, si, ni, action, sessionID, "", pid, now, time.Time{}, "", 0), &accepted)
		if err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(duration):
		}
		_, _ = sp.do(ctx, speakerNode, http.MethodPut, "/instance/action", actionBody(sp.st, si, ni, action, sessionID, accepted.UUID, pid, now, now.Add(duration), status, nLines), nil)
	}()
}

// nodeLoop is the activity of a node of its own: the stats, the daily
// inventory, and the action queue when the speaker hears it has some.
func (s *sim) nodeLoop(ctx context.Context, n *node, dequeue <-chan struct{}) {
	r := s.spec.newRand("node", n.name)
	now := time.Now()
	statsNext := now.Add(time.Duration(r.Int64N(int64(s.run.StatsInterval))))
	statsFrom := now.Add(-s.run.StatsInterval)
	type push struct {
		name string
		next time.Time
		send func()
	}
	var pushes []*push
	spread := s.run.InventorySpread
	if spread <= 0 {
		spread = s.run.DailyInterval
	}
	if !s.run.NoInventory {
		for _, p := range []struct {
			name string
			send func()
		}{
			{"asset", func() { _, _ = s.do(ctx, n, http.MethodPost, "/node/system", nodeAssetBody(s.spec, n), nil) }},
			{"packages", func() { _, _ = s.do(ctx, n, http.MethodPost, "/node/system", nodePackagesBody(s.spec, n), nil) }},
			{"checks", func() {
				_, _ = s.do(ctx, n, http.MethodPost, "/node/checks", nodeChecksBody(s.spec, n, time.Now()), nil)
			}},
			{"disks", func() { _, _ = s.do(ctx, n, http.MethodPost, "/node/disk", nodeDisksBody(s.spec, n), nil) }},
		} {
			pushes = append(pushes, &push{name: p.name, send: p.send, next: now.Add(time.Duration(r.Int64N(int64(spread))))})
		}
		// The first node of the k-th cluster is the proxy of the k-th array.
		if ai := n.cluster.idx; n.idx == 0 && ai < s.spec.Arrays {
			body := arrayBody(s.spec, s.fleet, ai)
			pushes = append(pushes, &push{name: "array", next: now.Add(time.Duration(r.Int64N(int64(spread)))), send: func() {
				_, _ = s.do(ctx, n, http.MethodPost, "/array", body, nil)
			}})
		}
		// The first node of the fleet inventories the san switches.
		if n.cluster.idx == 0 && n.idx == 0 {
			for _, sw := range fleetSANSwitches(s.spec, s.fleet) {
				body := sanSwitchBody(s.spec, s.fleet, sw)
				pushes = append(pushes, &push{name: "switch", next: now.Add(time.Duration(r.Int64N(int64(spread)))), send: func() {
					_, _ = s.do(ctx, n, http.MethodPost, "/sanswitch", body, nil)
				}})
			}
		}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-dequeue:
			s.dequeueActions(ctx, n)
		case now := <-ticker.C:
			if !s.run.NoInventory && !now.Before(statsNext) {
				_, _ = s.do(ctx, n, http.MethodPost, "/node/stats", nodeStatsBody(r, n, statsFrom, now, 10*time.Minute/10), nil)
				statsFrom = now
				statsNext = statsNext.Add(s.run.StatsInterval)
			}
			for _, p := range pushes {
				if !now.Before(p.next) {
					p.send()
					p.next = p.next.Add(s.run.DailyInterval)
				}
			}
		}
	}
}

// dequeueActions takes the actions queued for the node, and reports them done
// at once, without running anything.
func (s *sim) dequeueActions(ctx context.Context, n *node) {
	var queued struct {
		Actions []struct {
			ID int `json:"id"`
		} `json:"actions"`
	}
	if _, err := s.do(ctx, n, http.MethodGet, "/node/actionq", nil, &queued); err != nil || len(queued.Actions) == 0 {
		return
	}
	ids := make([]int, len(queued.Actions))
	for i, a := range queued.Actions {
		ids[i] = a.ID
	}
	_, _ = s.do(ctx, n, http.MethodPost, "/node/actionq/running", map[string]any{"ids": ids}, nil)
	for _, id := range ids {
		_, _ = s.do(ctx, n, http.MethodPost, "/node/actionq/done", map[string]any{
			"id": id, "ret": 0, "dequeued_at": ts(time.Now()), "stdout": "simulated", "stderr": "",
		}, nil)
	}
}

func uniq(l []string) []string {
	seen := make(map[string]bool, len(l))
	out := l[:0:0]
	for _, s := range l {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
