// SPDX-License-Identifier: AGPL-3.0-only
// Emits Markdown to stdout; choose the explicitly authorized report path in
// the caller. Never treats absent metrics or failed actions as budget passes.
import { readdir, readFile } from 'node:fs/promises'
import { join } from 'node:path'

const actions = ['list', 'outline', 'graph', 'return-list', 'filter', 'sort', 'search', 'hide-closed', 'open-ticket', 'close-ticket', 'decision-desk', 'agents', 'header-hover', 'scroll']
async function files(root) {
  const out = []
  let entries
  try { entries = await readdir(root, { withFileTypes: true }) } catch (error) { if (error.code === 'ENOENT') return out; throw error }
  for (const entry of entries) {
    const path = join(root, entry.name)
    if (entry.isDirectory()) out.push(...await files(path))
    else if (entry.name === 'metrics.json.md') out.push(path)
  }
  return out
}
const inventory = JSON.parse(await readFile(new URL('./counts.json', import.meta.url), 'utf8'))
const root = process.argv[2] ?? 'web/test-results/remote'
const sha = process.argv[3]
const rows = new Map()
for (const path of await files(root)) {
  const row = JSON.parse(await readFile(path, 'utf8'))
  if (sha && row.source_sha !== sha) continue
  const key = `${row.project}:${row.action}:${row.cache}`
  if (rows.has(key)) throw Error('Multiple samples for a cell; select one artifact directory explicitly')
  if (!Number.isFinite(row.usable_ms) || !Array.isArray(row.long_tasks) || !Array.isArray(row.network)) throw Error('Invalid measurement artifact')
  rows.set(key, { ...row, artifact: path })
}
const lines = [
  '# AEON-1355 · phase 1 baseline', '',
  `Count inventory captured at ${inventory.captured_at}. Product source: \`2844a0f2d8416eaff2ad28d0adaabe708cc56305\` (the assigned origin/main snapshot).`, '',
  rows.size === 56 ? 'All 56 action/project/cache cells have runtime artifacts. These are single observations, not medians or percentiles.' : `**Incomplete: ${rows.size}/56 runtime cells measured. Missing cells are UNMEASURED, never a pass.**`, '',
  'No product implementation, release version, live content, live login, push to origin, deployment, or review gate belongs to this phase.', '',
  '## Count-only fixture inventory', '',
  '| Project | Work items | Visible comments | Incident relations | Maximum depth |',
  '| --- | ---: | ---: | ---: | ---: |',
  ...inventory.projects.map(p => `| ${p.key} | ${p.work_items} | ${p.visible_comments} | ${p.incident_relations} | ${Math.max(...p.shapes.map(s => s.depth))} |`), '',
  'The inventory uses ppm through the approved aeon CLI. It follows bounded node, activity and relation cursors with two readers. Raw responses exist only in memory. Only numeric depth/fanout and comment histograms are retained; no production IDs, titles, bodies, names, comments or relation topology are copied. Counts span the collection interval, not a database snapshot. Relations are deduplicated per project and include edges incident to its work; an edge between projects can occur in both totals.', '',
  'Fixtures are generated directly into one disposable Postgres database on the remote build host. Work counts, depth/fanout and the per-ticket comment-count distribution match the inventory. Relation counts match, with generated endpoints and types. Names, body length (900 characters), dates, priorities, estimates and the 25% closed proportion are synthetic. Decision Desk and Agents exercise their empty states; populated question/session scaling is not established by this inventory. Generated data never enters product source or a live instance. The database is dropped after the run.', '',
  '## Method and limits', '',
  'Run only through LEAD remote-spec.sh with REMOTE_ONLY=1. The harness builds Vite production assets and the unchanged Go server, then runs one headless Chromium worker against loopback Postgres 18 on mbp2606. Dev login provides an isolated synthetic person; it is outside every trace and timing window. Runtime artifacts record the actual host, OS, architecture, Chromium version and harness source SHA. The ticket requested Linux Chromium; the approved runner currently targets a Darwin Mac. A Darwin run must be labelled Darwin and cannot be described as Linux/CI-equivalent.', '',
  'Cold list means a new browser context with disabled HTTP cache, timed from Navigation Timing start. Other cold actions mean first use after a fresh prerequisite list in that context. Warm list uses resident SPA navigation through Projects; other warm actions execute once and then restore prerequisites in the same browser context, retaining HTTP/V8 caches. Re-entering a list still calls its API; warm is not proof of a persistent list-response cache. Server/Postgres caches are not flushed, so “cold” refers to browser state, not a cold database.', '',
  'A browser-local timer starts immediately before the event handler (pointerover for hover). A frame containing the completed result supplies interaction first paint; the next animation frame supplies usable time. Initial navigation uses the browser first-paint entry. Readiness checks loaded rows, loaded graph data, loaded ticket content or the page loading states. This measures handler-to-render latency; pointer hit-testing, human movement and automation transport are excluded. Search dispatches an input event with the complete query and includes the debounce; it is not a character-by-character typing sample. Scroll is one 300-pixel programmatic movement of the list container, not a sustained wheel/trackpad trace.', '',
  'Each cell records PerformanceObserver long tasks >50 ms and a Chrome trace with main-thread timeline and V8 CPU samples. JSON trace and waterfall files have a .md suffix solely because the approved artifact transport copies .md/.png. Rename chrome-trace.json.md to .json for Chrome Performance. Waterfall offsets are relative to the first measured request, not the user event; timings retain DNS/connect/TTFB/response fields when Chromium supplies them. SSE requests can remain open. Cookies, request/response headers and request bodies are excluded. CPU/network throttling is not applied. Single observations require repetition before a phase-2 regression threshold is committed.', '',
  '## Action budget', '',
  'Budget: cold list usable ≤1,000 ms; warm list ≤300 ms; other results ≤100 ms. The table additionally flags any observed >50 ms main-thread task; the explicit typing/scrolling requirement is checked for search and scroll. Failure here is a baseline finding, not a failing CI assertion. A successful Playwright run establishes that measurements were captured, not that the product met its budgets.', '',
  '| Project | Action | Cache | First paint ms | Usable ms | Long tasks count / total ms / max ms | Requests | Budget ms | Result |',
  '| --- | --- | --- | ---: | ---: | --- | ---: | ---: | --- |',
]
const round = value => value == null ? '—' : Math.round(value)
for (const project of ['AIT', 'AEON']) for (const action of actions) for (const cache of ['cold', 'warm']) {
  const row = rows.get(`${project}:${action}:${cache}`)
  const budget = action === 'list' ? (cache === 'cold' ? 1000 : 300) : 100
  if (!row) lines.push(`| ${project} | ${action} | ${cache} | — | — | — | — | ${budget} | UNMEASURED |`)
  else {
    const total = row.long_tasks.reduce((n, task) => n + task.duration_ms, 0)
    const max = Math.max(0, ...row.long_tasks.map(task => task.duration_ms))
    lines.push(`| ${project} | ${action} | ${cache} | ${round(row.first_paint_ms)} | ${round(row.usable_ms)} | ${row.long_tasks.length} / ${round(total)} / ${round(max)} | ${row.request_count} | ${budget} | ${row.pass ? 'PASS' : 'FAIL'} |`)
  }
}
lines.push('', '## Runtime evidence', '')
if (rows.size) {
  const sample = rows.values().next().value
  lines.push(`Actual runner: ${sample.host}, ${sample.platform}/${sample.arch}, Chromium ${sample.chromium}; harness SHA ${sample.source_sha}.`, '',
    '| Action cell | Metrics and waterfall | Chrome trace |', '| --- | --- | --- |')
  for (const row of rows.values()) lines.push(`| ${row.project} ${row.cache} ${row.action} | ${row.artifact} | ${row.artifact.replace('metrics.json.md', 'chrome-trace.json.md')} |`)
} else lines.push('No browser runtime evidence is available. Runner refusals are recorded below. No numeric paint, usable, long-task or network result can be inferred from source inspection.')
lines.push('', '## Slowest three and phase-2 proposals', '',
  'A measured top three cannot be named until the table has runtime evidence. The following are source-inspection candidates, not a measured ranking or confirmed root cause:', '',
  '1. **Search debounce:** web/src/components/work/ListToolbar.vue:127 waits 220 ms after input before sending the query. That delay alone exceeds the 100 ms result budget, before API/render time. Proposal: immediate local feedback over the loaded page, preserving explicit incomplete-result disclosure, with a cancellable server refresh; test query/record identity fences and full server-search semantics. Effort: 0.5–1 day. Risk: medium (stale or falsely complete local results, added request volume). Measure debounce, request and render portions separately before changing it.', '',
  '2. **Column-header hover:** web/src/components/TooltipHost.vue:106 schedules pointer disclosure after 380 ms. The column sort button uses that shared tooltip at web/src/components/work/TicketTable.vue:663. Proposal: decide whether an intentional tooltip dwell is exempt from the click budget; if it is not, add a scoped faster dwell for header guidance while retaining focus/touch behavior and stable control geometry. Effort: 0.25–0.5 day. Risk: low to medium (accidental popovers and accessibility changes). This is not evidence of a long task.', '',
  '3. **List/filtered refresh path:** web/src/lib/useTicketList.ts:104 starts omitted-filter facet reads and web/src/lib/useTicketList.ts:110 reads the full page; web/src/lib/useTicketList.ts:113 adopts page rows into the reactive store. The ticket table then renders those rows. Proposal, only if traces confirm this path dominates: retain a revision/identity-bound completed page during re-entry and batch row adoption; keep server authoritative filtering, live-gap invalidation and explicit partial-data states. Effort: 1–2 days. Risk: medium to high (permission revocation, stale rows, live updates). There is no measured evidence yet that this path is one of the slowest three.', '',
  '## Network and API attribution', '',
  'Browser render/CPU cost belongs to the web client. Request queueing and asset transfer are visible in each waterfall; response wait includes Go handler, Postgres and loopback overhead. Go/API latency is our server code, while real transport RTT, bandwidth, proxy behavior, OIDC and host contention are outside the web client. Loopback timings do not quantify live ppm network/API latency. Agent CLI count reads include CLI startup and public-network/API time and are not web action timings. Source-only candidates cannot assign residual time to the network. Coordinator confirmation of the measured top three in Markus’s person browser, with the required heads-up, remains a later step; this worker never uses that browser.', '',
  '## Validation and handover', '',
  '- Count collector parsed; inventory totals and invalid-count rejection passed without a browser.',
  '- JavaScript syntax checks passed for all harness modules.',
  '- Browser benchmark and full ci-static --merge-main evidence: pending guarded runner capacity.',
  '- Only web/perf/, this report, and the additive web/perf/** ownership entry in scripts/audit/slices.json changed. The measurement benchmark lives outside the product test configs; phase 2 owns the CI performance check and its canonical tier registration.', '',
  'Replay (no origin push; the approved runner uses its own temporary test ref):', '',
  '```sh',
  'REMOTE_ONLY=1 /Users/markus/.aeon/lead/aeon/remote-spec.sh "$PWD" "perf/baseline.spec.mjs --config=perf/playwright.config.mjs --max-failures=2" AEON-1355',
  'node web/perf/report.mjs web/test-results/remote <measured-harness-sha> > docs/perf/aeon-1355-baseline.md',
  '```', '',
)
console.log(lines.join('\n').trimEnd())
