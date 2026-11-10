package main

import (
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
)

// The payloads follow those of the om3 agent (daemon/collector, core/object
// push*), as captured from a v3 agent feeding oc3: the same shapes and about
// the same sizes, with made-up values.

const zeroTime = "0001-01-01T00:00:00Z"

func ts(t time.Time) string { return t.Format(time.RFC3339Nano) }

func (st *clusterState) daemonStatusBody(prev, now time.Time, changes []string) map[string]any {
	return map[string]any{
		"previous_updated_at": ts(prev),
		"updated_at":          ts(now),
		"changes":             changes,
		"version":             st.version,
		"data": map[string]any{
			"cluster": st.clusterData(now),
			"daemon":  map[string]any{"nodename": st.c.nodes[0].name, "routines": 300 + len(st.c.services)},
		},
	}
}

func (st *clusterState) pingBody() map[string]any {
	return map[string]any{
		"nodes":   st.nodeNames(),
		"Objects": st.objectPaths(),
		"version": st.version,
	}
}

func (st *clusterState) clusterData(now time.Time) map[string]any {
	c := st.c
	objects := map[string]any{
		"cluster": map[string]any{"avail": "up", "overall": "up", "updated_at": ts(st.startedAt)},
	}
	for si, s := range c.services {
		avail, overall, up := st.objectAvail(si)
		objects[s.path] = map[string]any{
			"priority":           50,
			"scope":              st.nodeNames(),
			"updated_at":         ts(st.objAt[si]),
			"avail":              avail,
			"frozen":             "unfrozen",
			"orchestrate":        "ha",
			"overall":            overall,
			"placement_policy":   "nodes order",
			"placement_state":    "optimal",
			"provisioned":        "true",
			"topology":           s.topology,
			"up_instances_count": up,
		}
	}
	nodes := map[string]any{}
	gen := map[string]any{}
	for _, n := range c.nodes {
		gen[n.name] = st.gen
	}
	for ni, n := range c.nodes {
		instances := map[string]any{}
		for si := range c.services {
			instances[c.services[si].path] = st.instanceData(si, ni)
		}
		nodes[n.name] = map[string]any{
			"instance": instances,
			"monitor": map[string]any{
				"global_expect": "none", "local_expect": "none", "state": "idle",
				"global_expect_updated_at": zeroTime, "local_expect_updated_at": zeroTime,
				"state_updated_at": ts(st.startedAt), "updated_at": ts(st.startedAt),
				"orchestration_id": "00000000-0000-0000-0000-000000000000", "orchestration_is_done": false,
				"session_id": "00000000-0000-0000-0000-000000000000", "preserved": false,
			},
			"stats": map[string]any{"load_15m": 0.4, "mem_avail": 41, "mem_total": 32000, "score": 63, "swap_avail": 90, "swap_total": 4096},
			"status": map[string]any{
				"agent": "v3.0.0-sim", "api": 0, "arbitrators": map[string]any{}, "compat": 0,
				"frozen_at": zeroTime, "gen": gen, "is_leader": ni == 0, "is_overloaded": false,
				"booted_at": ts(st.startedAt), "left_at": zeroTime, "rejoined_at": zeroTime,
			},
			"os":   map[string]any{"paths": []any{}},
			"pool": map[string]any{"default": map[string]any{"type": "directory", "head": "/srv", "size": 49292255232, "used": 24120201216, "free": 25172054016, "capabilities": []string{"rox", "rwx", "roo", "rwo", "blk"}, "errors": []any{}, "updated_at": ts(now)}},
			"config": map[string]any{
				"env": "PRD", "max_parallel": 10, "labels": map[string]any{}, "split_action": "crash",
				"collector": map[string]any{"ping_interval": 60000000000, "status_delay": 10000000000, "action_batch": 100},
			},
			"daemon": map[string]any{
				"nodename":   n.name,
				"started_at": ts(st.startedAt),
				"collector":  map[string]any{"id": "collector", "state": map[bool]string{true: "speaker", false: "speaker-candidate"}[ni == 0]},
				"heartbeat":  st.heartbeat(ni, now),
			},
		}
	}
	return map[string]any{
		"config": map[string]any{
			"id": c.id, "name": c.name, "nodes": st.nodeNames(), "dns": []string{}, "quorum": false,
			"listener": map[string]any{"port": 1215},
		},
		"status": map[string]any{"is_compat": true, "is_frozen": false},
		"object": objects,
		"node":   nodes,
	}
}

func (st *clusterState) heartbeat(ni int, now time.Time) map[string]any {
	var streams []any
	for _, dir := range []string{"rx", "tx"} {
		peers := map[string]any{}
		for pi, p := range st.c.nodes {
			if pi == ni {
				continue
			}
			peers[p.name] = map[string]any{"desc": dir + " " + p.name, "is_beating": true, "changed_at": ts(st.startedAt), "last_beating_at": ts(now)}
		}
		streams = append(streams, map[string]any{
			"id": "hb#1." + dir, "state": "running", "type": "unicast", "peers": peers, "alerts": []any{},
			"configured_at": ts(st.startedAt), "created_at": ts(st.startedAt), "updated_at": zeroTime,
		})
	}
	return map[string]any{"streams": streams, "updated_at": ts(now)}
}

func (st *clusterState) instanceData(si, ni int) map[string]any {
	s := st.c.services[si]
	is := st.inst[si][ni]
	avail, overall := st.avail(si, ni)
	cfgRes := map[string]any{}
	statusRes := map[string]any{}
	for ri, r := range s.resources {
		cfgRes[r.rid] = map[string]any{"is_disabled": false, "is_monitored": false, "is_standby": false, "restart_delay": 500000000}
		statusRes[r.rid] = map[string]any{
			"label": r.label, "status": is.res[ri], "type": r.kind, "optional": r.optional,
			"provisioned": map[string]any{"mtime": ts(st.startedAt), "state": "true"},
			"info":        r.info,
		}
	}
	monitorState := "idle"
	localExpect := "none"
	if st.running(si, ni) {
		localExpect = "started"
	}
	return map[string]any{
		"config": map[string]any{
			"csum": csum(s.path, len(s.resources)), "priority": 50, "scope": st.nodeNames(),
			"updated_at": ts(st.startedAt), "app": s.app, "monitor_action": []string{"none"},
			"orchestrate": "ha", "placement_policy": "nodes order", "resources": cfgRes,
			"schedules": []any{
				map[string]any{"action": "status", "schedule": "@10m", "key": "status_schedule"},
				map[string]any{"action": "info", "schedule": "@60m", "key": "info_schedule"},
			},
			"stonith": false, "subsets": map[string]any{}, "topology": s.topology, "is_disabled": false,
		},
		"monitor": map[string]any{
			"global_expect": "none", "global_expect_updated_at": zeroTime, "is_leader": ni == s.primary,
			"is_ha_leader": ni == s.primary, "local_expect": localExpect, "local_expect_updated_at": ts(st.startedAt),
			"orchestration_id": "00000000-0000-0000-0000-000000000000", "state": monitorState,
			"state_updated_at": ts(is.updatedAt), "preserved": false, "updated_at": ts(is.updatedAt),
		},
		"status": map[string]any{
			"avail": avail, "overall": overall, "provisioned": "true", "frozen_at": zeroTime,
			"last_started_at": ts(is.startedAt), "stopped_at": zeroTime, "resources": statusRes,
			"updated_at": ts(is.updatedAt), "config_csum": csum(s.path, len(s.resources)),
		},
	}
}

// resinfoBody is the info of the resources of an instance, as the agent sends
// it on the info schedule of the object (@60m).
func (st *clusterState) resinfoBody(si, ni int) map[string]any {
	s := st.c.services[si]
	var info []any
	for _, r := range s.resources {
		keys := []any{
			map[string]any{"key": "driver", "value": r.kind},
			map[string]any{"key": "standby", "value": "false"},
			map[string]any{"key": "optional", "value": strconv.FormatBool(r.optional)},
			map[string]any{"key": "disable", "value": "false"},
			map[string]any{"key": "monitor", "value": "false"},
			map[string]any{"key": "shared", "value": "false"},
			map[string]any{"key": "encap", "value": "false"},
			map[string]any{"key": "restart_count", "value": "0"},
			map[string]any{"key": "restart_delay", "value": "500ms"},
		}
		for k, v := range r.info {
			keys = append(keys, map[string]any{"key": k, "value": fmt.Sprint(v)})
		}
		info = append(info, map[string]any{"rid": r.rid, "keys": keys})
	}
	return map[string]any{"path": s.path, "nodename": st.c.nodes[ni].name, "topology": s.topology, "info": info}
}

func (st *clusterState) objectConfigBody(si int) map[string]any {
	s := st.c.services[si]
	var b strings.Builder
	fmt.Fprintf(&b, "[DEFAULT]\nnodes = %s\ntopology = %s\norchestrate = ha\napp = %s\n\n", strings.Join(st.nodeNames(), " "), s.topology, s.app)
	for _, r := range s.resources {
		fmt.Fprintf(&b, "[%s]\ntype = %s\n", r.rid, r.kind)
		for k, v := range r.info {
			fmt.Fprintf(&b, "%s = %v\n", k, v)
		}
		b.WriteString("\n")
	}
	return map[string]any{
		"path": s.path, "app": s.app, "comment": "", "drp_node": "", "env": "",
		"flex_max": 0, "flex_min": 0, "flex_target": 0, "monitored_resource_count": 0,
		"orchestrate": "ha", "scope": st.nodeNames(), "topology": s.topology,
		"raw_config": base64.StdEncoding.EncodeToString([]byte(b.String())),
	}
}

// actionBody is the begin (end zero) or the end of an action.
func actionBody(st *clusterState, si, ni int, action, sessionID, uuid string, pid int, begin, end time.Time, status string, nLines int) map[string]any {
	s := st.c.services[si]
	body := map[string]any{
		"path": s.path, "nodename": st.c.nodes[ni].name, "action": action,
		"argv": []string{s.path, "instance", action}, "pid": strconv.Itoa(pid), "rids": "",
		"origin": "daemon/monitor", "version": strings.TrimPrefix(st.version, "v"), "cron": false,
		"session_uuid": sessionID, "begin": ts(begin), "end": "", "status": "", "status_log": "",
		"uuid": "", "lines": []any{},
	}
	if end.IsZero() {
		return body
	}
	body["end"] = ts(end)
	body["status"] = status
	body["uuid"] = uuid
	lines := []any{map[string]any{
		"begin": begin.Format(time.DateTime), "pid": strconv.Itoa(pid), "rid": "", "status": "ok", "subset": "",
		"status_log": fmt.Sprintf("instance: %s: >>> do %s [/usr/bin/om %s instance %s] (origin daemon/monitor, session_id %s)", s.path, action, s.path, action, sessionID),
	}}
	for i := 0; i < nLines && i < len(s.resources); i++ {
		r := s.resources[i]
		lineStatus := "ok"
		if status == "err" && i == nLines-1 {
			lineStatus = "err"
		}
		lines = append(lines, map[string]any{
			"begin": begin.Add(time.Duration(i) * time.Second).Format(time.DateTime), "pid": strconv.Itoa(pid),
			"rid": r.rid, "status": lineStatus, "subset": "",
			"status_log": fmt.Sprintf("instance: %s: %s: %s %s\ninstance: %s: %s: %s done", s.path, r.rid, action, r.label, s.path, r.rid, action),
		})
	}
	body["lines"] = lines
	return body
}

// The node pushes. Each node draws its inventory from a generator of its own,
// so that it sends the same inventory every time, as a real node would.

func nodeStatsBody(r *rand.Rand, n *node, from, to time.Time, step time.Duration) map[string]any {
	groups := map[string]any{}
	add := func(group string, cols []string, keyVals []string, gen func() []string) {
		g := map[string]any{"columns": cols}
		var rows []any
		for t := from.Add(step); !t.After(to); t = t.Add(step) {
			if keyVals == nil {
				rows = append(rows, append([]string{t.UTC().Format(time.RFC3339)}, gen()...))
				continue
			}
			for _, k := range keyVals {
				rows = append(rows, append([]string{t.UTC().Format(time.RFC3339), k}, gen()...))
			}
		}
		g["rows"] = rows
		groups[group] = g
	}
	f := func(max float64) string { return strconv.FormatFloat(r.Float64()*max, 'f', 2, 64) }
	add("cpu", []string{"date", "cpu", "usr", "nice", "sys", "iowait", "steal", "irq", "soft", "guest", "gnice", "idle"},
		[]string{"all", "0", "1", "2", "3"}, func() []string {
			return []string{f(40), f(1), f(10), f(5), f(1), f(1), f(1), "0.00", "0.00", f(100)}
		})
	add("mem_u", []string{"date", "kbmemfree", "kbavail", "kbmemused", "pct_memused", "kbbuffers", "kbcached", "kbcommit", "pct_commit", "kbactive", "kbinact", "kbdirty"},
		nil, func() []string {
			return []string{f(8e6), f(16e6), f(16e6), f(100), f(1e5), f(8e6), f(2e7), f(100), f(1e7), f(5e6), f(1e4)}
		})
	add("swap", []string{"date", "kbswpfree", "kbswpused", "pct_swpused", "kbswpcad", "pct_swpcad"},
		nil, func() []string { return []string{f(4e6), f(1e5), f(10), f(1e4), f(10)} })
	add("proc", []string{"date", "runq_sz", "plist_sz", "ldavg_1", "ldavg_5", "ldavg_15"},
		nil, func() []string { return []string{f(4), f(800), f(4), f(4), f(4)} })
	add("block", []string{"date", "tps", "rtps", "wtps", "rbps", "wbps"},
		nil, func() []string { return []string{f(500), f(200), f(300), f(1e4), f(1e4)} })
	add("blockdev", []string{"date", "dev", "tps", "rsecps", "wsecps", "avgrq_sz", "avgqu_sz", "await", "svctm", "pct_util"},
		[]string{"sda", "sdb", "dm-0", "dm-1"}, func() []string {
			return []string{f(200), f(1e4), f(1e4), f(100), f(2), f(20), f(5), f(100)}
		})
	add("netdev", []string{"date", "dev", "rxpckps", "txpckps", "rxkBps", "txkBps"},
		[]string{"eth0", "eth1"}, func() []string { return []string{f(1e4), f(1e4), f(1e4), f(1e4)} })
	add("netdev_err", []string{"date", "dev", "rxerrps", "txerrps", "collps", "rxdropps", "txdropps"},
		[]string{"eth0", "eth1"}, func() []string { return []string{"0.00", "0.00", "0.00", f(1), "0.00"} })
	add("fs_u", []string{"date", "mntpt", "size", "used"},
		[]string{"/", "/var", "/srv"}, func() []string { return []string{"52428800", strconv.Itoa(r.IntN(100))} })
	return map[string]any{"data": groups}
}

func nodeAssetBody(spec fleetSpec, n *node) map[string]any {
	r := spec.newRand("asset", n.name)
	prop := func(title string, v any) map[string]any {
		return map[string]any{"source": "probe", "title": title, "value": v}
	}
	var hw, gids, uids []any
	for i := 0; i < 30; i++ {
		hw = append(hw, map[string]any{"path": fmt.Sprintf("00:%02x.0", i), "description": fmt.Sprintf("Simulated PCI device %d (rev 01)", i), "class": "Bridge", "driver": "virtio-pci", "type": "pci"})
	}
	for i := 0; i < 60; i++ {
		gids = append(gids, map[string]any{"gid": i, "groupname": fmt.Sprintf("grp%d", i)})
		uids = append(uids, map[string]any{"uid": i, "username": fmt.Sprintf("usr%d", i)})
	}
	mac := fmt.Sprintf("52:54:00:%02x:%02x:%02x", r.IntN(256), r.IntN(256), r.IntN(256))
	hba, targets := nodeHBAs(spec, n)
	ip := fmt.Sprintf("10.%d.%d.%d", 10+n.cluster.idx/250%200, n.cluster.idx%250, n.idx+10)
	return map[string]any{
		"properties": map[string]any{
			"nodename": prop("nodename", n.name), "fqdn": prop("fqdn", n.name+".sim.example.com"),
			"version": prop("agent version", "v3.0.0-sim"), "os_name": prop("os name", "Linux"),
			"os_vendor": prop("os vendor", "Ubuntu"), "os_release": prop("os release", "24.04"),
			"os_kernel": prop("os kernel", "6.8.0-45-generic"), "os_arch": prop("os arch", "x86_64"),
			"mem_bytes": prop("mem bytes", 32768), "mem_slots": prop("mem slots", 4), "mem_banks": prop("mem banks", 4),
			"cpu_freq": prop("cpu freq", 2400), "cpu_threads": prop("cpu threads", 8), "cpu_cores": prop("cpu cores", 4),
			"cpu_dies": prop("cpu dies", 1), "cpu_model": prop("cpu model", "Simulated CPU @ 2.40GHz"),
			"serial": prop("serial", fmt.Sprintf("SIM%010d", r.IntN(1e9))), "model": prop("model", "Simulated VM"),
			"manufacturer": prop("manufacturer", "SimCorp"), "bios_version": prop("bios version", "1.16.3"),
			"node_env": prop("node env", "PRD"), "listener_port": prop("listener port", 1215),
			"cluster_id": prop("cluster id", n.cluster.id), "tz": prop("timezone", time.Now().Format("-07:00")),
			"last_boot": prop("last boot", time.Now().Add(-72*time.Hour).Truncate(time.Second).Format(time.RFC3339)),
			"boot_id":   prop("boot id", spec.uuid("boot", n.name)), "connect_to": prop("connect to", ""),
			"enclosure":  map[string]any{"source": "default", "title": "enclosure", "value": nil, "error": "not implemented"},
			"sp_version": map[string]any{"source": "default", "title": "sp version", "value": nil, "error": "not implemented"},
		},
		"hardware": hw,
		"gids":     gids,
		"uids":     uids,
		"hba":      hba,
		"targets":  targets,
		"lan": map[string]any{
			mac: []any{map[string]any{"addr": ip, "flag_deprecated": false, "intf": "eth0", "mask": "24", "type": "ipv4"}},
		},
	}
}

func nodePackagesBody(spec fleetSpec, n *node) map[string]any {
	r := spec.newRand("packages", n.name)
	count := 300 + r.IntN(750)
	var pkgs []any
	installed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		pkgs = append(pkgs, map[string]any{
			"name": fmt.Sprintf("pkg-%04d", i), "version": fmt.Sprintf("%d.%d.%d-1ubuntu%d", 1+i%9, i%30, r.IntN(3), r.IntN(3)),
			"arch": "amd64", "type": "deb", "installed_at": installed.Add(time.Duration(i) * time.Hour).Format(time.RFC3339), "sig": "",
		})
	}
	return map[string]any{"package": pkgs}
}

func nodeChecksBody(spec fleetSpec, n *node, now time.Time) map[string]any {
	r := spec.newRand("checks", n.name, now.Unix()/86400)
	var data []any
	for _, mnt := range []string{"/", "/var", "/srv", "/tmp", "/boot"} {
		data = append(data, map[string]any{"driver": "df", "instance": mnt, "path": "", "type": "fs_u", "unit": "%", "value": r.IntN(100)})
		data = append(data, map[string]any{"driver": "df", "instance": mnt, "path": "", "type": "fs_i", "unit": "%", "value": r.IntN(30)})
	}
	for _, s := range n.cluster.services {
		data = append(data, map[string]any{"driver": "df", "instance": "/srv/" + s.name + "/fs0", "path": s.path, "type": "fs_u", "unit": "%", "value": r.IntN(100)})
	}
	for _, eth := range []string{"eth0", "eth1"} {
		data = append(data, map[string]any{"driver": "ethtool", "instance": eth + ".link", "path": "", "type": "eth", "unit": "", "value": 1})
	}
	data = append(data, map[string]any{"driver": "lvm", "instance": "rootvg", "path": "", "type": "vg_u", "unit": "%", "value": r.IntN(100)})
	return map[string]any{"data": data}
}

// serviceDiskSerial is the serial of the disk of a service: 24 hex digits.
func serviceDiskSerial(spec fleetSpec, s *service) string {
	return fmt.Sprintf("%024x", spec.newRand("disk", s.path).Uint64())
}

// serviceDiskSize is the size of the disk of a service, in MB.
func serviceDiskSize(spec fleetSpec, s *service) int {
	if spec.Arrays == 0 {
		return 10240
	}
	return 10240 * (1 + spec.newRand("disksize", s.path).IntN(20))
}

func nodeDisksBody(spec fleetSpec, n *node) map[string]any {
	var data []any
	for _, s := range n.cluster.services {
		serial := serviceDiskSerial(spec, s)
		size := serviceDiskSize(spec, s)
		d := map[string]any{
			"dg": "vg_" + s.name, "id": "36001405" + serial,
			"model": "SimDisk", "object_path": s.path, "region": "0", "size": size, "used": size, "vendor": "SIMSTOR ",
		}
		if spec.Arrays > 0 {
			// A volume of a pure array, as the agent reports it.
			d["id"], d["model"], d["vendor"] = "624a9370"+serial, "FlashArray", "PURE    "
		}
		data = append(data, d)
	}
	data = append(data, map[string]any{
		"dg": "rootvg", "id": fmt.Sprintf("36001405%024x", spec.newRand("disk", n.name).Uint64()),
		"model": "SimDisk", "object_path": "", "region": "0", "size": 51200, "used": 51200, "vendor": "SIMSTOR ",
	})
	return map[string]any{"data": data}
}

// arrayName is the name of the ai-th array of the fleet.
func arrayName(spec fleetSpec, ai int) string {
	return fmt.Sprintf("%s-fa%02d", spec.Prefix, ai+1)
}

// arrayBody is the report of the ai-th pure array, as "om node push arrays"
// sends it: the array, its fc ports, and the volumes of the services of the
// clusters it serves (cluster ci on array ci%arrays).
func arrayBody(spec fleetSpec, f *fleet, ai int) map[string]any {
	name := arrayName(spec, ai)
	r := spec.newRand("array", name)
	var ports []any
	for i, wwn := range arrayPortWWNs(spec, ai) {
		ports = append(ports, map[string]any{"name": fmt.Sprintf("CT%d.FC%d", i/4, i%4), "wwn": strings.ToUpper(wwn[0:2] + ":" + wwn[2:4] + ":" + wwn[4:6] + ":" + wwn[6:8] + ":" + wwn[8:10] + ":" + wwn[10:12] + ":" + wwn[12:14] + ":" + wwn[14:16])})
	}
	volumes := []any{}
	for _, c := range f.clusters {
		if c.idx%spec.Arrays != ai {
			continue
		}
		for _, s := range c.services {
			size := int64(serviceDiskSize(spec, s)) * 1024 * 1024
			volumes = append(volumes, map[string]any{
				"id":          spec.uuid("volume", s.path),
				"name":        "vg_" + s.name,
				"serial":      strings.ToUpper(serviceDiskSerial(spec, s)),
				"provisioned": size,
				"space":       map[string]any{"total_physical": size * int64(20+r.IntN(60)) / 100},
				"subtype":     "regular",
			})
		}
	}
	return map[string]any{
		"name": name,
		"type": "pure",
		"data": map[string]any{
			"arrays":  []any{map[string]any{"name": name, "version": "6.5.3", "os": "Purity//FA"}},
			"volumes": volumes,
			"ports":   ports,
		},
	}
}

// arrayPortWWNs are the wwn of the 8 fc ports of the ai-th array, 16 hex
// digits: ports 0-3 on controller 0, 4-7 on controller 1, the even ports on
// fabric A and the odd ones on fabric B.
func arrayPortWWNs(spec fleetSpec, ai int) []string {
	r := spec.newRand("arrayports", arrayName(spec, ai))
	l := make([]string, 8)
	for i := range l {
		l[i] = fmt.Sprintf("524a9370%08x", r.Uint32())
	}
	return l
}

// nodeHBAs are the fc host bus adapters of a node of a fleet with arrays, one
// a fabric, and the target ports each sees: the ports of the array of its
// cluster on its fabric.
func nodeHBAs(spec fleetSpec, n *node) (hba, targets []any) {
	hba, targets = []any{}, []any{}
	if spec.Arrays == 0 {
		return
	}
	ports := arrayPortWWNs(spec, n.cluster.idx%spec.Arrays)
	for fabric, id := range nodeHBAIDs(spec, n) {
		hba = append(hba, map[string]any{"hba_id": id, "hba_type": "fc", "host": fmt.Sprintf("host%d", fabric+1)})
		for i := fabric; i < len(ports); i += 2 {
			targets = append(targets, map[string]any{"hba_id": id, "tgt_id": ports[i]})
		}
	}
	return
}
