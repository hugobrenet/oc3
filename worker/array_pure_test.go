package worker

import (
	"reflect"
	"testing"
)

// A pure array is named as it names itself, its target ids are the wwn of
// its fc ports, its disks its volumes, as insert_pure made them.
func TestParsePure(t *testing.T) {
	got, err := parsePure("pure-section", loadArrayReport(t, "pure/fa-test"))
	if err != nil {
		t.Fatal(err)
	}
	want := &arrayRows{
		Name:     "fa-x70-1",
		Model:    "Pure",
		Firmware: "6.5.3",
		TgtIDs:   []string{"524a937ae31b7c00", "524a937ae31b7c10"},
		Disks: []arrayDisk{
			{ID: "624a9370a1b2c3d4e5f6a7b8c9d0e1f2", Name: "vol-db01", DevID: "0b6e4a1c-5f9e-27d3-8a41-6c2e9f0b1d77", Size: 204800, Alloc: 1048576, Raid: "regular"},
			{ID: "624a93700011223344556677889900aa", Name: "pod1::vol-app02", DevID: "5d3c2b1a-0f9e-8d7c-6b5a-4f3e2d1c0b9a", Size: 0, Alloc: 512000, Raid: "regular"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

// A report without the name of the array, or without a required section,
// is an error rather than rows of an unnamed array.
func TestParsePureRefused(t *testing.T) {
	data := loadArrayReport(t, "pure/fa-test")
	delete(data, "ports")
	if _, err := parsePure("pure-section", data); err == nil {
		t.Error("no ports: no error")
	}
	data = loadArrayReport(t, "pure/fa-test")
	data["arrays"] = []byte(`[]`)
	if _, err := parsePure("pure-section", data); err == nil {
		t.Error("no array: no error")
	}
}
