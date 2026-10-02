// SPDX-License-Identifier: AGPL-3.0-only

package crm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Binding is one explicit contact-to-person link.
type Binding struct {
	PrincipalID        string    `json:"principal_id"`
	ContactNodeID      string    `json:"contact_node_id"`
	BoundByPrincipalID string    `json:"bound_by_principal_id"`
	BoundAt            time.Time `json:"bound_at"`
}

var (
	errClosed    = errors.New("business_crm is not enabled for this operation")
	errNotFound  = errors.New("contact or principal not found")
	errConflict  = errors.New("contact binding requires a live contact and a person principal")
	errForbidden = errors.New("admin session required")
)

type module struct {
	pool          *pgxpool.Pool
	reg           *plugins.Registry
	providers     map[string]Provider
	noteGenerator NoteGenerator
}

// New returns the CRM HTTP module. reg is the sealed registry that contains
// Plugin. A nil or unregistered plugin fails closed.
func New(pool *pgxpool.Pool, reg *plugins.Registry) httpapi.Module {
	return &module{pool: pool, reg: reg}
}

func (m *module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/crm/contacts/{contactId}/principals", m.bind)
	mux.HandleFunc("GET /api/crm/organisations", m.listCustomers)
	mux.HandleFunc("POST /api/crm/organisations", m.createCustomer)
	mux.HandleFunc("GET /api/crm/organisations/{organisationId}", m.getCustomer)
	mux.HandleFunc("PATCH /api/crm/organisations/{organisationId}", m.updateCustomer)
	mux.HandleFunc("DELETE /api/crm/organisations/{organisationId}", m.deleteCustomer)
	mux.HandleFunc("PATCH /api/crm/organisations/{organisationId}/visibility", m.customerVisibility)
	mux.HandleFunc("GET /api/crm/organisations/{organisationId}/contacts", m.listContacts)
	mux.HandleFunc("POST /api/crm/organisations/{organisationId}/contacts", m.createContact)
	mux.HandleFunc("GET /api/crm/contacts/{contactId}", m.getContact)
	mux.HandleFunc("PATCH /api/crm/contacts/{contactId}", m.updateContact)
	mux.HandleFunc("DELETE /api/crm/contacts/{contactId}", m.deleteContact)
	mux.HandleFunc("POST /api/crm/organisations/{organisationId}/primary-contact", m.promoteContact)
	mux.HandleFunc("POST /api/crm/organisations/{organisationId}/number/reformat", m.reformatNumber)
	mux.HandleFunc("GET /api/crm/organisations/{organisationId}/related", m.related)
	mux.HandleFunc("PUT /api/crm/documents/{attachmentId}/metadata", m.putDocumentMetadata)
	mux.HandleFunc("PUT /api/crm/projects/{projectId}/customer", m.setProjectCustomer)
	mux.HandleFunc("PUT /api/crm/projects/{projectId}/cooperation", m.putCooperation)
	mux.HandleFunc("GET /api/crm/projects/{projectId}/cooperation", m.getCooperation)
	mux.HandleFunc("POST /api/crm/organisations/{organisationId}/note-rewrite", m.draftNote)
	mux.HandleFunc("GET /api/crm/organisations/{organisationId}/note-ai", m.noteAIStatus)
	mux.HandleFunc("POST /api/crm/organisations/{organisationId}/note-ai/generate", m.generateNote)
	mux.HandleFunc("POST /api/crm/organisations/{organisationId}/note-rewrite/{draftId}/apply", m.applyNote)
	mux.HandleFunc("GET /api/crm/providers", m.listProviders)
	mux.HandleFunc("PUT /api/crm/providers/{providerId}/config", m.putProviderConfig)
	mux.HandleFunc("GET /api/crm/providers/search", m.searchProviders)
	mux.HandleFunc("POST /api/crm/providers/{providerId}/import", m.importProvider)
	mux.HandleFunc("POST /api/crm/organisations/{organisationId}/sync", m.syncProvider)
	mux.HandleFunc("GET /api/crm/organisations/{organisationId}/sync-status", m.providerSyncStatus)
}

// BindContactPrincipal links a person to a contact and records crm.contact_bound.
// A repeat of the same pair returns the existing binding and writes nothing.
func BindContactPrincipal(ctx context.Context, tx pgx.Tx, p tenant.Principal, contactID, principalID string) (Binding, error) {
	var out Binding
	slug, err := lockContact(ctx, tx, p.TenantID, contactID)
	if err != nil {
		return out, err
	}
	if slug != Contact {
		return out, errConflict
	}
	kind, err := lockPrincipalKind(ctx, tx, p.TenantID, principalID)
	if err != nil {
		return out, err
	}
	if kind != string(tenant.Person) {
		return out, errConflict
	}
	existing, err := lockBinding(ctx, tx, p.TenantID, contactID, principalID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	out, err = insertBinding(ctx, tx, p, contactID, principalID)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return lockBinding(ctx, tx, p.TenantID, contactID, principalID)
		}
		return out, err
	}
	_, err = events.Append(ctx, tx, p, events.Change{NodeID: &out.ContactNodeID, Type: EventContactBound, After: out})
	return out, err
}

func (m *module) bind(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	contactID, ok := parseUUID(r.PathValue("contactId"))
	if !ok {
		writeErr(w, errInvalid("invalid contact id"))
		return
	}
	var body struct {
		PrincipalID string `json:"principal_id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeErr(w, errInvalid("invalid contact binding"))
		return
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeErr(w, errInvalid("invalid contact binding"))
		return
	}
	principalID, ok := parseUUID(body.PrincipalID)
	if !ok {
		writeErr(w, errInvalid("invalid contact binding"))
		return
	}
	var out Binding
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.installationOpen(r.Context(), tx, p.TenantID); err != nil {
			return err
		}
		if err := m.recheckInstallation(r.Context(), tx, p.TenantID); err != nil {
			return err
		}
		var err error
		out, err = BindContactPrincipal(r.Context(), tx, p, contactID, principalID)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusCreated, out)
}

func (m *module) installationOpen(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if m.reg == nil {
		return errClosed
	}
	ok, err := plugins.Enabled(ctx, tx, m.reg, tenantID, ID, OperationBind)
	if err != nil {
		return err
	}
	if !ok {
		return errClosed
	}
	return nil
}

func (m *module) recheckInstallation(ctx context.Context, tx pgx.Tx, tenantID string) error {
	plug, found := m.reg.Lookup(ID)
	if !found || plug.StepPermissions[OperationBind] != fence.PermStepsApply {
		return errClosed
	}
	var enabled bool
	var digest string
	var perms []string
	err := tx.QueryRow(ctx, `SELECT enabled, manifest_digest_sha256, permissions
		FROM plugin_installations
		WHERE tenant_id = $1::uuid AND plugin_id = $2
		FOR UPDATE`, tenantID, ID).Scan(&enabled, &digest, &perms)
	if errors.Is(err, pgx.ErrNoRows) {
		return errClosed
	}
	if err != nil {
		return err
	}
	if !enabled || digest != plug.Manifest.DigestSHA256 || !slices.Contains(perms, fence.PermStepsApply) {
		return errClosed
	}
	return nil
}

func lockContact(ctx context.Context, tx pgx.Tx, tenantID, contactID string) (string, error) {
	var slug string
	err := tx.QueryRow(ctx, `SELECT k.slug
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE n.tenant_id = $1::uuid AND n.id = $2::uuid AND n.deleted_at IS NULL
		FOR UPDATE OF n`, tenantID, contactID).Scan(&slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errNotFound
	}
	return slug, err
}

func lockPrincipalKind(ctx context.Context, tx pgx.Tx, tenantID, principalID string) (string, error) {
	var kind string
	err := tx.QueryRow(ctx, `SELECT kind FROM principals
		WHERE tenant_id = $1::uuid AND id = $2::uuid
		FOR UPDATE`, tenantID, principalID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errNotFound
	}
	return kind, err
}

func lockBinding(ctx context.Context, tx pgx.Tx, tenantID, contactID, principalID string) (Binding, error) {
	var out Binding
	err := tx.QueryRow(ctx, `SELECT principal_id::text, contact_node_id::text, bound_by_principal_id::text, bound_at
		FROM crm_contact_principals
		WHERE tenant_id = $1::uuid AND contact_node_id = $2::uuid AND principal_id = $3::uuid
		FOR UPDATE`, tenantID, contactID, principalID).Scan(
		&out.PrincipalID, &out.ContactNodeID, &out.BoundByPrincipalID, &out.BoundAt)
	return out, err
}

func insertBinding(ctx context.Context, tx pgx.Tx, p tenant.Principal, contactID, principalID string) (Binding, error) {
	var out Binding
	err := tx.QueryRow(ctx, `INSERT INTO crm_contact_principals
		(tenant_id, contact_node_id, principal_id, bound_by_principal_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid)
		RETURNING principal_id::text, contact_node_id::text, bound_by_principal_id::text, bound_at`,
		p.TenantID, contactID, principalID, p.ID).Scan(
		&out.PrincipalID, &out.ContactNodeID, &out.BoundByPrincipalID, &out.BoundAt)
	return out, err
}

type httpError struct {
	status  int
	code    string
	message string
}

func (e *httpError) Error() string { return e.message }

func errInvalid(message string) error {
	return &httpError{status: http.StatusBadRequest, code: "invalid_request", message: message}
}

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		writeBody(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return tenant.Principal{}, false
	}
	return p, true
}

func parseUUID(s string) (string, bool) {
	var u pgtype.UUID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' || u.Scan(s) != nil || !u.Valid {
		return "", false
	}
	return strings.ToLower(s), true
}

func writeErr(w http.ResponseWriter, err error) {
	err = providerImportError(err)
	var he *httpError
	if errors.As(err, &he) {
		writeBody(w, he.status, he.code, he.message)
		return
	}
	switch {
	case errors.Is(err, errForbidden):
		writeBody(w, http.StatusForbidden, "forbidden", errForbidden.Error())
	case errors.Is(err, errNotFound), errors.Is(err, pgx.ErrNoRows):
		writeBody(w, http.StatusNotFound, "not_found", errNotFound.Error())
	case errors.Is(err, errClosed):
		writeBody(w, http.StatusConflict, "conflict", errClosed.Error())
	case errors.Is(err, errConflict):
		writeBody(w, http.StatusConflict, "conflict", errConflict.Error())
	default:
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "P0001" {
			writeBody(w, http.StatusConflict, "conflict", errConflict.Error())
			return
		}
		slog.Error("crm", "err", err)
		writeBody(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}

func writeBody(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message})
}

// A competing import is a normal conflict; unrelated unique violations remain errors.
func providerImportError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "crm_external_identity_unique" {
		return providerIdentityConflict()
	}
	return err
}

func providerIdentityConflict() error {
	return &httpError{status: http.StatusConflict, code: "external_identity_conflict", message: "customer already imported from this provider"}
}
