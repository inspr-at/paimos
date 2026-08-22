// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	runnerControlLeaseTTL     = 90 * time.Second
	runnerControlRenewEvery   = 30 * time.Second
	runnerControlPullInterval = 250 * time.Millisecond
	runnerControlRetryMin     = time.Second
	runnerControlRetryMax     = 10 * time.Second
)

var (
	runnerDeliveryKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
	runnerIssueKeyPattern    = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,15}-[1-9][0-9]*$`)
)

type runnerControlTarget struct {
	DeliveryID                 int64  `json:"delivery_id"`
	DeliveryKey                string `json:"delivery_key"`
	DeliveryRevision           int64  `json:"delivery_revision"`
	RootIssueID                int64  `json:"root_issue_id"`
	IssueRevision              int64  `json:"issue_revision"`
	AttemptID                  int64  `json:"attempt_id"`
	AttemptNumber              int64  `json:"attempt_number"`
	PlanRevision               int64  `json:"plan_revision"`
	StageKey                   string `json:"stage_key"`
	ExecutionNumber            int64  `json:"execution_number"`
	ExecutionStartStageEventID int64  `json:"execution_start_stage_event_id"`
	AuthorityEpoch             int64  `json:"authority_epoch"`
	AuthorityStageEventID      int64  `json:"authority_stage_event_id"`
	ReporterID                 int64  `json:"reporter_id"`
	RunID                      int64  `json:"run_id"`
}

func (target runnerControlTarget) validForRun(runID int64) bool {
	stage := target.StageKey == "specification" || target.StageKey == "implementation" || target.StageKey == "qa" ||
		target.StageKey == "deployment" || target.StageKey == "verification"
	return runID > 0 && target.RunID == runID && target.DeliveryID > 0 && validRunnerDeliveryKey(target.DeliveryKey) &&
		target.DeliveryRevision > 0 && target.RootIssueID > 0 && target.IssueRevision > 0 && target.AttemptID > 0 &&
		target.AttemptNumber > 0 && target.PlanRevision > 0 && stage && target.ExecutionNumber > 0 &&
		target.ExecutionStartStageEventID > 0 && target.AuthorityEpoch > 0 && target.AuthorityStageEventID > 0 &&
		target.ReporterID > 0
}

func validRunnerDeliveryKey(value string) bool {
	return len(value) >= 7 && len(value) <= 80 && runnerDeliveryKeyPattern.MatchString(value)
}

func (lease runnerControlLease) validForRun(runID int64) bool {
	return lease.validForRunActions(runID, []string{"run.cancel.running"})
}

func (lease runnerControlLease) validForRunActions(runID int64, supported []string) bool {
	canonical, err := canonicalRunnerControlActions(supported)
	if err != nil || lease.LeaseID == "" || lease.Revision <= 0 || !validRunnerDeliveryKey(lease.DeliveryKey) ||
		!runnerIssueKeyPattern.MatchString(lease.IssueKey) || len(lease.Actions) == 0 ||
		!lease.Target.validForRun(runID) || lease.Target.DeliveryKey != lease.DeliveryKey || !lease.ExpiresAt.After(time.Now()) {
		return false
	}
	allowed := make(map[string]bool, len(canonical))
	for _, action := range canonical {
		allowed[action] = true
	}
	previous := -1
	for _, action := range lease.Actions {
		if !allowed[action] {
			return false
		}
		position := -1
		for index, candidate := range runnerControlActionOrder {
			if candidate == action {
				position = index
				break
			}
		}
		if position <= previous {
			return false
		}
		previous = position
	}
	return true
}

func reflectStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type runnerControlLease struct {
	LeaseID     string              `json:"lease_id"`
	Revision    int64               `json:"revision"`
	DeliveryKey string              `json:"delivery_key"`
	IssueKey    string              `json:"issue_key"`
	Actions     []string            `json:"actions"`
	ExpiresAt   time.Time           `json:"expires_at"`
	Target      runnerControlTarget `json:"target"`
}

var runnerControlActionOrder = []string{"run.cancel.running", "input.respond", "run.pause", "run.resume"}

// runnerControlAdapter is the closed boundary between the durable control
// transport and a provider runtime. An adapter may advertise only actions it
// can durably apply and reconcile after a runner restart.
type runnerControlAdapter interface {
	SupportedActions() []string
	Apply(context.Context, runnerControlEffect) (runnerControlDecision, error)
	Reconcile(context.Context, runnerControlEffect) (runnerControlDecision, bool, error)
}

type runnerControlDecision struct {
	Deferred bool
	Outcome  string
	Reason   string
}

// oneShotRunnerControlAdapter is the truthful production capability boundary
// for the current Claude Code, Codex, and custom command runners. They expose
// no durable provider input or pause channel; owned process cancellation is
// deferred to the supervisor so it can acknowledge only after process reap.
type oneShotRunnerControlAdapter struct{}

func (oneShotRunnerControlAdapter) SupportedActions() []string {
	return []string{"run.cancel.running"}
}

func (oneShotRunnerControlAdapter) Apply(_ context.Context, effect runnerControlEffect) (runnerControlDecision, error) {
	if effect.Action != "run.cancel.running" {
		return runnerControlDecision{}, errors.New("one-shot runner received an unsupported control action")
	}
	return runnerControlDecision{Deferred: true}, nil
}

func (oneShotRunnerControlAdapter) Reconcile(context.Context, runnerControlEffect) (runnerControlDecision, bool, error) {
	// A replacement process cannot prove the outcome of a prior process-tree
	// termination. Server reconciliation must conservatively seal it unknown.
	return runnerControlDecision{}, false, nil
}

type runnerControlEffect struct {
	OutboxID        int64               `json:"outbox_id"`
	CommandID       string              `json:"command_id"`
	Action          string              `json:"action"`
	EffectSequence  int64               `json:"effect_sequence"`
	LeaseID         string              `json:"lease_id"`
	LeaseRevision   int64               `json:"lease_revision"`
	Target          runnerControlTarget `json:"target"`
	InputRequestID  string              `json:"input_request_id,omitempty"`
	InputRevision   int64               `json:"input_request_revision,omitempty"`
	InputResponse   string              `json:"input_response,omitempty"`
	ChoiceOrdinal   int                 `json:"choice_ordinal,omitempty"`
	ChoiceCode      string              `json:"choice_code,omitempty"`
	RuntimeRevision int64               `json:"runtime_revision,omitempty"`
}

type runnerControlPull struct {
	SnapshotHighWater int64                 `json:"snapshot_high_water"`
	NextCursor        int64                 `json:"next_cursor"`
	HasMore           bool                  `json:"has_more"`
	Effects           []runnerControlEffect `json:"effects"`
}

type runnerClaimedCancellation struct {
	Effect runnerControlEffect
}

type runnerControlHTTP struct {
	client           *Client
	supportedActions []string
}

func canonicalRunnerControlActions(actions []string) ([]string, error) {
	if len(actions) == 0 {
		return nil, errors.New("runner control adapter advertises no actions")
	}
	wanted := make(map[string]bool, len(actions))
	for _, action := range actions {
		if wanted[action] {
			return nil, errors.New("runner control adapter contains a duplicate action")
		}
		wanted[action] = true
	}
	out := make([]string, 0, len(actions))
	for _, action := range runnerControlActionOrder {
		if wanted[action] {
			out = append(out, action)
			delete(wanted, action)
		}
	}
	if len(wanted) != 0 || len(out) == 0 {
		return nil, errors.New("runner control adapter contains an unsupported action")
	}
	hasPause, hasResume := false, false
	for _, action := range out {
		hasPause = hasPause || action == "run.pause"
		hasResume = hasResume || action == "run.resume"
	}
	if hasPause != hasResume {
		return nil, errors.New("runner pause and resume capabilities must be advertised together")
	}
	return out, nil
}

func (h runnerControlHTTP) actions() ([]string, error) {
	actions := h.supportedActions
	if actions == nil {
		actions = (oneShotRunnerControlAdapter{}).SupportedActions()
	}
	return canonicalRunnerControlActions(actions)
}

func (h runnerControlHTTP) issueLease(ctx context.Context, runID int64, deviceID, attemptID string) (runnerControlLease, error) {
	if runID <= 0 || strings.TrimSpace(deviceID) == "" || strings.TrimSpace(attemptID) != attemptID || attemptID == "" {
		return runnerControlLease{}, errors.New("runner control lease request is invalid")
	}
	actions, err := h.actions()
	if err != nil {
		return runnerControlLease{}, err
	}
	body := struct {
		DeviceID         string   `json:"device_id"`
		SupportedActions []string `json:"supported_actions"`
	}{DeviceID: deviceID, SupportedActions: actions}
	var lease runnerControlLease
	err = h.postWithKey(ctx, fmt.Sprintf("/api/runs/%d/control-capability-leases", runID), body,
		stableRunnerControlKey("lease.issue", strconv.FormatInt(runID, 10), deviceID, attemptID), &lease)
	if err == nil && !lease.validForRunActions(runID, actions) {
		err = errors.New("runner control lease response is invalid")
	}
	return lease, err
}

func (h runnerControlHTTP) renewLease(ctx context.Context, lease runnerControlLease, deviceID, attemptID string) (runnerControlLease, error) {
	if strings.TrimSpace(attemptID) != attemptID || attemptID == "" {
		return runnerControlLease{}, errors.New("runner control lease renewal identity is unavailable")
	}
	actions, err := h.actions()
	if err != nil {
		return runnerControlLease{}, err
	}
	body := struct {
		Operation        string   `json:"operation"`
		Revision         int64    `json:"revision"`
		DeviceID         string   `json:"device_id"`
		SupportedActions []string `json:"supported_actions"`
	}{Operation: "renew", Revision: lease.Revision, DeviceID: deviceID, SupportedActions: actions}
	var renewed runnerControlLease
	err = h.postWithKey(ctx, "/api/control-capability-leases/"+lease.LeaseID, body,
		stableRunnerControlKey("lease.renew", lease.LeaseID, strconv.FormatInt(lease.Revision, 10), attemptID), &renewed)
	if err == nil && (renewed.LeaseID != lease.LeaseID || renewed.Revision != lease.Revision ||
		renewed.DeliveryKey != lease.DeliveryKey || renewed.IssueKey != lease.IssueKey || renewed.Target != lease.Target ||
		!renewed.validForRunActions(lease.Target.RunID, actions) || !reflectStringSlices(renewed.Actions, lease.Actions) ||
		!renewed.ExpiresAt.After(lease.ExpiresAt)) {
		err = errors.New("runner control lease renewal response is invalid")
	}
	return renewed, err
}

func (h runnerControlHTTP) revokeLease(ctx context.Context, lease runnerControlLease, deviceID string) error {
	body := struct {
		Operation string `json:"operation"`
		Revision  int64  `json:"revision"`
		DeviceID  string `json:"device_id"`
	}{Operation: "revoke", Revision: lease.Revision, DeviceID: deviceID}
	return h.postWithKey(ctx, "/api/control-capability-leases/"+lease.LeaseID, body,
		stableRunnerControlKey("lease.revoke", lease.LeaseID, strconv.FormatInt(lease.Revision, 10)), nil)
}

func (h runnerControlHTTP) pull(ctx context.Context, lease runnerControlLease, cursor int64, deviceID string) (runnerControlPull, error) {
	body := struct {
		LeaseID       string `json:"lease_id"`
		LeaseRevision int64  `json:"lease_revision"`
		DeviceID      string `json:"device_id"`
		Cursor        int64  `json:"cursor"`
	}{LeaseID: lease.LeaseID, LeaseRevision: lease.Revision, DeviceID: deviceID, Cursor: cursor}
	var page runnerControlPull
	err := h.postWithKey(ctx, fmt.Sprintf("/api/runs/%d/control-commands", lease.Target.RunID), body,
		stableRunnerControlKey("command.pull", lease.LeaseID, strconv.FormatInt(lease.Revision, 10), strconv.FormatInt(cursor, 10)), &page)
	if err == nil && !page.validForLease(lease, cursor) {
		err = errors.New("runner control pull response is invalid")
	}
	return page, err
}

func (page runnerControlPull) validForLease(lease runnerControlLease, cursor int64) bool {
	if cursor < 0 || page.SnapshotHighWater < cursor || page.NextCursor < cursor ||
		page.NextCursor > page.SnapshotHighWater || len(page.Effects) > 100 {
		return false
	}
	previous := cursor
	for _, effect := range page.Effects {
		if effect.OutboxID <= previous || effect.OutboxID > page.SnapshotHighWater ||
			effect.CommandID == "" || effect.EffectSequence != 1 ||
			effect.LeaseID != lease.LeaseID || effect.LeaseRevision != lease.Revision ||
			!effect.Target.validForRun(lease.Target.RunID) || effect.Target != lease.Target || !effect.validForLease(lease) {
			return false
		}
		previous = effect.OutboxID
	}
	if len(page.Effects) == 0 {
		return page.NextCursor == cursor && !page.HasMore
	}
	return page.NextCursor == previous && (!page.HasMore || len(page.Effects) == 100)
}

func (effect runnerControlEffect) validForLease(lease runnerControlLease) bool {
	supported := false
	for _, action := range lease.Actions {
		if action == effect.Action {
			supported = true
			break
		}
	}
	if !supported {
		return false
	}
	hasInput := effect.InputRequestID != "" || effect.InputRevision != 0 || effect.InputResponse != "" ||
		effect.ChoiceOrdinal != 0 || effect.ChoiceCode != ""
	switch effect.Action {
	case "run.cancel.running":
		return !hasInput && effect.RuntimeRevision == 0
	case "run.pause", "run.resume":
		return !hasInput && effect.RuntimeRevision > 0
	case "input.respond":
		if effect.InputRequestID == "" || effect.InputRevision <= 0 || effect.RuntimeRevision != 0 {
			return false
		}
		switch effect.InputResponse {
		case "approve", "reject":
			return effect.ChoiceOrdinal == 0 && effect.ChoiceCode == ""
		case "choice":
			return effect.ChoiceOrdinal >= 1 && effect.ChoiceOrdinal <= 8 &&
				effect.ChoiceCode == "choice_"+strconv.Itoa(effect.ChoiceOrdinal)
		default:
			return false
		}
	default:
		return false
	}
}

func (h runnerControlHTTP) claim(ctx context.Context, effect runnerControlEffect, deviceID string) (runnerControlEffect, error) {
	body := struct {
		Operation      string `json:"operation"`
		LeaseID        string `json:"lease_id"`
		LeaseRevision  int64  `json:"lease_revision"`
		EffectSequence int64  `json:"effect_sequence"`
		DeviceID       string `json:"device_id"`
	}{Operation: "claim", LeaseID: effect.LeaseID, LeaseRevision: effect.LeaseRevision,
		EffectSequence: effect.EffectSequence, DeviceID: deviceID}
	var claimed runnerControlEffect
	err := h.postWithKey(ctx, "/api/control-commands/"+effect.CommandID, body,
		stableRunnerControlKey("command.claim", effect.CommandID, effect.LeaseID,
			strconv.FormatInt(effect.LeaseRevision, 10), strconv.FormatInt(effect.EffectSequence, 10)), &claimed)
	if err == nil && (claimed != effect ||
		!claimed.validForLease(runnerControlLease{LeaseID: effect.LeaseID, Revision: effect.LeaseRevision,
			Actions: []string{effect.Action}, Target: effect.Target})) {
		err = errors.New("runner control claim response is invalid")
	}
	return claimed, err
}

func (h runnerControlHTTP) result(ctx context.Context, effect runnerControlEffect, deviceID, outcome, reason string) error {
	body := struct {
		Operation      string `json:"operation"`
		LeaseID        string `json:"lease_id"`
		LeaseRevision  int64  `json:"lease_revision"`
		EffectSequence int64  `json:"effect_sequence"`
		ClaimSequence  int64  `json:"claim_sequence"`
		ResultSequence int64  `json:"result_sequence"`
		DeviceID       string `json:"device_id"`
		Outcome        string `json:"outcome"`
		Reason         string `json:"reason,omitempty"`
	}{Operation: "result", LeaseID: effect.LeaseID, LeaseRevision: effect.LeaseRevision,
		EffectSequence: effect.EffectSequence, ClaimSequence: 1, ResultSequence: 1,
		DeviceID: deviceID, Outcome: outcome, Reason: reason}
	return h.postWithKey(ctx, "/api/control-commands/"+effect.CommandID, body,
		stableRunnerControlKey("command.result", effect.CommandID, effect.LeaseID,
			strconv.FormatInt(effect.LeaseRevision, 10), outcome, reason), nil)
}

func (h runnerControlHTTP) postWithKey(ctx context.Context, path string, body any, key string, dst any) error {
	if h.client == nil || h.client.http == nil || ctx == nil || key == "" {
		return errors.New("runner control client is unavailable")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.client.baseURL+path, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		h.client.prepareRequest(req, true, "application/json", "application/json")
		req.Header.Set(idempotencyHeader, key)
		response, err := h.client.doRequest(req)
		if err == nil {
			if dst == nil {
				return nil
			}
			decoder := json.NewDecoder(bytes.NewReader(response))
			decoder.DisallowUnknownFields()
			if decodeErr := decoder.Decode(dst); decodeErr != nil {
				return errors.New("runner control response is invalid")
			}
			if decodeErr := decoder.Decode(&struct{}{}); !errors.Is(decodeErr, io.EOF) {
				return errors.New("runner control response has trailing data")
			}
			return nil
		}
		var httpErr *httpError
		if errors.As(err, &httpErr) && httpErr.Code < 500 {
			return err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func stableRunnerControlKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80
	hexValue := hex.EncodeToString(sum[:16])
	return hexValue[:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:32]
}

func runnerControlStateDir(deviceID string) (string, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return "", errors.New("runner device id is unavailable")
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", errors.New("runner home directory is unavailable")
	}
	digest := sha256.Sum256([]byte(deviceID))
	return filepath.Join(home, ".paimos", "state", "run-agent", hex.EncodeToString(digest[:8])), nil
}

type runControlArbiter struct {
	http     runnerControlHTTP
	runID    int64
	deviceID string
	journal  *runnerControlJournal
	adapter  runnerControlAdapter

	mu       sync.Mutex
	lease    runnerControlLease
	cancel   context.CancelFunc
	done     chan struct{}
	requests chan runnerClaimedCancellation
	revoked  bool
	pumpErr  error

	now           func() time.Time
	newAttempt    func() string
	renewEvery    time.Duration
	pullInterval  time.Duration
	operationTime time.Duration
	retryMin      time.Duration
	retryMax      time.Duration
}

func newRunControlArbiter(client *Client, runID int64, deviceID string, journal *runnerControlJournal) *runControlArbiter {
	return newRunControlArbiterWithAdapter(client, runID, deviceID, journal, oneShotRunnerControlAdapter{})
}

func newRunControlArbiterWithAdapter(client *Client, runID int64, deviceID string, journal *runnerControlJournal,
	adapter runnerControlAdapter) *runControlArbiter {
	actions := []string(nil)
	if adapter != nil {
		actions = adapter.SupportedActions()
	}
	return &runControlArbiter{http: runnerControlHTTP{client: client, supportedActions: actions}, runID: runID, deviceID: deviceID,
		journal: journal, requests: make(chan runnerClaimedCancellation, 100), now: time.Now,
		adapter:    adapter,
		newAttempt: uuid.NewString, renewEvery: runnerControlRenewEvery, pullInterval: runnerControlPullInterval,
		operationTime: 10 * time.Second, retryMin: runnerControlRetryMin, retryMax: runnerControlRetryMax}
}

func (a *runControlArbiter) start(ctx context.Context, owned bool) error {
	if !owned {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done != nil {
		return nil
	}
	if a.adapter == nil {
		return errors.New("runner control adapter is unavailable")
	}
	// Validate the capability snapshot captured at construction; never ask a
	// mutable adapter again after its lease contract has been frozen.
	if _, err := canonicalRunnerControlActions(a.http.supportedActions); err != nil {
		return err
	}
	attemptID := a.newAttempt()
	if attemptID == "" {
		return errors.New("runner control lease attempt identity is unavailable")
	}
	lease, err := a.http.issueLease(ctx, a.runID, a.deviceID, attemptID)
	if err != nil {
		return err
	}
	if err := a.replayDurable(ctx, lease); err != nil {
		return err
	}
	pumpCtx, cancel := context.WithCancel(context.Background())
	a.lease, a.cancel, a.done, a.pumpErr = lease, cancel, make(chan struct{}), nil
	go a.pump(pumpCtx)
	return nil
}

func (a *runControlArbiter) pump(ctx context.Context) {
	a.mu.Lock()
	done := a.done
	a.mu.Unlock()
	err := a.pumpLoop(ctx)
	a.mu.Lock()
	if err != nil {
		a.pumpErr = err
	}
	a.mu.Unlock()
	close(done)
}

func (a *runControlArbiter) pumpLoop(ctx context.Context) error {
	renew := time.NewTimer(a.renewEvery)
	poll := time.NewTicker(a.pullInterval)
	defer renew.Stop()
	defer poll.Stop()
	var cursor int64
	retryDelay := a.retryMin
	issueAttemptID := ""
	renewAttemptID := ""
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-renew.C:
			a.mu.Lock()
			lease := a.lease
			a.mu.Unlock()
			if !a.now().Before(lease.ExpiresAt) {
				renewAttemptID = ""
				if issueAttemptID == "" {
					issueAttemptID = a.newAttempt()
					if issueAttemptID == "" {
						return errors.New("runner control lease attempt identity became unavailable")
					}
				}
				opCtx, cancel := context.WithTimeout(ctx, a.operationTime)
				fresh, err := a.http.issueLease(opCtx, a.runID, a.deviceID, issueAttemptID)
				cancel()
				if err != nil {
					if isDefinitiveRunnerControlError(err) {
						return fmt.Errorf("runner control lease reissue failed definitively: %w", err)
					}
					renew.Reset(retryDelay)
					retryDelay = nextRunnerControlRetry(retryDelay, a.retryMax)
					continue
				}
				a.mu.Lock()
				a.lease = fresh
				a.mu.Unlock()
				cursor, issueAttemptID, retryDelay = 0, "", a.retryMin
				renew.Reset(a.renewEvery)
				continue
			}
			if renewAttemptID == "" {
				renewAttemptID = a.newAttempt()
				if renewAttemptID == "" {
					return errors.New("runner control lease renewal identity became unavailable")
				}
			}
			opCtx, cancel := context.WithTimeout(ctx, a.operationTime)
			renewed, err := a.http.renewLease(opCtx, lease, a.deviceID, renewAttemptID)
			cancel()
			if err != nil {
				if isDefinitiveRunnerControlError(err) {
					remaining := lease.ExpiresAt.Sub(a.now())
					if remaining > 0 {
						renew.Reset(remaining)
					} else {
						renew.Reset(0)
					}
					continue
				}
				remaining := lease.ExpiresAt.Sub(a.now())
				if remaining <= 0 {
					renew.Reset(0)
				} else {
					delay := retryDelay
					if delay > remaining {
						delay = remaining
					}
					renew.Reset(delay)
					retryDelay = nextRunnerControlRetry(retryDelay, a.retryMax)
				}
				continue
			}
			a.mu.Lock()
			a.lease = renewed
			a.mu.Unlock()
			cursor, retryDelay, renewAttemptID = 0, a.retryMin, ""
			renew.Reset(a.renewEvery)
		case <-poll.C:
			a.mu.Lock()
			lease := a.lease
			a.mu.Unlock()
			for {
				opCtx, cancel := context.WithTimeout(ctx, a.operationTime)
				page, err := a.http.pull(opCtx, lease, cursor, a.deviceID)
				cancel()
				if err != nil {
					break
				}
				for _, effect := range page.Effects {
					cursor = effect.OutboxID
					if effect.Target.RunID != a.runID || !effect.validForLease(lease) ||
						effect.LeaseID != lease.LeaseID || effect.LeaseRevision != lease.Revision || effect.EffectSequence != 1 {
						continue
					}
					claimCtx, claimCancel := context.WithTimeout(ctx, 10*time.Second)
					claimed, claimErr := a.http.claim(claimCtx, effect, a.deviceID)
					claimCancel()
					if claimErr != nil {
						continue
					}
					if a.journal != nil {
						digest := sha256.Sum256([]byte(claimed.CommandID + "\x00claimed"))
						if journalErr := a.journal.put(runnerControlJournalRecord{CommandID: claimed.CommandID, LeaseID: claimed.LeaseID,
							LeaseRevision: claimed.LeaseRevision, EffectSequence: claimed.EffectSequence,
							ClaimSequence: 1, ResultSequence: 1, RequestDigest: hex.EncodeToString(digest[:]),
							Outcome: "outcome_unknown", State: "claimed", Effect: &claimed}); journalErr != nil {
							return fmt.Errorf("persist claimed runner control effect: %w", journalErr)
						}
					}
					decision, applyErr := a.adapter.Apply(ctx, claimed)
					if applyErr != nil {
						return fmt.Errorf("apply runner control effect: %w", applyErr)
					}
					if decision.Deferred {
						if claimed.Action != "run.cancel.running" || decision.Outcome != "" || decision.Reason != "" {
							return errors.New("runner control adapter returned an invalid deferred decision")
						}
						select {
						case a.requests <- runnerClaimedCancellation{Effect: claimed}:
						case <-ctx.Done():
							return nil
						}
						continue
					}
					if !validRunnerControlDecision(decision) {
						return errors.New("runner control adapter returned an invalid result")
					}
					resultCtx, resultCancel := context.WithTimeout(ctx, a.operationTime)
					resultErr := a.recordResult(resultCtx, runnerClaimedCancellation{Effect: claimed}, decision.Outcome, decision.Reason)
					resultCancel()
					if resultErr != nil {
						return fmt.Errorf("record runner control result: %w", resultErr)
					}
				}
				if !page.HasMore {
					break
				}
				cursor = page.NextCursor
			}
		}
	}
}

func isDefinitiveRunnerControlError(err error) bool {
	var httpErr *httpError
	return errors.As(err, &httpErr) && httpErr.Code < http.StatusInternalServerError
}

func nextRunnerControlRetry(current, maximum time.Duration) time.Duration {
	if current <= 0 {
		return time.Millisecond
	}
	next := current * 2
	if maximum > 0 && next > maximum {
		return maximum
	}
	return next
}

func validRunnerControlDecision(decision runnerControlDecision) bool {
	if decision.Deferred {
		return decision.Outcome == "" && decision.Reason == ""
	}
	if decision.Outcome == "applied" {
		return decision.Reason == ""
	}
	return decision.Outcome == "rejected" && (decision.Reason == "effect_rejected" ||
		decision.Reason == "unsupported_platform")
}

func (a *runControlArbiter) recordResult(ctx context.Context, claim runnerClaimedCancellation, outcome, reason string) error {
	if a == nil {
		return errors.New("runner control arbiter is unavailable")
	}
	effect := claim.Effect
	bodyDigest := sha256.Sum256([]byte(effect.CommandID + "\x00" + outcome + "\x00" + reason))
	record := runnerControlJournalRecord{CommandID: effect.CommandID, LeaseID: effect.LeaseID,
		LeaseRevision: effect.LeaseRevision, EffectSequence: effect.EffectSequence, ClaimSequence: 1,
		ResultSequence: 1, RequestDigest: hex.EncodeToString(bodyDigest[:]), Outcome: outcome, Reason: reason,
		State: "completed", Effect: &effect}
	if a.journal != nil {
		if err := a.journal.put(record); err != nil {
			return err
		}
	}
	if err := a.http.result(ctx, effect, a.deviceID, outcome, reason); err != nil {
		return err
	}
	if a.journal != nil {
		return a.journal.delete(effect.CommandID)
	}
	return nil
}

func (a *runControlArbiter) rejectNaturalExit(ctx context.Context) error {
	if a == nil {
		return nil
	}
	var result error
	for {
		select {
		case claim := <-a.requests:
			if err := a.recordResult(ctx, claim, "rejected", "natural_exit"); err != nil {
				result = errors.Join(result, err)
			}
		default:
			return result
		}
	}
}

func (a *runControlArbiter) replayDurable(ctx context.Context, lease runnerControlLease) error {
	if a == nil || a.journal == nil {
		return nil
	}
	for _, record := range a.journal.snapshot() {
		if record.LeaseID != lease.LeaseID || record.LeaseRevision != lease.Revision {
			continue
		}
		effect := runnerControlEffect{CommandID: record.CommandID, Action: "run.cancel.running", LeaseID: record.LeaseID,
			LeaseRevision: record.LeaseRevision, EffectSequence: record.EffectSequence, Target: lease.Target}
		if record.Effect != nil {
			effect = *record.Effect
		}
		if !effect.validForLease(lease) || effect.Target != lease.Target {
			continue
		}
		if record.State == "claimed" {
			decision, known, err := a.adapter.Reconcile(ctx, effect)
			if err != nil {
				return fmt.Errorf("reconcile runner control effect: %w", err)
			}
			if !known {
				continue
			}
			if !validRunnerControlDecision(decision) || decision.Deferred {
				return errors.New("runner control adapter returned an invalid reconciliation result")
			}
			record.Outcome, record.Reason, record.State = decision.Outcome, decision.Reason, "completed"
			digest := sha256.Sum256([]byte(effect.CommandID + "\x00" + decision.Outcome + "\x00" + decision.Reason))
			record.RequestDigest = hex.EncodeToString(digest[:])
			if err := a.journal.put(record); err != nil {
				return err
			}
		}
		claimed, err := a.http.claim(ctx, effect, a.deviceID)
		if err != nil {
			continue
		}
		if err := a.http.result(ctx, claimed, a.deviceID, record.Outcome, record.Reason); err != nil {
			continue
		}
		if err := a.journal.delete(record.CommandID); err != nil {
			return err
		}
	}
	return nil
}

func (a *runControlArbiter) quiesce(ctx context.Context) {
	_ = a.quiesceConfirmed(ctx)
}

func (a *runControlArbiter) quiesceConfirmed(ctx context.Context) bool {
	if a == nil {
		return true
	}
	a.mu.Lock()
	cancel, done := a.cancel, a.done
	a.mu.Unlock()
	if cancel == nil {
		return true
	}
	cancel()
	select {
	case <-done:
		a.mu.Lock()
		healthy := a.pumpErr == nil
		a.mu.Unlock()
		return healthy
	case <-ctx.Done():
		return false
	}
}

func (a *runControlArbiter) stop(ctx context.Context) {
	if a == nil {
		return
	}
	_ = a.revokeConfirmed(ctx)
}

// revokeConfirmed is the success-boundary barrier: the pump must terminate
// without an internal failure and the server must atomically close the exact
// cancellation lease. Callers must not publish success when it returns an
// error.
func (a *runControlArbiter) revokeConfirmed(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if !a.quiesceConfirmed(ctx) {
		return errors.New("runner control stream did not terminate cleanly")
	}
	a.mu.Lock()
	lease := a.lease
	if a.revoked {
		a.mu.Unlock()
		return nil
	}
	if lease.LeaseID == "" {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	revokeCtx, revokeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer revokeCancel()
	if err := a.http.revokeLease(revokeCtx, lease, a.deviceID); err != nil {
		return fmt.Errorf("revoke runner control lease: %w", err)
	}
	a.mu.Lock()
	a.revoked = true
	a.mu.Unlock()
	return nil
}
