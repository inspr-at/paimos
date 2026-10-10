# Model catalog facts from sources

`GET /api/models/catalog` reads a tenant-scoped catalog independently of immutable
model pins. A model has one `provider:model_id` identity and retains each exact
API or harness route. Evidence contains `fact`, `value`, `source`, `source_kind`,
`field_path`, `fetched_at`, and `route`. Consumers resolve facts per field:
exact-route harness evidence, then native provider evidence, then aggregators,
then ID-only offline bootstrap. A missing native field permits an explicitly
sourced fallback. Null means unknown; an empty array means a known empty list.
Native effort names and reasoning control metadata are preserved without mapping
them onto a universal numerical scale. Google's thinking boolean does not imply
a list of token budgets. Cursor IDs fold only when the reported effort exactly
matches the ID suffix; the original launch ID remains on the route. Google IDs
shared by Gemini and OpenCode resolve to the same provider identity.

The existing manual/scheduled refresh path fetches OpenRouter, models.dev and
LiteLLM without keys. `api_enabled` still controls credentialed native requests;
public discovery runs on the same refresh schedule and cooldown without a vault.
Available OpenAI, Anthropic, xAI and Google account credentials add native facts.
Google uses the same vault derivation and the credential endpoint's additive
`google` vendor. Credentials are never returned by the catalog, logged, or stored
in provenance. Both native credentials and refresh authority are checked again
before publishing fetched data. A disabled/revoked account cannot publish its
in-flight native result.

Fetches use fixed origins, refuse redirects, share a 30-second refresh deadline,
and bound response bytes, model count, pagination and fact values. Truncated or
failed lists report `limited` or `stale`, rather than success or disappearance.
The catalog read uses keyset pagination (`limit`, `cursor`, `next_cursor`) and
explicit `truncated` flags for bounded evidence. Public discovery records facts
and inactive profile proposals; it does not grant an account a model. Existing
person policy and the qualified successor activation fence remain in force.
An ID-only response creates no default effort and borrows no predecessor levels.

Migration 1331 adds facts and Google credential storage under forced tenant RLS.
It intercepts metadata for newly inserted pins so the historical display table
and effort scale cannot invent new names or levels. Previously stored pins,
display metadata, routes, and registered numeric levels remain readable. The
production seed retains IDs only and initializes catalog evidence without
creating level-bearing pins or replacing tenant ladders. Historical v3 pins live
only in compatibility test fixtures. Harness schedule/probes and outdated/bulk
retirement are the follow-on packages AEON-1152 and AEON-1153.

Recorded public fixtures cover keyless refresh and provenance; native response
examples cover precedence, unknown fields and the Google boolean. Transport is
injected in tests; no tests need a vendor key or live provider request.

Source formats checked on 2026-10-11:

- [OpenAI Models](https://developers.openai.com/api/reference/resources/models/methods/list)
- [Anthropic Models](https://platform.claude.com/docs/en/api/models/list)
- [xAI Models](https://docs.x.ai/developers/rest-api-reference/inference/models)
- [Google Models](https://ai.google.dev/api/models)
- [models.dev reasoning options](https://github.com/anomalyco/models.dev/blob/dev/packages/core/src/schema.ts)
- [OpenRouter Models](https://openrouter.ai/docs/api/api-reference/models/list-all-models-and-their-properties)
- [LiteLLM model JSON](https://github.com/BerriAI/litellm/blob/main/model_prices_and_context_window.json)
