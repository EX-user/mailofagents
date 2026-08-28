package server

import (
	"encoding/base64"
	"net/http"
	"testing"
)

// TestLivenessPullStampsOwnerInbox pins the weak-evidence expansion
// (contract addendum, superior ruling via Felix 01M0VFNX): the owner's own
// inbox pull stamps the liveness mark, while the superior's read-only
// browse through the subs path never does (Q2 invariant — viewing must
// not pollute the viewed account's liveness).
func TestLivenessPullStampsOwnerInbox(t *testing.T) {
	ts, st := newRegisterTestServer(t)

	mk := func(name string) (addr, pw string) {
		t.Helper()
		r, err := st.CreateAccount(name, "test.example", false)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return r.Address, r.Password
	}
	boss, bossPw := mk("boss")
	work, workPw := mk("worker")
	if err := st.DeclareSubordinate(boss, work); err != nil {
		t.Fatalf("declare: %v", err)
	}
	workAcc, err := st.GetAccount(work)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if got := st.LastReadAt(workAcc.UUID); got != 0 {
		t.Fatalf("precondition: lastread = %d, want 0", got)
	}

	get := func(path, addr, pw string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set("Authorization", "Basic "+
			base64.StdEncoding.EncodeToString([]byte(addr+":"+pw)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// 1. Owner's own pull stamps the mark (from 0 to ~now).
	if code := get("/api/inbox?limit=5", work, workPw); code != http.StatusOK {
		t.Fatalf("owner inbox pull = %d, want 200", code)
	}
	first := st.LastReadAt(workAcc.UUID)
	if first == 0 {
		t.Fatal("owner inbox pull did not stamp lastread")
	}

	// 2. Superior browsing the subordinate's inbox leaves the stamp alone.
	if code := get("/api/subs/"+work+"/messages?folder=inbox", boss, bossPw); code != http.StatusOK {
		t.Fatalf("superior subs browse = %d, want 200", code)
	}
	if got := st.LastReadAt(workAcc.UUID); got != first {
		t.Fatalf("superior browse moved lastread: %d -> %d", first, got)
	}

	// 3. Unauthenticated pulls obviously never stamp.
	if code := get("/api/inbox?limit=5", work, "wrong-password"); code != http.StatusUnauthorized {
		t.Fatalf("bad-auth pull = %d, want 401", code)
	}
	if got := st.LastReadAt(workAcc.UUID); got != first {
		t.Fatalf("bad-auth pull moved lastread: %d -> %d", first, got)
	}
}
