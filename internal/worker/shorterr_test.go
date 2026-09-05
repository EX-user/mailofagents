package worker

import "testing"

// S8 ground truth (2026-09-05): the real deepseek zero-balance failure
// surfaces as "Insufficient Balance" — no "quota"/"429" token. Before the
// knife it fell through to the generic class; the board must show the
// quota class for a genuinely quota-exhausted account.
func TestShortErrQuotaClass(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Error: Insufficient Balance", "provider quota/429"},
		{"opencode: Insufficient balance for this request", "provider quota/429"},
		{"insufficient_quota: You exceeded your current quota (402)", "provider quota/429"},
		{"HTTP 429 too many requests", "provider quota/429"},
		{"账户使用上限已达", "provider quota/429"},
		{"余额不足，请充值", "provider quota/429"},
		{
			// S8 ground truth verbatim shape: APIError JSON on stdout —
			// response headers carry "connection", which must NOT hijack
			// a billing error into the network class.
			`opencode wake: exit status 1 code=1; stdout head: {"error":{"name":"APIError","data":{"message":"Insufficient Balance","statusCode":402,"responseHeaders":{"connection":"keep-alive"}}}}`,
			"provider quota/429",
		},
		{"dial tcp: connection refused", "network/tls unreachable"},
		{"exit status 1; stderr: oops", "exit status 1"},
	}
	for _, c := range cases {
		if got := shortErr(errString(c.in)); got != c.want {
			t.Errorf("shortErr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// errString is the error carrier shortErr actually inspects.
type errString string

func (e errString) Error() string { return string(e) }
