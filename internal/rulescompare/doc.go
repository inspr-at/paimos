// SPDX-License-Identifier: AGPL-3.0-only

// Package rulescompare reports one offline comparison of explicit instruction
// files, supplied AR1 merge metadata and AEON-219 provenance metadata.
//
// Files the operator names are expected inputs. Their raw SHA-256 identifies
// those bytes. Canonical provenance metadata with an explicitly matching
// session can report a raw hash match. Offline JSON cannot prove its API
// origin. Proposal-shaped input stays an unverified comparison. A logical
// name or normalized line hash is not a raw match. None of this verifies
// model load or obedience.
//
// Compare does not wait, publish, read directories, or replace active
// instruction files. Supplied merge metadata cannot prove publication or the
// trusted floor; rollout remains unauthorized.
//
// LoadChain plus DiffChain is the one-time harness comparison. It reads the
// allowlisted CLAUDE.md / AGENTS.md chain for an explicit repo and home,
// then diffs those rules against a merged bundle the caller already fetched.
// It does not list directories, follow imports, call the API, publish, wait,
// or replace instruction files. An empty home skips user-level files.
package rulescompare
