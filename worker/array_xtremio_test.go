package worker

import (
	"encoding/json"
	"reflect"
	"testing"
)

// An xtremio cluster is named as it names itself, with one disk group, its
// fc and iscsi target ids, and its volumes and snapshots, as insert_xtremio
// made them, with its iscsi ports.
func TestParseXtremio(t *testing.T) {
	got, err := parseXtremio("xio-section", loadArrayReport(t, "xtremio/xio-test"))
	if err != nil {
		t.Fatal(err)
	}
	want := &arrayRows{
		Name:     "xbrick01",
		Model:    "XtremIO",
		Firmware: "6.3.3-8",
		DGs:      []arrayDG{{Name: "default", Free: 29491200, Used: 9830400, Size: 39321600}},
		TgtIDs:   []string{"514f0c50a23b1c00", "iqn.2008-05.com.xtremio:xio00171234567-514f0c50a23b1c04"},
		Disks: []arrayDisk{
			{ID: "514f0c5d8a600001", Name: "vol_db01", DevID: "1", Size: 1048576, Alloc: 262144, Raid: "volume", Group: "default"},
			{ID: "514f0c5d8a600002", Name: "vol_db01.snap", DevID: "2", Size: 1048576, Alloc: 1, Raid: "snapshot", Group: "default"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

// The sizes the api sends as numbers or as strings read the same.
func TestFlexInt(t *testing.T) {
	var v struct {
		A flexInt `json:"a"`
		B flexInt `json:"b"`
		C flexInt `json:"c"`
	}
	if err := json.Unmarshal([]byte(`{"a":"42","b":42,"c":null}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != 42 || v.B != 42 || v.C != 0 {
		t.Errorf("got %+v", v)
	}
	if err := json.Unmarshal([]byte(`{"a":"4.2"}`), &v); err == nil {
		t.Error("no error on a float")
	}
}
