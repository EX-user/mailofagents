// Package httpclient is the gateway's thin HTTP client for talking to
// agentmail-server. It has one method per server endpoint, handles Basic auth,
// and decodes JSON responses. There is no retry, no caching, no state — each
// call is a standalone request, which keeps the gateway stateless apart from
// its access_code map.
package httpclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one agentmail-server origin.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a client for the given server origin (e.g. http://127.0.0.1:8090).
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// BaseURL returns the server origin this client talks to.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// Error is returned for non-2xx responses, carrying the status and a snippet
// of the body for diagnostics.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("server returned %d: %s", e.Status, e.Body)
}

// --- response shapes ---

type RegisterResponse struct {
	Address  string `json:"address"`
	Password string `json:"password"`
}

type AuthenticateResponse struct {
	AccessCode string `json:"access_code"`
}

type SendResponse struct {
	MessageID string `json:"message_id"`
}

type MessageSummary struct {
	ID         string   `json:"id"`
	From       string   `json:"from"`
	To         []string `json:"to"`
	CC         []string `json:"cc"`
	InReplyTo  string   `json:"in_reply_to"`
	Subject    string   `json:"subject"`
	Preview    string   `json:"preview"`
	ReceivedAt int64    `json:"received_at"`
	Unread     bool     `json:"unread"`
	Files      int      `json:"files"`
}

type InboxResponse struct {
	Messages []MessageSummary `json:"messages"`
	Count    int              `json:"count"`
}

type AttachmentInfo struct {
	ID         string `json:"id"`
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
	AccessCode string `json:"access_code"`
}

type MessageResponse struct {
	MessageID string `json:"message_id"`
	From      string `json:"from"`
	To        []string `json:"to"`
	CC        []string `json:"cc"`
	InReplyTo string `json:"in_reply_to"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	ReceivedAt int64 `json:"received_at"`
	Attachments []AttachmentInfo `json:"attachments"`
}

// --- API methods ---

// Register creates an account. No auth required.
func (c *Client) Register(name string) (*RegisterResponse, error) {
	var out RegisterResponse
	err := c.do("POST", "/api/register", "", nil, map[string]any{"name": name}, &out)
	return &out, err
}

// VerifyPassword checks credentials. No auth required (it IS the credential
// check). Returns nil on success.
func (c *Client) VerifyPassword(address, password string) error {
	return c.do("POST", "/api/verify-password", "", nil, map[string]any{
		"address":  address,
		"password": password,
	}, nil)
}

// Send posts a message as authUser. authUser:authPass is sent as Basic auth;
// the server treats the authed user as the sender. public=true additionally
// writes a showcase copy (portal sample) — explicit sender opt-in.
// cc (optional) lists carbon-copy addresses, delivered like To and visible
// to recipients. attachments references previously uploaded file IDs
// (server validates they belong to the sender and grants recipients
// download access).
func (c *Client) Send(authUser, authPass string, to []string, cc []string, subject, body string, public bool, attachments []string, inReplyTo string) (*SendResponse, error) {
	payload := map[string]any{
		"to":      to,
		"subject": subject,
		"body":    body,
		"public":  public,
	}
	if inReplyTo != "" {
		payload["in_reply_to"] = inReplyTo
	}
	if len(cc) > 0 {
		payload["cc"] = cc
	}
	if len(attachments) > 0 {
		payload["attachments"] = attachments
	}
	var out SendResponse
	err := c.do("POST", "/api/send", basicAuth(authUser, authPass), nil, payload, &out)
	return &out, err
}

// UploadResponse mirrors POST /api/files/upload.
type UploadResponse struct {
	ID         string `json:"id"`
	AccessCode string `json:"access_code"`
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
}

// UploadFile posts one file as multipart to /api/files/upload.
func (c *Client) UploadFile(authUser, authPass, filename string, content []byte) (*UploadResponse, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := fw.Write(content); err != nil {
		return nil, fmt.Errorf("write form file: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", c.baseURL+"/api/files/upload", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", basicAuth(authUser, authPass))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upload: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out UploadResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DownloadFile fetches an attachment's content. Returns the bytes plus the
// server-supplied filename. Non-200 (missing file, no permission, bad code)
// maps to a descriptive error.
func (c *Client) DownloadFile(authUser, authPass, fileID, code string) ([]byte, string, error) {
	endpoint := c.baseURL + "/api/files/" + fileID + "/download?code=" + url.QueryEscape(code)
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", basicAuth(authUser, authPass))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // hard read cap 8MB
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return nil, "", fmt.Errorf("download: %s", msg)
	}
	// Best-effort filename from Content-Disposition (filename*= preferred).
	name := fileID
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if i := strings.Index(cd, "filename*=UTF-8''"); i >= 0 {
			if dec, err := url.QueryUnescape(cd[i+len("filename*=UTF-8''"):]); err == nil && dec != "" {
				name = dec
			}
		}
	}
	return body, name, nil
}

// Inbox lists the authed user's inbox.
func (c *Client) Inbox(authUser, authPass string, limit int) (*InboxResponse, error) {
	var out InboxResponse
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	err := c.do("GET", "/api/inbox", basicAuth(authUser, authPass), q, nil, &out)
	return &out, err
}

// ThreadTopic is one topic-index row (mirror of the server payload).
type ThreadTopic struct {
	RootID       string   `json:"root_id"`
	Subject      string   `json:"subject"`
	Participants []string `json:"participants"`
	Count        int      `json:"count"`
	LastAt       int64    `json:"last_at"`
}

// ThreadsResponse mirrors GET /api/threads.
type ThreadsResponse struct {
	Threads []ThreadTopic `json:"threads"`
	Total   int           `json:"total"`
	Limit   int           `json:"limit"`
	Offset  int           `json:"offset"`
}

// TopicResponse mirrors GET /api/thread?root= (any member id resolves).
type TopicResponse struct {
	Root     string           `json:"root"`
	Messages []MessageSummary `json:"messages"`
	Count    int              `json:"count"`
}

// Threads fetches the topic index for the authed user.
func (c *Client) Threads(authUser, authPass string, limit, offset, minCount int) (*ThreadsResponse, error) {
	var out ThreadsResponse
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	if offset > 0 {
		q.Set("offset", fmt.Sprintf("%d", offset))
	}
	if minCount > 0 {
		q.Set("min_count", fmt.Sprintf("%d", minCount))
	}
	err := c.do("GET", "/api/threads", basicAuth(authUser, authPass), q, nil, &out)
	return &out, err
}

// Topic fetches one topic tree; id may be ANY message in the tree (the
// server resolves it to its connected component).
func (c *Client) Topic(authUser, authPass, id string) (*TopicResponse, error) {
	var out TopicResponse
	q := url.Values{"root": []string{id}}
	err := c.do("GET", "/api/thread", basicAuth(authUser, authPass), q, nil, &out)
	return &out, err
}

// GetMessage fetches one message by id for the authed user.
func (c *Client) GetMessage(authUser, authPass, id string) (*MessageResponse, error) {
	var out MessageResponse
	q := url.Values{}
	q.Set("id", id)
	err := c.do("GET", "/api/message", basicAuth(authUser, authPass), q, nil, &out)
	return &out, err
}

// InfoRaw calls /api/info?query=<query> and returns the raw JSON as a generic
// map. For admin-only queries (accounts, audit), pass authUser/authPass; for
// public queries, pass empty strings.
func (c *Client) InfoRaw(authUser, authPass, query string) (map[string]any, error) {
	var out map[string]any
	q := url.Values{}
	q.Set("query", query)
	authHeader := ""
	if authUser != "" {
		authHeader = basicAuth(authUser, authPass)
	}
	err := c.do("GET", "/api/info", authHeader, q, nil, &out)
	return out, err
}

// AccountInfoRaw calls the account-scoped /api/account/info?query=<query>
// endpoint and returns the raw JSON. Always requires account Basic auth
// (authUser/authPass). query is "self" (caller's own profile) or "directory"
// (public address book). Mirrors InfoRaw but for account-level queries.
func (c *Client) AccountInfoRaw(authUser, authPass, query string) (map[string]any, error) {
	var out map[string]any
	q := url.Values{}
	q.Set("query", query)
	err := c.do("GET", "/api/account/info", basicAuth(authUser, authPass), q, nil, &out)
	return out, err
}

// UpdateProfile POSTs to /api/profile/self to set the caller's directory
// visibility and signature. Returns the raw JSON the server replies with
// ({"ok":true,"visible":...,"signature":...}).
func (c *Client) UpdateProfile(authUser, authPass string, visible bool, signature string) (map[string]any, error) {
	var out map[string]any
	err := c.do("POST", "/api/profile/self", basicAuth(authUser, authPass), nil, map[string]any{
		"visible":   visible,
		"signature": signature,
	}, &out)
	return out, err
}

// --- transport ---

func (c *Client) do(method, path, authHeader string, q url.Values, body any, out any) error {
	endpoint := c.baseURL + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		return err
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		snippet := string(respBody)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return &Error{Status: resp.StatusCode, Body: snippet}
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w (body: %s)", err, string(respBody[:min(len(respBody), 200)]))
		}
	}
	return nil
}

func basicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// SubsDeclare declares the authenticated account a subordinate of superior
// (POST /api/subs). Errors mirror the HTTP semantics (404 masquerade etc.).
func (c *Client) SubsDeclare(authUser, authPass, superior string) error {
	payload := map[string]any{"superior": superior, "scope": "both"}
	return c.do("POST", "/api/subs", basicAuth(authUser, authPass), nil, payload, nil)
}

// SubsRevoke removes the authenticated account's declaration under superior
// (DELETE /api/subs?superior=...). Idempotent.
func (c *Client) SubsRevoke(authUser, authPass, superior string) error {
	q := url.Values{}
	q.Set("superior", superior)
	return c.do("DELETE", "/api/subs", basicAuth(authUser, authPass), q, nil, nil)
}

// SubsList returns the caller's own edges (GET /api/subs) as raw JSON.
func (c *Client) SubsList(authUser, authPass string) (map[string]any, error) {
	var out map[string]any
	err := c.do("GET", "/api/subs", basicAuth(authUser, authPass), nil, nil, &out)
	return out, err
}
