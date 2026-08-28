package store

import (
	"errors"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func newShowcaseStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + string(rune('a'+len(t.Name())%26)) + "-showcase.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })
	return s
}

func TestTeeShowcaseIndependentID(t *testing.T) {
	s := newShowcaseStore(t)
	// Send requires real accounts; the tee itself does not.
	if _, err := s.CreateAccount("a", "t", false); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if _, err := s.CreateAccount("b", "t", false); err != nil {
		t.Fatalf("create b: %v", err)
	}
	res, err := s.Send("a@t", "a", []string{"b@t"}, nil, "subj", "body", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := s.TeeShowcase("a@t", []string{"b@t"}, "subj", "body"); err != nil {
		t.Fatalf("tee: %v", err)
	}
	entries, err := s.ListShowcase()
	if err != nil || len(entries) != 1 {
		t.Fatalf("list: %v entries=%d", err, len(entries))
	}
	e := entries[0]
	if e.ID == res.MessageID {
		t.Error("showcase ID must be independent of the real message ID")
	}
	if e.From != "a@t" || e.Subject != "subj" || e.Body != "body" {
		t.Errorf("entry content wrong: %+v", e)
	}
	if e.ReceivedAt == 0 {
		t.Error("ReceivedAt should be set")
	}
}

func TestTeeShowcaseCapEviction(t *testing.T) {
	if ShowcaseCap != 1000 {
		t.Fatalf("test assumes cap 1000, got %d", ShowcaseCap)
	}
	s := newShowcaseStore(t)
	// Insert cap+50 entries in monotonically increasing time so ULID order
	// matches insert order; the 50 oldest must be evicted.
	base := time.Now().Add(-5 * time.Hour)
	for i := 0; i < ShowcaseCap+50; i++ {
		s.now = func() time.Time { return base.Add(time.Duration(i) * time.Second) }
		if err := s.TeeShowcase("a@t", nil, "s", "b"); err != nil {
			t.Fatalf("tee %d: %v", i, err)
		}
	}
	n, err := s.CountShowcase()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != ShowcaseCap {
		t.Errorf("count after eviction = %d, want %d", n, ShowcaseCap)
	}
	entries, err := s.ListShowcase()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Newest first; the first inserted (subject "s" all identical) — verify
	// by timestamp: the oldest surviving entry must be insert #50.
	if entries[len(entries)-1].ReceivedAt != base.Add(50*time.Second).Unix() {
		t.Errorf("oldest surviving entry is not insert #50: ts=%d want=%d",
			entries[len(entries)-1].ReceivedAt, base.Add(50*time.Second).Unix())
	}
}

func TestListShowcaseNewestFirst(t *testing.T) {
	s := newShowcaseStore(t)
	base := time.Now().Add(-2 * time.Hour)
	for i, subj := range []string{"old", "mid", "new"} {
		s.now = func() time.Time { return base.Add(time.Duration(i) * time.Minute) }
		if err := s.TeeShowcase("a@t", nil, subj, "b"); err != nil {
			t.Fatalf("tee: %v", err)
		}
	}
	entries, _ := s.ListShowcase()
	if len(entries) != 3 || entries[0].Subject != "new" || entries[2].Subject != "old" {
		t.Errorf("order wrong: %v", []string{entries[0].Subject, entries[1].Subject, entries[2].Subject})
	}
}

// TestClearShowcase empties the bucket without touching real mail.
func TestClearShowcase(t *testing.T) {
	s := newShowcaseStore(t)
	if _, err := s.CreateAccount("a", "t", false); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.CreateAccount("b", "t", false); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Send("a@t", "a", []string{"b@t"}, nil, "real", "mail", ""); err != nil {
		t.Fatalf("send: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := s.TeeShowcase("a@t", nil, "pub", "b"); err != nil {
			t.Fatalf("tee: %v", err)
		}
	}
	if n, _ := s.CountShowcase(); n != 3 {
		t.Fatalf("pre-clear count = %d, want 3", n)
	}
	if err := s.ClearShowcase(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if n, _ := s.CountShowcase(); n != 0 {
		t.Errorf("post-clear count = %d, want 0", n)
	}
	// Real mail untouched.
	msgs, err := s.ReadInbox("b@t", 10)
	if err != nil || len(msgs) != 1 || msgs[0].Subject != "real" {
		t.Errorf("real mail affected: %v %+v", err, msgs)
	}
	// Bucket still usable after clear.
	if err := s.TeeShowcase("a@t", nil, "post", "b"); err != nil {
		t.Fatalf("tee after clear: %v", err)
	}
	if n, _ := s.CountShowcase(); n != 1 {
		t.Errorf("post-clear tee count = %d, want 1", n)
	}
}

// TestDeleteShowcaseEntry covers exact delete + not-found distinction.
func TestDeleteShowcaseEntry(t *testing.T) {
	s := newShowcaseStore(t)
	if err := s.TeeShowcase("a@t", nil, "one", "b"); err != nil {
		t.Fatalf("tee1: %v", err)
	}
	if err := s.TeeShowcase("a@t", nil, "two", "b"); err != nil {
		t.Fatalf("tee2: %v", err)
	}
	entries, _ := s.ListShowcase()
	if len(entries) != 2 {
		t.Fatalf("pre-delete count = %d, want 2", len(entries))
	}
	if err := s.DeleteShowcaseEntry(entries[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// ListShowcase is newest-first, so entries[0] was "two": "one" remains.
	after, _ := s.ListShowcase()
	if len(after) != 1 || after[0].Subject != "one" {
		t.Errorf("post-delete = %+v, want only 'one'", after)
	}
	if err := s.DeleteShowcaseEntry(entries[0].ID); err == nil || !errors.Is(err, ErrShowcaseNotFound) {
		t.Errorf("second delete should be ErrShowcaseNotFound, got %v", err)
	}
}

// TestGetShowcaseEntry covers exact fetch + not-found.
func TestGetShowcaseEntry(t *testing.T) {
	s := newShowcaseStore(t)
	if err := s.TeeShowcase("a@t", []string{"b@t"}, "findme", "body-text"); err != nil {
		t.Fatalf("tee: %v", err)
	}
	entries, _ := s.ListShowcase()
	e, err := s.GetShowcaseEntry(entries[0].ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if e.Subject != "findme" || e.From != "a@t" {
		t.Errorf("entry = %+v", e)
	}
	if _, err := s.GetShowcaseEntry("NOPE"); err == nil || !errors.Is(err, ErrShowcaseNotFound) {
		t.Errorf("missing id should be ErrShowcaseNotFound, got %v", err)
	}
}

// TestListShowcaseSkipsCorrupt mirrors the growth-scan tolerance.
func TestListShowcaseSkipsCorrupt(t *testing.T) {
	s := newShowcaseStore(t)
	if err := s.TeeShowcase("a@t", nil, "good", "b"); err != nil {
		t.Fatalf("tee: %v", err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bShowcase).Put([]byte("BAD"), []byte("not json"))
	}); err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}
	entries, err := s.ListShowcase()
	if err != nil {
		t.Fatalf("list should not fail on corrupt record: %v", err)
	}
	if len(entries) != 1 || entries[0].Subject != "good" {
		t.Errorf("entries = %+v, want only the good one", entries)
	}
}
