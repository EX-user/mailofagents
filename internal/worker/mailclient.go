package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// MailClient is the read-only-ish server API surface the MVP worker needs:
// unread list (with previews) and mechanical alert sending. Full bodies are
// fetched by the CLI agent itself with its own credentials (dual-holder
// design, v4).
type MailClient struct {
	base string
	user string
	pass string
	http *http.Client
}

func NewMailClient(server, address, password string) *MailClient {
	return &MailClient{
		base: server,
		user: address,
		pass: password,
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// MailSummary mirrors the /api/inbox entry fields the worker needs.
type MailSummary struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Preview string `json:"preview"`
	Unread  bool   `json:"unread"`
}

func (m *MailClient) get(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, m.base+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(m.user, m.pass)
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s %s", path, resp.Status, truncate(string(body), 200))
	}
	return json.Unmarshal(body, out)
}

// UnreadInbox returns the newest inbox page and filters unread entries.
// The unread list IS the work queue (v4: no cursor).
func (m *MailClient) UnreadInbox(limit int) ([]MailSummary, error) {
	var d struct {
		Messages []MailSummary `json:"messages"`
	}
	if err := m.get(fmt.Sprintf("/api/inbox?limit=%d", limit), &d); err != nil {
		return nil, err
	}
	var unread []MailSummary
	for _, msg := range d.Messages {
		if msg.Unread {
			unread = append(unread, msg)
		}
	}
	return unread, nil
}

// SendMail posts a plain mail (used for mechanical alerts only).
func (m *MailClient) SendMail(to, subject, body string) error {
	payload, err := json.Marshal(map[string]any{"to": []string{to}, "subject": subject, "body": body})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, m.base+"/api/send", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.SetBasicAuth(m.user, m.pass)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST /api/send: %s %s", resp.Status, truncate(string(respBody), 200))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
