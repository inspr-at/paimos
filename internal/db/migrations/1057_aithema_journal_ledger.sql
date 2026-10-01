-- SPDX-License-Identifier: AGPL-3.0-only
-- Plugin-owned immutable journal, authority/generation projection and ledger.
SET LOCAL lock_timeout = '5s';

CREATE TABLE aithema_sessions (
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    sid uuid NOT NULL,
    project_id text NOT NULL,
    plugin_principal text NOT NULL,
    authorization_bytes bytea NOT NULL,
    worker_generation bigint NOT NULL CHECK (worker_generation BETWEEN 1 AND 9007199254740991),
    auth_epoch bigint NOT NULL CHECK (auth_epoch BETWEEN 1 AND 9007199254740991),
    tombstone boolean NOT NULL DEFAULT false,
    suspended boolean NOT NULL DEFAULT false,
    host_mode text NOT NULL CHECK (host_mode IN ('review', 'working_spec_only')),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    evidence boolean NOT NULL,
    local_lanes text[] NOT NULL DEFAULT ARRAY[]::text[],
    session_cap bigint NOT NULL CHECK (session_cap BETWEEN 0 AND 9007199254740991),
    seq bigint NOT NULL DEFAULT 0 CHECK (seq BETWEEN 0 AND 9007199254740991),
    consumed_seq bigint NOT NULL DEFAULT 0 CHECK (consumed_seq BETWEEN 0 AND seq),
    working_rev bigint NOT NULL DEFAULT 0 CHECK (working_rev BETWEEN 0 AND 9007199254740991),
    snapshot_seq bigint,
    PRIMARY KEY (tenant_id, sid)
);
CREATE TABLE aithema_journal_records (
    tenant_id uuid NOT NULL,
    sid uuid NOT NULL,
    seq bigint NOT NULL CHECK (seq BETWEEN 1 AND 9007199254740991),
    client_event_id uuid NOT NULL,
    contract text NOT NULL,
    kind text NOT NULL,
    original_bytes bytea NOT NULL CHECK (octet_length(original_bytes) <= 1048576),
    document bytea NOT NULL,
    PRIMARY KEY (tenant_id, sid, seq),
    UNIQUE (tenant_id, sid, client_event_id),
    FOREIGN KEY (tenant_id, sid) REFERENCES aithema_sessions(tenant_id, sid) ON DELETE CASCADE
);
CREATE TABLE aithema_budget_policy (
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    currency text NOT NULL,
    principal_day_cap bigint NOT NULL CHECK (principal_day_cap BETWEEN 0 AND 9007199254740991),
    tenant_day_cap bigint NOT NULL CHECK (tenant_day_cap BETWEEN 0 AND 9007199254740991),
    PRIMARY KEY (tenant_id, currency)
);
CREATE TABLE aithema_budget_holds (
    tenant_id uuid NOT NULL,
    sid uuid NOT NULL,
    attempt_id text NOT NULL,
    hold_id uuid NOT NULL,
    hold_order bigint GENERATED ALWAYS AS IDENTITY,
    worker_generation bigint NOT NULL CHECK (worker_generation BETWEEN 1 AND 9007199254740991),
    auth_epoch bigint NOT NULL CHECK (auth_epoch BETWEEN 1 AND 9007199254740991),
    principal_id text NOT NULL,
    admission_day date NOT NULL,
    lane text NOT NULL CHECK (lane IN ('reaction', 'spec', 'design', 'stt', 'tts')),
    lane_kind text NOT NULL CHECK (lane_kind IN ('remote', 'operator_local')),
    max_micro bigint NOT NULL CHECK (max_micro BETWEEN 0 AND 9007199254740991),
    currency text NOT NULL,
    state text NOT NULL CHECK (state IN ('admitted', 'denied', 'closed')),
    denied_reason text,
    closed_reason text CHECK (closed_reason IN ('void', 'settled', 'unknown')),
    charged_micro bigint CHECK (charged_micro BETWEEN 0 AND max_micro),
    original_bytes bytea NOT NULL,
    verdict bytea NOT NULL,
    settlement_bytes bytea,
    created_at timestamptz NOT NULL,
    closed_at timestamptz,
    PRIMARY KEY (tenant_id, sid, attempt_id),
    UNIQUE (tenant_id, sid, hold_id),
    FOREIGN KEY (tenant_id, sid) REFERENCES aithema_sessions(tenant_id, sid) ON DELETE CASCADE,
    CHECK (max_micro > 0 OR lane_kind = 'operator_local'),
    CHECK ((state = 'closed') = (closed_reason IS NOT NULL AND charged_micro IS NOT NULL AND closed_at IS NOT NULL)),
    CHECK ((state = 'denied') = (denied_reason IS NOT NULL))
);
CREATE INDEX aithema_budget_open ON aithema_budget_holds(tenant_id, sid, hold_order) WHERE state = 'admitted';
CREATE INDEX aithema_budget_day ON aithema_budget_holds(tenant_id, currency, admission_day, principal_id) WHERE state <> 'denied';
CREATE TABLE aithema_budget_claims (
    tenant_id uuid NOT NULL,
    sid uuid NOT NULL,
    claim_id uuid NOT NULL,
    hold_id uuid NOT NULL,
    request_sha256 text NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
    worker_generation bigint NOT NULL CHECK (worker_generation BETWEEN 1 AND 9007199254740991),
    auth_epoch bigint NOT NULL CHECK (auth_epoch BETWEEN 1 AND 9007199254740991),
    state text NOT NULL CHECK (state IN ('claimed', 'settled', 'unknown')),
    settled_micro bigint CHECK (settled_micro BETWEEN 0 AND 9007199254740991),
    claimed_at timestamptz NOT NULL,
    settled_at timestamptz,
    PRIMARY KEY (tenant_id, sid, claim_id),
    UNIQUE (tenant_id, sid, hold_id),
    FOREIGN KEY (tenant_id, sid, hold_id) REFERENCES aithema_budget_holds(tenant_id, sid, hold_id) ON DELETE CASCADE,
    CHECK ((state = 'claimed') = (settled_at IS NULL AND settled_micro IS NULL))
);

ALTER TABLE aithema_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE aithema_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON aithema_sessions
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

ALTER TABLE aithema_journal_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE aithema_journal_records FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON aithema_journal_records
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

ALTER TABLE aithema_budget_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE aithema_budget_policy FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON aithema_budget_policy
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

ALTER TABLE aithema_budget_holds ENABLE ROW LEVEL SECURITY;
ALTER TABLE aithema_budget_holds FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON aithema_budget_holds
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

ALTER TABLE aithema_budget_claims ENABLE ROW LEVEL SECURITY;
ALTER TABLE aithema_budget_claims FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON aithema_budget_claims
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
