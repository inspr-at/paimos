-- SPDX-License-Identifier: AGPL-3.0-only
-- Public configuration only; provider credentials stay in the local pi profile.
ALTER TABLE agent_accounts
    ADD COLUMN provider text NOT NULL DEFAULT '',
    ADD COLUMN model text NOT NULL DEFAULT '',
    ADD COLUMN model_status text NOT NULL DEFAULT 'unchecked' CHECK (model_status IN ('known','unknown','unchecked')),
    ADD COLUMN model_data_note boolean NOT NULL DEFAULT false,
    ADD COLUMN openrouter_credits jsonb;

UPDATE agent_accounts a SET provider=split_part(p.model,'/',1),
    model=substring(p.model from position('/' in p.model)+1)
FROM agent_pairing_enrollments e JOIN model_profiles p ON p.tenant_id=e.tenant_id AND p.id=e.model_profile_id
WHERE a.tenant_id=e.tenant_id AND a.id=e.account_id AND a.harness='pi' AND position('/' in p.model)>1;

ALTER TABLE model_profiles DROP CONSTRAINT model_profiles_family_check;
ALTER TABLE model_profiles ADD CONSTRAINT model_profiles_family_check
    CHECK (family IN ('openai','anthropic','xai','cursor') OR (harness='pi' AND family='unknown'));
