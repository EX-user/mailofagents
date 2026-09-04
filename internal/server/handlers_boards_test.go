package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/agentmail/agentmail/internal/store"
)

// mkAccount registers a fresh account (name auto-suffixed to dodge
// conflicts across tests sharing a server) and returns address+password.
func mkAccount(t *testing.T, st *store.Store, name string) (string, string) {
	t.Helper()
	r, err := st.CreateAccount(fmt.Sprintf("%s-%d", name, acctSerial()), "test.example", false)
	if err != nil {
		t.Fatalf("create account %s: %v", name, err)
	}
	return r.Address, r.Password
}

var acctCounter int

func acctSerial() int {
	acctCounter++
	return acctCounter
}

// TestBoardCreateAndReadLifecycle pins the create → append → read lifecycle
// in both code modes, plus the no-param preamble-only default.
func TestBoardCreateAndReadLifecycle(t *testing.T) {
	ts, st := newRegisterTestServer(t)
	addr, pw := mkAccount(t, st, "boarder")

	// Single-code default: one full-power code.
	var single map[string]any
	if code := apiCall(t, "POST", ts.URL, "/api/boards", addr, pw,
		`{"name":"standup"}`, &single); code != http.StatusCreated {
		t.Fatalf("create single = %d, want 201", code)
	}
	if single["mode"] != "single" || single["write_code"] != nil {
		t.Fatalf("single mode wrong: %v", single)
	}
	full, _ := single["code"].(string)
	if len(full) != store.BoardCodeLen {
		t.Fatalf("code len = %d, want %d", len(full), store.BoardCodeLen)
	}

	// No params: preamble + meta only, no content key.
	var light map[string]any
	if code := apiCall(t, "GET", ts.URL, "/api/boards/"+full, "", "", "", &light); code != 200 {
		t.Fatalf("light read = %d, want 200", code)
	}
	if _, has := light["content"]; has {
		t.Fatalf("no-param read must not carry content: %v", light)
	}
	if light["name"] != "standup" {
		t.Fatalf("name = %v", light["name"])
	}

	// seq must never leak: append response and content lines carry none.
	for i := 0; i < 3; i++ {
		var out map[string]any
		line := fmt.Sprintf(`{"body":"line-%d"}`, i)
		if code := apiCall(t, "POST", ts.URL, "/api/boards/"+full+"/lines", addr, pw, line, &out); code != 200 {
			t.Fatalf("append %d = %d", i, code)
		}
		if _, has := out["seq"]; has {
			t.Fatalf("append response leaked seq: %v", out)
		}
	}
	var fullRead struct {
		Content []map[string]any `json:"content"`
		Lines   int              `json:"lines"`
	}
	if code := apiCall(t, "GET", ts.URL, "/api/boards/"+full+"?part=full", "", "", "", &fullRead); code != 200 {
		t.Fatalf("full read = %d", code)
	}
	if fullRead.Lines != 3 || len(fullRead.Content) != 3 {
		t.Fatalf("lines=%d content=%d, want 3/3", fullRead.Lines, len(fullRead.Content))
	}
	if fullRead.Content[0]["body"] != "line-0" || fullRead.Content[2]["body"] != "line-2" {
		t.Fatalf("content order wrong: %v", fullRead.Content)
	}
	for _, l := range fullRead.Content {
		if _, has := l["seq"]; has {
			t.Fatalf("content line leaked seq: %v", l)
		}
	}

	// Split mode: read and write codes differ; write-only code still reads
	// (v1 rule: either code reads), read code cannot append.
	var split map[string]any
	if code := apiCall(t, "POST", ts.URL, "/api/boards", addr, pw,
		`{"name":"announce","split_codes":true,"line_count":3}`, &split); code != http.StatusCreated {
		t.Fatalf("create split = %d, want 201", code)
	}
	rc, _ := split["read_code"].(string)
	wc, _ := split["write_code"].(string)
	if rc == "" || wc == "" || rc == wc {
		t.Fatalf("split codes wrong: %v", split)
	}
	if code := apiCall(t, "POST", ts.URL, "/api/boards/"+rc+"/lines", addr, pw, `{"body":"x"}`, nil); code != http.StatusForbidden {
		t.Fatalf("append via read code = %d, want 403", code)
	}
	if code := apiCall(t, "POST", ts.URL, "/api/boards/"+wc+"/lines", "", "", `{"body":"via-write"}`, nil); code != 200 {
		t.Fatalf("append via write code anon = %d, want 200", code)
	}
	var wcRead map[string]any
	if code := apiCall(t, "GET", ts.URL, "/api/boards/"+wc+"?part=full", "", "", "", &wcRead); code != 200 {
		t.Fatalf("read via write-only code = %d, want 200", code)
	}
	if code := apiCall(t, "GET", ts.URL, "/api/boards/nope123456", "", "", "", nil); code != http.StatusNotFound {
		t.Fatalf("unknown code = %d, want 404", code)
	}
}

// TestBoardOperators pins ?latest=N, ?match= (case-insensitive substring,
// filter-then-take-last composition) and the ?after 501 stub.
func TestBoardOperators(t *testing.T) {
	ts, st := newRegisterTestServer(t)
	addr, pw := mkAccount(t, st, "operator")

	var board map[string]any
	if code := apiCall(t, "POST", ts.URL, "/api/boards", addr, pw, `{"name":"ops"}`, &board); code != 201 {
		t.Fatalf("create = %d", code)
	}
	code, _ := board["code"].(string)
	bodies := []string{"alpha start", "Beta Middle", "gamma", "beta end", "omega"}
	for _, b := range bodies {
		if c := apiCall(t, "POST", ts.URL, "/api/boards/"+code+"/lines", "", "",
			fmt.Sprintf(`{"body":%q}`, b), nil); c != 200 {
			t.Fatalf("append %q = %d", b, c)
		}
	}

	var latest struct {
		Content []struct {
			Body string `json:"body"`
		} `json:"content"`
	}
	if c := apiCall(t, "GET", ts.URL, "/api/boards/"+code+"?latest=2", "", "", "", &latest); c != 200 {
		t.Fatalf("latest read = %d", c)
	}
	if len(latest.Content) != 2 || latest.Content[0].Body != "beta end" || latest.Content[1].Body != "omega" {
		t.Fatalf("latest=2 wrong: %v", latest.Content)
	}

	// match is case-insensitive; match+latest filters first, then takes last N.
	var matched struct {
		Content []struct {
			Body string `json:"body"`
		} `json:"content"`
	}
	if c := apiCall(t, "GET", ts.URL, "/api/boards/"+code+"?match=BETA", "", "", "", &matched); c != 200 {
		t.Fatalf("match read = %d", c)
	}
	if len(matched.Content) != 2 || matched.Content[0].Body != "Beta Middle" || matched.Content[1].Body != "beta end" {
		t.Fatalf("match wrong: %v", matched.Content)
	}
	if c := apiCall(t, "GET", ts.URL, "/api/boards/"+code+"?match=beta&latest=1", "", "", "", &matched); c != 200 {
		t.Fatalf("match+latest read = %d", c)
	}
	if len(matched.Content) != 1 || matched.Content[0].Body != "beta end" {
		t.Fatalf("match+latest wrong: %v", matched.Content)
	}
	// No hit: 200 with empty content (filter semantics, not an error).
	if c := apiCall(t, "GET", ts.URL, "/api/boards/"+code+"?match=zzz", "", "", "", &matched); c != 200 || len(matched.Content) != 0 {
		t.Fatalf("match miss = %d len=%d, want 200/0", c, len(matched.Content))
	}

	// after: frozen pending the owner's ruling — explicit 501 stub.
	var stub map[string]any
	if c := apiCall(t, "GET", ts.URL, "/api/boards/"+code+"?after=alpha", "", "", "", &stub); c != http.StatusNotImplemented {
		t.Fatalf("after stub = %d, want 501", c)
	}
	// latest=0 or negative: 400, never a silent full read.
	if c := apiCall(t, "GET", ts.URL, "/api/boards/"+code+"?latest=0", "", "", "", nil); c != 400 {
		t.Fatalf("latest=0 = %d, want 400", c)
	}
}

// TestBoardValidation pins the line-shape gates: length in runes, single
// line, non-empty — and the meta endpoint.
func TestBoardValidation(t *testing.T) {
	ts, st := newRegisterTestServer(t)
	addr, pw := mkAccount(t, st, "validator")

	var board map[string]any
	if code := apiCall(t, "POST", ts.URL, "/api/boards", addr, pw, `{"name":"v"}`, &board); code != 201 {
		t.Fatalf("create = %d", code)
	}
	code, _ := board["code"].(string)

	long := strings.Repeat("汉", store.BoardLineMaxRunes+1) // 401 runes ≈ 1203 bytes
	if c := apiCall(t, "POST", ts.URL, "/api/boards/"+code+"/lines", "", "",
		fmt.Sprintf(`{"body":%q}`, long), nil); c != 400 {
		t.Fatalf("401-rune line = %d, want 400", c)
	}
	exact := strings.Repeat("汉", store.BoardLineMaxRunes) // 400 runes fits
	if c := apiCall(t, "POST", ts.URL, "/api/boards/"+code+"/lines", "", "",
		fmt.Sprintf(`{"body":%q}`, exact), nil); c != 200 {
		t.Fatalf("400-rune line = %d, want 200", c)
	}
	if c := apiCall(t, "POST", ts.URL, "/api/boards/"+code+"/lines", "", "", `{"body":"two\nlines"}`, nil); c != 400 {
		t.Fatalf("multiline = %d, want 400", c)
	}
	if c := apiCall(t, "POST", ts.URL, "/api/boards/"+code+"/lines", "", "", `{"body":""}`, nil); c != 400 {
		t.Fatalf("empty = %d, want 400", c)
	}
	if c := apiCall(t, "POST", ts.URL, "/api/boards", addr, pw, `{"name":""}`, nil); c != 400 {
		t.Fatalf("empty name = %d, want 400", c)
	}
	if c := apiCall(t, "POST", ts.URL, "/api/boards", addr, pw, `{"name":"x","line_count":501}`, nil); c != 400 {
		t.Fatalf("line_count=501 = %d, want 400", c)
	}

	var meta map[string]any
	if c := apiCall(t, "GET", ts.URL, "/api/boards/"+code+"/meta", "", "", "", &meta); c != 200 {
		t.Fatalf("meta = %d", c)
	}
	if meta["lines"] != float64(1) || meta["name"] != "v" {
		t.Fatalf("meta wrong: %v", meta)
	}
}

// TestBoardRolling pins the rolling cap: past line_count the oldest lines
// drop, counts stay exact, and the cap never resurrects dropped content.
func TestBoardRolling(t *testing.T) {
	ts, st := newRegisterTestServer(t)
	addr, pw := mkAccount(t, st, "roller")

	var board map[string]any
	if code := apiCall(t, "POST", ts.URL, "/api/boards", addr, pw,
		`{"name":"roll","line_count":3}`, &board); code != 201 {
		t.Fatalf("create = %d", code)
	}
	code, _ := board["code"].(string)
	for i := 0; i < 5; i++ {
		if c := apiCall(t, "POST", ts.URL, "/api/boards/"+code+"/lines", "", "",
			fmt.Sprintf(`{"body":"n-%d"}`, i), nil); c != 200 {
			t.Fatalf("append %d = %d", i, c)
		}
	}
	var read struct {
		Lines   int `json:"lines"`
		Content []struct {
			Body string `json:"body"`
		} `json:"content"`
	}
	if c := apiCall(t, "GET", ts.URL, "/api/boards/"+code+"?part=full", "", "", "", &read); c != 200 {
		t.Fatalf("read = %d", c)
	}
	if read.Lines != 3 || len(read.Content) != 3 {
		t.Fatalf("after rolling lines=%d content=%d, want 3/3", read.Lines, len(read.Content))
	}
	if read.Content[0].Body != "n-2" || read.Content[2].Body != "n-4" {
		t.Fatalf("rolling kept wrong lines: %v", read.Content)
	}
}

// TestBoardPreambleAndDelete pins creator-only preamble rewrite and
// creator-or-admin delete.
func TestBoardPreambleAndDelete(t *testing.T) {
	ts, st := newRegisterTestServer(t)
	owner, ownerPw := mkAccount(t, st, "owner")
	other, otherPw := mkAccount(t, st, "other")

	var board map[string]any
	if code := apiCall(t, "POST", ts.URL, "/api/boards", owner, ownerPw,
		`{"name":"pd","split_codes":true}`, &board); code != 201 {
		t.Fatalf("create = %d", code)
	}
	wc, _ := board["write_code"].(string)

	// Preamble: creator with the write code → ok.
	if c := apiCall(t, "POST", ts.URL, "/api/boards/"+wc+"/preamble", owner, ownerPw,
		`{"body":"board rules: be kind"}`, nil); c != 200 {
		t.Fatalf("owner preamble = %d, want 200", c)
	}
	// A write code is not ownership: another account holding it → 403.
	if c := apiCall(t, "POST", ts.URL, "/api/boards/"+wc+"/preamble", other, otherPw,
		`{"body":"hijack"}`, nil); c != http.StatusForbidden {
		t.Fatalf("other preamble = %d, want 403", c)
	}
	// Anonymous (code only, no account) → 401.
	if c := apiCall(t, "POST", ts.URL, "/api/boards/"+wc+"/preamble", "", "",
		`{"body":"anon"}`, nil); c != http.StatusUnauthorized {
		t.Fatalf("anon preamble = %d, want 401", c)
	}
	// Multi-line preamble → 400.
	if c := apiCall(t, "POST", ts.URL, "/api/boards/"+wc+"/preamble", owner, ownerPw,
		`{"body":"two\nlines"}`, nil); c != 400 {
		t.Fatalf("multiline preamble = %d, want 400", c)
	}
	var read map[string]any
	if c := apiCall(t, "GET", ts.URL, "/api/boards/"+wc, "", "", "", &read); c != 200 || read["preamble"] != "board rules: be kind" {
		t.Fatalf("preamble read = %d %v", c, read["preamble"])
	}

	// Delete: non-owner account → 403; admin → ok; codes die with the board.
	if c := apiCall(t, "DELETE", ts.URL, "/api/boards/"+wc, other, otherPw, "", nil); c != http.StatusForbidden {
		t.Fatalf("other delete = %d, want 403", c)
	}
	if c := apiCall(t, "DELETE", ts.URL, "/api/boards/"+wc, "admin@test.example", "adminpassword1", "", nil); c != 200 {
		t.Fatalf("admin delete = %d, want 200", c)
	}
	if c := apiCall(t, "GET", ts.URL, "/api/boards/"+wc, "", "", "", nil); c != http.StatusNotFound {
		t.Fatalf("read after delete = %d, want 404", c)
	}
	if c := apiCall(t, "POST", ts.URL, "/api/boards/"+wc+"/lines", "", "", `{"body":"x"}`, nil); c != http.StatusNotFound {
		t.Fatalf("append after delete = %d, want 404", c)
	}
}

// TestBoardMineAndQuota pins /mine's owner view and the 200-boards-per-
// account ceiling (boss parameter, enforced inside the create txn).
func TestBoardMineAndQuota(t *testing.T) {
	ts, st := newRegisterTestServer(t)
	addr, pw := mkAccount(t, st, "quota")

	var first map[string]any
	if code := apiCall(t, "POST", ts.URL, "/api/boards", addr, pw,
		`{"name":"first","split_codes":true}`, &first); code != 201 {
		t.Fatalf("create = %d", code)
	}
	for i := 0; i < store.BoardsPerAccount-1; i++ {
		if code := apiCall(t, "POST", ts.URL, "/api/boards", addr, pw,
			fmt.Sprintf(`{"name":"b%d"}`, i), nil); code != 201 {
			t.Fatalf("create %d = %d", i, code)
		}
	}
	if code := apiCall(t, "POST", ts.URL, "/api/boards", addr, pw, `{"name":"over"}`, nil); code != http.StatusConflict {
		t.Fatalf("201st board = %d, want 409", code)
	}

	var mine struct {
		Boards []map[string]any `json:"boards"`
		Used   int              `json:"used"`
		Max    int              `json:"max"`
	}
	if code := apiCall(t, "GET", ts.URL, "/api/boards/mine", addr, pw, "", &mine); code != 200 {
		t.Fatalf("mine = %d", code)
	}
	if mine.Used != store.BoardsPerAccount || mine.Max != store.BoardsPerAccount {
		t.Fatalf("used=%d max=%d", mine.Used, mine.Max)
	}
	if mine.Boards[0]["write_code"] == nil {
		t.Fatalf("owner view must show split codes: %v", mine.Boards[0])
	}

	// Another account sees no boards and no shared quota pressure.
	other, otherPw := mkAccount(t, st, "fresh")
	var theirMine struct {
		Used int `json:"used"`
	}
	if code := apiCall(t, "GET", ts.URL, "/api/boards/mine", other, otherPw, "", &theirMine); code != 200 || theirMine.Used != 0 {
		t.Fatalf("fresh mine = %d used=%d", code, theirMine.Used)
	}
	if code := apiCall(t, "POST", ts.URL, "/api/boards", other, otherPw, `{"name":"ok"}`, nil); code != 201 {
		t.Fatalf("fresh create = %d, want 201 (quota is per account)", code)
	}
}

// TestBoardRateLimits pins v1.2 point 6: 10 lines/min/code and the 30/min
// board ceiling; the per-account window gates only credentialled appends.
func TestBoardRateLimits(t *testing.T) {
	ts, st := newRegisterTestServer(t)
	addr, pw := mkAccount(t, st, "rated")

	var board map[string]any
	if code := apiCall(t, "POST", ts.URL, "/api/boards", addr, pw,
		`{"name":"fast","split_codes":true}`, &board); code != 201 {
		t.Fatalf("create = %d", code)
	}
	wc, _ := board["write_code"].(string)

	// Anonymous appends through one code: the 11th inside the minute window
	// hits the per-code limit (board ceiling 30 not yet reached).
	for i := 0; i < boardCodeAppendPerMin; i++ {
		if c := apiCall(t, "POST", ts.URL, "/api/boards/"+wc+"/lines", "", "",
			fmt.Sprintf(`{"body":"m-%d"}`, i), nil); c != 200 {
			t.Fatalf("append %d = %d, want 200", i, c)
		}
	}
	if c := apiCall(t, "POST", ts.URL, "/api/boards/"+wc+"/lines", "", "", `{"body":"over"}`, nil); c != http.StatusTooManyRequests {
		t.Fatalf("11th append = %d, want 429", c)
	}

	// Per-account window, disambiguated from the code window: ten
	// AUTHENTICATED appends fill the account window AND the first board's
	// code window; an 11th through a FRESH board's fresh code still 429s —
	// only the account window is exhausted.
	var second map[string]any
	if code := apiCall(t, "POST", ts.URL, "/api/boards", addr, pw,
		`{"name":"fast2"}`, &second); code != 201 {
		t.Fatalf("create 2 = %d", code)
	}
	code2, _ := second["code"].(string)
	for i := 0; i < boardAcctAppendPerMin; i++ {
		if c := apiCall(t, "POST", ts.URL, "/api/boards/"+code2+"/lines", addr, pw,
			fmt.Sprintf(`{"body":"a-%d"}`, i), nil); c != 200 {
			t.Fatalf("authed append %d = %d, want 200", i, c)
		}
	}
	var third map[string]any
	if code := apiCall(t, "POST", ts.URL, "/api/boards", addr, pw,
		`{"name":"fast3"}`, &third); code != 201 {
		t.Fatalf("create 3 = %d", code)
	}
	code3, _ := third["code"].(string)
	if c := apiCall(t, "POST", ts.URL, "/api/boards/"+code3+"/lines", addr, pw, `{"body":"over-acct"}`, nil); c != http.StatusTooManyRequests {
		t.Fatalf("fresh-code append after 10 authed = %d, want 429 (per account)", c)
	}
	// The same code stays usable anonymously (no account window on it).
	other, otherPw := mkAccount(t, st, "bystander")
	_ = otherPw
	if c := apiCall(t, "POST", ts.URL, "/api/boards/"+code3+"/lines", other, otherPw, `{"body":"other-acct"}`, nil); c != 200 {
		t.Fatalf("other account through fresh code = %d, want 200 (window is per account)", c)
	}
}

// TestBoardInfoPublicAndAdminPage pins the public self-description and the
// admin pagination (meta only, codes never leave the owner).
func TestBoardInfoPublicAndAdminPage(t *testing.T) {
	ts, st := newRegisterTestServer(t)
	addr, pw := mkAccount(t, st, "paged")

	for i := 0; i < 3; i++ {
		if code := apiCall(t, "POST", ts.URL, "/api/boards", addr, pw,
			fmt.Sprintf(`{"name":"p%d"}`, i), nil); code != 201 {
			t.Fatalf("create %d = %d", i, code)
		}
	}
	var info map[string]any
	if code := apiCall(t, "GET", ts.URL, "/api/boards/info", "", "", "", &info); code != 200 {
		t.Fatalf("info = %d, want 200 (public)", code)
	}
	if info["feature"] != "boards" {
		t.Fatalf("info wrong: %v", info)
	}

	var page1 struct {
		Boards []map[string]any `json:"boards"`
		Total  int              `json:"total"`
		Page   int              `json:"page"`
	}
	if code := apiCall(t, "GET", ts.URL, "/api/admin/boards?size=2&page=1", "admin@test.example", "adminpassword1", "", &page1); code != 200 {
		t.Fatalf("admin page1 = %d", code)
	}
	if page1.Total != 3 || len(page1.Boards) != 2 || page1.Page != 1 {
		t.Fatalf("page1 = total %d boards %v", page1.Total, page1.Boards)
	}
	if _, has := page1.Boards[0]["read_code"]; has {
		t.Fatalf("admin listing leaked codes: %v", page1.Boards[0])
	}
	if page1.Boards[0]["owner"] == nil || page1.Boards[0]["bytes"] == nil {
		t.Fatalf("admin listing missing meta: %v", page1.Boards[0])
	}
	var page2 struct {
		Boards []map[string]any `json:"boards"`
		Total  int              `json:"total"`
	}
	if code := apiCall(t, "GET", ts.URL, "/api/admin/boards?size=2&page=2", "admin@test.example", "adminpassword1", "", &page2); code != 200 {
		t.Fatalf("admin page2 = %d", code)
	}
	if page2.Total != 3 || len(page2.Boards) != 1 {
		t.Fatalf("page2 = total %d boards %d", page2.Total, len(page2.Boards))
	}
	// Admin endpoint is admin-gated.
	if code := apiCall(t, "GET", ts.URL, "/api/admin/boards", addr, pw, "", nil); code != http.StatusUnauthorized {
		t.Fatalf("admin page as account = %d, want 401", code)
	}
}
