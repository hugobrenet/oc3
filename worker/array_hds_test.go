package worker

import (
	"reflect"
	"testing"
)

// An hitachi array is named as the agent names it, with its named pools as
// disk groups, the wwn of its ports as target ids, and its logical units as
// disks, as insert_hds made them, display name as devid and label as name.
func TestParseHds(t *testing.T) {
	got, err := parseHds("hds-section", loadArrayReport(t, "hds/hds-test"))
	if err != nil {
		t.Fatal(err)
	}
	want := &arrayRows{
		Name:     "hds-section",
		Model:    "HUS VM",
		Firmware: "73-03-58-00/00",
		DGs: []arrayDG{
			{Name: "dp_gold", Free: 524288, Used: 524288, Size: 1048576},
			{Name: "dp_silver", Free: 1048576, Used: 1048576, Size: 2097152},
		},
		TgtIDs:        []string{"50060e8007c37500", "50060e8007c37510"},
		SvcdiskPrefix: "60060e8007c3",
		Disks: []arrayDisk{
			{ID: "210945.100", DevID: "00:00:64", Name: "other_disk", Size: 10240, Alloc: 1024, Raid: "RAID6(6D+2P)", Group: "dp_gold", Ldev: "000064"},
			{ID: "210945.101", DevID: "00:00:65", Size: 20480, Raid: "RAID6(6D+2P)", Group: "dp_gold", Ldev: "000065"},
			{ID: "210945.4660", DevID: "00:12:34", Name: "svc1_disk0", Size: 10240, Raid: "RAID6(6D+2P)", Group: "dp_gold", Ldev: "001234"},
			{ID: "210945.200", DevID: "00:00:C8", Size: 10240, Raid: "RAID5(7D+1P)", Group: "dp_silver", Ldev: "0000c8"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

// A disk a node sees takes the id the node reports it by: the one ending
// with its ldev. The others keep the id of the array.
func TestHdsSvcdiskIDs(t *testing.T) {
	r := &arrayRows{Disks: []arrayDisk{
		{ID: "210945.100", Ldev: "000064"},
		{ID: "210945.101", Ldev: "000065"},
	}}
	r.setSvcdiskIDs([]string{"60060e8007c375000030c37500000000", "60060e8007c375000030c37500000064", "short"})
	if r.Disks[0].ID != "60060e8007c375000030c37500000064" || r.Disks[1].ID != "210945.101" {
		t.Errorf("got %+v", r.Disks)
	}
}
