package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestSearch pins the /api/search contract: case-insensitive substring over
// subject/from/to/body with inbox-style paging, box tri-state for self
// searches only, subordinate targets (both boxes, audited), and the 404
// masquerade rule for unknown/non-subordinate accounts.
func TestSearch(t *testing.T) {
	ts, _ := newRegisterTestServer(t)

	// Seed: admin self-sends three letters (each lands in admin's inbox AND
	// sent; ids differ per delivery), with case variety across fields.
	seed := func(subject, body string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/send",
			strings.NewReader(`{"to":["admin@test.example"],"subject":"`+subject+`","body":"`+body+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth("admin@test.example", "adminpassword1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("seed send %q = %d", subject, resp.StatusCode)
		}
	}
	seed("PaYrOl Report", "routine numbers")
	seed("lunch menu", "URGENT token inside")
	seed("third letter", "payrol lowercase body match")

	// A subordinate account (declares under admin) with its own mail.
	regReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/register",
		strings.NewReader(`{"name":"sub1","password":"subpassword1"}`))
	regReq.Header.Set("Content-Type", "application/json")
	regResp, err := http.DefaultClient.Do(regReq)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, regResp.Body)
	regResp.Body.Close()
	if regResp.StatusCode != http.StatusOK {
		t.Fatalf("register sub1 = %d", regResp.StatusCode)
	}
	subReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/subs",
		strings.NewReader(`{"superior":"admin@test.example"}`))
	subReq.Header.Set("Content-Type", "application/json")
	subReq.SetBasicAuth("sub1@test.example", "subpassword1")
	subResp, err := http.DefaultClient.Do(subReq)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, subResp.Body)
	subResp.Body.Close()
	if subResp.StatusCode != http.StatusOK && subResp.StatusCode != http.StatusCreated {
		t.Fatalf("sub1 declare = %d", subResp.StatusCode)
	}
	seedSub := func() {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/send",
			strings.NewReader(`{"to":["sub1@test.example"],"subject":"internal memo","body":"sub secret needle"}`))
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth("sub1@test.example", "subpassword1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	seedSub()

	call := func(query string, basicUser, basicPass string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/search?"+query, nil)
		req.SetBasicAuth(basicUser, basicPass)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return resp.StatusCode, out
	}
	ids := func(out map[string]any) []string {
		msgs, _ := out["messages"].([]any)
		var outIDs []string
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok {
				outIDs = append(outIDs, mm["id"].(string))
			}
		}
		return outIDs
	}

	// 1. Self both-mode: case-insensitive subject match, echo fields.
	code, out := call("q=payrol", "admin@test.example", "adminpassword1")
	if code != http.StatusOK {
		t.Fatalf("self search = %d", code)
	}
	if out["total_count"].(float64) < 2 || out["account"] != "admin@test.example" || out["box"] != "both" {
		t.Fatalf("self both: total=%v account=%v box=%v", out["total_count"], out["account"], out["box"])
	}
	if len(ids(out)) < 2 {
		t.Fatalf("self both ids = %v", ids(out))
	}

	// 2. Box tri-state: the same letter is found in either single box; ids
	// across in/out overlap (same delivery id) and dedup collapses both-mode.
	code, in := call("q=payrol&box=in", "admin@test.example", "adminpassword1")
	code2, outBox := call("q=payrol&box=out", "admin@test.example", "adminpassword1")
	if code != http.StatusOK || code2 != http.StatusOK {
		t.Fatalf("box calls = %d/%d", code, code2)
	}
	if in["box"] != "in" || outBox["box"] != "out" {
		t.Fatalf("box echo = %v/%v", in["box"], outBox["box"])
	}
	if len(ids(in)) == 0 || len(ids(outBox)) == 0 {
		t.Fatalf("box results empty: %v / %v", ids(in), ids(outBox))
	}

	// 3. Body match, case-insensitive.
	code, out = call("q=URGENT+token", "admin@test.example", "adminpassword1")
	if code != http.StatusOK || out["total_count"].(float64) < 1 {
		t.Fatalf("body match = %d %v", code, out["total_count"])
	}

	// 4. Pagination: limit+offset walks pages; past-end yields empty with the
	// same total.
	_, p0 := call("q=payrol&limit=1&offset=0", "admin@test.example", "adminpassword1")
	_, p1 := call("q=payrol&limit=1&offset=1", "admin@test.example", "adminpassword1")
	p0ids, p1ids := ids(p0), ids(p1)
	if len(p0ids) != 1 || len(p1ids) != 1 || p0ids[0] == p1ids[0] {
		t.Fatalf("paging: p0=%v p1=%v", p0ids, p1ids)
	}
	_, tail := call("q=payrol&limit=5&offset=99", "admin@test.example", "adminpassword1")
	if tail["total_count"] != p0["total_count"] || len(ids(tail)) != 0 {
		t.Fatalf("past-end = %v (ids %v)", tail["total_count"], ids(tail))
	}

	// 5. Subordinate target: both boxes scanned, box param ignored, audited.
	code, out = call("q=needle&account=sub1@test.example&box=in", "admin@test.example", "adminpassword1")
	if code != http.StatusOK {
		t.Fatalf("sub search = %d", code)
	}
	if out["account"] != "sub1@test.example" || out["box"] != "both" {
		t.Fatalf("sub echo = %v/%v", out["account"], out["box"])
	}
	if out["total_count"].(float64) < 1 {
		t.Fatalf("sub search total = %v", out["total_count"])
	}

	// 6. Masquerade: unknown and non-subordinate accounts answer 404.
	for _, acct := range []string{"ghost@test.example", "other@test.example"} {
		code, _ := call("q=x&account="+acct, "admin@test.example", "adminpassword1")
		if code != http.StatusNotFound {
			t.Fatalf("account=%s = %d, want 404", acct, code)
		}
	}

	// 7. Bad requests: missing q, bogus box.
	if code, _ = call("", "admin@test.example", "adminpassword1"); code != http.StatusBadRequest {
		t.Fatalf("missing q = %d, want 400", code)
	}
	if code, _ = call("q=x&box=sideways", "admin@test.example", "adminpassword1"); code != http.StatusBadRequest {
		t.Fatalf("bogus box = %d, want 400", code)
	}

	// 8. Sub-read audit trail: the subordinate search recorded ActionSubRead.
	aReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/audit?limit=100", nil)
	aReq.SetBasicAuth("admin@test.example", "adminpassword1")
	aResp, err := http.DefaultClient.Do(aReq)
	if err != nil {
		t.Fatal(err)
	}
	defer aResp.Body.Close()
	var auditOut struct {
		Entries []struct {
			Action string `json:"action"`
			Detail string `json:"detail"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(aResp.Body).Decode(&auditOut); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range auditOut.Entries {
		if strings.Contains(e.Detail, "sub-search target=sub1@test.example") {
			found = true
		}
	}
	if !found {
		t.Fatalf("sub-search audit entry missing in %d entries", len(auditOut.Entries))
	}
}
