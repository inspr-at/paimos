-- SPDX-License-Identifier: AGPL-3.0-only
-- Initial, explicitly requested OpenRouter option. No automatic role route or
-- allowance grant; person approval and normal pacing still govern execution.
INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier)
SELECT id,'pi-openrouter-space-bunny-alpha-off','2','pi','unknown',
    'openrouter/stealth/space-bunny-alpha','off','standard' FROM tenants t
WHERE EXISTS (SELECT 1 FROM model_profiles p WHERE p.tenant_id=t.id)
ON CONFLICT (tenant_id,slug,version) DO NOTHING;
