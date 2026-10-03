package store

import (
	"encoding/json"
	"fmt"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// Whitelist gate (0.3.7, boss directive 10-03 23:51). Each account can
// enable a sender whitelist: when enabled, an inbound letter from an
// address that is not on the list is not stored at all — the recipient
// has zero awareness — and the send API answers 403
// code=whitelist_rejected for that recipient.
//
// Hierarchy exemption is bidirectional (spec point 6): if the sender and
// the recipient are related by a declare edge in EITHER direction
// (superior->subordinate or subordinate->superior), the letter is
// delivered regardless of the whitelist. The hierarchy check runs FIRST;
// the whitelist is only consulted on a miss.
//
// Disabling the gate is a no-op by construction: the check lives on the
// delivery path and simply never fires when disabled (spec point 4).

// SetWhitelistEnabled toggles an account's inbound whitelist gate.
func (s *Store) SetWhitelistEnabled(address string, enabled bool) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return mutateAccountBytes(tx, address, func(acc *Account) error {
			acc.WhitelistEnabled = enabled
			return nil
		})
	})
}

// WhitelistSet replaces the account's whitelist with the given addresses
// (lowercased, deduplicated, empties dropped). Works while the gate is
// off too — pre-filling is explicitly allowed (spec point 1).
func (s *Store) WhitelistSet(address string, addresses []string) error {
	clean := normalizeWhitelist(addresses)
	return s.db.Update(func(tx *bolt.Tx) error {
		return mutateAccountBytes(tx, address, func(acc *Account) error {
			acc.Whitelist = clean
			return nil
		})
	})
}

// WhitelistAdd adds one address (idempotent).
func (s *Store) WhitelistAdd(address, entry string) error {
	entry = strings.ToLower(strings.TrimSpace(entry))
	if entry == "" {
		return fmt.Errorf("empty address")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return mutateAccountBytes(tx, address, func(acc *Account) error {
			for _, a := range acc.Whitelist {
				if a == entry {
					return nil
				}
			}
			acc.Whitelist = append(acc.Whitelist, entry)
			return nil
		})
	})
}

// WhitelistRemove drops one address (no error when absent).
func (s *Store) WhitelistRemove(address, entry string) error {
	entry = strings.ToLower(strings.TrimSpace(entry))
	return s.db.Update(func(tx *bolt.Tx) error {
		return mutateAccountBytes(tx, address, func(acc *Account) error {
			var kept []string
			for _, a := range acc.Whitelist {
				if a != entry {
					kept = append(kept, a)
				}
			}
			acc.Whitelist = kept
			return nil
		})
	})
}

func normalizeWhitelist(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range in {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// WhitelistRelated reports whether a declare edge exists between the two
// addresses in either direction. Best-effort: any store anomaly reads as
// "not related" so the whitelist still applies.
func (s *Store) WhitelistRelated(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	related := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bSubs).Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			sup, sub := splitSubKey(k)
			if (sup == a && sub == b) || (sup == b && sub == a) {
				related = true
				return nil
			}
		}
		return nil
	})
	return related
}

// WhitelistAdmits answers "may sender deliver into recipient's inbox?".
// Hierarchy exemption first (spec point 6), then the list. A disabled
// gate always admits without touching the list (spec point 4).
func (s *Store) WhitelistAdmits(recipient, sender string) bool {
	if s.WhitelistRelated(recipient, sender) {
		return true
	}
	acc, err := s.GetAccount(recipient)
	if err != nil || !acc.WhitelistEnabled {
		return true // no gate / unknown recipient: default open
	}
	sender = strings.ToLower(sender)
	for _, a := range acc.Whitelist {
		if a == sender {
			return true
		}
	}
	return false
}

// mutateAccountBytes loads, mutates, and stores one account record inside
// an open Update tx.
func mutateAccountBytes(tx *bolt.Tx, address string, fn func(acc *Account) error) error {
	b := tx.Bucket(bAccounts)
	val := b.Get([]byte(address))
	if val == nil {
		return ErrAccountNotFound
	}
	var acc Account
	if err := json.Unmarshal(val, &acc); err != nil {
		return err
	}
	if err := fn(&acc); err != nil {
		return err
	}
	newVal, err := json.Marshal(acc)
	if err != nil {
		return err
	}
	return b.Put([]byte(address), newVal)
}

// whitelistRelatedInTx is the in-transaction form of WhitelistRelated for
// the delivery gate: a declare edge in either direction admits.
func whitelistRelatedInTx(tx *bolt.Tx, a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	c := tx.Bucket(bSubs).Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		sup, sub := splitSubKey(k)
		if (sup == a && sub == b) || (sup == b && sub == a) {
			return true
		}
	}
	return false
}

// whitelistHasInTx answers "is sender on this account's list?" against an
// already-loaded record (case-normalized by the store mutators).
func whitelistHasInTx(acc *Account, sender string) bool {
	sender = strings.ToLower(sender)
	for _, a := range acc.Whitelist {
		if a == sender {
			return true
		}
	}
	return false
}
