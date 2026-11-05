package cachekeys

const (
	QueuePrefix = "oc3:q:feed_"

	FeedDaemonPingQ        = "oc3:q:feed_daemon_ping"
	FeedDaemonPingH        = "oc3:h:feed_daemon_ping"
	FeedDaemonPingPendingH = "oc3:h:feed_daemon_ping_pending"

	FeedDaemonStatusChangesH = "oc3:h:feed_daemon_status_changes"
	FeedDaemonStatusH        = "oc3:h:feed_daemon_status"
	FeedDaemonStatusQ        = "oc3:q:feed_daemon_status"
	FeedDaemonStatusPendingH = "oc3:h:feed_daemon_status_pending"

	FeedInstanceResourceInfoH        = "oc3:h:feed_instance_resource_info"
	FeedInstanceResourceInfoQ        = "oc3:q:feed_instance_resource_info"
	FeedInstanceResourceInfoPendingH = "oc3:h:feed_instance_resource_info_pending"

	FeedNodeDiskH        = "oc3:h:feed_node_disk"
	FeedNodeDiskQ        = "oc3:q:feed_node_disk"
	FeedNodeDiskPendingH = "oc3:h:feed_node_disk_pending"

	FeedArrayH        = "oc3:h:feed_array"
	FeedArrayQ        = "oc3:q:feed_array"
	FeedArrayPendingH = "oc3:h:feed_array_pending"

	// FeedArrayProxyS is the prefix of the set of the nodes that pushed
	// an array since its last parse: FeedArrayProxyS + <type>@<name>.
	FeedArrayProxyS = "oc3:s:feed_array_proxy:"

	FeedSANSwitchH        = "oc3:h:feed_san_switch"
	FeedSANSwitchQ        = "oc3:q:feed_san_switch"
	FeedSANSwitchPendingH = "oc3:h:feed_san_switch_pending"

	FeedObjectConfigForClusterIDH = "oc3:h:feed_object_config_for_cluster_id"

	FeedObjectConfigH        = "oc3:h:feed_object_config"
	FeedObjectConfigQ        = "oc3:q:feed_object_config"
	FeedObjectConfigPendingH = "oc3:h:feed_object_config_pending"

	FeedSystemQ        = "oc3:q:feed_system"
	FeedSystemH        = "oc3:h:feed_system"
	FeedSystemPendingH = "oc3:h:feed_system_pending"

	FeedInstanceStatusH        = "oc3:h:feed_instance_status"
	FeedInstanceStatusQ        = "oc3:q:feed_instance_status"
	FeedInstanceStatusP        = "oc3:p:feed_instance_status"
	FeedInstanceStatusPendingH = "oc3:h:feed_instance_status_pending"

	FeedInstanceActionH        = "oc3:h:feed_instance_action"
	FeedInstanceActionQ        = "oc3:q:feed_instance_action"
	FeedInstanceActionPendingH = "oc3:h:feed_instance_action_pending"

	FeedSysreportQ = "oc3:q:feed_sysreport"

	// SysreportPurgeQ queues the purges of files from the sysreport history, run
	// by the scheduler; SysreportPurgePrefix keys their status, by purge id.
	SysreportPurgeQ      = "oc3:q:sysreport_purge"
	SysreportPurgePrefix = "oc3:sysreport_purge:"

	// PkgDiffPrefix keys the packages diffs computed, by the nodes compared and
	// their generations. PkgDiffGenPrefix keys the generation of the packages of a
	// node, incremented when they change; PkgDiffGen the generation of all of
	// them, incremented when old packages are scrubbed.
	PkgDiffPrefix    = "oc3:pkgdiff:"
	PkgDiffGenPrefix = "oc3:pkgdiff_gen:"
	PkgDiffGen       = "oc3:pkgdiff_gen"

	FeedNodeChecksH        = "oc3:h:feed_node_checks"
	FeedNodeChecksQ        = "oc3:q:feed_node_checks"
	FeedNodeChecksPendingH = "oc3:h:feed_node_checks_pending"
)
