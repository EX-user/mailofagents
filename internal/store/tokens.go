package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Session tokens ("remember login", v0.6.27). The wire token is 256-bit
// random hex; bbolt stores ONLY its SHA-256 hash (a db leak must not yield
// usable bearer credentials). TTL 30 days with rolling renewal — each
// successful resolve slides the expiry forward, so an active device never
// re-authenticates while an abandoned token ages out on its own.
//
// Multiple tokens per account coexist (repeat login keeps old tokens:
// multi-device friendly, alice's ruling). Password change revokes all of
// them (handleChangePassword); logout revokes just the presented one.

var (
	ErrTokenExpired = errors.New("session token expired")
	ErrTokenInvalid = errors.New("session token invalid")
)

// SessionToken is the stored record (keyed by token hash).
type SessionToken struct {
	Address   string `json:"address"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
}

// sessionTTL is the rolling validity window.
const sessionTTL = 30 * 24 * time.Hour

// CreateSessionToken mints a new token for the account and stores only its
// hash. Returns the wire token (hex) and its expiry.
func (s *Store) CreateSessionToken(address string) (string, int64, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", 0, err
	}
	token := hex.EncodeToString(raw)
	now := time.Now().Unix()
	rec := SessionToken{Address: address, CreatedAt: now, ExpiresAt: now + int64(sessionTTL.Seconds())}
	err := s.db.Update(func(tx *bolt.Tx) error {
		data, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		return tx.Bucket(bTokens).Put(tokenHash(token), data)
	})
	if err != nil {
		return "", 0, err
	}
	return token, rec.ExpiresAt, nil
}

// ResolveSessionToken maps a wire token to its account, rejecting unknown
// or expired tokens and ROLLING the expiry forward (throttled to one write
// per hour per token so the hot auth path usually stays read-only).
func (s *Store) ResolveSessionToken(token string) (string, error) {
	var addr string
	var expired bool
	err := s.db.Update(func(tx *bolt.Tx) error {
		key := tokenHash(token)
		v := tx.Bucket(bTokens).Get(key)
		if v == nil {
			return ErrTokenInvalid
		}
		var rec SessionToken
		if err := json.Unmarshal(v, &rec); err != nil {
			return ErrTokenInvalid
		}
		now := time.Now().Unix()
		if now >= rec.ExpiresAt {
			// Return nil so the delete COMMITS — a non-nil return would roll
			// the whole transaction back and resurrect the dead token.
			expired = true
			return tx.Bucket(bTokens).Delete(key)
		}
		// Rolling renewal: only rewrite when a meaningful slice of the TTL
		// has elapsed, otherwise this resolves as a plain read.
		if rec.ExpiresAt-now < int64(sessionTTL.Seconds())-3600 {
			rec.ExpiresAt = now + int64(sessionTTL.Seconds())
			ndata, err := json.Marshal(rec)
			if err != nil {
				return err
			}
			if err := tx.Bucket(bTokens).Put(key, ndata); err != nil {
				return err
			}
		}
		addr = rec.Address
		return nil
	})
	if err != nil {
		return "", err
	}
	if expired {
		return "", ErrTokenExpired
	}
	return addr, nil
}

// RevokeSessionToken deletes one token (logout).
func (s *Store) RevokeSessionToken(token string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bTokens).Delete(tokenHash(token))
	})
}

// RevokeAllSessionTokens wipes every token of the account (password change
// invalidates every remembered device). Full-scan is fine: token counts are
// tiny and password changes are rare.
func (s *Store) RevokeAllSessionTokens(address string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		c := tx.Bucket(bTokens).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rec SessionToken
			if json.Unmarshal(v, &rec) == nil && rec.Address == address {
				if err := c.Delete(); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func tokenHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return []byte(hex.EncodeToString(h[:]))
}
