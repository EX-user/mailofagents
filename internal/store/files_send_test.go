package store

import (
	"strings"
	"testing"
)

func TestSendWithAttachments(t *testing.T) {
	s := newFilesStore(t)
	rec, err := s.SaveFile("a@t", "report.txt", nil, []byte("file-body"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	res, err := s.SendWithAttachments("a@t", "a", []string{"b@t", "c@t"}, nil, "with file",
		"see attachment", []string{rec.ID}, "")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	// Recipient sees the attachment metadata (with access code).
	msg, err := s.GetMessage("b@t", res.MessageID)
	if err != nil {
		t.Fatalf("get message: %v", err)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %+v, want 1", msg.Attachments)
	}
	am := msg.Attachments[0]
	if am.ID != rec.ID || am.Filename != "report.txt" || am.AccessCode != rec.AccessCode || am.Size != 9 {
		t.Errorf("attachment meta wrong: %+v", am)
	}
	// The recipient was added to the file's allowed list and can download.
	if _, err := s.AuthorizeFileDownload("b@t", rec.ID, rec.AccessCode); err != nil {
		t.Errorf("recipient should be authorized after send: %v", err)
	}
	if _, err := s.AuthorizeFileDownload("c@t", rec.ID, rec.AccessCode); err != nil {
		t.Errorf("second recipient should be authorized: %v", err)
	}
	// Outsider still denied.
	if _, err := s.AuthorizeFileDownload("admin@t", rec.ID, rec.AccessCode); err == nil {
		t.Error("outsider must stay denied")
	}
}

func TestSendWithAttachmentsOwnership(t *testing.T) {
	s := newFilesStore(t)
	rec, err := s.SaveFile("a@t", "mine.txt", nil, []byte("x"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	// b tries to attach a's file.
	if _, err := s.SendWithAttachments("b@t", "b", []string{"c@t"}, nil, "steal", "x", []string{rec.ID}, ""); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Errorf("attaching another account's file must fail, got %v", err)
	}
	// Unknown file id.
	if _, err := s.SendWithAttachments("a@t", "a", []string{"b@t"}, nil, "x", "x", []string{"NOPE"}, ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown attachment id must fail, got %v", err)
	}
	// Nothing was delivered on failure.
	msgs, _ := s.ReadInbox("b@t", 10)
	for _, m := range msgs {
		if m.Subject == "steal" || m.Subject == "x" {
			t.Errorf("failed send leaked a message: %+v", m)
		}
	}
}

func TestFilesTotalLimit(t *testing.T) {
	s := newFilesStore(t)
	if err := s.SetFilesTotalLimit(1 << 20); err != nil { // 1MB total
		t.Fatalf("set limit: %v", err)
	}
	if _, err := s.SaveFile("a@t", "one.bin", nil, make([]byte, 600<<10)); err != nil {
		t.Fatalf("600KB should fit: %v", err)
	}
	if _, err := s.SaveFile("b@t", "two.bin", nil, make([]byte, 600<<10)); err != ErrQuotaExceeded {
		t.Errorf("second 600KB must hit the total cap, got %v", err)
	}
	if got := s.GetFilesTotalLimit(); got != 1<<20 {
		t.Errorf("limit = %d, want %d", got, 1<<20)
	}
}
