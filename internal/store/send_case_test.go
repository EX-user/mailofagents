package store

import "testing"

// Regression for the superior's case-variant report (alice 01M13YGD): sends
// to an uppercase address variant must resolve the lowercase-stored account
// (275d0ba made GetAccount case-insensitive; the in-transaction send path
// must match).
func TestSendToUppercaseRecipient(t *testing.T) {
	s := newTokensStore(t) // registers alice@t

	res, err := s.Send("alice@t", "Alice", []string{"ALICE@T"}, nil, "case", "body", "")
	if err != nil {
		t.Fatalf("send to uppercase variant failed: %v", err)
	}
	_ = res
	msgs, err := s.ReadInboxPaged("alice@t", 10, 0)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("uppercase recipient delivery missing: %d msgs (%v)", len(msgs), err)
	}
}
