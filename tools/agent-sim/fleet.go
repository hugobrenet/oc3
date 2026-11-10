package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
)

type (
	// fleetSpec is what the command line says of the simulated fleet: the same
	// spec gives the same fleet, names and ids included.
	fleetSpec struct {
		Prefix          string
		Seed            uint64
		Nodes           int
		NodesPerCluster int
		Services        int
		Resources       int
		// Arrays is the number of storage arrays holding the disks of the
		// services, 0 for local disks.
		Arrays int
		// Apps are the apps of the clusters, round robin: the nodes and
		// the services of a cluster are of its app. Empty, the nodes are of
		// App and the services of the "default" app, as a fresh agent says.
		Apps []string
		App  string
		Team string
	}

	fleet struct {
		spec     fleetSpec
		clusters []*cluster
	}

	cluster struct {
		idx      int
		id       string
		name     string
		app      string
		nodes    []*node
		services []*service
	}

	node struct {
		idx     int
		name    string
		id      string
		uuid    string
		cluster *cluster
	}

	service struct {
		idx       int
		path      string
		name      string
		topology  string
		app       string
		resources []resource
		// primary is the index in the cluster nodes of the node a failover
		// service runs on.
		primary int
	}

	resource struct {
		rid   string
		kind  string
		label string
		info  map[string]any
		// optional resources may flap without the service going down.
		optional bool
	}
)

// newRand returns a generator of its own for each use of each object: the
// draws of one object do not depend on the count of the others, so that a
// larger fleet keeps the objects of a smaller one as they were.
func (s fleetSpec) newRand(keys ...any) *rand.Rand {
	h := sha256.New()
	_, _ = fmt.Fprint(h, s.Prefix, s.Seed)
	for _, k := range keys {
		_, _ = fmt.Fprint(h, "|", k)
	}
	sum := h.Sum(nil)
	return rand.New(rand.NewPCG(binary.BigEndian.Uint64(sum[:8]), binary.BigEndian.Uint64(sum[8:16])))
}

// uuid derives a stable uuid (v4 layout) from the spec and keys.
func (s fleetSpec) uuid(keys ...any) string {
	h := sha256.New()
	_, _ = fmt.Fprint(h, s.Prefix, s.Seed)
	for _, k := range keys {
		_, _ = fmt.Fprint(h, "|", k)
	}
	b := h.Sum(nil)[:16]
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

var namespaces = []string{"prd", "pre", "uat", "dev", "ops"}

func newFleet(spec fleetSpec) (*fleet, error) {
	if spec.Nodes < 1 || spec.NodesPerCluster < 1 {
		return nil, fmt.Errorf("need at least one node and one node a cluster")
	}
	if spec.Services < 0 || spec.Resources < 1 {
		return nil, fmt.Errorf("need a positive services count and at least one resource a service")
	}
	f := &fleet{spec: spec}
	nClusters := (spec.Nodes + spec.NodesPerCluster - 1) / spec.NodesPerCluster
	for ci := 0; ci < nClusters; ci++ {
		c := &cluster{
			idx:  ci,
			id:   spec.uuid("cluster", ci),
			name: fmt.Sprintf("%s-c%04d", spec.Prefix, ci),
		}
		if len(spec.Apps) > 0 {
			c.app = spec.Apps[ci%len(spec.Apps)]
		}
		for ni := 0; ni < spec.NodesPerCluster; ni++ {
			gi := ci*spec.NodesPerCluster + ni
			if gi >= spec.Nodes {
				break
			}
			name := fmt.Sprintf("%s-c%04dn%d", spec.Prefix, ci, ni+1)
			c.nodes = append(c.nodes, &node{
				idx:     ni,
				name:    name,
				id:      spec.uuid("node", name),
				uuid:    spec.uuid("auth", name),
				cluster: c,
			})
		}
		f.clusters = append(f.clusters, c)
	}
	// The services go round robin over the clusters, so that adding services
	// adds them everywhere.
	for si := 0; si < spec.Services; si++ {
		c := f.clusters[si%nClusters]
		c.services = append(c.services, newService(spec, c, si))
	}
	return f, nil
}

func newService(spec fleetSpec, c *cluster, si int) *service {
	r := spec.newRand("service", si)
	ns := namespaces[r.IntN(len(namespaces))]
	name := fmt.Sprintf("%s-svc%05d", spec.Prefix, si)
	s := &service{
		idx:      si,
		name:     name,
		path:     ns + "/svc/" + name,
		topology: "failover",
		app:      "default",
		primary:  r.IntN(len(c.nodes)),
	}
	if c.app != "" {
		s.app = c.app
	}
	if len(c.nodes) > 1 && r.IntN(5) == 0 {
		s.topology = "flex"
	}
	n := max(1, spec.Resources/2+r.IntN(spec.Resources+1))
	for i := 0; i < n; i++ {
		s.resources = append(s.resources, newResource(r, s, i))
	}
	return s
}

func newResource(r *rand.Rand, s *service, i int) resource {
	switch i % 6 {
	case 0:
		ip := fmt.Sprintf("10.%d.%d.%d", 100+s.idx/65025%100, s.idx/255%255, s.idx%255+1)
		return resource{rid: fmt.Sprintf("ip#%d", i/6), kind: "ip.host", label: ip + "/24 eth0",
			info: map[string]any{"dev": "eth0", "ipaddr": ip, "netmask": 24, "hostname": s.name}}
	case 1:
		return resource{rid: fmt.Sprintf("disk#%d", i/6), kind: "disk.vg", label: fmt.Sprintf("vg_%s_%d", s.name, i/6),
			info: map[string]any{"name": fmt.Sprintf("vg_%s_%d", s.name, i/6)}}
	case 2:
		mnt := fmt.Sprintf("/srv/%s/fs%d", s.name, i/6)
		return resource{rid: fmt.Sprintf("fs#%d", i/6), kind: "fs.xfs", label: "/dev/vg_" + s.name + "/lv " + mnt,
			info: map[string]any{"mnt": mnt, "dev": "/dev/vg_" + s.name + "/lv"}}
	case 3:
		name := fmt.Sprintf("%s..%s.container.%d", namespaceOf(s.path), s.name, i/6)
		return resource{rid: fmt.Sprintf("container#%d", i/6), kind: "container.docker", label: "docker docker.io/library/nginx:1.27",
			info: map[string]any{"name": name, "id": fmt.Sprintf("%016x%016x", r.Uint64(), r.Uint64()), "pid": 1000 + r.IntN(100000)}}
	case 4:
		return resource{rid: fmt.Sprintf("app#%d", i/6), kind: "app.forking", label: fmt.Sprintf("/srv/%s/bin/app%d", s.name, i/6),
			optional: true, info: map[string]any{"script": fmt.Sprintf("/srv/%s/bin/app%d", s.name, i/6)}}
	default:
		return resource{rid: fmt.Sprintf("sync#%d", i/6), kind: "sync.rsync", label: "rsync to peers",
			optional: true, info: map[string]any{"target": []string{"nodes"}}}
	}
}

func (f *fleet) nodeCount() (n int) {
	for _, c := range f.clusters {
		n += len(c.nodes)
	}
	return
}

func (f *fleet) resourceCount() (n int) {
	for _, c := range f.clusters {
		for _, s := range c.services {
			n += len(s.resources)
		}
	}
	return
}
