// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestAccountLinkLaunchUsesPrivateLoginAndPrintsOneLine(t *testing.T) {
	origin := httptest.NewServer(http.NotFoundHandler())
	defer origin.Close()
	rt, stdout, stderr := heartbeatRuntime(t, origin)
	root, err := os.MkdirTemp("/tmp", "aeon-link-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "agentd.sock")
	const token = "12345678901234567890123456789012"
	if err = os.WriteFile(socket+".token", []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	home := filepath.Join(root, "vendor-login")
	t.Setenv("CODEX_HOME", home)
	var mu sync.Mutex
	var calls []agentd.AccountLinkRequest
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in agentd.AccountLinkRequest
		if r.URL.Path != "/v1/account-link" || r.Header.Get("Authorization") != "Bearer "+token || json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		calls = append(calls, in)
		n := len(calls)
		mu.Unlock()
		v := agentsetup.AccountLinkView{AccountID: "account", RequestID: "request", State: "pending", ShowPrompt: n == 1}
		if v.ShowPrompt {
			v.URI, v.Code = origin.URL+"/link", "482 913"
		}
		_ = json.NewEncoder(w).Encode(v)
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	o := heartbeatOptions{Harness: "codex", LinkSocket: socket}
	for range 2 {
		stop := rt.startAccountLink(t.Context(), o)
		stop()
	}
	if stderr.String() != "Link this account to you: "+origin.URL+"/link · code 482 913\n" || stdout.Len() != 0 {
		t.Fatal("prompt repeated or changed child stdout")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0].Home != home || calls[1].Home != home || calls[0].Harness != "codex" {
		t.Fatal("private selected login was not bound")
	}
}

type linkWatchClient struct {
	calls chan agentd.AccountLinkRequest
	view  agentsetup.AccountLinkView
}

func (c linkWatchClient) AccountLink(_ context.Context, in agentd.AccountLinkRequest) (agentsetup.AccountLinkView, error) {
	c.calls <- in
	return c.view, nil
}
func TestAccountLinkWatchReportsOnlyFirstLinkedResultAndStops(t *testing.T) {
	for _, show := range []bool{true, false} {
		var stderr bytes.Buffer
		rt := &runtime{stderr: &stderr}
		c := linkWatchClient{calls: make(chan agentd.AccountLinkRequest, 2), view: agentsetup.AccountLinkView{State: "linked", PersonName: "Markus", ShowResult: show}}
		in := agentd.AccountLinkRequest{Operation: "poll", AccountID: "account", RequestID: "request", Harness: "codex"}
		stop := rt.watchAccountLink(t.Context(), c, in, time.Millisecond)
		select {
		case got := <-c.calls:
			if got != in {
				t.Fatal("poll lost its account/request binding")
			}
		case <-time.After(time.Second):
			t.Fatal("pending offer was not watched")
		}
		stop()
		want := ""
		if show {
			want = "Linked to Markus\n"
		}
		if stderr.String() != want || len(c.calls) != 0 {
			t.Fatal("result repeated or watcher kept running")
		}
	}
}
