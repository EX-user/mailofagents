package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestUpdatesLatestAndRead pins the modal contract end to end: the seed push
// (boss-approved copy, verbatim) is unread on first query, the exact title
// and body reach the client unmodified, and acknowledging clears unread.
func TestUpdatesLatestAndRead(t *testing.T) {
	ts, _ := newRegisterTestServer(t)

	get := func() map[string]any {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/updates/latest", nil)
		req.SetBasicAuth("admin@test.example", "adminpassword1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("latest = %d, want 200", resp.StatusCode)
		}
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	first := get()
	if first["unread"] != true {
		t.Fatalf("first unread = %v, want true", first["unread"])
	}
	push, _ := first["push"].(map[string]any)
	if push["version"] != "v0.3.1" {
		t.Fatalf("version = %v, want v0.3.1", push["version"])
	}
	if push["title"] != "Mail of Agents v0.3.1" {
		t.Fatalf("title = %v, want verbatim boss copy", push["title"])
	}
	wantBody := "· 优化连接图渲染逻辑及控件显示\n· worker: 终端窗口缩放即时重绘，不再错位花屏；增加鼠标交互控件\n· worker 心跳状态接入概览"
	if push["body_md"] != wantBody {
		t.Fatalf("body_md = %q, want verbatim boss copy", push["body_md"])
	}

	// Ack and re-query: unread flips false, figures drop to zero.
	id := int64(push["id"].(float64))
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/updates/read",
		strings.NewReader(`{"id":`+itoa64(id)+`}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin@test.example", "adminpassword1")
	ack, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer ack.Body.Close()
	if ack.StatusCode != http.StatusOK {
		t.Fatalf("read = %d, want 200", ack.StatusCode)
	}
	second := get()
	if second["unread"] != false || second["unread_more"] != float64(0) {
		t.Fatalf("after ack = %v/%v, want false/0", second["unread"], second["unread_more"])
	}

	// Unknown push ids are rejected on read.
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/updates/read", strings.NewReader(`{"id":999}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin@test.example", "adminpassword1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown id read = %d, want 404", resp.StatusCode)
	}
}

// TestUpdatesAdminCuration pins the admin split: non-admin writes are
// rejected, drafts stay off the self endpoints until published, and
// unread_more counts published pushes beyond the shown one.
func TestUpdatesAdminCuration(t *testing.T) {
	ts, st := newRegisterTestServer(t)
	if _, err := st.CreateAccountWithPassword("user", "test.example", false, "userpassword1"); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Non-admin write on the admin endpoint -> rejected.
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/admin/updates",
		strings.NewReader(`{"title":"x","body_md":"y"}`))
	req.SetBasicAuth("user@test.example", "userpassword1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("non-admin PUT succeeded, want rejection")
	}

	// Admin publishes a second push on top of the seed.
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/api/admin/updates",
		strings.NewReader(`{"version":"v0.3.2","title":"Mail of Agents v0.3.2","body_md":"· next","published":true}`))
	req.SetBasicAuth("admin@test.example", "adminpassword1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || created["ok"] != true {
		t.Fatalf("admin PUT = %d %v, want 200 ok", resp.StatusCode, created)
	}

	// Latest is now the newer push; the seed counts as "1 more".
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/updates/latest", nil)
	req.SetBasicAuth("admin@test.example", "adminpassword1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var latest map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&latest); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	push := latest["push"].(map[string]any)
	if push["version"] != "v0.3.2" {
		t.Fatalf("latest version = %v, want v0.3.2", push["version"])
	}
	if latest["unread_more"] != float64(1) {
		t.Fatalf("unread_more = %v, want 1", latest["unread_more"])
	}

	// Drafts never show on latest, but the admin list carries them.
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/api/admin/updates",
		strings.NewReader(`{"version":"v0.3.3","title":"draft only","body_md":"· wip"}`))
	req.SetBasicAuth("admin@test.example", "adminpassword1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("draft PUT = %d, want 200", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/updates/latest", nil)
	req.SetBasicAuth("admin@test.example", "adminpassword1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var still map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&still); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if still["push"].(map[string]any)["version"] != "v0.3.2" {
		t.Fatalf("draft leaked to latest: %v", still["push"])
	}
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/admin/updates", nil)
	req.SetBasicAuth("admin@test.example", "adminpassword1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Pushes []struct {
			Version   string `json:"version"`
			Published bool   `json:"published"`
		} `json:"pushes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(list.Pushes) != 3 {
		t.Fatalf("admin list = %d %v, want 3 pushes", resp.StatusCode, list.Pushes)
	}
	if !list.Pushes[0].Published {
		t.Fatalf("seed push lost published flag: %+v", list.Pushes[0])
	}
	if list.Pushes[2].Published {
		t.Fatalf("draft flagged published: %+v", list.Pushes[2])
	}
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [21]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
