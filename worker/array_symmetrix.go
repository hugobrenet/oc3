package worker

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

type (
	symDev struct {
		name          string
		configuration string
		meta          string
		megabytes     int64
		alloc         int64
		wwn           string
		identName     string
		metaCount     int
		diskgroup     *symDiskGroup
		diskgroupName string
	}

	symDiskGroup struct {
		number    int
		name      string
		total     int64
		used      int64
		devs      map[string]bool
		maskedDev map[string]bool
	}

	symDisk struct {
		group     int
		groupName string
	}
)

// symSection returns the xml tree of a section, nil when the section is
// absent or empty: the old collector skipped the outputs it could not read.
func symSection(data map[string]json.RawMessage, section string) (*xmlNode, error) {
	if _, ok := data[section]; !ok {
		return nil, nil
	}
	var s string
	if err := arraySection(data, section, &s); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	n, err := parseXMLNode(s)
	if err != nil {
		return nil, fmt.Errorf("section %s: %w", section, err)
	}
	return n, nil
}

// add counts a device in the disk group of its back-end disk.
func (t *symDiskGroup) add(dev *symDev) {
	if t.devs[dev.name] {
		return
	}
	t.devs[dev.name] = true
	if dev.configuration != "VDEV" {
		t.total += dev.megabytes
	}
}

// addMasked counts a device exported to a host as used in its disk group.
func (t *symDiskGroup) addMasked(dev *symDev) {
	if t.maskedDev[dev.name] {
		return
	}
	t.maskedDev[dev.name] = true
	if dev.configuration != "VDEV" {
		t.used += dev.megabytes
	}
}

// parseSymmetrix makes the rows of an emc symmetrix, vmax or powermax, from
// the xml outputs of the symcli commands the agent reports, as the old
// collector did in insert_sym with its symmetrix module:
//
//   - the array is named by its sid, its firmware is the version and patch
//     level of its microcode and the version of its symmwin;
//   - its disk groups are its back-end disk groups, the devices of their
//     disks counted in their size and the ficon devices in their use, its
//     thin pools and its storage resource pools;
//   - its target ids are the wwn of the ports of its directors;
//   - its disks are its devices, but the members of a meta device, sized by
//     their thin device allocation when they are thin, in the disk group of
//     their back-end disk, or in the storage resource pool of their storage
//     group on a vmax.
//
// The outputs but sym_info, sym_dir_info, sym_dev_info and sym_dev_wwn_info
// are optional, as an array has no pool or no srp depending on its model.
// Where the old collector failed on an element naming a device or a disk
// group it did not know, the element is skipped, and a device without a wwn
// is not reported.
func parseSymmetrix(_ string, data map[string]json.RawMessage) (*arrayRows, error) {
	info := make(map[string]string)
	devs := make(map[string]*symDev)
	devOrder := make([]string, 0)
	diskgroups := make(map[int]*symDiskGroup)
	disks := make(map[string]symDisk)
	r := &arrayRows{}

	// sym_info: the array.
	tree, err := symSection(data, "sym_info")
	if err != nil {
		return nil, err
	}
	if tree == nil {
		return nil, fmt.Errorf("missing section sym_info")
	}
	if l := tree.iter("Symm_Info"); len(l) > 0 {
		for k, v := range l[len(l)-1].children() {
			info[k] = v
		}
	}
	if info["symid"] == "" || info["model"] == "" {
		return nil, fmt.Errorf("section sym_info: no symid or no model")
	}
	isVmax := strings.Contains(info["model"], "VMAX") || strings.Contains(info["model"], "PowerMax")

	// sym_diskgroup_info: the back-end disk groups.
	if tree, err = symSection(data, "sym_diskgroup_info"); err != nil {
		return nil, err
	} else if tree != nil {
		for _, e := range tree.iter("Disk_Group") {
			m := make(map[string]string)
			for i := range e.Nodes {
				for k, v := range e.Nodes[i].children() {
					m[k] = v
				}
			}
			n, err := strconv.Atoi(m["disk_group_number"])
			if err != nil {
				continue
			}
			diskgroups[n] = &symDiskGroup{number: n, name: m["disk_group_name"], devs: map[string]bool{}, maskedDev: map[string]bool{}}
		}
	}

	// sym_pool_info: the thin pools, with their totals.
	if tree, err = symSection(data, "sym_pool_info"); err != nil {
		return nil, err
	} else if tree != nil {
		for _, e := range tree.iter("DevicePool") {
			name := e.text("pool_name")
			totals := e.find("Totals")
			if name == "" || totals == nil {
				continue
			}
			t := totals.children()
			if _, ok := t["total_used_tracks_mb"]; !ok {
				continue
			}
			used, _ := strconv.ParseInt(t["total_used_tracks_mb"], 10, 64)
			free, _ := strconv.ParseInt(t["total_free_tracks_mb"], 10, 64)
			size, _ := strconv.ParseInt(t["total_tracks_mb"], 10, 64)
			r.DGs = append(r.DGs, arrayDG{Name: name, Free: free, Used: used, Size: size})
		}
	}

	// sym_srp_info: the storage resource pools, sized in GB.
	if tree, err = symSection(data, "sym_srp_info"); err != nil {
		return nil, err
	} else if tree != nil {
		for _, e := range tree.iter("SRP") {
			m := make(map[string]string)
			for _, si := range e.findAll("SRP_Info") {
				for k, v := range si.children() {
					m[k] = v
				}
			}
			if m["name"] == "" {
				continue
			}
			if _, ok := m["allocated_capacity_gigabytes"]; !ok {
				continue
			}
			gb := func(k string) int64 {
				f, _ := strconv.ParseFloat(m[k], 64)
				return int64(math.Round(f * 1024))
			}
			r.DGs = append(r.DGs, arrayDG{Name: m["name"], Free: gb("free_capacity_gigabytes"), Used: gb("allocated_capacity_gigabytes"), Size: gb("usable_capacity_gigabytes")})
		}
	}

	// sym_disk_info: the back-end disks, by director:interface:tid.
	if tree, err = symSection(data, "sym_disk_info"); err != nil {
		return nil, err
	} else if tree != nil {
		for _, e := range tree.iter("Disk") {
			di := e.find("Disk_Info")
			if di == nil {
				continue
			}
			m := di.children()
			n, err := strconv.Atoi(m["disk_group"])
			if err != nil {
				continue
			}
			disks[m["da_number"]+":"+m["interface"]+":"+m["tid"]] = symDisk{group: n, groupName: m["disk_group_name"]}
		}
	}

	// sym_dev_info: the devices, in the disk group of their first back-end
	// disk.
	if tree, err = symSection(data, "sym_dev_info"); err != nil {
		return nil, err
	}
	if tree == nil {
		return nil, fmt.Errorf("missing section sym_dev_info")
	}
	for _, e := range tree.iter("Device") {
		dev := &symDev{
			name:          e.text("Dev_Info/dev_name"),
			configuration: e.text("Dev_Info/configuration"),
			meta:          e.text("Flags/meta"),
		}
		if dev.name == "" {
			continue
		}
		dev.megabytes, _ = strconv.ParseInt(e.text("Capacity/megabytes"), 10, 64)
		dev.alloc = dev.megabytes
		if be := e.find("Back_End/Disk"); be != nil {
			id := be.text("director") + ":" + be.text("interface") + ":" + be.text("tid")
			if disk, ok := disks[id]; ok {
				dev.diskgroupName = disk.groupName
				if dg, ok := diskgroups[disk.group]; ok {
					dev.diskgroup = dg
					dg.add(dev)
				}
			}
		}
		if _, ok := devs[dev.name]; !ok {
			devOrder = append(devOrder, dev.name)
		}
		devs[dev.name] = dev
	}

	// sym_dev_name_info: the identifier names of the devices.
	if tree, err = symSection(data, "sym_dev_name_info"); err != nil {
		return nil, err
	} else if tree != nil {
		for _, e := range tree.iter("Dev_Info") {
			if dev, ok := devs[e.text("dev_name")]; ok {
				dev.identName = e.text("dev_ident_name")
			}
		}
	}

	// sym_dev_wwn_info: the wwn of the devices.
	if tree, err = symSection(data, "sym_dev_wwn_info"); err != nil {
		return nil, err
	}
	if tree == nil {
		return nil, fmt.Errorf("missing section sym_dev_wwn_info")
	}
	for _, e := range tree.iter("Device") {
		if dev, ok := devs[e.text("dev_name")]; ok {
			dev.wwn = e.text("wwn")
		}
	}

	// sym_ficondev_info: the ficon devices, used in their disk group.
	if tree, err = symSection(data, "sym_ficondev_info"); err != nil {
		return nil, err
	} else if tree != nil {
		for _, e := range tree.iter("Device") {
			if dev, ok := devs[e.text("Dev_Info/dev_name")]; ok && dev.diskgroup != nil {
				dev.diskgroup.addMasked(dev)
			}
		}
	}

	// sym_meta_info: the members of the meta devices.
	if tree, err = symSection(data, "sym_meta_info"); err != nil {
		return nil, err
	} else if tree != nil {
		for _, e := range tree.iter("Device") {
			if dev, ok := devs[e.text("Dev_Info/dev_name")]; ok {
				dev.metaCount = len(e.findAll("Meta/Meta_Device/dev_name"))
			}
		}
	}

	// sym_dir_info: the microcode and symmwin versions, and the ports of
	// the directors.
	if tree, err = symSection(data, "sym_dir_info"); err != nil {
		return nil, err
	}
	if tree == nil {
		return nil, fmt.Errorf("missing section sym_dir_info")
	}
	for _, e := range tree.iter("Microcode") {
		for k, v := range e.children() {
			info[k] = v
		}
	}
	for _, e := range tree.iter("Symmwin") {
		for k, v := range e.children() {
			info["symmwin_"+k] = v
		}
	}
	for _, e := range tree.iter("Director") {
		for _, p := range e.findAll("Port/Port_Info/port_wwn") {
			if wwn := strings.TrimSpace(p.Content); wwn != "" && wwn != "N/A" {
				r.TgtIDs = append(r.TgtIDs, wwn)
			}
		}
	}

	// sym_tdev_info: the size and allocation of the thin devices.
	if tree, err = symSection(data, "sym_tdev_info"); err != nil {
		return nil, err
	} else if tree != nil {
		for _, e := range tree.iter("Device") {
			dev, ok := devs[e.text("dev_name")]
			if !ok || !e.has("total_tracks_mb") {
				continue
			}
			dev.megabytes, _ = strconv.ParseInt(e.text("total_tracks_mb"), 10, 64)
			dev.alloc, _ = strconv.ParseInt(e.text("alloc_tracks_mb"), 10, 64)
		}
	}

	// sym_sg_info: on a vmax, the devices of a storage group in a storage
	// resource pool are in that pool.
	if isVmax {
		if tree, err = symSection(data, "sym_sg_info"); err != nil {
			return nil, err
		} else if tree != nil {
			for _, e := range tree.findAll("SG") {
				srp := e.text("SG_Info/SRP_name")
				if srp == "none" {
					continue
				}
				for _, d := range e.findAll("DEVS_List/Device") {
					if dev, ok := devs[d.text("dev_name")]; ok {
						dev.diskgroupName = srp
					}
				}
			}
		}
	}

	if info["version"] == "" {
		return nil, fmt.Errorf("section sym_dir_info: no microcode version")
	}
	r.Name = info["symid"]
	r.Model = info["model"]
	r.Cache, _ = strconv.ParseInt(info["cache_megabytes"], 10, 64)
	r.Firmware = strings.Join([]string{info["version"], info["patch_level"], info["symmwin_version"]}, ".")

	numbers := make([]int, 0, len(diskgroups))
	for n := range diskgroups {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)
	dgs := make([]arrayDG, 0, len(numbers))
	for _, n := range numbers {
		dg := diskgroups[n]
		dgs = append(dgs, arrayDG{Name: dg.name, Free: dg.total - dg.used, Used: dg.used, Size: dg.total})
	}
	r.DGs = append(dgs, r.DGs...)

	for _, name := range devOrder {
		dev := devs[name]
		if dev.meta != "Head" && dev.meta != "None" {
			continue
		}
		if dev.wwn == "" {
			continue
		}
		r.Disks = append(r.Disks, arrayDisk{
			ID:    dev.wwn,
			Name:  dev.identName,
			DevID: dev.name,
			Size:  dev.megabytes,
			Alloc: dev.alloc,
			Raid:  fmt.Sprintf("Meta-%d %s", dev.metaCount, dev.configuration),
			Group: dev.diskgroupName,
		})
	}
	return r, nil
}
