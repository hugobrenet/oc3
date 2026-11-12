package aischedule

import (
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	days, err := Validate([]string{"fri", "mon", "mon"}, "08:00", "Europe/Paris")
	if err != nil || len(days) != 2 || days[0] != "mon" || days[1] != "fri" {
		t.Fatalf("days %v, %v", days, err)
	}
	for name, c := range map[string]struct {
		days     []string
		at, zone string
	}{
		"no day":   {nil, "08:00", "UTC"},
		"bad day":  {[]string{"lun"}, "08:00", "UTC"},
		"bad time": {[]string{"mon"}, "8:00", "UTC"},
		"hour 24":  {[]string{"mon"}, "24:00", "UTC"},
		"no zone":  {[]string{"mon"}, "08:00", ""},
		"bad zone": {[]string{"mon"}, "08:00", "Mars/Olympus"},
	} {
		if _, err := Validate(c.days, c.at, c.zone); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestNextRun(t *testing.T) {
	paris, _ := time.LoadLocation("Europe/Paris")
	weekdays := []string{"mon", "tue", "wed", "thu", "fri"}
	for name, c := range map[string]struct {
		days  []string
		after time.Time
		want  time.Time
	}{
		// Wednesday 12 November 2026, 07:00 in Paris: today at 08:00.
		"later today": {weekdays, time.Date(2026, 11, 12, 7, 0, 0, 0, paris), time.Date(2026, 11, 12, 8, 0, 0, 0, paris)},
		// At 08:00 exactly: the run of the day is past, tomorrow.
		"at the time": {weekdays, time.Date(2026, 11, 12, 8, 0, 0, 0, paris), time.Date(2026, 11, 13, 8, 0, 0, 0, paris)},
		// Friday evening: Monday.
		"over the weekend": {weekdays, time.Date(2026, 11, 13, 18, 0, 0, 0, paris), time.Date(2026, 11, 16, 8, 0, 0, 0, paris)},
		// One day a week, that day past: the next week.
		"weekly": {[]string{"wed"}, time.Date(2026, 11, 12, 9, 0, 0, 0, paris), time.Date(2026, 11, 18, 8, 0, 0, 0, paris)},
		// The clocks go back on 25 October 2026: 08:00 is still 08:00 in Paris.
		"across a change of the clocks": {[]string{"mon"}, time.Date(2026, 10, 24, 12, 0, 0, 0, paris), time.Date(2026, 10, 26, 8, 0, 0, 0, paris)},
	} {
		got, err := NextRun(c.days, "08:00", "Europe/Paris", c.after)
		if err != nil || !got.Equal(c.want) || got.Location() != time.UTC {
			t.Errorf("%s: %v (%v), want %v", name, got, err, c.want.UTC())
		}
	}
}
