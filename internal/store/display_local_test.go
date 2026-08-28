package store

import "testing"

// TestDisplayLocalLifecycle covers V06_DISPLAY_ADDRESS: registration keeps
// the caller's casing as display, the setter enforces the case invariant,
// and clearing falls back to key display.
func TestDisplayLocalLifecycle(t *testing.T) {
	s := newTokensStore(t)

	// Registration with mixed case preserves it as display.
	if _, err := s.CreateAccountWithPassword("PoP", "t", false, "pw-one-2-3"); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetDisplayLocal("pop@t")
	if err != nil || got != "PoP" {
		t.Fatalf("registration display = %q (%v), want PoP", got, err)
	}

	// All-lowercase registration leaves display unset.
	if _, err := s.CreateAccountWithPassword("flat", "t", false, "pw-one-2-3"); err != nil {
		t.Fatalf("create flat: %v", err)
	}
	if got, _ := s.GetDisplayLocal("flat@t"); got != "" {
		t.Fatalf("flat registration display = %q, want empty", got)
	}

	// Setter: valid change, invalid casing rejected, foreign key rejected.
	if err := s.SetDisplayLocal("flat@t", "Flat"); err != nil {
		t.Fatalf("set Flat: %v", err)
	}
	if got, _ := s.GetDisplayLocal("flat@t"); got != "Flat" {
		t.Fatalf("after set = %q, want Flat", got)
	}
	if err := s.SetDisplayLocal("flat@t", "Wrong"); err == nil {
		t.Fatal("display lowercasing to another key must be rejected")
	}
	if err := s.SetDisplayLocal("flat@t", "Fl@t!"); err == nil {
		t.Fatal("bad runes must be rejected")
	}
	if err := s.SetDisplayLocal("flat@t", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got, _ := s.GetDisplayLocal("flat@t"); got != "" {
		t.Fatalf("after clear = %q, want empty", got)
	}
	// The other account's display is untouched by flat's churn.
	if got, _ := s.GetDisplayLocal("pop@t"); got != "PoP" {
		t.Fatalf("pop display corrupted: %q", got)
	}
}
