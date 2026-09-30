// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentd

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Use Apple's fixed system utility with a minimal environment, bounded output
// and an Apple certificate-chain requirement, not just a self-reported Team ID.
func inspectAttachSignature(ctx context.Context, path string) (attachSignature, error) {
	return inspectAttachSignatureWith(ctx, path, runAttachCodesign)
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
	if output.overflow {
		return "", errAttachSignature
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
func inspectAttachSignatureWith(ctx context.Context, path string, run func(context.Context, ...string) (string, error)) (attachSignature, error) {
	output, err := run(ctx, "-dv", "--verbose=4", path)
	if err != nil {
		// Only the explicit unsigned diagnostic enables the local fallback.
		if strings.TrimSpace(output) == path+": code object is not signed at all" {
			return attachSignature{}, nil
		}
		return attachSignature{}, errAttachSignature
	}
	team := ""
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "TeamIdentifier=") {
			if team != "" {
				return attachSignature{}, errAttachSignature
			}
			team = strings.TrimPrefix(line, "TeamIdentifier=")
		}
	}
	if team == "" || team == "not set" || strings.Contains(output, "Signature=adhoc") {
		return attachSignature{}, errAttachSignature
	}
	if _, err := run(ctx, "--verify", "--strict", "-R=anchor apple generic", path); err != nil {
		return attachSignature{}, errAttachSignature
	}
	return attachSignature{TeamID: team, Signed: true}, nil
}
