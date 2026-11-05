package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/go-redis/redis/v8"

	"github.com/opensvc/oc3/cachekeys"
	"github.com/opensvc/oc3/feeder"
	"github.com/opensvc/oc3/util/logkey"
)

type (
	// jobFeedSANSwitch parses the configuration of a SAN switch an agent
	// reported, and stores it in the switches, san_zone and san_zone_alias
	// tables, as the old collector did from the update_brocade rpc.
	jobFeedSANSwitch struct {
		JobBase
		JobRedis
		JobDB

		// idx is <type>@<name>, the key of the report in FeedSANSwitchH.
		idx string

		data    feeder.SANSwitch
		brocade *brocadeSwitch
	}
)

func newSANSwitch(idx string) *jobFeedSANSwitch {
	return &jobFeedSANSwitch{
		JobBase: JobBase{
			name:   jtSANSwitch,
			detail: "switch: " + idx,
			logger: slog.With(logkey.Object, idx, logkey.JobName, jtSANSwitch),
		},
		JobRedis: JobRedis{
			cachePendingH:   cachekeys.FeedSANSwitchPendingH,
			cachePendingIDX: idx,
		},
		idx: idx,
	}
}

func (d *jobFeedSANSwitch) Operations() []operation {
	return []operation{
		{name: "dropPending", do: d.dropPending},
		{name: "getData", do: d.getData},
		{name: "parse", do: d.parse},
		{name: "dbNow", do: d.dbNow},
		{name: "updateDB", do: d.updateDB},
		{name: "pushFromTableChanges", do: d.pushFromTableChanges},
	}
}

func (d *jobFeedSANSwitch) getData(ctx context.Context) error {
	result, err := d.redis.HGet(ctx, cachekeys.FeedSANSwitchH, d.idx).Result()
	switch err {
	case nil:
	case redis.Nil:
		return fmt.Errorf("HGET: no results")
	default:
		return fmt.Errorf("HGET: %w", err)
	}
	if err := json.Unmarshal([]byte(result), &d.data); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	return nil
}

// parse reads the command outputs of the switch, with the parser of its
// type.
func (d *jobFeedSANSwitch) parse(context.Context) error {
	switch d.data.Type {
	case feeder.Brocade:
		s, err := parseBrocade(d.data.Data["switchshow"], d.data.Data["nsshow"], d.data.Data["zoneshow"])
		if err != nil {
			return fmt.Errorf("parse %s: %w", d.idx, err)
		}
		if s.Name == "" {
			// The switch did not say its name: keep the one it was
			// reached by, as the old collector named it after the
			// directory its report was stored in.
			s.Name = d.data.Name
		}
		d.brocade = s
		return nil
	default:
		return fmt.Errorf("unsupported switch type %q", d.data.Type)
	}
}

// updateDB replaces the rows of the switch, of the aliases of each of its
// configurations, and of the zones of its effective configuration, by the
// ones just parsed: the rows written now are upserted, and the older ones
// of the same switch or configuration deleted.
//
//	CREATE TABLE `switches` (
//	  `id` int(11) NOT NULL AUTO_INCREMENT,
//	  `sw_name` varchar(64) NOT NULL,
//	  `sw_slot` int(11) DEFAULT NULL,
//	  `sw_port` int(11) DEFAULT NULL,
//	  `sw_portspeed` int(11) DEFAULT NULL,
//	  `sw_portnego` varchar(1) DEFAULT '',
//	  `sw_porttype` varchar(16) DEFAULT '',
//	  `sw_portstate` varchar(16) DEFAULT '',
//	  `sw_portname` varchar(16) DEFAULT '',
//	  `sw_rportname` varchar(128) DEFAULT NULL,
//	  `sw_updated` datetime NOT NULL,
//	  `sw_fabric` varchar(128) DEFAULT NULL,
//	  `sw_index` int(11) DEFAULT NULL,
//	  PRIMARY KEY (`id`),
//	  UNIQUE KEY `idx1` (`sw_name`,`sw_slot`,`sw_port`,`sw_rportname`),
//	  KEY `idx2` (`sw_portname`,`sw_rportname`)
//	)
//
//	CREATE TABLE `san_zone` (
//	  `id` int(11) NOT NULL AUTO_INCREMENT,
//	  `cfg` varchar(128) DEFAULT '',
//	  `zone` varchar(128) DEFAULT '',
//	  `port` varchar(16) DEFAULT '',
//	  `updated` datetime NOT NULL,
//	  PRIMARY KEY (`id`),
//	  UNIQUE KEY `idx1` (`cfg`,`zone`,`port`)
//	)
//
//	CREATE TABLE `san_zone_alias` (
//	  `id` int(11) NOT NULL AUTO_INCREMENT,
//	  `cfg` varchar(128) DEFAULT '',
//	  `alias` varchar(128) DEFAULT '',
//	  `port` varchar(16) DEFAULT '',
//	  `updated` datetime NOT NULL,
//	  PRIMARY KEY (`id`),
//	  UNIQUE KEY `idx1` (`cfg`,`alias`,`port`)
//	)
func (d *jobFeedSANSwitch) updateDB(ctx context.Context) error {
	s := d.brocade
	now := d.now

	for cfg, aliases := range s.Alias {
		rows := make([]any, 0)
		for alias, ports := range aliases {
			for _, port := range ports {
				rows = append(rows, map[string]any{"cfg": cfg, "alias": alias, "port": port, "updated": now})
			}
		}
		if err := d.upsert(ctx, "san_zone_alias", []string{"cfg", "alias", "port"}, []string{"cfg", "alias", "port", "updated"}, rows); err != nil {
			return err
		}
		if err := d.deleteOlder(ctx, "san_zone_alias", "cfg", cfg, "updated", now); err != nil {
			return err
		}
	}

	if s.Cfg != "" {
		rows := make([]any, 0)
		for zone, ports := range s.Zone {
			for _, port := range ports {
				rows = append(rows, map[string]any{"cfg": s.Cfg, "zone": zone, "port": port, "updated": now})
			}
		}
		if err := d.upsert(ctx, "san_zone", []string{"cfg", "zone", "port"}, []string{"cfg", "zone", "port", "updated"}, rows); err != nil {
			return err
		}
		if err := d.deleteOlder(ctx, "san_zone", "cfg", s.Cfg, "updated", now); err != nil {
			return err
		}
	}

	rows := s.rows()
	for _, row := range rows {
		row.(map[string]any)["sw_updated"] = now
	}
	columns := []string{"sw_name", "sw_portname", "sw_index", "sw_slot", "sw_port", "sw_portspeed", "sw_portnego", "sw_portstate", "sw_porttype", "sw_rportname", "sw_updated"}
	if err := d.upsert(ctx, "switches", []string{"sw_name", "sw_slot", "sw_port", "sw_rportname"}, columns, rows); err != nil {
		return err
	}
	return d.deleteOlder(ctx, "switches", "sw_name", s.Name, "sw_updated", now)
}
