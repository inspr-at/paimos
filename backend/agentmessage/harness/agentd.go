// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package harness

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentdwire"
)

const (
	AdapterAgentdCodex  = "agentd_codex"
	AdapterAgentdClaude = "agentd_claude"
	KindAgentdSession   = "agentd_session"
)

type AgentdTarget struct {
	Socket    string `json:"socket"`
	SessionID string `json:"session_id"`
}

type AgentdCodexPlugin struct{}
type AgentdClaudePlugin struct{}

func (AgentdCodexPlugin) Name() string          { return AdapterAgentdCodex }
func (AgentdCodexPlugin) Kind() string          { return KindAgentdSession }
func (AgentdCodexPlugin) MaximumLevel() string  { return LevelSteer }
func (AgentdCodexPlugin) Mode() string          { return ModeLocal }
func (AgentdClaudePlugin) Name() string         { return AdapterAgentdClaude }
func (AgentdClaudePlugin) Kind() string         { return KindAgentdSession }
func (AgentdClaudePlugin) MaximumLevel() string { return LevelSteer }
func (AgentdClaudePlugin) Mode() string         { return ModeLocal }

func decodeAgentdTarget(ref string) (AgentdTarget, error) {
	if len(ref) > 4096 || !json.Valid([]byte(ref)) {
		return AgentdTarget{}, errors.New("invalid agentd target")
	}
	var target AgentdTarget
	decoder := json.NewDecoder(strings.NewReader(ref))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil {
		return AgentdTarget{}, errors.New("invalid agentd target")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return AgentdTarget{}, errors.New("invalid agentd target")
	}
	parsed, err := uuid.Parse(target.SessionID)
	if err != nil || parsed.String() != target.SessionID || !filepath.IsAbs(target.Socket) || strings.ContainsAny(target.Socket, "\x00\r\n") {
		return AgentdTarget{}, errors.New("invalid agentd target")
	}
	return target, nil
}

func (AgentdCodexPlugin) ValidateTarget(_ context.Context, ref string) error {
	if _, err := decodeAgentdTarget(ref); err != nil {
		return &Error{Code: CodeTargetRefInvalid, Message: "agentd target must name one private Unix socket and managed session UUID"}
	}
	return nil
}

func (AgentdClaudePlugin) ValidateTarget(ctx context.Context, ref string) error {
	return (AgentdCodexPlugin{}).ValidateTarget(ctx, ref)
}

// Deliver is intentionally steer-only and lease-correlated. Simple delivery
// remains on the ordinary inbox adapter; managed control never queue-fakes a
// steer and cannot be invoked through the legacy unleased --deliver-target.
func (AgentdCodexPlugin) Deliver(ctx context.Context, request DeliverRequest) (DeliverResult, error) {
	return deliverAgentdSteer(ctx, request, "codex app-server turn/steer", false)
}

func (AgentdClaudePlugin) Deliver(ctx context.Context, request DeliverRequest) (DeliverResult, error) {
	return deliverAgentdSteer(ctx, request, agentdwire.ClaudeSteerPrimitive, true)
}

func deliverAgentdSteer(ctx context.Context, request DeliverRequest, primitive string, requireVendorMessageID bool) (DeliverResult, error) {
	if request.Level != LevelSteer {
		return DeliverResult{}, &UnavailableError{Message: "agentd managed target is steer-only", FallbackReason: "not_steerable", Reroute: true}
	}
	if request.CorrelationID == "" || len(request.CorrelationID) > 128 || strings.ContainsAny(request.CorrelationID, "\x00\r\n") {
		return DeliverResult{}, &UnavailableError{Message: "agentd managed control requires a leased steer delivery"}
	}
	if request.Instance == "" || request.Instance != strings.TrimSpace(request.Instance) || len(request.Instance) > 512 ||
		request.ProjectID <= 0 || request.Identity == "" || request.Identity != strings.TrimSpace(request.Identity) ||
		len(request.Identity) > 256 || !utf8.ValidString(request.Instance) || !utf8.ValidString(request.Identity) ||
		strings.ContainsAny(request.Instance+request.Identity, "\x00\r\n") {
		return DeliverResult{}, errors.New("agentd managed control lease scope is invalid")
	}
	target, err := decodeAgentdTarget(request.TargetRef)
	if err != nil {
		return DeliverResult{}, &Error{Code: CodeTargetRefInvalid, Message: "agentd target is invalid"}
	}
	receipt, err := agentdwire.Steer(ctx, target.Socket, target.SessionID, agentdwire.ControlRequest{
		Instance: request.Instance, ProjectID: request.ProjectID, Identity: request.Identity,
		CorrelationID: request.CorrelationID, Text: request.Body,
	})
	if err != nil {
		if errors.Is(err, agentdwire.ErrSessionUnavailable) {
			return DeliverResult{}, &UnavailableError{Message: "agentd managed session is not running", FallbackReason: "idle", Reroute: true}
		}
		if errors.Is(err, agentdwire.ErrCapabilityUnavailable) {
			return DeliverResult{}, &UnavailableError{Message: "agentd managed session is not steerable", FallbackReason: "not_steerable", Reroute: true}
		}
		if errors.Is(err, agentdwire.ErrTransportUnavailable) {
			return DeliverResult{}, &UnavailableError{Message: "agentd local transport is unavailable", FallbackReason: "transport_error", Reroute: true}
		}
		if errors.Is(err, agentdwire.ErrScopeMismatch) {
			return DeliverResult{}, errors.New("agentd rejected mismatched managed control scope")
		}
		if errors.Is(err, agentdwire.ErrReplayConflict) {
			return DeliverResult{}, errors.New("agentd rejected conflicting managed control replay")
		}
		if errors.Is(err, agentdwire.ErrReplayCapacity) {
			return DeliverResult{}, errors.New("agentd managed control replay bound reached")
		}
		return DeliverResult{}, err
	}
	if receipt.CorrelationID != request.CorrelationID || receipt.Primitive != primitive ||
		receipt.SessionID != target.SessionID || receipt.Instance != request.Instance || receipt.ProjectID != request.ProjectID ||
		receipt.Identity != request.Identity || receipt.Operation != "steer" || receipt.EffectiveLevel != LevelSteer || receipt.FallbackReason != "" {
		return DeliverResult{}, errors.New("agentd returned mismatched control evidence")
	}
	if requireVendorMessageID && (receipt.VendorMessageID == "" || len(receipt.VendorMessageID) > 256 || strings.ContainsAny(receipt.VendorMessageID, "\x00\r\n")) {
		return DeliverResult{}, errors.New("agentd returned incomplete Claude Query input UUID evidence")
	}
	return DeliverResult{EffectiveLevel: LevelSteer, Primitive: receipt.Primitive}, nil
}

func init() {
	if err := Register(AgentdCodexPlugin{}); err != nil {
		panic(err)
	}
	if err := Register(AgentdClaudePlugin{}); err != nil {
		panic(err)
	}
}
