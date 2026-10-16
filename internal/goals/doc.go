// SPDX-License-Identifier: AGPL-3.0-only

// Package goals implements the goal/outcome node model from AEON-1456 concept
// r3. Goals use node fields and serves/asks_for relations, never a second work
// tree. All derivations take already visibility-filtered inputs.
//
// This first build slice supplies storage, validation and shared derivations.
// HTTP/CLI writes, worker-ETA events and the event-backed reads are subsequent
// slices; this package does not yet mount an API or start a background job.
package goals
