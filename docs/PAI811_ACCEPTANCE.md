# PAI-811 hardening and live-acceptance record

This is the durable acceptance ledger for the PAI-799…811 Agent Mode delivery
train. A checked local or CI gate proves only the named candidate; release and
live rows remain unchecked until the same commit is protected-merged,
published, deployed, and observed in the target environment.

## Visual authority and accessibility

- [x] Preserve the approved Detail-10 desktop shell; no redesign.
- [x] Document final ImageGen taste references for Detail 1, Detail 10,
  Detail 100, and 390 px Detail 10 in
  [`brand/ai-ux/SPEC.md`](brand/ai-ux/SPEC.md).
- [x] Reflow 390 px compact narration and operator controls in normal document
  flow: one latest narration line, no redundant feed dock, no horizontal
  overflow, and the complete selected delivery remains in the initial canvas.
- [x] Measure non-occlusion at 390, 520, 736, 900, 1024, and 1440 CSS pixels.
- [x] Measure a 512×450 CSS-pixel viewport at device scale 2 as the 200%
  effective-zoom boundary; the canvas remains reachable and scrollable, with
  no dock/card/control intersection or horizontal document overflow.
- [x] Under `prefers-reduced-motion: reduce`, no visible Agent Mode element has
  a non-zero animation or spatial transition.
- [x] Inspect deterministic Detail 1/10/100, narrow, phone, forbidden, and
  dark-OS/reduced-motion screenshots from the exact production build.

Authoritative browser gate:

```text
PAI805_SELF_HOST_DIST=1 PAI805_SHOT_DIR=/tmp/pai811-mobile-shots \
  npx playwright test e2e/agent-mode-visual.spec.ts
```

Latest candidate result: **4/4 passed**, including the 200%-effective-zoom
boundary, with deterministic screenshots in `/tmp/pai811-mobile-shots`.

## Scale and deterministic build

- [x] Detail 10 renders exactly one selected identity across a 100-delivery
  fixture and preserves all 100 authoritative lane totals.
- [x] The client accepts the frozen 1,000-row API budget and rejects the first
  oversized 1,001-row snapshot before it enters reactive UI state.
- [x] The server rejects aggregate snapshots above 1,000 while resolving an
  authorized exact-detail request before the aggregate limit.
- [x] Full frontend unit suite passes (122 files; 1,078 tests, including the
  new 1,000/1,001 boundary assertions).
- [x] Typecheck, production build, changed-file ESLint, and `git diff --check`
  pass; only the established Vite chunk-size advisory remains.

## Failure, authority, and concurrency matrix

These rows must be refreshed against the protected-merge candidate rather than
inferred from earlier ticket-specific runs.

- [ ] Loading, empty, offline/retained, forbidden, not-found, malformed, and
  retry states pass in unit and browser surfaces without fabricated truth.
- [ ] SSE resume, oversized cursor refusal, disconnect/reconnect, permissions
  epoch change, and stale-event non-influence pass.
- [ ] Full ACL revocation clears previously authorized identity and narration;
  unauthorized requests cannot move selection, controls, stages, or ETA.
- [ ] Runner/reporter restart, server restart, clock skew, concurrent control,
  cancellation, and stale-authority `412` cases pass without split brain.
- [ ] Detail 1 editor, upload, comments, voice, two-phase controls,
  deployed-unverified, fresh verification, and stale/unknown states pass in the
  final browser matrix.
- [ ] Full backend, focused race, gosec, schema/OpenAPI, release-hygiene, and
  generated-artifact gates pass on the final protected-merge candidate.

## Release and live proof

- [ ] PAI-811 candidate receives independent frozen-SHA review and DCO scope
  review.
- [ ] Protected PR checks pass and the reviewed SHA is present in the merge
  history.
- [ ] Version, changelog, release notes, public claim matrix, image signature,
  SBOM, and SLSA provenance bind to the exact release commit.
- [ ] Target deployment reports the exact release digest and passes readiness.
- [ ] A real Janus dependency report succeeds but cannot complete the owner
  stage.
- [ ] A real Pharos deployment report produces `deployed_unverified`.
- [ ] Only a later exact, fresh Pharos verification reaches `verified`.
- [ ] Rotation, revocation, lost-response replay, malformed media/DTO, and
  cross-credential refusal are observed against the released services.
- [ ] PPM acceptance evidence is attached, PAI-799 through PAI-811 are closed,
  and temporary branches/worktrees are cleaned only after live verification.
