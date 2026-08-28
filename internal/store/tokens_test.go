package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// newTokensStore boots a bare store with one account.
func newTokensStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.DB().Close() })
	if _, err := s.CreateAccountWithPassword("alice", "t", false, "pw-one-2-3"); err != nil {
		t.Fatalf("register: %v", err)
	}
	return s
}

// TestSessionTokenLifecycle pins the v0.6.27 contract: mint → resolve →
// revoke; unknown and revoked tokens are invalid; a second token for the
// same account survives a sibling's logout (multi-device friendly).
func TestSessionTokenLifecycle(t *testing.T) {
	s := newTokensStore(t)

	tok1, _, err := s.CreateSessionToken("alice@t")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if len(tok1) != 64 { // 256-bit hex
		t.Fatalf("token len = %d, want 64", len(tok1))
	}
	if addr, err := s.ResolveSessionToken(tok1); err != nil || addr != "alice@t" {
		t.Fatalf("resolve: %v %q", err, addr)
	}

	// Second device: independent token, both live.
	tok2, _, _ := s.CreateSessionToken("alice@t")
	if _, err := s.ResolveSessionToken(tok2); err != nil {
		t.Fatalf("resolve tok2: %v", err)
	}

	// Logout device 1 — device 2 keeps working.
	if err := s.RevokeSessionToken(tok1); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.ResolveSessionToken(tok1); err == nil {
		t.Fatal("revoked token still resolves")
	}
	if _, err := s.ResolveSessionToken(tok2); err != nil {
		t.Fatalf("sibling token died with logout: %v", err)
	}

	// Unknown token.
	if _, err := s.ResolveSessionToken("deadbeef"); err == nil {
		t.Fatal("unknown token resolves")
	}
}

// TestSessionTokenExpiryAndRenewal: an expired token is rejected (and
// deleted); a live token's expiry rolls forward as it is used.
func TestSessionTokenExpiryAndRenewal(t *testing.T) {
	s := newTokensStore(t)
	tok, exp, err := s.CreateSessionToken("alice@t")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if d := time.Until(time.Unix(exp, 0)); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Fatalf("ttl = %v, want ~30d", d)
	}

	// Force-expire by rewriting the record.
	err = s.db.Update(func(tx *bolt.Tx) error {
		key := tokenHash(tok)
		v := tx.Bucket(bTokens).Get(key)
		var rec SessionToken
		if err := json.Unmarshal(v, &rec); err != nil {
			return err
		}
		rec.ExpiresAt = time.Now().Unix() - 1
		data, _ := json.Marshal(rec)
		return tx.Bucket(bTokens).Put(key, data)
	})
	if err != nil {
		t.Fatalf("force-expire: %v", err)
	}
	if _, err := s.ResolveSessionToken(tok); err != ErrTokenExpired {
		t.Fatalf("expired resolve err = %v, want ErrTokenExpired", err)
	}
	if _, err := s.ResolveSessionToken(tok); err != ErrTokenInvalid {
		t.Fatalf("expired token not deleted, err = %v", err)
	}
}

// TestRevokeAllSessionTokens: password change wipes every token of the
// account and only that account.
func TestRevokeAllSessionTokens(t *testing.T) {
	s := newTokensStore(t)
	if _, err := s.CreateAccountWithPassword("bob", "t", false, "pw-456-789"); err != nil {
		t.Fatalf("register bob: %v", err)
	}
	at1, _, _ := s.CreateSessionToken("alice@t")
	at2, _, _ := s.CreateSessionToken("alice@t")
	bt, _, _ := s.CreateSessionToken("bob@t")

	if err := s.RevokeAllSessionTokens("alice@t"); err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	for _, tok := range []string{at1, at2} {
		if _, err := s.ResolveSessionToken(tok); err == nil {
			t.Fatal("alice token survived password change")
		}
	}
	if _, err := s.ResolveSessionToken(bt); err != nil {
		t.Fatalf("bob token collateral damage: %v", err)
	}
}
