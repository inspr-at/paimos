// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/statusautopilot"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/ticketbenefits"
	"github.com/jackc/pgx/v5"
)

var mergeTicketKey = regexp.MustCompile(`(?i)\bAEON-[1-9][0-9]*\b`)

func mergeKeys(title, branch string) ([]string, error) {
	if len(title) > 4096 || len(branch) > 255 {
		return nil, errRead
	}
	keys := mergeTicketKey.FindAllString(title+" "+branch, 51)
	if len(keys) > 50 {
		return nil, errRead
	}
	for i := range keys {
		keys[i] = strings.ToUpper(keys[i])
	}
	slices.Sort(keys)
	return slices.Compact(keys), nil
}

// Completion runs only after the original ingress has authenticated and
// accepted. Its transaction/receipts are independent of the disposable
// delivery projection, so a crash or redelivery retries the unfinished work.
func (m *Module) mergeWebhook(w http.ResponseWriter, r *http.Request) {
	reader, enabled := m.github.(MergeReader)
	if !enabled {
		m.auditWebhook(w, r)
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	raw, err := io.ReadAll(io.LimitReader(r.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		class := webhookClass("body_too_large")
		if err != nil {
			class = "body_read"
		}
		m.rejectWebhook(w, r, "unknown", "parse", 413, class)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	r = r.WithContext(ctx)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	response := &auditResponse{header: w.Header()}
	m.auditWebhook(response, r)
	if response.status != 204 {
		w.WriteHeader(response.status)
		return
	}
	m.finishMergeWebhook(w, r, raw, reader)
}

// The authenticated audit phase has accepted before merge completion starts.
func (m *Module) finishMergeWebhook(w http.ResponseWriter, r *http.Request, raw []byte, reader MergeReader) {
	ctx := r.Context()
	var e envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		m.rejectWebhook(w, r, webhookLogAction(raw, r.Header.Get("X-GitHub-Event")), "parse", 400, webhookClass("invalid_json"))
		return
	}
	var err error
	step := ""
	facts := []MergeFact{}
	switch r.Header.Get("X-GitHub-Event") {
	case "pull_request":
		if e.Action == "closed" {
			step = "reader-MergedPull"
			var f *MergeFact
			f, err = reader.MergedPull(ctx, e.Pull.Number)
			if f != nil {
				facts = append(facts, *f)
			}
		}
	case "merge_group":
		if e.Action == "destroyed" {
			step = "reader-completedGroup"
			facts, err = m.completedGroup(ctx, reader, e.Group.Head)
		}
	case "push":
		if e.Ref == "refs/heads/main" {
			step = "reader-MergedGroup"
			facts, err = reader.MergedGroup(ctx, e.After)
		}
	}
	if err == nil && len(facts) > 0 {
		step = "completeMerges"
		err = m.completeMerges(ctx, facts)
	}
	if err != nil {
		m.rejectWebhook(w, r, e.Action, step, 502, err)
		return
	}
	w.WriteHeader(204)
}

// Queue refs can disappear before destroyed is delivered. The authenticated
// checks_requested ledger retains every constituent; confirm each through the
// canonical pull endpoint, including distinct merge SHAs in one group.
func (m *Module) completedGroup(ctx context.Context, reader MergeReader, head string) ([]MergeFact, error) {
	var raw []byte
	err := db.InTenant(db.AllProjects(ctx, "merge group completion subjects"), m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT observations FROM delivery_github_events WHERE repository=$1 AND event='merge_group' AND action='checks_requested' AND head_sha=$2 ORDER BY sequence DESC LIMIT 1`, m.config.Repository, head).Scan(&raw)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return reader.MergedGroup(ctx, head)
	}
	if err != nil {
		return nil, err
	}
	var observations []Observation
	if json.Unmarshal(raw, &observations) != nil || len(observations) > 100 {
		return nil, errRead
	}
	if len(observations) == 0 {
		return reader.MergedGroup(ctx, head)
	}
	out := []MergeFact{}
	for _, o := range observations {
		if o.PR == nil {
			return nil, errRead
		}
		f, err := reader.MergedPull(ctx, *o.PR)
		if err != nil {
			return nil, err
		}
		if f != nil {
			out = append(out, *f)
		}
	}
	return out, nil
}

type mergeEntry struct {
	Ticket         string    `json:"ticket_node_id"`
	Key            string    `json:"key"`
	Project        *string   `json:"project_id"`
	PR             int64     `json:"pull_request"`
	SHA            string    `json:"merge_sha"`
	Head           string    `json:"head_sha"`
	MergedAt       time.Time `json:"merged_at"`
	Revision       time.Time `json:"revision"`
	From           string    `json:"from"`
	To             string    `json:"to"`
	Result         string    `json:"result"`
	Reason         string    `json:"reason,omitempty"`
	before         json.RawMessage
	fields         json.RawMessage
	kind, category string
}

func mergeIdentity(repo string, e mergeEntry) string {
	return digest([]byte(repo + "|" + e.SHA + "|" + e.Ticket + "|" + e.To))
}

// Resolve every title/branch reference; never infer parent completion or rely
// on the projection's single-ticket association. Lock the full sorted batch
// before any write/event, so multi-ticket PRs preserve the global lock order.
func mergeEntriesTx(ctx context.Context, tx pgx.Tx, facts []MergeFact, project string) ([]mergeEntry, error) {
	byKey := map[string]MergeFact{}
	for _, f := range facts {
		if f.PR == nil || *f.PR < 1 || !reviewgate.ValidSHA(f.SHA) || !reviewgate.ValidSHA(f.Head) || f.At.IsZero() {
			return nil, errRead
		}
		keys, err := mergeKeys(f.Title, f.Branch)
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			if prior, ok := byKey[key]; !ok || f.At.After(prior.At) {
				byKey[key] = f
			}
		}
	}
	if len(byKey) > 100 {
		return nil, errRead
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	rows, err := tx.Query(ctx, `SELECT n.id::text,n.key,n.project_id::text,n.state,n.updated_at,n.fields,k.slug,
 aeon_work_status_category(n.state,k.field_schema),to_jsonb(n)
 FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE upper(n.key)=ANY($1::text[]) AND n.deleted_at IS NULL
 AND k.slug='work' AND NOT EXISTS(SELECT 1 FROM work_orders w WHERE w.node_id=n.id)
 AND ($2='' OR n.project_id=nullif($2,'')::uuid) ORDER BY n.id FOR UPDATE OF n`, keys, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []mergeEntry{}
	for rows.Next() {
		var e mergeEntry
		if err := rows.Scan(&e.Ticket, &e.Key, &e.Project, &e.From, &e.Revision, &e.fields, &e.kind, &e.category, &e.before); err != nil {
			return nil, err
		}
		f := byKey[strings.ToUpper(e.Key)]
		e.PR, e.SHA, e.Head, e.MergedAt, e.To = *f.PR, f.SHA, f.Head, f.At, "done"
		out = append(out, e)
	}
	return out, rows.Err()
}

func mergeGateTx(ctx context.Context, tx pgx.Tx, e mergeEntry) (string, error) {
	parent, err := db.WorkStatusParentTx(ctx, tx, e.Ticket)
	if err != nil {
		return "", err
	}
	// The worker rule also protects projects whose parent-status rollout is off.
	if !parent {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes c JOIN node_kinds k ON k.id=c.kind_id WHERE c.parent_id=$1 AND c.deleted_at IS NULL AND k.slug='work')`, e.Ticket).Scan(&parent); err != nil {
			return "", err
		}
	}
	if parent {
		return "parent status follows its children", nil
	}
	if ticketbenefits.Completed(e.category) && !(e.category == "done" && e.To == "delivered") {
		return "", nil
	}
	if e.category == "cancelled" || e.category == "archived" {
		return "ticket is cancelled or archived", nil
	}
	var busy, handover bool
	var human *string
	// aeon_work_busy is live sessions, queued runs and running child orders.
	// A waiting split or cancel is aeon_work_pending after that work has stopped.
	if err := tx.QueryRow(ctx, `SELECT aeon_work_busy(id),aeon_work_pending(id) IS NOT NULL,human_check FROM nodes WHERE id=$1`, e.Ticket).Scan(&busy, &handover, &human); err != nil {
		return "", err
	}
	if handover {
		return "waiting for handover", nil
	}
	if busy {
		return "a worker is still running or waiting for handover", nil
	}
	if human != nil && strings.TrimSpace(*human) != "" {
		return "needs a human check: " + *human, nil
	}
	if issues := ticketbenefits.Transition(e.kind, e.category, "done", e.fields); len(issues) > 0 {
		return "before done: " + strings.Join(issues, "; "), nil
	}
	return "", nil
}

// mergeOwnerLimit bounds each responsible-person class. A configured lead is
// not a class: that person is the only candidate, and a missing grant refuses.
const mergeOwnerLimit = 16

// mergeOwnerTx authorizes the state change as a person who already holds
// nodes.write. A configured lead owner (project lead settings, else the lead
// principal) is authoritative: if that person lacks the grant, completion
// refuses and does not substitute anyone else. Only a project with neither
// setting falls back, in order, to an existing project role binding (admin,
// then member, then any other project role), a workspace owner, admin, or
// member, then the person who created the ticket. No role, key, or scope is
// created, and an agent is never the fallback.
func mergeOwnerTx(ctx context.Context, tx pgx.Tx, tid string, project *string, ticket string) (*tenant.Principal, error) {
	var id *string
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT owner_person_id FROM project_lead_settings WHERE project_id=$1),
 (SELECT owner_principal_id FROM project_leads WHERE project_id=$1))::text`, project).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if id != nil {
		return mergeOwnerAllowed(ctx, tx, tid, project, *id)
	}
	p, err := mergeOwnerCandidate(ctx, tx, tid, project, `SELECT p.id::text FROM role_bindings b
 JOIN principals p ON p.tenant_id=b.tenant_id AND p.id=b.principal_id
 JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
 WHERE p.kind='person' AND p.status='active' AND p.linked_to IS NULL
 AND b.scope_type='project' AND b.scope_id=$1::uuid
 GROUP BY p.id
 ORDER BY min(CASE r.key WHEN 'admin' THEN 0 WHEN 'member' THEN 1 ELSE 2 END), min(b.created_at), p.id
 LIMIT $2`, project, mergeOwnerLimit)
	if p != nil || err != nil {
		return p, err
	}
	p, err = mergeOwnerCandidate(ctx, tx, tid, project, `SELECT p.id::text FROM role_bindings b
 JOIN principals p ON p.tenant_id=b.tenant_id AND p.id=b.principal_id
 JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
 WHERE p.kind='person' AND p.status='active' AND p.linked_to IS NULL
 AND b.scope_type='workspace' AND r.key IN ('owner','admin','member')
 GROUP BY p.id
 ORDER BY min(CASE r.key WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END), min(b.created_at), p.id
 LIMIT $1`, mergeOwnerLimit)
	if p != nil || err != nil {
		return p, err
	}
	return mergeOwnerCandidate(ctx, tx, tid, project, `SELECT canonical.id::text FROM events e
 JOIN principals actor ON actor.tenant_id=e.tenant_id AND actor.id=e.actor_principal_id
 JOIN principals canonical ON canonical.tenant_id=actor.tenant_id AND canonical.id=coalesce(actor.linked_to,actor.id)
 WHERE e.node_id=$1::uuid AND e.type='node.created' AND canonical.kind='person' AND canonical.status='active' AND canonical.linked_to IS NULL
 ORDER BY e.id LIMIT 1`, ticket)
}

func mergeOwnerAllowed(ctx context.Context, tx pgx.Tx, tid string, project *string, id string) (*tenant.Principal, error) {
	p := tenant.Principal{ID: id, TenantID: tid, Kind: tenant.Person}
	err := authz.RequireTx(ctx, tx, p, "nodes.write", scope(project))
	if errors.Is(err, authz.ErrForbidden) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func mergeOwnerCandidate(ctx context.Context, tx pgx.Tx, tid string, project *string, query string, args ...any) (*tenant.Principal, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) > mergeOwnerLimit {
		return nil, errRead
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		p, err := mergeOwnerAllowed(ctx, tx, tid, project, id)
		if p != nil || err != nil {
			return p, err
		}
	}
	return nil, nil
}

// prepareMergeTx records merge evidence for leaf completion and already-Done
// leaves, retaining other fields and human checks.
// Receipt lookup and writes share the tenant/tree fence. Events are returned
// for the caller to append after the whole batch's resource locks and writes.
func prepareMergeTx(ctx context.Context, tx pgx.Tx, tid, repo string, e *mergeEntry) (*events.Change, error) {
	identity := mergeIdentity(repo, *e)
	var seen bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE node_id=$1 AND type='delivery.merge_done' AND metadata->>'merge_identity'=$2)`, e.Ticket, identity).Scan(&seen); err != nil {
		return nil, err
	}
	if seen {
		e.Result = "already_applied"
		return nil, nil
	}
	if e.Result == "refused" {
		return nil, nil
	}
	alreadyDone := e.category == "done" && e.To != "delivered"
	if ticketbenefits.Completed(e.category) && e.category != "done" {
		e.Result = "already_complete"
		return nil, nil
	}
	var after json.RawMessage
	if alreadyDone {
		// This is an evidence write, not another completion. Keep the exact
		// configured Done state and all existing marks under the same fence.
		if err := tx.QueryRow(ctx, `UPDATE nodes SET fields=jsonb_set(fields,'{merged_at}',to_jsonb($2::text)),updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1 RETURNING to_jsonb(nodes)`, e.Ticket, e.MergedAt.UTC().Format(time.RFC3339Nano)).Scan(&after); err != nil {
			return nil, err
		}
		e.Result = "already_complete"
	} else {
		if err := tx.QueryRow(ctx, `UPDATE nodes SET state=$2,fields=jsonb_set(fields,'{merged_at}',to_jsonb($3::text)),status_autopilot='{}',updated_at=greatest(clock_timestamp(),updated_at+interval '1 microsecond') WHERE id=$1 RETURNING to_jsonb(nodes)`, e.Ticket, e.To, e.MergedAt.UTC().Format(time.RFC3339Nano)).Scan(&after); err != nil {
			return nil, err
		}
		e.Result = "applied"
	}
	if _, err := tx.Exec(ctx, `UPDATE status_autopilot_proposals SET status='applied' WHERE node_id=$1 AND rule='merge_refused' AND anchor LIKE $2`, e.Ticket, identity+"/%"); err != nil {
		return nil, err
	}
	meta, _ := json.Marshal(map[string]any{"job": "merge-done", "repository": repo, "pull_request": e.PR, "merge_sha": e.SHA, "head_sha": e.Head, "merged_at": e.MergedAt, "merge_identity": identity})
	return &events.Change{Type: "delivery.merge_done", NodeID: &e.Ticket, Before: e.before, After: after, Metadata: meta}, nil
}

func flushMergeTx(ctx context.Context, tx pgx.Tx, actor tenant.Principal, repo string, entries []mergeEntry, changes []events.Change) error {
	for _, e := range entries {
		if e.Result != "refused" {
			continue
		}
		anchor := mergeIdentity(repo, e) + "/" + digest([]byte(e.Reason))
		reason := fmt.Sprintf("%s · PR #%d merged to main; %s transition refused: %s", e.Key, e.PR, e.To, e.Reason)
		if err := statusautopilot.MergeRefusalTx(ctx, tx, actor, e.Ticket, anchor, reason); err != nil {
			return err
		}
	}
	for _, c := range changes {
		// node.updated keeps existing live-update and node-history consumers.
		update := c
		update.Type = "node.updated"
		if _, err := events.Append(ctx, tx, actor, update); err != nil {
			return err
		}
		if _, err := events.Append(ctx, tx, actor, c); err != nil {
			return err
		}
	}
	return nil
}

func lockMergeEventParentsTx(ctx context.Context, tx pgx.Tx, actor tenant.Principal, project *string) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM principals WHERE id=$1 FOR KEY SHARE`, actor.ID); err != nil {
		return err
	}
	if project != nil {
		_, err := tx.Exec(ctx, `SELECT id FROM nodes WHERE id=$1 FOR KEY SHARE`, project)
		return err
	}
	return nil
}

func (m *Module) completeMerges(ctx context.Context, facts []MergeFact) error {
	if len(facts) > 100 {
		return errRead
	}
	actor, err := m.auditActor(ctx, m.config.TenantID)
	if err != nil {
		return err
	}
	return db.InTenant(db.AllProjects(ctx, "authenticated main merge completion"), m.pool, m.config.TenantID, func(tx pgx.Tx) error {
		if err := db.LockWorkTreeTx(ctx, tx); err != nil {
			return err
		}
		entries, err := mergeEntriesTx(ctx, tx, facts, "")
		if err != nil {
			return err
		}
		if err := lockMergeEventParentsTx(ctx, tx, actor, nil); err != nil {
			return err
		}
		changes := []events.Change{}
		for i := range entries {
			e := &entries[i]
			p, err := mergeOwnerTx(ctx, tx, actor.TenantID, e.Project, e.Ticket)
			if err != nil {
				return err
			}
			if p == nil {
				e.Reason = "no active project owner with nodes.write"
			} else {
				e.Reason, err = mergeGateTx(ctx, tx, *e)
			}
			if err != nil {
				return err
			}
			if e.Reason != "" {
				e.Result = "refused"
			}
			c, err := prepareMergeTx(ctx, tx, actor.TenantID, m.config.Repository, e)
			if err != nil {
				return err
			}
			if c != nil {
				changes = append(changes, *c)
			}
		}
		return flushMergeTx(ctx, tx, actor, m.config.Repository, entries, changes)
	})
}
