// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/inspr-at/paimos/internal/client"
)

// Remote uses a scoped agent key through AEON's public HTTP contract.
// NewRemote callers should pin the base URL to HTTPS or a trusted loopback.
type Remote struct {
	Client               *client.Client
	mu                   sync.RWMutex
	daemonID, generation string
}

func NewRemote(baseURL, token string) *Remote { return &Remote{Client: client.New(baseURL, token)} }

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
	RequestApproval(context.Context, string, string, string, string) error
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

func (r *Remote) RequestApproval(ctx context.Context, runID, scope, rationale, expiry string) error {
	return r.Client.Do(ctx, "POST", "/api/approvals", map[string]string{
		"scope": scope, "resource_kind": "run", "resource_id": runID,
		"run_id": runID, "rationale": rationale, "expires_at": expiry,
	}, nil)
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
	err := r.Client.Do(ctx, "GET", "/api/runs/queued?limit=100", nil, &runs)
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
	err := r.Client.Do(ctx, "POST", "/api/projects/"+url.PathEscape(s.ProjectID)+"/harness-sessions", map[string]any{
		"agent_principal_id": agentID, "run_id": runID, "ticket_node_id": orderID,
		"work_order_id": orderID, "harness": harness, "host": host,
		"management_mode": "managed", "role": "worker", "work_shape": "ship",
		"advertised_capabilities": caps, "harness_session_ref": s.ID, "worker_lease": s.Lease,
	}, &result)
	if err != nil {
		return HarnessSession{}, err
	}
	if result.ID == "" || result.ProjectID != s.ProjectID {
		return HarnessSession{}, errors.New("harness registration binding mismatch")
	}
	result.Lease = s.Lease
	return result, nil
}

func (r *Remote) harnessWorker(ctx context.Context, s HarnessSession, suffix string, body, dest any) error {
	err := r.Client.DoWithHeaders(ctx, "POST", harnessPath(s)+suffix, body, dest,
		map[string]string{"X-Aeon-Worker-Lease": s.Lease})
	var status *client.StatusError
	if errors.As(err, &status) && status.Status == 410 && status.Message == "harness generation archived" {
		return ErrHarnessArchived
	}
	return err
}

func (r *Remote) HeartbeatHarness(ctx context.Context, s HarnessSession, phase string) error {
	return r.harnessWorker(ctx, s, "/heartbeat", map[string]any{
		"phase": phase, "activity": "busy", "activity_sequence": 1, "process_ownership": s.Ownership,
	}, nil)
}

func (r *Remote) YieldHarness(ctx context.Context, s HarnessSession) ([]HarnessControl, error) {
	var result struct {
		Controls []HarnessControl `json:"controls"`
	}
	err := r.harnessWorker(ctx, s, "/yield", struct{}{}, &result)
	return result.Controls, err
}

func (r *Remote) DrainHarness(ctx context.Context, s HarnessSession) ([]HarnessDelivery, error) {
	var result []HarnessDelivery
	err := r.harnessWorker(ctx, s, "/drain", struct{}{}, &result)
	return result, err
}

func (r *Remote) CompleteHarnessControl(ctx context.Context, s HarnessSession, id, outcome, reason string) error {
	return r.harnessWorker(ctx, s, "/controls/"+url.PathEscape(id)+"/complete",
		map[string]string{"outcome": outcome, "reason": reason}, nil)
}

func (r *Remote) CompleteHarnessDelivery(ctx context.Context, s HarnessSession, d HarnessDelivery) error {
	return r.harnessWorker(ctx, s, "/complete-delivery", map[string]any{
		"delivery_id": d.ID, "cursor": d.Cursor, "effective_level": "simple",
	}, nil)
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
	err := r.Client.Do(ctx, "POST", "/api/runs/"+url.PathEscape(runID)+"/claim", map[string]any{
		"daemon_id": daemonID, "daemon_generation": generation, "reservation_ids": reservations,
	}, nil)
	if err == nil {
		r.mu.Lock()
		r.daemonID, r.generation = daemonID, generation
		r.mu.Unlock()
	}
	return err
}

func (r *Remote) Report(ctx context.Context, runID string, t Telemetry) error {
	r.mu.RLock()
	daemon, generation := r.daemonID, r.generation
	r.mu.RUnlock()
	if daemon == "" || generation == "" {
		return errors.New("run has no daemon claim")
	}
	return r.Client.DoWithHeaders(ctx, "POST", "/api/runs/"+url.PathEscape(runID)+"/telemetry", t, nil,
		map[string]string{"X-Aeon-Daemon-ID": daemon, "X-Aeon-Daemon-Generation": generation})
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
	return r.Client.Do(ctx, "POST", "/api/work-orders/"+url.PathEscape(workOrderID)+"/evidence",
		map[string]any{"kind": "text", "reference": answer, "run_id": runID}, nil)
}

func (r *Remote) Probe(ctx context.Context, accountID, daemonID, generation string, available bool) error {
	return r.Client.Do(ctx, "POST", "/api/agent-accounts/"+url.PathEscape(accountID)+"/probe",
		map[string]any{"daemon_id": daemonID, "daemon_generation": generation, "available": available}, nil)
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
