// SPDX-License-Identifier: AGPL-3.0-only

// Package stagehandoff implements one fenced request, ordered evidence and a
// terminal result for compiled stage plugins. The coordinator mounts New.
// Evidence, result, launch admission and launch consumption require an active
// agent principal in the handoff's tenant whose stored name is the routed
// plugin ID (Janus for prepare/apply, Pharos for deploy/verify), plus that
// agent's scoped key and live grant. Each write checks the principal while
// holding the handoff lock in the same tenant transaction.
//
// # Native candidate artifacts
//
// PUT /api/projects/{projectId}/releases/{releaseId}/candidate-artifact records
// a typed built artifact without classic batch provenance. The active requester
// (or an authorized project stage_handoffs.decide holder) also needs project
// stage_handoffs.write; keys carry that same ceiling. Exact attempt, epoch and
// journey revision, current authority, release state and live gates are checked.
// The first registration pins a null release scheme/version without incrementing
// revisions. Every later native or compatibility receipt for the release must
// preserve the complete artifact identity. The existing immutable build-evidence
// row carries the native receipt and QA digest; exact same-principal replay is
// historical and creates no authority or event. GET at the same path requires
// stage_handoffs.read and returns the version pin plus the latest recorded
// artifact metadata, or a null registration before the first receipt.
//
// # Launch readiness
//
// Kind launch_readiness is Pharos observation posted by the routed principal
// on POST /api/stage-handoffs/{id}/evidence. It is allowed on an active Pharos
// deploy handoff. The fields are reviewed_plan_digest (lowercase sha256 hex),
// host, all_host_eval_passed, target_build_passed, backup_ready,
// backup_observed_at, restart_required, running_kernel, expected_kernel,
// observed_at and authority_epoch, plus the evidence sequence and outcome.
// Flags are the authority. The outcome word does not admit a launch. Other
// evidence kinds must omit these fields.
//
// Aeon's artifact record is stage_handoff_build_evidence on that deploy
// handoff, written when a built receipt is accepted. It is the reviewed or
// approved candidate for the release, and admission also requires a live
// deploy gate (the deploy stage's gate_approval_id) and a live candidate
// gate while the release is in candidate or deploying. The mapping onto
// StageArtifact is version_scheme, version, release_channel, release_sequence,
// digest_sha256 = oci_config_digest, commit_digest, manifest_coordinate =
// release_manifest_coordinate, manifest_digest_sha256 = release_manifest_digest.
// The admit body must equal that row. The body confirms identity. It is not
// evidence. A missing row refuses.
//
// EvidenceLaunchChecks admits only when a launch_readiness row exists for
// this handoff and its current authority_epoch, all_host_eval_passed,
// target_build_passed and backup_ready are true, and observed_at is within
// LaunchFreshness (900 seconds) of Aeon's clock. The caller cannot supply
// that window. A later launch_readiness row for the handoff contradicts the
// claim when it has a false flag or a different reviewed_plan_digest, and
// admission is refused. restart_required is stored and is not an admission
// input.
//
// # Launch binding
//
// binding_digest_sha256 is hex(sha256("inspr.aeon.launch-binding.v1" ||
// 0x00 || canonical_json)). canonical_json is UTF-8 with sorted keys and no
// whitespace:
//
//	{"artifact_digest_sha256":"<hex>","authority_epoch":<number>,"handoff_id":"<uuid>","release_node_id":"<uuid>","reviewed_plan_digest":"<hex>"}
//
// authority_epoch is a JSON number. Consume recomputes the digest from the
// current handoff, the built artifact and the qualifying readiness row, and
// refuses on any drift.
//
// # Launch request replay
//
// HTTP admit and consume require a UUID Idempotency-Key. Their request digest
// is hex(SHA-256(canonical JSON request body)), with UTF-8, lexically sorted
// object keys and no insignificant whitespace, like the fixed sorted-key
// launch binding representation. The JSON release_sequence remains an exact
// integer; no binary float enters the digest. A receipt binds handoff, action,
// principal, key, body digest and the original response. The routed principal
// is checked before replay. Exact replay writes no event and is available
// through 24 hours after a terminal result; a new key follows the normal
// one-use admission and consumption rules. GET reports the handoff's attempt,
// latest superseding attempt ID, and current authority_open status. Exact
// launch replays retain their original receipt fields while recomputing
// authority_open; a superseded or closed handoff reports false. Neither status
// nor a stored receipt grants permission for a new write. The coordinator
// mounts New or NewService as httpapi.Module and supplies the Pharos launch
// checks.
// Reporter responses carry Aeon-Contract headers: stage-handoffs/1.0 for
// create/read, stage-evidence/1.0, stage-result/1.0, stage-launch/1.0 for
// admit/consume, and baseline-batches/1.0 for classic alias/built receipt.
// The version stays out of JSON for strict Pharos parsers. An optional
// response addition requires a minor bump; a removal, type change or newly
// required field requires a major bump. internal/reportercontract pins the
// OpenAPI response schemas; scripts/reporter-contract.go since <git-ref>
// reports changes for release announcements.
package stagehandoff
