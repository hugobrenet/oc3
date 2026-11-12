// Package aischedule holds the rules of the schedules of the AI assistant
// skills: the days, the time and the time zone they run at, and when they run
// next. The API checks a schedule with them, and the scheduler sets its next
// run with them.
package aischedule

import (
	"fmt"
	"regexp"
	"slices"
	"time"
)

// Days are the days of the week a schedule may run on, in their order.
var Days = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

var (
	timePattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	weekdays    = map[time.Weekday]string{
		time.Monday: "mon", time.Tuesday: "tue", time.Wednesday: "wed", time.Thursday: "thu",
		time.Friday: "fri", time.Saturday: "sat", time.Sunday: "sun",
	}
)

// Validate checks the days, the time and the time zone of a schedule, and
// gives back its days in the order of the week, each once.
func Validate(days []string, at, timezone string) ([]string, error) {
	if len(days) == 0 {
		return nil, fmt.Errorf("days: at least one of %v", Days)
	}
	var ordered []string
	for _, d := range Days {
		if slices.Contains(days, d) {
			ordered = append(ordered, d)
		}
	}
	for _, d := range days {
		if !slices.Contains(Days, d) {
			return nil, fmt.Errorf("days: %q is none of %v", d, Days)
		}
	}
	if !timePattern.MatchString(at) {
		return nil, fmt.Errorf("at: %q is not a time HH:MM", at)
	}
	if _, err := time.LoadLocation(timezone); err != nil || timezone == "" {
		return nil, fmt.Errorf("timezone: %q is not a time zone, such as Europe/Paris", timezone)
	}
	return ordered, nil
}

// NextRun is the first time after after a schedule runs at: its time, in its
// time zone, on one of its days. A time a change of the clocks skips runs at
// the time the clocks go to, once.
func NextRun(days []string, at, timezone string, after time.Time) (time.Time, error) {
	if _, err := Validate(days, at, timezone); err != nil {
		return time.Time{}, err
	}
	loc, _ := time.LoadLocation(timezone)
	var hour, minute int
	if _, err := fmt.Sscanf(at, "%d:%d", &hour, &minute); err != nil {
		return time.Time{}, fmt.Errorf("at: %w", err)
	}
	local := after.In(loc)
	for offset := 0; offset <= 7; offset++ {
		day := local.AddDate(0, 0, offset)
		candidate := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, loc)
		if candidate.After(after) && slices.Contains(days, weekdays[candidate.Weekday()]) {
			return candidate.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("no run within a week")
}
