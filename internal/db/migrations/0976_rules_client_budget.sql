-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-328: optional capability reports; legacy writers remain valid.
SET LOCAL lock_timeout = '5s';
ALTER TABLE harness_sessions
    ADD COLUMN max_session_file_bytes integer CHECK (max_session_file_bytes BETWEEN 2000 AND 64000),
    ADD COLUMN rules_client_version text CHECK (char_length(rules_client_version) BETWEEN 1 AND 80 AND rules_client_version !~ '[[:cntrl:]]'),
    ADD COLUMN rules_client_seen_at timestamptz;
CREATE INDEX harness_rules_client_activity ON harness_sessions
    (tenant_id, (greatest(created_at, heartbeat_at, rules_client_seen_at)));

ALTER TABLE rule_budget_settings
    DROP CONSTRAINT rule_budget_settings_max_bytes_check,
    DROP CONSTRAINT rule_budget_settings_company_bytes_check,
    DROP CONSTRAINT rule_budget_settings_project_bytes_check,
    DROP CONSTRAINT rule_budget_settings_person_bytes_check,
    DROP CONSTRAINT rule_budget_settings_agent_bytes_check,
    ADD CHECK (max_bytes BETWEEN 2000 AND 64000),
    ADD CHECK (company_bytes BETWEEN 500 AND 64000),
    ADD CHECK (project_bytes BETWEEN 500 AND 64000),
    ADD CHECK (person_bytes BETWEEN 500 AND 64000),
    ADD CHECK (agent_bytes BETWEEN 500 AND 64000);
ALTER TABLE rule_served_manifests
    DROP CONSTRAINT rule_served_manifests_byte_size_check,
    ADD CHECK (byte_size BETWEEN 1 AND 64000);
