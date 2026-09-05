package worker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// time_beat — clock-scheduled beat interrupts (boss spec 2026-09-05).
//
// Schedule forms:
//
//	{8:00,9:30,21:00}   enumeration of HH:MM slots (bare "8" = 8:00)
//	[8:1:22]            step range: 8:00..22:00 every 1h (bare step = hours)
//	[9:15m:18]          step with unit: 9:00..18:00 every 15 minutes
//
// Slots are minutes-of-day, sorted. Absent/empty schedule = feature off.
// The minimum interval between beat INTERRUPTS is a worker-internal
// constant (user-unconfigurable by design — its only job is to keep the
// beat from interrupting the model too often):
const beatMinInterval = 5 * time.Minute

// ParseTimeBeat parses a schedule string into sorted minutes-of-day.
func ParseTimeBeat(schedule string) ([]int, error) {
	s := strings.TrimSpace(schedule)
	if s == "" {
		return nil, nil
	}
	switch {
	case strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}"):
		var out []int
		seen := map[int]bool{}
		for _, tok := range strings.Split(strings.Trim(s, "{}"), ",") {
			m, err := parseHourMin(strings.TrimSpace(tok))
			if err != nil {
				return nil, fmt.Errorf("enum slot %q: %w", tok, err)
			}
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("empty enumeration")
		}
		sort.Ints(out)
		return out, nil
	case strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]"):
		parts := strings.Split(strings.Trim(s, "[]"), ":")
		if len(parts) != 3 {
			return nil, fmt.Errorf("step range wants [start:step:end], got %q", s)
		}
		start, err := parseHourMin(parts[0])
		if err != nil {
			return nil, fmt.Errorf("range start %q: %w", parts[0], err)
		}
		end, err := parseHourMin(parts[2])
		if err != nil {
			return nil, fmt.Errorf("range end %q: %w", parts[2], err)
		}
		stepTok := strings.TrimSpace(parts[1])
		stepMin := 0
		if strings.HasSuffix(stepTok, "m") {
			stepMin, err = strconv.Atoi(strings.TrimSuffix(stepTok, "m"))
			if err != nil || stepMin <= 0 {
				return nil, fmt.Errorf("range step %q: want a positive minute count", stepTok)
			}
		} else {
			h, err := strconv.Atoi(stepTok)
			if err != nil || h <= 0 {
				return nil, fmt.Errorf("range step %q: want hours or minutes (e.g. 1 or 15m)", stepTok)
			}
			stepMin = h * 60
		}
		if end < start {
			return nil, fmt.Errorf("range end %02d:%02d before start %02d:%02d", end/60, end%60, start/60, start%60)
		}
		var out []int
		for m := start; m <= end; m += stepMin {
			out = append(out, m)
			if len(out) > 1440 { // pathological step guard; a day holds 1440 minutes
				return nil, fmt.Errorf("step range yields more than a day of slots")
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("want {8:00,9:30} or [8:1:22], got %q", s)
	}
}

// parseHourMin parses "8" (=8:00) or "HH:MM" into minutes-of-day.
func parseHourMin(tok string) (int, error) {
	hh, mm := 0, 0
	if strings.Contains(tok, ":") {
		parts := strings.Split(tok, ":")
		if len(parts) != 2 {
			return 0, fmt.Errorf("want HH:MM")
		}
		h, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, err
		}
		m, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, err
		}
		hh, mm = h, m
	} else {
		h, err := strconv.Atoi(tok)
		if err != nil {
			return 0, fmt.Errorf("want HH:MM or a bare hour")
		}
		hh = h
	}
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("time %02d:%02d out of range", hh, mm)
	}
	return hh*60 + mm, nil
}

// beatSlotCrossed reports whether any scheduled slot lies in the minute
// interval (from, to] — inclusive at `to` (the slot fires AT its minute).
// Both bounds are truncated to minute resolution; a midnight wrap in
// (from, to] checks the two halves.
func beatSlotCrossed(slots []int, from, to time.Time) bool {
	if len(slots) == 0 || !to.After(from) {
		return false
	}
	f := from.Hour()*60 + from.Minute()
	t := to.Hour()*60 + to.Minute()
	hit := func(lo, hi int) bool {
		for _, m := range slots {
			if m > lo && m <= hi {
				return true
			}
		}
		return false
	}
	if f < t {
		return hit(f, t)
	}
	if f == t {
		return false
	}
	return hit(f, 1440) || hit(0, t) // midnight wrap
}
