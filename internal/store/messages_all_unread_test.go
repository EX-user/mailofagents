package store

import "testing"

// TestReadAllAccountsMessagesUnread (superior report 2026-08-27): the
// all-accounts admin browse view used to show every message as read —
// summarize() never set Unread. It must reflect ANY-recipient unread:
// ● while an owner has not read the letter, blank once all owners have.
func TestReadAllAccountsMessagesUnread(t *testing.T) {
	s := newFilesStore(t)
	if _, err := s.Send("a@t", "a", []string{"b@t"}, nil, "hello", "body", ""); err != nil {
		t.Fatalf("send: %v", err)
	}
	msgs, _, err := s.ReadAllAccountsMessages("all", 10)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Subject != "hello" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
	if !msgs[0].Unread {
		t.Errorf("fresh recipient-unread message shows as read in all-accounts view")
	}
	// Owner reads it -> the dot must go away.
	acc, err := s.GetAccount("b@t")
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if err := s.MarkRead(acc.UUID, msgs[0].ID); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	msgs, _, err = s.ReadAllAccountsMessages("all", 10)
	if err != nil {
		t.Fatalf("re-read all: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Unread {
		t.Errorf("read message still flagged unread: %+v", msgs)
	}
}

// A cc'd-only recipient also counts as an owner for the any-recipient rule.
func TestReadAllAccountsMessagesUnreadCC(t *testing.T) {
	s := newFilesStore(t)
	if _, err := s.Send("a@t", "a", []string{"b@t"}, []string{"c@t"}, "ccsubj", "body", ""); err != nil {
		t.Fatalf("send cc: %v", err)
	}
	msgs, _, err := s.ReadAllAccountsMessages("all", 10)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	found := false
	for _, m := range msgs {
		if m.Subject == "ccsubj" {
			found = true
			if !m.Unread {
				t.Errorf("cc recipient unread not reflected")
			}
		}
	}
	if !found {
		t.Fatalf("cc message missing from all-accounts view")
	}
}
