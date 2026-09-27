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
