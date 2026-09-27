// SPDX-License-Identifier: AGPL-3.0-only

// Package sessionusage normalizes bounded usage metadata into US1's
// cumulative per-session/model reports. It opens no vendor stores, invokes no
// harness, sends no HTTP, and estimates no prices. Fixtures are not live capture.
//
// ManagedCodex is the incremental app-server path used by agentd for fresh
// threads. It shares the strict counter/identity schemas below, retains no raw
// stream and splits model changes only when last usage proves the full interval.
// Its caller binds the returned counters to the registered Aeon session, assigns
// immutable receipts, retries HTTP and owns final settlement. Unlike the offline
// path it retains bounded normalized state only for the current daemon lifetime.
// Cursor's managed ACP cost-only usage_update is not a terminal result.usage
// record and does not supply tokens to this path.
//
// Offline Parse capture contract:
//   - One Aeon session maps to one source session, beginning at zero usage. Set
//     FromStart explicitly for the initial complete capture. A resumed thread,
//     missing history, a rotated log, and multiple source sessions are unsupported.
//   - Every call replays the FULL prefix (max 1MiB, 64KiB/line, 128 unique usage
//     observations). Supply Previous to verify the unchanged prefix and bindings.
//     A checkpoint is local continuity evidence, not authenticated provenance.
//   - Bind an exact Model whenever the vendor omits it. Thread-cumulative Codex
//     usage requires this fixed model for the entire source session: its totals
//     cannot safely be split by the model of the latest turn. Explicitly modeled
//     deltas may contribute to multiple models; Auto is not exact attribution.
//   - Deltas require a stable event identity. Cursor's request_id suffices when
//     present. Otherwise the capture hook must persist an envelope before retry:
//     {"record_id":"<UUID>","event":<vendor record>}. In particular, documented
//     Codex turn.completed lacks a stable turn ID. Assigning a fresh ID to a
//     duplicate event defeats deduplication and violates the capture contract.
//   - Unknown counters stay null. Cursor inputTokens excludes cache categories;
//     inclusive input is input+cacheRead+cacheWrite and cached input is cacheRead.
//     Missing Cursor cache categories make inclusive input unknown. No cost,
//     prompt, tool, result text, source identity, or source key enters a report.
//   - Final is explicit, and means the ENTIRE session has ended. Default reports
//     remain provisional even with known counters. Unknown counters stay
//     provisional even at Final. Finalized captures cannot be extended.
//
// Offline transport contract (coordinator-owned): serialize one writer per Aeon session;
// persist the full Result before sending any Reports; send each report body to
// POST /api/projects/{projectId}/harness-sessions/{sessionId}/usage using the
// existing worker-proof transport; after an uncertain response retry the EXACT
// saved body. Sequence and UUID are deterministic across re-parsing unchanged
// input/options. Same identity with a different body is intentionally a US1
// conflict, never a new randomly generated receipt. Do not mix these sequences
// with another reporter or recover missing history by adding server totals.
//
// External capture hook proposal (coordinator-owned): at the already-running process's
// structured stdout/JSON-RPC decoder, project only usage counters, actual model,
// and source/record identities. Discard text before persistence. Check identities
// against dispatch metadata, attach/persist a stable event UUID when required,
// then append one complete JSON line to a bounded capture. Preserve duplicate
// native IDs. Stop reporting on lost continuity, unsupported fields or bounds.
// Codex exec emits turn.completed (delta); app-server thread/tokenUsage/updated
// emits thread totals. Cursor terminal result usage is a per-turn delta. Capture
// init/context identity/model changes as metadata too; never substitute global
// model defaults or Cursor's human-readable init model name for an exact ID.
// Interactive sessions without a supported hook remain unreported; do not read
// transcripts or auth stores to backfill them.
//
// The CLI is scripts/session-usage-parse.go. For a fresh synthetic capture:
//
//	usage-parser --source codex --session-id UUID --source-session-id ID \
//	  --model EXACT --from-start < usage.capture.jsonl > result.pending.json
//
// For later full prefixes replace --from-start with --checkpoint-file PATH
// (the previous Result.checkpoint object). Save stdout durably before POST;
// extract only Result.reports for the endpoint. Keep checkpoints local. These
// commands consume existing metadata captures; they do not capture live usage.
//
// Source format references:
// https://developers.openai.com/codex/noninteractive
// https://cursor.com/docs/cli/reference/output-format
// Cursor's staff clarification of input/cache categories and per-turn scope:
// https://forum.cursor.com/t/discrepancies-between-cursor-cli-usage-and-billing/164471/7
package sessionusage
