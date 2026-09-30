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
	"strings"
	"testing"
)

func TestAttachCodesignRequiresValidAppleSignature(t *testing.T) {
	for _, attack := range []string{"vendor", "unsigned", "foreign", "ad-hoc", "missing-team", "invalid-signature", "self-signed", "tool-error", "duplicate-team"} {
		t.Run(attack, func(t *testing.T) {
			calls := 0
			signature, err := inspectAttachSignatureWith(t.Context(), "/fixture/image", func(_ context.Context, args ...string) (string, error) {
				calls++
				if calls == 1 {
					if !reflect.DeepEqual(args, []string{"-dv", "--verbose=4", "/fixture/image"}) {
						t.Fatal("signature display arguments changed")
					}
					switch attack {
					case "unsigned":
						return "/fixture/image: code object is not signed at all\n", errors.New("unsigned")
					case "tool-error":
						return "verification tool failed", errors.New("unavailable")
					case "ad-hoc":
						return "Signature=adhoc\nTeamIdentifier=Q6L2SF6YDW\n", nil
					case "missing-team":
						return "TeamIdentifier=not set\n", nil
					case "duplicate-team":
						return "TeamIdentifier=Q6L2SF6YDW\nTeamIdentifier=2DC432GLL2\n", nil
					case "foreign":
						return "TeamIdentifier=2DC432GLL2\n", nil
					}
					return "TeamIdentifier=Q6L2SF6YDW\n", nil
				}
				if !reflect.DeepEqual(args, []string{"--verify", "--strict", "-R=anchor apple generic", "/fixture/image"}) {
					t.Fatal("strict Apple certificate-chain requirement removed")
				}
				if attack == "invalid-signature" || attack == "self-signed" {
					return "", errors.New("invalid signature")
				}
				return "", nil
			})
			switch attack {
			case "vendor":
				if err != nil || !signature.Signed || signature.TeamID != attachVendorTeam(Claude) || calls != 2 {
					t.Fatal("vendor not validated", err)
				}
			case "foreign":
				if err != nil || signature.TeamID != attachVendorTeam(Codex) || !signature.Signed || calls != 2 {
					t.Fatal("foreign signature not separately identified", err)
				}
			case "unsigned":
				if err != nil || signature.Signed || calls != 1 {
					t.Fatal("unsigned fallback unavailable", err)
				}
			default:
				if err == nil {
					t.Fatal("unverified signature accepted")
				}
			}
		})
	}
}

func TestAttachRealCodesignUnsignedAndAdHoc(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	unsigned := filepath.Join(root, "unsigned")
	if err = os.WriteFile(unsigned, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := inspectAttachSignature(t.Context(), unsigned); err != nil || got.Signed {
		t.Fatal("real unsigned diagnostic not recognized", err)
	}
	adhoc := filepath.Join(root, "adhoc")
	raw, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(adhoc, raw, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "/usr/bin/codesign", "--force", "--sign", "-", adhoc)
	if err = cmd.Run(); err != nil {
		t.Fatal("fixture ad-hoc signing failed", err)
	}
	if _, err = inspectAttachSignature(t.Context(), adhoc); err == nil {
		t.Fatal("real ad-hoc image accepted")
	}
}

func TestAttachRealInstalledVendorSignatures(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	candidates := map[string][]string{
		Claude: {filepath.Join(home, ".npm-global/bin/claude"), filepath.Join(home, ".local/bin/claude")},
		Codex:  {filepath.Join(home, ".npm-global/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex")},
		Cursor: {"/Applications/Cursor.app/Contents/MacOS/Cursor"},
	}
	for harness, paths := range candidates {
		t.Run(harness, func(t *testing.T) {
			found := false
			for _, path := range paths {
				physical, err := filepath.EvalSymlinks(path)
				if err != nil {
					continue
				}
				found = true
				got, err := inspectAttachSignature(t.Context(), physical)
				if err != nil || !got.Signed || got.TeamID != attachVendorTeam(harness) {
					t.Fatal("installed vendor identity verification failed", harness, err)
				}
			}
			if !found {
				t.Skip("harness is not installed on this test machine")
			}
		})
	}
	output := &attachCodesignOutput{}
	_, _ = output.Write([]byte(strings.Repeat("x", 16385)))
	if !output.overflow || output.text.Len() != 0 {
		t.Fatal("codesign output is not bounded")
	}
}
