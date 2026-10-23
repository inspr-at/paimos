// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

// HostedAssistantAdapter supplies only the vendor's wire format. Encryption,
// public HTTPS enforcement, signatures, durable claims, retries and receipts
// belong to the inbox. Implementing another vendor grants no new authority.
type HostedAssistantAdapter interface {
	Vendor() string
	TargetAdapter() string
	DefaultDeliveryMode() string
	Encode(AssistantDelivery) ([]byte, error)
}

// AssistantDelivery contains untrusted message data, never endpoint credentials.
type AssistantDelivery struct {
	Message        CompatMessage
	DeliveryID     string
	Project        string
	Cursor         int64
	FallbackReason string
}

type grokAssistantAdapter struct{}

func (grokAssistantAdapter) Vendor() string              { return "grok" }
func (grokAssistantAdapter) TargetAdapter() string       { return "grok_bot_routine" }
func (grokAssistantAdapter) DefaultDeliveryMode() string { return "pull" }
func (grokAssistantAdapter) Encode(in AssistantDelivery) ([]byte, error) {
	return json.Marshal(routineWake{Event: "agent_message.available", Version: 1, Instance: "aeon", DeliveryID: in.DeliveryID,
		Project: in.Project, MessageID: in.Message.ID, Cursor: in.Cursor, To: in.Message.To,
		RequestedLevel: in.Message.Level, EffectiveLevel: "simple", FallbackReason: in.FallbackReason,
		Content: routineFrame(in.Message, in.Project)})
}

type assistantAdapterRegistry struct {
	byVendor map[string]HostedAssistantAdapter
	byTarget map[string]HostedAssistantAdapter
	targets  []string
}

func newAssistantAdapterRegistry() assistantAdapterRegistry {
	// OpenAI/Meta deliberately have no implementation or registration yet.
	return registerAssistantAdapters(grokAssistantAdapter{})
}

func registerAssistantAdapters(adapters ...HostedAssistantAdapter) assistantAdapterRegistry {
	r := assistantAdapterRegistry{byVendor: map[string]HostedAssistantAdapter{}, byTarget: map[string]HostedAssistantAdapter{}}
	for _, adapter := range adapters {
		if adapter == nil || adapter.Vendor() == "" || adapter.TargetAdapter() == "" || (adapter.DefaultDeliveryMode() != "push" && adapter.DefaultDeliveryMode() != "pull") || r.byVendor[adapter.Vendor()] != nil || r.byTarget[adapter.TargetAdapter()] != nil {
			panic("invalid hosted assistant adapter registration")
		}
		r.byVendor[adapter.Vendor()], r.byTarget[adapter.TargetAdapter()] = adapter, adapter
		r.targets = append(r.targets, adapter.TargetAdapter())
	}
	return r
}

// AssistantAdapterConfig is deliberately redacted, including its health errors.
// Its ID is the versioned inbox target ID, so queued deliveries stay bound to
// the configuration originally selected by the sender.
type AssistantAdapterConfig struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"project_id"`
	PrincipalID     string     `json:"principal_id"`
	Address         string     `json:"address"`
	Vendor          string     `json:"vendor"`
	Version         int        `json:"version"`
	Enabled         bool       `json:"enabled"`
	CreatedAt       time.Time  `json:"created_at"`
	LastSuccessAt   *time.Time `json:"last_success_at"`
	LastFailureAt   *time.Time `json:"last_failure_at"`
	LastFailureCode *string    `json:"last_failure_code"`
	DeliveryMode    string     `json:"delivery_mode"`
	Implicit        bool       `json:"implicit"`
	LastPullAt      *time.Time `json:"last_pull_at"`
	OldestUnreadAt  *time.Time `json:"oldest_unread_at"`
	OldestUnreadAge *int64     `json:"oldest_unread_age_seconds"`
}

const assistantAdapterCols = `t.id::text,t.project_id::text,t.principal_id::text,t.address,t.vendor,t.version,t.enabled,t.created_at,t.last_success_at,t.last_failure_at,t.last_failure_code,t.delivery_mode,t.implicit,
 CASE WHEN t.delivery_mode='pull' THEN p.assistant_last_pull_at END,u.created_at`

// Existing messaging-only assistants have no project target. Project managers
// can inspect their own assistants without registering them or changing their
// principal/key lifecycle. The project remains the permission/view context.
const assistantAdapterJoin = ` FROM (
 SELECT t.id,t.tenant_id,t.project_id,t.principal_id,t.address,a.vendor,t.version,t.enabled,t.created_at,
 CASE WHEN t.target_kind<>'pull' AND NOT coalesce(p.messaging_only,false) THEN a.last_success_at END AS last_success_at,
 CASE WHEN t.target_kind<>'pull' AND NOT coalesce(p.messaging_only,false) THEN a.last_failure_at END AS last_failure_at,
 CASE WHEN t.target_kind<>'pull' AND NOT coalesce(p.messaging_only,false) THEN a.last_failure_code END AS last_failure_code,
 CASE WHEN t.target_kind='pull' OR p.messaging_only IS TRUE THEN 'pull' ELSE 'push' END AS delivery_mode,false AS implicit
 FROM assistant_adapters a JOIN inbox_message_targets t ON t.tenant_id=a.tenant_id AND t.id=a.target_id
 JOIN principals p ON p.tenant_id=t.tenant_id AND p.id=t.principal_id WHERE t.project_id=$1::uuid
 UNION ALL
 SELECT p.id,p.tenant_id,$1::uuid,p.id,p.id::text,'generic',1,p.status='active' AND p.linked_to IS NULL,p.created_at,
 NULL::timestamptz,NULL::timestamptz,NULL::text,'pull',true
 FROM principals p WHERE p.kind='agent' AND p.messaging_only IS TRUE AND p.messaging_owner_id=$2::uuid
 ) t JOIN principals p ON p.tenant_id=t.tenant_id AND p.id=t.principal_id
 LEFT JOIN LATERAL (SELECT m.created_at FROM inbox_messages m
 WHERE t.delivery_mode='pull' AND m.tenant_id=t.tenant_id AND m.recipient_principal_id=t.principal_id
 AND m.content_mode='durable' AND m.chat_thread_id IS NULL AND m.recipient_session_id IS NULL AND m.acked_at IS NULL
 AND (m.expires_at IS NULL OR m.expires_at>clock_timestamp()) ORDER BY m.created_at LIMIT 1) u ON true `

func scanAssistantAdapter(row pgx.Row, now time.Time) (AssistantAdapterConfig, error) {
	var v AssistantAdapterConfig
	err := row.Scan(&v.ID, &v.ProjectID, &v.PrincipalID, &v.Address, &v.Vendor, &v.Version, &v.Enabled, &v.CreatedAt, &v.LastSuccessAt, &v.LastFailureAt, &v.LastFailureCode, &v.DeliveryMode, &v.Implicit, &v.LastPullAt, &v.OldestUnreadAt)
	if v.OldestUnreadAt != nil {
		age := max(int64(0), int64(now.Sub(*v.OldestUnreadAt)/time.Second))
		v.OldestUnreadAge = &age
	}
	return v, err
}

func (m *messaging) adapterPrincipal(w http.ResponseWriter, r *http.Request, personOnly bool) (tenant.Principal, string, bool) {
	p, project, ok := m.messagingPrincipal(w, r, true)
	if ok && personOnly && (p.Kind != tenant.Person || p.KeyID != "" || r.Header.Get("Authorization") != "") {
		messagingFailure(w, errForbidden)
		ok = false
	}
	return p, project, ok
}

func requireAdapterTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, write bool) error {
	if write {
		if p.Kind != tenant.Person || p.KeyID != "" {
			return errForbidden
		}
		if err := authz.LockProjectWrite(ctx, tx, p.TenantID); err != nil {
			return err
		}
	}
	if authz.RequireTx(ctx, tx, p, "inbox.manage", authz.Scope{ProjectID: project}) != nil {
		return errForbidden
	}
	return messagingProject(ctx, tx, project)
}

func (m *messaging) createAssistantAdapter(w http.ResponseWriter, r *http.Request) {
	p, project, ok := m.adapterPrincipal(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Vendor      string `json:"vendor"`
		Mode        string `json:"delivery_mode"`
		PrincipalID string `json:"principal_id"`
		Address     string `json:"address"`
		Endpoint    string `json:"endpoint"`
		Secret      string `json:"webhook_secret"`
	}
	if !decodeJSON(w, r, 8<<10, &in) {
		return
	}
	adapter := m.adapters.byVendor[in.Vendor]
	principalID, valid := parseUUID(in.PrincipalID)
	if adapter == nil || !valid || strings.HasPrefix(in.Secret, "aeon_") {
		messagingFailure(w, badRequest("invalid vendor or agent principal"))
		return
	}
	if in.Mode == "" {
		in.Mode = adapter.DefaultDeliveryMode()
		// Keep legacy endpoint clients on their already configured push mode.
		if in.Endpoint != "" || in.Secret != "" {
			in.Mode = "push"
		}
	}
	if in.Mode != "push" && in.Mode != "pull" || in.Mode == "pull" && (in.Endpoint != "" || in.Secret != "") {
		messagingFailure(w, badRequest("invalid delivery mode or pull endpoint"))
		return
	}
	kind := "https_webhook"
	if in.Mode == "pull" {
		kind = "pull"
	}
	target := targetInput{boundPrincipalID: principalID, PrincipalID: principalID, Address: in.Address, Adapter: adapter.TargetAdapter(), Kind: kind, Ref: in.Endpoint, Secret: in.Secret, Role: "primary", MaximumLevel: "simple"}
	if err := validateCompatTarget(r.Context(), &target); err != nil {
		messagingFailure(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var out AssistantAdapterConfig
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.base.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := requireAdapterTx(ctx, tx, p, project, true); err != nil {
			return err
		}
		var active, messagingOnly bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1::uuid AND kind='agent' AND status='active' AND linked_to IS NULL),EXISTS(SELECT 1 FROM principals WHERE id=$1::uuid AND messaging_only IS TRUE)`, principalID).Scan(&active, &messagingOnly); err != nil {
			return err
		}
		if !active {
			return errNotFound
		}
		if messagingOnly && in.Mode != "pull" {
			return badRequest("messaging-only assistants use pull delivery")
		}
		t, err := m.storeTargetTx(ctx, tx, p, project, target)
		if err != nil {
			return err
		}
		now, err := m.databaseNow(ctx, tx)
		if err != nil {
			return err
		}
		out, err = scanAssistantAdapter(tx.QueryRow(ctx, `SELECT `+assistantAdapterCols+assistantAdapterJoin+` WHERE t.id=$3::uuid`, project, p.ID, t.ID), now)
		return err
	})
	if err != nil {
		messagingFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (m *messaging) listAssistantAdapters(w http.ResponseWriter, r *http.Request) {
	p, project, ok := m.adapterPrincipal(w, r, false)
	if !ok {
		return
	}
	limit, after := 50, r.URL.Query().Get("after")
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			messagingFailure(w, badRequest("invalid limit"))
			return
		}
		limit = value
	}
	if after != "" {
		if _, valid := parseUUID(after); !valid {
			messagingFailure(w, badRequest("invalid cursor"))
			return
		}
	}
	out := struct {
		Items     []AssistantAdapterConfig `json:"items"`
		NextAfter *string                  `json:"next_after"`
	}{Items: []AssistantAdapterConfig{}}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.base.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := requireAdapterTx(ctx, tx, p, project, false); err != nil {
			return err
		}
		now, err := m.databaseNow(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+assistantAdapterCols+assistantAdapterJoin+` WHERE (NULLIF($3,'')::uuid IS NULL OR t.id>NULLIF($3,'')::uuid) ORDER BY t.id LIMIT $4`, project, p.ID, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanAssistantAdapter(rows, now)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		messagingFailure(w, err)
		return
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.NextAfter = &out.Items[limit-1].ID
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *messaging) getAssistantAdapter(w http.ResponseWriter, r *http.Request) {
	p, project, ok := m.adapterPrincipal(w, r, false)
	if !ok {
		return
	}
	id, valid := parseUUID(r.PathValue("adapterId"))
	if !valid {
		messagingFailure(w, errNotFound)
		return
	}
	out, err := m.readAssistantAdapter(r.Context(), p, project, id, false)
	if err != nil {
		messagingFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *messaging) readAssistantAdapter(ctx context.Context, p tenant.Principal, project, id string, test bool) (AssistantAdapterConfig, error) {
	if test && (p.Kind != tenant.Person || p.KeyID != "") {
		return AssistantAdapterConfig{}, errForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out AssistantAdapterConfig
	var input *targetInput
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.base.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := requireAdapterTx(ctx, tx, p, project, false); err != nil {
			return err
		}
		now, err := m.databaseNow(ctx, tx)
		if err != nil {
			return err
		}
		out, err = scanAssistantAdapter(tx.QueryRow(ctx, `SELECT `+assistantAdapterCols+assistantAdapterJoin+` WHERE t.id=$3::uuid`, project, p.ID, id), now)
		if err == nil && test && !out.Enabled {
			return errConflict
		}
		if err == nil && test && !out.Implicit {
			var sealed []byte
			var adapter, role string
			if err := tx.QueryRow(ctx, `SELECT sealed_target,adapter,role FROM inbox_message_targets WHERE id=$1::uuid`, id).Scan(&sealed, &adapter, &role); err != nil {
				return err
			}
			if role != "primary" || m.adapters.byTarget[adapter] == nil || len(sealed) < m.aead.NonceSize() {
				return errConflict
			}
			plain, err := m.aead.Open(nil, sealed[:m.aead.NonceSize()], sealed[m.aead.NonceSize():], []byte(p.TenantID+"/"+project+"/"+id))
			if err != nil {
				return errConflict
			}
			var private struct {
				Ref    string `json:"ref"`
				Secret string `json:"secret"`
			}
			if json.Unmarshal(plain, &private) != nil {
				return errConflict
			}
			kind := "https_webhook"
			if out.DeliveryMode == "pull" {
				// A legacy messaging-only principal can still have a webhook
				// binding. Its durable inbox is authoritative; never test it by
				// contacting the sleeping host.
				kind, private.Ref, private.Secret = "pull", "", ""
			}
			input = &targetInput{Address: out.Address, Adapter: adapter, Kind: kind, Ref: private.Ref, Secret: private.Secret, MaximumLevel: "simple", Role: "primary"}
		}
		return err
	})
	// DNS may wait on an external resolver. Finish the read transaction first;
	// live sends re-check authority and target identity under their write fence.
	if err == nil && input != nil && validateCompatTarget(ctx, input) != nil {
		err = &httpError{409, "target_invalid", "adapter target is unavailable"}
	}
	return out, err
}

type assistantAdapterTestResult struct {
	AdapterID   string `json:"adapter_id"`
	DryRun      bool   `json:"dry_run"`
	State       string `json:"state"`
	MessageID   string `json:"message_id,omitempty"`
	ReceiptPath string `json:"receipt_path,omitempty"`
}

func (m *messaging) testAssistantAdapter(w http.ResponseWriter, r *http.Request) {
	p, project, ok := m.adapterPrincipal(w, r, true)
	if !ok {
		return
	}
	id, valid := parseUUID(r.PathValue("adapterId"))
	if !valid {
		messagingFailure(w, errNotFound)
		return
	}
	var in struct {
		DryRun *bool  `json:"dry_run"`
		Body   string `json:"body"`
		Key    string `json:"idempotency_key"`
	}
	if !decodeJSON(w, r, 24<<10, &in) {
		return
	}
	if !utf8.ValidString(in.Body) || utf8.RuneCountInString(in.Body) > 4096 || strings.ContainsRune(in.Body, 0) || len(in.Key) > 128 {
		messagingFailure(w, badRequest("invalid test input"))
		return
	}
	target, err := m.readAssistantAdapter(r.Context(), p, project, id, true)
	if err != nil {
		messagingFailure(w, err)
		return
	}
	out := assistantAdapterTestResult{AdapterID: id, DryRun: in.DryRun == nil || *in.DryRun, State: "validated"}
	if out.DryRun {
		writeJSON(w, http.StatusOK, out)
		return
	}
	send := compatSend{To: target.Address, Body: in.Body, Key: in.Key, Level: "simple", adapterTestID: id}
	if err := validateCompatSend(&send); err != nil {
		messagingFailure(w, err)
		return
	}
	message, err := m.commitMessage(r.Context(), p, project, send)
	if err != nil {
		messagingFailure(w, err)
		return
	}
	out.State, out.MessageID, out.ReceiptPath = "queued", message.ID, "/api/inbox/messages/"+message.ID+"/receipt"
	writeJSON(w, http.StatusAccepted, out)
}

// Called under the access fence and before the message write. This is an
// internal flag; callers cannot smuggle it through the ordinary messages API.
func requireAdapterTestTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, id, address string) error {
	if p.Kind != tenant.Person || p.KeyID != "" || authz.RequireTx(ctx, tx, p, "inbox.manage", authz.Scope{ProjectID: project}) != nil {
		return errForbidden
	}
	// Older message-target writers use this address fence. Hold it through
	// queueing too, so replacement cannot retarget a test between this check
	// and queueCompatDeliveryWith's binding snapshot.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,54))`, p.TenantID+project+address); err != nil {
		return err
	}
	var current bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assistant_adapters a JOIN inbox_message_targets t ON t.tenant_id=a.tenant_id AND t.id=a.target_id
	 WHERE t.project_id=$1::uuid AND t.id=$2::uuid AND t.address=$3 AND t.role='primary' AND t.enabled)
	 OR EXISTS(SELECT 1 FROM principals WHERE id=$2::uuid AND id::text=$3 AND kind='agent' AND messaging_only IS TRUE
	 AND messaging_owner_id=$4::uuid AND status='active' AND linked_to IS NULL)`, project, id, address, p.ID).Scan(&current); err != nil {
		return err
	}
	if !current {
		return errConflict
	}
	return nil
}

// Health is written in the same transaction as the fenced delivery result,
// before the event counter. Remote response bodies and URLs are never stored.
func recordAssistantHealth(ctx context.Context, tx pgx.Tx, deliveryID, failure string, now time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE assistant_adapters a SET
	 last_success_at=CASE WHEN $2='' THEN $3::timestamptz ELSE a.last_success_at END,
	 last_failure_at=CASE WHEN $2<>'' THEN $3::timestamptz ELSE a.last_failure_at END,
	 last_failure_code=CASE WHEN $2<>'' THEN $2 ELSE a.last_failure_code END
	 FROM inbox_message_deliveries d WHERE d.tenant_id=a.tenant_id AND COALESCE(d.effective_target_id,d.target_id)=a.target_id AND d.id=$1::uuid`, deliveryID, failure, now)
	return err
}
