package worker

import (
	"encoding/json"
	"fmt"
	"strings"
)

type (
	hdsArray struct {
		ArrayType         string `json:"arrayType"`
		ControllerVersion string `json:"controllerVersion"`
	}

	hdsLU struct {
		ObjectID             string  `json:"objectID"`
		DisplayName          string  `json:"displayName"`
		CapacityInKB         flexInt `json:"capacityInKB"`
		ConsumedCapacityInKB flexInt `json:"consumedCapacityInKB"`
		Label                string  `json:"label"`
		DPPoolID             string  `json:"dpPoolID"`
	}

	hdsPool struct {
		Name             string  `json:"name"`
		PoolID           string  `json:"poolID"`
		CapacityInKB     flexInt `json:"capacityInKB"`
		FreeCapacityInKB flexInt `json:"freeCapacityInKB"`
		RaidLevel        string  `json:"raidLevel"`
	}

	hdsPort struct {
		WWPN string `json:"worldWidePortName"`
	}
)

// hdsLdevOffset is where the ldev starts in the wwid of an hds disk a node
// reports.
const hdsLdevOffset = 26

// parseHds makes the rows of an hitachi array, from the Device Manager
// answers the agent reports, as the old collector did in insert_hds: the
// array is named as the agent names it, its disk groups are its pools, its
// target ids the wwn of its ports, and its disks its logical units, sizes in
// KB, under the id the nodes see them by when they do.
//
// As the old collector, the disk_devid of a disk is its display name, and its
// disk_name its label.
func parseHds(name string, data map[string]json.RawMessage) (*arrayRows, error) {
	var (
		arrays []hdsArray
		lus    []hdsLU
		pools  []hdsPool
		ports  []hdsPort
	)
	for section, v := range map[string]any{
		"array": &arrays,
		"lu":    &lus,
		"pool":  &pools,
		"port":  &ports,
	} {
		if err := arraySection(data, section, v); err != nil {
			return nil, err
		}
	}
	if len(arrays) == 0 {
		return nil, fmt.Errorf("section array: no storage array")
	}
	r := &arrayRows{
		Name:     name,
		Model:    arrays[0].ArrayType,
		Firmware: arrays[0].ControllerVersion,
	}
	poolByID := make(map[string]hdsPool)
	for _, p := range pools {
		// The old collector read the named pools only.
		if p.Name == "" {
			continue
		}
		poolByID[p.PoolID] = p
		size := int64(p.CapacityInKB) / 1024
		free := int64(p.FreeCapacityInKB) / 1024
		r.DGs = append(r.DGs, arrayDG{Name: p.Name, Free: free, Used: size - free, Size: size})
	}
	for _, p := range ports {
		if p.WWPN == "" {
			continue
		}
		r.TgtIDs = append(r.TgtIDs, strings.ToLower(strings.ReplaceAll(p.WWPN, ".", "")))
	}
	// The disks the nodes see of this array have an id starting with the
	// one of its first port.
	if len(r.TgtIDs) > 0 && len(r.TgtIDs[0]) >= 12 {
		r.SvcdiskPrefix = "60" + r.TgtIDs[0][2:12]
	}
	for _, lu := range lus {
		// "LU.HUSVM.210945.100": the serial and the device number.
		var id string
		if parts := strings.Split(lu.ObjectID, "."); len(parts) > 2 {
			id = strings.Join(parts[2:], ".")
		}
		disk := arrayDisk{
			ID:    id,
			DevID: lu.DisplayName,
			Name:  lu.Label,
			Size:  int64(lu.CapacityInKB) / 1024,
			Alloc: int64(lu.ConsumedCapacityInKB) / 1024,
			Group: lu.DPPoolID,
			Ldev:  strings.ToLower(strings.ReplaceAll(lu.DisplayName, ":", "")),
		}
		if p, ok := poolByID[lu.DPPoolID]; ok {
			disk.Group = p.Name
			disk.Raid = p.RaidLevel
		}
		r.Disks = append(r.Disks, disk)
	}
	return r, nil
}

// setSvcdiskIDs gives the disks the id among ids, the ids the nodes report
// the disks of the array by, that ends with their ldev.
func (r *arrayRows) setSvcdiskIDs(ids []string) {
	byLdev := make(map[string]string, len(ids))
	for _, id := range ids {
		if len(id) > hdsLdevOffset {
			byLdev[id[hdsLdevOffset:]] = id
		}
	}
	for i, disk := range r.Disks {
		if id, ok := byLdev[disk.Ldev]; ok && disk.Ldev != "" {
			r.Disks[i].ID = id
		}
	}
}
