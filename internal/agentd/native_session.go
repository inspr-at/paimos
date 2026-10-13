// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/localjournal"
	"github.com/inspr-at/paimos/internal/ownedprocess"
)

const NativeSessionProtocol = "aeon-native-session/1"
const NativeSessionProtocolHeader = "X-Aeon-Session-Protocol"

type NativeSessionStartRequest struct {
	RequestID string `json:"request_id"`
	ProjectID string `json:"project_id"`
	Profile   string `json:"profile"`
	Role      string `json:"role"`
	Harness   string `json:"harness"`
	Cwd       string `json:"cwd"`
	Resume    string `json:"resume,omitempty"`
	AfterExit int    `json:"after_exit,omitempty"`
}

func (r NativeSessionStartRequest) Validate() error {
	if !uuidPattern.MatchString(r.RequestID) || !uuidPattern.MatchString(r.ProjectID) || r.Profile != "native" || r.Role != "coordinator" || r.Harness != Claude || !filepath.IsAbs(r.Cwd) || len(r.Cwd) > 4096 || r.Resume != "" && !uuidPattern.MatchString(r.Resume) || r.AfterExit < 0 || r.AfterExit > 2147483647 || r.AfterExit > 0 && r.Resume == "" {
		return ErrScope
	}
	physical, err := filepath.EvalSymlinks(r.Cwd)
	if err != nil || physical != r.Cwd {
		return ErrScope
	}
	info, err := os.Stat(physical)
	if err != nil || !info.IsDir() {
		return ErrScope
	}
	return nil
}

type NativeSessionStartResult struct {
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	State     string `json:"state"`
}

// NativeLeadPlan is produced by an authorized server start intent, never decoded
// from the local request. The server owns root account/host reservation and
// actor/history/tool authority. The account key and cwd remain local.
type NativeLeadPlan struct {
	Spec                        NativeLeadSpec
	Session                     HarnessSession
	AccountKey, Workspace, Host string
}

// All callbacks are mandatory; nil is the released, disabled configuration.
// This is the P1b/I1 integration port, not a person-consent or admission bypass.
// Authorize reserves the server root before return. Revalidate fences current
// rights immediately before fork/input. ReportExit settles only observed exits.
type NativeSessionConfig struct {
	Authorize     func(context.Context, NativeSessionStartRequest) (NativeLeadPlan, error)
	Revalidate    func(context.Context, NativeLeadGrant) error
	Register      func(context.Context, NativeLeadPlan) (HarnessSession, error)
	ReportStarted func(context.Context, HarnessSession, ownedprocess.Identity) error
	ReportExit    func(context.Context, HarnessSession, ownedprocess.Identity) error
	Abandon       func(context.Context, NativeLeadPlan) error
	SessionIO     func(HarnessSession) LeadSessionIO
	Chat          func(HarnessSession) ChatRelayAPI
	// Test clock/exit barrier; no user request can supply either implementation.
	PinExit func(int, string, string) (func(context.Context) error, error)
}

type nativeLeadRecord struct {
	Request           NativeSessionStartRequest `json:"request"`
	Digest            string                    `json:"digest"`
	SessionID         string                    `json:"session_id"`
	PolicyDigest      string                    `json:"policy_digest"`
	AccountHomeDigest string                    `json:"account_home_digest"`
	VendorSessionID   string                    `json:"vendor_session_id,omitempty"`
	Attempts          map[string]string         `json:"attempts,omitempty"`
}

type nativeLeadEntry struct {
	mu            sync.Mutex
	record        leadRecord
	plan          NativeLeadPlan
	process       Process
	adapter       LeadRuntimeAdapter
	chat          *sessionChat
	relay         *ChatRelay
	result        NativeSessionStartResult
	cancel        context.CancelFunc
	sequence      int64
	vendorSession chan string
	done          chan struct{}
	doneOnce      sync.Once
	// In-memory reconciliation only; persisted receipts never grant adoption.
	ownershipSaved bool
	ledgerRunning  bool
	relayStarted   bool
}
type nativeSessions struct {
	mu      sync.Mutex
	config  *NativeSessionConfig
	entries map[string]*nativeLeadEntry
}

// Version 2 adds native root provenance and input attempt digests. Version 1
// migrates without gaining native authority. Older daemons reject version 2 in
// their retained-lead fence before dispatch; future versions refuse explicitly.
const leadRecordSchemaVersion = 2

func decodeLeadRecord(raw []byte) (leadRecord, error) {
	var header struct {
		Version int `json:"version"`
	}
	if len(raw) > 32768 || json.Unmarshal(raw, &header) != nil {
		return leadRecord{}, ErrProcessesUnconfirmed
	}
	if header.Version != 1 && header.Version != leadRecordSchemaVersion {
		return leadRecord{}, &localjournal.SchemaVersionError{Stored: header.Version, Supported: leadRecordSchemaVersion}
	}
	var r leadRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&r) != nil || decoder.Decode(&struct{}{}) != io.EOF || header.Version == 1 && r.Native != nil {
		return leadRecord{}, ErrProcessesUnconfirmed
	}
	r.Version = leadRecordSchemaVersion
	return r, nil
}

func nativeConfigReady(c *NativeSessionConfig) bool {
	return c != nil && c.Authorize != nil && c.Revalidate != nil && c.Register != nil && c.ReportStarted != nil && c.ReportExit != nil && c.Abandon != nil && c.SessionIO != nil && c.Chat != nil
}

func (s *Supervisor) StartNativeSession(ctx context.Context, request NativeSessionStartRequest) (NativeSessionStartResult, error) {
	if err := request.Validate(); err != nil {
		return NativeSessionStartResult{}, err
	}
	if s.native == nil || !nativeConfigReady(s.native.config) || s.ledger == nil {
		return NativeSessionStartResult{}, ErrNativeUnqualified
	}
	if err := s.dispatchMu.LockContext(ctx); err != nil {
		return NativeSessionStartResult{}, err
	}
	defer s.dispatchMu.Unlock()
	if !s.dispatchAllowed("") {
		return NativeSessionStartResult{}, ErrDraining
	}
	rawRequest, _ := json.Marshal(request)
	digest := nativeDigest(string(rawRequest))
	s.native.mu.Lock()
	if entry := s.native.entries[request.RequestID]; entry != nil {
		s.native.mu.Unlock()
		entry.mu.Lock()
		defer entry.mu.Unlock()
		if entry.record.Native.Digest != digest {
			return NativeSessionStartResult{}, ErrReplay
		}
		return entry.result, nil
	}
	// A fresh request ID must not create another writer for a conversation
	// already owned (or awaiting handover) by this daemon, even if account/host
	// policy permits several different conversations concurrently.
	if request.Resume != "" {
		for _, entry := range s.native.entries {
			entry.mu.Lock()
			occupied := !entry.record.ExitObserved && !entry.record.Abandoned && (entry.record.Native.Request.Resume == request.Resume || entry.record.Native.VendorSessionID == request.Resume)
			entry.mu.Unlock()
			if occupied {
				s.native.mu.Unlock()
				return NativeSessionStartResult{}, ErrProcessesUnconfirmed
			}
		}
	}
	if len(s.native.entries) >= 32 {
		s.native.mu.Unlock()
		return NativeSessionStartResult{}, ErrHostCapacity
	}
	s.native.mu.Unlock()
	name := "lead-runtime-" + request.RequestID + ".json"
	if _, err := s.state.Read(name, 32768); err == nil {
		return NativeSessionStartResult{}, ErrProcessesUnconfirmed
	} else if !errors.Is(err, os.ErrNotExist) {
		return NativeSessionStartResult{}, err
	}
	c := s.native.config
	plan, err := c.Authorize(ctx, request)
	if err != nil {
		return NativeSessionStartResult{}, err
	}
	plan.Spec.Grant.AllowedTools = slices.Clone(plan.Spec.Grant.AllowedTools)
	if plan.Spec.Tools != nil {
		tools := *plan.Spec.Tools
		plan.Spec.Tools = &tools
	}
	g := plan.Spec.Grant
	var adapter *ClaudeAdapter
	for _, account := range s.accounts {
		if account.ID == g.AccountID && account.Key == plan.AccountKey && account.Harness == Claude {
			adapter, _ = s.adapters[Claude].(*ClaudeAdapter)
			break
		}
	}
	if adapter == nil || !g.valid(s.capacityNow()) || g.TenantID != s.tenantID || g.PrincipalID != s.principalID || g.ProjectID != request.ProjectID || g.DaemonID != s.daemonID || g.Generation != s.generation || plan.Workspace != request.Cwd || plan.Spec.Resume != request.Resume || plan.Session.ID != g.SessionID || plan.Session.ProjectID != g.ProjectID || plan.Session.Harness != Claude || plan.Session.AttachedHook || !s.dispatchAllowed(g.AccountID) || !s.accountAvailable(g.AccountID) {
		return NativeSessionStartResult{}, errors.Join(ErrScope, c.Abandon(ctx, plan))
	}
	home, err := localHome(adapter.Homes, plan.AccountKey)
	if err != nil || nativeDigest(home) != g.AccountHomeDigest {
		return NativeSessionStartResult{}, errors.Join(ErrScope, c.Abandon(ctx, plan))
	}
	// No CLI or SDK query is launched while the old writer still exists.
	var wait func(context.Context) error
	if request.AfterExit > 0 {
		pin := c.PinExit
		if pin == nil {
			pin = pinNativeExit
		}
		wait, err = pin(request.AfterExit, plan.Workspace, adapter.ClaudePath)
		if err != nil {
			return NativeSessionStartResult{}, errors.Join(err, c.Abandon(ctx, plan))
		}
	}
	group, err := agentsetup.LedgerID()
	if err != nil {
		return NativeSessionStartResult{}, errors.Join(err, c.Abandon(ctx, plan))
	}
	candidates, err := s.ledger.Acquire(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, group, s.ledgerCandidates([]string{g.AccountID}, s.ledgerBinding.Generation))
	if err != nil {
		return NativeSessionStartResult{}, errors.Join(err, c.Abandon(ctx, plan))
	}
	if len(candidates) != 1 || candidates[0].AccountID != g.AccountID {
		return NativeSessionStartResult{}, ErrProcessesUnconfirmed
	}
	record := leadRecord{Version: leadRecordSchemaVersion, Pickup: agentsetup.LeadPickup{Demand: agentsetup.LeadDemand{ID: request.RequestID, ProjectID: g.ProjectID, OwnerID: g.OwnerID}, DaemonID: s.daemonID, DaemonGeneration: s.generation, LedgerGeneration: s.ledgerBinding.Generation, AccountID: g.AccountID}, Group: group, Native: &nativeLeadRecord{Request: request, Digest: digest, SessionID: g.SessionID, PolicyDigest: g.PolicyDigest, AccountHomeDigest: g.AccountHomeDigest, Attempts: map[string]string{}}}
	if err := s.writeLead(record, true); err != nil {
		return NativeSessionStartResult{}, err
	}
	lifetime, cancel := context.WithDeadline(s.lifetime, g.ExpiresAt)
	entry := &nativeLeadEntry{record: record, plan: plan, adapter: adapter, cancel: cancel, vendorSession: make(chan string, 1), done: make(chan struct{}), result: NativeSessionStartResult{RequestID: request.RequestID, SessionID: g.SessionID, State: "waiting_for_exit"}}
	s.native.mu.Lock()
	s.native.entries[request.RequestID] = entry
	s.native.mu.Unlock()
	if wait != nil {
		result := entry.result
		go func() {
			if err := wait(lifetime); err != nil {
				s.abandonNative(entry)
				return
			}
			if err := s.dispatchMu.LockContext(lifetime); err != nil {
				s.abandonNative(entry)
				return
			}
			defer s.dispatchMu.Unlock()
			_ = s.launchNative(lifetime, lifetime, entry)
		}()
		return result, nil
	}
	if err := s.launchNative(ctx, lifetime, entry); err != nil {
		return NativeSessionStartResult{}, err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.result, nil
}

// Called under dispatchMu; persist the possible-fork boundary before StartLead.
func (s *Supervisor) launchNative(ctx, lifetime context.Context, entry *nativeLeadEntry) (resultErr error) {
	c := s.native.config
	entry.mu.Lock()
	defer func() {
		if resultErr != nil && entry.result.State != "abandoned" && entry.result.State != "exited" {
			entry.result.State = "exit_unconfirmed"
		}
		entry.mu.Unlock()
	}()
	g := entry.plan.Spec.Grant
	if !s.dispatchAllowed(g.AccountID) || !s.accountAvailable(g.AccountID) {
		return errors.Join(ErrDraining, s.abandonNativeLocked(entry))
	}
	if err := c.Revalidate(ctx, g); err != nil {
		return errors.Join(err, s.abandonNativeLocked(entry))
	}
	entry.plan.Spec.Require = func(ctx context.Context) error { return c.Revalidate(ctx, g) }
	session, err := c.Register(ctx, entry.plan)
	if err != nil {
		return errors.Join(err, s.abandonNativeLocked(entry))
	}
	if session.ID != g.SessionID || session.ProjectID != g.ProjectID || session.Harness != Claude || session.AttachedHook || len(session.Lease) < 16 {
		return errors.Join(ErrScope, s.abandonNativeLocked(entry))
	}
	entry.plan.Session = session
	chatAPI := c.Chat(session)
	if chatAPI == nil {
		return errors.Join(ErrNativeUnqualified, s.abandonNativeLocked(entry))
	}
	if err := s.ledger.Claimed(s.ledgerBinding.Member.ID, entry.record.Pickup.LedgerGeneration, entry.record.Group, s.ledgerLogin(g.AccountID, entry.record.Pickup.LedgerGeneration)); err != nil {
		return err
	}
	entry.chat = newSessionChat(ChatBinding{TenantID: g.TenantID, PrincipalID: g.PrincipalID, Generation: g.Generation, SessionID: session.ID}, sessionChatCapabilities(Claude, []string{"interrupt"}))
	entry.plan.Spec.Require = func(ctx context.Context) error { return c.Revalidate(ctx, g) }
	entry.plan.Spec.Now = s.capacityNow
	entry.plan.Spec.BeforeInput = func(ctx context.Context, id, digest string) error {
		return s.nativeInputAttempt(ctx, entry, id, digest)
	}
	entry.plan.Spec.Observe = func(event AdapterEvent) {
		if event.Chat != nil {
			entry.chat.publishCorrelated(*event.Chat, event.InputID)
		}
		if event.VendorSessionID != "" {
			select {
			case entry.vendorSession <- event.VendorSessionID:
			default:
			}
		}
	}
	entry.relay = NewChatRelay(chatAPI, session, ChatRelayOptions{RequireInputCorrelation: true, Deliver: func(ctx context.Context, id, body string, writing func()) error {
		if len(body) > chatChunkBytes {
			return ErrScope
		}
		entry.mu.Lock()
		process, ok := entry.process.(*nativeClaudeLead)
		entry.mu.Unlock()
		if !ok {
			return ErrNotOwned
		}
		writing()
		return process.Input(ctx, id, chatInputText(id, body))
	}})
	entry.chat.attachRelay(entry.relay)
	entry.record.Attempted = true
	entry.result.State = "starting"
	if err := s.writeLead(entry.record, false); err != nil {
		return err
	}
	if err := s.ledger.Launching(s.ledgerBinding.Member.ID, entry.record.Pickup.LedgerGeneration, entry.record.Group); err != nil {
		return err
	}
	if err := c.Revalidate(ctx, g); err != nil {
		entry.record.Attempted = false
		return errors.Join(err, s.abandonNativeLocked(entry))
	}
	process, err := entry.adapter.StartLead(ctx, LeadStartRequest{Native: &entry.plan.Spec, Session: session, AccountKey: entry.plan.AccountKey, Workspace: entry.plan.Workspace, Lifetime: lifetime, SessionIO: c.SessionIO(session)})
	if err != nil || process == nil {
		entry.result.State = "exit_unconfirmed"
		return ErrProcessesUnconfirmed
	}
	entry.process = process
	go s.waitNative(entry)
	return s.reconcileNativeStartupLocked(ctx, entry)
}

// Called under dispatchMu and entry.mu. A post-fork failure retains its owned
// process and retries only unfinished initialization, never another fork.
func (s *Supervisor) reconcileNativeStartupLocked(ctx context.Context, entry *nativeLeadEntry) (resultErr error) {
	defer func() {
		if resultErr != nil {
			entry.result.State = "exit_unconfirmed"
		}
	}()
	recovery, ok := entry.process.(RecoveryProcess)
	if !ok {
		return ErrProcessesUnconfirmed
	}
	identity, err := recovery.Ownership()
	if err != nil || identity.DaemonID != s.daemonID || identity.Generation != s.generation || identity.RootPID != entry.process.PID() {
		return ErrProcessesUnconfirmed
	}
	if previous := entry.record.Ownership; previous != nil && *previous != identity {
		return ErrNotOwned
	}
	entry.record.Ownership = &identity
	if entry.record.Native.VendorSessionID == "" {
		select {
		case entry.record.Native.VendorSessionID = <-entry.vendorSession:
		default:
			return ErrProcessesUnconfirmed
		}
	}
	if !entry.ownershipSaved {
		if err := s.writeLead(entry.record, false); err != nil {
			return err
		}
		entry.ownershipSaved = true
	}
	if !entry.ledgerRunning {
		data, _, err := s.ledger.Snapshot()
		if err != nil {
			return err
		}
		group := data.Groups[entry.record.Group]
		// A failed fsync may have committed Running without returning success.
		// Reconcile only this exact retained owner, never another group's PID.
		if data.Generation != entry.record.Pickup.LedgerGeneration || group.Instance != s.ledgerBinding.Member.ID || group.Generation != entry.record.Pickup.LedgerGeneration || group.State != "running" || group.PID != identity.RootPID || !group.StartedAt.Equal(identity.StartedAt) {
			if err := s.ledger.Running(s.ledgerBinding.Member.ID, entry.record.Pickup.LedgerGeneration, entry.record.Group, identity.RootPID, identity.StartedAt); err != nil {
				return err
			}
		}
		entry.ledgerRunning = true
	}
	if err := s.native.config.ReportStarted(ctx, entry.plan.Session, identity); err != nil {
		return err
	}
	if !entry.relayStarted {
		entry.relayStarted = true
		go entry.relay.Run(s.lifetime)
	}
	entry.result.State = "running"
	return nil
}

func (s *Supervisor) nativeInputAttempt(ctx context.Context, entry *nativeLeadEntry, id, digest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry.record.ExitObserved || !entry.record.Attempted {
		return ErrNotOwned
	}
	if previous := entry.record.Native.Attempts[id]; previous != "" {
		if previous != digest {
			return ErrReplay
		}
		return ErrControlUnconfirmed
	}
	// Exhaustion must still permit one durable close attempt, without evicting
	// an uncertain input. Input cannot choose this private reserved identity.
	if len(entry.record.Native.Attempts) >= 128 && (id != "native-stop" || digest != nativeDigest("stop") || len(entry.record.Native.Attempts) > 128) {
		return ErrControlUnconfirmed
	}
	entry.record.Native.Attempts[id] = digest
	return s.writeLead(entry.record, false)
}

func (s *Supervisor) waitNative(entry *nativeLeadEntry) {
	defer entry.doneOnce.Do(func() { close(entry.done) })
	_ = entry.process.Wait()
	entry.cancel()
	entry.relay.Close()
	entry.chat.close()
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.record.Ownership == nil || !entry.adapter.LeadExitConfirmed(entry.process) {
		entry.result.State = "exit_unconfirmed"
		return
	}
	entry.record.ExitObserved = true
	entry.result.State = "exited"
	_ = s.writeLead(entry.record, false) // Failure retains the possible-fork hold.
}

func (s *Supervisor) abandonNative(entry *nativeLeadEntry) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.dispatchMu.LockContext(ctx); err != nil {
		return
	}
	defer s.dispatchMu.Unlock()
	entry.mu.Lock()
	defer entry.mu.Unlock()
	_ = s.abandonNativeLocked(entry)
}

func (s *Supervisor) abandonNativeLocked(entry *nativeLeadEntry) error {
	if entry.record.Attempted {
		return ErrProcessesUnconfirmed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.native.config.Abandon(ctx, entry.plan); err != nil {
		return err
	}
	if err := s.ledger.Release(s.ledgerBinding.Member.ID, entry.record.Pickup.LedgerGeneration, entry.record.Group); err != nil {
		return err
	}
	entry.record.Abandoned = true
	entry.result.State = "abandoned"
	entry.cancel()
	err := s.writeLead(entry.record, false)
	if err == nil {
		entry.doneOnce.Do(func() { close(entry.done) })
	}
	return err
}

// Poll services only in-memory owners; a saved PID never becomes launch or
// signaling authority. API failure and idle turn completion retain occupancy.
func (s *Supervisor) PollNativeSessions(ctx context.Context) error {
	if s.native == nil || !nativeConfigReady(s.native.config) {
		return nil
	}
	if err := s.dispatchMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.dispatchMu.Unlock()
	s.native.mu.Lock()
	entries := make([]*nativeLeadEntry, 0, len(s.native.entries))
	for _, entry := range s.native.entries {
		entries = append(entries, entry)
	}
	s.native.mu.Unlock()
	for _, entry := range entries {
		entry.mu.Lock()
		process, session, record := entry.process, entry.plan.Session, entry.record
		entry.mu.Unlock()
		if process == nil || record.Abandoned || record.ExitReported {
			continue
		}
		if record.ExitObserved {
			if err := s.native.config.ReportStarted(ctx, session, *record.Ownership); err != nil {
				return err
			}
			if err := s.native.config.ReportExit(ctx, session, *record.Ownership); err != nil {
				return err
			}
			if err := s.ledger.Release(s.ledgerBinding.Member.ID, record.Pickup.LedgerGeneration, record.Group); err != nil {
				return err
			}
			entry.mu.Lock()
			entry.record.ExitReported = true
			err := s.writeLead(entry.record, false)
			entry.mu.Unlock()
			if err != nil {
				return err
			}
			continue
		}
		entry.mu.Lock()
		if entry.record.ExitObserved {
			entry.mu.Unlock()
			continue // The waiter won; report its exit on the next poll.
		}
		if err := s.reconcileNativeStartupLocked(ctx, entry); err != nil {
			entry.mu.Unlock()
			return err
		}
		session.Ownership = entry.record.Ownership
		entry.sequence++
		session.ActivitySequence = entry.sequence
		entry.mu.Unlock()
		if err := s.api.HeartbeatHarness(ctx, session, "working"); err != nil {
			return err
		}
		if err := entry.adapter.ServiceLead(ctx, process, nil); err != nil {
			return err
		}
	}
	return nil
}
