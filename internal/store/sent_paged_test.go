package store

import "testing"

// TestReadSentPaged pins the /api/sent offset contract: paging is newest-first
// with offset skipping, mirroring ReadInboxPaged (requested for the browse UI's
// sent pagination; the endpoint previously ignored offset entirely).
func TestReadSentPaged(t *testing.T) {
	s := newTokensStore(t) // registers alice@t

	// alice sends three letters (A, B, C — A oldest, C newest in sent index).
	for _, subj := range []string{"sent-a", "sent-b", "sent-c"} {
		if _, err := s.Send("alice@t", "Alice", []string{"alice@t"}, nil, subj, "body", ""); err != nil {
			t.Fatalf("seed send %s: %v", subj, err)
		}
	}

	page1, err := s.ReadSentPaged("alice@t", 2, 0)
	if err != nil || len(page1) != 2 {
		t.Fatalf("page1 = %d msgs (%v), want 2", len(page1), err)
	}
	if page1[0].Subject != "sent-c" || page1[1].Subject != "sent-b" {
		t.Fatalf("page1 order = [%s, %s], want newest-first [sent-c, sent-b]",
			page1[0].Subject, page1[1].Subject)
	}

	page2, err := s.ReadSentPaged("alice@t", 2, 2)
	if err != nil || len(page2) != 1 {
		t.Fatalf("page2 = %d msgs (%v), want 1", len(page2), err)
	}
	if page2[0].Subject != "sent-a" {
		t.Fatalf("page2 = [%s], want sent-a", page2[0].Subject)
	}

	// Offset past the end yields an empty page, not an error.
	tail, err := s.ReadSentPaged("alice@t", 2, 9)
	if err != nil || len(tail) != 0 {
		t.Fatalf("offset past end = %d msgs (%v), want 0", len(tail), err)
	}
}
