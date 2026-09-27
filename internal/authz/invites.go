// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/identity"
	"github.com/inspr-at/paimos/internal/tenant"
)

var emailPattern = regexp.MustCompile(`^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$`)

const (
	inviteDaysDefault = 14
	inviteDaysMax     = 90
)

type PrincipalRef struct {
	PrincipalID string `json:"principal_id"`
	Name        string `json:"name"`
}

type Invite struct {
	ID            string        `json:"id"`
	Email         string        `json:"email"`
	WorkspaceRole *RoleRef      `json:"workspace_role"`
	ProjectRoles  []ProjectRole `json:"project_roles"`
	Status        string        `json:"status"`
	CreatedBy     PrincipalRef  `json:"created_by"`
	CreatedAt     time.Time     `json:"created_at"`
	ExpiresAt     time.Time     `json:"expires_at"`
	AcceptedBy    *PrincipalRef `json:"accepted_by"`
	AcceptedAt    *time.Time    `json:"accepted_at"`
}

type inviteProject struct {
	ProjectID string `json:"project_id"`
	RoleID    string `json:"role_id"`
}

var (
	errAlreadyMember = errors.New("already a member")
	errNotProject    = errors.New("not a project")
)

type roleMissing struct{ field string }

// roleScopeError is a role that cannot be granted at the requested scope.
type roleScopeError struct{ field, reason string }

func (e roleScopeError) Error() string { return e.reason }

func (e roleMissing) Error() string { return "role missing" }

func (m *Module) createInvite(w http.ResponseWriter, r *http.Request) {
	p := actor(r)
	var body struct {
		Email            string          `json:"email"`
		DisplayName      string          `json:"display_name"`
		ProvisionAccount bool            `json:"provision_account"`
		WorkspaceRoleID  *string         `json:"workspace_role_id"`
		ProjectRoles     []inviteProject `json:"project_roles"`
		ExpiresInDays    *int            `json:"expires_in_days"`
	}
	if err := decode(r, &body); err != nil {
		apiFail(w, 400, "invalid", "body", "Request body must be one JSON object")
		return
	}
	email := strings.TrimSpace(body.Email)
	if !emailPattern.MatchString(email) || len(email) > 320 {
		apiFail(w, 400, "invalid", "email", "Enter an email address")
		return
	}
	name := strings.TrimSpace(body.DisplayName)
	if body.ProvisionAccount && m.provisionerFor(p.TenantID) == nil {
		apiFail(w, 400, "not_configured", "provision_account", "Account provisioning is not configured")
		return
	}
	if len(name) > 200 || (body.ProvisionAccount && name == "") {
		apiFail(w, 400, "invalid", "display_name", "Enter the invitee's name (up to 200 characters)")
		return
	}
	days := inviteDaysDefault
	if body.ExpiresInDays != nil {
		days = *body.ExpiresInDays
	}
	if days < 1 || days > inviteDaysMax {
		apiFail(w, 400, "invalid", "expires_in_days", "Expiry must be between 1 and 90 days")
		return
	}
	if body.WorkspaceRoleID == nil && len(body.ProjectRoles) == 0 {
		apiFail(w, 400, "invalid", "workspace_role_id", "Choose a workspace role or a project role")
		return
	}
	if body.WorkspaceRoleID != nil && !uuidPattern.MatchString(*body.WorkspaceRoleID) {
		apiFail(w, 400, "invalid", "workspace_role_id", "Role ID must be a UUID")
		return
	}
	if len(body.ProjectRoles) > 50 {
		apiFail(w, 400, "invalid", "project_roles", "An invite can name at most 50 projects")
		return
	}
	seenProject := map[string]bool{}
	for _, item := range body.ProjectRoles {
		if !uuidPattern.MatchString(item.ProjectID) || !uuidPattern.MatchString(item.RoleID) || seenProject[item.ProjectID] {
			apiFail(w, 400, "invalid", "project_roles", "Each project role needs a project and a role, once")
			return
		}
		seenProject[item.ProjectID] = true
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		internalFail(w, err)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	var created Invite
	var slug string
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT slug FROM tenants WHERE id=$1::uuid`, p.TenantID).Scan(&slug); err != nil {
			return err
		}
		if err := m.authorizeMutation(r.Context(), tx, p, "members.manage", nil); err != nil {
			return err
		}
		if body.WorkspaceRoleID != nil {
			role, err := grantRole(r.Context(), tx, p, *body.WorkspaceRoleID, "workspace_role_id")
			if err != nil {
				return err
			}
			// Guest is a project-only role (ADR-003 P2).
			if role.Builtin && role.Key == "guest" {
				return roleScopeError{field: "workspace_role_id", reason: "Guest is a project role; invite to a project instead"}
			}
		}
		for _, item := range body.ProjectRoles {
			role, err := grantRole(r.Context(), tx, p, item.RoleID, "project_roles")
			if err != nil {
				return err
			}
			if !projectRoleAllowed(role) {
				return roleScopeError{field: "project_roles", reason: "Owner and Customer are workspace roles; choose a project role"}
			}
			if err := requireProject(r.Context(), tx, p.TenantID, item.ProjectID); err != nil {
				return err
			}
		}
		if err := refuseActiveMember(r.Context(), tx, p.TenantID, email); err != nil {
			return err
		}
		var workspace any
		if body.WorkspaceRoleID != nil {
			workspace = *body.WorkspaceRoleID
		}
		var id string
		if err := tx.QueryRow(r.Context(), `INSERT INTO invites(tenant_id,email,workspace_role_id,token_hash,expires_at,created_by,account_display_name)
			VALUES($1::uuid,$2,$3::uuid,$4,now() + make_interval(days => $5),$6::uuid,$7)
			RETURNING id::text`, p.TenantID, email, workspace, sum[:], days, p.ID, optionalName(body.ProvisionAccount, name)).Scan(&id); err != nil {
			return err
		}
		for _, item := range body.ProjectRoles {
			if _, err := tx.Exec(r.Context(), `INSERT INTO invite_project_roles(tenant_id,invite_id,project_id,role_id)
				VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid)`, p.TenantID, id, item.ProjectID, item.RoleID); err != nil {
				return err
			}
		}
		var err error
		created, err = inviteByID(r.Context(), tx, p.TenantID, id)
		if err != nil {
			return err
		}
		return appendEvent(r.Context(), tx, p, "invite.created", nil, created)
	})
	if err != nil {
		writeInviteErr(w, err)
		return
	}
	out := map[string]any{"invite": created, "join_url": joinURL(r, slug, token)}
	if body.ProvisionAccount {
		out["account"] = m.provisionAccount(r.Context(), p, created.ID, email, name, "")
	}
	reply(w, http.StatusCreated, out)
}

func optionalName(enabled bool, name string) any {
	if !enabled {
		return nil
	}
	return name
}

type accountResult struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// provisionAccount is called only after createInvite commits, or after retry
// has checked the invite and the actor in a tenant transaction.
func (m *Module) provisionAccount(ctx context.Context, p tenant.Principal, id, email, name, subject string) accountResult {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	out := accountResult{Status: "failed", Reason: "The sign-in account could not be set up. Retry the invite or ask an administrator."}
	if subject != "" {
		if sender, ok := m.provisioner.(identity.InviteSender); ok && sender.SendInvite(ctx, subject) == nil {
			out = accountResult{Status: "invited"}
		}
	} else {
		result, err := m.provisioner.EnsureUser(ctx, email, name)
		if err == nil && (result.Status == "invited" || result.Status == "exists") {
			out = accountResult{Status: result.Status}
			subject = result.Subject
		} else {
			var pe *identity.ProvisionError
			if errors.As(err, &pe) {
				subject = pe.Subject
			}
		}
	}
	// Store only status and a subject needed to finish our own created user.
	// Provider responses and the credential never enter this event.
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE invites SET account_status=$1,account_subject=$2,account_started_at=NULL
			WHERE tenant_id=$3::uuid AND id=$4::uuid`, out.Status, nullText(subject), p.TenantID, id); err != nil {
			return err
		}
		typ := "invite.account_provision_failed"
		if out.Status != "failed" {
			typ = "invite.account_provisioned"
		}
		return appendEvent(ctx, tx, p, typ, nil, map[string]string{"invite_id": id, "status": out.Status})
	})
	if err != nil {
		return accountResult{Status: "failed", Reason: "The sign-in account status could not be saved. Ask an administrator."}
	}
	return out
}

func nullText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (m *Module) retryInviteProvision(w http.ResponseWriter, r *http.Request) {
	p := actor(r)
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		apiFail(w, 400, "invalid", "id", "Invite ID must be a UUID")
		return
	}
	if m.provisionerFor(p.TenantID) == nil {
		apiFail(w, 400, "not_configured", "", "Account provisioning is not configured")
		return
	}
	var email, name, subject string
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.authorizeMutation(r.Context(), tx, p, "members.manage", nil); err != nil {
			return err
		}
		var status, savedSubject, savedName *string
		var startedAt *time.Time
		err := tx.QueryRow(r.Context(), `SELECT email,account_display_name,account_status,account_subject,account_started_at
			FROM invites WHERE tenant_id=$1::uuid AND id=$2::uuid AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at>now() FOR UPDATE`, p.TenantID, id).Scan(&email, &savedName, &status, &savedSubject, &startedAt)
		if err != nil {
			return err
		}
		if status == nil || savedName == nil || (*status != "failed" && (*status != "processing" || startedAt == nil || time.Since(*startedAt) < 30*time.Second)) {
			return errInviteProvisionClosed
		}
		name = *savedName
		if savedSubject != nil {
			subject = *savedSubject
		}
		if _, err := tx.Exec(r.Context(), `UPDATE invites SET account_status='processing',account_started_at=now() WHERE tenant_id=$1::uuid AND id=$2::uuid`, p.TenantID, id); err != nil {
			return err
		}
		return appendEvent(r.Context(), tx, p, "invite.account_provision_started", nil, map[string]string{"invite_id": id, "status": "processing"})
	})
	if errors.Is(err, errInviteProvisionClosed) {
		apiFail(w, 409, "conflict", "id", "This invite cannot be provisioned again")
		return
	}
	if err != nil {
		internalFail(w, err)
		return
	}
	reply(w, 200, m.provisionAccount(r.Context(), p, id, email, name, subject))
}

var errInviteProvisionClosed = errors.New("invite provisioning closed")

func (m *Module) revokeInvite(w http.ResponseWriter, r *http.Request) {
	p := actor(r)
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		apiFail(w, 400, "invalid", "id", "Invite ID must be a UUID")
		return
	}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.authorizeMutation(r.Context(), tx, p, "members.manage", nil); err != nil {
			return err
		}
		before, err := inviteByID(r.Context(), tx, p.TenantID, id)
		if err != nil {
			return err
		}
		switch before.Status {
		case "accepted":
			return errInviteClosed
		case "revoked":
			return nil
		}
		if _, err := tx.Exec(r.Context(), `UPDATE invites SET revoked_at=now() WHERE tenant_id=$1::uuid AND id=$2::uuid AND accepted_at IS NULL AND revoked_at IS NULL`, p.TenantID, id); err != nil {
			return err
		}
		after, err := inviteByID(r.Context(), tx, p.TenantID, id)
		if err != nil {
			return err
		}
		return appendEvent(r.Context(), tx, p, "invite.revoked", before, after)
	})
	if errors.Is(err, errInviteClosed) {
		apiFail(w, 409, "conflict", "id", "This invite was already accepted")
		return
	}
	if err != nil {
		writeInviteErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errInviteClosed = errors.New("invite closed")

func grantRole(ctx context.Context, tx pgx.Tx, p tenant.Principal, roleID, field string) (Role, error) {
	role, err := roleTx(ctx, tx, roleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Role{}, roleMissing{field: field}
	}
	if err != nil {
		return Role{}, err
	}
	if err := canGrantTx(ctx, tx, p, role.Permissions); err != nil {
		return Role{}, grantError{field: field}
	}
	if role.Key == "owner" {
		if err := requireTx(ctx, tx, p, "ownership.transfer", Scope{}); err != nil {
			return Role{}, grantError{field: field}
		}
	}
	return role, nil
}

func requireProject(ctx context.Context, tx pgx.Tx, tenantID, projectID string) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT true FROM nodes n
		JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE n.tenant_id=$1::uuid AND n.id=$2::uuid AND k.slug='project' AND n.deleted_at IS NULL`, tenantID, projectID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotProject
	}
	return err
}

func refuseActiveMember(ctx context.Context, tx pgx.Tx, tenantID, email string) error {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM principals p
		LEFT JOIN identities i ON i.id=p.identity_id
		JOIN principals canonical ON canonical.tenant_id=p.tenant_id AND canonical.id=coalesce(p.linked_to,p.id)
		WHERE p.tenant_id=$1::uuid AND p.kind='person' AND canonical.status='active'
		  AND lower(coalesce(nullif(p.email,''), nullif(i.email,''), '')) = lower($2)
	)`, tenantID, email).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return errAlreadyMember
	}
	return nil
}

func writeInviteErr(w http.ResponseWriter, err error) {
	var ge grantError
	var missing roleMissing
	var scope roleScopeError
	switch {
	case errors.As(err, &ge):
		apiFail(w, 403, "forbidden", ge.field, "You cannot grant a role with permissions you do not hold")
	case errors.Is(err, errAlreadyMember):
		apiFail(w, 409, "already_member", "email", "This person is already an active member")
	case errors.Is(err, errNotProject):
		apiFail(w, 400, "invalid", "project_roles", "Choose a project in this workspace")
	case errors.As(err, &missing):
		apiFail(w, 400, "invalid", missing.field, "Choose a role in this workspace")
	case errors.As(err, &scope):
		apiFail(w, 400, "project_only_role", scope.field, scope.reason)
	default:
		internalFail(w, err)
	}
}

func listInvites(ctx context.Context, tx pgx.Tx, tenantID string) ([]Invite, error) {
	rows, err := tx.Query(ctx, inviteSelect+` WHERE i.tenant_id=$1::uuid ORDER BY i.created_at DESC, i.id`, tenantID)
	if err != nil {
		return nil, err
	}
	out := []Invite{}
	for rows.Next() {
		item, err := scanInvite(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := attachInviteProjects(ctx, tx, tenantID, out); err != nil {
		return nil, err
	}
	return out, nil
}

func inviteByID(ctx context.Context, tx pgx.Tx, tenantID, id string) (Invite, error) {
	rows, err := tx.Query(ctx, inviteSelect+` WHERE i.tenant_id=$1::uuid AND i.id=$2::uuid`, tenantID, id)
	if err != nil {
		return Invite{}, err
	}
	if !rows.Next() {
		err = rows.Err()
		rows.Close()
		if err != nil {
			return Invite{}, err
		}
		return Invite{}, pgx.ErrNoRows
	}
	item, err := scanInvite(rows)
	rows.Close()
	if err != nil {
		return Invite{}, err
	}
	list := []Invite{item}
	if err := attachInviteProjects(ctx, tx, tenantID, list); err != nil {
		return Invite{}, err
	}
	return list[0], nil
}

const inviteSelect = `SELECT i.id::text, i.email,
	wr.id::text, wr.key, wr.name,
	CASE WHEN i.accepted_at IS NOT NULL THEN 'accepted'
	     WHEN i.revoked_at IS NOT NULL THEN 'revoked'
	     WHEN i.expires_at <= now() THEN 'expired'
	     ELSE 'pending' END,
	cb.id::text, cb.name, i.created_at, i.expires_at,
	ab.id::text, ab.name, i.accepted_at
	FROM invites i
	LEFT JOIN roles wr ON wr.tenant_id=i.tenant_id AND wr.id=i.workspace_role_id
	JOIN principals cb ON cb.tenant_id=i.tenant_id AND cb.id=i.created_by
	LEFT JOIN principals ab ON ab.tenant_id=i.tenant_id AND ab.id=i.accepted_by`

type inviteScanner interface {
	Scan(dest ...any) error
}

func scanInvite(row inviteScanner) (Invite, error) {
	var item Invite
	var roleID, roleKey, roleName, acceptedID, acceptedName *string
	if err := row.Scan(&item.ID, &item.Email, &roleID, &roleKey, &roleName, &item.Status, &item.CreatedBy.PrincipalID, &item.CreatedBy.Name, &item.CreatedAt, &item.ExpiresAt, &acceptedID, &acceptedName, &item.AcceptedAt); err != nil {
		return Invite{}, err
	}
	if roleID != nil {
		item.WorkspaceRole = &RoleRef{ID: *roleID, Key: *roleKey, Name: *roleName}
	}
	if acceptedID != nil {
		item.AcceptedBy = &PrincipalRef{PrincipalID: *acceptedID, Name: *acceptedName}
	}
	item.ProjectRoles = []ProjectRole{}
	return item, nil
}

func attachInviteProjects(ctx context.Context, tx pgx.Tx, tenantID string, invites []Invite) error {
	if len(invites) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT ip.invite_id::text, n.id::text, n.key, n.title, r.id::text, r.key, r.name
		FROM invite_project_roles ip
		JOIN nodes n ON n.tenant_id=ip.tenant_id AND n.id=ip.project_id
		JOIN roles r ON r.tenant_id=ip.tenant_id AND r.id=ip.role_id
		WHERE ip.tenant_id=$1::uuid
		ORDER BY n.key, n.id`, tenantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	byID := map[string]*Invite{}
	for i := range invites {
		byID[invites[i].ID] = &invites[i]
	}
	for rows.Next() {
		var inviteID string
		var role ProjectRole
		if err := rows.Scan(&inviteID, &role.ProjectID, &role.ProjectKey, &role.ProjectTitle, &role.Role.ID, &role.Role.Key, &role.Role.Name); err != nil {
			return err
		}
		if item := byID[inviteID]; item != nil {
			item.ProjectRoles = append(item.ProjectRoles, role)
		}
	}
	return rows.Err()
}

func joinURL(r *http.Request, slug, token string) string {
	q := url.Values{}
	if slug != "" {
		q.Set("tenant", slug)
	}
	q.Set("invite", token)
	path := "/api/auth/login?" + q.Encode()
	if r.Host == "" {
		return path
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + path
}
