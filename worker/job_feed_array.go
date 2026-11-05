package worker

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/go-redis/redis/v8"

	"github.com/opensvc/oc3/cachekeys"
	"github.com/opensvc/oc3/feeder"
	"github.com/opensvc/oc3/util/logkey"
)

type (
	// jobFeedArray parses the configuration of a storage array an agent
	// reported, and stores it in the stor_array, stor_array_dg,
	// stor_array_tgtid, stor_array_proxy and diskinfo tables, as the old
	// collector did from the update_<type> rpc.
	jobFeedArray struct {
		JobBase
		JobRedis
		JobDB

		// idx is <type>@<name>, the key of the report in FeedArrayH.
		idx string

		// found is false when no report waits under idx: a former job
		// took it, along with the report of a later push.
		found bool

		data struct {
			Name string                     `json:"name"`
			Type feeder.ArrayType           `json:"type"`
			Data map[string]json.RawMessage `json:"data"`
		}

		// proxies are the nodes that pushed the array since its last
		// parse.
		proxies []string

		rows *arrayRows
	}
)

func newArray(idx string) *jobFeedArray {
	return &jobFeedArray{
		JobBase: JobBase{
			name:   jtArray,
			detail: "array: " + idx,
			logger: slog.With(logkey.Object, idx, logkey.JobName, jtArray),
		},
		JobRedis: JobRedis{
			cachePendingH:   cachekeys.FeedArrayPendingH,
			cachePendingIDX: idx,
		},
		idx: idx,
	}
}

func (d *jobFeedArray) Operations() []operation {
	found := func() bool { return d.found }
	return []operation{
		{name: "dropPending", do: d.dropPending, blocking: true},
		{name: "getData", do: d.getData, blocking: true},
		{name: "parse", do: d.parse, blocking: true, condition: found},
		{name: "dbNow", do: d.dbNow, blocking: true, condition: found},
		{name: "updateDB", do: d.updateDB, blocking: true, condition: found},
		{name: "cleanOrphans", do: d.cleanOrphans, blocking: true, condition: found},
		{name: "pushFromTableChanges", do: d.pushFromTableChanges, condition: found},
	}
}

// getData takes the report and the proxies of the array out of the cache:
// a report weighs megabytes, and is not kept once read. A push arriving
// meanwhile queues a job that finds nothing, its report parsed by this one.
func (d *jobFeedArray) getData(ctx context.Context) error {
	proxyKey := cachekeys.FeedArrayProxyS + d.idx
	var (
		report  *redis.StringCmd
		proxies *redis.StringSliceCmd
	)
	if _, err := d.redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		report = pipe.HGet(ctx, cachekeys.FeedArrayH, d.idx)
		pipe.HDel(ctx, cachekeys.FeedArrayH, d.idx)
		proxies = pipe.SMembers(ctx, proxyKey)
		pipe.Del(ctx, proxyKey)
		return nil
	}); err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("take the report: %w", err)
	}
	b, err := report.Bytes()
	switch {
	case errors.Is(err, redis.Nil):
		d.logger.Debug("no report left to parse")
		return nil
	case err != nil:
		return fmt.Errorf("HGET: %w", err)
	}
	d.proxies = proxies.Val()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("decompress: %w", err)
	}
	defer func() { _ = zr.Close() }()
	if err := json.NewDecoder(zr).Decode(&d.data); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	d.found = true
	return nil
}

// parse reads the sections of the array, with the parser of its type.
func (d *jobFeedArray) parse(context.Context) error {
	parser, ok := arrayParsers[d.data.Type]
	if !ok {
		return fmt.Errorf("unsupported array type %q", d.data.Type)
	}
	rows, err := parser(d.data.Name, d.data.Data)
	if err != nil {
		return fmt.Errorf("parse %s: %w", d.idx, err)
	}
	if rows.Name == "" {
		return fmt.Errorf("parse %s: no array name", d.idx)
	}
	d.rows = rows
	return nil
}

// updateDB replaces the rows of the array by the ones just parsed: the rows
// written now are upserted, and the older ones of the array deleted, even
// when none is written now.
//
//	CREATE TABLE `stor_array` (
//	  `id` int(11) NOT NULL AUTO_INCREMENT,
//	  `array_name` varchar(100) DEFAULT NULL,
//	  `array_model` varchar(30) NOT NULL,
//	  `array_cache` int(11) DEFAULT NULL,
//	  `array_firmware` varchar(60) DEFAULT NULL,
//	  `array_updated` timestamp NOT NULL DEFAULT current_timestamp() ON UPDATE current_timestamp(),
//	  `array_level` int(11) NOT NULL DEFAULT 0,
//	  `array_comment` varchar(300) DEFAULT NULL,
//	  `array_type` varchar(32) DEFAULT NULL,
//	  PRIMARY KEY (`id`),
//	  UNIQUE KEY `index_1` (`array_name`)
//	)
//
//	CREATE TABLE `stor_array_dg` (
//	  `array_id` varchar(60) NOT NULL,
//	  `dg_name` varchar(60) NOT NULL,
//	  `dg_free` int(11) NOT NULL,
//	  `dg_updated` timestamp NOT NULL,
//	  `dg_size` int(11) DEFAULT NULL,
//	  `dg_used` int(11) DEFAULT NULL,
//	  UNIQUE KEY `index_1` (`array_id`,`dg_name`)
//	)
//
//	CREATE TABLE `stor_array_tgtid` (
//	  `array_id` varchar(60) NOT NULL,
//	  `array_tgtid` varchar(60) NOT NULL,
//	  `updated` timestamp NOT NULL,
//	  UNIQUE KEY `index_1` (`array_id`,`array_tgtid`)
//	)
//
//	CREATE TABLE `stor_array_proxy` (
//	  `array_id` int(11) NOT NULL,
//	  `node_id` char(36) DEFAULT '',
//	  UNIQUE KEY `index_1` (`node_id`,`array_id`)
//	)
//
//	CREATE TABLE `diskinfo` (
//	  `disk_id` varchar(120) DEFAULT NULL,
//	  `disk_devid` varchar(60) DEFAULT '',
//	  `disk_arrayid` varchar(100) DEFAULT NULL,
//	  `disk_updated` datetime DEFAULT NULL,
//	  `disk_raid` varchar(128) DEFAULT NULL,
//	  `disk_size` int(11) DEFAULT NULL,
//	  `disk_group` varchar(60) DEFAULT '',
//	  `disk_name` varchar(120) DEFAULT '',
//	  `disk_alloc` int(11) DEFAULT NULL,
//	  UNIQUE KEY `new_index` (`disk_id`,`disk_group`)
//	)
func (d *jobFeedArray) updateDB(ctx context.Context) error {
	r := d.rows
	now := d.now
	arrayType := string(d.data.Type)

	// The name of an array is unique, whatever its type: two arrays of
	// different types under one name would mix their rows.
	var heldBy sql.NullString
	err := d.db.QueryRowContext(ctx, "SELECT array_type FROM stor_array WHERE array_name = ?", r.Name).Scan(&heldBy)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return fmt.Errorf("read the type of the array %s: %w", r.Name, err)
	case heldBy.String != "" && heldBy.String != arrayType:
		return fmt.Errorf("the name %s is held by a %s array: refuse the %s array %s", r.Name, heldBy.String, arrayType, d.data.Name)
	}

	row := map[string]any{
		"array_name":     r.Name,
		"array_model":    r.Model,
		"array_cache":    r.Cache,
		"array_firmware": r.Firmware,
		"array_type":     arrayType,
		"array_updated":  now,
	}
	if err := d.upsert(ctx, "stor_array", []string{"array_name"}, []string{"array_name", "array_model", "array_cache", "array_firmware", "array_type", "array_updated"}, []any{row}); err != nil {
		return err
	}
	var id int64
	if err := d.db.QueryRowContext(ctx, "SELECT id FROM stor_array WHERE array_name = ?", r.Name).Scan(&id); err != nil {
		return fmt.Errorf("read the id of the array %s: %w", r.Name, err)
	}
	arrayID := strconv.FormatInt(id, 10)

	rows := make([]any, 0, len(r.DGs))
	for _, dg := range r.DGs {
		rows = append(rows, map[string]any{"array_id": arrayID, "dg_name": dg.Name, "dg_free": dg.Free, "dg_used": dg.Used, "dg_size": dg.Size, "dg_updated": now})
	}
	if err := d.upsert(ctx, "stor_array_dg", []string{"array_id", "dg_name"}, []string{"array_id", "dg_name", "dg_free", "dg_used", "dg_size", "dg_updated"}, rows); err != nil {
		return err
	}
	if err := d.deleteOlder(ctx, "stor_array_dg", "array_id", arrayID, "dg_updated", now); err != nil {
		return err
	}

	rows = make([]any, 0, len(r.TgtIDs))
	for _, tgtID := range r.TgtIDs {
		rows = append(rows, map[string]any{"array_id": arrayID, "array_tgtid": tgtID, "updated": now})
	}
	if err := d.upsert(ctx, "stor_array_tgtid", []string{"array_id", "array_tgtid"}, []string{"array_id", "array_tgtid", "updated"}, rows); err != nil {
		return err
	}
	if err := d.deleteOlder(ctx, "stor_array_tgtid", "array_id", arrayID, "updated", now); err != nil {
		return err
	}

	if err := d.svcdiskIDs(ctx); err != nil {
		return err
	}
	rows = make([]any, 0, len(r.Disks))
	for _, disk := range r.Disks {
		rows = append(rows, map[string]any{
			"disk_id":      disk.ID,
			"disk_arrayid": r.Name,
			"disk_name":    disk.Name,
			"disk_devid":   disk.DevID,
			"disk_size":    disk.Size,
			"disk_alloc":   disk.Alloc,
			"disk_raid":    disk.Raid,
			"disk_group":   disk.Group,
			"disk_updated": now,
		})
	}
	if err := d.upsert(ctx, "diskinfo", []string{"disk_id", "disk_group"}, []string{"disk_id", "disk_arrayid", "disk_name", "disk_devid", "disk_size", "disk_alloc", "disk_raid", "disk_group", "disk_updated"}, rows); err != nil {
		return err
	}
	// The old collector deleted the disks updated before, and the ones
	// never updated.
	query := "DELETE FROM `diskinfo` WHERE `disk_arrayid` = ? AND (`disk_updated` < ? OR `disk_updated` IS NULL)"
	if count, err := d.oDb.ExecContextAndCountRowsAffected(ctx, query, r.Name, now); err != nil {
		return fmt.Errorf("query %s: %w", query, err)
	} else if count > 0 {
		d.oDb.SetChange("diskinfo")
	}

	rows = make([]any, 0, len(d.proxies))
	for _, nodeID := range d.proxies {
		rows = append(rows, map[string]any{"array_id": id, "node_id": nodeID})
	}
	return d.upsert(ctx, "stor_array_proxy", []string{"node_id", "array_id"}, []string{"array_id", "node_id"}, rows)
}

// cleanOrphans deletes the disk groups, target ids and proxies of the arrays
// gone, and the quotas of the disk groups gone, as the old collector did at
// each array feed.
func (d *jobFeedArray) cleanOrphans(ctx context.Context) error {
	for _, c := range []struct{ table, query string }{
		{"stor_array_dg", "DELETE FROM stor_array_dg WHERE array_id NOT IN (SELECT id FROM stor_array)"},
		{"stor_array_tgtid", "DELETE FROM stor_array_tgtid WHERE array_id NOT IN (SELECT id FROM stor_array)"},
		{"stor_array_proxy", "DELETE FROM stor_array_proxy WHERE array_id NOT IN (SELECT id FROM stor_array)"},
		{"stor_array_dg_quota", "DELETE FROM stor_array_dg_quota WHERE dg_id NOT IN (SELECT id FROM stor_array_dg)"},
	} {
		if count, err := d.oDb.ExecContextAndCountRowsAffected(ctx, c.query); err != nil {
			return fmt.Errorf("query %s: %w", c.query, err)
		} else if count > 0 {
			d.oDb.SetChange(c.table)
		}
	}
	return nil
}

// svcdiskIDs gives the disks of an hds array the id the nodes see them by,
// as the old collector did in insert_hds: the disks the nodes report under
// the prefix of the array are indexed by their ldev, the end of their id.
func (d *jobFeedArray) svcdiskIDs(ctx context.Context) error {
	r := d.rows
	if r.SvcdiskPrefix == "" {
		return nil
	}
	rows, err := d.db.QueryContext(ctx, "SELECT DISTINCT disk_id FROM svcdisks WHERE disk_id LIKE ?", r.SvcdiskPrefix+"%")
	if err != nil {
		return fmt.Errorf("read the svcdisks of the array %s: %w", r.Name, err)
	}
	defer func() { _ = rows.Close() }()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("read the svcdisks of the array %s: %w", r.Name, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the svcdisks of the array %s: %w", r.Name, err)
	}
	r.setSvcdiskIDs(ids)
	return nil
}
