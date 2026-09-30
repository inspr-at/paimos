-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-432: budgets are independent of client capacity; old reports stay valid.
SET LOCAL lock_timeout = '5s';
ALTER TABLE harness_sessions
    DROP CONSTRAINT harness_sessions_max_session_file_bytes_check,
    ADD CHECK (max_session_file_bytes BETWEEN 2000 AND 512000);

ALTER TABLE rule_budget_settings
    DROP CONSTRAINT rule_budget_settings_max_bytes_check,
    DROP CONSTRAINT rule_budget_settings_company_bytes_check,
    DROP CONSTRAINT rule_budget_settings_project_bytes_check,
    DROP CONSTRAINT rule_budget_settings_person_bytes_check,
    DROP CONSTRAINT rule_budget_settings_agent_bytes_check,
    ADD CHECK (max_bytes BETWEEN 2000 AND 500000),
    ADD CHECK (company_bytes BETWEEN 500 AND 500000),
    ADD CHECK (project_bytes BETWEEN 500 AND 500000),
    ADD CHECK (person_bytes BETWEEN 500 AND 500000),
    ADD CHECK (agent_bytes BETWEEN 500 AND 500000);

ALTER TABLE rule_served_manifests
    DROP CONSTRAINT rule_served_manifests_byte_size_check,
    ADD CHECK (byte_size BETWEEN 1 AND 512000);
