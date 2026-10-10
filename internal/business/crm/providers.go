// SPDX-License-Identifier: AGPL-3.0-only
package crm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Provider is compiled into the host. Tenant configuration contains only an
// opaque secret reference; it cannot select an outbound URL or executable.
type Provider interface {
	Search(context.Context, string, string) ([]RemoteCustomer, error)
	Fetch(context.Context, string, string) (RemoteCustomer, error)
}
type RemoteCustomer struct {
	Provider   string         `json:"provider"`
	ExternalID string         `json:"external_id"`
	Name       string         `json:"name"`
	Fields     CustomerFields `json:"fields"`
}
type SecretResolver func(context.Context, string) (string, error)
type HTTPProvider struct {
	base    *url.URL
	client  *http.Client
	resolve SecretResolver
}

var secretRefRe = regexp.MustCompile(`^secret://[a-zA-Z0-9_./-]{1,480}$`)

// NewHTTPProvider binds an operator-supplied HTTPS origin. The tenant API
// cannot override it; redirects are refused and responses are bounded.
func NewHTTPProvider(origin string, client *http.Client, resolve SecretResolver) (*HTTPProvider, error) {
	u, e := url.Parse(origin)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || resolve == nil {
		return nil, fmt.Errorf("invalid CRM provider origin")
	}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	copy := *client
	copy.Timeout = 8 * time.Second
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPProvider{base: u, client: &copy, resolve: resolve}, nil
}
func (h *HTTPProvider) call(ctx context.Context, ref, path string, dst any) error {
	return h.callMethod(ctx, ref, http.MethodGet, path, nil, dst)
}
func (h *HTTPProvider) callMethod(ctx context.Context, ref, method, path string, body any, dst any) error {
	if !secretRefRe.MatchString(ref) {
		return errors.New("invalid secret reference")
	}
	token, e := h.resolve(ctx, ref)
	if e != nil {
		// Resolver errors may contain credential-store details or key material.
		return errors.New("provider secret is unavailable")
	}
	if token == "" || strings.ContainsAny(token, "\r\n\t ") {
		return errors.New("provider secret is unavailable")
	}
	u := *h.base
	parts := strings.SplitN(path, "?", 2)
	u.Path = parts[0]
	if len(parts) == 2 {
		u.RawQuery = parts[1]
	}
	var encoded []byte
	if body != nil {
		encoded, e = json.Marshal(body)
		if e != nil {
			return e
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(encoded))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, e := h.client.Do(req)
	if e != nil {
		// Transport errors can quote the outbound URL or Authorization header.
		return errors.New("provider request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("provider returned status %d", resp.StatusCode)
	}
	d := json.NewDecoder(io.LimitReader(resp.Body, 1<<20+1))
	if e = d.Decode(dst); e != nil {
		return e
	}
	return nil
}
func (h *HTTPProvider) Search(ctx context.Context, ref, q string) ([]RemoteCustomer, error) {
	var out []RemoteCustomer
	e := h.call(ctx, ref, "/customers/search?q="+url.QueryEscape(q), &out)
	if e != nil {
		return nil, e
	}
	if len(out) > 100 {
		return nil, errors.New("provider page too large")
	}
	return out, nil
}
func (h *HTTPProvider) Fetch(ctx context.Context, ref, id string) (RemoteCustomer, error) {
	var out RemoteCustomer
	if len(id) == 0 || len(id) > 500 {
		return out, errors.New("invalid external id")
	}
	e := h.call(ctx, ref, "/customers/"+url.PathEscape(id), &out)
	return out, e
}

// HubSpotProvider uses the same secret boundary but maps the company API to
// the neutral CRM customer record. Its API origin is supplied by the host.
type HubSpotProvider struct{ http *HTTPProvider }

func NewHubSpotProvider(origin string, client *http.Client, resolve SecretResolver) (*HubSpotProvider, error) {
	h, e := NewHTTPProvider(origin, client, resolve)
	if e != nil {
		return nil, e
	}
	return &HubSpotProvider{h}, nil
}

type hubCompany struct {
	ID         string `json:"id"`
	Properties struct {
		Name        string `json:"name"`
		Domain      string `json:"domain"`
		Website     string `json:"website"`
		Phone       string `json:"phone"`
		Industry    string `json:"industry"`
		City        string `json:"city"`
		Country     string `json:"country"`
		Address     string `json:"address"`
		Address2    string `json:"address2"`
		Zip         string `json:"zip"`
		Description string `json:"description"`
		Employees   string `json:"numberofemployees"`
		Revenue     string `json:"annualrevenue"`
	} `json:"properties"`
}

func decimalMinor(s string) *int64 {
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() < 0 {
		return nil
	}
	r.Mul(r, big.NewRat(100, 1))
	n, rem := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	if new(big.Int).Mul(rem, big.NewInt(2)).Cmp(r.Denom()) >= 0 {
		n.Add(n, big.NewInt(1))
	}
	if !n.IsInt64() {
		return nil
	}
	v := n.Int64()
	return &v
}
func hubRemote(c hubCompany) RemoteCustomer {
	var employees *int64
	if c.Properties.Employees != "" {
		n, e := strconv.ParseInt(c.Properties.Employees, 10, 64)
		if e == nil && n >= 0 {
			employees = &n
		}
	}
	street := c.Properties.Address
	if c.Properties.Address2 != "" {
		street += ", " + c.Properties.Address2
	}
	return RemoteCustomer{Provider: "hubspot", ExternalID: c.ID, Name: c.Properties.Name, Fields: CustomerFields{Domain: c.Properties.Domain, Website: c.Properties.Website, Phone: c.Properties.Phone, Industry: c.Properties.Industry, Description: c.Properties.Description, EmployeeCount: employees, AnnualRevenueMinor: decimalMinor(c.Properties.Revenue), BillingAddress: &Address{Street: street, PostalCode: c.Properties.Zip, City: c.Properties.City, Country: c.Properties.Country}}}
}
func (h *HubSpotProvider) Search(ctx context.Context, ref, q string) ([]RemoteCustomer, error) {
	var body struct {
		Results []hubCompany `json:"results"`
	}
	e := h.http.callMethod(ctx, ref, http.MethodPost, "/crm/v3/objects/companies/search", map[string]any{"query": q, "limit": 100, "properties": []string{"name", "domain", "industry", "city", "country"}}, &body)
	if e != nil {
		return nil, e
	}
	out := make([]RemoteCustomer, 0, len(body.Results))
	for _, c := range body.Results {
		out = append(out, hubRemote(c))
	}
	return out, nil
}
func (h *HubSpotProvider) Fetch(ctx context.Context, ref, id string) (RemoteCustomer, error) {
	var c hubCompany
	e := h.http.call(ctx, ref, "/crm/v3/objects/companies/"+url.PathEscape(id)+"?properties="+url.QueryEscape("name,domain,website,industry,numberofemployees,annualrevenue,description,phone,address,address2,city,zip,country"), &c)
	return hubRemote(c), e
}

type providerIntegration struct{ providers map[string]Provider }

func (h providerIntegration) Call(ctx context.Context, call plugins.Call, id string, input any) (any, error) {
	if id != "crm_provider" || !call.Grant.Allows(fence.PermIntegrationsCall) {
		return nil, plugins.ErrDenied
	}
	in, ok := input.(ProviderCall)
	if !ok {
		return nil, plugins.ErrDenied
	}
	provider := h.providers[in.ProviderID]
	if provider == nil {
		return nil, plugins.ErrClosed
	}
	switch in.Action {
	case "search":
		return provider.Search(ctx, in.SecretRef, in.Query)
	case "fetch":
		return provider.Fetch(ctx, in.SecretRef, in.ExternalID)
	default:
		return nil, plugins.ErrDenied
	}
}

type ProviderCall struct {
	ProviderID string
	Action     string
	SecretRef  string
	Query      string
	ExternalID string
}

// PluginWithProviders has the same manifest digest as Plugin; only the in-
// process integration implementation changes.
func PluginWithProviders(providers map[string]Provider) (plugins.Plugin, error) {
	p, e := Plugin()
	if e != nil {
		return p, e
	}
	p.Integrations = providerIntegration{providers: providers}
	return p, nil
}

// NewWithProviders mounts manual CRM and optional compiled provider adapters.
func NewWithProviders(pool *pgxpool.Pool, reg *plugins.Registry, providers map[string]Provider) httpapi.Module {
	return &module{pool: pool, reg: reg, providers: providers}
}

type providerConfig struct {
	ID         string `json:"id"`
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
	Revision   int64  `json:"revision"`
}

func (m *module) config(ctx context.Context, tx pgx.Tx, id string) (string, bool, error) {
	if m.providers[id] == nil {
		return "", false, errNotFound
	}
	var ref string
	var enabled bool
	e := tx.QueryRow(ctx, `SELECT secret_ref,enabled FROM crm_provider_configs WHERE provider_id=$1`, id).Scan(&ref, &enabled)
	if errors.Is(e, pgx.ErrNoRows) {
		return "", false, nil
	}
	return ref, enabled, e
}
func (m *module) providerGate(ctx context.Context, tx pgx.Tx, tenantID string) error {
	return m.gate(ctx, tx, tenantID, fence.PermIntegrationsCall)
}
func (m *module) listProviders(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	out := []providerConfig{}
	e := m.run(r, p, fence.PermIntegrationsCall, func(tx pgx.Tx) error {
		ids := make([]string, 0, len(m.providers))
		for id := range m.providers {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			var v providerConfig
			v.ID = id
			var ref string
			e := tx.QueryRow(r.Context(), `SELECT enabled,secret_ref,revision FROM crm_provider_configs WHERE provider_id=$1`, id).Scan(&v.Enabled, &ref, &v.Revision)
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
			v.Configured = ref != ""
			out = append(out, v)
		}
		return nil
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *module) putProviderConfig(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	id := r.PathValue("providerId")
	if m.providers[id] == nil {
		writeErr(w, errNotFound)
		return
	}
	var in struct {
		Enabled          bool   `json:"enabled"`
		SecretRef        string `json:"secret_ref"`
		ExpectedRevision int64  `json:"expected_revision"`
	}
	if e := decodeCRM(r, &in); e != nil {
		writeErr(w, e)
		return
	}
	if in.Enabled && !secretRefRe.MatchString(in.SecretRef) || in.SecretRef != "" && !secretRefRe.MatchString(in.SecretRef) {
		writeErr(w, errInvalid("invalid secret reference"))
		return
	}
	out := providerConfig{ID: id, Enabled: in.Enabled, Configured: in.SecretRef != ""}
	e := m.providerWrite(r.Context(), p, fence.PermIntegrationsCall, "crm.manage", func(tx pgx.Tx) error {
		var revision int64
		var oldEnabled bool
		var oldRef string
		e := tx.QueryRow(r.Context(), `SELECT revision,enabled,secret_ref FROM crm_provider_configs WHERE provider_id=$1 FOR UPDATE`, id).Scan(&revision, &oldEnabled, &oldRef)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if revision != in.ExpectedRevision {
			return errConflict
		}
		e = tx.QueryRow(r.Context(), `INSERT INTO crm_provider_configs(tenant_id,provider_id,enabled,secret_ref) VALUES($1::uuid,$2,$3,$4) ON CONFLICT(tenant_id,provider_id) DO UPDATE SET enabled=excluded.enabled,secret_ref=excluded.secret_ref,revision=crm_provider_configs.revision+1 RETURNING revision`, p.TenantID, id, in.Enabled, in.SecretRef).Scan(&out.Revision)
		if e != nil {
			return e
		}
		ev, e := events.Append(r.Context(), tx, p, events.Change{Type: "crm.provider_config_changed", Before: map[string]any{"provider_id": id, "revision": revision}, After: out})
		if e != nil {
			return e
		}
		var prevEnabled any
		var prevRef any
		var prevRevision any
		if revision > 0 {
			prevEnabled = oldEnabled
			prevRef = oldRef
			prevRevision = revision
		}
		_, e = tx.Exec(r.Context(), `INSERT INTO crm_provider_config_history(tenant_id,event_id,provider_id,previous_enabled,previous_secret_ref,previous_revision) VALUES($1::uuid,$2,$3,$4,$5,$6)`, p.TenantID, ev.ID, id, prevEnabled, prevRef, prevRevision)
		return e
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *module) searchProviders(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 200 {
		writeErr(w, errInvalid("invalid query"))
		return
	}
	type target struct {
		id, ref  string
		provider Provider
	}
	targets := []target{}
	e := m.run(r, p, fence.PermIntegrationsCall, func(tx pgx.Tx) error {
		ids := make([]string, 0, len(m.providers))
		for id := range m.providers {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			provider := m.providers[id]
			ref, enabled, e := m.config(r.Context(), tx, id)
			if e != nil {
				return e
			}
			if enabled {
				targets = append(targets, target{id, ref, provider})
			}
		}
		return nil
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	out := []RemoteCustomer{}
	availability := []struct {
		ProviderID string `json:"provider_id"`
		State      string `json:"state"`
	}{}
	failed := 0
	for _, t := range targets {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		items, e := t.provider.Search(ctx, t.ref, q)
		cancel()
		state := "available"
		if e != nil || len(items) > 100 {
			state = "unavailable"
			failed++
		}
		availability = append(availability, struct {
			ProviderID string `json:"provider_id"`
			State      string `json:"state"`
		}{t.id, state})
		if state == "unavailable" {
			continue
		}
		for _, item := range items {
			if item.ExternalID == "" || item.Name == "" {
				continue
			}
			item.Provider = t.id
			out = append(out, item)
		}
	}
	summary, err := json.Marshal(availability)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("X-CRM-Provider-Availability", string(summary))
	w.Header().Set("X-CRM-Partial-Results", strconv.FormatBool(failed > 0))
	if len(targets) > 0 && failed == len(targets) {
		writeBody(w, http.StatusServiceUnavailable, "provider_unavailable", "customer providers are unavailable")
		return
	}
	// Deduplicate already imported identities under RLS.
	filtered := []RemoteCustomer{}
	seen := map[string]bool{}
	e = m.run(r, p, fence.PermIntegrationsCall, func(tx pgx.Tx) error {
		for _, item := range out {
			var exists bool
			e := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='organisation' AND n.deleted_at IS NULL AND n.fields->>'external_provider'=$1 AND n.fields->>'external_id'=$2)`, item.Provider, item.ExternalID).Scan(&exists)
			if e != nil {
				return e
			}
			key := item.Provider + "\x00" + item.ExternalID
			if !exists && !seen[key] {
				seen[key] = true
				filtered = append(filtered, item)
			}
		}
		return nil
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	httpapi.WriteJSON(w, 200, filtered)
}
func (m *module) importProvider(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	id := r.PathValue("providerId")
	var in struct {
		ExternalID string `json:"external_id"`
	}
	if e := decodeCRM(r, &in); e != nil {
		writeErr(w, e)
		return
	}
	if in.ExternalID == "" || len(in.ExternalID) > 500 {
		writeErr(w, errInvalid("invalid external id"))
		return
	}
	var ref string
	var enabled bool
	e := m.run(r, p, fence.PermIntegrationsCall, func(tx pgx.Tx) error { var e error; ref, enabled, e = m.config(r.Context(), tx, id); return e })
	if e != nil {
		writeErr(w, e)
		return
	}
	if !enabled {
		writeErr(w, errClosed)
		return
	}
	fetchCtx, fetchCancel := context.WithTimeout(r.Context(), 8*time.Second)
	remote, e := m.providers[id].Fetch(fetchCtx, ref, in.ExternalID)
	fetchCancel()
	if e != nil {
		writeBody(w, http.StatusConflict, "provider_unavailable", "customer provider is unavailable")
		return
	}
	remote.Provider = id
	remote.ExternalID = in.ExternalID
	write := CustomerWrite{Name: remote.Name, CustomerFields: remote.Fields}
	write.ExternalProvider = id
	write.ExternalID = in.ExternalID
	if e = write.validate(); e != nil {
		writeErr(w, e)
		return
	}
	var out Customer
	e = m.providerWrite(r.Context(), p, fence.PermNodesContribute, "crm.manage", func(tx pgx.Tx) error {
		if e := m.providerGate(r.Context(), tx, p.TenantID); e != nil {
			return e
		}
		ref2, on, e := m.config(r.Context(), tx, id)
		if e != nil {
			return e
		}
		if !on || ref2 != ref {
			return errClosed
		}
		var exists bool
		e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM nodes n WHERE n.fields->>'external_provider'=$1 AND n.fields->>'external_id'=$2 AND n.deleted_at IS NULL)`, id, in.ExternalID).Scan(&exists)
		if e != nil {
			return e
		}
		if exists {
			return providerIdentityConflict()
		}
		fields, _ := json.Marshal(write.CustomerFields)
		var nodeID string
		e = tx.QueryRow(r.Context(), `WITH kind AS (SELECT id,short_prefix FROM node_kinds WHERE slug='organisation') INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1::uuid,kind.id,aeon_next_node_key($1::uuid,kind.short_prefix),$2,$3::jsonb FROM kind RETURNING id::text`, p.TenantID, write.Name, fields).Scan(&nodeID)
		if e != nil {
			return providerImportError(e)
		}
		_, e = tx.Exec(r.Context(), `INSERT INTO crm_organisation_profiles(tenant_id,organisation_node_id) VALUES($1::uuid,$2::uuid) ON CONFLICT DO NOTHING`, p.TenantID, nodeID)
		if e != nil {
			return e
		}
		out, e = customer(r.Context(), tx, nodeID, false)
		if e != nil {
			return e
		}
		return appendCRM(r.Context(), tx, p, nodeID, "crm.customer_imported", nil, out)
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	httpapi.WriteJSON(w, 201, out)
}
func (m *module) syncProvider(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	org, e := pathUUID(r, "organisationId")
	if e != nil {
		writeErr(w, e)
		return
	}
	var before Customer
	var ref string
	var enabled bool
	e = m.run(r, p, fence.PermIntegrationsCall, func(tx pgx.Tx) error {
		var e error
		before, e = customer(r.Context(), tx, org, false)
		if e != nil {
			return e
		}
		ref, enabled, e = m.config(r.Context(), tx, before.ExternalProvider)
		return e
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	if !enabled || before.ExternalID == "" {
		writeErr(w, errClosed)
		return
	}
	fetchCtx, fetchCancel := context.WithTimeout(r.Context(), 8*time.Second)
	remote, e := m.providers[before.ExternalProvider].Fetch(fetchCtx, ref, before.ExternalID)
	fetchCancel()
	if e != nil {
		// Fetch may have cancelled its request. Preserve attribution and scope,
		// give audit persistence its own bounded context, and reauthorize the write.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		auditErr := m.providerWrite(ctx, p, fence.PermIntegrationsCall, "crm.write", func(tx pgx.Tx) error {
			ref2, on, err := m.config(ctx, tx, before.ExternalProvider)
			if err != nil {
				return err
			}
			if !on || ref2 != ref {
				return errClosed
			}
			current, err := customer(ctx, tx, org, true)
			if err != nil {
				return err
			}
			if current.ExternalProvider != before.ExternalProvider || current.ExternalID != before.ExternalID {
				return errConflict
			}
			_, err = tx.Exec(ctx, `INSERT INTO crm_provider_sync_status(tenant_id,organisation_node_id,provider_id,state,last_error)
    VALUES($1::uuid,$2::uuid,$3,'error','provider_unavailable')
    ON CONFLICT(tenant_id,organisation_node_id) DO UPDATE SET provider_id=excluded.provider_id,state='error',attempted_at=clock_timestamp(),last_error='provider_unavailable'`, p.TenantID, org, before.ExternalProvider)
			if err != nil {
				return err
			}
			return appendCRM(ctx, tx, p, org, "crm.customer_sync_failed", nil, map[string]any{"provider_id": before.ExternalProvider, "reason": "provider_unavailable"})
		})
		if auditErr != nil {
			// Neither provider nor database error text crosses this diagnostic boundary.
			slog.Error("crm sync failure could not be recorded", "provider_id", before.ExternalProvider, "reason", "persistence_unavailable")
			writeBody(w, http.StatusServiceUnavailable, "sync_failure_unrecorded", "provider unavailable; failure status could not be recorded")
			return
		}
		writeBody(w, http.StatusConflict, "provider_unavailable", "customer provider is unavailable; failure status recorded")
		return
	}

	write := CustomerWrite{Name: remote.Name, CustomerFields: remote.Fields}
	write.CustomerNotes = before.CustomerNotes
	write.ExternalProvider = before.ExternalProvider
	write.ExternalID = before.ExternalID
	if e = write.validate(); e != nil {
		writeErr(w, e)
		return
	}
	var out Customer
	e = m.providerWrite(r.Context(), p, fence.PermNodesContribute, "crm.write", func(tx pgx.Tx) error {
		if e := m.providerGate(r.Context(), tx, p.TenantID); e != nil {
			return e
		}
		ref2, on, e := m.config(r.Context(), tx, before.ExternalProvider)
		if e != nil {
			return e
		}
		if !on || ref2 != ref {
			return errClosed
		}
		current, e := customer(r.Context(), tx, org, true)
		if e != nil {
			return e
		}
		if current.Revision != before.Revision || current.ExternalProvider != before.ExternalProvider || current.ExternalID != before.ExternalID {
			return errConflict
		}
		fields, _ := json.Marshal(write.CustomerFields)
		_, e = tx.Exec(r.Context(), `UPDATE nodes SET title=$1,fields=fields||$2::jsonb,updated_at=clock_timestamp() WHERE id=$3::uuid`, write.Name, fields, org)
		if e != nil {
			return e
		}
		out, e = customer(r.Context(), tx, org, false)
		if e != nil {
			return e
		}
		_, e = tx.Exec(r.Context(), `INSERT INTO crm_provider_sync_status(tenant_id,organisation_node_id,provider_id,state,synced_at)
			VALUES($1::uuid,$2::uuid,$3,'ok',clock_timestamp())
			ON CONFLICT(tenant_id,organisation_node_id) DO UPDATE SET provider_id=excluded.provider_id,state='ok',attempted_at=clock_timestamp(),synced_at=clock_timestamp(),last_error=''`, p.TenantID, org, before.ExternalProvider)
		if e != nil {
			return e
		}
		return appendCRM(r.Context(), tx, p, org, "crm.customer_synced", current, out)
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func (m *module) providerSyncStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	org, err := pathUUID(r, "organisationId")
	if err != nil {
		writeErr(w, err)
		return
	}
	out := struct {
		State       string     `json:"state"`
		ProviderID  string     `json:"provider_id"`
		AttemptedAt *time.Time `json:"attempted_at"`
		SyncedAt    *time.Time `json:"synced_at"`
		Error       string     `json:"error"`
	}{State: "never"}
	err = m.run(r, p, fence.PermViewsProvide, func(tx pgx.Tx) error {
		c, err := customer(r.Context(), tx, org, false)
		if err != nil {
			return err
		}
		out.ProviderID = c.ExternalProvider
		err = tx.QueryRow(r.Context(), `SELECT state,attempted_at,synced_at,last_error FROM crm_provider_sync_status WHERE organisation_node_id=$1::uuid AND provider_id=$2`, org, c.ExternalProvider).Scan(&out.State, &out.AttemptedAt, &out.SyncedAt, &out.Error)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

// providerWrite serializes access changes before reauthorization and follows
// tenant -> tree -> record -> event-counter ordering in the final transaction.
func (m *module) providerWrite(ctx context.Context, p tenant.Principal, permission, authority string, fn func(pgx.Tx) error) error {
	return db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, p.TenantID).Scan(&locked); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id',true),0))`); err != nil {
			return err
		}
		if authz.RequireTx(ctx, tx, p, authority, authz.Scope{}) != nil {
			return errForbidden
		}
		if err := m.gate(ctx, tx, p.TenantID, permission); err != nil {
			return err
		}
		return fn(tx)
	})
}
