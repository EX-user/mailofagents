package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
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

// pngBytes renders a WxH opaque image for upload tests.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 200, G: 10, B: 10, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}

// avatarUpload PUTs one multipart image as the given account.
func avatarUpload(t *testing.T, ts *httptest.Server, acct, pass string, body []byte) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, _ := mw.CreateFormFile("file", "avatar.png")
	part.Write(body)
	mw.Close()
	req, _ := http.NewRequest("PUT", ts.URL+"/api/account/avatar", &buf)
	req.SetBasicAuth(acct, pass)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("avatar upload: %v", err)
	}
	return res
}

func setupAvatarServer(t *testing.T) *httptest.Server {
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
	c := &http.Client{}
	setReg, _ := http.NewRequest("POST", ts.URL+"/admin/set-registration",
		strings.NewReader(`{"enabled":true}`))
	setReg.SetBasicAuth("admin@test.example", "adminpassword1")
	setReg.Header.Set("Content-Type", "application/json")
	res0, err := c.Do(setReg)
	if err != nil {
		t.Fatalf("set-registration: %v", err)
	}
	res0.Body.Close()
	reg, _ := http.Post(ts.URL+"/api/register", "application/json",
		strings.NewReader(`{"name":"avuser","password":"avpassword1"}`))
	reg.Body.Close()
	if reg.StatusCode != http.StatusOK && reg.StatusCode != http.StatusConflict {
		t.Fatalf("register: %d", reg.StatusCode)
	}
	return ts
}

func TestAvatarLifecycle(t *testing.T) {
	ts := setupAvatarServer(t)
	defer ts.Close()
	c := &http.Client{}

	// 1) GET before upload: clean 404 for the owner (the default-avatar
	// fallback signal); anonymous callers hit the auth wall (401) — the
	// public guest-page endpoint is D1, boss-pending.
	res, err := http.Get(ts.URL + "/api/avatar/avuser@test.example")
	if err != nil {
		t.Fatalf("anon get before: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anon get: want 401, got %d", res.StatusCode)
	}
	req0, _ := http.NewRequest("GET", ts.URL+"/api/avatar/avuser@test.example", nil)
	req0.SetBasicAuth("avuser@test.example", "avpassword1")
	res, err = c.Do(req0)
	if err != nil {
		t.Fatalf("get before: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("get before upload: want 404, got %d", res.StatusCode)
	}

	// 2) Valid upload: 200 + hash; profile carries the hash.
	res = avatarUpload(t, ts, "avuser@test.example", "avpassword1", pngBytes(t, 64, 64))
	var up struct {
		AvatarHash string `json:"avatar_hash"`
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d %s", res.StatusCode, b)
	}
	if err := json.Unmarshal(b, &up); err != nil || up.AvatarHash == "" {
		t.Fatalf("upload body: %v hash=%q", err, up.AvatarHash)
	}

	// 3) GET serves the bytes with ETag/immutable and honors If-None-Match.
	req1, _ := http.NewRequest("GET", ts.URL+"/api/avatar/avuser@test.example", nil)
	req1.SetBasicAuth("avuser@test.example", "avpassword1")
	res, err = c.Do(req1)
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || len(body) == 0 {
		t.Fatalf("get after upload: %d len=%d", res.StatusCode, len(body))
	}
	etag := res.Header.Get("ETag")
	if etag != `"`+up.AvatarHash+`"` {
		t.Fatalf("etag %q != hash %q", etag, up.AvatarHash)
	}
	if !strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("cache-control missing immutable: %q", res.Header.Get("Cache-Control"))
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/avatar/avuser@test.example", nil)
	req.SetBasicAuth("avuser@test.example", "avpassword1")
	req.Header.Set("If-None-Match", etag)
	res304, err := c.Do(req)
	if err != nil {
		t.Fatalf("conditional get: %v", err)
	}
	res304.Body.Close()
	if res304.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional get: want 304, got %d", res304.StatusCode)
	}

	// 4) Overwrite: bytes replaced (different image -> different hash).
	res = avatarUpload(t, ts, "avuser@test.example", "avpassword1", pngBytes(t, 48, 96))
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	var up2 struct {
		AvatarHash string `json:"avatar_hash"`
	}
	json.Unmarshal(b, &up2)
	if up2.AvatarHash == "" || up2.AvatarHash == up.AvatarHash {
		t.Fatalf("overwrite: hash not re-minted (%q vs %q)", up2.AvatarHash, up.AvatarHash)
	}

	// 5) Validation matrix: oversize dimensions, wrong format, oversize bytes.
	res = avatarUpload(t, ts, "avuser@test.example", "avpassword1", pngBytes(t, 600, 40))
	res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize dims: want 413, got %d", res.StatusCode)
	}
	res = avatarUpload(t, ts, "avuser@test.example", "avpassword1", []byte("this is not an image at all"))
	res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("non-image: want 413, got %d", res.StatusCode)
	}
	res = avatarUpload(t, ts, "avuser@test.example", "avpassword1", bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 40000))
	res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize bytes: want 413, got %d", res.StatusCode)
	}

	// 6) Delete: back to clean 404 (default-avatar signal restored).
	req, _ = http.NewRequest("DELETE", ts.URL+"/api/account/avatar", nil)
	req.SetBasicAuth("avuser@test.example", "avpassword1")
	res, err = c.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	req2, _ := http.NewRequest("GET", ts.URL+"/api/avatar/avuser@test.example", nil)
	req2.SetBasicAuth("avuser@test.example", "avpassword1")
	res, err = c.Do(req2)
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete: want 404, got %d", res.StatusCode)
	}

	// 7) Anonymous wall: both endpoints reject unauthenticated callers.
	res, err = c.Get(ts.URL + "/api/avatar/avuser@test.example")
	if err != nil {
		t.Fatalf("anon get: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anon get: want 401, got %d", res.StatusCode)
	}
}

// D1 (boss approved): the guest page shows real avatars — but ONLY for
// directory-visible accounts with an avatar set; every other case is a
// clean 404 (invisibility must not leak bytes).
func TestPublicAvatarGate(t *testing.T) {
	ts := setupAvatarServer(t)
	defer ts.Close()
	c := &http.Client{}

	// Upload an avatar for the (not-yet-visible) account.
	res := avatarUpload(t, ts, "avuser@test.example", "avpassword1", pngBytes(t, 64, 64))
	res.Body.Close()

	// Invisible account: 404 even though an avatar exists.
	pub, err := http.Get(ts.URL + "/api/public/avatar?address=avuser@test.example")
	if err != nil {
		t.Fatalf("public get invisible: %v", err)
	}
	pub.Body.Close()
	if pub.StatusCode != http.StatusNotFound {
		t.Fatalf("invisible account: want 404, got %d", pub.StatusCode)
	}

	// Make the account visible via profile update.
	pref, _ := http.NewRequest("POST", ts.URL+"/api/profile/self", strings.NewReader(`{"visible":true,"signature":""}`))
	pref.SetBasicAuth("avuser@test.example", "avpassword1")
	pref.Header.Set("Content-Type", "application/json")
	if res, err := c.Do(pref); err != nil {
		t.Fatalf("profile: %v", err)
	} else {
		res.Body.Close()
	}

	// Visible now: 200 with bytes and public caching headers.
	pub, err = http.Get(ts.URL + "/api/public/avatar?address=avuser@test.example")
	if err != nil {
		t.Fatalf("public get visible: %v", err)
	}
	body, _ := io.ReadAll(pub.Body)
	pub.Body.Close()
	if pub.StatusCode != http.StatusOK || len(body) == 0 {
		t.Fatalf("visible account: %d len=%d", pub.StatusCode, len(body))
	}
	if !strings.Contains(pub.Header.Get("Cache-Control"), "public") {
		t.Fatalf("public cache-control: %q", pub.Header.Get("Cache-Control"))
	}

	// Unknown address and missing query: 404.
	pub, err = http.Get(ts.URL + "/api/public/avatar?address=nobody@test.example")
	if err != nil {
		t.Fatal(err)
	}
	pub.Body.Close()
	if pub.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown address: want 404, got %d", pub.StatusCode)
	}
	pub, err = http.Get(ts.URL + "/api/public/avatar")
	if err != nil {
		t.Fatal(err)
	}
	pub.Body.Close()
	if pub.StatusCode != http.StatusNotFound {
		t.Fatalf("missing address: want 404, got %d", pub.StatusCode)
	}
}

// The public directory carries avatar_hash so the guest page can decide
// real-vs-default avatar without an extra request (0.3.3 A contract).
func TestPublicDirectoryCarriesAvatarHash(t *testing.T) {
	ts := setupAvatarServer(t)
	defer ts.Close()
	c := &http.Client{}

	// Upload an avatar (account still invisible) — hash must be absent.
	res := avatarUpload(t, ts, "avuser@test.example", "avpassword1", pngBytes(t, 64, 64))
	res.Body.Close()
	dir, err := http.Get(ts.URL + "/api/info?query=directory")
	if err != nil {
		t.Fatalf("public directory: %v", err)
	}
	var d struct {
		Entries []struct {
			Address    string `json:"address"`
			AvatarHash string `json:"avatar_hash"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(dir.Body).Decode(&d); err != nil {
		t.Fatalf("decode directory: %v", err)
	}
	dir.Body.Close()
	for _, e := range d.Entries {
		if e.Address == "avuser@test.example" && e.AvatarHash != "" {
			t.Fatal("invisible account must not expose avatar_hash in the public directory")
		}
	}

	// Make the account visible — the hash must now appear.
	pref, _ := http.NewRequest("POST", ts.URL+"/api/profile/self", strings.NewReader(`{"visible":true,"signature":""}`))
	pref.SetBasicAuth("avuser@test.example", "avpassword1")
	pref.Header.Set("Content-Type", "application/json")
	res2, err := c.Do(pref)
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	res2.Body.Close()

	dir2, err := http.Get(ts.URL + "/api/info?query=directory")
	if err != nil {
		t.Fatalf("directory 2: %v", err)
	}
	var d2 struct {
		Entries []struct {
			Address    string `json:"address"`
			AvatarHash string `json:"avatar_hash"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(dir2.Body).Decode(&d2); err != nil {
		t.Fatalf("decode directory 2: %v", err)
	}
	dir2.Body.Close()
	found := false
	for _, e := range d2.Entries {
		if e.Address == "avuser@test.example" {
			if e.AvatarHash == "" {
				t.Fatal("visible account with avatar must expose avatar_hash")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("visible account missing from public directory")
	}
}
