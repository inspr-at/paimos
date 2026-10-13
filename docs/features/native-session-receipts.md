# Native session receipts and explicit final retries

AEON-1271 / AEON-1259 P3a is groundwork against the frozen C0
`native-session/1.0` contract. It remains unwired and disabled in production;
I1/I2 own the route/supervisor composition. `hidden_from_release_notes: true`.
There are no live vendor/model calls, profile changes, credential reads, new
permissions, automatic resumes or process ownership changes in this slice.

## Evidence and storage

`internal/nativereceipts` uses a dedicated owner-only subdirectory of the
protected daemon state root. `Open` holds an exclusive file lock. Regular files
are 0600, the directory is 0700, symlinks/hardlinked files and shared state are
refused, and writes commit with file fsync, atomic rename and directory fsync.
Failed disk commits close further native write admission on that handle.
Two fixed temporary names bound crash leftovers. Only the committed snapshots
are read, with byte limits before decode and strict field decoding.

`receipts.json` is content-free: exact C0 runtime binding, request/operation/input
IDs, actor ID, payload digest, expiry, process/effect digests, receipt stages and
bounded metadata-report retries. Prompt and control bodies are never accepted
by the journal API. Inputs remain in existing authorized inbox/chat storage.
The local journal holds at most 512 operations, 32 unresolved inputs per logical
session and 16 metadata reports per operation; pressure is an explicit refusal.
Uncertain operations are retained and never silently evicted.

`InputWriter` requires a current write authorizer and an already-owned C0 native
transport. While the supervisor holds its native mutation/ownership lock, the
authorizer must recheck the actor/controller and the monotonic claim deadline.
`BeginWrite` is durably committed before touching native stdin/control I/O.
Input authority/runtime/deadline are checked again after that disk barrier.
Provider evidence must match the exact request/input/runtime, increasing source
sequence and any expected effect/turn. Stop requires matching process identity,
observed root exit AND group absence through `ConfirmExit`; a final is no exit
evidence. Recovery changes an unacknowledged `write_started` to `uncertain`.
Neither retry, a fresh request/operation ID for the same input, nor metadata
replay authorizes reinjection. Late correlated native evidence may resolve the
hold. Queue acceptance never changes existing delivered/read definitions.

`FlushReports` replays only the bounded metadata prefix in order. Its durable
offset advances only after an exact matching server receipt. A lost API ack
replays the same metadata without touching native stdin. The supervisor owns
one flusher per store. `Page` is a bounded metadata-only local keyset port;
the owner-socket transport supplies caller authorization.

`finals.json` is a separately classified private outbox containing only approved
explicit completed answers, their stable final IDs, exact input/turn/binding and
original drain grant, plus retry metadata. `RetainFinal` requires the matching
durable completed-input receipt and an explicit owner retention approval;
local-history consent alone is insufficient. Interim text, reasoning, native
protocol, tool bodies, terminal frames and credentials are not outbox inputs.

The outbox holds at most 64 outcomes and 4 MiB of pending UTF-8 text, 64 KiB per
answer, further narrowed by the grant's item/byte limits. The retention deadline
is the earliest of owner approval, original drain expiry and 24 hours after
retention. It is never renewed on retry/restart. Attempts are bounded at 128;
backoff is clock-driven. API failure/lost ack/native exit retain the exact
answer. A persisted, rejected or expired receipt purges its text. Metadata stays
visible; `PruneExpired` is an explicit lifecycle cleanup after examining outcomes,
and never deletes possible native writes. Reopening enforces the original
expiry. The lifecycle owner must schedule retry/expiry processing while the
daemon is alive; after downtime, `Open` expires answers before publication.

## Server composition and authority

Migration **1347**, reserved for AEON-1271, adds two isolated tenant/project-RLS
projections, `native_session_receipts` and `native_session_finals`. No existing
schema, enum, writer or route changes. These tables contain no prompt/final
bodies. Final text remains in existing chat-isolated `inbox_messages`.

`internal/nativesessions/receipts.Repository` supplies `Report`, `Receipts` and
`Persist` seams for the future C0 Store/FinalSink composition. Every call binds
the immutable actor to the authenticated principal in context, takes the
canonical tenant -> tree fences, and requires current target permissions inside
the final mutation transaction. Metadata reports require the active paired
daemon generation and exact qualified intent. Receipt retries increment a
bounded counter without duplicating audit events; states cannot skip possible
I/O or claim delivery from queueing. Public pages reauthorize each request,
limit 25/default, 100/max, 1 MiB, and bind their opaque keyset cursor to
caller/tenant/session/runtime/binding with a five-minute lifetime. Cursor
position is not authority and cannot broaden the reauthorized scope.

The mandatory `Authority` implementation belongs to the native binding/claim
owner. Its callbacks run inside the same fenced transaction, with no network or
native I/O. Before wiring, it must validate current pairing/key/daemon/ledger
and worker proof, immutable actor/tool/settings/account grants, exact current
binding/intent, provider correlation and stop provenance. `FinalTx` additionally
checks the original unexpired drain-only grant, issued-at time, exact immutable
conversation, current retention approval and any allowed same-conversation
successor. The returned sender ceiling is the original actor's current bounded
grant, not the paired daemon's ambient authority. Missing authority fails closed.

The C0 `POST /api/agentd/native-sessions/finals` route is reserved for this final
sink. It remains C0-disabled here. A restarted daemon can drain an older exact
runtime only with its CURRENT authenticated pairing and the same original
drain-only grant; ordinary receipt/control paths keep their strict generation
check. Draining cannot launch, resume, approve, input or release occupancy.

`chat.Module.StoreNativeFinalTx` is the narrow existing chat storage helper.
It rechecks original session/role/person/project/native context and current
sender chat.receive/chat.send plus owner chat.read/target visibility. It preserves
chat RLS and legacy projection isolation. Stopped/archived writer liveness is
not borrowed as a general lease; the independent native authorizer has already
validated the exact final-only grant. The legacy live/final endpoints remain
unchanged. Stable final ID plus publication digest/actor/turn prevents conflict
or duplication, including a lost commit acknowledgement. Grant byte/item
consumption remains bounded after text is acknowledged. Notifications occur
only after commit; persisted means message stored, never input read or exit.

## Acceptance evidence and verification handoff

1. Pre-I/O journaling and honest evidence: `TestCrashBeforeNativeAckHoldsInputAndReplaysOnlyMetadata`
   and `TestReceiptEvidenceRejectsWrongInputAndFalseExit` establish the write/ack
   barrier and reject the exact wrong input/exit failure reasons.
2. Content-free bounded states/retries: `TestPrivateStoreBoundsAndRejectsSymlinkOrSharedState`
   and `TestReceiptProjectionRejectsQueueAsDeliveryAndScopesPages` cover pressure,
   privacy, idempotent metadata and tenant/target scope.
3. Approved explicit final storage: `TestFinalOutboxSurvivesOutageExitAndLostAck`
   and `TestNativeFinalStorageKeepsStoppedLeaseAndChatIsolation` cover retained
   bodies and unchanged stopped legacy lease/isolated history.
4. Outage/exit and future authorization: `TestFinalDrainReplayAfterExitPersistsOnceAndRechecksAuthority`
   uses a coordinator root with null run_id, restarted daemon and original drain
   grant; `TestFinalDrainRevocationWinsBeforeFinalWrite` observes a real blocked
   transaction behind the canonical access fence before revoking authority.
5. Crash uncertainty and at-most-once final replay: the barrier test proves one
   stdin write; the outbox/server tests replay the same final ID after losing its
   acknowledgement and assert one durable message/projection/event.

Required guarded commands (committed HEAD; no real native accounts):

```sh
/Users/markus/.aeon/lead/aeon/remote-test.sh "$PWD" ./internal/nativereceipts/... ./internal/nativesessions/receipts/... ./internal/chat/...
/Users/markus/.aeon/lead/aeon/ci-static-locked.sh
node scripts/test-tiers/affected.mjs --base origin/main
node scripts/check-migrations.mjs --base-ref v261012211543.0.0
```

The chat package's test setup generates the ignored OpenAPI bundle with the
prescribed generator when a fresh guarded checkout lacks it. The existing
legacy schema assertions stay unchanged; no authored contract is modified.

No browser check applies to this non-browser slice. Exit 3 means incomplete,
never green. Authoritative PR CI and the coordinator's cross-family review
remain prerequisites to merge; workers neither push nor run model gates.

Guarded validation of implementation commit `d01501a87` on the approved remote runner:

* **PASS 1:** pre-write journal barrier, correlated evidence and false-exit denial.
* **PASS 2:** bounded content-free journal/report retries, receipt pages and RLS.
* **PASS 3:** approved explicit final outbox and existing chat-isolated helper;
  stopped legacy final authority and legacy response snapshots stay unchanged.
* **PASS 4:** outage/exit/restart retention, original grant expiry/revocation and
  current authorization under an observed access-fence interleaving.
* **PASS 5:** crash becomes uncertain, one native write, one durable final on ID
  replay after losing its acknowledgement.
* Required three-package `remote-test.sh` run: **exit 0**, all packages pass.
* Migration guard against `v261012211543.0.0`: **exit 0**; migration checker
  tests: **43/43 pass**. Migration 1347 remains absent from origin/main.
* Offline affected selector: **exit 0**, **FULL CI required** (R4 unsupported or
  incomplete SQL selector classification for the new isolated migration).
* Guarded `ci-static-locked.sh --merge-main`: **exit 1**, **39 checks pass, zero
  optional skips**. Failures are the eight unclassified Go cases and the seven
  receipt-package files awaiting R0 source ownership (go-tiers, audit-tests and
  audit-coverage). Go vet, web type/lint/shard checks, migration checks and
  OpenAPI properties/parity pass. This is **not green**.

R0 must apply the rows below on the same PR and rerun strict static checks,
the DSAR migration inventory guard and shared-fence caller inventory before
merge. These shared files are outside P3a's exclusive write set; the builder
did not edit them. No push, deployment, live cutover or model gate was run.

## Fix round 2: incomplete metadata handoff

The supplied CI findings for PR #622 target `a79fb6901`. The full
`go-test-3-internal-agentpairing.jsonl` artifact confirms that
`TestSharedFenceCallerInventory` fails because `db.LockTree` observes
`nativesessions/receipts/repository.go` and the expected inventory omits it.
`Repository.write` already acquires the canonical tenant and tree fences before
authority/record work; the missing inventory row requires the R0 change below.
The other reported failures are the audit's missing ownership for the seven
`internal/nativereceipts` source files. These are metadata failures; the supplied
findings identify no behavior change requiring a new regression test.

On 2026-10-13, a fresh fetch confirmed `origin/main` at `828bf8697`, already an
ancestor of this branch. The central native-package audit registration had not
merged. Inspection also confirmed that the eight ESSENTIAL tier rows and both
DSAR entries below remain absent. The P3a exclusive write set and the plan's
single R0 metadata lane reserve all four affected shared files to R0;
`worker-common.md` additionally directs LEAD to fix native audit ownership
centrally. This round preserves that ownership and leaves all existing test
assertions intact. PPM/INSPR and the coordinator-recorded builder marker
`29f48451-23a0-4f6c-85b4-44a57a42526a` were verified before the handoff edit.

The guarded `remote-test.sh -count=1` attempt covered the three required P3a
packages plus `internal/agentpairing` and `internal/dsar`; both the initial run
and retry refused with **exit 3** because both remote slots were occupied.
`ci-static-locked.sh` also refused with **exit 3** for the occupied remote slots.
No tests executed in those attempts, and no heavy local fallback was run.
Browser/web unit checks do not apply to this non-browser slice. The earlier
acceptance passes above remain historical evidence, not fresh round-2 passes.

**INCOMPLETE:** both reported CI failures and the missing tier/DSAR registrations
remain same-PR R0 prerequisites. Apply the exact rows below, merge the central
audit registration once released to main, and rerun guarded Go/static checks
before the coordinator's cross-family gate. This documentation handoff does not
mark CI green or authorize merge/release.

## Fix round 3: public documentation check and remaining R0 prerequisites

The full `go-static` CI log for `bd7b9b6dc` identifies an additional failure:
the public-name guard rejects this document's operator-specific host name.
`node scripts/check-public-names.mjs` reproduces that exact failure on the
round's starting HEAD. The validation evidence now names the approved remote
runner generically; no guard, allowlist or test assertion is changed.

A fresh fetch initially confirmed `origin/main` at `06929d1a4`, already an
ancestor of this branch; PR #622 was mergeable. During verification, central
R0 audit PR #623 merged as `3507e3319`. This branch then merged that main
commit cleanly, acquiring R0's `internal/native*/**` registration in S2 and
resolving the seven unassigned receipt files without a builder ownership edit.
The supplied JSONL still confirms the missing shared-fence row. Inspection
also confirms that the eight ESSENTIAL test rows and two DSAR table entries
below remain absent.

The normative plan assigns these four shared files exclusively to the single
R0 metadata lane, and the worker rules require the native audit fix centrally.
This round preserves that ownership. Applying the remaining exact registration
request below remains necessary before CI can be green. There is no behavior
change requiring a new regression test against `bd7b9b6dc`.

Fresh guarded `remote-test.sh -count=1` on `bd7b9b6dc` completed:

* **PASS 1-5:** `internal/nativereceipts`, `internal/nativesessions/receipts`
  and `internal/chat` pass, covering all five acceptance items above.
* `internal/dsar` passes its existing package tests; the two missing table
  classifications below still require registration.
* **FAIL:** `internal/agentpairing` reproduces the exact missing
  `nativesessions/receipts/repository.go` entry in
  `TestSharedFenceCallerInventory`. The five-package command exits **1**.
* Public-name checker: **exit 1** on the starting HEAD for the document's host
  name, **exit 0** after the fix. `git diff --check` also passes.

Guarded `ci-static-remote.sh` on committed `e6247e4ab` runs the required
underlying `node scripts/ci-static.mjs --merge-main`: **exit 1**, **41 checks
pass, zero optional skips**. Public names, source audit tests/coverage, test-tier
unit tests, web shard tests, Go vet, web type/lint, migration and OpenAPI checks
pass. Its only failure is `go-tiers`, which lists exactly the eight unclassified
Go cases below. Earlier static attempts refused with **exit 3** for occupied
remote slots; no heavy local fallback was used.

**INCOMPLETE:** R0 must apply the shared-fence row, eight ESSENTIAL tier rows
and both DSAR table classifications before the next guarded static/PR checks.
The central audit fix and public documentation fix are complete. Browser/web
unit feature checks do not apply to this non-browser slice. No origin push,
deployment, live/model call or review gate was run by this worker.

## Fix round 4: confirmed R0 scope conflict

The supplied `review-ci622-0656.out` targets `c68b3becf`, which is also PR
#622's current head. Its JSONL confirms the same missing
`nativesessions/receipts/repository.go` caller in
`TestSharedFenceCallerInventory`. The complete failed `go-static` step,
job `115132305433`, identifies exactly the eight unclassified tests listed
below; it is a tier-registration failure. Both DSAR table entries below are
still absent. No new behavior finding is identified by this review.

A fresh fetch confirms `origin/main` at `3507e3319`, already merged into this
branch, and PR #622 is mergeable. There is no newer central R0 fix to acquire.
The P3a exclusive write set and normative R0 metadata lane still reserve
`internal/agentpairing/shared_fence_test.go`,
`scripts/ci/go-test-tiers.json` and `internal/dsar/inventory.json` to the
coordinator or an explicitly assigned metadata integrator. This fix brief
does not transfer those files to P3a. The exact same-PR registration request
below remains necessary; the builder does not change lock calls, assertions,
test names or inventory guards to evade it.

Hosted CI on `c68b3becf` passes all four web unit shards and all twelve browser
shards. The failed non-required `gate/policy-preview` reports that the current
head lacks a trusted verdict; only the coordinator supplies that verdict after
the metadata fixes and final validation. No review gate is run by this worker.

Fresh guarded `remote-test.sh -count=1` on `c68b3becf` completed:

* **PASS 1-5:** `internal/nativereceipts`, `internal/nativesessions/receipts`
  and `internal/chat` pass, covering the five acceptance items above.
* `internal/dsar` passes its existing inventory-parsing test. That test does
  not establish registration of the two missing native tables.
* **FAIL:** `internal/agentpairing` reproduces the exact shared-fence caller
  omission. The combined five-package command exits **1**.
* Offline affected selector: **exit 0**, **FULL CI required** for migration
  1347's unsupported or incomplete SQL classification.
* Public-name checker and `git diff --check`: **exit 0** after the handoff edit.

Fresh guarded `ci-static-remote.sh` on committed `c68b3becf` runs the required
`node scripts/ci-static.mjs --merge-main`: **exit 1**, **41 checks pass, zero
optional skips**. Its sole failure is `go-tiers`, listing exactly the eight
missing registrations below. Audit ownership/coverage, tier and web-shard
unit tests, Go vet, web type/lint, migration and OpenAPI checks all pass.

**INCOMPLETE:** R0 must apply the shared-fence row, eight ESSENTIAL tier rows
and both DSAR classifications in the same PR. No behavior change or assertion
change was made; this round has no new regression test requiring a negative
control against `c68b3becf`. Source/test code and shared inventories remain
unchanged. No origin push, deployment, model CLI or review gate was run.

## Fix round 5: R0 registration remains the blocking repair

The supplied `review-ci622-0741.out` targets `3dabbddd4`. Its complete
`go-test-3-internal-agentpairing.jsonl` artifact confirms the same missing
`nativesessions/receipts/repository.go` entry in
`TestSharedFenceCallerInventory`. The complete failed `go-static` step,
job `115143457597`, fails strict Go classification for exactly the eight
tests in the registration request below. The selector regression step's
synthetic failure output is fixture evidence, not an additional CI failure.

An initial fetch finds `origin/main` at `bf4aa778f`; during guarded
verification it advances to `a633f5118` (P1b start intents). The latest main
and working branch still lack the receipt fence row, eight tier registrations
and two DSAR classifications. Migration 1347 remains absent from main.
PR #622 is mergeable at the start, and a fresh local merge-tree check against
the latest main confirms no conflict requiring a merge. The current
PPM/INSPR principal and coordinator-recorded P3a builder marker were verified
before this documentation change. The ticket, P3a exclusive write set and R0
metadata lane contain no transfer of the three remaining shared files.

The original brief explicitly requires stopping when a repair needs files
outside P3a's write set. R0 remains the sole authorized owner of
`internal/agentpairing/shared_fence_test.go`,
`scripts/ci/go-test-tiers.json` and `internal/dsar/inventory.json`. The
builder therefore stops the code repair and preserves all lock calls,
test names, assertions and inventory guards. R0 must apply the exact
same-PR registrations below before the next fix/gate round.

Hosted CI on `3dabbddd4` passes all four web unit shards and all twelve
browser shards. No feature browser or web unit test applies to this
non-browser slice. The failed `gate/policy-preview` remains a coordinator
verdict prerequisite; this worker does not run that gate. This round changes
documentation only, so no behavior regression or negative control against
`3dabbddd4` is introduced.

Fresh guarded `remote-test.sh -count=1` on `3dabbddd4` completed:

* **PASS 1:** pre-write journal barrier and correlated input/exit evidence.
* **PASS 2:** bounded content-free receipt metadata, retries and scoped pages.
* **PASS 3:** approved explicit final outbox and isolated native chat storage.
* **PASS 4:** outage/exit retention and current fenced final authorization.
* **PASS 5:** crash uncertainty prevents reinjection; final ID replay stores once.
* Both receipt packages and `internal/chat` pass. `internal/dsar` passes its
  existing package tests; the two missing table classifications still need R0.
* **FAIL:** `internal/agentpairing` reproduces the missing receipt caller in
  `TestSharedFenceCallerInventory`. The five-package command exits **1**.
* Offline affected selector: **exit 0**, **FULL CI required** for migration
  1347's unsupported or incomplete SQL classification. Public-name and
  `git diff --check` checks pass after this documentation edit.

Guarded `ci-static-remote.sh` on committed `3dabbddd4` initially refuses
with **exit 3** because both remote slots are occupied. After the Go run
finishes, its retry runs `node scripts/ci-static.mjs --merge-main` and
exits **1**: **41 checks pass, zero optional skips**. The sole failure is
`go-tiers`, listing exactly the eight R0 registrations below. Source audit,
tier/shard regression tests, Go vet, web type/lint, migration and OpenAPI
checks pass. No heavy local fallback is run.

**INCOMPLETE:** the blocking fence/tier repair and both DSAR classifications
still require R0's same-PR edit. This round supplies fresh evidence and a
scoped documentation handoff. Source/test code and shared inventories remain
unchanged. No origin push, deployment, model CLI or review gate is run by this
worker.

## Fix round 6: repair remains exclusively R0-owned

The supplied `review-ci622-0834.out` and its full agentpairing JSONL target
`6e4b665b8`. They confirm the missing receipt repository in the shared
`db.LockTree` caller inventory. The complete failed `go-static` log for job
`115152143035` confirms exactly the eight unclassified tests listed below;
the selector tests' synthetic failure output is fixture evidence. Both DSAR
classifications below also remain absent.

PPM / INSPR / workstation-agents and the coordinator-recorded builder session
`8d10b6c5-f68b-4547-916e-5d651c26519c` were verified before this handoff edit.
A fresh fetch finds `origin/main` at `fd8fae22f`. A merge-tree check is clean,
so the fix brief requires no conflict-resolution merge. Neither this branch
nor main contains the three outstanding registrations; migration 1347 has
no main collision. All four web unit shards and twelve browser shards pass
on the current PR head. No feature web unit or browser check applies to P3a.

The normative plan still assigns `internal/agentpairing/shared_fence_test.go`,
`scripts/ci/go-test-tiers.json` and `internal/dsar/inventory.json` exclusively
to R0. The original brief requires stopping a repair that needs files outside
P3a's write set, and neither the current ticket nor fix6 transfers ownership.
The builder therefore stops the code repair at that boundary. The exact
same-PR request below already supplies every required row. R0 must apply it
before another P3a fix/gate round can resolve these failures.

Fresh guarded validation on committed `6e4b665b8`:

* **PASS 1:** durable pre-write journal and correlated input/exit evidence.
* **PASS 2:** bounded content-free receipt states, retries and scoped pages.
* **PASS 3:** approved explicit final outbox and isolated native chat storage.
* **PASS 4:** outage/exit retention and current fenced final authorization.
* **PASS 5:** crash uncertainty prevents reinjection; final ID replay stores once.
* Both receipt packages, `internal/chat` and `internal/dsar` pass. The DSAR
  package result does not establish the two missing table classifications.
* `internal/agentpairing` reproduces the reported missing receipt fence caller;
  the combined five-package command exits **1**. The first attempt refused
  with **exit 3** for remote load; the guarded retry executed, with no local run.
* Guarded remote static `--merge-main`: **exit 1**, **41 checks pass, zero
  optional skips**; the sole failure is the eight missing Go tier rows.
* Offline affected selector: **exit 0**, **FULL CI required** for migration
  1347's unsupported or incomplete SQL classification. Public-name and
  `git diff --check` checks pass after this handoff edit.

**INCOMPLETE:** no behavior, assertion, source ownership or shared inventory
change is made. No new behavior regression or negative control against
`6e4b665b8` is required for this documentation-only handoff. The coordinator
still owns the trusted review verdict and all merge/release decisions. No
origin push, deployment, model CLI or review gate is run by this worker.

## Exact R0 registration request (same PR, before merge)

R0 exclusively owns the following metadata files. They are deliberately not
edited by P3a. Apply these rows on this branch before final static/PR checks;
there is no deferred inventory allowance.

* Source ownership: **resolved** by central R0 PR #623, merged in fix round 3.
  S2 (`agent-runtime`) now includes `internal/native*/**`, covering all seven
  receipt files. Existing S2 `internal/nativesessions/**`, S1 `internal/db/**`
  and S3 `internal/chat/**` cover the other source/test/migration files.
* Shared fence inventory: add `nativesessions/receipts/repository.go` to
  `db.LockTree` in `internal/agentpairing/shared_fence_test.go`, sorted with the
  existing rows. Entry order is authenticated context -> tenant -> tree ->
  authority/target/receipt rows -> existing chat parent/FK locks -> event counter
  last. No new `db.LockTenant`, permission, route table, contract pin or web
  shard row is required.
* Register these Go cases as **ESSENTIAL** in `scripts/ci/go-test-tiers.json`
  through the established tier CLI:

```json
[
  {"kind":"go","package":"internal/nativereceipts","name":"TestCrashBeforeNativeAckHoldsInputAndReplaysOnlyMetadata","tier":"ESSENTIAL"},
  {"kind":"go","package":"internal/nativereceipts","name":"TestFinalOutboxSurvivesOutageExitAndLostAck","tier":"ESSENTIAL"},
  {"kind":"go","package":"internal/nativereceipts","name":"TestReceiptEvidenceRejectsWrongInputAndFalseExit","tier":"ESSENTIAL"},
  {"kind":"go","package":"internal/nativereceipts","name":"TestPrivateStoreBoundsAndRejectsSymlinkOrSharedState","tier":"ESSENTIAL"},
  {"kind":"go","package":"internal/nativesessions/receipts","name":"TestFinalDrainReplayAfterExitPersistsOnceAndRechecksAuthority","tier":"ESSENTIAL"},
  {"kind":"go","package":"internal/nativesessions/receipts","name":"TestFinalDrainRevocationWinsBeforeFinalWrite","tier":"ESSENTIAL"},
  {"kind":"go","package":"internal/nativesessions/receipts","name":"TestReceiptProjectionRejectsQueueAsDeliveryAndScopesPages","tier":"ESSENTIAL"},
  {"kind":"go","package":"internal/chat","name":"TestNativeFinalStorageKeepsStoppedLeaseAndChatIsolation","tier":"ESSENTIAL"}
]
```

Add these exact `internal/dsar/inventory.json` rows. Both personal tables use
`tenant_id + owner_person_id` as the locator; original input/publication digests
are conservatively personal. Receipt JSON has only the validated C0 metadata
shape, including personal observation timestamps; it has no content fields.

```json
[
  {
    "classification":"personal",
    "table":"native_session_receipts",
    "locator":{"tenant_column":"tenant_id","person_column":"owner_person_id"},
    "columns":{
      "tenant_id":"metadata","operation_id":"metadata","request_id":"metadata",
      "project_id":"metadata","owned_session_id":"metadata","owner_person_id":"personal",
      "actor_principal_id":"personal","payload_sha256":"personal","expires_at":"personal",
      "receipt":"personal","created_at":"personal","updated_at":"personal","attempts":"metadata"
    }
  },
  {
    "classification":"personal",
    "table":"native_session_finals",
    "locator":{"tenant_column":"tenant_id","person_column":"owner_person_id"},
    "columns":{
      "tenant_id":"metadata","final_id":"metadata","project_id":"metadata",
      "owned_session_id":"metadata","owner_person_id":"personal","sender_principal_id":"personal",
      "conversation_id":"metadata","drain_grant_id":"metadata","binding":"metadata",
      "input_id":"metadata","turn_ref":"metadata","publication_sha256":"personal",
      "text_bytes":"metadata","message_id":"metadata","state":"metadata","reason":"metadata",
      "created_at":"personal","updated_at":"personal"
    }
  }
]
```

Private local files are classified separately: `receipts.json` contains personal
actor/time/digest metadata; `finals.json` contains personal approved final text
and owner/input/turn/delivery metadata. The local locator is owner state root +
tenant/owned session, narrowed to the original owner approval. Neither file
contains vendor credentials or a shared-account capacity proxy. Only workspace
owners' own accounts may be used by later qualified native wiring.
