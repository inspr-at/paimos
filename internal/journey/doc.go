// SPDX-License-Identifier: AGPL-3.0-only

// Package journey defines AEON R3's journey, cited intake, stage handoff and
// first-party plugin contracts. api/openapi.yaml and migrations 0300-0303 are
// the executable handoff. This package intentionally has no handlers or shared
// server wiring. The coordinator mounts builders' httpapi.Module values and
// reconciles shared contract changes.
// GET /api/projects/{projectId}/journey emits Aeon-Contract: journey/1.1.
// Additive optional response fields require a minor bump; breaking changes
// require a major bump. internal/reportercontract pins its response schema.
//
// Journey face (accepted INSPR Flow prototype, read-only source
// /Users/markus/Code/inspr-flow-next-20260922) is the Journey view of the
// project page, /p/:projectKey?view=journey&stage=<stage>; earlier
// /projects/:projectId links redirect there. The stage shown by default is the
// one GET /projects/{projectId}/journey returns. Each project is one R1 project node.
// The eight stages are Inspire -> Shape -> Requirements -> Plan -> Build ->
// Deploy -> Access -> Live. Stage and exactly one next action are projections,
// not mutable node fields. Derive them from an accepted brief, the human Shape
// decision, the agreed requirements revision, the current release node state,
// R2 gate decisions, and terminal stage handoff results. Never infer progress
// from a worker heartbeat, timer, forecast, or a client-supplied stage string.
// Each stages[] entry names its gate_scope, includes gate_approval_id as
// historical identity and gate_live as the server's current approval/grant
// validity check. Plan reports Build and Build reports Candidate, including
// when the Candidate gate is absent. Gate_live is false when none is live,
// without erasing history.
// Optional gate_offer_id, gate_offer_state and gate_offer_expires_at describe
// the selected request. A consumed live gate remains visible but cannot be
// reused for an action; gate_approval_id records the selected consumed gate.
// Preserve history when a later release starts; Live becomes the prior release
// state while Plan is current. Park/drop retains the Shape stage with Reopen.
// Personal skips Shape after a brief; Access is skipped only when the release
// has no explicit access change. Professional uses budget and scope decisions.
// Enterprise adds a named independent candidate reviewer and compliance checks.
// Profiles are stable slugs personal/professional/enterprise; the prototype's
// learn/lean/regulated identifiers are fixture aliases only. A profile change
// re-evaluates future gates but never deletes or retroactively approves a gate.
// If prerequisites are missing, next_action is present but unavailable with a
// reason. A passive wait_for_build action is the sole CTA during active build.
//
// R2 approval_requests/decisions/grants supply all human gates: Shape decision,
// Requirements agreement, Build start, candidate review, deployment and permit.
// An agent proposes the bounded scope on a project or release node; a person
// decides it via R2. The journey action checks the matching live approval,
// resource, actor, expiry and current revision, then writes its event with the
// projection update in one db.InTenant transaction. No UI click, plugin result,
// or approval on a different resource can grant authority. Enterprise candidate
// approval additionally requires a distinct person from the builder/author.
// A manually created ticket that changes agreed scope remains marked as such;
// Build start requires a fresh Requirements agreement, never silently revises
// the agreed text. Budget/cap checks compare the selected release plan with the
// current approved cap; estimates are guesses and never evidence of completion.
//
// Functional requirements are requirement nodes. Journey initialization calls
// aeon_seed_requirement_kind inside db.InTenant; R3 does not change the nine
// globally seeded R1/R2 kinds for tenants without a journey. Agreement creates one epic
// node per requirement; the epic is the feature and its R1 key is the epic key.
// Accepted Aithema breakdowns generate ticket nodes once, linked to the epic
// and optionally selected into release 1. A feature with no accepted breakdown
// stays visibly empty; the server does not invent work. People may add draft
// requirements directly. Agreement pins a digest of requirement node content;
// actions recompute it so direct R1 node edits invalidate stale gates without
// modifying R1 handlers.
// Nonfunctional requirements become
// memory/knowledge nodes or checkable criteria on generated tickets. A release
// is an R1 release node; journey_releases tracks sequence, state and explicit
// version_scheme/version. Each project keeps a ticket backlog independent of a
// release. R1 relations express implements/cites links; R3 projection rows
// make release selection, lineage and walker order queryable. Reuse the R1
// aeon_next_node_key allocator for epic and ticket keys; no parallel counter.
// The agreement handler locks the project, uses its revision and idempotency
// key, and commits nodes, links and one R1 event per mutation atomically.
//
// Vue split: components/journey/JourneyView owns loading and the one next
// action; JourneyRail owns the eight-step rail (navigation only; the stage's
// decision card carries the one primary button);
// InspireStage, ShapeStage, RequirementsStage, PlanStage, BuildStage,
// DeployStage, AccessStage and LiveStage own stage content; GateCard and
// GateApprovals render R2 gates in the agents' "Needs you" pattern;
// ReleaseWalker and WalkerBar own the full-screen A3 walker (screens are the
// ticket's image attachments). Typed requests live in web/src/lib/journey.ts,
// the projection in web/src/stores/journey.ts and stage data in
// web/src/lib/useJourneyData.ts. Use Vue script setup, design tokens and
// centered SVG icons.
// Port directly from app.js: STAGES/labels, stage bar geometry, next-action
// copy, grouped ticket display, keyboard map, screen filmstrip, search/jump,
// compare and zoom controls, and drag-to-scroll behavior (pointer threshold
// prevents accidental ticket activation). Port from lab.html row A3: feature
// arrows over ticket arrows, tri-state all/some/none checkboxes with a local
// remembered partial set, first-ticket jump on feature label, right-aligned
// epic key as the only feature link, fade-aware draggable header, and shortcut
// sheet. Adapt the A3 CSS from style.css/lab.html into token-based Vue styles.
// Keyboard focus and reduced-motion behavior survive the port. The server
// persists ticket order and release inclusion via one revision-fenced plan PUT;
// tri-state and remembered partial picks are client derivations. The walker
// GET includes node IDs and keys, so no fixture IDs, localStorage state,
// simulated agent ticker, synthetic screenshots, heuristic classification, or
// guessed release version is treated as real data. Navigation arrows wrap
// within the ordered tickets; Shift+arrows move between feature boundaries.
//
// Aithema intake (AIT-34) owns conversation turns. Each source records kind,
// public/symbolic locator, file reference where applicable, digest, actor and
// tenant. Transcripts append immutable turns with a stable source ordinal.
// A brief or requirement draft is an immutable agent proposal with citations
// to source and optional turn plus a stable locator. Citation membership,
// speaker provenance, same-project references and digest are validated before
// write. Source or transcript text is never copied into the tenant-wide event
// payload; events contain resource IDs and safe metadata. Functional requirement
// drafts may include immutable ordered ticket suggestions; only accepted ones
// feed agreement. File content stays behind Aeon's tenant-scoped file storage
// boundary; the intake handler verifies file ownership before linking file_id.
// A draft records the target's last event ID.
// Accept is a person-only compare-and-swap: if the target changed after that
// event, return 409 and preserve both the person's edit and the proposal for
// reconciliation. New drafts create nodes only upon explicit acceptance.
// Later Aithema drafts never patch an accepted or person-edited node. All
// accepted text changes append R1 events; old drafts and citations stay intact.
// Agents require a scoped key and, where needed, a matching R2 grant; neither
// the conversation component nor a citation is an approval of requirements.
//
// Stage handoff v1 is one request -> zero or more evidence facts -> one terminal
// result. A request pins project/release node, stage, operation, compiled plugin, attempt,
// journey revision, authority epoch, expiry, evidence ceiling, plan digest,
// predecessor digest, context digest and a sealed required-dependency set.
// The server resolves those values from current state; callers provide only
// stage selection, expected revision and idempotency key. A new attempt gets
// a new authority epoch; old attempts remain readable but cannot report.
// Evidence sequence is monotonic; an exact replay is idempotent, a divergent
// replay conflicts. A result references terminal evidence and succeeds only
// when current authority, approved gate, required dependency seal, artifact
// identity and policy all match. Stale, missing or failed evidence blocks the
// stage. Every transition appends a tenant event. The read API exposes safe
// metadata only, never credentials, callback material or provider payloads.
// Journey stages expose the selected handoff ID with its attempt and authority
// epoch so a reporter can compare it with its own pinned handoff before acting.
//
// This replaces classic paimos/backend/externalstage and baselinebatch's
// external HTTP handoff. Preserve their useful invariants: immutable attempt
// and plan lineage, authority fencing, exact replay semantics, typed evidence
// ceilings, Pharos artifact scheme/channel/sequence/manifest identity, fresh
// prerequisite seals, one-use launch admission, observed-vs-guessed progress,
// explicit blocker codes, and no fabricated baseline revision. The in-process
// plugin call removes the one-time handoff secret, credential epoch and remote
// pull/accept/heartbeat protocol. Imported legacy claims remain marked as
// claims; they cannot satisfy a fresh R3 gate without current verification.
// Pharos owns Deploy: its deploy and verify operations check reviewed artifact
// identity, current backup/readiness, consume one launch admission before host
// change, report deployment and verification facts, and may refuse stale evidence.
// Janus owns Access: prepare can run before deployment as a required Pharos
// prerequisite; it does not itself advance the Access stage. Apply follows a
// successful deployment and consumes a person-approved bounded permit, then
// reports only authorized and credential_ready booleans with observed time.
// No principal IDs, grants, secret values, paths, digests, commands, URLs or
// arbitrary text pass through
// Janus evidence. Required Janus checks seal before a dependent Pharos launch;
// an empty or optional-only set cannot prove a prerequisite. A plugin cannot
// turn a failed result into a release or skip a person decision.
//
// Plugin API v1 (ADR-001 decision 10): first-party Go packages register a
// compile-time typed Manifest with ID, exact version/digest, owner, permission
// ceiling, node kinds and JSON field schemas, views/panels, workflow steps and
// gates, agent tools, integrations and background jobs. Registry.Register
// rejects duplicate IDs, unknown permission names, schema collisions and a
// digest mismatch at startup. Typed interfaces are NodeKindContributor,
// ViewProvider, StepPlugin (Evaluate/Request/ApplyResult), ToolProvider,
// IntegrationProvider and JobProvider. Each call receives context, tenant and
// principal plus a narrowed capability set; plugins never get a raw global DB
// pool, environment map or unscoped HTTP client. Manifest declarations are
// ceilings, and per-tenant plugin_installations pins a digest and a permission
// subset. Only a tenant admin can enable/change an installation; every change
// writes a tenant event. Disable prevents new work, while historical records
// retain the manifest identity. A mismatch fails closed. There is no uploaded
// executable, interpreted extension, arbitrary SQL or runtime network load.
// Pharos and Janus are initial StepPlugin and IntegrationProvider instances;
// Aithema adds its panel/tool/jobs through the same registry. Background jobs
// run under tenant-scoped leases and the manifest's narrow capability set.
//
// Worker file ownership (AEON-33 handoff; builders do not edit shared files):
//   - journey projection/actions: internal/journey/*.go except doc.go;
//     /projects/{projectId}/journey*, migration 0300; owns stage derivation.
//   - requirements/release walker: internal/requirements/*.go and
//     internal/releases/*.go; /requirements* and /releases/*; 0300.
//   - Aithema intake: internal/intake/*.go; /intake/*; 0301. AIT-34 owns the
//     conversation component and uses this contract for persistence.
//   - stage handoffs: internal/stagehandoff/*.go; /stage-handoffs/*; 0302.
//   - plugin registry: internal/plugins/*.go; /plugins/*; 0303. Pharos and
//     Janus implementations live under internal/plugins/pharos and /janus.
//   - Vue face: web/src/components/journey/*.vue (in the project page),
//     web/src/lib/journey.ts, web/src/lib/useJourneyData.ts, web/src/lib/usePlan.ts
//     and web/src/stores/journey.ts.
//
// The contract worker owns api/openapi.yaml, migrations 0300-0303 and this
// doc.go. All builders consume db.InTenant, tenant.PrincipalFrom, the R1 event
// writer, R2 approvals and httpapi.Module. Each module tests tenant isolation,
// authorization, optimistic revision, idempotency, event atomicity and stale
// evidence. The coordinator alone mounts modules and conducts release QA.
//
// AEON-188 adds an explicit read-only disposable flag to the Journey document.
// The coordinator wires RunOperator into `aeon journey`; the host-only commands
// are `mark-disposable --tenant SLUG --project KEY` and
// `seed --tenant SLUG --project KEY --to-stage build|candidate|deploy
// [--brief 1|2|3]`. AEON-208's fixed Host status page, Release notes digest
// and Maintenance window planner briefs are host-only synthetic intake for
// disposable projects. The seed accepts and confirms the selected brief, then
// creates an agreed minimal requirement, feature and completed release ticket.
// It records revision-fenced action receipts and operator events with the brief
// number on every step; replaying the same brief is a no-op, and changing the
// brief after intake is refused.
// Development is the default; production host runs require --production and
// --confirm-project equal to --project. Production events record production:true
// and the Access operator. MarkDisposable appends an operator event and
// refuses deployed or released projects. SeedDisposable uses the normal
// journey revision, receipt and transition path for release opening, build
// start and candidate marking, with an explicit audited build-gate waiver on
// disposable projects. Without --brief it does not create requirements or
// complete tickets. It never decides candidate/deploy gates. A person approves
// those gates through the normal UI. An unmet work prerequisite rolls the seed
// transaction back;
// a pending human gate commits preparation and reports the pending action.
package journey
