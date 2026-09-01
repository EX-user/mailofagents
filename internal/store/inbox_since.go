package store

import (
	"encoding/json"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// ReadInboxSince returns inbox messages with ID strictly greater than sinceID,
// newest-first, up to limit. ULIDs are lexicographically time-ordered, so
// string comparison gives correct chronological filtering. This enables
// incremental pulls: the caller passes the last-seen id and receives only
// newer letters.
func (s *Store) ReadInboxSince(address, sinceID string, limit int) ([]MessageSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	acc, err := s.GetAccount(address)
	if err != nil {
		return nil, err
	}
	prefix := indexKey(acc.UUID, "")
	prefixStr := string(prefix)
	var ids []string
	err = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bInbox)
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, _ := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), prefixStr); k, _ = c.Next() {
			id := string(k[len(prefix):])
			if id > sinceID {
				ids = append(ids, id)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Newest-first: cursor gives ascending ULID; reverse.
	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}
	if len(ids) == 0 {
		return []MessageSummary{}, nil
	}
	out := make([]MessageSummary, 0, len(ids))
	err = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMessages)
		if mb == nil {
			return nil
		}
		ub := tx.Bucket(bUnread)
		for _, id := range ids {
			val := mb.Get([]byte(id))
			if val == nil {
				continue
			}
			var msg Message
			if err := json.Unmarshal(val, &msg); err != nil {
				continue
			}
			ms := summarize(msg)
			if ub != nil {
				ms.Unread = ub.Get(indexKey(acc.UUID, id)) != nil
			}
			out = append(out, ms)
		}
		return nil
	})
	return out, err
}
