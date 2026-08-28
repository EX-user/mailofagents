package store

import (
	"testing"
	"time"
)

// TestMgmtSubsOverviewWindow (v0.6.21 range button): a letter older than
// the 7d window must stay out of days=7 counts but count under days=0
// (all time). 30d must include 10-day-old traffic.
func TestMgmtSubsOverviewWindow(t *testing.T) {
	s := newFilesStore(t)
	// The overview graph only renders me + declared subordinates.
	if err := s.DeclareSubordinate("a@t", "b@t"); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if _, err := s.Send("a@t", "a", []string{"b@t"}, nil, "fresh", "x", ""); err != nil {
		t.Fatalf("send fresh: %v", err)
	}
	// Backdate the clock 10 days, send the "old" letter, restore.
	s.now = func() time.Time { return time.Now().Add(-10 * 24 * time.Hour) }
	if _, err := s.Send("a@t", "a", []string{"b@t"}, nil, "old", "x", ""); err != nil {
		t.Fatalf("send old: %v", err)
	}
	s.now = func() time.Time { return time.Now() }

	total := func(days int) int {
		out, err := s.MgmtSubsOverviewWindow("a@t", days)
		if err != nil {
			t.Fatalf("overview(%d): %v", days, err)
		}
		n := 0
		for _, e := range out.Graph.Edges {
			n += e.AToB + e.BToA
		}
		return n
	}
	if got := total(7); got != 1 {
		t.Errorf("7d window counted %d edges, want 1 (old letter excluded)", got)
	}
	if got := total(30); got != 2 {
		t.Errorf("30d window counted %d edges, want 2", got)
	}
	if got := total(0); got != 2 {
		t.Errorf("all-time counted %d edges, want 2", got)
	}
	// Window echo must round-trip.
	out, _ := s.MgmtSubsOverviewWindow("a@t", 0)
	if out.WindowDays != 0 {
		t.Errorf("WindowDays = %d, want 0", out.WindowDays)
	}
}
