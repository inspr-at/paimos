// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/client"
)

// These narrow projections deliberately omit owner identity, local paths and
// early-recovery intent: checking usage grants no execution authority.
type CapacityCheckRequest struct {
	ID               string    `json:"id"`
	AccountID        string    `json:"account_id"`
	BindingRevision  int64     `json:"binding_revision"`
	DaemonGeneration *string   `json:"daemon_generation"`
	State            string    `json:"state"`
	ExpiresAt        time.Time `json:"expires_at"`
}
type CapacityResource struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	BindingRevision int64  `json:"binding_revision"`
}
type CapacityCheckAccount struct {
	ID                 string                `json:"id"`
	AccountKey         string                `json:"account_key"`
	Harness            string                `json:"harness"`
	DaemonID           string                `json:"daemon_id"`
	LinkRevision       int64                 `json:"link_revision"`
	OngoingUseApproved bool                  `json:"ongoing_use_approved"`
	State              string                `json:"state"`
	PendingCheck       *CapacityCheckRequest `json:"pending_check,omitempty"`
	Resources          []CapacityResource    `json:"readiness_resources,omitempty"`
}
type CapacityCheckFact struct {
	ResourceID   string     `json:"resource_id"`
	WindowKey    string     `json:"window_key"`
	Source       string     `json:"source"`
	ObservedAt   time.Time  `json:"observed_at"`
	ResetsAt     *time.Time `json:"resets_at"`
	ReadingAt    *time.Time `json:"reading_at"`
	UsedPercent  *float64   `json:"used_percent"`
	CreditState  string     `json:"credit_state"`
	Remaining    *float64   `json:"remaining"`
	StopKind     string     `json:"stop_kind"`
	DenialReason string     `json:"denial_reason"`
}
type CapacityCheckReport struct {
	CheckID         string              `json:"check_id,omitempty"`
	BindingRevision int64               `json:"binding_revision"`
	Result          string              `json:"result"`
	Facts           []CapacityCheckFact `json:"facts,omitempty"`
}
type capacityChecksAPI interface {
	PollCapacityChecks(context.Context) ([]CapacityCheckAccount, error)
	CapacityCheckHeartbeat(context.Context, string, string, string) error
	ReportCapacityCheck(context.Context, string, string, string, ProbeStatus, CapacityCheckReport) error
}

func (r *Remote) CapacityCheckHeartbeat(ctx context.Context, account, daemon, generation string) error {
	return r.capacityCheckJSON(ctx, http.MethodPost, "/api/agent-accounts/"+url.PathEscape(account)+"/probe", map[string]any{
		"daemon_id": daemon, "daemon_generation": generation, "available": false, "measurement_only": true,
	}, nil)
}

func (r *Remote) PollCapacityChecks(ctx context.Context) ([]CapacityCheckAccount, error) {
	var accounts []CapacityCheckAccount
	// The legacy account route is a bounded array. Decode only the small check
	// projection; cap bytes before allocating/decode and reject partial lists.
	if err := r.capacityCheckJSON(ctx, http.MethodGet, "/api/agent-accounts?include_checks=true", nil, &accounts); err != nil {
		return nil, err
	}
	if len(accounts) > 4096 {
		return nil, errors.New("capacity check account limit")
	}
	for _, a := range accounts {
		if len(a.Resources) > 16 {
			return nil, errors.New("capacity check resource limit")
		}
	}
	return accounts, nil
}

func boundedCapacityResult(result string) bool {
	switch result {
	case "success", "unsupported", "timeout", "protocol", "launch_failed", "identity_mismatch", "authentication_failed":
		return true
	}
	return false
}

func (r *Remote) ReportCapacityCheck(ctx context.Context, account, daemon, generation string, status ProbeStatus, report CapacityCheckReport) error {
	if !boundedCapacityResult(report.Result) || report.BindingRevision < 0 || len(report.Facts) > 32 || report.Result != "success" && len(report.Facts) > 0 {
		return errors.New("invalid capacity check report")
	}
	body := map[string]any{"daemon_id": daemon, "daemon_generation": generation, "available": status.OK, "readiness": report, "measurement_only": true}
	if !status.OK {
		body["failure"] = ProbeUnavailable
		if status.Failure == ProbeAuthFailed {
			body["failure"] = ProbeAuthFailed
		}
	}
	if status.OpenRouterCredits != nil {
		body["openrouter_credits"] = status.OpenRouterCredits
	}
	// Never retry without readiness: that would discard the fence or cause.
	return r.capacityCheckJSON(ctx, http.MethodPost, "/api/agent-accounts/"+url.PathEscape(account)+"/probe", body, nil)
}

func (r *Remote) capacityCheckJSON(ctx context.Context, method, path string, body, dest any) error {
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil || len(raw) > 64<<10 {
			return errors.New("capacity check request bound")
		}
		input = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(op, method, r.Client.BaseURL+path, input)
	if err != nil {
		return errors.New("capacity check request unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+r.Client.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := *r.Client.HTTP
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := hc.Do(req)
	if err != nil {
		return errors.New("capacity check transport unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &client.StatusError{Status: res.StatusCode}
	}
	if dest == nil {
		return nil
	}
	const maximum = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(res.Body, maximum+1))
	if err != nil || len(raw) > maximum {
		return errors.New("capacity check response bound")
	}
	if json.Unmarshal(raw, dest) != nil {
		return errors.New("capacity check response malformed")
	}
	return nil
}

// Retry state and every pending completion survive restarts. No vendor
// payload, credential, owner identity or execution permit is stored here.
type capacityCheckState struct {
	Revision           int64                    `json:"binding_revision"`
	Failures           int                      `json:"failures"`
	NextAttempt        time.Time                `json:"next_attempt_at"`
	RefreshAt          time.Time                `json:"refresh_at"`
	LastResult         string                   `json:"last_result"`
	HandledCheck       string                   `json:"handled_check,omitempty"`
	Pending            *capacityCheckCompletion `json:"pending,omitempty"`
	CleanupUnconfirmed bool                     `json:"cleanup_unconfirmed,omitempty"`
}
type capacityCheckCompletion struct {
	Generation string              `json:"generation"`
	Report     CapacityCheckReport `json:"report"`
}
type capacityChecksState struct {
	TenantID    string                        `json:"tenant_id"`
	PrincipalID string                        `json:"principal_id"`
	Accounts    map[string]capacityCheckState `json:"accounts"`
}

func (s *Supervisor) capacityChecksName() string { return "aeon-agentd-" + s.daemonID + ".checks.json" }
func (s *Supervisor) loadCapacityChecks() error {
	s.capacityChecks = map[string]capacityCheckState{}
	s.capacityCheckHeartbeat = map[string]bool{}
	s.capacityCheckConnected = map[string]bool{}
	s.capacityCheckRefresh = map[string]bool{}
	raw, err := s.state.Read(s.capacityChecksName(), 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved capacityChecksState
	if json.Unmarshal(raw, &saved) != nil || saved.TenantID != s.tenantID || saved.PrincipalID != s.principalID || len(saved.Accounts) > 4096 {
		return errors.New("invalid capacity check state binding")
	}
	for id, v := range saved.Accounts {
		if v.Revision < 0 || v.Failures < 0 || v.Failures > 1000000 || v.LastResult != "" && !boundedCapacityResult(v.LastResult) || v.Pending != nil && (!boundedCapacityResult(v.Pending.Report.Result) || len(v.Pending.Report.Facts) > 32) {
			return errors.New("invalid capacity retry state")
		}
		if v.CleanupUnconfirmed {
			s.capacityCapturing = true
			s.capacityAccountID = id
		}
		s.capacityChecks[id] = v
		if v.LastResult == "identity_mismatch" || v.LastResult == "authentication_failed" {
			s.blockedAccounts[id] = true
		}
	}
	return nil
}
func (s *Supervisor) saveCapacityChecksLocked() error {
	if s.state == nil {
		return errors.New("capacity retry store unavailable")
	}
	raw, err := json.Marshal(capacityChecksState{s.tenantID, s.principalID, s.capacityChecks})
	if err != nil || len(raw) > 1<<20 {
		return errors.New("capacity retry state bound")
	}
	return s.state.Write(s.capacityChecksName(), raw, false)
}
func checkRetry(failures int) time.Duration {
	switch failures {
	case 1:
		return time.Minute
	case 2:
		return 2 * time.Minute
	case 3:
		return 4 * time.Minute
	default:
		return 30 * time.Minute
	}
}

// Wake only on a first connection or a reconnect. This does not reset retries
// or mint a check; regular polling can never bypass persisted failure backoff.
func (s *Supervisor) capacityProbeConnection(id string, accepted bool) {
	s.mu.Lock()
	if s.capacityCheckConnected == nil {
		s.capacityCheckConnected = map[string]bool{}
	}
	if s.capacityCheckRefresh == nil {
		s.capacityCheckRefresh = map[string]bool{}
	}
	wake := accepted && !s.capacityCheckConnected[id]
	s.capacityCheckConnected[id] = accepted
	if wake {
		s.capacityCheckRefresh[id] = true
	}
	s.mu.Unlock()
	if wake {
		select {
		case s.capacityWake <- struct{}{}:
		default:
		}
	}
}

func currentCheckAccount(local EnrolledAccount, remote CapacityCheckAccount, daemon string) bool {
	return local.ID == remote.ID && local.Key == remote.AccountKey && local.Harness == remote.Harness &&
		remote.DaemonID == daemon && remote.OngoingUseApproved && remote.State == "available" && !local.DependencyBlocked && remote.LinkRevision >= 0 && len(remote.Resources) > 0 && len(remote.Resources) <= 16
}
func checkRequestCurrent(c *CapacityCheckRequest, account CapacityCheckAccount, generation string, now time.Time) bool {
	return c != nil && c.ID != "" && c.AccountID == account.ID && c.BindingRevision == account.LinkRevision &&
		c.State == "pending" && now.Before(c.ExpiresAt) && (c.DaemonGeneration == nil || *c.DaemonGeneration == generation)
}

func (s *Supervisor) captureCapacityChecks(ctx context.Context, now time.Time) {
	api, ok := s.api.(capacityChecksAPI)
	if !ok || !s.capacityCheckMu.TryLock() {
		return
	}
	defer s.capacityCheckMu.Unlock()
	accounts, err := api.PollCapacityChecks(ctx)
	if err != nil {
		logCapacityError("capacity check poll failed", "", err)
		return
	}
	if len(accounts) > 4096 {
		return
	}
	byID := map[string]CapacityCheckAccount{}
	for _, a := range accounts {
		byID[a.ID] = a
	}
	s.mu.Lock()
	locals := append([]EnrolledAccount(nil), s.accounts...)
	s.mu.Unlock()
	sort.Slice(locals, func(i, j int) bool { return locals[i].ID < locals[j].ID })
	for _, local := range locals {
		if ctx.Err() != nil {
			return
		}
		account, exists := byID[local.ID]
		if !exists || !currentCheckAccount(local, account, s.daemonID) || !s.dispatchAllowed(local.ID) {
			continue
		}
		s.mu.Lock()
		adapter := s.adapters[local.Harness]
		lastResult := s.capacityChecks[local.ID].LastResult
		identityFailure := lastResult == "identity_mismatch" || lastResult == "authentication_failed"
		available := s.probedAccounts[local.ID] && (!s.blockedAccounts[local.ID] || identityFailure)
		heartbeat := s.capacityCheckHeartbeat[local.ID]
		replay := s.capacityChecks[local.ID].Pending != nil
		s.mu.Unlock()
		if adapter == nil || !available && !replay {
			continue
		}
		if !heartbeat {
			// Commit this generation without a check first; package A intentionally
			// rolls the whole probe back when an old-generation check accompanies it.
			if err := api.CapacityCheckHeartbeat(ctx, local.ID, s.daemonID, s.generation); err != nil {
				logCapacityError("capacity restart heartbeat failed", local.ID, err)
				continue
			}
			s.mu.Lock()
			s.capacityCheckHeartbeat[local.ID] = true
			s.mu.Unlock()
			// Fetch again: the heartbeat invalidates previous-generation requests.
			latest, err := api.PollCapacityChecks(ctx)
			if err != nil {
				continue
			}
			exists = false
			for _, a := range latest {
				if a.ID == local.ID {
					account = a
					exists = true
					break
				}
			}
			if !exists || !currentCheckAccount(local, account, s.daemonID) {
				continue
			}
		}
		s.captureCapacityCheck(ctx, now, local, account, adapter, api)
	}
}

func (s *Supervisor) captureCapacityCheck(ctx context.Context, now time.Time, local EnrolledAccount, account CapacityCheckAccount, adapter Adapter, api capacityChecksAPI) {
	s.mu.Lock()
	saved, known := s.capacityChecks[local.ID]
	if !known || saved.Revision != account.LinkRevision {
		saved = capacityCheckState{Revision: account.LinkRevision}
	}
	manual := checkRequestCurrent(account.PendingCheck, account, s.generation, now) && saved.HandledCheck != account.PendingCheck.ID
	refresh := s.capacityCheckRefresh[local.ID]
	// Manual requests belong to their original generation/deadline. Automatic
	// observations remain useful after restart while the binding is unchanged.
	if saved.Pending != nil && !capacityCompletionCurrent(*saved.Pending, account, s.generation, now) {
		saved.Pending = nil
	}
	pending := saved.Pending
	last := s.capacityLast[local.ID]
	due := manual || pending != nil || (saved.LastResult != "unsupported" && !now.Before(saved.NextAttempt) && (saved.LastResult == "" || saved.Failures > 0)) || (saved.Failures == 0 && saved.LastResult != "unsupported" && (refresh || !saved.RefreshAt.IsZero() && !now.Before(saved.RefreshAt) || !now.Before(saved.NextAttempt) && now.Sub(last) >= s.capacityInterval))
	s.mu.Unlock()
	if !due || !s.dispatchMu.TryLock() {
		return
	}
	if !s.dispatchAllowed(local.ID) {
		s.dispatchMu.Unlock()
		return
	}
	s.mu.Lock()
	identityResult := saved.LastResult
	identityFailure := identityResult == "identity_mismatch" || identityResult == "authentication_failed"
	busy := s.capacityCapturing || pending == nil && (!s.probedAccounts[local.ID] || s.blockedAccounts[local.ID] && !identityFailure)
	runs := make([]*owned, 0, len(s.runs))
	for _, e := range s.runs {
		runs = append(runs, e)
	}
	s.mu.Unlock()
	for _, e := range runs {
		e.mu.Lock()
		busy = busy || e.record.AccountID == local.ID && (!noLocalProcess(e.record) || e.record.State == "claim_pending")
		e.mu.Unlock()
	}
	if busy {
		s.dispatchMu.Unlock()
		return
	}
	// Persist an attempt BEFORE IO. A crash cannot turn a pending request into
	// repeated launches or erase its retry deadline.
	if pending == nil {
		saved.HandledCheck = ""
		if manual {
			saved.HandledCheck = account.PendingCheck.ID
		}
		saved.Failures = min(saved.Failures+1, 1000000)
		saved.NextAttempt = now.Add(checkRetry(saved.Failures))
		if !identityFailure {
			saved.LastResult = "timeout"
		}
	}
	s.mu.Lock()
	s.capacityChecks[local.ID] = saved
	err := s.saveCapacityChecksLocked()
	if err == nil {
		s.capacityCapturing = true
		s.capacityAccountID = local.ID
		s.capacityStartedAt = now
		s.capacityCheckRefresh[local.ID] = false
	}
	s.mu.Unlock()
	s.dispatchMu.Unlock()
	if err != nil {
		logCapacityError("capacity retry persistence failed", local.ID, err)
		return
	}
	cleanupBlocked := false
	defer func() {
		s.mu.Lock()
		if !cleanupBlocked {
			s.capacityCapturing = false
			s.capacityAccountID = ""
			s.capacityStartedAt = time.Time{}
		}
		s.mu.Unlock()
	}()
	if pending != nil {
		s.finishCapacityCheck(ctx, local, account, *pending, api)
		return
	}
	op, cancel := context.WithTimeout(ctx, 10*time.Second)
	var capture CapacityCapture
	if c, ok := adapter.(interface {
		CaptureCapacityResult(context.Context, string) CapacityCapture
	}); ok {
		capture = c.CaptureCapacityResult(op, local.Key)
	} else if c, ok := adapter.(interface{ CanCaptureCapacity(string) bool }); ok && !c.CanCaptureCapacity(local.Key) {
		capture.Result = "unsupported"
	} else if c, ok := adapter.(interface {
		CaptureCapacity(context.Context, string) []capacity.Reading
	}); ok {
		capture.Readings = c.CaptureCapacity(op, local.Key)
		capture.Result = "success"
		if len(capture.Readings) == 0 {
			capture = captureError(op)
		}
	} else {
		capture.Result = "unsupported"
	}
	cancel()
	now = s.capacityNow()
	if !boundedCapacityResult(capture.Result) {
		capture = CapacityCapture{Result: "protocol", CleanupUnconfirmed: capture.CleanupUnconfirmed}
	}
	if capture.Result == "success" && !validCapacityCapture(capture, now) {
		capture = CapacityCapture{Result: "protocol", CleanupUnconfirmed: capture.CleanupUnconfirmed}
	}
	report := CapacityCheckReport{BindingRevision: account.LinkRevision, Result: capture.Result}
	if manual {
		report.CheckID = account.PendingCheck.ID
	}
	if capture.Result == "success" {
		report.Facts = capacityCheckFacts(account, capture, now)
		if len(report.Facts) == 0 {
			report.Result = "protocol"
			capture.Result = "protocol"
		}
	}
	if report.Result != "success" && report.Result != "identity_mismatch" && report.Result != "authentication_failed" && identityFailure {
		report.Result = identityResult
	}
	saved.LastResult = report.Result
	saved.CleanupUnconfirmed = capture.CleanupUnconfirmed
	cleanupBlocked = capture.CleanupUnconfirmed
	saved.RefreshAt = capacityRefreshAt(capture, now)
	if report.Result == "success" || report.Result == "unsupported" {
		saved.Failures = 0
		saved.NextAttempt = now.Add(s.capacityInterval)
	} else {
		saved.NextAttempt = now.Add(checkRetry(saved.Failures))
	}
	completion := capacityCheckCompletion{Generation: s.generation, Report: report}
	saved.Pending = &completion
	s.mu.Lock()
	s.capacityChecks[local.ID] = saved
	err = s.saveCapacityChecksLocked()
	if report.Result == "identity_mismatch" || report.Result == "authentication_failed" {
		s.blockedAccounts[local.ID] = true
		if s.probeFailureReasons == nil {
			s.probeFailureReasons = map[string]string{}
		}
		s.probeFailureReasons[local.ID] = ProbeIdentityMismatch
		s.loginRequired[local.ID] = report.Result == "authentication_failed"
		if s.loginRequired[local.ID] {
			s.probeFailureReasons[local.ID] = ProbeAuthFailed
		}
	} else if report.Result == "success" && identityFailure && s.probedAccounts[local.ID] {
		s.blockedAccounts[local.ID] = false
		s.loginRequired[local.ID] = false
		delete(s.probeFailureReasons, local.ID)
	}
	s.mu.Unlock()
	if err != nil {
		logCapacityError("capacity result persistence failed", local.ID, err)
		return
	}
	// Re-check remote binding and local consent AFTER capture. The final server
	// transaction remains authoritative for revocation between poll and report.
	s.finishCapacityCheck(ctx, local, account, completion, api)
	if capture.Result == "success" {
		s.rememberCapacityCapture(local.ID, capture.Readings)
	}
}

func capacityCompletionCurrent(c capacityCheckCompletion, a CapacityCheckAccount, generation string, now time.Time) bool {
	if c.Report.BindingRevision != a.LinkRevision {
		return false
	}
	if c.Report.CheckID != "" && (c.Generation != generation || !checkRequestCurrent(a.PendingCheck, a, generation, now) || c.Report.CheckID != a.PendingCheck.ID) {
		return false
	}
	for _, fact := range c.Report.Facts {
		member := false
		for _, resource := range a.Resources {
			member = member || resource.ID == fact.ResourceID && resource.BindingRevision == a.LinkRevision
		}
		if !member {
			return false
		}
	}
	return true
}

func validCapacityCapture(c CapacityCapture, now time.Time) bool {
	if len(c.Readings) > 32 || len(c.Readings) == 0 && c.Credits == nil {
		return false
	}
	if c.Credits != nil && (!c.Credits.Valid() || c.Credits.ObservedAt.After(now.Add(time.Minute))) {
		return false
	}
	for _, r := range c.Readings {
		if r.Validate(now) != nil {
			return false
		}
	}
	return true
}
func capacityRefreshAt(c CapacityCapture, now time.Time) time.Time {
	var at time.Time
	for _, r := range c.Readings {
		for _, t := range []time.Time{r.ResetsAt, r.ReadAt.Add(10 * time.Minute)} {
			if t.After(now) && (at.IsZero() || t.Before(at)) {
				at = t
			}
		}
	}
	if c.Credits != nil {
		at = c.Credits.ObservedAt.Add(10 * time.Minute)
	}
	return at
}
func capacityCheckFacts(a CapacityCheckAccount, c CapacityCapture, now time.Time) []CapacityCheckFact {
	var facts []CapacityCheckFact
	for _, resource := range a.Resources {
		if resource.BindingRevision != a.LinkRevision {
			continue
		}
		if resource.Kind == "subscription_quota" {
			for _, r := range c.Readings {
				// Preserve the legacy window identity, including model-specific buckets.
				key := r.WindowKind + ":" + r.Bucket
				fact := CapacityCheckFact{ResourceID: resource.ID, WindowKey: key, Source: "agentd", ObservedAt: now, ReadingAt: &r.ReadAt, UsedPercent: &r.UsedPercent, ResetsAt: &r.ResetsAt, CreditState: "unknown", StopKind: "none"}
				if r.UsedPercent >= 100 || r.OrdinaryUsageAllowed != nil && !*r.OrdinaryUsageAllowed {
					fact.StopKind = "named_reset"
					fact.DenialReason = "quota_exhausted"
					if r.UsedPercent < 100 {
						fact.DenialReason = "vendor_denied"
					}
				}
				facts = append(facts, fact)
			}
		}
		if resource.Kind == "key_cap" && c.Credits != nil {
			credit := c.Credits
			fact := CapacityCheckFact{ResourceID: resource.ID, WindowKey: "key_cap", Source: "provider", ObservedAt: now, ReadingAt: &credit.ObservedAt, CreditState: "unknown", StopKind: "none"}
			// A null cap makes no statement about total credit or replenishment.
			fact.Remaining = credit.KeyRemaining()
			if fact.Remaining != nil && *fact.Remaining == 0 {
				fact.CreditState = "exhausted"
				fact.StopKind = "unnamed"
				fact.DenialReason = "key_cap_exhausted"
			}
			facts = append(facts, fact)
		}
	}
	// Never publish a truncated successful result.
	if len(facts) > 32 {
		return nil
	}
	return facts
}
func (s *Supervisor) finishCapacityCheck(ctx context.Context, local EnrolledAccount, account CapacityCheckAccount, completion capacityCheckCompletion, api capacityChecksAPI) {
	if !s.dispatchAllowed(local.ID) {
		return
	}
	latest, err := api.PollCapacityChecks(ctx)
	if err != nil {
		return
	}
	current := false
	for _, a := range latest {
		if a.ID == local.ID && currentCheckAccount(local, a, s.daemonID) && a.LinkRevision == completion.Report.BindingRevision {
			current = capacityCompletionCurrent(completion, a, s.generation, s.capacityNow())
		}
	}
	if !current {
		return
	}
	status := ProbeStatus{OK: true}
	if completion.Report.Result == "identity_mismatch" {
		status = ProbeStatus{Failure: ProbeIdentityMismatch}
	}
	if completion.Report.Result == "authentication_failed" {
		status = ProbeStatus{Failure: ProbeAuthFailed}
	}
	if err := api.ReportCapacityCheck(ctx, local.ID, s.daemonID, s.generation, status, completion.Report); err != nil {
		logCapacityError("capacity check report failed", local.ID, err)
		return
	}
	s.mu.Lock()
	v := s.capacityChecks[local.ID]
	v.Pending = nil
	s.capacityChecks[local.ID] = v
	err = s.saveCapacityChecksLocked()
	s.mu.Unlock()
	if err != nil {
		logCapacityError("capacity completion persistence failed", local.ID, err)
	}
}
