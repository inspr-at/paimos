-- SPDX-License-Identifier: AGPL-3.0-only
-- Fixed UTF-8 conversion of an immutable validated document is deterministic;
-- no locale, session setting or mutable table participates in this key.
CREATE FUNCTION aeon_budget_ack_key(raw bytea) RETURNS jsonb
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
 SELECT jsonb_build_array(d->'sid',d->'writer'->'kind',d->'writer'->'generation',
    d->'data'->'hold_id',d->'data'->'attempt_id',d->'data'->'lane',
    d->'data'->'max_micro',d->'data'->'currency',
    coalesce(nullif(d->'data'->>'lane_kind',''),'remote'))
 FROM (SELECT convert_from(raw,'UTF8')::jsonb AS d) doc
$$;
CREATE INDEX aithema_budget_ack_lookup
 ON aithema_journal_records(tenant_id,sid,aeon_budget_ack_key(document))
 WHERE kind='budget.hold';
