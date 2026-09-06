//go:build darwin || linux

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package runtimehealth

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/agentd"
)

func serviceFixture(t *testing.T, platform string) (*PlatformService, *bool, *[]string) {
	t.Helper()
	home, homeErr := os.MkdirTemp("/tmp", "runtime-service-")
	if homeErr != nil {
		t.Fatal(homeErr)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	root := filepath.Join(home, "state")
	exe := filepath.Join(home, "paimos-agentd")
	if e := os.WriteFile(exe, []byte("fixture executable never invoked"), 0700); e != nil {
		t.Fatal(e)
	}
	name := "paimos-agentd-fixture.service"
	file := filepath.Join(home, name)
	if platform == "darwin" {
		name = "cm.paimos.agentd.fixture"
		file = filepath.Join(home, name+".plist")
	}
	args := []string{exe, "serve", "--instance", "fixture", "--state-root", root}
	var definition string
	if platform == "darwin" {
		var b strings.Builder
		b.WriteString(`<plist version="1.0"><dict><key>Label</key><string>` + name + `</string><key>ProgramArguments</key><array>`)
		for _, a := range args {
			b.WriteString("<string>")
			_ = xml.EscapeText(&b, []byte(a))
			b.WriteString("</string>")
		}
		b.WriteString(`</array><key>KeepAlive</key><true/></dict></plist>`)
		definition = b.String()
	} else {
		definition = "[Unit]\nDescription=fixture\n[Service]\nExecStart=" + strings.Join(args, " ") + "\nRestart=on-failure\n[Install]\nWantedBy=default.target\n"
	}
	putFixture(t, file, definition)
	p, e := NewPlatformService(platform, home, "fixture", root, file, name)
	if e != nil {
		t.Fatal(e)
	}
	running := false
	calls := []string{}
	p.Run = func(_ context.Context, command string, argv ...string) ([]byte, error) {
		call := command + " " + strings.Join(argv, " ")
		calls = append(calls, call)
		if platform == "darwin" {
			if argv[0] == "list" {
				if running {
					return []byte("PID Status Label\n222 0 " + name + "\n"), nil
				}
				return []byte("PID Status Label\n"), nil
			}
			if argv[0] == "bootstrap" || argv[0] == "kickstart" {
				running = true
				return nil, nil
			}
			if argv[0] == "bootout" {
				running = false
				return nil, nil
			}
		}
		if platform == "linux" {
			if argv[1] == "show" {
				pid := 0
				state := "inactive"
				if running {
					pid = 222
					state = "active"
				}
				return []byte(fmt.Sprintf("LoadState=loaded\nActiveState=%s\nMainPID=%d\nNRestarts=0\nFragmentPath=%s\nDropInPaths=\n", state, pid, file)), nil
			}
			if argv[1] == "start" {
				running = true
				return nil, nil
			}
			if argv[1] == "stop" {
				running = false
				return nil, nil
			}
		}
		return nil, errors.New("unexpected manager command")
	}
	return p, &running, &calls
}
func TestRuntimePlatformManagersVerifyDeclarativeOwnership(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			p, running, calls := serviceFixture(t, platform)
			before, _ := os.ReadFile(p.File)
			s, e := p.Inspect(context.Background())
			if e != nil || !s.Verified || s.Running {
				t.Fatal("fixture service not verified")
			}
			if p.Start(context.Background()) != nil || !*running {
				t.Fatal("service not started")
			}
			if p.Start(context.Background()) != nil {
				t.Fatal("idempotent start failed")
			}
			if p.Stop(context.Background()) != nil || *running {
				t.Fatal("service not stopped")
			}
			after, _ := os.ReadFile(p.File)
			if string(before) != string(after) {
				t.Fatal("declarative file overwritten")
			}
			for _, c := range *calls {
				if strings.Contains(c, "print") || strings.Contains(c, "kill") || strings.Contains(c, "enable") {
					t.Fatal("unexpected manager authority")
				}
			}
			if p.InstallAction() == "" {
				t.Fatal("installation action missing")
			}
		})
	}
}
func TestRuntimeServiceRejectsAmbiguousOrForeignDefinitions(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, kind := range []string{"instance", "wrapper", "duplicate", "mode"} {
			t.Run(platform+"/"+kind, func(t *testing.T) {
				p, _, calls := serviceFixture(t, platform)
				b, _ := os.ReadFile(p.File)
				s := string(b)
				switch kind {
				case "instance":
					s = strings.Replace(s, "--instance", "--foreign-instance", 1)
				case "wrapper":
					s = strings.Replace(s, "paimos-agentd", "vendor-wrapper", 1)
				case "duplicate":
					if platform == "linux" {
						s += "[Service]\nExecStart=/bin/true\n"
					} else {
						s = strings.Replace(s, "</dict>", "<key>Program</key><string>/bin/true</string></dict>", 1)
					}
				case "mode":
					_ = os.Chmod(p.File, 0666)
				}
				if kind != "mode" {
					putFixture(t, p.File, s)
				}
				if _, e := p.Inspect(context.Background()); e == nil {
					t.Fatal("unsafe service verified")
				}
				if len(*calls) != 0 {
					t.Fatal("manager reached with invalid definition")
				}
			})
		}
	}
}
func TestRuntimeHomeManagerSymlinkIsReadOnly(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			p, _, _ := serviceFixture(t, platform)
			original := p.File
			p.File = filepath.Join(filepath.Dir(original), "declarative-link")
			if e := os.Symlink(original, p.File); e != nil {
				t.Fatal(e)
			}
			if _, e := p.Inspect(context.Background()); e != nil {
				t.Fatal(e)
			}
			i, _ := os.Lstat(p.File)
			if i.Mode()&os.ModeSymlink == 0 {
				t.Fatal("declarative link replaced")
			}
		})
	}
}
func TestRuntimeLinuxDropInCannotWidenOwnership(t *testing.T) {
	p, _, _ := serviceFixture(t, "linux")
	p.Run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("LoadState=loaded\nMainPID=222\nActiveState=active\nDropInPaths=/unreviewed/override.conf\n"), nil
	}
	if _, e := p.Inspect(context.Background()); e == nil {
		t.Fatal("drop-in widened authority")
	}
}

func TestRuntimeServiceDefinitionChangesFailClosed(t *testing.T) {
	p, _, calls := serviceFixture(t, "linux")
	if _, e := p.Inspect(context.Background()); e != nil {
		t.Fatal(e)
	}
	raw, _ := os.ReadFile(p.File)
	putFixture(t, p.File, string(raw)+"# changed after review\n")
	before := len(*calls)
	if e := p.Start(context.Background()); e == nil || len(*calls) != before {
		t.Fatal("changed declaration executed")
	}
}
func TestRuntimeReporterURLMustMatchNamedDeployment(t *testing.T) {
	p, _, calls := serviceFixture(t, "linux")
	keyPath := filepath.Join(t.TempDir(), "reporter")
	putFixture(t, keyPath, "fixture key never read")
	raw, _ := os.ReadFile(p.File)
	s := strings.Replace(string(raw), "\nRestart=", " --report-host fixture-host --report-url https://other.example.invalid --report-api-key-file "+keyPath+"\nRestart=", 1)
	putFixture(t, p.File, s)
	p.ExpectedURL = "https://selected.example.invalid"
	if _, e := p.Inspect(context.Background()); e == nil || len(*calls) != 0 {
		t.Fatal("cross-instance reporter accepted")
	}
}

func TestRuntimePlatformSupportsFullNamedInstanceLength(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		if _, e := NewPlatformService(platform, t.TempDir(), strings.Repeat("x", 64), t.TempDir(), "", ""); e != nil {
			t.Fatal("valid named instance rejected")
		}
	}
}

func TestRuntimePlatformRecoveryScenarios(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, scenario := range []string{"stale_socket_lock", "corrupt_journal", "ownership_lost", "repeated_crash"} {
			t.Run(platform+"/"+scenario, func(t *testing.T) {
				manager, running, _ := serviceFixture(t, platform)
				daemon := &fixtureDaemon{unavailable: true, status: agentd.RuntimeStatus{Instance: "fixture", DaemonID: "fixture-generation"}}
				baseRun := manager.Run
				starts := 0
				manager.Run = func(ctx context.Context, command string, args ...string) ([]byte, error) {
					start := command == "launchctl" && (args[0] == "bootstrap" || args[0] == "kickstart") || command == "systemctl" && args[1] == "start"
					if start {
						starts++
						if scenario == "repeated_crash" {
							return nil, errors.New("fixture repeated crash")
						}
					}
					raw, e := baseRun(ctx, command, args...)
					daemon.unavailable = !*running
					if *running {
						daemon.status.PID = 222
						if start {
							daemon.status.Closed = false
						}
					} else {
						daemon.status.PID = 0
					}
					return raw, e
				}
				local, e := New(Config{Instance: "fixture", StateRoot: manager.StateRoot, Service: manager, Daemon: daemon, Remote: readyRemote, Wait: func(context.Context, time.Duration) error { return nil }})
				if e != nil {
					t.Fatal(e)
				}
				if e = local.pathsSafe(true); e != nil {
					t.Fatal(e)
				}
				switch scenario {
				case "stale_socket_lock":
					lock, e := lockState(local.directory, "agentd.lock")
					if e != nil {
						t.Fatal(e)
					}
					lock.Close()
					path := filepath.Join(local.directory, "agentd.sock")
					listener, e := net.Listen("unix", path)
					if e != nil {
						t.Fatal(e)
					}
					listener.(*net.UnixListener).SetUnlinkOnClose(false)
					_ = listener.Close()
					_ = os.Chmod(path, 0600)
					if _, e = local.Setup(context.Background()); e != nil || starts != 1 {
						t.Fatal("platform failed stale-state setup")
					}
				case "corrupt_journal":
					path := filepath.Join(local.directory, "sessions.journal")
					putFixture(t, path, "corrupt fixture\n")
					if _, e = local.Repair(context.Background()); e == nil || starts != 0 {
						t.Fatal("platform repair discarded corruption")
					}
					plan, e := local.PreviewReset(context.Background())
					if e != nil {
						t.Fatal(e)
					}
					reset, e := local.Reset(context.Background(), plan.Plan.Token)
					if e != nil {
						t.Fatal(e)
					}
					if _, e = safeFile(filepath.Join(reset.Archive, "quarantine", "sessions.journal"), false); e != nil {
						t.Fatal(e)
					}
				case "ownership_lost":
					*running = true
					daemon.unavailable = false
					daemon.status.PID = 222
					daemon.status.Sessions = []agentd.RuntimeSession{{ID: "old", PID: 444, State: agentd.StateOwnershipLost}, {ID: "owned", PID: 333, Owned: true, State: agentd.StateRunning}}
					if findLayer(t, local.Doctor(context.Background()), "stale_generations").State != ActionRequired {
						t.Fatal("platform lost-generation evidence hidden")
					}
					plan, e := local.PreviewReset(context.Background())
					if e != nil {
						t.Fatal(e)
					}
					if len(plan.Plan.Processes) != 2 {
						t.Fatal("platform adopted lost generation")
					}
					if _, e = local.Reset(context.Background(), plan.Plan.Token); e != nil {
						t.Fatal(e)
					}
					if daemon.status.Sessions[0].PID != 444 {
						t.Fatal("platform touched ownership-lost PID")
					}
				case "repeated_crash":
					if _, e = local.Repair(context.Background()); e == nil || starts != 3 || *running {
						t.Fatal("platform crash loop not bounded")
					}
					if _, e = safeFile(filepath.Join(local.directory, "runtime-attention.json"), false); e != nil {
						t.Fatal(e)
					}
				}
			})
		}
	}
}
