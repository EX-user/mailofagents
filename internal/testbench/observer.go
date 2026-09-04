package testbench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Obs is the observer's mailbox-side client. Assertion surface is
// STRICTLY the public API contract (whitepaper: no session access, no
// internal knowledge) — /api/info, /api/inbox, /api/sent, /api/message,
// /api/send. It records every call into the timeline so assertions can
// cite evidence.
type Obs struct {
	server string
	addr   string
	pass   string
	http   *http.Client
	tl     *Timeline
}

// NewObs builds an observer. addr/pass may be empty for public-only
// endpoints (selfcheck); authenticated calls then fail with 401, which
// is itself timeline evidence.
func NewObs(server, addr, pass string, tl *Timeline) *Obs {
	return &Obs{
		server: server,
		addr:   addr,
		pass:   pass,
		http:   &http.Client{Timeout: 30 * time.Second},
		tl:     tl,
	}
}

// ServiceInfo is the /api/info contract subset the bench relies on.
// (Real shape, verified against production 2026-09-04: status query
// answers domain/initialized/version/gateway floor.)
type ServiceInfo struct {
	Domain      string `json:"domain"`
	Initialized bool   `json:"initialized"`
	Version     string `json:"version"`
}

// MailSummary mirrors the public inbox/sent list contract fields.
type MailSummary struct {
	ID         string   `json:"id"`
	From       string   `json:"from"`
	To         []string `json:"to"`
	Subject    string   `json:"subject"`
	Preview    string   `json:"preview"`
	Unread     bool     `json:"unread"`
	ReceivedAt int64    `json:"received_at"`
	Files      int      `json:"files"`
}

// Mail is the /api/message contract: full body + attachments metadata.
type Mail struct {
	ID          string   `json:"message_id"`
	From        string   `json:"from"`
	To          []string `json:"to"`
	Subject     string   `json:"subject"`
	Body        string   `json:"body"`
	InReplyTo   string   `json:"in_reply_to,omitempty"`
	ReceivedAt  int64    `json:"received_at"`
	Attachments []struct {
		ID         string `json:"id"`
		AccessCode string `json:"access_code"`
		Filename   string `json:"filename"`
		Size       int    `json:"size"`
	} `json:"attachments"`
}

func (o *Obs) call(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, o.server+path, nil)
	if err != nil {
		return err
	}
	if o.addr != "" {
		req.SetBasicAuth(o.addr, o.pass)
	}
	resp, err := o.http.Do(req)
	if err != nil {
		_ = o.tl.Add("call", method+" "+path, map[string]string{"error": err.Error()})
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = o.tl.Add("call", method+" "+path, map[string]any{"status": resp.StatusCode, "bytes": len(body)})
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, truncate(string(body), 200))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// Info fetches public service identity.
func (o *Obs) Info(ctx context.Context) (ServiceInfo, error) {
	var info ServiceInfo
	err := o.call(ctx, http.MethodGet, "/api/info?query=status", &info)
	return info, err
}

// Inbox lists the observed account's inbox (public contract fields).
func (o *Obs) Inbox(ctx context.Context, limit int) ([]MailSummary, error) {
	var box struct {
		Messages []MailSummary `json:"messages"`
	}
	err := o.call(ctx, http.MethodGet, fmt.Sprintf("/api/inbox?limit=%d", limit), &box)
	return box.Messages, err
}

// Sent lists the observed account's sent letters.
func (o *Obs) Sent(ctx context.Context, limit int) ([]MailSummary, error) {
	var box struct {
		Messages []MailSummary `json:"messages"`
	}
	err := o.call(ctx, http.MethodGet, fmt.Sprintf("/api/sent?limit=%d", limit), &box)
	return box.Messages, err
}

// Message fetches one letter (marks it read — contract side effect).
func (o *Obs) Message(ctx context.Context, id string) (Mail, error) {
	var m Mail
	err := o.call(ctx, http.MethodGet, "/api/message?id="+id, &m)
	return m, err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
