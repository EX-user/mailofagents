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

	// 2b) 0021: /api/profile/self carries avatar_hash symmetrically.
	// 0.3.4: and the self-describing avatar block (hash + fetch path +
	// upload time) alongside it.
	preq, _ := http.NewRequest("GET", ts.URL+"/api/profile/self", nil)
	preq.SetBasicAuth("avuser@test.example", "avpassword1")
	pres, err := c.Do(preq)
	if err != nil {
		t.Fatalf("profile self: %v", err)
	}
	pb, _ := io.ReadAll(pres.Body)
	pres.Body.Close()
	if pres.StatusCode != http.StatusOK {
		t.Fatalf("profile self: %d %s", pres.StatusCode, pb)
	}
	// 0.3.3.4 cache hardening: payload endpoints carry the freshness
	// anchors, so they revalidate every use.
	if cc := pres.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("profile self cache-control = %q, want no-cache", cc)
	}
	var prof struct {
		AvatarHash string `json:"avatar_hash"`
		Avatar     *struct {
			Hash      string `json:"hash"`
			URL       string `json:"url"`
			UpdatedAt int64  `json:"updated_at"`
		} `json:"avatar"`
	}
	if err := json.Unmarshal(pb, &prof); err != nil {
		t.Fatalf("profile body: %v", err)
	}
	if prof.AvatarHash != up.AvatarHash {
		t.Fatalf("profile hash = %q, want %q", prof.AvatarHash, up.AvatarHash)
	}
	if prof.Avatar == nil {
		t.Fatalf("profile avatar block missing, want {hash,url,updated_at}")
	}
	if prof.Avatar.Hash != up.AvatarHash {
		t.Fatalf("avatar.hash = %q, want %q", prof.Avatar.Hash, up.AvatarHash)
	}
	if want := "/api/avatar/avuser@test.example?v=" + up.AvatarHash; prof.Avatar.URL != want {
		t.Fatalf("avatar.url = %q, want %q", prof.Avatar.URL, want)
	}
	if prof.Avatar.UpdatedAt <= 0 {
		t.Fatalf("avatar.updated_at = %d, want upload time", prof.Avatar.UpdatedAt)
	}

	// 3) GET serves the bytes with ETag/short max-age and honors If-None-Match.
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
	// 0.3.3.4 cache hardening: minute-level max-age, no immutable — a stale
	// ?v= reference must self-heal within minutes, never persist a year.
	cc := res.Header.Get("Cache-Control")
	if strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=300") {
		t.Fatalf("cache-control = %q, want max-age=300 without immutable", cc)
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

	// 6b) 0.3.4: after delete the profile/self avatar block is gone too
	// (absence = the generated-default signal, omitempty style).
	pres2, err := c.Do(preq)
	if err != nil {
		t.Fatalf("profile self after delete: %v", err)
	}
	pb2, _ := io.ReadAll(pres2.Body)
	pres2.Body.Close()
	if pres2.StatusCode != http.StatusOK {
		t.Fatalf("profile self after delete: %d %s", pres2.StatusCode, pb2)
	}
	var prof2 struct {
		AvatarHash string         `json:"avatar_hash"`
		Avatar     map[string]any `json:"avatar"`
	}
	if err := json.Unmarshal(pb2, &prof2); err != nil {
		t.Fatalf("profile body after delete: %v", err)
	}
	if prof2.AvatarHash != "" || prof2.Avatar != nil {
		t.Fatalf("after delete: avatar_hash=%q avatar=%v, want both absent", prof2.AvatarHash, prof2.Avatar)
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
	// 0.3.3.4: the guest face is minute-level too — no immutable anywhere.
	if strings.Contains(pub.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("public cache-control still immutable: %q", pub.Header.Get("Cache-Control"))
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
