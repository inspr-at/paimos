// SPDX-License-Identifier: AGPL-3.0-only

// Package approvals implements scoped agent permissions (AEON-29).
//
// New returns an httpapi.Module. The coordinator mounts it on the API server;
// this package does not edit cmd/aeon or the OpenAPI contract. Routes, under
// the /api server:
//
//	GET  /approvals
//	POST /approvals
//	POST /approvals/{approvalId}/decision
//	POST /approvals/{approvalId}/revoke
//
// An agent proposes only for itself. The presented API key is a ceiling:
// the requested scope must equal one of the key's scopes or be a dotted
// refinement of one. A proposal writes approval.proposed and grants nothing.
// A person session decides before expiry. approval.approved and the matching
// grant commit together; approval.denied and expiry grant nothing. The
// database rejects an agent decision and a grant that does not match an
// approved request. Revoke writes approval.revoked, closes the grant, and
// a repeat is idempotent (no second event).
//
// B7 adds computed risk to every Approval response and event snapshot:
// tenant-wide resources or control/deploy/delete scope segments are high;
// read-only operations (including dotted read refinements) are low; all other
// requests are medium. Risk is presentation metadata and grants no authority.
//
// AEON-171 adds agent_name to approval responses only when the caller has
// members.read or harness.read at the approval's workspace or project scope.
// The field is omitted otherwise. Project-scoped readers see only approvals
// in projects where they hold approvals.read; event snapshots omit the name.
//
// Every read and write runs inside db.InTenant. The mutation and its event
// share that transaction via events.Append. LiveGrant checks expiry,
// revocation and the acting key's ceiling; callers use it inside the
// sensitive action's own tenant transaction. A grant matches one resource
// exactly (a tenant grant does not cover a node or a run).
// Every approval route emits Aeon-Contract: approvals/1.0. Additive optional
// response fields require a minor bump; breaking changes require a major
// bump. internal/reportercontract pins the approval response schema.
package approvals
