package cmd

import (
	"strings"

	"github.com/spf13/viper"

	"github.com/opensvc/oc3/oc2websocket"
)

var (
	configCandidateDirs = []string{"/etc/oc3/", "$HOME/.config/oc3", "./"}
)

func workerSection(name string) string {
	if name != "" {
		return sectionWorker + "." + name
	} else {
		return sectionWorker
	}
}

func setDefaultWorkerConfig(name string) {
	section := workerSection(name)
	uxSocket := "/var/run/oc3_worker_pprof.sock"
	if name != "" {
		uxSocket = "/var/run/oc3_worker_" + name + "_pprof.sock"
	}
	viper.SetDefault(section+".addr", "127.0.0.1:8100")
	viper.SetDefault(section+".pprof.net.enable", false)
	viper.SetDefault(section+".pprof.ux.enable", false)
	viper.SetDefault(section+".pprof.ux.socket", uxSocket)
	viper.SetDefault(section+".metrics.enable", false)

	viper.SetDefault(section+".runners", 1)
	viper.SetDefault(section+".log.request.level", "none")
	viper.SetDefault(section+".directories.uploads", "/oc3/uploads")
}

func setDefaultFeederConfig() {
	s := sectionFeeder
	viper.SetDefault(s+".addr", "127.0.0.1:8080")
	viper.SetDefault(s+".pprof.uxsocket", "/var/run/oc3_feeder_pprof.sock")
	viper.SetDefault(s+".pprof.net.enable", false)
	viper.SetDefault(s+".pprof.ux.enable", false)
	viper.SetDefault(s+".pprof.ux.socket", "/var/run/oc3_feeder_pprof.sock")
	viper.SetDefault(s+".pprof.enable", false)
	viper.SetDefault(s+".metrics.enable", false)
	viper.SetDefault(s+".ui.enable", false)
	viper.SetDefault(s+".sync.timeout", "2s")
	viper.SetDefault(s+".log.request.level", "none")
	// The configuration of a big symmetrix weighs tens of megabytes.
	viper.SetDefault(s+".array.max_size", 256<<20)
}

func setDefaultServerConfig() {
	s := sectionServer
	viper.SetDefault(s+".addr", "127.0.0.1:8081")
	viper.SetDefault(s+"pprof.pprof.net.enable", false)
	viper.SetDefault(s+"pprof.pprof.ux.enable", false)
	viper.SetDefault(s+"pprof.pprof.ux.socket", "/var/run/oc3_server_pprof.sock")
	viper.SetDefault(s+".metrics.enable", false)
	viper.SetDefault(s+".ui.enable", false)
	viper.SetDefault(s+".sync.timeout", "2s")
	// The packages diffs: computed at most this many at once, each given this
	// long, and cached in redis until the packages of a compared node change.
	viper.SetDefault(s+".packages_diff.concurrency", 2)
	viper.SetDefault(s+".packages_diff.timeout", "30s")
	viper.SetDefault(s+".allow_anon_register", false)
	// A node may register a node joining its cluster, already registered.
	viper.SetDefault(s+".allow_node_enroll", true)
	// One git repository per form, holding its yaml history, as the historical
	// collector keeps them under private/forms.
	viper.SetDefault(s+".directories.forms", "/oc3/uploads/forms")
	// One git repository per node, committed by the scheduler from the reports of
	// the agents.
	viper.SetDefault(s+".directories.sysreport", "/oc3/uploads/sysreport")
	// One git repository holding the compliance export, committed at each commit
	// of the compliance designer.
	viper.SetDefault(s+".directories.compliance", "/oc3/uploads/compliance")
	// The bare git repository of the compliance modules, archived for the agents
	// (om node updatecomp).
	viper.SetDefault(s+".directories.compliance_modules", "/oc3/uploads/compliance_modules.git")
	// Serve the archive of the compliance modules (GET
	// /compliance/modules_repo/current) to unauthenticated callers too: the v2
	// agents' om node updatecomp fetches it without credentials. Off by default:
	// the modules, and what they reveal of the hosts, are then for authenticated
	// nodes and users only. Read at start.
	viper.SetDefault(s+".compliance_modules.anonymous_download", false)
	// How long the mangler of a form rest output may run, in the javascript
	// interpreter of oc3 (server/handlers/forms_mangle.go).
	viper.SetDefault(s+".forms.mangle_timeout", "5s")
	// The longest a statement of the API runs in the database, see newServer.
	// 0: no limit.
	viper.SetDefault(s+".db.max_statement_time", "60s")
	// meta.total of a list is counted up to this many rows, and left out above:
	// counting millions of rows for the page count costs more than the page.
	// 0: always counted.
	viper.SetDefault(s+".list.total_max", 10000)
	// The external hosts a rest output may call by its absolute url, "host" or
	// "host:port"; none by default: an external API is called through a connector.
	viper.SetDefault(s+".forms.external_hosts", []string{})
	// The loopback, private or reserved networks those hosts may resolve to, as
	// CIDR prefixes; the public addresses are always allowed (see util/egress).
	viper.SetDefault(s+".forms.external_networks", []string{})
	viper.SetDefault(s+".forms.external_timeout", "60s")
	viper.SetDefault(s+".forms.external_max_bytes", 10*1024*1024)
	viper.SetDefault(s+".log.request.level", "none")

	setDefaultAuthConfig()
	setDefaultOIDCConfig(s)
}

func setDefaultSchedulerConfig() {
	s := sectionScheduler
	viper.SetDefault(s+".addr", "127.0.0.1:8082")
	viper.SetDefault(s+".directories.uploads", "/oc3/uploads")
	viper.SetDefault(s+".pprof.net.enable", false)
	viper.SetDefault(s+".pprof.ux.enable", false)
	viper.SetDefault(s+".pprof.ux.socket", "/var/run/oc3_scheduler_pprof.sock")
	viper.SetDefault(s+".metrics.enable", false)
	viper.SetDefault(s+".task.trim.retention", 365)
	viper.SetDefault(s+".task.trim.batch_size", 1000)
	viper.SetDefault(s+".task.purge_services.max_age", "0")
	viper.SetDefault(s+".task.purge_services.batch_size", 1000)
	viper.SetDefault(s+".task.purge_nodes.max_age", "0")
	viper.SetDefault(s+".task.purge_nodes.batch_size", 1000)
	viper.SetDefault(s+".task.alert_checks_not_updated.max_age", "1d")
	viper.SetDefault(s+".task.alert_instances_not_updated.max_age", "16m")
	viper.SetDefault(s+".task.alert_nodes_not_updated.max_age", "25h")
	viper.SetDefault(s+".task.alert_service_config_not_updated.max_age", "25h")
	viper.SetDefault(s+".task.log_instances_not_updated.max_age", "2h")
	viper.SetDefault(s+".task.scrub_checks_live.max_age", "2d")
	viper.SetDefault(s+".task.scrub_comp_status.max_age", "31d")
	viper.SetDefault(s+".task.scrub_comp_status_unattached.max_age", "7d")
	viper.SetDefault(s+".task.scrub_diskinfo.max_age", "2d")
	viper.SetDefault(s+".task.scrub_instances.max_age", "21m")
	viper.SetDefault(s+".task.scrub_node_hba.max_age", "7d")
	viper.SetDefault(s+".task.scrub_object.max_age", "15m")
	viper.SetDefault(s+".task.scrub_packages.max_age", "100d")
	viper.SetDefault(s+".task.scrub_patches.max_age", "100d")
	viper.SetDefault(s+".task.scrub_pdf.max_age", "1d")
	viper.SetDefault(s+".task.scrub_resmon.max_age", "1d")
	viper.SetDefault(s+".task.scrub_resources.max_age", "15m")
	viper.SetDefault(s+".task.scrub_static.max_age", "1h")
	viper.SetDefault(s+".task.scrub_stor_array.max_age", "2d")
	viper.SetDefault(s+".task.scrub_svcdisks.max_age", "2d")
	viper.SetDefault(s+".task.scrub_tempviz.max_age", "1h")
	viper.SetDefault(s+".task.scrub_unfinished_actions.max_age", "2h")
	viper.SetDefault(s+".log.request.level", "none")
}

func setDefaultMessengerConfig() {
	s := sectionMessenger
	viper.SetDefault(s+".addr", "127.0.0.1:8083")
	viper.SetDefault(s+".pprof.net.enable", false)
	viper.SetDefault(s+".pprof.ux.enable", false)
	viper.SetDefault(s+".pprof.ux.socket", "/var/run/oc3_messenger_pprof.sock")
	viper.SetDefault(s+".metrics.enable", false)
	viper.SetDefault(s+".key", "magix123")
	viper.SetDefault(s+".url", "http://127.0.0.1:8889")
	viper.SetDefault(s+".require_token", false)
	viper.SetDefault(s+".key_file", "")
	viper.SetDefault(s+".cert_file", "")
	viper.SetDefault(s+".log.request.level", "none")

	setDefaultAuthConfig()
}

func setDefaultRunnerConfig() {
	s := sectionRunner
	viper.SetDefault(s+".addr", "127.0.0.1:8084")
	viper.SetDefault(s+".pprof.net.enable", false)
	viper.SetDefault(s+".pprof.ux.enable", false)
	viper.SetDefault(s+".pprof.ux.socket", "/var/run/oc3_runner_pprof.sock")
	viper.SetDefault(s+".metrics.enable", false)
	viper.SetDefault(s+".log.request.level", "none")
	viper.SetDefault(s+".nb_workers", 0)
	viper.SetDefault(s+".purge_timeout", 0)
	viper.SetDefault(s+".notification_timeout", 0)
	viper.SetDefault(s+".command_timeout", 0)
}

// setDefaultDBConfig sets the defaults of the db settings, shared by the
// sections. Each section may set its own, <section>.db.<name>, but the
// password, see dbKey.
func setDefaultDBConfig() {
	viper.SetDefault("db.username", "opensvc")
	viper.SetDefault("db.host", "127.0.0.1")
	viper.SetDefault("db.port", "3306")
	viper.SetDefault("db.log.level", "warn")
	viper.SetDefault("db.log.slow_query_threshold", "1s")
	// enforce: the subsystems wait up to db.schema.wait for oc3 db migrate, then
	// refuse to start on a schema missing a migration; warn: they log it and start.
	viper.SetDefault("db.schema.check", "enforce")
	viper.SetDefault("db.schema.wait", "5m")
	// connection pool of each subsystem: 0 means no limit for max_open_conns
	// and conn_max_lifetime, no idle connection kept for max_idle_conns.
	viper.SetDefault("db.max_open_conns", 20)
	viper.SetDefault("db.max_idle_conns", 20)
	viper.SetDefault("db.conn_max_lifetime", "2h")
}

func setDefaultRedisConfig() {
	viper.SetDefault("redis.db", 0)
	viper.SetDefault("redis.address", "localhost:6379")
	viper.SetDefault("redis.password", "")
}

func setDefaultAuthConfig() {
	viper.SetDefault("w2p_hmac", "sha512:7755f108-1b83-45dc-8302-54be8f3616a1")
}

func initConfig() error {
	// env
	viper.SetEnvPrefix("OC3")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// defaults
	setDefaultDBConfig()
	setDefaultRedisConfig()
	setDefaultFeederConfig()
	setDefaultServerConfig()
	setDefaultSchedulerConfig()
	setDefaultMessengerConfig()
	setDefaultMessengerConfig()
	setDefaultRunnerConfig()

	viper.SetDefault("git.user_email", "nobody@localhost.localdomain")

	// config file
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	for _, d := range configCandidateDirs {
		viper.AddConfigPath(d)
	}
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return err
		} else {
			return err
		}
	}
	return nil
}

func newEv() *oc2websocket.T {
	return &oc2websocket.T{
		Url: viper.GetString("messenger.url"),
		Key: []byte(viper.GetString("messenger.key")),
	}
}
