// SPDX-License-Identifier: AGPL-3.0-only

// Package workorders implements AEON-28 / P2.3 against R2 migration 0201.
// The coordinator appends workorders.New(pool) and agentruns.New(pool, recorder)
// to httpapi.Server.Modules behind auth.Middleware. This package owns no server
// wiring, schema migration or frontend files.
//
// Person sessions can read and manage tenant orders. Agent keys need the exact
// work_orders.read or work_orders.write scope; writes additionally require the
// requesting or assigned principal. R1 Principal has no key-scope context, so
// Endpoint verifies the acting bearer key's hash, tenant, principal, expiry,
// revocation and exact scope inside db.InTenant. An approval cannot enlarge it.
// All shared endpoints finish reading at most 1 MiB before preparation or any
// transaction/fence, using the request's 30-second deadline for transport reads.
// Stalled/cancelled bodies return 408 and oversized bodies return 413 before
// preparation; ordinary handlers decode the buffered body inside their write.
//
// An order is a live work_order node plus typed detail. Creation uses the R1
// tree lock and key allocator, and appends node.created and work_order.created
// in the same transaction as its criteria. Mutators lock the order and append
// R1 events before commit. Criteria/evidence updates also advance its revision.
// Done requires all criteria checked, some attached evidence, and no queued or
// live runs. A done order must be reopened before criteria can be unchecked.
// Evidence may refer only to criteria/runs on this order and nodes in its tenant.
//
// Budgets apply to the sum of all runs, including server-measured elapsed time
// for live runs. Zero cost is an exhausted ceiling. Dispatch requires ready or
// running and remaining budget; actual telemetry is retained even if it exceeds
// the ceiling, atomically marking the order blocked. The daemon must also stop
// its local process when its budget is exhausted; reporting cannot undo usage
// already incurred. Cancelling an order prevents new dispatch but is not proof
// that an existing local process has stopped.
//
// GET /work-orders returns an array ordered by node UUID. Link and X-Next-Cursor
// response headers expose the opaque tenant-bound continuation, with limit
// 1..200 (default 50). Run integration
// tests in internal/agentruns use dbtest's NOBYPASSRLS role for both modules.
package workorders
