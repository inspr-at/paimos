// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/inspr-at/paimos/internal/openrouter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRouterSetupKeepsKeyLocal(t *testing.T) {
	const fake = "obviously-fake-openrouter-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/key" || r.Header.Get("Authorization") != "Bearer "+fake {
			t.Error("unexpected request")
		}
		fmt.Fprint(w, `{"data":{"usage":1,"limit":10,"limit_remaining":9}}`)
	}))
	defer server.Close()
	home, path, _ := piNodeFixture(t)
	d := Discovery{Home: home, Workspace: physicalTemp(t), LookPath: func(string) (string, error) { return path, nil }, Executor: executorFunc(func(_ context.Context, c Command) ([]byte, error) {
		if strings.Contains(strings.Join(c.Args, " ")+strings.Join(c.Env, " "), fake) {
			t.Error("key in child launch")
		}
		return []byte("0.87.1"), nil
	})}
	// A native fixture avoids interpreter discovery while checking argv/env.
	os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700)
	root := physicalTemp(t)
	c, err := d.PrepareOpenRouter(t.Context(), root, fake, openrouter.Client{Base: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), fake) || strings.Contains(string(raw), root) || c.Provider != "openrouter" {
		t.Fatal("public candidate leaked local binding")
	}
	info, err := os.Stat(filepath.Join(c.Home, "auth.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credential permissions")
	}
	if info, _ := os.Stat(c.Home); info.Mode().Perm() != 0700 {
		t.Fatal("profile permissions")
	}
	if _, err := OpenRouterCredits(t.Context(), c.Home, openrouter.Client{Base: server.URL}); err != nil {
		t.Fatal(err)
	}
	c2, err := d.PrepareOpenRouter(t.Context(), root, fake, openrouter.Client{Base: server.URL})
	if err != nil || c2.Home == c.Home {
		t.Fatal("accounts share profile")
	}
}
func TestOpenRouterKeyFileNeverExecutesAndRejectsUnsafePaths(t *testing.T) {
	dir := physicalTemp(t)
	p := filepath.Join(dir, "credential.fixture")
	for _, value := range []string{"OPENROUTER_API_KEY=obviously-fake-key\n", "OPENROUTER_API_KEY=$(touch should-not-exist)\n", "OTHER=value\n", "OPENROUTER_API_KEY=!security-command\n"} {
		if err := os.WriteFile(p, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		key, err := OpenRouterKeyFile(p)
		if strings.Contains(value, "obviously-fake") {
			if err != nil || key != "obviously-fake-key" {
				t.Fatal("literal rejected")
			}
		} else if err == nil {
			t.Fatal("nonliteral accepted")
		}
	}
	os.Chmod(p, 0644)
	if _, err := OpenRouterKeyFile(p); err == nil {
		t.Fatal("public credential accepted")
	}
	os.Chmod(p, 0600)
	link := filepath.Join(dir, "link")
	os.Symlink(p, link)
	if _, err := OpenRouterKeyFile(link); err == nil {
		t.Fatal("symlink accepted")
	}
}
