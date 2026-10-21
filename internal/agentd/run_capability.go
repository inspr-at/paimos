// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/inspr-at/paimos/internal/db"
)

type runCapabilityContextKey struct{}
type capabilityTransport struct {
	remote *Remote
	base   http.RoundTripper
}

func (t capabilityTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, explicit := req.Context().Value(runCapabilityContextKey{}).(string)
	if !explicit {
		parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
		if len(parts) >= 5 && parts[1] == "projects" && parts[3] == "harness-sessions" {
			t.remote.mu.RLock()
			token = t.remote.runCapabilities[t.remote.sessionRuns[parts[4]]]
			t.remote.mu.RUnlock()
		}
	}
	copy := req.Clone(req.Context())
	// Caller-supplied headers cannot select a sibling capability.
	copy.Header.Del(db.RunCapabilityHeader)
	copy.Header.Set(db.RunCapabilityPolicyHeader, db.RunCapabilityPolicy)
	if token != "" {
		copy.Header.Set(db.RunCapabilityHeader, token)
	}
	response, err := t.base.RoundTrip(copy)
	if response != nil {
		response.Request = copy
	}
	return response, err
}

func (r *Remote) RunContext(ctx context.Context, runID string) context.Context {
	r.mu.RLock()
	token := r.runCapabilities[runID]
	r.mu.RUnlock()
	return context.WithValue(ctx, runCapabilityContextKey{}, token)
}

// PrepareRunContent is the sole acquisition point. Proofs live only in daemon
// memory and are never serialized in Run, Record, prompts or child settings.
func (r *Remote) PrepareRunContent(ctx context.Context, run Run, daemon, generation string) (outRun Run, outNode Node, outOrder WorkOrder, resultErr error) {
	stored := false
	defer func() {
		if stored && resultErr != nil {
			r.forgetRunContent(run.ID)
		}
	}()
	if run.RunCapabilityRequired {
		r.mu.RLock()
		capacityFull := len(r.runCapabilities) >= 256 && r.runCapabilities[run.ID] == ""
		r.mu.RUnlock()
		if capacityFull {
			return Run{}, Node{}, WorkOrder{}, errors.New("run content credential capacity reached")
		}
		var grant struct {
			Capability string `json:"capability"`
			RunID      string `json:"run_id"`
			PersonID   string `json:"acting_person_id"`
			Daemon     string `json:"daemon_id"`
			Generation string `json:"daemon_generation"`
		}
		err := r.Client.DoWithHeaders(ctx, "POST", "/api/runs/"+url.PathEscape(run.ID)+"/capability",
			map[string]string{"daemon_id": daemon, "daemon_generation": generation}, &grant,
			map[string]string{db.RunCapabilityPolicyHeader: db.RunCapabilityPolicy})
		if err != nil {
			return Run{}, Node{}, WorkOrder{}, err
		}
		if len(grant.Capability) != 64 || grant.RunID != run.ID || grant.PersonID == "" || grant.PersonID != run.ActingPersonID || grant.Daemon != daemon || grant.Generation != generation {
			return Run{}, Node{}, WorkOrder{}, ErrScope
		}
		r.mu.Lock()
		if r.runCapabilities == nil {
			r.runCapabilities = map[string]string{}
		}
		r.runCapabilities[run.ID] = grant.Capability
		stored = true
		r.mu.Unlock()
		ctx = r.RunContext(ctx, run.ID)
		full, err := r.GetRun(ctx, run.ID)
		if err != nil {
			return Run{}, Node{}, WorkOrder{}, err
		}
		if full.ID != run.ID || full.WorkOrderID != run.WorkOrderID || full.AgentPrincipalID != run.AgentPrincipalID || full.ActingPersonID != run.ActingPersonID || full.Status != "queued" {
			return Run{}, Node{}, WorkOrder{}, ErrScope
		}
		run = full
	}
	node, err := r.Node(ctx, run.WorkOrderID)
	if err != nil {
		return Run{}, Node{}, WorkOrder{}, err
	}
	order, err := r.WorkOrder(ctx, run.WorkOrderID)
	return run, node, order, err
}

type runToolRemote struct {
	*Remote
	runID string
}

func (r *Remote) RunToolsFor(runID string) RunToolAPI { return runToolRemote{r, runID} }
func (r runToolRemote) WorkOrder(ctx context.Context, id string) (WorkOrder, error) {
	return r.Remote.WorkOrder(r.RunContext(ctx, r.runID), id)
}
func (r runToolRemote) Comment(ctx context.Context, id, body string) error {
	return r.Remote.Comment(r.RunContext(ctx, r.runID), id, body)
}
func (r runToolRemote) SetWorkStatus(ctx context.Context, id string, rev int64, status string) (WorkOrder, error) {
	return r.Remote.SetWorkStatus(r.RunContext(ctx, r.runID), id, rev, status)
}
func (r runToolRemote) CheckCriterion(ctx context.Context, id, criterion string, checked bool) error {
	return r.Remote.CheckCriterion(r.RunContext(ctx, r.runID), id, criterion, checked)
}
func (r runToolRemote) Evidence(ctx context.Context, id, run, criterion, reference string) error {
	return r.Remote.Evidence(r.RunContext(ctx, r.runID), id, run, criterion, reference)
}
func (r runToolRemote) RequestApproval(ctx context.Context, run string, in ApprovalRequest) error {
	return r.Remote.RequestApproval(r.RunContext(ctx, r.runID), run, in)
}
func (r runToolRemote) ReplyInbox(ctx context.Context, id string, target InboxReplyTarget, body, key string) error {
	return r.Remote.ReplyInbox(r.RunContext(ctx, r.runID), id, target, body, key)
}

func (r *Remote) forgetRunContent(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.runCapabilities, runID)
	for session, run := range r.sessionRuns {
		if run == runID {
			delete(r.sessionRuns, session)
		}
	}
}
