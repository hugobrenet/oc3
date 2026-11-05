package worker

import (
	"encoding/json"
	"fmt"
	"strings"
)

type (
	pureArray struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}

	pureVolume struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Serial      string `json:"serial"`
		Provisioned int64  `json:"provisioned"`
		Space       struct {
			TotalPhysical int64 `json:"total_physical"`
		} `json:"space"`
		SubType string `json:"subtype"`
	}

	purePort struct {
		WWN string `json:"wwn"`
	}
)

// pureWWIDPrefix is the prefix of the wwid of a pure volume, before its
// serial.
const pureWWIDPrefix = "624a9370"

// parsePure makes the rows of a pure array, as the old collector did in
// insert_pure: the array is named as it names itself, its target ids are
// the wwn of its fc ports, its disks are its volumes, and it reports no disk
// group.
func parsePure(_ string, data map[string]json.RawMessage) (*arrayRows, error) {
	var (
		arrays  []pureArray
		volumes []pureVolume
		ports   []purePort
	)
	if err := arraySection(data, "arrays", &arrays); err != nil {
		return nil, err
	}
	if len(arrays) == 0 || arrays[0].Name == "" {
		return nil, fmt.Errorf("section arrays: no array name")
	}
	if err := arraySection(data, "volumes", &volumes); err != nil {
		return nil, err
	}
	if err := arraySection(data, "ports", &ports); err != nil {
		return nil, err
	}
	r := &arrayRows{
		Name:     arrays[0].Name,
		Model:    "Pure",
		Firmware: arrays[0].Version,
	}
	for _, port := range ports {
		// An iscsi or nvme port has no wwn.
		if port.WWN == "" {
			continue
		}
		r.TgtIDs = append(r.TgtIDs, strings.ToLower(strings.ReplaceAll(port.WWN, ":", "")))
	}
	for _, v := range volumes {
		r.Disks = append(r.Disks, arrayDisk{
			ID:    pureWWIDPrefix + strings.ToLower(v.Serial),
			Name:  v.Name,
			DevID: v.ID,
			Size:  mb(v.Space.TotalPhysical),
			Alloc: mb(v.Provisioned),
			Raid:  v.SubType,
		})
	}
	return r, nil
}
