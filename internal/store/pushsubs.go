package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	bolt "go.etcd.io/bbolt"
)

// Push subscriptions (v0.6.30 app notifications, docs/push/DESIGN.md).
// Each record binds a Web Push endpoint to the account that created it —
// ownership is enforced at creation time (the request must carry that
// account's credential), so a subscription can never outlive or escape its
// owner. The endpoint is stored hashed as the key tail: the bbolt file alone
// must not yield usable push targets (same reasoning as session-token
// hashing, v0.6.27).
//
// Multiple devices per account coexist as separate entries; logout does NOT
// remove them (multi-device friendly), but DELETE /api/push/subscribe and
// account deletion do.

var ErrPushSubInvalid = errors.New("push subscription invalid")

// PushSubscription is one device's Web Push registration.
type PushSubscription struct {
	Address   string `json:"address"`    // owner account
	Endpoint  string `json:"endpoint"`   // push service URL (HTTPS)
	P256dh    string `json:"p256dh"`     // client public key
	Auth      string `json:"auth"`       // auth secret
	CreatedAt int64  `json:"created_at"`
}

func subHash(endpoint string) []byte {
	h := sha256.Sum256([]byte(endpoint))
	return []byte(hex.EncodeToString(h[:]))
}

func pushSubKey(endpoint string) []byte {
	return subHash(endpoint)
}

// UpsertPushSub stores (or idempotently re-stores) a subscription. The push
// endpoint is the global identity: a refresh from its owning account
// overwrites in place, while the SAME endpoint arriving under a DIFFERENT
// account is rejected — one account cannot hijack another's registration.
func (s *Store) UpsertPushSub(rec *PushSubscription) error {
	if rec.Address == "" || rec.Endpoint == "" {
		return ErrPushSubInvalid
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bPushSubs)
		key := pushSubKey(rec.Endpoint)
		if old := b.Get(key); old != nil {
			var prev PushSubscription
			if json.Unmarshal(old, &prev) == nil && prev.Address != rec.Address {
				return ErrPushSubInvalid // endpoint owned by another account
			}
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		return b.Put(key, data)
	})
}

// RemovePushSub deletes one subscription; safe when absent or foreign.
func (s *Store) RemovePushSub(address, endpoint string) error {
	if address == "" || endpoint == "" {
		return ErrPushSubInvalid
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bPushSubs)
		key := pushSubKey(endpoint)
		v := b.Get(key)
		if v == nil {
			return nil
		}
		var rec PushSubscription
		if json.Unmarshal(v, &rec) == nil && rec.Address != address {
			return ErrPushSubInvalid // not ours to remove
		}
		return b.Delete(key)
	})
}

// PushSubsByAddress lists every live subscription of the account. A full-scan
// filter is fine here: subscription counts are tiny (devices, not messages).
func (s *Store) PushSubsByAddress(address string) ([]*PushSubscription, error) {
	var out []*PushSubscription
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bPushSubs).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rec PushSubscription
			if json.Unmarshal(v, &rec) == nil && rec.Address == address {
				out = append(out, &rec)
			}
		}
		return nil
	})
	return out, err
}

// DeleteAllPushSubs wipes every subscription of the account (account removal
// cascade must not leave orphaned endpoints behind).
func (s *Store) DeleteAllPushSubs(address string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bPushSubs)
		c := b.Cursor()
		var doomed [][]byte
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var rec PushSubscription
			if json.Unmarshal(v, &rec) == nil && rec.Address == address {
				doomed = append(doomed, append([]byte(nil), k...))
			}
		}
		for _, k := range doomed {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return tx.Bucket(bPushDND).Delete([]byte(address)) // cascade the DND preference too
	})
}

// PushDND is an account's do-not-disturb window for notifications. Ruling
// (alice 01M11J45M): the SERVER decides silence — client clocks are
// untrusted. Default off; the preference page turns it on.
type PushDND struct {
	Enabled  bool `json:"enabled"`
	StartMin int  `json:"start_min"` // minutes since local midnight, inclusive
	EndMin   int  `json:"end_min"`   // exclusive; Start==End means never silent
}

func (d PushDND) ActiveAt(minOfDay int) bool {
	if !d.Enabled || d.StartMin == d.EndMin {
		return false
	}
	if d.StartMin < d.EndMin {
		return minOfDay >= d.StartMin && minOfDay < d.EndMin
	}
	// Window wraps midnight (e.g. 22:00–07:00).
	return minOfDay >= d.StartMin || minOfDay < d.EndMin
}

// GetPushDND returns the account's DND window (zero value = disabled).
func (s *Store) GetPushDND(address string) (PushDND, error) {
	var d PushDND
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bPushDND).Get([]byte(address))
		if v == nil {
			return nil
		}
		return json.Unmarshal(v, &d)
	})
	return d, err
}

// SetPushDND persists the DND window.
func (s *Store) SetPushDND(address string, d PushDND) error {
	valid := func(n int) bool { return n >= 0 && n < 24*60 }
	if !valid(d.StartMin) || !valid(d.EndMin) {
		return ErrPushSubInvalid
	}
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bPushDND).Put([]byte(address), data)
	})
}
