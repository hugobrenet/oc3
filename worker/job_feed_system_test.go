package worker

import "testing"

func TestDropUnsetOptionalNodeProperties(t *testing.T) {
	data := map[string]any{
		"connect_to":  map[string]any{"source": "default", "title": "connect to", "value": ""},
		"sec_zone":    map[string]any{"source": "default", "title": "security zone", "value": nil},
		"loc_city":    map[string]any{"source": "config", "title": "loc, city", "value": "Paris"},
		"team_integ":  map[string]any{"title": "team, integration", "value": nil},
		"enclosure":   map[string]any{"source": "default", "title": "enclosure", "value": nil},
		"nodename":    map[string]any{"source": "probe", "title": "nodename", "value": "n1"},
		"connect_tox": map[string]any{"source": "default"},
	}
	dropUnsetOptionalNodeProperties(data)
	for _, k := range []string{"connect_to", "sec_zone", "team_integ"} {
		if _, ok := data[k]; ok {
			t.Errorf("%s must be dropped", k)
		}
	}
	for _, k := range []string{"loc_city", "enclosure", "nodename", "connect_tox"} {
		if _, ok := data[k]; !ok {
			t.Errorf("%s must be kept", k)
		}
	}
}
