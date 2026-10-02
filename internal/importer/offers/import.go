// SPDX-License-Identifier: AGPL-3.0-only
// Package offers imports an offline, GET-only classic offer bundle. The
// coordinator wires RunCommand as `paimos import paimos-offers`; no HTTP module or
// plugin manifest is needed because this is an operator CLI, not an API.
package offers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

var instanceRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
var uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var customerNumberRE = regexp.MustCompile(`^K([0-9]{4})([1-9][0-9]*)$`)

type Mapping struct {
	SourceKind     string `json:"source_kind"`
	SourceID       string `json:"source_id"`
	SourceNumber   string `json:"source_number,omitempty"`
	SourceRevision string `json:"source_revision"`
	NodeID         string `json:"node_id,omitempty"`
	Action         string `json:"action"`
	Review         string `json:"review,omitempty"`
}
type Report struct {
	SourceSystem   string    `json:"source_system"`
	SourceInstance string    `json:"source_instance"`
	Applied        bool      `json:"applied"`
	Mappings       []Mapping `json:"mappings"`
}

type record struct {
	kind, id, number, revision, title string
	rank                              int64
	fields                            map[string]any
	digest                            string
}

// Import validates the complete bundle before writing. Apply uses one
// db.InTenant transaction and a per-source advisory lock; any failure rolls
// back every CRM, quote, provenance, sequence and event change together.
func Import(ctx context.Context, pool *pgxpool.Pool, tenantID, actorID, instance string, b Bundle, apply bool) (Report, error) {
	r := Report{SourceSystem: "paimos", SourceInstance: instance, Applied: apply, Mappings: []Mapping{}}
	if !instanceRE.MatchString(instance) {
		return r, errors.New("invalid source instance")
	}
	if len(b.Customers) == 0 {
		return r, errors.New("empty bundle")
	}
	customers := map[int64]Customer{}
	contacts := map[int64]Contact{}
	for _, c := range b.Customers {
		if _, ok := customers[c.ID]; ok {
			return r, errors.New("duplicate customer")
		}
		customers[c.ID] = c
	}
	for _, c := range b.Contacts {
		if _, ok := contacts[c.ID]; ok || customers[c.CustomerID].ID == 0 {
			return r, errors.New("duplicate or orphan contact")
		}
		contacts[c.ID] = c
	}
	for _, o := range b.Offers {
		if customers[o.CustomerID].ID == 0 {
			return r, errors.New("orphan offer")
		}
		if _, err := convertDocument(instance, o, ""); err != nil {
			return r, err
		}
		if o.Status != "draft" && o.Status != "sent" && o.Status != "accepted" {
			return r, fmt.Errorf("unsupported offer state %q", o.Status)
		}
		if !offerNumber.MatchString(o.OfferNo) {
			return r, fmt.Errorf("invalid classic offer number %q", o.OfferNo)
		}
	}
	if !apply {
		for _, c := range b.Customers {
			r.Mappings = append(r.Mappings, Mapping{SourceKind: "customer", SourceID: strconv.FormatInt(c.ID, 10), SourceNumber: c.CustomerNo, SourceRevision: c.UpdatedAt, Action: "plan"})
		}
		for _, c := range b.Contacts {
			r.Mappings = append(r.Mappings, Mapping{SourceKind: "contact", SourceID: strconv.FormatInt(c.ID, 10), SourceRevision: c.UpdatedAt, Action: "plan"})
		}
		for _, o := range b.Offers {
			m := Mapping{SourceKind: "offer", SourceID: strconv.FormatInt(o.ID, 10), SourceNumber: o.OfferNo, SourceRevision: strconv.FormatInt(o.Revision, 10), Action: "plan"}
			if o.Status == "accepted" {
				m.Review = "source acceptance retained as provenance; native acceptance evidence unavailable"
			}
			r.Mappings = append(r.Mappings, m)
		}
		return r, nil
	}
	if pool == nil || !uuidRE.MatchString(tenantID) || !uuidRE.MatchString(actorID) {
		return r, errors.New("apply requires pool, tenant ID and actor principal ID")
	}
	batch := &importEvents{}
	ctx = context.WithValue(ctx, importEventsKey{}, batch)
	err := db.InTenant(db.AllProjects(ctx, "classic offers importer"), pool, tenantID, func(tx pgx.Tx) error {
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, tenantID).Scan(&locked); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id',true),0))`); err != nil {
			return err
		}
		// Lock all existing mapped resources in native order before any event append.
		if err := lockMappedRecords(ctx, tx, tenantID, instance); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenantID+":paimos-offers:"+instance); err != nil {
			return err
		}
		actor := tenant.Principal{TenantID: tenantID, ID: actorID, Kind: tenant.Person}
		if err := authz.RequireTx(ctx, tx, actor, "imports.manage", authz.Scope{}); err != nil {
			return errors.New("actor requires import management permission")
		}
		var profile *ProfileSnapshot
		var selected ProfileSnapshot
		var profileRaw []byte
		profileErr := tx.QueryRow(ctx, `
			SELECT p.id::text,r.revision,r.definition
			FROM quote_settings s
			JOIN quote_document_profiles p ON p.tenant_id=s.tenant_id AND p.id=s.default_profile_id
			JOIN quote_document_profile_revisions r ON r.tenant_id=p.tenant_id AND r.profile_id=p.id AND r.revision=p.current_revision
			WHERE s.tenant_id=$1::uuid AND p.archived_at IS NULL`, tenantID).Scan(&selected.ID, &selected.Revision, &profileRaw)
		if profileErr != nil && !errors.Is(profileErr, pgx.ErrNoRows) {
			return profileErr
		}
		if profileErr == nil {
			if err := json.Unmarshal(profileRaw, &selected.Definition); err != nil {
				return err
			}
			selected.Definition = selected.Definition.Normalized()
			profile = &selected
		}
		orgIDs := map[int64]string{}
		changedContacts := map[int64]bool{}
		relatedOrgs := map[string]bool{}
		contactIDs := map[int64]string{}
		for _, c := range b.Customers {
			rc, err := customerRecord(c)
			if err != nil {
				return err
			}
			id, action, err := upsertNode(ctx, tx, actor, instance, rc, "organisation")
			if err != nil {
				return err
			}
			orgIDs[c.ID] = id
			if action != "skip" {
				if _, err = tx.Exec(ctx, `INSERT INTO crm_organisation_profiles(tenant_id,organisation_node_id) VALUES($1::uuid,$2::uuid) ON CONFLICT DO NOTHING`, tenantID, id); err != nil {
					return err
				}
				if c.CustomerNo != "" {
					var existing string
					err = tx.QueryRow(ctx, `SELECT customer_no FROM crm_customer_numbers WHERE organisation_node_id=$1::uuid`, id).Scan(&existing)
					if errors.Is(err, pgx.ErrNoRows) {
						if _, err = tx.Exec(ctx, `INSERT INTO crm_customer_numbers(tenant_id,organisation_node_id,customer_no,provenance) VALUES($1::uuid,$2::uuid,$3,'imported')`, tenantID, id, c.CustomerNo); err != nil {
							return fmt.Errorf("customer number collision: %w", err)
						}
					} else if err != nil {
						return err
					} else if existing != c.CustomerNo {
						return errors.New("imported customer number changed")
					}
					if match := customerNumberRE.FindStringSubmatch(c.CustomerNo); match != nil {
						n, parseErr := strconv.ParseInt(match[2], 10, 64)
						if parseErr != nil {
							return parseErr
						}
						if _, err = tx.Exec(ctx, `INSERT INTO quote_number_sequences(tenant_id,kind,period,value) VALUES($1::uuid,'customer',$2,$3) ON CONFLICT (tenant_id,kind,period) DO UPDATE SET value=greatest(quote_number_sequences.value,EXCLUDED.value)`, tenantID, match[1], n); err != nil {
							return err
						}
					}
				}
			}
			r.Mappings = append(r.Mappings, Mapping{SourceKind: "customer", SourceID: rc.id, SourceNumber: rc.number, SourceRevision: rc.revision, NodeID: id, Action: action})
		}
		for _, c := range b.Contacts {
			rc, err := contactRecord(c)
			if err != nil {
				return err
			}
			id, action, err := upsertNode(ctx, tx, actor, instance, rc, "contact")
			if err != nil {
				return err
			}
			contactIDs[c.ID] = id
			changedContacts[c.ID] = action != "skip"
			if action != "skip" {
				if _, err = tx.Exec(ctx, `INSERT INTO crm_contact_profiles(tenant_id,contact_node_id,organisation_node_id) VALUES($1::uuid,$2::uuid,$3::uuid) ON CONFLICT (tenant_id,contact_node_id) DO UPDATE SET revision=crm_contact_profiles.revision+1`, tenantID, id, orgIDs[c.CustomerID]); err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1::uuid,$2::uuid,$3::uuid,'contact_for') ON CONFLICT DO NOTHING`, tenantID, id, orgIDs[c.CustomerID]); err != nil {
					return err
				}
			}
			r.Mappings = append(r.Mappings, Mapping{SourceKind: "contact", SourceID: rc.id, SourceRevision: rc.revision, NodeID: id, Action: action})
		}
		for _, c := range b.Contacts {
			if !c.IsPrimary || !changedContacts[c.ID] {
				continue
			}
			id := contactIDs[c.ID]
			if id == "" {
				return errors.New("missing primary contact")
			}
			var old string
			err := tx.QueryRow(ctx, `SELECT coalesce(primary_contact_node_id::text,'') FROM crm_organisation_profiles WHERE organisation_node_id=$1::uuid FOR UPDATE`, orgIDs[c.CustomerID]).Scan(&old)
			if err != nil {
				return err
			}
			if old != id {
				// The customer may have an unchanged source revision while a newer
				// contact tries to replace a primary contact chosen in Aeon.
				if customerSkipped(r.Mappings, orgIDs[c.CustomerID]) && !relatedOrgs[orgIDs[c.CustomerID]] {
					if err := checkNativeBaseline(ctx, tx, orgIDs[c.CustomerID], "customer"); err != nil {
						return err
					}
				}
				relatedOrgs[orgIDs[c.CustomerID]] = true
				if _, err = tx.Exec(ctx, `UPDATE crm_organisation_profiles SET primary_contact_node_id=$1::uuid,revision=revision+1 WHERE organisation_node_id=$2::uuid`, id, orgIDs[c.CustomerID]); err != nil {
					return err
				}
				if err = event(ctx, tx, actor, orgIDs[c.CustomerID], "import.primary_contact", map[string]any{"contact_node_id": old}, map[string]any{"contact_node_id": id}); err != nil {
					return err
				}
			}
		}
		for _, o := range b.Offers {
			rc, err := offerRecord(o)
			if err != nil {
				return err
			}
			var sourceDoc legacyDocument
			if err = decode(o.Document, &sourceDoc); err != nil {
				return err
			}
			contactID := ""
			var recipient struct {
				Contact string `json:"contact"`
				Email   string `json:"email"`
			}
			if err = json.Unmarshal(sourceDoc.Customer, &recipient); err != nil {
				return err
			}
			for _, c := range b.Contacts {
				if c.CustomerID == o.CustomerID && (recipient.Email != "" && strings.EqualFold(c.Email, recipient.Email) || recipient.Contact != "" && c.Name == recipient.Contact) {
					if contactID != "" && contactID != contactIDs[c.ID] {
						return errors.New("ambiguous recipient contact")
					}
					contactID = contactIDs[c.ID]
				}
			}
			doc, err := convertDocument(instance, o, contactID)
			if err != nil {
				return err
			}
			doc.Profile = profile
			id, action, err := upsertNode(ctx, tx, actor, instance, rc, "quote")
			if err != nil {
				return err
			}
			if action != "skip" {
				if err = writeOffer(ctx, tx, actor, id, orgIDs[o.CustomerID], o, doc, action); err != nil {
					return err
				}
			}
			m := Mapping{SourceKind: "offer", SourceID: rc.id, SourceNumber: rc.number, SourceRevision: rc.revision, NodeID: id, Action: action}
			if o.Status == "accepted" {
				m.Review = "source acceptance retained as provenance; native acceptance evidence unavailable"
			}
			r.Mappings = append(r.Mappings, m)
		}
		if err := batch.flush(); err != nil {
			return err
		}
		// Persist a complete final native baseline, including primary-contact
		// changes, only after every related write in this atomic bundle succeeds.
		for _, mapping := range r.Mappings {
			if mapping.Action == "skip" && !relatedOrgs[mapping.NodeID] {
				continue
			}
			if err := appendImport(ctx, tx, actor, mapping.NodeID, mapping.SourceKind, mapping.Action, record{id: mapping.SourceID, number: mapping.SourceNumber, revision: mapping.SourceRevision}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	return r, nil
}

func customerRecord(c Customer) (record, error) {
	rank, err := revisionTime(c.UpdatedAt)
	if err != nil {
		return record{}, err
	}
	fields := map[string]any{"legal_name": c.Name, "industry": c.Industry, "domain": c.Domain, "external_provider": c.ExternalProvider, "external_id": c.ExternalID, "external_url": c.ExternalURL, "employee_count": c.EmployeeCount, "annual_revenue_minor": c.AnnualRevenueCents, "currency": "EUR", "website": c.Website, "phone": c.Phone, "description": c.Description, "customer_notes": c.Notes, "vat_id": c.VATID, "tax_id": c.TaxID, "register_no": c.CompanyRegisterNo, "billing_address": map[string]string{"street": c.BillingStreet, "postal_code": c.BillingZIP, "city": c.BillingCity, "country": c.BillingCountry, "freeform": c.Address}, "visiting_address": map[string]string{"street": c.VisitingStreet, "postal_code": c.VisitingZIP, "country": c.Country}}
	if c.RateHourly != "" {
		cents, e := majorMoney(c.RateHourly)
		if e != nil {
			return record{}, e
		}
		fields["hourly_rate_minor"] = cents
	}
	if c.RateLP != "" {
		cents, e := majorMoney(c.RateLP)
		if e != nil {
			return record{}, e
		}
		fields["lp_rate_minor"] = cents
	}
	r := record{"customer", strconv.FormatInt(c.ID, 10), c.CustomerNo, c.UpdatedAt, c.Name, rank, fields, ""}
	r.digest, err = sha(c)
	return r, err
}
func contactRecord(c Contact) (record, error) {
	rank, err := revisionTime(c.UpdatedAt)
	if err != nil {
		return record{}, err
	}
	fields := map[string]any{"email": c.Email, "phone": c.Phone, "role": c.Role, "note": c.Notes, "external_provider": c.ExternalProvider, "external_id": c.ExternalID, "external_url": c.ExternalURL}
	r := record{"contact", strconv.FormatInt(c.ID, 10), "", c.UpdatedAt, c.Name, rank, fields, ""}
	r.digest, err = sha(c)
	return r, err
}
func offerRecord(o Offer) (record, error) {
	encoded, err := json.Marshal(o)
	if err != nil {
		return record{}, err
	}
	var canonical any
	if err = decode(encoded, &canonical); err != nil {
		return record{}, err
	}
	digest, err := sha(canonical)
	if err != nil {
		return record{}, err
	}
	var d struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(o.Document, &d); err != nil {
		return record{}, err
	}
	return record{"offer", strconv.FormatInt(o.ID, 10), o.OfferNo, strconv.FormatInt(o.Revision, 10), d.Title, o.Revision, map[string]any{"source_status": o.Status, "source_sent_at": o.SentAt}, digest}, nil
}

func upsertNode(ctx context.Context, tx pgx.Tx, p tenant.Principal, instance string, r record, kind string) (string, string, error) {
	var id, priorDigest string
	var priorRank int64
	err := tx.QueryRow(ctx, `SELECT node_id::text,revision_rank,source_sha256 FROM paimos_offer_imports WHERE tenant_id=$1::uuid AND source_instance=$2 AND source_kind=$3 AND source_id=$4 FOR UPDATE`, p.TenantID, instance, r.kind, r.id).Scan(&id, &priorRank, &priorDigest)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", "", err
	}
	if err == nil {
		if r.rank < priorRank {
			return id, "skip", nil
		}
		if r.rank == priorRank {
			if r.digest != priorDigest {
				return "", "", errors.New("source changed without a newer revision")
			}
			return id, "skip", nil
		}
	}
	if id != "" {
		if err := checkNativeBaseline(ctx, tx, id, r.kind); err != nil {
			return "", "", err
		}
	}
	provenance := map[string]any{"source_system": "paimos", "source_instance": instance, "source_kind": r.kind, "source_id": r.id, "source_number": r.number, "source_revision": r.revision, "imported_at": time.Now().UTC().Format(time.RFC3339Nano)}
	fields := map[string]any{}
	for key, value := range r.fields {
		fields[key] = value
	}
	fields["provenance"] = provenance
	raw, err := json.Marshal(fields)
	if err != nil {
		return "", "", err
	}
	action := "update"
	if id == "" {
		action = "create"
		err = tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,fields) SELECT $1::uuid,aeon_next_node_key($1::uuid,k.short_prefix),k.id,$2,$3::jsonb FROM node_kinds k WHERE k.tenant_id=$1::uuid AND k.slug=$4 RETURNING id::text`, p.TenantID, r.title, string(raw), kind).Scan(&id)
		if err != nil {
			return "", "", fmt.Errorf("create %s node (kind configured?): %w", kind, err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO paimos_offer_imports(tenant_id,source_instance,source_kind,source_id,node_id,source_number,source_revision,revision_rank,source_sha256) VALUES($1::uuid,$2,$3,$4,$5::uuid,$6,$7,$8,$9)`, p.TenantID, instance, r.kind, r.id, id, r.number, r.revision, r.rank, r.digest)
	} else {
		var existingKind string
		if err = tx.QueryRow(ctx, `SELECT k.slug FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1::uuid AND n.deleted_at IS NULL FOR UPDATE OF n`, id).Scan(&existingKind); err != nil || existingKind != kind {
			return "", "", errors.New("source mapping points to missing or wrong node kind")
		}
		_, err = tx.Exec(ctx, `UPDATE nodes SET title=$1,fields=fields||$2::jsonb,updated_at=clock_timestamp() WHERE id=$3::uuid`, r.title, string(raw), id)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE paimos_offer_imports SET source_number=$1,source_revision=$2,revision_rank=$3,source_sha256=$4,imported_at=clock_timestamp() WHERE tenant_id=$5::uuid AND source_instance=$6 AND source_kind=$7 AND source_id=$8`, r.number, r.revision, r.rank, r.digest, p.TenantID, instance, r.kind, r.id)
		}
	}
	return id, action, err
}
func appendImport(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, kind, action string, r record) error {
	baseline, err := nativeSnapshot(ctx, tx, id, kind)
	if err != nil {
		return err
	}
	return event(ctx, tx, p, id, "import."+kind, nil, map[string]any{"action": action, "source_system": "paimos", "source_id": r.id, "source_number": r.number, "source_revision": r.revision, "node_id": id, "native_baseline": json.RawMessage(baseline)})
}
func event(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, typ string, before, after any) error {
	return queueImportEvent(ctx, func() error {
		_, err := events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: typ, Before: before, After: after})
		return err
	})
}
