// SPDX-License-Identifier: AGPL-3.0-only

// Package usagedashboard serves the dashboard and project lead usage reads.
//
// It sums the current harness_session_usage rows for sessions the caller can
// already see. One session may have several model rows. Token totals and the
// list-price estimate are cumulative for each session and model. Filtering by
// time keeps sessions whose harness_sessions.created_at falls in the range;
// the figures are lifetime usage for those sessions, not spend consumed inside
// the range. Dashboard token/cost totals do not price tokens or add run
// telemetry or agent-run counters. This is not a ticket session list.
//
// Null counters and a null estimate are unknown, not zero. A reported
// subscription label is metadata, not verified coverage. Dashboard dollar
// totals count only billing_mode api, so a stored estimate on a subscription
// or unknown row is not summed. Allowance pacing uses registered
// account_allowance_windows and only for callers who hold account.read.
// A provisional window, including mixed settled evidence, leaves used,
// headroom and hard remaining null. Declared allowance, explicit
// reservations and schedule capacity stay numbers. Overflow is null, not zero.
//
// AEON-738: /api/projects/{projectId}/lead/usage consumes AEON-734 generation
// history and AEON-689 episode contribution segments. The independently owned
// contracts need to land before the full projection is available. No dependency
// code is imported, no historical ownership is inferred, and no new scheduler,
// quota authority, admission exception or execution grant is introduced.
//
// usage_lead_session_id is measurement provenance frozen at registration.
// Coordinators record their own ID; nested workers inherit the parent's frozen
// ID. Dispatchers must register workers with their original parent. Later binds
// and succession never migrate counters. Legacy/unbound lineage remains NULL.
// Range selection uses session creation and segment first_work_at; measurements
// remain cumulative. Only the lead's exclusive managed run supplies its timing;
// workers use episode segment counters, never overlapping session/run totals.
//
// Holds are separate native-unit account reservations for the visible measured
// or registered attempts, deduplicated by run/window. Queued attempts without
// registration or episode lineage are outside this explicit scope. Window
// allowance/pooled usage is never a lead budget, and finish forecast is unavailable.
// Account details require their owner and the existing pooled privacy policy.
package usagedashboard
