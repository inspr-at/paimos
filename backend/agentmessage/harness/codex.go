// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package harness

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"unicode/utf8"
)

const (
	AdapterCodex         = "codex"
	KindCodexThread      = "codex_thread"
	defaultClientVersion = "dev"
)

type CodexPlugin struct{}

func (CodexPlugin) Name() string         { return AdapterCodex }
func (CodexPlugin) Kind() string         { return KindCodexThread }
func (CodexPlugin) MaximumLevel() string { return LevelSteer }
func (CodexPlugin) Mode() string         { return ModeLocal }

func (CodexPlugin) ValidateTarget(_ context.Context, ref string) error {
	if !utf8.ValidString(ref) || len([]byte(ref)) < 1 || len([]byte(ref)) > 256 || strings.ContainsAny(ref, "\x00\r\n") {
		return &Error{Code: CodeTargetRefInvalid, Message: "Codex thread references must be 1 to 256 safe UTF-8 bytes"}
	}
	return nil
}

// Deliver runs the exact documented primitives: `codex queue --thread` for a
// simple request, and for a steer request the app-server `turn/steer`
// sequence in codex_app_server.go with the exact queue primitive as the
// documented fallback for idle, not-loaded, raced, not-steerable, and
// app-server transport-failure outcomes.
func (p CodexPlugin) Deliver(ctx context.Context, req DeliverRequest) (DeliverResult, error) {
	target := strings.TrimSpace(req.TargetRef)
	if target == "" {
		target = strings.TrimSpace(os.Getenv("CODEX_THREAD_ID"))
	}
	if target == "" {
		target = strings.TrimSpace(os.Getenv("CODEX_SESSION_ID"))
	}
	if target == "" {
		return DeliverResult{}, &UnavailableError{Message: "Codex delivery requires --deliver-target or CODEX_THREAD_ID"}
	}
	if err := p.ValidateTarget(ctx, target); err != nil {
		return DeliverResult{}, err
	}
	if req.Level != LevelSteer {
		return DeliverResult{EffectiveLevel: LevelSimple, Primitive: "codex queue --thread"}, deliverCodexQueue(ctx, req.Body, target, req.Stdout, req.Stderr)
	}
	steered, reason, err := DeliverCodexSteer(ctx, req.Body, target, req.Stderr, req.ClientVersion)
	if err != nil {
		return DeliverResult{}, err
	}
	if steered {
		return DeliverResult{EffectiveLevel: LevelSteer, Primitive: "codex app-server turn/steer"}, nil
	}
	if err := deliverCodexQueue(ctx, req.Body, target, req.Stdout, req.Stderr); err != nil {
		return DeliverResult{}, err
	}
	return DeliverResult{EffectiveLevel: LevelSimple, FallbackReason: reason, Primitive: "codex queue --thread"}, nil
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

func deliverCodexQueue(ctx context.Context, body, target string, stdout, stderr io.Writer) error {
	path, err := exec.LookPath("codex")
	if err != nil {
		return &UnavailableError{Message: "Codex delivery requires the codex CLI in PATH"}
	}
	cmd := exec.CommandContext(ctx, path, "queue", "--thread", target, "--message", body) // #nosec G204 G702 -- fixed argv and operator-controlled PATH.
	cmd.Stdout, cmd.Stderr = writerOrDiscard(stdout), writerOrDiscard(stderr)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("deliver to Codex thread: %w", err)
	}
	return nil
}

func init() {
	if err := Register(CodexPlugin{}); err != nil {
		panic(err)
	}
}
