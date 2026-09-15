// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/lifecycleclient"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

type configuredConversation struct {
	AccountKey             string `json:"account_key"`
	AttachmentRevision     int64  `json:"attachment_revision"`
	DispatchProfileID      string `json:"dispatch_profile_id"`
	DispatchProfileVersion string `json:"dispatch_profile_version"`
	ExecutionPolicyID      string `json:"execution_policy_id"`
	MaxOutputBytes         int    `json:"max_output_bytes"`
	MaxEvents              int    `json:"max_events"`
}

type projectConversationLauncher struct {
	project     *projectLifecycle
	adapter     *agentd.CodexAdapter
	scratchRoot string
}

type projectConversationProcess struct {
	execution agentd.CodexConversationExecution
}

func (p *projectConversationProcess) Identity() (string, string, error) {
	return p.execution.ConversationIdentity()
}

func (p *projectConversationProcess) Wait(ctx context.Context) (lifecycleclient.ConversationExecutionResult, error) {
	result, err := p.execution.WaitConversation(ctx)
	return lifecycleclient.ConversationExecutionResult{
		Outcome: string(result.Outcome), Failure: string(result.Failure), ThreadID: result.ThreadID, TurnID: result.TurnID, Text: result.Text,
	}, err
}

func (p *projectConversationProcess) Stop(ctx context.Context) error {
	_, err := p.execution.Stop(ctx, agentd.ControlRequest{CorrelationID: "conversation-authority-stop"})
	return err
}

func (l *projectConversationLauncher) LaunchConversation(ctx context.Context, claim lifecycleclient.ConversationClaim) (lifecycleclient.ConversationProcess, error) {
	c := l.project.config.Conversation
	if c == nil || claim.ExecutionPolicyID != lifecycleclient.ConversationPolicyV1 || claim.AccountKey != c.AccountKey ||
		claim.AttachmentRevision != c.AttachmentRevision || claim.DispatchProfileID != c.DispatchProfileID ||
		claim.DispatchProfileVersion != c.DispatchProfileVersion || claim.Limits.MaxOutputBytes > c.MaxOutputBytes || claim.Limits.MaxEvents > c.MaxEvents {
		return nil, lifecycleclient.ErrOwnership
	}
	if l.project.owner.supervisor.Status().DaemonID != claim.RuntimeGeneration || !l.adapter.HasAccount(claim.AccountKey) {
		return nil, lifecycleclient.ErrOwnership
	}
	profile, err := configuredProfile(claim.DispatchProfileID, claim.DispatchProfileVersion)
	if err != nil || profile.Harness != agentd.AdapterCodex {
		return nil, lifecycleclient.ErrOwnership
	}
	resolved, err := l.project.owner.reporter.ResolveDispatchProfile(ctx, profile.ID, profile.Version, profile.Harness)
	if err != nil || resolved != profile {
		return nil, lifecycleclient.ErrOwnership
	}
	_, mode, revision := l.project.owner.supervisor.AttachedAccountKeys(l.project.config.ProjectID, agentd.AdapterCodex)
	if mode != agentd.AccountLifecycleExplicit || revision != claim.AttachmentRevision ||
		!l.project.owner.supervisor.AccountAttached(l.project.config.ProjectID, agentd.AdapterCodex, claim.AccountKey) ||
		!l.project.registration.MatchScopeAtRevision("chatgpt", claim.AccountKey, profile.ID, profile.Version, true, claim.AttachmentRevision) {
		return nil, lifecycleclient.ErrOwnership
	}
	prompt, err := json.Marshal(struct {
		Purpose  string                                `json:"purpose"`
		System   string                                `json:"system"`
		Messages []lifecycleclient.ConversationMessage `json:"messages"`
	}{claim.Purpose, claim.System, claim.Messages})
	if err != nil {
		return nil, lifecycleclient.ErrOwnership
	}
	execution, err := l.adapter.StartConversation(ctx, agentd.StartRequest{
		Adapter: agentd.AdapterCodex, Identity: "codex:conversation", ProjectID: l.project.config.ProjectID,
		Prompt: string(prompt), AccountKey: claim.AccountKey, AttachmentRevision: claim.AttachmentRevision,
		DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version, ResolvedProfile: &profile,
	}, agentd.CodexConversationOptions{
		MaxOutputBytes: claim.Limits.MaxOutputBytes, MaxEvents: claim.Limits.MaxEvents,
		ScratchRoot: l.scratchRoot, OutputSchema: claim.OutputSchema,
	}, nil)
	if err != nil {
		return nil, err
	}
	return &projectConversationProcess{execution: execution}, nil
}

func configureProjectConversation(p *projectLifecycle, projectDir string) error {
	p.conversation = nil
	p.registration.Conversation = nil
	c := p.config.Conversation
	if c == nil {
		return nil
	}
	if !agentd.ValidAccountKey(c.AccountKey) || c.AttachmentRevision <= 0 || c.ExecutionPolicyID != lifecycleclient.ConversationPolicyV1 ||
		c.MaxOutputBytes <= 0 || c.MaxOutputBytes > 256<<10 || c.MaxEvents < 2 || c.MaxEvents > 512 {
		return errors.New("conversation binding configuration invalid")
	}
	profile, err := configuredProfile(c.DispatchProfileID, c.DispatchProfileVersion)
	if err != nil || profile.Harness != agentd.AdapterCodex ||
		!p.registration.MatchScopeAtRevision("chatgpt", c.AccountKey, profile.ID, profile.Version, true, c.AttachmentRevision) {
		return errors.New("conversation binding configuration invalid")
	}
	adapter, ok := p.owner.supervisor.CodexConversationAdapter()
	if !ok || !adapter.HasAccount(c.AccountKey) {
		return errors.New("conversation binding account is unavailable")
	}
	_, mode, revision := p.owner.supervisor.AttachedAccountKeys(p.config.ProjectID, agentd.AdapterCodex)
	if mode != agentd.AccountLifecycleExplicit || revision != c.AttachmentRevision ||
		!p.owner.supervisor.AccountAttached(p.config.ProjectID, agentd.AdapterCodex, c.AccountKey) {
		return errors.New("conversation binding attachment is unavailable")
	}
	scratchRoot := filepath.Join(projectDir, "conversation-scratch")
	if err := os.MkdirAll(scratchRoot, 0o700); err != nil {
		return errors.New("conversation scratch root unavailable")
	}
	if info, err := os.Lstat(scratchRoot); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("conversation scratch root is not private")
	}
	launcher := &projectConversationLauncher{project: p, adapter: adapter, scratchRoot: scratchRoot}
	runner, err := lifecycleclient.NewConversationRunner(filepath.Join(projectDir, "conversation"), p.registration.Generation, p.authority, launcher)
	if err != nil {
		return err
	}
	capability := &lifecycleintents.ConversationCapability{
		SchemaVersion: lifecycleintents.ConversationSchemaV1, AccountKey: c.AccountKey,
		AttachmentRevision: c.AttachmentRevision, DispatchProfileID: c.DispatchProfileID,
		DispatchProfileVersion: c.DispatchProfileVersion, ExecutionPolicyID: c.ExecutionPolicyID,
		MaxOutputBytes: int64(c.MaxOutputBytes), MaxEvents: int64(c.MaxEvents),
	}
	p.registration.Conversation = capability
	if err := lifecycleintents.ValidateRegistration(p.registration); err != nil {
		p.registration.Conversation = nil
		return errors.New("conversation readiness advertisement invalid")
	}
	p.conversation = runner
	return nil
}

func (p *projectLifecycle) runConversation(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		runtime := p.runtimeSnapshot()
		if runtime.ID != "" {
			stepCtx, cancel := context.WithTimeout(ctx, 190*time.Second)
			_ = p.conversation.Step(stepCtx, runtime)
			cancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *projectLifecycle) runtimeSnapshot() lifecycleintents.Runtime {
	p.runtimeMu.RLock()
	defer p.runtimeMu.RUnlock()
	return p.record.Runtime
}
