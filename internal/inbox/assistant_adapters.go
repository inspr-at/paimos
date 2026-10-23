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

func (grokAssistantAdapter) Vendor() string        { return "grok" }
func (grokAssistantAdapter) TargetAdapter() string { return "grok_bot_routine" }
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
		if adapter == nil || adapter.Vendor() == "" || adapter.TargetAdapter() == "" || r.byVendor[adapter.Vendor()] != nil || r.byTarget[adapter.TargetAdapter()] != nil {
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
}

const assistantAdapterCols = `t.id::text,t.project_id::text,t.principal_id::text,t.address,a.vendor,t.version,t.enabled,t.created_at,a.last_success_at,a.last_failure_at,a.last_failure_code`
const assistantAdapterJoin = ` FROM assistant_adapters a JOIN inbox_message_targets t ON t.tenant_id=a.tenant_id AND t.id=a.target_id `

func scanAssistantAdapter(row pgx.Row) (AssistantAdapterConfig, error) {
	var v AssistantAdapterConfig
	err := row.Scan(&v.ID, &v.ProjectID, &v.PrincipalID, &v.Address, &v.Vendor, &v.Version, &v.Enabled, &v.CreatedAt, &v.LastSuccessAt, &v.LastFailureAt, &v.LastFailureCode)
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
	target := targetInput{boundPrincipalID: principalID, PrincipalID: principalID, Address: in.Address, Adapter: adapter.TargetAdapter(), Kind: "https_webhook", Ref: in.Endpoint, Secret: in.Secret, Role: "primary", MaximumLevel: "simple"}
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
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1::uuid AND kind='agent' AND status='active' AND linked_to IS NULL)`, principalID).Scan(&active); err != nil {
			return err
		}
		if !active {
			return errNotFound
		}
		t, err := m.storeTargetTx(ctx, tx, p, project, target)
		if err != nil {
			return err
		}
		out, err = scanAssistantAdapter(tx.QueryRow(ctx, `SELECT `+assistantAdapterCols+assistantAdapterJoin+` WHERE t.project_id=$1::uuid AND t.id=$2::uuid`, project, t.ID))
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
		rows, err := tx.Query(ctx, `SELECT `+assistantAdapterCols+assistantAdapterJoin+` WHERE t.project_id=$1::uuid AND (NULLIF($2,'')::uuid IS NULL OR t.id>NULLIF($2,'')::uuid) ORDER BY t.id LIMIT $3`, project, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanAssistantAdapter(rows)
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
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out AssistantAdapterConfig
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.base.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := requireAdapterTx(ctx, tx, p, project, test); err != nil {
			return err
		}
		var err error
		out, err = scanAssistantAdapter(tx.QueryRow(ctx, `SELECT `+assistantAdapterCols+assistantAdapterJoin+` WHERE t.project_id=$1::uuid AND t.id=$2::uuid`, project, id))
		if err == nil && test && !out.Enabled {
			return errConflict
		}
		if err == nil && test {
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
			input := targetInput{Address: out.Address, Adapter: adapter, Kind: "https_webhook", Ref: private.Ref, Secret: private.Secret, MaximumLevel: "simple", Role: "primary"}
			if validateCompatTarget(ctx, &input) != nil {
				return &httpError{409, "target_invalid", "adapter target is unavailable"}
			}
		}
		return err
	})
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
	 WHERE t.project_id=$1::uuid AND t.id=$2::uuid AND t.address=$3 AND t.role='primary' AND t.enabled)`, project, id, address).Scan(&current); err != nil {
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
