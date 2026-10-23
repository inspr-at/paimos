# P11 replacement proof and remaining cuts

AEON-1683 implements a conservative first part of P11 under Markus's standing AEON-1670 approval of 2026-10-22. The full package is **not complete**. Its frozen input is 493 browser cases across 40 source owners; the planned verified overlap below reduces that to **450**, above the requested ceiling of 22. No protected historical identity, historical replay evidence, production behavior, CI activation flag, required check or visual reference is changed.

The exact original-case mappings are in `scripts/test-prune/p11-execution.json`, pinned to P01's immutable baseline digest. All 68 original registrations in the five changed groups have explicit destinations. The other 35 owners map every frozen registration to itself. An original identity protected by the standing ledger cannot be removed by this package's mapping.

| Browser owner | Before | Retained after planned cuts | Risks intentionally retained beyond its one-case plan |
|---|---:|---:|---|
| status-icons | 12 | 5 | Phone icon geometry; native animation of every menu mark; off-screen/hidden-tab suspension; actual CSS bar hover/live/static behavior |
| portal-admin | 3 | 1 | Publishing and moderation, a stale project/revision refusal, authoritative binding refresh and explicit retry |
| delivery-flow-modes | 25 | 9 | Actual autoplay; desktop/phone release-record geometry; absent facts; empty/first-run integration; phone long empty copy; retained failed preference saves |
| delivery-ui | 17 | 6 | Desktop/phone selector stability; reader restrictions and empty/unavailable states; lifting a newer hold again; partial shadow-queue rendering |
| delivery-flow-lanes | 11 | 4 | Native desktop wheel/selection dispatch; painted incident-caption clipping; both narrow 15-minute edge handles; phone brush/resize/playhead integration |
| Other 35 P11 owners | 425 | 425 | Protected lineage and lower-layer transfers whose replacement execution has not yet been established |
| Total | 493 | 450 | The 22-case ceiling is not claimed |

The five new cases use one rendering profile each. Their workflows perform actual browser interactions, capture request identities/revisions and hold responses before selecting a newer time. Existing nonredundant assertions are retained. The 1 ms/frame assertion is an explicitly approved retirement, not a transferred performance guarantee. FCP/LCP remain observations; both production cold-request/lazy-module tests retain their bounds and configuration. Their cosmetic 350 ms sleep is replaced by font readiness and a rendered-frame barrier.

`delivery-flow-modes.unit.test.ts` also adds a runtime subscription-lifetime case using production `useDeliveryFlow`, mounted Vue components, an injected clock and counted sources/requests. Twenty deactivate/reactivate cycles require no stream or timer while inactive, exactly one stream while active, two reads per activation and no extra reads for an equal-project rerender. Its native identity is ESSENTIAL. This runtime test has been collected, but its execution and negative control remain unproven; **delivery-tab-switch's two original browser cases remain**.

Five replacement cases passed through LEAD's `remote-spec.sh` before cuts. A separate scratch branch then changed one production behavior for each case; all five cases failed on commit `9bc7d7532`, with the original product behavior restored on the work branch:

| Negative control | Observed refusal |
|---|---|
| Disable the reduced-motion CSS rule | Expected zero animations, received two |
| Omit the portal's authoritative refresh after 409 | Expected current `p-aeon`, received stale `p-pharos` |
| Reset the selected Flow time to each live update | Expected `Viewing 20:18`, received `Live · now 20:26` |
| Clear retained Delivery rows after a 503 | The retained-results status disappears into the empty/error slot |
| Ignore the overview's right resize target | Expected the end to become less than 1305, received 1305 |

The original status-icon run reported **10 passed, 2 failed in 27.2 s**. Both failures were at 960 px, where the old matrix measured a deliberately hidden QA label. The new focused case passed in **3.7 s**, and the phone timeline case in **3.4 s**. Portal/status replacements together passed in **8.0 s**; Delivery write/Flow-mode replacements together passed in **7.7 s**. These are observed targeted runner times, **not a measured full-P11 saving**; the failed baseline and different selected sets do not establish that comparison. The four existing status-motion runtime cases passed in a single small local fallback in 158 ms after remote load refusal.

The remaining blockers are explicit:

- `visual-manifest.json` still has Tick approval `pending`, baseline authority `pending-review`, and no pinned attachment/member digests. The standing checker requires those before any protected cut. The pending authority is not fabricated or bypassed.
- Runtime conversions for names, notes, versions, scopes, metrics, public participation and retained failures must execute and pass their negative controls before their browser donors can be removed. Existing suite filenames alone are not equivalence proof.
- Remote static execution returned exit 3: `remote static check paused at night (night protocol: mbp2606 is shared with the CI pool); rely on the PR's hosted CI static job`. Static success, the production performance configuration and the changed runtime suites still need remote/hosted evidence.

The coordinator can continue the remaining conversions after P02 establishes visual authority. The existing Go/API/contract and real-server checks remain in force. Retired Journey functionality remains closed; this package introduces no Journey implementation or tests for that retired feature.
