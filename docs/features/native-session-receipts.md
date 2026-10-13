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

Guarded validation of implementation commit `d01501a87` on mbp2606:

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

## Exact R0 registration request (same PR, before merge)

R0 exclusively owns the following metadata files. They are deliberately not
edited by P3a. Apply these rows on this branch before final static/PR checks;
there is no deferred inventory allowance.

* Source ownership: add `internal/nativereceipts/**` to S2 (`agent-runtime`).
  Existing S2 `internal/nativesessions/**`, S1 `internal/db/**` and S3
  `internal/chat/**` already cover the other new source/test/migration files.
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
