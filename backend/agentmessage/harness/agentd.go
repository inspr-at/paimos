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

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentdwire"
)

const (
	AdapterAgentdCodex = "agentd_codex"
	KindAgentdSession  = "agentd_session"
)

type AgentdTarget struct {
	Socket    string `json:"socket"`
	SessionID string `json:"session_id"`
}

type AgentdCodexPlugin struct{}

func (AgentdCodexPlugin) Name() string         { return AdapterAgentdCodex }
func (AgentdCodexPlugin) Kind() string         { return KindAgentdSession }
func (AgentdCodexPlugin) MaximumLevel() string { return LevelSteer }
func (AgentdCodexPlugin) Mode() string         { return ModeLocal }

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

// Deliver is intentionally steer-only and lease-correlated. Simple delivery
// remains on the ordinary inbox adapter; managed control never queue-fakes a
// steer and cannot be invoked through the legacy unleased --deliver-target.
func (AgentdCodexPlugin) Deliver(ctx context.Context, request DeliverRequest) (DeliverResult, error) {
	if request.Level != LevelSteer {
		return DeliverResult{}, &UnavailableError{Message: "agentd managed target is steer-only", FallbackReason: "not_steerable", Reroute: true}
	}
	if request.CorrelationID == "" || len(request.CorrelationID) > 128 || strings.ContainsAny(request.CorrelationID, "\x00\r\n") {
		return DeliverResult{}, &UnavailableError{Message: "agentd managed control requires a leased steer delivery"}
	}
	target, err := decodeAgentdTarget(request.TargetRef)
	if err != nil {
		return DeliverResult{}, &Error{Code: CodeTargetRefInvalid, Message: "agentd target is invalid"}
	}
	receipt, err := agentdwire.Steer(ctx, target.Socket, target.SessionID, agentdwire.ControlRequest{CorrelationID: request.CorrelationID, Text: request.Body})
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
		return DeliverResult{}, err
	}
	if receipt.CorrelationID != request.CorrelationID || receipt.Primitive != "codex app-server turn/steer" ||
		receipt.SessionID != target.SessionID || receipt.Operation != "steer" || receipt.EffectiveLevel != LevelSteer || receipt.FallbackReason != "" {
		return DeliverResult{}, errors.New("agentd returned mismatched control evidence")
	}
	return DeliverResult{EffectiveLevel: LevelSteer, Primitive: receipt.Primitive}, nil
}

func init() {
	if err := Register(AgentdCodexPlugin{}); err != nil {
		panic(err)
	}
}
