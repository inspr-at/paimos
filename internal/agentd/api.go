// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/deploytarget"
	"github.com/inspr-at/paimos/internal/modelreport"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/version"
)

// Remote uses a scoped agent key through AEON's public HTTP contract.
// NewRemote callers should pin the base URL to HTTPS or a trusted loopback.
type Remote struct {
	Client               *client.Client
	mu                   sync.RWMutex
	daemonID, generation string
}

func NewRemote(baseURL, token string) *Remote {
	c := client.New(baseURL, token)
	c.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Remote{Client: c}
}

// runCredential derives a local capability without exposing the daemon key to
// a vendor process. Its scope is also checked by the owning supervisor.
func (r *Remote) runCredential(tenantID, principalID, runID, generation string) string {
	mac := hmac.New(sha256.New, []byte(r.Client.Token))
	for _, part := range []string{"aeon-managed-tools-v1", tenantID, principalID, runID, generation} {
		_, _ = mac.Write([]byte(part))
		_, _ = mac.Write([]byte{0})
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// RunToolAPI is deliberately narrower than the daemon API. Each method uses
// an existing authenticated Aeon route, whose handler enforces tenant RLS,
// caller scope and event writes. The daemon key needs comments.write,
// work_orders.read/write, approvals.request and inbox.send in addition to its
// existing run/harness scopes. There is no new httpapi.Module or plugin to
// mount: NewSupervisor starts and closes this local MCP surface per run.
type RunToolAPI interface {
	WorkOrder(context.Context, string) (WorkOrder, error)
	Comment(context.Context, string, string) error
	SetWorkStatus(context.Context, string, int64, string) (WorkOrder, error)
	CheckCriterion(context.Context, string, string, bool) error
	Evidence(context.Context, string, string, string, string) error
	RequestApproval(context.Context, string, ApprovalRequest) error
	ReplyInbox(context.Context, string, string, string, string) error
}

func (r *Remote) Comment(ctx context.Context, nodeID, body string) error {
	return r.Client.Do(ctx, "POST", "/api/nodes/"+url.PathEscape(nodeID)+"/comments", map[string]string{"body_markdown": body}, nil)
}

func (r *Remote) SetWorkStatus(ctx context.Context, orderID string, revision int64, status string) (WorkOrder, error) {
	var out WorkOrder
	err := r.Client.Do(ctx, "PATCH", "/api/work-orders/"+url.PathEscape(orderID), map[string]any{"expected_revision": revision, "status": status}, &out)
	return out, err
}

func (r *Remote) CheckCriterion(ctx context.Context, orderID, criterionID string, checked bool) error {
	return r.Client.Do(ctx, "POST", "/api/work-orders/"+url.PathEscape(orderID)+"/criteria/"+url.PathEscape(criterionID)+"/check", map[string]bool{"checked": checked}, nil)
}

func (r *Remote) Evidence(ctx context.Context, orderID, runID, criterionID, reference string) error {
	return r.Client.Do(ctx, "POST", "/api/work-orders/"+url.PathEscape(orderID)+"/evidence",
		map[string]string{"kind": "text", "reference": reference, "criterion_id": criterionID, "run_id": runID}, nil)
}

// ApprovalRequest retains the originating run. Deploy scopes may additionally provide
// an explicit release node and target; the server checks the caller's key ceiling,
// tenant visibility and run ownership. Requesting grants no authority.
type ApprovalRequest struct {
	Scope, Rationale, ExpiresAt string
	ReleaseNodeID               string
	Target                      *deploytarget.Target
}

func (in *ApprovalRequest) normalizeTarget() error {
	if in.Scope == "journey.deploy" || in.Scope == "stage.deploy" {
		if in.ReleaseNodeID != "" && !uuidPattern.MatchString(in.ReleaseNodeID) {
			return errors.New("invalid release_node_id")
		}
		target, _, err := deploytarget.Normalize(in.Target)
		if err != nil {
			return err
		}
		in.Target = target
	} else if in.ReleaseNodeID != "" || in.Target != nil {
		return errors.New("release_node_id and target are only valid for deploy approvals")
	}
	return nil
}

func (r *Remote) RequestApproval(ctx context.Context, runID string, in ApprovalRequest) error {
	if err := in.normalizeTarget(); err != nil {
		return err
	}
	body := map[string]any{
		"scope": in.Scope, "resource_kind": "run", "resource_id": runID,
		"run_id": runID, "rationale": in.Rationale, "expires_at": in.ExpiresAt,
	}
	if in.Target != nil {
		body["target"] = in.Target
	}
	if in.ReleaseNodeID != "" {
		body["resource_kind"], body["resource_id"] = "node", in.ReleaseNodeID
	}
	return r.Client.Do(ctx, "POST", "/api/approvals", body, nil)
}

func (r *Remote) ReplyInbox(ctx context.Context, messageID, recipientID, body, key string) error {
	return r.Client.Do(ctx, "POST", "/api/inbox/messages", map[string]string{
		"recipient_principal_id": recipientID, "body": body,
		"idempotency_key": key, "reply_to_id": messageID,
	}, nil)
}

func (r *Remote) Identity(ctx context.Context) (string, string, error) {
	me, err := r.Client.Me(ctx)
	if err != nil {
		return "", "", err
	}
	if me.Principal.Kind != "agent" || me.Principal.ID == "" || me.Tenant.ID == "" || me.Principal.TenantID != me.Tenant.ID {
		return "", "", errors.New("agent key identity is invalid")
	}
	return me.Tenant.ID, me.Principal.ID, nil
}

func (r *Remote) Queued(ctx context.Context) ([]Run, error) {
	var runs []Run
	err := r.Client.DoWithHeaders(ctx, "GET", "/api/runs/queued?limit=100", nil, &runs, map[string]string{reviewgate.PolicyHeader: reviewgate.Policy})
	return runs, err
}

func (r *Remote) GetRun(ctx context.Context, id string) (Run, error) {
	var run Run
	err := r.Client.Do(ctx, "GET", "/api/runs/"+url.PathEscape(id), nil, &run)
	return run, err
}

func (r *Remote) Profiles(ctx context.Context) ([]Profile, error) {
	var profiles []Profile
	err := r.Client.Do(ctx, "GET", "/api/models", nil, &profiles)
	return profiles, err
}

func (r *Remote) Node(ctx context.Context, id string) (Node, error) {
	var node Node
	err := r.Client.Do(ctx, "GET", "/api/nodes/"+url.PathEscape(id), nil, &node)
	return node, err
}

// ProjectForNode resolves the work order's current project through the public
// key lookup. Registration then validates the same binding on the server.
func (r *Remote) ProjectForNode(ctx context.Context, key string) (string, error) {
	var page struct {
		Items []struct {
			Key       string `json:"key"`
			ProjectID string `json:"project_id"`
		} `json:"items"`
	}
	if err := r.Client.Do(ctx, "GET", "/api/nodes/lookup?keys="+url.QueryEscape(key), nil, &page); err != nil {
		return "", err
	}
	if len(page.Items) != 1 || page.Items[0].Key != key || page.Items[0].ProjectID == "" {
		return "", errors.New("work order project unavailable")
	}
	return page.Items[0].ProjectID, nil
}

func harnessPath(s HarnessSession) string {
	return "/api/projects/" + url.PathEscape(s.ProjectID) + "/harness-sessions/" + url.PathEscape(s.ID)
}

func (r *Remote) RegisterHarness(ctx context.Context, s HarnessSession, agentID, runID, orderID, harness, host string, caps []string) (HarnessSession, error) {
	var result HarnessSession
	body := map[string]any{
		"max_session_file_bytes": rules.SessionFileLimit(harness), "rules_client_version": version.Version,
		"agent_principal_id": agentID, "run_id": runID, "ticket_node_id": orderID,
		"work_order_id": orderID, "harness": harness, "host": host,
		"management_mode": "managed", "role": "worker", "work_shape": "ship",
		"account_label":           s.AccountLabel,
		"advertised_capabilities": caps, "harness_session_ref": s.ID, "worker_lease": s.Lease,
	}
	if s.Model != "" {
		body["model"] = s.Model
		if modelreport.ValidTuple(s.Model, s.ReasoningEffort) {
			body["model_reports"] = []modelreport.Observation{{ReportID: modelreport.EvidenceID(s.ID + "/advertised/" + s.Model + "/" + s.ReasoningEffort), Harness: harness, Model: s.Model, Effort: s.ReasoningEffort, Status: "advertised"}}
		}
	}
	if s.ReasoningEffort != "" {
		body["reasoning_effort"] = s.ReasoningEffort
	}
	err := r.Client.Do(ctx, "POST", "/api/projects/"+url.PathEscape(s.ProjectID)+"/harness-sessions", body, &result)
	if err != nil {
		return HarnessSession{}, err
	}
	if result.ID == "" || result.ProjectID != s.ProjectID {
		return HarnessSession{}, errors.New("harness registration binding mismatch")
	}
	result.Lease = s.Lease
	result.Harness = harness
	return result, nil
}

func (r *Remote) harnessWorker(ctx context.Context, s HarnessSession, suffix string, body, dest any) error {
	err := r.Client.DoWithHeaders(ctx, "POST", harnessPath(s)+suffix, body, dest,
		map[string]string{"X-Aeon-Worker-Lease": s.Lease, rules.ClientMaximumHeader: strconv.Itoa(rules.SessionFileLimit(s.Harness))})
	var status *client.StatusError
	if errors.As(err, &status) && status.Status == 410 && status.Message == "harness generation archived" {
		return ErrHarnessArchived
	}
	return err
}

func (r *Remote) HeartbeatHarness(ctx context.Context, s HarnessSession, phase string) error {
	sequence := s.ActivitySequence
	if sequence == 0 {
		sequence = 1
	}
	activity := s.Activity
	if activity == "" {
		activity = "busy"
	}
	if phase == "stopping" {
		activity = "idle"
	}
	body := map[string]any{
		"max_session_file_bytes": rules.SessionFileLimit(s.Harness), "rules_client_version": version.Version,
		"phase": phase, "activity": activity, "activity_sequence": sequence, "process_ownership": s.Ownership,
	}
	if s.Model != "" {
		body["model"] = s.Model
		if modelreport.ValidTuple(s.Model, s.ReasoningEffort) && s.Harness != "" {
			body["model_reports"] = []modelreport.Observation{{ReportID: modelreport.EvidenceID(s.ID + "/advertised/" + s.Model + "/" + s.ReasoningEffort), Harness: s.Harness, Model: s.Model, Effort: s.ReasoningEffort, Status: "advertised"}}
		}
	}
	if s.ReasoningEffort != "" {
		body["reasoning_effort"] = s.ReasoningEffort
	}
	return r.harnessWorker(ctx, s, "/heartbeat", body, nil)
}

func (r *Remote) YieldHarness(ctx context.Context, s HarnessSession) ([]HarnessControl, error) {
	var result struct {
		Controls []HarnessControl `json:"controls"`
	}
	started := time.Now()
	err := r.harnessWorker(ctx, s, "/yield", struct{}{}, &result)
	for i := range result.Controls {
		c := &result.Controls[i]
		// Deduct the full round trip, including body decoding. Starting the TTL
		// at receipt would extend authorization after a slow response. Missing,
		// invalid or legacy TTLs fail closed; expires_at is audit metadata only.
		if c.ExpiresInMS > 0 && c.ExpiresInMS <= 45000 {
			c.deadline = started.Add(time.Duration(c.ExpiresInMS) * time.Millisecond)
		}
	}
	return result.Controls, err
}

func (r *Remote) DrainHarness(ctx context.Context, s HarnessSession) ([]HarnessDelivery, error) {
	var result []HarnessDelivery
	err := r.harnessWorker(ctx, s, "/drain", struct{}{}, &result)
	return result, err
}

func (r *Remote) CompleteHarnessControl(ctx context.Context, s HarnessSession, id, outcome, reason string) error {
	err := r.harnessWorker(ctx, s, "/controls/"+url.PathEscape(id)+"/complete",
		map[string]string{"outcome": outcome, "reason": reason}, nil)
	var status *client.StatusError
	if errors.As(err, &status) && status.Status == http.StatusConflict && terminalControlCompletion(status.Message) {
		return ErrControlTerminal
	}
	return err
}

// These conflicts never become a successful retry of the same outcome. The
// server has already finished the control, or it has refused the applied
// setting for good. Wording matches completeControl.
func terminalControlCompletion(message string) bool {
	switch message {
	case "divergent control completion", "setting authorization expired":
		return true
	default:
		return false
	}
}

func (r *Remote) CompleteHarnessDelivery(ctx context.Context, s HarnessSession, d HarnessDelivery) error {
	body := map[string]any{"delivery_id": d.ID, "cursor": d.Cursor, "effective_level": "simple"}
	if d.Outcome != "" {
		body["outcome"] = d.Outcome
	}
	if d.FailureReason != "" {
		body["failure_reason"] = d.FailureReason
	}
	return r.harnessWorker(ctx, s, "/complete-delivery", body, nil)
}

func (r *Remote) StopHarness(ctx context.Context, s HarnessSession, reason string) error {
	return r.harnessWorker(ctx, s, "/stop", map[string]string{"reason": reason}, nil)
}

func (r *Remote) WorkOrder(ctx context.Context, id string) (WorkOrder, error) {
	var order WorkOrder
	err := r.Client.Do(ctx, "GET", "/api/work-orders/"+url.PathEscape(id), nil, &order)
	return order, err
}

func (r *Remote) Route(ctx context.Context, runID, daemonID string, accountIDs []string, estimates map[string]int64) (Route, error) {
	var route Route
	err := r.Client.Do(ctx, "POST", "/api/agent-accounts/route", map[string]any{
		"run_id": runID, "daemon_id": daemonID, "account_ids": accountIDs, "estimated_units": estimates,
	}, &route)
	return route, err
}

func (r *Remote) Claim(ctx context.Context, runID, daemonID, generation string, reservations []string) error {
	// Retain the attempted binding even if the HTTP response is lost. It is
	// only a header hint; the server remains the authority for claim ownership.
	r.mu.Lock()
	r.daemonID, r.generation = daemonID, generation
	r.mu.Unlock()
	err := r.Client.DoWithHeaders(ctx, "POST", "/api/runs/"+url.PathEscape(runID)+"/claim", map[string]any{
		"daemon_id": daemonID, "daemon_generation": generation, "reservation_ids": reservations,
	}, nil, map[string]string{reviewgate.PolicyHeader: reviewgate.Policy})
	return err
}

// ErrTelemetryProtocol means the server rejected the telemetry itself. It does
// not include authentication, enrollment fences, or uncertain delivery errors.
var ErrTelemetryProtocol = errors.New("telemetry protocol violation")

func (r *Remote) Report(ctx context.Context, runID string, t Telemetry) error {
	r.mu.RLock()
	daemon, generation := r.daemonID, r.generation
	r.mu.RUnlock()
	return r.ReportForClaim(ctx, runID, daemon, generation, t)
}

// ReportForClaim preserves the durable run's fencing identity across restart
// or another claim. Possession of these public strings grants no authority.
func (r *Remote) ReportForClaim(ctx context.Context, runID, daemon, generation string, t Telemetry) error {
	if daemon == "" || generation == "" {
		return errors.New("run has no daemon claim")
	}
	err := r.Client.DoWithHeaders(ctx, "POST", "/api/runs/"+url.PathEscape(runID)+"/telemetry", t, nil,
		map[string]string{"X-Aeon-Daemon-ID": daemon, "X-Aeon-Daemon-Generation": generation})
	var status *client.StatusError
	if errors.As(err, &status) {
		protocol := status.Status == http.StatusBadRequest || status.Status == http.StatusRequestEntityTooLarge || status.Status == http.StatusUnprocessableEntity
		if status.Status == http.StatusConflict {
			// Other conflicts include generation changes and enrollment drain.
			// Only the telemetry endpoint's explicit protocol errors are fatal.
			switch status.Message {
			case "divergent telemetry replay", "telemetry sequence is not monotonic", "run cannot return to starting":
				protocol = true
			}
		}
		if protocol {
			// Do not propagate arbitrary server response text as diagnostics.
			return fmt.Errorf("%w (HTTP %d)", ErrTelemetryProtocol, status.Status)
		}
	}
	return err
}

func (r *Remote) Inbox(ctx context.Context, after int64) (InboxPage, error) {
	var page InboxPage
	err := r.Client.Do(ctx, "GET", fmt.Sprintf("/api/inbox/messages?after=%d&wait_ms=0&limit=100", after), nil, &page)
	return page, err
}

func (r *Remote) Ack(ctx context.Context, id string) error {
	return r.Client.Do(ctx, "POST", "/api/inbox/messages/"+url.PathEscape(id)+"/ack", nil, nil)
}

func (r *Remote) AddEvidence(ctx context.Context, workOrderID, runID, answer string) error {
	if answer == "" || len(answer) > 64<<10 {
		return errors.New("run evidence is empty or exceeds bound")
	}
	r.mu.RLock()
	daemon, generation := r.daemonID, r.generation
	r.mu.RUnlock()
	return r.Client.DoWithHeaders(ctx, "POST", "/api/work-orders/"+url.PathEscape(workOrderID)+"/evidence",
		map[string]any{"kind": "text", "reference": answer, "run_id": runID}, nil,
		map[string]string{"X-Aeon-Daemon-ID": daemon, "X-Aeon-Daemon-Generation": generation})
}

func (r *Remote) Probe(ctx context.Context, accountID, daemonID, generation string, available bool) error {
	status := ProbeStatus{OK: available}
	if !available {
		status.Failure = ProbeUnavailable
	}
	return r.ProbeStatus(ctx, accountID, daemonID, generation, status)
}

// ProbeStatusReporter is implemented by API clients that also send why a probe
// failed; older fakes keep the boolean Probe.
type ProbeStatusReporter interface {
	ProbeStatus(ctx context.Context, accountID, daemonID, generation string, status ProbeStatus) error
}

// ProbeStatus reports a probe and, when it failed, only its cause category.
func (r *Remote) ProbeStatus(ctx context.Context, accountID, daemonID, generation string, status ProbeStatus) error {
	// Keep historical server probe causes unchanged. The richer unverified
	// reason travels in lifecycle details; it never implies a vendor sign-out.
	if !status.OK && status.Failure == ProbeUnverified {
		status.Failure = ProbeUnavailable
	}
	body := map[string]any{"daemon_id": daemonID, "daemon_generation": generation, "available": status.OK}
	if !status.OK && (status.Failure == ProbeAuthFailed || status.Failure == ProbeUnavailable) {
		body["failure"] = status.Failure
	}
	if host, err := os.Hostname(); err == nil && host != "" && len(host) <= 128 {
		body["host_label"] = host
	}
	if status.OpenRouterCredits != nil {
		body["openrouter_credits"] = status.OpenRouterCredits
	}
	path := "/api/agent-accounts/" + url.PathEscape(accountID) + "/probe"
	err := r.Client.Do(ctx, "POST", path, body, nil)
	if err != nil && body["failure"] != nil {
		// A server from before probe causes rejects the extra field; the boolean
		// probe must still land so routing sees the failure.
		delete(body, "failure")
		return r.Client.Do(ctx, "POST", path, body, nil)
	}
	return err
}

// ValidateBaseURL rejects credential-bearing and remote cleartext endpoints.
func ValidateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(raw) != raw {
		return errors.New("invalid AEON URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1") {
		return nil
	}
	return errors.New("AEON URL must use HTTPS outside loopback")
}

// RefuseVerification reports no-launch evidence through the existing claim path.
func (r *Remote) RefuseVerification(ctx context.Context, run, daemon, generation, reason string) error {
	return r.Client.Do(ctx, "POST", "/api/runs/"+url.PathEscape(run)+"/claim", map[string]any{
		"daemon_id": daemon, "daemon_generation": generation, "reservation_ids": []string{}, "verification_unavailable": reason,
	}, nil)
}
