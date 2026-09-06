// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/runtimehealth"
	"github.com/zalando/go-keyring"
)

func runtimeRemoteFixture(t *testing.T, health string) (runtimehealth.RemoteProbe, *[]string) {
	t.Helper()
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != "GET" {
			t.Error("doctor mutated server")
		}
		switch r.URL.Path {
		case "/api/auth/me":
			fmt.Fprint(w, `{"user":{"id":1}}`)
		case "/api/health":
			fmt.Fprint(w, health)
		case "/api/ai/execution-options":
			_ = json.NewEncoder(w).Encode(map[string]any{"dispatch_profiles": dispatchprofile.List()})
		case "/api/projects/922/agents":
			fmt.Fprint(w, `[{"id":1,"project_id":922,"name":"fixture","body":"fixture-proprietary-payload"}]`)
		case "/api/projects/922/message-targets":
			fmt.Fprint(w, `{"targets":[{"target_ref":"fixture-target-private"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv(envURL, "")
	t.Setenv(envPPMURL, "")
	keyring.MockInit()
	if e := keyringSet("fixture", "fixture-credential-never-render"); e != nil {
		t.Fatal(e)
	}
	old := flagConfigPath
	flagConfigPath = filepath.Join(t.TempDir(), "config.yaml")
	t.Cleanup(func() { flagConfigPath = old })
	if e := os.WriteFile(flagConfigPath, []byte("instances:\n  fixture:\n    url: "+server.URL+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	return runtimeRemoteProbe("fixture", "fixture", 922), &paths
}
func TestRuntimeRemoteReadinessUsesNamedAuthorityAndRedacts(t *testing.T) {
	probe, _ := runtimeRemoteFixture(t, `{"agent_bus_instance":"fixture","deployment_instance":"fixture","agent_bus_identity_enforced":true}`)
	e := probe(context.Background())
	if e.Auth.State != runtimehealth.Known || e.Identity.State != runtimehealth.Known || e.Profiles.State != runtimehealth.Known || e.Agents.State != runtimehealth.Known || e.Targets.State != runtimehealth.Unknown {
		t.Fatal("remote readiness evidence wrong")
	}
	raw, _ := json.Marshal(e)
	for _, canary := range []string{"fixture-proprietary-payload", "fixture-target-private", "fixture-credential-never-render"} {
		if bytes.Contains(raw, []byte(canary)) {
			t.Fatal("private evidence leaked")
		}
	}
}
func TestRuntimeRemoteIdentityMismatchFailsClosed(t *testing.T) {
	probe, paths := runtimeRemoteFixture(t, `{"agent_bus_instance":"other","deployment_instance":"other","agent_bus_identity_enforced":true}`)
	e := probe(context.Background())
	if e.Identity.State != runtimehealth.ActionRequired {
		t.Fatal("wrong deployment accepted")
	}
	if len(*paths) != 2 {
		t.Fatal("queried project after identity mismatch")
	}
}
func TestRuntimeRemoteMissingAuthAndLegacyConfigAreReadOnly(t *testing.T) {
	probe, _ := runtimeRemoteFixture(t, `{}`)
	original := []byte("instances:\n  fixture:\n    url: https://example.invalid\n    api_key: fixture-legacy-private\n")
	if e := os.WriteFile(flagConfigPath, original, 0600); e != nil {
		t.Fatal(e)
	}
	out := probe(context.Background())
	current, _ := os.ReadFile(flagConfigPath)
	if !bytes.Equal(original, current) || out.Auth.State != runtimehealth.Unknown {
		t.Fatal("doctor migrated credentials")
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "fixture-legacy-private") {
		t.Fatal("credential rendered")
	}
}
func TestRuntimeCommandFamilyHasExplicitResetConfirmation(t *testing.T) {
	cmd := runtimeCmd()
	for _, name := range []string{"setup", "doctor", "repair", "reset"} {
		c, _, e := cmd.Find([]string{name})
		if e != nil || c.Name() != name {
			t.Fatal("missing runtime command")
		}
		if name == "reset" && c.Flags().Lookup("confirm") == nil {
			t.Fatal("reset confirmation missing")
		}
	}
}
