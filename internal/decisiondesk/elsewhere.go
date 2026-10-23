// SPDX-License-Identifier: AGPL-3.0-only

package decisiondesk

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// AEON-1709: browser-only instance connections. Each home instance serves only
// the connected person's first lines; no server reads another instance.

// AskedBy names the principal who asked (the source principal).
type AskedBy struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// UpcomingStep is a person step owned by the reader that is not due yet: a
// human check on work that has not reached Done (the Goals band's Coming line).
type UpcomingStep struct {
	NodeID    string `json:"node_id"`
	CheckID   string `json:"check_id"`
	ProjectID string `json:"project_id"`
	TicketKey string `json:"ticket_key"`
	Label     string `json:"label"`
}

// WithInstances names this instance and its configured siblings.
func (m *Module) WithInstances(instances config.Instances, publicOrigin ...string) *Module {
	m.instances = instances
	if len(publicOrigin) > 0 {
		m.publicOrigin = strings.TrimRight(publicOrigin[0], "/")
	}
	return m
}

const (
	maxUpcoming      = 5
	maxSummaryItems  = 5
	maxSummaryTitle  = 200
	maxUpcomingLabel = 200
)

// Askers for the page's questions (their first asker) and approvals (the
// asking agent). Other sources name no asker.
const askersSQL = `SELECT 'question',a.question_id::text,p.id::text,p.name FROM (
 SELECT DISTINCT ON (question_id) question_id,principal_id FROM desk_askers
 WHERE tenant_id=$1 AND question_id=ANY($2::uuid[]) ORDER BY question_id,created_at,id) a
 JOIN principals p ON p.tenant_id=$1 AND p.id=a.principal_id
UNION ALL
SELECT 'approval',r.id::text,p.id::text,p.name FROM approval_requests r
 JOIN principals p ON p.tenant_id=r.tenant_id AND p.id=r.agent_principal_id
 WHERE r.tenant_id=$1 AND r.id=ANY($3::uuid[])`

func askersTx(ctx context.Context, tx pgx.Tx, tenantID string, items []Item) error {
	questions, approvals := []string{}, []string{}
	for _, item := range items {
		switch item.Kind {
		case "question":
			questions = append(questions, item.ID)
		case "approval":
			approvals = append(approvals, item.ID)
		}
	}
	if len(questions)+len(approvals) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, askersSQL, tenantID, questions, approvals)
	if err != nil {
		return err
	}
	defer rows.Close()
	askers := map[string]AskedBy{}
	for rows.Next() {
		var kind, id string
		var who AskedBy
		if err := rows.Scan(&kind, &id, &who.ID, &who.Name); err != nil {
			return err
		}
		askers[kind+":"+id] = who
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range items {
		if who, ok := askers[items[i].Kind+":"+items[i].ID]; ok {
			items[i].AskedBy = &who
		}
	}
	return nil
}

// Human checks the reader owns on work that is not done yet. Owner routing
// matches aeon_person_steps: the latest reassignment, else the assignee,
// through aeon_person_step_owner. Bounded before any decoding.
const upcomingSQL = `SELECT n.id::text,n.project_id::text,coalesce(n.key,''),left(btrim(n.human_check),$3) FROM nodes n
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 JOIN nodes project ON project.tenant_id=n.tenant_id AND project.id=n.project_id AND project.deleted_at IS NULL
 WHERE n.tenant_id=$1 AND n.deleted_at IS NULL AND k.slug IN ('work','ticket','task')
 AND n.human_check IS NOT NULL AND btrim(n.human_check)<>''
 AND aeon_work_status_category(n.state,k.field_schema) IN ('open','in_progress','blocked')
 AND aeon_person_step_owner(n.project_id,coalesce(
   (SELECT (e.after->>'owner_id')::uuid FROM events e WHERE e.tenant_id=n.tenant_id AND e.node_id=n.id
    AND e.type='node.person_step_reassigned' AND e.after->>'check_id'='human' ORDER BY e.id DESC LIMIT 1),
   CASE WHEN coalesce(n.fields->>'assignee','') ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
    THEN (n.fields->>'assignee')::uuid END),NULL)=$2::uuid
 ORDER BY n.updated_at DESC,n.id LIMIT 100`

// upcomingTx lists at most maxUpcoming steps in projects the reader can read.
func upcomingTx(ctx context.Context, tx pgx.Tx, p tenant.Principal) ([]UpcomingStep, error) {
	check, err := authz.ProjectsTx(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, upcomingSQL, p.TenantID, p.ID, maxUpcomingLabel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	steps := []UpcomingStep{}
	for rows.Next() {
		step := UpcomingStep{CheckID: "human"}
		if err := rows.Scan(&step.NodeID, &step.ProjectID, &step.TicketKey, &step.Label); err != nil {
			return nil, err
		}
		if len(steps) < maxUpcoming && check("nodes.read", step.ProjectID) {
			steps = append(steps, step)
		}
	}
	return steps, rows.Err()
}

// Instances lists the configured siblings for any active signed-in person.
type Instances struct {
	Here     string           `json:"here"`
	Siblings []config.Sibling `json:"siblings"`
}

func browserPerson(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, 401, "authentication required")
		return p, false
	}
	if p.Kind != tenant.Person || p.KeyCreatorID != "" || p.AuthKeyID != "" || p.KeyID != "" || !p.BrowserSession || r.Header.Get("Authorization") != "" {
		httpapi.WriteError(w, 403, "a signed-in person is required")
		return p, false
	}
	return p, true
}

func (m *Module) listInstances(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := browserPerson(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	err := db.InTenantReadSnapshot(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		return authz.RequireTx(ctx, tx, p, "profile.read", authz.Scope{AnyProject: true})
	})
	if err != nil {
		connectionError(w, r, err)
		return
	}
	siblings := slices.Clone(m.instances.Siblings)
	if siblings == nil {
		siblings = []config.Sibling{}
	}
	httpapi.WriteJSON(w, 200, Instances{Here: m.instances.Here, Siblings: siblings})
}

// OwnerSummary retains the wire name; its contents belong to the connected person.
type OwnerSummary struct {
	Instance string        `json:"instance"`
	Items    []SummaryItem `json:"items"`
	Counts   struct {
		Open int `json:"open"`
		Held int `json:"held"`
	} `json:"counts"`
	Upcoming *UpcomingStep `json:"upcoming,omitempty"`
	AsOf     time.Time     `json:"as_of"`
}

// SummaryItem is one first line: no context, findings, options or answers.
type SummaryItem struct {
	CreatedAt  time.Time `json:"created_at"`
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	ProjectKey string    `json:"project_key,omitempty"`
	Title      string    `json:"title"`
	AskedBy    string    `json:"asked_by,omitempty"`
	Held       bool      `json:"held"`
	Href       string    `json:"href"`
}

func (m *Module) ownerSummary(w http.ResponseWriter, r *http.Request) {
	if !m.summaryOrigin(w, r) {
		return
	}
	p, hash, ok := summaryCredential(r)
	if !ok {
		httpapi.WriteError(w, 401, "summary connection required")
		return
	}
	// The parsed identity has no authority until the exact hash, origin, expiry
	// and live person permission are verified inside this transaction.
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(r.Context(), p), 8*time.Second)
	defer cancel()
	ctx = db.WithAdditionalTenantGuard(ctx, func(ctx context.Context, tx pgx.Tx, tid string) error {
		if err := db.LockTenant(ctx, tx, tid); err != nil {
			return err
		}
		var id string
		err := tx.QueryRow(ctx, `SELECT c.id::text FROM desk_instance_connections c
   JOIN principals p ON p.tenant_id=c.tenant_id AND p.id=c.principal_id AND p.kind='person' AND p.status='active'
   WHERE c.tenant_id=$1 AND c.principal_id=$2 AND c.token_hash=$3 AND c.origin=$4 AND c.revoked_at IS NULL AND c.expires_at>clock_timestamp()`, p.TenantID, p.ID, hash, r.Header.Get("Origin")).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConnectionEnded
		}
		if err != nil {
			return err
		}
		if err = authz.RequireTx(ctx, tx, p, "profile.read", authz.Scope{AnyProject: true}); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE desk_instance_connections SET last_used_at=now() WHERE tenant_id=$1 AND id=$2`, tid, id)
		return err
	})
	out := OwnerSummary{Instance: m.instances.Here, Items: []SummaryItem{}}
	// READ COMMITTED is deliberate: visibility setup can precede a wait for the
	// access fence. A repeatable-read snapshot taken before that wait could
	// retain a revoked grant or role. Under the fence every authority query sees
	// current committed rows; ReadTx obtains items and counts in one statement.
	err := db.InTenant(db.AllProjects(ctx, "decision desk: connected person summary reauthorized per source"), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		page, err := ReadTx(ctx, tx, p, maxSummaryItems, nil, m.routineRuntime)
		if err != nil {
			return err
		}
		if err = askersTx(ctx, tx, p.TenantID, page.Items); err != nil {
			return err
		}
		keys, err := projectKeysTx(ctx, tx, p.TenantID, page.Items)
		if err != nil {
			return err
		}
		upcoming, err := upcomingTx(ctx, tx, p)
		if err != nil {
			return err
		}
		out.Counts.Open, out.Counts.Held, out.AsOf = page.Counts.Open, page.Counts.Held, page.AsOf
		if len(upcoming) > 0 {
			out.Upcoming = &upcoming[0]
		}
		for _, item := range page.Items {
			line := SummaryItem{ID: item.ID, Kind: item.Kind, ProjectKey: keys[item.ProjectID], Title: clip(item.Title, maxSummaryTitle), CreatedAt: item.CreatedAt, Held: item.Held, Href: item.Href}
			if item.AskedBy != nil {
				line.AskedBy = item.AskedBy.Name
			}
			if !strings.HasPrefix(line.Href, "/") || strings.HasPrefix(line.Href, "//") {
				line.Href = "/decision-desk"
			}
			out.Items = append(out.Items, line)
		}
		return nil
	})
	if errors.Is(err, errConnectionEnded) || errors.Is(err, authz.ErrForbidden) {
		httpapi.WriteError(w, 401, "connection ended")
		return
	}
	if errors.Is(err, ErrCoverage) || errors.Is(err, ErrPersonStepCoverage) {
		httpapi.WriteError(w, 422, err.Error())
		return
	}
	if err != nil {
		httpapi.WriteInternalError(w, r, "decision_desk", err, "the summary could not be read")
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func projectKeysTx(ctx context.Context, tx pgx.Tx, tenantID string, items []Item) (map[string]string, error) {
	ids := []string{}
	for _, item := range items {
		if item.ProjectID != "" && !slices.Contains(ids, item.ProjectID) {
			ids = append(ids, item.ProjectID)
		}
	}
	keys := map[string]string{}
	if len(ids) == 0 {
		return keys, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text,coalesce(key,'') FROM nodes WHERE tenant_id=$1 AND id=ANY($2::uuid[]) AND deleted_at IS NULL`, tenantID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, err
		}
		keys[id] = key
	}
	return keys, rows.Err()
}

func clip(text string, max int) string {
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	return string([]rune(text)[:max-1]) + "…"
}
