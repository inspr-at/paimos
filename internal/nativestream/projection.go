// SPDX-License-Identifier: AGPL-3.0-only

package nativestream

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

// ExportPolicy is the existing authorized output policy, applied independently
// on both sides of the relay. Missing policies fail closed. Terminal must prove
// qualified owner-consented screen export; login/auth PTYs are never eligible.
type ExportPolicy struct {
	Text     func(context.Context, nativecontract.Binding, string) (string, error)
	Terminal func(context.Context, nativecontract.Binding, []byte) error
}

type Projector struct {
	key       [32]byte
	policy    ExportPolicy
	validator *nativecontract.Validator
	opaque    *regexp.Regexp
}

func NewProjector(policy ExportPolicy) (*Projector, error) {
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	p := &Projector{policy: policy, validator: v, opaque: regexp.MustCompile(`^ref:[0-9a-f]{64}$`)}
	if _, err := rand.Read(p.key[:]); err != nil {
		return nil, Refuse(nativecontract.ReasonNativeFailure)
	}
	return p, nil
}

func (p *Projector) reference(binding nativecontract.Binding, domain, value string) string {
	if p.opaque.MatchString(value) {
		return value
	}
	h := hmac.New(sha256.New, p.key[:])
	for _, part := range []string{binding.TenantID, binding.OwnedSessionID, binding.RuntimeEpoch, domain, value} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return "ref:" + hex.EncodeToString(h.Sum(nil))
}

func (p *Projector) text(ctx context.Context, binding nativecontract.Binding, text string) (string, error) {
	if len(text) > nativecontract.InputUTF8Bytes || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return "", Refuse(nativecontract.ReasonBufferFull)
	}
	if sensitiveText(text) {
		return "", Refuse(nativecontract.ReasonFrameOmitted)
	}
	if p.policy.Text == nil {
		return "", Refuse(nativecontract.ReasonQualificationMissing)
	}
	out, err := p.policy.Text(ctx, binding, text)
	if err != nil {
		return "", Refuse(nativecontract.ReasonFrameOmitted)
	}
	if len(out) > nativecontract.InputUTF8Bytes || !utf8.ValidString(out) || strings.ContainsRune(out, 0) || sensitiveText(out) {
		return "", Refuse(nativecontract.ReasonFrameOmitted)
	}
	return out, nil
}

// boundedNative rejects duplicates, trailing documents, invalid UTF-8 and deep
// nesting before allocating the provider object. No raw decode errors escape.
func boundedNative(raw []byte) (map[string]any, error) {
	if len(raw) > nativecontract.NativeFrameBeforeDecodeBytes || !utf8.Valid(raw) {
		return nil, Refuse(nativecontract.ReasonBufferFull)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > nativecontract.NativeFrameDecodeDepth {
			return nil, Refuse(nativecontract.ReasonBufferFull)
		}
		t, err := d.Token()
		if err != nil {
			return nil, Refuse(nativecontract.ReasonInvalidInput)
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				m := map[string]any{}
				for d.More() {
					key, err := d.Token()
					if err != nil {
						return nil, Refuse(nativecontract.ReasonInvalidInput)
					}
					name, ok := key.(string)
					if !ok {
						return nil, Refuse(nativecontract.ReasonInvalidInput)
					}
					if _, duplicate := m[name]; duplicate {
						return nil, Refuse(nativecontract.ReasonInvalidInput)
					}
					value, err := read(depth + 1)
					if err != nil {
						return nil, err
					}
					m[name] = value
				}
				end, err := d.Token()
				if err != nil || end != json.Delim('}') {
					return nil, Refuse(nativecontract.ReasonInvalidInput)
				}
				return m, nil
			case '[':
				a := []any{}
				for d.More() {
					value, err := read(depth + 1)
					if err != nil {
						return nil, err
					}
					a = append(a, value)
				}
				end, err := d.Token()
				if err != nil || end != json.Delim(']') {
					return nil, Refuse(nativecontract.ReasonInvalidInput)
				}
				return a, nil
			}
			return nil, Refuse(nativecontract.ReasonInvalidInput)
		}
		return t, nil
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, Refuse(nativecontract.ReasonInvalidInput)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, Refuse(nativecontract.ReasonInvalidInput)
	}
	return object, nil
}

type export struct {
	p       *Projector
	ctx     context.Context
	binding nativecontract.Binding
	paths   []string
	invalid bool
}

func (e *export) redact(path string) {
	// Unknown field names may themselves contain credentials or local paths.
	// Paths describe known schema locations, never arbitrary native key names.
	if len(e.paths) >= nativecontract.ExpertRedactionPaths {
		e.invalid = true
		return
	}
	e.paths = append(e.paths, path)
}

func (e *export) fields(src map[string]any, path string, allowed ...string) map[string]any {
	out := map[string]any{}
	for key := range src {
		if !slices.Contains(allowed, key) {
			e.redact(path + ".[omitted]")
		}
	}
	return out
}

func (e *export) id(src, out map[string]any, key, path, domain string) {
	if value, ok := src[key].(string); ok && len(value) > 0 && len(value) <= 4096 {
		out[key] = e.p.reference(e.binding, domain, value)
		e.redact(path + "." + key)
	} else if _, exists := src[key]; exists {
		e.invalid = true
	}
}

func (e *export) enum(src, out map[string]any, key string, values ...string) {
	if value, exists := src[key]; exists {
		if s, ok := value.(string); ok && slices.Contains(values, s) {
			out[key] = s
		} else {
			e.invalid = true
		}
	}
}

func (e *export) number(src, out map[string]any, key string) {
	if value, exists := src[key]; exists {
		n, ok := value.(json.Number)
		if !ok {
			e.invalid = true
			return
		}
		f, err := n.Float64()
		if err != nil || f < 0 || f > 9007199254740991 {
			e.invalid = true
			return
		}
		out[key] = n
	}
}

func (e *export) contentText(src, out map[string]any, key, path string) {
	if value, exists := src[key]; exists {
		text, ok := value.(string)
		if !ok {
			e.invalid = true
			return
		}
		outText, err := e.p.text(e.ctx, e.binding, text)
		if err != nil || outText == "" {
			e.redact(path + "." + key)
			return
		}
		out[key] = outText
	}
}

func (e *export) message(src map[string]any, path string) map[string]any {
	out := e.fields(src, path, "id", "type", "role", "content", "stop_reason")
	e.id(src, out, "id", path, "message")
	e.enum(src, out, "type", "message")
	e.enum(src, out, "role", "assistant")
	e.enum(src, out, "stop_reason", "end_turn", "max_tokens", "stop_sequence", "tool_use", "pause_turn", "refusal")
	if content, exists := src["content"]; exists {
		blocks, ok := content.([]any)
		if !ok {
			e.invalid = true
			return out
		}
		allowed := []any{}
		for _, block := range blocks {
			m, ok := block.(map[string]any)
			if !ok {
				e.invalid = true
				continue
			}
			if m["type"] != "text" {
				e.redact(path + ".content.[omitted]")
				continue
			}
			b := e.fields(m, path+".content[]", "type", "text")
			b["type"] = "text"
			e.contentText(m, b, "text", path+".content[]")
			if _, hasText := b["text"]; hasText {
				allowed = append(allowed, b)
			}
		}
		out["content"] = allowed
	}
	return out
}

func (e *export) claude(src map[string]any) (string, map[string]any) {
	kind, _ := src["type"].(string)
	if !slices.Contains([]string{"assistant", "stream_event", "system", "result", "tool_progress", "tool_use_summary", "task_started", "task_progress", "task_notification", "rate_limit_event"}, kind) {
		e.invalid = true
		return "unknown", nil
	}
	out := e.fields(src, "$", "type", "subtype", "is_error", "stop_reason", "duration_ms", "duration_api_ms", "num_turns", "total_cost_usd", "tool_name", "task_id", "session_id", "uuid", "parent_tool_use_id", "user_message_uuid", "user_message_uuids", "message", "event")
	out["type"] = kind
	e.enum(src, out, "subtype", "init", "status", "compact_boundary", "success", "error_during_execution", "error_max_turns", "error_max_budget_usd", "error_during_resume")
	e.enum(src, out, "stop_reason", "end_turn", "max_tokens", "stop_sequence", "tool_use", "pause_turn", "refusal")
	for _, key := range []string{"session_id", "uuid", "parent_tool_use_id", "user_message_uuid", "task_id"} {
		e.id(src, out, key, "$", key)
	}
	if values, exists := src["user_message_uuids"]; exists {
		list, ok := values.([]any)
		if !ok || len(list) > 32 {
			e.invalid = true
		} else {
			refs := []string{}
			for _, value := range list {
				if s, ok := value.(string); ok && len(s) <= 4096 {
					refs = append(refs, e.p.reference(e.binding, "user_message_uuid", s))
					e.redact("$.user_message_uuids[]")
				} else {
					e.invalid = true
				}
			}
			out["user_message_uuids"] = refs
		}
	}
	for _, key := range []string{"duration_ms", "duration_api_ms", "num_turns", "total_cost_usd"} {
		e.number(src, out, key)
	}
	if value, exists := src["is_error"]; exists {
		if b, ok := value.(bool); ok {
			out["is_error"] = b
		} else {
			e.invalid = true
		}
	}
	if value, exists := src["tool_name"]; exists {
		if s, ok := value.(string); ok && slices.Contains([]string{"Bash", "Read", "Write", "Edit", "Glob", "Grep", "Agent", "Task", "WebFetch", "WebSearch", "TodoWrite"}, s) {
			out["tool_name"] = s
		} else {
			e.redact("$.tool_name")
		}
	}
	if message, exists := src["message"]; exists {
		m, ok := message.(map[string]any)
		if !ok || kind != "assistant" {
			e.invalid = true
		} else {
			out["message"] = e.message(m, "$.message")
		}
	}
	if event, exists := src["event"]; exists {
		m, ok := event.(map[string]any)
		if !ok || kind != "stream_event" {
			e.invalid = true
			return kind, out
		}
		typeName, _ := m["type"].(string)
		if !slices.Contains([]string{"message_start", "message_delta", "message_stop", "content_block_start", "content_block_delta", "content_block_stop"}, typeName) {
			e.invalid = true
			return kind, out
		}
		ev := e.fields(m, "$.event", "type", "index", "delta", "content_block", "message")
		ev["type"] = typeName
		e.number(m, ev, "index")
		if delta, exists := m["delta"]; exists {
			d, ok := delta.(map[string]any)
			if !ok {
				e.invalid = true
			} else if typeName == "content_block_delta" && d["type"] == "text_delta" {
				a := e.fields(d, "$.event.delta", "type", "text")
				a["type"] = "text_delta"
				e.contentText(d, a, "text", "$.event.delta")
				ev["delta"] = a
			} else if typeName == "message_delta" {
				a := e.fields(d, "$.event.delta", "stop_reason")
				e.enum(d, a, "stop_reason", "end_turn", "max_tokens", "stop_sequence", "tool_use", "pause_turn", "refusal")
				ev["delta"] = a
			} else {
				e.redact("$.event.delta")
			}
		}
		if block, exists := m["content_block"]; exists {
			b, ok := block.(map[string]any)
			if !ok {
				e.invalid = true
			} else if b["type"] == "text" && typeName == "content_block_start" {
				a := e.fields(b, "$.event.content_block", "type", "text")
				a["type"] = "text"
				e.contentText(b, a, "text", "$.event.content_block")
				ev["content_block"] = a
			} else {
				e.redact("$.event.content_block")
			}
		}
		if message, exists := m["message"]; exists {
			if a, ok := message.(map[string]any); ok && typeName == "message_start" {
				ev["message"] = e.message(a, "$.event.message")
			} else {
				e.invalid = true
			}
		}
		out["event"] = ev
	}
	return kind, out
}

func (e *export) codex(src map[string]any) (string, map[string]any) {
	method, _ := src["method"].(string)
	// This is a compiled codec vocabulary, not a client-supplied method allowlist.
	if !slices.Contains([]string{"thread/started", "turn/started", "turn/completed", "item/started", "item/completed", "item/agentMessage/delta", "thread/tokenUsage/updated", "account/rateLimits/updated", "item/commandExecution/requestApproval", "item/fileChange/requestApproval"}, method) {
		e.invalid = true
		return "unknown", nil
	}
	out := e.fields(src, "$", "method", "params")
	out["method"] = method
	params, ok := src["params"].(map[string]any)
	if !ok {
		e.invalid = true
		return method, out
	}
	a := e.fields(params, "$.params", "threadId", "turnId", "itemId", "item", "turn", "delta", "tokenUsage", "rateLimits")
	for _, key := range []string{"threadId", "turnId", "itemId"} {
		e.id(params, a, key, "$.params", key)
	}
	if value, exists := params["delta"]; exists {
		if method == "item/agentMessage/delta" {
			e.contentText(map[string]any{"delta": value}, a, "delta", "$.params")
		} else {
			e.redact("$.params.delta")
		}
	}
	if item, exists := params["item"]; exists {
		m, ok := item.(map[string]any)
		if !ok {
			e.invalid = true
		} else {
			b := e.fields(m, "$.params.item", "id", "type", "status", "text")
			e.id(m, b, "id", "$.params.item", "itemId")
			e.enum(m, b, "type", "agentMessage", "commandExecution", "fileChange", "mcpToolCall", "reasoning", "webSearch", "plan")
			e.enum(m, b, "status", "inProgress", "completed", "failed", "declined")
			if m["type"] == "agentMessage" {
				e.contentText(m, b, "text", "$.params.item")
			} else if _, exists := m["text"]; exists {
				e.redact("$.params.item.text")
			}
			a["item"] = b
		}
	}
	if turn, exists := params["turn"]; exists {
		m, ok := turn.(map[string]any)
		if !ok {
			e.invalid = true
		} else {
			b := e.fields(m, "$.params.turn", "id", "status")
			e.id(m, b, "id", "$.params.turn", "turnId")
			e.enum(m, b, "status", "inProgress", "completed", "interrupted", "failed")
			a["turn"] = b
		}
	}
	// Quota has its own typed port. Do not copy arbitrary measurement objects.
	for _, key := range []string{"tokenUsage", "rateLimits"} {
		if _, exists := params[key]; exists {
			e.redact("$.params." + key)
		}
	}
	out["params"] = a
	return method, out
}

// Expert preserves native envelope names/casing, never arbitrary payloads.
// Unknown providers/shapes and overflow have empty bodies and safe metadata.
func (p *Projector) Expert(ctx context.Context, binding nativecontract.Binding, provider string, raw []byte) nativecontract.ExpertFrame {
	frame := nativecontract.ExpertFrame{Provider: provider, EventName: "unknown", FrameJSON: "{}", RedactedPaths: []string{}, Omitted: true, OriginalBytes: int64(len(raw))}
	if !slices.Contains([]string{"claude", "codex", "acp", "pi"}, provider) {
		frame.Provider = "unknown"
		return frame
	}
	src, err := boundedNative(raw)
	if err != nil {
		return frame
	}
	e := &export{p: p, ctx: ctx, binding: binding, paths: []string{}}
	var projected map[string]any
	switch provider {
	case "claude":
		frame.EventName, projected = e.claude(src)
	case "codex":
		frame.EventName, projected = e.codex(src)
	default:
		return frame
	}
	if e.invalid {
		return frame
	}
	data, err := json.Marshal(projected)
	if err != nil || len(data) > nativecontract.ExportedFrameBytes/2 {
		return frame
	}
	frame.FrameJSON = string(data)
	frame.RedactedPaths = e.paths
	frame.Omitted = false
	if _, err := Encode(p.validator, "NativeExpertFrame", frame); err != nil {
		frame.FrameJSON = "{}"
		frame.RedactedPaths = []string{}
		frame.Omitted = true
	}
	return frame
}

// Sanitize independently checks the public boundary; a provider's assertion of
// prior filtering is never sufficient. Explicit final bodies are refused here.
func (p *Projector) Sanitize(ctx context.Context, event nativecontract.StreamEvent, access nativecontract.ValidationContext) (nativecontract.StreamEvent, error) {
	if event.Kind == "assistant.final" {
		return event, Refuse(nativecontract.ReasonRetentionUnapproved)
	}
	if event.Kind == "snapshot" && !access.Private {
		var frame nativecontract.SessionView
		if err := p.validator.DecodeJSON("NativeSessionView", event.Payload, &frame); err != nil {
			return event, err
		}
		frame = nativecontract.PersonProjection(frame, access.PrincipalKind, access.Web)
		event.Payload, _ = json.Marshal(frame)
	}
	data, err := Encode(p.validator, "NativeStreamEvent", event)
	if err != nil {
		return event, err
	}
	// A verified private publisher produces frames; this does not permit an
	// agent to receive Expert/terminal. The public delivery path has Private=false.
	if access.Private {
		access.PrincipalKind = "person"
	}
	if err := p.validator.ValidateBoundJSON("NativeStreamEvent", data, access); err != nil {
		return event, err
	}
	var payload any
	switch event.Kind {
	case "expert":
		var frame nativecontract.ExpertFrame
		_ = json.Unmarshal(event.Payload, &frame)
		out := p.Expert(ctx, event.Binding, frame.Provider, []byte(frame.FrameJSON))
		if frame.Omitted {
			out.FrameJSON = "{}"
			out.Omitted = true
			out.EventName = "unknown"
		}
		out.OriginalBytes = frame.OriginalBytes
		// Original redaction paths are untrusted too; keep only safe structural
		// paths emitted by this implementation, with a finite union.
		for _, path := range frame.RedactedPaths {
			if len(out.RedactedPaths) >= nativecontract.ExpertRedactionPaths {
				out.Omitted = true
				out.FrameJSON = "{}"
				break
			}
			if safeRedactionPath(path) && !slices.Contains(out.RedactedPaths, path) {
				out.RedactedPaths = append(out.RedactedPaths, path)
			}
		}
		payload = out
	case "structured":
		var frame nativecontract.StructuredFrame
		_ = json.Unmarshal(event.Payload, &frame)
		if frame.Kind == "final_reference" || frame.Kind == "tool_status" {
			frame.Text = nil
		}
		if frame.Text != nil {
			text, err := p.text(ctx, event.Binding, *frame.Text)
			if err != nil {
				return event, err
			}
			frame.Text = &text
		}
		payload = frame
	case "assistant.delta":
		var frame nativecontract.AssistantDelta
		_ = json.Unmarshal(event.Payload, &frame)
		text, err := p.text(ctx, event.Binding, frame.Text)
		if err != nil {
			return event, err
		}
		frame.Text = text
		payload = frame
	case "tool.start":
		var frame nativecontract.ToolStart
		_ = json.Unmarshal(event.Payload, &frame)
		frame.ArgsSummary = nil
		payload = frame
	case "tool.result":
		var frame nativecontract.ToolResult
		_ = json.Unmarshal(event.Payload, &frame)
		frame.ResultSummary = nil
		payload = frame
	case "input.delivered":
		var frame nativecontract.InputDelivered
		_ = json.Unmarshal(event.Payload, &frame)
		text, err := p.text(ctx, event.Binding, frame.Text)
		if err != nil {
			return event, err
		}
		frame.Text = text
		payload = frame
	case "approval":
		var frame nativecontract.Approval
		_ = json.Unmarshal(event.Payload, &frame)
		text, err := p.text(ctx, event.Binding, frame.Summary)
		if err != nil {
			return event, err
		}
		frame.Summary = text
		payload = frame
	case "snapshot":
		var frame nativecontract.SessionView
		_ = json.Unmarshal(event.Payload, &frame)
		payload = nativecontract.PersonProjection(frame, access.PrincipalKind, access.Web)
	case "terminal":
		var frame nativecontract.TerminalFrame
		_ = json.Unmarshal(event.Payload, &frame)
		if p.policy.Terminal == nil {
			return event, Refuse(nativecontract.ReasonQualificationMissing)
		}
		if frame.TerminalEpoch != event.Binding.RuntimeEpoch {
			return event, Refuse(nativecontract.ReasonWrongGeneration)
		}
		decoded, _ := base64.StdEncoding.Strict().DecodeString(frame.DataBase64)
		if err := p.policy.Terminal(ctx, event.Binding, decoded); err != nil {
			return event, Refuse(nativecontract.ReasonFrameOmitted)
		}
	}
	if payload != nil {
		event.Payload, err = json.Marshal(payload)
		if err != nil {
			return event, Refuse(nativecontract.ReasonInvalidInput)
		}
	}
	_, err = Encode(p.validator, "NativeStreamEvent", event)
	return event, err
}

// SanitizeHistory is exclusively for the authorized Host.EventsPage result.
// Host proves that a final was retained through FinalSink under the applicable
// permission/retention policy. Live publication, SSE and RAM replay never use
// this exception, and Buffer.Append still refuses every assistant.final body.
func (p *Projector) SanitizeHistory(ctx context.Context, event nativecontract.StreamEvent, access nativecontract.ValidationContext) (nativecontract.StreamEvent, error) {
	if event.Kind != "assistant.final" {
		return p.Sanitize(ctx, event, access)
	}
	if access.Private {
		return event, Refuse(nativecontract.ReasonWrongPrincipal)
	}
	data, err := Encode(p.validator, "NativeStreamEvent", event)
	if err != nil {
		return event, err
	}
	if err := p.validator.ValidateBoundJSON("NativeStreamEvent", data, access); err != nil {
		return event, err
	}
	var final nativecontract.AssistantFinal
	_ = json.Unmarshal(event.Payload, &final)
	text, err := p.text(ctx, event.Binding, final.Text)
	if err != nil {
		return event, err
	}
	final.Text = text
	event.Payload, _ = json.Marshal(final)
	_, err = Encode(p.validator, "NativeStreamEvent", event)
	return event, err
}

func safeRedactionPath(path string) bool {
	if !strings.HasPrefix(path, "$.") || len(path) > 128 {
		return false
	}
	parts := strings.FieldsFunc(strings.TrimPrefix(path, "$."), func(r rune) bool { return r == '.' || r == '[' || r == ']' })
	for _, part := range parts {
		if !slices.Contains([]string{"omitted", "message", "content", "event", "delta", "content_block", "params", "item", "turn", "id", "session_id", "uuid", "task_id", "parent_tool_use_id", "user_message_uuid", "user_message_uuids", "threadId", "turnId", "itemId", "text", "tool_name", "tokenUsage", "rateLimits"}, part) {
			return false
		}
	}
	return len(parts) > 0
}

func sensitiveText(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{"bearer ", "authorization:", "access_token", "refresh_token", "id_token", "api_key", "\"password\"", "sk-ant-", "sk-proj-", "claude_config_dir", ".claude/", ".codex/"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func Allows(view, kind string) bool {
	return kind != "assistant.final" && (kind != "expert" || view == "expert") && (kind != "terminal" || view == "terminal")
}

func sequence(event nativecontract.StreamEvent) uint64 {
	value, _ := strconv.ParseUint(event.Seq, 10, 64)
	return value
}
