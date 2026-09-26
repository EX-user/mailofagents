// Avatar storage: a dedicated bucket mapping lowercase address -> image
// bytes, with the Account carrying avatar_hash/avatar_at for zero-migration
// discovery (0.3.3 IM-ization, feature A). Deliberately NOT the attachment
// buckets — avatars carry no TTL, no share-grant, no quota accounting; and
// NOT Prefs — image bytes would weigh down every account read. One Update
// transaction writes bytes + hash atomically; overwrite deletes the old
// bytes in the same transaction (contract: replace-on-success).
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// updateAvatarMeta rewrites the account record inside the caller's
// transaction (the account write pattern of UpdateProfile, inlined so the
// avatar bytes and their hash land in ONE transaction).
func (s *Store) updateAvatarMeta(tx *bolt.Tx, address string, hash string, at int64) error {
	b := tx.Bucket(bAccounts)
	val := b.Get([]byte(address))
	if val == nil {
		return ErrAccountNotFound
	}
	var acc Account
	if err := json.Unmarshal(val, &acc); err != nil {
		return err
	}
	acc.AvatarHash = hash
	acc.AvatarAt = at
	newVal, err := json.Marshal(acc)
	if err != nil {
		return err
	}
	return b.Put([]byte(address), newVal)
}

// AvatarMaxBytes caps one avatar image (server-side hard cap; the API
// layer also enforces dimension/size checks before calling SaveAvatar).
const AvatarMaxBytes = 100 << 10 // 100KB

// SaveAvatar stores (or replaces) the avatar image for an address and
// updates the account's avatar_hash/avatar_at in the same transaction.
// content must already be validated by the API layer (magic-number sniff,
// dimensions, size). Returns the new hash (hex of a random 16-byte tag —
// content-addressing is unnecessary at this size and a random tag makes
// the ETag unguessable).
func (s *Store) SaveAvatar(address string, content []byte) (hash string, err error) {
	if len(content) == 0 || len(content) > AvatarMaxBytes {
		return "", ErrQuotaExceeded
	}
	buf := make([]byte, 16)
	if _, err = rand.Read(buf); err != nil {
		return "", fmt.Errorf("avatar tag: %w", err)
	}
	hash = hex.EncodeToString(buf)
	key := []byte(address)
	err = s.db.Update(func(tx *bolt.Tx) error {
		ab := tx.Bucket(bAvatars)
		if err := ab.Put(key, content); err != nil {
			return err
		}
		return s.updateAvatarMeta(tx, address, hash, s.now().Unix())
	})
	if err != nil {
		return "", fmt.Errorf("save avatar: %w", err)
	}
	return hash, nil
}

// DeleteAvatar removes the avatar image and clears the account's
// hash/timestamp. Missing avatar = no-op (idempotent delete).
func (s *Store) DeleteAvatar(address string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		tx.Bucket(bAvatars).Delete([]byte(address))
		return s.updateAvatarMeta(tx, address, "", 0)
	})
}

// GetAvatar returns the stored avatar bytes for an address (nil, nil when
// none is set — callers map that to 404 so the client falls back to the
// generated default avatar).
func (s *Store) GetAvatar(address string) ([]byte, error) {
	var out []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bAvatars).Get([]byte(address)); v != nil {
			out = append([]byte(nil), v...) // copy: value invalid after tx
		}
		return nil
	})
	return out, err
}
