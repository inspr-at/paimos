// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package harness

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

const (
	AdapterClaudeResume  = "claude_resume"
	AdapterClaudeChannel = "claude_channel"
	KindClaudeSession    = "claude_session"
)

var (
	claudeLocalSessionPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	claudeCloudSessionPattern = regexp.MustCompile(`^(session|cse)_[A-Za-z0-9_-]{1,128}$`)
)

// ClaudeSessionPrimitive returns the documented print-mode target flag.
func ClaudeSessionPrimitive(ref string) (string, bool) {
	switch {
	case claudeLocalSessionPattern.MatchString(ref):
		return "--resume", true
	case claudeCloudSessionPattern.MatchString(ref):
		return "--cloud", true
	default:
		return "", false
	}
}

type ClaudePlugin struct{ Channel bool }

func (p ClaudePlugin) Name() string {
	if p.Channel {
		return AdapterClaudeChannel
	}
	return AdapterClaudeResume
}
func (ClaudePlugin) Kind() string         { return KindClaudeSession }
func (ClaudePlugin) MaximumLevel() string { return LevelSimple }
func (ClaudePlugin) Mode() string         { return ModeLocal }

func (p ClaudePlugin) ValidateTarget(_ context.Context, ref string) error {
	flag, ok := ClaudeSessionPrimitive(ref)
	if !ok {
		return &Error{Code: CodeTargetRefInvalid, Message: "Claude delivery target must be a canonical lowercase local or cloud session id"}
	}
	if p.Channel && flag != "--resume" {
		return &Error{Code: CodeTargetRefInvalid, Message: "claude_channel targets name the local session UUID that loaded the PAIMOS channel"}
	}
	return nil
}

func (p ClaudePlugin) Deliver(ctx context.Context, req DeliverRequest) (DeliverResult, error) {
	if p.Channel {
		return DeliverResult{}, &UnavailableError{Message: "claude_channel delivery is owned by the attributed channel worker"}
	}
	target := strings.TrimSpace(req.TargetRef)
	if target == "" {
		return DeliverResult{}, &UnavailableError{Message: "Claude delivery requires --deliver-target: a local session UUID (claude -p --resume) or a cloud session id session_…/cse_… (claude -p --cloud)"}
	}
	flag, ok := ClaudeSessionPrimitive(target)
	if !ok {
		return DeliverResult{}, &UnavailableError{Message: "Claude delivery target must be a lowercase local session UUID or a cloud session id (session_…/cse_…); socket paths, URLs, and session names are not accepted"}
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		return DeliverResult{}, &UnavailableError{Message: "Claude delivery requires the claude CLI in PATH"}
	}
	result := DeliverResult{EffectiveLevel: LevelSimple, Primitive: "claude -p " + flag}
	if req.Level == LevelSteer {
		result.FallbackReason = "unsupported"
		fmt.Fprintf(writerOrDiscard(req.Stderr), "claude: steer is unsupported; delivering as simple via %s (fallback_reason=unsupported)\n", result.Primitive)
	}
	cmd := exec.CommandContext(ctx, path, "-p", flag, target) // #nosec G204 G702 -- fixed argv and operator-controlled PATH.
	cmd.Stdin = strings.NewReader(req.Body)
	cmd.Stdout, cmd.Stderr = io.Discard, writerOrDiscard(req.Stderr)
	if err := cmd.Run(); err != nil {
		return DeliverResult{}, fmt.Errorf("deliver to Claude session via %s: %w", result.Primitive, err)
	}
	return result, nil
}

func init() {
	if err := Register(ClaudePlugin{}); err != nil {
		panic(err)
	}
	if err := Register(ClaudePlugin{Channel: true}); err != nil {
		panic(err)
	}
	if err := RegisterAlias("claude", AdapterClaudeResume); err != nil {
		panic(err)
	}
}
