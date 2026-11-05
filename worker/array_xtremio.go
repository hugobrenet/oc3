package worker

import (
	"encoding/json"
	"fmt"
	"strings"
)

type (
	xtremioCluster struct {
		Name            string  `json:"name"`
		SWVersion       string  `json:"sys-sw-version"`
		UDSSDSpace      flexInt `json:"ud-ssd-space"`
		UDSSDSpaceInUse flexInt `json:"ud-ssd-space-in-use"`
	}

	xtremioVolume struct {
		Name              string          `json:"name"`
		NAAName           string          `json:"naa-name"`
		Index             flexInt         `json:"index"`
		VolSize           flexInt         `json:"vol-size"`
		LogicalSpaceInUse flexInt         `json:"logical-space-in-use"`
		CreatedFromVolume json.RawMessage `json:"created-from-volume"`
	}

	xtremioTarget struct {
		PortType    string `json:"port-type"`
		PortAddress string `json:"port-address"`
	}
)

// parseXtremio makes the rows of an xtremio cluster, as the old collector
// did in insert_xtremio: the array is named as the cluster names itself,
// with a single disk group, "default", its target ids are the wwn of its fc
// ports and the iqn of its iscsi ports, and its disks are its volumes and
// snapshots. The sizes of the api are in KB.
//
// The old collector skipped the iscsi ports, by a mistake in the test of
// their type, and looked the array up by the name the agent sent rather than
// by the one it inserted.
func parseXtremio(_ string, data map[string]json.RawMessage) (*arrayRows, error) {
	var (
		clusters []xtremioCluster
		volumes  []xtremioVolume
		targets  []xtremioTarget
	)
	if err := arraySection(data, "clusters_details", &clusters); err != nil {
		return nil, err
	}
	if len(clusters) == 0 || clusters[0].Name == "" {
		return nil, fmt.Errorf("section clusters_details: no cluster name")
	}
	if err := arraySection(data, "volumes_details", &volumes); err != nil {
		return nil, err
	}
	if err := arraySection(data, "targets_details", &targets); err != nil {
		return nil, err
	}
	c := clusters[0]
	total := int64(c.UDSSDSpace) / 1024
	used := int64(c.UDSSDSpaceInUse) / 1024
	r := &arrayRows{
		Name:     c.Name,
		Model:    "XtremIO",
		Firmware: c.SWVersion,
		DGs:      []arrayDG{{Name: "default", Free: total - used, Used: used, Size: total}},
	}
	for _, target := range targets {
		switch target.PortType {
		case "fc":
			r.TgtIDs = append(r.TgtIDs, strings.ToLower(strings.ReplaceAll(target.PortAddress, ":", "")))
		case "iscsi":
			r.TgtIDs = append(r.TgtIDs, target.PortAddress)
		}
	}
	for _, v := range volumes {
		raid := "snapshot"
		if s := strings.TrimSpace(string(v.CreatedFromVolume)); s == "" || s == `""` || s == "null" {
			raid = "volume"
		}
		r.Disks = append(r.Disks, arrayDisk{
			ID:    v.NAAName,
			Name:  v.Name,
			DevID: fmt.Sprint(int64(v.Index)),
			Size:  int64(v.VolSize) / 1024,
			Alloc: int64(v.LogicalSpaceInUse) / 1024,
			Raid:  raid,
			Group: "default",
		})
	}
	return r, nil
}
