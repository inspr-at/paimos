// SPDX-License-Identifier: AGPL-3.0-only
// Temporary remote evidence driver for AEON-1697, removed after capture. The
// enduring regression extends the already registered lanes unit case only.
import { expect, test, type TestInfo } from '@playwright/test'
import { spawnSync } from 'node:child_process'
import { createServer } from 'node:http'
import { readFile, stat } from 'node:fs/promises'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { extname, join, resolve, sep } from 'node:path'
import { fixtures, mockWork } from './work-fixtures'
import { deliveryMetrics, mockDelivery } from './delivery-numbers-fixtures'
import { ZONE } from './delivery-flow-fixtures'
import { LIVE_HARNESS_NOW, liveHarnessFlow } from './delivery-flow-live-fixtures'
import { laneModel, laneRows } from '../src/lib/deliveryFlow'
import { liveData, recordedRuns } from '../src/lib/deliveryFlowData'

function command(info: TestInfo, name: string, executable: string, args: string[]) {
  const result = spawnSync(executable, args, { cwd: process.cwd(), encoding: 'utf8', timeout: 240_000, maxBuffer: 8 * 1024 * 1024 })
  const log = `${result.stdout ?? ''}\n${result.stderr ?? ''}`
  const path = info.outputPath(`aeon-1697/${name}.txt`)
  mkdirSync(resolve(path, '..'), { recursive: true })
  writeFileSync(path, log)
  expect(result.error, `${name}: ${String(result.error)}`).toBeUndefined()
  return { status: result.status, log }
}

test('AEON-1697 manual Expert Live proof from harness records', async ({ browser }, info) => {
  test.setTimeout(900_000)
  // Build and check on the admitted remote host; never on the working Mac.
  for (const task of ['typecheck', 'lint', 'build']) {
    const result = command(info, task, 'npm', ['run', task])
    expect(result.status, result.log.slice(-5000)).toBe(0)
    console.log(`AEON1697 ${task}: PASS`)
  }
  const unit = command(info, 'lanes-unit', process.execPath, ['node_modules/vitest/vitest.mjs', 'run', 'tests/delivery-flow-lanes.unit.test.ts', '--maxWorkers=1'])
  expect(unit.status, unit.log.slice(-5000)).toBe(0)
  console.log(`AEON1697 unit: ${unit.log.match(/Tests\s+[^\n]+/)?.[0] ?? 'PASS'}`)

  // A scratch copy restores only the old four-row allocation, retaining today's
  // API. The named behaviour case must fail at the ten-row assertion.
  const control = info.outputPath('aeon-1697/negative-control')
  mkdirSync(control, { recursive: true })
  const source = readFileSync('src/lib/deliveryFlow.ts', 'utf8')
  const current = 'if (row < 0) { row = ends.length; ends.push(0) }'
  expect(source).toContain(current)
  writeFileSync(join(control, 'deliveryFlow.ts'), source.replace(current, 'if (row < 0) { row = Math.min(ends.length, 3); if (row === ends.length) ends.push(0) }'))
  writeFileSync(join(control, 'vitest.config.mjs'), `export default { resolve: { alias: [{ find: /(?:^|\\/)deliveryFlow(?:\\.ts)?$/, replacement: ${JSON.stringify(join(control, 'deliveryFlow.ts'))} }] } }`)
  const negative = command(info, 'negative-control', process.execPath, ['node_modules/vitest/vitest.mjs', 'run', 'tests/delivery-flow-lanes.unit.test.ts', '--maxWorkers=1', '--config', join(control, 'vitest.config.mjs'), '-t', 'ten concurrent builders take ten rows'])
  expect(negative.status).not.toBe(0)
  expect(negative.log).toMatch(/expected 4 to be 10/)
  console.log('AEON1697 negative control: old four-row packing rejected (expected 4 to be 10)')

  // Serve the actual production build on a fresh loopback port. The runner also
  // owns its normal dev server, but proof pages read only these built assets.
  const root = resolve('dist')
  const types: Record<string, string> = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.json': 'application/json', '.woff2': 'font/woff2', '.png': 'image/png', '.ico': 'image/x-icon' }
  const server = createServer(async (request, response) => {
    try {
      let path = resolve(root, `.${decodeURIComponent(new URL(request.url!, 'http://localhost').pathname)}`)
      if (!(path === root || path.startsWith(root + sep))) { response.writeHead(400); response.end(); return }
      if (!(await stat(path).catch(() => null))?.isFile()) path = join(root, 'index.html')
      response.writeHead(200, { 'content-type': types[extname(path)] ?? 'application/octet-stream' })
      response.end(await readFile(path))
    } catch { response.writeHead(500); response.end() }
  })
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve))
  const port = (server.address() as { port: number }).port
  const answer = liveHarnessFlow()
  const [packed] = laneModel(liveData(recordedRuns(answer, { extendOpen: true }))!)
  expect(packed!.rows.build).toBe(10)
  expect(laneRows(packed!, 'build', false)).toEqual({ bars: 10, fold: null, total: 10 })
  const measured: unknown[] = []
  try {
    // A light failure stops this one test immediately, before the dark capture.
    for (const theme of ['light', 'dark'] as const) {
      const context = await browser.newContext({ viewport: { width: 1440, height: 1100 }, timezoneId: ZONE, reducedMotion: 'reduce' })
      try {
        const page = await context.newPage()
        await page.clock.install({ time: new Date(LIVE_HARNESS_NOW) })
        const work = fixtures()
        work.preferences.theme = { choice: theme }
        work.preferences['delivery:numbers'] = { level: 'expert', window: 7 }
        await mockWork(page, work)
        await mockDelivery(page, async () => ({ status: 200, body: deliveryMetrics() }), ['delivery.read'])
        await page.route(/\/api\/projects\/[^/]+\/delivery\/flow(\?|$)/, route => route.fulfill({ json: answer }))
        await page.route(/\/api\/projects\/[^/]+\/delivery\/flow\/stream/, route => route.fulfill({ contentType: 'text/event-stream', body: 'retry: 600000\n\n' }))
        await page.goto(`http://127.0.0.1:${port}/p/AEON/delivery?view=flow`)
        const host = page.getByTestId('flow-lanes')
        await expect(page.getByRole('button', { name: 'Expert', exact: true })).toHaveAttribute('aria-pressed', 'true')
        await expect(page.getByTestId('flow-modes').getByRole('radio', { name: 'Live', exact: true })).toHaveAttribute('aria-checked', 'true')
        await expect(page.getByTestId('flow-clock')).toHaveText('At 12:06 (now)')
        await expect(host.locator('.ln-lane')).toHaveCount(6)
        await page.getByTestId('flow-bar').getByRole('radio', { name: 'Fit all' }).click()
        await page.mouse.move(0, 0)
        await expect(host.locator('[data-step]')).toHaveCount(21)
        const geometry = await host.evaluate(host => {
          const box = (element: Element) => { const r = element.getBoundingClientRect(); return { x: r.x, y: r.y, w: r.width, h: r.height } }
          const lane = box(host.querySelector('.ln-lane[data-lane="build"]')!)
          const bars = Array.from(host.querySelectorAll('[data-step]')).map(group => {
            const rects = Array.from(group.querySelectorAll('.fl-seg')).map(box)
            const x = Math.min(...rects.map(r => r.x)), y = Math.min(...rects.map(r => r.y))
            const label = group.querySelector('.fl-t')
            return { key: group.getAttribute('data-step')!, x, y, w: Math.max(...rects.map(r => r.x + r.w)) - x, h: Math.max(...rects.map(r => r.y + r.h)) - y, label: label ? box(label) : null }
          })
          return { lane, bars, rows: new Set(bars.map(bar => Math.round(bar.y * 2) / 2)).size }
        })
        const { bars, lane } = geometry
        expect(geometry.rows).toBe(10)
        expect(lane.h).toBeGreaterThanOrEqual(10 * 15)
        const overlaps: string[] = [], outside: string[] = [], timeOverlaps: string[] = []
        let labels = 0
        for (const bar of bars) {
          expect(bar.w).toBeGreaterThan(0)
          expect(bar.y).toBeGreaterThanOrEqual(lane.y - 0.5)
          expect(bar.y + bar.h).toBeLessThanOrEqual(lane.y + lane.h + 0.5)
          const label = bar.label
          if (label) {
            labels++
            if (label.x < bar.x - 0.5 || label.x + label.w > bar.x + bar.w + 0.5 || label.y < bar.y - 0.5 || label.y + label.h > bar.y + bar.h + 0.5) outside.push(bar.key)
          }
        }
        for (let i = 0; i < bars.length; i++) for (let j = i + 1; j < bars.length; j++) {
          const a = bars[i]!, b = bars[j]!
          const across = Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x), down = Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y)
          if (across > 0.5 && down > 0.5) overlaps.push(`${a.key} x ${b.key}`)
          if (Math.abs(a.y - b.y) <= 0.5) {
            const stepOf = (key: string) => { const [, item, index] = key.split(':'); return answer.steps.filter(step => step.item_id === item).sort((a, b) => Date.parse(a.started_at) - Date.parse(b.started_at))[Number(index)]! }
            const s = stepOf(a.key), t = stepOf(b.key)
            if (Date.parse(s.started_at) < Date.parse(t.ended_at!) && Date.parse(t.started_at) < Date.parse(s.ended_at!)) timeOverlaps.push(`${a.key} x ${b.key}`)
          }
        }
        await expect(host.getByTestId('flow-lane-fold')).toHaveCount(0)
        const record = { width: 1440, theme, steps: bars.length, rows: geometry.rows, laneHeight: lane.h, overlappingBars: overlaps.length, sameRowTimeOverlaps: timeOverlaps.length, labels, labelsOutsideOwnBar: outside.length, geometry }
        measured.push(record)
        const output = info.outputPath('aeon-1697/checks.json')
        writeFileSync(output, JSON.stringify(measured, null, 2))
        // Preserve the failing render too; no component edits in this round.
        await page.screenshot({ path: info.outputPath(`aeon-1697/flow-live-1440-${theme}-full.png`), fullPage: true })
        await host.locator('.ln-lane[data-lane="build"]').screenshot({ path: info.outputPath(`aeon-1697/flow-live-1440-${theme}-builder.png`) })
        console.log(`AEON1697 ${JSON.stringify({ ...record, geometry: undefined })}`)
        expect(overlaps).toEqual([])
        expect(timeOverlaps).toEqual([])
        expect(labels).toBeGreaterThan(0)
        expect(outside).toEqual([])
      } finally { await context.close() }
    }
  } finally { await new Promise<void>(resolve => server.close(() => resolve())) }
})
