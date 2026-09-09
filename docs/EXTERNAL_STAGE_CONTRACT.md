# External delivery-stage contracts v1 and v2

PAIMOS external-stage handoffs let registered machine reporters contribute
deployment or dependency evidence without transferring delivery ownership.
The v1 wire contract is frozen at Paimos commit
`e5f4c86bc061775c853d5847e8fb8bb7e3a31c34` and is published through
`GET /api/openapi.json` and `GET /api/schema`.

## Frozen v1 wire surface

All JSON requests and responses use
`application/vnd.paimos.external-stage.v1+json`. Mint and rotate responses use
`application/vnd.paimos.external-stage-secret.v1` and contain exactly 32 raw
bytes—never JSON, base64, or a response header.

| Audience | Method and route | Purpose |
|---|---|---|
| internal | `POST /api/agent-mode/deliveries/{deliveryKey}/external-stage-handoffs` | Create immutable safe metadata; no credential |
| internal | `POST /api/agent-mode/external-stage-handoffs/{handoffID}/mint` | Mint the first credential once |
| internal | `POST /api/agent-mode/external-stage-handoffs/{handoffID}/rotate` | Invalidate the prior credential and advance its epoch |
| internal | `POST /api/agent-mode/external-stage-handoffs/{handoffID}/revoke` | Terminally revoke the handoff |
| external | `GET /api/external-stage/handoffs/{handoffID}` | Pull the current value-free projection |
| external | `POST /api/external-stage/handoffs/{handoffID}/accept` | Accept as sequence 1 |
| external | `POST /api/external-stage/handoffs/{handoffID}/reports` | Append the exact-next report |

The additive launch-admission sidecar described below changes neither these
seven routes nor either frozen media type or any v1/v2 fixture byte.

External calls require two independent credentials: the exact registered
Bearer API key and the handoff credential in the inbound-only
`X-PAIMOS-Handoff-Secret` header. The header contains unpadded base64url of the
32 raw bytes. The raw or encoded handoff credential is forbidden from URLs,
queries, JSON, cookies, argv, environment variables, stdout/stderr, logs,
audits, errors, and fixtures.

## Safe CLI workflow

Reporter registration and prerequisite setup is a separate authenticated Agent
Mode admin control plane; it does not enlarge or alter the seven frozen adapter
routes. It uses standard `application/json`, normal editor authorization, and a
mandatory `Idempotency-Key` on every POST. Every mutation reauthorizes current
delivery/project ownership and writes mandatory append-only audit evidence.

| Method and route | Purpose |
|---|---|
| `GET /api/agent-mode/deliveries/{deliveryKey}/external-reporter-registrations` | Discover exact safe IDs for current, non-revoked registrations |
| `POST /api/agent-mode/deliveries/{deliveryKey}/external-reporter-registrations` | Register an exact API key as Pharos owner or Janus dependency |
| `POST /api/agent-mode/deliveries/{deliveryKey}/external-reporter-registrations/{registrationID}/revoke` | Revoke one exact registration |
| `POST /api/agent-mode/deliveries/{deliveryKey}/external-owner-activations` | Atomically start deployment/verification and hand authority to one exact current Pharos owner |
| `POST /api/agent-mode/deliveries/{deliveryKey}/external-prerequisite-sets` | Seal 0–16 exact current Janus bindings for one stage execution |

Use the corresponding CLI discovery and setup commands. Never guess an ID or
provision with direct SQL:

```sh
paimos --json external-stage registrations list issue:4664

paimos --json external-stage registrations create issue:4664 \
  --api-key-id "$PHAROS_API_KEY_ID" --class pharos --role owner \
  --workflow deploy-production --environment production-eu1 \
  --target-ref "$SERVER_LISTED_TARGET_REF"

paimos --json external-stage registrations create issue:4664 \
  --api-key-id "$JANUS_API_KEY_ID" --class janus --role dependency \
  --dependency authorization

paimos --json external-stage owner activate issue:4664 \
  --stage deployment --attempt 1 --plan-revision 3 \
  --reporter-registration-id "$PHAROS_REGISTRATION_ID" \
  --current-execution 0 --current-authority-epoch 0

paimos --json external-stage prerequisites seal issue:4664 \
  --stage deployment --execution 1 --plan-revision 3 --authority-epoch 2 \
  --prerequisite "required:authorization=$JANUS_AUTH_REGISTRATION_ID" \
  --prerequisite "optional:credential-handoff=$JANUS_CREDENTIAL_REGISTRATION_ID"
```

Every declared item must explicitly use
`required:dependency-key=registration-id` or
`optional:dependency-key=registration-id`; there is no default requirement.
Only `required` rows gate owner success. Optional rows remain visible
dependency facts but do not block owner completion. Omitting all
`--prerequisite` flags intentionally seals the mandatory empty set as
`"prerequisites":[]`; an unsealed set is not equivalent. Required-only,
optional-only, mixed, and empty sets are all valid, with at most 16 declared
items.

Read `registration_id` from the create response or the current-only
`registrations list`; that exact safe ID is the only supported input to handoff
creation. Then create metadata and mint directly into a path that does not exist:

```sh
paimos external-stage create issue:4664 \
  --stage deployment --execution 1 --plan-revision 3 \
  --authority-epoch 2 --reporter-registration-id "$REGISTRATION_ID" \
  --expires-at 2026-08-22T12:00:00Z

paimos external-stage mint 01ARZ3NDEKTSV4RRFFQ69G5FAV \
  --expected-credential-epoch 0 \
  --secret-output /run/credentials/pharos-handoff.bin
```

`mint` and `rotate` reserve the destination with `O_EXCL` and mode `0600`
before the request, stream exactly 32 bytes, fsync and close the file, then
fsync its parent directory. They never print the bytes, an encoding, a digest,
or a prefix. An existing target fails before the request. Any ambiguous/lost
response or output-finalization failure requires `rotate`; mint cannot recover
or replay the original raw value.

External operations read the raw credential only from an owner-owned,
single-link, owner-only regular file or stdin:

```sh
paimos external-stage pull 01ARZ3NDEKTSV4RRFFQ69G5FAV \
  --secret-file /run/credentials/pharos-handoff.bin

paimos external-stage accept 01ARZ3NDEKTSV4RRFFQ69G5FAV \
  --secret-file /run/credentials/pharos-handoff.bin \
  --observed-at 2026-08-21T10:00:00Z

paimos external-stage report 01ARZ3NDEKTSV4RRFFQ69G5FAV \
  --secret-file /run/credentials/pharos-handoff.bin \
  --report-file report.json
```

Use `--secret-stdin` for a protected pipe from a secret manager. A report can
also use `--report-file -`, but not when the independent credential consumes
stdin. Report JSON is decoded as one strict value; unknown fields and invalid
closed enums, evidence, blocker, timestamp, digest, or state combinations fail
locally before the credential is read or a request is sent.

## Ownership, dependencies, and verification

- Pharos is the owner reporter for guarded deployment and a separate fresh
  verification stage. Deployment success establishes only
  `deployed_unverified`.
- Verification is a distinct verification-stage handoff for the same
  delivery and attempt. Environment plus artifact version, SHA-256 digest,
  and 40- or 64-character lowercase commit digest must exactly match the
  deployment. Deployment and verification workflow symbols may differ.
- Verification `observed_at` and server receipt must both be strictly after
  the matching deployment server receipt. Otherwise state remains
  `deployed_unverified`.
- Janus is dependency-only. Its evidence is restricted to enum, boolean, and
  timestamp authorization or credential-handoff facts. It has no free-text,
  URL, path, ID, digest, ciphertext, callback, or command field and can never
  complete canonical stage state.
- Prerequisite `required|optional` is server-owned Agent Mode setup policy, not
  an external adapter report field. A required binding gates owner completion
  until it commits terminal satisfied evidence. That immutable satisfaction
  survives later credential expiry or registration revocation; an unsatisfied
  revoked binding still blocks. Optional dependency evidence never completes
  canonical state.
- Reporter class, role, dependency key, evidence ceiling, key binding, and
  authority are server-owned. JSON never grants them. Owner and dependency
  sequences and latest projections are independent.

Exact same-sequence/same-body replay returns the prior safe receipt without a
write or wake. Conflicting replay, a gap/regression, stale authority, a late
new report, or invalid evidence fails closed. Server receipt time—not reporter
clock time—controls freshness and liveness. While a reporter is nonterminal,
the server rejects semantic progress after the active liveness window expires;
a currently authorized heartbeat revives that window using its server receipt,
even when its reporter timestamp is old. Terminal satisfied dependencies do not
become stale merely with age.

## Canonical fixtures and adapter pins

Canonical exact-byte fixtures live in
`backend/contracts/fixtures/external-stage/`:

- `owner-pharos-v1.json` is one ordered deployment → verification sequence
  bound to a single delivery and attempt. It proves exact artifact/environment
  matching and fresh cross-stage verification.
- `dependency-janus-v1.json` contains only value-free dependency evidence and
  explicitly records that neither case completes canonical stage state.
- `manifest-v1.json` pins schema major, media type, exact lengths, per-file
  SHA-256 values, the certified contract commit, release tag, and fixture-set
  digest.
- [`backend/contracts/external-stage-v1.schema.json`](../backend/contracts/external-stage-v1.schema.json)
  is the standalone Draft 2020-12 catalogue for every v1 JSON route body. Its
  complete 22-definition inventory is mechanically compared with the
  `ExternalStage*` OpenAPI components, every reference must resolve locally,
  and its exact UTF-8 bytes are pinned at
  `sha256:c9de59698e68cb7c21dd84ff8d8a9a209eef1188a54bdca8f766613f540182ff`.
  The raw mint/rotate secret uses a separate binary media type and is therefore
  intentionally not a JSON-schema root.

The v1 fixture-set digest is:

```text
sha256:0318f4025902c9d5dd790384950cc9daebb16e02e79a4a90ce7dddc673e68bed
```

It is SHA-256 over `paimos.external-stage.fixtures.v1\0`, followed in lexical
filename order by `filename + \0 + exact fixture bytes + \0`. The manifest is
excluded so release metadata can be finalized without changing fixture
identity. Fixture files are compact UTF-8 JSON with exactly one trailing LF.

External-stage v1 is immutably pinned to Paimos commit
`e5f4c86bc061775c853d5847e8fb8bb7e3a31c34` and its first release,
`v5.11.0`. The standalone schema was published later without changing those
already certified v1 semantics or fixture bytes. Pharos and Janus adapters
must embed the complete tuple: schema
major, fixture-set digest, certified Paimos contract commit, and immutable
release tag. Release CI requires the pinned commit to be an ancestor of the
release ref and compares both canonical fixture files plus
`backend/externalstage/contract.go` byte-for-byte with that commit. It also
recomputes every fixture digest and requires the release tag to resolve before
any later release can be published; the first release may establish that tag.

Changing route spelling, media types, DTO fields, enums, fixture bytes, digest
algorithm, or evidence semantics requires a new contract major and new fixture
directory. Never rewrite v1 in place after release.

## Additive scheme-aware v2

External pull, accept, and report routes also support exact negotiation of
`application/vnd.paimos.external-stage.v2+json`. Internal create, mint, rotate,
revoke, authority, and credential mechanics remain frozen v1 controls. An
adapter chooses one exact media type for a request; missing, wildcard,
parameterized, mixed, or unknown media types fail closed. No version scheme is
ever inferred from punctuation in the version string.

V2 changes only the Pharos artifact evidence and the pull certification tuple.
Every Pharos deployment or verification fact carries all of:

- `version_scheme`: exactly `legacy` or `inspr-calendar-v1`;
- the original `version` spelling, preserved without translation;
- an explicit symbolic `release_channel` and non-negative monotonic
  `release_sequence`;
- the exact artifact SHA-256 and source commit digest from v1;
- an immutable `release_manifest_coordinate` and its exact SHA-256 digest.

Calendar versions use `yy.mm.dd` or `yy.mm.dd.hh.mm.ss`, with fixed-width
two-digit fields and real Gregorian dates. Legacy versions remain explicitly
legacy even when their spelling resembles a calendar. Rollback is an explicit
new deployment fact naming the older immutable artifact and release-set
manifest; the service never silently rewrites, downgrades, or promotes an
earlier fact. Exact idempotent replay returns the original receipt, while any
same-key mutation of scheme, sequence, channel, manifest, artifact, or source
identity conflicts.

This grammar describes the external Pharos artifact named by the evidence. It
does not reinterpret PAIMOS's own repository-specific release tag. The
`paimos_release` field in the fixture manifest pins the PAIMOS release that
publishes this contract; it is not an external artifact version and therefore
does not participate in `version_scheme` validation.

The additive storage table extends an already committed v1-compatible Pharos
fact. A v2 verification row names the exact earlier v2 deployment row and the
database guard requires exact equality across environment, version, artifact,
commit, scheme, channel, release sequence, manifest coordinate, and manifest
digest. This keeps v1 consumers functional during cutover without weakening
authority, freshness, replay, or secret boundaries. A Pharos adapter may move
from v1 to v2 per delivery attempt: deployment evidence must use v2 before a
verification handoff can bind to that exact v2 deployment identity. After all
supported Pharos releases pin the v2 fixture and schema tuple, a later ticket
may retire v1 negotiation. V1 itself remains byte-for-byte immutable.

Baseline-owned deliveries (PAI-960) additionally bind the artifact this batch
built before owner deployment completion. That expected identity is taken only
from implementation evidence:

- `artifact` / `digest` — running OCI image **config** digest (`sha256`, 32 bytes);
- `artifact` / `external_ref` prefixed `release-manifest:sha256:` — immutable
  **release-set** document digest (the bytes named by owner-v2
  `release_manifest_digest`, e.g. Pharos `release-set.json`);
- `artifact` / `external_ref` prefixed `release-coordinate:` — immutable
  coordinate of that **release-set**, not an OCI registry index;
- `artifact` / `external_ref` prefixed `oci-manifest:sha256:` — optional OCI
  image **index or manifest** digest; it may be recorded but is never a
  release-set digest and never fills `release_manifest_digest`;
- `artifact` / `external_ref` prefixed `inspr-release-v1:` — typed
  `{scheme}/{channel}/{sequence}/{version}` where `scheme` is `legacy` or
  `inspr-calendar-v1`. Generic `external_ref` strings, including colon-delimited
  tuples without that prefix, are ignored and never parsed as release identity.
  A recognized prefix with a malformed or conflicting payload is refused rather
  than guessed as another digest class;
- `implementation_result` / `commit` — source revision.

A QA `test_result` digest remains the delivery QA binding digest from
[`docs/DATA_MODEL.md`](DATA_MODEL.md); it is not a release-manifest identity.
Non-baseline v1 owner reports keep digest/commit compatibility even when QA
carries a digest. A baseline-owned delivery that already has a handoff refuses
v1 owner success with `v2_report_required` rather than a generic invalid
request. Frozen owner-v2 and Janus-v1 fixture bytes are unchanged.

The first v2 publication pull request must be merged with a true merge commit,
not a squash or rebase merge. The certified content commit recorded by
`manifest-v2.json` must remain an ancestor of `main`; the release guard rejects
an unavailable, non-ancestor, or byte-divergent pin. The executable release
fixture models this branch-and-merge topology so later changes cannot silently
make the first calendar release impossible.

The canonical v2 owner fixture lives in
`backend/contracts/fixtures/external-stage-v2/owner-pharos-v2.json`. It covers
an explicit legacy deployment, an explicit calendar deployment with its exact
verification, and a later explicit legacy rollback. Its fixture-set digest is:

```text
sha256:6bba9613230c6ea728db58ffea5533399caed19e6d56a8d78ef19d0fde20be8a
```

The v2 fixture digest uses the same framed algorithm with the domain changed to
`paimos.external-stage.fixtures.v2\0`. The v2 OpenAPI components are published
alongside v1 through `/api/openapi.json`; `/api/schema` advertises both contract
majors, their exact media types, and the v2 fixture digest (v1 remains available
through the immutable v1 contract response). The immutable certified commit
and first release are recorded in `manifest-v2.json` beside the fixture.

## Additive one-shot launch admission

PAI-978 adds two routes under the existing external audience without changing
the frozen reporting contracts:

| Method and route | Closed body and result |
|---|---|
| `POST /api/external-stage/handoffs/{handoffID}/launch-candidates` | `ExternalStageLaunchCandidate` → immutable `ExternalStageLaunchAdmission` |
| `POST /api/external-stage/handoffs/{handoffID}/launch-admissions/{admissionID}/consume` | `{schema,version,admission_digest}` → immutable consumed receipt |

Both use
`application/vnd.paimos.external-stage-launch-admission.v1+json`, the registered
Bearer API key, the separate `X-PAIMOS-Handoff-Secret`, and `Idempotency-Key`.
Bodies are at most 64 KiB and are decoded as exactly one closed JSON value:
unknown or duplicate fields, trailing values, a wrong or parameterized media
type, and any non-canonical identifier fail closed. Responses use `no-store`.

Possessing an automatic-mode batch, a handoff, or its credentials is not host
consent. The root authority exists only when a current, non-impersonated human
editor deliberately selects the default-off `delegated_launch` during review
and confirms Start. The server binds that grant to the exact project, baseline,
draft/revision, review/session, canonical scope, worker provenance, batch,
delivery, current attempt/plan, server-listed target provenance, workflow,
environment, one-launch ceiling, and expiry. Start refuses a selection more
than 24 hours ahead. A delegated candidate additionally requires the exact
still-live Pharos owner registration to carry the same `target_ref`; there is
no sole-registration fallback. Legacy registrations may omit `target_ref` only
for attended reporting.

Pharos submits no grant or admission identifier. Its candidate contains only:

- `schema`, `version`, `target_ref`, `workflow`, `environment`, and `observed_at`;
- the existing owner-v2 artifact fields `version_scheme`, `version`,
  `release_channel`, `release_sequence`, `digest`, `commit_digest`,
  `release_manifest_coordinate`, and `release_manifest_digest`;
- `reviewed_plan_digest`, Pharos's domain-separated digest of the exact reviewed
  deployment plan;
- `operation_binding_digest`, Pharos's separate domain-separated digest binding
  that operation to every available public-pull binding: handoff/credential
  epoch, target/workflow/environment, the full artifact, `reviewed_plan_digest`,
  deployment stage, execution/authority, and the exact
  plan/predecessor/context digests.

The public pull does not expose raw attempt numbers or plan revisions, so Pharos
must not guess or reconstruct them. The immutable, Paimos-derived `plan_digest`
transitively commits to the attempt ID, plan revision, and attempt-start event;
`context_digest` commits to the delivery key, attempt ID, stage, execution,
authority, and registration; and `predecessor_digest` commits to the current
execution, authority, and semantic event lineage. Database guards independently
reproduce and seal these domain-separated commitments. Together with the other
public-pull fields above, they are the canonical attempt/plan binding available
to the Pharos candidate.

Paimos validates both supplied digests as distinct canonical SHA-256 values and
stores them immutably. They remain Pharos-produced review fingerprints, not
free-form Paimos authority. Paimos independently resolves its root grant and
revalidates the live human session and project edit, immutable review/batch
digests, target and registration, current handoff secret/credential epoch,
attempt, plan, execution, authority, predecessor/context, successful immutable
implementation+QA evidence, and exact eight-field artifact identity. Impact,
data-loss, privacy, scope, and review gates are not bypassed or enlarged.

The returned admission is server-derived and binds the root grant ID/revision/
digest, admission ID/digest, handoff/epoch, target/workflow/environment,
artifact, deployment stage, attempt/plan/execution/authority, the existing
plan/predecessor/context digests, both Pharos digests, `max_launches:1`,
`used_launches:0`, issue/expiry times, and `state:"issued"`. Its expiry is the
earliest of the root-grant expiry, handoff expiry, and server receipt time plus
15 minutes. A different candidate or idempotency key conflicts; it cannot mint
a second identity. The admission exposes the server-resolved raw attempt and
plan values, but these are outputs rather than candidate inputs: the consumer
verifies them and the admission's plan/predecessor/context commitments against
the pull used to build the candidate; it does not derive a candidate from a
future admission. Even an exact candidate retry may refuse after authority is
paused or revoked.

First consume revalidates every current gate and atomically spends launch 1.
Pause, cancel/stop, replan, retry, authority or credential rotation, revocation,
target drift, registration loss, stale artifact, or expiry refuses without a
fallback or new retry authority. Exact replay returns the byte-identical durable
receipt, including after a restart. Once consumed, that receipt remains
historical idempotent evidence even if the grant or handoff later expires: it
does not refresh authority, authorize another launch, serve as a fresh dispatch
instruction, or change a duplicate flag. These endpoints only seal and spend
authority; Paimos performs no Pharos, host, provider, command, URL, path,
credential, or secret effect.

CLI adapters use protected file/stdin inputs:

```sh
paimos --json external-stage launch-candidate "$HANDOFF_ID" \
  --candidate-file candidate.json --secret-file /run/credentials/pharos-handoff.bin

paimos --json external-stage launch-consume "$HANDOFF_ID" "$ADMISSION_ID" \
  --admission-digest "$ADMISSION_DIGEST" \
  --secret-file /run/credentials/pharos-handoff.bin
```

`--candidate-file -` has file/stdin parity but cannot share stdin with
`--secret-stdin`. The standalone closed schema and immutable fixtures live at
`backend/contracts/external-stage-launch-admission-v1.schema.json` and
`backend/contracts/fixtures/external-stage-launch-admission-v1/`.
