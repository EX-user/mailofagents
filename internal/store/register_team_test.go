package store

import (
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// TestRegisterTeamV2NamedMembers pins the v2 contract: caller-chosen
// member names are used, collisions are serially de-duplicated, and the
// response carries the ACTUAL (post-dedup) addresses.
func TestRegisterTeamV2NamedMembers(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })

	// Pre-create an account that collides with the second requested member
	// name, so the store must dedup it to alpha-2.
	if _, err := s.CreateAccountWithPassword("alpha", "test.example", false, "password123"); err != nil {
		t.Fatalf("seed alpha: %v", err)
	}

	owner, members, err := s.RegisterTeam("captain", "test.example", "password123", 3, []string{"beta", "alpha", "gamma"})
	if err != nil {
		t.Fatalf("RegisterTeam v2: %v", err)
	}
	if owner.Address != "captain@test.example" {
		t.Errorf("owner = %q, want captain@test.example", owner.Address)
	}
	if len(*members) != 3 {
		t.Fatalf("len(members) = %d, want 3", len(*members))
	}
	got := []string{(*members)[0].Address, (*members)[1].Address, (*members)[2].Address}
	want := []string{"beta@test.example", "alpha-2@test.example", "gamma@test.example"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("members[%d] = %q, want %q", i, got[i], want[i])
		}
		if (*members)[i].Password == "" {
			t.Errorf("members[%d] password empty", i)
		}
	}
	// Each returned address must actually exist and be a declared subordinate.
	for _, m := range *members {
		if _, err := s.GetAccount(m.Address); err != nil {
			t.Errorf("member %q not found after register: %v", m.Address, err)
		}
		if !s.IsSubordinate("captain@test.example", m.Address) {
			t.Errorf("member %q not declared as subordinate", m.Address)
		}
	}
}

// TestRegisterTeamV1Fallback keeps the v1 path working: an empty member
// list falls back to bot-<8random> names.
func TestRegisterTeamV1Fallback(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })

	_, members, err := s.RegisterTeam("lead", "test.example", "password123", 2, nil)
	if err != nil {
		t.Fatalf("RegisterTeam v1: %v", err)
	}
	if len(*members) != 2 {
		t.Fatalf("len(members) = %d, want 2", len(*members))
	}
	for _, m := range *members {
		if !strings.HasPrefix(m.Address, "bot-") {
			t.Errorf("v1 member = %q, want bot- prefix", m.Address)
		}
	}
}

// TestRegisterTeamMemberCountMismatch guards the store-level contract: a
// member list whose length disagrees with team_size is rejected before
// any account is created.
func TestRegisterTeamMemberCountMismatch(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })

	if _, _, err := s.RegisterTeam("x", "test.example", "password123", 3, []string{"only-one"}); err == nil {
		t.Fatal("mismatched members accepted; want error")
	}
	// Nothing was created.
	if exists, _ := bucketHas(s, "x@test.example"); exists {
		t.Error("owner created despite rejected call")
	}
}

func bucketHas(s *Store, key string) (bool, error) {
	var found bool
	err := s.db.View(func(tx *bolt.Tx) error {
		found = tx.Bucket(bAccounts).Get([]byte(key)) != nil
		return nil
	})
	return found, err
}
