package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// oldCollectorRows are the rows the old collector made of an array, as
// <sample>.rows.json holds them: its stor_array, stor_array_dg,
// stor_array_tgtid and diskinfo values, in the order of its insert function.
type oldCollectorRows struct {
	Array []string   `json:"array"`
	DG    [][]string `json:"dg"`
	TgtID []string   `json:"tgtid"`
	Disk  [][]string `json:"disk"`
}

// asOldCollectorRows renders rows as the old collector listed them, its
// disk values in the order of insert_hds or of insert_sym.
func asOldCollectorRows(r *arrayRows, disk func(arrayDisk) []string) oldCollectorRows {
	o := oldCollectorRows{
		Array: []string{r.Name, r.Model, fmt.Sprint(r.Cache), r.Firmware},
		DG:    make([][]string, 0),
		TgtID: append([]string{}, r.TgtIDs...),
		Disk:  make([][]string, 0),
	}
	for _, dg := range r.DGs {
		o.DG = append(o.DG, []string{dg.Name, fmt.Sprint(dg.Free), fmt.Sprint(dg.Used), fmt.Sprint(dg.Size)})
	}
	for _, d := range r.Disks {
		o.Disk = append(o.Disk, disk(d))
	}
	less := func(l [][]string) func(i, j int) bool {
		return func(i, j int) bool { return fmt.Sprint(l[i]) < fmt.Sprint(l[j]) }
	}
	sort.Slice(o.DG, less(o.DG))
	sort.Slice(o.Disk, less(o.Disk))
	sort.Strings(o.TgtID)
	return o
}

func loadOldCollectorRows(t *testing.T, sample string) oldCollectorRows {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "array", sample+".rows.json"))
	if err != nil {
		t.Fatal(err)
	}
	var o oldCollectorRows
	if err := json.Unmarshal(b, &o); err != nil {
		t.Fatal(err)
	}
	sort.Slice(o.DG, func(i, j int) bool { return fmt.Sprint(o.DG[i]) < fmt.Sprint(o.DG[j]) })
	sort.Slice(o.Disk, func(i, j int) bool { return fmt.Sprint(o.Disk[i]) < fmt.Sprint(o.Disk[j]) })
	sort.Strings(o.TgtID)
	return o
}

func compareOldCollectorRows(t *testing.T, got, want oldCollectorRows) {
	t.Helper()
	if !reflect.DeepEqual(got.Array, want.Array) {
		t.Errorf("array: got %v, want %v", got.Array, want.Array)
	}
	if !reflect.DeepEqual(got.DG, want.DG) {
		t.Errorf("disk groups: got %v, want %v", got.DG, want.DG)
	}
	if !reflect.DeepEqual(got.TgtID, want.TgtID) {
		t.Errorf("target ids: got %v, want %v", got.TgtID, want.TgtID)
	}
	if len(got.Disk) != len(want.Disk) {
		t.Errorf("got %d disks, want %d", len(got.Disk), len(want.Disk))
	}
	for i := range min(len(got.Disk), len(want.Disk)) {
		if !reflect.DeepEqual(got.Disk[i], want.Disk[i]) {
			t.Errorf("disk %d: got %v, want %v", i, got.Disk[i], want.Disk[i])
		}
	}
}

// The rows of a production hitachi, anonymized, are the ones the old
// collector made of it. Its disk ids are the ones of the array: the lookup
// of the ids the nodes see is the job's.
func TestParseHdsSample(t *testing.T) {
	r, err := parseHds("HM700.900001", loadArrayReport(t, "hds/HM700.900001"))
	if err != nil {
		t.Fatal(err)
	}
	got := asOldCollectorRows(r, func(d arrayDisk) []string {
		return []string{d.ID, r.Name, d.DevID, d.Name, fmt.Sprint(d.Size), fmt.Sprint(d.Alloc), d.Raid, d.Group}
	})
	compareOldCollectorRows(t, got, loadOldCollectorRows(t, "hds/HM700.900001"))
}

// The rows of a production vmax, anonymized, are the ones the old collector
// made of it.
func TestParseSymmetrixSample(t *testing.T) {
	r, err := parseSymmetrix("vmax-section", loadArrayReport(t, "symmetrix/000299900001"))
	if err != nil {
		t.Fatal(err)
	}
	got := asOldCollectorRows(r, func(d arrayDisk) []string {
		return []string{d.ID, r.Name, d.Name, d.DevID, fmt.Sprint(d.Size), fmt.Sprint(d.Alloc), d.Raid, d.Group}
	})
	compareOldCollectorRows(t, got, loadOldCollectorRows(t, "symmetrix/000299900001"))
}
