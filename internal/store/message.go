package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// ErrMessageNotFound is returned when a message lookup misses.
var ErrMessageNotFound = errors.New("message not found")

// Message is the stored record for one piece of mail.
type Message struct {
	ID          string           `json:"id"`                     // ULID
	From        string           `json:"from"`                   // sender address
	To          []string         `json:"to"`                     // recipient addresses
	CC          []string         `json:"cc,omitempty"`           // carbon-copy addresses (delivered like To; visible to recipients)
	InReplyTo   string           `json:"in_reply_to,omitempty"`  // parent message ULID; empty = standalone root (threads v0.6.15)
	Subject     string           `json:"subject"`
	Body        string           `json:"body"`
	Attachments []AttachmentMeta `json:"attachments,omitempty"` // metadata only; content lives in the file store
	ReceivedAt  int64            `json:"received_at"`           // unix seconds
}

// ErrNoSuchParent is returned by the send paths when in_reply_to names a
// message that does not exist (existence is checked in the same transaction
// as the send; visibility deliberately is NOT — referencing a parent the
// sender cannot see is harmless, the read side prunes it).
var ErrNoSuchParent = errors.New("in_reply_to parent not found")

// AttachmentMeta is the message-side projection of an uploaded file: what
// the recipient needs to download it. The access code is included because
// download authorization requires it.
type AttachmentMeta struct {
	ID         string `json:"id"`
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
	AccessCode string `json:"access_code"`
	CreatedAt  int64  `json:"created_at,omitempty"` // upload time; 0 on legacy entries (pre-v0.5.3)
}

// AttachmentExpiresAt derives the expiry timestamp for one attachment
// meta. Legacy entries (CreatedAt == 0) resolve via the live file record;
// if that is gone too (already evicted), 0 is returned and callers may
// treat the attachment as expired.
func (s *Store) AttachmentExpiresAt(m AttachmentMeta) int64 {
	if m.CreatedAt != 0 {
		return m.CreatedAt + int64(FileTTL.Seconds())
	}
	if fr, err := s.GetFileMeta(m.ID); err == nil {
		return fr.CreatedAt + int64(FileTTL.Seconds())
	}
	return 0
}

// MessageSummary is a lightweight inbox entry (no body).
type MessageSummary struct {
	ID         string   `json:"id"`
	From       string   `json:"from"`
	To         []string `json:"to"`
	CC         []string `json:"cc,omitempty"`
	InReplyTo  string   `json:"in_reply_to,omitempty"` // populated so thread rails render without N+1 detail fetches
	Subject    string   `json:"subject"`
	Preview    string   `json:"preview"`
	ReceivedAt int64    `json:"received_at"`
	Unread     bool     `json:"unread"`
	Files      int      `json:"files"` // attachment count (detail fetch lists them)
}

// SendResult is returned by Send.
type SendResult struct {
	MessageID string `json:"message_id"`
}

// Send composes and delivers a message from "from" to every address in "to"
// (plus "cc", delivered identically and recorded on the message). It writes
// one copy of the message body and adds inbox references for each recipient
// plus a sent reference for the sender. Recipients that do not exist are
// silently skipped (the message still goes to the valid ones); if NO
// recipient is valid, Send returns an error.
//
// The whole operation is one bbolt transaction, so a crash mid-send leaves
// nothing half-delivered.
func (s *Store) Send(from, fromName string, to []string, cc []string, subject, body string, inReplyTo string) (*SendResult, error) {
	msgID := newULID()
	now := s.now().Unix()
	msg := Message{
		ID:         msgID,
		From:       from,
		To:         to,
		CC:         cc,
		InReplyTo:  inReplyTo,
		Subject:    subject,
		Body:       body,
		ReceivedAt: now,
	}
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	delivered := 0
	err = s.db.Update(func(tx *bolt.Tx) error {
		// Parent existence is checked in the SAME transaction as the write
		// (contract: one Get, miss -> 400, prevents fat-fingered chains).
		// Visibility is deliberately NOT checked — referencing a parent the
		// sender cannot see is harmless; the read side prunes it.
		if inReplyTo != "" && tx.Bucket(bMessages).Get([]byte(inReplyTo)) == nil {
			return ErrNoSuchParent
		}
		// Store the message body once.
		mb := tx.Bucket(bMessages)
		if existing := mb.Get([]byte(msgID)); existing != nil {
			// Idempotency guard: a retried send with the same ID is a no-op.
			return nil
		}
		if err := mb.Put([]byte(msgID), msgBytes); err != nil {
			return err
		}

		// Add an inbox reference for each valid recipient (To, then CC —
		// an address in both gets a single copy via the seen-set).
		ib := tx.Bucket(bInbox)
		ub := tx.Bucket(bUnread)
		seen := map[string]bool{}
		for _, addr := range append(append([]string{}, to...), cc...) {
			l := strings.ToLower(addr)
			if seen[l] {
				continue // dedup To/CC overlap
			}
			seen[l] = true
			acc, err := getAccountInTx(tx, addr)
			if err != nil {
				continue // unknown recipient: skip
			}
			key := indexKey(acc.UUID, msgID)
			if err := ib.Put(key, nil); err != nil {
				return err
			}
			// Mark as unread for this recipient.
			if err := ub.Put(key, nil); err != nil {
				return err
			}
			delivered++
		}

		// Add a sent reference for the sender.
		sb := tx.Bucket(bSent)
		if sender, err := getAccountInTx(tx, from); err == nil {
			key := indexKey(sender.UUID, msgID)
			if err := sb.Put(key, nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	if delivered == 0 {
		return nil, fmt.Errorf("no valid recipients among %v (cc: %v)", to, cc)
	}
	return &SendResult{MessageID: msgID}, nil
}

// ReadAllAccountsMessages returns the newest messages across EVERY
// account, filtered by folder ("inbox" = delivered to at least one
// existing recipient, "sent" = sent by an existing account, "all" =
// everything), newest-first, capped at limit. One reverse cursor pass
// over the global message bucket replaces the 2-requests-per-account
// fan-out that saturated the server on the admin Mail page.
func (s *Store) ReadAllAccountsMessages(folder string, limit int) ([]MessageSummary, int, error) {
	if limit <= 0 {
		limit = 50
	}
	folder = strings.ToLower(strings.TrimSpace(folder))
	if folder == "" {
		folder = "all"
	}
	var out []MessageSummary
	total := 0
	err := s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMessages)
		ub := tx.Bucket(bUnread)
		ab := tx.Bucket(bAccounts)
		uuidCache := map[string]string{} // address -> uuid, resolved lazily
		resolve := func(addr string) string {
			a := strings.ToLower(strings.TrimSpace(addr))
			if u, ok := uuidCache[a]; ok {
				return u
			}
			u := ""
			if ab != nil {
				if val := ab.Get([]byte(a)); val != nil {
					var acc Account
					if json.Unmarshal(val, &acc) == nil {
						u = acc.UUID
					}
				}
			}
			uuidCache[a] = u
			return u
		}
		c := mb.Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			var m Message
			if json.Unmarshal(v, &m) != nil {
				continue // corrupt record
			}
			switch folder {
			case "inbox":
				if len(m.To) == 0 {
					continue
				}
			case "sent":
				if m.From == "" {
					continue
				}
			}
			total++
			if len(out) < limit {
				ms := summarize(m)
				// Unread in the all-accounts view = ANY recipient (To+CC)
				// still has an unread marker — the admin is not a recipient,
				// so the meaningful signal is whether an owner has read it
				// (superior report: fresh mail showed as read here while
				// the owner-side state was unread).
				if ub != nil {
					for _, rcpt := range append(append([]string{}, m.To...), m.CC...) {
						if u := resolve(rcpt); u != "" && ub.Get(indexKey(u, m.ID)) != nil {
							ms.Unread = true
							break
						}
					}
				}
				out = append(out, ms)
			}
		}
		return nil
	})
	return out, total, err
}

// ReadInbox returns up to limit most-recent inbox messages for the account
// with the given address. limit<=0 defaults to 20. Unread status reflects the
// inbox owner's view (the account itself).
func (s *Store) ReadInbox(address string, limit int) ([]MessageSummary, error) {
	return s.ReadInboxPaged(address, limit, 0)
}

// ThreadEntry is one side of a bilateral conversation.
type ThreadEntry struct {
	MessageSummary
	Dir string `json:"dir"` // "in" = received from peer, "out" = sent to peer
}

// ReadThread returns the bilateral conversation between the account and one
// peer: messages the account sent whose recipients include the peer ("out")
// plus messages the account received from the peer ("in"), newest first.
// Case-insensitive address match. Full index scan per side — fine at current
// scale (thousands); revisit with a peer index if volume grows.
func (s *Store) ReadThread(address, peer string, limit, offset int) ([]ThreadEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	acc, err := s.GetAccount(address)
	if err != nil {
		return nil, err
	}
	// Scan a generous window per side, then merge. The window must exceed
	// limit+offset so pagination reaches deep-enough entries on one side.
	window := limit + offset + 200
	inbox, err := s.readIndex(bInbox, acc.UUID, acc.UUID, window, 0)
	if err != nil {
		return nil, err
	}
	sent, err := s.readIndex(bSent, acc.UUID, acc.UUID, window, 0)
	if err != nil {
		return nil, err
	}
	var merged []ThreadEntry
	for _, m := range inbox {
		if strings.EqualFold(m.From, peer) {
			e := ThreadEntry{MessageSummary: m, Dir: "in"}
			merged = append(merged, e)
		}
	}
	for _, m := range sent {
		for _, r := range m.To {
			if strings.EqualFold(r, peer) {
				e := ThreadEntry{MessageSummary: m, Dir: "out"}
				merged = append(merged, e)
				break
			}
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].ReceivedAt > merged[j].ReceivedAt
	})
	if offset >= len(merged) {
		return []ThreadEntry{}, nil
	}
	merged = merged[offset:]
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged, nil
}

// ReadInboxPaged returns up to limit inbox messages for the account, skipping
// the first offset (newest-first). offset<0 is treated as 0.
func (s *Store) ReadInboxPaged(address string, limit, offset int) ([]MessageSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	acc, err := s.GetAccount(address)
	if err != nil {
		return nil, err
	}
	return s.readIndex(bInbox, acc.UUID, acc.UUID, limit, offset)
}

// CountInbox returns the total number of messages in the account's inbox
// (independent of paging). Used to compute total pages for the Inbox UI.
func (s *Store) CountInbox(address string) (int, error) {
	acc, err := s.GetAccount(address)
	if err != nil {
		return 0, err
	}
	prefix := indexKey(acc.UUID, "")
	prefixStr := string(prefix)
	count := 0
	err = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bInbox)
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, _ := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), prefixStr); k, _ = c.Next() {
			count++
		}
		return nil
	})
	return count, err
}

// CountSent returns the total number of messages in the account's sent
// index (independent of any page limit). Mirrors CountInbox.
func (s *Store) CountSent(address string) (int, error) {
	acc, err := s.GetAccount(address)
	if err != nil {
		return 0, err
	}
	prefix := indexKey(acc.UUID, "")
	prefixStr := string(prefix)
	count := 0
	err = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bSent)
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, _ := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), prefixStr); k, _ = c.Next() {
			count++
		}
		return nil
	})
	return count, err
}

// ReadSent returns the account's sent messages (used by the admin UI later).
func (s *Store) ReadSent(address string, limit int) ([]MessageSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	acc, err := s.GetAccount(address)
	if err != nil {
		return nil, err
	}
	return s.readIndex(bSent, acc.UUID, acc.UUID, limit, 0)
}

// ListContacts returns the deduplicated, sorted list of addresses the account
// has exchanged mail with: senders in the account's inbox plus recipients in
// the account's sent messages. The account's own address is excluded. Scans up
// to the 500 most recent inbox and sent messages each to bound the work.
func (s *Store) ListContacts(address string) ([]string, error) {
	acc, err := s.GetAccount(address)
	if err != nil {
		return nil, err
	}
	const scan = 500
	inbox, err := s.readIndex(bInbox, acc.UUID, acc.UUID, scan, 0)
	if err != nil {
		return nil, err
	}
	sent, err := s.readIndex(bSent, acc.UUID, acc.UUID, scan, 0)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{})
	addAddrs := func(raw string) {
		for _, a := range normalizeContactAddrs(raw) {
			if a != address {
				set[a] = struct{}{}
			}
		}
	}
	for _, m := range inbox {
		addAddrs(m.From)
	}
	for _, m := range sent {
		for _, r := range m.To {
			addAddrs(r)
		}
	}
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out, nil
}

// normalizeContactAddrs cleans one raw address pulled from a message's From/To
// and returns zero or more clean addresses. Historically some messages stored a
// JSON-array-shaped string (e.g. `["admin@x.local"]`) inside the To slice,
// which leaked as a bogus contact. If raw parses as a JSON string array, its
// elements are returned (recursively, so a nested array-string also cleans up).
// Otherwise the value is kept only if it looks like a single mail address
// (contains "@"). Empty/whitespace/invalid entries are dropped.
func normalizeContactAddrs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(raw), &arr); err == nil {
			var out []string
			for _, e := range arr {
				out = append(out, normalizeContactAddrs(e)...)
			}
			return out
		}
		return nil // array-shaped but not valid JSON: drop the bogus entry
	}
	if !strings.Contains(raw, "@") {
		return nil
	}
	return []string{raw}
}

// ReadInboxAsViewer is like ReadInbox but the unread status reflects the
// viewer's perspective (e.g. admin viewing alice's inbox sees admin's own
// unread state, which is always "read" since admin is not a recipient).
// Used by admin endpoints so admin views don't mutate the owner's unread state.
func (s *Store) ReadInboxAsViewer(owner, viewer string, limit int) ([]MessageSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	ownerAcc, err := s.GetAccount(owner)
	if err != nil {
		return nil, err
	}
	viewerAcc, err := s.GetAccount(viewer)
	if err != nil {
		// viewer unknown: fall back to owner's view
		return s.readIndex(bInbox, ownerAcc.UUID, ownerAcc.UUID, limit, 0)
	}
	return s.readIndex(bInbox, ownerAcc.UUID, viewerAcc.UUID, limit, 0)
}

// readIndex scans an index bucket (inbox or sent) for the given account UUID,
// newest first, and resolves each referenced message to a summary. indexOwner
// selects whose inbox/sent to read; viewerUuid selects whose unread state to
// report (often the same, but different for admin viewing another's inbox).
func (s *Store) readIndex(bucket []byte, indexOwner, viewerUuid string, limit, offset int) ([]MessageSummary, error) {
	prefix := indexKey(indexOwner, "")
	prefixStr := string(prefix)
	var ids []string
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		if b == nil {
			return nil // bucket missing — treat as empty index
		}
		c := b.Cursor()
		for k, _ := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), prefixStr); k, _ = c.Next() {
			ids = append(ids, string(k[len(prefix):]))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Newest-first: bbolt cursor returns ascending ULID order; reverse so the
	// newest message is first.
	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}
	// Apply offset pagination (skip the first `offset` newest, then take limit).
	if offset < 0 {
		offset = 0
	}
	if offset >= len(ids) {
		return []MessageSummary{}, nil
	}
	ids = ids[offset:]
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
			// Unread is per-viewer: check if this viewer's uuid+ulid key exists
			// in the unread bucket. Absent = read (default).
			if ub != nil {
				ms.Unread = ub.Get(indexKey(viewerUuid, id)) != nil
			}
			out = append(out, ms)
		}
		return nil
	})
	return out, err
}

// GetMessage returns the full body of a single message, provided it is
// reachable by the requesting account (in their inbox OR sent).
func (s *Store) GetMessage(requester, messageID string) (*Message, error) {
	acc, err := s.GetAccount(requester)
	if err != nil {
		return nil, err
	}
	// Verify the requester has a reference to this message.
	visible := false
	prefix := indexKey(acc.UUID, "")
	err = s.db.View(func(tx *bolt.Tx) error {
		for _, bucket := range [][]byte{bInbox, bSent} {
			c := tx.Bucket(bucket).Cursor()
			for k, _ := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), string(prefix)); k, _ = c.Next() {
				if string(k[len(prefix):]) == messageID {
					visible = true
					return nil
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, ErrMessageNotFound
	}
	// Load the body.
	var msg Message
	err = s.db.View(func(tx *bolt.Tx) error {
		val := tx.Bucket(bMessages).Get([]byte(messageID))
		if val == nil {
			return ErrMessageNotFound
		}
		return json.Unmarshal(val, &msg)
	})
	if err != nil {
		return nil, err
	}
	// Mark as read for this viewer (best-effort: ignore error, the message is
	// still returned even if the unread marker can't be cleared).
	_ = s.MarkRead(acc.UUID, messageID)
	return &msg, nil
}

// MarkRead removes the unread marker for (uuid, messageID). Best-effort: a
// missing key is a no-op (already read). Fast path: a cheap View first checks
// whether the marker exists; only if it does (truly unread) do we pay for an
// Update. This avoids unnecessary write transactions (bbolt write txns are
// exclusive and serialize all access) on repeated reads of already-read mail.
func (s *Store) MarkRead(uuidHex, messageID string) error {
	key := indexKey(uuidHex, messageID)
	// Fast path: if already read, skip the write transaction entirely.
	needsWrite := false
	if err := s.db.View(func(tx *bolt.Tx) error {
		ub := tx.Bucket(bUnread)
		if ub == nil {
			return nil
		}
		if ub.Get(key) != nil {
			needsWrite = true
		}
		return nil
	}); err != nil {
		return err
	}
	if !needsWrite {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		ub := tx.Bucket(bUnread)
		if ub == nil {
			return nil
		}
		if err := ub.Delete(key); err != nil {
			return err
		}
		return touchLastRead(tx, uuidHex, s.now().Unix())
	})
}

// GetMessageAdmin returns the full body of a message by ID with no requester
// check. Intended only for the admin endpoints; regular account access must go
// through GetMessage (which enforces the inbox/sent reference check).
func (s *Store) GetMessageAdmin(messageID string) (*Message, error) {
	var msg Message
	err := s.db.View(func(tx *bolt.Tx) error {
		val := tx.Bucket(bMessages).Get([]byte(messageID))
		if val == nil {
			return ErrMessageNotFound
		}
		return json.Unmarshal(val, &msg)
	})
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

// DayCount is one day's message count for the portal's 7-day chart.
type DayCount struct {
	Date  string `json:"date"` // "2006-01-02" (UTC)
	Count int    `json:"count"`
}

// GrowthChartDays is how many whole UTC calendar days the growth chart
// array carries: the panel slices by viewport (7/10/14), the guest portal
// keeps a fixed 7 — both from this one array.
const GrowthChartDays = 14

// Growth counts messages in standard age buckets. It powers the guest
// portal's activity stats; the handler caches the result so the underlying
// scan runs at most once per cache interval even under heavy traffic.
type Growth struct {
	Today int        `json:"today"` // since UTC midnight
	Week  int        `json:"week"`  // last 7 days rolling (inclusive of today)
	Month int        `json:"month"` // last 30 days rolling (inclusive of today)
	Total int        `json:"total"` // all time
	Days  []DayCount `json:"days"`  // last GrowthChartDays UTC calendar days, oldest first
}

// MessageGrowth counts messages into age buckets relative to now. One pass
// over bMessages; a corrupt record is skipped rather than failing the scan.
// Days covers GrowthChartDays whole UTC calendar days ending today (so
// Days[len-1].Count == Today); Week/Month are rolling windows and may
// differ slightly from the calendar-day sums.
func (s *Store) MessageGrowth(now time.Time) (Growth, error) {
	var g Growth
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	chartFloor := dayStart.AddDate(0, 0, -(GrowthChartDays - 1)) // first chart day, UTC midnight
	weekStart := now.Unix() - 7*24*3600
	monthStart := now.Unix() - 30*24*3600
	days := make([]DayCount, GrowthChartDays)
	for i := range days {
		days[i] = DayCount{Date: chartFloor.AddDate(0, 0, i).Format("2006-01-02")}
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bMessages).ForEach(func(_, v []byte) error {
			var m Message
			if err := json.Unmarshal(v, &m); err != nil {
				return nil // skip corrupt records, keep counting
			}
			g.Total++
			switch {
			case m.ReceivedAt >= dayStart.Unix():
				g.Today++
				g.Week++
				g.Month++
			case m.ReceivedAt >= weekStart:
				g.Week++
				g.Month++
			case m.ReceivedAt >= monthStart:
				g.Month++
			}
			if m.ReceivedAt >= chartFloor.Unix() {
				// UTC has no DST, so integer day arithmetic on the timestamp
				// is exact. Clamp guards corrupt far-future timestamps.
				idx := int((m.ReceivedAt - chartFloor.Unix()) / 86400)
				if idx >= 0 && idx < len(days) {
					days[idx].Count++
				}
			}
			return nil
		})
	})
	g.Days = days
	return g, err
}

// MyDayCount is one day of an account's personal in/out counts (for the
// panel's My activity row; same shape as the portal growth days so the
// frontend can reuse its chart logic).
type MyDayCount struct {
	Date string `json:"date"`
	In   int    `json:"in"`  // received that day
	Out  int    `json:"out"` // sent that day
}

// MyGrowth is an account's recent in/out activity. Week covers the same 7
// UTC calendar days as Days (their sum), for consistency with the chart.
type MyGrowth struct {
	TodayIn  int          `json:"today_in"`
	TodayOut int          `json:"today_out"`
	WeekIn   int          `json:"week_in"`
	WeekOut  int          `json:"week_out"`
	Days     []MyDayCount `json:"days"` // 7 entries, oldest first
}

// MyGrowthStats scans the account's inbox and sent indexes and buckets the
// referenced messages' timestamps into today/week scalars plus a 7-day
// array. Index entries carry no timestamps, so each referenced message is
// fetched by ID (bolt point reads). Corrupt or missing records are skipped.
func (s *Store) MyGrowthStats(address string, now time.Time) (MyGrowth, error) {
	acc, err := s.GetAccount(address)
	if err != nil {
		return MyGrowth{}, err
	}
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	weekFloor := dayStart.AddDate(0, 0, -6)
	g := MyGrowth{Days: make([]MyDayCount, 7)}
	for i := range g.Days {
		g.Days[i] = MyDayCount{Date: weekFloor.AddDate(0, 0, i).Format("2006-01-02")}
	}
	err = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMessages)
		prefix := indexKey(acc.UUID, "")
		prefixStr := string(prefix)
		scan := func(bucket []byte, isOut bool) {
			ib := tx.Bucket(bucket)
			if ib == nil {
				return
			}
			c := ib.Cursor()
			for k, _ := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), prefixStr); k, _ = c.Next() {
				raw := mb.Get(k[len(prefix):])
				if raw == nil {
					continue // index points at a missing record; skip
				}
				var m Message
				if json.Unmarshal(raw, &m) != nil {
					continue
				}
				ts := m.ReceivedAt
				if ts < weekFloor.Unix() {
					continue // outside the 7-day window
				}
				idx := int((ts - weekFloor.Unix()) / 86400)
				if idx < 0 || idx > 6 {
					continue // corrupt far-future timestamp guard
				}
				if isOut {
					g.Days[idx].Out++
					g.WeekOut++
					if idx == 6 {
						g.TodayOut++
					}
				} else {
					g.Days[idx].In++
					g.WeekIn++
					if idx == 6 {
						g.TodayIn++
					}
				}
			}
		}
		scan(bInbox, false)
		scan(bSent, true)
		return nil
	})
	return g, err
}

// getAccountInTx reads an account inside an existing transaction.
// Case-insensitive like GetAccount (275d0ba fixed the public lookup but
// missed this transactional twin — sends to uppercase address variants
// silently found no recipients). Account keys are always lowercase.
func getAccountInTx(tx *bolt.Tx, address string) (*Account, error) {
	val := tx.Bucket(bAccounts).Get([]byte(strings.ToLower(address)))
	if val == nil {
		return nil, ErrAccountNotFound
	}
	var acc Account
	if err := json.Unmarshal(val, &acc); err != nil {
		return nil, err
	}
	return &acc, nil
}

// summarize projects a Message to a MessageSummary with a short body preview.
func summarize(m Message) MessageSummary {
	preview := m.Body
	// Truncate by RUNE count, not byte count, to avoid cutting a multibyte char
	// mid-sequence AND to avoid the slice-bounds panic when byte length > 100
	// but rune count < 100 (e.g. 72 Chinese chars = 216 bytes, but only 72 runes).
	if r := []rune(preview); len(r) > 100 {
		preview = string(r[:100])
	}
	return MessageSummary{
		ID:         m.ID,
		From:       m.From,
		To:         m.To,
		CC:         m.CC,
		InReplyTo:  m.InReplyTo,
		Files:      len(m.Attachments),
		Subject:    m.Subject,
		Preview:    preview,
		ReceivedAt: m.ReceivedAt,
	}
}

// CountUnread returns the total number of unread messages across the
// account's WHOLE inbox (not the current page). The inbox handler exposes
// this as unread_count for the nav badge — it must be limit-independent:
// the page-scoped count made the badge vanish whenever the unread mail
// fell outside the requested page (caught live by mrf2000/Felix).
func (s *Store) CountUnread(address string) (int, error) {
	acc, err := s.GetAccount(address)
	if err != nil {
		return 0, err
	}
	prefix := indexKey(acc.UUID, "")
	prefixStr := string(prefix)
	count := 0
	err = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bUnread)
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, _ := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), prefixStr); k, _ = c.Next() {
			count++
		}
		return nil
	})
	return count, err
}

// MarkAllRead clears every unread marker in the account's inbox in one
// transaction and returns how many were cleared. The bulk companion of
// MarkRead — the panel's "mark all as read" button calls it so a deep
// backlog of old unread can be dismissed without paging through
// /api/message per mail (the nav badge then drops to zero immediately).
func (s *Store) MarkAllRead(address string) (int, error) {
	acc, err := s.GetAccount(address)
	if err != nil {
		return 0, err
	}
	prefix := indexKey(acc.UUID, "")
	prefixStr := string(prefix)
	// Collect first (cursor over bUnread), delete in the same Update —
	// bbolt forbids Put/Delete while iterating a cursor.
	var keys [][]byte
	err = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bUnread)
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, _ := c.Seek(prefix); k != nil && strings.HasPrefix(string(k), prefixStr); k, _ = c.Next() {
			keys = append(keys, append([]byte(nil), k...))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bUnread)
		for _, k := range keys {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		if err := touchLastRead(tx, acc.UUID, s.now().Unix()); err != nil {
			return err
		}
		return nil
	})
	return len(keys), err
}

// lastReadKey is the bMeta key under which an account's most recent read
// event is stamped (liveness "weak evidence"). Since the v0.6.6.1-era
// contract addendum this covers BOTH unread->read transitions and the
// owner's own inbox pulls.
func lastReadKey(uuidHex string) []byte {
	return []byte("lastread:" + uuidHex)
}

// touchLastRead stamps the account's latest read event. Called from the
// same transaction that clears an unread flag, so the stamp is exactly as
// durable as the read itself.
func touchLastRead(tx *bolt.Tx, uuidHex string, at int64) error {
	mb := tx.Bucket(bMeta)
	if mb == nil {
		return nil
	}
	return mb.Put(lastReadKey(uuidHex), []byte(fmt.Sprintf("%d", at)))
}

// TouchLastReadPull stamps a read event for the owner's own inbox pull
// (contract addendum, superior ruling via Felix 01M0VFNX): a watcher that
// polls GET /api/inbox is exactly the automation the yellow tier wants to
// surface, while pure unread->read transitions alone left it almost
// unreachable. Separate best-effort transaction — the pull itself is a
// read, so the stamp lands right after it serves. Superior browsing via
// the subs path must NOT call this (Q2 semantics: viewing must not
// pollute the viewed account's liveness).
func (s *Store) TouchLastReadPull(address string) {
	acc, err := s.GetAccount(address)
	if err != nil {
		return
	}
	_ = s.db.Update(func(tx *bolt.Tx) error {
		return touchLastRead(tx, acc.UUID, s.now().Unix())
	})
}

// LastReadAt returns the account's most recent read event (unix seconds;
// 0 = never): an unread->read transition or the owner's own inbox pull,
// whichever happened last. Sending is the strong liveness signal and lives
// on last_out_at.
func (s *Store) LastReadAt(uuidHex string) int64 {
	var out int64
	_ = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bMeta)
		if mb == nil {
			return nil
		}
		if raw := mb.Get(lastReadKey(uuidHex)); raw != nil {
			out, _ = strconv.ParseInt(string(raw), 10, 64)
		}
		return nil
	})
	return out
}
