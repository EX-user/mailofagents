package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// Display local-part (V06_DISPLAY_ADDRESS, superior ruling 01M13ZXK): each
// account keeps ONE optional case-preserved spelling of its own address,
// shown ONLY to the account itself (settings page + self-query APIs). Mail
// surfaces stay all-lowercase — display never enters headers, lists, or
// directory. The single invariant: strings.ToLower(display) == account key.
// Empty = unset (older records; UI falls back to the key, zero migration).

var (
	ErrDisplayLocalInvalid = errors.New("display local-part invalid")
)

// ValidateDisplayLocal checks the case-preserved local part: ASCII rules
// identical to registration ([a-zA-Z0-9_-]+) and lowercased it must equal
// the account key's local part.
func ValidateDisplayLocal(display, key string) error {
	local := key
	if at := strings.IndexByte(key, '@'); at > 0 {
		local = key[:at]
	}
	if len(display) > 64 {
		return fmt.Errorf("%w: too long", ErrDisplayLocalInvalid)
	}
	for _, r := range display {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if !ok {
			return fmt.Errorf("%w: bad rune %q", ErrDisplayLocalInvalid, r)
		}
	}
	if strings.ToLower(display) != local {
		return fmt.Errorf("%w: lowercased display must equal account local part", ErrDisplayLocalInvalid)
	}
	return nil
}

// SetDisplayLocal persists the account's case-preserved local part. Empty
// string clears it (falls back to key display).
func (s *Store) SetDisplayLocal(address, display string) error {
	address = strings.ToLower(address)
	if display != "" {
		if err := ValidateDisplayLocal(display, address); err != nil {
			return err
		}
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bAccounts)
		val := b.Get([]byte(address))
		if val == nil {
			return ErrAccountNotFound
		}
		var acc Account
		if err := json.Unmarshal(val, &acc); err != nil {
			return err
		}
		acc.DisplayLocal = display
		data, err := json.Marshal(acc)
		if err != nil {
			return err
		}
		return b.Put([]byte(address), data)
	})
}

// GetDisplayLocal returns the account's display local-part ("" = unset).
func (s *Store) GetDisplayLocal(address string) (string, error) {
	acc, err := s.GetAccount(strings.ToLower(address))
	if err != nil {
		return "", err
	}
	return acc.DisplayLocal, nil
}
