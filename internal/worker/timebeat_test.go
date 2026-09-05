package worker

import (
	"testing"
	"time"
)

func TestParseTimeBeat(t *testing.T) {
	eq := func(got []int, want ...int) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	cases := []struct {
		in   string
		want []int
		ok   bool
	}{
		{"", nil, true},                            // absent = off
		{"{8:00}", []int{480}, true},               // single enum slot
		{"{21:30,8:00,8}", []int{480, 1290}, true}, // enum, dedup + sort, bare hour
		{"{9:30}", []int{570}, true},
		{"[8:1:22]", []int{480, 540, 600, 660, 720, 780, 840, 900, 960, 1020, 1080, 1140, 1200, 1260, 1320}, true}, // boss's example: hourly 8..22
		{"[9:15m:10]", []int{540, 555, 570, 585, 600}, true},                                                       // 15-minute steps 9:00..10:00
		{"[8:2:14]", []int{480, 600, 720, 840}, true},
		{"[8:2:9]", []int{480}, true},                        // 2h step from 8:00 never reaches 9:00: start only
		{"[8.5:2:12]", []int{510, 630}, true},                // decimal hours: 8:30, 10:30 (boss ask)
		{"{8.5}", []int{510}, true},                          // decimal hour in enum = 8:30
		{"[8:0.5:10]", []int{480, 510, 540, 570, 600}, true}, // 0.5h step = 30 minutes    // two-hour steps
		{"[22:1:22]", []int{1320}, true},                     // start==end: single slot
		{"chat", nil, false},                                 // no braces
		{"{8:00", nil, false},                                // unbalanced
		{"{}", nil, false},                                   // empty enum
		{"{25:00}", nil, false},                              // out of range
		{"[8:1:7]", nil, false},                              // end before start
		{"[8:0:22]", nil, false},                             // zero step
		{"[8:1:2:22]", nil, false},                           // four fields
		{"[9:0m:10]", nil, false},                            // zero step
		{"[9:xm:10]", nil, false},                            // bad step number
	}
	for _, c := range cases {
		got, err := ParseTimeBeat(c.in)
		if c.ok && err != nil {
			t.Errorf("ParseTimeBeat(%q) unexpected error: %v", c.in, err)
			continue
		}
		if !c.ok {
			if err == nil {
				t.Errorf("ParseTimeBeat(%q) = %v, want error", c.in, got)
			}
			continue
		}
		if !eq(got, c.want...) {
			t.Errorf("ParseTimeBeat(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestBeatSlotCrossed(t *testing.T) {
	slots := []int{480, 540, 1290} // 8:00, 9:00, 21:30
	day := func(h, m int) time.Time {
		return time.Date(2026, 9, 5, h, m, 0, 0, time.Local)
	}
	if !beatSlotCrossed(slots, day(7, 59), day(8, 0)) {
		t.Error("slot 8:00 must fire at its own minute")
	}
	if beatSlotCrossed(slots, day(8, 0), day(8, 30)) {
		t.Error("9:00 must not fire inside (8:00, 8:30]")
	}
	if !beatSlotCrossed(slots, day(8, 30), day(9, 0)) {
		t.Error("slot 9:00 missed")
	}
	if beatSlotCrossed(slots, day(22, 0), day(23, 0)) {
		t.Error("no slots late evening")
	}
	if beatSlotCrossed(slots, day(23, 0), day(23, 0)) {
		t.Error("empty interval must not fire")
	}
	// midnight wrap: from 23:50 to 00:10 — no slot in this example set
	if beatSlotCrossed(slots, day(23, 50), day(0, 10).AddDate(0, 0, 1)) {
		t.Error("wrap should only match slots in (23:50,24:00] or [0:00,0:10]")
	}
	wrap := []int{1435, 5} // 23:55 and 00:05
	if !beatSlotCrossed(wrap, day(23, 50), day(0, 10).AddDate(0, 0, 1)) {
		t.Error("wrap slots 23:55/00:05 missed")
	}
	// sub-minute to-boundary: to at 8:00:30 still covers the 8:00 slot
	if !beatSlotCrossed(slots, day(7, 0), day(8, 0).Add(30*time.Second)) {
		t.Error("minute-truncated bound must include the 8:00 slot")
	}
}
