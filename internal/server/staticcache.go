package server

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// Static surface caching (v0.1.7 order): the panel shell used to ship with
// NO cache headers, so browsers could keep a stale index.html/app.js for a
// whole release cycle (the superior's "old shell" false reports). Every
// static asset now carries a strong ETag plus Cache-Control: no-cache — the
// browser must revalidate each load, and a matching If-None-Match gets a
// bodyless 304. URLs here are unfingerprinted, so long max-age would serve
// stale code; revalidation-with-304 is both correct and cheap.

const staticCacheControl = "no-cache"

func staticETag(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// etagMatch reports whether an If-None-Match header matches etag (handles
// comma lists and W/ weak prefixes).
func etagMatch(header, etag string) bool {
	for _, cand := range strings.Split(header, ",") {
		cand = strings.TrimSpace(cand)
		if strings.HasPrefix(cand, "W/") {
			cand = strings.TrimPrefix(cand, "W/")
		}
		if cand == etag || cand == "*" {
			return true
		}
	}
	return false
}

// writeCachedStatic writes one embedded static asset with cache headers.
func writeCachedStatic(w http.ResponseWriter, r *http.Request, data []byte, name string) {
	etag := staticETag(data)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", staticCacheControl)
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatch(inm, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write(data)
}

// handleStaticCached serves /static/* from the embedded FS with ETag +
// no-cache revalidation.
func handleStaticCached(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	name = strings.TrimPrefix(path.Clean("/"+name), "/")
	if name == "" {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(staticSubFS, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeCachedStatic(w, r, data, name)
}

// serveIndexCached serves the panel shell (index.html) with the same
// revalidate-always semantics: a new release must never be masked by a
// cached shell.
func serveIndexCached(w http.ResponseWriter, r *http.Request, data []byte) {
	writeCachedStatic(w, r, data, "index.html")
}
