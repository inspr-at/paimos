# Native LEAD integration: I1 ownership handoff (AEON-1290)

This slice is **incomplete**. No runtime, route, account, daemon, CLI or vendor
configuration changed. Native public routes and native runtime callbacks remain
disabled. This document records the ownership blocker encountered before source
implementation; it does not describe a delivered pilot.

The full brief at
`/Users/markus/.aeon/lead/aeon/briefs/aeon-1290-i1-pilotwire.md` requires:
"Write ONLY inside your exclusive_write_set. If you need a file outside it,
STOP and say so in your summary. Never touch another slice's files."
The normative `plan.json` I1 write set does not include the files below. The R0
grant permits metadata rows for this slice's additions, not these source edits.

## Required ownership clarification

| Outside-scope file | Concrete dependency |
| --- | --- |
| `internal/agentaccounts/groups.go` | `quotaOccupancyExcept` counts only account-backed `agent_runs`. `slotCount` selects that shared-quota count whenever an account has a quota fingerprint. Existing selection, readiness and routing callers therefore ignore native roots even if the per-account count in the in-scope `store.go` is changed. Acceptance 3 requires native root holds in every existing admission path, including aliases that share quota. |
| `internal/agentaccounts/native_root.go` | `requireNativeCapacityTx` calls the existing per-account and quota counters, then independently adds unreleased null-run native roots to both maps. Expanding the shared counters without adapting this caller would double-count roots, including the current claim on replay, and defeat its `exceptSession` projection. |
| `internal/nativesessions/module.go` | The frozen C0 `New(Services)` integration constructor always returns `feature_disabled`, and `Module` has no service fields. `cmd/aeon/serve.go` mounts `NewDisabled`. Wiring qualified services through this existing public module requires implementing its reserved constructor and authorized handlers. This file is reserved outside I1's listed write set. |

The coordinator must assign these changes explicitly or supply an approved
alternative integration seam before implementation resumes. No duplicate public
module, partial admission workaround or ownership transfer is inferred here.

The counter repair must retain the exact AEON-1251 account-backed worker
predicate and conservative `exit_unconfirmed` worker holds. Native occupancy
must count only `run_id IS NULL AND released_at IS NULL`, without filtering by
heartbeat, daemon generation, idle state or viewer attachment. The native
capacity caller must exclude exactly its own root on claim replay and count
siblings once. A shared-quota alias test must verify both existing worker
admission and native claim/retry; simply testing the account's own count is
insufficient.

## Acceptance handoff

| Item | Result |
| --- | --- |
| A1: C0 Host/Launcher, start Store, relay, durable receipt/final sink and attach wiring | INCOMPLETE. No composition or fake-native integration smoke was implemented after discovery of the ownership blocker. |
| A2: person-invoked path separate from automatic LeadRuntime | INCOMPLETE. Existing disabled callbacks were preserved; no new path was installed. |
| A3: native roots in all legacy account/host admission paths; exact root context | BLOCKED by the counter ownership above. No partial counter changes or fake worker run were introduced. |
| A4: account default, confined adapter and legacy CLI compatibility; strict inventories | INCOMPLETE. Existing source was preserved. No source, test, route, permission, migration, DSAR, fence or tier rows were added. |
| A5: fake start/input/exact turn/explicit final/interrupt/confirmed exit; detach survives | NOT RUN. `internal/nativeintegration` has not been created, so the planned integration package and smoke do not exist. |
| A6: concrete disabled pilot for coordinator self-handover | INCOMPLETE. No live LEAD switch, daemon restart or vendor process was attempted. |

Z1 remains an integration requirement: the validated, person-only registration
`terminal_viewer_url` must reach the existing "Open live terminal" link when
I1/I2 are wired. No browser or terminal-viewer code changed in this handoff.
`G_TERMS`, `G_LEAD_GRANT` and `G_P1_LIVE` remain coordinator/person decisions;
no review, enablement or 48-hour live-soak evidence is claimed.

## Verification

PASS: PPM instance `ppm`, tenant INSPR and principal `workstation-agents` were
verified. The ticket contains the coordinator-recorded builder marker for
`aeon-1290-i1-pilotwire`, session `cfd43c1e-4ff5-46ef-a9f3-be1c9974fc89`.
The worker doctrine, both normative references, common rules, I1 JSON/Markdown
specification and C0 amendments were read before the ignored progress file and
this explicitly assigned documentation path were written.

PASS: read-only inspection identified the named counters, shared-quota callers,
native caller's additional root sum and disabled C0 constructor. No credentials,
foreign worktree, release version or source file were changed.

PASS: `node scripts/test-tiers/affected.mjs --base origin/main` exits 0. After
the handoff commit, its JSON projection reports mode `essential`, layout
`static`, reason `essential plus changed area and reverse dependencies`.
This is a selection result, not an executed test-suite pass.

PASS: `git diff --check` and the staged whitespace check exit 0. The committed
file set contains only this assigned documentation path; the ignored progress
file records that the worker handoff is closed but implementation is incomplete.

INCOMPLETE: `/Users/markus/.aeon/lead/aeon/ci-static-locked.sh` exits 3 because
both remote run slots on mbp2606 are occupied (`2/2`; CI pool `0/4`). The static
checks did not execute. No local fallback or copied runner was used. Any resumed
implementation still requires guarded checks and authoritative green PR CI.

NOT RUN: the planned implementation Go suite and negative control. No Go code
or tests changed, and the required integration package/smoke does not exist.
Browser checks are not applicable to I1. No origin push, deployment, model CLI,
review gate, live pilot or ticket-status change was performed. Acceptance and
release remain incomplete; this local commit is an ownership handoff only.
