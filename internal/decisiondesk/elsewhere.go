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

// AEON-1495: Waiting on you across instances (owner only), the asker on each
// Desk line and the person's upcoming steps (Coming).
//
// Trust contexts: no server ever reads another instance. This instance tells
// its workspace owner which siblings exist (deployment configuration) and
// answers the owner's own summary read from a sibling's page in the owner's
// browser. Nothing from a sibling is stored or logged here, agents and keys
// never see a sibling, and the summary carries first lines only.

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

// PeekIssuer copies the owner's browser session into the cross-site summary
// cookie, or clears it (auth.Module.IssueDeskPeek).
type PeekIssuer interface {
	IssueDeskPeek(w http.ResponseWriter, r *http.Request, owner bool)
}

// WithInstances names this instance and its siblings.
func (m *Module) WithInstances(instances config.Instances, peek PeekIssuer) *Module {
	m.instances, m.peek = instances, peek
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

func workspaceOwnerTx(ctx context.Context, tx pgx.Tx, p tenant.Principal) (bool, error) {
	if p.Kind != tenant.Person || p.KeyCreatorID != "" {
		return false, nil
	}
	var owner bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM role_bindings b
	 JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
	 JOIN principals person ON person.tenant_id=b.tenant_id AND person.id=b.principal_id AND person.kind='person' AND person.status='active'
	 WHERE b.tenant_id=$1 AND b.principal_id=$2 AND b.scope_type='workspace' AND r.key='owner')`, p.TenantID, p.ID).Scan(&owner)
	return owner, err
}

// Instances is this instance's tag and, for the workspace owner only, the
// sibling instances the owner's browser may read.
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
	if p.Kind != tenant.Person || p.KeyCreatorID != "" || !p.BrowserSession || r.Header.Get("Authorization") != "" {
		httpapi.WriteError(w, 403, "a signed-in person is required")
		return p, false
	}
	return p, true
}

func (m *Module) ownerTx(ctx context.Context, p tenant.Principal) (bool, error) {
	var owner bool
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		owner, err = workspaceOwnerTx(ctx, tx, p)
		return err
	})
	return owner, err
}

func (m *Module) listInstances(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := browserPerson(w, r)
	if !ok {
		return
	}
	out := Instances{Here: m.instances.Here, Siblings: []config.Sibling{}}
	owner := false
	if len(m.instances.Siblings) > 0 {
		var err error
		if owner, err = m.ownerTx(r.Context(), p); err != nil {
			httpapi.WriteInternalError(w, r, "decision_desk", err, "instances could not be read")
			return
		}
	}
	if owner {
		out.Siblings = slices.Clone(m.instances.Siblings)
	}
	if m.peek != nil {
		m.peek.IssueDeskPeek(w, r, owner)
	}
	httpapi.WriteJSON(w, 200, out)
}

// OwnerSummary is the owner's first lines, read by a sibling's page.
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
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	ProjectKey string `json:"project_key,omitempty"`
	Title      string `json:"title"`
	AskedBy    string `json:"asked_by,omitempty"`
	Held       bool   `json:"held"`
	Href       string `json:"href"`
}

var errNotOwner = errors.New("not the workspace owner")

func (m *Module) ownerSummary(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Origin")
	if origin := r.Header.Get("Origin"); origin != "" && len(r.Header.Values("Origin")) == 1 && slices.Contains(m.instances.Origins(), origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	p, ok := browserPerson(w, r)
	if !ok {
		return
	}
	if len(m.instances.Siblings) == 0 {
		httpapi.WriteError(w, 404, "this instance has no sibling instances")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	out := OwnerSummary{Instance: m.instances.Here, Items: []SummaryItem{}}
	err := db.InTenantReadSnapshot(db.AllProjects(ctx, "decision desk: the owner's own summary for a sibling instance"), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		owner, err := workspaceOwnerTx(ctx, tx, p)
		if err != nil {
			return err
		}
		if !owner {
			return errNotOwner
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
			line := SummaryItem{ID: item.ID, Kind: item.Kind, ProjectKey: keys[item.ProjectID], Title: clip(item.Title, maxSummaryTitle), Held: item.Held, Href: item.Href}
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
	if errors.Is(err, errNotOwner) {
		httpapi.WriteError(w, 403, "only the workspace owner reads this summary")
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
