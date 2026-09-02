package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Invalid-mail administration: admin review and real deletion of messages
// whose TO recipients all fail account lookup today. Superior directive
// (0902): deletion touches database content — every operation is audited
// upstream, and mass removals are preceded by an automatic snapshot.
//
// Classification (architect-settled boundary): a message is invalid iff its
// TO list is non-empty and EVERY TO address fails account lookup in the
// current account table (case-insensitive). CC recipients are not counted —
// a message delivered to any live CC or TO address is not invalid, and
// mixed-delivery mail is neither listed nor deletable. Because accounts are
// only ever added (never deleted) today, such mail comes from admin sends
// to never-existing addresses; the scan still re-checks live accounts so a
// future account deletion cannot make historic mail silently vanish.

// ulidKeyLen is the fixed ULID width (26 chars) that ends every
// inbox/unread/sent index key (see indexKey).
const ulidKeyLen = 26

// InvalidMail is one invalid message surfaced for admin review.
type InvalidMail struct {
	ID         string   `json:"id"`
	From       string   `json:"from"`
	Subject    string   `json:"subject"`
	To         []string `json:"to"` // the stored TO addresses; all invalid by definition
	ReceivedAt int64    `json:"received_at"`
}

// allToMissing reports whether m carries at least one TO address and every
// one of them fails account lookup in this transaction.
func allToMissing(tx *bolt.Tx, m Message) bool {
	if len(m.To) == 0 {
		return false // pure-CC or degenerate mail: delivered somewhere, not invalid
	}
	for _, addr := range m.To {
		if tx.Bucket(bAccounts).Get([]byte(strings.ToLower(addr))) != nil {
			return false // at least one live recipient: not invalid
		}
	}
	return true
}

// ListInvalidMail returns every invalid message, newest first.
func (s *Store) ListInvalidMail() ([]InvalidMail, error) {
	var out []InvalidMail
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bMessages).Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			var m Message
			if json.Unmarshal(v, &m) != nil {
				continue // corrupt record: skip, not fail the scan
			}
			if !allToMissing(tx, m) {
				continue
			}
			out = append(out, InvalidMail{
				ID:         m.ID,
				From:       m.From,
				Subject:    m.Subject,
				To:         m.To,
				ReceivedAt: m.ReceivedAt,
			})
		}
		return nil
	})
	return out, err
}

// DeleteInvalidMail removes whole message records — body plus every
// inbox/unread/sent index reference — for ids that STILL qualify as invalid,
// re-verified inside the same transaction (the account table may have
// changed since the listing; mixed-delivery mail is never removed).
// all=true re-scans instead of trusting the id list. Returns the number of
// records actually removed.
func (s *Store) DeleteInvalidMail(ids []string, all bool) (int, error) {
	deleted := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMessages)
		kill := map[string]bool{}
		if all {
			c := mb.Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				var m Message
				if json.Unmarshal(v, &m) == nil && allToMissing(tx, m) {
					kill[string(k)] = true
				}
			}
		} else {
			for _, id := range ids {
				id = strings.TrimSpace(id)
				if id == "" || kill[id] {
					continue
				}
				v := mb.Get([]byte(id))
				if v == nil {
					continue
				}
				var m Message
				if json.Unmarshal(v, &m) != nil {
					continue
				}
				if allToMissing(tx, m) {
					kill[id] = true
				}
			}
		}
		if len(kill) == 0 {
			return nil
		}
		for id := range kill {
			if err := mb.Delete([]byte(id)); err != nil {
				return err
			}
			deleted++
		}
		// Sweep every index bucket for references to the deleted ids: the
		// ULID is the fixed-width tail of each key, so a suffix match finds
		// refs of recipients that are valid, deleted, or unknown.
		for _, bucket := range [][]byte{bInbox, bUnread, bSent} {
			c := tx.Bucket(bucket).Cursor()
			for k, _ := c.First(); k != nil; k, _ = c.Next() {
				if len(k) >= ulidKeyLen && kill[string(k[len(k)-ulidKeyLen:])] {
					if err := c.Delete(); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	return deleted, err
}

// BackupDir returns the directory holding the database file; snapshots land
// next to it so they ride the same volume as the data they protect.
func (s *Store) BackupDir() string {
	return filepath.Dir(s.db.Path())
}

// BackupTo writes a consistent snapshot of the whole database via a
// read-only transaction (bbolt Tx.WriteTo) — safe while the server serves
// traffic, unlike a raw file copy.
func (s *Store) BackupTo(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create backup %q: %w", path, err)
	}
	defer f.Close()
	return s.db.View(func(tx *bolt.Tx) error {
		if _, err := tx.WriteTo(f); err != nil {
			return fmt.Errorf("write backup %q: %w", path, err)
		}
		return f.Sync()
	})
}

// BackupTimestamped writes a snapshot named backup-invalid-<timestamp>.db
// into BackupDir and returns its path.
func (s *Store) BackupTimestamped() (string, error) {
	name := fmt.Sprintf("backup-invalid-%s.db", time.Now().Format("20060102-150405"))
	path := filepath.Join(s.BackupDir(), name)
	return path, s.BackupTo(path)
}
