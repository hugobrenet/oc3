package worker

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/mariadb"
)

type (
	JobDB struct {
		dbPool *sql.DB

		// db is the generic DB operator
		db cdb.DBOperater

		// oDb is the DB collector helper
		oDb *cdb.DB

		now time.Time
	}
)

var (
	_ cdb.DBTxer = &JobDB{}
)

func (j *JobDB) ODBSetter(odb *cdb.DB) error {
	if j == nil {
		return fmt.Errorf("nil JobDB")
	}
	j.oDb = odb
	j.db = odb.DB
	return nil
}

func (j *JobDB) CreateTx(ctx context.Context) error {
	return j.oDb.CreateTx(ctx, nil)
}

func (j *JobDB) CommitAndCreateTx(ctx context.Context) error {
	if err := j.oDb.Commit(); err != nil {
		return fmt.Errorf("CommitAndCreateTx pre-commit: %w", err)
	}
	if err := j.oDb.CreateTx(ctx, nil); err != nil {
		return fmt.Errorf("CommitAndCreateTx: %w", err)
	}
	return nil
}

func (j *JobDB) Commit() error {
	return j.oDb.Commit()
}

func (j *JobDB) Rollback() error {
	return j.oDb.Rollback()
}

func (j *JobDB) DB() cdb.DBOperater {
	return j.db
}

func (j *JobDB) dbNow(ctx context.Context) (err error) {
	rows, err := j.db.QueryContext(ctx, "SELECT NOW()")
	if err != nil {
		return err
	}
	if rows == nil {
		return fmt.Errorf("no result rows for SELECT NOW()")
	}
	defer rows.Close()
	if !rows.Next() {
		return fmt.Errorf("no result rows next for SELECT NOW()")
	}
	if err := rows.Scan(&j.now); err != nil {
		return err
	}
	return nil
}

func (d *JobDB) pushFromTableChanges(ctx context.Context) error {
	return d.oDb.Session.NotifyChanges(ctx)
}

// pushScopedTableChanges announces the changes of the job as concerning only the
// services and the nodes it fed: a view showing another object needs not read it
// again. Used by the jobs that know every object they write.
func (d *JobDB) pushScopedTableChanges(ctx context.Context, svcIDs, nodeIDs []string) error {
	d.oDb.Session.Scope(svcIDs, nodeIDs)
	return d.oDb.Session.NotifyChanges(ctx)
}

// upsert inserts or updates rows in table, on the unique key keys.
func (d *JobDB) upsert(ctx context.Context, table string, keys, columns []string, rows []any) error {
	if len(rows) == 0 {
		return nil
	}
	mappings := make(mariadb.Mappings, len(columns))
	for i, column := range columns {
		mappings[i] = mariadb.Mapping{To: column}
	}
	request := mariadb.InsertOrUpdate{
		Table:    table,
		Mappings: mappings,
		Keys:     keys,
		Data:     rows,
	}
	if count, err := request.ExecContextAndCountRowsAffected(ctx, d.db); err != nil {
		return fmt.Errorf("upsert %s: %w", table, err)
	} else if count > 0 {
		d.oDb.SetChange(table)
	}
	return nil
}

// deleteOlder deletes the rows of table whose column key is value, updated
// before the rows just written.
func (d *JobDB) deleteOlder(ctx context.Context, table, key string, value any, updated string, now any) error {
	query := fmt.Sprintf("DELETE FROM `%s` WHERE `%s` = ? AND `%s` < ?", table, key, updated)
	if count, err := d.oDb.ExecContextAndCountRowsAffected(ctx, query, value, now); err != nil {
		return fmt.Errorf("query %s: %w", query, err)
	} else if count > 0 {
		d.oDb.SetChange(table)
	}
	return nil
}
