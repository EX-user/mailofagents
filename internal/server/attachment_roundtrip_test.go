package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentmail/agentmail/internal/audit"
	"github.com/agentmail/agentmail/internal/config"
	"github.com/agentmail/agentmail/internal/store"
)

// Attachment round-trip gate (0.2.4.1, P0 field report 2026-09-03):
// upload → send-with-attachment → read back through BOTH views and assert
// the attachment survived every hop. This is an API-level go test, not a
// manual regression — the send pipeline must never silently drop
// attachments again.
func TestAttachmentRoundTrip(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.DB().Close()
	if err := st.BootstrapSystem("admin", "adminpassword1", "test.example"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	a, err := audit.New(st.DB())
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	ts := httptest.NewServer(New(st, a, &config.Config{}).Handler())
	defer ts.Close()

	c := &http.Client{}

	// Register a sender account (fresh store: registration is open).
	reg, _ := http.Post(ts.URL+"/api/register", "application/json",
		strings.NewReader(`{"name":"sender","password":"senderpass1"}`))
	if reg != nil {
		rb, _ := io.ReadAll(reg.Body)
		reg.Body.Close()
		t.Logf("register: %d %s", reg.StatusCode, rb)
		if reg.StatusCode != http.StatusOK && reg.StatusCode != http.StatusConflict {
			t.Fatalf("register: %d %s", reg.StatusCode, rb)
		}
	}

	auth := func(req *http.Request) { req.SetBasicAuth("sender@test.example", "senderpass1") }

	// 1) upload a file.
	var upBuf bytes.Buffer
	mw := multipart.NewWriter(&upBuf)
	part, _ := mw.CreateFormFile("file", "roundtrip.txt")
	part.Write([]byte("attachment round-trip payload"))
	mw.Close()
	chk, _ := http.NewRequest("GET", ts.URL+"/api/inbox?limit=1", nil)
	auth(chk)
	chkRes, err := c.Do(chk)
	if err != nil {
		t.Fatalf("auth probe: %v", err)
	}
	chkRes.Body.Close()
	if chkRes.StatusCode != http.StatusOK {
		t.Fatalf("auth probe: %d — Basic auth itself failing", chkRes.StatusCode)
	}
	upReq, _ := http.NewRequest("POST", ts.URL+"/api/files/upload", &upBuf)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	auth(upReq)
	up, err := c.Do(upReq)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	upBody, _ := io.ReadAll(up.Body)
	up.Body.Close()
	if up.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d %s", up.StatusCode, upBody)
	}
	var upRes struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(upBody, &upRes); err != nil || upRes.ID == "" {
		t.Fatalf("upload response: %s (%v)", upBody, err)
	}

	// 2) send with the attachment.
	sendReq, _ := http.NewRequest("POST", ts.URL+"/api/send",
		strings.NewReader(`{"to":["sender@test.example"],"subject":"rt","body":"round trip body","attachments":["`+upRes.ID+`"]}`))
	sendReq.Header.Set("Content-Type", "application/json")
	auth(sendReq)
	send, err := c.Do(sendReq)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	sendBody, _ := io.ReadAll(send.Body)
	send.Body.Close()
	if send.StatusCode != http.StatusOK {
		t.Fatalf("send: %d %s", send.StatusCode, sendBody)
	}
	var sendRes struct {
		MessageID string `json:"message_id"`
	}
	json.Unmarshal(sendBody, &sendRes)
	if sendRes.MessageID == "" {
		t.Fatalf("send: no message_id in %s", sendBody)
	}

	// 3a) detail view: the attachment array must be present with the file.
	detReq, _ := http.NewRequest("GET", ts.URL+"/api/message?id="+sendRes.MessageID, nil)
	auth(detReq)
	det, err := c.Do(detReq)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	detBody, _ := io.ReadAll(det.Body)
	det.Body.Close()
	if det.StatusCode != http.StatusOK {
		t.Fatalf("detail: %d", det.StatusCode)
	}
	if !strings.Contains(string(detBody), "roundtrip.txt") {
		t.Errorf("detail view lost the attachment: %s", detBody)
	}

	// 3b) list view: files count must be ≥1.
	listReq, _ := http.NewRequest("GET", ts.URL+"/api/sent?limit=1", nil)
	auth(listReq)
	list, err := c.Do(listReq)
	if err != nil {
		t.Fatalf("sent: %v", err)
	}
	listBody, _ := io.ReadAll(list.Body)
	list.Body.Close()
	if !strings.Contains(string(listBody), `"files":1`) {
		t.Errorf("sent list files count != 1: %s", listBody)
	}
}
