package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 0.3.7.4 (boss 10-15 12:38): registering a subordinate (single, batch, or
// register-team) makes the system send one intro letter from the new
// subordinate to its superior. The letter rides the hierarchy exemption, so
// it lands even when the superior's whitelist is enabled.
func TestSubordinateIntroLetterSingle(t *testing.T) {
	ts, _ := newRegisterTestServer(t)
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin@test.example:adminpassword1"))

	req, _ := http.NewRequest("POST", ts.URL+"/api/register-subordinate", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		Address string `json:"address"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || created.Address == "" {
		t.Fatalf("register-subordinate = %d, want 201 with address", resp.StatusCode)
	}

	// superior inbox: the intro letter is there, from the new subordinate
	req2, _ := http.NewRequest("GET", ts.URL+"/api/inbox", nil)
	req2.Header.Set("Authorization", auth)
	resp2, _ := http.DefaultClient.Do(req2)
	var inbox struct {
		Messages []struct {
			From    string `json:"from"`
			Subject string `json:"subject"`
		} `json:"messages"`
	}
	json.NewDecoder(resp2.Body).Decode(&inbox)
	resp2.Body.Close()
	found := false
	for _, m := range inbox.Messages {
		if m.From == created.Address && strings.HasPrefix(m.Subject, "报到：") {
			found = true
		}
	}
	if !found {
		t.Fatalf("intro letter missing in superior inbox: %+v", inbox.Messages)
	}
}

func TestSubordinateIntroLetterBatchOneEach(t *testing.T) {
	ts, _ := newRegisterTestServer(t)
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin@test.example:adminpassword1"))

	req, _ := http.NewRequest("POST", ts.URL+"/api/register-subordinate",
		strings.NewReader(`{"username":"intro-a,intro-b"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Created []struct {
			Address string `json:"address"`
		} `json:"created"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || len(out.Created) != 2 {
		t.Fatalf("batch register = %d created=%d, want 201 x2", resp.StatusCode, len(out.Created))
	}

	req2, _ := http.NewRequest("GET", ts.URL+"/api/inbox", nil)
	req2.Header.Set("Authorization", auth)
	resp2, _ := http.DefaultClient.Do(req2)
	var inbox struct {
		Messages []struct {
			From    string `json:"from"`
			Subject string `json:"subject"`
		} `json:"messages"`
	}
	json.NewDecoder(resp2.Body).Decode(&inbox)
	resp2.Body.Close()
	got := map[string]bool{}
	for _, m := range inbox.Messages {
		if strings.HasPrefix(m.Subject, "报到：") {
			got[m.From] = true
		}
	}
	for _, c := range out.Created {
		if !got[c.Address] {
			t.Fatalf("batch: no intro letter from %s", c.Address)
		}
	}
}
