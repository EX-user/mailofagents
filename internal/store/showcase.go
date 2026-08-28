package store

import (
	"encoding/json"
	"errors"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// ErrShowcaseNotFound is returned by DeleteShowcaseEntry when no entry has
// the given id (handler maps it to 404).
var ErrShowcaseNotFound = errors.New("showcase entry not found")

// bShowcase stores optional public copies of messages (the "showcase" tee:
// senders can mark a send public, and the portal shows a random sample of
// recent public mail). Completely decoupled from real mail: separate bucket,
// independent IDs, capped size, never touched by inbox/sent reads.
var bShowcase = []byte("showcase")

// ShowcaseCap is the maximum number of public copies kept; the oldest are
// evicted on insert (ULID keys are time-ordered, so oldest = first key).
const ShowcaseCap = 1000

// ShowcaseEntry is one public copy of a sent message. The ID is independent
// of the real message ID (public entries must not be correlated with — or
// grant access to — the originals).
type ShowcaseEntry struct {
	ID         string   `json:"id"`
	From       string   `json:"from"`
	To         []string `json:"to"`
	Subject    string   `json:"subject"`
	Body       string   `json:"body"`
	ReceivedAt int64    `json:"received_at"`
}

// TeeShowcase writes a public copy of a sent message and evicts oldest
// entries beyond ShowcaseCap. One transaction: the tee either lands whole
// or not at all.
func (s *Store) TeeShowcase(from string, to []string, subject, body string) error {
	now := s.now()
	entry := ShowcaseEntry{
		ID:         newULID(),
		From:       from,
		To:         to,
		Subject:    subject,
		Body:       body,
		ReceivedAt: now.Unix(),
	}
	val, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal showcase: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		sb := tx.Bucket(bShowcase)
		if err := sb.Put([]byte(entry.ID), val); err != nil {
			return err
		}
		// Evict oldest beyond the cap. Count via cursor walk: Bucket.Stats()
		// is unreliable inside the writing transaction (over-reported counts
		// caused mass over-eviction in tests). Keys are ULIDs, so byte order
		// is time order — the first keys are the oldest.
		var keys [][]byte
		c := sb.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			keys = append(keys, k)
		}
		if over := len(keys) - ShowcaseCap; over > 0 {
			for _, k := range keys[:over] {
				if err := sb.Delete(k); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ListShowcase returns public entries, newest first.
func (s *Store) ListShowcase() ([]ShowcaseEntry, error) {
	var entries []ShowcaseEntry
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bShowcase).ForEach(func(_, v []byte) error {
			var e ShowcaseEntry
			if err := json.Unmarshal(v, &e); err != nil {
				return nil // skip corrupt records
			}
			entries = append(entries, e)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	// ForEach walks in key order (oldest first for ULIDs); reverse.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}

// CountShowcase returns the number of public entries (for tests + stats).
func (s *Store) CountShowcase() (int, error) {
	var n int
	err := s.db.View(func(tx *bolt.Tx) error {
		n = tx.Bucket(bShowcase).Stats().KeyN
		return nil
	})
	return n, err
}

// ClearShowcase removes every public entry (admin operation, e.g. after bad
// data lands in the showcase). Recreates the bucket so it is atomically
// empty afterwards.
func (s *Store) ClearShowcase() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bShowcase); err != nil {
			return err
		}
		_, err := tx.CreateBucket(bShowcase)
		return err
	})
}

// DeleteShowcaseEntry removes one public entry by id. ErrShowcaseNotFound
// when the id is absent, so the handler can answer 404.
func (s *Store) DeleteShowcaseEntry(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		sb := tx.Bucket(bShowcase)
		if sb.Get([]byte(id)) == nil {
			return ErrShowcaseNotFound
		}
		return sb.Delete([]byte(id))
	})
}

// GetShowcaseEntry fetches one public entry by id (admin lookup for the
// search-then-delete flow). ErrShowcaseNotFound when absent.
func (s *Store) GetShowcaseEntry(id string) (*ShowcaseEntry, error) {
	var e ShowcaseEntry
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bShowcase).Get([]byte(id))
		if raw == nil {
			return ErrShowcaseNotFound
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return fmt.Errorf("corrupt showcase entry %q: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &e, nil
}
