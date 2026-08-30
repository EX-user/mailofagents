package server

import (
	"net/http"
	"testing"
)

// TestStaticCacheHeaders: v0.1.7 stale-shell fix — every static asset and
// the index shell carry ETag + Cache-Control: no-cache, and a matching
// If-None-Match gets a bodyless 304.
func TestStaticCacheHeaders(t *testing.T) {
	ts, _ := newSiteCopyServer(t)

	get := func(path, inm string) (int, http.Header, string) {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		defer resp.Body.Close()
		body := make([]byte, 4096)
		n, _ := resp.Body.Read(body)
		return resp.StatusCode, resp.Header, string(body[:n])
	}

	// index shell: 200 + headers on first load
	code, hdr, _ := get("/", "")
	if code != http.StatusOK {
		t.Fatalf("index = %d", code)
	}
	if hdr.Get("Cache-Control") != "no-cache" {
		t.Fatalf("index Cache-Control = %q, want no-cache", hdr.Get("Cache-Control"))
	}
	etag := hdr.Get("ETag")
	if etag == "" {
		t.Fatal("index ETag missing")
	}

	// second load with the ETag -> 304 empty body
	code, _, body := get("/", etag)
	if code != http.StatusNotModified || body != "" {
		t.Fatalf("revalidate = %d %q, want 304 empty", code, body)
	}

	// static asset: 200 + ETag + no-cache, then 304 on revalidate
	code, hdr, _ = get("/static/i18n.js", "")
	if code != http.StatusOK || hdr.Get("ETag") == "" || hdr.Get("Cache-Control") != "no-cache" {
		t.Fatalf("i18n.js = %d ETag=%q CC=%q", code, hdr.Get("ETag"), hdr.Get("Cache-Control"))
	}
	code, _, _ = get("/static/i18n.js", hdr.Get("ETag"))
	if code != http.StatusNotModified {
		t.Fatalf("i18n.js revalidate = %d, want 304", code)
	}

	// stale/wrong ETag must get the full 200 body again
	code, _, _ = get("/static/i18n.js", `"bogus"`)
	if code != http.StatusOK {
		t.Fatalf("wrong ETag = %d, want 200", code)
	}

	// missing static file stays 404
	code, _, _ = get("/static/nope.js", "")
	if code != http.StatusNotFound {
		t.Fatalf("missing file = %d, want 404", code)
	}
}
