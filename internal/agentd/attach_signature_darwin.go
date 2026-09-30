// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentd

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Use Apple's fixed system utility with a minimal environment, bounded output
// and an Apple certificate-chain requirement, not just a self-reported Team ID.
func inspectAttachSignature(ctx context.Context, pid string) (attachSignature, error) {
	return inspectAttachSignatureWith(ctx, pid, runAttachCodesign, readAttachProcargs)
}
func runAttachCodesign(ctx context.Context, args ...string) (string, error) {
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(op, "/usr/bin/codesign", args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.Dir = "/"
	cmd.WaitDelay = time.Second
	output := &attachCodesignOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	if op.Err() != nil {
		return "", errAttachSignatureUnavailable
	}
	if output.overflow {
		return "", errAttachSignature
	}
	var exitError *exec.ExitError
	if err != nil && !errors.As(err, &exitError) {
		return "", errAttachSignatureUnavailable
	}
	return output.text.String(), err
}

type attachCodesignOutput struct {
	text     strings.Builder
	overflow bool
}

func (b *attachCodesignOutput) Write(p []byte) (int, error) {
	if b.text.Len()+len(p) > 16384 {
		b.overflow = true
		return len(p), nil
	}
	_, _ = b.text.Write(p)
	return len(p), nil
}
func inspectAttachSignatureWith(ctx context.Context, pid string, run func(context.Context, ...string) (string, error), readEnv func(string) (attachObservedEnv, error)) (attachSignature, error) {
	n, err := strconv.Atoi(pid)
	if err != nil || n < 1 || strconv.Itoa(n) != pid {
		return attachSignature{}, errAttachSignature
	}
	output, err := run(ctx, "-dv", "--verbose=4", pid)
	if err != nil {
		if errors.Is(err, errAttachSignatureUnavailable) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return attachSignature{}, errAttachSignatureUnavailable
		}
		// Report an explicitly unsigned image; macOS attach refuses it.
		if strings.TrimSpace(output) == pid+": code object is not signed at all" {
			return attachSignature{}, nil
		}
		return attachSignature{}, errAttachSignature
	}
	team, identifier := "", ""
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "TeamIdentifier=") {
			if team != "" {
				return attachSignature{}, errAttachSignature
			}
			team = strings.TrimPrefix(line, "TeamIdentifier=")
		}
		if strings.HasPrefix(line, "Identifier=") {
			if identifier != "" {
				return attachSignature{}, errAttachSignature
			}
			identifier = strings.TrimPrefix(line, "Identifier=")
		}
	}
	if team == "" || team == "not set" || strings.Contains(output, "Signature=adhoc") {
		return attachSignature{}, errAttachSignature
	}
	// Display text selects a fixed built-in requirement; it is never authority.
	// Team, CLI identifier and Developer ID markers are enforced by verification.
	// The leaf OID is Developer ID Application; certificate 1 is its intermediate.
	// A same-team Apple Development certificate does not carry those markers.
	matched := false
	for _, harness := range []string{Claude, Codex} {
		matched = matched || team == attachVendorTeam(harness) && identifier == attachVendorIdentifier(harness)
	}
	if !matched {
		return attachSignature{}, errAttachSignature
	}
	requirement := fmt.Sprintf("-R=anchor apple generic and certificate leaf[subject.OU] = %q and identifier %q and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate 1[field.1.2.840.113635.100.6.2.6] exists", team, identifier)
	// Verbosity adds the full static check to dynamic PID verification, including
	// the check that the code on disk matches what is actually running.
	if _, err := run(ctx, "--verify", "--strict", "-v", requirement, pid); err != nil {
		if errors.Is(err, errAttachSignatureUnavailable) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return attachSignature{}, errAttachSignatureUnavailable
		}
		return attachSignature{}, errAttachSignature
	}
	env, envErr := readEnv(pid)
	if envErr != nil {
		return attachSignature{}, errAttachSignatureUnavailable
	}
	decision := decideClaudeRuntime(identifier, env)
	if decision == claudeRuntimeInjected || decision == claudeRuntimeUnobservable {
		if decision == claudeRuntimeUnobservable {
			return attachSignature{}, errAttachRuntimeUnobservable
		}
		return attachSignature{}, errAttachRuntimeDenied
	}
	return attachSignature{TeamID: team, Identifier: identifier, Signed: true}, nil
}
