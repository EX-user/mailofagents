package store

import (
	"fmt"
	"testing"
)

func newSubsStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "-subs.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })
	return s
}

func seedSubsAccounts(t *testing.T, s *Store) {
	t.Helper()
	for _, n := range []string{"a", "b", "c"} {
		if _, err := s.CreateAccount(n, "t", false); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
	}
}

func TestSubsDeclareRevokeLifecycle(t *testing.T) {
	s := newSubsStore(t)
	seedSubsAccounts(t, s)

	// A declares under B.
	if err := s.DeclareSubordinate("b@t", "a@t"); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if !s.IsSubordinate("b@t", "a@t") {
		t.Fatal("a should be subordinate of b after declare")
	}
	// Case-insensitive lookup.
	if !s.IsSubordinate("B@T", "A@T") {
		t.Fatal("lookup should be case-insensitive")
	}
	// Listings.
	if got := s.SubordinatesOf("b@t"); len(got) != 1 || got[0].Address != "a@t" || got[0].Scope != "both" {
		t.Fatalf("SubordinatesOf(b) = %+v", got)
	}
	if got := s.SuperiorsOf("a@t"); len(got) != 1 || got[0].Address != "b@t" {
		t.Fatalf("SuperiorsOf(a) = %+v", got)
	}
	if got := s.SubordinatesOf("c@t"); len(got) != 0 {
		t.Fatalf("c should have no subordinates, got %+v", got)
	}

	// Revoke: takes effect immediately on the next lookup.
	if err := s.RevokeSubordinate("b@t", "a@t"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if s.IsSubordinate("b@t", "a@t") {
		t.Fatal("a should no longer be subordinate of b after revoke")
	}
	if got := s.SubordinatesOf("b@t"); len(got) != 0 {
		t.Fatalf("b should have no subordinates after revoke, got %+v", got)
	}
	// Idempotent revoke.
	if err := s.RevokeSubordinate("b@t", "a@t"); err != nil {
		t.Fatalf("idempotent revoke: %v", err)
	}
}

func TestSubsDeclareValidation(t *testing.T) {
	s := newSubsStore(t)
	seedSubsAccounts(t, s)

	if err := s.DeclareSubordinate("a@t", "a@t"); err == nil {
		t.Fatal("self-declare should be rejected")
	}
	if err := s.DeclareSubordinate("nope@t", "a@t"); err == nil {
		t.Fatal("unknown superior should be rejected")
	}
	if err := s.DeclareSubordinate("b@t", "nope@t"); err == nil {
		t.Fatal("unknown subordinate should be rejected")
	}
}

func TestSubsReadMessages(t *testing.T) {
	s := newSubsStore(t)
	seedSubsAccounts(t, s)
	if err := s.DeclareSubordinate("b@t", "a@t"); err != nil {
		t.Fatalf("declare: %v", err)
	}
	// A receives mail and sends mail.
	if _, err := s.Send("b@t", "b", []string{"a@t"}, nil, "to-a", "x", ""); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := s.Send("a@t", "a", []string{"c@t"}, nil, "from-a", "x", ""); err != nil {
		t.Fatalf("send: %v", err)
	}

	both, err := s.ReadSubordinateMessages("a@t", "both", 10)
	if err != nil {
		t.Fatalf("read both: %v", err)
	}
	if len(both) != 2 {
		t.Fatalf("both should see 2, got %d", len(both))
	}
	// Newest first: the later message (from-a) leads.
	if both[0].Subject != "from-a" {
		t.Fatalf("newest-first violated: %+v", both)
	}

	inbox, err := s.ReadSubordinateMessages("a@t", "inbox", 10)
	if err != nil || len(inbox) != 1 || inbox[0].Subject != "to-a" {
		t.Fatalf("inbox filter: %v %+v", err, inbox)
	}
	sent, err := s.ReadSubordinateMessages("a@t", "sent", 10)
	if err != nil || len(sent) != 1 || sent[0].Subject != "from-a" {
		t.Fatalf("sent filter: %v %+v", err, sent)
	}

	// After revoke the caller gates access; the read itself still works for
	// anyone the handler authorizes (masquerade is the handler's job).
	if err := s.RevokeSubordinate("b@t", "a@t"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if s.IsSubordinate("b@t", "a@t") {
		t.Fatal("revoke should be immediate")
	}
}

func TestSubsShouldAuditSampled(t *testing.T) {
	s := newSubsStore(t)
	if !s.ShouldAuditSubRead("b@t", "a@t") {
		t.Fatal("first read in an hour should be audited")
	}
	if s.ShouldAuditSubRead("b@t", "a@t") {
		t.Fatal("second read in the same hour should not be audited")
	}
	if !s.ShouldAuditSubRead("c@t", "a@t") {
		t.Fatal("a different pair audits independently")
	}
}

func TestUnreadCountDecrementsAfterRead(t *testing.T) {
	s := newSubsStore(t)
	seedSubsAccounts(t, s)
	for i := 0; i < 3; i++ {
		if _, err := s.Send("b@t", "b", []string{"a@t"}, nil, fmt.Sprintf("m%d", i), "x", ""); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	n, err := s.CountUnread("a@t")
	if err != nil || n != 3 {
		t.Fatalf("after sends: count=%d err=%v (want 3)", n, err)
	}
	msgs, _ := s.ReadInbox("a@t", 10)
	if len(msgs) != 3 {
		t.Fatalf("inbox=%d", len(msgs))
	}
	if _, err := s.GetMessage("a@t", msgs[0].ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	n, _ = s.CountUnread("a@t")
	if n != 2 {
		t.Fatalf("after read: count=%d (want 2)", n)
	}
	msgs2, _ := s.ReadInbox("a@t", 10)
	if msgs2[0].Unread {
		t.Fatalf("summary says unread but count=%d", n)
	}
}
