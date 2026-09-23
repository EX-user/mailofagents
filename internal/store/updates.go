package store

// System-update pushes (0.3.2 updates-modal data plane; contract 0922 Devi,
// front-end shell already live in app.js updatesModalInit). A small
// admin-curated list of release announcements; each account sees the newest
// published push once, then acknowledges it.
//
// Row shape {id, version, title, body_md, published_at, published}. IDs are
// unix-milli ints stored as 8-byte big-endian keys, so bolt's lexicographic
// key order equals chronological order and every "unread" question reduces
// to an integer comparison against the account's LastReadPushID watermark.
// Drafts (Published=false) are invisible on the self endpoints; the admin
// list sees them.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

// PushRecord is one system-update announcement.
type PushRecord struct {
	ID          int64  `json:"id"`
	Version     string `json:"version,omitempty"`
	Title       string `json:"title"`
	BodyMD      string `json:"body_md"`
	PublishedAt int64  `json:"published_at,omitempty"` // unix seconds; stamped on first publish
	Published   bool   `json:"published"`
}

func pushKey(id int64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(id))
	return buf
}

// UpsertPush inserts a new push (ID==0 mints a unix-milli id and, when
// publishing, stamps PublishedAt) or updates an existing one in place.
// Title and BodyMD are required; Version is capped to keep the modal
// headline sane.
func (s *Store) UpsertPush(p PushRecord) (PushRecord, error) {
	if p.ID == 0 {
		p.ID = time.Now().UnixMilli()
	}
	if p.ID < 0 {
		return PushRecord{}, fmt.Errorf("negative push id")
	}
	if p.Published && p.PublishedAt == 0 {
		p.PublishedAt = time.Now().Unix()
	}
	if len(p.Title) == 0 {
		return PushRecord{}, fmt.Errorf("push title required")
	}
	if len(p.Version) > 32 {
		return PushRecord{}, fmt.Errorf("push version too long (max 32 chars)")
	}
	if len(p.Title) > 200 {
		return PushRecord{}, fmt.Errorf("push title too long (max 200 chars)")
	}
	if len(p.BodyMD) > 10000 {
		return PushRecord{}, fmt.Errorf("push body too long (max 10000 chars)")
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		val, err := json.Marshal(p)
		if err != nil {
			return err
		}
		return tx.Bucket(bUpdates).Put(pushKey(p.ID), val)
	})
	if err != nil {
		return PushRecord{}, err
	}
	return p, nil
}

// GetPush fetches one push by id (drafts included).
func (s *Store) GetPush(id int64) (PushRecord, bool, error) {
	var out PushRecord
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		val := tx.Bucket(bUpdates).Get(pushKey(id))
		if val == nil {
			return nil
		}
		found = true
		return json.Unmarshal(val, &out)
	})
	return out, found, err
}

// ListPushes returns pushes in ascending id (chronological) order. Drafts
// are included only when includeDrafts is set (admin list).
func (s *Store) ListPushes(includeDrafts bool) ([]PushRecord, error) {
	var out []PushRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bUpdates).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var p PushRecord
			if err := json.Unmarshal(v, &p); err != nil {
				return err
			}
			if includeDrafts || p.Published {
				out = append(out, p)
			}
		}
		return nil
	})
	return out, err
}

// LatestPublishedPush returns the newest published push (drafts never win,
// even when newer).
func (s *Store) LatestPublishedPush() (PushRecord, bool, error) {
	all, err := s.ListPushes(false)
	if err != nil || len(all) == 0 {
		return PushRecord{}, false, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all[len(all)-1], true, nil
}

// CountPublishedAfter counts published pushes with id strictly greater than
// watermark — the "N more updates" figure behind unread_more.
func (s *Store) CountPublishedAfter(watermark int64) (int, error) {
	all, err := s.ListPushes(false)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range all {
		if p.ID > watermark {
			n++
		}
	}
	return n, nil
}

// LastReadPushID returns the account's read watermark (0 = nothing read).
func (s *Store) LastReadPushID(address string) (int64, error) {
	var out int64
	err := s.db.View(func(tx *bolt.Tx) error {
		val := tx.Bucket(bAccounts).Get([]byte(address))
		if val == nil {
			return ErrAccountNotFound
		}
		var acc Account
		if err := json.Unmarshal(val, &acc); err != nil {
			return err
		}
		out = acc.LastReadPushID
		return nil
	})
	return out, err
}

// MarkPushRead advances the account's read watermark to id (monotonic: an
// older id never lowers it, so a stale client ack cannot resurrect unread
// state). Unknown accounts are rejected; unknown pushes are the caller's
// problem and simply advance the watermark.
func (s *Store) MarkPushRead(address string, id int64) error {
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
		if id <= acc.LastReadPushID {
			return nil // already caught up; keep the record byte-identical
		}
		acc.LastReadPushID = id
		newVal, err := json.Marshal(acc)
		if err != nil {
			return err
		}
		return b.Put([]byte(address), newVal)
	})
}

// EnsureSeedPushes inserts the given pushes only when the updates bucket has
// no published entries at all (first boot with the data plane). Idempotent:
// once any published push exists, the seed is a no-op and admin-entered
// content is never overwritten.
func (s *Store) EnsureSeedPushes(seeds []PushRecord) (int, error) {
	existing, err := s.ListPushes(false)
	if err != nil {
		return 0, err
	}
	if len(existing) > 0 {
		return 0, nil
	}
	n := 0
	for _, p := range seeds {
		if _, err := s.UpsertPush(p); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
