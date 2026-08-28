package store

import (
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// Regression for the superior's "auto-registration created mixed-case
// accounts" report (alice 01M13YN3): EVERY registration path must store the
// account under an all-lowercase key, whatever case the caller used.
func TestAllRegistrationPathsStoreLowercaseKeys(t *testing.T) {
	s := newTokensStore(t) // domain "t"; also registers alice@t via the normal path

	// 1. Plain registration with an uppercase name.
	if _, err := s.CreateAccountWithPassword("MIXEDcase", "t", false, "pw-one-2-3"); err != nil {
		t.Fatalf("create: %v", err)
	}
	// 2. Team registration: uppercase owner + uppercase member names.
	if _, _, err := s.RegisterTeam("TeamBoss", "t", "pw-one-2-3", 2, []string{"MemberA", "memberB"}); err != nil {
		t.Fatalf("team: %v", err)
	}
	// 3. Admin bootstrap with an uppercase address.
	if err := s.EnsureAdmin("ADMIN@t", "pw-one-2-3"); err != nil {
		t.Fatalf("admin: %v", err)
	}

	// Sweep the bucket: every key must be all-lowercase, and each mixed-case
	// input must be readable via its lowercase form.
	var bad []string
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bAccounts).ForEach(func(k, v []byte) error {
			for _, r := range string(k) {
				if r >= 'A' && r <= 'Z' {
					bad = append(bad, string(k))
					break
				}
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(bad) != 0 {
		t.Fatalf("non-lowercase account keys stored: %v", bad)
	}
	for _, addr := range []string{"mixedcase@t", "teamboss@t", "membera@t", "memberb@t", "admin@t"} {
		if _, err := s.GetAccount(addr); err != nil {
			t.Fatalf("account %s not resolvable: %v", addr, err)
		}
	}
}

// TestNoMixedCaseViaListPaths guards the return-layer merge: every creation
// path plus ListAccounts must present only lowercase addresses.
func TestNoMixedCaseViaListPaths(t *testing.T) {
	s := newTokensStore(t)
	if _, err := s.CreateAccountWithPassword("PoP", "t", false, "pw-one-2-3"); err != nil {
		t.Fatalf("create: %v", err)
	}
	accs, err := s.ListAccounts()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, a := range accs {
		if a.Address != strings.ToLower(a.Address) {
			t.Fatalf("ListAccounts exposed mixed-case address: %s", a.Address)
		}
	}
}
