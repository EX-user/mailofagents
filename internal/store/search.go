package store

import (
	"encoding/json"
	"sort"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// SearchAccount scans the given boxes ("in", "out") of the account's mail for
// messages whose subject, from, to, cc or body contain q as a case-insensitive
// substring. Results are newest-first with box-merge dedup by message id, and
// total is the match count across the whole scanned range (mailbox-wide
// semantics, mirroring ReadInboxPaged's total_count contract). Empty boxes
// slice scans nothing.
func (s *Store) SearchAccount(address string, boxes []string, q string, limit, offset int) ([]MessageSummary, int, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	needle := strings.ToLower(q)
	acc, err := s.GetAccount(address)
	if err != nil {
		return nil, 0, err
	}
	var matched []MessageSummary
	err = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMessages)
		if mb == nil {
			return nil
		}
		ub := tx.Bucket(bUnread)
		for _, box := range boxes {
			var bucket []byte
			switch box {
			case "in":
				bucket = bInbox
			case "out":
				bucket = bSent
			default:
				continue
			}
			b := tx.Bucket(bucket)
			if b == nil {
				continue
			}
			prefix := indexKey(acc.UUID, "")
			prefixStr := string(prefix)
			c := b.Cursor()
			for k, _ := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), prefixStr); k, _ = c.Next() {
				id := string(k[len(prefix):])
				val := mb.Get([]byte(id))
				if val == nil {
					continue
				}
				var msg Message
				if err := json.Unmarshal(val, &msg); err != nil {
					continue
				}
				if !messageMatches(msg, needle) {
					continue
				}
				ms := summarize(msg)
				if ub != nil {
					ms.Unread = ub.Get(indexKey(acc.UUID, id)) != nil
				}
				matched = append(matched, ms)
			}
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	// Merge boxes newest-first: ids are ULIDs, so string order is time order.
	// A letter present in both boxes (e.g. self-sent) surfaces once.
	sort.Slice(matched, func(i, j int) bool { return matched[i].ID > matched[j].ID })
	dedup := matched[:0]
	seen := make(map[string]struct{}, len(matched))
	for _, ms := range matched {
		if _, dup := seen[ms.ID]; dup {
			continue
		}
		seen[ms.ID] = struct{}{}
		dedup = append(dedup, ms)
	}
	matched = dedup
	total := len(matched)
	if offset >= len(matched) {
		return []MessageSummary{}, total, nil
	}
	matched = matched[offset:]
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, total, nil
}

// messageMatches reports whether any searchable field contains the
// pre-lowered needle.
func messageMatches(m Message, needle string) bool {
	if strings.Contains(strings.ToLower(m.Subject), needle) ||
		strings.Contains(strings.ToLower(m.From), needle) ||
		strings.Contains(strings.ToLower(m.Body), needle) {
		return true
	}
	for _, t := range m.To {
		if strings.Contains(strings.ToLower(t), needle) {
			return true
		}
	}
	for _, t := range m.CC {
		if strings.Contains(strings.ToLower(t), needle) {
			return true
		}
	}
	return false
}
