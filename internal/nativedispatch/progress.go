// SPDX-License-Identifier: AGPL-3.0-only

package nativedispatch

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const progressGrace = 10 * time.Minute
const maxRemaining = 524160 // existing harness ETA ceiling, in minutes

// ProgressReport is the only agent-supplied tool input. Session identity and
// credentials cannot be supplied in the tool arguments.
type ProgressReport struct {
	Pct          int     `json:"pct"`
	RemainingMin float64 `json:"remaining_min"`
	Note         string  `json:"note"`
}

// ProgressHeartbeat uses existing harness fields. Source timestamps stay local;
// the server uses its own acceptance clock to evaluate the attention rule.
type ProgressHeartbeat struct {
	ProgressPct  *int       `json:"progress_pct,omitempty"`
	ETAReadyAt   *time.Time `json:"eta_ready_at,omitempty"`
	ActivityNote *string    `json:"activity_note,omitempty"`
}

type progressSample struct {
	pct  int
	eta  *time.Time
	note string
	at   time.Time
}

type planStep struct {
	Text   string
	Status string
}

// Progress belongs to exactly one registered runtime generation. The owning
// supervisor supplies its lifetime and monotonic grant deadline. Closing or
// replacing that runtime invalidates its tool without handing the agent a key.
type Progress struct {
	mu                sync.Mutex
	binding           nativecontract.Binding
	identity          nativecontract.NativeIdentity
	lifetime          context.Context
	deadline          time.Time
	now               func() time.Time
	closed            bool
	sequence          uint64
	plan              []planStep
	planAt            time.Time
	baselineCompleted int
	derived, explicit *progressSample
}

func NewProgress(binding nativecontract.Binding, identity nativecontract.NativeIdentity, lifetime context.Context, deadline time.Time, now func() time.Time) (*Progress, error) {
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	if err = validate(v, "NativeBinding", binding); err != nil {
		return nil, err
	}
	if lifetime == nil || binding.RunID == nil || identity.RuntimeEpoch != binding.RuntimeEpoch || (identity.Provider != "claude" && identity.Provider != "codex") || identity.Provider == "claude" && identity.NativeSessionRef == "" || identity.Provider == "codex" && identity.NativeThreadRef == "" {
		return nil, refusal(nativecontract.ReasonIdentityMismatch)
	}
	if len(identity.ProcessIdentityDigest) != 64 || len(identity.NativeSessionRef) > 256 || len(identity.NativeThreadRef) > 256 {
		return nil, refusal(nativecontract.ReasonPinInvalid)
	}
	if _, err := hex.DecodeString(identity.ProcessIdentityDigest); err != nil {
		return nil, refusal(nativecontract.ReasonPinInvalid)
	}
	if now == nil {
		now = time.Now
	}
	if !now().Before(deadline) {
		return nil, refusal(nativecontract.ReasonGrantExpired)
	}
	// RunID is a pointer in C0; freeze it rather than borrowing caller memory.
	run := *binding.RunID
	binding.RunID = &run
	return &Progress{binding: binding, identity: identity, lifetime: lifetime, deadline: deadline, now: now}, nil
}

func (p *Progress) active(binding nativecontract.Binding) error {
	if !sameBinding(binding, p.binding) {
		return refusal(nativecontract.ReasonWrongGeneration)
	}
	if p.closed || p.lifetime.Err() != nil {
		return refusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if !p.now().Before(p.deadline) {
		return refusal(nativecontract.ReasonGrantExpired)
	}
	return nil
}

func (p *Progress) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.plan, p.derived, p.explicit = nil, nil, nil
}

// boundedJSON rejects duplicate fields/deep nesting before struct decoding.
// This parser sees only a bounded local frame; none of its raw body is exported.
func boundedJSON(raw []byte, limit int, out any, strict bool) error {
	if len(raw) > limit {
		return refusal(nativecontract.ReasonBufferFull)
	}
	if !utf8.Valid(raw) {
		return refusal(nativecontract.ReasonInvalidInput)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var scan func(int) error
	scan = func(depth int) error {
		if depth > 16 {
			return refusal(nativecontract.ReasonBufferFull)
		}
		token, err := d.Token()
		if err != nil {
			return refusal(nativecontract.ReasonInvalidInput)
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				seen := make(map[string]bool)
				for d.More() {
					name, err := d.Token()
					key, ok := name.(string)
					if err != nil || !ok || seen[key] {
						return refusal(nativecontract.ReasonInvalidInput)
					}
					seen[key] = true
					if err = scan(depth + 1); err != nil {
						return err
					}
				}
			case '[':
				for d.More() {
					if err = scan(depth + 1); err != nil {
						return err
					}
				}
			default:
				return refusal(nativecontract.ReasonInvalidInput)
			}
			end, err := d.Token()
			if err != nil || delim == '{' && end != json.Delim('}') || delim == '[' && end != json.Delim(']') {
				return refusal(nativecontract.ReasonInvalidInput)
			}
		}
		return nil
	}
	if err := scan(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return refusal(nativecontract.ReasonInvalidInput)
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	if strict {
		d.DisallowUnknownFields()
	}
	if err := d.Decode(out); err != nil {
		return refusal(nativecontract.ReasonInvalidInput)
	}
	return nil
}

// ObservePlan consumes pinned local Claude SDK assistant TodoWrite snapshots or
// Codex turn/plan/updated notifications. The adapter supplies source correlation
// and its monotonic frame sequence, not a public/client-selected binding.
func (p *Progress) ObservePlan(binding nativecontract.Binding, source nativecontract.NativeIdentity, sequence uint64, raw []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.active(binding); err != nil {
		return err
	}
	if source.Provider != p.identity.Provider || source.RuntimeEpoch != p.identity.RuntimeEpoch || source.ProcessIdentityDigest != p.identity.ProcessIdentityDigest || source.NativeSessionRef != p.identity.NativeSessionRef || source.NativeThreadRef != p.identity.NativeThreadRef {
		return refusal(nativecontract.ReasonIdentityMismatch)
	}
	if sequence == 0 || sequence <= p.sequence {
		return refusal(nativecontract.ReasonStaleRevision)
	}
	var steps []planStep
	switch p.identity.Provider {
	case "claude":
		var event struct {
			Type            string  `json:"type"`
			SessionID       string  `json:"session_id"`
			ParentToolUseID *string `json:"parent_tool_use_id"`
			Message         struct {
				Content []struct {
					Type, Name string
					Input      struct {
						Todos []struct{ Content, Status string } `json:"todos"`
					} `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := boundedJSON(raw, 65536, &event, false); err != nil {
			return err
		}
		if event.Type != "assistant" || event.SessionID != p.identity.NativeSessionRef || event.ParentToolUseID != nil && *event.ParentToolUseID != "" {
			return refusal(nativecontract.ReasonIdentityMismatch)
		}
		found := false
		for _, block := range event.Message.Content {
			if block.Type != "tool_use" || block.Name != "TodoWrite" {
				continue
			}
			if found {
				return refusal(nativecontract.ReasonInvalidInput)
			}
			found = true
			for _, todo := range block.Input.Todos {
				steps = append(steps, planStep{todo.Content, todo.Status})
			}
		}
		if !found {
			return refusal(nativecontract.ReasonUnsupported)
		}
	case "codex":
		var event struct {
			Method string `json:"method"`
			Params struct {
				ThreadID string                          `json:"threadId"`
				TurnID   string                          `json:"turnId"`
				Plan     []struct{ Step, Status string } `json:"plan"`
			} `json:"params"`
		}
		if err := boundedJSON(raw, 65536, &event, false); err != nil {
			return err
		}
		if event.Method != "turn/plan/updated" {
			return refusal(nativecontract.ReasonUnsupported)
		}
		if event.Params.ThreadID != p.identity.NativeThreadRef || event.Params.TurnID != source.NativeTurnRef || event.Params.TurnID == "" {
			return refusal(nativecontract.ReasonIdentityMismatch)
		}
		for _, step := range event.Params.Plan {
			status := step.Status
			if status == "inProgress" {
				status = "in_progress"
			}
			steps = append(steps, planStep{step.Step, status})
		}
	}
	if len(steps) == 0 || len(steps) > 128 {
		return refusal(nativecontract.ReasonBufferFull)
	}
	completed := 0
	for _, s := range steps {
		if s.Text == "" || len(s.Text) > 512 || strings.ContainsRune(s.Text, 0) || (s.Status != "pending" && s.Status != "in_progress" && s.Status != "completed") {
			return refusal(nativecontract.ReasonInvalidInput)
		}
		if s.Status == "completed" {
			completed++
		}
	}
	now := p.now()
	changed := len(p.plan) != len(steps)
	if !changed {
		for i := range steps {
			if steps[i].Text != p.plan[i].Text {
				changed = true
				break
			}
		}
	}
	if changed || completed < p.baselineCompleted {
		p.planAt, p.baselineCompleted = now, completed
	}
	sample := &progressSample{pct: completed * 100 / len(steps), at: now, note: fmt.Sprintf("Native plan: %d of %d steps completed", completed, len(steps))}
	remaining := len(steps) - completed
	if remaining == 0 {
		at := now
		sample.eta = &at
	} else if completed > p.baselineCompleted && now.After(p.planAt) {
		minutes := now.Sub(p.planAt).Minutes() * float64(remaining) / float64(completed-p.baselineCompleted)
		if minutes <= maxRemaining {
			at := now.Add(time.Duration(math.Ceil(minutes)) * time.Minute)
			sample.eta = &at
		}
	}
	p.plan, p.derived, p.sequence = steps, sample, sequence
	return nil
}

func (p *Progress) Report(binding nativecontract.Binding, raw []byte) error {
	var report struct {
		Pct       *int     `json:"pct"`
		Remaining *float64 `json:"remaining_min"`
		Note      *string  `json:"note"`
	}
	if err := boundedJSON(raw, 4096, &report, true); err != nil {
		return err
	}
	if report.Pct == nil || report.Remaining == nil || report.Note == nil || *report.Pct < 0 || *report.Pct > 100 || math.IsNaN(*report.Remaining) || math.IsInf(*report.Remaining, 0) || *report.Remaining < 0 || *report.Remaining > maxRemaining || !utf8.ValidString(*report.Note) || len(*report.Note) > 512 || strings.TrimSpace(*report.Note) == "" || strings.ContainsFunc(*report.Note, unicode.IsControl) {
		return refusal(nativecontract.ReasonInvalidInput)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.active(binding); err != nil {
		return err
	}
	now := p.now()
	eta := now.Add(time.Duration(*report.Remaining * float64(time.Minute)))
	p.explicit = &progressSample{*report.Pct, &eta, strings.TrimSpace(*report.Note), now}
	return nil
}

// AddTool registers only on the session's existing scoped tool server. Agentd
// authenticates that connection; the tool has no URL, credential or target ID.
func (p *Progress) AddTool(server *mcp.Server) {
	server.AddTool(&mcp.Tool{Name: "report_progress", Description: "Report this owned worker's progress, minutes until ready for review, and a short public note.", InputSchema: map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"pct", "remaining_min", "note"},
		"properties": map[string]any{
			"pct":           map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
			"remaining_min": map[string]any{"type": "number", "minimum": 0, "maximum": maxRemaining},
			"note":          map[string]any{"type": "string", "minLength": 1, "maxLength": 512},
		},
	}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := p.Report(p.binding, request.Params.Arguments); err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Progress accepted locally; server persistence is confirmed by the session heartbeat."}}}, nil
	})
}

func (p *Progress) Heartbeat() ProgressHeartbeat {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active(p.binding) != nil {
		return ProgressHeartbeat{}
	}
	now := p.now()
	sample := p.explicit
	if sample == nil || now.Sub(sample.at) >= progressGrace {
		sample = p.derived
	}
	if sample == nil || now.Sub(sample.at) >= progressGrace {
		return ProgressHeartbeat{}
	}
	pct, note := sample.pct, sample.note
	out := ProgressHeartbeat{ProgressPct: &pct, ActivityNote: &note}
	if sample.eta != nil {
		at := *sample.eta
		out.ETAReadyAt = &at
	}
	return out
}

// AttentionState is assembled from the server's current authorized generation
// and accepted heartbeat, under its existing tenant/tree/row locks. Its clock
// and timestamps are server-owned, not payload timestamps supplied by an agent.
type AttentionState struct {
	Binding                                      nativecontract.Binding
	ParentHarnessSessionID, Role, WorkShape      string
	Owned, Stopped                               bool
	StartedAt, ProgressReportedAt, ETAReportedAt time.Time
	ProgressPct                                  *int
	ETAReadyAt                                   *time.Time
}

type ProgressAttention struct {
	ParentHarnessSessionID, ChildHarnessSessionID, Code, Note string
	MissingProgress, MissingETA                               bool
}

func MissingProgressAttention(state AttentionState, now time.Time) *ProgressAttention {
	if !state.Owned || state.Stopped || state.Role != "worker" || (state.WorkShape != "build" && state.WorkShape != "ship") || state.ParentHarnessSessionID == "" || state.ParentHarnessSessionID == state.Binding.HarnessSessionID || state.Binding.RunID == nil || state.StartedAt.IsZero() || now.Sub(state.StartedAt) < progressGrace {
		return nil
	}
	missingProgress := state.ProgressPct == nil || state.ProgressReportedAt.IsZero() || now.Sub(state.ProgressReportedAt) >= progressGrace
	missingETA := state.ETAReadyAt == nil || state.ETAReportedAt.IsZero() || now.Sub(state.ETAReportedAt) >= progressGrace
	if !missingProgress && !missingETA {
		return nil
	}
	return &ProgressAttention{state.ParentHarnessSessionID, state.Binding.HarnessSessionID, "native_missing_progress_eta", "No progress/ETA", missingProgress, missingETA}
}
