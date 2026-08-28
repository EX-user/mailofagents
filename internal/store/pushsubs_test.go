package store

import "testing"

func TestPushSubsOwnershipAndCascade(t *testing.T) {
	s := newTokensStore(t)

	if err := s.UpsertPushSub(&PushSubscription{Address: "", Endpoint: "https://p/1"}); err == nil {
		t.Fatal("empty address must be rejected")
	}
	a := &PushSubscription{Address: "a@t", Endpoint: "https://p/dev1", P256dh: "k", Auth: "x", CreatedAt: 1}
	if err := s.UpsertPushSub(a); err != nil {
		t.Fatalf("upsert a: %v", err)
	}
	// Same endpoint, different account = hijack attempt.
	b := &PushSubscription{Address: "b@t", Endpoint: "https://p/dev1"}
	if err := s.UpsertPushSub(b); err == nil {
		t.Fatal("cross-account endpoint takeover must be rejected")
	}
	// Same account refreshing keys overwrites in place.
	a2 := *a
	a2.P256dh = "k2"
	if err := s.UpsertPushSub(&a2); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	got, err := s.PushSubsByAddress("a@t")
	if err != nil || len(got) != 1 || got[0].P256dh != "k2" {
		t.Fatalf("after refresh: %+v (%v)", got, err)
	}
	if err := s.UpsertPushSub(&PushSubscription{Address: "a@t", Endpoint: "https://p/dev9"}); err != nil {
		t.Fatalf("dev9: %v", err)
	}
	if n := len(mustSubs(t, s, "a@t")); n != 2 {
		t.Fatalf("want 2 devices, got %d", n)
	}
	if err := s.DeleteAllPushSubs("a@t"); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	if n := len(mustSubs(t, s, "a@t")); n != 0 {
		t.Fatalf("cascade left %d subs", n)
	}
}

func mustSubs(t *testing.T, s *Store, addr string) []*PushSubscription {
	t.Helper()
	subs, err := s.PushSubsByAddress(addr)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return subs
}
