package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// newGrowthTestStore opens a throwaway store and seeds messages at the given
// unix timestamps (bypassing Send, which always stamps "now").
func newGrowthTestStore(t *testing.T, stamps []int64) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })
	if err := s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMessages)
		for i, ts := range stamps {
			m := Message{ID: newTestULID(i), From: "a@t", To: []string{"b@t"}, ReceivedAt: ts}
			val, err := json.Marshal(m)
			if err != nil {
				return err
			}
			if err := mb.Put([]byte(m.ID), val); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return s
}

func newTestULID(i int) string {
	// Any unique, ULID-shaped key works; the growth scan does not rely on
	// ordering, only on ReceivedAt.
	return time.Now().UTC().Format("20060102150405") + "-msg-" + string(rune('a'+i)) + "0000000000000000"
}

func TestMessageGrowthBuckets(t *testing.T) {
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	dayStart := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC).Unix()
	weekStart := now.Unix() - 7*24*3600
	monthStart := now.Unix() - 30*24*3600

	stamps := []int64{
		now.Unix() - 60,      // today (also week + month)
		dayStart,             // exactly midnight: today
		now.Unix() - 3600,    // earlier today (before "now-60" chronologically, still today)
		weekStart,            // exactly 7 days ago: week bucket edge
		weekStart - 1,        // just past a week: month only
		monthStart,           // exactly 30 days ago: month bucket edge
		monthStart - 86400,   // older than a month: total only
	}
	s := newGrowthTestStore(t, stamps)

	g, err := s.MessageGrowth(now)
	if err != nil {
		t.Fatalf("MessageGrowth: %v", err)
	}
	// today: msgs at now-60, midnight, now-3600 => 3
	// week:  those 3 + weekStart exactly (>= weekStart) => 4
	// month: those 4 + (weekStart-1) + monthStart exactly => 6
	// total: 7
	if g.Today != 3 {
		t.Errorf("Today = %d, want 3", g.Today)
	}
	if g.Week != 4 {
		t.Errorf("Week = %d, want 4", g.Week)
	}
	if g.Month != 6 {
		t.Errorf("Month = %d, want 6", g.Month)
	}
	if g.Total != 7 {
		t.Errorf("Total = %d, want 7", g.Total)
	}
}

func TestMessageGrowthEmpty(t *testing.T) {
	s := newGrowthTestStore(t, nil)
	g, err := s.MessageGrowth(time.Now())
	if err != nil {
		t.Fatalf("MessageGrowth: %v", err)
	}
	if g.Today != 0 || g.Week != 0 || g.Month != 0 || g.Total != 0 {
		t.Errorf("empty store counts = %+v, want zeros", g)
	}
}

// TestMessageGrowthDays verifies the 14-day chart array: ordering, dates,
// zero-fill, and that the last bucket equals the Today count.
func TestMessageGrowthDays(t *testing.T) {
	now := time.Date(2026, 8, 14, 15, 30, 0, 0, time.UTC)
	weekFloor := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)

	stamps := []int64{
		now.Unix() - 60,              // today
		now.Unix() - 3600 * 5,        // still today
		weekFloor.Unix() + 3600,      // Aug 8
		weekFloor.AddDate(0, 0, 3).Add(2 * time.Hour).Unix(), // Aug 11
		weekFloor.AddDate(0, 0, 3).Add(9 * time.Hour).Unix(), // Aug 11
		weekFloor.AddDate(0, 0, -1).Unix(),                   // Aug 7: outside the 7-day window, inside the 14-day chart
	}
	s := newGrowthTestStore(t, stamps)
	g, err := s.MessageGrowth(now)
	if err != nil {
		t.Fatalf("MessageGrowth: %v", err)
	}
	if len(g.Days) != GrowthChartDays {
		t.Fatalf("len(Days) = %d, want %d", len(g.Days), GrowthChartDays)
	}
	wantDates := []string{
		"2026-08-01", "2026-08-02", "2026-08-03", "2026-08-04", "2026-08-05", "2026-08-06", "2026-08-07",
		"2026-08-08", "2026-08-09", "2026-08-10", "2026-08-11", "2026-08-12", "2026-08-13", "2026-08-14",
	}
	for i, d := range g.Days {
		if d.Date != wantDates[i] {
			t.Errorf("Days[%d].Date = %q, want %q", i, d.Date, wantDates[i])
		}
	}
	wantCounts := []int{0, 0, 0, 0, 0, 0, 1, 1, 0, 0, 2, 0, 0, 2}
	for i, c := range wantCounts {
		if g.Days[i].Count != c {
			t.Errorf("Days[%d].Count = %d, want %d", i, g.Days[i].Count, c)
		}
	}
	last := len(g.Days) - 1
	if g.Days[last].Count != g.Today {
		t.Errorf("Days[%d].Count = %d, want == Today (%d)", last, g.Days[last].Count, g.Today)
	}
}

// TestMessageGrowthSkipsCorrupt verifies one bad record doesn't fail the scan.
func TestMessageGrowthSkipsCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })
	now := time.Now()
	if err := s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMessages)
		good, _ := json.Marshal(Message{ID: "G1", ReceivedAt: now.Unix()})
		if err := mb.Put([]byte("G1"), good); err != nil {
			return err
		}
		return mb.Put([]byte("BAD"), []byte("not json"))
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	g, err := s.MessageGrowth(now)
	if err != nil {
		t.Fatalf("MessageGrowth should not fail on corrupt record: %v", err)
	}
	if g.Total != 1 {
		t.Errorf("Total = %d, want 1 (corrupt skipped)", g.Total)
	}
}
