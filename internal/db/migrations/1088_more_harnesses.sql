-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-452: widen existing authorities; retain every historical profile and RLS policy.
SET LOCAL lock_timeout = '5s';

ALTER TABLE model_profiles DROP CONSTRAINT model_profiles_harness_check;
ALTER TABLE model_profiles ADD CONSTRAINT model_profiles_harness_check
    CHECK (harness IN ('codex','claude','pi','cursor','grok','gemini','opencode'));
ALTER TABLE model_profiles DROP CONSTRAINT model_profiles_family_check;
ALTER TABLE model_profiles ADD CONSTRAINT model_profiles_family_check
    CHECK (family IN ('openai','anthropic','xai','cursor','google','local')
        OR (harness IN ('pi','opencode') AND family='unknown'));

ALTER TABLE agent_accounts DROP CONSTRAINT agent_accounts_harness_check;
ALTER TABLE agent_accounts ADD CONSTRAINT agent_accounts_harness_check
    CHECK (harness IN ('codex','claude','pi','cursor','grok','gemini','opencode'));
ALTER TABLE harness_sessions DROP CONSTRAINT harness_sessions_harness_check;
ALTER TABLE harness_sessions ADD CONSTRAINT harness_sessions_harness_check
    CHECK (harness IN ('codex','claude','pi','cursor','grok','gemini','opencode'));
ALTER TABLE account_groups DROP CONSTRAINT account_groups_harness_check;
ALTER TABLE account_groups ADD CONSTRAINT account_groups_harness_check
    CHECK (harness IN ('codex','claude','pi','cursor','grok','gemini','opencode'));
ALTER TABLE account_ticket_pins DROP CONSTRAINT account_ticket_pins_harness_check;
ALTER TABLE account_ticket_pins ADD CONSTRAINT account_ticket_pins_harness_check
    CHECK (harness IN ('codex','claude','pi','cursor','grok','gemini','opencode'));
ALTER TABLE rule_served_manifests DROP CONSTRAINT rule_served_manifests_harness_check;
ALTER TABLE rule_served_manifests ADD CONSTRAINT rule_served_manifests_harness_check
    CHECK (harness IN ('claude-code','codex','grok','pi','cursor','gemini','opencode'));

ALTER TABLE work_order_reviews DROP CONSTRAINT work_order_reviews_author_family_check;
ALTER TABLE work_order_reviews ADD CONSTRAINT work_order_reviews_author_family_check
    CHECK (author_family IN ('openai','anthropic','xai','cursor','google','local'));
-- The original multi-column inline CHECK has PostgreSQL's table-level name.
ALTER TABLE work_order_reviews DROP CONSTRAINT work_order_reviews_check;
ALTER TABLE work_order_reviews ADD CONSTRAINT work_order_reviews_reviewer_family_check
    CHECK (reviewer_family IN ('openai','anthropic','xai','cursor','google','local') AND reviewer_family<>author_family);
