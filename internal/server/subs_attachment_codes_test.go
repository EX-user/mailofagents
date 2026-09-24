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

// 0924 boss report: a subordinate's letter TO the viewer showed its
// attachments as "download not authorized" in the read-only subordinate
// pane. The subs detail endpoint used to strip access codes unconditionally
// (Q2); now the codes survive when the reader is himself a to/cc recipient
// of the message — the download endpoint's own authorization is unchanged,
// so non-recipient readers still get metadata only.
func TestSubReadRecipientKeepsAttachmentCodes(t *testing.T) {
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

	register := func(name, pass string) {
		res, err := http.Post(ts.URL+"/api/register", "application/json",
			strings.NewReader(`{"name":"`+name+`","password":"`+pass+`"}`))
		if err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusConflict {
			b, _ := io.ReadAll(res.Body)
			t.Fatalf("register %s: %d %s", name, res.StatusCode, b)
		}
	}
	register("lead", "leadpassword1")
	register("sub", "subpassword1")
	register("bystander", "bystanderpassword1")

	// sub declares itself a subordinate of lead — lead may read sub's mail.
	req, _ := http.NewRequest("POST", ts.URL+"/api/subs",
		strings.NewReader(`{"superior":"lead@test.example","scope":"both"}`))
	req.SetBasicAuth("sub@test.example", "subpassword1")
	req.Header.Set("Content-Type", "application/json")
	if res, err := c.Do(req); err != nil {
		t.Fatalf("declare sub: %v", err)
	} else {
		res.Body.Close()
		if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
			b, _ := io.ReadAll(res.Body)
			t.Fatalf("declare sub: %d %s", res.StatusCode, b)
		}
	}

	// sub uploads one file and sends it to two different letters: one TO
	// lead (recipient view) and one to a bystander (pure superior read).
	upload := func() (id, code string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, _ := mw.CreateFormFile("file", "probe.txt")
		part.Write([]byte("sub attachment payload"))
		mw.Close()
		up, _ := http.NewRequest("POST", ts.URL+"/api/files/upload", &buf)
		up.Header.Set("Content-Type", mw.FormDataContentType())
		up.SetBasicAuth("sub@test.example", "subpassword1")
		res, err := c.Do(up)
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		defer res.Body.Close()
		var d struct {
			ID         string `json:"id"`
			AccessCode string `json:"access_code"`
		}
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("upload: %d %s", res.StatusCode, b)
		}
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatalf("upload body: %v", err)
		}
		return d.ID, d.AccessCode
	}
	send := func(to, fileID string) {
		body := map[string]any{
			"to":          []string{to},
			"subject":     "att to " + to,
			"body":        "see attachment",
			"attachments": []string{fileID},
		}
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", ts.URL+"/api/send", bytes.NewReader(b))
		req.SetBasicAuth("sub@test.example", "subpassword1")
		req.Header.Set("Content-Type", "application/json")
		res, err := c.Do(req)
		if err != nil {
			t.Fatalf("send to %s: %v", to, err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			rb, _ := io.ReadAll(res.Body)
			t.Fatalf("send to %s: %d %s", to, res.StatusCode, rb)
		}
	}
	// Three uploads: the model re-authorizes the recipients of each send,
	// so every letter carries its own authorized copy reference.
	f1, _ := upload()
	f2, _ := upload()
	send("lead@test.example", f1)
	send("bystander@test.example", f2)

	inboxID := func(acct, pass, subject string) string {
		req, _ := http.NewRequest("GET", ts.URL+"/api/inbox?limit=20", nil)
		req.SetBasicAuth(acct, pass)
		res, err := c.Do(req)
		if err != nil {
			t.Fatalf("inbox %s: %v", acct, err)
		}
		defer res.Body.Close()
		var d struct {
			Messages []struct {
				ID      string `json:"id"`
				Subject string `json:"subject"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(res.Body).Decode(&d); err != nil {
			t.Fatalf("inbox %s decode: %v", acct, err)
		}
		for _, m := range d.Messages {
			if m.Subject == subject {
				return m.ID
			}
		}
		t.Fatalf("inbox %s: letter %q not found", acct, subject)
		return ""
	}
	toLead := inboxID("lead@test.example", "leadpassword1", "att to lead@test.example")

	subDetail := func(letterID string) (code string, raw []byte) {
		req, _ := http.NewRequest("GET",
			ts.URL+"/api/subs/sub@test.example/message?id="+letterID, nil)
		req.SetBasicAuth("lead@test.example", "leadpassword1")
		res, err := c.Do(req)
		if err != nil {
			t.Fatalf("sub detail: %v", err)
		}
		defer res.Body.Close()
		raw, _ = io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("sub detail: %d %s", res.StatusCode, raw)
		}
		var d struct {
			Message struct {
				Attachments []struct {
					ID         string `json:"id"`
					AccessCode string `json:"access_code"`
				} `json:"attachments"`
			} `json:"message"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("sub detail decode: %v", err)
		}
		if len(d.Message.Attachments) != 1 {
			t.Fatalf("sub detail: want 1 attachment, got %d", len(d.Message.Attachments))
		}
		return d.Message.Attachments[0].AccessCode, raw
	}

	// Recipient-viewer: the code survives, and the download endpoint honors
	// it with the same session.
	code, _ := subDetail(toLead)
	if code == "" {
		t.Fatal("lead is the recipient — subs detail must keep the access code")
	}
	dl, _ := http.NewRequest("GET",
		ts.URL+"/api/files/"+f1+"/download?code="+code, nil)
	dl.SetBasicAuth("lead@test.example", "leadpassword1")
	res, err := c.Do(dl)
	if err != nil {
		t.Fatalf("download as lead: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("download as lead: %d %s", res.StatusCode, body)
	}
	if string(body) != "sub attachment payload" {
		t.Fatalf("download body mismatch: %q", body)
	}

	// Non-recipient superior read (letter to the bystander): codes stay
	// stripped — the Q2 boundary is unchanged for pure superior reads.
	toBystander := inboxID("bystander@test.example", "bystanderpassword1", "att to bystander@test.example")
	code2, _ := subDetail(toBystander)
	if code2 != "" {
		t.Fatal("non-recipient reader must still get the stripped payload")
	}
}
