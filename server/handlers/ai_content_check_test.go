package serverhandlers

import (
	"strings"
	"testing"

	"github.com/opensvc/oc3/server"
)

const skillMD = `---
name: maintenance
description: |
  The maintenance of a node: freeze, drain, then stop.
metadata:
  opensvc/tools: freeze_node drain_node get_node_status
---
# Maintenance
`

func TestCheckAIResource(t *testing.T) {
	title, plain := "Naming", server.Textplain
	groups := []int{12, 3}
	c, err := checkAIResource(server.AIResourceInput{
		Title: &title, Description: " The naming rules. ", MimeType: &plain,
		Backends: []server.AIBackend{server.Oc3, server.Om3}, Publications: &groups, Body: "abc",
	}, "naming")
	if err != nil {
		t.Fatal(err)
	}
	if c.Description != "The naming rules." || c.MimeType != "text/plain" || strings.Join(c.Backends, ",") != "oc3,om3" ||
		c.Publications[0] != 3 || c.Size != 3 ||
		c.Digest != "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("checked %+v", c)
	}
	base := server.AIResourceInput{Description: "d", Backends: []server.AIBackend{server.Om3}, Body: "x"}
	long := strings.Repeat("x", aiMaxBody+1)
	twoLines := "a\nb"
	for name, c := range map[string]struct {
		name string
		edit func(*server.AIResourceInput)
		says string
	}{
		"no name":        {"", func(*server.AIResourceInput) {}, "name: mandatory"},
		"bad name":       {"Naming_rules", func(*server.AIResourceInput) {}, "name:"},
		"no description": {"n", func(in *server.AIResourceInput) { in.Description = " " }, "description: mandatory"},
		"no backend":     {"n", func(in *server.AIResourceInput) { in.Backends = nil }, "backends:"},
		"bad backend":    {"n", func(in *server.AIResourceInput) { in.Backends = []server.AIBackend{"om2"} }, `"om2" is not`},
		"twice":          {"n", func(in *server.AIResourceInput) { in.Backends = []server.AIBackend{server.Om3, server.Om3} }, "given twice"},
		"long body":      {"n", func(in *server.AIResourceInput) { in.Body = long }, "more than 65536 bytes"},
		"NUL":            {"n", func(in *server.AIResourceInput) { in.Body = "a\x00b" }, "NUL"},
		"two lines":      {"n", func(in *server.AIResourceInput) { in.Title = &twoLines }, "title: more than one line"},
	} {
		in := base
		c.edit(&in)
		if _, err := checkAIResource(in, c.name); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want %q", name, err, c.says)
		}
	}
}

func TestCheckAISkill(t *testing.T) {
	c, err := checkAISkill(server.AISkillInput{Backends: []server.AIBackend{server.Om3}, Body: skillMD}, "maintenance")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Frontmatter, `"opensvc/tools":"freeze_node drain_node get_node_status"`) ||
		!strings.Contains(c.Frontmatter, `"description":"The maintenance of a node: freeze, drain, then stop.\n"`) {
		t.Errorf("frontmatter %s", c.Frontmatter)
	}
	tools := strings.Repeat("t ", aiMaxTools+1)
	for name, c := range map[string]struct{ body, says string }{
		"other name":     {strings.Replace(skillMD, "name: maintenance", "name: other", 1), `is not the name of the skill`},
		"no description": {"---\nname: maintenance\n---\n", "no description"},
		"no frontmatter": {"# Maintenance\n", "does not start with a --- line"},
		"not closed":     {"---\nname: maintenance\n", "no closing --- line"},
		"a list":         {"---\nname: maintenance\ndescription: d\ntags: [a, b]\n---\n", `"tags" is not a string nor a map`},
		"deep map":       {"---\nname: maintenance\ndescription: d\nmetadata:\n  a:\n    b: c\n---\n", "one level"},
		"no value":       {"---\nname: maintenance\ndescription:\n---\n", `"description" has no value`},
		"twice":          {"---\nname: maintenance\nname: maintenance\ndescription: d\n---\n", "given twice"},
		"many tools":     {"---\nname: maintenance\ndescription: d\nmetadata:\n  opensvc/tools: " + tools + "\n---\n", "more than 24 tools"},
		"bad tool":       {"---\nname: maintenance\ndescription: d\nmetadata:\n  opensvc/tools: a;b\n---\n", `"a;b" is not a tool name`},
	} {
		if _, err := checkAISkill(server.AISkillInput{Backends: []server.AIBackend{server.Om3}, Body: c.body}, "maintenance"); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want %q", name, err, c.says)
		}
	}
	// A SKILL.md written on Windows reads as one written elsewhere.
	if _, err := checkAISkill(server.AISkillInput{Backends: []server.AIBackend{server.Om3}, Body: strings.ReplaceAll(skillMD, "\n", "\r\n")}, "maintenance"); err != nil {
		t.Errorf("CRLF: %v", err)
	}
}
