-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-300: seed published api list prices (USD per 1M tokens).
-- Subscription billing never uses these rates for dashboard dollars.
-- Prices are explicit list prices, not inferred at request time.
-- One row cannot store a second context tier or a cache-write premium, so
-- those tiers are omitted. cached_input_usd_per_million is the cache-hit
-- or cached-input rate, never a cache-write rate.
--
-- Sources fetched 2026-09-29:
--   https://docs.x.ai/developers/pricing
--     grok-4.7 below 200k prompt tokens: input 2, cached 0.50, output 6.
--     At or above 200k the same id is 4 / 1 / 12 and is not stored.
--     grok-4 and grok-4-fast are absent from this page and are not seeded.
--   https://platform.claude.com/docs/en/about-claude/pricing
--     claude-sonnet-5 and claude-sonnet-5-5: 2 / 0.20 / 10
--     claude-opus-5: 5 / 0.50 / 25
--     claude-opus-5-5: 4 / 0.20 / 20
--     claude-haiku-4-5-20251001 and alias claude-haiku-4-5: 1 / 0.10 / 5
--     claude-fable-5: 10 / 1 / 50
--     claude-fable-5-1: 10 / 0.25 / 50
--     anthropic/claude-sonnet-5 and anthropic/claude-opus-5 are the Pi
--     catalog ids, priced the same as the Claude API ids. Short harness
--     aliases (haiku, sonnet, opus, fable) are not API ids and are not seeded.
--   https://developers.openai.com/api/docs/pricing
--     Standard short-context rates: gpt-6-luna 0.10 / 0.01 / 0.50,
--     gpt-6-sol 2 / 0.20 / 10, gpt-6-astra 10 / 1 / 50, gpt-4.1 2 / 0.50 / 8.
--     Long-context rates are not stored. gpt-6-terra is not on this page.
-- Cursor composer and grok-4.7 effort labels have no public API list price.
SET LOCAL lock_timeout = '5s';

CREATE FUNCTION aeon_seed_model_prices(target uuid) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
DECLARE
    prior_setting text := current_setting('aeon.tenant_id', true);
BEGIN
    PERFORM set_config('aeon.tenant_id', target::text, true);
    INSERT INTO model_prices(
        tenant_id, model, version,
        input_usd_per_million, output_usd_per_million, cached_input_usd_per_million)
    SELECT target, model, 1, input_rate, output_rate, cached_rate FROM (VALUES
        ('grok-4.7'::text, 2.000000::numeric, 6.000000::numeric, 0.500000::numeric),
        ('claude-sonnet-5', 2.000000, 10.000000, 0.200000),
        ('claude-sonnet-5-5', 2.000000, 10.000000, 0.200000),
        ('anthropic/claude-sonnet-5', 2.000000, 10.000000, 0.200000),
        ('claude-opus-5', 5.000000, 25.000000, 0.500000),
        ('claude-opus-5-5', 4.000000, 20.000000, 0.200000),
        ('anthropic/claude-opus-5', 5.000000, 25.000000, 0.500000),
        ('claude-haiku-4-5-20251001', 1.000000, 5.000000, 0.100000),
        ('claude-haiku-4-5', 1.000000, 5.000000, 0.100000),
        ('claude-fable-5', 10.000000, 50.000000, 1.000000),
        ('claude-fable-5-1', 10.000000, 50.000000, 0.250000),
        ('gpt-6-luna', 0.100000, 0.500000, 0.010000),
        ('gpt-6-sol', 2.000000, 10.000000, 0.200000),
        ('gpt-6-astra', 10.000000, 50.000000, 1.000000),
        ('gpt-4.1', 2.000000, 8.000000, 0.500000)
    ) AS prices(model, input_rate, output_rate, cached_rate)
    ON CONFLICT (tenant_id, model, version) DO NOTHING;
    PERFORM set_config('aeon.tenant_id', coalesce(prior_setting, ''), true);
END;
$$;
REVOKE EXECUTE ON FUNCTION aeon_seed_model_prices(uuid) FROM PUBLIC;

CREATE FUNCTION aeon_seed_model_prices_trigger() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=public,pg_temp AS $$
BEGIN
    PERFORM aeon_seed_model_prices(NEW.id);
    RETURN NEW;
END;
$$;
REVOKE EXECUTE ON FUNCTION aeon_seed_model_prices_trigger() FROM PUBLIC;

CREATE TRIGGER tenants_seed_model_prices AFTER INSERT ON tenants
    FOR EACH ROW EXECUTE FUNCTION aeon_seed_model_prices_trigger();

DO $$
DECLARE
    tenant uuid;
BEGIN
    FOR tenant IN SELECT id FROM tenants ORDER BY id LOOP
        PERFORM aeon_seed_model_prices(tenant);
    END LOOP;
END;
$$;
