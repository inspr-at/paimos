// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

const (
	evGroupSet  = "account.group_set"
	evPinSet    = "account.pin_set"
	evRunTarget = "account.run_target"
)

// AccountGroup is one split of a vendor pool. Exclusive groups keep their
// projects inside the group; other work waits rather than using the members.
type AccountGroup struct {
	ID         string   `json:"id"`
	Harness    string   `json:"harness"`
	Name       string   `json:"name"`
	Exclusive  bool     `json:"exclusive"`
	AccountIDs []string `json:"account_ids"`
	ProjectIDs []string `json:"project_ids"`
}

type groupWrite struct {
	Harness    string    `json:"harness"`
	Name       string    `json:"name"`
	Exclusive  *bool     `json:"exclusive"`
	AccountIDs *[]string `json:"account_ids"`
	ProjectIDs *[]string `json:"project_ids"`
}

type pinWrite struct {
	TicketID  string `json:"ticket_id"`
	Harness   string `json:"harness"`
	AccountID string `json:"account_id"`
	GroupID   string `json:"group_id"`
}

// TicketPin remembers an account or a group for one ticket and harness.
type TicketPin struct {
	TicketID  string `json:"ticket_id"`
	Harness   string `json:"harness"`
	AccountID string `json:"account_id,omitempty"`
	GroupID   string `json:"group_id,omitempty"`
}

// UseAccount is the path-free handle aeon use resolves through the local agentd.
type UseAccount struct {
	AccountID        string `json:"account_id"`
	DaemonID         string `json:"daemon_id"`
	Harness          string `json:"harness"`
	Label            string `json:"label"`
	HostLabel        string `json:"host_label"`
	QuotaFingerprint string `json:"quota_fingerprint"`
}

// UseResult lists the doors of one quota. It never carries a path or a credential.
type UseResult struct {
	Accounts []UseAccount `json:"accounts"`
}

func (m *Module) groups(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	permission := "account.read"
	if r.Method == http.MethodPost {
		permission = "account.manage"
	}
	if err := m.requirePermission(r, p, permission); err != nil {
		writeErr(w, err)
		return
	}
	var in groupWrite
	if r.Method == http.MethodPost {
		if err := decodeJSON(w, r, &in); err != nil {
			writeErr(w, err)
			return
		}
	}
	var out any
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if r.Method == http.MethodGet {
			items, err := listGroups(r.Context(), tx)
			out = items
			return err
		}
		group, err := createGroup(r.Context(), tx, p, in)
		out = group
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if r.Method == http.MethodPost {
		httpapi.WriteJSON(w, http.StatusCreated, out)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) group(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.manage"); err != nil {
		writeErr(w, err)
		return
	}
	id := strings.ToLower(r.PathValue("id"))
	if !uuidRE.MatchString(id) {
		writeErr(w, fail(http.StatusNotFound, "group not found"))
		return
	}
	var in groupWrite
	if r.Method == http.MethodPatch {
		if err := decodeJSON(w, r, &in); err != nil {
			writeErr(w, err)
			return
		}
	}
	var out AccountGroup
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if r.Method == http.MethodDelete {
			return deleteGroup(r.Context(), tx, p, id)
		}
		var err error
		out, err = patchGroup(r.Context(), tx, p, id, in)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) pins(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	permission := "account.read"
	if r.Method != http.MethodGet {
		permission = "run.create"
	}
	if err := m.requirePermission(r, p, permission); err != nil {
		writeErr(w, err)
		return
	}
	var in pinWrite
	if r.Method == http.MethodPut {
		if err := decodeJSON(w, r, &in); err != nil {
			writeErr(w, err)
			return
		}
	} else {
		in.TicketID = r.URL.Query().Get("ticket_id")
		in.Harness = r.URL.Query().Get("harness")
	}
	var out []TicketPin
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		switch r.Method {
		case http.MethodGet:
			var err error
			out, err = listPins(r.Context(), tx, in.TicketID)
			return err
		case http.MethodDelete:
			return deletePin(r.Context(), tx, p, in.TicketID, in.Harness)
		default:
			return putPin(r.Context(), tx, p, in)
		}
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if r.Method == http.MethodGet {
		httpapi.WriteJSON(w, http.StatusOK, out)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) use(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	harness, label := r.URL.Query().Get("harness"), r.URL.Query().Get("label")
	projectID := r.URL.Query().Get("project_id")
	if projectID != "" && !uuidRE.MatchString(projectID) {
		writeErr(w, fail(400, "invalid project id"))
		return
	}
	accountID := strings.ToLower(r.URL.Query().Get("account_id"))
	if !validHarness(harness) || (label == "") == (accountID == "") || label != "" && !pathFreeLabel(label) || accountID != "" && !uuidRE.MatchString(accountID) {
		writeErr(w, fail(http.StatusBadRequest, "invalid account label"))
		return
	}
	if p.Kind != tenant.Person {
		// Same gate as capacity next: a paired agent sees only its own doors.
	} else if err := m.requirePermission(r, p, "account.read"); err != nil {
		writeErr(w, err)
		return
	}
	var out UseResult
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ownOnly := p.Kind == tenant.Agent
		if p.Kind == tenant.Agent {
			scopes, err := keyScopes(r.Context(), tx, r, p)
			if err != nil {
				return err
			}
			paired, err := agentpairing.PairedPrincipal(r.Context(), tx, p.ID)
			if err != nil {
				return err
			}
			reader := p
			reader.Scopes = scopes
			if hasScope(scopes, "account.read") && authz.RequireTx(r.Context(), tx, reader, "account.read", authz.Scope{}) == nil {
				ownOnly = paired
			} else if !hasScope(scopes, "account.probe") {
				return fail(http.StatusForbidden, "account read or own-account probe authority required")
			}
		}
		all, err := listAccounts(r.Context(), tx)
		if err != nil {
			return err
		}
		matched := []Account{}
		want := strings.ToLower(label)
		for _, a := range all {
			if a.Harness == harness && (accountID != "" && a.ID == accountID || accountID == "" && strings.ToLower(a.Label) == want) && (!ownOnly || a.RegisteredBy == p.ID) {
				matched = append(matched, a)
			}
		}
		if len(matched) == 0 {
			return fail(http.StatusNotFound, "account not found")
		}
		if accountID != "" {
			if err := accountuse.RequireProject(r.Context(), tx, accountID, projectID); err != nil {
				return err
			}
		}
		matched, err = applyUse(r.Context(), tx, matched, projectID)
		if err != nil {
			return err
		}
		if len(matched) == 0 {
			return fail(409, accountuse.NotAllowed)
		}
		quotas := map[string]bool{}
		for _, a := range matched {
			key := a.QuotaPoolFingerprint
			if key == "" {
				key = "account:" + a.ID
			}
			quotas[key] = true
		}
		if len(quotas) != 1 {
			return fail(http.StatusConflict, "several accounts share that label")
		}
		sort.Slice(matched, func(i, j int) bool { return matched[i].ID < matched[j].ID })
		out.Accounts = make([]UseAccount, 0, len(matched))
		for _, a := range matched {
			item := UseAccount{AccountID: a.ID, DaemonID: a.DaemonID, Harness: a.Harness, Label: a.Label, HostLabel: a.HostLabel, QuotaFingerprint: a.QuotaPoolFingerprint}
			if !publicUseAccount(item) {
				return fail(http.StatusConflict, "account label is not available")
			}
			out.Accounts = append(out.Accounts, item)
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) runTarget(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "run.create"); err != nil {
		writeErr(w, err)
		return
	}
	runID := strings.ToLower(r.PathValue("runId"))
	var in struct {
		AccountID string `json:"account_id"`
		GroupID   string `json:"group_id"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		return setRunTarget(r.Context(), tx, p, runID, strings.ToLower(in.AccountID), strings.ToLower(in.GroupID))
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func listGroups(ctx context.Context, tx pgx.Tx) ([]AccountGroup, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, harness, name, exclusive FROM account_groups ORDER BY harness, name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccountGroup{}
	for rows.Next() {
		var g AccountGroup
		if err := rows.Scan(&g.ID, &g.Harness, &g.Name, &g.Exclusive); err != nil {
			return nil, err
		}
		g.AccountIDs, g.ProjectIDs = []string{}, []string{}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := attachGroupMembers(ctx, tx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func attachGroupMembers(ctx context.Context, tx pgx.Tx, groups []AccountGroup) error {
	if len(groups) == 0 {
		return nil
	}
	index := map[string]int{}
	for i := range groups {
		index[groups[i].ID] = i
	}
	rows, err := tx.Query(ctx, `SELECT group_id::text, id::text FROM agent_accounts WHERE group_id IS NOT NULL ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var gid, id string
		if err := rows.Scan(&gid, &id); err != nil {
			return err
		}
		if i, ok := index[gid]; ok {
			groups[i].AccountIDs = append(groups[i].AccountIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	prows, err := tx.Query(ctx, `SELECT gp.group_id::text, gp.project_id::text FROM account_group_projects gp JOIN nodes n ON n.tenant_id=gp.tenant_id AND n.id=gp.project_id WHERE n.deleted_at IS NULL ORDER BY gp.project_id`)
	if err != nil {
		return err
	}
	defer prows.Close()
	for prows.Next() {
		var gid, id string
		if err := prows.Scan(&gid, &id); err != nil {
			return err
		}
		if i, ok := index[gid]; ok {
			groups[i].ProjectIDs = append(groups[i].ProjectIDs, id)
		}
	}
	return prows.Err()
}

func loadGroup(ctx context.Context, tx pgx.Tx, id string) (AccountGroup, error) {
	var g AccountGroup
	err := tx.QueryRow(ctx, `SELECT id::text, harness, name, exclusive FROM account_groups WHERE id=$1::uuid`, id).Scan(&g.ID, &g.Harness, &g.Name, &g.Exclusive)
	if isNoRows(err) {
		return AccountGroup{}, fail(http.StatusNotFound, "group not found")
	}
	if err != nil {
		return AccountGroup{}, err
	}
	one := []AccountGroup{g}
	one[0].AccountIDs, one[0].ProjectIDs = []string{}, []string{}
	if err := attachGroupMembers(ctx, tx, one); err != nil {
		return AccountGroup{}, err
	}
	return one[0], nil
}

func createGroup(ctx context.Context, tx pgx.Tx, p tenant.Principal, in groupWrite) (AccountGroup, error) {
	name, err := groupName(in.Name)
	if err != nil || !validHarness(in.Harness) {
		return AccountGroup{}, fail(http.StatusBadRequest, "invalid group")
	}
	exclusive := in.Exclusive != nil && *in.Exclusive
	accounts, err := idList(in.AccountIDs)
	if err != nil {
		return AccountGroup{}, err
	}
	projects, err := idList(in.ProjectIDs)
	if err != nil {
		return AccountGroup{}, err
	}
	if exclusive && len(projects) == 0 {
		return AccountGroup{}, fail(http.StatusBadRequest, "an exclusive group needs a project")
	}
	if err := groupRefs(ctx, tx, in.Harness, accounts, projects); err != nil {
		return AccountGroup{}, err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO account_groups(tenant_id, harness, name, exclusive) VALUES($1,$2,$3,$4) RETURNING id::text`, p.TenantID, in.Harness, name, exclusive).Scan(&id)
	if err != nil {
		return AccountGroup{}, groupDBErr(err)
	}
	if err := replaceGroupMembers(ctx, tx, id, in.Harness, accounts, projects); err != nil {
		return AccountGroup{}, err
	}
	group, err := loadGroup(ctx, tx, id)
	if err != nil {
		return AccountGroup{}, err
	}
	return group, writeEvent(ctx, tx, p, evGroupSet, nil, group)
}

func patchGroup(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, in groupWrite) (AccountGroup, error) {
	before, err := loadGroup(ctx, tx, id)
	if err != nil {
		return AccountGroup{}, err
	}
	name := before.Name
	if in.Name != "" {
		name, err = groupName(in.Name)
		if err != nil {
			return AccountGroup{}, err
		}
	}
	exclusive := before.Exclusive
	if in.Exclusive != nil {
		exclusive = *in.Exclusive
	}
	accounts := before.AccountIDs
	if in.AccountIDs != nil {
		accounts, err = idList(in.AccountIDs)
		if err != nil {
			return AccountGroup{}, err
		}
	}
	projects := before.ProjectIDs
	if in.ProjectIDs != nil {
		projects, err = idList(in.ProjectIDs)
		if err != nil {
			return AccountGroup{}, err
		}
	}
	if exclusive && len(projects) == 0 {
		// Hidden stored fences still satisfy exclusivity after a partial PATCH.
		var hidden bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_group_projects gp
 WHERE gp.group_id=$1::uuid AND NOT EXISTS(SELECT 1 FROM nodes n
 WHERE n.tenant_id=gp.tenant_id AND n.id=gp.project_id AND n.deleted_at IS NULL))`, id).Scan(&hidden); err != nil {
			return AccountGroup{}, err
		}
		if !hidden {
			return AccountGroup{}, fail(http.StatusBadRequest, "an exclusive group needs a project")
		}
	}
	if in.Harness != "" && in.Harness != before.Harness {
		return AccountGroup{}, fail(http.StatusBadRequest, "invalid group")
	}
	if err := groupRefs(ctx, tx, before.Harness, accounts, projects); err != nil {
		return AccountGroup{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE account_groups SET name=$2, exclusive=$3 WHERE id=$1::uuid`, id, name, exclusive); err != nil {
		return AccountGroup{}, groupDBErr(err)
	}
	if err := replaceGroupMembers(ctx, tx, id, before.Harness, accounts, projects); err != nil {
		return AccountGroup{}, err
	}
	group, err := loadGroup(ctx, tx, id)
	if err != nil {
		return AccountGroup{}, err
	}
	return group, writeEvent(ctx, tx, p, evGroupSet, before, group)
}

func deleteGroup(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) error {
	before, err := loadGroup(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM account_groups WHERE id=$1::uuid`, id); err != nil {
		return groupDBErr(err)
	}
	return writeEvent(ctx, tx, p, evGroupSet, before, nil)
}

func replaceGroupMembers(ctx context.Context, tx pgx.Tx, id, harness string, accounts, projects []string) error {
	if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET group_id=NULL WHERE group_id=$1::uuid AND NOT (id::text=ANY($2::text[]))`, id, accounts); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE agent_accounts SET group_id=$1::uuid WHERE id::text=ANY($2::text[]) AND harness=$3`, id, accounts, harness)
	if err != nil {
		return groupDBErr(err)
	}
	if int(tag.RowsAffected()) != len(accounts) {
		return fail(http.StatusBadRequest, "invalid account")
	}
	// RLS on nodes makes this replacement touch only visible live projects.
	// Omitted hidden memberships remain stored and never enter the response.
	if _, err := tx.Exec(ctx, `DELETE FROM account_group_projects gp WHERE group_id=$1::uuid
 AND EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=gp.tenant_id AND n.id=gp.project_id AND n.deleted_at IS NULL)`, id); err != nil {
		return err
	}
	if len(projects) == 0 {
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO account_group_projects(tenant_id, group_id, project_id) SELECT tenant_id, id, x::uuid FROM account_groups, unnest($2::text[]) AS x WHERE id=$1::uuid ON CONFLICT DO NOTHING`, id, projects)
	return groupDBErr(err)
}

func groupRefs(ctx context.Context, tx pgx.Tx, harness string, accounts, projects []string) error {
	if len(accounts) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM agent_accounts WHERE harness=$1 AND id::text=ANY($2::text[])`, harness, accounts).Scan(&n); err != nil {
			return err
		}
		if n != len(accounts) {
			return fail(http.StatusBadRequest, "invalid account")
		}
	}
	if len(projects) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='project' AND n.deleted_at IS NULL AND n.id::text=ANY($1::text[])`, projects).Scan(&n); err != nil {
			return err
		}
		if n != len(projects) {
			return fail(http.StatusBadRequest, "group project must be a project")
		}
	}
	return nil
}

func groupName(raw string) (string, error) {
	name, err := cleanText(raw, 80)
	if err != nil || strings.ContainsAny(name, "/\\") {
		return "", fail(http.StatusBadRequest, "invalid group")
	}
	return name, nil
}

func idList(in *[]string) ([]string, error) {
	if in == nil {
		return []string{}, nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, id := range *in {
		id = strings.ToLower(strings.TrimSpace(id))
		if !uuidRE.MatchString(id) {
			return nil, fail(http.StatusBadRequest, "invalid id")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func groupDBErr(err error) error {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return err
	}
	switch pe.Message {
	case "project already belongs to a group for this harness", "group already exists":
		return fail(http.StatusConflict, pe.Message)
	case "exclusive group needs a project", "group project must be a project", "group harness mismatch", "invalid group schedule", "pin target must be a ticket", "pin account harness mismatch", "pin group harness mismatch":
		return fail(http.StatusBadRequest, pe.Message)
	}
	if pe.Code == "23505" {
		return fail(http.StatusConflict, "group already exists")
	}
	return err
}

func listPins(ctx context.Context, tx pgx.Tx, ticketID string) ([]TicketPin, error) {
	if !uuidRE.MatchString(ticketID) {
		return nil, fail(http.StatusBadRequest, "invalid ticket")
	}
	rows, err := tx.Query(ctx, `SELECT p.ticket_id::text, p.harness, COALESCE(p.account_id::text,''), COALESCE(p.group_id::text,'') FROM account_ticket_pins p JOIN nodes n ON n.tenant_id=p.tenant_id AND n.id=p.ticket_id WHERE p.ticket_id=$1::uuid AND n.deleted_at IS NULL ORDER BY p.harness`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketPin{}
	for rows.Next() {
		var pin TicketPin
		if err := rows.Scan(&pin.TicketID, &pin.Harness, &pin.AccountID, &pin.GroupID); err != nil {
			return nil, err
		}
		out = append(out, pin)
	}
	return out, rows.Err()
}

func putPin(ctx context.Context, tx pgx.Tx, p tenant.Principal, in pinWrite) error {
	in.TicketID = strings.ToLower(in.TicketID)
	in.AccountID = strings.ToLower(in.AccountID)
	in.GroupID = strings.ToLower(in.GroupID)
	if !uuidRE.MatchString(in.TicketID) || !validHarness(in.Harness) {
		return fail(http.StatusBadRequest, "invalid pin")
	}
	if (in.AccountID == "") == (in.GroupID == "") || in.AccountID != "" && !uuidRE.MatchString(in.AccountID) || in.GroupID != "" && !uuidRE.MatchString(in.GroupID) {
		return fail(http.StatusBadRequest, "invalid pin")
	}
	if err := canEditPin(ctx, tx, p, in.TicketID); err != nil {
		return err
	}
	var account, group any
	if in.AccountID != "" {
		var project *string
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE id=$1`, in.TicketID).Scan(&project); err != nil {
			return err
		}
		projectID := ""
		if project != nil {
			projectID = *project
		}
		if err := accountuse.RequireProject(ctx, tx, in.AccountID, projectID); err != nil {
			return err
		}
		account = in.AccountID
	}
	if in.GroupID != "" {
		group = in.GroupID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO account_ticket_pins(tenant_id, ticket_id, harness, account_id, group_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant_id, ticket_id, harness) DO UPDATE SET account_id=EXCLUDED.account_id, group_id=EXCLUDED.group_id`, p.TenantID, in.TicketID, in.Harness, account, group); err != nil {
		return groupDBErr(err)
	}
	return writeEvent(ctx, tx, p, evPinSet, nil, TicketPin{TicketID: in.TicketID, Harness: in.Harness, AccountID: in.AccountID, GroupID: in.GroupID})
}

func deletePin(ctx context.Context, tx pgx.Tx, p tenant.Principal, ticketID, harness string) error {
	ticketID = strings.ToLower(ticketID)
	if !uuidRE.MatchString(ticketID) || !validHarness(harness) {
		return fail(http.StatusBadRequest, "invalid pin")
	}
	if err := canEditPin(ctx, tx, p, ticketID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM account_ticket_pins WHERE ticket_id=$1::uuid AND harness=$2`, ticketID, harness)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	return writeEvent(ctx, tx, p, evPinSet, TicketPin{TicketID: ticketID, Harness: harness}, nil)
}

// Pins edit a ticket, rather than a work order. Resolve under the caller's
// node RLS and require the same project write authority as a ticket edit.
func canEditPin(ctx context.Context, tx pgx.Tx, p tenant.Principal, ticketID string) error {
	var project *string
	err := tx.QueryRow(ctx, `SELECT n.project_id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1::uuid AND n.deleted_at IS NULL AND k.slug IN ('work','ticket') FOR UPDATE OF n`, ticketID).Scan(&project)
	if isNoRows(err) {
		return fail(http.StatusNotFound, "ticket not found")
	}
	if err != nil {
		return err
	}
	scope := authz.Scope{}
	if project != nil {
		scope.ProjectID = *project
	}
	if p.Kind != tenant.Person || authz.RequireTx(ctx, tx, p, "nodes.write", scope) != nil {
		return fail(http.StatusForbidden, "ticket edit permission required")
	}
	return nil
}

func setRunTarget(ctx context.Context, tx pgx.Tx, p tenant.Principal, runID, accountID, groupID string) error {
	if !uuidRE.MatchString(runID) || (accountID == "") == (groupID == "") || accountID != "" && !uuidRE.MatchString(accountID) || groupID != "" && !uuidRE.MatchString(groupID) {
		return fail(http.StatusBadRequest, "invalid run target")
	}
	var workOrder, status, purpose, harness string
	var current *string
	err := tx.QueryRow(ctx, `SELECT r.work_order_id::text, r.status, r.purpose, r.account_id::text, m.harness FROM agent_runs r JOIN model_profiles m ON m.id=r.model_profile_id WHERE r.id=$1::uuid FOR UPDATE OF r`, runID).Scan(&workOrder, &status, &purpose, &current, &harness)
	if isNoRows(err) {
		return fail(http.StatusNotFound, "run not found")
	}
	if err != nil {
		return err
	}
	order, err := workorders.Load(ctx, tx, workOrder, false)
	if err != nil {
		return err
	}
	if err := workorders.CanEdit(p, order); err != nil {
		var we *workorders.Error
		if errors.As(err, &we) {
			return fail(we.Status, we.Message)
		}
		return err
	}
	if status != "queued" || purpose != "managed" || current != nil {
		return fail(http.StatusConflict, "run is not awaiting an account")
	}
	if accountID != "" {
		if err := accountuse.RequireRun(ctx, tx, accountID, runID); err != nil {
			return err
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_accounts WHERE id=$1::uuid AND harness=$2)`, accountID, harness).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return fail(http.StatusBadRequest, "invalid account")
		}
		policy, err := modelprefs.RunRequirement(ctx, tx, runID)
		if err != nil {
			return err
		}
		var profile string
		if err := tx.QueryRow(ctx, `SELECT model_profile_id::text FROM agent_runs WHERE id=$1`, runID).Scan(&profile); err != nil {
			return err
		}
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		qualifies, err := AccountMeetsResidency(ctx, tx, accountID, profile, policy.Residency, now)
		if err != nil {
			return err
		}
		if !qualifies {
			return &httpError{status: 409, code: "residency_unmet", msg: "account is outside the allowed providers"}
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_runs SET requested_account_id=$2::uuid WHERE id=$1::uuid`, runID, accountID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM account_run_targets WHERE run_id=$1::uuid`, runID); err != nil {
			return err
		}
		return writeEvent(ctx, tx, p, evRunTarget, nil, map[string]string{"run_id": runID, "account_id": accountID})
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_groups WHERE id=$1::uuid AND harness=$2)`, groupID, harness).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return fail(http.StatusBadRequest, "invalid group")
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_runs SET requested_account_id=NULL WHERE id=$1::uuid`, runID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO account_run_targets(tenant_id, run_id, group_id) VALUES($1,$2,$3) ON CONFLICT(tenant_id, run_id) DO UPDATE SET group_id=EXCLUDED.group_id`, p.TenantID, runID, groupID); err != nil {
		return err
	}
	return writeEvent(ctx, tx, p, evRunTarget, nil, map[string]string{"run_id": runID, "group_id": groupID})
}

func pathFreeLabel(s string) bool {
	if s == "" || len(s) > 128 || strings.ContainsAny(s, "/\\\x00\r\n") {
		return false
	}
	lower := strings.ToLower(s)
	return !strings.Contains(lower, "codex_home") && !strings.Contains(lower, "claude_config")
}

func publicUseAccount(a UseAccount) bool {
	if !uuidRE.MatchString(a.AccountID) || !validHarness(a.Harness) || !pathFreeLabel(a.Label) || !pathFreeLabel(a.DaemonID) {
		return false
	}
	if a.HostLabel != "" && !pathFreeLabel(a.HostLabel) {
		return false
	}
	if a.QuotaFingerprint != "" && !fingerprintRE.MatchString(a.QuotaFingerprint) {
		return false
	}
	// Validate values, not JSON escapes: a quoted display label legitimately
	// serializes with backslashes and must still resolve by its stable ID.
	lower := strings.ToLower(strings.Join([]string{a.AccountID, a.DaemonID, a.Harness, a.Label, a.HostLabel, a.QuotaFingerprint}, " "))
	for _, bad := range []string{"/users", "codex_home", "claude_config", "socket", "\\"} {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	return true
}

var fingerprintRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

func fillGroupNames(ctx context.Context, tx pgx.Tx, accounts []Account) error {
	if len(accounts) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text, name FROM account_groups`)
	if err != nil {
		return err
	}
	defer rows.Close()
	names := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		names[id] = name
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range accounts {
		accounts[i].GroupName = names[accounts[i].GroupID]
	}
	return nil
}

type groupFence struct {
	exclusive bool
	projects  map[string]bool
}

// narrowCandidates applies the project fence, then a run or ticket pin.
// A pin never widens a fence. An empty result waits; it does not spill.
func narrowCandidates(ctx context.Context, tx pgx.Tx, run runRow, harness string, accounts []Account) ([]Account, bool, error) {
	if run.Purpose == "pairing_verification" {
		kept := []Account{}
		for _, a := range accounts {
			if err := accountuse.RequireRun(ctx, tx, a.ID, run.ID); err != nil {
				var denied *accountuse.Error
				if errors.As(err, &denied) && denied.Message == accountuse.NotAllowed {
					continue
				}
				return nil, false, err
			}
			kept = append(kept, a)
		}
		return kept, false, nil
	}
	projectID, err := runProjectID(ctx, tx, run.ID)
	if err != nil {
		return nil, false, err
	}
	fences, err := loadFences(ctx, tx, harness)
	if err != nil {
		return nil, false, err
	}
	kept, err := applyUse(ctx, tx, applyFence(accounts, fences, projectID), projectID)
	if err != nil {
		return nil, false, err
	}
	policy, err := modelprefs.RunRequirement(ctx, tx, run.ID)
	if err != nil {
		return nil, false, err
	}
	req := modelprefs.Strictest(policy.Residency, run.Residency)
	now, err := dbNow(ctx, tx)
	if err != nil {
		return nil, false, err
	}
	profile := ""
	if run.ProfileID != nil {
		profile = *run.ProfileID
	}
	kept, residencyEmptied, err := applyResidency(ctx, tx, kept, profile, req, now)
	if err != nil {
		return nil, false, err
	}
	if run.RequestedAccountID != nil && *run.RequestedAccountID != "" {
		return keepAccount(kept, *run.RequestedAccountID), residencyEmptied, nil
	}
	var target *string
	err = tx.QueryRow(ctx, `SELECT group_id::text FROM account_run_targets WHERE run_id=$1::uuid`, run.ID).Scan(&target)
	if err != nil && !isNoRows(err) {
		return nil, false, err
	}
	if target != nil && *target != "" {
		return keepGroup(kept, *target), residencyEmptied, nil
	}
	ticketID, err := runTicketID(ctx, tx, run.ID)
	if err != nil || ticketID == "" {
		return kept, residencyEmptied, err
	}
	var accountID, groupID *string
	err = tx.QueryRow(ctx, `SELECT account_id::text, group_id::text FROM account_ticket_pins WHERE ticket_id=$1::uuid AND harness=$2`, ticketID, harness).Scan(&accountID, &groupID)
	if isNoRows(err) {
		return kept, residencyEmptied, nil
	}
	if err != nil {
		return nil, false, err
	}
	if accountID != nil && *accountID != "" {
		return keepAccount(kept, *accountID), residencyEmptied, nil
	}
	if groupID != nil && *groupID != "" {
		return keepGroup(kept, *groupID), residencyEmptied, nil
	}
	return kept, residencyEmptied, nil
}

// The routing agent often has no project grant. The fence still has to know
// which project the run sits in, then the caller's visibility is put back
// before any later statement can read a node.
func withAllProjects(ctx context.Context, tx pgx.Tx, query string, runID string) (string, error) {
	var prior string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects', true), '')`).Scan(&prior); err != nil {
		return "", err
	}
	if prior != "*" {
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true)`); err != nil {
			return "", err
		}
	}
	var id *string
	qerr := tx.QueryRow(ctx, query, runID).Scan(&id)
	if prior != "*" {
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true)`, prior); err != nil && (qerr == nil || isNoRows(qerr)) {
			return "", err
		}
	}
	if isNoRows(qerr) {
		return "", nil
	}
	if qerr != nil {
		return "", qerr
	}
	if id == nil {
		return "", nil
	}
	return *id, nil
}

func runProjectID(ctx context.Context, tx pgx.Tx, runID string) (string, error) {
	return withAllProjects(ctx, tx, `SELECT n.project_id::text FROM agent_runs r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_order_id WHERE r.id=$1::uuid`, runID)
}

func runTicketID(ctx context.Context, tx pgx.Tx, runID string) (string, error) {
	return withAllProjects(ctx, tx, `
		WITH RECURSIVE up AS (
			SELECT n.tenant_id, n.id, n.parent_id, k.slug, 0 AS depth
			FROM agent_runs r
			JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_order_id
			JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
			WHERE r.id=$1::uuid
			UNION ALL
			SELECT p.tenant_id, p.id, p.parent_id, pk.slug, up.depth+1
			FROM up
			JOIN nodes p ON p.tenant_id=up.tenant_id AND p.id=up.parent_id
			JOIN node_kinds pk ON pk.tenant_id=p.tenant_id AND pk.id=p.kind_id
			WHERE up.slug NOT IN ('work','ticket') AND up.depth<32
		)
		SELECT id::text FROM up WHERE slug IN ('work','ticket') ORDER BY depth LIMIT 1`, runID)
}

func loadFences(ctx context.Context, tx pgx.Tx, harness string) (map[string]groupFence, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, exclusive FROM account_groups WHERE harness=$1`, harness)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]groupFence{}
	for rows.Next() {
		var id string
		var g groupFence
		if err := rows.Scan(&id, &g.exclusive); err != nil {
			return nil, err
		}
		g.projects = map[string]bool{}
		out[id] = g
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	prows, err := tx.Query(ctx, `SELECT gp.group_id::text, gp.project_id::text FROM account_group_projects gp JOIN account_groups g ON g.tenant_id=gp.tenant_id AND g.id=gp.group_id WHERE g.harness=$1`, harness)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var gid, pid string
		if err := prows.Scan(&gid, &pid); err != nil {
			return nil, err
		}
		if g, ok := out[gid]; ok {
			g.projects[pid] = true
			out[gid] = g
		}
	}
	return out, prows.Err()
}

func applyFence(accounts []Account, fences map[string]groupFence, projectID string) []Account {
	fenced := ""
	if projectID != "" {
		for id, g := range fences {
			if g.projects[projectID] {
				fenced = id
				break
			}
		}
	}
	out := []Account{}
	for _, a := range accounts {
		if fenced != "" && a.GroupID != fenced {
			continue
		}
		if g, ok := fences[a.GroupID]; ok && a.GroupID != "" && g.exclusive && (projectID == "" || !g.projects[projectID]) {
			continue
		}
		out = append(out, a)
	}
	return out
}

func keepAccount(accounts []Account, id string) []Account {
	id = strings.ToLower(id)
	out := []Account{}
	for _, a := range accounts {
		if strings.ToLower(a.ID) == id {
			out = append(out, a)
		}
	}
	return out
}

func keepGroup(accounts []Account, id string) []Account {
	id = strings.ToLower(id)
	out := []Account{}
	for _, a := range accounts {
		if strings.ToLower(a.GroupID) == id {
			out = append(out, a)
		}
	}
	return out
}

func quotaOccupancy(ctx context.Context, tx pgx.Tx) (map[string]int, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.harness || ':' || a.quota_pool_fingerprint, count(*)
		FROM agent_runs r
		JOIN agent_accounts a ON a.tenant_id=r.tenant_id AND a.id=r.account_id
		WHERE a.quota_pool_fingerprint<>'' AND r.account_id IS NOT NULL
		  AND (r.status IN ('queued','starting','running','waiting')
		    OR r.trace->'work_lifecycle_release'->>'exit_unconfirmed'='true')
		GROUP BY a.harness, a.quota_pool_fingerprint`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var fp string
		var n int
		if err := rows.Scan(&fp, &n); err != nil {
			return nil, err
		}
		out[fp] = n
	}
	return out, rows.Err()
}

func slotCount(a Account, byAccount, byQuota map[string]int) int {
	if a.QuotaPoolFingerprint != "" {
		return byQuota[a.Harness+":"+a.QuotaPoolFingerprint]
	}
	return byAccount[a.ID]
}

// annotateQuotas marks one gauge per fingerprint: freshest windows, every host,
// and alias rows pointing at that gauge. Routing slots stay on the door that
// still has them so a caller summing slots counts the quota once.
func annotateQuotas(out []accountCapacity, accounts []Account) {
	byID := map[string]Account{}
	for _, a := range accounts {
		byID[a.ID] = a
	}
	type bucket struct {
		ids      []string
		hosts    []string
		freshest string
		at       time.Time
	}
	groups := map[string]*bucket{}
	for i := range out {
		a, ok := byID[out[i].AccountID]
		if !ok {
			continue
		}
		out[i].QuotaFingerprint = a.QuotaPoolFingerprint
		out[i].GroupID = a.GroupID
		out[i].GroupName = a.GroupName
		out[i].HostLabel = a.HostLabel
		if a.QuotaPoolFingerprint == "" {
			continue
		}
		key := a.Harness + ":" + a.QuotaPoolFingerprint
		b := groups[key]
		if b == nil {
			b = &bucket{}
			groups[key] = b
		}
		b.ids = append(b.ids, a.ID)
		host := a.HostLabel
		if host == "" {
			host = a.DaemonID
		}
		b.hosts = append(b.hosts, host)
		var at time.Time
		if read := lastRead(a.Windows); read != nil {
			at = *read
		}
		if b.freshest == "" || at.After(b.at) || at.Equal(b.at) && a.ID < b.freshest {
			b.freshest, b.at = a.ID, at
		}
	}
	for _, b := range groups {
		if len(b.ids) < 2 {
			continue
		}
		hosts := uniqueStrings(b.hosts)
		member := map[string]bool{}
		for _, id := range b.ids {
			member[id] = true
		}
		for i := range out {
			if !member[out[i].AccountID] {
				continue
			}
			out[i].Hosts = hosts
			if out[i].AccountID != b.freshest {
				out[i].SameQuotaAs = b.freshest
			}
		}
	}
}

func uniqueStrings(in []string) []string {
	sort.Strings(in)
	out := []string{}
	for _, s := range in {
		if s == "" || len(out) > 0 && out[len(out)-1] == s {
			continue
		}
		out = append(out, s)
	}
	return out
}
