// SPDX-License-Identifier: AGPL-3.0-only

// Package sessionusage parses offline Codex JSON and Cursor result records
// into the cumulative session-usage body US1 accepts.
//
// Vendor prompt, tool, and result text is never copied into the result.
// Token prices are not estimated. A vendor cost, when present, is checked
// with the same microdollar conversion as internal/agentd and then dropped.
package sessionusage
