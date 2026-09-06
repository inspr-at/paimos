// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
)

// This is an opt-in adapter primitive proof, not server/reporter or clean-host
// acceptance. Normal CLI auth inheritance is intentional: HOME and CODEX_HOME
// are neither copied nor overridden; Codex may write normal per-session state.
func codexProof(r *receipt, root, binary string) (ret error) {
	r.Stage = "codex_preflight"
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		return errors.New("workspace root must be new")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		return err
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(signalCtx, 100*time.Second)
	defer cancel()
	adapter := agentd.NewCodexAdapter(binary, "pai928-platform-proof")
	account := adapter.AccountLabel(ctx)
	r.Evidence["account_label"] = account
	if account != "chatgpt" {
		return errors.New("reviewed account label mismatch")
	}
	profile, err := dispatchprofile.Resolve("codex-sol-high", "1", agentd.AdapterCodex)
	if err != nil {
		return err
	}
	r.Evidence["dispatch_profile"] = "codex-sol-high@1"
	r.Evidence["scope"] = "one owned app-server, three input submissions; no Paimos server, reporter, shared config mutation or credential copy"
	r.Evidence["completion_evidence_boundary"] = "owned completed/interrupted event; failed or unknown terminal status fails closed; no model answer quality claim"
	r.Evidence["tool_evidence_boundary"] = "owned documented tool item started; no tool exit/output claim"
	r.Evidence["query_budget"] = 1
	r.Evidence["input_budget"] = 3
	var mu sync.Mutex
	var observed []agentd.AdapterEvent
	events := make(chan agentd.AdapterEvent, 256)
	observe := func(e agentd.AdapterEvent) {
		mu.Lock()
		if len(observed) < 256 {
			observed = append(observed, e)
		}
		mu.Unlock()
		select {
		case events <- e:
		default:
		}
	}
	defer func() { mu.Lock(); r.Evidence["events"] = append([]agentd.AdapterEvent(nil), observed...); mu.Unlock() }()
	r.Stage = "codex_start"
	r.Evidence["queries_attempted"] = 1
	r.Evidence["inputs_attempted"] = 1
	p, err := adapter.Start(ctx, agentd.StartRequest{KeepAlive: true, Adapter: agentd.AdapterCodex, Identity: "codex:platform-proof", Workspace: workspace, WorkspaceMode: agentd.WorkspaceExclusive, Prompt: "Run the command sleep 20 in this empty workspace, then say done. This bounded delay verifies live owned control.", ResolvedProfile: &profile}, observe)
	if err != nil {
		return err
	}
	pid := p.PID()
	r.Evidence["owned_pgid"] = pid
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		effect, stopErr := p.Stop(cleanupCtx, agentd.ControlRequest{CorrelationID: "pai928-stop"})
		waitDone := make(chan error, 1)
		go func() { waitDone <- p.Wait() }()
		reaped := false
		select {
		case <-waitDone:
			reaped = true
		case <-cleanupCtx.Done():
		}
		start := time.Now()
		absent := false
		for time.Since(start) < 2*time.Second {
			if errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
				absent = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		r.Evidence["stop_effect"] = effect
		r.Evidence["reaped"] = reaped
		r.Evidence["exact_group_absent"] = absent
		r.Evidence["group_observation_ms"] = time.Since(start).Milliseconds()
		r.Cleanup = stopErr == nil && reaped && absent
		if !r.Cleanup && ret == nil {
			ret = errors.New("owned group cleanup incomplete")
		}
	}()
	// Record exact owned identity before control calls, so a harness interruption
	// cannot erase the cleanup target. Never reconstruct authority from this file.
	ownership, err := os.OpenFile(filepath.Join(root, "ownership.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(ownership).Encode(map[string]any{"owned_pgid": pid, "observed_at": time.Now().UTC(), "authority": "live production adapter handle only; receipt is not signal authority"})
	closeErr := ownership.Close()
	if err != nil || closeErr != nil {
		return errors.New("ownership receipt failed")
	}
	wait := func(kind agentd.EventKind) error {
		for {
			select {
			case e := <-events:
				if e.ErrorCode != "" {
					r.Evidence["adapter_error_code"] = e.ErrorCode
					return errors.New("adapter event failed")
				}
				if e.Kind == kind {
					return nil
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	r.Stage = "live_tool"
	if err = wait(agentd.EventToolStarted); err != nil {
		return err
	}
	r.Stage = "midturn_steer"
	r.Evidence["inputs_attempted"] = 2
	effect, err := p.Steer(ctx, agentd.ControlRequest{CorrelationID: "pai928-steer", Text: "After the current command, reply with only done."})
	if err != nil {
		return err
	}
	r.Evidence["steer_effect"] = effect
	r.Stage = "interrupt"
	effect, err = p.Interrupt(ctx, agentd.ControlRequest{CorrelationID: "pai928-interrupt"})
	if err != nil {
		return err
	}
	r.Evidence["interrupt_effect"] = effect
	if err = wait(agentd.EventTurnCompleted); err != nil {
		return err
	}
	// Notification callbacks run before the adapter clears its active turn.
	// Wait for its actual idle predicate instead of racing the completion event.
	ready, ok := p.(interface{ InboxReady() bool })
	if !ok {
		return errors.New("idle readiness unavailable")
	}
	for !ready.InboxReady() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	inbox, ok := p.(agentd.InboxProcess)
	if !ok {
		return errors.New("inbox unsupported")
	}
	r.Stage = "simple_inbox"
	r.Evidence["inputs_attempted"] = 3
	effect, err = inbox.Inbox(ctx, agentd.ControlRequest{CorrelationID: "pai928-simple", Text: "Reply with only done. Do not use tools."})
	if err != nil {
		return err
	}
	r.Evidence["inbox_effect"] = effect
	if err = wait(agentd.EventTurnCompleted); err != nil {
		return err
	}
	r.Stage = "complete"
	return nil
}
