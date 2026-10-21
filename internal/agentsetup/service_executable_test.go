// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func serviceBinary(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture executable"), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestStableServiceExecutable(t *testing.T) {
	for _, layout := range []string{"homebrew", "checksum"} {
		for _, goos := range []string{"darwin", "linux"} {
			t.Run(layout+"/"+goos, func(t *testing.T) {
				e, a, l, o, x := engineFixture(t)
				home := e.Services.Home
				physical := filepath.Join(home, "brew", "Cellar", "aeon-agentd", "v1", "bin", "aeon-agentd")
				stable := filepath.Join(home, "brew", "opt", "aeon-agentd", "bin", "aeon-agentd")
				if layout == "checksum" {
					physical = filepath.Join(home, ".local", "lib", "aeon", "v1", goos+"-arm64", "paimos-agentd")
					stable = filepath.Join(home, ".local", "bin", "aeon-agentd")
				}
				serviceBinary(t, physical)
				if _, err := ServiceExecutable(physical, home); err == nil {
					t.Fatal("missing stable link accepted")
				}
				if err := os.MkdirAll(filepath.Dir(stable), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(physical, stable); err != nil {
					t.Fatal(err)
				}
				for _, input := range []string{physical, stable} {
					got, err := ServiceExecutable(input, home)
					if err != nil || got != stable {
						t.Fatalf("stable path lost: %v", err)
					}
				}
				e.Services.Executable = stable
				e.Services.Platform.OS = goos
				e.Services.Systemctl = "/usr/bin/systemctl"
				o.Platform.OS, o.StartService = goos, true
				vendor := filepath.Join(home, "vendor-sign-in-sentinel")
				if err := os.WriteFile(vendor, []byte("preserve vendor state"), 0600); err != nil {
					t.Fatal(err)
				}
				p, err := e.Begin(t.Context(), o)
				if err != nil || p.Stage != "awaiting_approval" {
					t.Fatalf("begin: %s %v", p.Stage, err)
				}
				dir, name, _ := e.Services.location()
				unit := filepath.Join(dir, name)
				if _, err := os.Lstat(unit); !errors.Is(err, os.ErrNotExist) || x.active {
					t.Fatal("service installed before approval")
				}
				a.approved = true
				e.Now = func() time.Time { return time.Now().Add(time.Minute) }
				if _, err := e.Step(t.Context()); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(unit)
				if err != nil || !bytes.Contains(raw, []byte(stable)) || bytes.Contains(raw, []byte("Cellar")) || bytes.Contains(raw, []byte(physical)) {
					t.Fatal("unit did not preserve stable path")
				}
				// Updating the installation link must not change the receipt or
				// prevent the upgraded binary from draining/removing its service.
				next := strings.Replace(physical, "/v1/", "/v2/", 1)
				serviceBinary(t, next)
				if err := os.Remove(stable); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(next, stable); err != nil {
					t.Fatal(err)
				}
				if got, err := ServiceExecutable(next, home); err != nil || got != stable {
					t.Fatal("upgrade lost stable entry point")
				}
				// Maintenance invoked through a physical release path must still
				// remove only its receipted unit when the installer link is gone.
				if err := os.Rename(stable, stable+".unlinked"); err != nil {
					t.Fatal(err)
				}
				e.Services.Executable = next
				l.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "draining", Active: []string{"run"}}
				l.states[testAccount] = l.states[""]
				p, err = e.Disconnect(t.Context(), "")
				if err != nil || p.Stage != "draining" || !x.active {
					t.Fatal("service stopped before drain")
				}
				if a.view.ComputerState != "revoked" {
					t.Fatal("server access not revoked")
				}
				if _, err := os.Stat(unit); err != nil {
					t.Fatal("unit removed before drain")
				}
				l.states[""] = LocalStatus{DaemonID: "paired-daemon", State: "drained"}
				l.states[testAccount] = l.states[""]
				p, err = e.Step(t.Context())
				if err != nil || p.Stage != "disconnected" || x.active {
					t.Fatalf("cleanup: %s %v", p.Stage, err)
				}
				if _, err := os.Stat(unit); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unit retained")
				}
				if raw, err := os.ReadFile(vendor); err != nil || string(raw) != "preserve vendor state" {
					t.Fatal("vendor state changed")
				}
			})
		}
	}
}

func TestStableExecutableRefusesDifferentBinary(t *testing.T) {
	home := physicalTemp(t)
	physical := filepath.Join(home, "brew", "Cellar", "aeon-agentd", "v1", "bin", "aeon-agentd")
	other := filepath.Join(home, "other")
	stable := filepath.Join(home, "brew", "opt", "aeon-agentd", "bin", "aeon-agentd")
	serviceBinary(t, physical)
	serviceBinary(t, other)
	if err := os.MkdirAll(filepath.Dir(stable), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, stable); err != nil {
		t.Fatal(err)
	}
	if _, err := ServiceExecutable(physical, home); err == nil {
		t.Fatal("unrelated binary adopted")
	}
}

func TestHomebrewOptSurvivesUnlinkedOrConflictingBin(t *testing.T) {
	home := physicalTemp(t)
	prefix := filepath.Join(home, "brew")
	physical := filepath.Join(prefix, "Cellar", "aeon-agentd", "v1", "bin", "aeon-agentd")
	serviceBinary(t, physical)
	if err := os.MkdirAll(filepath.Join(prefix, "opt"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(filepath.Dir(physical)), filepath.Join(prefix, "opt", "aeon-agentd")); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(prefix, "opt", "aeon-agentd", "bin", "aeon-agentd")
	for _, conflict := range []bool{false, true} {
		if conflict {
			serviceBinary(t, filepath.Join(prefix, "bin", "aeon-agentd"))
		}
		got, err := ServiceExecutable(physical, home)
		if err != nil || got != want {
			t.Fatalf("opt path lost when bin conflict=%t: %v", conflict, err)
		}
		manager := ServiceManager{Executable: got, Home: home}
		if manager.managedExecutable() {
			t.Fatal("verified opt directory link treated as declarative")
		}
	}
}

func TestForeignUnitRefusedBeforeAndAfterApproval(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		for _, afterApproval := range []bool{false, true} {
			t.Run(goos+"/"+map[bool]string{true: "after", false: "before"}[afterApproval], func(t *testing.T) {
				e, a, _, o, x := engineFixture(t)
				e.Services.Platform.OS, e.Services.Systemctl = goos, "/usr/bin/systemctl"
				o.StartService = true
				if afterApproval {
					approveFixture(t, e, a, o)
				}
				dir, name, _ := e.Services.location()
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				unit := filepath.Join(dir, name)
				if err := os.WriteFile(unit, []byte("foreign unit"), 0600); err != nil {
					t.Fatal(err)
				}
				calls := len(x.calls)
				var err error
				if afterApproval {
					_, err = e.Disconnect(t.Context(), "")
				} else {
					_, err = e.Begin(t.Context(), o)
				}
				if !errors.Is(err, ErrServiceConflict) || len(x.calls) != calls {
					t.Fatal("foreign unit was controlled")
				}
				if raw, err := os.ReadFile(unit); err != nil || string(raw) != "foreign unit" {
					t.Fatal("foreign unit changed")
				}
			})
		}
	}
}

// Risk: rewriting the unit for a new exit code makes Install reject every
// receipt saved before that edit, so add-harness on a stopped daemon returns
// service_conflict. Both the logged and the legacy text must stay put.
func TestInstalledServiceUnitMatchesReceipt(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "agentd")
	const plistTail = `<key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>Umask</key><integer>63</integer></dict></plist>`
	for _, logs := range []bool{false, true} {
		for _, platform := range []Platform{{OS: "darwin", Arch: "arm64"}, {OS: "linux", Arch: "amd64"}} {
			raw, err := (ServiceManager{Platform: platform, Home: root, Executable: exe}).definitionLogs(root, logs)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			if strings.Contains(text, "SuccessExitStatus") || strings.Contains(text, "AEON-1596") || strings.Contains(text, "<!--") {
				t.Fatalf("logs=%v os=%s drifted from installed receipts", logs, platform.OS)
			}
			if platform.OS == "darwin" {
				glue := "</array>"
				if logs {
					glue = "stderr.log</string>"
					if !strings.Contains(text, "<key>StandardOutPath</key>") || !strings.Contains(text, "<key>StandardErrorPath</key>") {
						t.Fatal("log paths missing")
					}
				} else if strings.Contains(text, "StandardOutPath") || strings.Contains(text, "StandardErrorPath") {
					t.Fatal("legacy unit gained log paths")
				}
				if !strings.Contains(text, glue+plistTail) {
					t.Fatalf("logs=%v launchd receipt mismatch", logs)
				}
				continue
			}
			quoted := make([]string, 0, 4)
			for _, arg := range []string{exe, "serve", "--setup-root", root} {
				q, quoteErr := unitQuote(arg)
				if quoteErr != nil {
					t.Fatal(quoteErr)
				}
				quoted = append(quoted, q)
			}
			want := "[Unit]\nDescription=Aeon paired computer\n[Service]\nType=simple\nExecStart=" + strings.Join(quoted, " ") + "\nRestart=on-failure\nUMask=0077\nKillMode=none\nTimeoutStopSec=infinity\n[Install]\nWantedBy=default.target\n"
			if text != want {
				t.Fatalf("logs=%v systemd receipt mismatch:\n%s", logs, text)
			}
		}
	}
}
