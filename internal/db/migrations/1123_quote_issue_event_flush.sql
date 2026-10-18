-- SPDX-License-Identifier: AGPL-3.0-only
-- Keep resource validation at INSERT, but validate event linkage at COMMIT so
-- imports can finish all quote writes before taking the tenant event counter.
ALTER TABLE quote_issues ALTER CONSTRAINT quote_issues_tenant_id_event_id_fkey
    DEFERRABLE INITIALLY DEFERRED;

CREATE OR REPLACE FUNCTION aeon_guard_quote_issue() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM quote_versions v JOIN business_quotes q ON q.tenant_id=v.tenant_id AND q.quote_node_id=v.quote_node_id
        WHERE v.tenant_id=NEW.tenant_id AND v.quote_node_id=NEW.quote_node_id AND v.version=NEW.version
          AND q.current_version=v.version AND q.state='draft'
          AND ((v.digest_mode='r4-v1' AND EXISTS (SELECT 1 FROM quote_line_items l WHERE l.tenant_id=v.tenant_id AND l.quote_node_id=v.quote_node_id AND l.version=v.version)
                AND v.subtotal=(SELECT coalesce(sum(l.net_amount),0) FROM quote_line_items l WHERE l.tenant_id=v.tenant_id AND l.quote_node_id=v.quote_node_id AND l.version=v.version)
                AND v.tax_total=(SELECT coalesce(sum(round(l.net_amount*l.tax_rate,4)),0) FROM quote_line_items l WHERE l.tenant_id=v.tenant_id AND l.quote_node_id=v.quote_node_id AND l.version=v.version))
            OR (v.digest_mode IN ('document-v1','document-v2') AND EXISTS (SELECT 1 FROM quote_version_snapshots s WHERE s.tenant_id=v.tenant_id AND s.quote_node_id=v.quote_node_id AND s.version=v.version)
                AND EXISTS (SELECT 1 FROM quote_document_lines l WHERE l.tenant_id=v.tenant_id AND l.quote_node_id=v.quote_node_id AND l.version=v.version)
                AND v.subtotal=(SELECT coalesce(sum(l.net_amount),0) FROM quote_document_lines l WHERE l.tenant_id=v.tenant_id AND l.quote_node_id=v.quote_node_id AND l.version=v.version)
                AND v.tax_total=0))
    ) THEN RAISE EXCEPTION 'issue requires a complete current quote version'; END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION aeon_guard_quote_issue_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM events e
        WHERE e.tenant_id=NEW.tenant_id AND e.id=NEW.event_id
          AND e.type='quote.issued' AND e.actor_principal_id=NEW.issued_by_principal_id
    ) THEN RAISE EXCEPTION 'issue requires its matching quote.issued event'; END IF;
    RETURN NEW;
END;
$$;
CREATE CONSTRAINT TRIGGER quote_issues_event_guard
    AFTER INSERT ON quote_issues DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION aeon_guard_quote_issue_event();
