// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestAccountLinkPromptAndTerminalInjection(t *testing.T) {
	v := agentsetup.AccountLinkView{State: "pending", ShowPrompt: true, URI: "https://aeon.test/link", Code: "482 913"}
	line, err := AccountLinkPrompt(v)
	if err != nil || line != "Link this account to you: https://aeon.test/link · code 482 913" {
		t.Fatal("prompt is not one line")
	}
	for _, uri := range []string{"https://aeon.test/link?code=123456", "https://user:password@aeon.test/link", "http://outside.test/link", "https://aeon.test/link#code"} {
		v.URI = uri
		if _, err := AccountLinkPrompt(v); err == nil {
			t.Fatal("unsafe prompt accepted")
		}
	}
	if _, err := AccountLinkedLine(agentsetup.AccountLinkView{State: "linked", PersonName: "Markus\x1b[31m"}); err == nil {
		t.Fatal("terminal control accepted")
	}
}
func TestAccountLinkRemotePinsOriginAndAccount(t *testing.T) {
	const account = "11111111-1111-4111-8111-111111111111"
	var response agentsetup.AccountLinkView
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("invalid body")
		}
		if body["device_proof"] != strings.Repeat("f", 64) || body["account_id"] != account || body["operation"] != "offer" || len(body) != 3 {
			t.Error("binding missing or local fields uploaded")
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	r := NewRemote(server.URL, "fixture-key")
	if _, err := r.AccountLink(t.Context(), account, "offer", ""); err == nil || calls != 0 {
		t.Fatal("request sent without installation proof")
	}
	r.SetAccountLinkProof(strings.Repeat("f", 64))
	expires := time.Now().Add(time.Minute)
	response = agentsetup.AccountLinkView{AccountID: account, RequestID: account, State: "pending", ShowPrompt: true, URI: server.URL + "/link", Code: "482 913", ExpiresAt: &expires}
	if _, err := r.AccountLink(t.Context(), account, "offer", ""); err != nil {
		t.Fatal(err)
	}
	response.URI = "https://foreign.test/link"
	if _, err := r.AccountLink(t.Context(), account, "offer", ""); err == nil {
		t.Fatal("foreign origin accepted")
	}
	response.ShowPrompt = false
	response.AccountID = "different"
	if _, err := r.AccountLink(t.Context(), account, "offer", ""); err == nil {
		t.Fatal("different account accepted")
	}
}

type linkAPI struct {
	fakeAPI
	calls int
}

func (a *linkAPI) AccountLink(_ context.Context, id, op, request string) (agentsetup.AccountLinkView, error) {
	a.calls++
	return agentsetup.AccountLinkView{AccountID: id, State: "pending"}, nil
}

type linkAdapter struct {
	fakeAdapter
	available bool
}

func (a *linkAdapter) Probe(context.Context, string) bool { return a.available }
func TestAccountLinkUsesExistingEnrollmentAndProber(t *testing.T) {
	a := &linkAPI{}
	adapter := &linkAdapter{available: true}
	s := &Supervisor{api: a, daemonID: "daemon", accounts: []EnrolledAccount{{ID: "first", Key: "local", Harness: Codex}}, adapters: map[string]Adapter{Codex: adapter}}
	if _, err := s.AccountLink(t.Context(), AccountLinkRequest{Harness: Codex, Operation: "offer"}); err != nil || a.calls != 1 {
		t.Fatal("enrolled login not offered")
	}
	adapter.available = false
	if _, err := s.AccountLink(t.Context(), AccountLinkRequest{Harness: Codex, Operation: "offer"}); err == nil || a.calls != 1 {
		t.Fatal("unverified login offered")
	}
	adapter.available = true
	s.accounts = append(s.accounts, EnrolledAccount{ID: "second", Key: "other", Harness: Codex})
	if _, err := s.AccountLink(t.Context(), AccountLinkRequest{Harness: Codex, Operation: "offer"}); err == nil || a.calls != 1 {
		t.Fatal("shared login chosen implicitly")
	}
	if _, err := s.AccountLink(t.Context(), AccountLinkRequest{Harness: Codex, AccountID: "second", Operation: "offer"}); err != nil || a.calls != 2 {
		t.Fatal("explicit enrolled login refused")
	}
	if _, err := s.AccountLink(t.Context(), AccountLinkRequest{Harness: Codex, AccountID: "foreign", Operation: "offer"}); err == nil {
		t.Fatal("foreign enrollment offered")
	}
}
