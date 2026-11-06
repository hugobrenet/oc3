package serverhandlers

import (
	"reflect"
	"strings"
	"testing"
)

func TestCheckReportObjectFields(t *testing.T) {
	name := "  capacity  "
	got, err := checkReportObjectFields(reportKindReport, &name, nil)
	if err != nil || got == nil || *got != "capacity" {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := checkReportObjectFields(reportKindChart, nil, nil); err != nil || got != nil {
		t.Errorf("nothing to change: got %v, %v", got, err)
	}
	empty := "  "
	if _, err := checkReportObjectFields(reportKindReport, nil, &empty); err != nil {
		t.Errorf("empty definition: %v", err)
	}
	long := strings.Repeat("x", 101)
	broken := "Sections: [unclosed\n"
	for _, c := range []struct {
		name, definition *string
	}{{&empty, nil}, {&long, nil}, {nil, &broken}} {
		if _, err := checkReportObjectFields(reportKindChart, c.name, c.definition); err == nil {
			t.Errorf("%v %v: expected an error", c.name, c.definition)
		}
	}
}

func TestRefID(t *testing.T) {
	for _, c := range []struct {
		v    any
		want int
		ok   bool
	}{{3, 3, true}, {int64(4), 4, true}, {5.0, 5, true}, {" 6 ", 6, true}, {"x", 0, false}, {nil, 0, false}} {
		got, ok := refID(c.v)
		if got != c.want || ok != c.ok {
			t.Errorf("refID(%#v) = %d, %v", c.v, got, ok)
		}
	}
}

func TestParseDefinition(t *testing.T) {
	if d, err := parseDefinition(" "); err != nil || len(d) != 0 {
		t.Errorf("empty: %v, %v", d, err)
	}
	if d, err := parseDefinition("- a\n- b\n"); err != nil || len(d) != 0 {
		t.Errorf("not a map: %v, %v", d, err)
	}
	if _, err := parseDefinition("a: [b"); err == nil {
		t.Error("invalid yaml: expected an error")
	}
	d, err := parseDefinition("Title: t\nSections:\n- Title: s\n")
	if err != nil || d["Title"] != "t" {
		t.Errorf("got %v, %v", d, err)
	}
}

// The entries of a section, in its Charts, its Metrics and its children, are
// gathered in children, in that order, as the export writes them.
func TestSectionChildren(t *testing.T) {
	d, err := parseDefinition("Sections:\n- Title: s\n  Charts:\n  - chart_id: 1\n  Metrics:\n  - metric_id: 2\n  children:\n  - chart_id: 3\n")
	if err != nil {
		t.Fatal(err)
	}
	section := asMaps(d["Sections"])[0]
	got := sectionChildren(section)
	want := []map[string]any{{"chart_id": 1}, {"metric_id": 2}, {"chart_id": 3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, key := range []string{"Charts", "Metrics", "children"} {
		if _, set := section[key]; set {
			t.Errorf("%s left in the section", key)
		}
	}
	if section["Title"] != "s" {
		t.Errorf("title lost: %v", section)
	}
}

func TestReportKinds(t *testing.T) {
	if reportKindMetric.obj.TeamTable(true) != "" || !reportKindMetric.managerOnly {
		t.Error("the metrics have no responsibles: the managers only change them")
	}
	for _, k := range []reportKind{reportKindReport, reportKindChart} {
		if k.obj.TeamTable(true) == "" || k.managerOnly {
			t.Errorf("%s: responsibles expected", k.obj.Kind)
		}
	}
}
