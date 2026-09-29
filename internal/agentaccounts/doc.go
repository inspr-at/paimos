// SPDX-License-Identifier: AGPL-3.0-only

// Package agentaccounts serves /api/agent-accounts and the allowance ledger
// behind usage-aware routing.
//
// New returns an httpapi.Module. The coordinator mounts it; this package does
// not edit cmd/aeon. Registration stores an opaque account key for the
// calling agent principal and rejects credential-shaped values. Probes are
// accepted only from that principal and only for the account's daemon id.
// A route request names the claiming daemon and its locally enrolled account
// IDs. Selection is restricted to those IDs, that daemon, and accounts
// registered by the calling agent. A reservation requires a successful probe
// newer than ProbeFreshness. The coordinator mounts New as an httpapi.Module.
//
// GET /api/agent-accounts/catalog is an account.read person-only projection:
// daemon enrollment -> harness -> account -> registry model -> effort/profile.
// It contains display labels, active allowance and pacing headroom, owner probe
// freshness, capacity, and advisory defaults. It never exposes local routing
// keys. The first eligible role route chooses a profile; the eligible account
// with greatest known minimum remaining fraction wins, with UUID tie-breaking.
// Any provisional active window makes the aggregate fraction unknown (null),
// including mixed measured/provisional windows. With no eligible measured
// account there is no default. Provisional accounts can still be selected under
// existing queue policy: available and per-window remaining/pace_remaining
// describe ledger headroom, not proof of measured provider allowance. Unknown
// usage is never measured zero. No role route means no default model, and absent
// allowance means unavailable.
// Registry initialization remains with /api/models; the catalog is read-only.
//
// PUT /api/agent-accounts/{id}/metadata replaces label, plan, host_label and
// allowed_model_profile_ids. A person needs account.manage; the registering
// agent needs an account.manage key. Profile UUIDs must be enabled tenant
// profiles for that harness. An empty array denies every profile. Legacy NULL
// retains the existing tenant-harness catalog policy; it does not assert a
// provider entitlement. Plan names are display metadata, never inferred model
// grants. Explicit grants are checked at run creation, reservation and claim.
// A grant change does not stop already owned runs. Metadata is audited and an
// identical retry is a no-op. Credentials, home paths and identities stay local.
// Display labels accept up to 128 Unicode characters, matching session metadata.
//
// Agentd's local account enrollment accepts an optional metadata object with
// exactly those four fields and publishes it once on daemon startup. Owner
// probes can fill a missing host label; configured labels take precedence.
// Route responses include account_label, which agentd forwards with model and
// reasoning_effort to the existing harness session metadata contract.
//
// Capacity readings (AEON-297): POST /api/agent-accounts/{id}/readings requires
// an account.probe key and the registering agent. Reports are append-only,
// tenant-isolated and idempotent; optional run IDs must use that account.
// Derived percent windows have allowance 100 and conservatively round used
// percent upward for the integer reservation ledger. Fresh means <=10 minutes;
// aging <=window/6, stale beyond, expired at reset. Aging/stale readings allow
// one estimated 1% refresh job per observation, with no other live run. If all
// windows reset, a provisional five-minute 1% grant permits that same single
// refresh; expiry/release never renews it without a new measured observation. Explicit vendor denial fails closed until explicit vendor recovery.
// Readings never approve pairing: POST {id}/capacity/approve requires a person
// with account.manage and records the existing separate ongoing-use approval.
// Active manual windows override pacing; fresh vendor denial still fences them. Percent reservations hold an
// initial 1% per job; tokens/dollars never masquerade as vendor quota.
// Settlement releases that hold without adding usage already in observations.
//
// GET /api/agent-accounts/capacity (person account.read) returns exact percentages,
// original observation time, source, freshness, and schedule pacing separately
// from the legacy account contract. GET {id}/readings returns 200 recent samples.
// GET/PUT /api/agent-accounts/capacity/schedule stores the current person's user,
// pool (harness) or account override; account wins, then pool, then user. Null
// removes an override. Default: the person profile timezone (UTC if absent), Monday-Friday, 08-22, nights disabled,
// off_days expire. Per-day half-hour bands, an optional crossing night, three
// shifts and 24 hourly blocks match the approved schedule editor. Shifts and
// blocks replace work bands; reduced rates are adjustable from 0.1 to 0.9.
// Routing enforces max(0,budget-used_today), including outstanding reservations.
// The pairing approver owns the schedule; for unpaired accounts the first person
// saving an account schedule establishes ownership. Account/pool/user schedules
// support override=sprint (remaining quota until the next reset in scope, stored
// as override_until) or hold (zero allowance). Outside work bands routing waits
// unless Sprint is active.
// A missing period baseline anchors at the first sample and remains marked unknown.
// Every report is a full normalized snapshot per source; missing buckets retire
// derived windows but never delete history. Claude sparse events retain unexpired
// peers at their original observation times and drop them after reset.
//
// Lead workers can use harness heartbeat or run-heartbeat with --capacity-account
// UUID --capacity-source codex|claude --capacity-file PHYSICAL_JSONL_PATH. The
// one-shot heartbeat also accepts --capacity-phase start|update|end; the loop
// captures start/end automatically. The account must belong to the reporting
// principal. Codex accepts app-server
// rate-limit events/responses or timestamped rollout token_count.rate_limits.
// Claude accepts rate_limit_event stream frames; prefix them with a timestamp
// field for durable capture. An undated frame is usable only at the file tail,
// with the file modification time, so later output cannot freshen old quota.
// No credential files, vendor identity or raw stream content are uploaded.
// agentd captures Codex at start/end and updates, with quota-neutral app-server
// fallback on a separate idle ticker, at most every five minutes (configurable),
// never while an account has a live managed run. Claude preserves its first/last available stream
// observation, including the original read time on the end snapshot; it does
// not invent a pre-run baseline when the vendor supplies none.
//
// Pace for elapsed window fraction f in [0,1] is f (steady),
// 1-(1-f)^2 (frontload) or 1 (unrestricted). The cumulative allowed fraction
// is min(1, pace+burst_ratio), and the estimate must also fit the hard
// allowance. Eligible accounts are ordered by projected
// (used+reserved+estimate)/allowance, then by account id. Reservations for
// every active window commit with run.account_id or not at all. A repeated
// route returns that same choice. While a run is queued, retries recheck its
// account state, probe, profile and grants; revoked eligibility returns conflict
// without rerouting or releasing the held reservation.
//
// Settle and Release run inside the caller's db.InTenant transaction. Settle
// turns monotonic telemetry sums into used units and is idempotent per
// reservation. Release returns unused reserved units only for a queued run or
// a terminal fenced transition, and it does not touch a live process. Draining
// blocks new reservations and leaves an owned run where it is. Account and
// window responses expose provisional=true when a window has no positive
// measurement for its unit or a settled reservation lacked one. Historical
// zero telemetry cannot prove measured zero, so it remains provisional.
package agentaccounts
