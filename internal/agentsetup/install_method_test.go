// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func link(t *testing.T, target, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// Risk: the add-harness command Settings offers names an entry point that
// does not reach the running daemon (AEON-733: Nix path on a Homebrew machine).
func TestInstallMethodNamesOnlyAnEntryPointThatReachesTheDaemon(t *testing.T) {
	for _, tc := range []struct {
		name string
		lay  func(t *testing.T, home, store string) string
		want string
	}{
		{"homebrew keg linked into the prefix", func(t *testing.T, home, _ string) string {
			physical := filepath.Join(home, "brew", "Cellar", "aeon-agentd", "v1", "bin", "aeon-agentd")
			serviceBinary(t, physical)
			link(t, physical, filepath.Join(home, "brew", "opt", "aeon-agentd", "bin", "aeon-agentd"))
			link(t, physical, filepath.Join(home, "brew", "bin", "aeon-agentd"))
			return filepath.Join(home, "brew", "opt", "aeon-agentd", "bin", "aeon-agentd")
		}, "homebrew"},
		{"unlinked homebrew keg has no prefix entry point", func(t *testing.T, home, _ string) string {
			physical := filepath.Join(home, "brew", "Cellar", "aeon-agentd", "v1", "bin", "aeon-agentd")
			serviceBinary(t, physical)
			return physical
		}, ""},
		{"nix profile reaches the store", func(t *testing.T, home, store string) string {
			running := filepath.Join(store, "aaa-aeon-agentd-1", "bin", "aeon-agentd")
			serviceBinary(t, running)
			newer := filepath.Join(store, "bbb-aeon-agentd-2", "bin", "aeon-agentd")
			serviceBinary(t, newer)
			link(t, newer, filepath.Join(home, ".nix-profile", "bin", "aeon-agentd"))
			return running
		}, "nix"},
		{"store binary without a profile entry point", func(t *testing.T, _ string, store string) string {
			running := filepath.Join(store, "aaa-aeon-agentd-1", "bin", "aeon-agentd")
			serviceBinary(t, running)
			return running
		}, ""},
		{"checksum link, also after an update", func(t *testing.T, home, _ string) string {
			running := filepath.Join(home, ".local", "lib", "aeon", "v1", "darwin-arm64", "paimos-agentd")
			serviceBinary(t, running)
			newer := filepath.Join(home, ".local", "lib", "aeon", "v2", "darwin-arm64", "paimos-agentd")
			serviceBinary(t, newer)
			link(t, newer, filepath.Join(home, ".local", "bin", "aeon-agentd"))
			return running
		}, "direct"},
		{"a link elsewhere is not the checksum install", func(t *testing.T, home, _ string) string {
			running := filepath.Join(home, "src", "aeon-agentd")
			serviceBinary(t, running)
			link(t, running, filepath.Join(home, ".local", "bin", "aeon-agentd"))
			return running
		}, ""},
		{"missing executable", func(_ *testing.T, home, _ string) string { return filepath.Join(home, "gone") }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			home, store := filepath.Join(root, "home"), filepath.Join(root, "store")
			executable := tc.lay(t, home, store)
			resolved, err := filepath.EvalSymlinks(store)
			if err != nil {
				resolved = store
			}
			if got := installMethod(executable, home, resolved); got != tc.want {
				t.Fatalf("install method = %q, want %q", got, tc.want)
			}
		})
	}
}

// Risk: an older strict server rejects the additive field and the lifecycle
// report stops, or a newer server never receives it.
func TestInstallMethodNegotiatesAndSurvivesServerDowngrade(t *testing.T) {
	e := &Engine{InstallMethod: "homebrew"}
	if got := e.reportInstall(View{ServerCapabilities: []string{LedgerCapability}}, &SetupProgress{State: "connected"}); got.InstallMethod != "" {
		t.Fatal("install method sent to a server that does not advertise it")
	}
	if got := e.reportInstall(View{ServerCapabilities: []string{LedgerCapability, InstallCapability}}, &SetupProgress{State: "connected"}); got.InstallMethod != "homebrew" {
		t.Fatal("advertised install method not reported")
	}
	if got := (&Engine{}).reportInstall(View{ServerCapabilities: []string{InstallCapability}}, &SetupProgress{State: "connected"}); got.InstallMethod != "" {
		t.Fatal("unknown install method invented")
	}

	var seen []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var proof struct {
			Progress map[string]any `json:"progress"`
		}
		if err := json.NewDecoder(r.Body).Decode(&proof); err != nil {
			t.Error(err)
			return
		}
		method, _ := proof.Progress["install_method"].(string)
		seen = append(seen, method)
		if method != "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "invalid_request"})
			return
		}
		_ = json.NewEncoder(w).Encode(View{SetupState: "connected"})
	}))
	defer server.Close()
	client := HTTPClient{Origin: server.URL, HTTP: server.Client()}
	progress := e.reportInstall(View{ServerCapabilities: []string{InstallCapability}}, &SetupProgress{State: "connected"})
	view, err := client.Reconcile(context.Background(), ProofRequest{Progress: progress})
	if err != nil || view.SetupState != "connected" || len(seen) != 2 || seen[0] != "homebrew" || seen[1] != "" || progress.InstallMethod != "homebrew" {
		t.Fatalf("downgrade blocked lifecycle or changed caller: %v %v", err, seen)
	}
}
