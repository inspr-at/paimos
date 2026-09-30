// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func TestAttachCodesignRequiresValidAppleSignature(t *testing.T) {
	for _, attack := range []string{"vendor", "unsigned", "foreign", "ad-hoc", "missing-team", "invalid-signature", "self-signed", "tool-error", "duplicate-team", "desktop-identifier", "duplicate-identifier", "cursor-app", "display-timeout", "verify-timeout"} {
		t.Run(attack, func(t *testing.T) {
			calls := 0
			harness := Claude
			if attack == "foreign" {
				harness = Codex
			}
			signature, err := inspectAttachSignatureWith(t.Context(), "123", func(_ context.Context, args ...string) (string, error) {
				calls++
				if calls == 1 {
					if !reflect.DeepEqual(args, []string{"-dv", "--verbose=4", "123"}) {
						t.Fatal("display must use the running PID")
					}
					switch attack {
					case "unsigned":
						return "123: code object is not signed at all\n", errors.New("unsigned")
					case "tool-error":
						return "verification tool failed", errors.New("unavailable")
					case "display-timeout":
						return "123: code object is not signed at all", context.DeadlineExceeded
					case "ad-hoc":
						return "Signature=adhoc\nTeamIdentifier=Q6L2SF6YDW\nIdentifier=com.anthropic.claude-code\n", nil
					case "missing-team":
						return "TeamIdentifier=not set\nIdentifier=com.anthropic.claude-code\n", nil
					case "duplicate-team":
						return "TeamIdentifier=Q6L2SF6YDW\nTeamIdentifier=2DC432GLL2\nIdentifier=com.anthropic.claude-code\n", nil
					case "desktop-identifier":
						return "TeamIdentifier=Q6L2SF6YDW\nIdentifier=com.anthropic.claudefordesktop\n", nil
					case "duplicate-identifier":
						return "TeamIdentifier=Q6L2SF6YDW\nIdentifier=com.anthropic.claude-code\nIdentifier=other\n", nil
					case "cursor-app":
						return "TeamIdentifier=VDXQ22DGB9\nIdentifier=com.todesktop.230313mzl4w4u92\n", nil
					}
					return "TeamIdentifier=" + attachVendorTeam(harness) + "\nIdentifier=" + attachVendorIdentifier(harness) + "\n", nil
				}
				requirement := `-R=anchor apple generic and certificate leaf[subject.OU] = "Q6L2SF6YDW" and identifier "com.anthropic.claude-code"`
				if harness == Codex {
					requirement = `-R=anchor apple generic and certificate leaf[subject.OU] = "2DC432GLL2" and identifier "codex"`
				}
				if !reflect.DeepEqual(args, []string{"--verify", "--strict", "-v", requirement, "123"}) {
					t.Fatal("dynamic/static PID verification with pinned team and identifier removed", args)
				}
				if attack == "invalid-signature" || attack == "self-signed" {
					return "", errors.New("invalid signature")
				}
				if attack == "verify-timeout" {
					return "", context.DeadlineExceeded
				}
				return "", nil
			})
			switch attack {
			case "vendor", "foreign":
				if err != nil || !signature.Signed || signature.TeamID != attachVendorTeam(harness) || signature.Identifier != attachVendorIdentifier(harness) || calls != 2 {
					t.Fatal("vendor not validated", err)
				}
			case "unsigned":
				if err != nil || signature.Signed || calls != 1 {
					t.Fatal("unsigned image misidentified", err)
				}
			case "display-timeout", "verify-timeout":
				if !errors.Is(err, errAttachSignatureUnavailable) {
					t.Fatal("timeout misidentified", err)
				}
			default:
				if err == nil {
					t.Fatal("unverified signature accepted")
				}
			}
		})
	}
}

func TestAttachCodesignRejectsPathAndNonPIDTargets(t *testing.T) {
	for _, target := range []string{"/fixture/image", "0", "-1", "+123", "00123", "123:4"} {
		if _, err := inspectAttachSignatureWith(t.Context(), target, func(context.Context, ...string) (string, error) {
			t.Fatal("non-PID target reached codesign")
			return "", nil
		}); err == nil {
			t.Fatal("invalid target accepted")
		}
	}
}

func TestAttachRealCodesignAdHoc(t *testing.T) {
	// The Go test executable is ad-hoc signed on Darwin, never a vendor CLI.
	if _, err := inspectAttachSignature(t.Context(), strconv.Itoa(os.Getpid())); err == nil {
		t.Fatal("real non-vendor running image accepted")
	}
	output := &attachCodesignOutput{}
	_, _ = output.Write([]byte(strings.Repeat("x", 16385)))
	if !output.overflow || output.text.Len() != 0 {
		t.Fatal("codesign output is not bounded")
	}
}

func TestAttachRealCodesignTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runAttachCodesign(ctx, "-dv", strconv.Itoa(os.Getpid())); !errors.Is(err, errAttachSignatureUnavailable) {
		t.Fatal("canceled verification lost retry diagnostic", err)
	}
}

func TestAttachRealInstalledVendorSignatures(t *testing.T) {
	// Read executable names only. Never inspect process arguments/environments
	// or launch another model CLI to manufacture a positive fixture.
	raw, err := exec.CommandContext(t.Context(), "/bin/ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		t.Fatal("process-name enumeration unavailable", err)
	}
	for _, harness := range []string{Claude, Codex} {
		t.Run(harness, func(t *testing.T) {
			found := false
			for _, line := range strings.Split(string(raw), "\n") {
				fields := strings.Fields(line)
				if len(fields) != 2 || filepath.Base(fields[1]) != harness && filepath.Base(fields[1]) != harness+".exe" {
					continue
				}
				pid, err := strconv.Atoi(fields[0])
				if err != nil {
					continue
				}
				before, err := observeAttachProcess(pid)
				if err != nil {
					continue
				}
				got, err := inspectAttachSignature(t.Context(), fields[0])
				after, observeErr := observeAttachProcess(pid)
				if observeErr != nil || after.Process != before.Process {
					continue
				}
				if err != nil || !got.Signed || got.TeamID != attachVendorTeam(harness) || got.Identifier != attachVendorIdentifier(harness) {
					t.Fatal("running installed vendor verification failed", harness, err)
				}
				found = true
				break
			}
			if !found {
				t.Skip("no running installed harness on this test machine")
			}
		})
	}
}

func TestAttachRealRenameOverRunningImage(t *testing.T) {
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
	cmd := exec.CommandContext(t.Context(), image, "-test.run=^TestAttachExecWrapperChild$")
	cmd.Env = append(os.Environ(), "AEON_ATTACH_WRAPPER_CHILD=1")
	cmd.Dir = m.cfg.Workspace
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	var before attachObservation
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		before, err = observeAttachProcess(cmd.Process.Pid)
		if err == nil && before.Executable == image {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || before.Executable != image {
		t.Fatal("fixture native process unavailable", err)
	}
	originalInfo, err := os.Stat(image)
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	vendor, err := filepath.EvalSymlinks(filepath.Join(home, ".npm-global/bin/claude"))
	if err != nil {
		t.Skip("installed Claude CLI is needed for rename-over regression")
	}
	replacement := image + ".replacement"
	if err = os.Link(vendor, replacement); err != nil {
		t.Fatal("vendor hardlink fixture unavailable", err)
	}
	if err = os.Rename(replacement, image); err != nil {
		t.Fatal(err)
	}
	after, err := observeAttachProcess(before.PID)
	renamedInfo, statErr := os.Stat(image)
	if err != nil || statErr != nil || after.Process != before.Process || os.SameFile(originalInfo, renamedInfo) {
		t.Fatal("rename-over did not preserve process identity while changing disk inode", err)
	}
	// Prove the path alone now passes the exact vendor requirement.
	requirement := `-R=anchor apple generic and certificate leaf[subject.OU] = "Q6L2SF6YDW" and identifier "com.anthropic.claude-code"`
	if _, err = runAttachCodesign(t.Context(), "--verify", "--strict", "-v", requirement, image); err != nil {
		t.Fatal("replacement was not a valid vendor file", err)
	}
	output, verifyErr := runAttachCodesign(t.Context(), "--verify", "--strict", "-v", requirement, strconv.Itoa(before.PID))
	if verifyErr == nil || !strings.Contains(output, "code on disk does not match what is running") {
		t.Fatal("PID verification did not detect the mismatched running image", verifyErr)
	}
	if _, err = inspectAttachSignature(t.Context(), strconv.Itoa(before.PID)); err == nil {
		t.Fatal("vendor replacement conferred identity on foreign running code")
	}
	*target = after
	req.PID = after.PID
	fixtureObserve := m.observe
	m.observe = func(pid int) (attachObservation, error) {
		if pid == after.PID {
			return observeAttachProcess(pid)
		}
		return fixtureObserve(pid)
	}
	useRealAttachAncestry(m, peer)
	m.signature = inspectAttachSignature
	_, err = m.handle(t.Context(), peer, req)
	var diagnostic *AttachLocalError
	if !errors.As(err, &diagnostic) || diagnostic.Code != "harness_identity_mismatch" || len(m.sessions) != 0 {
		t.Fatal("rename-over attack accepted by real attach manager", err)
	}
}

// Keep the independent helper and its leader as fixtures, but read the real
// target and every ancestor from kernel metadata rather than inventing a chain.
func useRealAttachAncestry(m *AttachManager, peer attachObservation) {
	fixture := m.ancestry
	m.ancestry = func(pid int) (attachObservation, error) {
		if pid == peer.PID || pid == peer.Session {
			return fixture(pid)
		}
		return observeAttachProcessIdentity(pid)
	}
}

func TestAttachMacOSRefusesUnsignedImages(t *testing.T) {
	for _, harness := range []string{Claude, Codex} {
		for _, pin := range []string{"exact", "recorded-root"} {
			t.Run(harness+"/"+pin, func(t *testing.T) {
				m, peer, _, req, image := attachIdentityFixture(t, harness)
				m.cfg.Executables[harness] = image
				if pin == "recorded-root" {
					identity := agentsetup.RecordAttachIdentity(harness, image, m.cfg.Workspace)
					if identity == nil {
						t.Fatal("missing recorded-root fixture")
					}
					m.cfg.Identities = map[string]agentsetup.AttachIdentity{harness: *identity}
				}
				m.signature = func(context.Context, string) (attachSignature, error) { return attachSignature{}, nil }
				_, err := m.handle(t.Context(), peer, req)
				var diagnostic *AttachLocalError
				if !errors.As(err, &diagnostic) || diagnostic.Code != "harness_identity_mismatch" || !strings.Contains(diagnostic.Hint, "Unsigned Claude and Codex") || len(m.sessions) != 0 {
					t.Fatal("unsigned macOS image accepted or lost its refusal diagnostic", err)
				}
			})
		}
	}
}

func TestAttachRealUnsignedRosettaImage(t *testing.T) {
	if runtime.GOARCH == "arm64" {
		output, err := exec.CommandContext(t.Context(), "/usr/bin/arch", "-x86_64", "/usr/bin/true").CombinedOutput()
		if err != nil {
			if strings.Contains(string(output), "Bad CPU type in executable") {
				t.Skip("Rosetta is not installed")
			}
			t.Fatal("Rosetta availability probe failed", err)
		}
	}
	source := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(source, []byte("// SPDX-License-Identifier: AGPL-3.0-only\npackage main\nimport \"time\"\nfunc main() { time.Sleep(time.Minute) }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	unsigned := filepath.Join(t.TempDir(), "unsigned-amd64")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", unsigned, source)
	build.Env = append(os.Environ(), "GOARCH=amd64", "CGO_ENABLED=0", "GOMAXPROCS=2")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("amd64 fixture build failed: %v\n%s", err, output)
	}
	// Sign first so removing the signature is explicit even if the linker emits
	// an unsigned amd64 executable on this Go version.
	if _, err := runAttachCodesign(t.Context(), "--force", "--sign", "-", unsigned); err != nil {
		t.Fatal("could not prepare fixture signature", err)
	}
	if _, err := runAttachCodesign(t.Context(), "--remove-signature", unsigned); err != nil {
		t.Fatal("could not remove fixture signature", err)
	}
	binary, err := os.ReadFile(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	for _, harness := range []string{Claude, Codex} {
		t.Run(harness, func(t *testing.T) {
			m, peer, target, req, image := attachIdentityFixture(t, harness)
			if harness == Codex {
				image = strings.Replace(image, "@anthropic-ai/claude-code", "@openai/codex", 1)
			}
			writeAttachImage(t, image)
			identity := agentsetup.RecordAttachIdentity(harness, image, m.cfg.Workspace)
			if identity == nil || identity.Exact {
				t.Fatal("missing updater-root identity")
			}
			m.cfg.Executables[harness] = image
			m.cfg.Identities = map[string]agentsetup.AttachIdentity{harness: *identity}
			attacker := strings.Replace(image, "package-v1", "package-attacker", 1)
			writeAttachImage(t, attacker)
			if err := os.WriteFile(attacker, binary, 0755); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(attacker)
			if err != nil || !identity.Matches(attacker, info) {
				t.Fatal("attacker fixture does not reach the old install-root fallback", err)
			}
			cmd := exec.CommandContext(t.Context(), attacker)
			cmd.Dir = m.cfg.Workspace
			if err := cmd.Start(); err != nil {
				t.Fatal("unsigned amd64 fixture did not run", err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			var running attachObservation
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				running, err = observeAttachProcess(cmd.Process.Pid)
				if err == nil && running.Executable == attacker {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil || running.Executable != attacker {
				t.Fatal("unsigned amd64 running image unavailable", err)
			}
			signature, err := inspectAttachSignature(t.Context(), strconv.Itoa(running.PID))
			if err != nil || signature.Signed {
				t.Fatal("real running fixture was not explicitly unsigned", err)
			}
			*target = running
			req.PID = running.PID
			fixtureObserve := m.observe
			m.observe = func(pid int) (attachObservation, error) {
				if pid == running.PID {
					return observeAttachProcess(pid)
				}
				return fixtureObserve(pid)
			}
			useRealAttachAncestry(m, peer)
			m.signature = inspectAttachSignature
			_, err = m.handle(t.Context(), peer, req)
			var diagnostic *AttachLocalError
			if !errors.As(err, &diagnostic) || diagnostic.Code != "harness_identity_mismatch" || !strings.Contains(diagnostic.Hint, "Unsigned Claude and Codex") || len(m.sessions) != 0 {
				t.Fatal("real unsigned updater-path attack accepted or refused before identity validation", err)
			}
		})
	}
}
