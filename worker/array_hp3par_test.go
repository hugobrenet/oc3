package worker

import (
	"reflect"
	"testing"
)

// A 3par array is named as the agent names it, with the memory of its nodes
// as cache, its cpgs as disk groups, the wwn of its connected ports as
// target ids, and its virtual volumes as disks, as insert_hp3par made them.
func TestParseHp3par(t *testing.T) {
	got, err := parseHp3par("3par-section", loadArrayReport(t, "hp3par/3par-test"))
	if err != nil {
		t.Fatal(err)
	}
	want := &arrayRows{
		Name:     "3par-section",
		Model:    "HP_3PAR 8440",
		Cache:    196608,
		Firmware: "3.3.1.648",
		DGs:      []arrayDG{{Name: "FC_r5"}, {Name: "SSD_r6"}},
		TgtIDs:   []string{"20110002ac012345", "21110002ac012345"},
		Disks: []arrayDisk{
			{ID: "60002ac0000000000000000100012345", Name: "admin", Size: 10240, Alloc: 10240, Group: "--"},
			{ID: "60002ac000000000000000a200012345", Name: "vv_db01", Size: 204800, Alloc: 53248, Group: "FC_r5"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}
