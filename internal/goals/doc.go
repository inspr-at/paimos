// SPDX-License-Identifier: AGPL-3.0-only

// Package goals implements AEON-1456 concept r3 with goal/outcome nodes,
// node fields and serves/asks_for relations. New mounts the transactional
// create/update and filtered read/history/series API. Run revalidates only
// time-driven passed flips; ETA captures belong to their source writes.
// Historical progress and stall derive from visibility-filtered events.
package goals
