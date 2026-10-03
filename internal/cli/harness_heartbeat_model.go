// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// heartbeatModelUpdate holds proposed request/observation state. Building the
// POST must not consume a request before the server acknowledges its identity.
type heartbeatModelUpdate struct {
	requestedModel, requestedEffort string
	baseline                        *heartbeatModelBaseline
}

// Flags seed registration only. Missing evidence preserves the last accepted
// value, including across a helper restart. A failed beat does not acknowledge
// its identity, so the next beat retries the same change.
func putHeartbeatModel(ctx context.Context, o heartbeatOptions, s *heartbeatSession, body map[string]any) heartbeatModelUpdate {
	next := *s
	s = &next
	model, effort := s.disk.SentModel, s.disk.SentEffort
	// Legacy helper state has no accepted identity cache. Only new transcript
	// evidence may fill it; stale launch flags must not overwrite server data.
	if s.disk.RequestedModel != "" && o.Model == s.disk.ModelFlagAtRequest && o.Effort == s.disk.EffortFlagAtRequest {
		model, effort = s.disk.RequestedModel, s.disk.RequestedEffort
	} else {
		s.disk.RequestedModel, s.disk.RequestedEffort = "", ""
	}
	observedModel, observedEffort := readHeartbeatModel(ctx, o, s)
	if observedModel != "" {
		if observedModel != model {
			// An effort belongs to its model, never to a different request.
			effort = ""
		}
		model = observedModel
		// Evidence supersedes the prediction, but its observation fence must
		// remain: a model-only record cannot revive pre-request effort.
		s.disk.RequestedModel, s.disk.RequestedEffort = "", ""
	}
	if observedEffort != "" {
		effort = observedEffort
	}
	if model != "" && (!s.disk.ModelSent || model != s.disk.SentModel) {
		body["model"] = model
	}
	if effort != "" && (!s.disk.ModelSent || effort != s.disk.SentEffort) {
		body["reasoning_effort"] = effort
	}
	return heartbeatModelUpdate{
		requestedModel: s.disk.RequestedModel, requestedEffort: s.disk.RequestedEffort,
		baseline: s.disk.RequestedModelBaseline,
	}
}

func acceptHeartbeatModel(s *heartbeatSession, body map[string]any, update heartbeatModelUpdate) {
	s.disk.RequestedModel, s.disk.RequestedEffort = update.requestedModel, update.requestedEffort
	s.disk.RequestedModelBaseline = update.baseline
	if model, ok := body["model"].(string); ok {
		s.disk.SentModel = model
	}
	if effort, ok := body["reasoning_effort"].(string); ok {
		s.disk.SentEffort = effort
	}
	s.disk.ModelSent = true
}

func readHeartbeatModel(ctx context.Context, o heartbeatOptions, s *heartbeatSession) (string, string) {
	target, kind, ok := heartbeatModelTarget(ctx, o, s)
	if !ok {
		return "", ""
	}
	id := o.UsageID
	if id == "" {
		id = o.SourceSession
	}
	var offset int64
	if s.disk.RequestedModelBaseline != nil || s.disk.RequestedModel != "" {
		baseline := s.disk.RequestedModelBaseline
		if baseline == nil || baseline.FileHash != heartbeatModelFileHash(target) {
			// A legacy request, unavailable file or replaced source has no
			// observation fence yet. Establish it without replaying history.
			s.disk.RequestedModelBaseline = heartbeatModelFileBaseline(target, kind)
			return "", ""
		}
		offset = baseline.Offset
	}
	return readHeartbeatModelFileSince(ctx, target.Path, target.Source, kind, id, offset)
}

// Each harness has one authoritative session log. Codex capacity captures
// are shared and may contain older thread-start responses; never use them
// as either an override or a fallback for the bound rollout.
func heartbeatModelTarget(ctx context.Context, o heartbeatOptions, s *heartbeatSession) (usageTarget, harnessFileKind, bool) {
	if ctx.Err() != nil || o.Harness != "claude" && o.Harness != "codex" {
		return usageTarget{}, 0, false
	}
	target, err := resolveSessionHeartbeatUsageWithBinding(o, s, false)
	if err != nil || target.Source != o.Harness || target.Path == "" {
		return usageTarget{}, 0, false
	}
	kind, ok := harnessKindForSource(target.Source)
	if !ok {
		return usageTarget{}, 0, false
	}
	abs, ok := resolveHarnessPath(kind, target.Path)
	if !ok {
		return usageTarget{}, 0, false
	}
	target.Path = abs
	return target, kind, true
}

// Persist only an opaque source identity and byte boundary, never raw records.
type heartbeatModelBaseline struct {
	FileHash string `json:"file_hash"`
	Offset   int64  `json:"offset"`
}

func heartbeatModelFileHash(target usageTarget) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(target.Source+"\x00"+target.Path)))
}

func heartbeatModelFileBaseline(target usageTarget, kind harnessFileKind) *heartbeatModelBaseline {
	info, err := statHarnessFile(kind, target.Path)
	if err != nil {
		return nil
	}
	return &heartbeatModelBaseline{FileHash: heartbeatModelFileHash(target), Offset: info.Size()}
}

func captureHeartbeatModelBaseline(ctx context.Context, o heartbeatOptions, s *heartbeatSession) *heartbeatModelBaseline {
	target, kind, ok := heartbeatModelTarget(ctx, o, s)
	if !ok {
		return nil
	}
	return heartbeatModelFileBaseline(target, kind)
}

func readHeartbeatModelFile(ctx context.Context, path, source string, kind harnessFileKind, id string) (string, string) {
	return readHeartbeatModelFileSince(ctx, path, source, kind, id, 0)
}

func readHeartbeatModelFileSince(ctx context.Context, path, source string, kind harnessFileKind, id string, offset int64) (string, string) {
	f, err := openHarnessFile(kind, path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || offset < 0 || offset > info.Size() {
		return "", ""
	}
	// Look at the newest records, independently of the usage cursor/backlog.
	// Oversized and incomplete lines cannot hide a later valid record.
	const maxTail = 16 << 20
	start := max(int64(0), info.Size()-maxTail, offset)
	reader := bufio.NewReaderSize(io.NewSectionReader(f, start, info.Size()-start), heartbeatUsageLineMax)
	discard := false
	if start > 0 {
		var previous [1]byte
		if _, err := f.ReadAt(previous[:], start-1); err != nil {
			return "", ""
		}
		discard = previous[0] != '\n'
	}
	model, effort := "", ""
	for ctx.Err() == nil {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			discard = true
			continue
		}
		if err != nil {
			break
		}
		if discard {
			discard = false
			continue
		}
		m, e := heartbeatModelLine(source, line, id)
		if m != "" {
			if m != model {
				effort = ""
			}
			model = m
		}
		if e != "" {
			effort = e
		}
	}
	if ctx.Err() != nil {
		return "", ""
	}
	return model, effort
}

// Decode only metadata from recognized records; message text and tool output
// never become model evidence. No raw record or path reaches logs/state/API.
func heartbeatModelLine(source string, line []byte, sessionID string) (string, string) {
	type identity struct {
		Model           string `json:"model"`
		Effort          string `json:"effort"`
		ReasoningEffort string `json:"reasoning_effort"`
		CamelEffort     string `json:"reasoningEffort"`
		OutputConfig    struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
		Reasoning json.RawMessage `json:"reasoning"`
	}
	var record struct {
		Type      string   `json:"type"`
		SessionID string   `json:"sessionId"`
		Message   identity `json:"message"`
		Payload   identity `json:"payload"`
		Method    string   `json:"method"`
		Params    struct {
			identity
			ThreadID string `json:"threadId"`
			Thread   struct {
				identity
				ID string `json:"id"`
			} `json:"thread"`
			Turn identity `json:"turn"`
		} `json:"params"`
		Result struct {
			identity
			Thread struct {
				identity
				ID string `json:"id"`
			} `json:"thread"`
		} `json:"result"`
	}
	if json.Unmarshal(line, &record) != nil {
		return "", ""
	}
	var v identity
	if source == "claude" {
		if record.Type != "assistant" || sessionID != "" && record.SessionID != "" && record.SessionID != sessionID {
			return "", ""
		}
		v = record.Message
	} else {
		switch {
		case record.Type == "turn_context":
			v = record.Payload
		case record.Method == "thread/started":
			v = record.Params.Thread.identity
		case record.Method == "turn/started":
			v = record.Params.Turn
			if v.Model == "" {
				v = record.Params.identity
			}
		case record.Method == "thread/tokenUsage/updated":
			v = record.Params.identity
		case record.Method == "" && record.Result.Thread.ID != "":
			v = record.Result.identity
			if v.Model == "" {
				v = record.Result.Thread.identity
			}
		default:
			return "", ""
		}
		thread := record.Params.ThreadID
		if thread == "" {
			thread = record.Params.Thread.ID
		}
		if thread == "" {
			thread = record.Result.Thread.ID
		}
		// Rollout turn_context is scoped by its bound file. Shared app-server
		// records require both sides of an explicit, matching thread binding.
		if record.Type != "turn_context" && (sessionID == "" || thread == "" || thread != sessionID) {
			return "", ""
		}
	}
	model := strings.TrimSpace(v.Model)
	if len(model) > 128 || !heartbeatModelRE.MatchString(model) {
		return "", ""
	}
	effort := v.ReasoningEffort
	if effort == "" {
		effort = v.CamelEffort
	}
	if effort == "" {
		effort = v.Effort
	}
	if effort == "" {
		effort = v.OutputConfig.Effort
	}
	if effort == "" && len(v.Reasoning) > 0 {
		var reasoning struct {
			Effort string `json:"effort"`
		}
		if json.Unmarshal(v.Reasoning, &reasoning) == nil {
			effort = reasoning.Effort
		}
	}
	switch effort {
	case "default", "minimal", "none", "low", "medium", "high", "xhigh", "max", "ultra":
	default:
		effort = ""
	}
	return model, effort
}
