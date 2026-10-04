// SPDX-License-Identifier: AGPL-3.0-only

// Package web supplies the Vue application through Static. P1.4b (AEON-23)
// adds the R1 workspace at /: a tenant-configured, paginated work tree, filtered
// lists, saved views, keyboard search (Ctrl/Cmd+K), and a Markdown node sidebar.
// Nodes can be created, edited, moved and deleted through the R1 API contract.
//
// Integration uses the existing R0 Static() (fs.FS, bool) entry point and
// httpapi.Server.Web. This frontend package adds no /api routes, so it does not
// introduce an httpapi.Module constructor. The coordinator alone wires the R1
// backend modules into cmd/aeon. Build with npm run build in web, then build
// the binary with -tags webembed to include these assets.
//
// src/lib/api.ts owns typed R1 calls and EventSource lifecycle. SSE named
// resource events and reconnection refresh loaded pages, kinds, saved views,
// active search and the selected node. Native EventSource manages replay IDs.
// Keep its event-name list aligned with backend writers during integration.
// Unsaved drafts survive refreshes; observed concurrent changes block saving.
// Conditional node writes send the observed updated_at in If-Unmodified-Since;
// the backend compares it under the row lock and returns HTTP 412 for a stale
// write, including a change between the preflight GET and PATCH. Markdown raw
// HTML and embedded images are disabled; links use markdown-it's protocol
// validation.
//
// Validation: npm run typecheck, npm run build, npm run test:unit, and npm test
// (playwright.ui.config.ts). UI tests mock R1 routes and EventSource, covering
// pagination, filters, views, draft safety, rendering, mutation errors, live
// updates and mobile light/dark layouts without another backend worker.
//
// P2.7 (AEON-31) adds /agents, /runs/:runId?, /approvals and /pacing.
// src/lib/agents.ts mirrors the R2 human-session API. Agent accounts show
// daemon/principal bindings and probes; administrators may change account state.
// Run details expose durable status, requested/effective model evidence, pinned
// profile, timestamps and aggregate content-free token/cost telemetry. Approval
// decisions require a person identity and an explicit review step; conflicts
// preserve the reason, expired requests cannot be decided, and revocation does
// not rewrite historical approval decisions. Pacing previews use the canonical
// steady/frontload/unrestricted curves and submit bounded allowance windows.
// All writes go to the owning backend modules, which own tenant transactions
// and events. This package performs no database queries or server wiring.
//
// src/lib/agents.ts owns the agent SSE connection. src/lib/streamHealth.ts checks
// stream health at a 5-second interval and reconnects after 45-second silence
// while visible and online, or on browser wake signals. Named R2 hints and
// reconnections refresh authorized projections; a latest-request fence discards
// stale responses. Connections and timers close on unmount, and user drafts
// survive refresh. The coordinator
// should align named hints with the final backend event writers. Polling covers
// additional names without inventing projection data from event snapshots.
//
// Contract gaps for coordinator integration (api/openapi.yaml at R2 handoff):
// no agent/session directory, no human run list, no run telemetry history GET,
// no allowance-window GET or windows on AgentAccount. Therefore /agents lists
// registered accounts, /runs opens by UUID or an approval link, and /pacing
// previews/creates policy without claiming live allowance counters. The created
// window is explicitly a creation snapshot. Approval has no revoked field, so
// revocation is reported as action feedback, not an inferred active-grant state.
// GET /approvals has limit but no cursor/filter; UI labels the 200-row bound.
// Do not use the agent-only queued-run, claim, probe or telemetry-write APIs
// from the browser. Full session and live allowance lists need coordinator-owned
// read contracts before this frontend can expose them.
//
// R2 verification adds tests/agents.spec.ts (mocked routes and EventSource) for
// account actions, telemetry, approvals/revocation, expiry/identity restrictions,
// pacing curves/validation, stale reads, failures, cleanup and mobile themes.
// API wire and pacing math checks also run under npm run test:unit. Run npm test
// in a host context that permits Chromium's macOS Mach-port registration; the
// restricted worker sandbox prevents browser startup before assertions run.
package web
