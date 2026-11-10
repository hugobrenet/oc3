package main

import (
	"fmt"
	"sort"
	"strings"
)

// The SAN of a fleet with arrays: two fabrics, a and b, each made of a core
// switch the array ports are plugged in, and edge switches the node adapters
// are plugged in, each edge linked to the core by a trunk of two
// inter-switch links. The adapter of fabric a of a node is its first one.

// edgePorts is the number of node adapters an edge switch takes.
const edgePorts = 24

type (
	sanSwitch struct {
		name   string
		wwn    string // 16 hex digits
		domain int
		fabric int
		ports  []sanPort
	}

	sanPort struct {
		speed  string
		kind   string // F-Port or E-Port
		remote string // 16 hex digits: the port or the switch at the other end
		// remoteName is the name of the switch at the other end of an E-Port.
		remoteName string
		// node is the node of the adapter plugged in an F-Port, nil for an
		// array port.
		node *node
	}
)

var fabrics = []string{"a", "b"}

// colonWWN writes a wwn of 16 hex digits as the switches do.
func colonWWN(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(s[i : i+2])
	}
	return b.String()
}

// nodeHBAIDs are the wwn of the adapters of a node, one a fabric.
func nodeHBAIDs(spec fleetSpec, n *node) []string {
	r := spec.newRand("hba", n.name)
	l := make([]string, len(fabrics))
	for i := range l {
		l[i] = fmt.Sprintf("2100%012x", r.Uint64()&0xffffffffffff)
	}
	return l
}

func newSANSwitch(spec fleetSpec, name string, domain int) *sanSwitch {
	r := spec.newRand("switch", name)
	return &sanSwitch{name: name, wwn: fmt.Sprintf("1000000533%06x", r.Uint32()&0xffffff), domain: domain}
}

// fleetSANSwitches are the switches of the two fabrics of a fleet.
func fleetSANSwitches(spec fleetSpec, f *fleet) []*sanSwitch {
	if spec.Arrays == 0 {
		return nil
	}
	var nodes []*node
	for _, c := range f.clusters {
		nodes = append(nodes, c.nodes...)
	}
	nEdges := (len(nodes) + edgePorts - 1) / edgePorts
	var all []*sanSwitch
	for fi, fab := range fabrics {
		core := newSANSwitch(spec, fmt.Sprintf("%s-fab%s-core", spec.Prefix, fab), 1)
		core.fabric = fi
		for ai := 0; ai < spec.Arrays; ai++ {
			for pi, wwn := range arrayPortWWNs(spec, ai) {
				if pi%2 == fi {
					core.ports = append(core.ports, sanPort{speed: "N32", kind: "F-Port", remote: wwn})
				}
			}
		}
		all = append(all, core)
		for ei := 0; ei < nEdges; ei++ {
			edge := newSANSwitch(spec, fmt.Sprintf("%s-fab%s-edge%d", spec.Prefix, fab, ei+1), 2+ei)
			edge.fabric = fi
			for _, n := range nodes[ei*edgePorts : min(len(nodes), (ei+1)*edgePorts)] {
				edge.ports = append(edge.ports, sanPort{speed: "N16", kind: "F-Port", remote: nodeHBAIDs(spec, n)[fi], node: n})
			}
			for range 2 {
				edge.ports = append(edge.ports, sanPort{speed: "32G", kind: "E-Port", remote: core.wwn, remoteName: core.name})
				core.ports = append(core.ports, sanPort{speed: "32G", kind: "E-Port", remote: edge.wwn, remoteName: edge.name})
			}
			all = append(all, edge)
		}
	}
	return all
}

// sanSwitchBody is the report of a brocade switch, as the agent pushes it:
// the outputs of switchshow, nsshow and zoneshow. The zoning, the same on
// every switch of a fabric, has a zone by adapter with the array ports it
// sees.
func sanSwitchBody(spec fleetSpec, f *fleet, sw *sanSwitch) map[string]any {
	fabricIdx := sw.fabric
	var show, ns strings.Builder
	fmt.Fprintf(&show, "switchName:\t%s\nswitchType:\t162.0\nswitchState:\tOnline\nswitchMode:\tNative\nswitchRole:\t%s\nswitchDomain:\t%d\nswitchId:\tfffc%02x\nswitchWwn:\t%s\nzoning:\t\tON (%s)\nswitchBeacon:\tOFF\n\n",
		sw.name, map[bool]string{true: "Principal", false: "Subordinate"}[sw.domain == 1], sw.domain, sw.domain, colonWWN(sw.wwn), zoneCfg(spec, fabricIdx))
	show.WriteString("Index Port Address Media Speed State     Proto\n")
	show.WriteString(strings.Repeat("=", 50) + "\n")
	ns.WriteString("{\n Type Pid    COS     PortName                NodeName                 TTL(sec)\n")
	nNS := 0
	for i, p := range sw.ports {
		comment := p.kind + "  " + colonWWN(p.remote)
		if p.kind == "E-Port" {
			comment += fmt.Sprintf(" %q (%s)", p.remoteName, map[bool]string{true: "downstream", false: "upstream"}[sw.domain == 1])
		}
		fmt.Fprintf(&show, "%3d %3d   %02x%02x00   id    %-4s Online      FC  %s\n", i, i, sw.domain, i, p.speed, comment)
		if p.kind == "F-Port" {
			nodeName := "20" + p.remote[2:]
			fmt.Fprintf(&ns, " N    %02x%02x00;      3;%s;%s; na\n    FC4s: FCP\n    Port Index: %d\n", sw.domain, i, colonWWN(p.remote), colonWWN(nodeName), i)
			nNS++
		}
	}
	fmt.Fprintf(&ns, "The Local Name Server has %d entries }\n", nNS)
	return map[string]any{
		"name": sw.name,
		"type": "brocade",
		"data": map[string]any{
			"switchshow": show.String(),
			"nsshow":     ns.String(),
			"zoneshow":   fabricZoneshow(spec, f, fabricIdx),
		},
	}
}

func zoneCfg(spec fleetSpec, fabricIdx int) string {
	return strings.ToUpper(fmt.Sprintf("%s_fab%s_cfg", spec.Prefix, fabrics[fabricIdx]))
}

// fabricZoneshow is the zoning of a fabric: an alias by adapter and by array,
// and a zone by adapter with the ports of its array on the fabric.
func fabricZoneshow(spec fleetSpec, f *fleet, fabricIdx int) string {
	cfg := zoneCfg(spec, fabricIdx)
	type zone struct {
		name    string
		members []string
	}
	aliases := map[string][]string{}
	var zones []zone
	for ai := 0; ai < spec.Arrays; ai++ {
		var l []string
		for pi, wwn := range arrayPortWWNs(spec, ai) {
			if pi%2 == fabricIdx {
				l = append(l, colonWWN(wwn))
			}
		}
		aliases[strings.ToUpper(arrayName(spec, ai))] = l
	}
	for _, c := range f.clusters {
		array := strings.ToUpper(arrayName(spec, c.idx%spec.Arrays))
		for _, n := range c.nodes {
			hba := colonWWN(nodeHBAIDs(spec, n)[fabricIdx])
			alias := strings.ToUpper(n.name) + "_HBA" + fmt.Sprint(fabricIdx)
			aliases[alias] = []string{hba}
			zones = append(zones, zone{name: alias + "_" + array, members: append([]string{hba}, aliases[array]...)})
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Defined configuration:\n cfg:\t%s\n", cfg)
	names := make([]string, 0, len(aliases))
	for a := range aliases {
		names = append(names, a)
	}
	sort.Strings(names)
	for _, a := range names {
		// One port under the alias line, more on the alias line by two.
		ports := aliases[a]
		if len(ports) == 1 {
			fmt.Fprintf(&b, " alias:\t%s\n\t\t%s\n", a, ports[0])
			continue
		}
		fmt.Fprintf(&b, " alias:\t%s\t%s; %s;\n", a, ports[0], ports[1])
		for i := 2; i < len(ports); i += 2 {
			fmt.Fprintf(&b, "\t\t%s\n", strings.Join(ports[i:min(len(ports), i+2)], "; "))
		}
	}
	fmt.Fprintf(&b, "\nEffective configuration:\n cfg:\t%s\n", cfg)
	for _, z := range zones {
		fmt.Fprintf(&b, " zone:\t%s\n", z.name)
		for _, m := range z.members {
			fmt.Fprintf(&b, "\t\t%s\n", strings.ToLower(m))
		}
	}
	return b.String()
}
