package cdb

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"
)

// countResult is a sql.Result of a count of affected rows.
type countResult int64

func (r countResult) LastInsertId() (int64, error) { return 0, nil }
func (r countResult) RowsAffected() (int64, error) { return int64(r), nil }

// deleteRecorder is a DBOperater recording the statements executed, each
// affecting the next count given.
type deleteRecorder struct {
	DBOperater
	counts  []int64
	queries []string
}

func (r *deleteRecorder) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	r.queries = append(r.queries, query)
	var n int64
	if len(r.counts) > 0 {
		n, r.counts = r.counts[0], r.counts[1:]
	}
	return countResult(n), nil
}

// TestPurgeServicesLogOrphans checks the orphan logs of both tables are deleted
// by batches, until a batch is not full, and the deleted rows counted.
func TestPurgeServicesLogOrphans(t *testing.T) {
	rec := &deleteRecorder{counts: []int64{servicesLogPurgeBatch, 5, 0}}
	// HasTx: the statements run on rec, not each in its own transaction of a pool.
	odb := &DB{DB: rec, HasTx: true, Metrics: testMetrics(), Session: NewSession(rec, nil)}
	total, err := odb.PurgeServicesLogOrphans(context.Background(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(servicesLogPurgeBatch + 5); total != want {
		t.Errorf("total %d, want %d", total, want)
	}
	var tables []string
	for _, q := range rec.queries {
		tables = append(tables, strings.Fields(q)[2])
	}
	if want := []string{"`services_log_last`", "`services_log_last`", "`services_log`"}; !slices.Equal(tables, want) {
		t.Errorf("tables %v, want %v", tables, want)
	}
	if !strings.Contains(rec.queries[0], "NOT EXISTS (SELECT 1 FROM `services` s WHERE s.`svc_id` = `services_log_last`.`svc_id`)") {
		t.Errorf("query %q does not keep the logs of the existing services", rec.queries[0])
	}
}

// TestServiceCascadeTablesHaveTheStatusLogs checks the deletion of a service
// deletes its availability logs.
func TestServiceCascadeTablesHaveTheStatusLogs(t *testing.T) {
	for _, table := range []string{"services_log", "services_log_last"} {
		if !slices.Contains(serviceCascadeTables, table) {
			t.Errorf("%s not in serviceCascadeTables", table)
		}
	}
}

// TestServiceCascadeTablesDeleteTheServiceIDFirst checks the deletion of a
// service deletes its service_ids row, before the others: a feed of the service
// meanwhile then gives it a new svc_id instead of recreating the one deleted.
func TestServiceCascadeTablesDeleteTheServiceIDFirst(t *testing.T) {
	if len(serviceCascadeTables) == 0 || serviceCascadeTables[0] != "service_ids" {
		t.Errorf("serviceCascadeTables starts with %v, want service_ids", serviceCascadeTables[:1])
	}
}
