package store

import (
	"encoding/json"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// newThreadsStore opens a store with the fixture accounts.
func newThreadsStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.db.Close() })
	for _, n := range []string{"a", "b", "c", "boss"} {
		if _, err := s.CreateAccount(n, "t", false); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
	}
	return s
}

// putThreadMsg writes a message record directly (full control over
// InReplyTo, including dangling refs and cycles that validated sends cannot
// produce).
func putThreadMsg(t *testing.T, s *Store, id, from string, to []string, inReplyTo string, at int64) {
	t.Helper()
	m := Message{ID: id, From: from, To: to, InReplyTo: inReplyTo, Subject: "s", Body: "x", ReceivedAt: at}
	val, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bMessages).Put([]byte(m.ID), val); err != nil {
			return err
		}
		// Index refs make the message visible through the normal
		// inbox/sent scan (same delivery shape the Send paths write).
		sender, err := getAccountInTx(tx, from)
		if err != nil {
			return err
		}
		if err := tx.Bucket(bSent).Put(indexKey(sender.UUID, m.ID), nil); err != nil {
			return err
		}
		for _, rcpt := range to {
			acc, err := getAccountInTx(tx, rcpt)
			if err != nil {
				continue
			}
			if err := tx.Bucket(bInbox).Put(indexKey(acc.UUID, m.ID), nil); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("put msg: %v", err)
	}
}

// TestThreadComponentsHappyPath pins the core derivation: one maximal
// component across generations and forks, earliest = root, index fields,
// min_count filter, pagination total, member resolution.
func TestThreadComponentsHappyPath(t *testing.T) {
	s := newThreadsStore(t)
	// a -> b root; b -> a reply; a -> b reply-to-reply; b -> a fork on root.
	r1, err := s.Send("a@t", "a", []string{"b@t"}, nil, "root", "x", "")
	if err != nil {
		t.Fatalf("send root: %v", err)
	}
	r2, err := s.Send("b@t", "b", []string{"a@t"}, nil, "re: root", "x", r1.MessageID)
	if err != nil {
		t.Fatalf("send reply: %v", err)
	}
	r3, err := s.Send("a@t", "a", []string{"b@t"}, nil, "re: re: root", "x", r2.MessageID)
	if err != nil {
		t.Fatalf("send reply2: %v", err)
	}
	if _, err := s.Send("b@t", "b", []string{"a@t"}, nil, "fork", "x", r1.MessageID); err != nil {
		t.Fatalf("send fork: %v", err)
	}
	// Unrelated singleton.
	soloRes, err := s.Send("a@t", "a", []string{"c@t"}, nil, "solo", "x", "")
	if err != nil {
		t.Fatalf("send solo: %v", err)
	}
	soloID := soloRes.MessageID

	topics, total, err := s.Threads("a@t", 50, 0, 2)
	if err != nil {
		t.Fatalf("threads: %v", err)
	}
	if total != 1 || len(topics) != 1 {
		t.Fatalf("threads total=%d len=%d, want the single 4-message component", total, len(topics))
	}
	topic := topics[0]
	if topic.RootID != r1.MessageID || topic.Count != 4 || topic.Subject != "root" {
		t.Fatalf("topic = %+v", topic)
	}
	if len(topic.Participants) != 2 || topic.Participants[0] != "a@t" || topic.Participants[1] != "b@t" {
		t.Fatalf("participants = %v (want first-appearance a,b)", topic.Participants)
	}

	// min_count=1 exposes the singleton too.
	_, totalAll, err := s.Threads("a@t", 50, 0, 1)
	if err != nil || totalAll != 2 {
		t.Fatalf("min_count=1 total=%d err=%v, want 2", totalAll, err)
	}

	// Component view via ANY member resolves to the whole block, root = earliest.
	view, err := s.ThreadByRoot("a@t", r3.MessageID)
	if err != nil {
		t.Fatalf("by member: %v", err)
	}
	if view.Root != r1.MessageID || view.Count != 4 {
		t.Fatalf("view root=%s count=%d", view.Root, view.Count)
	}
	want := map[string]bool{r1.MessageID: true, r2.MessageID: true, r3.MessageID: true}
	got := map[string]bool{}
	for i, m := range view.Messages {
		got[m.ID] = true
		if i > 0 && view.Messages[i-1].ID > m.ID {
			t.Fatalf("view not ID-ascending at %d", i)
		}
	}
	if len(got) != 4 {
		t.Fatalf("view members = %v", got)
	}
	_ = want
	var replySum *MessageSummary
	for i := range view.Messages {
		if view.Messages[i].ID == r2.MessageID {
			replySum = &view.Messages[i]
		}
	}
	if replySum == nil || replySum.InReplyTo != r1.MessageID {
		t.Fatalf("reply summary missing/unchecked: %+v", replySum)
	}

	// Addendum-2 equivalence: mid/leaf member access == root access.
	fromRoot, err := s.ThreadByRoot("a@t", r1.MessageID)
	if err != nil {
		t.Fatalf("by root: %v", err)
	}
	if fromRoot.Root != view.Root || fromRoot.Count != view.Count || len(fromRoot.Messages) != len(view.Messages) {
		t.Fatalf("member access != root access: %+v vs %+v", fromRoot, view)
	}
	for i := range fromRoot.Messages {
		if fromRoot.Messages[i].ID != view.Messages[i].ID {
			t.Fatalf("member/root views diverge at %d", i)
		}
	}
	// A singleton (no refs either way) is its own one-node tree.
	solo2, err := s.ThreadByRoot("a@t", soloID)
	if err != nil || solo2.Count != 1 || solo2.Root != soloID {
		t.Fatalf("singleton view = %+v err=%v", solo2, err)
	}

	// Unknown / invisible id.
	if _, err := s.ThreadByRoot("a@t", "01ARZ3NDEKTSV4RRFFQ69G5FAV"); err != ErrNoSuchThread {
		t.Fatalf("unknown id err=%v, want ErrNoSuchThread", err)
	}
	if _, err := s.ThreadByRoot("c@t", r1.MessageID); err != ErrNoSuchThread {
		t.Fatalf("invisible id err=%v, want ErrNoSuchThread (c never saw the thread)", err)
	}
}

// TestThreadDanglingAndCycle pins the two defensive clauses: dangling refs
// degrade to no-edge; cycles are union-safe (no hang, one component).
func TestThreadDanglingAndCycle(t *testing.T) {
	s := newThreadsStore(t)
	// Dangling: parent id does not exist.
	putThreadMsg(t, s, "01AAAAAAAAAAAAAAAAAAAAAAAA", "a@t", []string{"b@t"}, "01ZZZZZZZZZZZZZZZZZZZZZZZZ", 1000)
	// Cycle: X -> Y, Y -> X (validated sends cannot produce this).
	putThreadMsg(t, s, "01BBBBBBBBBBBBBBBBBBBBBBBB", "a@t", []string{"b@t"}, "01CCCCCCCCCCCCCCCCCCCCCCCC", 2000)
	putThreadMsg(t, s, "01CCCCCCCCCCCCCCCCCCCCCCCC", "a@t", []string{"b@t"}, "01BBBBBBBBBBBBBBBBBBBBBBBB", 3000)

	topics, total, err := s.Threads("a@t", 50, 0, 1)
	if err != nil {
		t.Fatalf("threads: %v", err)
	}
	if total != 2 {
		t.Fatalf("total=%d, want 2 (dangling singleton + cycle component)", total)
	}
	var cycle, dangling *ThreadTopic
	for i := range topics {
		if topics[i].Count == 2 {
			cycle = &topics[i]
		} else if topics[i].Count == 1 {
			dangling = &topics[i]
		}
	}
	if cycle == nil || dangling == nil {
		t.Fatalf("topics = %+v", topics)
	}
	if cycle.RootID != "01BBBBBBBBBBBBBBBBBBBBBBBB" {
		t.Fatalf("cycle root = %s, want the earlier ULID", cycle.RootID)
	}
	if dangling.RootID != "01AAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("dangling degraded to non-singleton root: %+v", dangling)
	}
}

// TestThreadVisibilitySameSource pins the contract clause that threads and
// the mgmt overview share ONE visibility rule: a declared subordinate's
// mail merges read-only into the superior's visible set.
func TestThreadVisibilitySameSource(t *testing.T) {
	s := newThreadsStore(t)
	if err := s.DeclareSubordinate("boss@t", "b@t"); err != nil {
		t.Fatalf("declare: %v", err)
	}
	r1, err := s.Send("b@t", "b", []string{"a@t"}, nil, "sub thread", "x", "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := s.Send("a@t", "a", []string{"b@t"}, nil, "re: sub thread", "x", r1.MessageID); err != nil {
		t.Fatalf("send reply: %v", err)
	}
	// The boss was never a participant yet sees b's component.
	view, err := s.ThreadByRoot("boss@t", r1.MessageID)
	if err != nil {
		t.Fatalf("boss view: %v", err)
	}
	if view.Count != 2 {
		t.Fatalf("boss sees count=%d, want 2 (subordinate read-only merge)", view.Count)
	}
	// a (a participant, but not a party to any declaration) sees it too.
	if _, err := s.ThreadByRoot("a@t", r1.MessageID); err != nil {
		t.Fatalf("participant view: %v", err)
	}
	// c sees nothing.
	if _, err := s.ThreadByRoot("c@t", r1.MessageID); err != ErrNoSuchThread {
		t.Fatalf("outsider err=%v, want ErrNoSuchThread", err)
	}
}

// TestSendParentExistence pins the write-side validation: a nonexistent
// parent fails the whole send; a valid one round-trips into summaries.
func TestSendParentExistence(t *testing.T) {
	s := newThreadsStore(t)
	if _, err := s.Send("a@t", "a", []string{"b@t"}, nil, "bad", "x", "01ZZZZZZZZZZZZZZZZZZZZZZZZ"); err == nil {
		t.Fatal("nonexistent parent accepted")
	}
	r1, err := s.Send("a@t", "a", []string{"b@t"}, nil, "root", "x", "")
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if _, err := s.Send("b@t", "b", []string{"a@t"}, nil, "re", "x", r1.MessageID); err != nil {
		t.Fatalf("reply: %v", err)
	}
	inbox, err := s.ReadInbox("a@t", 10)
	if err != nil || len(inbox) != 1 {
		t.Fatalf("inbox len=%d err=%v", len(inbox), err)
	}
	if inbox[0].InReplyTo != r1.MessageID {
		t.Fatalf("inbox summary in_reply_to = %q, want %q", inbox[0].InReplyTo, r1.MessageID)
	}
}
