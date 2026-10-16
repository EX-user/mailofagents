package server

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// 1016 rc23 option A (Devi's cut): owner-specific re-authorization after
// upload — POST /api/files/{id}/allowed extends the allowed list without a
// re-upload; send-time extension already grants to+cc. The endpoint is
// owner-only, validates every address, and shares the hourly management
// cap; missing/foreign ids answer 404 (no oracle), like every file op.

func newFileTestServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.DB().Close() })
	if err := st.BootstrapSystem("admin", "adminpassword1", "test.example"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	a, err := audit.New(st.DB())
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	ts := httptest.NewServer(New(st, a, &config.Config{}).Handler())
	t.Cleanup(ts.Close)
	return ts, &http.Client{}
}

func registerFileUser(t *testing.T, ts *httptest.Server, c *http.Client, name, pass string) {
	t.Helper()
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

func uploadFile(t *testing.T, ts *httptest.Server, c *http.Client, who, pass, content string) (id, code string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", "probe.txt")
	part.Write([]byte(content))
	mw.Close()
	up, _ := http.NewRequest("POST", ts.URL+"/api/files/upload", &buf)
	up.Header.Set("Content-Type", mw.FormDataContentType())
	up.SetBasicAuth(who+"@test.example", pass)
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

func downloadStatus(t *testing.T, ts *httptest.Server, c *http.Client, who, pass, id, code string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+"/api/files/"+id+"/download?code="+code, nil)
	req.SetBasicAuth(who+"@test.example", pass)
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer res.Body.Close()
	io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

func postAllowed(t *testing.T, ts *httptest.Server, c *http.Client, who, pass, id, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+"/api/files/"+id+"/allowed", strings.NewReader(body))
	req.SetBasicAuth(who+"@test.example", pass)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("post allowed: %v", err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(b)
}

func TestFileAllowedOwnerGrantLetsRecipientDownload(t *testing.T) {
	ts, c := newFileTestServer(t)
	for _, u := range [][]string{{"owner", "ownerpassword1"}, {"mate", "matepassword1"}, {"stranger", "strangerpassword1"}} {
		registerFileUser(t, ts, c, u[0], u[1])
	}
	id, code := uploadFile(t, ts, c, "owner", "ownerpassword1", "payload for allowed test")

	// Before the grant the recipient fails even with the code in hand.
	if got := downloadStatus(t, ts, c, "mate", "matepassword1", id, code); got != http.StatusNotFound {
		t.Fatalf("pre-grant download: got %d want 404", got)
	}

	res, body := postAllowed(t, ts, c, "owner", "ownerpassword1", id, `{"allowed":["Mate@Test.Example","owner@test.example","mate@test.example"]}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("allowed: %d %s", res.StatusCode, body)
	}
	var d struct {
		ID      string   `json:"id"`
		Allowed []string `json:"allowed"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("allowed body: %v", err)
	}
	// Lowercased, deduped, owner skipped, sorted.
	if len(d.Allowed) != 1 || d.Allowed[0] != "mate@test.example" {
		t.Fatalf("allowed list: %v", d.Allowed)
	}

	// Now the recipient downloads (allowed + code). Bystander still 404s.
	if got := downloadStatus(t, ts, c, "mate", "matepassword1", id, code); got != http.StatusOK {
		t.Fatalf("post-grant download: got %d want 200", got)
	}
	if got := downloadStatus(t, ts, c, "stranger", "strangerpassword1", id, code); got != http.StatusNotFound {
		t.Fatalf("bystander download: got %d want 404", got)
	}
}

func TestFileAllowedNoOracleValidationAndCaps(t *testing.T) {
	ts, c := newFileTestServer(t)
	for _, u := range [][]string{{"owner", "ownerpassword1"}, {"foe", "foepassword1"}} {
		registerFileUser(t, ts, c, u[0], u[1])
	}
	id, _ := uploadFile(t, ts, c, "owner", "ownerpassword1", "payload")

	// Foreign owner and missing id both 404 — no oracle.
	if res, body := postAllowed(t, ts, c, "foe", "foepassword1", id, `{"allowed":["a@test.example"]}`); res.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign owner: %d %s", res.StatusCode, body)
	}
	if res, body := postAllowed(t, ts, c, "owner", "ownerpassword1", "01_NOPE", `{"allowed":["a@test.example"]}`); res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing id: %d %s", res.StatusCode, body)
	}

	// Key validation: junk addresses answer 400 (input shape, not existence).
	for _, bad := range []string{`{"allowed":[]}`, `{"allowed":["no-at-sign"]}`, `{"allowed":["a@b@c"]}`, `{"allowed":[" a@b ]}`, `{"allowed":[""]}`, `{not json}`} {
		if res, body := postAllowed(t, ts, c, "owner", "ownerpassword1", id, bad); res.StatusCode != http.StatusBadRequest {
			t.Fatalf("bad body %q: %d %s", bad, res.StatusCode, body)
		}
	}

	// The cap: list full at FileAllowedMax -> 409.
	addresses := make([]string, 0, store.FileAllowedMax)
	for i := 0; i < store.FileAllowedMax; i++ {
		addresses = append(addresses, fmt.Sprintf("user%03d@test.example", i))
	}
	packed, _ := json.Marshal(map[string]any{"allowed": addresses})
	if res, body := postAllowed(t, ts, c, "owner", "ownerpassword1", id, string(packed)); res.StatusCode != http.StatusOK {
		t.Fatalf("fill to cap: %d %s", res.StatusCode, body)
	}
	if res, body := postAllowed(t, ts, c, "owner", "ownerpassword1", id, `{"allowed":["onemore@test.example"]}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("over cap: %d %s", res.StatusCode, body)
	}

	// The rate cap: extendRateLimit mutations per hour (this file already
	// spent several; run against a fresh file to count them all).
	id2, _ := uploadFile(t, ts, c, "owner", "ownerpassword1", "payload2")
	limited := false
	for i := 0; i < 12; i++ {
		res, _ := postAllowed(t, ts, c, "owner", "ownerpassword1", id2, `{"allowed":["x@test.example"]}`)
		if res.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatalf("rate cap never bit (%d calls)", 12)
	}
}

func TestSendWithAttachmentsGrantsRecipients(t *testing.T) {
	ts, c := newFileTestServer(t)
	for _, u := range [][]string{{"sender", "senderpassword1"}, {"getter", "getterpassword1"}} {
		registerFileUser(t, ts, c, u[0], u[1])
	}
	id, code := uploadFile(t, ts, c, "sender", "senderpassword1", "ride-along payload")

	body := map[string]any{
		"to":          []string{"getter@test.example"},
		"subject":     "att rides",
		"body":        "see attachment",
		"attachments": []string{id},
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", ts.URL+"/api/send", bytes.NewReader(b))
	req.SetBasicAuth("sender@test.example", "senderpassword1")
	req.Header.Set("Content-Type", "application/json")
	if res, err := c.Do(req); err != nil {
		t.Fatalf("send: %v", err)
	} else {
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			rb, _ := io.ReadAll(res.Body)
			t.Fatalf("send: %d %s", res.StatusCode, rb)
		}
	}

	// Send-time extension put the recipient on the allowed list: the code
	// that rode inside the letter now unlocks the download.
	if got := downloadStatus(t, ts, c, "getter", "getterpassword1", id, code); got != http.StatusOK {
		t.Fatalf("recipient download after send: got %d want 200", got)
	}
}
