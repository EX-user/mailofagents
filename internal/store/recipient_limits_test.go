package store

import "testing"

// TestRecipientLimitsStore covers the bloat-lever persistence: defaults are
// unlimited (0) on fresh and legacy records, the setter round-trips both
// caps, 0 lifts a cap again, negatives are refused, and a missing account
// is the canonical sentinel.
func TestRecipientLimitsStore(t *testing.T) {
	s := newTokensStore(t)

	r, err := s.CreateAccountWithPassword("worker", "t", false, "pw-one-2-3")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if acc, err := s.GetAccount(r.Address); err != nil || acc.MaxRecipients != 0 || acc.MaxCC != 0 {
		t.Fatalf("fresh account = %d/%d (%v), want 0/0", acc.MaxRecipients, acc.MaxCC, err)
	}

	if err := s.SetRecipientLimits(r.Address, 3, 1); err != nil {
		t.Fatalf("set: %v", err)
	}
	acc, err := s.GetAccount(r.Address)
	if err != nil || acc.MaxRecipients != 3 || acc.MaxCC != 1 {
		t.Fatalf("after set = %d/%d (%v), want 3/1", acc.MaxRecipients, acc.MaxCC, err)
	}

	if err := s.SetRecipientLimits(r.Address, 0, 0); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if acc, _ := s.GetAccount(r.Address); acc.MaxRecipients != 0 || acc.MaxCC != 0 {
		t.Fatalf("after clear = %d/%d, want 0/0", acc.MaxRecipients, acc.MaxCC)
	}

	if err := s.SetRecipientLimits(r.Address, -1, 0); err == nil {
		t.Fatal("negative maxTo accepted, want error")
	}
	if err := s.SetRecipientLimits(r.Address, 0, -5); err == nil {
		t.Fatal("negative maxCC accepted, want error")
	}
	if err := s.SetRecipientLimits("nobody@t", 1, 1); err != ErrAccountNotFound {
		t.Fatalf("missing account = %v, want ErrAccountNotFound", err)
	}
}
