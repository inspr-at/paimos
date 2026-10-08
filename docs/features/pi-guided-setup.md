# Pi guided setup

`paimos-agentd setup --harness pi` and `add-harness --harness pi` use the same
person-approved pairing flow as the other harnesses. Install pi normally and use
its `/login` and `/model` commands first. Setup pins the executable and reads its
version. For npm's `#!/usr/bin/env node` entrypoint it also resolves Node (or
uses `--node-path`), pins its physical path and version privately, and prepends
its directory to the probe and runtime PATH. Node must be installed outside the
workspace. A missing or changed interpreter pin blocks only that account.
Setup then checks
the selected provider/model against pi's public RPC
`get_available_models` response. `--account-context anthropic` selects a specific
configured provider instead. For existing native sign-ins the local profile is `~/.pi/agent`; Aeon never
opens its credential files, inherits provider keys for this check, or sends a
prompt. This establishes configured authentication, not remote credential
validity or a person's identity. The approval label names the provider and local
profile; the server selects an enabled pi model profile for that provider, and
only the computer keeps the profile path. The profile directory must be private
(no group or other access), as required by the daemon. Managed installations stay
with their owning Nix/Home Manager configuration.

The paired daemon rechecks that provider at most once a minute during polling
and afresh before each run, and refuses a model profile from a different
provider. Probe startup failures report a harness startup problem; only missing
provider configuration requests vendor login. Pi verification is explicitly
unavailable: its managed adapter has no qualified no-tools boundary. Connect without verification, then
set request limits for ongoing work. Pi retains its existing SVG harness icon and
account allowance pipeline; missing vendor token, cost or capacity readings
remain unreported, never inferred from a provider name or a successful probe.

For OpenRouter, run `aeon-agentd add-harness --harness pi --provider openrouter`
on an already paired computer (or append `--harness pi --provider openrouter`
to `aeon-agentd pair` for a new one). The hidden terminal prompt checks the key
with `GET https://openrouter.ai/api/v1/key`, without spending tokens, and stores
it in a new 0700 per-account pi profile with a 0600 `auth.json`. Headless setup
accepts `--openrouter-env-file /absolute/private/path`: only a literal
`OPENROUTER_API_KEY` assignment, never shell commands. No key flag, inherited
provider key, keychain dump, server upload, or key in child arguments/environment.
Normal native provider setup also accepts `--provider PROVIDER`.

Settings → Accounts lets a person with `account.manage` choose a pi model;
OpenRouter uses `vendor/model[:variant]`. The server checks its public models
list with a four-second timeout and a fifteen-minute cache (failed lookups cache
for thirty seconds). Unconfirmed slugs save as **unknown**. The initial
OpenRouter option is `stealth/space-bunny-alpha`; edits create immutable model
profiles for the account catalog/planning selector. Active runs keep their pin;
previously queued stale choices must be selected again. OpenRouter's underlying
model family is not assumed, so unknown families cannot qualify for review gates.

Stealth and free models show the provider data note. Local probes report only
numeric dollar usage, key limit and remaining credits with their reading time;
missing values stay unknown. Credit amounts are not a renewable allowance window:
normal request limits and the blind-provider pacing policy still apply. No
completion call is used to validate a key or a model.
