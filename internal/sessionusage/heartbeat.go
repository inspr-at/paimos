// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// HeartbeatLine is one usage observation from a harness log. Absolute totals
// replace the previous cumulative figure. Deltas are added.
type HeartbeatLine struct {
	Model                            string
	Input, Output, Cached, Reasoning int64
	ReasoningKnown                   bool
	ID                               string
	Absolute                         bool
}

// CountReport builds a provisional unknown-billing usage report from known counters.
// A false result means the model or a counter cannot be reported.
func CountReport(model string, input, output, cached int64, cachedKnown bool) (UsageReport, bool) {
	canonical, err := canonicalModel(model)
	if err != nil || input < 0 || output < 0 || cached < 0 || input > maxToken || output > maxToken || cached > maxToken {
		return UsageReport{}, false
	}
	if cachedKnown && cached > input {
		return UsageReport{}, false
	}
	in, out := input, output
	report := UsageReport{Model: canonical, InputTokens: &in, OutputTokens: &out, Provisional: true, BillingMode: "unknown"}
	if cachedKnown {
		c := cached
		report.CachedInputTokens = &c
	}
	return report, true
}

// ParseHeartbeatLine reads one Codex rollout or Cursor result line.
// ok is false when the line is not a usable usage record. The error is set
// only when a counter overflows.
// CodexContextModel returns the model recorded on a Codex turn_context line.
// token_count events often omit the model; callers keep the latest context
// model and pass it as the fallback for the next usage line.
func CodexContextModel(line []byte) (string, bool) {
	if len(line) == 0 || len(line) > 1<<20 {
		return "", false
	}
	fields, err := decodeLine(line)
	if err != nil {
		return "", false
	}
	kind, err := parseString(fields["type"])
	if err != nil || kind != "turn_context" {
		return "", false
	}
	payload, err := objectField(fields, "payload")
	if err != nil {
		return "", false
	}
	model, err := modelFields(payload)
	if err != nil || model == "" {
		return "", false
	}
	return model, true
}

func ParseHeartbeatLine(source, fallback string, line []byte) (HeartbeatLine, bool, error) {
	if len(line) == 0 || len(line) > 1<<20 {
		return HeartbeatLine{}, false, nil
	}
	fields, err := decodeLine(line)
	if err != nil {
		return HeartbeatLine{}, false, nil
	}
	switch source {
	case "codex":
		return parseCodexHeartbeat(fields, fallback)
	case "cursor":
		return parseCursorHeartbeat(fields, fallback)
	default:
		return HeartbeatLine{}, false, nil
	}
}

func parseCodexHeartbeat(fields map[string]json.RawMessage, fallback string) (HeartbeatLine, bool, error) {
	line, ok := codexTotals(fields)
	if !ok {
		return HeartbeatLine{}, false, nil
	}
	model := line.Model
	if model == "" {
		model = fallback
	}
	model, err := canonicalModel(model)
	if err != nil {
		return HeartbeatLine{}, false, nil
	}
	line.Model = model
	return line, true, nil
}

// CodexTotals reads one Codex cumulative token record without requiring a
// model. The totals are session-wide; Model is the record's own model or "".
// Callers attribute the increase over their session baseline to the model in
// context, so a model switch never re-counts earlier tokens.
func CodexTotals(line []byte) (HeartbeatLine, bool) {
	if len(line) == 0 || len(line) > 1<<20 {
		return HeartbeatLine{}, false
	}
	fields, err := decodeLine(line)
	if err != nil {
		return HeartbeatLine{}, false
	}
	return codexTotals(fields)
}

func codexTotals(fields map[string]json.RawMessage) (HeartbeatLine, bool) {
	rec, outcome, err := classifyCodex(fields)
	if err != nil || outcome != outcomeUse || rec.cumulative == nil || !rec.cumulative.inputKnown {
		return HeartbeatLine{}, false
	}
	model := ""
	if rec.model != "" {
		if canonical, err := canonicalModel(rec.model); err == nil {
			model = canonical
		}
	}
	snap := rec.cumulative
	cached := snap.cached
	if !snap.cachedKnown {
		cached = 0
	}
	if cached > snap.input {
		return HeartbeatLine{}, false
	}
	return HeartbeatLine{Model: model, Input: snap.input, Output: snap.output, Cached: cached, Reasoning: snap.reasoning, ReasoningKnown: snap.reasoningKnown, Absolute: true}, true
}

func parseCursorHeartbeat(fields map[string]json.RawMessage, fallback string) (HeartbeatLine, bool, error) {
	rec, outcome, err := classifyCursor(fields)
	if err != nil || outcome != outcomeUse || rec.delta == nil || !rec.delta.inputKnown || !rec.delta.cachedKnown {
		return HeartbeatLine{}, false, nil
	}
	model := rec.model
	if model == "" {
		model = fallback
	}
	model, err = canonicalModel(model)
	if err != nil {
		return HeartbeatLine{}, false, nil
	}
	return HeartbeatLine{Model: model, Input: rec.delta.input, Output: rec.delta.output, Cached: rec.delta.cached, Reasoning: rec.delta.reasoning, ReasoningKnown: rec.delta.reasoningKnown, ID: rec.id}, true, nil
}

// CursorPromptUsage reads a Cursor prompt result usage object. Cost-only ACP
// updates are not token records and return false.
func CursorPromptUsage(fields map[string]json.RawMessage, model string) (UsageReport, bool) {
	if len(fields) == 0 {
		return UsageReport{}, false
	}
	snap, err := parseCursorUsage(fields)
	if err != nil || !snap.inputKnown || !snap.cachedKnown {
		return UsageReport{}, false
	}
	report, ok := CountReport(model, snap.input, snap.output, snap.cached, true)
	if ok && snap.reasoningKnown {
		reasoning := snap.reasoning
		report.ReasoningTokens = &reasoning
	}
	return report, ok
}

// ParseGrokUsage reads a rewritten usage.json snapshot. Per-model counters win
// over the session total. costUsdTicks is ignored. Inclusive input is
// inputTokens + cachedReadTokens + cacheCreationTokens. Cached input is
// cachedReadTokens only.
func ParseGrokUsage(raw []byte, fallback string) ([]HeartbeatLine, error) {
	if len(bytes.TrimSpace(raw)) == 0 || len(raw) > 1<<20 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var root map[string]json.RawMessage
	if err := dec.Decode(&root); err != nil {
		return nil, nil
	}
	session, err := decodeObject(root["session"], nil)
	if err != nil {
		return nil, nil
	}
	if rawUsage, ok := session["modelUsage"]; ok {
		models, err := decodeObject(rawUsage, nil)
		if err != nil {
			return nil, nil
		}
		if len(models) > 0 {
			return grokModels(models)
		}
	}
	line, ok, err := grokOne(fallbackModel(session, fallback), session)
	if err != nil || !ok {
		return nil, err
	}
	return []HeartbeatLine{line}, nil
}

func grokModels(models map[string]json.RawMessage) ([]HeartbeatLine, error) {
	out := make([]HeartbeatLine, 0, len(models))
	for name, raw := range models {
		fields, err := decodeObject(raw, nil)
		if err != nil {
			continue
		}
		line, ok, err := grokOne(name, fields)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, line)
		}
	}
	return out, nil
}

func fallbackModel(session map[string]json.RawMessage, fallback string) string {
	if raw, ok := session["primaryModelId"]; ok {
		if text, err := parseString(raw); err == nil && text != "" {
			return text
		}
	}
	return fallback
}

func grokOne(model string, fields map[string]json.RawMessage) (HeartbeatLine, bool, error) {
	input, okIn := optionalCount(fields, "inputTokens")
	output, okOut := optionalCount(fields, "outputTokens")
	if !okIn || !okOut {
		return HeartbeatLine{}, false, nil
	}
	read, okRead := optionalCount(fields, "cachedReadTokens")
	if !okRead {
		read = 0
	}
	created, okCreated := optionalCount(fields, "cacheCreationTokens")
	if !okCreated {
		created = 0
	}
	var reasoning int64
	var reasoningKnown bool
	if raw, ok := fields["reasoningTokens"]; ok && len(bytes.TrimSpace(raw)) > 0 && string(raw) != "null" {
		parsed, err := parseCount(raw)
		if err != nil || parsed > output {
			return HeartbeatLine{}, false, nil
		}
		reasoning, reasoningKnown = parsed, true
	}
	if input > maxToken-read || input+read > maxToken-created {
		return HeartbeatLine{}, false, fmt.Errorf("%w: inclusive input overflow", ErrRejected)
	}
	canonical, err := canonicalModel(model)
	if err != nil {
		return HeartbeatLine{}, false, nil
	}
	return HeartbeatLine{Model: canonical, Input: input + read + created, Output: output, Cached: read, Reasoning: reasoning, ReasoningKnown: reasoningKnown, Absolute: true}, true, nil
}

func optionalCount(fields map[string]json.RawMessage, key string) (int64, bool) {
	raw, ok := fields[key]
	if !ok || len(bytes.TrimSpace(raw)) == 0 || string(raw) == "null" {
		return 0, false
	}
	n, err := parseCount(raw)
	if err != nil {
		return 0, false
	}
	return n, true
}
