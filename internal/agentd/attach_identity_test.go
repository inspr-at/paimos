// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin || linux

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/attachwatch"
)

func attachIdentityFixture(t *testing.T, harness string) (*AttachManager, attachObservation, *attachObservation, AttachLocalRequest, string) {
	t.Helper()
	install, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(install, ".ai-cli-updates", "package-v1", "lib", "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")
	writeAttachImage(t, image)
	wrapper := filepath.Join(install, "wrapper")
	shim := filepath.Join(install, "shim")
	// Realistic release-13 state: the pin ends at an unsigned shell wrapper,
	// whose exec chain ends in the versioned, kernel-observed vendor image.
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec \""+image+"\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexec \""+wrapper+"\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"schema": "aeon.agent-runtime.v1", "origin": "https://paired.test", "computer_id": "11111111-1111-4111-8111-111111111111", "workspace": workspace, "accounts": []map[string]string{{"harness": harness, "path": shim}}})
	var legacy agentsetup.RuntimeConfig
	if err := json.Unmarshal(raw, &legacy); err != nil || legacy.AttachIdentities != nil {
		t.Fatal("invalid legacy fixture", err)
	}
	paths := map[string]string{}
	for _, account := range legacy.Accounts {
		paths[account.Harness] = account.Path
	}
	m, err := NewAttachManager(AttachConfig{Origin: legacy.Origin, ComputerID: legacy.ComputerID, Workspace: legacy.Workspace, Host: "fixture", Executables: paths, Exchange: func(_ context.Context, in attachwatch.DeviceRequest) (attachwatch.View, error) {
		return attachwatch.View{State: "pending", RequestID: in.RequestID, Snapshot: in.Snapshot, Digest: in.Digest, ConsentMode: attachwatch.ConsentAeon, ConsentDigest: attachwatch.ConsentDigest(in.RequestID, in.Digest, attachwatch.ConsentAeon)}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	peer := attachObservation{Process: attachwatch.Process{PID: 30, UID: os.Getuid(), Started: "helper", Executable: shim, CWD: workspace}, Parent: 20, Session: 20, TTY: true}
	leader := attachObservation{Process: attachwatch.Process{PID: 20, UID: os.Getuid(), Started: "terminal"}, Parent: 1, Session: 20, TTY: true}
	target := &attachObservation{Process: attachwatch.Process{PID: 40, UID: os.Getuid(), Started: "agent", Executable: image, CWD: workspace}, Parent: 1}
	m.observe = func(pid int) (attachObservation, error) {
		for _, p := range []attachObservation{peer, leader, *target} {
			if p.PID == pid {
				return p, nil
			}
		}
		return attachObservation{}, errors.New("unknown fixture PID")
	}
	m.ancestry = m.observe
	m.signature = func(_ context.Context, path string) (attachSignature, error) {
		if path != target.Executable {
			t.Fatal("signature was taken from the wrapper")
		}
		return attachSignature{TeamID: attachVendorTeam(harness), Signed: true}, nil
	}
	t.Cleanup(func() { m.Close(context.Background()) })
	req := AttachLocalRequest{Operation: "preview", PID: 40, Harness: harness, ProjectID: "22222222-2222-4222-8222-222222222222", TicketID: "33333333-3333-4333-8333-333333333333", StatusOnly: true}
	return m, peer, target, req, image
}
func writeAttachImage(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture native image"), 0755); err != nil {
		t.Fatal(err)
	}
}
func TestAttachRelease13WrapperPairingUpgradeWithoutRepin(t *testing.T) {
	for _, harness := range []string{Claude, Codex, Cursor} {
		t.Run(harness, func(t *testing.T) {
			m, peer, target, req, image := attachIdentityFixture(t, harness)
			if _, err := m.handle(t.Context(), peer, req); err != nil {
				t.Fatal("upgrade required re-pair or repin", err)
			}
			if m.cfg.Identities != nil {
				t.Fatal("daemon invented pairing state")
			}
			target.Executable = strings.Replace(image, "package-v1", "package-v2", 1)
			writeAttachImage(t, target.Executable)
			if _, err := m.handle(t.Context(), peer, req); err != nil {
				t.Fatal("vendor auto-update rejected", err)
			}
		})
	}
}
func TestAttachIdentitySecurityChecks(t *testing.T) {
	for _, attack := range []string{"unsigned", "foreign-team", "empty-team", "ad-hoc", "invalid-signature", "writable-directory", "writable-file", "workspace-image", "unenrolled", "kernel-image-change", "replacement-during-signature"} {
		t.Run(attack, func(t *testing.T) {
			m, peer, target, req, image := attachIdentityFixture(t, Claude)
			switch attack {
			case "unsigned":
				m.signature = func(context.Context, string) (attachSignature, error) { return attachSignature{}, nil }
			case "foreign-team":
				m.signature = func(context.Context, string) (attachSignature, error) {
					return attachSignature{TeamID: attachVendorTeam(Codex), Signed: true}, nil
				}
			case "empty-team":
				m.signature = func(context.Context, string) (attachSignature, error) { return attachSignature{Signed: true}, nil }
			case "ad-hoc", "invalid-signature":
				m.signature = func(context.Context, string) (attachSignature, error) { return attachSignature{}, errAttachSignature }
			case "writable-directory":
				if err := os.Chmod(filepath.Dir(image), 0777); err != nil {
					t.Fatal(err)
				}
			case "writable-file":
				if err := os.Chmod(image, 0775); err != nil {
					t.Fatal(err)
				}
			case "workspace-image":
				target.Executable = filepath.Join(m.cfg.Workspace, "image")
				writeAttachImage(t, target.Executable)
			case "unenrolled":
				delete(m.cfg.Executables, Claude)
			case "kernel-image-change":
				m.signature = func(context.Context, string) (attachSignature, error) {
					target.Started = "reused"
					return attachSignature{TeamID: attachVendorTeam(Claude), Signed: true}, nil
				}
			case "replacement-during-signature":
				m.signature = func(context.Context, string) (attachSignature, error) {
					if err := os.Rename(image, image+".old"); err != nil {
						t.Fatal(err)
					}
					writeAttachImage(t, image)
					return attachSignature{TeamID: attachVendorTeam(Claude), Signed: true}, nil
				}
			}
			_, err := m.handle(t.Context(), peer, req)
			var diagnostic *AttachLocalError
			if !errors.As(err, &diagnostic) || len(m.sessions) != 0 {
				t.Fatal("unsafe image accepted or lost diagnostic", err)
			}
		})
	}
}
func TestAttachUnsignedRecordedRootAndUpdate(t *testing.T) {
	m, peer, target, req, image := attachIdentityFixture(t, Claude)
	identity := agentsetup.RecordAttachIdentity(Claude, image, m.cfg.Workspace)
	if identity == nil {
		t.Fatal("missing install-root identity")
	}
	m.cfg.Identities = map[string]agentsetup.AttachIdentity{Claude: *identity}
	m.signature = func(context.Context, string) (attachSignature, error) { return attachSignature{}, nil }
	for _, version := range []string{"package-v1", "package-v2"} {
		target.Executable = strings.Replace(image, "package-v1", version, 1)
		writeAttachImage(t, target.Executable)
		if _, err := m.handle(t.Context(), peer, req); err != nil {
			t.Fatal("recorded root update refused", err)
		}
	}
	target.Executable = strings.Replace(image, "/claude-code/", "/foreign/", 1)
	target.Executable = filepath.Clean(target.Executable)
	writeAttachImage(t, target.Executable)
	if _, err := m.handle(t.Context(), peer, req); err == nil {
		t.Fatal("unsigned sibling installation accepted")
	}
	target.Executable = image
	changed := *identity
	changed.Owner = os.Getuid() + 10000
	m.cfg.Identities[Claude] = changed
	if _, err := m.handle(t.Context(), peer, req); err == nil {
		t.Fatal("different recorded owner accepted")
	}
}
func TestAttachRechecksImageOnConfirmAndPoll(t *testing.T) {
	for _, operation := range []string{"confirm", "poll"} {
		for _, attack := range []string{"replacement", "writable-directory"} {
			t.Run(operation+"/"+attack, func(t *testing.T) {
				m, peer, _, req, image := attachIdentityFixture(t, Claude)
				preview, err := m.handle(t.Context(), peer, req)
				if err != nil {
					t.Fatal(err)
				}
				if operation == "poll" {
					if _, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: "confirm", ID: preview.ID, Digest: preview.Digest}); err != nil {
						t.Fatal(err)
					}
					m.sessions[preview.ID].touched = time.Now().Add(-2 * time.Second)
				}
				if attack == "replacement" {
					if err = os.Rename(image, image+".old"); err != nil {
						t.Fatal(err)
					}
					writeAttachImage(t, image)
				} else {
					if err = os.Chmod(filepath.Dir(image), 0777); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: operation, ID: preview.ID, Digest: preview.Digest}); err == nil || len(m.sessions) != 0 {
					t.Fatal("changed image retained attach authority", err)
				}
			})
		}
	}
}

func TestAttachRealExecWrapperChain(t *testing.T) {
	m, peer, target, req, image := attachIdentityFixture(t, Claude)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(image, binary, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), m.cfg.Executables[Claude], "-test.run=^TestAttachExecWrapperChild$")
	cmd.Env = append(os.Environ(), "AEON_ATTACH_WRAPPER_CHILD=1")
	cmd.Dir = m.cfg.Workspace
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	var running attachObservation
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		running, err = observeAttachProcess(cmd.Process.Pid)
		if err == nil && running.Executable == image {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || running.Executable != image {
		t.Fatal("wrapper did not exec the pinned native image", err)
	}
	*target = running
	req.PID = target.PID
	original := m.observe
	m.observe = func(pid int) (attachObservation, error) {
		if pid == target.PID {
			return observeAttachProcess(pid)
		}
		return original(pid)
	}
	// The real child proves the exec chain; the helper/leader remain fixtures.
	m.ancestry = func(pid int) (attachObservation, error) {
		if pid == target.PID {
			p := *target
			p.Parent = 1
			return p, nil
		}
		return original(pid)
	}
	if _, err = m.handle(t.Context(), peer, req); err != nil {
		t.Fatal("real exec-wrapper chain refused", err)
	}
}

func TestAttachExecWrapperChild(t *testing.T) {
	if os.Getenv("AEON_ATTACH_WRAPPER_CHILD") == "1" {
		time.Sleep(30 * time.Second)
	}
}

func TestAttachGrokRetainsExactPin(t *testing.T) {
	m, peer, target, req, image := attachIdentityFixture(t, Grok)
	m.cfg.Executables[Grok] = image
	m.signature = func(context.Context, string) (attachSignature, error) {
		t.Fatal("Grok vendor check is outside this change")
		return attachSignature{}, nil
	}
	if _, err := m.handle(t.Context(), peer, req); err != nil {
		t.Fatal("legacy Grok exact pin rejected", err)
	}
	target.Executable = strings.Replace(image, "package-v1", "package-v2", 1)
	writeAttachImage(t, target.Executable)
	if _, err := m.handle(t.Context(), peer, req); err == nil {
		t.Fatal("Grok path pin widened")
	}
}

func TestAttachExactPinCannotBypassVendorOrRecordedOwner(t *testing.T) {
	for _, attack := range []string{"foreign-signature", "invalid-signature", "wrong-recorded-owner"} {
		t.Run(attack, func(t *testing.T) {
			m, peer, _, req, image := attachIdentityFixture(t, Claude)
			m.cfg.Executables[Claude] = image
			switch attack {
			case "foreign-signature":
				m.signature = func(context.Context, string) (attachSignature, error) {
					return attachSignature{TeamID: attachVendorTeam(Codex), Signed: true}, nil
				}
			case "invalid-signature":
				m.signature = func(context.Context, string) (attachSignature, error) { return attachSignature{}, errAttachSignature }
			case "wrong-recorded-owner":
				identity := agentsetup.RecordAttachIdentity(Claude, image, m.cfg.Workspace)
				if identity == nil {
					t.Fatal("missing identity")
				}
				identity.Owner = os.Getuid() + 10000
				m.cfg.Identities = map[string]agentsetup.AttachIdentity{Claude: *identity}
				m.signature = func(context.Context, string) (attachSignature, error) { return attachSignature{}, nil }
			}
			if _, err := m.handle(t.Context(), peer, req); err == nil {
				t.Fatal("exact path bypassed identity", attack)
			}
		})
	}
}
