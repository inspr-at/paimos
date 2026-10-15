// SPDX-License-Identifier: AGPL-3.0-only
package reviewgate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const CredentialHeader = "X-Aeon-Review-Credential"

var ErrRunBinding = errors.New("review run binding is invalid or no longer current")
var ErrRunCredential = errors.New("review run credential is invalid, expired or consumed")

type RunBinding struct {
	Ticket     string `json:"ticket_node_id"`
	Repository string `json:"repository"`
	PR         int64  `json:"pull_request"`
	Target     string `json:"target_branch"`
	Base       string `json:"base_sha"`
	Head       string `json:"head_sha"`
	Scope      string `json:"scope"`
}

func (b RunBinding) Valid() bool {
	return ValidRepository(b.Repository) && b.PR > 0 && len(b.Target) > 0 && len(b.Target) <= 255 &&
		!strings.ContainsAny(b.Target, " \t\r\n\x00") && utf8.ValidString(b.Target) &&
		ValidSHA(b.Base) && ValidSHA(b.Head) && b.Base != b.Head && b.Scope == b.Base+".."+b.Head
}

type RunCreate struct {
	Repository      string `json:"repository"`
	PR              int64  `json:"pull_request"`
	Target          string `json:"target_branch"`
	Base            string `json:"base_sha"`
	Head            string `json:"head_sha"`
	Scope           string `json:"scope"`
	AuthorSession   string `json:"author_session_id"`
	ReviewerSession string `json:"reviewer_session_id"`
	Kind            string `json:"kind"`
}

type Run struct {
	RunBinding
	ID              string    `json:"id"`
	Project         string    `json:"project_id"`
	AuthorSession   string    `json:"author_session_id"`
	ReviewerSession string    `json:"reviewer_session_id"`
	Reviewer        string    `json:"reviewer_principal_id"`
	AuthorFamily    string    `json:"author_family"`
	ReviewerFamily  string    `json:"reviewer_family"`
	Harness         string    `json:"harness"`
	Model           string    `json:"model"`
	Effort          string    `json:"reasoning_effort"`
	Kind            string    `json:"kind"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	Verdict         string    `json:"verdict"`
	VisualImpact    string    `json:"visual_impact"`
	Reason          string    `json:"reason"`
}

type RunSubmit struct {
	Binding      RunBinding `json:"binding"`
	Verdict      string     `json:"verdict"`
	VisualImpact string     `json:"visual_impact"`
	Reason       string     `json:"reason"`
}

func (s RunSubmit) Valid() bool {
	return s.Binding.Valid() && (s.Verdict == "ok" || s.Verdict == "changes") &&
		(s.VisualImpact == "yes" || s.VisualImpact == "no" || s.VisualImpact == "uncertain") &&
		len(s.Reason) <= 2000 && strings.TrimSpace(s.Reason) != "" && utf8.ValidString(s.Reason) && !strings.ContainsRune(s.Reason, '\x00')
}

// Store functions run under the caller's tenant -> tree -> record fence. They
// never append events: the handler appends only after every lock and write.
func CreateRunTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, ticket string, in RunCreate, now time.Time) (Run, error) {
	b := RunBinding{ticket, in.Repository, in.PR, in.Target, in.Base, in.Head, in.Scope}
	r := Run{RunBinding: b, Kind: in.Kind, AuthorSession: in.AuthorSession, ReviewerSession: in.ReviewerSession, CreatedBy: p.ID, CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute)}
	if !b.Valid() || in.Kind != "review" && in.Kind != "visual" || in.AuthorSession == in.ReviewerSession {
		return r, ErrRunBinding
	}
	if err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE id=$1 AND deleted_at IS NULL AND NOT EXISTS(SELECT 1 FROM nodes child WHERE child.parent_id=$1 AND child.deleted_at IS NULL) FOR NO KEY UPDATE`, ticket).Scan(&r.Project); err != nil {
		return r, err
	}
	if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: r.Project}); err != nil {
		return r, err
	}
	if p.Kind == tenant.Person {
		if err := authz.RequireProjectAdminTx(ctx, tx, p, r.Project); err != nil {
			return r, err
		}
	} else if err := authz.RequireQueueCoordinatorTx(ctx, tx, p, r.Project); err != nil {
		return r, err
	}
	var authorPrincipal, authorHarness, authorModel string
	err := tx.QueryRow(ctx, `SELECT a.agent_principal_id::text,a.harness,coalesce(a.model,''),r.agent_principal_id::text,r.harness,coalesce(r.model,''),coalesce(r.reasoning_effort,'')
 FROM harness_sessions a JOIN harness_sessions r ON r.tenant_id=a.tenant_id AND r.project_id=a.project_id AND r.ticket_node_id=a.ticket_node_id
 WHERE a.id=$1 AND r.id=$2 AND a.ticket_node_id=$3 AND a.project_id=$4 AND a.role='worker' AND r.role='worker'
 AND a.archived_at IS NULL AND r.archived_at IS NULL AND r.stopped_at IS NULL`, in.AuthorSession, in.ReviewerSession, ticket, r.Project).
		Scan(&authorPrincipal, &authorHarness, &authorModel, &r.Reviewer, &r.Harness, &r.Model, &r.Effort)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrRunBinding
	}
	if err != nil {
		return r, err
	}
	r.AuthorFamily = harnesslaunch.ModelFamily(authorHarness, authorModel)
	r.ReviewerFamily = harnesslaunch.ModelFamily(r.Harness, r.Model)
	if authorPrincipal == r.Reviewer || r.Reviewer == p.ID || !ValidFamily(r.AuthorFamily) || !ValidFamily(r.ReviewerFamily) || r.Model == "" ||
		in.Kind == "review" && r.AuthorFamily == r.ReviewerFamily {
		return r, ErrRunBinding
	}
	if err := currentRunTargetTx(ctx, tx, r); err != nil {
		return r, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO review_runs(tenant_id,ticket_node_id,project_id,repository,pull_request,target_branch,base_sha,head_sha,scope,kind,author_session_id,reviewer_session_id,reviewer_principal_id,author_family,reviewer_family,harness,model,reasoning_effort,created_by,created_at,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21) RETURNING id::text`, p.TenantID, ticket, r.Project, r.Repository, r.PR, r.Target, r.Base, r.Head, r.Scope, r.Kind, r.AuthorSession, r.ReviewerSession, r.Reviewer, r.AuthorFamily, r.ReviewerFamily, r.Harness, r.Model, r.Effort, r.CreatedBy, r.CreatedAt, r.ExpiresAt).Scan(&r.ID)
	return r, err
}

func loadRunTx(ctx context.Context, tx pgx.Tx, id string) (r Run, digest []byte, key *string, err error) {
	err = tx.QueryRow(ctx, `SELECT id::text,ticket_node_id::text,project_id::text,repository,pull_request,target_branch,base_sha,head_sha,scope,kind,author_session_id::text,reviewer_session_id::text,reviewer_principal_id::text,author_family,reviewer_family,harness,model,reasoning_effort,created_by::text,created_at,expires_at,coalesce(verdict,''),coalesce(visual_impact,''),coalesce(reason,''),credential_digest,credential_key_id::text FROM review_runs WHERE id=$1 FOR NO KEY UPDATE`, id).
		Scan(&r.ID, &r.Ticket, &r.Project, &r.Repository, &r.PR, &r.Target, &r.Base, &r.Head, &r.Scope, &r.Kind, &r.AuthorSession, &r.ReviewerSession, &r.Reviewer, &r.AuthorFamily, &r.ReviewerFamily, &r.Harness, &r.Model, &r.Effort, &r.CreatedBy, &r.CreatedAt, &r.ExpiresAt, &r.Verdict, &r.VisualImpact, &r.Reason, &digest, &key)
	return
}

func currentRunTargetTx(ctx context.Context, tx pgx.Tx, r Run) error {
	var current bool
	// Use the latest delivery, not any historical matching row. Retargets,
	// changed ranges, re-linked PRs and project moves all invalidate the run.
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT d.repository=$2 AND d.pull_request=$3 AND d.head_sha=$4 AND d.observation->>'base_sha'=$5 AND d.observation->>'target_branch'=$6 AND d.project_id=$7
 FROM delivery_items d JOIN delivery_metric_sources s ON s.tenant_id=d.tenant_id AND s.project_id=d.project_id AND s.repository=d.repository
 WHERE d.ticket_node_id=$1 ORDER BY d.updated_at DESC,d.id DESC LIMIT 1),false)
 AND EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND project_id=$7 AND deleted_at IS NULL)`, r.Ticket, r.Repository, r.PR, r.Head, r.Base, r.Target, r.Project).Scan(&current)
	if err == nil && !current {
		return ErrRunBinding
	}
	return err
}

func reviewerRunAccessTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, r Run) error {
	if p.Kind != tenant.Agent || p.ID != r.Reviewer || p.ID == r.CreatedBy || p.KeyID == "" {
		return authz.ErrForbidden
	}
	if err := authz.RequireIndependentReviewerTx(ctx, tx, p, r.Project); err != nil {
		return err
	}
	if r.Kind == "visual" {
		if err := authz.RequireTx(ctx, tx, p, "reviews.visual_approve", authz.Scope{ProjectID: r.Project}); err != nil {
			return err
		}
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions WHERE id=$1 AND ticket_node_id=$2 AND project_id=$3 AND agent_principal_id=$4
 AND role='worker' AND archived_at IS NULL AND stopped_at IS NULL AND harness=$5 AND model=$6 AND coalesce(reasoning_effort,'')=$7)`, r.ReviewerSession, r.Ticket, r.Project, p.ID, r.Harness, r.Model, r.Effort).Scan(&valid)
	if err == nil && !valid {
		return authz.ErrForbidden
	}
	return err
}

// ClaimRunTx deliberately returns the credential only to the bound reviewer.
// Run creators cannot mint or retrieve a reviewer's submission capability.
func ClaimRunTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, now time.Time) (string, time.Time, error) {
	r, digest, _, err := loadRunTx(ctx, tx, id)
	if err != nil {
		return "", time.Time{}, err
	}
	if err = reviewerRunAccessTx(ctx, tx, p, r); err != nil {
		return "", time.Time{}, err
	}
	if digest != nil || !now.Before(r.ExpiresAt) || r.Verdict != "" {
		return "", time.Time{}, ErrRunCredential
	}
	if err = currentRunTargetTx(ctx, tx, r); err != nil {
		return "", time.Time{}, err
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return "", time.Time{}, err
	}
	token := "aer_" + p.TenantID + "_" + base64.RawURLEncoding.EncodeToString(secret)
	hash := sha256.Sum256([]byte(token))
	_, err = tx.Exec(ctx, `UPDATE review_runs SET credential_digest=$2,credential_key_id=$3 WHERE id=$1`, id, hash[:], p.KeyID)
	return token, r.ExpiresAt, err
}

func CredentialTenant(token string) string {
	if len(token) != 84 || !strings.HasPrefix(token, "aer_") || token[40] != '_' {
		return ""
	}
	id := token[4:40]
	if len(strings.ReplaceAll(id, "-", "")) != 32 {
		return ""
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return ""
			}
		} else if !strings.ContainsRune("0123456789abcdef", c) {
			return ""
		}
	}
	if raw, err := base64.RawURLEncoding.DecodeString(token[41:]); err != nil || len(raw) != 32 {
		return ""
	}
	return id
}

// CredentialPrincipalTx is a bounded capability lookup before entering a keyed
// transaction. The final write rechecks digest, run, expiry, live key and grants.
func CredentialPrincipalTx(ctx context.Context, tx pgx.Tx, id, token string, now time.Time) (tenant.Principal, error) {
	p := tenant.Principal{Kind: tenant.Agent, TenantID: CredentialTenant(token)}
	if p.TenantID == "" {
		return p, ErrRunCredential
	}
	hash := sha256.Sum256([]byte(token))
	err := tx.QueryRow(ctx, `SELECT k.principal_id::text,k.id::text,k.scopes,coalesce(k.full_access,false),coalesce(k.created_by_principal_id::text,'')
 FROM review_runs r JOIN agent_keys k ON k.tenant_id=r.tenant_id AND k.id=r.credential_key_id AND k.principal_id=r.reviewer_principal_id
 WHERE r.id=$1 AND r.credential_digest=$2 AND r.expires_at>$3 AND r.submitted_at IS NULL
 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>$3)`, id, hash[:], now).Scan(&p.ID, &p.KeyID, &p.Scopes, &p.FullAccess, &p.KeyCreatorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrRunCredential
	}
	p.AuthKeyID = p.KeyID
	p.Scopes = authz.ResolveKeyScopes(p.Scopes, p.FullAccess)
	return p, err
}

func SubmitRunTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, token string, in RunSubmit, now time.Time) (Run, error) {
	r, digest, key, err := loadRunTx(ctx, tx, id)
	if err != nil {
		return r, err
	}
	hash := sha256.Sum256([]byte(token))
	if key == nil || *key != p.KeyID || subtle.ConstantTimeCompare(digest, hash[:]) != 1 || !now.Before(r.ExpiresAt) || r.Verdict != "" {
		return r, ErrRunCredential
	}
	if err = reviewerRunAccessTx(ctx, tx, p, r); err != nil {
		return r, err
	}
	if !in.Valid() || in.Binding != r.RunBinding || r.Kind == "review" && (!ValidFamily(r.AuthorFamily) || !ValidFamily(r.ReviewerFamily) || r.AuthorFamily == r.ReviewerFamily) {
		return r, ErrRunBinding
	}
	if err = currentRunTargetTx(ctx, tx, r); err != nil {
		return r, err
	}
	r.Verdict = in.Verdict
	r.VisualImpact = in.VisualImpact
	r.Reason = in.Reason
	_, err = tx.Exec(ctx, `UPDATE review_runs SET verdict=$2,visual_impact=$3,reason=$4,submitted_at=$5 WHERE id=$1`, id, r.Verdict, r.VisualImpact, r.Reason, now)
	return r, err
}

type EvidenceProvenance struct {
	ReviewLegacy bool `json:"review_legacy_unverified"`
	VisualLegacy bool `json:"visual_legacy_unverified"`
}

func EvidenceProvenanceTx(ctx context.Context, tx pgx.Tx, ticket string) (out EvidenceProvenance, err error) {
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events e WHERE e.node_id=$1 AND e.type='coordinator_review.recorded'
 AND NOT EXISTS(SELECT 1 FROM review_runs r WHERE r.id::text=e.after->>'run_id' AND r.ticket_node_id=e.node_id AND r.submitted_at IS NOT NULL)),
 EXISTS(SELECT 1 FROM events e WHERE e.node_id=$1 AND e.type='entry_check.evidence' AND e.after->>'check_id'='visual_ok'
 AND NOT EXISTS(SELECT 1 FROM review_runs r WHERE r.id::text=e.after->>'run_id' AND r.ticket_node_id=e.node_id AND r.kind='visual' AND r.submitted_at IS NOT NULL))`, ticket).Scan(&out.ReviewLegacy, &out.VisualLegacy)
	return
}

// AuthenticatedOKTx consumes the latest observation only when it points to an
// actual submitted run and the complete current delivery binding still matches.
func AuthenticatedOKTx(ctx context.Context, tx pgx.Tx, ticket, kind string) (bool, error) {
	var r Run
	var verdict string
	err := tx.QueryRow(ctx, `SELECT r.ticket_node_id::text,r.project_id::text,r.repository,r.pull_request,r.target_branch,r.base_sha,r.head_sha,r.scope,r.author_family,r.reviewer_family,r.verdict
 FROM events e JOIN review_runs r ON r.tenant_id=e.tenant_id AND r.id::text=e.after->>'run_id' AND r.ticket_node_id=e.node_id AND r.reviewer_principal_id=e.actor_principal_id
 WHERE e.id=(SELECT id FROM events WHERE node_id=$1 AND (type='coordinator_review.recorded' AND $2='review' OR type='entry_check.evidence' AND after->>'check_id'='visual_ok' AND $2='visual') ORDER BY id DESC LIMIT 1)
 AND r.kind=$2 AND r.submitted_at IS NOT NULL`, ticket, kind).Scan(&r.Ticket, &r.Project, &r.Repository, &r.PR, &r.Target, &r.Base, &r.Head, &r.Scope, &r.AuthorFamily, &r.ReviewerFamily, &verdict)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if verdict != "ok" || !r.RunBinding.Valid() || kind == "review" && (!ValidFamily(r.AuthorFamily) || !ValidFamily(r.ReviewerFamily) || r.AuthorFamily == r.ReviewerFamily) {
		return false, nil
	}
	err = currentRunTargetTx(ctx, tx, r)
	if errors.Is(err, ErrRunBinding) {
		return false, nil
	}
	return err == nil, err
}
