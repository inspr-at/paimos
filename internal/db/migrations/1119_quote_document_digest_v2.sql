-- SPDX-License-Identifier: AGPL-3.0-only
-- New documents use one canonical digest across native and import boundaries.
-- Issued v1 rows, snapshots, links and acceptance evidence remain immutable.
ALTER TABLE quote_versions DROP CONSTRAINT quote_versions_digest_mode_check;
ALTER TABLE quote_versions ADD CONSTRAINT quote_versions_digest_mode_check
    CHECK (digest_mode IN ('r4-v1','document-v1','document-v2'));

CREATE OR REPLACE FUNCTION aeon_guard_quote_issue() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM quote_versions v JOIN business_quotes q ON q.tenant_id=v.tenant_id AND q.quote_node_id=v.quote_node_id
        WHERE v.tenant_id=NEW.tenant_id AND v.quote_node_id=NEW.quote_node_id AND v.version=NEW.version
          AND q.current_version=v.version AND q.state='draft'
          AND EXISTS (SELECT 1 FROM events e WHERE e.tenant_id=NEW.tenant_id AND e.id=NEW.event_id AND e.type='quote.issued' AND e.actor_principal_id=NEW.issued_by_principal_id)
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

CREATE OR REPLACE FUNCTION aeon_queue_quote_confirmation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  -- Historical imports and R4 rate-only versions have no native document
  -- receipt to render. Never synthesize one or queue email from import data.
  IF NOT EXISTS (
    SELECT 1 FROM events e JOIN quote_versions v
      ON v.tenant_id=e.tenant_id AND v.quote_node_id=NEW.quote_node_id AND v.version=NEW.version
    JOIN quote_version_snapshots s
      ON s.tenant_id=v.tenant_id AND s.quote_node_id=v.quote_node_id AND s.version=v.version
    WHERE e.tenant_id=NEW.tenant_id AND e.id=NEW.event_id
      AND e.type IN ('quote.accepted','quote.accepted_public')
      AND v.digest_mode IN ('document-v1','document-v2')
  ) THEN RETURN NEW; END IF;
  INSERT INTO quote_confirmation_jobs(tenant_id,quote_node_id,version,acceptance_event_id,queued_event_id,recipient_snapshot)
   SELECT NEW.tenant_id,NEW.quote_node_id,NEW.version,NEW.event_id,NEW.event_id,s.recipient
   FROM quote_version_snapshots s
   WHERE s.tenant_id=NEW.tenant_id AND s.quote_node_id=NEW.quote_node_id AND s.version=NEW.version;
  RETURN NEW;
END;
$$;
