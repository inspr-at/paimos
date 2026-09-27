// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"

	"github.com/inspr-at/paimos/internal/grokprobe"
)

func validGuidedGrok(c Candidate, workspace string) error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("native Grok guided setup requires macOS arm64")
	}
	b := c.Grok
	if c.Harness != "grok" || c.Path != b.BinaryPath || c.Identity != b.PrincipalSHA256 ||
		!hashPattern.MatchString(b.PrincipalSHA256) || filepath.Base(c.Home) != ".grok" ||
		b.AuthPath != filepath.Join(c.Home, "auth.json") || !filepath.IsAbs(c.Home) ||
		!filepath.IsAbs(b.ScratchRoot) || within(workspace, b.ScratchRoot) || within(b.ScratchRoot, workspace) {
		return errors.New("native Grok private binding invalid")
	}
	return nil
}

func (e *Engine) verifyGuidedGrok(ctx context.Context, c Candidate, workspace string) error {
	if err := validGuidedGrok(c, workspace); err != nil {
		return err
	}
	probe := e.GrokProbe
	if probe == nil {
		probe = grokprobe.Probe
	}
	verified, err := probe(ctx, c.Grok)
	if err != nil {
		return err
	}
	if verified.Binding != c.Grok {
		return errors.New("native Grok private binding changed")
	}
	return nil
}
