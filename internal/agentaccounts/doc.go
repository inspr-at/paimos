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
// including mixed measured/provisional windows. With no measured account, the
// only runnable account is the default. Several unread accounts stay unranked.
// Provisional accounts can still be selected under
// existing queue policy: available and per-window remaining/pace_remaining
// describe ledger headroom, not proof of measured provider allowance. Unknown
// usage is never measured zero. No role route means no default model, and absent
// allowance can use the explicitly provisional first-run policy below.
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
// PUT {id}/label (person account.manage) renames only, so Settings' inline
// rename never turns a legacy null grant into an explicit one.
//
// GET/PUT {id}/residency-evidence reads/replaces a bounded host attestation.
// People read with account.read; only the linked owning person (account.manage)
// or the connected paired host's bound runtime key (account.probe) may write.
// An unpaired registering key, workspace admin or key creator is not implicitly
// an owner. Writes share the pairing -> tree -> tenant -> account lock order with
// readiness and pairing lifecycle operations, rechecking live permissions inside
// the final transaction. The tenant fence uses NO KEY UPDATE so FK share locks
// remain compatible. Writes append account.residency_evidence_updated last;
// advisory GETs avoid write fences.
// Evidence names covered profiles, inference/storage/log country sets, explicit
// local execution, verification/expiry times and an opaque proof reference;
// optional retention days and training opt-out are retained as declarations.
// EU needs three nonempty EU-only sets; local needs explicit local execution.
// Neither class qualifies without unexpired proof for that profile. Evidence
// is loaded with accounts and evaluated using the routing transaction's clock.
// A host/owner/provider/model binding change invalidates prior evidence. GET
// keeps it inspectable with binding_current=false; history lives in events.
// Evidence is an attestation, not automated verification of proof documents.
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
// aging <=window/6, stale beyond, expired at reset. Stale room is unknown:
// estimates and unknown windows never impose a start count or serial budget.
// Every named denial retains its own deadline, independently of other buckets.
// Unnamed stops use durable resource-scoped recovery waits across restarts.
// Readings never approve pairing: POST {id}/capacity/approve requires a person
// with account.manage and records the existing separate ongoing-use approval.
// AEON-353 permits saving this approval after the person approves pairing,
// before helper redemption; dispatch still requires redemption and a fresh probe.
// Unknown usage follows the actual parallel cap and the person's schedule,
// including permitted nights and off days. Run now skips schedule pacing,
// while vendor waits, Hold and manual limits remain authoritative.
// Catalog and queued runs expose structured advisory waits; reservation and
// claim recheck admission under the account lock. A person's per-run "now"
// override skips schedule pacing, never holds, truth or ownership fences.
// Active manual windows cap on top of readings (AEON-384): readings, pacing and
// Keep for you still apply next to them, and fresh vendor denial fences them.
// A removed manual window keeps its row and ledger but no longer caps. Percent
// reservations hold an initial 1% per job; tokens/dollars never masquerade as
// vendor quota.
//
// PUT/DELETE {id}/limit stores the one Advanced sentence per account in
// account_limit_rules: at most N percent, runs, requests, tokens or cost micros
// per local day, week or month of the account owner's schedule timezone. A rule
// is evaluated at admission, next to the readings, and never replaces them.
// Percent counts the rise of each current vendor window within the period (a
// reset inside the period starts that count again; your own use counts too).
// Runs count managed runs started, or holding the account, in the period.
// Requests, tokens and cost count run telemetry in the period, so a run that
// starts under the limit may finish above it. A reached limit waits with code
// allowance until the period ends; Run now does not skip it. Replacing or
// removing a rule keeps the old row as history. POST {id}/windows/{wid}/repeat
// turns an old manual window into a rule (its length picks day, week or month)
// and removes the window in the same transaction; DELETE {id}/windows/{wid}
// removes one. GET /capacity projects the rule with its use this period, the
// manual windows still in force, and list-price spend this month for accounts
// billed by API key.
// Settlement releases that hold without adding usage already in observations.
//
// GET /api/agent-accounts/capacity (person account.read) returns exact percentages,
// original observation time, source, freshness, and schedule pacing separately
// from the legacy account contract. GET {id}/readings returns 200 recent samples.
// Paired keys use account.probe and can read only their computer's enrollments.
// Idle capture lookup failures log a bounded error category and fall back to
// successful local capture times persisted across daemon restarts. A quota
// capture reports lifecycle capturing, or draining when a drain is requested;
// it never grants permission to clean up a still-owned local process.
// GET/PUT /api/agent-accounts/capacity/schedule stores the current person's user,
// pool (harness) or account override; account wins, then pool, then user. Null
// removes an override. Default: the person profile timezone (UTC if absent), Monday-Friday, 08-22, nights disabled,
// off_days expire. Per-day half-hour bands, an optional crossing night, three
// shifts and 24 hourly blocks match the approved schedule editor. Shifts and
// blocks replace work bands; reduced rates are adjustable from 0.1 to 0.9.
// Routing enforces max(0,budget-used_today), including outstanding reservations.
// The pairing approver owns the schedule; for unpaired accounts the first person
// saving an account schedule establishes ownership. Schedules also inherit
// from an unpaired agent's unambiguous human key creator before an
// account override exists, including the owner's profile timezone by default.
// Ambiguous or unknown ownership does not select another person's schedule.
// Account/pool/user schedules support override=sprint (remaining quota until the next reset in scope, stored
// as override_until) or hold (zero allowance). Outside work bands routing waits
// unless Sprint is active.
// Daily shares use the schedule period, including for accounts first read late
// in the day. A missing usage baseline counts deltas from the first sample and
// remains marked unknown; it never shrinks the share to the remaining day.
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
// Advanced limits use the capacity owner's local day, Monday-first week, or
// month. Positive percent deltas survive vendor resets; the largest total per
// vendor series binds. Refresh grants obey the same limit. Each managed route
// snapshots per-run estimates with its account reservation under the account
// lock. Admission counts recorded use plus remaining live holds; a terminal run
// with missing usage retains its estimate in each overlapping period. Unmeasured
// defaults are one request, 100k tokens and one dollar; measured estimates use
// the largest total from the last twenty finished runs. Dollar limits require
// positive priced run telemetry (422 unsupported_limit_unit otherwise).
// Make this repeat retains the original manual cap until its expiry, preserves
// its ledger, and refuses to overwrite an existing Advanced sentence.
//
// Settle and Release run inside the caller's db.InTenant transaction. Settle
// turns monotonic telemetry sums into used units and is idempotent per
// reservation. Release returns unused reserved units only for a queued run or
// a terminal fenced transition, and it does not touch a live process. Draining
// blocks new reservations and leaves an owned run where it is. Account and
// window responses expose provisional=true when a window has no positive
// measurement for its unit or a settled reservation lacked one. Historical
// zero telemetry cannot prove measured zero, so it remains provisional.
// AEON-478 package A adds GET /agent-accounts/readiness (keyset pagination),
// POST /{accountId}/check (person owner + account.manage; revision-bound,
// idempotent/coalesced, persisted 60-second gap), and PUT /{accountId}/sharing.
// Migration 1135 stores opaque resource memberships, per-window facts, durable
// wait/backoff/early-recovery markers and check receipts. Unknown measurements
// carry usage_unknown_reserve_not_enforceable and never impose a start budget.
// Check requests express early-recovery intent only: package B consumes it
// atomically with automatic recovery, and rechecks launch/admission authority.
// account_readiness_check_waits freezes the resource/window/wait IDs at the
// original click; a pending retry cannot authorize a later wait. B compares
// that snapshot with the canonical fact wait and early_recovery_used marker.
// Package C opts into GET /agent-accounts?include_checks=true with account.read
// and account.probe, and reports through the existing scoped probe's optional
// readiness object (check ID, binding revision, bounded result, at most 32 facts).
// Revocation, relinking, archival and generation changes invalidate pending
// checks. A provider-confirmed no-reset 402 survives all ordinary check reports;
// only evidenced successful recovery inference may replenish it.
// Owners and enrolling daemons see account details; teammates (including admins)
// require the owner's sharing setting. HTTP account responses and event/SSE
// replay apply current server-side redaction. Required old shapes remain valid
// (empty windows/history, null timestamps); projections mark details_redacted.
// This package supplies no daemon executable or UI. Existing
// ledgers remain unchanged; rollback disables new callers and retains history.
//
// Pending captures expire after five minutes from their original requested_at;
// aliases never renew them. Polling and reports reject expired captures, new
// keys invalidate them and request fresh work, and old keys return 409.
// Restart reports advance daemon generation even when carrying an old check:
// the heartbeat commits, completion/facts are rejected with 409 stale_binding
// and X-Aeon-Write-Committed, and the daemon refreshes pending work.
// Account-local capture errors never affect peers.
// Owners/registered daemons retain their own check result/controls while
// pooled quota detail remains withheld until all resource owners share it.
// Legacy readings and key caps use the account's canonical local membership;
// positive key caps with unknown total balance still carry UsageUnknownReason.
// Availability-only advice uses historical state enums. Dashboard windows and
// harness reset details use the same current accountprivacy policy. Dashboard
// allowances consider at most 1024 windows, mark truncation explicitly, and
// report partial when visible windows omit withheld or truncated windows. If a
// successful mutation's response cannot be delivered safely, the error states
// "write committed" and sets X-Aeon-Write-Committed so callers refresh first.
//
// Package B uses per-run provisional ledgers for unknown usage, with no start
// counter or serial quota fence. Real slots, schedules and manual limits bind.
// Unnamed vendor and provider 402 waits grant one durable resource-scoped
// recovery at expiry or on a current owner check. Failed inference advances
// 1/2/4/8-hour backoff; only evidenced successful inference clears a 402.
// An eligible unstarted reservation may acquire that permit during claim;
// other queued holds stay intact and cannot launch during its recovery wait.
// A managed run's own expired provisional estimate reaches current admission;
// an obsolete measured hold also reaches current admission, which binds a
// recovery permit only while a recoverable stop remains. Aging alone and a
// cleared stop cannot strand queued holds. Manual and pairing ledgers retain
// their expiry checks.
// Telemetry-only denials acquire a canonical wait at claim or a fresh owner
// check, preserving the original stop/deadline and the exact early intent.
// Readiness evaluates the same non-mutating admission policy, including fresh
// fact-only reserves, manual window pace and recovery eligibility, without
// consuming the permit. Recovery uses the same current sibling/resource
// authority at reservation and claim; withdrawn pools and stale membership
// revisions release holds.
// Manual allowances cap work but never make unknown vendor usage measured.
// A restarted heartbeat survives rejection of an obsolete pending check. The
// 409 response identifies the committed heartbeat; stale facts are discarded,
// polling stops offering the old check, and old receipts cannot rewind it.
package agentaccounts
