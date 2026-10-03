package store

import (
	"strings"
	"testing"
)

func newWhitelistStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "-wl.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })
	return s
}

func mkAcc(t *testing.T, s *Store, addr string) {
	t.Helper()
	name, domain, _ := strings.Cut(addr, "@")
	if _, err := s.CreateAccount(name, domain, false); err != nil {
		t.Fatalf("create %s: %v", addr, err)
	}
}

// Leg 1: gate off = zero change, path never consults the list (spec 4).
func TestWhitelistOffIsNoop(t *testing.T) {
	s := newWhitelistStore(t)
	mkAcc(t, s, "alice@test.example")
	mkAcc(t, s, "mallory@test.example")
	if err := s.WhitelistAdd("alice@test.example", "friend@test.example"); err != nil {
		t.Fatal(err)
	}
	if !s.WhitelistAdmits("alice@test.example", "mallory@test.example") {
		t.Fatal("gate off must admit everyone")
	}
}

// Leg 2: gate on + sender on the list = admitted.
func TestWhitelistHitAdmits(t *testing.T) {
	s := newWhitelistStore(t)
	mkAcc(t, s, "alice@test.example")
	mkAcc(t, s, "friend@test.example")
	if err := s.SetWhitelistEnabled("alice@test.example", true); err != nil {
		t.Fatal(err)
	}
	if err := s.WhitelistAdd("alice@test.example", "friend@test.example"); err != nil {
		t.Fatal(err)
	}
	if !s.WhitelistAdmits("alice@test.example", "friend@test.example") {
		t.Fatal("listed sender must be admitted")
	}
}

// Leg 3: gate on + sender absent = rejected (nothing stored upstream).
func TestWhitelistMissRejects(t *testing.T) {
	s := newWhitelistStore(t)
	mkAcc(t, s, "alice@test.example")
	mkAcc(t, s, "mallory@test.example")
	_ = s.SetWhitelistEnabled("alice@test.example", true)
	if s.WhitelistAdmits("alice@test.example", "mallory@test.example") {
		t.Fatal("unlisted sender must be rejected")
	}
}

// Leg 4: hierarchy exemption is bidirectional and outranks the list
// (spec 6): sub->super and super->sub both pass even when the gate is on
// and the other side is unlisted.
func TestWhitelistHierarchyExemption(t *testing.T) {
	s := newWhitelistStore(t)
	if _, _, err := s.RegisterTeam("owner", "test.example", "password123", 1, []string{"bot1"}); err != nil {
		t.Fatal(err)
	}
	_ = s.SetWhitelistEnabled("owner@test.example", true)
	_ = s.SetWhitelistEnabled("bot1@test.example", true)
	if !s.WhitelistAdmits("owner@test.example", "bot1@test.example") {
		t.Fatal("subordinate -> gated owner must pass (exemption)")
	}
	if !s.WhitelistAdmits("bot1@test.example", "owner@test.example") {
		t.Fatal("owner -> gated subordinate must pass (exemption)")
	}
}

// Leg 5: management surface — set/add/remove/prefill-while-off/negative
// default + normalize (dedupe, lowercase).
func TestWhitelistManagement(t *testing.T) {
	s := newWhitelistStore(t)
	mkAcc(t, s, "alice@test.example")
	// prefill while off (spec 1)
	if err := s.WhitelistSet("alice@test.example", []string{"A@X.example", "b@x.example", "a@x.example", ""}); err != nil {
		t.Fatal(err)
	}
	acc, _ := s.GetAccount("alice@test.example")
	if acc.WhitelistEnabled {
		t.Fatal("prefill must not enable the gate")
	}
	if len(acc.Whitelist) != 2 || acc.Whitelist[0] != "a@x.example" || acc.Whitelist[1] != "b@x.example" {
		t.Fatalf("normalize (lowercase+dedupe) broken: %v", acc.Whitelist)
	}
	// toggle on
	if err := s.SetWhitelistEnabled("alice@test.example", true); err != nil {
		t.Fatal(err)
	}
	acc, _ = s.GetAccount("alice@test.example")
	if !acc.WhitelistEnabled || len(acc.Whitelist) != 2 {
		t.Fatalf("toggle/list broken: %+v", acc)
	}
	// add + remove
	_ = s.WhitelistAdd("alice@test.example", "c@x.example")
	_ = s.WhitelistAdd("alice@test.example", "c@x.example") // idempotent
	acc, _ = s.GetAccount("alice@test.example")
	if len(acc.Whitelist) != 3 {
		t.Fatalf("add/idempotence broken: %v", acc.Whitelist)
	}
	_ = s.WhitelistRemove("alice@test.example", "b@x.example")
	acc, _ = s.GetAccount("alice@test.example")
	if len(acc.Whitelist) != 2 {
		t.Fatalf("remove broken: %v", acc.Whitelist)
	}
	// unknown recipient defaults open (no gate possible)
	if !s.WhitelistAdmits("ghost@test.example", "anyone@test.example") {
		t.Fatal("unknown recipient must default open")
	}
}
