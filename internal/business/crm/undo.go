// SPDX-License-Identifier: AGPL-3.0-only
package crm

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/tenant"
)

// UndoHandlers is registered by the coordinator with events.WithUndoHandlers.
// Undo runs inside the event module's db.InTenant transaction. Immutable
// commercial numbers remain guarded if a quote has used them.
func UndoHandlers(registries ...*plugins.Registry) map[string]events.UndoFunc {
	var reg *plugins.Registry
	if len(registries) > 0 {
		reg = registries[0]
	} else {
		reg = plugins.NewRegistry()
		p, e := Plugin()
		if e != nil || reg.Register(p) != nil {
			reg = nil
		} else {
			reg.Seal()
		}
	}
	m := &module{reg: reg}
	return map[string]events.UndoFunc{
		"crm.customer_created": m.undoCustomer, "crm.customer_updated": m.undoCustomer, "crm.customer_deleted": m.undoCustomer, "crm.customer_imported": m.undoCustomer, "crm.customer_synced": m.undoCustomer, "crm.note_rewrite_applied": m.undoCustomer,
		"crm.contact_created": m.undoContact, "crm.contact_updated": m.undoContact, "crm.contact_deleted": m.undoContact, "crm.primary_contact_changed": m.undoPrimary,
		EventContactBound:              m.undoBinding,
		"crm.project_customer_changed": m.undoProjectCustomer, "crm.document_metadata_changed": m.undoDocument, "crm.project_cooperation_changed": m.undoCooperation,
		"crm.note_rewrite_drafted": m.undoDraft, "crm.provider_config_changed": m.undoProviderConfig,
		"crm.customer_number_allocated": m.undoNumber, "crm.customer_number_converted": m.undoNumber,
		EventCustomerVisibility: m.undoVisibility,
	}
}
func (m *module) undoAuthority(ctx context.Context, tx pgx.Tx, p tenant.Principal, pluginPermission, permission string) error {
	// Serialize with role/binding changes before consulting live authority and
	// before taking plugin or CRM row locks. Legacy roles are not grants.
	if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, p.TenantID); err != nil {
		return err
	}
	var kind string
	err := tx.QueryRow(ctx, `SELECT kind FROM principals WHERE id=$1::uuid`, p.ID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (kind != "person" || p.Kind != tenant.Person || p.KeyCreatorID != "") {
		return events.ErrForbidden
	}
	if err != nil {
		return err
	}
	if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return events.ErrForbidden
		}
		return err
	}
	if err := m.gate(ctx, tx, p.TenantID, pluginPermission); err != nil {
		if errors.Is(err, errClosed) {
			return events.ErrForbidden
		}
		return err
	}
	return nil
}

func undoID(e events.Event) (string, error) {
	if e.NodeID == nil {
		return "", events.ErrConflict
	}
	id, ok := parseUUID(*e.NodeID)
	if !ok {
		return "", events.ErrConflict
	}
	return id, nil
}
func snapshotAs[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return v, events.ErrConflict
	}
	return v, nil
}
func matches[T any](a, b T) bool { return reflect.DeepEqual(a, b) }
func restoreCustomerFields(ctx context.Context, tx pgx.Tx, id string, c Customer) error {
	fields, e := json.Marshal(c.CustomerFields)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE nodes SET title=$1,fields=fields||$2::jsonb,updated_at=clock_timestamp() WHERE id=$3::uuid`, c.Name, fields, id); e != nil {
		return e
	}
	return nil
}
func (m *module) undoCustomer(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	change := events.Change{Type: e.Type}
	permission := "crm.write"
	if e.Type == "crm.customer_imported" || e.Type == "crm.note_rewrite_applied" {
		permission = "crm.manage"
	}
	if err := m.undoAuthority(ctx, tx, p, fence.PermNodesContribute, permission); err != nil {
		return change, err
	}
	id, err := undoID(e)
	if err != nil {
		return change, err
	}
	change.NodeID = &id
	switch e.Type {
	case "crm.customer_created", "crm.customer_imported":
		expected, err := snapshotAs[Customer](e.After)
		if err != nil {
			return change, err
		}
		current, err := customer(ctx, tx, id, true)
		if err != nil || !matches(current, expected) {
			return change, events.ErrConflict
		}
		var deps bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM business_quotes WHERE customer_org_node_id=$1::uuid) OR EXISTS(SELECT 1 FROM node_relations WHERE source_node_id=$1::uuid AND type='customer_of') OR EXISTS(SELECT 1 FROM crm_contact_profiles c JOIN nodes n ON n.tenant_id=c.tenant_id AND n.id=c.contact_node_id WHERE c.organisation_node_id=$1::uuid AND n.deleted_at IS NULL)`, id).Scan(&deps)
		if err != nil {
			return change, err
		}
		if deps {
			return change, events.ErrConflict
		}
		_, err = tx.Exec(ctx, `UPDATE nodes SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1::uuid`, id)
		if err != nil {
			return change, err
		}
		change.Before = current
		return change, nil
	case "crm.customer_updated", "crm.customer_synced", "crm.note_rewrite_applied":
		before, err := snapshotAs[Customer](e.Before)
		if err != nil {
			return change, err
		}
		var expected Customer
		var draftID string
		if e.Type == "crm.note_rewrite_applied" {
			var wrap struct {
				Customer Customer `json:"customer"`
				DraftID  string   `json:"draft_id"`
			}
			if json.Unmarshal(e.After, &wrap) != nil {
				return change, events.ErrConflict
			}
			expected = wrap.Customer
			draftID = wrap.DraftID
		} else {
			expected, err = snapshotAs[Customer](e.After)
			if err != nil {
				return change, err
			}
		}
		current, err := customer(ctx, tx, id, true)
		if err != nil || !matches(current, expected) {
			return change, events.ErrConflict
		}
		if err = restoreCustomerFields(ctx, tx, id, before); err != nil {
			return change, err
		}
		if draftID != "" {
			_, err = tx.Exec(ctx, `UPDATE crm_note_rewrite_drafts SET applied_at=NULL WHERE id=$1::uuid AND organisation_node_id=$2::uuid`, draftID, id)
			if err != nil {
				return change, err
			}
		}
		after, err := customer(ctx, tx, id, false)
		if err != nil {
			return change, err
		}
		change.Before = current
		change.After = after
		return change, nil
	case "crm.customer_deleted":
		before, err := snapshotAs[Customer](e.Before)
		if err != nil {
			return change, err
		}
		var rev int64
		var deleted bool
		err = tx.QueryRow(ctx, `SELECT o.revision,n.deleted_at IS NOT NULL FROM crm_organisation_profiles o JOIN nodes n ON n.tenant_id=o.tenant_id AND n.id=o.organisation_node_id WHERE o.organisation_node_id=$1::uuid FOR UPDATE OF o,n`, id).Scan(&rev, &deleted)
		if err != nil || !deleted || rev != before.Revision {
			return change, events.ErrConflict
		}
		_, err = tx.Exec(ctx, `UPDATE nodes SET deleted_at=NULL,updated_at=clock_timestamp() WHERE id=$1::uuid`, id)
		if err != nil {
			return change, err
		}
		after, err := customer(ctx, tx, id, false)
		if err != nil {
			return change, err
		}
		change.After = after
		return change, nil
	default:
		return change, events.ErrConflict
	}
}
func restoreContactFields(ctx context.Context, tx pgx.Tx, id string, c ContactRecord) error {
	fields, e := json.Marshal(c.ContactFields)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE nodes SET title=$1,fields=fields||$2::jsonb,updated_at=clock_timestamp() WHERE id=$3::uuid`, c.Name, fields, id); e != nil {
		return e
	}
	return nil
}
func (m *module) undoContact(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	change := events.Change{Type: e.Type}
	if err := m.undoAuthority(ctx, tx, p, fence.PermNodesContribute, "crm.write"); err != nil {
		return change, err
	}
	id, err := undoID(e)
	if err != nil {
		return change, err
	}
	change.NodeID = &id
	switch e.Type {
	case "crm.contact_created":
		expected, err := snapshotAs[ContactRecord](e.After)
		if err != nil {
			return change, err
		}
		current, err := contact(ctx, tx, id, true)
		if err != nil || !matches(current, expected) {
			return change, events.ErrConflict
		}
		var bound bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM crm_contact_principals WHERE contact_node_id=$1::uuid)`, id).Scan(&bound)
		if err != nil {
			return change, err
		}
		if bound || current.Primary {
			return change, events.ErrConflict
		}
		_, err = tx.Exec(ctx, `UPDATE nodes SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1::uuid`, id)
		if err != nil {
			return change, err
		}
		change.Before = current
		return change, nil
	case "crm.contact_updated":
		before, err := snapshotAs[ContactRecord](e.Before)
		if err != nil {
			return change, err
		}
		expected, err := snapshotAs[ContactRecord](e.After)
		if err != nil {
			return change, err
		}
		current, err := contact(ctx, tx, id, true)
		if err != nil || !matches(current, expected) {
			return change, events.ErrConflict
		}
		if err = restoreContactFields(ctx, tx, id, before); err != nil {
			return change, err
		}
		after, err := contact(ctx, tx, id, false)
		if err != nil {
			return change, err
		}
		change.Before = current
		change.After = after
		return change, nil
	case "crm.contact_deleted":
		before, err := snapshotAs[ContactRecord](e.Before)
		if err != nil {
			return change, err
		}
		var rev int64
		var deleted bool
		err = tx.QueryRow(ctx, `SELECT c.revision,n.deleted_at IS NOT NULL FROM crm_contact_profiles c JOIN nodes n ON n.tenant_id=c.tenant_id AND n.id=c.contact_node_id WHERE c.contact_node_id=$1::uuid FOR UPDATE OF c,n`, id).Scan(&rev, &deleted)
		if err != nil || !deleted || rev != before.Revision {
			return change, events.ErrConflict
		}
		_, err = tx.Exec(ctx, `UPDATE nodes SET deleted_at=NULL,updated_at=clock_timestamp() WHERE id=$1::uuid`, id)
		if err != nil {
			return change, err
		}
		after, err := contact(ctx, tx, id, false)
		if err != nil {
			return change, err
		}
		change.After = after
		return change, nil
	default:
		return change, events.ErrConflict
	}
}
func (m *module) undoPrimary(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	change := events.Change{Type: e.Type}
	if err := m.undoAuthority(ctx, tx, p, fence.PermNodesContribute, "crm.write"); err != nil {
		return change, err
	}
	id, err := undoID(e)
	if err != nil {
		return change, err
	}
	change.NodeID = &id
	before, err := snapshotAs[Customer](e.Before)
	if err != nil {
		return change, err
	}
	expected, err := snapshotAs[Customer](e.After)
	if err != nil {
		return change, err
	}
	current, err := customer(ctx, tx, id, true)
	if err != nil || !matches(current, expected) {
		return change, events.ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE crm_organisation_profiles SET primary_contact_node_id=$1::uuid,revision=revision+1 WHERE organisation_node_id=$2::uuid`, before.PrimaryContactNodeID, id)
	if err != nil {
		return change, err
	}
	after, err := customer(ctx, tx, id, false)
	if err != nil {
		return change, err
	}
	change.Before = current
	change.After = after
	return change, nil
}
func (m *module) undoNumber(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	change := events.Change{Type: e.Type}
	if err := m.undoAuthority(ctx, tx, p, fence.PermNodesContribute, "crm.write"); err != nil {
		return change, err
	}
	id, err := undoID(e)
	if err != nil {
		return change, err
	}
	change.NodeID = &id
	if err = liveNode(ctx, tx, id, "organisation", true); err != nil {
		return change, events.ErrConflict
	}
	var after struct {
		CustomerNo string `json:"customer_no"`
	}
	if json.Unmarshal(e.After, &after) != nil || after.CustomerNo == "" {
		return change, events.ErrConflict
	}
	var current string
	err = tx.QueryRow(ctx, `SELECT customer_no FROM crm_customer_numbers WHERE organisation_node_id=$1::uuid FOR UPDATE`, id).Scan(&current)
	if err != nil || current != after.CustomerNo {
		return change, events.ErrConflict
	}
	rows, err := tx.Query(ctx, `SELECT quote_node_id::text,state,revision FROM business_quotes WHERE customer_org_node_id=$1::uuid ORDER BY quote_node_id FOR UPDATE`, id)
	if err != nil {
		return change, err
	}
	type quote struct {
		id, state string
		revision  int64
	}
	var quotes []quote
	for rows.Next() {
		var q quote
		if err = rows.Scan(&q.id, &q.state, &q.revision); err != nil {
			break
		}
		quotes = append(quotes, q)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return change, err
	}
	for _, q := range quotes {
		if q.state != "draft" {
			return change, events.ErrConflict
		}
	}
	if e.Type == "crm.customer_number_allocated" {
		if len(quotes) > 0 {
			return change, events.ErrConflict
		}
		_, err = tx.Exec(ctx, `DELETE FROM crm_customer_numbers WHERE organisation_node_id=$1::uuid`, id)
		if err != nil {
			return change, err
		}
		change.Before = map[string]string{"customer_no": current}
		return change, nil
	}
	var before struct {
		CustomerNo string `json:"customer_no"`
	}
	if json.Unmarshal(e.Before, &before) != nil || before.CustomerNo == "" {
		return change, events.ErrConflict
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('aeon.crm_undo','on',true)`); err != nil {
		return change, err
	}
	_, err = tx.Exec(ctx, `UPDATE crm_customer_numbers SET customer_no=$1,provenance='imported' WHERE organisation_node_id=$2::uuid`, before.CustomerNo, id)
	if err != nil {
		return change, err
	}
	var changes []events.Change
	for _, q := range quotes {
		var draftRev int64
		err = tx.QueryRow(ctx, `UPDATE quote_drafts SET document=jsonb_set(document,'{recipient,customer_no}',to_jsonb($1::text),true),draft_revision=draft_revision+1,updated_at=clock_timestamp(),updated_by_principal_id=$2::uuid WHERE quote_node_id=$3::uuid RETURNING draft_revision`, before.CustomerNo, p.ID, q.id).Scan(&draftRev)
		if err != nil && err != pgx.ErrNoRows {
			return change, err
		}
		if _, err = tx.Exec(ctx, `UPDATE business_quotes SET revision=revision+1 WHERE quote_node_id=$1::uuid`, q.id); err != nil {
			return change, err
		}
		changes = append(changes, events.Change{NodeID: &q.id, Type: "quote.customer_number_changed", Before: map[string]any{"revision": q.revision}, After: map[string]any{"revision": q.revision + 1, "draft_revision": draftRev}})
	}
	for _, item := range changes {
		if _, err = events.Append(ctx, tx, p, item); err != nil {
			return change, err
		}
	}
	change.Before = map[string]string{"customer_no": current}
	change.After = map[string]string{"customer_no": before.CustomerNo}
	return change, nil
}
func (m *module) undoProjectCustomer(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	change := events.Change{Type: e.Type}
	if err := m.undoAuthority(ctx, tx, p, fence.PermNodesContribute, "crm.write"); err != nil {
		return change, err
	}
	id, err := undoID(e)
	if err != nil {
		return change, err
	}
	change.NodeID = &id
	if err = liveNode(ctx, tx, id, "project", true); err != nil {
		return change, events.ErrConflict
	}
	var after map[string]string
	if json.Unmarshal(e.After, &after) != nil || after["project_node_id"] != id {
		return change, events.ErrConflict
	}
	org := after["organisation_node_id"]
	var current string
	err = tx.QueryRow(ctx, `SELECT source_node_id::text FROM node_relations WHERE target_node_id=$1::uuid AND type='customer_of' FOR UPDATE`, id).Scan(&current)
	if err != nil || current != org {
		return change, events.ErrConflict
	}
	var quotes bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM business_quotes WHERE project_node_id=$1::uuid)`, id).Scan(&quotes)
	if err != nil {
		return change, err
	}
	if quotes {
		return change, events.ErrConflict
	}
	var old *string
	if len(e.Before) > 0 {
		var v string
		if json.Unmarshal(e.Before, &v) != nil {
			return change, events.ErrConflict
		}
		old = &v
	}
	_, err = tx.Exec(ctx, `DELETE FROM node_relations WHERE target_node_id=$1::uuid AND type='customer_of'`, id)
	if err != nil {
		return change, err
	}
	if old != nil {
		if err = liveNode(ctx, tx, *old, "organisation", false); err != nil {
			return change, events.ErrConflict
		}
		_, err = tx.Exec(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1::uuid,$2::uuid,$3::uuid,'customer_of')`, p.TenantID, *old, id)
		if err != nil {
			return change, err
		}
	}
	change.Before = after
	change.After = old
	return change, nil
}
func (m *module) undoDocument(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	change := events.Change{Type: e.Type}
	if err := m.undoAuthority(ctx, tx, p, fence.PermNodesContribute, "crm.write"); err != nil {
		return change, err
	}
	expected, err := snapshotAs[DocumentMetadata](e.After)
	if err != nil || expected.AttachmentID == "" {
		return change, events.ErrConflict
	}
	var current DocumentMetadata
	err = tx.QueryRow(ctx, `SELECT title,category,status,valid_from::text,valid_until::text,revision FROM crm_document_metadata WHERE attachment_id=$1::uuid FOR UPDATE`, expected.AttachmentID).Scan(&current.Title, &current.Category, &current.Status, &current.ValidFrom, &current.ValidUntil, &current.Revision)
	if err != nil {
		return change, events.ErrConflict
	}
	current.AttachmentID = expected.AttachmentID
	expected.ExpectedRevision = 0
	if !matches(current, expected) {
		return change, events.ErrConflict
	}
	if len(e.Before) == 0 {
		_, err = tx.Exec(ctx, `DELETE FROM crm_document_metadata WHERE attachment_id=$1::uuid`, current.AttachmentID)
		if err != nil {
			return change, err
		}
		change.Before = current
		return change, nil
	}
	before, err := snapshotAs[DocumentMetadata](e.Before)
	if err != nil {
		return change, err
	}
	var rev int64
	err = tx.QueryRow(ctx, `UPDATE crm_document_metadata SET title=$1,category=$2,status=$3,valid_from=$4::date,valid_until=$5::date,revision=revision+1 WHERE attachment_id=$6::uuid RETURNING revision`, before.Title, before.Category, before.Status, before.ValidFrom, before.ValidUntil, current.AttachmentID).Scan(&rev)
	if err != nil {
		return change, err
	}
	before.Revision = rev
	change.Before = current
	change.After = before
	return change, nil
}
func (m *module) undoCooperation(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	change := events.Change{Type: e.Type}
	if err := m.undoAuthority(ctx, tx, p, fence.PermNodesContribute, "crm.write"); err != nil {
		return change, err
	}
	id, err := undoID(e)
	if err != nil {
		return change, err
	}
	change.NodeID = &id
	expected, err := snapshotAs[Cooperation](e.After)
	if err != nil {
		return change, err
	}
	var raw []byte
	var revision int64
	err = tx.QueryRow(ctx, `SELECT data,revision FROM crm_project_cooperation WHERE project_node_id=$1::uuid FOR UPDATE`, id).Scan(&raw, &revision)
	if err != nil || revision != expected.Revision {
		return change, events.ErrConflict
	}
	var current Cooperation
	if json.Unmarshal(raw, &current) != nil {
		return change, events.ErrConflict
	}
	current.Revision = revision
	current.ExpectedRevision = 0
	expected.ExpectedRevision = 0
	if !matches(current, expected) {
		return change, events.ErrConflict
	}
	if len(e.Before) == 0 {
		_, err = tx.Exec(ctx, `DELETE FROM crm_project_cooperation WHERE project_node_id=$1::uuid`, id)
		if err != nil {
			return change, err
		}
		change.Before = current
		return change, nil
	}
	var before Cooperation
	if json.Unmarshal(e.Before, &before) != nil {
		return change, events.ErrConflict
	}
	var next int64
	err = tx.QueryRow(ctx, `UPDATE crm_project_cooperation SET data=$1::jsonb,revision=revision+1 WHERE project_node_id=$2::uuid RETURNING revision`, e.Before, id).Scan(&next)
	if err != nil {
		return change, err
	}
	before.Revision = next
	change.Before = current
	change.After = before
	return change, nil
}
func (m *module) undoDraft(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	change := events.Change{Type: e.Type}
	if err := m.undoAuthority(ctx, tx, p, fence.PermNodesContribute, "crm.manage"); err != nil {
		return change, err
	}
	id, err := undoID(e)
	if err != nil {
		return change, err
	}
	change.NodeID = &id
	var after struct {
		DraftID      string `json:"draft_id"`
		BaseRevision int64  `json:"base_revision"`
	}
	if json.Unmarshal(e.After, &after) != nil || after.DraftID == "" {
		return change, events.ErrConflict
	}
	var revision int64
	var applied bool
	err = tx.QueryRow(ctx, `SELECT base_revision,applied_at IS NOT NULL FROM crm_note_rewrite_drafts WHERE id=$1::uuid AND organisation_node_id=$2::uuid FOR UPDATE`, after.DraftID, id).Scan(&revision, &applied)
	if err != nil || applied || revision != after.BaseRevision {
		return change, events.ErrConflict
	}
	_, err = tx.Exec(ctx, `DELETE FROM crm_note_rewrite_drafts WHERE id=$1::uuid`, after.DraftID)
	if err != nil {
		return change, err
	}
	change.Before = after
	return change, nil
}
func (m *module) undoProviderConfig(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	change := events.Change{Type: e.Type}
	if err := m.undoAuthority(ctx, tx, p, fence.PermIntegrationsCall, "crm.manage"); err != nil {
		return change, err
	}
	after, err := snapshotAs[providerConfig](e.After)
	if err != nil {
		return change, err
	}
	var enabled bool
	var ref string
	var revision int64
	err = tx.QueryRow(ctx, `SELECT enabled,secret_ref,revision FROM crm_provider_configs WHERE provider_id=$1 FOR UPDATE`, after.ID).Scan(&enabled, &ref, &revision)
	if err != nil || enabled != after.Enabled || (ref != "") != after.Configured || revision != after.Revision {
		return change, events.ErrConflict
	}
	var prevEnabled *bool
	var prevRef *string
	var prevRevision *int64
	err = tx.QueryRow(ctx, `SELECT previous_enabled,previous_secret_ref,previous_revision FROM crm_provider_config_history WHERE event_id=$1 AND provider_id=$2`, e.ID, after.ID).Scan(&prevEnabled, &prevRef, &prevRevision)
	if err != nil {
		return change, events.ErrConflict
	}
	if prevRevision == nil {
		_, err = tx.Exec(ctx, `DELETE FROM crm_provider_configs WHERE provider_id=$1`, after.ID)
		if err != nil {
			return change, err
		}
		change.Before = after
		return change, nil
	}
	var next int64
	err = tx.QueryRow(ctx, `UPDATE crm_provider_configs SET enabled=$1,secret_ref=$2,revision=revision+1 WHERE provider_id=$3 RETURNING revision`, *prevEnabled, *prevRef, after.ID).Scan(&next)
	if err != nil {
		return change, err
	}
	change.Before = after
	change.After = providerConfig{ID: after.ID, Enabled: *prevEnabled, Configured: *prevRef != "", Revision: next}
	return change, nil
}
func (m *module) undoBinding(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	change := events.Change{Type: e.Type}
	if err := m.undoAuthority(ctx, tx, p, fence.PermStepsApply, "crm.manage"); err != nil {
		return change, err
	}
	expected, err := snapshotAs[Binding](e.After)
	if err != nil {
		return change, err
	}
	current, err := lockBinding(ctx, tx, p.TenantID, expected.ContactNodeID, expected.PrincipalID)
	if err != nil || !matches(current, expected) {
		return change, events.ErrConflict
	}
	_, err = tx.Exec(ctx, `DELETE FROM crm_contact_principals WHERE contact_node_id=$1::uuid AND principal_id=$2::uuid`, expected.ContactNodeID, expected.PrincipalID)
	if err != nil {
		return change, err
	}
	change.NodeID = &expected.ContactNodeID
	change.Before = current
	return change, nil
}

// undoVisibility restores the archived state a customer had before, while
// nothing else changed it since (QL1/AEON-109).
func (m *module) undoVisibility(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	change := events.Change{Type: e.Type}
	if err := m.undoAuthority(ctx, tx, p, fence.PermNodesContribute, "crm.write"); err != nil {
		return change, err
	}
	id, err := undoID(e)
	if err != nil {
		return change, err
	}
	change.NodeID = &id
	before, err := snapshotAs[Customer](e.Before)
	if err != nil {
		return change, err
	}
	after, err := snapshotAs[Customer](e.After)
	if err != nil {
		return change, err
	}
	current, err := customer(ctx, tx, id, true)
	if err != nil || current.Revision != after.Revision || current.Archived != after.Archived {
		return change, events.ErrConflict
	}
	if err = setCustomerArchived(ctx, tx, p, id, before.Archived); err != nil {
		return change, err
	}
	restored, err := customer(ctx, tx, id, false)
	if err != nil {
		return change, err
	}
	change.Before, change.After = current, restored
	return change, nil
}
