package serverhandlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/server"
)

// The bounds of the resources and the skills of the AI assistant: the MCP
// servers serve them as they are, and the agent refuses a skill whose
// frontmatter it could not read, so the collector refuses them first.
const (
	aiMaxName        = 64
	aiMaxTitle       = 128
	aiMaxDescription = 1024
	aiMaxBody        = 65536
	aiMaxTools       = 24
	aiMaxPerBackend  = 100
)

var (
	aiNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	aiToolPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
)

// checkAIName checks the name of a resource or of a skill.
func checkAIName(name string) error {
	if name == "" {
		return fmt.Errorf("name: mandatory")
	}
	if len(name) > aiMaxName {
		return fmt.Errorf("name: more than %d bytes", aiMaxName)
	}
	if !aiNamePattern.MatchString(name) {
		return fmt.Errorf("name: %q is not lowercase letters and digits separated by single dashes", name)
	}
	return nil
}

// checkAIBackends checks the backends an object is for, and gives them
// sorted.
func checkAIBackends(backends []server.AIBackend) ([]string, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("backends: at least one of om3, oc3")
	}
	var out []string
	for _, b := range backends {
		if b != server.Om3 && b != server.Oc3 {
			return nil, fmt.Errorf("backends: %q is not om3 nor oc3", b)
		}
		if slices.Contains(out, string(b)) {
			return nil, fmt.Errorf("backends: %q given twice", b)
		}
		out = append(out, string(b))
	}
	slices.Sort(out)
	return out, nil
}

// checkAIPublications checks the ids of the groups an object is published
// to, and gives them sorted; none, everybody reads it.
func checkAIPublications(groups *[]int) ([]int, error) {
	out := []int{}
	if groups == nil {
		return out, nil
	}
	for _, g := range *groups {
		if g <= 0 {
			return nil, fmt.Errorf("publications: %d is not a group id", g)
		}
		if slices.Contains(out, g) {
			return nil, fmt.Errorf("publications: group %d given twice", g)
		}
		out = append(out, g)
	}
	slices.Sort(out)
	return out, nil
}

// checkAIBody checks the text of an object.
func checkAIBody(body string) error {
	switch {
	case body == "":
		return fmt.Errorf("body: mandatory")
	case len(body) > aiMaxBody:
		return fmt.Errorf("body: more than %d bytes", aiMaxBody)
	case !utf8.ValidString(body):
		return fmt.Errorf("body: not valid UTF-8")
	case strings.ContainsRune(body, 0):
		return fmt.Errorf("body: holds a NUL byte")
	}
	return nil
}

// aiDigest is the digest of a body, as the MCP Skills extension gives it.
func aiDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// checkAIResource checks a resource named name, and gives it as stored,
// its time and author left to the caller.
func checkAIResource(in server.AIResourceInput, name string) (cdb.AIContent, error) {
	c := cdb.AIContent{Name: name, MimeType: string(server.Textmarkdown)}
	if err := checkAIName(name); err != nil {
		return c, err
	}
	if in.Title != nil {
		c.Title = strings.TrimSpace(*in.Title)
	}
	if len(c.Title) > aiMaxTitle {
		return c, fmt.Errorf("title: more than %d bytes", aiMaxTitle)
	}
	if strings.ContainsAny(c.Title, "\r\n") {
		return c, fmt.Errorf("title: more than one line")
	}
	c.Description = strings.TrimSpace(in.Description)
	if c.Description == "" {
		return c, fmt.Errorf("description: mandatory, it tells the model what the text holds and when to read it")
	}
	if len(c.Description) > aiMaxDescription {
		return c, fmt.Errorf("description: more than %d bytes", aiMaxDescription)
	}
	if in.MimeType != nil {
		if *in.MimeType != server.Textmarkdown && *in.MimeType != server.Textplain {
			return c, fmt.Errorf("mime_type: %q is not text/markdown nor text/plain", *in.MimeType)
		}
		c.MimeType = string(*in.MimeType)
	}
	var err error
	if c.Backends, err = checkAIBackends(in.Backends); err != nil {
		return c, err
	}
	if c.Publications, err = checkAIPublications(in.Publications); err != nil {
		return c, err
	}
	if err := checkAIBody(in.Body); err != nil {
		return c, err
	}
	c.Body, c.Size, c.Digest = in.Body, len(in.Body), aiDigest(in.Body)
	return c, nil
}

// checkAISkill checks a skill named name, its SKILL.md and its frontmatter,
// and gives it as stored, its time and author left to the caller.
func checkAISkill(in server.AISkillInput, name string) (cdb.AIContent, error) {
	c := cdb.AIContent{Name: name}
	if err := checkAIName(name); err != nil {
		return c, err
	}
	var err error
	if c.Backends, err = checkAIBackends(in.Backends); err != nil {
		return c, err
	}
	if c.Publications, err = checkAIPublications(in.Publications); err != nil {
		return c, err
	}
	if err := checkAIBody(in.Body); err != nil {
		return c, err
	}
	frontmatter, err := parseSkillFrontmatter(in.Body)
	if err != nil {
		return c, err
	}
	if got, _ := frontmatter["name"].(string); got != name {
		return c, fmt.Errorf("body: the name of the frontmatter, %q, is not the name of the skill, %q", got, name)
	}
	description, _ := frontmatter["description"].(string)
	if strings.TrimSpace(description) == "" {
		return c, fmt.Errorf("body: the frontmatter has no description")
	}
	if len(description) > aiMaxDescription {
		return c, fmt.Errorf("body: the description of the frontmatter is more than %d bytes", aiMaxDescription)
	}
	if metadata, ok := frontmatter["metadata"].(map[string]string); ok {
		if tools, ok := metadata["opensvc/tools"]; ok {
			names := strings.Fields(tools)
			if len(names) > aiMaxTools {
				return c, fmt.Errorf("body: metadata.opensvc/tools names more than %d tools", aiMaxTools)
			}
			for _, tool := range names {
				if !aiToolPattern.MatchString(tool) {
					return c, fmt.Errorf("body: metadata.opensvc/tools: %q is not a tool name", tool)
				}
			}
		}
	}
	data, err := json.Marshal(frontmatter)
	if err != nil {
		return c, fmt.Errorf("body: the frontmatter: %w", err)
	}
	c.Frontmatter = string(data)
	c.Body, c.Size, c.Digest = in.Body, len(in.Body), aiDigest(in.Body)
	return c, nil
}

// parseSkillFrontmatter reads the frontmatter of a SKILL.md, a YAML map
// between two --- lines that start it, in the form the agent reads: its
// values strings, plain, quoted or in a block, or maps of such strings on one
// level. A key holding anything else, a list, a deeper map or an alias, is
// refused, as the agent would refuse the skill.
func parseSkillFrontmatter(body string) (map[string]any, error) {
	lines := strings.Split(body, "\n")
	if strings.TrimRight(lines[0], "\r") != "---" {
		return nil, fmt.Errorf("body: no frontmatter, the SKILL.md does not start with a --- line")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("body: the frontmatter has no closing --- line")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &doc); err != nil {
		return nil, fmt.Errorf("body: the frontmatter is not valid YAML: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("body: the frontmatter is not a YAML map")
	}
	out := map[string]any{}
	pairs := doc.Content[0].Content
	for i := 0; i+1 < len(pairs); i += 2 {
		key, value := pairs[i], pairs[i+1]
		if key.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("body: the frontmatter has a key that is not a string")
		}
		if _, dup := out[key.Value]; dup {
			return nil, fmt.Errorf("body: the frontmatter key %q is given twice", key.Value)
		}
		switch value.Kind {
		case yaml.ScalarNode:
			if value.Tag == "!!null" {
				return nil, fmt.Errorf("body: the frontmatter key %q has no value", key.Value)
			}
			out[key.Value] = value.Value
		case yaml.MappingNode:
			inner := map[string]string{}
			for j := 0; j+1 < len(value.Content); j += 2 {
				k, v := value.Content[j], value.Content[j+1]
				if k.Kind != yaml.ScalarNode || v.Kind != yaml.ScalarNode || v.Tag == "!!null" {
					return nil, fmt.Errorf("body: the frontmatter key %q is not a map of strings on one level", key.Value)
				}
				if _, dup := inner[k.Value]; dup {
					return nil, fmt.Errorf("body: the frontmatter key %q.%q is given twice", key.Value, k.Value)
				}
				inner[k.Value] = v.Value
			}
			out[key.Value] = inner
		default:
			return nil, fmt.Errorf("body: the frontmatter key %q is not a string nor a map of strings", key.Value)
		}
	}
	return out, nil
}
