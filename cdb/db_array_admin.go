package cdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// ArrayRecord is the name and the comment of a storage array.
type ArrayRecord struct {
	ID      int64
	Name    string
	Comment string
}

// GetArray returns one storage array.
func (oDb *DB) GetArray(ctx context.Context, arrayID int64, p ListParams) ([]map[string]any, error) {
	return oDb.listQuery(ctx, "getArray", "stor_array", []string{"stor_array.id = ?"}, []any{arrayID},
		"stor_array.id", p)
}

// ArrayRecordByID returns the name and the comment of an array, nil when it
// does not exist.
func (oDb *DB) ArrayRecordByID(ctx context.Context, arrayID int64) (*ArrayRecord, error) {
	var (
		r       = ArrayRecord{ID: arrayID}
		name    sql.NullString
		comment sql.NullString
	)
	err := oDb.DB.QueryRowContext(ctx, "SELECT array_name, array_comment FROM stor_array WHERE id = ?", arrayID).
		Scan(&name, &comment)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("arrayRecordByID: %w", err)
	}
	r.Name, r.Comment = name.String, comment.String
	return &r, nil
}

// UpdateArrayComment sets the comment of an array.
func (oDb *DB) UpdateArrayComment(ctx context.Context, arrayID int64, comment string) error {
	if _, err := oDb.ExecContext(ctx, "UPDATE stor_array SET array_comment = ? WHERE id = ?", comment, arrayID); err != nil {
		return fmt.Errorf("updateArrayComment: %w", err)
	}
	oDb.SetChange("stor_array")
	return nil
}

// GetArraysDiskgroups returns the disk groups of all the arrays, the columns of
// their array reachable as stor_array.<column>.
func (oDb *DB) GetArraysDiskgroups(ctx context.Context, p ListParams) ([]map[string]any, error) {
	return oDb.listQuery(ctx, "getArraysDiskgroups",
		"stor_array_dg JOIN stor_array ON stor_array.id = stor_array_dg.array_id", nil, nil,
		"stor_array.array_name, stor_array_dg.dg_name", p)
}

// GetArrayDiskgroup returns a disk group of an array, by its id or its name;
// arrayID 0 looks for it among those of all the arrays, as the historical
// handler did for the array "0".
func (oDb *DB) GetArrayDiskgroup(ctx context.Context, arrayID int64, dg string, p ListParams) ([]map[string]any, error) {
	var (
		conds []string
		args  []any
	)
	if arrayID != 0 {
		conds = append(conds, "stor_array_dg.array_id = ?")
		args = append(args, arrayID)
	}
	if id, err := strconv.ParseInt(dg, 10, 64); err == nil {
		conds = append(conds, "stor_array_dg.id = ?")
		args = append(args, id)
	} else {
		conds = append(conds, "stor_array_dg.dg_name = ?")
		args = append(args, dg)
	}
	return oDb.listQuery(ctx, "getArrayDiskgroup",
		"stor_array_dg LEFT JOIN stor_array ON stor_array.id = stor_array_dg.array_id", conds, args,
		"stor_array_dg.id", p)
}

// GetArrayProxies returns the nodes inventorying an array, the columns of the
// node reachable as nodes.<column>.
func (oDb *DB) GetArrayProxies(ctx context.Context, arrayID int64, p ListParams) ([]map[string]any, error) {
	return oDb.listQuery(ctx, "getArrayProxies",
		"stor_array_proxy LEFT JOIN nodes ON nodes.node_id = stor_array_proxy.node_id",
		[]string{"stor_array_proxy.array_id = ?"}, []any{arrayID}, "nodes.nodename, stor_array_proxy.node_id", p)
}

// ArrayProxyExists tells whether the node is a proxy of the array.
func (oDb *DB) ArrayProxyExists(ctx context.Context, arrayID int64, nodeID string) (bool, error) {
	var one int
	err := oDb.DB.QueryRowContext(ctx, "SELECT 1 FROM stor_array_proxy WHERE array_id = ? AND node_id = ? LIMIT 1",
		arrayID, nodeID).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("arrayProxyExists: %w", err)
	}
	return true, nil
}

// InsertArrayProxy makes the node a proxy of the array.
func (oDb *DB) InsertArrayProxy(ctx context.Context, arrayID int64, nodeID string) error {
	if _, err := oDb.ExecContext(ctx, "INSERT INTO stor_array_proxy (array_id, node_id) VALUES (?, ?)", arrayID, nodeID); err != nil {
		return fmt.Errorf("insertArrayProxy: %w", err)
	}
	oDb.SetChange("stor_array_proxy")
	return nil
}

// DeleteArrayProxy unsets the node as a proxy of the array.
func (oDb *DB) DeleteArrayProxy(ctx context.Context, arrayID int64, nodeID string) error {
	if _, err := oDb.ExecContext(ctx, "DELETE FROM stor_array_proxy WHERE array_id = ? AND node_id = ?", arrayID, nodeID); err != nil {
		return fmt.Errorf("deleteArrayProxy: %w", err)
	}
	oDb.SetChange("stor_array_proxy")
	return nil
}

// GetArrayTargets returns the target ports of an array.
func (oDb *DB) GetArrayTargets(ctx context.Context, arrayID int64, p ListParams) ([]map[string]any, error) {
	return oDb.listQuery(ctx, "getArrayTargets", "stor_array_tgtid",
		[]string{"stor_array_tgtid.array_id = ?"}, []any{arrayID}, "stor_array_tgtid.array_tgtid", p)
}
