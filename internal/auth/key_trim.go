// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

var (
	errTrimInvalid = errors.New("invalid key trim input")
	errTrimChanged = errors.New("the key scopes changed; prepare a new proposal")
	errTrimRecent  = errors.New("a dropped scope was used in the last 24 hours")
	errTrimExpired = errors.New("the proposal expired")
	errTrimReplay  = errors.New("the request conflicts with an earlier decision")
	errTrimRestore = errors.New("the 30-day restore window is closed")
)

type trimRisk struct {
	Scope    string `json:"scope"`
	Evidence string `json:"evidence"`
	Risk     string `json:"risk_if_dropped"`
}
type trimEvidence struct {
	Summary    string     `json:"summary"`
	ObservedAt time.Time  `json:"observed_at"`
	Risks      []trimRisk `json:"risks"`
}
type trimUsage struct {
	Scope      string     `json:"scope"`
	LastUsedAt *time.Time `json:"last_used_at"`
}
type trimInput struct {
	RequestID string       `json:"request_id"`
	Expected  []string     `json:"expected_scopes"`
	Candidate []string     `json:"candidate_scopes"`
	Evidence  trimEvidence `json:"evidence"`
	ExpiresAt time.Time    `json:"expires_at"`
}
type trimDecision struct {
	RequestID      string `json:"request_id"`
	ExpectedDigest string `json:"expected_digest"`
	Decision       string `json:"decision,omitempty"`
}
type trimProposal struct {
	ID              string       `json:"id"`
	KeyID           string       `json:"key_id"`
	KeyName         string       `json:"key_name"`
	OwnerID         string       `json:"owner_id"`
	OwnerName       string       `json:"owner_name"`
	Previous        []string     `json:"previous_scopes"`
	SnapshotDigest  string       `json:"snapshot_digest"`
	Candidate       []string     `json:"candidate_scopes"`
	CandidateDigest string       `json:"candidate_digest"`
	Evidence        trimEvidence `json:"evidence"`
	Usage           []trimUsage  `json:"usage"`
	CreatedBy       string       `json:"created_by"`
	CreatedAt       time.Time    `json:"created_at"`
	ExpiresAt       time.Time    `json:"expires_at"`
	State           string       `json:"state"`
	Revision        int64        `json:"revision"`
	AppliedAt       *time.Time   `json:"applied_at"`
	RestoreUntil    *time.Time   `json:"restore_until"`
	BlockedReason   string       `json:"blocked_reason,omitempty"`

	requestID, requestDigest                                         string
	decisionRequest, decidedBy, decision, restoreRequest, restoredBy *string
}

// Scope-set digest: canonical dot notation, sorted and unique. It contains no
// credential input and does not depend on array order or legacy colon spelling.
func trimScopes(in []string) ([]string, error) {
	if in == nil || len(in) > maxScopeInput {
		return nil, errTrimInvalid
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ReplaceAll(s, ":", ".")
		if s == "" || len(s) > 128 || strings.ContainsAny(s, " \t\r\n\x00") {
			return nil, errTrimInvalid
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return slices.Compact(out), nil
}
func trimScopeDigest(scopes []string) string {
	data, _ := json.Marshal(scopes)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func trimDifference(before, after []string) []string {
	out := []string{}
	for _, scope := range before {
		if !slices.Contains(after, scope) {
			out = append(out, scope)
		}
	}
	return out
}
func validateTrimInput(in *trimInput) error {
	if !uuidRe.MatchString(in.RequestID) {
		return errTrimInvalid
	}
	in.RequestID = strings.ToLower(in.RequestID)
	var err error
	in.Expected, err = trimScopes(in.Expected)
	if err != nil {
		return err
	}
	in.Candidate, err = trimScopes(in.Candidate)
	if err != nil {
		return err
	}
	if len(in.Candidate) >= len(in.Expected) || len(trimDifference(in.Candidate, in.Expected)) > 0 {
		return errTrimInvalid
	}
	for _, scope := range in.Candidate {
		perm, ok := authz.Lookup(scope)
		if !ok || !perm.AgentGrantable {
			return errTrimInvalid
		}
	}
	if strings.TrimSpace(in.Evidence.Summary) == "" || len(in.Evidence.Summary) > 2000 || in.Evidence.ObservedAt.IsZero() || len(in.Evidence.Risks) > maxScopeInput {
		return errTrimInvalid
	}
	dropped := trimDifference(in.Expected, in.Candidate)
	if len(in.Evidence.Risks) != len(dropped) {
		return errTrimInvalid
	}
	seen := map[string]bool{}
	for i, risk := range in.Evidence.Risks {
		risk.Scope = strings.ReplaceAll(risk.Scope, ":", ".")
		if !slices.Contains(dropped, risk.Scope) || seen[risk.Scope] || strings.TrimSpace(risk.Evidence) == "" || len(risk.Evidence) > 2000 || strings.TrimSpace(risk.Risk) == "" || len(risk.Risk) > 1000 {
			return errTrimInvalid
		}
		seen[risk.Scope] = true
		in.Evidence.Risks[i] = risk
	}
	slices.SortFunc(in.Evidence.Risks, func(a, b trimRisk) int { return strings.Compare(a.Scope, b.Scope) })
	return nil
}
func decodeTrim(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeBadRequest(w, "invalid key trim JSON")
		return false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		writeBadRequest(w, "expected one JSON object")
		return false
	}
	return true
}
func (m *Module) trimClock() time.Time {
	if m.trimNow != nil {
		return m.trimNow().UTC()
	}
	return time.Now().UTC()
}
func (m *Module) trimReply(w http.ResponseWriter, status int, value any, err error) {
	switch {
	case errors.Is(err, authz.ErrForbidden):
		writeForbidden(w)
	case errors.Is(err, errNotFound), errors.Is(err, pgx.ErrNoRows):
		writeJSON(w, 404, errorJSON{Error: "key trim resource not found"})
	case errors.Is(err, errTrimInvalid):
		writeBadRequest(w, errTrimInvalid.Error())
	case errors.Is(err, errTrimChanged), errors.Is(err, errTrimRecent), errors.Is(err, errTrimExpired), errors.Is(err, errTrimReplay), errors.Is(err, errTrimRestore), errors.Is(err, errKeyInactive):
		writeJSON(w, 409, errorJSON{Error: err.Error()})
	case err != nil:
		writeInternal(w)
	default:
		writeJSON(w, status, value)
	}
}
func (m *Module) handleProposeKeyTrim(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w)
		return
	}
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeBadRequest(w, "invalid key id")
		return
	}
	var in trimInput
	if !decodeTrim(w, r, &in) {
		return
	}
	if err := validateTrimInput(&in); err != nil {
		m.trimReply(w, 0, nil, err)
		return
	}
	out, err := m.proposeKeyTrim(r.Context(), p, id, in)
	m.trimReply(w, 201, out, err)
}
func trimPerson(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w)
		return p, false
	}
	if p.Kind != tenant.Person || p.KeyCreatorID != "" || p.KeyID != "" || r.Header.Get("Authorization") != "" {
		writeForbidden(w)
		return p, false
	}
	return p, true
}
func (m *Module) handleDecideKeyTrim(w http.ResponseWriter, r *http.Request) {
	p, ok := trimPerson(w, r)
	if !ok {
		return
	}
	id := r.PathValue("proposalId")
	if !uuidRe.MatchString(id) {
		writeBadRequest(w, "invalid proposal id")
		return
	}
	var in trimDecision
	if !decodeTrim(w, r, &in) {
		return
	}
	restore := strings.HasSuffix(r.URL.Path, "/restore")
	if !uuidRe.MatchString(in.RequestID) || len(in.ExpectedDigest) != 64 || strings.Trim(in.ExpectedDigest, "0123456789abcdef") != "" || restore && in.Decision != "" || !restore && in.Decision != "approve" && in.Decision != "decline" {
		writeBadRequest(w, "invalid decision")
		return
	}
	out, err := m.decideKeyTrim(r.Context(), p, id, in, restore)
	m.trimReply(w, 200, out, err)
}
func (m *Module) handleListKeyTrims(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	p, ok := trimPerson(w, r)
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "pending"
	}
	limit := 50
	var err error
	if r.URL.Query().Has("limit") {
		limit, err = strconv.Atoi(r.URL.Query().Get("limit"))
	}
	cursor := r.URL.Query().Get("cursor")
	if err != nil || limit < 1 || limit > 100 || state != "pending" && state != "decided" || cursor != "" && !uuidRe.MatchString(cursor) {
		writeBadRequest(w, "invalid key trim page")
		return
	}
	items := []trimProposal{}
	hasMore := false
	err = m.inTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(r.Context(), tx, p, "keys.manage", authz.Scope{}); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT id::text FROM key_trim_proposals WHERE tenant_id=$1::uuid AND
   (CASE WHEN $2='pending' THEN state='pending' AND expires_at>$3 ELSE state<>'pending' OR expires_at<=$3 END)
   AND ($4='' OR id>nullif($4,'')::uuid) ORDER BY id LIMIT $5`, p.TenantID, state, m.trimClock(), cursor, limit+1)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		hasMore = len(ids) > limit
		if hasMore {
			ids = ids[:limit]
		}
		for _, id := range ids {
			proposal, err := readTrimTx(r.Context(), tx, p.TenantID, id, false)
			if err != nil {
				return err
			}
			key, err := readTrimKeyTx(r.Context(), tx, p.TenantID, proposal.KeyID, false)
			if err != nil {
				return err
			}
			if err := m.decorateTrimTx(r.Context(), tx, p, &proposal, key); err != nil {
				return err
			}
			items = append(items, proposal)
		}
		return nil
	})
	out := map[string]any{"items": items, "has_more": hasMore}
	if hasMore {
		out["next_cursor"] = items[len(items)-1].ID
	}
	m.trimReply(w, 200, out, err)
}

// This metadata reader deliberately does not use keyRecord/keyJSON/keySnapshot,
// because those legacy management surfaces carry a prefix.
type trimKey struct {
	ID, PrincipalID, Name, OwnerName string
	Scopes                           []string
	CreatorID                        *string
	ExpiresAt, RevokedAt             *time.Time
	Active                           bool
}

func readTrimKeyTx(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (trimKey, error) {
	key := trimKey{}
	query := `SELECT k.id::text,k.principal_id::text,k.name,p.name,k.scopes,k.created_by_principal_id::text,k.expires_at,k.revoked_at,p.status='active'
  FROM agent_keys k JOIN principals p ON p.tenant_id=k.tenant_id AND p.id=k.principal_id
  WHERE k.tenant_id=$1::uuid AND k.id=$2::uuid`
	if lock {
		query += ` FOR NO KEY UPDATE OF k`
	}
	err := tx.QueryRow(ctx, query, tenantID, id).Scan(&key.ID, &key.PrincipalID, &key.Name, &key.OwnerName, &key.Scopes, &key.CreatorID, &key.ExpiresAt, &key.RevokedAt, &key.Active)
	return key, err
}
func readTrimTx(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (trimProposal, error) {
	out := trimProposal{}
	var evidence, usage []byte
	query := `SELECT id::text,key_id::text,previous_scopes,snapshot_digest,candidate_scopes,candidate_digest,evidence,usage,
  created_by::text,created_at,expires_at,state,revision,applied_at,restore_until,request_id::text,request_digest,
  decision_request_id::text,decided_by::text,decision,restore_request_id::text,restored_by::text
  FROM key_trim_proposals WHERE tenant_id=$1::uuid AND id=$2::uuid`
	if lock {
		query += ` FOR UPDATE`
	}
	err := tx.QueryRow(ctx, query, tenantID, id).Scan(&out.ID, &out.KeyID, &out.Previous, &out.SnapshotDigest, &out.Candidate, &out.CandidateDigest, &evidence, &usage, &out.CreatedBy, &out.CreatedAt, &out.ExpiresAt, &out.State, &out.Revision, &out.AppliedAt, &out.RestoreUntil, &out.requestID, &out.requestDigest, &out.decisionRequest, &out.decidedBy, &out.decision, &out.restoreRequest, &out.restoredBy)
	if err == nil {
		err = json.Unmarshal(evidence, &out.Evidence)
	}
	if err == nil {
		err = json.Unmarshal(usage, &out.Usage)
	}
	return out, err
}
func trimFence(ctx context.Context, tx pgx.Tx, p tenant.Principal, permission string) error {
	// The complete key admission batch is already held before this tenant
	// access fence, whether this is an agent proposal or a person decision.
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, p.TenantID).Scan(&locked); err != nil {
		return err
	}
	return authz.RequireTx(ctx, tx, p, permission, authz.Scope{})
}
func activeTrimKey(key trimKey, now time.Time) error {
	if !key.Active || key.RevokedAt != nil || key.ExpiresAt != nil && !key.ExpiresAt.After(now) {
		return errKeyInactive
	}
	return nil
}
func recentTrimUseTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, keyID string, dropped []string, now time.Time) error {
	var recent bool
	// Debounce can lag actual usage by <1 minute: keep a conservative margin.
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_key_scope_usage WHERE tenant_id=$1::uuid AND key_id=$2::uuid
  AND scope=ANY($3::text[]) AND last_used_at>$4::timestamptz-interval '24 hours 1 minute')`, p.TenantID, keyID, dropped, now).Scan(&recent)
	if err != nil {
		return err
	}
	if recent {
		return errTrimRecent
	}
	return nil
}
func trimUsageTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, keyID string, scopes []string) ([]trimUsage, error) {
	result := make([]trimUsage, 0, len(scopes))
	rows, err := tx.Query(ctx, `SELECT s.scope,u.last_used_at FROM unnest($3::text[]) s(scope)
  LEFT JOIN agent_key_scope_usage u ON u.tenant_id=$1::uuid AND u.key_id=$2::uuid AND u.scope=s.scope ORDER BY s.scope`, p.TenantID, keyID, scopes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u trimUsage
		if err := rows.Scan(&u.Scope, &u.LastUsedAt); err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, rows.Err()
}
func trimGrantTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, key trimKey, scopes []string) error {
	editor, err := authz.EffectiveTx(ctx, tx, p, "")
	if err != nil {
		return err
	}
	agent := tenant.Principal{ID: key.PrincipalID, TenantID: p.TenantID, Kind: tenant.Agent}
	if key.CreatorID != nil {
		agent.KeyCreatorID = *key.CreatorID
	}
	ceiling, err := authz.AgentKeyCeilingTx(ctx, tx, agent)
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		perm, ok := authz.Lookup(scope)
		if !ok || !perm.AgentGrantable || !slices.Contains(editor.Workspace.Permissions, scope) || !slices.Contains(ceiling, scope) {
			return authz.ErrForbidden
		}
	}
	return nil
}
func (m *Module) decorateTrimTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, out *trimProposal, key trimKey) error {
	out.KeyName, out.OwnerID, out.OwnerName = key.Name, key.PrincipalID, key.OwnerName
	now := m.trimClock()
	if out.State == "pending" {
		if !out.ExpiresAt.After(now) {
			out.State = "expired"
			out.BlockedReason = errTrimExpired.Error()
			return nil
		}
		scopes, err := trimScopes(key.Scopes)
		if err != nil {
			return err
		}
		switch {
		case activeTrimKey(key, now) != nil:
			out.BlockedReason = errKeyInactive.Error()
		case trimScopeDigest(scopes) != out.SnapshotDigest:
			out.BlockedReason = errTrimChanged.Error()
		default:
			err := recentTrimUseTx(ctx, tx, p, key.ID, trimDifference(out.Previous, out.Candidate), now)
			if errors.Is(err, errTrimRecent) {
				out.BlockedReason = errTrimRecent.Error()
			} else if err != nil {
				return err
			}
		}
	}
	return nil
}
func (m *Module) proposeKeyTrim(ctx context.Context, p tenant.Principal, keyID string, in trimInput) (trimProposal, error) {
	out := trimProposal{}
	keyID = strings.ToLower(keyID)
	ctx = tenant.WithPrincipal(ctx, p)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if p.ID == "" || p.Kind != tenant.Agent && p.Kind != tenant.Person {
		return out, authz.ErrForbidden
	}
	if err := validateTrimInput(&in); err != nil {
		return out, err
	}
	payload, _ := json.Marshal(struct {
		KeyID string
		Input trimInput
	}{keyID, in})
	fingerprint := trimScopeDigest([]string{string(payload)})
	ctx = db.WithKeyScopeUse(ctx, p.TenantID, keyID)
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := trimFence(ctx, tx, p, "approvals.request"); err != nil {
			return err
		}
		key, err := readTrimKeyTx(ctx, tx, p.TenantID, keyID, true)
		if err != nil {
			return err
		}
		// Existing requests are replayed only with the same actor and exact payload.
		var existing string
		err = tx.QueryRow(ctx, `SELECT id::text FROM key_trim_proposals WHERE tenant_id=$1::uuid AND created_by=$2::uuid AND request_id=$3::uuid`, p.TenantID, p.ID, in.RequestID).Scan(&existing)
		if err == nil {
			out, err = readTrimTx(ctx, tx, p.TenantID, existing, false)
			if err != nil {
				return err
			}
			if out.requestDigest != fingerprint {
				return errTrimReplay
			}
			return m.decorateTrimTx(ctx, tx, p, &out, key)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		now := m.trimClock()
		if !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(7*24*time.Hour)) || in.Evidence.ObservedAt.After(now) {
			return errTrimInvalid
		}
		if err := activeTrimKey(key, now); err != nil {
			return err
		}
		live, err := trimScopes(key.Scopes)
		if err != nil {
			return err
		}
		if !slices.Equal(live, in.Expected) {
			return errTrimChanged
		}
		if err := recentTrimUseTx(ctx, tx, p, keyID, trimDifference(live, in.Candidate), now); err != nil {
			return err
		}
		usage, err := trimUsageTx(ctx, tx, p, keyID, live)
		if err != nil {
			return err
		}
		evidenceJSON, _ := json.Marshal(in.Evidence)
		usageJSON, _ := json.Marshal(usage)
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO key_trim_proposals(tenant_id,key_id,created_by,request_id,request_digest,previous_scopes,snapshot_digest,candidate_scopes,candidate_digest,evidence,usage,created_at,expires_at)
   VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb,$12,$13) RETURNING id::text`,
			p.TenantID, keyID, p.ID, in.RequestID, fingerprint, live, trimScopeDigest(live), in.Candidate, trimScopeDigest(in.Candidate), evidenceJSON, usageJSON, now, in.ExpiresAt).Scan(&id); err != nil {
			return err
		}
		out, err = readTrimTx(ctx, tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		if err := m.decorateTrimTx(ctx, tx, p, &out, key); err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "agent_key.trim_proposed", After: map[string]any{"proposal_id": out.ID, "key_id": keyID, "previous_scopes": live, "candidate_scopes": in.Candidate, "expires_at": in.ExpiresAt}})
		return err
	})
	return out, err
}
func (m *Module) decideKeyTrim(ctx context.Context, p tenant.Principal, id string, in trimDecision, restore bool) (trimProposal, error) {
	out := trimProposal{}
	in.RequestID = strings.ToLower(in.RequestID)
	ctx = tenant.WithPrincipal(ctx, p)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if p.Kind != tenant.Person || p.ID == "" || p.KeyID != "" || p.KeyCreatorID != "" {
		return out, authz.ErrForbidden
	}
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Protect metadata discovery too; trimFence repeats the check under
		// the access-change lock before any mutation.
		if err := authz.RequireTx(ctx, tx, p, "keys.manage", authz.Scope{}); err != nil {
			return err
		}
		// Discover the immutable key identity without taking any resource lock.
		// The key admission fence must precede the tenant access fence.
		var keyID string
		if err := tx.QueryRow(ctx, `SELECT key_id::text FROM key_trim_proposals WHERE tenant_id=$1::uuid AND id=$2::uuid`, p.TenantID, id).Scan(&keyID); err != nil {
			return err
		}
		if err := authz.LockKeyScopeUseTx(ctx, tx, p.TenantID, keyID); err != nil {
			return err
		}
		if err := trimFence(ctx, tx, p, "keys.manage"); err != nil {
			return err
		}
		key, err := readTrimKeyTx(ctx, tx, p.TenantID, keyID, true)
		if err != nil {
			return err
		}
		out, err = readTrimTx(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if out.KeyID != key.ID {
			return errTrimChanged
		}
		now := m.trimClock()
		before, err := trimScopes(key.Scopes)
		if err != nil {
			return err
		}
		after := out.Candidate
		if restore {
			if in.ExpectedDigest != out.CandidateDigest {
				return errTrimChanged
			}
			if out.restoreRequest != nil {
				if *out.restoreRequest != in.RequestID || out.restoredBy == nil || *out.restoredBy != p.ID {
					return errTrimReplay
				}
				return m.decorateTrimTx(ctx, tx, p, &out, key)
			}
			if out.State != "applied" || out.RestoreUntil == nil || !out.RestoreUntil.After(now) {
				return errTrimRestore
			}
			if err := activeTrimKey(key, now); err != nil {
				return err
			}
			if trimScopeDigest(before) != out.CandidateDigest {
				return errTrimChanged
			}
			after = out.Previous
		} else {
			if in.ExpectedDigest != out.SnapshotDigest {
				return errTrimChanged
			}
			if out.decisionRequest != nil {
				if *out.decisionRequest != in.RequestID || out.decidedBy == nil || *out.decidedBy != p.ID || out.decision == nil || *out.decision != in.Decision {
					return errTrimReplay
				}
				return m.decorateTrimTx(ctx, tx, p, &out, key)
			}
			if out.State != "pending" {
				return errTrimReplay
			}
			if !out.ExpiresAt.After(now) {
				return errTrimExpired
			}
			if in.Decision == "decline" {
				if _, err := tx.Exec(ctx, `UPDATE key_trim_proposals SET state='declined',revision=revision+1,decision_request_id=$3::uuid,decided_by=$4::uuid,decision='decline' WHERE tenant_id=$1::uuid AND id=$2::uuid`, p.TenantID, id, in.RequestID, p.ID); err != nil {
					return err
				}
				out, err = readTrimTx(ctx, tx, p.TenantID, id, false)
				if err != nil {
					return err
				}
				if err := m.decorateTrimTx(ctx, tx, p, &out, key); err != nil {
					return err
				}
				_, err = events.Append(ctx, tx, p, events.Change{Type: "agent_key.trim_declined", After: map[string]any{"proposal_id": id, "key_id": keyID}})
				return err
			}
			if in.Decision != "approve" {
				return errTrimInvalid
			}
			if err := activeTrimKey(key, now); err != nil {
				return err
			}
			if trimScopeDigest(before) != out.SnapshotDigest {
				return errTrimChanged
			}
			if err := recentTrimUseTx(ctx, tx, p, keyID, trimDifference(out.Previous, out.Candidate), now); err != nil {
				return err
			}
		}
		if err := trimGrantTx(ctx, tx, p, key, after); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_keys SET scopes=$3::text[] WHERE tenant_id=$1::uuid AND id=$2::uuid`, p.TenantID, keyID, after); err != nil {
			return err
		}
		if restore {
			_, err = tx.Exec(ctx, `UPDATE key_trim_proposals SET state='restored',revision=revision+1,restore_request_id=$3::uuid,restored_by=$4::uuid WHERE tenant_id=$1::uuid AND id=$2::uuid`, p.TenantID, id, in.RequestID, p.ID)
		} else {
			_, err = tx.Exec(ctx, `UPDATE key_trim_proposals SET state='applied',revision=revision+1,decision_request_id=$3::uuid,decided_by=$4::uuid,decision='approve',applied_at=$5,restore_until=$6 WHERE tenant_id=$1::uuid AND id=$2::uuid`, p.TenantID, id, in.RequestID, p.ID, now, now.Add(30*24*time.Hour))
		}
		if err != nil {
			return err
		}
		out, err = readTrimTx(ctx, tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		if err := m.decorateTrimTx(ctx, tx, p, &out, key); err != nil {
			return err
		}
		// Last lock: events.Append takes the event counter. Never read/write records
		// requiring a new lock after this point. Audit snapshots deliberately omit
		// legacy keySnapshot's prefix as well as any credential material.
		_, err = events.Append(ctx, tx, p, events.Change{Type: "agent_key.scopes_changed",
			Before: map[string]any{"key_id": keyID, "principal_id": key.PrincipalID, "scopes": before},
			After:  map[string]any{"key_id": keyID, "principal_id": key.PrincipalID, "scopes": after, "proposal_id": id, "restored": restore}})
		return err
	})
	return out, err
}
