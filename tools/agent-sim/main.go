// Command agent-sim simulates OpenSVC v3 agents feeding an oc3 collector, for
// load tests: a cluster speaker posting the daemon status, its pings, the
// actions and the resource info of the objects, and each node pushing its
// stats and inventory. See README.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	// The agents date the lines of the action logs in their local time,
	// without a zone: run with the TZ of the collector (TZ=Europe/Paris), also
	// where the system has no zoneinfo, as in a minimal container.
	_ "time/tzdata"
)

const usage = `usage: agent-sim <command> [flags]

commands:
  plan      describe the fleet and the expected request rates
  setup     print the SQL registering the nodes of the fleet (pipe it to mysql)
  teardown  print the SQL removing the fleet and what the collector made of it
  run       feed the collector as the agents of the fleet

agent-sim <command> -h for the flags of a command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "plan":
		err = cmdPlan(args)
	case "setup":
		err = cmdSetup(args)
	case "teardown":
		err = cmdTeardown(args)
	case "run":
		err = cmdRun(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-sim:", err)
		os.Exit(1)
	}
}

func fleetFlags(fs *flag.FlagSet) *fleetSpec {
	spec := &fleetSpec{}
	fs.StringVar(&spec.Prefix, "prefix", "sim", "prefix of the names of the nodes, clusters and services: the fleet is what bears it")
	fs.Uint64Var(&spec.Seed, "seed", 1, "seed of the draws: the same flags give the same fleet and the same events")
	fs.IntVar(&spec.Nodes, "nodes", 10, "nodes count")
	fs.IntVar(&spec.NodesPerCluster, "nodes-per-cluster", 2, "nodes a cluster")
	fs.IntVar(&spec.Services, "services", 50, "services count, spread over the clusters")
	fs.IntVar(&spec.Resources, "resources", 8, "average resources a service")
	fs.Func("apps", "comma-separated apps of the clusters, round robin (default: the nodes of --app, the services of the default app)", func(v string) error {
		spec.Apps = nil
		for _, a := range strings.Split(v, ",") {
			if a = strings.TrimSpace(a); a != "" {
				spec.Apps = append(spec.Apps, a)
			}
		}
		return nil
	})
	fs.IntVar(&spec.Arrays, "arrays", 0, "pure arrays holding the disks of the services, 0 for local disks")
	fs.StringVar(&spec.App, "app", "SIM", "app of the nodes")
	fs.StringVar(&spec.Team, "team", "SimOps", "team responsible of the nodes and ops team of the app")
	return spec
}

func runFlags(fs *flag.FlagSet) *runSpec {
	run := &runSpec{}
	fs.StringVar(&run.FeederURL, "feeder", "http://127.0.0.1:8080", "url of the oc3 feeder, without /api")
	fs.StringVar(&run.Version, "agent-version", "3.0.0", "agent version announced in the daemon status")
	fs.DurationVar(&run.Timeout, "timeout", 5*time.Second, "request timeout (the agent default is 5s)")
	fs.DurationVar(&run.Duration, "duration", 10*time.Minute, "run duration")
	fs.DurationVar(&run.Ramp, "ramp", time.Minute, "time to start all the clusters, one after the other")
	fs.DurationVar(&run.StatusDelay, "status-delay", 10*time.Second, "period of the daemon status changes of a speaker")
	fs.DurationVar(&run.PingInterval, "ping-interval", time.Minute, "ping interval of a speaker without changes")
	fs.DurationVar(&run.InfoInterval, "info-interval", time.Hour, "resource info interval of each instance")
	fs.DurationVar(&run.StatsInterval, "stats-interval", 5*time.Minute, "node stats interval")
	fs.DurationVar(&run.DailyInterval, "daily-interval", 24*time.Hour, "interval of the node inventory pushes (asset, packages, checks, disks)")
	fs.Float64Var(&run.ChangesPerHour, "changes-per-hour", 0.5, "status changes an hour, by service")
	fs.Float64Var(&run.ActionsPerDay, "actions-per-day", 4, "actions a day, by service")
	fs.Float64Var(&run.ErrorRatio, "error-ratio", 0.05, "share of the actions ending in error")
	fs.DurationVar(&run.InventorySpread, "inventory-spread", 0, "time to send the first inventory pushes of all the nodes, 0 for the daily interval")
	fs.BoolVar(&run.NoInventory, "no-inventory", false, "leave out the node stats and inventory pushes")
	return run
}

func cmdPlan(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	spec := fleetFlags(fs)
	run := runFlags(fs)
	_ = fs.Parse(args)
	f, err := newFleet(*spec)
	if err != nil {
		return err
	}
	nodes := float64(f.nodeCount())
	clusters := float64(len(f.clusters))
	services := float64(spec.Services)
	instances := 0.0
	for _, c := range f.clusters {
		instances += float64(len(c.services) * len(c.nodes))
	}
	// A speaker posts a status when one of its services changed in the
	// period, a ping otherwise, at most every ping interval.
	perCluster := services / clusters
	pChanged := 1 - pow(1-run.ChangesPerHour*run.StatusDelay.Hours(), perCluster)
	status := clusters * pChanged / run.StatusDelay.Seconds()
	ping := clusters * (1 - pChanged) / max(run.PingInterval.Seconds(), run.StatusDelay.Seconds()/(1-pChanged+1e-9))
	actions := 2 * services * run.ActionsPerDay / 86400
	resinfo := instances / run.InfoInterval.Seconds()
	stats := nodes / run.StatsInterval.Seconds()
	daily := (4*nodes + float64(spec.Arrays)) / run.DailyInterval.Seconds()
	if run.NoInventory {
		stats, daily = 0, 0
	}
	fmt.Printf("fleet %q seed %d: %d clusters, %d nodes, %d services, %.0f instances, %d resources\n",
		spec.Prefix, spec.Seed, len(f.clusters), f.nodeCount(), spec.Services, instances, f.resourceCount())
	st := newClusterState(*spec, f.clusters[0], run.Version, time.Now())
	b, _ := json.Marshal(st.daemonStatusBody(time.Now(), time.Now(), st.allChanges()))
	fmt.Printf("daemon status of the first cluster: %.0f KB\n\n", float64(len(b))/1024)
	fmt.Printf("expected requests a second, at steady state:\n")
	for _, l := range []struct {
		name string
		v    float64
	}{
		{"daemon/status", status}, {"daemon/ping", ping}, {"instance/action (begin+end)", actions},
		{"instance/resource_info", resinfo}, {"node/stats", stats}, {"node/system,checks,disk,array", daily},
	} {
		fmt.Printf("  %-28s %8.2f\n", l.name, l.v)
	}
	fmt.Printf("  %-28s %8.2f\n", "total", status+ping+actions+resinfo+stats+daily)
	return nil
}

func pow(x, n float64) float64 {
	r := 1.0
	for i := 0; i < int(n); i++ {
		r *= x
	}
	return r
}

func sqlQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	spec := fleetFlags(fs)
	_ = fs.Parse(args)
	f, err := newFleet(*spec)
	if err != nil {
		return err
	}
	w := os.Stdout
	fmt.Fprintf(w, "-- agent-sim fleet %q seed %d: %d nodes\n", spec.Prefix, spec.Seed, f.nodeCount())
	for _, app := range append([]string{spec.App}, spec.Apps...) {
		fmt.Fprintf(w, "INSERT IGNORE INTO apps (app, app_domain, app_team_ops, description) VALUES (%s, 'sim', %s, 'agent-sim load test');\n",
			sqlQuote(app), sqlQuote(spec.Team))
	}
	var nodes, auth []string
	flush := func() {
		if len(nodes) > 0 {
			fmt.Fprintf(w, "INSERT INTO nodes (node_id, nodename, cluster_id, app, node_env, team_responsible, os_name, updated) VALUES\n%s\n"+
				"ON DUPLICATE KEY UPDATE cluster_id = VALUES(cluster_id), app = VALUES(app), team_responsible = VALUES(team_responsible);\n",
				strings.Join(nodes, ",\n"))
			fmt.Fprintf(w, "INSERT INTO auth_node (nodename, uuid, node_id) VALUES\n%s\nON DUPLICATE KEY UPDATE uuid = VALUES(uuid), nodename = VALUES(nodename);\n",
				strings.Join(auth, ",\n"))
		}
		nodes, auth = nil, nil
	}
	for _, c := range f.clusters {
		app := spec.App
		if c.app != "" {
			app = c.app
		}
		for _, n := range c.nodes {
			nodes = append(nodes, fmt.Sprintf("(%s, %s, %s, %s, 'PRD', %s, 'Linux', NOW())",
				sqlQuote(n.id), sqlQuote(n.name), sqlQuote(c.id), sqlQuote(app), sqlQuote(spec.Team)))
			auth = append(auth, fmt.Sprintf("(%s, %s, %s)", sqlQuote(n.name), sqlQuote(n.uuid), sqlQuote(n.id)))
			if len(nodes) == 500 {
				flush()
			}
		}
	}
	flush()
	return nil
}

// The tables of the collector by the ids they hold: teardown removes the rows
// of the nodes, services and clusters of the fleet.
var (
	tablesByNodeID = []string{
		"action_queue", "b_action_errors", "checks_live", "checks_settings", "comp_log", "comp_log_daily",
		"comp_node_moduleset", "comp_rulesets_nodes", "comp_status", "dashboard", "dashboard_events",
		"form_output_results", "hbmon", "hbmon_log", "hbmon_log_last", "log", "node_groups", "node_hba",
		"node_hw", "node_ip", "node_pw", "node_tags", "node_users", "packages", "patches", "resinfo",
		"resmon", "resmon_log", "resmon_log_last", "saves", "saves_last", "stor_array_proxy", "stor_zone",
		"svcactions", "svcdisks", "svcmon", "svcmon_log", "svcmon_log_last",
	}
	tablesBySvcID = []string{
		"action_queue", "checks_live", "comp_log", "comp_log_daily", "comp_modulesets_services",
		"comp_rulesets_services", "comp_status", "dashboard", "dashboard_events", "drpservices", "log",
		"resinfo", "resmon", "resmon_log", "resmon_log_last", "saves", "saves_last", "services_log",
		"services_log_last", "services_test", "stat_day_svc", "svc_tags", "svcactions", "svcdisks",
		"svcmon", "svcmon_log", "svcmon_log_ack", "svcmon_log_last",
	}
)

func cmdTeardown(args []string) error {
	fs := flag.NewFlagSet("teardown", flag.ExitOnError)
	prefix := fs.String("prefix", "sim", "prefix of the fleet to remove")
	_ = fs.Parse(args)
	if *prefix == "" || strings.ContainsAny(*prefix, "%_'\\") {
		return fmt.Errorf("refusing prefix %q", *prefix)
	}
	like := sqlQuote(*prefix + "-c%")
	w := os.Stdout
	fmt.Fprintf(w, "-- agent-sim teardown of the fleet %q\n", *prefix)
	fmt.Fprintf(w, "CREATE TEMPORARY TABLE sim_nodes (node_id char(36) CHARACTER SET ascii PRIMARY KEY) AS SELECT node_id FROM nodes WHERE nodename LIKE %s;\n", like)
	fmt.Fprintf(w, "CREATE TEMPORARY TABLE sim_clusters (cluster_id char(36) CHARACTER SET ascii PRIMARY KEY) AS SELECT DISTINCT cluster_id FROM nodes WHERE nodename LIKE %s AND cluster_id != '';\n", like)
	fmt.Fprintf(w, "CREATE TEMPORARY TABLE sim_svcs (svc_id char(36) CHARACTER SET ascii PRIMARY KEY) AS SELECT svc_id FROM services WHERE cluster_id IN (SELECT cluster_id FROM sim_clusters);\n")
	for _, t := range tablesBySvcID {
		fmt.Fprintf(w, "DELETE FROM %s WHERE svc_id IN (SELECT svc_id FROM sim_svcs);\n", t)
	}
	for _, t := range tablesByNodeID {
		fmt.Fprintf(w, "DELETE FROM %s WHERE node_id IN (SELECT node_id FROM sim_nodes);\n", t)
	}
	fmt.Fprintf(w, "DELETE FROM service_ids WHERE cluster_id IN (SELECT cluster_id FROM sim_clusters);\n")
	fmt.Fprintf(w, "DELETE FROM services WHERE svc_id IN (SELECT svc_id FROM sim_svcs);\n")
	fmt.Fprintf(w, "DELETE FROM auth_node WHERE node_id IN (SELECT node_id FROM sim_nodes);\n")
	fmt.Fprintf(w, "DELETE FROM nodes WHERE node_id IN (SELECT node_id FROM sim_nodes);\n")
	fmt.Fprintf(w, "DELETE FROM clusters WHERE cluster_id IN (SELECT cluster_id FROM sim_clusters);\n")
	// The arrays of the fleet (--arrays), and the disks they hold.
	arrays := sqlQuote(*prefix + "-fa%")
	fmt.Fprintf(w, "CREATE TEMPORARY TABLE sim_arrays (id int PRIMARY KEY, array_name varchar(128)) AS SELECT id, array_name FROM stor_array WHERE array_name LIKE %s;\n", arrays)
	fmt.Fprintf(w, "DELETE FROM diskinfo WHERE disk_arrayid IN (SELECT array_name FROM sim_arrays);\n")
	fmt.Fprintf(w, "DELETE FROM stor_array_dg_quota WHERE dg_id IN (SELECT id FROM stor_array_dg WHERE array_id IN (SELECT id FROM sim_arrays));\n")
	for _, t := range []string{"stor_array_dg", "stor_array_tgtid", "stor_array_proxy"} {
		fmt.Fprintf(w, "DELETE FROM %s WHERE array_id IN (SELECT id FROM sim_arrays);\n", t)
	}
	fmt.Fprintf(w, "DELETE FROM stor_array WHERE id IN (SELECT id FROM sim_arrays);\n")
	// The san switches of the fleet and their zoning.
	fmt.Fprintf(w, "DELETE FROM switches WHERE sw_name LIKE %s;\n", sqlQuote(*prefix+"-fab%"))
	for _, t := range []string{"san_zone", "san_zone_alias"} {
		fmt.Fprintf(w, "DELETE FROM %s WHERE cfg LIKE %s;\n", t, sqlQuote(strings.ToUpper(*prefix)+"\\_FAB%"))
	}
	return nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	spec := fleetFlags(fs)
	run := runFlags(fs)
	results := fs.String("results", "results", "directory of the run results: a sub-directory by run")
	name := fs.String("name", "", "run name (default: the date)")
	redisAddr := fs.String("redis", "", "redis address of the collector (host:port), to follow the length of the worker queues")
	metricsURLs := fs.String("metrics", "", "comma-separated URLs of the /metrics of the oc3 workers (http://host:port or http://host/path/metrics), to follow the length of the worker queues when redis is not reachable")
	reportEvery := fs.Duration("report-every", 30*time.Second, "report period")
	_ = fs.Parse(args)
	f, err := newFleet(*spec)
	if err != nil {
		return err
	}
	if *name == "" {
		*name = time.Now().Format("20060102-150405")
	}
	dir := filepath.Join(*results, *name)
	rec, err := newRecorder(dir, os.Stdout)
	if err != nil {
		return err
	}
	meta := map[string]any{"args": os.Args[1:], "fleet": spec, "run": run, "started_at": time.Now(),
		"clusters": len(f.clusters), "nodes": f.nodeCount(), "resources": f.resourceCount()}
	if b, err := json.MarshalIndent(meta, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "run.json"), b, 0o644)
	}
	fmt.Printf("run %s: %d clusters, %d nodes, %d services, %d resources, for %s, results in %s\n",
		*name, len(f.clusters), f.nodeCount(), spec.Services, f.resourceCount(), run.Duration, dir)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *redisAddr != "" || *metricsURLs != "" {
		var urls []string
		for _, u := range strings.Split(*metricsURLs, ",") {
			if u = strings.TrimSpace(u); u != "" {
				urls = append(urls, u)
			}
		}
		go rec.sampleQueues(ctx, *redisAddr, urls, dir)
	}
	go func() {
		ticker := time.NewTicker(*reportEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rec.report(*reportEvery)
			}
		}
	}()
	s := newSim(*spec, *run, f, rec)
	if err := s.probe(ctx); err != nil {
		return err
	}
	s.Run(ctx)
	cancel()
	summary, err := rec.close(dir)
	printSummary(os.Stdout, summary)
	return err
}
