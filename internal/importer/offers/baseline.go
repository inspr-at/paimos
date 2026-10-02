// SPDX-License-Identifier: AGPL-3.0-only
package offers

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Baselines live in append-only import events, so they cannot be advanced by a
// native writer. Historical imports without one fail closed; an operator must
// reconcile the original source and native edits before starting a new mapping.
func checkNativeBaseline(ctx context.Context, tx pgx.Tx, id, kind string) error {
	var baseline []byte
	err := tx.QueryRow(ctx, `SELECT after->'native_baseline' FROM events WHERE node_id=$1::uuid AND type=$2 ORDER BY id DESC LIMIT 1`, id, "import."+kind).Scan(&baseline)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && len(baseline) == 0 {
		return fmt.Errorf("import conflict: %s has no verified native baseline", kind)
	}
	if err != nil {
		return err
	}
	current, err := nativeSnapshot(ctx, tx, id, kind)
	if err != nil {
		return err
	}
	var equal bool
	if err := tx.QueryRow(ctx, `SELECT $1::jsonb=$2::jsonb`, string(baseline), string(current)).Scan(&equal); err != nil {
		return err
	}
	if !equal {
		return fmt.Errorf("import conflict: native %s changed since last import", kind)
	}
	return nil
}

func nativeSnapshot(ctx context.Context, tx pgx.Tx, id, kind string) ([]byte, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT jsonb_build_object(
	 'node',jsonb_build_object('title',n.title,'body',n.body,'state',n.state,'kind_id',n.kind_id,'deleted_at',n.deleted_at,'fields',n.fields-'provenance'),
	 'organisation',(SELECT to_jsonb(o) FROM crm_organisation_profiles o WHERE o.organisation_node_id=n.id),
	 'contact',(SELECT to_jsonb(c) FROM crm_contact_profiles c WHERE c.contact_node_id=n.id),
	 'quote',(SELECT to_jsonb(q) FROM business_quotes q WHERE q.quote_node_id=n.id),
	 'draft',(SELECT to_jsonb(d) FROM quote_drafts d WHERE d.quote_node_id=n.id))
	 FROM nodes n WHERE n.id=$1::uuid`, id).Scan(&raw)
	return raw, err
}

func lockMappedRecords(ctx context.Context, tx pgx.Tx, tenantID, instance string) error {
	// Drain each lock batch before starting the next query.
	for _, query := range []string{
		`SELECT n.id FROM nodes n JOIN paimos_offer_imports i ON i.tenant_id=n.tenant_id AND i.node_id=n.id WHERE i.tenant_id=$1::uuid AND i.source_instance=$2 ORDER BY n.id FOR UPDATE OF n`,
		`SELECT o.organisation_node_id FROM crm_organisation_profiles o JOIN paimos_offer_imports i ON i.tenant_id=o.tenant_id AND i.node_id=o.organisation_node_id WHERE i.tenant_id=$1::uuid AND i.source_instance=$2 ORDER BY o.organisation_node_id FOR UPDATE OF o`,
		`SELECT c.contact_node_id FROM crm_contact_profiles c JOIN paimos_offer_imports i ON i.tenant_id=c.tenant_id AND i.node_id=c.contact_node_id WHERE i.tenant_id=$1::uuid AND i.source_instance=$2 ORDER BY c.contact_node_id FOR UPDATE OF c`,
		`SELECT q.quote_node_id FROM business_quotes q JOIN paimos_offer_imports i ON i.tenant_id=q.tenant_id AND i.node_id=q.quote_node_id WHERE i.tenant_id=$1::uuid AND i.source_instance=$2 ORDER BY q.quote_node_id FOR UPDATE OF q`,
		`SELECT d.quote_node_id FROM quote_drafts d JOIN paimos_offer_imports i ON i.tenant_id=d.tenant_id AND i.node_id=d.quote_node_id WHERE i.tenant_id=$1::uuid AND i.source_instance=$2 ORDER BY d.quote_node_id FOR UPDATE OF d`,
	} {
		rows, err := tx.Query(ctx, query, tenantID, instance)
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func customerSkipped(mappings []Mapping, id string) bool {
	for _, mapping := range mappings {
		if mapping.SourceKind == "customer" && mapping.NodeID == id {
			return mapping.Action == "skip"
		}
	}
	return false
}
