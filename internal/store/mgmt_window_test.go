package store

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

// 0.3.3 C feature: the account-page list row 3 shows the account's latest
// message — subject (100-rune truncated) and its received_at over ALL TIME.
// The newest letter wins regardless of direction (in or out).
func TestMgmtOverviewLatestSubject(t *testing.T) {
	s := newFilesStore(t)
	if err := s.DeclareSubordinate("a@t", "b@t"); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if _, err := s.Send("a@t", "a", []string{"b@t"}, nil, "Direct order to sub", "x", ""); err != nil {
		t.Fatalf("send 1: %v", err)
	}
	s.now = func() time.Time { return time.Now().Add(1 * time.Second) }
	if _, err := s.Send("b@t", "b", []string{"a@t"}, nil, "Sub replies: roger", "x", ""); err != nil {
		t.Fatalf("send 2: %v", err)
	}
	s.now = func() time.Time { return time.Now().Add(2 * time.Second) }
	if _, err := s.Send("a@t", "a", []string{"b@t"}, nil, "Follow-up after the reply", "x", ""); err != nil {
		t.Fatalf("send 3: %v", err)
	}
	s.now = func() time.Time { return time.Now().Add(3 * time.Second) }
	out, err := s.MgmtSubsOverviewWindow("a@t", 0)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	for _, sub := range out.Subs {
		if sub.Address != "b@t" {
			continue
		}
		if sub.LatestAt == 0 || sub.LatestSubject != "Follow-up after the reply" {
			t.Fatalf("latest: got %q at %d, want the newest letter's subject", sub.LatestSubject, sub.LatestAt)
		}
		// Long subjects truncate to 100 runes, rune-safe (no mid-char split).
		long := strings.Repeat("汉", 120)
		s.now = func() time.Time { return time.Now().Add(4 * time.Second) }
		if _, err := s.Send("a@t", "a", []string{"b@t"}, nil, long, "x", ""); err != nil {
			t.Fatalf("send long: %v", err)
		}
		out2, err := s.MgmtSubsOverviewWindow("a@t", 0)
		if err != nil {
			t.Fatalf("overview 2: %v", err)
		}
		for _, sub2 := range out2.Subs {
			if sub2.Address == "b@t" && utf8.RuneCountInString(sub2.LatestSubject) != 100 {
				t.Fatalf("truncation: got %d runes, want 100", utf8.RuneCountInString(sub2.LatestSubject))
			}
		}
		return
	}
	t.Fatal("sub row b@t missing")
}
