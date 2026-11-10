package cdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type (
	// Quota is the space an application code may use in a disk group of a
	// storage array, in MB, with the names it is shown by.
	Quota struct {
		ID        int64
		DgID      int64
		AppID     int64
		Quota     int64
		App       string
		DgName    string
		ArrayID   int64
		ArrayName string
	}

	// DiskGroup is a disk group of a storage array.
	DiskGroup struct {
		ID        int64
		Name      string
		ArrayID   int64
		ArrayName string
	}
)

// GetQuotas lists the quotas of the disk groups, from the v_disk_quota view: a
// Manager or a StorageManager sees them all, another user those of the
// applications one of their groups is responsible for, as the historical
// collector filters them by application (q_filter(app_field=...)). dgID, when
// not zero, keeps those of one disk group.
func (oDb *DB) GetQuotas(ctx context.Context, p ListParams, dgID int64) ([]map[string]any, error) {
	return oDb.getQuotas(ctx, p, dgID, 0)
}

// GetQuota returns one quota of a disk group, as GetQuotas lists them: empty
// when the caller may not see it.
func (oDb *DB) GetQuota(ctx context.Context, p ListParams, dgID, quotaID int64) ([]map[string]any, error) {
	return oDb.getQuotas(ctx, p, dgID, quotaID)
}

func (oDb *DB) getQuotas(ctx context.Context, p ListParams, dgID, quotaID int64) ([]map[string]any, error) {
	if len(p.SelectExprs) == 0 {
		return nil, fmt.Errorf("getQuotas: no select expressions")
	}
	query := "SELECT " + strings.Join(p.SelectExprs, ", ") + " FROM v_disk_quota WHERE id > 0"
	var args []any
	if dgID != 0 {
		query += " AND dg_id = ?"
		args = append(args, dgID)
	}
	if quotaID != 0 {
		query += " AND id = ?"
		args = append(args, quotaID)
	}
	if !p.IsManager && !p.HasGroup("StorageManager") {
		groups := cleanGroups(p.Groups)
		if len(groups) == 0 {
			return []map[string]any{}, nil
		}
		query += " AND app_id IN (SELECT apps_responsibles.app_id FROM apps_responsibles" +
			" JOIN auth_group ON auth_group.id = apps_responsibles.group_id" +
			" WHERE auth_group.role IN (" + Placeholders(len(groups)) + "))"
		args = append(args, stringsToAny(groups)...)
	}
	conds, condArgs := p.FilterConditions()
	for _, cond := range conds {
		query += " AND " + cond
	}
	args = append(args, condArgs...)
	if gb := p.GroupByClause(""); gb != "" {
		query += " " + gb
	}
	query += " " + p.OrderByClause("array_name, dg_name, app")
	query, args = appendLimitOffset(query, args, p.Limit, p.Offset)
	rows, err := oDb.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getQuotas: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRowsToMaps(rows, p.Props, p.TypeHints)
}

// DiskGroupOfArray returns the disk group dgID of the array an id or a name
// designates, nil when there is no such disk group in that array.
func (oDb *DB) DiskGroupOfArray(ctx context.Context, arrayIDOrName string, dgID int64) (*DiskGroup, error) {
	query := "SELECT stor_array_dg.id, stor_array_dg.dg_name, stor_array.id, COALESCE(stor_array.array_name, '')" +
		" FROM stor_array_dg JOIN stor_array ON stor_array_dg.array_id = stor_array.id" +
		" WHERE stor_array_dg.id = ? AND "
	args := []any{dgID}
	if id, err := strconv.ParseInt(arrayIDOrName, 10, 64); err == nil {
		query += "(stor_array.id = ? OR stor_array.array_name = ?)"
		args = append(args, id, arrayIDOrName)
	} else {
		query += "stor_array.array_name = ?"
		args = append(args, arrayIDOrName)
	}
	var dg DiskGroup
	err := oDb.DB.QueryRowContext(ctx, query, args...).Scan(&dg.ID, &dg.Name, &dg.ArrayID, &dg.ArrayName)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("diskGroupOfArray: %w", err)
	}
	return &dg, nil
}

// QuotaByID returns a quota, nil when it does not exist.
func (oDb *DB) QuotaByID(ctx context.Context, id int64) (*Quota, error) {
	return oDb.quotaWhere(ctx, "q.id = ?", id)
}

// QuotaOfApp returns the quota of an application code in a disk group, nil
// when it has none.
func (oDb *DB) QuotaOfApp(ctx context.Context, dgID, appID int64) (*Quota, error) {
	return oDb.quotaWhere(ctx, "q.dg_id = ? AND q.app_id = ?", dgID, appID)
}

func (oDb *DB) quotaWhere(ctx context.Context, cond string, args ...any) (*Quota, error) {
	query := "SELECT q.id, q.dg_id, q.app_id, COALESCE(q.quota, 0), COALESCE(apps.app, '')," +
		" COALESCE(dg.dg_name, ''), COALESCE(stor_array.id, 0), COALESCE(stor_array.array_name, '')" +
		" FROM stor_array_dg_quota q" +
		" LEFT JOIN apps ON apps.id = q.app_id" +
		" LEFT JOIN stor_array_dg dg ON dg.id = q.dg_id" +
		" LEFT JOIN stor_array ON stor_array.id = dg.array_id" +
		" WHERE " + cond + " LIMIT 1"
	var q Quota
	err := oDb.DB.QueryRowContext(ctx, query, args...).Scan(&q.ID, &q.DgID, &q.AppID, &q.Quota, &q.App, &q.DgName, &q.ArrayID, &q.ArrayName)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("quota: %w", err)
	}
	return &q, nil
}

// InsertQuota gives an application code a quota in a disk group, and returns
// its id.
func (oDb *DB) InsertQuota(ctx context.Context, dgID, appID, quota int64) (int64, error) {
	res, err := oDb.DB.ExecContext(ctx, "INSERT INTO stor_array_dg_quota (dg_id, app_id, quota) VALUES (?, ?, ?)", dgID, appID, quota)
	if err != nil {
		return 0, fmt.Errorf("insertQuota: %w", err)
	}
	oDb.SetChange("stor_array_dg_quota")
	return res.LastInsertId()
}

// UpdateQuota sets the size of a quota and the application code it is for.
func (oDb *DB) UpdateQuota(ctx context.Context, id, appID, quota int64) error {
	if _, err := oDb.DB.ExecContext(ctx, "UPDATE stor_array_dg_quota SET app_id = ?, quota = ? WHERE id = ?", appID, quota, id); err != nil {
		return fmt.Errorf("updateQuota: %w", err)
	}
	oDb.SetChange("stor_array_dg_quota")
	return nil
}

// DeleteQuota removes a quota.
func (oDb *DB) DeleteQuota(ctx context.Context, id int64) error {
	if _, err := oDb.DB.ExecContext(ctx, "DELETE FROM stor_array_dg_quota WHERE id = ?", id); err != nil {
		return fmt.Errorf("deleteQuota: %w", err)
	}
	oDb.SetChange("stor_array_dg_quota")
	return nil
}

// RefreshDiskGroupReserved sets the space reserved in a disk group to the sum
// of its quotas, as update_dg_reserved() of the historical collector does each
// time its quota view is read; here, each time a quota changes.
func (oDb *DB) RefreshDiskGroupReserved(ctx context.Context, dgID int64) error {
	const query = "UPDATE stor_array_dg SET dg_reserved = " +
		"(SELECT COALESCE(SUM(quota), 0) FROM stor_array_dg_quota WHERE dg_id = ?) WHERE id = ?"
	if _, err := oDb.DB.ExecContext(ctx, query, dgID, dgID); err != nil {
		return fmt.Errorf("refreshDiskGroupReserved: %w", err)
	}
	oDb.SetChange("stor_array_dg")
	return nil
}
