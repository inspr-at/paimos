// SPDX-License-Identifier: AGPL-3.0-only

package nativequalification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

// Recorder is a conformance observer, not a launcher, broker or final store.
// It deliberately has no authority to release occupancy or signal a process.
type Recorder struct {
	mu        sync.Mutex
	candidate Candidate
	identity  nativecontract.NativeIdentity
	sequence  uint64
	inputs    map[string]inputEvidence
	tasks     map[string]bool
	deadline  time.Time
}

type inputEvidence struct {
	digest, request, turn string
	completed             bool
}

func New(ctx context.Context, c Candidate, identity nativecontract.NativeIdentity) (*Recorder, error) {
	if err := validateCandidate(ctx, c); err != nil {
		return nil, err
	}
	if !validIdentity(identity) || identity.RuntimeEpoch != c.Spec.Binding.RuntimeEpoch || identity.NativeSessionRef != c.Spec.ResumeRef {
		return nil, ErrEvidence
	}
	c.AllowedTools = append([]string{}, c.AllowedTools...)
	// Never retain child capability bytes in qualification evidence.
	c.Spec.Tools.Token = nil
	c.Actor.KeyCeiling = append([]string{}, c.Actor.KeyCeiling...)
	deadline := c.Now().Add(time.Duration(c.Spec.Policy.MaximumRuntimeSeconds) * time.Second)
	if c.Spec.Policy.ExpiresAt.Before(deadline) {
		deadline = c.Spec.Policy.ExpiresAt
	}
	return &Recorder{candidate: c, identity: identity, inputs: map[string]inputEvidence{}, tasks: map[string]bool{}, deadline: deadline}, nil
}

func (r *Recorder) current(ctx context.Context) error {
	if !r.candidate.Now().Before(r.deadline) {
		return ErrDeadline
	}
	if err := r.candidate.current(ctx); err != nil {
		return err
	}
	if !r.candidate.Now().Before(r.deadline) {
		return ErrDeadline
	}
	return nil
}

// CheckTool observes the same name, physical path, resource lease and deadline
// fence a broker must enforce in its final write. It never performs the effect.
func (r *Recorder) CheckTool(ctx context.Context, name, path, resourceLease string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := r.current(ctx); err != nil {
		return err
	}
	allowed := false
	for _, tool := range r.candidate.AllowedTools {
		if name == "mcp__aeon__"+tool {
			allowed = true
		}
	}
	if !allowed {
		return ErrPolicy
	}
	if len(path) == 0 || len(path) > 4096 || !filepath.IsAbs(path) {
		return ErrFence
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil || physical != path {
		return ErrFence
	}
	root := r.candidate.Spec.Profile.WorkspaceRoot
	if resourceLease != "" {
		if resourceLease != r.candidate.ResourceLeaseID || r.candidate.ResourceRoot == "" {
			return ErrFence
		}
		root = r.candidate.ResourceRoot
	}
	if !within(root, path) {
		return ErrFence
	}
	if !r.candidate.Now().Before(r.candidate.Spec.Tools.ExpiresAt) {
		return ErrDeadline
	}
	return r.current(ctx)
}

// Queue retains only a digest and correlation metadata, never prompt content.
// Queuing does not prove native consumption or give permission to write.
func (r *Recorder) Queue(ctx context.Context, input nativecontract.Input) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.current(ctx); err != nil {
		return err
	}
	if !sameBinding(input.ExpectedBinding, r.candidate.Spec.Binding) || !reference(input.InputID) || !reference(input.RequestID) ||
		!r.candidate.Now().Before(input.ExpiresAt) || (input.Mode != "now" && input.Mode != "after_turn") {
		return ErrEvidence
	}
	if len(input.Text) == 0 || len(input.Text) > 65536 || !utf8.ValidString(input.Text) || strings.ContainsRune(input.Text, 0) {
		return ErrBound
	}
	h := sha256.Sum256([]byte(input.Text))
	digest := hex.EncodeToString(h[:])
	if previous, found := r.inputs[input.InputID]; found {
		if previous.digest == digest && previous.request == input.RequestID {
			return nil
		}
		return ErrEvidence
	}
	if len(r.inputs) >= 128 {
		return ErrBound
	}
	r.inputs[input.InputID] = inputEvidence{digest: digest, request: input.RequestID}
	return nil
}

// Observe accepts already decoded C0 events from one pinned reader. Invalid
// sequence/identity/correlation never advances evidence. No inferred final is
// accepted; finals must name an input consumed by this exact native turn.
func (r *Recorder) Observe(ctx context.Context, event nativecontract.ProviderEvent) (*nativecontract.FinalPublish, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.current(ctx); err != nil {
		return nil, err
	}
	i := event.Identity
	if !validIdentity(i) || i.RuntimeEpoch != r.identity.RuntimeEpoch || i.NativeSessionRef != r.identity.NativeSessionRef ||
		i.ProcessIdentityDigest != r.identity.ProcessIdentityDigest || i.PID != r.identity.PID || !i.StartedAt.Equal(r.identity.StartedAt) ||
		event.SourceSequence != r.sequence+1 {
		return nil, ErrEvidence
	}
	arms := 0
	if event.Structured != nil {
		arms++
	}
	if event.Receipt != nil {
		arms++
	}
	if event.Final != nil {
		arms++
	}
	if arms != 1 || event.Expert != nil || event.Approval != nil || event.Quota != nil {
		return nil, ErrEvidence
	}
	compacted := event.Structured != nil && event.Structured.Kind == "turn_state" && event.Structured.State != nil && *event.Structured.State == "compacted"
	if !compacted && (i.NativeThreadRef != r.identity.NativeThreadRef || i.ResumeCursor != r.identity.ResumeCursor) {
		return nil, ErrEvidence
	}
	var final *nativecontract.FinalPublish
	switch {
	case event.Structured != nil:
		frame := event.Structured
		if frame.Text != nil || frame.State == nil || frame.Reference == nil || !reference(*frame.Reference) {
			return nil, ErrEvidence
		}
		switch frame.Kind {
		case "background_task":
			active, found := r.tasks[*frame.Reference]
			switch *frame.State {
			case "started":
				if found || !event.BackgroundWork {
					return nil, ErrEvidence
				}
				if len(r.tasks) >= 32 {
					return nil, ErrBound
				}
				r.tasks[*frame.Reference] = true
			case "progress":
				if !found || !active || !event.BackgroundWork {
					return nil, ErrEvidence
				}
			case "completed":
				if !found || !active || event.BackgroundWork {
					return nil, ErrEvidence
				}
				r.tasks[*frame.Reference] = false
			default:
				return nil, ErrEvidence
			}
		case "turn_state":
			if *frame.State != "compacted" || *frame.Reference != i.ResumeCursor {
				return nil, ErrEvidence
			}
		default:
			return nil, ErrEvidence
		}
	case event.Receipt != nil:
		p := event.Receipt
		if p.Binding == nil || !sameBinding(*p.Binding, r.candidate.Spec.Binding) || p.InputID == nil || p.TurnRef == nil ||
			p.State != "turn_started" || p.Operation != "input" || !reference(*p.TurnRef) || *p.TurnRef != i.NativeTurnRef {
			return nil, ErrEvidence
		}
		input, found := r.inputs[*p.InputID]
		if !found || input.completed || input.turn != "" || p.RequestID != input.request {
			return nil, ErrEvidence
		}
		input.turn = *p.TurnRef
		r.inputs[*p.InputID] = input
	case event.Final != nil:
		f := event.Final
		input, found := r.inputs[f.InputID]
		if !found || input.completed || input.turn == "" || input.turn != f.TurnRef || i.NativeTurnRef != f.TurnRef ||
			!sameBinding(f.ExpectedBinding, r.candidate.Spec.Binding) || !reference(f.FinalID) || !reference(f.DrainGrantID) || event.BackgroundWork {
			return nil, ErrEvidence
		}
		if len(f.Text) == 0 || len(f.Text) > 65536 || !utf8.ValidString(f.Text) || strings.ContainsRune(f.Text, 0) {
			return nil, ErrBound
		}
		input.completed = true
		r.inputs[f.InputID] = input
		copy := *f
		final = &copy
	}
	r.identity, r.sequence = i, event.SourceSequence
	return final, nil
}

// Snapshot is content-free local evidence. Quiescent permits an idle transition
// only; turn completion, detach and task completion never prove process exit.
type Snapshot struct {
	Identity        nativecontract.NativeIdentity
	Sequence        uint64
	BackgroundTasks int
	PendingInputs   int
	Quiescent       bool
}

func (r *Recorder) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Snapshot{Identity: r.identity, Sequence: r.sequence}
	for _, active := range r.tasks {
		if active {
			s.BackgroundTasks++
		}
	}
	for _, input := range r.inputs {
		if !input.completed {
			s.PendingInputs++
		}
	}
	s.Quiescent = s.BackgroundTasks == 0 && s.PendingInputs == 0
	return s
}

func reference(value string) bool {
	return len(value) > 0 && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n")
}
func validIdentity(i nativecontract.NativeIdentity) bool {
	return i.Provider == "claude" && reference(i.NativeSessionRef) && reference(i.NativeThreadRef) && reference(i.ResumeCursor) &&
		reference(i.RuntimeEpoch) && len(i.ProcessIdentityDigest) == 64 && i.PID > 1 && !i.StartedAt.IsZero()
}

// CheckSuccessor is an after-exit conformance check, never PID signal authority.
// The caller must still hold the owning writer lock and obtain fresh admission
// immediately before launch. A fixture pass is not G_LEAD_GRANT/G_P1_LIVE.
func CheckSuccessor(ctx context.Context, c Candidate, previous nativecontract.WriterRecord, exit nativecontract.ExitEvidence) error {
	if err := validateCandidate(ctx, c); err != nil {
		return err
	}
	i := previous.Identity
	if !previous.PossibleFork || !validIdentity(i) || !exit.RootExited || !exit.GroupAbsent ||
		exit.ProcessIdentityDigest != i.ProcessIdentityDigest || exit.ObservedAt.Before(i.StartedAt) || exit.ObservedAt.After(c.Now()) {
		return ErrExit
	}
	before, after := previous.Binding, c.Spec.Binding
	if before.TenantID != after.TenantID || before.ProjectID != after.ProjectID || before.OwnedSessionID != after.OwnedSessionID ||
		before.RunID != nil || after.RunID != nil || before.RuntimeEpoch == after.RuntimeEpoch ||
		before.HarnessSessionID == after.HarnessSessionID || c.Spec.ResumeRef != i.NativeSessionRef {
		return ErrEvidence
	}
	return c.current(ctx)
}
