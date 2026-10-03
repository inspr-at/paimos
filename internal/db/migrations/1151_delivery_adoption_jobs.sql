-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-596 P1. Private, instance-local checkpoints; no job is queued here.
-- P3 owns CAS fencing, pre-effect intents, provider reconciliation and quotas.
SET LOCAL lock_timeout = '5s';

CREATE TABLE delivery_adoption_jobs (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    project_node_id uuid NOT NULL,
    instance_id text NOT NULL CHECK (octet_length(instance_id) BETWEEN 1 AND 128),
    migration_version text NOT NULL DEFAULT 'releases-v1' CHECK (migration_version = 'releases-v1'),
    rollout_artifact_ref text NOT NULL CHECK (length(rollout_artifact_ref) BETWEEN 1 AND 512),
    executing_principal_id uuid NOT NULL,
    authorizing_principal_id uuid NOT NULL,
    rollout_authorization_ref text NOT NULL CHECK (length(rollout_authorization_ref) BETWEEN 1 AND 512),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'checking', 'backing_up', 'applying', 'adopted', 'refused', 'retry_wait')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision >= 1),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    attempt_id uuid,
    attempt_started_at timestamptz,
    attempt_deadline_at timestamptz,
    checking_at timestamptz,
    backing_up_at timestamptz,
    applying_at timestamptz,
    adopted_at timestamptz,
    refused_at timestamptz,
    last_checked_at timestamptz,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    reason_code text CHECK (octet_length(reason_code) BETWEEN 1 AND 64),
    reason_message text NOT NULL DEFAULT '' CHECK (octet_length(reason_message) <= 2048),
    lease_token uuid,
    lease_generation bigint NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
    lease_until timestamptz,
    source_fingerprint text CHECK (source_fingerprint ~ '^[0-9a-f]{64}$'),
    report_ref text CHECK (length(report_ref) BETWEEN 1 AND 512),
    report_digest text CHECK (report_digest ~ '^[0-9a-f]{64}$'),
    report_counts jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (
        jsonb_typeof(report_counts) = 'object' AND octet_length(report_counts::text) <= 2048),
    report_incomplete boolean NOT NULL DEFAULT false,
    backup_ref text CHECK (length(backup_ref) BETWEEN 1 AND 512),
    backup_digest text CHECK (backup_digest ~ '^[0-9a-f]{64}$'),
    backup_verified_at timestamptz,
    restore_evidence_ref text CHECK (length(restore_evidence_ref) BETWEEN 1 AND 512),
    restore_evidence_digest text CHECK (restore_evidence_digest ~ '^[0-9a-f]{64}$'),
    recovery_pin_manifest_ref text CHECK (length(recovery_pin_manifest_ref) BETWEEN 1 AND 512),
    evidence_attempt_id uuid,
    failed_report_ref text CHECK (length(failed_report_ref) BETWEEN 1 AND 512),
    failed_report_digest text CHECK (failed_report_digest ~ '^[0-9a-f]{64}$'),
    failed_attempt_id uuid,
    failure_summary text NOT NULL DEFAULT '' CHECK (octet_length(failure_summary) <= 2048),
    resource_attempt_id uuid,
    reserved_backup_bytes bigint NOT NULL DEFAULT 0 CHECK (reserved_backup_bytes >= 0),
    reserved_restore_slots smallint NOT NULL DEFAULT 0 CHECK (reserved_restore_slots BETWEEN 0 AND 1),
    restore_expires_at timestamptz,
    failed_resources_expires_at timestamptz,
    cleanup_state text NOT NULL DEFAULT 'none' CHECK (cleanup_state IN ('none', 'pending', 'checking', 'blocked', 'reclaimed')),
    cleanup_attempts integer NOT NULL DEFAULT 0 CHECK (cleanup_attempts >= 0),
    next_reconcile_at timestamptz,
    reconciliation_cursor text CHECK (length(reconciliation_cursor) <= 512),
    operation_journal jsonb NOT NULL DEFAULT '{"operations":[]}'::jsonb CHECK ((
        octet_length(operation_journal::text) <= 16384
        AND jsonb_typeof(operation_journal) = 'object'
        AND operation_journal - 'operations' = '{}'::jsonb
        AND jsonb_typeof(operation_journal->'operations') = 'array'
        AND CASE WHEN jsonb_typeof(operation_journal->'operations') = 'array'
            THEN jsonb_array_length(operation_journal->'operations') <= 8 ELSE false END
        AND NOT jsonb_path_exists(operation_journal, '$.operations[*] ? (@.type() != "object")')
        AND NOT jsonb_path_exists(operation_journal, '$.operations[*] ? (!exists(@.operation_key) || !exists(@.attempt_id) || !exists(@.kind) || !exists(@.status))')
        AND NOT jsonb_path_exists(operation_journal, '$.operations[*].keyvalue() ? (
            @.key != "operation_key" && @.key != "attempt_id" && @.key != "kind" && @.key != "status"
            && @.key != "provider_handle" && @.key != "ownership_labels" && @.key != "reserved_bytes"
            && @.key != "reserved_restore_slots" && @.key != "deadline" && @.key != "cleanup_state"
            && @.key != "next_check_at" && @.key != "recovery_pin_manifest_ref")')) IS TRUE),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, project_node_id),
    FOREIGN KEY (tenant_id, project_node_id) REFERENCES nodes(tenant_id, id),
    FOREIGN KEY (tenant_id, executing_principal_id) REFERENCES principals(tenant_id, id),
    FOREIGN KEY (tenant_id, authorizing_principal_id) REFERENCES principals(tenant_id, id),
    CONSTRAINT delivery_adoption_jobs_lease_pair CHECK ((lease_token IS NULL) = (lease_until IS NULL)),
    CONSTRAINT delivery_adoption_jobs_leased_generation CHECK (lease_token IS NULL OR lease_generation > 0),
    CHECK ((attempt_id IS NULL) = (attempt_started_at IS NULL)),
    CHECK ((attempt_id IS NULL) = (attempt_deadline_at IS NULL)),
    CONSTRAINT delivery_adoption_jobs_attempt_deadline CHECK (attempt_deadline_at IS NULL OR (attempt_deadline_at > attempt_started_at
        AND attempt_deadline_at <= attempt_started_at + interval '15 minutes')),
    CHECK ((report_ref IS NULL) = (report_digest IS NULL)),
    CHECK ((backup_ref IS NULL) = (backup_digest IS NULL)),
    CHECK ((restore_evidence_ref IS NULL) = (restore_evidence_digest IS NULL)),
    CHECK ((failed_report_ref IS NULL) = (failed_report_digest IS NULL)),
    CONSTRAINT delivery_adoption_jobs_backup_verification CHECK (backup_verified_at IS NULL OR (backup_ref IS NOT NULL AND restore_evidence_ref IS NOT NULL
        AND source_fingerprint IS NOT NULL AND evidence_attempt_id IS NOT NULL)),
    CONSTRAINT delivery_adoption_jobs_recovery_proof CHECK (state NOT IN ('applying', 'adopted') OR (backup_verified_at IS NOT NULL
        AND report_ref IS NOT NULL AND NOT report_incomplete AND recovery_pin_manifest_ref IS NOT NULL
        AND attempt_id IS NOT NULL AND evidence_attempt_id = attempt_id)),
    CONSTRAINT delivery_adoption_jobs_adopted_time CHECK (state <> 'adopted' OR adopted_at IS NOT NULL),
    CONSTRAINT delivery_adoption_jobs_resource_attempt CHECK ((reserved_backup_bytes = 0 AND reserved_restore_slots = 0) OR resource_attempt_id IS NOT NULL)
);
CREATE INDEX delivery_adoption_jobs_claim_idx ON delivery_adoption_jobs(tenant_id, next_attempt_at, project_node_id)
    WHERE state IN ('pending', 'retry_wait');
CREATE INDEX delivery_adoption_jobs_lease_idx ON delivery_adoption_jobs(tenant_id, lease_until, project_node_id)
    WHERE lease_until IS NOT NULL;
-- Reconciliation remains claimable for terminal jobs, including adopted ones.
CREATE INDEX delivery_adoption_jobs_reconcile_idx ON delivery_adoption_jobs(tenant_id, next_reconcile_at, project_node_id)
    WHERE next_reconcile_at IS NOT NULL;
ALTER TABLE delivery_adoption_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_adoption_jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_adoption_jobs_tenant ON delivery_adoption_jobs
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY delivery_adoption_jobs_project_visibility ON delivery_adoption_jobs AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    WITH CHECK ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]));
