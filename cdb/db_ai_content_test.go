package cdb

import (
	"context"
	"slices"
	"testing"
)

// TestInsertAIPublications checks the groups an object is published to are
// written in one statement, and none for an object published to everybody.
func TestInsertAIPublications(t *testing.T) {
	rec := &execRecorder{}
	odb := &DB{DB: rec, HasTx: true, Metrics: testMetrics(), Session: NewSession(rec, nil)}
	if err := odb.insertAIPublications(context.Background(), AIResources, 7, nil); err != nil || len(rec.queries) != 0 {
		t.Fatalf("no group: %v, %q", err, rec.queries)
	}
	if err := odb.insertAIPublications(context.Background(), AISkills, 7, []int{3, 12}); err != nil {
		t.Fatal(err)
	}
	if want := "INSERT INTO ai_skills_publications (skill_id, group_id) VALUES (?, ?), (?, ?)"; len(rec.queries) != 1 || rec.queries[0] != want {
		t.Errorf("queries %q, want %q", rec.queries, want)
	}
	if want := []any{int64(7), 3, int64(7), 12}; !slices.Equal(rec.args[0], want) {
		t.Errorf("args %v, want %v", rec.args[0], want)
	}
}
