//go:build darwin || linux

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package runtimehealth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
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

func fullServeArgs(p *PlatformService, extra ...string) []string {
	args := []string{filepath.Join(p.Home, "paimos-agentd"), "serve", "--instance", p.Instance, "--state-root", p.StateRoot}
	return append(args, extra...)
}

func reportingLifecycleArgs(key, lifecycle string, extra ...string) []string {
	args := []string{
		"--report-host", "fixture-host",
		"--report-url", "https://selected.example.invalid",
		"--report-api-key-file", key,
		"--lifecycle-config", lifecycle,
	}
	return append(args, extra...)
}

func putServiceArgs(t *testing.T, p *PlatformService, args []string) {
	t.Helper()
	var definition string
	if p.Platform == "darwin" {
		var b strings.Builder
		b.WriteString(`<plist version="1.0"><dict><key>Label</key><string>` + p.Name + `</string><key>ProgramArguments</key><array>`)
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
	putFixture(t, p.File, definition)
}

func browserGuardRefusalShim(t *testing.T, dir string) string {
	t.Helper()
	return writeBrowserGuardShim(t, filepath.Join(dir, "browser-refusal"), browserGuardExactShim(t))
}

func browserGuardExactShim(t *testing.T) string {
	t.Helper()
	return "#!" + trustedBrowserGuardInterpreter(t) + "\n" + browserGuardRefusalBody
}

func trustedBrowserGuardInterpreter(t *testing.T) string {
	t.Helper()
	for _, path := range []string{"/bin/sh", "/bin/bash", "/usr/bin/bash", "/usr/bin/sh"} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 || !trustedDefinitionOwner(info) {
			continue
		}
		base := filepath.Base(path)
		if base != "sh" && base != "bash" {
			continue
		}
		return path
	}
	t.Fatal("no trusted absolute sh/bash interpreter available for Darwin fixture")
	return ""
}

func writeBrowserGuardShim(t *testing.T, path, body string) string {
	t.Helper()
	if e := os.WriteFile(path, []byte(body), 0700); e != nil {
		t.Fatal(e)
	}
	return path
}

func declaredBrowserGuardEnv(shim string) map[string]string {
	return map[string]string{
		"INSPR_AGENT_BROWSER_GUARD":           "env-only",
		"PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH": shim,
		"PUPPETEER_EXECUTABLE_PATH":           shim,
		"CHROME_PATH":                         shim,
	}
}

func putDarwinServiceEnv(t *testing.T, p *PlatformService, args []string, env map[string]string) {
	t.Helper()
	if p.Platform != "darwin" {
		t.Fatal("guard environment contract is a LaunchAgent declaration")
	}
	var b strings.Builder
	b.WriteString(`<plist version="1.0"><dict><key>Label</key><string>` + p.Name + `</string><key>ProgramArguments</key><array>`)
	for _, a := range args {
		b.WriteString("<string>")
		_ = xml.EscapeText(&b, []byte(a))
		b.WriteString("</string>")
	}
	b.WriteString(`</array>`)
	if env != nil {
		b.WriteString(`<key>EnvironmentVariables</key><dict>`)
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			b.WriteString("<key>")
			_ = xml.EscapeText(&b, []byte(k))
			b.WriteString("</key><string>")
			_ = xml.EscapeText(&b, []byte(env[k]))
			b.WriteString("</string>")
		}
		b.WriteString(`</dict>`)
	}
	b.WriteString(`<key>KeepAlive</key><true/></dict></plist>`)
	putFixture(t, p.File, b.String())
}

func darwinPlistWithDuplicateEnvKey(p *PlatformService, args []string, shim string) string {
	var b strings.Builder
	b.WriteString(`<plist version="1.0"><dict><key>Label</key><string>` + p.Name + `</string><key>ProgramArguments</key><array>`)
	for _, a := range args {
		b.WriteString("<string>")
		_ = xml.EscapeText(&b, []byte(a))
		b.WriteString("</string>")
	}
	b.WriteString(`</array><key>EnvironmentVariables</key><dict>`)
	b.WriteString(`<key>INSPR_AGENT_BROWSER_GUARD</key><string>env-only</string>`)
	for _, key := range []string{"PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH", "PUPPETEER_EXECUTABLE_PATH", "CHROME_PATH", "CHROME_PATH"} {
		b.WriteString("<key>")
		_ = xml.EscapeText(&b, []byte(key))
		b.WriteString("</key><string>")
		_ = xml.EscapeText(&b, []byte(shim))
		b.WriteString("</string>")
	}
	b.WriteString(`</dict><key>KeepAlive</key><true/></dict></plist>`)
	return b.String()
}

func protectedDummy(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	putFixture(t, path, "fixture metadata only; never a credential")
	return path
}

func protectedExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if e := os.WriteFile(path, []byte("fixture executable never invoked"), 0700); e != nil {
		t.Fatal(e)
	}
	return path
}

func harnessArgs(t *testing.T, p *PlatformService, kind string) []string {
	t.Helper()
	key := protectedDummy(t, p.Home, "reporter-key")
	lifecycle := protectedDummy(t, p.Home, "lifecycle.json")
	extra := reportingLifecycleArgs(key, lifecycle)
	pi := protectedExecutable(t, p.Home, "pi")
	cursor := protectedExecutable(t, p.Home, "cursor-agent")
	codex := protectedExecutable(t, p.Home, "codex")
	switch kind {
	case "pi":
		extra = append(extra, "--pi-path", pi, "--pi-accounts", protectedDummy(t, p.Home, "pi-accounts.json"))
	case "cursor":
		extra = append(extra, "--cursor-path", cursor, "--cursor-accounts", protectedDummy(t, p.Home, "cursor-accounts.json"))
	case "both":
		extra = append(extra,
			"--pi-path", pi, "--pi-accounts", protectedDummy(t, p.Home, "pi-accounts.json"),
			"--cursor-path", cursor, "--cursor-accounts", protectedDummy(t, p.Home, "cursor-accounts.json"),
		)
	case "mixed":
		extra = append(extra,
			"--codex-path", codex, "--codex-accounts", protectedDummy(t, p.Home, "codex-accounts.json"),
			"--pi-path", pi, "--pi-accounts", protectedDummy(t, p.Home, "pi-accounts.json"),
			"--cursor-path", cursor, "--cursor-accounts", protectedDummy(t, p.Home, "cursor-accounts.json"),
		)
	case "pi-symlink":
		real := protectedExecutable(t, p.Home, "pi-real")
		link := filepath.Join(p.Home, "pi-link")
		if e := os.Symlink(real, link); e != nil {
			t.Fatal(e)
		}
		extra = append(extra, "--pi-path", link, "--pi-accounts", protectedDummy(t, p.Home, "pi-accounts.json"))
	case "cursor-symlink":
		real := protectedExecutable(t, p.Home, "cursor-real")
		link := filepath.Join(p.Home, "cursor-link")
		if e := os.Symlink(real, link); e != nil {
			t.Fatal(e)
		}
		extra = append(extra, "--cursor-path", link, "--cursor-accounts", protectedDummy(t, p.Home, "cursor-accounts.json"))
	default:
		t.Fatalf("unknown harness kind %q", kind)
	}
	return extra
}

func TestRuntimeServiceAcceptsMixedHarnessFlags(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, kind := range []string{"pi", "cursor", "both", "mixed", "pi-symlink", "cursor-symlink"} {
			t.Run(platform+"/"+kind, func(t *testing.T) {
				p, _, calls := serviceFixture(t, platform)
				putServiceArgs(t, p, fullServeArgs(p, harnessArgs(t, p, kind)...))
				s, e := p.Inspect(context.Background())
				if e != nil || !s.Verified {
					t.Fatalf("%s harness definition not verified: %v", kind, e)
				}
				if len(*calls) == 0 {
					t.Fatal("verified definition never reached the platform manager")
				}
			})
		}
	}
}

func TestRuntimeServiceRejectsHostileHarnessRegistry(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, harness := range []struct {
			name string
			flag string
		}{
			{"pi", "--pi-accounts"},
			{"cursor", "--cursor-accounts"},
		} {
			for _, kind := range []string{"missing", "empty", "duplicate", "relative", "symlink", "hardlink", "mode", "directory", "unknown", "ambiguous"} {
				t.Run(platform+"/"+harness.name+"-accounts/"+kind, func(t *testing.T) {
					p, _, calls := serviceFixture(t, platform)
					key := protectedDummy(t, p.Home, "reporter-key")
					lifecycle := protectedDummy(t, p.Home, "lifecycle.json")
					exe := protectedExecutable(t, p.Home, harness.name)
					accounts := filepath.Join(p.Home, harness.name+"-accounts.json")
					extra := reportingLifecycleArgs(key, lifecycle,
						"--"+harness.name+"-path", exe,
					)
					switch kind {
					case "missing":
						extra = append(extra, harness.flag, accounts)
					case "empty":
						extra = append(extra, harness.flag, "")
					case "duplicate":
						putFixture(t, accounts, "fixture metadata only; never a credential")
						extra = append(extra, harness.flag, accounts, harness.flag, accounts)
					case "relative":
						putFixture(t, accounts, "fixture metadata only; never a credential")
						extra = append(extra, harness.flag, harness.name+"-accounts.json")
					case "symlink":
						real := protectedDummy(t, p.Home, harness.name+"-accounts-real.json")
						if e := os.Symlink(real, accounts); e != nil {
							t.Fatal(e)
						}
						extra = append(extra, harness.flag, accounts)
					case "hardlink":
						real := protectedDummy(t, p.Home, harness.name+"-accounts-real.json")
						if e := os.Link(real, accounts); e != nil {
							t.Fatal(e)
						}
						extra = append(extra, harness.flag, accounts)
					case "mode":
						putFixture(t, accounts, "fixture metadata only; never a credential")
						if e := os.Chmod(accounts, 0644); e != nil {
							t.Fatal(e)
						}
						extra = append(extra, harness.flag, accounts)
					case "directory":
						if e := os.Mkdir(accounts, 0700); e != nil {
							t.Fatal(e)
						}
						extra = append(extra, harness.flag, accounts)
					case "unknown":
						putFixture(t, accounts, "fixture metadata only; never a credential")
						extra = append(extra, harness.flag, accounts, "--foreign-flag", "x")
					case "ambiguous":
						extra = append(extra, harness.flag)
					}
					putServiceArgs(t, p, fullServeArgs(p, extra...))
					if _, e := p.Inspect(context.Background()); e == nil {
						t.Fatal("hostile registry accepted")
					}
					if len(*calls) != 0 {
						t.Fatal("manager reached with invalid definition")
					}
				})
			}
		}
	}
}

func TestRuntimeServiceRejectsHostileHarnessExecutablePaths(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, harness := range []string{"pi", "cursor"} {
			for _, kind := range []string{"missing", "relative", "mode", "directory"} {
				t.Run(platform+"/"+harness+"/"+kind, func(t *testing.T) {
					p, _, calls := serviceFixture(t, platform)
					extra := harnessArgs(t, p, harness)
					flag := "--" + harness + "-path"
					missing := filepath.Join(p.Home, harness+"-missing")
					switch kind {
					case "missing":
						for i := 0; i < len(extra); i++ {
							if extra[i] == flag {
								extra[i+1] = missing
								break
							}
						}
					case "relative":
						for i := 0; i < len(extra); i++ {
							if extra[i] == flag {
								extra[i+1] = harness
								break
							}
						}
					case "mode":
						for i := 0; i < len(extra); i++ {
							if extra[i] == flag {
								if e := os.Chmod(extra[i+1], 0600); e != nil {
									t.Fatal(e)
								}
								break
							}
						}
					case "directory":
						dir := filepath.Join(p.Home, harness+"-dir")
						if e := os.Mkdir(dir, 0700); e != nil {
							t.Fatal(e)
						}
						for i := 0; i < len(extra); i++ {
							if extra[i] == flag {
								extra[i+1] = dir
								break
							}
						}
					}
					putServiceArgs(t, p, fullServeArgs(p, extra...))
					if _, e := p.Inspect(context.Background()); e == nil {
						t.Fatal("hostile executable path accepted")
					}
					if len(*calls) != 0 {
						t.Fatal("manager reached with invalid definition")
					}
				})
			}
		}
	}
}

func TestRuntimeServiceAcceptsReportingLifecycleWithOptionalCodexAccounts(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, kind := range []string{"legacy", "accounts"} {
			t.Run(platform+"/"+kind, func(t *testing.T) {
				p, _, calls := serviceFixture(t, platform)
				key := protectedDummy(t, p.Home, "reporter-key")
				lifecycle := protectedDummy(t, p.Home, "lifecycle.json")
				extra := reportingLifecycleArgs(key, lifecycle)
				if kind == "accounts" {
					extra = append(extra, "--codex-accounts", protectedDummy(t, p.Home, "codex-accounts.json"))
				}
				putServiceArgs(t, p, fullServeArgs(p, extra...))
				s, e := p.Inspect(context.Background())
				if e != nil || !s.Verified {
					t.Fatalf("reporting+lifecycle+%s definition not verified: %v", kind, e)
				}
				if len(*calls) == 0 {
					t.Fatal("verified definition never reached the platform manager")
				}
			})
		}
	}
}

func TestRuntimeServiceRejectsHostileCodexAccountsRegistry(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, kind := range []string{"missing", "empty", "duplicate", "relative", "symlink", "hardlink", "mode", "directory", "unknown", "ambiguous"} {
			t.Run(platform+"/"+kind, func(t *testing.T) {
				p, _, calls := serviceFixture(t, platform)
				key := protectedDummy(t, p.Home, "reporter-key")
				lifecycle := protectedDummy(t, p.Home, "lifecycle.json")
				accounts := filepath.Join(p.Home, "codex-accounts.json")
				extra := reportingLifecycleArgs(key, lifecycle)
				switch kind {
				case "missing":
					extra = append(extra, "--codex-accounts", accounts)
				case "empty":
					extra = append(extra, "--codex-accounts", "")
				case "duplicate":
					putFixture(t, accounts, "fixture metadata only; never a credential")
					extra = append(extra, "--codex-accounts", accounts, "--codex-accounts", accounts)
				case "relative":
					putFixture(t, accounts, "fixture metadata only; never a credential")
					extra = append(extra, "--codex-accounts", "codex-accounts.json")
				case "symlink":
					real := protectedDummy(t, p.Home, "accounts-real.json")
					if e := os.Symlink(real, accounts); e != nil {
						t.Fatal(e)
					}
					extra = append(extra, "--codex-accounts", accounts)
				case "hardlink":
					real := protectedDummy(t, p.Home, "accounts-real.json")
					if e := os.Link(real, accounts); e != nil {
						t.Fatal(e)
					}
					extra = append(extra, "--codex-accounts", accounts)
				case "mode":
					putFixture(t, accounts, "fixture metadata only; never a credential")
					if e := os.Chmod(accounts, 0644); e != nil {
						t.Fatal(e)
					}
					extra = append(extra, "--codex-accounts", accounts)
				case "directory":
					if e := os.Mkdir(accounts, 0700); e != nil {
						t.Fatal(e)
					}
					extra = append(extra, "--codex-accounts", accounts)
				case "unknown":
					putFixture(t, accounts, "fixture metadata only; never a credential")
					extra = append(extra, "--codex-accounts", accounts, "--foreign-flag", "x")
				case "ambiguous":
					extra = append(extra, "--codex-accounts")
				}
				putServiceArgs(t, p, fullServeArgs(p, extra...))
				if _, e := p.Inspect(context.Background()); e == nil {
					t.Fatal("hostile registry accepted")
				}
				if len(*calls) != 0 {
					t.Fatal("manager reached with invalid definition")
				}
			})
		}
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

func TestRuntimeManagerRefusesUnlistedExecutable(t *testing.T) {
	if _, err := runManager(context.Background(), "sh", "-c", "exit 0"); err == nil {
		t.Fatal("platform runner accepted an executable outside its closed manager set")
	}
}

func TestRuntimeDarwinAcceptsDeclaredBrowserGuardEnvironment(t *testing.T) {
	sum := sha256.Sum256([]byte(browserGuardRefusalBody))
	if hex.EncodeToString(sum[:]) != "abc22b56513f3a970aab18055aa60336ff65cdce27a113ce4408e43e7245627a" {
		t.Fatal("canonical refusal body is not the exact NIX-445 mkRefusalText")
	}
	p, _, calls := serviceFixture(t, "darwin")
	shim := browserGuardRefusalShim(t, p.Home)
	putDarwinServiceEnv(t, p, fullServeArgs(p), declaredBrowserGuardEnv(shim))
	s, e := p.Inspect(context.Background())
	if e != nil || !s.Verified {
		t.Fatalf("declared browser-guard environment not verified: %v", e)
	}
	if len(*calls) == 0 {
		t.Fatal("verified definition never reached the platform manager")
	}
}

func TestRuntimeBrowserGuardShimOpenUsesNoFollowAndDocumentedGosecWaiver(t *testing.T) {
	helper, err := os.ReadFile("private_unix.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(helper, []byte("os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 --")) {
		t.Fatal("trusted shim open lost no-follow G304 waiver")
	}
	if !bytes.Contains(helper, []byte("!os.SameFile(info, actual)")) {
		t.Fatal("trusted shim open lost SameFile identity check")
	}
	service, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(service, []byte("func verifyBrowserGuardRefusalShim"))
	if start < 0 {
		t.Fatal("browser-guard shim verifier missing")
	}
	rest := service[start+1:]
	endRel := bytes.Index(rest, []byte("\nfunc "))
	if endRel < 0 {
		t.Fatal("browser-guard shim verifier bounds missing")
	}
	body := service[start : start+1+endRel]
	if bytes.Contains(body, []byte("os.Open(")) || bytes.Contains(body, []byte("EvalSymlinks")) {
		t.Fatal("browser-guard shim verifier opened a variable path without no-follow custody")
	}
	if !bytes.Contains(body, []byte("openUnfollowedRegular(path, info)")) {
		t.Fatal("browser-guard shim verifier does not use the no-follow custody helper")
	}
	interpStart := bytes.Index(service, []byte("func verifyBrowserGuardInterpreter"))
	if interpStart < 0 {
		t.Fatal("browser-guard interpreter verifier missing")
	}
	interpRest := service[interpStart+1:]
	interpEndRel := bytes.Index(interpRest, []byte("\nfunc "))
	if interpEndRel < 0 {
		t.Fatal("browser-guard interpreter verifier bounds missing")
	}
	interpBody := service[interpStart : interpStart+1+interpEndRel]
	if bytes.Contains(interpBody, []byte("EvalSymlinks")) || !bytes.Contains(interpBody, []byte("os.Lstat(path)")) {
		t.Fatal("browser-guard interpreter verifier no longer refuses symlink interpreters")
	}
}

func TestRuntimeDarwinRejectsHostileLaunchdEnvironment(t *testing.T) {
	exact := browserGuardExactShim(t)
	interp := trustedBrowserGuardInterpreter(t)
	for _, kind := range []string{
		"path",
		"node_options",
		"dyld",
		"unknown",
		"missing-mode",
		"sandbox-mode",
		"empty",
		"relative",
		"mismatch",
		"duplicate-env-key",
		"missing-shim",
		"directory",
		"mode",
		"binary",
		"command-before-exit",
		"comment-marker",
		"comment-exit",
		"appended",
		"unsafe-interpreter",
		"interpreter-args",
		"relative-interpreter",
		"writable-interpreter",
		"symlink-shim",
		"symlink-interpreter",
		"no-shebang",
		"string-value",
		"program",
	} {
		t.Run(kind, func(t *testing.T) {
			p, _, calls := serviceFixture(t, "darwin")
			shim := filepath.Join(p.Home, "browser-refusal")
			env := declaredBrowserGuardEnv(shim)
			rawPlist := ""
			switch kind {
			case "path":
				writeBrowserGuardShim(t, shim, exact)
				env["PATH"] = "/tmp"
			case "node_options":
				writeBrowserGuardShim(t, shim, exact)
				env["NODE_OPTIONS"] = "--require /tmp/loader.js"
			case "dyld":
				writeBrowserGuardShim(t, shim, exact)
				env["DYLD_INSERT_LIBRARIES"] = "/tmp/loader.dylib"
			case "unknown":
				writeBrowserGuardShim(t, shim, exact)
				env["FOREIGN_KEY"] = "x"
			case "missing-mode":
				writeBrowserGuardShim(t, shim, exact)
				delete(env, "INSPR_AGENT_BROWSER_GUARD")
			case "sandbox-mode":
				writeBrowserGuardShim(t, shim, exact)
				env["INSPR_AGENT_BROWSER_GUARD"] = "sandbox"
			case "empty":
				writeBrowserGuardShim(t, shim, exact)
				env = map[string]string{}
			case "relative":
				writeBrowserGuardShim(t, shim, exact)
				env["CHROME_PATH"] = "browser-refusal"
				env["PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH"] = "browser-refusal"
				env["PUPPETEER_EXECUTABLE_PATH"] = "browser-refusal"
			case "mismatch":
				writeBrowserGuardShim(t, shim, exact)
				other := protectedExecutable(t, p.Home, "other-bin")
				env["CHROME_PATH"] = other
			case "duplicate-env-key":
				writeBrowserGuardShim(t, shim, exact)
				rawPlist = darwinPlistWithDuplicateEnvKey(p, fullServeArgs(p), shim)
			case "missing-shim":
			case "directory":
				if e := os.Mkdir(shim, 0700); e != nil {
					t.Fatal(e)
				}
			case "mode":
				writeBrowserGuardShim(t, shim, exact)
				if e := os.Chmod(shim, 0777); e != nil {
					t.Fatal(e)
				}
			case "binary":
				if e := os.WriteFile(shim, bytes.Repeat([]byte{0xcf, 0xfa, 0xed, 0xfe}, 1024), 0700); e != nil {
					t.Fatal(e)
				}
			case "command-before-exit":
				writeBrowserGuardShim(t, shim, "#!"+interp+"\ntrue\n"+browserGuardRefusalBody)
			case "comment-marker":
				writeBrowserGuardShim(t, shim, "#!"+interp+"\n# INSPR agent browser guard (NIX-445): native browser launch refused.\nexit 0\n")
			case "comment-exit":
				writeBrowserGuardShim(t, shim, "#!"+interp+"\n# exit 78\ntrue\n")
			case "appended":
				writeBrowserGuardShim(t, shim, exact+"true\n")
			case "unsafe-interpreter":
				writeBrowserGuardShim(t, shim, "#!/usr/bin/env bash\n"+browserGuardRefusalBody)
			case "interpreter-args":
				writeBrowserGuardShim(t, shim, "#!/bin/sh -e\n"+browserGuardRefusalBody)
			case "relative-interpreter":
				writeBrowserGuardShim(t, shim, "#!sh\n"+browserGuardRefusalBody)
			case "writable-interpreter":
				localInterp := filepath.Join(p.Home, "bash")
				if e := os.WriteFile(localInterp, []byte("#!/bin/sh\n"), 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.Chmod(localInterp, 0777); e != nil {
					t.Fatal(e)
				}
				writeBrowserGuardShim(t, shim, "#!"+localInterp+"\n"+browserGuardRefusalBody)
			case "symlink-shim":
				real := writeBrowserGuardShim(t, filepath.Join(p.Home, "browser-refusal-real"), exact)
				if e := os.Symlink(real, shim); e != nil {
					t.Fatal(e)
				}
			case "symlink-interpreter":
				link := filepath.Join(p.Home, "sh")
				if e := os.Symlink(interp, link); e != nil {
					t.Fatal(e)
				}
				writeBrowserGuardShim(t, shim, "#!"+link+"\n"+browserGuardRefusalBody)
			case "no-shebang":
				writeBrowserGuardShim(t, shim, browserGuardRefusalBody)
			case "string-value":
				rawPlist = `<plist version="1.0"><dict><key>Label</key><string>` + p.Name + `</string><key>ProgramArguments</key><array><string>` + filepath.Join(p.Home, "paimos-agentd") + `</string><string>serve</string><string>--instance</string><string>fixture</string><string>--state-root</string><string>` + p.StateRoot + `</string></array><key>EnvironmentVariables</key><string>CHROME_PATH=/tmp</string></dict></plist>`
			case "program":
				raw, _ := os.ReadFile(p.File)
				rawPlist = strings.Replace(string(raw), "</dict>", "<key>Program</key><string>/bin/true</string></dict>", 1)
			}
			if rawPlist != "" {
				putFixture(t, p.File, rawPlist)
			} else {
				putDarwinServiceEnv(t, p, fullServeArgs(p), env)
			}
			if _, e := p.Inspect(context.Background()); e == nil {
				t.Fatal("hostile environment accepted")
			}
			if len(*calls) != 0 {
				t.Fatal("manager reached with invalid definition")
			}
		})
	}
}

func TestRuntimeLinuxServiceRejectsEnvironmentOverride(t *testing.T) {
	p, _, calls := serviceFixture(t, "linux")
	raw, _ := os.ReadFile(p.File)
	putFixture(t, p.File, strings.Replace(string(raw), "ExecStart=", "Environment=PATH=/tmp\nExecStart=", 1))
	if _, e := p.Inspect(context.Background()); e == nil || len(*calls) != 0 {
		t.Fatal("linux environment override accepted")
	}
}
