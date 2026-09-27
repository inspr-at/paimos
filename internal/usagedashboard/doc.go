// SPDX-License-Identifier: AGPL-3.0-only

// Package usagedashboard serves GET /api/usage/dashboard (AEON-217).
//
// It sums the current harness_session_usage rows for sessions the caller can
// already see. One session may have several model rows. Token totals and the
// list-price estimate are cumulative for each session and model. Filtering by
// time keeps sessions whose harness_sessions.created_at falls in the range;
// the figures are lifetime usage for those sessions, not spend consumed inside
// the range. This package does not price tokens, does not read run telemetry
// or agent-run totals, and does not list the sessions on a ticket (that
// section is TW1).
//
// Null counters and a null estimate are unknown, not zero. A reported
// subscription label is metadata, not verified coverage, and it does not
// remove the list estimate. Allowance pacing uses registered
// account_allowance_windows and only for callers who hold account.read.
package usagedashboard
