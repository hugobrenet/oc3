package worker

import (
	"reflect"
	"testing"
)

// The rows of a powermax are the ones insert_sym makes of the same outputs,
// with the old collector symmetrix module run under python 2.7: the outputs
// of testdata/array/symmetrix/000297600001 are the ones of the om3 driver
// tests, made to agree on their devices.
//
// Two differences, on purpose: a device without a wwn, 00002, is not
// reported rather than reported under an empty disk id, and the sizes of an
// srp, in GB, are rounded to the MB rather than left to the database to
// round.
func TestParseSymmetrix(t *testing.T) {
	got, err := parseSymmetrix("pmax-section", loadArrayReport(t, "symmetrix/000297600001"))
	if err != nil {
		t.Fatal(err)
	}
	want := &arrayRows{
		Name:     "000297600001",
		Model:    "PowerMax_8000",
		Cache:    1736704,
		Firmware: "5978.714.714",
		DGs: []arrayDG{
			{Name: "GRP_XXX1", Free: 0, Used: 8631, Size: 8631},
			{Name: "DG1_F_1", Free: 1315211, Used: 1889756, Size: 3204968},
			{Name: "SRP_XXX1", Free: 86125363, Used: 221519360, Size: 307644723},
		},
		TgtIDs: []string{"50000000080305DF"},
		Disks: []arrayDisk{
			{ID: "60000000000000000000000000000031", DevID: "00001", Size: 6, Alloc: 0, Raid: "Meta-0 TDEV"},
			{ID: "60000000000000000000000000003234", Name: "P_APP_00024", DevID: "00024", Size: 10241, Alloc: 10241, Raid: "Meta-0 RDF1+TDEV"},
			{ID: "60000970000297600001533030303235", Name: "P_APP_00025", DevID: "00025", Size: 10241, Alloc: 10241, Raid: "Meta-0 TDEV"},
			{ID: "60000970000297600001533030313945", DevID: "0019E", Size: 1026, Alloc: 1026, Raid: "Meta-0 RDF1+TDEV", Group: "SRP_XXX1"},
			{ID: "60000970000297600001533030313946", DevID: "0019F", Size: 2049, Alloc: 2049, Raid: "Meta-0 RDF1+TDEV", Group: "SRP_XXX1"},
			{ID: "60000970000297600001533030303330", DevID: "00030", Size: 40962, Alloc: 40962, Raid: "Meta-2 RAID-5"},
			{ID: "60000970000297600001533030303430", DevID: "00040", Size: 8631, Alloc: 8631, Raid: "Meta-0 2-Way Mir", Group: "GRP_XXX1"},
			{ID: "60000970000297600001533030303431", DevID: "00041", Size: 4316, Alloc: 4316, Raid: "Meta-0 VDEV", Group: "GRP_XXX1"},
		},
	}
	if !reflect.DeepEqual(got.DGs, want.DGs) {
		t.Errorf("disk groups:\ngot  %+v\nwant %+v", got.DGs, want.DGs)
	}
	if !reflect.DeepEqual(got.Disks, want.Disks) {
		t.Errorf("disks:\ngot  %+v\nwant %+v", got.Disks, want.Disks)
	}
	got.DGs, got.Disks, want.DGs, want.Disks = nil, nil, nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

// The optional outputs may be absent or empty: a powermax has no thin pool,
// a vmax2 no srp. The required ones may not.
func TestParseSymmetrixOptionalSections(t *testing.T) {
	data := loadArrayReport(t, "symmetrix/000297600001")
	for _, section := range []string{"sym_pool_info", "sym_srp_info", "sym_tdev_info", "sym_meta_info", "sym_ficondev_info", "sym_disk_info", "sym_diskgroup_info", "sym_sg_info", "sym_dev_name_info"} {
		delete(data, section)
	}
	data["sym_pool_info"] = []byte(`""`)
	r, err := parseSymmetrix("pmax-section", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.DGs) != 0 || len(r.Disks) != 8 {
		t.Errorf("got %d disk groups, %d disks", len(r.DGs), len(r.Disks))
	}
	for _, section := range []string{"sym_info", "sym_dir_info", "sym_dev_info", "sym_dev_wwn_info"} {
		data := loadArrayReport(t, "symmetrix/000297600001")
		delete(data, section)
		if _, err := parseSymmetrix("pmax-section", data); err == nil {
			t.Errorf("no %s: no error", section)
		}
	}
}
