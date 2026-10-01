// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type accountLinkReview struct {
	RequestID  string    `json:"request_id"`
	TenantID   string    `json:"tenant_id"`
	TenantName string    `json:"tenant_name"`
	AccountID  string    `json:"account_id"`
	Harness    string    `json:"harness"`
	Label      string    `json:"account_label"`
	Computer   string    `json:"computer_name"`
	PersonID   string    `json:"person_id"`
	PersonName string    `json:"person_name"`
	Revision   int64     `json:"revision"`
	State      string    `json:"state"`
	ExpiresAt  time.Time `json:"expires_at"`
	Digest     string    `json:"request_digest"`
}

func (m *Module) mountAccountLink(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/agent-pairing/account-link", m.accountLinkDevice)
	mux.HandleFunc("POST /api/agent-pairing/account-link/lookup", m.person("profile.write", m.accountLinkLookup))
	mux.HandleFunc("POST /api/agent-pairing/account-link/{requestId}/approve", m.person("profile.write", m.accountLinkApprove))
	mux.HandleFunc("GET /api/agent-pairing/account-links", m.person("profile.write", m.accountLinks))
	mux.HandleFunc("POST /api/agent-pairing/account-links/{accountId}/unlink", m.person("profile.write", m.accountUnlink))
}

func (m *Module) accountLinkDevice(w http.ResponseWriter, r *http.Request) {
	if m.origin == "" {
		WriteError(w, fail(503, "unavailable", "account linking needs the configured instance origin"))
		return
	}
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.Kind != tenant.Agent || authz.Require(authz.BindPool(r.Context(), m.pool), "account.probe", authz.Scope{}) != nil {
		WriteError(w, fail(403, "forbidden", "paired runtime required"))
		return
	}
	var in struct {
		AccountID string `json:"account_id"`
		Proof     string `json:"device_proof"`
		Operation string `json:"operation"`
		RequestID string `json:"request_id,omitempty"`
	}
	if err := decode(w, r, &in); err != nil {
		WriteError(w, err)
		return
	}
	if !uuidRE.MatchString(in.AccountID) || !hashRE.MatchString(in.Proof) || (in.Operation != "offer" && in.Operation != "poll" && in.Operation != "renew") || (in.Operation == "poll" && !uuidRE.MatchString(in.RequestID)) || (in.Operation != "poll" && in.RequestID != "") {
		WriteError(w, fail(400, "invalid_request", "invalid account link request"))
		return
	}
	bucket, max := "link_offer", 60
	if in.Operation == "poll" {
		bucket, max = "link_poll", 2400
	}
	if err := m.limit(r.Context(), p.TenantID, bucket, max); err != nil {
		WriteError(w, err)
		return
	}
	var out agentsetup.AccountLinkView
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var proof string
		var owner *string
		var revision int64
		var now time.Time
		err := tx.QueryRow(r.Context(), `SELECT c.lifecycle_hash,a.owner_person_id::text,a.link_revision,clock_timestamp()
FROM agent_accounts a JOIN agent_pairing_enrollments e ON e.tenant_id=a.tenant_id AND e.account_id=a.id
JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
WHERE a.id=$1 AND a.registered_by_principal_id=$2 AND c.principal_id=$2 AND a.daemon_id=c.daemon_id
AND c.state='connected' AND e.state='connected' AND c.archived_at IS NULL AND a.archived_at IS NULL FOR UPDATE OF a`, in.AccountID, p.ID).Scan(&proof, &owner, &revision, &now)
		if err != nil {
			return notFound(err)
		}
		if subtle.ConstantTimeCompare([]byte(proof), []byte(digest(in.Proof))) != 1 {
			return fail(404, "not_found", "connected account proof not found")
		}
		if err = AccountFence(r.Context(), tx, in.AccountID, false); err != nil {
			return err
		}
		out.AccountID = in.AccountID
		if in.Operation == "poll" {
			var oldRevision int64
			var linkedPerson *string
			var expiry time.Time
			err = tx.QueryRow(r.Context(), `SELECT state,expires_at,account_revision,person_id::text FROM account_person_link_requests WHERE id=$1 AND account_id=$2`, in.RequestID, in.AccountID).Scan(&out.State, &expiry, &oldRevision, &linkedPerson)
			if err != nil {
				return notFound(err)
			}
			out.RequestID = in.RequestID
			if oldRevision != revision || out.State == "linked" && (owner == nil || linkedPerson == nil || *owner != *linkedPerson) {
				out.State = "revoked"
			}
			if out.State == "pending" && !expiry.After(now) {
				out.State = "expired"
			}
			if out.State == "linked" {
				if err = tx.QueryRow(r.Context(), `SELECT name FROM principals WHERE id=$1 AND kind='person'`, owner).Scan(&out.PersonName); err != nil {
					return err
				}
				reported, err := tx.Exec(r.Context(), `UPDATE account_person_link_requests SET result_reported_at=clock_timestamp() WHERE id=$1 AND result_reported_at IS NULL`, in.RequestID)
				if err != nil {
					return err
				}
				out.ShowResult = reported.RowsAffected() == 1
				return nil
			}
			return nil
		}
		if owner != nil {
			out.State = "linked"
			return tx.QueryRow(r.Context(), `SELECT name FROM principals WHERE id=$1 AND kind='person'`, owner).Scan(&out.PersonName)
		}
		var expiry time.Time
		err = tx.QueryRow(r.Context(), `SELECT id::text,state,expires_at FROM account_person_link_requests WHERE account_id=$1 AND account_revision=$2 ORDER BY created_at DESC,id DESC LIMIT 1`, in.AccountID, revision).Scan(&out.RequestID, &out.State, &expiry)
		if err == nil {
			if out.State == "pending" && !expiry.After(now) {
				out.State = "expired"
			}
			if in.Operation != "renew" {
				return nil
			}
			if out.State == "pending" {
				return fail(409, "conflict", "account code is still pending")
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err = tx.Exec(r.Context(), `UPDATE account_person_link_requests SET state='expired' WHERE account_id=$1 AND state='pending'`, in.AccountID); err != nil {
			return err
		}
		var code string
		for i := 0; i < 16; i++ {
			n, err := rand.Int(rand.Reader, big.NewInt(1000000))
			if err != nil {
				return err
			}
			code = fmt.Sprintf("%06d", n.Int64())
			var exists bool
			if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM account_person_link_requests WHERE user_code=$1)`, code).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				break
			}
			code = ""
		}
		if code == "" {
			return fail(429, "rate_limited", "try again later")
		}
		if err = tx.QueryRow(r.Context(), `INSERT INTO account_person_link_requests(tenant_id,account_id,user_code,account_revision) VALUES($1,$2,$3,$4) RETURNING id::text,expires_at`, p.TenantID, in.AccountID, code, revision).Scan(&out.RequestID, &expiry); err != nil {
			return err
		}
		out.State = "pending"
		out.ShowPrompt = true
		out.Code = code[:3] + " " + code[3:]
		out.URI = m.origin + "/link"
		out.ExpiresAt = &expiry
		return m.accountLinkEvent(r.Context(), tx, p, "account.link_offered", in.AccountID, "", revision)
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}

func readAccountLink(ctx context.Context, tx pgx.Tx, id string, p tenant.Principal) (accountLinkReview, error) {
	var v accountLinkReview
	if !uuidRE.MatchString(id) {
		return v, fail(404, "not_found", "account link not found")
	}
	err := tx.QueryRow(ctx, `SELECT l.id::text,l.tenant_id::text,t.name,a.id::text,a.harness,a.label,q.details->>'computer_name',a.link_revision,l.state,l.expires_at
FROM account_person_link_requests l JOIN agent_accounts a ON a.tenant_id=l.tenant_id AND a.id=l.account_id
JOIN tenants t ON t.id=l.tenant_id JOIN agent_pairing_enrollments e ON e.tenant_id=a.tenant_id AND e.account_id=a.id
JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
WHERE l.id=$1 AND a.archived_at IS NULL AND l.account_revision=a.link_revision FOR UPDATE OF a,l`, id).Scan(&v.RequestID, &v.TenantID, &v.TenantName, &v.AccountID, &v.Harness, &v.Label, &v.Computer, &v.Revision, &v.State, &v.ExpiresAt)
	if err != nil {
		return v, notFound(err)
	}
	v.PersonID, v.PersonName = p.ID, p.Name
	b, err := json.Marshal(v)
	if err != nil {
		return v, err
	}
	v.Digest = digest(string(b))
	return v, nil
}
func pendingAccountLink(ctx context.Context, tx pgx.Tx, v accountLinkReview) error {
	if v.State != "pending" {
		return fail(410, "code_expired", "account code expired or was used")
	}
	if err := AccountFence(ctx, tx, v.AccountID, false); err != nil {
		return err
	}
	var unowned, live bool
	if err := tx.QueryRow(ctx, `SELECT owner_person_id IS NULL,clock_timestamp()<$2 FROM agent_accounts WHERE id=$1`, v.AccountID, v.ExpiresAt).Scan(&unowned, &live); err != nil {
		return err
	}
	if !live {
		return fail(410, "code_expired", "account code expired or was used")
	}
	if !unowned {
		return fail(409, "conflict", "account is already linked")
	}
	return nil
}
func (m *Module) accountLinkLookup(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	if r.Header.Get("Authorization") != "" {
		WriteError(w, fail(403, "forbidden", "person session required"))
		return
	}
	var in struct {
		Code string `json:"user_code"`
	}
	if err := decode(w, r, &in); err != nil {
		WriteError(w, err)
		return
	}
	if err := m.limit(r.Context(), p.TenantID, "link_lookup", 30); err != nil {
		WriteError(w, err)
		return
	}
	code := strings.NewReplacer(" ", "", "-", "").Replace(in.Code)
	if len(in.Code) > 16 || len(code) != 6 || strings.IndexFunc(code, func(c rune) bool { return c < '0' || c > '9' }) >= 0 {
		WriteError(w, fail(404, "not_found", "account code not found"))
		return
	}
	var out accountLinkReview
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(r.Context(), `SELECT id::text FROM account_person_link_requests WHERE user_code=$1`, code).Scan(&id); err != nil {
			return notFound(err)
		}
		var err error
		out, err = readAccountLink(r.Context(), tx, id, p)
		if err != nil {
			return err
		}
		return pendingAccountLink(r.Context(), tx, out)
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}
func (m *Module) accountLinkApprove(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	if r.Header.Get("Authorization") != "" {
		WriteError(w, fail(403, "forbidden", "person session required"))
		return
	}
	var in struct {
		TenantID string `json:"tenant_id"`
		PersonID string `json:"person_id"`
		Revision *int64 `json:"expected_revision"`
		Digest   string `json:"request_digest"`
	}
	if err := decode(w, r, &in); err != nil {
		WriteError(w, err)
		return
	}
	if in.TenantID != p.TenantID || in.PersonID != p.ID || in.Revision == nil || *in.Revision < 0 || !hashRE.MatchString(in.Digest) {
		WriteError(w, fail(409, "conflict", "review this account in the current session"))
		return
	}
	if err := m.limit(r.Context(), p.TenantID, "link_approve", 60); err != nil {
		WriteError(w, err)
		return
	}
	var out accountLinkReview
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = readAccountLink(r.Context(), tx, r.PathValue("requestId"), p)
		if err != nil {
			return err
		}
		if err = pendingAccountLink(r.Context(), tx, out); err != nil {
			return err
		}
		if out.Revision != *in.Revision || subtle.ConstantTimeCompare([]byte(out.Digest), []byte(in.Digest)) != 1 {
			return fail(409, "conflict", "account details changed; review the code again")
		}
		if _, err = tx.Exec(r.Context(), `UPDATE agent_accounts SET owner_person_id=$2,linked_at=clock_timestamp() WHERE id=$1`, out.AccountID, p.ID); err != nil {
			return err
		}
		consumed, err := tx.Exec(r.Context(), `UPDATE account_person_link_requests SET state='linked',person_id=$2 WHERE id=$1 AND state='pending' AND expires_at>clock_timestamp()`, out.RequestID, p.ID)
		if err != nil {
			return err
		}
		if consumed.RowsAffected() != 1 {
			return fail(410, "code_expired", "account code expired or was used")
		}
		out.State = "linked"
		return m.accountLinkEvent(r.Context(), tx, p, "account.linked", out.AccountID, p.ID, out.Revision)
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}
func (m *Module) accountLinks(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	if r.Header.Get("Authorization") != "" {
		WriteError(w, fail(403, "forbidden", "person session required"))
		return
	}
	out := []accountLinkReview{}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT l.id::text FROM account_person_link_requests l JOIN agent_accounts a ON a.tenant_id=l.tenant_id AND a.id=l.account_id WHERE a.owner_person_id=$1 AND l.state='linked' AND l.person_id=$1 AND l.account_revision=a.link_revision AND a.archived_at IS NULL ORDER BY a.created_at,a.id`, p.ID)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range ids {
			v, err := readAccountLink(r.Context(), tx, id, p)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return nil
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}
func (m *Module) accountUnlink(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	if r.Header.Get("Authorization") != "" {
		WriteError(w, fail(403, "forbidden", "person session required"))
		return
	}
	id := r.PathValue("accountId")
	var in struct {
		Revision *int64 `json:"expected_revision"`
	}
	if err := decode(w, r, &in); err != nil {
		WriteError(w, err)
		return
	}
	if !uuidRE.MatchString(id) || in.Revision == nil || *in.Revision < 0 {
		WriteError(w, fail(400, "invalid_request", "review the linked account first"))
		return
	}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var revision int64
		if err := tx.QueryRow(r.Context(), `SELECT link_revision FROM agent_accounts WHERE id=$1 AND owner_person_id=$2 FOR UPDATE`, id, p.ID).Scan(&revision); err != nil {
			return notFound(err)
		}
		if revision != *in.Revision {
			return fail(409, "conflict", "account ownership changed; refresh first")
		}
		if _, err := tx.Exec(r.Context(), `UPDATE agent_accounts SET owner_person_id=NULL,linked_at=NULL,link_revision=link_revision+1 WHERE id=$1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE account_person_link_requests SET state='revoked',person_id=NULL WHERE account_id=$1 AND state IN ('pending','linked')`, id); err != nil {
			return err
		}
		return m.accountLinkEvent(r.Context(), tx, p, "account.unlinked", id, p.ID, revision+1)
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(204)
}
func (m *Module) accountLinkEvent(ctx context.Context, tx pgx.Tx, p tenant.Principal, event, account, person string, revision int64) error {
	_, err := events.Append(ctx, tx, p, events.Change{Type: event, After: map[string]any{"account_id": account, "person_id": person, "revision": revision}})
	return err
}
