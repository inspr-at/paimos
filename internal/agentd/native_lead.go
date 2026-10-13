// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

var ErrNativeUnqualified = errors.New("native_coordinator_unqualified")

// NativeLeadGrant is an immutable root-coordinator grant supplied by the
// server-intent owner. It is distinct from a leaf run or an automatic routine.
// A profile name, local socket token or vendor login cannot manufacture it.
type NativeLeadGrant struct {
	TenantID, PrincipalID, OwnerID, ProjectID, AccountID string
	SessionID, DaemonID, Generation, PolicyDigest        string
	AccountHomeDigest                                    string
	WorkspaceDigest, ResumeDigest                        string
	Revision                                             int64
	ExpiresAt                                            time.Time
	HistoryApproved                                      bool
	AllowedTools                                         []string
}

var nativeLeadTools = []string{"aeon_comment", "aeon_status", "aeon_check_criterion", "aeon_evidence", "aeon_request_approval", "aeon_reply", "aeon_terminal", "aeon_read", "aeon_write", "aeon_edit", "aeon_glob", "aeon_grep", "aeon_activity"}

func (g NativeLeadGrant) valid(now time.Time) bool {
	for _, id := range []string{g.TenantID, g.PrincipalID, g.OwnerID, g.ProjectID, g.AccountID, g.SessionID} {
		if !uuidPattern.MatchString(id) {
			return false
		}
	}
	if !validChatID(g.DaemonID, 128) || !validChatID(g.Generation, 128) || !agentsetup.RoutineDigest(g.PolicyDigest) || !agentsetup.RoutineDigest(g.AccountHomeDigest) || !agentsetup.RoutineDigest(g.WorkspaceDigest) || g.ResumeDigest != "" && !agentsetup.RoutineDigest(g.ResumeDigest) || g.Revision < 1 || !g.HistoryApproved || !now.Before(g.ExpiresAt) || g.ExpiresAt.Sub(now) > 24*time.Hour || len(g.AllowedTools) > len(nativeLeadTools) {
		return false
	}
	seen := map[string]bool{}
	for _, tool := range g.AllowedTools {
		if !slices.Contains(nativeLeadTools, tool) || seen[tool] {
			return false
		}
		seen[tool] = true
	}
	return true
}

// NativeLeadSpec contains bound ports, never an API key. Require revalidates
// current actor/grant authority; BeforeInput persists a content-free attempt
// before a possible SDK write. Tool effects still authorize inside their own
// final broker transaction. P1b/I1 supplies these ports after qualification.
type NativeLeadSpec struct {
	Grant       NativeLeadGrant
	Resume      string
	Prompt      string
	Rules       string
	Model       string
	Effort      string
	Tools       *RunTools
	Require     func(context.Context) error
	BeforeInput func(context.Context, string, string) error
	Observe     func(AdapterEvent)
	Now         func() time.Time
}

func (n *NativeLeadSpec) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if n == nil || n.Require == nil || n.BeforeInput == nil || n.Observe == nil {
		return ErrNativeUnqualified
	}
	now := time.Now()
	if n.Now != nil {
		now = n.Now()
	}
	if !n.Grant.valid(now) {
		return ErrNativeUnqualified
	}
	return n.Require(ctx)
}

func (n *NativeLeadSpec) bridgeGrant() any {
	tools := append([]string{}, n.Grant.AllowedTools...)
	return struct {
		SessionID string   `json:"session_id"`
		Policy    string   `json:"policy_digest"`
		ExpiresAt string   `json:"expires_at"`
		History   bool     `json:"history_approved"`
		Tools     []string `json:"allowed_tools"`
	}{n.Grant.SessionID, n.Grant.PolicyDigest, n.Grant.ExpiresAt.UTC().Format(time.RFC3339Nano), true, tools}
}

// Authentication remains in the user's unchanged CLI and selected config dir.
// Preserve the login HOME for the keychain, including non-default profiles.
func nativeClaudeEnvironment(home, nodePath, claudePath string) ([]string, error) {
	env := claudeEnvironment(home, nodePath, claudePath)
	userHome, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	for i, entry := range env {
		if strings.HasPrefix(entry, "HOME=") {
			env[i] = "HOME=" + userHome
		}
	}
	return env, nil
}

// Implementing the coordinator seam never qualifies ordinary routine execution.
func (*ClaudeAdapter) RoutineExecutionSupported(agentsetup.RoutineGrant) bool { return false }

func (a *ClaudeAdapter) StartLead(ctx context.Context, r LeadStartRequest) (Process, error) {
	n := r.Native
	if err := n.check(ctx); err != nil {
		return nil, err
	}
	if r.Session.AttachedHook || r.Session.ID != n.Grant.SessionID || r.Session.ProjectID != n.Grant.ProjectID || r.Session.Harness != Claude || len(r.Session.Lease) < 16 || len(r.ResourceEnvironment) != 0 || r.Lifetime == nil || r.Lifetime.Err() != nil || n.Resume != "" && !uuidPattern.MatchString(n.Resume) || len(n.Prompt) == 0 || len(n.Prompt) > 256<<10 || strings.ContainsRune(n.Prompt, 0) || len(n.Rules) > 512000 || !validDispatchTuple(n.Model, n.Effort) {
		return nil, ErrScope
	}
	home, err := localHome(a.Homes, r.AccountKey)
	if err != nil {
		return nil, err
	}
	if nativeDigest(home) != n.Grant.AccountHomeDigest {
		return nil, ErrScope
	}
	if nativeDigest(r.Workspace) != n.Grant.WorkspaceDigest || n.Resume == "" && n.Grant.ResumeDigest != "" || n.Resume != "" && nativeDigest(n.Resume) != n.Grant.ResumeDigest {
		return nil, ErrScope
	}
	physical, err := filepath.EvalSymlinks(r.Workspace)
	if err != nil || physical != r.Workspace {
		return nil, ErrScope
	}
	if n.Tools == nil && len(n.Grant.AllowedTools) != 0 || n.Tools != nil && (n.Tools.Routine || n.Tools.BrowserCaptureOnly) {
		return nil, ErrNativeUnqualified
	}
	bound := *n
	bound.Grant.AllowedTools = slices.Clone(n.Grant.AllowedTools)
	if n.Tools != nil {
		tools := *n.Tools
		bound.Tools = &tools
	}
	request := StartRequest{Workspace: r.Workspace, AccountKey: r.AccountKey, Lifetime: r.Lifetime, Profile: Profile{Harness: Claude, Model: n.Model, Effort: n.Effort}, Prompt: n.Prompt, Rules: n.Rules, Tools: bound.Tools, ManagedPolicy: true, InboxEnabled: true, Capabilities: []string{"inbox", "interrupt", "stop", "chat"}}
	process, err := a.startBridge(ctx, request, bound.Observe, &bound)
	if err != nil {
		return nil, err
	}
	cp, ok := process.(*claudeProcess)
	if !ok {
		return nil, ErrProcessesUnconfirmed
	}
	cp.identity.DaemonID, cp.identity.Generation = n.Grant.DaemonID, n.Grant.Generation
	lead := &nativeClaudeLead{claudeProcess: cp, io: r.SessionIO, controls: map[string]nativeControlResult{}}
	// Cancelling startup alone never closes this accepted lifetime. Cancellation
	// of the bound lifetime sends close to this owned bridge, with no PID signal.
	context.AfterFunc(r.Lifetime, func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = cp.sendContext(closeCtx, map[string]any{"op": "stop", "correlation_id": "lifetime-close"})
	})
	return lead, nil
}

func validDispatchTuple(model, effort string) bool {
	if !validChatID(model, 128) || !slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, effort) {
		return false
	}
	for _, c := range model {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

func nativeDigest(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}

type nativeControlResult struct{ digest, outcome, reason string }
type nativeClaudeLead struct {
	*claudeProcess
	serviceMu sync.Mutex
	io        LeadSessionIO
	controls  map[string]nativeControlResult
	delivery  *HarnessDelivery
}

func (p *nativeClaudeLead) Control(ctx context.Context, op, text string) error {
	id, err := randomID()
	if err != nil {
		return err
	}
	if op == "inbox" {
		return p.Input(ctx, id, text)
	}
	if op != "interrupt" && op != "stop" {
		return ErrUnsupported
	}
	return p.nativeControl(ctx, id, op)
}

func (p *nativeClaudeLead) nativeControl(ctx context.Context, id, op string) error {
	if err := p.native.check(ctx); err != nil {
		return err
	}
	attemptID := id
	if op == "stop" {
		attemptID = "native-stop"
	} // One reserved, session-bound close attempt.
	if err := p.native.BeforeInput(ctx, attemptID, nativeDigest(op)); err != nil {
		return err
	}
	return p.controlCorrelated(ctx, op, "", id)
}

func (p *nativeClaudeLead) Input(ctx context.Context, id, text string) error {
	if !validChatID(id, 128) || id == "native-stop" || len(text) == 0 || len(text) > inboxTextLimit || strings.ContainsRune(text, 0) {
		return ErrScope
	}
	if err := p.native.check(ctx); err != nil {
		return err
	}
	if err := p.native.BeforeInput(ctx, id, nativeDigest(text)); err != nil {
		return err
	}
	return p.controlCorrelated(ctx, "inbox", text, id)
}

// Stop confirms owned group exit after SDK close, not just the close receipt.
func (p *nativeClaudeLead) Stop(ctx context.Context) error {
	if p.ProcessExited() {
		return p.cleanupResult()
	}
	if err := p.Control(ctx, "stop", ""); err != nil {
		return err
	}
	select {
	case <-p.waitDone:
		return p.cleanupResult()
	case <-ctx.Done():
		return ErrGracefulTimeout
	}
}

func (p *nativeClaudeLead) RoutineFixturesExited() bool {
	return p.ProcessExited() && p.cleanupResult() == nil
}
func (*ClaudeAdapter) LeadExitConfirmed(process Process) bool {
	p, ok := process.(*nativeClaudeLead)
	return ok && p.RoutineFixturesExited()
}

func (*ClaudeAdapter) ServiceLead(ctx context.Context, process Process, pause *HarnessPause) error {
	p, ok := process.(*nativeClaudeLead)
	if !ok {
		return ErrNativeUnqualified
	}
	p.serviceMu.Lock()
	defer p.serviceMu.Unlock()
	if err := p.native.check(ctx); err != nil {
		return err
	}
	if pause != nil {
		return ErrUnsupported
	} // Cooperative pause qualification is separate.
	if p.io.Yield == nil || p.io.CompleteControl == nil || p.io.Drain == nil || p.io.CompleteDelivery == nil {
		return ErrNativeUnqualified
	}
	controls, err := p.io.Yield(ctx)
	if err != nil {
		return err
	}
	if len(controls) > 32 {
		return ErrScope
	}
	for _, control := range controls {
		if !uuidPattern.MatchString(control.ID) || control.ExpectedGeneration != p.native.Grant.SessionID || control.ExpiresAt == nil || !time.Now().Before(*control.ExpiresAt) || control.ExpiresInMS < 1 || control.ExpiresInMS > 120000 {
			return ErrScope
		}
		if expected := control.ExpectedOwnership; expected != nil {
			actual := p.identity
			if expected.DaemonID != actual.DaemonID || expected.Generation != actual.Generation || expected.ProcessID != actual.ProcessID || expected.RootPID != actual.RootPID || expected.GroupID != actual.GroupID || !expected.StartedAt.Equal(actual.StartedAt) {
				return ErrNotOwned
			}
		}
		digest := nativeDigest(control.Kind + "\x00" + control.Text)
		result, attempted := p.controls[control.ID]
		if attempted && result.digest != digest {
			return ErrReplay
		}
		if !attempted {
			if len(p.controls) >= 256 {
				return ErrControlUnconfirmed
			}
			result = nativeControlResult{digest: digest, outcome: "rejected", reason: "unsupported"}
			p.controls[control.ID] = result // An uncertain native operation is never repeated.
			if control.Kind == "interrupt" || control.Kind == "stop" {
				deadline := time.Now().Add(time.Duration(control.ExpiresInMS) * time.Millisecond)
				if control.ExpiresAt.Before(deadline) {
					deadline = *control.ExpiresAt
				}
				operation, cancel := context.WithDeadline(ctx, deadline)
				err = p.nativeControl(operation, control.ID, control.Kind)
				if err == nil && control.Kind == "stop" {
					select {
					case <-p.waitDone:
						err = p.cleanupResult()
					case <-operation.Done():
						err = ErrGracefulTimeout
					}
				}
				cancel()
				result.reason = "native_control_unconfirmed"
				if err == nil {
					result.outcome, result.reason = "applied", ""
				}
				p.controls[control.ID] = result
			}
		}
		if err := p.io.CompleteControl(ctx, control.ID, result.outcome, result.reason); err != nil {
			return err
		}
	}
	if p.delivery != nil {
		if err := p.io.CompleteDelivery(ctx, *p.delivery); err != nil {
			return err
		}
		p.delivery = nil
	}
	deliveries, err := p.io.Drain(ctx)
	if err != nil {
		return err
	}
	if len(deliveries) > 1 {
		return ErrScope
	}
	for _, delivery := range deliveries {
		if !uuidPattern.MatchString(delivery.ID) || !uuidPattern.MatchString(delivery.MessageID) || len(delivery.Body) > chatChunkBytes || delivery.ProjectID != "" && delivery.ProjectID != p.native.Grant.ProjectID {
			return ErrScope
		}
		delivery.Outcome, delivery.FailureReason = "delivered", ""
		if err := p.Input(ctx, delivery.ID, chatInputText(delivery.MessageID, delivery.Body)); err != nil {
			delivery.Outcome, delivery.FailureReason = "failed", "native_input_unconfirmed"
		}
		p.delivery = &delivery
		if err := p.io.CompleteDelivery(ctx, delivery); err != nil {
			return err
		}
		p.delivery = nil
	}
	return nil
}

var _ LeadRuntimeAdapter = (*ClaudeAdapter)(nil)
