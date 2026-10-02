// SPDX-License-Identifier: AGPL-3.0-only

// Package agentruns implements AEON-28 / P2.3 queued runs, fenced claims and
// content-free telemetry. New(pool, recorder) returns an httpapi.Module for
// /api/runs, /api/runs/* and /api/work-orders/{workOrderId}/runs. Mount alongside
// workorders.New(pool) behind auth.Middleware; only the coordinator edits cmd.
//
// B7 history: GET /api/runs accepts session, agent and work_order UUID filters
// (intersection), limit 1..200 and an opaque cursor. It returns items and
// next_cursor, newest first by (created_at,id). Cursors bind tenant, principal
// and filters; agents still see only their own runs. A session selects its
// explicitly bound run_id, never all runs for the same agent or work order.
// Run responses include nullable outcome and duration_ms (whole milliseconds
// between terminal timestamps), requested/effective model and existing exact
// integer token/cost_micros counters. Missing terminal data stays null.
// AEON-300 adds nullable active_ms (duration minus time spent in waiting
// telemetry), outcome_detail (no_commit, committed, pr_opened, merged, or
// abandoned, derived when the run becomes terminal; local git evidence yields
// committed or no_commit only, merged is reserved for forge or release
// evidence), and retry_of_run_id.
// Direct SQL completion leaves the new fields null. A retry must name an
// earlier run of the same work order and agent.
// Creation stamps the canonical preference starter and residency requirement,
// including ticket-queue runs. The saved trace explains overrides that loosen
// an inherited residency lock; vendor retries preserve that creation evidence.
// This extends New; no new plugin installation or server wiring is required.
//
// AC3 / AEON-181: RunCreate accepts optional requested_account_id (0851).
// Creation validates its tenant, enrolling agent and profile harness and writes
// the choice in run.created. account_id stays null until allowance reservation.
// agentaccounts.New enforces the choice within the claiming daemon's enrollment;
// no eligible chosen account means queued, never an implicit fallback. Omitted
// or null keeps automatic routing. Existing New constructors and run.create /
// account.route permissions apply; the coordinator needs no additional wiring.
// AEON-353 adds person-only capacity_override=now at creation or via POST
// /api/runs/{id}/capacity-override while queued. It is run-scoped, idempotent
// and audited; capacity routing still checks approval, hold, vendor truth,
// probe freshness, models and concurrency. Queued responses include advisory
// structured wait reasons without reserving capacity. AEON-402 adds person-only
// POST /api/runs/{id}/cancel: a queued run ends as cancelled and releases its
// holds in the same transaction; a started run is never changed.
//
// Agent keys require exact run.read, run.create, run.claim or run.telemetry
// scopes. People may create/read runs; queue, claim and telemetry are agent-only.
// Agents create their own runs, read their own runs, and see only their queue.
// Claim requires the assigned agent or a live, unrevoked run.claim grant for
// this run. The caller must also own the reserved daemon account. A new claim
// verifies all reservation IDs, active windows, compatible profile, recent
// successful probe (two minutes), account availability and remaining order
// budget. Same-generation claims replay without mutation; another generation
// cannot take over a live process. Profile and requested model are pinned when
// queued; effective model is separately retained with vendor evidence.
//
// AEON-600 lane enforcement: the scheduler calls lanecontrol.ReserveTx with
// its durable dispatch ID, then BindRunTx for explicitly budgeted later work.
// These calls share authz.LockProjectMutation with policy/revocation/target
// changes and must precede event append. Claim narrows ordinary account
// admission with live owner permission, policy/window, subscription quota,
// person allocations and lane slots. The whole dispatch envelope stays held
// across retries; only confirmed elapsed-process settlement charges it, and
// CloseTx releases provably unused time. Unknown exits retain their slot/hold.
// Legacy daemons and unbound child/retry claims fail closed. Vendor automatic
// continuations and automatic per-ticket reviews cannot launch lane work.
// Qualification of production adapter boundaries is deliberately separate:
// the daemon requires an explicit LaneExecutionAdapter and local project map.
// No current production adapter advertises that qualification. The coordinator
// must integrate scheduler/lifecycle handoffs before enabling automatic launch.
//
// Coordinator contract reconciliation: R2 RunTelemetry lacks fencing fields.
// This module therefore requires X-Aeon-Daemon-ID and X-Aeon-Daemon-Generation
// on every telemetry request, matching the claim and current account generation.
// The coordinator must add these headers to OpenAPI and the daemon client; this
// worker does not edit the shared contract or another worker's package. Model
// and daemon identifiers accept at most 128 ASCII letters/digits or . _ - : /.
// Free-form fields and unknown JSON fields are rejected. No vendor transcript,
// credential, raw error text or request body is logged.
//
// Sequences must strictly increase but may have gaps. The numeric telemetry
// row, usage totals, status, budget block and R1 event commit atomically. Since
// migration 0201 has no columns for status/model replay, run.telemetry events
// retain the complete canonical content-free report. Exact replay returns the
// current run without charging or appending again; changes to any field conflict,
// including status/model fields. Terminal runs accept only historical replay.
// started defaults to running; finished defaults to completed. Claim starts
// server elapsed-time accounting, including starting/waiting time.
//
// Account integration: pass the account module's UsageRecorder to New for
// production allowance settlement. The callback receives updated cumulative
// totals and the accepted report in the same db.InTenant transaction, once per
// new sequence. It must append events for its own changed projections and must
// not nest transactions. It can release reservations on terminal transitions.
// Nil performs no allowance settlement: the account worker owns its projections,
// while work-order budget accounting is always enforced here. Lock order is
// work_order, run, account, then windows/reservations ordered by window UUID.
package agentruns
