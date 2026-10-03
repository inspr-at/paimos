// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Flags seed registration only. Missing evidence preserves the last accepted
// value, including across a helper restart. A failed beat does not acknowledge
// its identity, so the next beat retries the same change.
func putHeartbeatModel(ctx context.Context, o heartbeatOptions, s *heartbeatSession, body map[string]any) {
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
		model = observedModel
		// Actual transcript evidence supersedes an applied request's prediction.
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
}

func acceptHeartbeatModel(s *heartbeatSession, body map[string]any) {
	if model, ok := body["model"].(string); ok {
		s.disk.SentModel = model
	}
	if effort, ok := body["reasoning_effort"].(string); ok {
		s.disk.SentEffort = effort
	}
	s.disk.ModelSent = true
}

func readHeartbeatModel(ctx context.Context, o heartbeatOptions, s *heartbeatSession) (string, string) {
	if ctx.Err() != nil || o.Harness != "claude" && o.Harness != "codex" {
		return "", ""
	}
	id := o.UsageID
	if id == "" {
		id = o.SourceSession
	}
	if o.Harness == "codex" && o.Capacity.Source == "codex" && o.Capacity.File != "" {
		if model, effort := readHeartbeatModelFile(ctx, o.Capacity.File, "codex", harnessCodexStream, id); model != "" {
			return model, effort
		}
	}
	target, err := resolveSessionHeartbeatUsage(o, s)
	if err != nil || target.Source != o.Harness || target.Path == "" {
		return "", ""
	}
	kind, ok := harnessKindForSource(target.Source)
	if !ok {
		return "", ""
	}
	return readHeartbeatModelFile(ctx, target.Path, target.Source, kind, id)
}

func readHeartbeatModelFile(ctx context.Context, path, source string, kind harnessFileKind, id string) (string, string) {
	f, err := openHarnessFile(kind, path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", ""
	}
	// Look at the newest records, independently of the usage cursor/backlog.
	// Oversized and incomplete lines cannot hide a later valid record.
	const maxTail = 16 << 20
	start := max(int64(0), info.Size()-maxTail)
	reader := bufio.NewReaderSize(io.NewSectionReader(f, start, info.Size()-start), heartbeatUsageLineMax)
	discard := start > 0
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
			ToModel  string `json:"toModel"`
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
		case record.Method == "model/rerouted":
			v.Model = record.Params.ToModel
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
		if sessionID != "" && thread != "" && thread != sessionID {
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
