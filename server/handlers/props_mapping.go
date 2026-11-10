package serverhandlers

import (
	"fmt"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/schema"
)

type propDef struct {
	Col     *schema.Col
	SQLExpr string
	Kind    string
	// FilterExpr is what a column filter on the prop compares, when it is not the
	// column itself: the message of a log event, say, whose text is split between
	// a format and the values filling it.
	FilterExpr string
}

func (p propDef) selectExpr() string {
	if p.SQLExpr != "" {
		return p.SQLExpr
	}
	if p.Col != nil {
		return p.Col.Qualified()
	}
	return ""
}

// clusterListCol is a computed column of the clusters list, which names its derived
// table after the clusters table.
func clusterListCol(name string) *schema.Col {
	return &schema.Col{T: schema.TClusters, Name: name, Nullable: true}
}

// notifyDeliveryCol is a column of the delivery lists, whose derived table is
// named after notification_deliveries.
func notifyDeliveryCol(name string) *schema.Col {
	return &schema.Col{T: &schema.Table{Name: "notification_deliveries"}, Name: name, Nullable: true}
}

// oidcMappingListCol is a computed column of the claim rule lists, whose derived
// table is named after auth_oidc_mappings.
func oidcMappingListCol(name string) *schema.Col {
	return &schema.Col{T: schema.TAuthOidcMappings, Name: name, Nullable: true}
}

// The orchestration tables, which the generated schema does not hold yet: the
// derived tables of the orchestrations and runs lists (cdb/db_orchestrations_list.go)
// are named after them. To drop once schema/tables.go is generated again from a
// database holding them.
var (
	tOrchestrationsList    = &schema.Table{Name: "orchestrations"}
	tOrchestrationRunsList = &schema.Table{Name: "orchestration_runs"}
)

// colIntNullable is an integer column whose NULL stays null: the last run of an
// orchestration never run, say.
func colIntNullable(c *schema.Col) propDef {
	return propDef{Col: c, Kind: "int64"}
}

func orchestrationListCol(name string) *schema.Col {
	return &schema.Col{T: tOrchestrationsList, Name: name, Nullable: true}
}

func orchestrationRunListCol(name string) *schema.Col {
	return &schema.Col{T: tOrchestrationRunsList, Name: name, Nullable: true}
}

func col(c *schema.Col) propDef {
	return propDef{Col: c}
}

func colStr(c *schema.Col) propDef {
	return propDef{Col: c, SQLExpr: fmt.Sprintf("COALESCE(%s, '')", c.Qualified()), Kind: "string"}
}

func colInt(c *schema.Col) propDef {
	return propDef{Col: c, SQLExpr: fmt.Sprintf("COALESCE(%s, 0)", c.Qualified()), Kind: "int64"}
}

type JoinDef struct {
	MappingKey string
}

type propMapping struct {
	Available []string
	// Default is the subset of Available returned when no props are requested.
	// If nil, all Available props are returned by default.
	Default   []string
	Blacklist map[string]struct{}
	// Props maps prop names to their propDef (column reference + optional SQL override).
	// Enables column pushdown: only requested columns are fetched from DB.
	Props map[string]propDef
	// Joins declares joinable tables. A prop "table.column" is valid when "table"
	// is a key in Joins and "column" is listed in JoinDef.Columns.
	Joins map[string]JoinDef
	// PrimaryKey is the primary key of the main table of the queries, which
	// buildOrderBy appends to a requested sort so that rows with equal sort values
	// keep one order and pages neither repeat nor skip rows. Several columns when a
	// row joins two tables. When nil, the column of the "id" prop, if any.
	PrimaryKey []*schema.Col
}

// primaryKey returns the columns ending a requested sort: PrimaryKey, or else the
// column of the "id" prop; none when the mapping has neither.
func (m propMapping) primaryKey() []*schema.Col {
	if len(m.PrimaryKey) > 0 {
		return m.PrimaryKey
	}
	if def, ok := m.Props["id"]; ok && def.Col != nil {
		return []*schema.Col{def.Col}
	}
	return nil
}

var propsMapping = map[string]propMapping{
	"action_queue": {
		Available: []string{
			"id", "status", "command", "date_queued", "date_dequeued", "ret",
			"stdout", "stderr", "action_type", "user_id", "form_id", "connect_to",
			"node_id", "svc_id",
		},
		Props: map[string]propDef{
			"id":            col(schema.ActionQueueID),
			"status":        colStr(schema.ActionQueueStatus),
			"command":       colStr(schema.ActionQueueCommand),
			"date_queued":   colStr(schema.ActionQueueDateQueued),
			"date_dequeued": colStr(schema.ActionQueueDateDequeued),
			"ret":           colInt(schema.ActionQueueRet),
			"stdout":        colStr(schema.ActionQueueStdout),
			"stderr":        colStr(schema.ActionQueueStderr),
			"action_type":   colStr(schema.ActionQueueActionType),
			"user_id":       colInt(schema.ActionQueueUserID),
			"form_id":       colInt(schema.ActionQueueFormID),
			"connect_to":    colStr(schema.ActionQueueConnectTo),
			"node_id":       colStr(schema.ActionQueueNodeID),
			"svc_id":        colStr(schema.ActionQueueSvcID),
		},
		// A queued action points at its node, and at its service when it has one, by
		// id only: the names live in the joined tables, as for dashboard entries.
		Joins: map[string]JoinDef{
			"nodes":    {MappingKey: "node"},
			"services": {MappingKey: "service"},
		},
	},
	"cluster": {
		Available: []string{"id", "cluster_id", "cluster_name"},
		Props: map[string]propDef{
			"id":           col(schema.ClustersID),
			"cluster_id":   colStr(schema.ClustersClusterID),
			"cluster_name": colStr(schema.ClustersClusterName),
		},
	},
	// The clusters as listed (GET /clusters): the columns of the derived table of
	// cdb/db_clusters.go, some read from the daemon status the cluster pushed. Apart
	// from "cluster", the props of the clusters joined to a node, which only has
	// the table's own columns.
	"clusterList": {
		Available: []string{
			"id", "cluster_id", "cluster_name", "node_count", "svc_count", "agent_versions",
			"cluster_nodes", "quorum", "frozen", "compat", "listener_port", "cluster_updated",
		},
		Props: map[string]propDef{
			"id":              col(schema.ClustersID),
			"cluster_id":      colStr(schema.ClustersClusterID),
			"cluster_name":    colStr(schema.ClustersClusterName),
			"node_count":      colInt(clusterListCol("node_count")),
			"svc_count":       colInt(clusterListCol("svc_count")),
			"agent_versions":  colStr(clusterListCol("agent_versions")),
			"cluster_nodes":   colStr(clusterListCol("cluster_nodes")),
			"quorum":          colInt(clusterListCol("quorum")),
			"frozen":          colInt(clusterListCol("frozen")),
			"compat":          colInt(clusterListCol("compat")),
			"listener_port":   colInt(clusterListCol("listener_port")),
			"cluster_updated": colStr(clusterListCol("cluster_updated")),
		},
	},
	"node": {
		// node_id is unique too, but id is the key of the table.
		PrimaryKey: []*schema.Col{schema.NodesID},
		Available: []string{
			"node_id", "nodename", "app", "node_env", "cluster_id",
			"loc_country", "loc_city", "loc_addr", "loc_building", "loc_floor", "loc_room", "loc_rack", "loc_zip",
			"cpu_freq", "cpu_cores", "cpu_dies", "cpu_vendor", "cpu_model", "cpu_threads",
			"mem_banks", "mem_slots", "mem_bytes",
			"os_name", "os_release", "os_update", "os_segment", "os_arch", "os_vendor", "os_kernel", "os_concat",
			"team_responsible", "team_integ", "team_support",
			"serial", "model", "manufacturer", "type", "assetname", "asset_env",
			"warranty_end", "maintenance_end",
			"status", "role", "sec_zone",
			"power_cabinet1", "power_cabinet2", "power_supply_nb", "power_protect", "power_protect_breaker", "power_breaker1", "power_breaker2",
			"blade_cabinet", "enclosure", "enclosureslot",
			"hv", "hvpool", "hvvdc",
			"fqdn", "connect_to", "listener_port", "version", "collector", "sp_version", "bios_version",
			"tz", "last_boot", "last_comm",
			"node_frozen", "node_frozen_at",
			"snooze_till", "notifications", "action_type",
			"hw_obs_warn_date", "hw_obs_alert_date", "os_obs_warn_date", "os_obs_alert_date",
			"updated",
		},
		Props: map[string]propDef{
			"node_id":               colStr(schema.NodesNodeID),
			"nodename":              colStr(schema.NodesNodename),
			"app":                   colStr(schema.NodesApp),
			"node_env":              colStr(schema.NodesNodeEnv),
			"cluster_id":            colStr(schema.NodesClusterID),
			"loc_country":           colStr(schema.NodesLocCountry),
			"loc_city":              colStr(schema.NodesLocCity),
			"loc_addr":              colStr(schema.NodesLocAddr),
			"loc_building":          colStr(schema.NodesLocBuilding),
			"loc_floor":             colStr(schema.NodesLocFloor),
			"loc_room":              colStr(schema.NodesLocRoom),
			"loc_rack":              colStr(schema.NodesLocRack),
			"loc_zip":               colStr(schema.NodesLocZip),
			"cpu_freq":              colStr(schema.NodesCPUFreq),
			"cpu_cores":             colInt(schema.NodesCPUCores),
			"cpu_dies":              colInt(schema.NodesCPUDies),
			"cpu_vendor":            colStr(schema.NodesCPUVendor),
			"cpu_model":             colStr(schema.NodesCPUModel),
			"cpu_threads":           colInt(schema.NodesCPUThreads),
			"mem_banks":             colInt(schema.NodesMEMBanks),
			"mem_slots":             colInt(schema.NodesMEMSlots),
			"mem_bytes":             colInt(schema.NodesMEMBytes),
			"os_name":               colStr(schema.NodesOSName),
			"os_release":            colStr(schema.NodesOSRelease),
			"os_update":             colStr(schema.NodesOSUpdate),
			"os_segment":            colStr(schema.NodesOSSegment),
			"os_arch":               colStr(schema.NodesOSArch),
			"os_vendor":             colStr(schema.NodesOSVendor),
			"os_kernel":             colStr(schema.NodesOSKernel),
			"os_concat":             colStr(schema.NodesOSConcat),
			"team_responsible":      colStr(schema.NodesTeamResponsible),
			"team_integ":            colStr(schema.NodesTeamInteg),
			"team_support":          colStr(schema.NodesTeamSupport),
			"serial":                colStr(schema.NodesSerial),
			"model":                 colStr(schema.NodesModel),
			"manufacturer":          colStr(schema.NodesManufacturer),
			"type":                  colStr(schema.NodesType),
			"assetname":             colStr(schema.NodesAssetname),
			"asset_env":             colStr(schema.NodesAssetEnv),
			"warranty_end":          colStr(schema.NodesWarrantyEnd),
			"maintenance_end":       colStr(schema.NodesMaintenanceEnd),
			"status":                colStr(schema.NodesStatus),
			"role":                  colStr(schema.NodesRole),
			"sec_zone":              colStr(schema.NodesSecZone),
			"power_cabinet1":        colStr(schema.NodesPowerCabinet1),
			"power_cabinet2":        colStr(schema.NodesPowerCabinet2),
			"power_supply_nb":       colInt(schema.NodesPowerSupplyNb),
			"power_protect":         colStr(schema.NodesPowerProtect),
			"power_protect_breaker": colStr(schema.NodesPowerProtectBreaker),
			"power_breaker1":        colStr(schema.NodesPowerBreaker1),
			"power_breaker2":        colStr(schema.NodesPowerBreaker2),
			"blade_cabinet":         colStr(schema.NodesBladeCabinet),
			"enclosure":             colStr(schema.NodesEnclosure),
			"enclosureslot":         colStr(schema.NodesEnclosureslot),
			"hv":                    colStr(schema.NodesHv),
			"hvpool":                colStr(schema.NodesHvpool),
			"hvvdc":                 colStr(schema.NodesHvvdc),
			"fqdn":                  colStr(schema.NodesFqdn),
			"connect_to":            colStr(schema.NodesConnectTo),
			"listener_port":         colInt(schema.NodesListenerPort),
			"version":               colStr(schema.NodesVersion),
			"collector":             colStr(schema.NodesCollector),
			"sp_version":            colStr(schema.NodesSpVersion),
			"bios_version":          colStr(schema.NodesBiosVersion),
			"tz":                    colStr(schema.NodesTz),
			"last_boot":             colStr(schema.NodesLastBoot),
			"last_comm":             colStr(schema.NodesLastComm),
			"node_frozen":           colStr(schema.NodesNodeFrozen),
			"node_frozen_at":        colStr(schema.NodesNodeFrozenAt),
			"snooze_till":           colStr(schema.NodesSnoozeTill),
			"notifications":         colStr(schema.NodesNotifications),
			"action_type":           colStr(schema.NodesActionType),
			"hw_obs_warn_date":      colStr(schema.NodesHWObsWarnDate),
			"hw_obs_alert_date":     colStr(schema.NodesHWObsAlertDate),
			"os_obs_warn_date":      colStr(schema.NodesOSObsWarnDate),
			"os_obs_alert_date":     colStr(schema.NodesOSObsAlertDate),
			"updated":               colStr(schema.NodesUpdated),
		},
		// A node names its cluster by id only: the name lives in the joined table, as
		// for the node and service names of an instance.
		Joins: map[string]JoinDef{
			"clusters": {MappingKey: "cluster"},
		},
	},
	"disk": {
		// A disk used by several services is one row per service (svcdisks).
		PrimaryKey: []*schema.Col{schema.DiskinfoID, schema.SvcdisksID},
		Available: []string{
			"disk_id", "disk_name", "disk_devid", "disk_vendor", "disk_model",
			"disk_size", "disk_used", "disk_alloc", "disk_raid", "disk_group",
			"disk_level", "disk_arrayid", "disk_dg", "disk_region",
			"node_id", "nodename", "svc_id", "svcname", "app", "updated",
		},
		Props: map[string]propDef{
			"disk_id":      col(schema.DiskinfoDiskID),
			"disk_name":    colStr(schema.DiskinfoDiskName),
			"disk_devid":   colStr(schema.DiskinfoDiskDevid),
			"disk_vendor":  colStr(schema.SvcdisksDiskVendor),
			"disk_model":   colStr(schema.SvcdisksDiskModel),
			"disk_size":    colInt(schema.DiskinfoDiskSize),
			"disk_used":    colInt(schema.SvcdisksDiskUsed),
			"disk_alloc":   colInt(schema.DiskinfoDiskAlloc),
			"disk_raid":    colStr(schema.DiskinfoDiskRaid),
			"disk_group":   colStr(schema.DiskinfoDiskGroup),
			"disk_level":   colInt(schema.DiskinfoDiskLevel),
			"disk_arrayid": colStr(schema.DiskinfoDiskArrayid),
			"disk_dg":      colStr(schema.SvcdisksDiskDG),
			"disk_region":  colStr(schema.SvcdisksDiskRegion),
			"node_id":      colStr(schema.SvcdisksNodeID),
			"nodename":     colStr(schema.NodesNodename),
			"svc_id":       colStr(schema.SvcdisksSvcID),
			"svcname":      colStr(schema.ServicesSvcname),
			"app":          colStr(schema.AppsApp),
			"updated":      colStr(schema.DiskinfoDiskUpdated),
		},
	},
	// The backups the backup servers index (saves), with the node and the service
	// each is of, as the historical Saves view lists them. save_size is in bytes.
	"save": {
		PrimaryKey: []*schema.Col{schema.SavesID},
		Available: []string{
			"id", "save_server", "save_id", "save_app", "node_id", "nodename", "svc_id", "svcname",
			"save_name", "save_group", "save_level", "save_size", "save_volume",
			"save_date", "save_retention",
		},
		Props: map[string]propDef{
			"id":             col(schema.SavesID),
			"save_server":    colStr(schema.SavesSaveServer),
			"save_id":        colStr(schema.SavesSaveID),
			"save_app":       colStr(schema.SavesSaveApp),
			"node_id":        colStr(schema.SavesNodeID),
			"nodename":       colStr(schema.NodesNodename),
			"svc_id":         colStr(schema.SavesSvcID),
			"svcname":        colStr(schema.ServicesSvcname),
			"save_name":      colStr(schema.SavesSaveName),
			"save_group":     colStr(schema.SavesSaveGroup),
			"save_level":     colStr(schema.SavesSaveLevel),
			"save_size":      colInt(schema.SavesSaveSize),
			"save_volume":    colStr(schema.SavesSaveVolume),
			"save_date":      colStr(schema.SavesSaveDate),
			"save_retention": colStr(schema.SavesSaveRetention),
		},
	},
	// The disk groups and the quota of an application code on each, read from the
	// v_disk_quota view: each prop names the view column it reads, for sorting
	// and filtering.
	"disk_quota": {
		Available: []string{
			"array_name", "array_model", "dg_name",
			"dg_free", "dg_used", "dg_size", "dg_reserved", "dg_reservable",
			"app", "quota", "quota_used",
			"dg_id", "array_id", "app_id", "id",
		},
		Props: map[string]propDef{
			"array_name":    {Col: cdb.DiskQuotaCol("array_name"), SQLExpr: "COALESCE(array_name, '') AS array_name", Kind: "string"},
			"array_model":   {Col: cdb.DiskQuotaCol("array_model"), SQLExpr: "COALESCE(array_model, '') AS array_model", Kind: "string"},
			"dg_name":       {Col: cdb.DiskQuotaCol("dg_name"), SQLExpr: "COALESCE(dg_name, '') AS dg_name", Kind: "string"},
			"dg_free":       {Col: cdb.DiskQuotaCol("dg_free"), SQLExpr: "COALESCE(dg_free, 0) AS dg_free", Kind: "int64"},
			"dg_used":       {Col: cdb.DiskQuotaCol("dg_used"), SQLExpr: "COALESCE(dg_used, 0) AS dg_used", Kind: "int64"},
			"dg_size":       {Col: cdb.DiskQuotaCol("dg_size"), SQLExpr: "COALESCE(dg_size, 0) AS dg_size", Kind: "int64"},
			"dg_reserved":   {Col: cdb.DiskQuotaCol("dg_reserved"), SQLExpr: "COALESCE(dg_reserved, 0) AS dg_reserved", Kind: "int64"},
			"dg_reservable": {Col: cdb.DiskQuotaCol("dg_reservable"), SQLExpr: "COALESCE(dg_reservable, 0) AS dg_reservable", Kind: "int64"},
			"app":           {Col: cdb.DiskQuotaCol("app"), SQLExpr: "COALESCE(app, '') AS app", Kind: "string"},
			"quota":         {Col: cdb.DiskQuotaCol("quota"), SQLExpr: "COALESCE(quota, 0) AS quota", Kind: "int64"},
			"quota_used":    {Col: cdb.DiskQuotaCol("quota_used"), SQLExpr: "COALESCE(quota_used, 0) AS quota_used", Kind: "int64"},
			"dg_id":         {Col: cdb.DiskQuotaCol("dg_id"), SQLExpr: "COALESCE(dg_id, 0) AS dg_id", Kind: "int64"},
			"array_id":      {Col: cdb.DiskQuotaCol("array_id"), SQLExpr: "COALESCE(array_id, 0) AS array_id", Kind: "int64"},
			"app_id":        {Col: cdb.DiskQuotaCol("app_id"), SQLExpr: "COALESCE(app_id, 0) AS app_id", Kind: "int64"},
			"id":            {Col: cdb.DiskQuotaCol("id"), SQLExpr: "id", Kind: "int64"},
		},
	},
	// Addresses of the nodes, read from the v_nodenetworks view. Each prop keeps
	// its expression and names the view column it reads, for sorting and filtering.
	"node_ip": {
		Available: []string{
			"id", "node_id", "nodename", "intf", "mac", "type", "addr", "mask", "updated", "flag_deprecated",
			"net_id", "net_name", "net_network", "net_netmask", "net_broadcast", "net_gateway",
			"net_begin", "net_end", "net_pvid", "net_prio", "net_comment", "net_team_responsible",
		},
		Default: []string{"node_id", "intf", "addr", "mask", "net_name", "net_network", "net_netmask", "net_gateway"},
		Props: map[string]propDef{
			"id":                   {Col: cdb.NodeNetworksCol("id"), SQLExpr: "id", Kind: "int64"},
			"node_id":              {Col: cdb.NodeNetworksCol("node_id"), SQLExpr: "COALESCE(node_id, '') AS node_id", Kind: "string"},
			"nodename":             {Col: cdb.NodeNetworksCol("nodename"), SQLExpr: "COALESCE(nodename, '') AS nodename", Kind: "string"},
			"intf":                 {Col: cdb.NodeNetworksCol("intf"), SQLExpr: "COALESCE(intf, '') AS intf", Kind: "string"},
			"mac":                  {Col: cdb.NodeNetworksCol("mac"), SQLExpr: "COALESCE(mac, '') AS mac", Kind: "string"},
			"type":                 {Col: cdb.NodeNetworksCol("addr_type"), SQLExpr: "COALESCE(addr_type, '') AS type", Kind: "string"},
			"addr":                 {Col: cdb.NodeNetworksCol("addr"), SQLExpr: "COALESCE(addr, '') AS addr", Kind: "string"},
			"mask":                 {Col: cdb.NodeNetworksCol("mask"), SQLExpr: "COALESCE(mask, '') AS mask", Kind: "string"},
			"updated":              {Col: cdb.NodeNetworksCol("addr_updated"), SQLExpr: "COALESCE(addr_updated, '') AS updated", Kind: "string"},
			"flag_deprecated":      {Col: cdb.NodeNetworksCol("flag_deprecated"), SQLExpr: "COALESCE(flag_deprecated, 0) AS flag_deprecated", Kind: "int64"},
			"net_id":               {Col: cdb.NodeNetworksCol("net_id"), SQLExpr: "net_id", Kind: "int64"},
			"net_name":             {Col: cdb.NodeNetworksCol("net_name"), SQLExpr: "COALESCE(net_name, '') AS net_name", Kind: "string"},
			"net_network":          {Col: cdb.NodeNetworksCol("net_network"), SQLExpr: "COALESCE(net_network, '') AS net_network", Kind: "string"},
			"net_netmask":          {Col: cdb.NodeNetworksCol("net_netmask"), SQLExpr: "COALESCE(net_netmask, '') AS net_netmask", Kind: "string"},
			"net_broadcast":        {Col: cdb.NodeNetworksCol("net_broadcast"), SQLExpr: "COALESCE(net_broadcast, '') AS net_broadcast", Kind: "string"},
			"net_gateway":          {Col: cdb.NodeNetworksCol("net_gateway"), SQLExpr: "COALESCE(net_gateway, '') AS net_gateway", Kind: "string"},
			"net_begin":            {Col: cdb.NodeNetworksCol("net_begin"), SQLExpr: "COALESCE(net_begin, '') AS net_begin", Kind: "string"},
			"net_end":              {Col: cdb.NodeNetworksCol("net_end"), SQLExpr: "COALESCE(net_end, '') AS net_end", Kind: "string"},
			"net_pvid":             {Col: cdb.NodeNetworksCol("net_pvid"), SQLExpr: "COALESCE(net_pvid, '') AS net_pvid", Kind: "string"},
			"net_prio":             {Col: cdb.NodeNetworksCol("prio"), SQLExpr: "COALESCE(prio, 0) AS net_prio", Kind: "int64"},
			"net_comment":          {Col: cdb.NodeNetworksCol("net_comment"), SQLExpr: "COALESCE(net_comment, '') AS net_comment", Kind: "string"},
			"net_team_responsible": {Col: cdb.NodeNetworksCol("net_team_responsible"), SQLExpr: "COALESCE(net_team_responsible, '') AS net_team_responsible", Kind: "string"},
		},
	},
	"node_interface": {
		Available: []string{"id", "node_id", "intf", "mac", "type", "addr", "mask", "updated", "flag_deprecated"},
		Default:   []string{"id", "node_id", "intf", "mac", "updated", "flag_deprecated"},
		Blacklist: map[string]struct{}{"type": {}, "addr": {}, "mask": {}},
		Props: map[string]propDef{
			"id":              col(schema.NodeIPID),
			"node_id":         colStr(schema.NodeIPNodeID),
			"intf":            colStr(schema.NodeIPIntf),
			"mac":             colStr(schema.NodeIPMac),
			"type":            colStr(schema.NodeIPType),
			"addr":            colStr(schema.NodeIPAddr),
			"mask":            colStr(schema.NodeIPMask),
			"updated":         colStr(schema.NodeIPUpdated),
			"flag_deprecated": colInt(schema.NodeIPFlagDeprecated),
		},
		Joins: map[string]JoinDef{
			"nodes": {
				MappingKey: "node",
			},
		},
	},
	"node_hw": {
		Available: []string{"id", "node_id", "hw_type", "hw_path", "hw_class", "hw_description", "hw_driver", "updated"},
		Default:   []string{"node_id", "hw_type", "hw_path", "hw_class", "hw_description", "hw_driver", "updated"},
		Props: map[string]propDef{
			"id":             col(schema.NodeHWID),
			"node_id":        colStr(schema.NodeHWNodeID),
			"hw_type":        colStr(schema.NodeHWHWType),
			"hw_path":        colStr(schema.NodeHWHWPath),
			"hw_class":       colStr(schema.NodeHWHWClass),
			"hw_description": colStr(schema.NodeHWHWDescription),
			"hw_driver":      colStr(schema.NodeHWHWDriver),
			"updated":        colStr(schema.NodeHWUpdated),
		},
		// A component carries only the node id: its name lives in the joined nodes
		// table, as in the historical nodes hardware table.
		Joins: map[string]JoinDef{
			"nodes": {MappingKey: "node"},
		},
	},
	// The ports of the SAN switches, read from the v_switches view, which adds to the
	// switches table what is plugged on the other end of each port: sw_rname, the
	// name of a node, an array or another switch, and node_id when it is a node. The
	// numbers stay null when the switch does not report them: slot 0 is a slot.
	"switch": {
		Available: []string{
			"id", "sw_fabric", "sw_name", "sw_index", "sw_slot", "sw_port", "sw_portspeed",
			"sw_portnego", "sw_porttype", "sw_portstate", "sw_portname", "sw_rportname",
			"sw_rname", "node_id", "sw_updated",
		},
		Props: map[string]propDef{
			"id":           col(schema.VSwitchesID),
			"sw_fabric":    colStr(schema.VSwitchesSwFabric),
			"sw_name":      colStr(schema.VSwitchesSwName),
			"sw_index":     col(schema.VSwitchesSwIndex),
			"sw_slot":      col(schema.VSwitchesSwSlot),
			"sw_port":      col(schema.VSwitchesSwPort),
			"sw_portspeed": col(schema.VSwitchesSwPortspeed),
			"sw_portnego":  colStr(schema.VSwitchesSwPortnego),
			"sw_porttype":  colStr(schema.VSwitchesSwPorttype),
			"sw_portstate": colStr(schema.VSwitchesSwPortstate),
			"sw_portname":  colStr(schema.VSwitchesSwPortname),
			"sw_rportname": colStr(schema.VSwitchesSwRportname),
			"sw_rname":     colStr(schema.VSwitchesSwRname),
			"node_id":      colStr(schema.VSwitchesNodeID),
			"sw_updated":   colStr(schema.VSwitchesSwUpdated),
		},
	},
	"hba": {
		Available: []string{"id", "node_id", "hba_id", "hba_type", "updated"},
		Props: map[string]propDef{
			"id":       col(schema.NodeHBAID),
			"node_id":  colStr(schema.NodeHBANodeID),
			"hba_id":   colStr(schema.NodeHBAHBAID),
			"hba_type": colStr(schema.NodeHBAHBAType),
			"updated":  colStr(schema.NodeHBAUpdated),
		},
	},
	// The array ports a node sees through its host bus adapters: the stor_zone
	// rows of the node, joined to the array owning each port, whose columns read
	// as stor_array.<column> and stor_array_tgtid.<column>.
	"node_target": {
		Available: []string{"id", "node_id", "hba_id", "tgt_id", "updated"},
		Default: []string{
			"hba_id", "tgt_id", "updated",
			"stor_array.id", "stor_array.array_name", "stor_array.array_model", "stor_array.array_comment",
		},
		Props: map[string]propDef{
			"id":      col(schema.StorZoneID),
			"node_id": colStr(schema.StorZoneNodeID),
			"hba_id":  colStr(schema.StorZoneHBAID),
			"tgt_id":  colStr(schema.StorZoneTgtID),
			"updated": colStr(schema.StorZoneUpdated),
		},
		Joins: map[string]JoinDef{
			"stor_array":       {MappingKey: "array"},
			"stor_array_tgtid": {MappingKey: "array_tgtid"},
		},
	},
	// The luns of an array, as the arrays report them (diskinfo), each with the
	// nodes and services using it (svcdisks), one row per use, and none for a lun
	// no node reports. The columns of each table read as <table>.<column>.
	"array_disk": {
		PrimaryKey: []*schema.Col{schema.DiskinfoID, schema.SvcdisksID},
		Available: []string{
			"id", "disk_id", "disk_devid", "disk_name", "disk_size", "disk_alloc", "disk_group",
			"disk_raid", "disk_level", "disk_controller", "disk_arrayid", "disk_updated", "disk_created",
		},
		// The historical API returned every column of its three tables: the forms
		// read the lun, its uses and its array from them without asking.
		Default: []string{
			"diskinfo.id", "diskinfo.disk_id", "diskinfo.disk_devid", "diskinfo.disk_name",
			"diskinfo.disk_size", "diskinfo.disk_alloc", "diskinfo.disk_group", "diskinfo.disk_raid",
			"diskinfo.disk_updated",
			"svcdisks.node_id", "svcdisks.svc_id", "svcdisks.disk_dg", "svcdisks.disk_used",
			"stor_array.id", "stor_array.array_name", "stor_array.array_model", "stor_array.array_comment",
		},
		Props: map[string]propDef{
			"id":              col(schema.DiskinfoID),
			"disk_id":         colStr(schema.DiskinfoDiskID),
			"disk_devid":      colStr(schema.DiskinfoDiskDevid),
			"disk_name":       colStr(schema.DiskinfoDiskName),
			"disk_size":       colInt(schema.DiskinfoDiskSize),
			"disk_alloc":      colInt(schema.DiskinfoDiskAlloc),
			"disk_group":      colStr(schema.DiskinfoDiskGroup),
			"disk_raid":       colStr(schema.DiskinfoDiskRaid),
			"disk_level":      colInt(schema.DiskinfoDiskLevel),
			"disk_controller": colStr(schema.DiskinfoDiskController),
			"disk_arrayid":    colStr(schema.DiskinfoDiskArrayid),
			"disk_updated":    colStr(schema.DiskinfoDiskUpdated),
			"disk_created":    colStr(schema.DiskinfoDiskCreated),
		},
		Joins: map[string]JoinDef{
			"diskinfo":   {MappingKey: "diskinfo"},
			"svcdisks":   {MappingKey: "svcdisks"},
			"stor_array": {MappingKey: "array"},
			"nodes":      {MappingKey: "node"},
			"services":   {MappingKey: "service"},
		},
	},
	// The columns of diskinfo, for the lists joining it.
	"diskinfo": {
		Available: []string{
			"id", "disk_id", "disk_devid", "disk_name", "disk_size", "disk_alloc", "disk_group",
			"disk_raid", "disk_level", "disk_controller", "disk_arrayid", "disk_updated", "disk_created",
		},
		Props: map[string]propDef{
			"id":              col(schema.DiskinfoID),
			"disk_id":         colStr(schema.DiskinfoDiskID),
			"disk_devid":      colStr(schema.DiskinfoDiskDevid),
			"disk_name":       colStr(schema.DiskinfoDiskName),
			"disk_size":       colInt(schema.DiskinfoDiskSize),
			"disk_alloc":      colInt(schema.DiskinfoDiskAlloc),
			"disk_group":      colStr(schema.DiskinfoDiskGroup),
			"disk_raid":       colStr(schema.DiskinfoDiskRaid),
			"disk_level":      colInt(schema.DiskinfoDiskLevel),
			"disk_controller": colStr(schema.DiskinfoDiskController),
			"disk_arrayid":    colStr(schema.DiskinfoDiskArrayid),
			"disk_updated":    colStr(schema.DiskinfoDiskUpdated),
			"disk_created":    colStr(schema.DiskinfoDiskCreated),
		},
	},
	// The columns of svcdisks, the disks as the nodes report them, for the lists
	// joining it.
	"svcdisks": {
		Available: []string{
			"id", "disk_id", "node_id", "svc_id", "app_id", "disk_size", "disk_used",
			"disk_vendor", "disk_model", "disk_dg", "disk_local", "disk_region", "disk_updated",
		},
		Props: map[string]propDef{
			"id":           col(schema.SvcdisksID),
			"disk_id":      colStr(schema.SvcdisksDiskID),
			"node_id":      colStr(schema.SvcdisksNodeID),
			"svc_id":       colStr(schema.SvcdisksSvcID),
			"app_id":       colInt(schema.SvcdisksAppID),
			"disk_size":    colInt(schema.SvcdisksDiskSize),
			"disk_used":    colInt(schema.SvcdisksDiskUsed),
			"disk_vendor":  colStr(schema.SvcdisksDiskVendor),
			"disk_model":   colStr(schema.SvcdisksDiskModel),
			"disk_dg":      colStr(schema.SvcdisksDiskDG),
			"disk_local":   colStr(schema.SvcdisksDiskLocal),
			"disk_region":  colStr(schema.SvcdisksDiskRegion),
			"disk_updated": colStr(schema.SvcdisksDiskUpdated),
		},
	},
	// The disk groups of an array, the columns of the array reachable as
	// stor_array.<column>. The sizes are in MB.
	"array_dg": {
		Available: []string{
			"id", "array_id", "dg_name", "dg_size", "dg_used", "dg_free", "dg_reserved", "dg_updated",
		},
		Props: map[string]propDef{
			"id":          col(schema.StorArrayDGID),
			"array_id":    colStr(schema.StorArrayDGArrayID),
			"dg_name":     colStr(schema.StorArrayDGDGName),
			"dg_size":     colInt(schema.StorArrayDGDGSize),
			"dg_used":     colInt(schema.StorArrayDGDGUsed),
			"dg_free":     colInt(schema.StorArrayDGDGFree),
			"dg_reserved": colInt(schema.StorArrayDGDGReserved),
			"dg_updated":  colStr(schema.StorArrayDGDGUpdated),
		},
		Joins: map[string]JoinDef{
			"stor_array": {MappingKey: "array"},
		},
	},
	// The nodes inventorying an array, the columns of the node reachable as
	// nodes.<column>.
	"array_proxy": {
		Available: []string{"id", "array_id", "node_id"},
		Props: map[string]propDef{
			"id":       col(schema.StorArrayProxyID),
			"array_id": colStr(schema.StorArrayProxyArrayID),
			"node_id":  colStr(schema.StorArrayProxyNodeID),
		},
		Joins: map[string]JoinDef{
			"nodes": {MappingKey: "node"},
		},
	},
	"array_tgtid": {
		Available: []string{"id", "array_id", "array_tgtid", "updated"},
		Props: map[string]propDef{
			"id":          col(schema.StorArrayTgtidID),
			"array_id":    colStr(schema.StorArrayTgtidArrayID),
			"array_tgtid": colStr(schema.StorArrayTgtidArrayTgtid),
			"updated":     colStr(schema.StorArrayTgtidUpdated),
		},
	},
	"array": {
		Available: []string{
			"id", "array_name", "array_comment", "array_model",
			"array_firmware", "array_cache", "array_updated", "array_level",
		},
		Props: map[string]propDef{
			"id":             col(schema.StorArrayID),
			"array_name":     colStr(schema.StorArrayArrayName),
			"array_comment":  colStr(schema.StorArrayArrayComment),
			"array_model":    colStr(schema.StorArrayArrayModel),
			"array_firmware": colStr(schema.StorArrayArrayFirmware),
			"array_cache":    colInt(schema.StorArrayArrayCache),
			"array_updated":  colStr(schema.StorArrayArrayUpdated),
			"array_level":    colInt(schema.StorArrayArrayLevel),
		},
	},
	"app": {
		Available: []string{"id", "app", "updated", "app_domain", "app_team_ops", "description"},
		Props: map[string]propDef{
			"id":           col(schema.AppsID),
			"app":          col(schema.AppsApp),
			"updated":      colStr(schema.AppsUpdated),
			"app_domain":   colStr(schema.AppsAppDomain),
			"app_team_ops": colStr(schema.AppsAppTeamOps),
			"description":  colStr(schema.AppsDescription),
		},
	},
	// Read from the v_obsolescence view, declared in schema/views.go: its columns
	// give the props a column to sort and filter on.
	"obsolescence": {
		Available: []string{
			"id", "obs_type", "obs_name", "obs_warn_date", "obs_alert_date",
			"obs_warn_date_updated_by", "obs_alert_date_updated_by",
			"obs_warn_date_updated", "obs_alert_date_updated", "obs_count",
		},
		Props: map[string]propDef{
			"id":                        {Col: schema.VObsolescenceID, SQLExpr: "v_obsolescence.id", Kind: "int64"},
			"obs_type":                  {Col: schema.VObsolescenceObsType, SQLExpr: "v_obsolescence.obs_type", Kind: "string"},
			"obs_name":                  {Col: schema.VObsolescenceObsName, SQLExpr: "v_obsolescence.obs_name", Kind: "string"},
			"obs_warn_date":             {Col: schema.VObsolescenceObsWarnDate, SQLExpr: "COALESCE(v_obsolescence.obs_warn_date, '')", Kind: "string"},
			"obs_alert_date":            {Col: schema.VObsolescenceObsAlertDate, SQLExpr: "COALESCE(v_obsolescence.obs_alert_date, '')", Kind: "string"},
			"obs_warn_date_updated_by":  {Col: schema.VObsolescenceObsWarnDateUpdatedBy, SQLExpr: "v_obsolescence.obs_warn_date_updated_by", Kind: "string"},
			"obs_alert_date_updated_by": {Col: schema.VObsolescenceObsAlertDateUpdBy, SQLExpr: "v_obsolescence.obs_alert_date_updated_by", Kind: "string"},
			"obs_warn_date_updated":     {Col: schema.VObsolescenceObsWarnDateUpdated, SQLExpr: "v_obsolescence.obs_warn_date_updated", Kind: "string"},
			"obs_alert_date_updated":    {Col: schema.VObsolescenceObsAlertDateUpdated, SQLExpr: "v_obsolescence.obs_alert_date_updated", Kind: "string"},
			"obs_count":                 {Col: schema.VObsolescenceObsCount, SQLExpr: "v_obsolescence.obs_count", Kind: "int64"},
		},
	},
	"auth_group": {
		Available: []string{"id", "role", "privilege", "description"},
		Props: map[string]propDef{
			"id":          col(schema.AuthGroupID),
			"role":        colStr(schema.AuthGroupRole),
			"privilege":   colStr(schema.AuthGroupPrivilege),
			"description": colStr(schema.AuthGroupDescription),
		},
	},
	// Forms, as the historical collector lists them: form_definition is not a
	// column but form_yaml parsed, computed by the forms handlers.
	"form": {
		Available: []string{
			"id", "form_name", "form_yaml", "form_author", "form_created",
			"form_type", "form_folder", "form_definition",
		},
		Props: map[string]propDef{
			"id":           col(schema.FormsID),
			"form_name":    colStr(schema.FormsFormName),
			"form_yaml":    colStr(schema.FormsFormYaml),
			"form_author":  colStr(schema.FormsFormAuthor),
			"form_created": colStr(schema.FormsFormCreated),
			"form_type":    colStr(schema.FormsFormType),
			"form_folder":  colStr(schema.FormsFormFolder),
		},
	},
	// Form revisions: the definition of a form as submitted, kept by md5.
	"form_revision": {
		Available: []string{
			"id", "form_yaml", "form_md5", "form_date", "form_id",
			"form_folder", "form_name", "form_definition",
		},
		Props: map[string]propDef{
			"id":          col(schema.FormsRevisionsID),
			"form_yaml":   colStr(schema.FormsRevisionsFormYaml),
			"form_md5":    colStr(schema.FormsRevisionsFormMD5),
			"form_date":   colStr(schema.FormsRevisionsFormDate),
			"form_id":     col(schema.FormsRevisionsFormID),
			"form_folder": colStr(schema.FormsRevisionsFormFolder),
			"form_name":   colStr(schema.FormsRevisionsFormName),
		},
	},
	// Forms stored by workflows, joined to the revision they were submitted with,
	// as the historical collector's v_forms_store view does: the revision columns
	// keep their own name, flat.
	"form_store": {
		Available: []string{
			"id", "results_id", "form_submitter", "form_submit_date", "form_data",
			"form_next_id", "form_prev_id", "form_assignee", "form_head_id",
			"form_md5", "form_var_id", "form_yaml", "form_date", "form_id",
			"form_folder", "form_name", "form_definition",
		},
		Props: map[string]propDef{
			"id":               col(schema.FormsStoreID),
			"results_id":       col(schema.FormsStoreResultsID),
			"form_submitter":   colStr(schema.FormsStoreFormSubmitter),
			"form_submit_date": colStr(schema.FormsStoreFormSubmitDate),
			"form_data":        colStr(schema.FormsStoreFormData),
			"form_next_id":     col(schema.FormsStoreFormNextID),
			"form_prev_id":     col(schema.FormsStoreFormPrevID),
			"form_assignee":    colStr(schema.FormsStoreFormAssignee),
			"form_head_id":     col(schema.FormsStoreFormHeadID),
			"form_md5":         colStr(schema.FormsStoreFormMD5),
			"form_var_id":      col(schema.FormsStoreFormVarID),
			"form_yaml":        colStr(schema.FormsRevisionsFormYaml),
			"form_date":        colStr(schema.FormsRevisionsFormDate),
			"form_id":          col(schema.FormsRevisionsFormID),
			"form_folder":      colStr(schema.FormsRevisionsFormFolder),
			"form_name":        colStr(schema.FormsRevisionsFormName),
		},
	},
	// Files of the collector safe, a row per version, as the historical v_safe
	// view lists them: with the uploader's name and the groups linked. md5,
	// sha256 and size are those of the plaintext; encrypted tells whether the
	// file is encrypted at rest. The encryption keys are never props.
	"safe": {
		Available: []string{
			"id", "uuid", "name", "size", "md5", "sha256", "uploader", "uploader_name",
			"uploaded_from", "uploaded_date", "encrypted", "safe_team_publication", "safe_team_responsible",
		},
		Default: []string{
			"id", "uuid", "name", "size", "md5", "uploader", "uploader_name", "uploaded_from",
			"uploaded_date", "encrypted", "safe_team_publication", "safe_team_responsible",
		},
		Props: map[string]propDef{
			"id":            col(schema.SafeID),
			"uuid":          colStr(schema.SafeUuid),
			"name":          colStr(schema.SafeName),
			"size":          colInt(schema.SafeSize),
			"md5":           colStr(schema.SafeMD5),
			"sha256":        {Col: schema.SafeSha256, Kind: "string"},
			"uploader":      {Col: schema.SafeUploader, Kind: "int64"},
			"uploaded_from": colStr(schema.SafeUploadedFrom),
			"uploaded_date": colStr(schema.SafeUploadedDate),
			"uploader_name": {
				SQLExpr: "TRIM(CONCAT(COALESCE(auth_user.first_name, ''), ' ', COALESCE(auth_user.last_name, '')))",
				Kind:    "string",
			},
			"encrypted": {
				SQLExpr: "(safe.enc_version IS NOT NULL AND safe.enc_uuid = safe.uuid)",
				Kind:    "bool",
			},
			"safe_team_publication": {
				SQLExpr: "(SELECT GROUP_CONCAT(ag.role ORDER BY ag.role SEPARATOR ', ') FROM safe_team_publication x" +
					" JOIN auth_group ag ON ag.id = x.group_id WHERE x.file_id = safe.id)",
				Kind: "string",
			},
			"safe_team_responsible": {
				SQLExpr: "(SELECT GROUP_CONCAT(ag.role ORDER BY ag.role SEPARATOR ', ') FROM safe_team_responsible x" +
					" JOIN auth_group ag ON ag.id = x.group_id WHERE x.file_id = safe.id)",
				Kind: "string",
			},
		},
	},
	// Workflows chaining stored forms: the requests. form_name, form_folder and
	// form_yaml are those of the form revision the request started from, joined
	// by form_md5 as in the historical workflows table; they are not among the
	// default props, which stay those of the workflows table (the /dump output).
	"workflow": {
		Available: []string{
			"id", "form_head_id", "status", "steps", "creator", "create_date",
			"last_assignee", "last_update", "form_md5", "last_form_id", "last_form_name",
			"form_name", "form_folder", "form_yaml",
		},
		Default: []string{
			"id", "form_head_id", "status", "steps", "creator", "create_date",
			"last_assignee", "last_update", "form_md5", "last_form_id", "last_form_name",
		},
		Props: map[string]propDef{
			"id":             col(schema.WorkflowsID),
			"form_head_id":   col(schema.WorkflowsFormHeadID),
			"status":         colStr(schema.WorkflowsStatus),
			"steps":          col(schema.WorkflowsSteps),
			"creator":        colStr(schema.WorkflowsCreator),
			"create_date":    colStr(schema.WorkflowsCreateDate),
			"last_assignee":  colStr(schema.WorkflowsLastAssignee),
			"last_update":    colStr(schema.WorkflowsLastUpdate),
			"form_md5":       colStr(schema.WorkflowsFormMD5),
			"last_form_id":   col(schema.WorkflowsLastFormID),
			"last_form_name": colStr(schema.WorkflowsLastFormName),
			"form_name":      colStr(schema.FormsRevisionsFormName),
			"form_folder":    colStr(schema.FormsRevisionsFormFolder),
			"form_yaml":      colStr(schema.FormsRevisionsFormYaml),
		},
	},
	// The users. password, registration_key and reset_password_key are no props:
	// they are credentials, and a prop can also be filtered and sorted on, which
	// would let a caller guess a value it cannot read. The reset key alone is
	// enough to change the password of an account while a reset is pending.
	"user": {
		Available: []string{
			"id", "username", "email", "first_name", "last_name", "phone_work",
			"im_type", "im_username", "email_notifications", "im_notifications",
			"email_log_level", "im_log_level", "email_notifications_delay",
			"im_notifications_delay", "lock_filter", "registration_id",
			"quota_app", "quota_org_group", "quota_docker_registries",
			"oidc_linked",
		},
		Props: map[string]propDef{
			"id":                        col(schema.AuthUserID),
			"username":                  colStr(schema.AuthUserUsername),
			"email":                     colStr(schema.AuthUserEmail),
			"first_name":                colStr(schema.AuthUserFirstName),
			"last_name":                 colStr(schema.AuthUserLastName),
			"phone_work":                colStr(schema.AuthUserPhoneWork),
			"im_type":                   colStr(schema.AuthUserImType),
			"im_username":               colStr(schema.AuthUserImUsername),
			"email_notifications":       colStr(schema.AuthUserEmailNotifications),
			"im_notifications":          colStr(schema.AuthUserImNotifications),
			"email_log_level":           colStr(schema.AuthUserEmailLogLevel),
			"im_log_level":              colStr(schema.AuthUserImLogLevel),
			"email_notifications_delay": colStr(schema.AuthUserEmailNotificationsDelay),
			"im_notifications_delay":    colStr(schema.AuthUserImNotificationsDelay),
			"lock_filter":               colStr(schema.AuthUserLockFilter),
			"registration_id":           colStr(schema.AuthUserRegistrationID),
			"quota_app":                 colStr(schema.AuthUserQuotaApp),
			"quota_org_group":           colStr(schema.AuthUserQuotaOrgGroup),
			"quota_docker_registries":   colStr(schema.AuthUserQuotaDockerRegistries),
			// An OpenID Connect identity is linked to the account: its password is
			// the provider's, which POST /users/{user_id}/password refuses to set.
			"oidc_linked": {
				SQLExpr: "EXISTS(SELECT 1 FROM auth_user_identities i WHERE i.user_id = auth_user.id)",
				Kind:    "bool",
			},
		},
	},
	// The orchestrations as listed (GET /orchestrations): the columns of the derived
	// table of cdb/db_orchestrations_list.go. steps is the JSON of the steps; a
	// filter on it reads the actions of the steps, comma-separated.
	"orchestrationList": {
		Available: []string{
			"id", "name", "description", "steps", "step_count", "created", "updated",
			"updated_by", "run_count", "last_run_id", "last_run_status", "last_run_started",
		},
		Props: map[string]propDef{
			"id":               colIntNullable(orchestrationListCol("id")),
			"name":             colStr(orchestrationListCol("name")),
			"description":      colStr(orchestrationListCol("description")),
			"steps":            {Col: orchestrationListCol("steps"), SQLExpr: "COALESCE(orchestrations.steps, '[]')", Kind: "string", FilterExpr: "orchestrations.step_actions"},
			"step_count":       colInt(orchestrationListCol("step_count")),
			"created":          col(orchestrationListCol("created")),
			"updated":          col(orchestrationListCol("updated")),
			"updated_by":       colStr(orchestrationListCol("updated_by")),
			"run_count":        colInt(orchestrationListCol("run_count")),
			"last_run_id":      colIntNullable(orchestrationListCol("last_run_id")),
			"last_run_status":  col(orchestrationListCol("last_run_status")),
			"last_run_started": col(orchestrationListCol("last_run_started")),
		},
	},
	// The runs as listed (GET /orchestration_runs): the columns of the derived table
	// of cdb/db_orchestrations_list.go. duration is in seconds, up to now while
	// the run goes on.
	"orchestrationRunList": {
		Available: []string{
			"id", "orchestration_id", "name", "status", "current_step", "message", "started",
			"ended", "started_by", "step_count", "steps_done", "duration",
		},
		Props: map[string]propDef{
			"id":               colIntNullable(orchestrationRunListCol("id")),
			"orchestration_id": colIntNullable(orchestrationRunListCol("orchestration_id")),
			"name":             colStr(orchestrationRunListCol("name")),
			"status":           colStr(orchestrationRunListCol("status")),
			"current_step":     colInt(orchestrationRunListCol("current_step")),
			"message":          colStr(orchestrationRunListCol("message")),
			"started":          col(orchestrationRunListCol("started")),
			"ended":            col(orchestrationRunListCol("ended")),
			"started_by":       colStr(orchestrationRunListCol("started_by")),
			"step_count":       colInt(orchestrationRunListCol("step_count")),
			"steps_done":       colInt(orchestrationRunListCol("steps_done")),
			"duration":         colInt(orchestrationRunListCol("duration")),
		},
	},
	"tag": {
		Available: []string{"id", "tag_name", "tag_created", "tag_exclude", "tag_data", "tag_id"},
		Blacklist: map[string]struct{}{"id": {}},
		Props: map[string]propDef{
			"id":          col(schema.TagsID),
			"tag_name":    colStr(schema.TagsTagName),
			"tag_created": colStr(schema.TagsTagCreated),
			"tag_exclude": colStr(schema.TagsTagExclude),
			"tag_data":    col(schema.TagsTagData),
			"tag_id":      colStr(schema.TagsTagID),
		},
	},
	// The tags of one node, or of one service: the tag's props, and the data of the
	// attachment, which a form-based tag ("<form>::<name>") describes with its form.
	"nodeTagList": {
		Available: []string{"id", "tag_name", "tag_created", "tag_exclude", "tag_data", "tag_id", "tag_attach_data"},
		Blacklist: map[string]struct{}{"id": {}},
		Props: map[string]propDef{
			"id":              col(schema.TagsID),
			"tag_name":        colStr(schema.TagsTagName),
			"tag_created":     colStr(schema.TagsTagCreated),
			"tag_exclude":     colStr(schema.TagsTagExclude),
			"tag_data":        col(schema.TagsTagData),
			"tag_id":          colStr(schema.TagsTagID),
			"tag_attach_data": colStr(schema.NodeTagsTagAttachData),
		},
	},
	"svcTagList": {
		Available: []string{"id", "tag_name", "tag_created", "tag_exclude", "tag_data", "tag_id", "tag_attach_data"},
		Blacklist: map[string]struct{}{"id": {}},
		Props: map[string]propDef{
			"id":              col(schema.TagsID),
			"tag_name":        colStr(schema.TagsTagName),
			"tag_created":     colStr(schema.TagsTagCreated),
			"tag_exclude":     colStr(schema.TagsTagExclude),
			"tag_data":        col(schema.TagsTagData),
			"tag_id":          colStr(schema.TagsTagID),
			"tag_attach_data": colStr(schema.SvcTagsTagAttachData),
		},
	},
	"node_tag": {
		Available: []string{"id", "created", "node_id", "tag_id", "tag_attach_data"},
		Blacklist: map[string]struct{}{"id": {}},
		Props: map[string]propDef{
			"id":              col(schema.NodeTagsID),
			"created":         colStr(schema.NodeTagsCreated),
			"node_id":         colStr(schema.NodeTagsNodeID),
			"tag_id":          colStr(schema.NodeTagsTagID),
			"tag_attach_data": colStr(schema.NodeTagsTagAttachData),
		},
		// The node of an attachment, joined for its name.
		Joins: map[string]JoinDef{
			"nodes": {MappingKey: "node"},
		},
	},
	"svc_tag": {
		Available: []string{"id", "created", "svc_id", "tag_id", "tag_attach_data"},
		Blacklist: map[string]struct{}{"id": {}},
		Props: map[string]propDef{
			"id":              col(schema.SvcTagsID),
			"created":         colStr(schema.SvcTagsCreated),
			"svc_id":          colStr(schema.SvcTagsSvcID),
			"tag_id":          colStr(schema.SvcTagsTagID),
			"tag_attach_data": colStr(schema.SvcTagsTagAttachData),
		},
		// The service of an attachment, joined for its name.
		Joins: map[string]JoinDef{
			"services": {MappingKey: "service"},
		},
	},
	"service": {
		Available: []string{
			"id", "svc_id", "svcname", "cluster_id",
			"svc_status", "svc_availstatus",
			"svc_app", "svc_env", "svc_ha",
			"svc_topology", "svc_frozen", "svc_placement", "svc_provisioned",
			"svc_flex_min_nodes", "svc_flex_max_nodes", "svc_flex_target",
			"svc_flex_cpu_low_threshold", "svc_flex_cpu_high_threshold",
			"svc_autostart",
			"svc_nodes", "svc_drpnode", "svc_drpnodes", "svc_drptype",
			"svc_comment", "svc_created", "svc_status_updated",
			"svc_hostid", "svc_wave", "svc_config", "svc_config_updated",
			"svc_metrocluster", "svc_drnoaction",
			"svc_notifications", "svc_snooze_till",
			"svc_sla", "svc_availability", "svc_availability_trend", "svc_availability_updated",
			"updated",
		},
		Props: map[string]propDef{
			// The availability target in percent; null without SLA.
			"svc_sla": {Col: schema.ServicesSvcSla, Kind: "float64"},
			// The availability rate of the last 30 days in percent, stored by the
			// scheduler; null without status recorded.
			"svc_availability":            {Col: schema.ServicesSvcAvailability, Kind: "float64"},
			"svc_availability_trend":      {Col: schema.ServicesSvcAvailabilityTrend, Kind: "float64"},
			"svc_availability_updated":    colStr(schema.ServicesSvcAvailabilityUpdated),
			"id":                          col(schema.ServicesID),
			"svc_id":                      colStr(schema.ServicesSvcID),
			"svcname":                     colStr(schema.ServicesSvcname),
			"cluster_id":                  colStr(schema.ServicesClusterID),
			"svc_status":                  colStr(schema.ServicesSvcStatus),
			"svc_availstatus":             colStr(schema.ServicesSvcAvailstatus),
			"svc_app":                     colStr(schema.ServicesSvcApp),
			"svc_env":                     colStr(schema.ServicesSvcEnv),
			"svc_ha":                      colInt(schema.ServicesSvcHa),
			"svc_topology":                colStr(schema.ServicesSvcTopology),
			"svc_frozen":                  colStr(schema.ServicesSvcFrozen),
			"svc_placement":               colStr(schema.ServicesSvcPlacement),
			"svc_provisioned":             colStr(schema.ServicesSvcProvisioned),
			"svc_flex_min_nodes":          colInt(schema.ServicesSvcFlexMinNodes),
			"svc_flex_max_nodes":          colInt(schema.ServicesSvcFlexMaxNodes),
			"svc_flex_target":             colInt(schema.ServicesSvcFlexTarget),
			"svc_flex_cpu_low_threshold":  colInt(schema.ServicesSvcFlexCPULowThreshold),
			"svc_flex_cpu_high_threshold": colInt(schema.ServicesSvcFlexCPUHighThreshold),
			"svc_autostart":               colStr(schema.ServicesSvcAutostart),
			"svc_nodes":                   colStr(schema.ServicesSvcNodes),
			"svc_drpnode":                 colStr(schema.ServicesSvcDrpnode),
			"svc_drpnodes":                colStr(schema.ServicesSvcDrpnodes),
			"svc_drptype":                 colStr(schema.ServicesSvcDrptype),
			"svc_comment":                 colStr(schema.ServicesSvcComment),
			"svc_created":                 colStr(schema.ServicesSvcCreated),
			"svc_status_updated":          colStr(schema.ServicesSvcStatusUpdated),
			"svc_hostid":                  colStr(schema.ServicesSvcHostid),
			"svc_wave":                    colInt(schema.ServicesSvcWave),
			"svc_config":                  colStr(schema.ServicesSvcConfig),
			"svc_config_updated":          colStr(schema.ServicesSvcConfigUpdated),
			"svc_metrocluster":            colStr(schema.ServicesSvcMetrocluster),
			"svc_drnoaction":              colStr(schema.ServicesSvcDrnoaction),
			"svc_notifications":           colStr(schema.ServicesSvcNotifications),
			"svc_snooze_till":             colStr(schema.ServicesSvcSnoozeTill),
			"updated":                     colStr(schema.ServicesUpdated),
		},
	},
	"instance": {
		PrimaryKey: []*schema.Col{schema.SvcmonID},
		Available: []string{
			"svc_id", "node_id",
			"mon_svctype",
			"mon_availstatus", "mon_overallstatus",
			"mon_smon_status", "mon_smon_global_expect",
			"mon_ipstatus", "mon_fsstatus", "mon_diskstatus", "mon_containerstatus",
			"mon_sharestatus", "mon_syncstatus", "mon_appstatus", "mon_hbstatus",
			"mon_frozen", "mon_frozen_at", "mon_encap_frozen_at",
			"mon_vmname", "mon_vmtype", "mon_guestos", "mon_vcpus", "mon_vmem",
			"mon_updated", "mon_changed",
			"err",
		},
		Default: []string{
			"svc_id", "node_id",
			"mon_availstatus", "mon_overallstatus", "mon_smon_status",
			"mon_frozen", "mon_updated",
		},
		Props: map[string]propDef{
			"svc_id":                 colStr(schema.SvcmonSvcID),
			"node_id":                colStr(schema.SvcmonNodeID),
			"mon_svctype":            colStr(schema.SvcmonMonSvctype),
			"mon_availstatus":        colStr(schema.SvcmonMonAvailstatus),
			"mon_overallstatus":      colStr(schema.SvcmonMonOverallstatus),
			"mon_smon_status":        colStr(schema.SvcmonMonSmonStatus),
			"mon_smon_global_expect": colStr(schema.SvcmonMonSmonGlobalExpect),
			"mon_ipstatus":           colStr(schema.SvcmonMonIpstatus),
			"mon_fsstatus":           colStr(schema.SvcmonMonFsstatus),
			"mon_diskstatus":         colStr(schema.SvcmonMonDiskstatus),
			"mon_containerstatus":    colStr(schema.SvcmonMonContainerstatus),
			"mon_sharestatus":        colStr(schema.SvcmonMonSharestatus),
			"mon_syncstatus":         colStr(schema.SvcmonMonSyncstatus),
			"mon_appstatus":          colStr(schema.SvcmonMonAppstatus),
			"mon_hbstatus":           colStr(schema.SvcmonMonHbstatus),
			"mon_frozen":             colStr(schema.SvcmonMonFrozen),
			"mon_frozen_at":          colStr(schema.SvcmonMonFrozenAt),
			"mon_encap_frozen_at":    colStr(schema.SvcmonMonEncapFrozenAt),
			"mon_vmname":             colStr(schema.SvcmonMonVmname),
			"mon_vmtype":             colStr(schema.SvcmonMonVmtype),
			"mon_guestos":            colStr(schema.SvcmonMonGuestos),
			"mon_vcpus":              colInt(schema.SvcmonMonVcpus),
			"mon_vmem":               colInt(schema.SvcmonMonVmem),
			"mon_updated":            colStr(schema.SvcmonMonUpdated),
			"mon_changed":            colStr(schema.SvcmonMonChanged),
			// Unacknowledged action errors of the last day, as the historical
			// collector's "err" column: 0 for an instance without any.
			"err": {
				Col:        schema.BActionErrorsErr,
				SQLExpr:    "COALESCE(b_action_errors.err, 0)",
				Kind:       "int64",
				FilterExpr: "COALESCE(b_action_errors.err, 0)",
			},
		},
		// An instance row carries only the service and node ids: the names live in
		// the joined tables, as in the historical collector's svcmon table.
		Joins: map[string]JoinDef{
			"nodes":    {MappingKey: "node"},
			"services": {MappingKey: "service"},
		},
	},
	// Packages installed on the nodes, as the historical packages table lists them.
	// sig_provider names the provider of the signing key, from pkg_sig_provider;
	// it is empty when the signature is unknown.
	"package": {
		Available: []string{
			"id", "node_id",
			"pkg_name", "pkg_version", "pkg_arch", "pkg_type",
			"pkg_sig", "sig_provider",
			"pkg_install_date", "pkg_updated",
		},
		Default: []string{
			"id", "node_id",
			"pkg_name", "pkg_version", "pkg_arch", "pkg_type",
			"sig_provider", "pkg_install_date", "pkg_updated",
		},
		Props: map[string]propDef{
			"id":               col(schema.PackagesID),
			"node_id":          colStr(schema.PackagesNodeID),
			"pkg_name":         colStr(schema.PackagesPkgName),
			"pkg_version":      colStr(schema.PackagesPkgVersion),
			"pkg_arch":         colStr(schema.PackagesPkgArch),
			"pkg_type":         colStr(schema.PackagesPkgType),
			"pkg_sig":          colStr(schema.PackagesPkgSig),
			"sig_provider":     colStr(schema.PkgSigProviderSigProvider),
			"pkg_install_date": colStr(schema.PackagesPkgInstallDate),
			"pkg_updated":      colStr(schema.PackagesPkgUpdated),
		},
		// A package row carries only the node id: its name and properties live in
		// the joined nodes table.
		Joins: map[string]JoinDef{
			"nodes": {MappingKey: "node"},
		},
	},
	"instance_status_log": {
		Available: []string{
			"id",
			"svc_id", "node_id",
			"mon_begin", "mon_end",
			"mon_availstatus", "mon_overallstatus",
			"mon_ipstatus", "mon_fsstatus", "mon_diskstatus",
			"mon_sharestatus", "mon_containerstatus",
			"mon_syncstatus", "mon_hbstatus", "mon_appstatus",
		},
		Default: []string{
			"svc_id", "node_id",
			"mon_begin", "mon_end",
			"mon_availstatus", "mon_overallstatus",
		},
		Props: map[string]propDef{
			"id":                  col(schema.SvcmonLogID),
			"svc_id":              colStr(schema.SvcmonLogSvcID),
			"node_id":             colStr(schema.SvcmonLogNodeID),
			"mon_begin":           colStr(schema.SvcmonLogMonBegin),
			"mon_end":             colStr(schema.SvcmonLogMonEnd),
			"mon_availstatus":     colStr(schema.SvcmonLogMonAvailstatus),
			"mon_overallstatus":   colStr(schema.SvcmonLogMonOverallstatus),
			"mon_ipstatus":        colStr(schema.SvcmonLogMonIpstatus),
			"mon_fsstatus":        colStr(schema.SvcmonLogMonFsstatus),
			"mon_diskstatus":      colStr(schema.SvcmonLogMonDiskstatus),
			"mon_sharestatus":     colStr(schema.SvcmonLogMonSharestatus),
			"mon_containerstatus": colStr(schema.SvcmonLogMonContainerstatus),
			"mon_syncstatus":      colStr(schema.SvcmonLogMonSyncstatus),
			"mon_hbstatus":        colStr(schema.SvcmonLogMonHbstatus),
			"mon_appstatus":       colStr(schema.SvcmonLogMonAppstatus),
		},
	},
	"service_status_log": {
		Available: []string{
			"id", "svc_id", "svc_begin", "svc_end", "svc_availstatus",
		},
		Default: []string{
			"svc_id", "svc_begin", "svc_end", "svc_availstatus",
		},
		Props: map[string]propDef{
			"id":              col(schema.ServicesLogID),
			"svc_id":          colStr(schema.ServicesLogSvcID),
			"svc_begin":       colStr(schema.ServicesLogSvcBegin),
			"svc_end":         colStr(schema.ServicesLogSvcEnd),
			"svc_availstatus": colStr(schema.ServicesLogSvcAvailstatus),
		},
	},
	"moduleset": {
		Available: []string{"id", "modset_name", "modset_author", "modset_updated"},
		Props: map[string]propDef{
			"id":             col(schema.CompModulesetID),
			"modset_name":    colStr(schema.CompModulesetModsetName),
			"modset_author":  colStr(schema.CompModulesetModsetAuthor),
			"modset_updated": colStr(schema.CompModulesetModsetUpdated),
		},
	},
	"hidden_menu_entry": {
		Available: []string{"id", "group_id", "menu_entry"},
		Props: map[string]propDef{
			"id":         col(schema.GroupHiddenMenuEntriesID),
			"group_id":   colInt(schema.GroupHiddenMenuEntriesGroupID),
			"menu_entry": colStr(schema.GroupHiddenMenuEntriesMenuEntry),
		},
	},
	"ruleset": {
		Available: []string{"id", "ruleset_name", "ruleset_type", "ruleset_public"},
		Props: map[string]propDef{
			"id":             col(schema.CompRulesetsID),
			"ruleset_name":   colStr(schema.CompRulesetsRulesetName),
			"ruleset_type":   colStr(schema.CompRulesetsRulesetType),
			"ruleset_public": colStr(schema.CompRulesetsRulesetPublic),
		},
	},
	// Modules of a compliance moduleset: the agent modules it runs.
	"moduleset_module": {
		Available: []string{"id", "modset_id", "modset_mod_name", "autofix", "modset_mod_author", "modset_mod_updated"},
		Props: map[string]propDef{
			"id":                 col(schema.CompModulesetModulesID),
			"modset_id":          colInt(schema.CompModulesetModulesModsetID),
			"modset_mod_name":    colStr(schema.CompModulesetModulesModsetModName),
			"autofix":            colStr(schema.CompModulesetModulesAutofix),
			"modset_mod_author":  colStr(schema.CompModulesetModulesModsetModAuthor),
			"modset_mod_updated": colStr(schema.CompModulesetModulesModsetModUpdated),
		},
	},
	// Modules of every moduleset, as the historical v_comp_modulesets view: a
	// moduleset without module has a row with a zero id and empty module props.
	"modulesets_module": {
		Available: []string{
			"id", "modset_id", "modset_name", "teams_responsible", "teams_publication",
			"modset_mod_name", "autofix", "modset_mod_updated", "modset_mod_author",
			"modset_author", "modset_updated",
		},
		Props: map[string]propDef{
			"id":                 colInt(schema.CompModulesetModulesID),
			"modset_id":          col(schema.CompModulesetID),
			"modset_name":        colStr(schema.CompModulesetModsetName),
			"modset_author":      colStr(schema.CompModulesetModsetAuthor),
			"modset_updated":     colStr(schema.CompModulesetModsetUpdated),
			"modset_mod_name":    colStr(schema.CompModulesetModulesModsetModName),
			"autofix":            colStr(schema.CompModulesetModulesAutofix),
			"modset_mod_author":  colStr(schema.CompModulesetModulesModsetModAuthor),
			"modset_mod_updated": colStr(schema.CompModulesetModulesModsetModUpdated),
			"teams_responsible":  colStr(cdb.ModsetResponsibles),
			"teams_publication":  colStr(cdb.ModsetPublications),
		},
	},
	// Declared networks. begin, end and broadcast are generated by the database
	// from network and netmask.
	"network": {
		Available: []string{
			"id", "name", "network", "netmask", "broadcast", "gateway", "begin", "end",
			"pvid", "prio", "team_responsible", "comment", "updated",
		},
		Props: map[string]propDef{
			"id":               colInt(schema.NetworksID),
			"name":             colStr(schema.NetworksName),
			"network":          colStr(schema.NetworksNetwork),
			"netmask":          colInt(schema.NetworksNetmask),
			"broadcast":        colStr(schema.NetworksBroadcast),
			"gateway":          colStr(schema.NetworksGateway),
			"begin":            colStr(schema.NetworksBegin),
			"end":              colStr(schema.NetworksEnd),
			"pvid":             colInt(schema.NetworksPvid),
			"prio":             colInt(schema.NetworksPrio),
			"team_responsible": colStr(schema.NetworksTeamResponsible),
			"comment":          colStr(schema.NetworksComment),
			"updated":          colStr(schema.NetworksUpdated),
		},
	},
	// Variables of every ruleset, as the historical v_comp_rulesets view: a
	// ruleset lists its own variables, then those of each ruleset it
	// encapsulates; a ruleset of the chain without variable has a row with a zero
	// id and empty variable props.
	"rulesets_variable": {
		Available: []string{
			"id", "ruleset_id", "ruleset_name", "ruleset_type", "ruleset_public",
			"teams_responsible", "teams_publication", "fset_name", "chain", "chain_len",
			"encap_rset", "var_class", "var_name", "var_value", "var_updated", "var_author",
			"encap_rset_id", "fset_id",
		},
		Props: map[string]propDef{
			"id":                colInt(schema.CompRulesetsVariablesID),
			"ruleset_id":        col(schema.CompRulesetsID),
			"ruleset_name":      colStr(schema.CompRulesetsRulesetName),
			"ruleset_type":      colStr(schema.CompRulesetsRulesetType),
			"ruleset_public":    colStr(schema.CompRulesetsRulesetPublic),
			"teams_responsible": colStr(cdb.RsetResponsibles),
			"teams_publication": colStr(cdb.RsetPublications),
			"fset_name":         colStr(schema.GenFiltersetsFsetName),
			"chain":             colStr(cdb.RsetChainsChain),
			"chain_len":         colInt(cdb.RsetChainsLen),
			"encap_rset":        colStr(cdb.RsetChainsEncap),
			"var_class":         colStr(schema.CompRulesetsVariablesVarClass),
			"var_name":          colStr(schema.CompRulesetsVariablesVarName),
			"var_value":         colStr(schema.CompRulesetsVariablesVarValue),
			"var_updated":       colStr(schema.CompRulesetsVariablesVarUpdated),
			"var_author":        colStr(schema.CompRulesetsVariablesVarAuthor),
			"encap_rset_id":     colInt(cdb.RsetChainsEncapID),
			"fset_id":           colInt(schema.CompRulesetsFiltersetsFsetID),
		},
	},
	// Variables of a compliance ruleset: the rules the modules apply.
	"ruleset_variable": {
		Available: []string{"id", "ruleset_id", "var_name", "var_class", "var_value", "var_author", "var_updated"},
		Props: map[string]propDef{
			"id":          col(schema.CompRulesetsVariablesID),
			"ruleset_id":  colInt(schema.CompRulesetsVariablesRulesetID),
			"var_name":    colStr(schema.CompRulesetsVariablesVarName),
			"var_class":   colStr(schema.CompRulesetsVariablesVarClass),
			"var_value":   colStr(schema.CompRulesetsVariablesVarValue),
			"var_author":  colStr(schema.CompRulesetsVariablesVarAuthor),
			"var_updated": colStr(schema.CompRulesetsVariablesVarUpdated),
		},
	},
	"alert_event": {
		Available: []string{"id", "svc_id", "node_id", "dash_begin", "dash_end", "dash_md5"},
		Props: map[string]propDef{
			"id":         col(schema.DashboardEventsID),
			"svc_id":     colStr(schema.DashboardEventsSvcID),
			"node_id":    colStr(schema.DashboardEventsNodeID),
			"dash_begin": colStr(schema.DashboardEventsDashBegin),
			"dash_end":   colStr(schema.DashboardEventsDashEnd),
			"dash_md5":   colStr(schema.DashboardEventsDashMD5),
		},
	},
	"alert": {
		Available: []string{
			"id", "dash_type", "svc_id", "node_id",
			"dash_severity", "dash_fmt", "dash_dict",
			"dash_created", "dash_updated",
			"dash_md5", "dash_dict_md5", "dash_env", "dash_instance",
			"alert",
		},
		Default: []string{
			"id", "dash_type", "svc_id", "node_id",
			"dash_severity",
			"dash_created", "dash_updated",
			"dash_env", "dash_instance",
			"alert",
		},
		Props: map[string]propDef{
			"id":            col(schema.DashboardID),
			"dash_type":     colStr(schema.DashboardDashType),
			"svc_id":        colStr(schema.DashboardSvcID),
			"node_id":       colStr(schema.DashboardNodeID),
			"dash_severity": colInt(schema.DashboardDashSeverity),
			"dash_fmt":      colStr(schema.DashboardDashFmt),
			"dash_dict":     colStr(schema.DashboardDashDict),
			"dash_created":  colStr(schema.DashboardDashCreated),
			"dash_updated":  colStr(schema.DashboardDashUpdated),
			"dash_md5":      colStr(schema.DashboardDashMD5),
			"dash_dict_md5": colStr(schema.DashboardDashDictMD5),
			"dash_env":      colStr(schema.DashboardDashEnv),
			"dash_instance": colStr(schema.DashboardDashInstance),
			// Filter-only expressions: the object of an entry is its service, or its
			// node when it has none, as the Object column of the dashboard shows it;
			// the message is built from its format and its values after the query,
			// so it is filtered on both, as stored.
			"services.svcname": {FilterExpr: "COALESCE(NULLIF(services.svcname, ''), nodes.nodename)"},
			"alert": {
				Col:        schema.DashboardDashFmt,
				FilterExpr: "CONCAT(COALESCE(dashboard.dash_fmt, ''), ' ', COALESCE(dashboard.dash_dict, ''))",
			},
		},
		// A dashboard entry points at a node or a service by id only. The names live
		// in the joined tables, as in the historical collector's own alert query.
		Joins: map[string]JoinDef{
			"nodes":    {MappingKey: "node"},
			"services": {MappingKey: "service"},
		},
	},
	"check": {
		Available: []string{
			"id", "node_id", "svc_id",
			"chk_type", "chk_instance",
			"chk_value", "chk_low", "chk_high", "chk_err",
			"chk_threshold_provider",
			"chk_created", "chk_updated",
		},
		Props: map[string]propDef{
			"id":           col(schema.ChecksLiveID),
			"node_id":      colStr(schema.ChecksLiveNodeID),
			"svc_id":       colStr(schema.ChecksLiveSvcID),
			"chk_type":     colStr(schema.ChecksLiveChkType),
			"chk_instance": colStr(schema.ChecksLiveChkInstance),
			"chk_value":    colInt(schema.ChecksLiveChkValue),
			// A check without thresholds has none, not thresholds of 0,
			// and its error state is null then.
			"chk_low":                col(schema.ChecksLiveChkLow),
			"chk_high":               col(schema.ChecksLiveChkHigh),
			"chk_err":                col(schema.ChecksLiveChkErr),
			"chk_threshold_provider": colStr(schema.ChecksLiveChkThresholdProvider),
			"chk_created":            colStr(schema.ChecksLiveChkCreated),
			"chk_updated":            colStr(schema.ChecksLiveChkUpdated),
		},
		// A check points at its node and its object by id only, the names
		// live in the joined tables.
		Joins: map[string]JoinDef{
			"nodes":    {MappingKey: "node"},
			"services": {MappingKey: "service"},
		},
	},
	// The thresholds set on a check, which take precedence over the others.
	"check_setting": {
		Available: []string{
			"id", "node_id", "svc_id", "chk_type", "chk_instance",
			"chk_low", "chk_high", "chk_changed", "chk_changed_by",
		},
		Props: map[string]propDef{
			"id":             col(schema.ChecksSettingsID),
			"node_id":        colStr(schema.ChecksSettingsNodeID),
			"svc_id":         colStr(schema.ChecksSettingsSvcID),
			"chk_type":       colStr(schema.ChecksSettingsChkType),
			"chk_instance":   colStr(schema.ChecksSettingsChkInstance),
			"chk_low":        col(schema.ChecksSettingsChkLow),
			"chk_high":       col(schema.ChecksSettingsChkHigh),
			"chk_changed":    colStr(schema.ChecksSettingsChkChanged),
			"chk_changed_by": colStr(schema.ChecksSettingsChkChangedBy),
		},
		Joins: map[string]JoinDef{
			"nodes":    {MappingKey: "node"},
			"services": {MappingKey: "service"},
		},
	},
	// The default thresholds of a check type.
	"check_default": {
		Available: []string{"id", "chk_type", "chk_inst", "chk_low", "chk_high", "chk_prio"},
		Props: map[string]propDef{
			"id":       col(schema.ChecksDefaultsID),
			"chk_type": colStr(schema.ChecksDefaultsChkType),
			"chk_inst": colStr(schema.ChecksDefaultsChkInst),
			"chk_low":  col(schema.ChecksDefaultsChkLow),
			"chk_high": col(schema.ChecksDefaultsChkHigh),
			"chk_prio": colInt(schema.ChecksDefaultsChkPrio),
		},
	},
	// The thresholds of the checks of the nodes and objects a filterset selects.
	"check_contextual": {
		Available: []string{"id", "fset_id", "chk_type", "chk_instance", "chk_low", "chk_high"},
		Props: map[string]propDef{
			"id":           col(schema.GenFiltersetCheckThresholdID),
			"fset_id":      col(schema.GenFiltersetCheckThresholdFsetID),
			"chk_type":     colStr(schema.GenFiltersetCheckThresholdChkType),
			"chk_instance": colStr(schema.GenFiltersetCheckThresholdChkInstance),
			"chk_low":      col(schema.GenFiltersetCheckThresholdChkLow),
			"chk_high":     col(schema.GenFiltersetCheckThresholdChkHigh),
		},
		Joins: map[string]JoinDef{
			"gen_filtersets": {MappingKey: "filterset"},
		},
	},
	"resource": {
		Available: []string{
			"id", "svc_id", "node_id", "rid", "res_status",
			"res_type", "res_desc", "res_log",
			"res_monitor", "res_disable", "res_optional",
			"vmname", "changed", "updated",
		},
		Default: []string{
			"svc_id", "node_id", "rid", "res_status",
			"res_type", "res_monitor", "res_disable", "res_optional",
			"updated",
		},
		Props: map[string]propDef{
			"id":           col(schema.ResmonID),
			"svc_id":       colStr(schema.ResmonSvcID),
			"node_id":      colStr(schema.ResmonNodeID),
			"rid":          colStr(schema.ResmonRid),
			"res_status":   colStr(schema.ResmonResStatus),
			"res_type":     colStr(schema.ResmonResType),
			"res_desc":     colStr(schema.ResmonResDesc),
			"res_log":      colStr(schema.ResmonResLog),
			"res_monitor":  colStr(schema.ResmonResMonitor),
			"res_disable":  colStr(schema.ResmonResDisable),
			"res_optional": colStr(schema.ResmonResOptional),
			"vmname":       colStr(schema.ResmonVmname),
			"changed":      colStr(schema.ResmonChanged),
			"updated":      colStr(schema.ResmonUpdated),
		},
	},
	// The resources of every instance (GET /resources), with the names of their
	// service and node, joined by that list only: the resources of one service read
	// the resmon table alone (mapping "resource").
	"resourceList": {
		Available: []string{
			"id", "svc_id", "node_id", "vmname", "rid", "res_type", "res_status",
			"res_desc", "res_log", "res_monitor", "res_disable", "res_optional",
			"changed", "updated",
		},
		Default: []string{
			"id", "svc_id", "node_id", "vmname", "rid", "res_type", "res_status",
			"res_desc", "res_monitor", "res_disable", "res_optional", "updated",
		},
		Props: map[string]propDef{
			"id":           col(schema.ResmonID),
			"svc_id":       colStr(schema.ResmonSvcID),
			"node_id":      colStr(schema.ResmonNodeID),
			"vmname":       colStr(schema.ResmonVmname),
			"rid":          colStr(schema.ResmonRid),
			"res_type":     colStr(schema.ResmonResType),
			"res_status":   colStr(schema.ResmonResStatus),
			"res_desc":     colStr(schema.ResmonResDesc),
			"res_log":      colStr(schema.ResmonResLog),
			"res_monitor":  colStr(schema.ResmonResMonitor),
			"res_disable":  colStr(schema.ResmonResDisable),
			"res_optional": colStr(schema.ResmonResOptional),
			"changed":      colStr(schema.ResmonChanged),
			"updated":      colStr(schema.ResmonUpdated),
		},
		Joins: map[string]JoinDef{
			"services": {MappingKey: "service"},
			"nodes":    {MappingKey: "node"},
		},
	},
	// The actions the agents ran on a service (GET /services/{svc_id}/actions), as
	// the historical actions view lists them, with the names of the service and
	// the node as joined props. log_type tells an action ("status") from a log
	// line of an action ("").
	"service_action": {
		Available: []string{
			"id", "svc_id", "node_id", "sid", "pid", "rid", "subset",
			"action", "command", "origin", "status", "begin", "end", "time",
			"status_log", "log_type", "cron", "version", "hostid", "alert",
			"ack", "acked_by", "acked_date", "acked_comment",
		},
		Default: []string{
			"id", "node_id", "action", "status", "begin", "end", "time",
			"cron", "sid", "pid", "rid",
		},
		Props: map[string]propDef{
			"id":            colInt(schema.SvcactionsID),
			"svc_id":        colStr(schema.SvcactionsSvcID),
			"node_id":       colStr(schema.SvcactionsNodeID),
			"sid":           colStr(schema.SvcactionsSid),
			"pid":           colStr(schema.SvcactionsPid),
			"rid":           colStr(schema.SvcactionsRid),
			"subset":        colStr(schema.SvcactionsSubset),
			"action":        colStr(schema.SvcactionsAction),
			"command":       colStr(schema.SvcactionsCommand),
			"origin":        colStr(schema.SvcactionsOrigin),
			"status":        colStr(schema.SvcactionsStatus),
			"begin":         colStr(schema.SvcactionsBegin),
			"end":           colStr(schema.SvcactionsEnd),
			"time":          colInt(schema.SvcactionsTime),
			"status_log":    colStr(schema.SvcactionsStatusLog),
			"log_type":      colStr(schema.SvcactionsLogType),
			"cron":          colInt(schema.SvcactionsCron),
			"version":       colStr(schema.SvcactionsVersion),
			"hostid":        colStr(schema.SvcactionsHostid),
			"alert":         colInt(schema.SvcactionsAlert),
			"ack":           colInt(schema.SvcactionsAck),
			"acked_by":      colStr(schema.SvcactionsAckedBy),
			"acked_date":    colStr(schema.SvcactionsAckedDate),
			"acked_comment": colStr(schema.SvcactionsAckedComment),
		},
		Joins: map[string]JoinDef{
			"services": {MappingKey: "service"},
			"nodes":    {MappingKey: "node"},
		},
	},
	"resource_log": {
		Available: []string{
			"id", "svc_id", "node_id", "rid", "res_status",
			"res_begin", "res_end", "res_log",
		},
		Default: []string{
			"svc_id", "node_id", "rid", "res_status",
			"res_begin", "res_end",
		},
		Props: map[string]propDef{
			"id":         {SQLExpr: "id", Kind: "int64"},
			"svc_id":     {SQLExpr: "COALESCE(svc_id, '') AS svc_id", Kind: "string"},
			"node_id":    {SQLExpr: "COALESCE(node_id, '') AS node_id", Kind: "string"},
			"rid":        {SQLExpr: "COALESCE(rid, '') AS rid", Kind: "string"},
			"res_status": {SQLExpr: "COALESCE(res_status, '') AS res_status", Kind: "string"},
			"res_begin":  {SQLExpr: "COALESCE(res_begin, '') AS res_begin", Kind: "string"},
			"res_end":    {SQLExpr: "COALESCE(res_end, '') AS res_end", Kind: "string"},
			"res_log":    {SQLExpr: "COALESCE(res_log, '') AS res_log", Kind: "string"},
		},
	},
	"resinfo": {
		Available: []string{"id", "rid", "res_key", "res_value", "updated", "topology", "node_id", "svc_id"},
		Props: map[string]propDef{
			"id":        col(schema.ResinfoID),
			"rid":       colStr(schema.ResinfoRid),
			"res_key":   colStr(schema.ResinfoResKey),
			"res_value": colStr(schema.ResinfoResValue),
			"updated":   colStr(schema.ResinfoUpdated),
			"topology":  colStr(schema.ResinfoTopology),
			"node_id":   colStr(schema.ResinfoNodeID),
			"svc_id":    colStr(schema.ResinfoSvcID),
		},
	},
	"comp_status": {
		Available: []string{"id", "run_module", "node_id", "svc_id", "run_status", "run_log", "run_date", "run_action", "rset_md5"},
		Default:   []string{"id", "run_module", "run_status", "run_date", "run_action", "svc_id"},
		Props: map[string]propDef{
			"id":         col(schema.CompStatusID),
			"run_module": colStr(schema.CompStatusRunModule),
			"node_id":    colStr(schema.CompStatusNodeID),
			"svc_id":     colStr(schema.CompStatusSvcID),
			"run_status": colInt(schema.CompStatusRunStatus),
			"run_log":    colStr(schema.CompStatusRunLog),
			"run_date":   colStr(schema.CompStatusRunDate),
			"run_action": colStr(schema.CompStatusRunAction),
			"rset_md5":   colStr(schema.CompStatusRsetMD5),
		},
		// A run points at its node, and at its service when it has one, by id
		// only: the names live in the joined tables.
		Joins: map[string]JoinDef{
			"nodes":    {MappingKey: "node"},
			"services": {MappingKey: "service"},
		},
	},
	// The charts: time series of historized metrics, defined in YAML.
	"chart": {
		Available: []string{"id", "chart_name", "chart_yaml"},
		Props: map[string]propDef{
			"id":         col(schema.ChartsID),
			"chart_name": colStr(schema.ChartsChartName),
			"chart_yaml": colStr(schema.ChartsChartYaml),
		},
	},
	// The reports: pages of charts and metrics, defined in YAML.
	"report": {
		Available: []string{"id", "report_name", "report_yaml"},
		Props: map[string]propDef{
			"id":          col(schema.ReportsID),
			"report_name": colStr(schema.ReportsReportName),
			"report_yaml": colStr(schema.ReportsReportYaml),
		},
	},
	// The metrics: SQL requests feeding the charts and the reports.
	"metric": {
		Available: []string{
			"id", "metric_name", "metric_sql", "metric_author", "metric_created",
			"metric_col_value_index", "metric_col_instance_index",
			"metric_col_instance_label", "metric_historize",
		},
		Props: map[string]propDef{
			"id":                        col(schema.MetricsID),
			"metric_name":               colStr(schema.MetricsMetricName),
			"metric_sql":                colStr(schema.MetricsMetricSql),
			"metric_author":             colStr(schema.MetricsMetricAuthor),
			"metric_created":            colStr(schema.MetricsMetricCreated),
			"metric_col_value_index":    col(schema.MetricsMetricColValueIndex),
			"metric_col_instance_index": col(schema.MetricsMetricColInstanceIndex),
			"metric_col_instance_label": colStr(schema.MetricsMetricColInstanceLabel),
			"metric_historize":          colStr(schema.MetricsMetricHistorize),
		},
	},
	"filter": {
		Available: []string{
			"id", "f_table", "f_field", "f_value", "f_op",
			"f_updated", "f_author", "f_cksum", "f_label",
		},
		Props: map[string]propDef{
			"id":        col(schema.GenFiltersID),
			"f_table":   colStr(schema.GenFiltersFTable),
			"f_field":   colStr(schema.GenFiltersFField),
			"f_value":   colStr(schema.GenFiltersFValue),
			"f_op":      colStr(schema.GenFiltersFOp),
			"f_updated": colStr(schema.GenFiltersFUpdated),
			"f_author":  colStr(schema.GenFiltersFAuthor),
			"f_cksum":   colStr(schema.GenFiltersFCksum),
			"f_label":   colStr(schema.GenFiltersFLabel),
		},
	},
	// The constraints of the attributes of a kind of object, from the derived table
	// of cdb.GetFieldConstraintList.
	"field_constraint": {
		Available: []string{
			"object_kind", "field", "mode", "values", "value_count", "pattern", "allowed",
			"updated", "updated_by",
		},
		Props: map[string]propDef{
			"object_kind": colStr(cdb.FieldConstraintTableCol("object_kind")),
			"field":       colStr(cdb.FieldConstraintTableCol("field")),
			"mode":        colStr(cdb.FieldConstraintTableCol("mode")),
			"values":      colStr(cdb.FieldConstraintTableCol("values_json")),
			"value_count": colInt(cdb.FieldConstraintTableCol("value_count")),
			"pattern":     colStr(cdb.FieldConstraintTableCol("pattern")),
			"allowed":     colStr(cdb.FieldConstraintTableCol("allowed")),
			"updated":     colStr(cdb.FieldConstraintTableCol("updated")),
			"updated_by":  colStr(cdb.FieldConstraintTableCol("updated_by")),
		},
	},
	// The notifications sent, computed by the derived table of the delivery
	// queries (cdb.GetNotifyDeliveries).
	"notification_delivery": {
		Available: []string{
			"id", "alert_id", "recipient", "recipient_kind", "recipient_label", "channel_name", "group_role",
			"state", "attempts", "last_error", "next_try", "alert_severity", "alert_type", "alert_object",
			"alert_message", "alert_present", "created", "updated",
		},
		Default: []string{
			"id", "alert_id", "recipient_kind", "recipient_label", "state", "attempts", "last_error",
			"alert_severity", "alert_type", "alert_object", "alert_message", "alert_present", "updated",
		},
		Props: map[string]propDef{
			"id":              col(notifyDeliveryCol("id")),
			"alert_id":        col(notifyDeliveryCol("alert_id")),
			"recipient":       colStr(notifyDeliveryCol("recipient")),
			"recipient_kind":  colStr(notifyDeliveryCol("recipient_kind")),
			"recipient_label": colStr(notifyDeliveryCol("recipient_label")),
			"channel_name":    colStr(notifyDeliveryCol("channel_name")),
			"group_role":      colStr(notifyDeliveryCol("group_role")),
			"state":           colStr(notifyDeliveryCol("state")),
			"attempts":        colInt(notifyDeliveryCol("attempts")),
			"last_error":      colStr(notifyDeliveryCol("last_error")),
			"next_try":        col(notifyDeliveryCol("next_try")),
			"alert_severity":  colIntNullable(notifyDeliveryCol("alert_severity")),
			"alert_type":      colStr(notifyDeliveryCol("alert_type")),
			"alert_object":    colStr(notifyDeliveryCol("alert_object")),
			"alert_message":   colStr(notifyDeliveryCol("alert_message")),
			"alert_present":   {Col: notifyDeliveryCol("alert_present"), Kind: "bool"},
			"created":         colStr(notifyDeliveryCol("created")),
			"updated":         colStr(notifyDeliveryCol("updated")),
		},
	},
	"oidc_mapping": {
		Available: []string{
			"id", "claim", "value", "allow_access", "group_ids", "group_roles",
			"privilege_ids", "privilege_roles", "org_ids", "org_roles", "author", "updated",
		},
		Props: map[string]propDef{
			"id":           col(schema.AuthOidcMappingsID),
			"claim":        colStr(schema.AuthOidcMappingsClaim),
			"value":        colStr(schema.AuthOidcMappingsValue),
			"allow_access": colStr(schema.AuthOidcMappingsAllowAccess),
			// The teams granted, computed by the derived table of the rule queries.
			"group_ids":   colStr(oidcMappingListCol("group_ids")),
			"group_roles": colStr(oidcMappingListCol("group_roles")),
			// The same, privilege teams and organizational teams apart.
			"privilege_ids":   colStr(oidcMappingListCol("privilege_ids")),
			"privilege_roles": colStr(oidcMappingListCol("privilege_roles")),
			"org_ids":         colStr(oidcMappingListCol("org_ids")),
			"org_roles":       colStr(oidcMappingListCol("org_roles")),
			"author":          colStr(schema.AuthOidcMappingsAuthor),
			"updated":         colStr(schema.AuthOidcMappingsUpdated),
		},
	},
	"filterset": {
		Available: []string{
			"id", "fset_name", "fset_updated", "fset_author", "fset_stats",
		},
		Props: map[string]propDef{
			"id":           col(schema.GenFiltersetsID),
			"fset_name":    colStr(schema.GenFiltersetsFsetName),
			"fset_updated": colStr(schema.GenFiltersetsFsetUpdated),
			"fset_author":  colStr(schema.GenFiltersetsFsetAuthor),
			"fset_stats":   colStr(schema.GenFiltersetsFsetStats),
		},
	},
	"filterset_filter": {
		// The rows are the filterset's entries; the "id" prop is the filter's.
		PrimaryKey: []*schema.Col{schema.GenFiltersetsFiltersID},
		Available: []string{
			"id", "f_table", "f_field", "f_value", "f_op",
			"f_updated", "f_author", "f_cksum", "f_label",
			"f_log_op", "f_order", "fset_id", "encap_fset_id", "join_id",
		},
		Props: map[string]propDef{
			"id":            col(schema.GenFiltersID),
			"f_table":       colStr(schema.GenFiltersFTable),
			"f_field":       colStr(schema.GenFiltersFField),
			"f_value":       colStr(schema.GenFiltersFValue),
			"f_op":          colStr(schema.GenFiltersFOp),
			"f_updated":     colStr(schema.GenFiltersFUpdated),
			"f_author":      colStr(schema.GenFiltersFAuthor),
			"f_cksum":       colStr(schema.GenFiltersFCksum),
			"f_label":       colStr(schema.GenFiltersFLabel),
			"f_log_op":      colStr(schema.GenFiltersetsFiltersFLogOp),
			"f_order":       colInt(schema.GenFiltersetsFiltersFOrder),
			"fset_id":       col(schema.GenFiltersetsFiltersFsetID),
			"encap_fset_id": colInt(schema.GenFiltersetsFiltersEncapFsetID),
			"join_id":       {SQLExpr: "gen_filtersets_filters.id AS join_id", Kind: "int64"},
		},
	},
	"comp_log": {
		Available: []string{
			"id", "run_module", "run_status", "run_log",
			"run_date", "run_action", "rset_md5",
			"node_id", "svc_id",
		},
		Default: []string{
			"id", "run_module", "run_status", "run_date", "run_action",
			"node_id", "svc_id",
		},
		Props: map[string]propDef{
			"id":         col(schema.CompLogID),
			"run_module": colStr(schema.CompLogRunModule),
			"run_status": colInt(schema.CompLogRunStatus),
			"run_log":    colStr(schema.CompLogRunLog),
			"run_date":   colStr(schema.CompLogRunDate),
			"run_action": colStr(schema.CompLogRunAction),
			"rset_md5":   colStr(schema.CompLogRsetMD5),
			"node_id":    colStr(schema.CompLogNodeID),
			"svc_id":     colStr(schema.CompLogSvcID),
		},
		// A run points at its node, and at its service when it has one, by id
		// only: the names live in the joined tables.
		Joins: map[string]JoinDef{
			"nodes":    {MappingKey: "node"},
			"services": {MappingKey: "service"},
		},
	},
	"log_event": {
		Available: []string{
			"id", "log_action", "log_user", "log_impersonator", "log_origin", "log_request_id",
			"log_fmt", "log_dict", "log_date", "svc_id", "node_id", "log_level", "log_entry_id",
			"log_gtalk_sent", "log_email_sent",
		},
		Default: []string{
			"id", "log_action", "log_user", "log_impersonator", "log_origin", "log_fmt", "log_date",
			"svc_id", "node_id", "log_level",
		},
		Props: map[string]propDef{
			"id":         col(schema.LogID),
			"log_action": colStr(schema.LogLogAction),
			"log_user":   colStr(schema.LogLogUser),
			// The user who really signed in when log_user was impersonated; empty
			// otherwise.
			"log_impersonator": colStr(schema.LogLogImpersonator),
			// The client the request came through, as it declared it: assistant
			// or mcp; empty for the web interface, the API and the collector.
			"log_origin": colStr(schema.LogLogOrigin),
			// The X-Request-ID that client gave, on its audit lines too.
			"log_request_id": colStr(schema.LogLogRequestID),
			// Filtered on the format and its values together: the message shown is
			// the format filled with the values, and a node or tag name typed in the
			// filter is among the values.
			"log_fmt": {
				Col:        schema.LogLogFmt,
				SQLExpr:    "COALESCE(log.log_fmt, '')",
				Kind:       "string",
				FilterExpr: "CONCAT_WS(' ', log.log_fmt, log.log_dict)",
			},
			"log_dict":       colStr(schema.LogLogDict),
			"log_date":       colStr(schema.LogLogDate),
			"svc_id":         colStr(schema.LogSvcID),
			"node_id":        colStr(schema.LogNodeID),
			"log_level":      colStr(schema.LogLogLevel),
			"log_entry_id":   colInt(schema.LogLogEntryID),
			"log_gtalk_sent": colInt(schema.LogLogGtalkSent),
			"log_email_sent": colInt(schema.LogLogEmailSent),
		},
		// A log event names its node and service by id only: the names come from the
		// joined tables, as in the historical collector's log table.
		Joins: map[string]JoinDef{
			"nodes":    {MappingKey: "node"},
			"services": {MappingKey: "service"},
		},
	},
}
