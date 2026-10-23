// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Peer is an owned live generation, never a principal-wide transport target.
type Peer struct {
	SessionID   string `json:"session_id"`
	PrincipalID string `json:"principal_id"`
	Name        string `json:"name"`
	ProjectID   string `json:"project_id"`
	Project     string `json:"project"`
	Harness     string `json:"harness"`
	State       string `json:"state"`
	Address     string `json:"address"`
}

type PeerPage struct {
	Items     []Peer  `json:"items"`
	NextAfter *string `json:"next_after"`
}

type peerFilter struct {
	name, session, project, after string
	limit                         int
}

// A key's creator establishes ownership, as at session registration. An
// explicit acting-person ceiling must agree; it cannot bridge trust contexts.
func peerOwner(ctx context.Context, tx pgx.Tx, p tenant.Principal) (string, error) {
	id := p.ID
	if p.Kind == tenant.Agent {
		id = p.KeyCreatorID
	}
	canonical := func(id string) (string, error) {
		if _, ok := parseUUID(id); !ok {
			return "", errForbidden
		}
		var owner string
		err := tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals
 WHERE id=$1::uuid AND kind='person' AND status='active'
 AND EXISTS(SELECT 1 FROM principals target WHERE target.tenant_id=principals.tenant_id AND target.id=coalesce(principals.linked_to,principals.id) AND target.kind='person' AND target.status='active')`, id).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errForbidden
		}
		return owner, err
	}
	owner, err := canonical(id)
	if err != nil {
		return "", err
	}
	if p.Kind == tenant.Agent && p.KeyPersonID != "" {
		person, err := canonical(p.KeyPersonID)
		if err != nil {
			return "", err
		}
		if person != owner {
			return "", errForbidden
		}
	}
	return owner, nil
}

const peerLive = `s.stopped_at IS NULL AND s.archived_at IS NULL
 AND s.phase IN ('starting','working','yielded','stopping')
 AND s.heartbeat_at>now()-interval '2 minutes'
 AND NOT EXISTS(SELECT 1 FROM harness_attach_requests attached WHERE attached.tenant_id=s.tenant_id AND attached.session_id=s.id AND attached.state='active' AND attached.lease_until>now())`

func (m *messaging) peers(ctx context.Context, p tenant.Principal, filter peerFilter) (PeerPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out := PeerPage{Items: []Peer{}}
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.base.pool, p.TenantID, func(tx pgx.Tx) error {
		owner, err := peerOwner(ctx, tx, p)
		if err != nil {
			return err
		}
		if authz.RequireTx(ctx, tx, p, "harness.read", authz.Scope{AnyProject: true}) != nil {
			return errForbidden
		}
		allowed, err := authz.ProjectsTx(ctx, tx, p)
		if err != nil {
			return err
		}
		projects := []string{}
		if !allowed("harness.read", "") {
			projects, err = authz.GrantedProjectIDsTx(ctx, tx, p, "harness.read")
			if err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT s.id::text,s.agent_principal_id::text,
 coalesce(nullif(btrim(s.display_label),''),a.name),s.project_id::text,coalesce(nullif(n.fields->>'project_key',''),n.key),s.harness,s.phase||'/'||s.activity
 FROM harness_sessions s
 JOIN principals a ON a.tenant_id=s.tenant_id AND a.id=s.agent_principal_id AND a.kind='agent' AND a.status='active'
 JOIN principals owner ON owner.tenant_id=s.tenant_id AND owner.id=s.owner_principal_id AND owner.kind='person' AND owner.status='active'
 JOIN nodes n ON n.tenant_id=s.tenant_id AND n.id=s.project_id AND n.deleted_at IS NULL
 WHERE coalesce(owner.linked_to,owner.id)=$1::uuid AND `+peerLive+`
 AND ($2::text='' OR coalesce(nullif(btrim(s.display_label),''),a.name)=$2)
 AND ($3::uuid IS NULL OR s.id=$3) AND ($4::uuid IS NULL OR s.project_id=$4)
 AND ($5::uuid IS NULL OR s.id>$5) AND ($6::boolean OR s.project_id=ANY($7::uuid[]))
 ORDER BY s.id LIMIT $8`, owner, filter.name, peerUUID(filter.session), peerUUID(filter.project), peerUUID(filter.after), allowed("harness.read", ""), projects, filter.limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Peer
			if err := rows.Scan(&item.SessionID, &item.PrincipalID, &item.Name, &item.ProjectID, &item.Project, &item.Harness, &item.State); err != nil {
				return err
			}
			item.Address = "session:" + item.SessionID
			out.Items = append(out.Items, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out.Items) > filter.limit {
			out.Items = out.Items[:filter.limit]
			cursor := out.Items[len(out.Items)-1].SessionID
			out.NextAfter = &cursor
		}
		return nil
	})
	return out, err
}

func peerUUID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func (m *messaging) listPeers(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	filter := peerFilter{name: q.Get("name"), session: q.Get("session"), project: q.Get("project"), after: q.Get("after"), limit: 100}
	if q.Has("name") && (!utf8.ValidString(filter.name) || strings.TrimSpace(filter.name) == "" || len(filter.name) > 640 || utf8.RuneCountInString(filter.name) > 160 || strings.ContainsAny(filter.name, "\x00\r\n")) {
		messagingFailure(w, badRequest("invalid peer name"))
		return
	}
	for _, field := range []string{"session", "project", "after"} {
		if q.Has(field) {
			if _, valid := parseUUID(q.Get(field)); !valid {
				messagingFailure(w, badRequest("invalid peer filter"))
				return
			}
		}
	}
	if q.Has("limit") {
		var err error
		filter.limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || filter.limit < 1 || filter.limit > 100 {
			messagingFailure(w, badRequest("invalid peer limit"))
			return
		}
	}
	page, err := m.peers(r.Context(), p, filter)
	if err != nil {
		messagingFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (m *messaging) sendPeerMessage(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, valid := parseUUID(r.PathValue("sessionId"))
	if !valid {
		messagingFailure(w, errNotFound)
		return
	}
	// Resolve only on the authenticated instance. commitMessage rechecks the
	// exact recipient, project and owner under its final mutation fence.
	var body struct {
		Body            string  `json:"body"`
		Key             string  `json:"idempotency_key"`
		SenderSessionID *string `json:"sender_session_id"`
		ReplyTo         *string `json:"reply_to"`
		ThreadID        string  `json:"thread_id"`
		ExpectsReply    bool    `json:"expects_reply"`
		ActionRequest   bool    `json:"is_action_request"`
		Level           string  `json:"delivery_level"`
	}
	if !decodeJSON(w, r, 512<<10, &body) {
		return
	}
	if p.Kind == tenant.Agent && body.SenderSessionID == nil {
		messagingFailure(w, badRequest("peer messages require your exact sender_session_id"))
		return
	}
	page, err := m.peers(r.Context(), p, peerFilter{session: id, limit: 1})
	if err != nil {
		messagingFailure(w, err)
		return
	}
	if len(page.Items) != 1 {
		messagingFailure(w, errNotFound)
		return
	}
	peer := page.Items[0]
	in := compatSend{peer: true, To: peer.PrincipalID, RecipientSessionID: &id, SenderSessionID: body.SenderSessionID, Body: body.Body, Key: body.Key, ReplyTo: body.ReplyTo, ThreadID: body.ThreadID, ExpectsReply: body.ExpectsReply, ActionRequest: body.ActionRequest, Level: body.Level}
	if err := validateCompatSend(&in); err != nil {
		messagingFailure(w, err)
		return
	}
	out, err := m.commitMessage(r.Context(), p, peer.ProjectID, in)
	if err != nil {
		messagingFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// peerSession runs only under attachedmsg.Lock, before message/event locks.
// FOR SHARE keeps stop, archive, owner and project changes behind this write.
func peerSession(ctx context.Context, tx pgx.Tx, id *string, principal, owner string) (Peer, error) {
	var out Peer
	if id == nil {
		return out, errNotFound
	}
	err := tx.QueryRow(ctx, `SELECT s.id::text,s.agent_principal_id::text,s.project_id::text,coalesce(nullif(n.fields->>'project_key',''),n.key)
 FROM harness_sessions s
 JOIN principals a ON a.tenant_id=s.tenant_id AND a.id=s.agent_principal_id AND a.kind='agent' AND a.status='active'
 JOIN principals owner ON owner.tenant_id=s.tenant_id AND owner.id=s.owner_principal_id AND owner.kind='person' AND owner.status='active'
 JOIN nodes n ON n.tenant_id=s.tenant_id AND n.id=s.project_id AND n.deleted_at IS NULL
 WHERE s.id=$1::uuid AND s.agent_principal_id=$2::uuid AND coalesce(owner.linked_to,owner.id)=$3::uuid AND `+peerLive+` FOR SHARE OF s`, *id, principal, owner).Scan(&out.SessionID, &out.PrincipalID, &out.ProjectID, &out.Project)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, errNotFound
	}
	return out, err
}
