package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// seedUnread plants an unread flag for (uuid, msgID) directly.
func seedUnread(t *testing.T, s *Store, uuid, msgID string) {
	t.Helper()
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bUnread).Put(indexKey(uuid, msgID), []byte{1})
	}); err != nil {
		t.Fatalf("seed unread: %v", err)
	}
}

func rawMsg(t *testing.T, s *Store, id string, at int64) {
	t.Helper()
	m := Message{ID: id, From: "x@t", To: []string{"sub1@t"}, Body: "b", ReceivedAt: at}
	val, _ := json.Marshal(m)
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bMessages).Put([]byte(id), val)
	}); err != nil {
		t.Fatalf("raw msg: %v", err)
	}
}

// TestLastReadAtStamp pins the liveness weak-evidence stamp: only the
// unread->read transition updates it (repeat reads don't), MarkAllRead
// bumps it, and mgmt subs[] carries it.
func TestLastReadAtStamp(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })
	for _, n := range []string{"me", "sub1"} {
		if _, err := s.CreateAccount(n, "t", false); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
	}
	if err := s.DeclareSubordinate("me@t", "sub1@t"); err != nil {
		t.Fatalf("declare: %v", err)
	}
	sub, _ := s.GetAccount("sub1@t")

	t0 := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return t0 }
	rawMsg(t, s, "m1", t0.Unix())
	seedUnread(t, s, sub.UUID, "m1")

	if got := s.LastReadAt(sub.UUID); got != 0 {
		t.Fatalf("before any read = %d, want 0", got)
	}
	// First read transitions -> stamped at t0.
	if err := s.MarkRead(sub.UUID, "m1"); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if got := s.LastReadAt(sub.UUID); got != t0.Unix() {
		t.Fatalf("after first read = %d, want %d", got, t0.Unix())
	}
	// Repeat read of the same message: no bump.
	t1 := t0.Add(2 * time.Hour)
	s.now = func() time.Time { return t1 }
	_ = s.MarkRead(sub.UUID, "m1")
	if got := s.LastReadAt(sub.UUID); got != t0.Unix() {
		t.Fatalf("repeat read bumped stamp to %d, want unchanged %d", got, t0.Unix())
	}
	// A second message read later bumps to the later time.
	rawMsg(t, s, "m2", t1.Unix())
	seedUnread(t, s, sub.UUID, "m2")
	_ = s.MarkRead(sub.UUID, "m2")
	if got := s.LastReadAt(sub.UUID); got != t1.Unix() {
		t.Fatalf("after second read = %d, want %d", got, t1.Unix())
	}
	// MarkAllRead also stamps (it transitions flags in bulk).
	t2 := t1.Add(time.Hour)
	s.now = func() time.Time { return t2 }
	rawMsg(t, s, "m3", t2.Unix())
	seedUnread(t, s, sub.UUID, "m3")
	if _, err := s.MarkAllRead("sub1@t"); err != nil {
		t.Fatalf("mark all: %v", err)
	}
	if got := s.LastReadAt(sub.UUID); got != t2.Unix() {
		t.Fatalf("after mark-all = %d, want %d", got, t2.Unix())
	}
	// mgmt overview carries it on the sub row.
	s.now = func() time.Time { return t2 }
	out, err := s.MgmtSubsOverview("me@t")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if len(out.Subs) != 1 || out.Subs[0].LastReadAt != t2.Unix() {
		t.Fatalf("mgmt last_read_at = %+v, want %d", out.Subs, t2.Unix())
	}
	// A never-reader sub reports 0.
	if _, err := s.CreateAccount("sub2", "t", false); err != nil {
		t.Fatal(err)
	}
	if err := s.DeclareSubordinate("me@t", "sub2@t"); err != nil {
		t.Fatal(err)
	}
	out, _ = s.MgmtSubsOverview("me@t")
	for _, r := range out.Subs {
		if r.Address == "sub2@t" && r.LastReadAt != 0 {
			t.Errorf("sub2 last_read_at = %d, want 0", r.LastReadAt)
		}
	}
}
