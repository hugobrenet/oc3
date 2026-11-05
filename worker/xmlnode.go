package worker

import (
	"encoding/xml"
	"strings"
)

// xmlNode is an element of an xml document read without knowing its schema,
// with the lookups of the ElementTree api the old collector parsers used.
type xmlNode struct {
	XMLName xml.Name
	Content string    `xml:",chardata"`
	Nodes   []xmlNode `xml:",any"`
}

func parseXMLNode(s string) (*xmlNode, error) {
	var n xmlNode
	if err := xml.Unmarshal([]byte(s), &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// iter returns the elements of a name, the element itself included, at any
// depth, in document order, as ElementTree.iter.
func (t *xmlNode) iter(name string) []*xmlNode {
	l := make([]*xmlNode, 0)
	var walk func(*xmlNode)
	walk = func(n *xmlNode) {
		if n.XMLName.Local == name {
			l = append(l, n)
		}
		for i := range n.Nodes {
			walk(&n.Nodes[i])
		}
	}
	walk(t)
	return l
}

// findAll returns the elements at the path of child names, "a/b", as
// ElementTree.findall.
func (t *xmlNode) findAll(path string) []*xmlNode {
	l := []*xmlNode{t}
	for _, name := range strings.Split(path, "/") {
		next := make([]*xmlNode, 0)
		for _, n := range l {
			for i := range n.Nodes {
				if n.Nodes[i].XMLName.Local == name {
					next = append(next, &n.Nodes[i])
				}
			}
		}
		l = next
	}
	return l
}

// find returns the first element at the path, or nil.
func (t *xmlNode) find(path string) *xmlNode {
	if l := t.findAll(path); len(l) > 0 {
		return l[0]
	}
	return nil
}

// text returns the text of the element at the path, trimmed, empty when
// there is none.
func (t *xmlNode) text(path string) string {
	if n := t.find(path); n != nil {
		return strings.TrimSpace(n.Content)
	}
	return ""
}

// has tells if the element at the path exists.
func (t *xmlNode) has(path string) bool {
	return t.find(path) != nil
}

// children returns the texts of the child elements by name, the last one of
// a name winning, as the old collector read an element into a dict.
func (t *xmlNode) children() map[string]string {
	m := make(map[string]string, len(t.Nodes))
	for _, n := range t.Nodes {
		m[n.XMLName.Local] = strings.TrimSpace(n.Content)
	}
	return m
}
