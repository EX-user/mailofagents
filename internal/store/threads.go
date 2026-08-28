package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"

	bolt "go.etcd.io/bbolt"
)

// Threads (v0.6.15, contract alice V07_THREAD_BACKEND): in_reply_to is the
// ONLY edge source. A topic is a maximal connected component over the
// CALLER-VISIBLE message set (my inbox + my sent, read-only merged with each
// declared subordinate's — the exact same rule as the mgmt overview, so the
// two views can never disagree). The earliest message in a component is its
// root. Dangling references (parent invisible or deleted) degrade to
// no-edge; union-find is inherently cycle-safe (defensive: validated writes
// cannot produce cycles anyway).

// ErrNoSuchThread is returned when a root query names a message outside the
// caller's visible set (404 — masquerade semantics like the rest of subs).
var ErrNoSuchThread = errors.New("no such thread")

// ThreadTopic is one row of the topic index.
type ThreadTopic struct {
	RootID       string   `json:"root_id"`
	Subject      string   `json:"subject"`
	Participants []string `json:"participants"`
	Count        int      `json:"count"`
	LastAt       int64    `json:"last_at"`
}

// ThreadView is the connected-component payload for /api/thread?root=.
type ThreadView struct {
	Root     string           `json:"root"`
	Messages []MessageSummary `json:"messages"`
	Count    int              `json:"count"`
}

// visibleMessages loads every message visible to me (self + declared
// subordinates, inbox ∪ sent per account) keyed by ID. Single scan per
// account index — no new bucket (mgmt-overview precedent); revisit only
// past ~50k visible messages per account.
func (s *Store) visibleMessages(me string) (map[string]*Message, error) {
	ids := map[string]bool{}
	collect := func(addr string) {
		acc, err := s.GetAccount(addr)
		if err != nil {
			return
		}
		prefix := indexKey(acc.UUID, "")
		_ = s.db.View(func(tx *bolt.Tx) error {
			for _, b := range [][]byte{bInbox, bSent} {
				c := tx.Bucket(b).Cursor()
				for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
					ids[string(k[len(prefix):])] = true
				}
			}
			return nil
		})
	}
	collect(me)
	for _, e := range s.SubordinatesOf(me) {
		collect(e.Address)
	}
	out := make(map[string]*Message, len(ids))
	_ = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMessages)
		for id := range ids {
			if raw := mb.Get([]byte(id)); raw != nil {
				var m Message
				if json.Unmarshal(raw, &m) == nil {
					out[id] = &m
				}
			}
		}
		return nil
	})
	return out, nil
}

// threadComponent is one derived component, ready for either endpoint.
type threadComponent struct {
	root     *Message
	members  []*Message // ULID ascending (== chronological)
	lastAt   int64
	subject  string
	parts    []string
}

// deriveComponents unions the visible set along in_reply_to edges that stay
// INSIDE the set (dangling parents degrade), then orders members by ULID.
func deriveComponents(msgs map[string]*Message) []threadComponent {
	parent := map[string]string{}
	for id, m := range msgs {
		if m.InReplyTo == "" {
			continue
		}
		if _, ok := msgs[m.InReplyTo]; ok { // edge only between visible messages
			parent[id] = m.InReplyTo
		}
	}
	// Union-find over visible IDs.
	uf := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		for {
			p, ok := uf[x]
			if !ok || p == x {
				uf[x] = x
				return x
			}
			uf[x] = uf[p] // path compression
			x = uf[p]
		}
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			uf[ra] = rb
		}
	}
	for id := range msgs {
		find(id)
	}
	for id, p := range parent {
		union(id, p)
	}
	groups := map[string][]*Message{}
	for id, m := range msgs {
		r := find(id)
		groups[r] = append(groups[r], m)
	}
	out := make([]threadComponent, 0, len(groups))
	for _, ms := range groups {
		sort.Slice(ms, func(i, j int) bool { return ms[i].ID < ms[j].ID }) // ULID = time order
		c := threadComponent{members: ms, root: ms[0], subject: ms[0].Subject, lastAt: ms[0].ReceivedAt}
		seen := map[string]bool{}
		for _, m := range ms {
			if m.ReceivedAt > c.lastAt {
				c.lastAt = m.ReceivedAt
			}
			for _, addr := range append(append([]string{m.From}, m.To...), m.CC...) {
				key := addr
				if !seen[key] {
					seen[key] = true
					c.parts = append(c.parts, addr)
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// Threads returns the topic index: components with count >= minCount,
// last_at descending (root_id asc as deterministic tiebreak), paginated.
// total is the filtered count BEFORE pagination (v0.6 contract: total+limit+offset).
func (s *Store) Threads(me string, limit, offset, minCount int) ([]ThreadTopic, int, error) {
	msgs, err := s.visibleMessages(me)
	if err != nil {
		return nil, 0, err
	}
	comps := deriveComponents(msgs)
	filtered := make([]threadComponent, 0, len(comps))
	for _, c := range comps {
		if len(c.members) >= minCount {
			filtered = append(filtered, c)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].lastAt != filtered[j].lastAt {
			return filtered[i].lastAt > filtered[j].lastAt
		}
		return filtered[i].root.ID < filtered[j].root.ID
	})
	total := len(filtered)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	out := make([]ThreadTopic, 0, end-offset)
	for _, c := range filtered[offset:end] {
		out = append(out, ThreadTopic{
			RootID:       c.root.ID,
			Subject:      c.subject,
			Participants: c.parts,
			Count:        len(c.members),
			LastAt:       c.lastAt,
		})
	}
	return out, total, nil
}

// ThreadByRoot resolves any visible message ID to its component and returns
// the whole block (the ?root= parameter may name any member; the response's
// root is the block's earliest message — read-time normalization, same
// philosophy as dangling-subordinate promotion).
func (s *Store) ThreadByRoot(me, id string) (*ThreadView, error) {
	msgs, err := s.visibleMessages(me)
	if err != nil {
		return nil, err
	}
	if _, ok := msgs[id]; !ok {
		return nil, ErrNoSuchThread
	}
	for _, c := range deriveComponents(msgs) {
		for _, m := range c.members {
			if m.ID == id {
				sums := make([]MessageSummary, 0, len(c.members))
				for _, m := range c.members {
					sums = append(sums, summarize(*m))
				}
				return &ThreadView{Root: c.root.ID, Messages: sums, Count: len(sums)}, nil
			}
		}
	}
	return nil, ErrNoSuchThread
}
