// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Options struct{ Enabled bool }
type Module struct {
	pool    *pgxpool.Pool
	enabled bool
	live    liveRelay
}

// New ships disabled. Enabling supplies identity, participant history and
// authenticated live/final outbox routes. It never wakes or launches an agent.
func New(pool *pgxpool.Pool, options ...Options) *Module {
	m := &Module{pool: pool}
	if len(options) > 0 {
		m.enabled = options[0].Enabled
	}
	return m
}

func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/projects/{projectId}/chat-roles", handle(m, m.createRole))
	mux.HandleFunc("POST /api/projects/{projectId}/chat-threads/resolve", handle(m, m.resolveThread))
	mux.HandleFunc("GET /api/chat-threads/{id}", handle(m, m.getThread))
	mux.HandleFunc("GET /api/chat-threads/{id}/messages", handle(m, m.listMessages))
	mux.HandleFunc("GET /api/chat-threads/{id}/read-marker", handle(m, m.getSeen))
	mux.HandleFunc("PUT /api/chat-threads/{id}/read-marker", handle(m, m.unionSeen))
	mux.HandleFunc("POST /api/chat-threads/{id}/binding", handle(m, m.bindThread))
	mux.HandleFunc("POST /api/chat-deliveries/binding/resolve", handle(m, m.resolveWorker))
	mux.HandleFunc("GET /api/chat-threads/{id}/live", m.streamLive)
	mux.HandleFunc("POST /api/chat-deliveries/live", handle(m, m.publishLive))
	mux.HandleFunc("GET /api/chat-threads/{id}/outbox", handle(m, m.listOutbox))
	mux.HandleFunc("POST /api/chat-threads/{id}/outbox", handle(m, m.sendOutbox))
	mux.HandleFunc("POST /api/chat-deliveries/outbox", handle(m, m.workerOutbox))
	mux.HandleFunc("POST /api/chat-deliveries/final", handle(m, m.finalMessage))
	mux.HandleFunc("POST /api/chat-deliveries/outbox/receipt", handle(m, m.workerReceipt))
}

func unavailable() error { return workorders.Fail(404, "chat binding unavailable") }

func handle[Input any](m *Module, op func(*http.Request, pgx.Tx, tenant.Principal, Input) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !m.enabled {
			httpapi.WriteError(w, 404, "not found")
			return
		}
		p, ok := tenant.PrincipalFrom(r.Context())
		if !ok || !workorders.UUID(p.ID) || !workorders.UUID(p.TenantID) {
			httpapi.WriteError(w, 401, "unauthorized")
			return
		}
		ctx, cancel := context.WithTimeout(tenant.WithPrincipal(r.Context(), p), 3*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		// Both network reading and typed decoding finish before acquiring a
		// database connection. The same budget covers the final transaction.
		var in Input
		if r.Method != http.MethodGet {
			raw, err := readBody(ctx, w, r)
			if err != nil {
				workorders.WriteError(w, err)
				return
			}
			if !utf8.Valid(raw) {
				httpapi.WriteError(w, 400, "invalid UTF-8 request body")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			if err := workorders.Decode(r, &in); err != nil {
				workorders.WriteError(w, err)
				return
			}
		}
		var out any
		err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
			var err error
			out, err = op(r, tx, p, in)
			return err
		})
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, authz.ErrForbidden) {
			err = unavailable()
		}
		if err != nil {
			workorders.WriteError(w, err)
			return
		}
		if committed, ok := out.(interface{ afterCommit(*Module) }); ok {
			committed.afterCommit(m)
		}
		httpapi.WriteJSON(w, 200, out)
	}
}

func readBody(ctx context.Context, w http.ResponseWriter, r *http.Request) ([]byte, error) {
	controller := http.NewResponseController(w)
	if deadline, ok := ctx.Deadline(); ok {
		if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return nil, workorders.Fail(http.StatusRequestTimeout, "request body deadline unavailable")
		}
	}
	limit := int64(8192)
	if strings.HasSuffix(r.URL.Path, "/outbox") || strings.HasSuffix(r.URL.Path, "/final") {
		limit = 512 << 10
	}
	if strings.HasSuffix(r.URL.Path, "/live") {
		limit = 128 << 10
	}
	if r.Method == http.MethodPut {
		limit = 16384
	}
	body := http.MaxBytesReader(w, r.Body, limit)
	type result struct {
		raw []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		raw, err := io.ReadAll(body)
		done <- result{raw, err}
	}()
	select {
	case got := <-done:
		// Clear the connection deadline only after a complete body. An
		// incomplete/timed-out request must not resume keep-alive reads.
		if got.err == nil && ctx.Err() == nil {
			_ = controller.SetReadDeadline(time.Time{})
			return got.raw, nil
		}
		var tooLarge *http.MaxBytesError
		if errors.As(got.err, &tooLarge) {
			return nil, workorders.Fail(http.StatusBadRequest, "request body too large")
		}
		return nil, workorders.Fail(http.StatusRequestTimeout, "request body did not arrive in time")
	case <-ctx.Done():
		// An explicit socket deadline also interrupts a read when the
		// caller cancels before the three-second deadline. Closing releases
		// cancellable bodies used by middleware and isolated transports.
		_ = controller.SetReadDeadline(time.Now())
		_ = body.Close()
		return nil, workorders.Fail(http.StatusRequestTimeout, "request body did not arrive in time")
	}
}

// Chat identity writes do not move the tree. Take only the tenant access
// fence, then role, thread and session rows. Never take a tree lock after it.
func accessFence(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	var id string
	return tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, p.TenantID).Scan(&id)
}
func require(ctx context.Context, tx pgx.Tx, p tenant.Principal, permission, project string) error {
	return authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project})
}
func personID(ctx context.Context, tx pgx.Tx, p tenant.Principal) (string, error) {
	if p.Kind != tenant.Person {
		return "", unavailable()
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals WHERE id=$1 AND kind='person' AND status='active'`, p.ID).Scan(&id)
	return id, err
}

var slotPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*$`)

func (m *Module) createRole(r *http.Request, tx pgx.Tx, p tenant.Principal, in RoleCreate) (any, error) {
	if len(in.SlotKey) < 1 || len(in.SlotKey) > 128 || !slotPattern.MatchString(in.SlotKey) ||
		!(in.Kind == "lead" && in.SlotKey == "lead" || in.Kind == "worker" && in.SlotKey != "lead" && in.SlotKey != "worker") {
		return nil, workorders.Fail(400, "invalid role kind or assignment slot")
	}
	project := r.PathValue("projectId")
	if !workorders.UUID(project) {
		return nil, unavailable()
	}
	if err := accessFence(r.Context(), tx, p); err != nil {
		return nil, err
	}
	person, err := personID(r.Context(), tx, p)
	if err != nil {
		return nil, err
	}
	if err = require(r.Context(), tx, p, "chat.bind", project); err != nil {
		return nil, err
	}
	var live bool
	if err = tx.QueryRow(r.Context(), `SELECT n.deleted_at IS NULL AND k.slug='project' FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1`, project).Scan(&live); err != nil {
		return nil, err
	}
	if !live {
		return nil, unavailable()
	}
	var out Role
	out.Contract = "chat-v1"
	err = tx.QueryRow(r.Context(), `INSERT INTO chat_roles(tenant_id,owner_person_id,project_id,kind,slot_key) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT (tenant_id,owner_person_id,project_id,slot_key) DO UPDATE SET slot_key=chat_roles.slot_key
 RETURNING id::text,project_id::text,kind,slot_key,conversation_scope`, p.TenantID, person, project, in.Kind, in.SlotKey).Scan(&out.ID, &out.ProjectID, &out.Kind, &out.SlotKey, &out.ConversationScope)
	if err != nil {
		return nil, err
	}
	if out.Kind != in.Kind {
		return nil, workorders.Fail(409, "role slot conflict")
	}
	return out, nil
}

func (m *Module) resolveThread(r *http.Request, tx pgx.Tx, p tenant.Principal, in ThreadResolve) (any, error) {
	project := r.PathValue("projectId")
	if !workorders.UUID(project) || !workorders.UUID(in.RoleID) {
		return nil, unavailable()
	}
	if err := accessFence(r.Context(), tx, p); err != nil {
		return nil, err
	}
	person, err := personID(r.Context(), tx, p)
	if err != nil {
		return nil, err
	}
	if err = require(r.Context(), tx, p, "chat.read", project); err != nil {
		return nil, err
	}
	var role string
	if err = tx.QueryRow(r.Context(), `SELECT id::text FROM chat_roles WHERE id=$1 AND project_id=$2 AND owner_person_id=$3 FOR NO KEY UPDATE`, in.RoleID, project, person).Scan(&role); err != nil {
		return nil, err
	}
	var id string
	err = tx.QueryRow(r.Context(), `INSERT INTO chat_threads(tenant_id,role_id,person_id,project_id) VALUES($1,$2,$3,$4)
 ON CONFLICT (tenant_id,person_id,role_id) DO UPDATE SET person_id=chat_threads.person_id RETURNING id::text`, p.TenantID, role, person, project).Scan(&id)
	if err != nil {
		return nil, err
	}
	return loadThread(r.Context(), tx, id)
}

func loadThread(ctx context.Context, tx pgx.Tx, id string) (Thread, error) {
	out := Thread{Contract: "chat-v1", Role: Role{Contract: "chat-v1"}, Readiness: Readiness{State: "offline", Capabilities: []string{}, Reason: "no_binding"}}
	var revision, epoch int64
	err := tx.QueryRow(ctx, `SELECT t.id::text,t.revision,r.id::text,r.project_id::text,r.kind,r.slot_key,r.conversation_scope,r.binding_epoch FROM chat_threads t JOIN chat_roles r ON r.tenant_id=t.tenant_id AND r.id=t.role_id WHERE t.id=$1 AND t.archived_at IS NULL`, id).Scan(&out.ID, &revision, &out.Role.ID, &out.Role.ProjectID, &out.Role.Kind, &out.Role.SlotKey, &out.Role.ConversationScope, &epoch)
	if err != nil {
		return out, err
	}
	out.Revision = strconv.FormatInt(revision, 10)
	out.BindingEpoch = strconv.FormatInt(epoch, 10)
	var live bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat_session_bindings b JOIN harness_sessions s ON s.tenant_id=b.tenant_id AND s.id=b.session_id
 WHERE b.role_id=$1 AND b.valid_to IS NULL AND s.stopped_at IS NULL AND s.archived_at IS NULL AND s.phase NOT IN ('stopping','stopped')
 AND s.management='unmanaged' AND s.owner_principal_id=b.owner_person_id AND s.agent_principal_id=b.agent_principal_id
 AND coalesce(s.heartbeat_at,s.created_at)>clock_timestamp()-interval '2 minutes')`, out.Role.ID).Scan(&live)
	if live {
		out.Readiness.State = "unavailable"
		out.Readiness.Reason = "receiver_unqualified"
	} else if epoch > 0 {
		out.Readiness.Reason = "session_offline"
	}
	return out, err
}

func (m *Module) getThread(r *http.Request, tx pgx.Tx, p tenant.Principal, _ struct{}) (any, error) {
	id := r.PathValue("id")
	if !workorders.UUID(id) {
		return nil, unavailable()
	}
	if err := accessFence(r.Context(), tx, p); err != nil {
		return nil, err
	}
	person, err := personID(r.Context(), tx, p)
	if err != nil {
		return nil, err
	}
	var project string
	if err = tx.QueryRow(r.Context(), `SELECT project_id::text FROM chat_threads WHERE id=$1 AND person_id=$2 AND archived_at IS NULL`, id, person).Scan(&project); err != nil {
		return nil, err
	}
	if err = require(r.Context(), tx, p, "chat.read", project); err != nil {
		return nil, err
	}
	return loadThread(r.Context(), tx, id)
}

func position(value string) (int64, error) {
	if len(value) == 0 || len(value) > 19 || len(value) > 1 && value[0] == '0' {
		return 0, workorders.Fail(400, "invalid binding epoch")
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, workorders.Fail(400, "invalid binding epoch")
		}
	}
	v, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, workorders.Fail(400, "invalid binding epoch")
	}
	return v, nil
}

func (m *Module) bindThread(r *http.Request, tx pgx.Tx, p tenant.Principal, in BindingWrite) (any, error) {
	expected, err := position(in.ExpectedEpoch)
	if err != nil {
		return nil, err
	}
	id := r.PathValue("id")
	if !workorders.UUID(id) || !workorders.UUID(in.SessionID) {
		return nil, unavailable()
	}
	if err = accessFence(r.Context(), tx, p); err != nil {
		return nil, err
	}
	person, err := personID(r.Context(), tx, p)
	if err != nil {
		return nil, err
	}
	var role, project, kind string
	var epoch int64
	if err = tx.QueryRow(r.Context(), `SELECT role_id::text,project_id::text FROM chat_threads WHERE id=$1 AND person_id=$2 AND archived_at IS NULL`, id, person).Scan(&role, &project); err != nil {
		return nil, err
	}
	if err = require(r.Context(), tx, p, "chat.bind", project); err != nil {
		return nil, err
	}
	if err = tx.QueryRow(r.Context(), `SELECT binding_epoch,kind FROM chat_roles WHERE id=$1 AND owner_person_id=$2 FOR NO KEY UPDATE`, role, person).Scan(&epoch, &kind); err != nil {
		return nil, err
	}
	if epoch != expected || epoch == int64(^uint64(0)>>1) {
		return nil, workorders.Fail(409, "binding epoch conflict")
	}
	if _, err = tx.Exec(r.Context(), `SELECT 1 FROM chat_threads WHERE id=$1 FOR NO KEY UPDATE`, id); err != nil {
		return nil, err
	}
	s, err := harness.ExternalRegistrationTx(r.Context(), tx, in.SessionID, true)
	if err != nil {
		return nil, err
	}
	if s.OwnerPersonID != person || s.ProjectID != project || kind == "lead" && s.Role != "coordinator" || kind == "worker" && s.Role != "worker" {
		return nil, unavailable()
	}
	// The person can see only their own binding history; session ownership
	// prevents another person's hidden bindings from being reused.
	var incompatible bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM chat_session_bindings WHERE session_id=$1 AND role_id<>$2)`, s.ID, role).Scan(&incompatible); err != nil {
		return nil, err
	}
	if incompatible {
		return nil, workorders.Fail(409, "session belongs to another chat role")
	}
	if err = claimNativeContext(r.Context(), tx, s, role); err != nil {
		return nil, err
	}
	var current string
	err = tx.QueryRow(r.Context(), `SELECT session_id::text FROM chat_session_bindings WHERE role_id=$1 AND valid_to IS NULL`, role).Scan(&current)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if current == s.ID {
		return loadThread(r.Context(), tx, id)
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO chat_session_contexts(tenant_id,session_id,role_id,owner_person_id,project_id,harness,ref_digest,vendor_ref_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (tenant_id,session_id) DO NOTHING`, p.TenantID, s.ID, role, person, project, s.Harness, s.RefDigest, s.VendorRefDigest); err != nil {
		return nil, err
	}
	var sameContext bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM chat_session_contexts WHERE session_id=$1 AND role_id=$2 AND owner_person_id=$3)`, s.ID, role, person).Scan(&sameContext); err != nil {
		return nil, err
	}
	if !sameContext {
		return nil, unavailable()
	}
	if _, err = tx.Exec(r.Context(), `UPDATE chat_session_bindings SET valid_to=clock_timestamp() WHERE role_id=$1 AND valid_to IS NULL`, role); err != nil {
		return nil, err
	}
	reason := "selected"
	if epoch > 0 {
		reason = "handover"
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO chat_session_bindings(tenant_id,role_id,project_id,owner_person_id,session_id,agent_principal_id,binding_epoch,reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.TenantID, role, project, person, s.ID, s.AgentPrincipalID, epoch+1, reason); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(r.Context(), `UPDATE chat_roles SET binding_epoch=$2 WHERE id=$1`, role, epoch+1); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(r.Context(), `UPDATE chat_threads SET revision=revision+1 WHERE id=$1`, id); err != nil {
		return nil, err
	}
	return loadThread(r.Context(), tx, id)
}

func (m *Module) resolveWorker(r *http.Request, tx pgx.Tx, p tenant.Principal, in WorkerBindingRequest) (any, error) {
	if err := accessFence(r.Context(), tx, p); err != nil {
		return nil, err
	}
	if _, err := AuthorizeWorkerTx(r.Context(), tx, r, p, in); err != nil {
		return nil, err
	}
	return loadThread(r.Context(), tx, in.ConversationID)
}

// AuthorizeWorkerTx is the common boundary for subsequent history/claim
// packages. The caller must first hold the tenant access fence. All context
// derives from verified server rows, never headers naming a role/person.
func AuthorizeWorkerTx(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal, in WorkerBindingRequest) (harness.ExternalRegistration, error) {
	if !workorders.UUID(in.ConversationID) || !workorders.UUID(in.SessionID) {
		return harness.ExternalRegistration{}, unavailable()
	}
	epoch, err := position(in.BindingEpoch)
	if err != nil {
		return harness.ExternalRegistration{}, err
	}
	s, err := harness.VerifyExternalLeaseTx(ctx, tx, r, p, in.SessionID, false)
	if err != nil {
		return s, err
	}
	project := s.ProjectID
	if err = require(ctx, tx, p, "chat.receive", s.ProjectID); err != nil {
		return s, err
	}
	if err = workerKeyTx(ctx, tx, r, p); err != nil {
		return s, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('aeon.chat_session_id',$1,true)`, s.ID); err != nil {
		return s, err
	}
	var role string
	if err = tx.QueryRow(ctx, `SELECT role_id::text FROM chat_session_bindings WHERE session_id=$1 AND agent_principal_id=$2 AND owner_person_id=$3 AND binding_epoch=$4 AND valid_to IS NULL`, s.ID, p.ID, s.OwnerPersonID, epoch).Scan(&role); err != nil {
		return s, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('aeon.chat_role_id',$1,true)`, role); err != nil {
		return s, err
	}
	var owner, kind string
	var liveEpoch int64
	if err = tx.QueryRow(ctx, `SELECT owner_person_id::text,kind,binding_epoch FROM chat_roles WHERE id=$1 AND project_id=$2 FOR NO KEY UPDATE`, role, s.ProjectID).Scan(&owner, &kind, &liveEpoch); err != nil {
		return s, err
	}
	if owner != s.OwnerPersonID || liveEpoch != epoch || kind == "lead" && s.Role != "coordinator" || kind == "worker" && s.Role != "worker" {
		return s, unavailable()
	}
	var id string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM chat_threads WHERE id=$1 AND role_id=$2 AND person_id=$3 AND archived_at IS NULL FOR NO KEY UPDATE`, in.ConversationID, role, owner).Scan(&id); err != nil {
		return s, err
	}
	// Recheck the exact lease and liveness after acquiring the ordered locks.
	s, err = harness.VerifyExternalLeaseTx(ctx, tx, r, p, in.SessionID, true)
	if err != nil {
		return s, err
	}
	if s.OwnerPersonID != owner || s.ProjectID != project || kind == "lead" && s.Role != "coordinator" || kind == "worker" && s.Role != "worker" {
		return s, unavailable()
	}
	if err = require(ctx, tx, p, "chat.receive", s.ProjectID); err != nil {
		return s, err
	}
	if err = workerKeyTx(ctx, tx, r, p); err != nil {
		return s, err
	}
	if err = verifyNativeContext(ctx, tx, s, role); err != nil {
		return s, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('aeon.chat_conversation_id',$1,true)`, id); err != nil {
		return s, err
	}
	return s, nil
}

// The tenant access fence serializes context claims. Role/thread and the
// registration are already locked; aliases use one sorted digest namespace.
// No event counter or network work precedes these final record writes.
func nativeReferences(s harness.ExternalRegistration) [][]byte {
	refs := [][]byte{s.RefDigest}
	if len(s.VendorRefDigest) != 0 && !bytes.Equal(s.RefDigest, s.VendorRefDigest) {
		refs = append(refs, s.VendorRefDigest)
	}
	slices.SortFunc(refs, bytes.Compare)
	return refs
}

func claimNativeContext(ctx context.Context, tx pgx.Tx, s harness.ExternalRegistration, role string) error {
	if err := harness.StoreNativeContextTx(ctx, tx, s.Harness, s.RefDigest, s.VendorRefDigest, &s.OwnerPersonID, s.ProjectID, &role); err != nil {
		var denied *workorders.Error
		if errors.As(err, &denied) && denied.Status == 409 {
			return unavailable()
		}
		return err
	}
	return verifyNativeContext(ctx, tx, s, role)
}

func verifyNativeContext(ctx context.Context, tx pgx.Tx, s harness.ExternalRegistration, role string) error {
	for _, ref := range nativeReferences(s) {
		var same bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat_native_contexts WHERE harness=$1 AND ref_digest=$2
 AND role_id=$3 AND owner_person_id=$4 AND project_id=$5)`, s.Harness, ref, role, s.OwnerPersonID, s.ProjectID).Scan(&same); err != nil {
			return err
		}
		if !same {
			return unavailable()
		}
	}
	return nil
}

func workerKeyTx(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal) error {
	return workerKeyPermissionTx(ctx, tx, r, p, "chat.receive")
}

func workerKeyPermissionTx(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal, permission string) error {
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return unavailable()
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(token), "aeon_")
	if !ok {
		return unavailable()
	}
	prefix, secret, ok := strings.Cut(rest, "_")
	if !ok || len(token) > 8192 {
		return unavailable()
	}
	sum := sha256.Sum256([]byte(secret))
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_keys WHERE principal_id=$1 AND prefix=$2 AND hash=$3 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp()) AND ((coalesce(full_access,false) AND $4::boolean) OR $5=ANY(scopes)))`, p.ID, prefix, hex.EncodeToString(sum[:]), authz.KeyGrantable(permission, false), permission).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return unavailable()
	}
	return nil
}
