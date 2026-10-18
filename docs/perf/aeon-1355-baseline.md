# AEON-1355 · phase 1 baseline

Count inventory captured at 2026-10-14T15:47:02.568532+00:00. Product source: `2844a0f2d8416eaff2ad28d0adaabe708cc56305` (the assigned origin/main snapshot).

**Incomplete: 0/56 runtime cells measured. Missing cells are UNMEASURED, never a pass.**

No product implementation, release version, live content, live login, push to origin, deployment, or review gate belongs to this phase.

## Count-only fixture inventory

| Project | Work items | Visible comments | Incident relations | Maximum depth |
| --- | ---: | ---: | ---: | ---: |
| AIT | 168 | 1295 | 112 | 2 |
| AEON | 1353 | 11359 | 134 | 3 |

The inventory uses ppm through the approved aeon CLI. It follows bounded node, activity and relation cursors with two readers. Raw responses exist only in memory. Only numeric depth/fanout and comment histograms are retained; no production IDs, titles, bodies, names, comments or relation topology are copied. Counts span the collection interval, not a database snapshot. Relations are deduplicated per project and include edges incident to its work; an edge between projects can occur in both totals.

The harness generates fixtures directly into one disposable Postgres database on the remote build host. It is configured to match work counts, depth/fanout, the per-ticket comment-count distribution and relation counts, with generated relation endpoints and types. Names, body length (990 characters), dates, priorities, estimates and the approximately 25% closed proportion are synthetic. Decision Desk and Agents exercise their empty states; populated question/session scaling is not established by this inventory. Generated data never enters product source or a live instance. The harness drops the database after the run; this lifecycle requires runtime proof.

## Method and limits

Run only through LEAD remote-spec.sh with REMOTE_ONLY=1. The harness builds Vite production assets and the unchanged Go server, then runs one headless Chromium worker against loopback Postgres 18 on the approved remote build host. Dev login provides an isolated synthetic person; it is outside every trace and timing window. Uncommitted runtime artifacts record the actual host, OS, architecture, Chromium version and harness source SHA. The ticket requested Linux Chromium; the approved runner currently targets a Darwin Mac. A Darwin run must be labelled Darwin and cannot be described as Linux/CI-equivalent.

Cold list means a new browser context with disabled HTTP cache, timed from Navigation Timing start. Other cold actions mean first use after a fresh prerequisite list in that context. Warm list uses resident SPA navigation through Projects; other warm actions execute once and then restore prerequisites in the same browser context, retaining HTTP/V8 caches. Re-entering a list still calls its API; warm is not proof of a persistent list-response cache. Server/Postgres caches are not flushed, so “cold” refers to browser state, not a cold database.

A browser-local timer starts immediately before the event handler (pointerover for hover). A frame containing the completed result supplies interaction first paint; the next animation frame supplies usable time. Initial navigation uses the browser first-paint entry. Readiness checks loaded rows, loaded graph data, loaded ticket content or the page loading states. This measures handler-to-render latency; pointer hit-testing, human movement and automation transport are excluded. Search dispatches an input event with the complete query and includes the debounce; it is not a character-by-character typing sample. Scroll is one 300-pixel programmatic movement of the list container, not a sustained wheel/trackpad trace.

Each cell records PerformanceObserver long tasks >50 ms and a Chrome trace with main-thread timeline and V8 CPU samples. JSON trace and waterfall files have a .md suffix solely because the approved artifact transport copies .md/.png. Rename chrome-trace.json.md to .json for Chrome Performance. Waterfall offsets are relative to the first measured request, not the user event; timings retain DNS/connect/TTFB/response fields when Chromium supplies them. SSE requests can remain open. Cookies, request/response headers and request bodies are excluded. CPU/network throttling is not applied. Single observations require repetition before a phase-2 regression threshold is committed.

## Action budget

Budget: cold list usable ≤1,000 ms; warm list ≤300 ms; other results ≤100 ms. The table additionally flags any observed >50 ms main-thread task; the explicit typing/scrolling requirement is checked for search and scroll. Failure here is a baseline finding, not a failing CI assertion. A successful Playwright run establishes that measurements were captured, not that the product met its budgets.

| Project | Action | Cache | First paint ms | Usable ms | Long tasks count / total ms / max ms | Requests | Budget ms | Result |
| --- | --- | --- | ---: | ---: | --- | ---: | ---: | --- |
| AIT | list | cold | — | — | — | — | 1000 | UNMEASURED |
| AIT | list | warm | — | — | — | — | 300 | UNMEASURED |
| AIT | outline | cold | — | — | — | — | 100 | UNMEASURED |
| AIT | outline | warm | — | — | — | — | 100 | UNMEASURED |
| AIT | graph | cold | — | — | — | — | 100 | UNMEASURED |
| AIT | graph | warm | — | — | — | — | 100 | UNMEASURED |
| AIT | return-list | cold | — | — | — | — | 100 | UNMEASURED |
| AIT | return-list | warm | — | — | — | — | 100 | UNMEASURED |
| AIT | filter | cold | — | — | — | — | 100 | UNMEASURED |
| AIT | filter | warm | — | — | — | — | 100 | UNMEASURED |
| AIT | sort | cold | — | — | — | — | 100 | UNMEASURED |
| AIT | sort | warm | — | — | — | — | 100 | UNMEASURED |
| AIT | search | cold | — | — | — | — | 100 | UNMEASURED |
| AIT | search | warm | — | — | — | — | 100 | UNMEASURED |
| AIT | hide-closed | cold | — | — | — | — | 100 | UNMEASURED |
| AIT | hide-closed | warm | — | — | — | — | 100 | UNMEASURED |
| AIT | open-ticket | cold | — | — | — | — | 100 | UNMEASURED |
| AIT | open-ticket | warm | — | — | — | — | 100 | UNMEASURED |
| AIT | close-ticket | cold | — | — | — | — | 100 | UNMEASURED |
| AIT | close-ticket | warm | — | — | — | — | 100 | UNMEASURED |
| AIT | decision-desk | cold | — | — | — | — | 100 | UNMEASURED |
| AIT | decision-desk | warm | — | — | — | — | 100 | UNMEASURED |
| AIT | agents | cold | — | — | — | — | 100 | UNMEASURED |
| AIT | agents | warm | — | — | — | — | 100 | UNMEASURED |
| AIT | header-hover | cold | — | — | — | — | 100 | UNMEASURED |
| AIT | header-hover | warm | — | — | — | — | 100 | UNMEASURED |
| AIT | scroll | cold | — | — | — | — | 100 | UNMEASURED |
| AIT | scroll | warm | — | — | — | — | 100 | UNMEASURED |
| AEON | list | cold | — | — | — | — | 1000 | UNMEASURED |
| AEON | list | warm | — | — | — | — | 300 | UNMEASURED |
| AEON | outline | cold | — | — | — | — | 100 | UNMEASURED |
| AEON | outline | warm | — | — | — | — | 100 | UNMEASURED |
| AEON | graph | cold | — | — | — | — | 100 | UNMEASURED |
| AEON | graph | warm | — | — | — | — | 100 | UNMEASURED |
| AEON | return-list | cold | — | — | — | — | 100 | UNMEASURED |
| AEON | return-list | warm | — | — | — | — | 100 | UNMEASURED |
| AEON | filter | cold | — | — | — | — | 100 | UNMEASURED |
| AEON | filter | warm | — | — | — | — | 100 | UNMEASURED |
| AEON | sort | cold | — | — | — | — | 100 | UNMEASURED |
| AEON | sort | warm | — | — | — | — | 100 | UNMEASURED |
| AEON | search | cold | — | — | — | — | 100 | UNMEASURED |
| AEON | search | warm | — | — | — | — | 100 | UNMEASURED |
| AEON | hide-closed | cold | — | — | — | — | 100 | UNMEASURED |
| AEON | hide-closed | warm | — | — | — | — | 100 | UNMEASURED |
| AEON | open-ticket | cold | — | — | — | — | 100 | UNMEASURED |
| AEON | open-ticket | warm | — | — | — | — | 100 | UNMEASURED |
| AEON | close-ticket | cold | — | — | — | — | 100 | UNMEASURED |
| AEON | close-ticket | warm | — | — | — | — | 100 | UNMEASURED |
| AEON | decision-desk | cold | — | — | — | — | 100 | UNMEASURED |
| AEON | decision-desk | warm | — | — | — | — | 100 | UNMEASURED |
| AEON | agents | cold | — | — | — | — | 100 | UNMEASURED |
| AEON | agents | warm | — | — | — | — | 100 | UNMEASURED |
| AEON | header-hover | cold | — | — | — | — | 100 | UNMEASURED |
| AEON | header-hover | warm | — | — | — | — | 100 | UNMEASURED |
| AEON | scroll | cold | — | — | — | — | 100 | UNMEASURED |
| AEON | scroll | warm | — | — | — | — | 100 | UNMEASURED |

## Runtime evidence

No browser runtime evidence is available. Runner refusals are recorded below. No numeric paint, usable, long-task or network result can be inferred from source inspection.

## Slowest three and phase-2 proposals

A measured top three cannot be named until the table has runtime evidence. The following are source-inspection candidates, not a measured ranking or confirmed root cause:

1. **Search debounce:** web/src/components/work/ListToolbar.vue:127 waits 220 ms after input before sending the query. That delay alone exceeds the 100 ms result budget, before API/render time. Proposal: immediate local feedback over the loaded page, preserving explicit incomplete-result disclosure, with a cancellable server refresh; test query/record identity fences and full server-search semantics. Effort: 0.5–1 day. Risk: medium (stale or falsely complete local results, added request volume). Measure debounce, request and render portions separately before changing it.

2. **Column-header hover:** web/src/components/TooltipHost.vue:106 schedules pointer disclosure after 380 ms. The column sort button uses that shared tooltip at web/src/components/work/TicketTable.vue:663. Proposal: decide whether an intentional tooltip dwell is exempt from the click budget; if it is not, add a scoped faster dwell for header guidance while retaining focus/touch behavior and stable control geometry. Effort: 0.25–0.5 day. Risk: low to medium (accidental popovers and accessibility changes). This is not evidence of a long task.

3. **List/filtered refresh path:** web/src/lib/useTicketList.ts:104 starts omitted-filter facet reads and web/src/lib/useTicketList.ts:110 reads the full page; web/src/lib/useTicketList.ts:113 adopts page rows into the reactive store. The ticket table then renders those rows. Proposal, only if traces confirm this path dominates: retain a revision/identity-bound completed page during re-entry and batch row adoption; keep server authoritative filtering, live-gap invalidation and explicit partial-data states. Effort: 1–2 days. Risk: medium to high (permission revocation, stale rows, live updates). There is no measured evidence yet that this path is one of the slowest three.

## Network and API attribution

Browser render/CPU cost belongs to the web client. Request queueing and asset transfer are visible in each waterfall; response wait includes Go handler, Postgres and loopback overhead. Go/API latency is our server code, while real transport RTT, bandwidth, proxy behavior, OIDC and host contention are outside the web client. Loopback timings do not quantify live ppm network/API latency. Agent CLI count reads include CLI startup and public-network/API time and are not web action timings. Source-only candidates cannot assign residual time to the network. Coordinator confirmation of the measured top three in Markus’s person browser, with the required heads-up, remains a later step; this worker never uses that browser.

## Validation and handover

- Count collector parsed; inventory totals and invalid-count rejection passed without a browser.
- JavaScript syntax checks passed for all harness modules.
- All seven harness files passed the ownership checker (slice S9).
- Remote ci-static --merge-main ran: 41 checks passed; public-names failed on the initial report. The host label was neutralised, and the targeted public-name check now passes. The final full remote retry returned exit 3: remote busy, 2/2 slots occupied. This is not a full green static result.
- The guarded benchmark reached the production-built UI with its generated DB. Its nested-config working directory was corrected in 910960b3a. The next actual run stopped because string readiness predicates violated CSP. Structured DOM predicates replaced those in 375f835cb, without bypassing CSP. JavaScript syntax passes; the corrected benchmark has no runtime proof.
- Corrected benchmark retries were refused by the live shared browser-suite lock or by the remote slot guard (exit 3, remote busy). No foreign process or lock was signalled, and no local browser was launched.
- **BLOCKED-REMOTE-CAPACITY:** 56 runtime metric cells, their Chrome traces/waterfalls, the measured slowest-three ranking and code attribution, and the final full static pass remain unproven. Worker progress is 95%, not 100%. The coordinator must rerun the corrected benchmark and finish the evidence/ranking before accepting phase 1. PR static CI alone cannot populate this manual measurement table.
- Only web/perf/, this report, and the additive web/perf/** ownership entry in scripts/audit/slices.json changed. The measurement benchmark lives outside the product test configs; phase 2 owns the CI performance check and its canonical tier registration.

Replay (no origin push; the approved runner uses its own temporary test ref):

```sh
REMOTE_ONLY=1 /Users/markus/.aeon/lead/aeon/remote-spec.sh "$PWD" "perf/baseline.spec.mjs --config=perf/playwright.config.mjs --max-failures=2" AEON-1355
node web/perf/report.mjs web/test-results/remote <measured-harness-sha> > docs/perf/aeon-1355-baseline.md
```
