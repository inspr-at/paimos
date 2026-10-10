// SPDX-License-Identifier: AGPL-3.0-only

package approvals

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/deploytarget"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Approval is one permission request and, once a person has decided, that decision.
// Revocation stays on the grant and in approval.revoked; this object keeps the
// contract shape, which has no revoked field.
type Approval struct {
	ID                   string  `json:"id"`
	AgentPrincipalID     string  `json:"agent_principal_id"`
	AgentName            *string `json:"agent_name,omitempty"`
	projectID            *string
	Risk                 string               `json:"risk"`
	Scope                string               `json:"scope"`
	ResourceKind         string               `json:"resource_kind"`
	ResourceID           *string              `json:"resource_id"`
	RunID                *string              `json:"run_id"`
	Rationale            string               `json:"rationale"`
	ExpiresAt            time.Time            `json:"expires_at"`
	ProposedAt           time.Time            `json:"proposed_at"`
	Decision             *string              `json:"decision"`
	DecidedByPrincipalID *string              `json:"decided_by_principal_id"`
	Target               *deploytarget.Target `json:"target,omitempty"`
	TargetDigestSHA256   string               `json:"target_digest_sha256,omitempty"`
}

type proposal struct {
	Scope              string               `json:"scope"`
	ResourceKind       string               `json:"resource_kind"`
	ResourceID         *string              `json:"resource_id"`
	RunID              *string              `json:"run_id"`
	Rationale          string               `json:"rationale"`
	ExpiresAt          time.Time            `json:"expires_at"`
	Target             *deploytarget.Target `json:"target,omitempty"`
	TargetDigestSHA256 string               `json:"-"`
}

type decisionWrite struct {
	Decision string  `json:"decision"`
	Reason   *string `json:"reason"`
}

func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person && p.Kind != tenant.Agent {
		writeError(w, 403, "forbidden")
		return
	}
	id := r.PathValue("approvalId")
	if !uuidPattern.MatchString(id) {
		writeError(w, 404, "approval not found")
		return
	}
	var item Approval
	err := m.inTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		item, err = loadApproval(r.Context(), tx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(404, "approval not found")
		}
		if err != nil {
			return err
		}
		if p.Kind == tenant.Agent && item.AgentPrincipalID != p.ID {
			return fail(404, "approval not found")
		}
		if err = approvalVisible(r.Context(), tx, p, item); err != nil {
			if errors.Is(err, authz.ErrForbidden) {
				return fail(404, "approval not found")
			}
			return err
		}
		return exposeAgentName(r.Context(), tx, p, &item)
	})
	writeResult(w, 200, item, err)
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	q := r.URL.Query()
	if q.Has("pending") && q.Get("pending") != "true" && q.Get("pending") != "false" {
		writeError(w, http.StatusBadRequest, "pending must be true or false")
		return
	}
	items, err := m.list(r.Context(), p, limit, q.Get("pending") == "true")
	if err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (m *Module) handlePropose(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Agent {
		writeError(w, http.StatusForbidden, "only an agent may propose its own permission")
		return
	}
	var in proposal
	if err := decodeJSON(w, r, &in); err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	if err := validateProposal(&in); err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	out, err := m.propose(r.Context(), p, r.Header.Get("Authorization"), in)
	writeResult(w, http.StatusCreated, out, err)
}

func (m *Module) handleDecide(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in decisionWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	reason, err := validateDecision(in)
	if err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	out, err := m.decide(r.Context(), p, id, in.Decision, reason)
	writeResult(w, http.StatusOK, out, err)
}

func (m *Module) handleRevoke(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := m.revoke(r.Context(), p, id)
	writeResult(w, http.StatusOK, out, err)
}

func requirePrincipal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !uuidPattern.MatchString(p.ID) || !uuidPattern.MatchString(p.TenantID) {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return tenant.Principal{}, false
	}
	return p, true
}

func pathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("approvalId")
	if !uuidPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "approval not found")
		return "", false
	}
	return id, true
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return 50, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 200 {
		return 0, fail(http.StatusBadRequest, "limit must be from 1 to 200")
	}
	return n, nil
}

func validateProposal(in *proposal) error {
	in.Rationale = strings.TrimSpace(in.Rationale)
	if in.Rationale == "" || utf8.RuneCountInString(in.Rationale) > 8000 || strings.ContainsRune(in.Rationale, 0) {
		return fail(http.StatusBadRequest, "rationale is required")
	}
	if !scopePattern.MatchString(in.Scope) {
		return fail(http.StatusBadRequest, "invalid scope")
	}
	if approvalPermission(in.Scope) == "" {
		return fail(http.StatusBadRequest, "unknown permission")
	}
	switch in.ResourceKind {
	case "tenant", "node", "run":
	default:
		return fail(http.StatusBadRequest, "invalid resource_kind")
	}
	if in.ResourceID != nil && !uuidPattern.MatchString(*in.ResourceID) {
		return fail(http.StatusBadRequest, "invalid resource_id")
	}
	if in.RunID != nil && !uuidPattern.MatchString(*in.RunID) {
		return fail(http.StatusBadRequest, "invalid run_id")
	}
	if in.ExpiresAt.IsZero() || !in.ExpiresAt.After(time.Now()) {
		return fail(http.StatusBadRequest, "expires_at must be in the future")
	}
	if in.ResourceKind == "tenant" && in.ResourceID != nil {
		return fail(http.StatusBadRequest, "tenant approval has no resource_id")
	}
	if in.ResourceKind != "tenant" && in.ResourceID == nil {
		return fail(http.StatusBadRequest, "resource_id is required")
	}
	if in.ResourceKind == "run" && in.RunID != nil && *in.RunID != *in.ResourceID {
		return fail(http.StatusBadRequest, "run_id does not match resource_id")
	}
	if in.Target != nil && (in.Scope == "journey.deploy" || in.Scope == "stage.deploy") {
		var err error
		in.Target, in.TargetDigestSHA256, err = deploytarget.Normalize(in.Target)
		if err != nil {
			return fail(http.StatusBadRequest, err.Error())
		}
	} else if in.Target != nil {
		return fail(http.StatusBadRequest, "target is only used for deploy approvals")
	}
	return nil
}

func validateDecision(in decisionWrite) (string, error) {
	if in.Decision != "approved" && in.Decision != "denied" {
		return "", fail(http.StatusBadRequest, "invalid decision")
	}
	reason := ""
	if in.Reason != nil {
		reason = strings.TrimSpace(*in.Reason)
		if utf8.RuneCountInString(reason) > 4000 || strings.ContainsRune(reason, 0) {
			return "", fail(http.StatusBadRequest, "invalid reason")
		}
	}
	return reason, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	body, err := io.ReadAll(r.Body)
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		return fail(http.StatusBadRequest, "bad request")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fail(http.StatusBadRequest, "bad request")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fail(http.StatusBadRequest, "bad request")
	}
	return nil
}
