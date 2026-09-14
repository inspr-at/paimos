// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package lifecycleclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
	"github.com/inspr-at/paimos/backend/localjournal"
)

const conversationRetention = 10 * time.Minute

type ConversationExecutionResult struct {
	Outcome  string
	Failure  string
	ThreadID string
	TurnID   string
	Text     string
}

type ConversationProcess interface {
	Identity() (string, string, error)
	Wait(context.Context) (ConversationExecutionResult, error)
	// Stop returns only after the owned process has stopped and been reaped.
	Stop(context.Context) error
}

// ConversationLauncher must return only after any child it started on an
// error path has been stopped and reaped. The runner journals "launching"
// before invoking it, so recovery never repeats an uncertain spawn.
type ConversationLauncher interface {
	LaunchConversation(context.Context, ConversationClaim) (ConversationProcess, error)
}

type conversationRecord struct {
	CallID      string              `json:"call_id"`
	Digest      string              `json:"digest"`
	Phase       string              `json:"phase"`
	Claim       ConversationClaim   `json:"claim"`
	Sequence    int                 `json:"sequence"`
	ThreadID    string              `json:"thread_id,omitempty"`
	TurnID      string              `json:"turn_id,omitempty"`
	Pending     []ConversationEvent `json:"pending,omitempty"`
	RetainUntil time.Time           `json:"retain_until"`
}

type ConversationRunner struct {
	mu        sync.Mutex
	authority ConversationAuthority
	launcher  ConversationLauncher
	journal   *localjournal.Journal[conversationRecord]
	now       func() time.Time
}

func NewConversationRunner(directory, namespace string, authority ConversationAuthority, launcher ConversationLauncher) (*ConversationRunner, error) {
	if authority == nil || launcher == nil || uuid.Validate(namespace) != nil {
		return nil, ErrOwnership
	}
	j, err := localjournal.Open(localjournal.Config[conversationRecord]{
		Directory: directory, Prefix: "conversation-" + namespace, Version: 1, MaxBytes: 4 << 20, MaxRecords: 16,
		Key: func(record conversationRecord) (string, error) { return record.CallID, nil },
		Validate: func(record conversationRecord) error {
			if uuid.Validate(record.CallID) != nil || record.Claim.Call.CallID != record.CallID || record.Digest != conversationClaimDigest(record.Claim) ||
				record.Sequence < 0 || record.Sequence > conversationMaxEvents || record.RetainUntil.IsZero() || len(record.Pending) > conversationMaxEvents {
				return ErrUnknown
			}
			switch record.Phase {
			case "reserved", "launching", "running", "terminal":
			default:
				return ErrUnknown
			}
			for index, event := range record.Pending {
				if validateConversationEvent(event) != nil || event.Sequence != record.Sequence-len(record.Pending)+index+1 {
					return ErrUnknown
				}
			}
			return nil
		},
	})
	if err != nil {
		return nil, ErrUnknown
	}
	return &ConversationRunner{authority: authority, launcher: launcher, journal: j, now: time.Now}, nil
}

func conversationClaimDigest(claim ConversationClaim) string {
	raw, _ := json.Marshal(claim)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// Step owns at most one local process. It durably reserves the exact execution
// generation before launch and never adopts a PID or respawns a generation
// whose launch may already have happened.
func (r *ConversationRunner) Step(ctx context.Context, runtime lifecycleintents.Runtime) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	runtimeExpiry, err := time.Parse(time.RFC3339Nano, runtime.ExpiresAt)
	if err != nil || !now.Before(runtimeExpiry) || uuid.Validate(runtime.ID) != nil || uuid.Validate(runtime.Generation) != nil {
		return ErrOwnership
	}
	var reserved *conversationRecord
	known := make(map[string]conversationRecord)
	for _, saved := range r.journal.Snapshot() {
		if !now.Before(saved.RetainUntil) {
			if err := r.journal.Delete(saved.CallID); err != nil {
				return ErrUnknown
			}
			continue
		}
		known[saved.CallID] = saved
		if len(saved.Pending) > 0 {
			if err := r.flush(ctx, runtime, &saved); err != nil {
				return err
			}
		}
		if saved.Phase == "launching" || saved.Phase == "running" {
			// A recovered journal has no process handle. It may poll its known
			// generation, but can neither adopt nor signal a saved PID.
			_, _ = r.authority.ConversationControl(ctx, runtime.ID, saved.CallID, saved.Claim.ExecutionGeneration)
			return ErrUnknown
		}
		if saved.Phase == "reserved" {
			copy := saved
			reserved = &copy
		}
	}
	if reserved == nil {
		claim, err := r.authority.ClaimConversation(ctx, runtime.ID, runtime.Generation)
		if err != nil || claim == nil {
			return err
		}
		if validateConversationClaim(*claim, runtime.ID, runtime.Generation, now) != nil {
			return ErrOwnership
		}
		if saved, exists := known[claim.Call.CallID]; exists {
			if saved.Digest != conversationClaimDigest(*claim) {
				return ErrOwnership
			}
			switch saved.Phase {
			case "terminal":
				return nil
			case "launching", "running":
				return ErrUnknown
			case "reserved":
				reserved = &saved
			}
		}
		if reserved != nil {
			return r.execute(ctx, runtime, *reserved)
		}
		record := conversationRecord{
			CallID: claim.Call.CallID, Digest: conversationClaimDigest(*claim), Phase: "reserved", Claim: *claim,
			RetainUntil: mustConversationDeadline(claim.Call.DeadlineAt).Add(conversationRetention),
		}
		if err := r.journal.Put(record); err != nil {
			return ErrUnknown
		}
		reserved = &record
	}
	return r.execute(ctx, runtime, *reserved)
}

func (r *ConversationRunner) execute(ctx context.Context, runtime lifecycleintents.Runtime, record conversationRecord) error {
	deadline := mustConversationDeadline(record.Claim.Call.DeadlineAt)
	if !r.now().Before(deadline) {
		return r.finishWithoutProcess(ctx, runtime, &record, "failed", "deadline_exceeded")
	}
	control, err := r.authority.ConversationControl(ctx, runtime.ID, record.CallID, record.Claim.ExecutionGeneration)
	if err != nil || control.DeadlineAt != record.Claim.Call.DeadlineAt {
		return errors.Join(ErrOwnership, err)
	}
	if !control.Continue {
		kind, code := "failed", "ownership_lost"
		if control.CancelRequested {
			kind, code = "cancelled", "cancelled"
		}
		return r.finishWithoutProcess(ctx, runtime, &record, kind, code)
	}
	record.Phase = "launching"
	if err := r.journal.Put(record); err != nil {
		return ErrUnknown
	}
	process, err := r.launcher.LaunchConversation(ctx, record.Claim)
	if err != nil || process == nil {
		return r.finishWithoutProcess(ctx, runtime, &record, "failed", "preflight_failed")
	}
	threadID, turnID, err := process.Identity()
	if err != nil || !conversationStableValue.MatchString(threadID) || !conversationStableValue.MatchString(turnID) {
		_ = process.Stop(context.Background())
		return r.finishWithoutProcess(ctx, runtime, &record, "failed", "protocol_error")
	}
	record.Phase, record.ThreadID, record.TurnID = "running", threadID, turnID
	if err := r.appendEvents(&record, ConversationEvent{Kind: "started", ThreadID: threadID, TurnID: turnID}); err != nil {
		_ = process.Stop(context.Background())
		return err
	}
	if err := r.journal.Put(record); err != nil {
		_ = process.Stop(context.Background())
		return ErrUnknown
	}
	// Publish the native identity before waiting for output. Cancellation and
	// deadline transitions may race this report, but the service keeps those
	// states cancel-only while retaining the exact execution sequence.
	if reportErr := r.flush(ctx, runtime, &record); reportErr != nil {
		if stopErr := process.Stop(context.Background()); stopErr != nil {
			return errors.Join(reportErr, ErrUnknown)
		}
		return r.finishProcess(ctx, runtime, &record, ConversationExecutionResult{
			Outcome: "failed", Failure: "ownership_lost", ThreadID: threadID, TurnID: turnID,
		}, reportErr)
	}
	control, err = r.authority.ConversationControl(ctx, runtime.ID, record.CallID, record.Claim.ExecutionGeneration)
	if err != nil || control.DeadlineAt != record.Claim.Call.DeadlineAt || !control.Continue || control.CancelRequested {
		if stopErr := process.Stop(context.Background()); stopErr != nil {
			return errors.Join(ErrUnknown, err, stopErr)
		}
		result := ConversationExecutionResult{Outcome: "failed", Failure: "ownership_lost", ThreadID: threadID, TurnID: turnID}
		if err == nil && control.DeadlineAt == record.Claim.Call.DeadlineAt && control.CancelRequested {
			result.Outcome, result.Failure = "cancelled", "cancelled"
		}
		return r.finishProcess(ctx, runtime, &record, result, err)
	}

	waitCtx, cancelWait := context.WithDeadline(ctx, deadline)
	defer cancelWait()
	resultCh := make(chan struct {
		result ConversationExecutionResult
		err    error
	}, 1)
	go func() {
		result, waitErr := process.Wait(waitCtx)
		resultCh <- struct {
			result ConversationExecutionResult
			err    error
		}{result, waitErr}
	}()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case completed := <-resultCh:
			return r.finishProcess(ctx, runtime, &record, completed.result, completed.err)
		case <-ticker.C:
			_ = r.flush(ctx, runtime, &record)
			control, controlErr := r.authority.ConversationControl(ctx, runtime.ID, record.CallID, record.Claim.ExecutionGeneration)
			if controlErr != nil || control.DeadlineAt != record.Claim.Call.DeadlineAt || !control.Continue || control.CancelRequested {
				_ = process.Stop(context.Background())
				completed := <-resultCh
				if controlErr != nil || control.DeadlineAt != record.Claim.Call.DeadlineAt || !control.Continue && !control.CancelRequested {
					completed.result.Outcome, completed.result.Failure = "failed", "ownership_lost"
				}
				return r.finishProcess(ctx, runtime, &record, completed.result, completed.err)
			}
		case <-ctx.Done():
			_ = process.Stop(context.Background())
			completed := <-resultCh
			return r.finishProcess(context.Background(), runtime, &record, completed.result, completed.err)
		}
	}
}

func (r *ConversationRunner) finishWithoutProcess(ctx context.Context, runtime lifecycleintents.Runtime, record *conversationRecord, kind, code string) error {
	record.Phase = "terminal"
	if err := r.appendEvents(record, ConversationEvent{Kind: kind, ErrorCode: code}); err != nil {
		return err
	}
	if err := r.journal.Put(*record); err != nil {
		return ErrUnknown
	}
	return r.flush(ctx, runtime, record)
}

func (r *ConversationRunner) finishProcess(ctx context.Context, runtime lifecycleintents.Runtime, record *conversationRecord, result ConversationExecutionResult, waitErr error) error {
	record.Phase = "terminal"
	terminal := ConversationEvent{ThreadID: record.ThreadID, TurnID: record.TurnID}
	if waitErr == nil && result.Outcome == "completed" && result.ThreadID == record.ThreadID && result.TurnID == record.TurnID && utf8.ValidString(result.Text) && len(result.Text) <= record.Claim.Limits.MaxOutputBytes {
		chunks, ok := conversationChunks(result.Text, record.Claim.Limits.MaxEvents-2)
		if !ok {
			terminal.Kind, terminal.ErrorCode = "failed", "output_bound"
		} else {
			for _, chunk := range chunks {
				if err := r.appendEvents(record, ConversationEvent{Kind: "assistant_delta", ThreadID: record.ThreadID, TurnID: record.TurnID, Text: chunk}); err != nil {
					return err
				}
			}
			digest := sha256.Sum256([]byte(result.Text))
			terminal.Kind, terminal.OutputSHA256 = "completed", hex.EncodeToString(digest[:])
		}
	} else if result.Outcome == "cancelled" || result.Failure == "cancelled" || result.Failure == "deadline_exceeded" {
		terminal.Kind, terminal.ErrorCode = "cancelled", result.Failure
		if terminal.ErrorCode == "" {
			terminal.ErrorCode = "cancelled"
		}
	} else {
		terminal.Kind, terminal.ErrorCode = "failed", closedConversationFailure(result.Failure)
	}
	if err := r.appendEvents(record, terminal); err != nil {
		return err
	}
	if err := r.journal.Put(*record); err != nil {
		return ErrUnknown
	}
	return r.flush(ctx, runtime, record)
}

func (r *ConversationRunner) appendEvents(record *conversationRecord, events ...ConversationEvent) error {
	if record.Sequence+len(events) > record.Claim.Limits.MaxEvents {
		return ErrUnknown
	}
	for _, event := range events {
		record.Sequence++
		event.Sequence = record.Sequence
		if validateConversationEvent(event) != nil {
			return ErrUnknown
		}
		record.Pending = append(record.Pending, event)
	}
	return nil
}

func (r *ConversationRunner) flush(ctx context.Context, runtime lifecycleintents.Runtime, record *conversationRecord) error {
	for len(record.Pending) > 0 {
		event := record.Pending[0]
		call, err := r.authority.ReportConversationEvent(ctx, runtime.ID, runtime.Generation, record.CallID, record.Claim.ExecutionGeneration, event)
		if err != nil {
			return err
		}
		if call.DeadlineAt != record.Claim.Call.DeadlineAt {
			return ErrOwnership
		}
		record.Pending = record.Pending[1:]
		if err := r.journal.Put(*record); err != nil {
			return ErrUnknown
		}
	}
	return nil
}

func conversationChunks(text string, maximum int) ([]string, bool) {
	if text == "" {
		return nil, true
	}
	var chunks []string
	for len(text) > 0 {
		end := min(len(text), conversationMaxDelta)
		for end > 0 && !utf8.ValidString(text[:end]) {
			end--
		}
		if end == 0 || len(chunks) >= maximum {
			return nil, false
		}
		chunks = append(chunks, text[:end])
		text = text[end:]
	}
	return chunks, true
}

func closedConversationFailure(value string) string {
	switch value {
	case "protocol_error", "output_bound", "event_bound", "transport_ended", "turn_failed", "deadline_exceeded", "ownership_lost", "preflight_failed":
		return value
	default:
		return "execution_failed"
	}
}

func mustConversationDeadline(value string) time.Time {
	deadline, _ := time.Parse(time.RFC3339, value)
	return deadline
}
