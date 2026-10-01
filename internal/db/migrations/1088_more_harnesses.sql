-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-452: widen existing authorities; retain every historical profile and RLS policy.
-- The runner commits install, validate and replacement separately, with exact-byte
-- phase checkpoints, so validation scans after installation's DDL locks release.
SET LOCAL lock_timeout = '5s';

-- Install supersets without scanning under ADD CONSTRAINT's exclusive lock.
-- Keep historical checks until every replacement has been validated.
ALTER TABLE model_profiles ADD CONSTRAINT model_profiles_harness_v2_check
    CHECK (harness IN ('codex','claude','pi','cursor','grok','gemini','opencode')) NOT VALID;
ALTER TABLE model_profiles ADD CONSTRAINT model_profiles_family_v2_check
    CHECK (family IN ('openai','anthropic','xai','cursor','google','local')
        OR (harness IN ('pi','opencode') AND family='unknown')) NOT VALID;
ALTER TABLE agent_accounts ADD CONSTRAINT agent_accounts_harness_v2_check
    CHECK (harness IN ('codex','claude','pi','cursor','grok','gemini','opencode')) NOT VALID;
ALTER TABLE harness_sessions ADD CONSTRAINT harness_sessions_harness_v2_check
    CHECK (harness IN ('codex','claude','pi','cursor','grok','gemini','opencode')) NOT VALID;
ALTER TABLE account_groups ADD CONSTRAINT account_groups_harness_v2_check
    CHECK (harness IN ('codex','claude','pi','cursor','grok','gemini','opencode')) NOT VALID;
ALTER TABLE account_ticket_pins ADD CONSTRAINT account_ticket_pins_harness_v2_check
    CHECK (harness IN ('codex','claude','pi','cursor','grok','gemini','opencode')) NOT VALID;
ALTER TABLE inbox_message_targets ADD CONSTRAINT inbox_message_targets_adapter_v2_check
    CHECK (adapter IN ('codex','agentd_codex','agentd_claude','agentd_pi','agentd_cursor',
        'agentd_gemini','agentd_opencode','grok_bot_routine','claude_resume','claude_channel')) NOT VALID;
ALTER TABLE rule_served_manifests ADD CONSTRAINT rule_served_manifests_harness_v2_check
    CHECK (harness IN ('claude-code','codex','grok','pi','cursor','gemini','opencode')) NOT VALID;
ALTER TABLE work_order_reviews ADD CONSTRAINT work_order_reviews_author_family_v2_check
    CHECK (author_family IN ('openai','anthropic','xai','cursor','google','local')) NOT VALID;
ALTER TABLE work_order_reviews ADD CONSTRAINT work_order_reviews_reviewer_family_v2_check
    CHECK (reviewer_family IN ('openai','anthropic','xai','cursor','google','local') AND reviewer_family<>author_family) NOT VALID;

ALTER TABLE model_profiles VALIDATE CONSTRAINT model_profiles_harness_v2_check;
ALTER TABLE model_profiles VALIDATE CONSTRAINT model_profiles_family_v2_check;
ALTER TABLE agent_accounts VALIDATE CONSTRAINT agent_accounts_harness_v2_check;
ALTER TABLE harness_sessions VALIDATE CONSTRAINT harness_sessions_harness_v2_check;
ALTER TABLE account_groups VALIDATE CONSTRAINT account_groups_harness_v2_check;
ALTER TABLE account_ticket_pins VALIDATE CONSTRAINT account_ticket_pins_harness_v2_check;
ALTER TABLE inbox_message_targets VALIDATE CONSTRAINT inbox_message_targets_adapter_v2_check;
ALTER TABLE rule_served_manifests VALIDATE CONSTRAINT rule_served_manifests_harness_v2_check;
ALTER TABLE work_order_reviews VALIDATE CONSTRAINT work_order_reviews_author_family_v2_check;
ALTER TABLE work_order_reviews VALIDATE CONSTRAINT work_order_reviews_reviewer_family_v2_check;

-- PostgreSQL names cross-column checks differently from column-only checks.
-- Identify the historical enum by its predicate, never by an assumed name.
-- In particular, the review null-pairing rule must remain byte-for-byte intact.
DO $$
DECLARE
    target record;
    old_name text;
    old_count integer;
    pairing_definition text;
BEGIN
    SELECT pg_get_constraintdef(oid) INTO STRICT pairing_definition
    FROM pg_constraint
    WHERE conrelid='work_order_reviews'::regclass AND contype='c'
        AND pg_get_constraintdef(oid) LIKE '%reviewer_profile_id IS NULL%'
        AND pg_get_constraintdef(oid) LIKE '%reviewer_family IS NULL%';

    FOR target IN SELECT * FROM (VALUES
        ('model_profiles', 'harness', 'model_profiles_harness_v2_check'),
        ('model_profiles', 'family', 'model_profiles_family_v2_check'),
        ('agent_accounts', 'harness', 'agent_accounts_harness_v2_check'),
        ('harness_sessions', 'harness', 'harness_sessions_harness_v2_check'),
        ('account_groups', 'harness', 'account_groups_harness_v2_check'),
        ('account_ticket_pins', 'harness', 'account_ticket_pins_harness_v2_check'),
        ('inbox_message_targets', 'adapter', 'inbox_message_targets_adapter_v2_check'),
        ('rule_served_manifests', 'harness', 'rule_served_manifests_harness_v2_check'),
        ('work_order_reviews', 'author_family', 'work_order_reviews_author_family_v2_check'),
        ('work_order_reviews', 'reviewer_family', 'work_order_reviews_reviewer_family_v2_check')
    ) AS enums(table_name, column_name, replacement_name)
    LOOP
        SELECT count(*), min(conname::text) INTO old_count, old_name
        FROM pg_constraint
        WHERE conrelid=target.table_name::regclass AND contype='c'
            AND conname<>target.replacement_name
            -- The new model-family rule also mentions a harness enum.
            AND conname<>'model_profiles_family_v2_check'
            AND pg_get_constraintdef(oid) ~ format('\m%s\M\s*=\s*ANY\s*\(', target.column_name);
        IF old_count<>1 THEN
            RAISE EXCEPTION 'expected one historical enum for %.%, found %',
                target.table_name, target.column_name, old_count;
        END IF;
        EXECUTE format('ALTER TABLE %I DROP CONSTRAINT %I', target.table_name, old_name);
    END LOOP;

    IF NOT EXISTS (SELECT 1 FROM pg_constraint
        WHERE conrelid='work_order_reviews'::regclass AND contype='c'
            AND pg_get_constraintdef(oid)=pairing_definition) THEN
        RAISE EXCEPTION 'review null-pairing constraint changed';
    END IF;
END $$;
