// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux || darwin

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/rules"
	"golang.org/x/sys/unix"
)

const testSession = "10000000-0000-4000-8000-000000000006"
const testRequest = "10000000-0000-4000-8000-000000000007"
const otherID = "10000000-0000-4000-8000-000000000008"

type fixture struct {
	t      *testing.T
	root   string
	b      binding
	o      options
	floor  []byte
	bundle rules.Merged
}

func write(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func encode(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// These are synthetic fixtures only, never a real binding or session.
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "tmp", "aeon-rules", testSession)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	c := rules.Context{TenantID: "10000000-0000-4000-8000-000000000001", ProjectID: "10000000-0000-4000-8000-000000000002", PersonID: "10000000-0000-4000-8000-000000000003", AgentID: "10000000-0000-4000-8000-000000000004", Role: "builder", Harness: "codex", TaskID: otherID}
	snap := rules.Snapshot{SetID: "10000000-0000-4000-8000-000000000005", Scope: rules.Scope{Layer: "company"}, Name: "Synthetic floor", Revision: 1, Version: "260928100000.0.0", Rules: []rules.Rule{
		{Identity: "fixture.safety", Text: "Preserve this synthetic safety rule.", Why: "fixture", Strength: "locked", Enabled: true, Source: rules.Source{Reference: "synthetic"}},
		{Identity: "fixture.routing", Text: "Keep the full synthetic routing rule, too.", Why: "fixture", Strength: "locked", Enabled: true, Source: rules.Source{Reference: "synthetic"}},
	}}
	snap.SHA256 = rules.SnapshotDigest(snap)
	m, err := rules.Merge(c, []rules.Snapshot{snap}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b := binding{Schema: "aeon.rules.bootstrap.v1", Workspace: root, Instance: "fixture", InstanceURL: "https://fixture.invalid", ConfigPath: filepath.Join(root, "client-reference.txt"), CLIPath: filepath.Join(root, "published-aeon"), Session: testSession, Agent: "fixture-worker", Context: c, Request: testRequest, Floor: filepath.Join(dir, "floor.txt"), FloorSHA: digest([]byte(m.Floor)), Cache: filepath.Join(dir, "cache.json"), Output: filepath.Join(dir, "received.txt"), State: filepath.Join(dir, "state.json"), LeaseFile: filepath.Join(dir, "lease.txt")}
	f := &fixture{t: t, root: root, b: b, o: options{path: filepath.Join(dir, "binding.json"), session: testSession, harness: "codex"}, floor: []byte(m.Floor), bundle: m}
	write(t, b.Floor, f.floor)
	// Invalid credential contents intentionally: floor never opens/parses them.
	write(t, b.ConfigPath, []byte("not a configuration; synthetic unread reference"))
	write(t, b.LeaseFile, []byte("not a credential; synthetic unread reference"))
	if err := os.WriteFile(b.CLIPath, []byte("synthetic executable; never run"), 0700); err != nil {
		t.Fatal(err)
	}
	f.bind()
	return f
}

func (f *fixture) bind() {
	raw := encode(f.t, f.b)
	write(f.t, f.o.path, raw)
	f.o.sha = digest(raw)
}
func (f *fixture) args(verb string) []string {
	a := []string{verb, "--binding", f.o.path, "--binding-sha256", f.o.sha, "--session", f.o.session, "--harness", f.o.harness}
	if f.o.retry {
		a = append(a, "--retry")
	}
	return a
}
func (f *fixture) state(source string) checkpoint {
	m := f.bundle
	if source == "floor-only" {
		m, _ = rules.Offline(nil, f.b.InstanceURL, f.b.Context, string(f.floor), time.Now())
	}
	s := checkpoint{Schema: "aeon.rules.receive.v1", Instance: f.b.InstanceURL, Session: f.b.Session, Agent: f.b.Agent, Output: f.b.Output, FloorSHA: f.b.FloorSHA, Bundle: m,
		Request: harness.RulesReceiptWrite{RequestID: f.b.Request, ExpectedRevision: &f.b.Revision, Context: f.b.Context, BodySHA256: m.SHA256, Version: m.Version, ByteSize: &m.ByteSize, Source: source}}
	if source != "online" {
		s.Gap = "synthetic unavailable"
	}
	return s
}
func (f *fixture) save(s checkpoint) {
	s.SHA256 = ""
	s.SHA256 = digest(encode(f.t, s))
	write(f.t, f.b.State, encode(f.t, s))
	write(f.t, f.b.Output, []byte(s.Bundle.Body))
}
func (f *fixture) result(s checkpoint, receipt string) metadata {
	m := metadata{Session: f.b.Session, Request: f.b.Request, Revision: f.b.Revision, Source: s.Request.Source, Stale: s.Request.Source != "online", Gap: s.Gap, Output: f.b.Output, State: f.b.State, SHA256: s.Bundle.SHA256, Version: s.Bundle.Version, ByteSize: s.Bundle.ByteSize, BytesReceived: true, ReceiptStatus: receipt}
	if receipt == "recorded" {
		replayed := f.o.retry
		m.ReceiptRecorded, m.Complete, m.ReceiptID, m.ReceiptRevision, m.Replayed = true, true, otherID, f.b.Revision+1, &replayed
	}
	return m
}

func TestFloorExactAndOffline(t *testing.T) {
	for _, h := range []string{"codex", "cursor", "claude-code"} {
		t.Run(h, func(t *testing.T) {
			f := newFixture(t)
			f.b.Context.Harness, f.o.harness = h, h
			f.bind()
			t.Chdir(f.root)
			var out, status bytes.Buffer
			err := execute(f.args("floor"), &out, &status, func(*exec.Cmd) error { t.Fatal("floor ran a subprocess"); return nil })
			if err != nil || !bytes.Equal(out.Bytes(), f.floor) || status.Len() != 0 {
				t.Fatalf("floor mismatch: %v", err)
			}
			for _, path := range []string{f.b.State, f.b.Output, f.b.Cache} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("floor mutated runtime")
				}
			}
		})
	}
}

func TestBindingRejectsMalformedAndMismatchedInputs(t *testing.T) {
	cases := map[string]func(*fixture){
		"malformed": func(f *fixture) { write(f.t, f.o.path, []byte(`{"schema":`)); f.o.sha = digest([]byte(`{"schema":`)) },
		"duplicate": func(f *fixture) {
			raw := encode(f.t, f.b)
			raw = append([]byte(`{"session_id":"`+testSession+`",`), raw[1:]...)
			write(f.t, f.o.path, raw)
			f.o.sha = digest(raw)
		},
		"nested-duplicate": func(f *fixture) {
			raw := bytes.Replace(encode(f.t, f.b), []byte(`"role":"builder"`), []byte(`"role":"reviewer","role":"builder"`), 1)
			write(f.t, f.o.path, raw)
			f.o.sha = digest(raw)
		},
		"unknown": func(f *fixture) {
			raw := append([]byte(`{"credential":"synthetic",`), encode(f.t, f.b)[1:]...)
			write(f.t, f.o.path, raw)
			f.o.sha = digest(raw)
		},
		"case-alias": func(f *fixture) {
			raw := bytes.Replace(encode(f.t, f.b), []byte(`"schema"`), []byte(`"Schema"`), 1)
			write(f.t, f.o.path, raw)
			f.o.sha = digest(raw)
		},
		"null-revision": func(f *fixture) {
			raw := bytes.Replace(encode(f.t, f.b), []byte(`"expected_revision":0`), []byte(`"expected_revision":null`), 1)
			write(f.t, f.o.path, raw)
			f.o.sha = digest(raw)
		},
		"missing-revision": func(f *fixture) {
			raw := bytes.Replace(encode(f.t, f.b), []byte(`"expected_revision":0,`), nil, 1)
			write(f.t, f.o.path, raw)
			f.o.sha = digest(raw)
		},
		"trailing": func(f *fixture) {
			raw := append(encode(f.t, f.b), []byte(` {}`)...)
			write(f.t, f.o.path, raw)
			f.o.sha = digest(raw)
		},
		"oversize": func(f *fixture) {
			raw := bytes.Repeat([]byte(" "), maxBinding+1)
			write(f.t, f.o.path, raw)
			f.o.sha = digest(raw)
		},
		"hash":                func(f *fixture) { f.o.sha = strings.Repeat("0", 64) },
		"missing-hash":        func(f *fixture) { f.o.sha = "" },
		"missing-binding":     func(f *fixture) { f.o.path = "" },
		"session":             func(f *fixture) { f.b.Session = otherID; f.bind() },
		"harness":             func(f *fixture) { f.b.Context.Harness = "cursor"; f.bind() },
		"unsupported-harness": func(f *fixture) { f.o.harness = "pi" },
		"workspace":           func(f *fixture) { f.b.Workspace = filepath.Dir(f.root); f.bind() },
		"context":             func(f *fixture) { f.b.Context.TenantID = "not-a-uuid"; f.bind() },
		"context-agent":       func(f *fixture) { f.b.Context.AgentID = ""; f.bind() },
		"floor-pin":           func(f *fixture) { f.b.FloorSHA = strings.Repeat("0", 64); f.bind() },
		"partial-floor":       func(f *fixture) { write(f.t, f.b.Floor, f.floor[:len(f.floor)/2]) },
		"embedded-url-auth":   func(f *fixture) { f.b.InstanceURL = "https://synthetic:example@fixture.invalid"; f.bind() },
		"url-query":           func(f *fixture) { f.b.InstanceURL += "?fixture=true"; f.bind() },
		"request":             func(f *fixture) { f.b.Request = ""; f.bind() },
		"revision":            func(f *fixture) { f.b.Revision = -1; f.bind() },
		"duplicate-artifacts": func(f *fixture) { f.b.Cache = f.b.State; f.bind() },
		"credential-alias":    func(f *fixture) { f.b.ConfigPath = f.o.path; f.bind() },
		"missing-cli":         func(f *fixture) { f.b.CLIPath = ""; f.bind() },
		"relative-cli":        func(f *fixture) { f.b.CLIPath = "published-aeon"; f.bind() },
		"unclean-cli":         func(f *fixture) { f.b.CLIPath = f.root + "/./published-aeon"; f.bind() },
		"shell-fragment-cli":  func(f *fixture) { f.b.CLIPath += ";true"; f.bind() },
		"missing-file-cli":    func(f *fixture) { f.b.CLIPath += "-missing"; f.bind() },
		"traversal":           func(f *fixture) { f.b.Floor = filepath.Dir(f.b.Floor) + "/../" + testSession + "/floor.txt"; f.bind() },
		"outside-runtime":     func(f *fixture) { f.b.LeaseFile = f.b.ConfigPath; f.bind() },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			change(f)
			if _, _, err := loadBinding(f.root, f.o); err == nil {
				t.Fatal("accepted invalid binding")
			}
		})
	}
}

func TestUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"floor-symlink", "floor-hardlink", "binding-hardlink", "lease-symlink", "cli-symlink", "cli-not-executable", "cli-world-writable", "cli-directory", "fifo", "directory", "file-mode", "special-mode", "directory-mode", "workspace-mode", "ancestor-symlink", "floor-bound", "cache-bound", "state-bound", "output-bound", "lease-bound", "owner"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "cli-symlink":
				must(os.Rename(f.b.CLIPath, f.b.CLIPath+".saved"))
				must(os.Symlink(f.b.CLIPath+".saved", f.b.CLIPath))
			case "cli-not-executable":
				must(os.Chmod(f.b.CLIPath, 0600))
			case "cli-world-writable":
				must(os.Chmod(f.b.CLIPath, 0702))
			case "cli-directory":
				must(os.Rename(f.b.CLIPath, f.b.CLIPath+".saved"))
				must(os.Mkdir(f.b.CLIPath, 0700))
			case "floor-symlink", "floor-hardlink", "binding-hardlink", "lease-symlink", "fifo", "directory":
				p := f.b.Floor
				if kind == "binding-hardlink" {
					p = f.o.path
				}
				if kind == "lease-symlink" {
					p = f.b.LeaseFile
				}
				must(os.Rename(p, p+".saved"))
				switch kind {
				case "floor-symlink", "lease-symlink":
					must(os.Symlink(p+".saved", p))
				case "floor-hardlink", "binding-hardlink":
					must(os.Link(p+".saved", p))
				case "fifo":
					must(unix.Mkfifo(p, 0600))
				case "directory":
					must(os.Mkdir(p, 0700))
				}
			case "file-mode":
				must(os.Chmod(f.b.Floor, 0644))
			case "special-mode":
				must(os.Chmod(f.b.Floor, 0600|os.ModeSetuid))
			case "directory-mode":
				must(os.Chmod(filepath.Dir(f.b.Floor), 0755))
			case "workspace-mode":
				must(os.Chmod(f.root, 0777))
			case "ancestor-symlink":
				p := filepath.Dir(f.b.Floor)
				must(os.Rename(p, p+"-saved"))
				must(os.Symlink(p+"-saved", p))
			case "floor-bound":
				write(t, f.b.Floor, bytes.Repeat([]byte("a"), rules.MaxBytes+1))
			case "cache-bound":
				write(t, f.b.Cache, bytes.Repeat([]byte("a"), rules.MaxCacheBytes+1))
			case "state-bound":
				write(t, f.b.State, bytes.Repeat([]byte("a"), rules.MaxCacheBytes+1))
			case "output-bound":
				write(t, f.b.Output, bytes.Repeat([]byte("a"), rules.MaxBytes+1))
			case "lease-bound":
				write(t, f.b.LeaseFile, bytes.Repeat([]byte("a"), 64*1024+1))
			case "owner":
				if os.Geteuid() != 0 {
					t.Skip("changing fixture ownership requires root; uid check remains enforced")
				}
				must(os.Chown(f.b.Floor, 65534, -1))
			}
			if _, _, err := loadBinding(f.root, f.o); err == nil {
				t.Fatal("accepted unsafe file")
			}
		})
	}
}

func TestReceiveArgvAndExactBytes(t *testing.T) {
	f := newFixture(t)
	t.Chdir(f.root)
	var out, status bytes.Buffer
	err := execute(f.args("receive"), &out, &status, func(cmd *exec.Cmd) error {
		want := []string{f.b.CLIPath, "--config", f.b.ConfigPath, "--instance", "fixture", "--json", "session", "start", "--rules-receive", "--project", f.b.Context.ProjectID, "--agent", f.b.Agent, "--session", testSession, "--worker-lease-file", f.b.LeaseFile, "--rules-request-id", testRequest, "--rules-expected-revision", "0", "--rules-state", f.b.State, "--rules-out", f.b.Output, "--rules-cache", f.b.Cache, "--rules-floor", f.b.Floor, "--rules-floor-sha256", f.b.FloorSHA, "--rules-tenant", f.b.Context.TenantID, "--rules-person", f.b.Context.PersonID, "--rules-agent", f.b.Context.AgentID, "--rules-role", "builder", "--rules-harness", "codex", "--rules-task", otherID}
		if !reflect.DeepEqual(cmd.Args, want) || cmd.Path != f.b.CLIPath || cmd.Dir != f.root || cmd.Env == nil || len(cmd.Env) != 0 || cmd.Stdin != nil {
			t.Fatal("command did not bind explicit argv/environment/workspace")
		}
		s := f.state("online")
		f.save(s)
		_, err := cmd.Stdout.Write(encode(t, f.result(s, "recorded")))
		return err
	})
	if err != nil || out.String() != f.bundle.Body {
		t.Fatalf("receive: %v", err)
	}
	for _, key := range []string{"load_verified", "execution_verified", "provenance_recorded", "publication_verified", "authority_granted", "rollout_verified"} {
		if !strings.Contains(status.String(), `"`+key+`":false`) {
			t.Fatal("upgraded evidence", status.String())
		}
	}
	if strings.Contains(status.String(), f.root) {
		t.Fatal("private path in status")
	}
}

func TestReceiveRejectsSwapsAndFalseEvidence(t *testing.T) {
	for _, kind := range []string{"context", "instance", "checkpoint-session", "checkpoint-floor", "checkpoint-hash", "truncated-output", "swapped-output", "linked-output", "binding-swapped", "floor-swapped", "result-session", "result-request", "result-revision", "result-state", "result-hash", "result-size", "result-version", "result-source", "result-stale", "result-complete", "false-load", "false-provenance", "false-execution", "duplicate-result", "unknown-result", "oversize-result", "missing-result"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			var out, status bytes.Buffer
			err := receive(f.b, f.floor, f.o, &out, &status, func(cmd *exec.Cmd) error {
				s := f.state("online")
				m := f.result(s, "recorded")
				switch kind {
				case "context":
					s.Request.Context.PersonID = otherID
				case "instance":
					s.Instance = "https://other.invalid"
				case "checkpoint-session":
					s.Session = otherID
				case "checkpoint-floor":
					s.Bundle.Floor += "extra\n"
				case "result-session":
					m.Session = otherID
				case "result-request":
					m.Request = otherID
				case "result-revision":
					m.Revision++
				case "result-state":
					m.State = f.b.Cache
				case "result-hash":
					m.SHA256 = strings.Repeat("0", 64)
				case "result-size":
					m.ByteSize--
				case "result-version":
					m.Version = "floor-only"
				case "result-source":
					m.Source = "cache"
				case "result-stale":
					m.Stale = true
				case "result-complete":
					m.Complete = false
				case "false-load":
					m.Load = true
				case "false-provenance":
					m.Provenance = true
				case "false-execution":
					m.Execution = true
				}
				f.save(s)
				switch kind {
				case "checkpoint-hash":
					write(t, f.b.State, bytes.Replace(encode(t, s), []byte(`"sha256":""`), []byte(`"sha256":"wrong"`), 1))
				case "truncated-output":
					write(t, f.b.Output, []byte(f.bundle.Body[:len(f.bundle.Body)-1]))
				case "swapped-output":
					write(t, f.b.Output, []byte(strings.Repeat("x", len(f.bundle.Body))))
				case "linked-output":
					if err := os.Link(f.b.Output, f.b.Output+".saved"); err != nil {
						t.Fatal(err)
					}
				case "binding-swapped":
					write(t, f.o.path, []byte(`{}`))
				case "floor-swapped":
					write(t, f.b.Floor, []byte("different floor"))
				}
				raw := encode(t, m)
				switch kind {
				case "duplicate-result":
					raw = append([]byte(`{"load_verified":true,`), raw[1:]...)
				case "unknown-result":
					raw = append([]byte(`{"unrecognized":false,`), raw[1:]...)
				case "oversize-result":
					raw = append(raw, bytes.Repeat([]byte(" "), maxMetadata)...)
				case "missing-result":
					raw = nil
				}
				_, e := cmd.Stdout.Write(raw)
				return e
			})
			if err == nil || out.Len() != 0 || status.Len() != 0 {
				t.Fatalf("accepted %s: %v", kind, err)
			}
		})
	}
}

func TestReceiptFailureAndExplicitRetryPreserveCheckpoint(t *testing.T) {
	for _, receipt := range []string{"unconfirmed", "rejected"} {
		t.Run(receipt, func(t *testing.T) {
			f := newFixture(t)
			var out, status bytes.Buffer
			calls := 0
			run := func(cmd *exec.Cmd) error {
				calls++
				s := f.state("online")
				if calls == 1 {
					f.save(s)
					write(t, f.b.Cache, []byte(`{"synthetic":"checkpoint test"}`))
				}
				if calls == 2 && cmd.Args[len(cmd.Args)-1] != "--rules-retry" {
					t.Fatal("retry not explicit")
				}
				outcome := receipt
				if calls == 2 {
					outcome = "recorded"
				}
				_, _ = cmd.Stdout.Write(encode(t, f.result(s, outcome)))
				fmt.Fprintln(cmd.Stderr, "Bearer synthetic-DO-NOT-FORWARD")
				if calls == 1 {
					return errors.New("synthetic-DO-NOT-FORWARD")
				}
				return nil
			}
			err := receive(f.b, f.floor, f.o, &out, &status, run)
			if err == nil || out.Len() != 0 || !strings.Contains(status.String(), `"receipt_status":"`+receipt+`"`) || strings.Contains(status.String()+err.Error(), "DO-NOT-FORWARD") {
				t.Fatal("failed receipt was misreported")
			}
			before, _ := os.ReadFile(f.b.State)
			if receive(f.b, f.floor, f.o, &out, &status, run) == nil || calls != 1 {
				t.Fatal("silently retried")
			}
			f.o.retry = true
			if err := receive(f.b, f.floor, f.o, &out, &status, run); err != nil || calls != 2 || out.String() != f.bundle.Body {
				t.Fatalf("retry failed: %v", err)
			}
			after, _ := os.ReadFile(f.b.State)
			if !bytes.Equal(before, after) {
				t.Fatal("checkpoint changed")
			}
		})
	}
}

func TestRetryRefusesChangedOrOfflineState(t *testing.T) {
	for _, kind := range []string{"context", "output", "offline", "child-mutates"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			s := f.state("online")
			if kind == "context" {
				s.Request.Context.PersonID = otherID
			}
			if kind == "offline" {
				s = f.state("cache")
			}
			f.save(s)
			if kind == "output" {
				write(t, f.b.Output, []byte("changed"))
			}
			f.o.retry = true
			var out, status bytes.Buffer
			calls := 0
			err := receive(f.b, f.floor, f.o, &out, &status, func(cmd *exec.Cmd) error {
				calls++
				write(t, f.b.State, []byte("changed"))
				_, _ = cmd.Stdout.Write(encode(t, f.result(s, "recorded")))
				return nil
			})
			if err == nil || out.Len() != 0 || (kind != "child-mutates" && calls != 0) {
				t.Fatal("bad retry accepted")
			}
		})
	}
}

func TestOfflineIsDegraded(t *testing.T) {
	for _, source := range []string{"cache", "floor-only"} {
		t.Run(source, func(t *testing.T) {
			f := newFixture(t)
			s := f.state(source)
			var out, status bytes.Buffer
			err := receive(f.b, f.floor, f.o, &out, &status, func(cmd *exec.Cmd) error {
				f.save(s)
				_, e := cmd.Stdout.Write(encode(t, f.result(s, "not_submitted_offline")))
				return e
			})
			if err == nil || out.String() != s.Bundle.Body || !strings.Contains(status.String(), `"degraded":true`) || !strings.Contains(status.String(), `"complete":false`) {
				t.Fatalf("offline status: %v %s", err, status.String())
			}
		})
	}
}

// A real synthetic child exercises stdout bounds and stderr suppression without
// invoking paimos, reading credentials, or receiving a real session.
func TestSyntheticChild(t *testing.T) {
	for i, a := range os.Args {
		if a == "--bootstrap-child" {
			fmt.Fprint(os.Stderr, "Bearer synthetic-DO-NOT-FORWARD")
			fmt.Fprint(os.Stdout, os.Args[i+1])
			os.Exit(9)
		}
	}
}
func TestSubprocessBodiesSuppressed(t *testing.T) {
	for _, raw := range []string{`{"error":"synthetic-DO-NOT-FORWARD"}`, strings.Repeat("synthetic-DO-NOT-FORWARD", 2000)} {
		f := newFixture(t)
		var out, status bytes.Buffer
		err := receive(f.b, f.floor, f.o, &out, &status, func(cmd *exec.Cmd) error {
			self, e := os.Executable()
			if e != nil {
				t.Fatal(e)
			}
			cmd.Path, cmd.Args = self, []string{self, "-test.run=^TestSyntheticChild$", "--", "--bootstrap-child", raw}
			return cmd.Run()
		})
		if err == nil || out.Len() != 0 || status.Len() != 0 || strings.Contains(err.Error(), "DO-NOT-FORWARD") {
			t.Fatal("child body escaped")
		}
	}
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("synthetic git: %v %s", err, raw)
	}
}
func checkFixture(t *testing.T) (string, manifest) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-q")
	if err := os.MkdirAll(filepath.Join(root, publicDir), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, ".gitignore"), []byte("/tmp/\n"))
	write(t, filepath.Join(root, "AGENTS.md"), []byte("Synthetic existing safety routing.\n"))
	m := manifest{Schema: "aeon.rules.rollout.v1", State: "prepared", AgentsSHA: digest([]byte("Synthetic existing safety routing.\n")), ClaudeAbsent: true}
	for i, n := range []string{"agents.txt", "claude.txt"} {
		body := []byte("Synthetic candidate " + n + "\n")
		v := target{Candidate: publicDir + "/" + n, Target: "AGENTS.md", Harnesses: []string{"codex", "cursor"}, SHA256: digest(body), Bytes: len(body)}
		if i == 1 {
			v.Target, v.Harnesses = "CLAUDE.md", []string{"claude-code"}
		}
		m.Targets = append(m.Targets, v)
		write(t, filepath.Join(root, v.Candidate), body)
	}
	write(t, filepath.Join(root, publicDir, "rollout.json"), encode(t, m))
	git(t, root, "add", ".gitignore", "AGENTS.md", publicDir)
	return root, m
}
func TestPreparedAndFutureActiveDrift(t *testing.T) {
	for _, kind := range []string{"prepared", "agents-drift", "claude-created", "candidate-drift", "candidate-bound", "unknown-field", "mapping", "tracked-private", "misplaced-private", "unignored-runtime", "ignored-private", "active", "active-drift"} {
		t.Run(kind, func(t *testing.T) {
			root, m := checkFixture(t)
			switch kind {
			case "agents-drift":
				write(t, filepath.Join(root, "AGENTS.md"), []byte("drift"))
			case "claude-created":
				write(t, filepath.Join(root, "CLAUDE.md"), []byte("unapproved activation"))
			case "candidate-drift":
				write(t, filepath.Join(root, m.Targets[0].Candidate), []byte("drift"))
			case "candidate-bound":
				m.Targets[0].Bytes = rules.MaxBytes + 1
				write(t, filepath.Join(root, publicDir, "rollout.json"), encode(t, m))
			case "unknown-field":
				raw := append([]byte(`{"private_selector":"synthetic",`), encode(t, m)[1:]...)
				write(t, filepath.Join(root, publicDir, "rollout.json"), raw)
			case "mapping":
				m.Targets[0].Target = "OTHER.md"
				write(t, filepath.Join(root, publicDir, "rollout.json"), encode(t, m))
			case "unignored-runtime":
				write(t, filepath.Join(root, ".gitignore"), nil)
			case "misplaced-private":
				p := filepath.Join(root, publicDir, "binding.json")
				write(t, p, []byte("synthetic misplaced binding"))
				git(t, root, "add", p)
			case "tracked-private", "ignored-private":
				p := filepath.Join(root, "tmp", "aeon-rules", testSession, "binding.json")
				if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
				write(t, p, []byte("synthetic private artifact; check must not read this"))
				if kind == "tracked-private" {
					git(t, root, "add", "-f", p)
				}
			case "active", "active-drift":
				m.State = "active"
				write(t, filepath.Join(root, publicDir, "rollout.json"), encode(t, m))
				for _, v := range m.Targets {
					raw, err := os.ReadFile(filepath.Join(root, v.Candidate))
					if err != nil {
						t.Fatal(err)
					}
					write(t, filepath.Join(root, v.Target), raw)
				}
				if kind == "active-drift" {
					write(t, filepath.Join(root, "CLAUDE.md"), []byte("drift"))
				}
			}
			err := check(root)
			valid := kind == "prepared" || kind == "active" || kind == "ignored-private"
			if (err == nil) != valid {
				t.Fatalf("check %s: %v", kind, err)
			}
		})
	}
}

func TestMissingBootstrapAndNonCheckoutBlockBeforeRunner(t *testing.T) {
	f := newFixture(t)
	t.Chdir(f.root)
	for _, args := range [][]string{{"floor"}, {"receive"}, {"floor", "--retry"}, {"receive", "--binding", "missing"}, {"check", "extra"}} {
		var out, status bytes.Buffer
		if execute(args, &out, &status, func(*exec.Cmd) error { t.Fatal("unexpected child"); return nil }) == nil || out.Len() != 0 {
			t.Fatal("missing bootstrap accepted")
		}
	}
	t.Chdir(filepath.Dir(f.b.Floor))
	if _, err := workspace(); err == nil {
		t.Fatal("non-checkout cwd accepted")
	}
}

func TestCheckCannotBeRedirectedToAnotherIndex(t *testing.T) {
	root, _ := checkFixture(t)
	decoy, _ := checkFixture(t)
	p := filepath.Join(root, "tmp", "aeon-rules", testSession, "binding.json")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, p, []byte("synthetic force-added runtime"))
	git(t, root, "add", "-f", p)
	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "index"))
	if check(root) == nil {
		t.Fatal("checked an ambient index instead of the bound checkout")
	}
}
