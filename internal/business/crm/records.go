// SPDX-License-Identifier: AGPL-3.0-only
package crm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

type Address struct {
	Street     string `json:"street"`
	PostalCode string `json:"postal_code"`
	City       string `json:"city"`
	Country    string `json:"country"`
	Freeform   string `json:"freeform"`
}
type CustomerFields struct {
	LegalName          string   `json:"legal_name"`
	Industry           string   `json:"industry"`
	Website            string   `json:"website"`
	Domain             string   `json:"domain"`
	Phone              string   `json:"phone"`
	Description        string   `json:"description"`
	CustomerNotes      string   `json:"customer_notes"`
	VATID              string   `json:"vat_id"`
	TaxID              string   `json:"tax_id"`
	RegisterNo         string   `json:"register_no"`
	EmployeeCount      *int64   `json:"employee_count"`
	AnnualRevenueMinor *int64   `json:"annual_revenue_minor"`
	Currency           string   `json:"currency"`
	BillingAddress     *Address `json:"billing_address"`
	VisitingAddress    *Address `json:"visiting_address"`
	HourlyRateMinor    *int64   `json:"hourly_rate_minor"`
	LPRateMinor        *int64   `json:"lp_rate_minor"`
	ExternalProvider   string   `json:"external_provider"`
	ExternalID         string   `json:"external_id"`
	ExternalURL        string   `json:"external_url"`
}
type CustomerWrite struct {
	Name             string `json:"name"`
	ExpectedRevision int64  `json:"expected_revision"`
	CustomerFields
}
type Customer struct {
	ID                   string  `json:"id"`
	Key                  string  `json:"key"`
	Name                 string  `json:"name"`
	Revision             int64   `json:"revision"`
	CustomerNo           *string `json:"customer_no"`
	PrimaryContactNodeID *string `json:"primary_contact_node_id"`
	Archived             bool    `json:"archived"`
	CustomerFields
}
type ContactFields struct {
	Email            string `json:"email"`
	Phone            string `json:"phone"`
	Role             string `json:"role"`
	Note             string `json:"note"`
	ExternalProvider string `json:"external_provider"`
	ExternalID       string `json:"external_id"`
	ExternalURL      string `json:"external_url"`
}
type ContactWrite struct {
	Name             string `json:"name"`
	ExpectedRevision int64  `json:"expected_revision"`
	ContactFields
}
type ContactRecord struct {
	ID                 string `json:"id"`
	Key                string `json:"key"`
	OrganisationNodeID string `json:"organisation_node_id"`
	Name               string `json:"name"`
	Revision           int64  `json:"revision"`
	Primary            bool   `json:"primary"`
	ContactFields
}

func decodeCRM(r *http.Request, target any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20+1))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return errInvalid("invalid JSON body")
	}
	if d.Decode(new(any)) != io.EOF {
		return errInvalid("invalid JSON body")
	}
	return nil
}
func validText(s string, max int) bool { return len(s) <= max }
func validURL(s string) bool {
	if s == "" {
		return true
	}
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}
func validAddress(a Address) bool {
	return validText(a.Street, 500) && validText(a.PostalCode, 50) && validText(a.City, 200) && validText(a.Country, 100) && validText(a.Freeform, 2000)
}
func nonnegative(v *int64) bool { return v == nil || *v >= 0 }
func addressOK(v *Address) bool { return v == nil || validAddress(*v) }
func (v CustomerWrite) validate() error {
	if strings.TrimSpace(v.Name) == "" || !validText(v.Name, 200) || !validText(v.LegalName, 200) || !validText(v.Industry, 200) || !validURL(v.Website) || !validText(v.Domain, 255) || !validText(v.Phone, 100) || !validText(v.Description, 20000) || !validText(v.CustomerNotes, 20000) || !validText(v.VATID, 100) || !validText(v.TaxID, 100) || !validText(v.RegisterNo, 100) || !nonnegative(v.EmployeeCount) || !nonnegative(v.AnnualRevenueMinor) || !nonnegative(v.HourlyRateMinor) || !nonnegative(v.LPRateMinor) || !addressOK(v.BillingAddress) || !addressOK(v.VisitingAddress) || !validText(v.ExternalProvider, 100) || !validText(v.ExternalID, 500) || !validURL(v.ExternalURL) {
		return errInvalid("invalid customer fields")
	}
	if v.Currency != "" && (len(v.Currency) != 3 || strings.ToUpper(v.Currency) != v.Currency) {
		return errInvalid("invalid currency")
	}
	if (v.ExternalProvider == "") != (v.ExternalID == "") {
		return errInvalid("external provider and id must be set together")
	}
	return nil
}
func (v ContactWrite) validate() error {
	if strings.TrimSpace(v.Name) == "" || !validText(v.Name, 200) || !validText(v.Email, 320) || !validText(v.Phone, 100) || !validText(v.Role, 200) || !validText(v.Note, 20000) || !validText(v.ExternalProvider, 100) || !validText(v.ExternalID, 500) || !validURL(v.ExternalURL) {
		return errInvalid("invalid contact fields")
	}
	if v.Email != "" {
		a, e := mail.ParseAddress(v.Email)
		if e != nil || a.Address != v.Email {
			return errInvalid("invalid email")
		}
	}
	if (v.ExternalProvider == "") != (v.ExternalID == "") {
		return errInvalid("external provider and id must be set together")
	}
	return nil
}
func (m *module) gate(ctx context.Context, tx pgx.Tx, tenantID, permission string) error {
	if m.reg == nil {
		return errClosed
	}
	plug, ok := m.reg.Lookup(ID)
	if !ok || !slices.Contains(plug.Manifest.Permissions, permission) {
		return errClosed
	}
	var enabled bool
	var digest string
	var perms []string
	err := tx.QueryRow(ctx, `SELECT enabled,manifest_digest_sha256,permissions FROM plugin_installations WHERE tenant_id=$1::uuid AND plugin_id=$2 FOR SHARE`, tenantID, ID).Scan(&enabled, &digest, &perms)
	if errors.Is(err, pgx.ErrNoRows) {
		return errClosed
	}
	if err != nil {
		return err
	}
	if !enabled || digest != plug.Manifest.DigestSHA256 || !slices.Contains(perms, permission) {
		return errClosed
	}
	return nil
}
func (m *module) run(r *http.Request, p tenant.Principal, permission string, fn func(pgx.Tx) error) error {
	return db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.gate(r.Context(), tx, p.TenantID, permission); err != nil {
			return err
		}
		return fn(tx)
	})
}
func (m *module) actor(w http.ResponseWriter, r *http.Request, write bool) (tenant.Principal, bool) {
	p, ok := principal(w, r)
	if !ok {
		return p, false
	}
	if authz.RequirePattern(authz.BindPool(r.Context(), m.pool), r.Pattern, authz.Scope{}) != nil {
		writeErr(w, errForbidden)
		return p, false
	}
	return p, true
}
func pathUUID(r *http.Request, key string) (string, error) {
	id, ok := parseUUID(r.PathValue(key))
	if !ok {
		return "", errInvalid("invalid id")
	}
	return id, nil
}
func appendCRM(ctx context.Context, tx pgx.Tx, p tenant.Principal, nodeID, typ string, before, after any) error {
	_, err := events.Append(ctx, tx, p, events.Change{NodeID: &nodeID, Type: typ, Before: before, After: after})
	return err
}
func customer(ctx context.Context, tx pgx.Tx, id string, lock bool) (Customer, error) {
	var c Customer
	var raw []byte
	var no, primary *string
	q := `SELECT n.id::text,n.key,n.title,n.fields,o.revision,x.customer_no,o.primary_contact_node_id::text,o.archived_at IS NOT NULL FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id JOIN crm_organisation_profiles o ON o.tenant_id=n.tenant_id AND o.organisation_node_id=n.id LEFT JOIN crm_customer_numbers x ON x.tenant_id=n.tenant_id AND x.organisation_node_id=n.id WHERE n.id=$1::uuid AND k.slug='organisation' AND n.deleted_at IS NULL`
	if lock {
		q += ` FOR UPDATE OF n,o`
	}
	err := tx.QueryRow(ctx, q, id).Scan(&c.ID, &c.Key, &c.Name, &raw, &c.Revision, &no, &primary, &c.Archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, errNotFound
	}
	if err != nil {
		return c, err
	}
	c.CustomerNo = no
	c.PrimaryContactNodeID = primary
	err = json.Unmarshal(raw, &c.CustomerFields)
	return c, err
}
func contact(ctx context.Context, tx pgx.Tx, id string, lock bool) (ContactRecord, error) {
	var c ContactRecord
	var raw []byte
	q := `SELECT n.id::text,n.key,n.title,n.fields,p.revision,p.organisation_node_id::text,coalesce(o.primary_contact_node_id=n.id,false) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id JOIN crm_contact_profiles p ON p.tenant_id=n.tenant_id AND p.contact_node_id=n.id JOIN crm_organisation_profiles o ON o.tenant_id=p.tenant_id AND o.organisation_node_id=p.organisation_node_id WHERE n.id=$1::uuid AND k.slug='contact' AND n.deleted_at IS NULL`
	if lock {
		q += ` FOR UPDATE OF n,p`
	}
	err := tx.QueryRow(ctx, q, id).Scan(&c.ID, &c.Key, &c.Name, &raw, &c.Revision, &c.OrganisationNodeID, &c.Primary)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, errNotFound
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(raw, &c.ContactFields)
	return c, err
}
func (m *module) listCustomers(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query().Get("q")
	if len(q) > 200 {
		writeErr(w, errInvalid("search too long"))
		return
	}
	limit := 50
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, _ = strconv.Atoi(v)
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		offset, _ = strconv.Atoi(v)
	}
	if limit < 1 || limit > 100 || offset < 0 {
		writeErr(w, errInvalid("invalid page"))
		return
	}
	// Archived customers leave the default list; archived=all or true asks for them.
	archived := r.URL.Query().Get("archived")
	if archived == "" {
		archived = "false"
	}
	if archived != "false" && archived != "true" && archived != "all" {
		writeErr(w, errInvalid("invalid archived filter"))
		return
	}
	out := struct {
		Items      []Customer `json:"items"`
		NextOffset *int       `json:"next_offset"`
	}{Items: []Customer{}}
	err := m.run(r, p, fence.PermViewsProvide, func(tx pgx.Tx) error {
		rows, e := tx.Query(r.Context(), `SELECT n.id::text,n.key,n.title,n.fields,o.revision,x.customer_no,o.primary_contact_node_id::text,o.archived_at IS NOT NULL
			FROM nodes n
			LEFT JOIN crm_organisation_profiles o ON o.tenant_id=n.tenant_id AND o.organisation_node_id=n.id
			LEFT JOIN crm_customer_numbers x ON x.tenant_id=n.tenant_id AND x.organisation_node_id=n.id
			WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid
			AND n.kind_id=(SELECT id FROM node_kinds WHERE tenant_id=current_setting('aeon.tenant_id')::uuid AND slug='organisation')
			AND n.deleted_at IS NULL AND ($4='all' OR (o.archived_at IS NOT NULL)=($4='true'))
			AND ($1='' OR n.title ILIKE '%'||$1||'%' OR n.fields->>'legal_name' ILIKE '%'||$1||'%'
				OR x.customer_no ILIKE '%'||$1||'%')
			ORDER BY n.title,n.id LIMIT $2 OFFSET $3`, q, limit+1, offset, archived)
		if e != nil {
			return e
		}
		for rows.Next() {
			var c Customer
			var fields []byte
			var revision *int64
			if e = rows.Scan(&c.ID, &c.Key, &c.Name, &fields, &revision, &c.CustomerNo, &c.PrimaryContactNodeID, &c.Archived); e != nil {
				break
			}
			if len(out.Items) == limit {
				n := offset + limit
				out.NextOffset = &n
				break
			}
			if revision == nil {
				e = errNotFound
				break
			}
			c.Revision = *revision
			if e = json.Unmarshal(fields, &c.CustomerFields); e != nil {
				break
			}
			out.Items = append(out.Items, c)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return e
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *module) getCustomer(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, false)
	if !ok {
		return
	}
	id, e := pathUUID(r, "organisationId")
	if e != nil {
		writeErr(w, e)
		return
	}
	var out Customer
	e = m.run(r, p, fence.PermViewsProvide, func(tx pgx.Tx) error { out, e = customer(r.Context(), tx, id, false); return e })
	if e != nil {
		writeErr(w, e)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func mergeShowcaseFields(base []byte, extra map[string]string) ([]byte, error) {
	if len(extra) == 0 {
		return base, nil
	}
	var fields map[string]any
	if err := json.Unmarshal(base, &fields); err != nil || fields == nil {
		return nil, errInvalid("invalid customer fields")
	}
	for key, value := range extra {
		if key != "showcase_key" && key != "showcase_principal_id" {
			return nil, errInvalid("unsupported showcase field")
		}
		if _, exists := fields[key]; exists || strings.TrimSpace(value) == "" || len(value) > 80 {
			return nil, errInvalid("invalid showcase field")
		}
		fields[key] = value
	}
	return json.Marshal(fields)
}

// InsertCustomer is the organisation create the CRM API uses. extra may carry
// showcase_key; it is stored beside the typed fields and is not part of the
// undo snapshot, which customer() reads back without unknown keys.
func InsertCustomer(ctx context.Context, tx pgx.Tx, p tenant.Principal, in CustomerWrite, extra map[string]string) (Customer, error) {
	var out Customer
	if err := in.validate(); err != nil {
		return out, err
	}
	if in.ExpectedRevision != 0 {
		return out, errInvalid("new customer has no revision")
	}
	raw, _ := json.Marshal(in.CustomerFields)
	fields, err := mergeShowcaseFields(raw, extra)
	if err != nil {
		return out, err
	}
	if err := tx.QueryRow(ctx, `WITH kind AS (SELECT id,short_prefix FROM node_kinds WHERE slug='organisation') INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1::uuid,kind.id,aeon_next_node_key($1::uuid,kind.short_prefix),$2,$3::jsonb FROM kind RETURNING id::text`, p.TenantID, in.Name, fields).Scan(&out.ID); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm_organisation_profiles(tenant_id,organisation_node_id) VALUES($1::uuid,$2::uuid) ON CONFLICT DO NOTHING`, p.TenantID, out.ID); err != nil {
		return out, err
	}
	out, err = customer(ctx, tx, out.ID, false)
	if err != nil {
		return out, err
	}
	return out, appendCRM(ctx, tx, p, out.ID, "crm.customer_created", nil, out)
}

// InsertContact is the contact create the CRM API uses, including the first
// contact becoming primary. extra may carry showcase_key and showcase_principal_id.
func InsertContact(ctx context.Context, tx pgx.Tx, p tenant.Principal, org string, in ContactWrite, extra map[string]string) (ContactRecord, error) {
	var out ContactRecord
	if err := in.validate(); err != nil {
		return out, err
	}
	if in.ExpectedRevision != 0 {
		return out, errInvalid("new contact has no revision")
	}
	before, err := customer(ctx, tx, org, true)
	if err != nil {
		return out, err
	}
	raw, _ := json.Marshal(in.ContactFields)
	fields, err := mergeShowcaseFields(raw, extra)
	if err != nil {
		return out, err
	}
	var id string
	if err := tx.QueryRow(ctx, `WITH kind AS (SELECT id,short_prefix FROM node_kinds WHERE slug='contact') INSERT INTO nodes(tenant_id,kind_id,key,title,fields) SELECT $1::uuid,kind.id,aeon_next_node_key($1::uuid,kind.short_prefix),$2,$3::jsonb FROM kind RETURNING id::text`, p.TenantID, in.Name, fields).Scan(&id); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1::uuid,$2::uuid,$3::uuid,'contact_for')`, p.TenantID, id, org); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm_contact_profiles(tenant_id,contact_node_id,organisation_node_id) VALUES($1::uuid,$2::uuid,$3::uuid) ON CONFLICT DO NOTHING`, p.TenantID, id, org); err != nil {
		return out, err
	}
	out, err = contact(ctx, tx, id, false)
	if err != nil {
		return out, err
	}
	if err := appendCRM(ctx, tx, p, id, "crm.contact_created", nil, out); err != nil {
		return out, err
	}
	if before.PrimaryContactNodeID == nil {
		if _, err := tx.Exec(ctx, `UPDATE crm_organisation_profiles SET primary_contact_node_id=$1::uuid,revision=revision+1 WHERE organisation_node_id=$2::uuid`, id, org); err != nil {
			return out, err
		}
		out.Primary = true
		after, err := customer(ctx, tx, org, false)
		if err != nil {
			return out, err
		}
		return out, appendCRM(ctx, tx, p, org, "crm.primary_contact_changed", before, after)
	}
	return out, nil
}

func (m *module) createCustomer(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	var in CustomerWrite
	if e := decodeCRM(r, &in); e != nil {
		writeErr(w, e)
		return
	}
	if e := in.validate(); e != nil {
		writeErr(w, e)
		return
	}
	if in.ExpectedRevision != 0 {
		writeErr(w, errInvalid("new customer has no revision"))
		return
	}
	var out Customer
	e := m.run(r, p, fence.PermNodesContribute, func(tx pgx.Tx) error {
		var err error
		out, err = InsertCustomer(r.Context(), tx, p, in, nil)
		return err
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	httpapi.WriteJSON(w, 201, out)
}
func (m *module) updateCustomer(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	id, e := pathUUID(r, "organisationId")
	if e != nil {
		writeErr(w, e)
		return
	}
	var in CustomerWrite
	if e = decodeCRM(r, &in); e != nil {
		writeErr(w, e)
		return
	}
	if e = in.validate(); e != nil {
		writeErr(w, e)
		return
	}
	if in.ExpectedRevision < 1 {
		writeErr(w, errInvalid("expected_revision required"))
		return
	}
	var out Customer
	e = m.run(r, p, fence.PermNodesContribute, func(tx pgx.Tx) error {
		before, e := customer(r.Context(), tx, id, true)
		if e != nil {
			return e
		}
		if before.Revision != in.ExpectedRevision {
			return errConflict
		}
		fields, _ := json.Marshal(in.CustomerFields)
		if _, e = tx.Exec(r.Context(), `UPDATE nodes SET title=$1,fields=fields||$2::jsonb,updated_at=clock_timestamp() WHERE id=$3::uuid`, in.Name, fields, id); e != nil {
			return e
		}
		out, e = customer(r.Context(), tx, id, false)
		if e != nil {
			return e
		}
		return appendCRM(r.Context(), tx, p, id, "crm.customer_updated", before, out)
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *module) deleteCustomer(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	id, e := pathUUID(r, "organisationId")
	if e != nil {
		writeErr(w, e)
		return
	}
	e = m.run(r, p, fence.PermNodesContribute, func(tx pgx.Tx) error {
		before, e := customer(r.Context(), tx, id, true)
		if e != nil {
			return e
		}
		var exists bool
		e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM business_quotes WHERE customer_org_node_id=$1::uuid) OR EXISTS(SELECT 1 FROM node_relations r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.target_node_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE r.source_node_id=$1::uuid AND r.type='customer_of' AND k.slug='project' AND n.deleted_at IS NULL)`, id).Scan(&exists)
		if e != nil {
			return e
		}
		if exists {
			return errConflict
		}
		if before.PrimaryContactNodeID != nil {
			_, e = tx.Exec(r.Context(), `UPDATE crm_organisation_profiles SET primary_contact_node_id=NULL,revision=revision+1 WHERE organisation_node_id=$1::uuid`, id)
			if e != nil {
				return e
			}
			after, e := customer(r.Context(), tx, id, false)
			if e != nil {
				return e
			}
			if e = appendCRM(r.Context(), tx, p, id, "crm.primary_contact_changed", before, after); e != nil {
				return e
			}
		}
		rows, e := tx.Query(r.Context(), `SELECT c.contact_node_id::text FROM crm_contact_profiles c JOIN nodes n ON n.tenant_id=c.tenant_id AND n.id=c.contact_node_id WHERE c.organisation_node_id=$1::uuid AND n.deleted_at IS NULL ORDER BY c.contact_node_id`, id)
		if e != nil {
			return e
		}
		var contacts []string
		for rows.Next() {
			var cid string
			if e = rows.Scan(&cid); e != nil {
				break
			}
			contacts = append(contacts, cid)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return e
		}
		for _, cid := range contacts {
			c, e := contact(r.Context(), tx, cid, true)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(r.Context(), `UPDATE nodes SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1::uuid`, cid); e != nil {
				return e
			}
			if e = appendCRM(r.Context(), tx, p, cid, "crm.contact_deleted", c, nil); e != nil {
				return e
			}
		}
		before, e = customer(r.Context(), tx, id, false)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(r.Context(), `UPDATE nodes SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1::uuid`, id); e != nil {
			return e
		}
		return appendCRM(r.Context(), tx, p, id, "crm.customer_deleted", before, nil)
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	w.WriteHeader(204)
}
func (m *module) listContacts(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, false)
	if !ok {
		return
	}
	id, e := pathUUID(r, "organisationId")
	if e != nil {
		writeErr(w, e)
		return
	}
	out := []ContactRecord{}
	e = m.run(r, p, fence.PermViewsProvide, func(tx pgx.Tx) error {
		if _, e := customer(r.Context(), tx, id, false); e != nil {
			return e
		}
		rows, e := tx.Query(r.Context(), `SELECT c.contact_node_id::text FROM crm_contact_profiles c JOIN nodes n ON n.tenant_id=c.tenant_id AND n.id=c.contact_node_id WHERE c.organisation_node_id=$1::uuid AND n.deleted_at IS NULL ORDER BY n.title,n.id`, id)
		if e != nil {
			return e
		}
		var ids []string
		for rows.Next() {
			var cid string
			if e = rows.Scan(&cid); e != nil {
				break
			}
			ids = append(ids, cid)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return e
		}
		for _, cid := range ids {
			c, e := contact(r.Context(), tx, cid, false)
			if e != nil {
				return e
			}
			out = append(out, c)
		}
		return nil
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *module) getContact(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, false)
	if !ok {
		return
	}
	id, e := pathUUID(r, "contactId")
	if e != nil {
		writeErr(w, e)
		return
	}
	var out ContactRecord
	e = m.run(r, p, fence.PermViewsProvide, func(tx pgx.Tx) error { var err error; out, err = contact(r.Context(), tx, id, false); return err })
	if e != nil {
		writeErr(w, e)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *module) createContact(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	org, e := pathUUID(r, "organisationId")
	if e != nil {
		writeErr(w, e)
		return
	}
	var in ContactWrite
	if e = decodeCRM(r, &in); e != nil {
		writeErr(w, e)
		return
	}
	if e = in.validate(); e != nil {
		writeErr(w, e)
		return
	}
	if in.ExpectedRevision != 0 {
		writeErr(w, errInvalid("new contact has no revision"))
		return
	}
	var out ContactRecord
	e = m.run(r, p, fence.PermNodesContribute, func(tx pgx.Tx) error {
		var err error
		out, err = InsertContact(r.Context(), tx, p, org, in, nil)
		return err
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	httpapi.WriteJSON(w, 201, out)
}
func (m *module) updateContact(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	id, e := pathUUID(r, "contactId")
	if e != nil {
		writeErr(w, e)
		return
	}
	var in ContactWrite
	if e = decodeCRM(r, &in); e != nil {
		writeErr(w, e)
		return
	}
	if e = in.validate(); e != nil {
		writeErr(w, e)
		return
	}
	if in.ExpectedRevision < 1 {
		writeErr(w, errInvalid("expected_revision required"))
		return
	}
	var out ContactRecord
	e = m.run(r, p, fence.PermNodesContribute, func(tx pgx.Tx) error {
		before, e := contact(r.Context(), tx, id, true)
		if e != nil {
			return e
		}
		if before.Revision != in.ExpectedRevision {
			return errConflict
		}
		fields, _ := json.Marshal(in.ContactFields)
		_, e = tx.Exec(r.Context(), `UPDATE nodes SET title=$1,fields=fields||$2::jsonb,updated_at=clock_timestamp() WHERE id=$3::uuid`, in.Name, fields, id)
		if e != nil {
			return e
		}
		out, e = contact(r.Context(), tx, id, false)
		if e != nil {
			return e
		}
		return appendCRM(r.Context(), tx, p, id, "crm.contact_updated", before, out)
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *module) promoteContact(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	org, e := pathUUID(r, "organisationId")
	if e != nil {
		writeErr(w, e)
		return
	}
	var in struct {
		ContactNodeID    string `json:"contact_node_id"`
		ExpectedRevision int64  `json:"expected_revision"`
	}
	if e = decodeCRM(r, &in); e != nil {
		writeErr(w, e)
		return
	}
	cid, valid := parseUUID(in.ContactNodeID)
	if !valid || in.ExpectedRevision < 1 {
		writeErr(w, errInvalid("invalid primary contact"))
		return
	}
	var out Customer
	e = m.run(r, p, fence.PermNodesContribute, func(tx pgx.Tx) error {
		before, e := customer(r.Context(), tx, org, true)
		if e != nil {
			return e
		}
		if before.Revision != in.ExpectedRevision {
			return errConflict
		}
		c, e := contact(r.Context(), tx, cid, false)
		if e != nil {
			return e
		}
		if c.OrganisationNodeID != org {
			return errConflict
		}
		if before.PrimaryContactNodeID != nil && *before.PrimaryContactNodeID == cid {
			out = before
			return nil
		}
		_, e = tx.Exec(r.Context(), `UPDATE crm_organisation_profiles SET primary_contact_node_id=$1::uuid,revision=revision+1 WHERE organisation_node_id=$2::uuid`, cid, org)
		if e != nil {
			return e
		}
		out, e = customer(r.Context(), tx, org, false)
		if e != nil {
			return e
		}
		return appendCRM(r.Context(), tx, p, org, "crm.primary_contact_changed", before, out)
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *module) deleteContact(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	id, e := pathUUID(r, "contactId")
	if e != nil {
		writeErr(w, e)
		return
	}
	e = m.run(r, p, fence.PermNodesContribute, func(tx pgx.Tx) error {
		initial, e := contact(r.Context(), tx, id, false)
		if e != nil {
			return e
		}
		org := initial.OrganisationNodeID
		beforeOrg, e := customer(r.Context(), tx, org, true)
		if e != nil {
			return e
		}
		before, e := contact(r.Context(), tx, id, true)
		if e != nil {
			return e
		}
		if before.Primary {
			var successor *string
			var s string
			e = tx.QueryRow(r.Context(), `SELECT c.contact_node_id::text FROM crm_contact_profiles c JOIN nodes n ON n.tenant_id=c.tenant_id AND n.id=c.contact_node_id WHERE c.organisation_node_id=$1::uuid AND c.contact_node_id<>$2::uuid AND n.deleted_at IS NULL ORDER BY n.title,n.id LIMIT 1`, org, id).Scan(&s)
			if e == nil {
				successor = &s
			} else if !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
			_, e = tx.Exec(r.Context(), `UPDATE crm_organisation_profiles SET primary_contact_node_id=$1::uuid,revision=revision+1 WHERE organisation_node_id=$2::uuid`, successor, org)
			if e != nil {
				return e
			}
			afterOrg, e := customer(r.Context(), tx, org, false)
			if e != nil {
				return e
			}
			if e = appendCRM(r.Context(), tx, p, org, "crm.primary_contact_changed", beforeOrg, afterOrg); e != nil {
				return e
			}
		}
		_, e = tx.Exec(r.Context(), `UPDATE nodes SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1::uuid`, id)
		if e != nil {
			return e
		}
		return appendCRM(r.Context(), tx, p, id, "crm.contact_deleted", before, nil)
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	w.WriteHeader(204)
}

// customerVisibility archives or restores a customer (QL1/AEON-109). An archived
// customer leaves the default list and keeps everything attached to it; the
// change is one event, undone through the event log.
func (m *module) customerVisibility(w http.ResponseWriter, r *http.Request) {
	p, ok := m.actor(w, r, true)
	if !ok {
		return
	}
	id, e := pathUUID(r, "organisationId")
	if e != nil {
		writeErr(w, e)
		return
	}
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
		Archived         *bool `json:"archived"`
	}
	if e = decodeCRM(r, &in); e != nil {
		writeErr(w, e)
		return
	}
	if in.ExpectedRevision < 1 || in.Archived == nil {
		writeErr(w, errInvalid("expected_revision and archived required"))
		return
	}
	var out Customer
	e = m.run(r, p, fence.PermNodesContribute, func(tx pgx.Tx) error {
		before, e := customer(r.Context(), tx, id, true)
		if e != nil {
			return e
		}
		if before.Revision != in.ExpectedRevision {
			return errConflict
		}
		if before.Archived == *in.Archived {
			out = before
			return nil
		}
		if e = setCustomerArchived(r.Context(), tx, p, id, *in.Archived); e != nil {
			return e
		}
		if out, e = customer(r.Context(), tx, id, false); e != nil {
			return e
		}
		return appendCRM(r.Context(), tx, p, id, EventCustomerVisibility, before, out)
	})
	if e != nil {
		writeErr(w, e)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

// EventCustomerVisibility records archiving or restoring a customer.
const EventCustomerVisibility = "crm.customer_visibility_changed"

func setCustomerArchived(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, archived bool) error {
	var e error
	if archived {
		_, e = tx.Exec(ctx, `UPDATE crm_organisation_profiles SET archived_at=clock_timestamp(),archived_by_principal_id=$2::uuid,revision=revision+1 WHERE organisation_node_id=$1::uuid`, id, p.ID)
	} else {
		_, e = tx.Exec(ctx, `UPDATE crm_organisation_profiles SET archived_at=NULL,archived_by_principal_id=NULL,revision=revision+1 WHERE organisation_node_id=$1::uuid`, id)
	}
	return e
}
