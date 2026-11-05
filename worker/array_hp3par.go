package worker

import (
	"encoding/json"
	"fmt"
	"strings"
)

type (
	hp3parVV struct {
		Name      string  `json:"Name"`
		WWN       string  `json:"VV_WWN"`
		VSizeMB   flexInt `json:"VSize_MB"`
		TotRsvdMB flexInt `json:"Tot_Rsvd_MB"`
		UsrCPG    string  `json:"UsrCPG"`
	}

	hp3parNode struct {
		DataMem    flexInt `json:"Data_Mem"`
		ControlMem flexInt `json:"Control_Mem"`
	}

	hp3parNamed struct {
		Name string `json:"Name"`
	}

	hp3parSys struct {
		Model string `json:"Model"`
	}

	hp3parPort struct {
		PortWWN string `json:"Port_WWN"`
	}

	hp3parVersion struct {
		Version string `json:"Version"`
	}
)

// parseHp3par makes the rows of an hp 3par array, from the outputs of its
// cli commands, as the old collector did in insert_hp3par: the array is
// named as the agent names it, its cache is the data and control memory of
// its nodes, its disk groups are its cpgs, its target ids the wwn of its
// ports, and its disks its virtual volumes, whose sizes the cli gives in MB.
//
// The old collector reported no size for a cpg: they are 0.
func parseHp3par(name string, data map[string]json.RawMessage) (*arrayRows, error) {
	var (
		vvs     []hp3parVV
		cpgs    []hp3parNamed
		sys     []hp3parSys
		nodes   []hp3parNode
		ports   []hp3parPort
		version hp3parVersion
	)
	for section, v := range map[string]any{
		"showvv":      &vvs,
		"showcpg":     &cpgs,
		"showsys":     &sys,
		"shownode":    &nodes,
		"showport":    &ports,
		"showversion": &version,
	} {
		if err := arraySection(data, section, v); err != nil {
			return nil, err
		}
	}
	if len(sys) == 0 {
		return nil, fmt.Errorf("section showsys: no system")
	}
	r := &arrayRows{
		Name:     name,
		Model:    sys[0].Model,
		Firmware: version.Version,
	}
	for _, node := range nodes {
		r.Cache += int64(node.DataMem) + int64(node.ControlMem)
	}
	for _, cpg := range cpgs {
		r.DGs = append(r.DGs, arrayDG{Name: cpg.Name})
	}
	for _, port := range ports {
		// A port not connected has no wwn: "-".
		if port.PortWWN == "" || port.PortWWN == "-" {
			continue
		}
		r.TgtIDs = append(r.TgtIDs, strings.ToLower(port.PortWWN))
	}
	for _, vv := range vvs {
		r.Disks = append(r.Disks, arrayDisk{
			ID:    strings.ToLower(vv.WWN),
			Name:  vv.Name,
			Size:  int64(vv.VSizeMB),
			Alloc: int64(vv.TotRsvdMB),
			Group: vv.UsrCPG,
		})
	}
	return r, nil
}
