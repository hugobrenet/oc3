package cdb

import (
	"slices"
	"strings"
	"testing"
)

// TestGivenRowsNamesTheFirstRow checks the rows given as a source name their
// columns once, in the first SELECT, the values all passed as arguments in order.
func TestGivenRowsNamesTheFirstRow(t *testing.T) {
	source, args := givenRows([]string{"svc_id", "dash_type"}, [][]any{{"s1", "t1"}, {"s2", "t2"}})
	if want := "SELECT ? AS svc_id, ? AS dash_type UNION ALL SELECT ?, ?"; source != want {
		t.Errorf("source %q, want %q", source, want)
	}
	if want := []any{"s1", "t1", "s2", "t2"}; !slices.Equal(args, want) {
		t.Errorf("args %v, want %v", args, want)
	}
}

// TestObjectStatusUpdateQueryNeverInserts checks the status of the services is
// only written to the rows there: a row inserted for a service deleted
// meanwhile would have no svcname.
func TestObjectStatusUpdateQueryNeverInserts(t *testing.T) {
	query, args := objectStatusUpdateQuery([]*DBObject{
		{SvcID: "s1", DBObjStatus: DBObjStatus{AvailStatus: "up", OverallStatus: "up", Placement: "optimal", Frozen: "unfrozen", Provisioned: "true"}},
		{SvcID: "s2", DBObjStatus: DBObjStatus{AvailStatus: "down", OverallStatus: "warn", Placement: "non-optimal", Frozen: "frozen", Provisioned: "false"}},
	})
	if !strings.HasPrefix(query, "UPDATE `services` t JOIN (") || strings.Contains(strings.ToUpper(query), "INSERT") {
		t.Errorf("query is not an update of the rows there: %s", query)
	}
	want := []any{"s1", "up", "up", "optimal", "unfrozen", "true", "s2", "down", "warn", "non-optimal", "frozen", "false"}
	if !slices.Equal(args, want) {
		t.Errorf("args %v, want %v", args, want)
	}
}
