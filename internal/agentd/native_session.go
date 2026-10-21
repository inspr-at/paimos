// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativeintegration/delivery"
	"github.com/inspr-at/paimos/internal/nativepty"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/nativesessions/daemon"
	"github.com/inspr-at/paimos/internal/nativesessions/stream"
	"github.com/inspr-at/paimos/internal/nativestream"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/localjournal"
	"github.com/inspr-at/paimos/internal/nativecontract"
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

// Nil keeps the released default disabled. The legacy callbacks below are
// mandatory for the P1a path; NativeProduction binds C0 independently.
// This is the P1b/I1 integration port, not a person-consent or admission bypass.
// Authorize reserves the server root before return. Revalidate fences current
// rights immediately before fork/input. ReportExit settles only observed exits.
type NativeSessionConfig struct {
	Bind     func(*Supervisor)
	Poll     func(context.Context) error
	Close    func(context.Context) error
	Host     nativecontract.Host
	Launcher nativecontract.Launcher
	// Terminal and Qualification are supplied together by an independently
	// qualified provider. Neither a socket caller nor a capability flag installs
	// them. The released composition leaves both nil.
	Terminal      nativecontract.PTYLauncher
	Qualification nativepty.Qualification
	// Local reads/control need a separately verified person and current server
	// grants. No default maps a socket peer or the paired agent to that person.
	LocalAuthority nativestream.LocalAuthority
	Projector      *nativestream.Projector
	Live           *stream.Relay

	// StartLocal resolves the exact persisted PERSON-issued C0 intent and
	// revalidates its original actor/current grant before using the owning
	// supervisor. Socket possession and wire selectors grant no authority.
	// Nil leaves C0 start unavailable; it never falls back to the P1a DTO.
	StartLocal    func(context.Context, nativecontract.LocalStart) (nativecontract.OperationResult, error)
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
	Transition        *nativeTransitionRecord   `json:"transition,omitempty"`
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

// Version is a major; additive fields use minor_version and are ignored by
// older readers. Native major 2 is stored beside the released major-1 fence.
// Future breaking majors must likewise retain the previous reader's fence.
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
	if json.Unmarshal(raw, &r) != nil || r.MinorVersion < 0 || header.Version == 1 && r.Native != nil {
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
	if s.native != nil && s.native.config != nil && s.native.config.Poll != nil {
		if err := s.native.config.Poll(ctx); err != nil {
			return err
		}
	}
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

// NativeProduction installs C0 on the owning supervisor. Profiles must come
// from separately qualified existing enrollments; the option creates none.
func NativeProduction(remote *Remote, computer string, owner string, profiles nativecontract.ProfileResolver, caps nativecontract.Capabilities) *NativeSessionConfig {
	h := &NativeHost{remote: remote, computer: computer, owner: owner, profiles: profiles, caps: caps, entries: map[string]*nativeOwned{}}
	c := &NativeSessionConfig{StartLocal: h.Start, Poll: h.Poll, Close: h.Close, Host: h, Launcher: h}
	h.config = c
	c.Bind = func(s *Supervisor) { h.s = s }
	return c
}

type NativeHost struct {
	owner    string
	config   *NativeSessionConfig
	s        *Supervisor
	remote   *Remote
	computer string
	profiles nativecontract.ProfileResolver
	caps     nativecontract.Capabilities
	mu       sync.Mutex
	entries  map[string]*nativeOwned
	// Abandoned starts whose server root is not yet settled; Poll retries them.
	unsettled []*nativeOwned
}

// Limits count live work only. Exited owners are retained as exact successor
// sources up to nativeRetainedExits, newest first; completed inputs leave the
// live set, and their final-drain routes stay until the grant expires.
const (
	nativeHostSessions  = 32
	nativeRetainedExits = 32
	nativeLiveInputs    = 128
)

// nativeDrain keeps the operation of a possibly completed input for its final.
type nativeDrain struct {
	grant     nativecontract.FinalDrainGrant
	operation string
}
type nativeOwned struct {
	owner         nativecontract.CommandActor
	pendingEvents []nativecontract.ProviderEvent
	eventMu       sync.Mutex
	claimDeadline time.Time
	released      chan struct{}
	releaseOnce   sync.Once
	releasedAt    time.Time
	ownership     *ownedprocess.Identity
	cancel        context.CancelFunc
	observations  map[string]nativecontract.NativeEvidence
	exit          *nativecontract.ExitEvidence
	drains        map[string]nativeDrain // input ID -> final route, until grant expiry
	// recorded: the possible lead record may exist. refused: report delivery
	// was refused for good, so only exit reconciliation and stop still run.
	recorded      bool
	refused       error
	abandonReason nativecontract.Reason

	host              *NativeHost
	local             nativecontract.LocalStart
	claim             nativecontract.Claim
	grant             nativecontract.ClaimResult
	profile           nativecontract.LocalProfile
	proof             daemon.Proof
	group             string
	journal           *nativereceipts.Store
	delivery          *delivery.Delivery
	transport         nativecontract.NativeTransport
	active            nativecontract.ClaimResult
	operations        map[string]string // live input ID -> operation ID
	mu                sync.Mutex
	opMu              contextMutex
	attempted, exited bool
	lastError         error
	transition        *nativeTransition
	eventCancel       context.CancelFunc
	eventDone         chan struct{}
	lifetime          context.Context
	startOperation    string
	recoveryResults   map[string]nativecontract.OperationResult
}

type nativeC0Key struct{}
type nativeC0Launch struct {
	spec    nativecontract.LaunchSpec
	observe func(json.RawMessage)
}

func nativeRefusal(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}

func (h *NativeHost) Start(ctx context.Context, local nativecontract.LocalStart) (out nativecontract.OperationResult, err error) {
	// Startup only. Cancelling this context does not close the accepted lifetime.
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	if h.s == nil || h.remote == nil || h.profiles == nil || !uuidPattern.MatchString(h.owner) || h.s.ledger == nil || h.caps.QualificationDigest == "" {
		return out, nativeRefusal(nativecontract.ReasonQualificationMissing)
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return out, err
	}
	if err = nativereceipts.Validate(v, "NativeLocalStart", local); err != nil {
		return out, err
	}
	if local.Intent.ComputerID != h.computer || local.Intent.Harness != Claude || local.Intent.Role != "coordinator" {
		return out, nativeRefusal(nativecontract.ReasonWrongPrincipal)
	}
	// Resume/handover requires an exact retained source writer; never adopt an
	// arbitrary saved PID or let the old first-start DTO bypass C0's barrier.
	if err = h.prepareAfterExit(ctx, local); err != nil {
		return out, err
	}
	s := h.s
	if err = s.dispatchMu.LockContext(ctx); err != nil {
		return out, err
	}
	defer s.dispatchMu.Unlock()
	if !s.dispatchAllowed(local.Intent.AccountID) || !s.accountAvailable(local.Intent.AccountID) {
		return out, ErrDraining
	}
	h.mu.Lock()
	existing := h.entries[local.Intent.RequestID]
	live := h.liveLocked()
	h.mu.Unlock()
	if existing != nil {
		if !reflect.DeepEqual(existing.local, local) {
			return out, nativeRefusal(nativecontract.ReasonIdentityMismatch)
		}
		existing.mu.Lock()
		grant := existing.grant
		ready := existing.transport != nil
		existing.mu.Unlock()
		if !ready {
			return out, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
		}
		return existing.ready(ctx, grant)
	}
	if live >= nativeHostSessions {
		return out, nativeRefusal(nativecontract.ReasonHostFull)
	}
	enrolled := false
	for _, a := range s.accounts {
		if a.ID == local.Intent.AccountID {
			enrolled = true
		}
		if a.ID == local.Intent.AccountID && (a.Harness != Claude || a.Key != local.AccountKey) {
			return out, nativeRefusal(nativecontract.ReasonIdentityMismatch)
		}
	}
	if !enrolled {
		return out, nativeRefusal(nativecontract.ReasonQualificationMissing)
	}
	record := "lead-runtime-" + local.Intent.RequestID + ".json"
	if _, err = s.state.Read(record, 16384); err == nil {
		return out, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	} else if !errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	group, err := agentsetup.LedgerID()
	if err != nil {
		return out, err
	}
	candidates, err := s.ledger.Acquire(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, group, s.ledgerCandidates([]string{local.Intent.AccountID}, s.ledgerBinding.Generation))
	if err != nil {
		return out, err
	}
	e := &nativeOwned{host: h, local: local, group: group, released: make(chan struct{}), operations: map[string]string{}, observations: map[string]nativecontract.NativeEvidence{}, drains: map[string]nativeDrain{}}
	// Before the possible-fork marker no provider exists: every refusal frees
	// the local slot and host entry and settles a claimed server root, like the
	// legacy abandonNativeLocked. Later failures retain the possible owner.
	defer func() {
		if err != nil && !e.attempted {
			err = errors.Join(err, h.abandon(e, err))
		}
	}()
	if len(candidates) != 1 || candidates[0].AccountID != local.Intent.AccountID {
		return out, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	ref, err := randomID()
	if err != nil {
		return out, err
	}
	lease, err := randomID()
	if err != nil {
		return out, err
	}
	request, reservation := uuid.NewString(), uuid.NewString()
	now := s.capacityNow()
	e.proof = daemon.Proof{DaemonID: s.daemonID, Generation: s.generation, Ledger: s.ledgerBinding.Generation, Capability: h.caps.QualificationDigest, WorkerLease: lease, SessionRef: ref, ReservationID: reservation, ObservedAt: now, ExpiresAt: now.Add(20 * time.Second), LocalStart: &local}
	e.claim = nativecontract.Claim{RequestID: request, OperationID: local.Intent.RequestID, DaemonID: s.daemonID, DaemonGeneration: s.generation, LedgerGeneration: s.ledgerBinding.Generation, CapabilityDigest: h.caps.QualificationDigest, ExpectedRevision: 1, ClaimKind: "start"}
	if err = e.call(ctx, "POST", "/api/agentd/native-sessions/claim", e.claim, &e.grant); err != nil {
		return out, err
	}
	g := e.grant
	e.startOperation = g.OperationID
	e.claimDeadline = now.Add(time.Duration(g.AuthorizationTTLMS) * time.Millisecond)
	if !g.LaunchAuthorized || g.ExpectedBinding == nil || !reflect.DeepEqual(g.Intent, local.Intent) || g.ExpectedBinding.DaemonID != s.daemonID || g.ExpectedBinding.DaemonGeneration != s.generation || g.ExpectedBinding.TenantID != s.tenantID {
		return out, nativeRefusal(nativecontract.ReasonIdentityMismatch)
	}
	b := *g.ExpectedBinding
	var currentView nativecontract.SessionView
	if err = e.call(ctx, "GET", "/api/projects/"+b.ProjectID+"/native-sessions/"+b.OwnedSessionID, nil, &currentView); err != nil {
		return out, err
	}
	if currentView.CurrentBinding == nil || !currentView.CurrentBinding.Equal(b) || currentView.OwnerPersonID != h.owner {
		return out, nativeRefusal(nativecontract.ReasonWrongPrincipal)
	}
	e.owner = nativecontract.CommandActor{TenantID: s.tenantID, PrincipalID: currentView.OwnerPersonID, OwnerPersonID: currentView.OwnerPersonID, GrantID: g.Policy.GrantID, GrantRevision: g.Policy.GrantRevision}
	profile, err := h.profiles.Resolve(ctx, nativecontract.ProfileRequest{TenantID: s.tenantID, OwnerPersonID: e.owner.OwnerPersonID, AccountID: local.Intent.AccountID, AccountKey: local.AccountKey, WorkspaceID: local.Intent.WorkspaceID, Harness: Claude, Policy: g.Policy, BindingRevision: b.BindingRevision})
	if err != nil {
		return out, err
	}
	if profile.AccountID != local.Intent.AccountID || profile.ProfileBindingRevision != b.BindingRevision || !nativeWithin(profile.WorkspaceRoot, local.Cwd) {
		return out, nativeRefusal(nativecontract.ReasonIdentityMismatch)
	}
	e.profile = profile
	e.proof.WorkspaceRoot = profile.WorkspaceRoot
	journal, err := nativereceipts.Open(filepath.Join(s.state.Path(), "native-c0-"+b.OwnedSessionID), s.capacityNow)
	if err != nil {
		return out, err
	}
	e.journal = journal
	h.mu.Lock()
	h.entries[local.Intent.RequestID] = e
	h.mu.Unlock()
	e.recorded = true
	if err = e.save("reserved"); err != nil {
		return out, err
	}
	if err = s.ledger.Claimed(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, group, s.ledgerLogin(local.Intent.AccountID, s.ledgerBinding.Generation)); err != nil {
		return out, err
	}
	spec := nativecontract.LaunchSpec{Binding: b, Profile: profile, Policy: g.Policy, Cwd: local.Cwd, Mode: "structured", OwnedWriterID: b.RuntimeEpoch, Options: g.LaunchConfig, Lifetime: s.lifetime}
	if local.NativeResumeRef != nil {
		spec.ResumeRef = *local.NativeResumeRef
	}
	deadline := s.capacityNow().Add(time.Duration(g.Policy.MaximumRuntimeSeconds) * time.Second)
	if g.Policy.ExpiresAt.Before(deadline) {
		deadline = g.Policy.ExpiresAt
	}
	spec.Lifetime, e.cancel = context.WithDeadline(s.lifetime, deadline)
	e.lifetime = spec.Lifetime
	if err = e.require(ctx); err != nil {
		return out, err
	}
	e.attempted = true
	if err = e.save("possible_fork"); err != nil {
		return out, err
	}
	if err = s.ledger.Launching(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, e.group); err != nil {
		return out, err
	}
	process, err := h.config.Launcher.Open(ctx, spec)
	if err != nil {
		return out, err
	}
	i := process.Identity()
	if i.RuntimeEpoch != b.RuntimeEpoch || i.PID < 1 || i.StartedAt.IsZero() || !nativereceipts.ValidDigest(i.ProcessIdentityDigest) {
		return out, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if owner, ok := process.(interface {
		Ownership() (ownedprocess.Identity, error)
	}); ok {
		identity, err := owner.Ownership()
		if err != nil {
			return out, err
		}
		e.ownership = &identity
	}
	if err = s.ledger.Running(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, e.group, i.PID, i.StartedAt); err != nil {
		return out, err
	}
	t := &nativeObservedTransport{NativeTransport: process, owner: e}
	e.mu.Lock()
	e.transport = t
	e.mu.Unlock()
	d, err := delivery.New(delivery.DeliveryOptions{Binding: b, Journal: journal, Transport: t, Authority: e, Reports: e, Finals: e, Now: s.capacityNow, Retention: e.retention, Release: e.release, Refused: func(err error) bool { _ = e.fault(err); return e.faulted() != nil }})
	if err != nil {
		return out, err
	}
	e.delivery = d
	out, err = e.ready(ctx, g)
	if err != nil {
		return out, err
	}
	e.startEvents()
	return out, nil
}
func nativeWithin(root, cwd string) bool {
	physical, err := filepath.EvalSymlinks(cwd)
	if err != nil || physical != cwd {
		return false
	}
	rel, err := filepath.Rel(root, cwd)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Open is reachable only for an already claimed, exact in-memory start. The
// possible-fork marker precedes the adapter and survives every uncertain error.
func (h *NativeHost) Open(ctx context.Context, spec nativecontract.LaunchSpec) (nativecontract.NativeTransport, error) {
	h.mu.Lock()
	var e *nativeOwned
	for _, candidate := range h.entries {
		if candidate.grant.ExpectedBinding != nil && candidate.grant.ExpectedBinding.Equal(spec.Binding) {
			e = candidate
			break
		}
	}
	h.mu.Unlock()
	if e == nil || !e.attempted || e.transport != nil || !reflect.DeepEqual(e.profile, spec.Profile) || spec.Cwd != e.local.Cwd {
		return nil, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	adapter, ok := h.s.adapters[Claude].(*ClaudeAdapter)
	if !ok {
		return nil, nativeRefusal(nativecontract.ReasonQualificationMissing)
	}
	if spec.Policy.LocalHistory != "vendor_native" {
		return nil, nativeRefusal(nativecontract.ReasonQualificationMissing)
	}
	t := newNativeC0Transport(e, spec, h.caps)
	grant := e.grant
	effort := ""
	if grant.LaunchConfig.Effort != nil {
		effort = *grant.LaunchConfig.Effort
	}
	policyDigest, _ := nativereceipts.Digest(spec.Policy)
	resumeDigest := ""
	if spec.ResumeRef != "" {
		resumeDigest = nativeDigest(spec.ResumeRef)
	}
	n := &NativeLeadSpec{Resume: spec.ResumeRef, Grant: NativeLeadGrant{TenantID: spec.Binding.TenantID, PrincipalID: h.s.principalID, OwnerID: e.owner.OwnerPersonID, ProjectID: spec.Binding.ProjectID, AccountID: spec.Profile.AccountID, SessionID: spec.Binding.HarnessSessionID, DaemonID: h.s.daemonID, Generation: h.s.generation, PolicyDigest: policyDigest, AccountHomeDigest: nativeDigest(spec.Profile.AccountHome), WorkspaceDigest: nativeDigest(spec.Cwd), ResumeDigest: resumeDigest, Revision: spec.Policy.GrantRevision, ExpiresAt: spec.Policy.ExpiresAt, HistoryApproved: true, AllowedTools: []string{}}, Prompt: "Wait for the person's next instruction. No tool use is authorized by this initialization.", Model: grant.LaunchConfig.Model, Effort: effort, Require: e.require, BeforeInput: t.beforeInput, Observe: func(AdapterEvent) {}, Now: h.s.capacityNow}
	launch := &nativeC0Launch{spec: spec, observe: t.observe}
	process, err := adapter.StartLead(context.WithValue(ctx, nativeC0Key{}, launch), LeadStartRequest{Native: n, Session: HarnessSession{ID: spec.Binding.HarnessSessionID, ProjectID: spec.Binding.ProjectID, Harness: Claude, Lease: e.proof.WorkerLease}, AccountKey: e.local.AccountKey, Workspace: spec.Cwd, Lifetime: spec.Lifetime})
	if err != nil {
		return nil, errors.Join(err, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed))
	}
	p, ok := process.(*nativeClaudeLead)
	if !ok {
		return nil, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	identity, err := p.Ownership()
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(identity)
	t.mu.Lock()
	t.process, t.writer = p, p
	t.identity.Provider = Claude
	t.identity.RuntimeEpoch = spec.Binding.RuntimeEpoch
	t.identity.PID = identity.RootPID
	t.identity.StartedAt = identity.StartedAt
	t.identity.ProcessIdentityDigest = nativeDigest(string(raw))
	t.mu.Unlock()
	if t.Identity().NativeSessionRef == "" {
		return nil, nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	e.ownership = &identity
	return t, nil
}
func (e *nativeOwned) save(state string) error {
	request := NativeSessionStartRequest{RequestID: e.local.Intent.RequestID, ProjectID: e.grant.ExpectedBinding.ProjectID, Profile: "native", Role: "coordinator", Harness: Claude, Cwd: e.local.Cwd}
	record := leadRecord{Version: leadRecordSchemaVersion, Pickup: agentsetup.LeadPickup{Demand: agentsetup.LeadDemand{ID: e.local.Intent.RequestID, ProjectID: e.grant.ExpectedBinding.ProjectID, OwnerID: e.owner.OwnerPersonID}, DaemonID: e.host.s.daemonID, DaemonGeneration: e.host.s.generation, LedgerGeneration: e.host.s.ledgerBinding.Generation, AccountID: e.local.Intent.AccountID}, Group: e.group, Attempted: e.attempted, Ownership: e.ownership, Native: &nativeLeadRecord{Request: request, SessionID: e.grant.ExpectedBinding.HarnessSessionID}}
	if e.transition != nil {
		record.MinorVersion = 1 // Additive transition metadata within native major 2.
		record.Native.Transition = &e.transition.record
	}
	if state == "exit_confirmed" {
		record.ExitObserved = true
		record.ExitReported = true
	}
	if state == "abandoned" {
		record.Abandoned = true
	}
	return e.host.s.writeLead(record, false)
}
func (e *nativeOwned) call(ctx context.Context, method, path string, body, dest any) error {
	e.mu.Lock()
	proof := e.proof
	t := e.transport
	e.mu.Unlock()
	if t != nil {
		i, ev, x := e.proofFor(nil)
		proof.SetRuntime(i, ev, x)
	}
	header, err := daemon.Encode(proof)
	if err != nil {
		return err
	}
	return e.host.remote.Client.DoWithHeaders(ctx, method, path, body, dest, map[string]string{daemon.ProofHeader: header})
}
func (e *nativeOwned) ready(ctx context.Context, g nativecontract.ClaimResult) (nativecontract.OperationResult, error) {
	r := nativecontract.Receipt{RequestID: e.local.Intent.RequestID, OperationID: e.startOperation, Binding: g.ExpectedBinding, Operation: "start", State: "applied", SourceSequence: "0", UpdatedAt: e.host.s.capacityNow().UTC()}
	var out nativecontract.OperationResult
	err := e.call(ctx, "POST", "/api/agentd/native-sessions/receipts", r, &out)
	if err == nil && (out.Session == nil || out.Session.OwnerPersonID != e.owner.OwnerPersonID) {
		return out, nativeRefusal(nativecontract.ReasonWrongPrincipal)
	}
	return out, err
}
func (e *nativeOwned) require(ctx context.Context) error {
	e.mu.Lock()
	t, active := e.transport, e.active
	e.mu.Unlock()
	if t == nil {
		if e.transition != nil {
			return e.revalidateSuccessor(ctx)
		}
		if !e.host.s.capacityNow().Before(e.claimDeadline) {
			return nativeRefusal(nativecontract.ReasonGrantExpired)
		}
		var current nativecontract.ClaimResult
		if err := e.call(ctx, "POST", "/api/agentd/native-sessions/claim", e.claim, &current); err != nil {
			return err
		}
		if !current.LaunchAuthorized || current.ExpectedBinding == nil || !current.ExpectedBinding.Equal(*e.grant.ExpectedBinding) || !reflect.DeepEqual(current.LaunchConfig, e.grant.LaunchConfig) || current.Policy.GrantID != e.grant.Policy.GrantID || current.AuthorizationTTLMS <= 0 {
			return nativeRefusal(nativecontract.ReasonIdentityMismatch)
		}
		return nil
	}
	profile, err := e.host.profiles.Resolve(ctx, nativecontract.ProfileRequest{TenantID: e.host.s.tenantID, OwnerPersonID: e.owner.OwnerPersonID, AccountID: e.local.Intent.AccountID, AccountKey: e.local.AccountKey, WorkspaceID: e.local.Intent.WorkspaceID, Harness: Claude, Policy: e.grant.Policy, BindingRevision: e.profile.ProfileBindingRevision})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(profile, e.profile) {
		return nativeRefusal(nativecontract.ReasonIdentityMismatch)
	}
	if active.OperationID != "" {
		claim := nativecontract.Claim{RequestID: uuid.NewString(), OperationID: active.OperationID, DaemonID: e.host.s.daemonID, DaemonGeneration: e.host.s.generation, LedgerGeneration: e.host.s.ledgerBinding.Generation, CapabilityDigest: e.host.caps.QualificationDigest, ExpectedRevision: e.grant.ExpectedBinding.BindingRevision, ClaimKind: "command"}
		var current nativecontract.ClaimResult
		if err = e.call(ctx, "POST", "/api/agentd/native-sessions/claim", claim, &current); err != nil {
			return err
		}
		if current.ExpectedBinding == nil || !current.ExpectedBinding.Equal(*e.grant.ExpectedBinding) || !reflect.DeepEqual(current.Command, active.Command) || current.CommandActor == nil || active.CommandActor == nil || *current.CommandActor != *active.CommandActor {
			return nativeRefusal(nativecontract.ReasonIdentityMismatch)
		}
		return nil
	}
	_, err = e.ready(ctx, e.grant)
	return err
}
func (e *nativeOwned) RequireWrite(ctx context.Context, b nativecontract.Binding, in nativereceipts.Intent) error {
	e.mu.Lock()
	active := e.active
	e.mu.Unlock()
	if !b.Equal(*e.grant.ExpectedBinding) || active.OperationID != in.Receipt.OperationID || active.CommandActor == nil || active.CommandActor.PrincipalID != in.ActorID || active.Command == nil {
		return nativeRefusal(nativecontract.ReasonGrantMissing)
	}
	var value any
	switch active.Command.Kind {
	case "input":
		var v nativecontract.Input
		if json.Unmarshal(active.Command.Params, &v) != nil {
			return ErrScope
		}
		value = v
	case "interrupt":
		var v nativecontract.Interrupt
		if json.Unmarshal(active.Command.Params, &v) != nil {
			return ErrScope
		}
		value = v
	case "stop":
		var v nativecontract.Stop
		if json.Unmarshal(active.Command.Params, &v) != nil {
			return ErrScope
		}
		value = v
	default:
		return ErrUnsupported
	}
	digest, err := nativereceipts.Digest(value)
	if err != nil || digest != in.PayloadSHA256 {
		return nativeRefusal(nativecontract.ReasonIdentityMismatch)
	}
	return e.require(ctx)
}
func (e *nativeOwned) Report(ctx context.Context, r nativecontract.Receipt) (nativecontract.Receipt, error) {
	e.mu.Lock()
	proof := e.proof
	t := e.transport
	e.mu.Unlock()
	if t == nil {
		return r, ErrNotOwned
	}
	i, ev, x := e.proofFor(&r)
	proof.SetRuntime(i, ev, x)
	header, err := daemon.Encode(proof)
	if err != nil {
		return r, err
	}
	var out nativecontract.OperationResult
	err = e.host.remote.Client.DoWithHeaders(ctx, "POST", "/api/agentd/native-sessions/receipts", r, &out, map[string]string{daemon.ProofHeader: header})
	return out.Receipt, err
}
func (e *nativeOwned) Persist(ctx context.Context, f nativecontract.FinalDelivery) (nativecontract.FinalReceipt, error) {
	var out nativecontract.FinalReceipt
	err := e.call(ctx, "POST", "/api/agentd/native-sessions/finals", f, &out)
	return out, err
}
func (e *nativeOwned) retention(ctx context.Context, f nativecontract.FinalPublish) (nativecontract.FinalDrainGrant, nativereceipts.RetentionApproval, error) {
	e.mu.Lock()
	drain, found := e.drains[f.InputID]
	e.mu.Unlock()
	grant := drain.grant
	if !found || grant.GrantID != f.DrainGrantID || !e.host.s.capacityNow().Before(grant.ExpiresAt) {
		return grant, nativereceipts.RetentionApproval{}, nativeRefusal(nativecontract.ReasonRetentionUnapproved)
	}
	claim := nativecontract.Claim{RequestID: uuid.NewString(), OperationID: drain.operation, DaemonID: e.host.s.daemonID, DaemonGeneration: e.host.s.generation, LedgerGeneration: e.host.s.ledgerBinding.Generation, CapabilityDigest: e.host.caps.QualificationDigest, ExpectedRevision: e.grant.ExpectedBinding.BindingRevision, ClaimKind: "final_drain"}
	var current nativecontract.ClaimResult
	if err := e.call(ctx, "POST", "/api/agentd/native-sessions/claim", claim, &current); err != nil {
		return grant, nativereceipts.RetentionApproval{}, err
	}
	if current.LaunchAuthorized || current.Command != nil || current.FinalDrainGrant == nil || !reflect.DeepEqual(*current.FinalDrainGrant, grant) {
		return grant, nativereceipts.RetentionApproval{}, nativeRefusal(nativecontract.ReasonRetentionUnapproved)
	}
	return grant, nativereceipts.RetentionApproval{Approved: true, OwnerPersonID: e.owner.OwnerPersonID, Until: grant.ExpiresAt}, nil
}
func (e *nativeOwned) release(ctx context.Context, b nativecontract.Binding, x nativecontract.ExitEvidence) error {
	if !x.RootExited || !x.GroupAbsent || x.ProcessIdentityDigest != e.transport.Identity().ProcessIdentityDigest {
		return nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if err := e.save("exit_confirmed"); err != nil {
		return err
	}
	s := e.host.s
	if err := s.ledger.Release(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, e.group); err != nil {
		return err
	}
	e.mu.Lock()
	e.exited = true
	e.releasedAt = s.capacityNow()
	if e.cancel != nil {
		e.cancel()
	}
	e.releaseOnce.Do(func() { close(e.released) })
	e.mu.Unlock()
	return nil
}

// liveLocked counts owners that may still hold a writer, under h.mu. Exited
// owners remain only as exact successor sources; beyond nativeRetainedExits
// the longest-released are dropped and their journals closed.
func (h *NativeHost) liveLocked() int {
	live := 0
	type retained struct {
		e  *nativeOwned
		at time.Time
	}
	var exited []retained
	for _, e := range h.entries {
		e.mu.Lock()
		done, at := e.exited, e.releasedAt
		e.mu.Unlock()
		if !done {
			live++
			continue
		}
		exited = append(exited, retained{e, at})
	}
	if len(exited) > nativeRetainedExits {
		slices.SortFunc(exited, func(a, b retained) int { return a.at.Compare(b.at) })
		for _, r := range exited[:len(exited)-nativeRetainedExits] {
			delete(h.entries, r.e.local.Intent.RequestID)
			_ = r.e.journal.Close()
		}
	}
	return live
}

// abandon settles a start refused before its possible-fork marker. No provider
// was opened, so the exact local slot and host entry are freed at once. A
// claimed server root is settled with a rejected start receipt; while the
// server cannot answer, Poll retries it. Every failure is returned.
func (h *NativeHost) abandon(e *nativeOwned, cause error) error {
	s := h.s
	var errs []error
	if e.cancel != nil {
		e.cancel()
	}
	if e.journal != nil {
		errs = append(errs, e.journal.Close())
	}
	if e.recorded {
		errs = append(errs, e.save("abandoned"))
	}
	errs = append(errs, s.ledger.Release(s.ledgerBinding.Member.ID, s.ledgerBinding.Generation, e.group))
	h.mu.Lock()
	if h.entries[e.local.Intent.RequestID] == e {
		delete(h.entries, e.local.Intent.RequestID)
	}
	h.mu.Unlock()
	if b := e.grant.ExpectedBinding; b == nil || e.grant.OperationID == "" || b.TenantID != s.tenantID || b.DaemonID != s.daemonID || b.DaemonGeneration != s.generation {
		return errors.Join(errs...) // No root of this daemon was reserved.
	}
	e.abandonReason = nativeRefusalReason(cause)
	ctx, cancel := context.WithTimeout(context.Background(), nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	if err := e.reject(ctx); err != nil {
		errs = append(errs, err)
		if !nativeDefinitive(err) {
			h.mu.Lock()
			if len(h.unsettled) < nativeHostSessions {
				h.unsettled = append(h.unsettled, e)
			}
			h.mu.Unlock()
		}
	}
	return errors.Join(errs...)
}

// reject reports verified no-fork abandonment for the exact claimed binding.
func (e *nativeOwned) reject(ctx context.Context) error {
	g := e.grant
	reason := e.abandonReason
	r := nativecontract.Receipt{RequestID: g.Intent.RequestID, OperationID: g.OperationID, Binding: g.ExpectedBinding, Operation: "start", State: "rejected", SourceSequence: "0", UpdatedAt: e.host.s.capacityNow().UTC(), Reason: &reason}
	var out nativecontract.OperationResult
	if err := e.call(ctx, "POST", "/api/agentd/native-sessions/receipts", r, &out); err != nil {
		return err
	}
	if out.Session == nil || out.Session.State != "failed" {
		return nativeRefusal(nativecontract.ReasonOwnershipUnconfirmed)
	}
	return nil
}

// settleAbandoned retries server settlement of abandoned starts; a definitive
// server answer ends the retry, an outage keeps it.
func (h *NativeHost) settleAbandoned(ctx context.Context) error {
	h.mu.Lock()
	pending := slices.Clone(h.unsettled)
	h.mu.Unlock()
	var errs []error
	for _, e := range pending {
		err := e.reject(ctx)
		if err == nil || nativeDefinitive(err) {
			h.mu.Lock()
			h.unsettled = slices.DeleteFunc(h.unsettled, func(c *nativeOwned) bool { return c == e })
			h.mu.Unlock()
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// nativeDefinitive reports a server refusal that a retry cannot change for
// the same binding (as opposed to an outage, timeout or rate limit).
func nativeDefinitive(err error) bool {
	var status *client.StatusError
	return errors.As(err, &status) && status.Status >= 400 && status.Status < 500 && status.Status != http.StatusRequestTimeout && status.Status != http.StatusTooManyRequests
}

// nativeRefusalReason names a start refusal with its closed native reason.
func nativeRefusalReason(err error) nativecontract.Reason {
	var refusal *nativecontract.ValidationError
	if errors.As(err, &refusal) {
		return refusal.Reason
	}
	var journal *nativereceipts.Error
	if errors.As(err, &journal) {
		return journal.Reason
	}
	var status *client.StatusError
	if errors.As(err, &status) {
		var body nativecontract.Error
		if json.Unmarshal([]byte(status.Message), &body) == nil {
			raw, _ := json.Marshal(body.Reason)
			if v, err := nativecontract.NewValidator(); err == nil && v.ValidateJSON("NativeReason", raw) == nil {
				return body.Reason
			}
		}
	}
	if errors.Is(err, ErrDraining) {
		return nativecontract.ReasonDraining
	}
	return nativecontract.ReasonNativeFailure
}

// Poll services every owner independently: one session's refused report,
// exit or command failure never starves another's input, final or stop. All
// failures are returned together.
func (h *NativeHost) Poll(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	errs := []error{h.settleAbandoned(ctx)}
	h.mu.Lock()
	entries := make([]*nativeOwned, 0, len(h.entries))
	for _, e := range h.entries {
		entries = append(entries, e)
	}
	h.mu.Unlock()
	for _, e := range entries {
		errs = append(errs, e.poll(ctx))
	}
	return errors.Join(errs...)
}

// poll services one owner. Once its report delivery is refused for good it no
// longer retries reports, events, finals or new input (their outcomes could not
// be reported), but it still reconciles its exit and executes the person's stop.
func (e *nativeOwned) poll(ctx context.Context) error {
	if err := e.opMu.LockContext(ctx); err != nil {
		return err
	}
	defer e.opMu.Unlock()
	transitioning := e.transition != nil
	e.mu.Lock()
	live := e.transport != nil && e.delivery != nil && !e.exited
	e.mu.Unlock()
	if !live {
		return nil
	}
	b := *e.grant.ExpectedBinding
	var errs []error
	if !transitioning && e.faulted() == nil {
		// The provider event loop flushes reports under eventMu too. The
		// journal permits one flusher; an HTTP completion is earlier than its
		// durable local report-offset update and cannot serve as that fence.
		e.eventMu.Lock()
		_, err := e.journal.FlushReports(ctx, b, 100, e)
		e.eventMu.Unlock()
		if err != nil {
			errs = append(errs, e.fault(err))
		}
	}
	if !transitioning {
		if err := e.reconcileExit(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	e.mu.Lock()
	exited := e.exited
	e.mu.Unlock()
	if exited {
		return errors.Join(errs...)
	}
	if !transitioning && e.faulted() == nil {
		if err := e.flushEvents(ctx); err != nil {
			errs = append(errs, e.fault(err))
		}
	}
	if !transitioning && e.faulted() == nil {
		for _, f := range e.journal.FinalStatuses() {
			if f.State == "pending" {
				if _, err := e.journal.RetryFinal(ctx, f.FinalID, e); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	var page nativecontract.PendingCommandPage
	if err := e.call(ctx, "GET", "/api/agentd/native-sessions/commands?limit=50", nil, &page); err != nil {
		return errors.Join(append(errs, err)...)
	}
	// A failed switch retains its exact handle. A new explicit owner stop
	// remains deliverable; it never retries the old takeover/hand-back write.
	stopOnly := transitioning || e.faulted() != nil
	for _, item := range page.Items {
		if !item.ExpectedBinding.Equal(b) {
			errs = append(errs, nativeRefusal(nativecontract.ReasonIdentityMismatch))
			continue
		}
		if stopOnly && item.Kind != "stop" {
			continue
		}
		if _, err := e.journal.Get(item.ExpectedBinding, item.OperationID); err == nil {
			continue
		}
		if _, err := e.executeLocked(ctx, item); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// fault marks report delivery refused for good after a definitive server
// refusal or the sticky local event fault; it returns err unchanged.
func (e *nativeOwned) fault(err error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.refused == nil && (nativeDefinitive(err) || e.lastError != nil && errors.Is(err, e.lastError)) {
		e.refused = err
	}
	return err
}
func (e *nativeOwned) faulted() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refused
}

// Reconcile only an exact retained stop and the owning provider's exit
// observer. A failed close is never retried as a second native write.
func (e *nativeOwned) reconcileExit(ctx context.Context) error {
	page, err := e.journal.Page(*e.grant.ExpectedBinding, "", 100)
	if err != nil {
		return err
	}
	if page.Truncated {
		return nativeRefusal(nativecontract.ReasonBufferFull)
	}
	for _, r := range page.Items {
		if r.Operation != "stop" || r.State != "uncertain" && r.State != "write_started" && r.State != "exit_confirmed" {
			continue
		}
		observer, ok := e.transport.(interface {
			ObserveExit(context.Context) (nativecontract.ExitEvidence, error)
		})
		if !ok {
			continue
		}
		x, err := observer.ObserveExit(ctx)
		if errors.Is(err, ErrProcessesUnconfirmed) {
			continue
		}
		if err != nil {
			return err
		}
		seq, err := strconv.ParseUint(r.SourceSequence, 10, 64)
		if err != nil {
			return err
		}
		if r.State != "exit_confirmed" {
			seq++
		}
		_, err = e.delivery.ConfirmExit(ctx, r.OperationID, seq, x)
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *nativeOwned) execute(ctx context.Context, item nativecontract.PendingCommand) (nativecontract.OperationResult, error) {
	if err := e.opMu.LockContext(ctx); err != nil {
		return nativecontract.OperationResult{}, err
	}
	defer e.opMu.Unlock()
	return e.executeLocked(ctx, item)
}

func (e *nativeOwned) executeLocked(ctx context.Context, item nativecontract.PendingCommand) (nativecontract.OperationResult, error) {
	var out nativecontract.OperationResult
	claim := nativecontract.Claim{RequestID: uuid.NewString(), OperationID: item.OperationID, DaemonID: e.host.s.daemonID, DaemonGeneration: e.host.s.generation, LedgerGeneration: e.host.s.ledgerBinding.Generation, CapabilityDigest: e.host.caps.QualificationDigest, ExpectedRevision: item.ExpectedBinding.BindingRevision, ClaimKind: "command"}
	var g nativecontract.ClaimResult
	if err := e.call(ctx, "POST", "/api/agentd/native-sessions/claim", claim, &g); err != nil {
		return out, err
	}
	if g.ExpectedBinding == nil || !g.ExpectedBinding.Equal(item.ExpectedBinding) || g.Command == nil || g.Command.Kind != item.Kind {
		return out, nativeRefusal(nativecontract.ReasonIdentityMismatch)
	}
	if g.CommandActor == nil || g.CommandActor.TenantID != item.ExpectedBinding.TenantID || g.CommandActor.OwnerPersonID != e.owner.OwnerPersonID {
		return out, nativeRefusal(nativecontract.ReasonWrongPrincipal)
	}
	if g.Command.Kind == "take" || g.Command.Kind == "return" {
		return e.switchRuntime(ctx, g)
	}
	e.mu.Lock()
	e.active = g
	e.mu.Unlock()
	actor := g.CommandActor.PrincipalID
	var r nativecontract.Receipt
	var err error
	switch g.Command.Kind {
	case "input":
		var in nativecontract.Input
		if json.Unmarshal(g.Command.Params, &in) != nil {
			return out, ErrScope
		}
		e.mu.Lock()
		now := e.host.s.capacityNow()
		for id, d := range e.drains {
			if !now.Before(d.grant.ExpiresAt) {
				delete(e.drains, id)
			}
		}
		// Only unfinished (live or uncertain) inputs count; the journal bounds
		// the routes of completed inputs whose final drain is still open.
		_, live := e.operations[in.InputID]
		_, routed := e.drains[in.InputID]
		if !live && len(e.operations) >= nativeLiveInputs || g.FinalDrainGrant != nil && !routed && len(e.drains) >= nativereceipts.MaxReceipts {
			e.mu.Unlock()
			return out, nativeRefusal(nativecontract.ReasonBufferFull)
		}
		e.operations[in.InputID] = item.OperationID
		if g.FinalDrainGrant != nil {
			e.drains[in.InputID] = nativeDrain{grant: *g.FinalDrainGrant, operation: item.OperationID}
		}
		e.mu.Unlock()
		r, err = e.delivery.Send(ctx, actor, item.OperationID, in)
		if nativeTerminal(r.State) {
			e.retireInput(in.InputID)
		}
	case "interrupt":
		var in nativecontract.Interrupt
		if json.Unmarshal(g.Command.Params, &in) != nil {
			return out, ErrScope
		}
		r, err = e.delivery.Interrupt(ctx, actor, item.OperationID, in)
	case "stop":
		var in nativecontract.Stop
		if json.Unmarshal(g.Command.Params, &in) != nil {
			return out, ErrScope
		}
		r, err = e.delivery.Close(ctx, actor, item.OperationID, in, uint64(1))
	default:
		err = ErrUnsupported
	}
	out.Receipt = r
	return out, err
}
func (e *nativeOwned) events(ctx context.Context) {
	e.mu.Lock()
	transport := e.transport
	e.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-transport.Events():
			if !ok {
				return
			}
			if event.Final != nil && len(event.Final.Text) > nativereceipts.MaxTextBytes {
				e.mu.Lock()
				e.lastError = nativeRefusal(nativecontract.ReasonBufferFull)
				e.mu.Unlock()
				return
			}
			e.eventMu.Lock()
			if len(e.pendingEvents) >= 128 {
				e.eventMu.Unlock()
				e.mu.Lock()
				e.lastError = nativeRefusal(nativecontract.ReasonBufferFull)
				e.mu.Unlock()
				return
			}
			e.pendingEvents = append(e.pendingEvents, event)
			e.eventMu.Unlock()
			// A failed HTTP report retains this bounded transient observation. Poll
			// retries metadata/outbox delivery without replaying input into the provider.
			_ = e.flushEvents(ctx)
		}
	}
}

// nativeTerminal reports a journal state that ends an input's live work.
func nativeTerminal(state string) bool {
	return state == "turn_completed" || state == "rejected" || state == "expired"
}

// retireInput removes a finished input from the live set; its final route,
// if any, stays in drains until the grant expires.
func (e *nativeOwned) retireInput(id string) {
	e.mu.Lock()
	delete(e.operations, id)
	e.mu.Unlock()
}

func (e *nativeOwned) flushEvents(ctx context.Context) error {
	e.eventMu.Lock()
	defer e.eventMu.Unlock()
	for len(e.pendingEvents) > 0 {
		event := e.pendingEvents[0]
		e.mu.Lock()
		operation := ""
		if event.Receipt != nil && event.Receipt.InputID != nil {
			operation = e.operations[*event.Receipt.InputID]
		}
		var grant *nativecontract.FinalDrainGrant
		if event.Final != nil {
			// A final follows its completion, which already left the live set.
			if d, ok := e.drains[event.Final.InputID]; ok {
				operation, grant = d.operation, &d.grant
			}
		}
		e.mu.Unlock()
		var err error
		if operation != "" && event.Receipt != nil {
			ev := nativecontract.NativeEvidence{Stage: event.Receipt.State, RequestID: event.Receipt.RequestID, SourceSequence: event.SourceSequence, ObservedAt: e.host.s.capacityNow()}
			if event.Receipt.InputID != nil {
				ev.InputID = *event.Receipt.InputID
			}
			if event.Receipt.TurnRef != nil {
				ev.TurnRef = *event.Receipt.TurnRef
			}
			e.mu.Lock()
			e.observations[ev.Stage+"/"+ev.RequestID] = ev
			e.mu.Unlock()
			var r nativecontract.Receipt
			r, err = e.delivery.Observe(ctx, operation, event.Identity, ev)
			if nativeTerminal(r.State) && ev.InputID != "" {
				e.retireInput(ev.InputID)
			}
		}
		if operation != "" && event.Final != nil && grant != nil {
			f := *event.Final
			f.DrainGrantID = grant.GrantID
			_, err = e.delivery.Final(ctx, operation, f)
		}
		if err != nil {
			return err
		}
		e.pendingEvents[0] = nativecontract.ProviderEvent{}
		e.pendingEvents = e.pendingEvents[1:]
	}
	e.mu.Lock()
	err := e.lastError
	e.mu.Unlock()
	return err
}

// Local commands select an already person-authorized pending command; daemon
// socket possession never creates a person command or expands its key ceiling.
func (h *NativeHost) localCommand(ctx context.Context, b nativecontract.Binding, request, kind string, value any) (nativecontract.OperationResult, error) {
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	schema := map[string]string{"input": "NativeInput", "interrupt": "NativeInterrupt", "stop": "NativeStop", "take": "NativeTakeIntent", "return": "NativeReturnIntent"}[kind]
	if schema == "" {
		return nativecontract.OperationResult{}, ErrUnsupported
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nativecontract.OperationResult{}, err
	}
	if _, err = nativestream.Encode(v, schema, value); err != nil {
		return nativecontract.OperationResult{}, err
	}
	h.mu.Lock()
	var e *nativeOwned
	for _, candidate := range h.entries {
		if candidate.grant.ExpectedBinding != nil && candidate.grant.ExpectedBinding.Equal(b) {
			e = candidate
			break
		}
	}
	h.mu.Unlock()
	if e == nil {
		return nativecontract.OperationResult{}, ErrNotOwned
	}
	if refused := e.faulted(); refused != nil && kind != "stop" {
		return nativecontract.OperationResult{}, refused // As Poll: its outcome could not be reported.
	}
	var page nativecontract.PendingCommandPage
	if err := e.call(ctx, "GET", "/api/agentd/native-sessions/commands?limit=50", nil, &page); err != nil {
		return nativecontract.OperationResult{}, err
	}
	for _, item := range page.Items {
		if item.RequestID == request && item.Kind == kind {
			claim := nativecontract.Claim{RequestID: uuid.NewString(), OperationID: item.OperationID, DaemonID: h.s.daemonID, DaemonGeneration: h.s.generation, LedgerGeneration: h.s.ledgerBinding.Generation, CapabilityDigest: h.caps.QualificationDigest, ExpectedRevision: b.BindingRevision, ClaimKind: "command"}
			var current nativecontract.ClaimResult
			if err := e.call(ctx, "POST", "/api/agentd/native-sessions/claim", claim, &current); err != nil {
				return nativecontract.OperationResult{}, err
			}
			expected, _ := json.Marshal(value)
			if current.Command == nil || !bytes.Equal(bytes.TrimSpace(current.Command.Params), bytes.TrimSpace(expected)) {
				return nativecontract.OperationResult{}, nativeRefusal(nativecontract.ReasonIdentityMismatch)
			}
			out, err := e.execute(ctx, item)
			if err == nil && (kind == "take" || kind == "return") {
				out = localTransitionReceipt(out, b)
			}
			return out, err
		}
	}
	return nativecontract.OperationResult{}, nativeRefusal(nativecontract.ReasonGrantMissing)
}
func (h *NativeHost) Input(ctx context.Context, in nativecontract.Input) (nativecontract.OperationResult, error) {
	return h.localCommand(ctx, in.ExpectedBinding, in.RequestID, "input", in)
}
func (h *NativeHost) Interrupt(ctx context.Context, in nativecontract.Interrupt) (nativecontract.OperationResult, error) {
	return h.localCommand(ctx, in.ExpectedBinding, in.RequestID, "interrupt", in)
}
func (h *NativeHost) Stop(ctx context.Context, in nativecontract.Stop) (nativecontract.OperationResult, error) {
	return h.localCommand(ctx, in.ExpectedBinding, in.RequestID, "stop", in)
}
func (h *NativeHost) Close(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.entries {
		e.mu.Lock()
		live := e.transport != nil && !e.exited
		e.mu.Unlock()
		if live {
			return ErrProcessesUnconfirmed
		}
	}
	return nil
}
func (h *NativeHost) Resume(ctx context.Context, in nativecontract.ResumeIntent) (nativecontract.OperationResult, error) {
	return h.resumeOwned(ctx, in)
}
func (h *NativeHost) Attach(context.Context, nativecontract.AttachIntent) (nativecontract.AttachResult, error) {
	return nativecontract.AttachResult{}, ErrUnsupported
}
func (h *NativeHost) Renew(context.Context, nativecontract.AttachRenew) (nativecontract.AttachResult, error) {
	return nativecontract.AttachResult{}, ErrUnsupported
}
func (h *NativeHost) Detach(context.Context, nativecontract.Detach) (nativecontract.OperationResult, error) {
	return nativecontract.OperationResult{}, ErrUnsupported
}
func (h *NativeHost) Take(ctx context.Context, in nativecontract.TakeIntent) (nativecontract.OperationResult, error) {
	return h.localCommand(ctx, in.ExpectedBinding, in.RequestID, "take", in)
}
func (h *NativeHost) Return(ctx context.Context, in nativecontract.ReturnIntent) (nativecontract.OperationResult, error) {
	return h.localCommand(ctx, in.ExpectedBinding, in.RequestID, "return", in)
}
func (h *NativeHost) PTYInput(context.Context, nativecontract.PTYInput) (nativecontract.OperationResult, error) {
	return nativecontract.OperationResult{}, ErrUnsupported
}
func (h *NativeHost) Resize(context.Context, nativecontract.Resize) (nativecontract.OperationResult, error) {
	return nativecontract.OperationResult{}, ErrUnsupported
}
func (h *NativeHost) Decide(context.Context, nativecontract.ApprovalDecision) (nativecontract.OperationResult, error) {
	return nativecontract.OperationResult{}, ErrUnsupported
}
func (h *NativeHost) EndControl(context.Context, nativecontract.EndControl) (nativecontract.OperationResult, error) {
	return nativecontract.OperationResult{}, ErrUnsupported
}
func (h *NativeHost) EventsPage(context.Context, nativecontract.Binding, nativecontract.EventQuery) (nativecontract.EventPage, error) {
	return nativecontract.EventPage{}, ErrUnsupported
}
func (h *NativeHost) Snapshot(context.Context, nativecontract.Binding) (nativecontract.SessionView, error) {
	return nativecontract.SessionView{}, ErrUnsupported
}
func (h *NativeHost) Receipts(context.Context, nativecontract.Binding, nativecontract.PageQuery) (nativecontract.ReceiptPage, error) {
	return nativecontract.ReceiptPage{}, ErrUnsupported
}
func (h *NativeHost) Subscribe(ctx context.Context, b nativecontract.Binding, q nativecontract.StreamQuery) (nativecontract.Subscription, error) {
	return h.localSubscription(ctx, b, q)
}

var _ nativecontract.Host = (*NativeHost)(nil)
var _ nativecontract.Launcher = (*NativeHost)(nil)
