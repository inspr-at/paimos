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
  --workflow deploy-production --environment production-eu1

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
majors, exact media types, and fixture digests. The immutable certified commit
and first release are recorded in `manifest-v2.json` beside the fixture.
