// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"fmt"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/openrouter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiOpenRouterLaunchPinsModelAndIsolatesKey(t *testing.T) {
	const fake = "obviously-fake-openrouter-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/key" || r.Header.Get("Authorization") != "Bearer "+fake {
			t.Error("unexpected key call")
		}
		fmt.Fprint(w, `{"data":{"usage":2,"limit":9,"limit_remaining":7}}`)
	}))
	defer server.Close()
	r := adapterRequest(t)
	r.Profile.Harness = Pi
	r.Profile.Model = "openrouter/stealth/test-alpha:free"
	r.Profile.Effort = "off"
	home := filepath.Join(r.StateRoot, "pi-local")
	store, err := agentsetup.OpenStore(home, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Write("auth.json", []byte(`{"openrouter":{"type":"api_key","key":"`+fake+`"}}`), true); err != nil {
		t.Fatal(err)
	}
	if err = store.Write("aeon-openrouter-profile", []byte("aeon.openrouter.v1"), true); err != nil {
		t.Fatal(err)
	}
	store.Close()
	path := filepath.Join(r.Workspace, "fixture-pi")
	script := `#!/bin/sh
[ -z "$OPENROUTER_API_KEY" ] && [ -z "$ANTHROPIC_API_KEY" ] && [ -z "$NODE_OPTIONS" ] || exit 8
if [ "$1" = --version ]; then echo 0.87.1; exit; fi
[ "$*" = '--mode rpc --no-session --provider openrouter --model stealth/test-alpha:free --thinking off' ] || exit 9
IFS= read -r line
printf '%s\n' '{"id":"1","type":"response","command":"get_state","success":true,"data":{"model":{"provider":"openrouter","id":"stealth/test-alpha:free"},"thinkingLevel":"off"}}'
IFS= read -r line
printf '%s\n' '{"id":"2","type":"response","command":"prompt","success":true}'
while IFS= read -r line; do :; done
`
	if err = os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a := NewPiAdapter(path, map[string]string{"account": home})
	a.SetExpectedProviders(map[string]string{"account": "openrouter"})
	a.OpenRouter = openrouter.Client{Base: server.URL}
	t.Setenv("OPENROUTER_API_KEY", "unrelated-fake-key")
	t.Setenv("ANTHROPIC_API_KEY", "unrelated-fake-key")
	t.Setenv("NODE_OPTIONS", "bad-option")
	p, err := a.Start(t.Context(), r, func(ev AdapterEvent) {
		if strings.Contains(fmt.Sprint(ev), fake) {
			t.Error("key escaped as event")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = p.Wait(); err != nil && !strings.Contains(err.Error(), "signal") {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "models.json"))
	if err != nil || strings.Contains(string(raw), fake) || !strings.Contains(string(raw), "stealth/test-alpha:free") {
		t.Fatal("public model config missing or contains credential")
	}
	a.probeMu.Lock()
	credits := a.probes["account"].credits
	a.probeMu.Unlock()
	if credits == nil || *credits.Usage != 2 {
		t.Fatal("safe credits missing")
	}
}
