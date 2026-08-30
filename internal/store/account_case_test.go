package store

import (
	"encoding/json"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// newCaseTestStore opens a temp store (no bootstrap needed — we write
// accounts directly to exercise the bAccounts bucket).
func newCaseTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })
	return s
}

// putRawAccount writes an account record under an EXACT key (no
// normalization), simulating what pre-fix code left behind.
func putRawAccount(t *testing.T, s *Store, key, address, sig string) {
	t.Helper()
	acc := Account{UUID: key, Address: address, Signature: sig, CreatedAt: 1}
	val, err := json.Marshal(acc)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bAccounts).Put([]byte(key), val)
	}); err != nil {
		t.Fatalf("put %q: %v", key, err)
	}
}

// TestAccountAddressLowercasedOnCreate pins the fix: a mixed-case name
// registers under an all-lowercase key, so Foo@ and foo@ can't become two
// accounts, and GetAccount resolves either form to the same record.
func TestAccountAddressLowercasedOnCreate(t *testing.T) {
	s := newCaseTestStore(t)

	if _, err := s.CreateAccountWithPassword("FooBar", "test.example", false, "password123"); err != nil {
		t.Fatalf("create FooBar: %v", err)
	}
	// A second registration under a different case must collide, not create
	// a twin.
	if _, err := s.CreateAccountWithPassword("foobar", "test.example", false, "otherpass1"); err != ErrAccountExists {
		t.Fatalf("create foobar = %v, want ErrAccountExists", err)
	}
	// GetAccount resolves either case to the one record.
	a, err := s.GetAccount("FOOBAR@test.example")
	if err != nil {
		t.Fatalf("GetAccount uppercase: %v", err)
	}
	if a.Address != "foobar@test.example" {
		t.Fatalf("stored address = %q, want foobar@test.example", a.Address)
	}
}

// TestNormalizeAccountCase repairs legacy uppercase keys: a lone uppercase
// record is renamed to lowercase; an uppercase record that already has a
// lowercase twin is dropped as a dupe (lowercase wins); a clean store is a
// no-op; running twice is stable.
func TestNormalizeAccountCase(t *testing.T) {
	s := newCaseTestStore(t)

	// 1) Lone uppercase record -> renamed to lowercase.
	putRawAccount(t, s, "Alpha@test.example", "Alpha@test.example", "sig-a")
	// 2) Uppercase record WITH a lowercase twin -> dupe, lowercase wins.
	putRawAccount(t, s, "Beta@test.example", "Beta@test.example", "sig-B-upper")
	putRawAccount(t, s, "beta@test.example", "beta@test.example", "sig-b-lower")
	// 3) Already lowercase -> untouched.
	putRawAccount(t, s, "gamma@test.example", "gamma@test.example", "sig-g")

	res, err := s.NormalizeAccountCase()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if res.AlreadyLower != 2 || res.Renamed != 1 || res.DeletedDupes != 1 {
		t.Fatalf("counts = %+v, want already=2 renamed=1 dupes=1", res)
	}
	// Alpha renamed: old key gone, lowercase key present with fixed address.
	if _, err := s.GetAccount("alpha@test.example"); err != nil {
		t.Errorf("alpha lowercase missing: %v", err)
	}
	// The raw uppercase KEY must be gone (GetAccount now lowercases its
	// input, so checking Get is not enough — look at the bucket directly).
	if rawKeyExists(s, "Alpha@test.example") {
		t.Error("Alpha uppercase key still present in bucket (rename incomplete)")
	}
	if a, _ := s.GetAccount("alpha@test.example"); a != nil && a.Address != "alpha@test.example" {
		t.Errorf("alpha address = %q, want alpha@test.example", a.Address)
	}
	// Beta: lowercase twin survived with ITS signature.
	if a, err := s.GetAccount("beta@test.example"); err != nil || a.Signature != "sig-b-lower" {
		t.Errorf("beta twin = %v sig=%q, want sig-b-lower", err, sigOr(a))
	}
	// Gamma untouched.
	if a, err := s.GetAccount("gamma@test.example"); err != nil || a.Signature != "sig-g" {
		t.Errorf("gamma = %v sig=%q, want sig-g", err, sigOr(a))
	}
	// Second run is a no-op.
	res2, err := s.NormalizeAccountCase()
	if err != nil {
		t.Fatalf("normalize 2: %v", err)
	}
	if res2.Renamed != 0 || res2.DeletedDupes != 0 {
		t.Errorf("second run = %+v, want no work", res2)
	}
}

func sigOr(a *Account) string {
	if a == nil {
		return "<nil>"
	}
	return a.Signature
}

// rawKeyExists checks the bAccounts bucket for an EXACT key (no
// lowercasing), to verify a rename actually removed the old key.
func rawKeyExists(s *Store, key string) bool {
	var found bool
	_ = s.db.View(func(tx *bolt.Tx) error {
		found = tx.Bucket(bAccounts).Get([]byte(key)) != nil
		return nil
	})
	return found
}
