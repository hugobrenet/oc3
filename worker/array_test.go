package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadArrayReport returns the sections of the report of testdata/array/<dir>,
// a file per section, named after it.
func loadArrayReport(t *testing.T, dir string) map[string]json.RawMessage {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "array", dir, "*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no report in %s: %v", dir, err)
	}
	data := make(map[string]json.RawMessage)
	for _, f := range files {
		if filepath.Base(f) == "README" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		section := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		if filepath.Ext(f) != ".json" {
			// A command output: a json string.
			b, _ = json.Marshal(string(b))
		}
		data[section] = b
	}
	return data
}
