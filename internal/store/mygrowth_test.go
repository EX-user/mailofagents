package store

import (
	"testing"
	"time"
)

// TestMyGrowthStats seeds inbox/sent via the real Send path at controlled
// times (via s.now override) and checks the buckets.
func TestMyGrowthStats(t *testing.T) {
	s, err := Open(t.TempDir() + "mygrowth.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })
	if _, err := s.CreateAccount("a", "t", false); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if _, err := s.CreateAccount("b", "t", false); err != nil {
		t.Fatalf("create b: %v", err)
	}

	now := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	weekFloor := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)

	// a sends 3 (all today): s1/s2/s3
	for _, subj := range []string{"s1", "s2", "s3"} {
		s.now = func() time.Time { return now.Add(-time.Hour) }
		if _, err := s.Send("a@t", "a", []string{"b@t"}, nil, subj, "x", ""); err != nil {
			t.Fatalf("send %s: %v", subj, err)
		}
	}
	// b sends 2 back (one today, one 3 days ago)
	s.now = func() time.Time { return now.Add(-2 * time.Hour) }
	if _, err := s.Send("b@t", "b", []string{"a@t"}, nil, "r-today", "x", ""); err != nil {
		t.Fatalf("send r-today: %v", err)
	}
	s.now = func() time.Time { return weekFloor.AddDate(0, 0, 3).Add(5 * time.Hour) } // Aug 12
	if _, err := s.Send("b@t", "b", []string{"a@t"}, nil, "r-old", "x", ""); err != nil {
		t.Fatalf("send r-old: %v", err)
	}

	g, err := s.MyGrowthStats("a@t", now)
	if err != nil {
		t.Fatalf("MyGrowthStats: %v", err)
	}
	if g.TodayOut != 3 || g.TodayIn != 1 {
		t.Errorf("today in/out = %d/%d, want 1/3", g.TodayIn, g.TodayOut)
	}
	if g.WeekIn != 2 || g.WeekOut != 3 {
		t.Errorf("week in/out = %d/%d, want 2/3", g.WeekIn, g.WeekOut)
	}
	if len(g.Days) != 7 {
		t.Fatalf("len(days) = %d, want 7", len(g.Days))
	}
	wantDates := []string{"2026-08-09", "2026-08-10", "2026-08-11", "2026-08-12", "2026-08-13", "2026-08-14", "2026-08-15"}
	for i, d := range g.Days {
		if d.Date != wantDates[i] {
			t.Errorf("days[%d].date = %q, want %q", i, d.Date, wantDates[i])
		}
	}
	// Aug 12: in=1; today (index 6): in=1, out=3; all else zero.
	if g.Days[3].In != 1 || g.Days[3].Out != 0 {
		t.Errorf("days[3] = %+v, want in=1 out=0", g.Days[3])
	}
	if g.Days[6].In != 1 || g.Days[6].Out != 3 {
		t.Errorf("days[6] = %+v, want in=1 out=3", g.Days[6])
	}
	for _, i := range []int{0, 1, 2, 4, 5} {
		if g.Days[i].In != 0 || g.Days[i].Out != 0 {
			t.Errorf("days[%d] should be zero, got %+v", i, g.Days[i])
		}
	}
}

func TestMyGrowthEmptyAccount(t *testing.T) {
	s, err := Open(t.TempDir() + "mygrowth-empty.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })
	if _, err := s.CreateAccount("solo", "t", false); err != nil {
		t.Fatalf("create: %v", err)
	}
	g, err := s.MyGrowthStats("solo@t", time.Now())
	if err != nil {
		t.Fatalf("MyGrowthStats: %v", err)
	}
	if g.TodayIn != 0 || g.TodayOut != 0 || g.WeekIn != 0 || g.WeekOut != 0 {
		t.Errorf("empty account growth = %+v, want zeros", g)
	}
	if len(g.Days) != 7 {
		t.Errorf("len(days) = %d, want 7 (zero-filled)", len(g.Days))
	}
}
