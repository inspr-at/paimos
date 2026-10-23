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
import { controlStability } from './control-stability'
import { mockSettings, settingsData } from './settings-fixtures'

function command(info: TestInfo, name: string, executable: string, args: string[]) {
  const result = spawnSync(executable, args, { cwd: process.cwd(), encoding: 'utf8', timeout: 240_000, maxBuffer: 8 * 1024 * 1024 })
  const log = `${result.stdout ?? ''}\n${result.stderr ?? ''}`
  const path = info.outputPath(`aeon-1698/${name}.txt`)
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

  // Scratch aliases reproduce the old geometry without changing the worktree
  // or leaving a negative-control commit on the assigned branch.
  const source = readFileSync('src/lib/deliveryFlow.ts', 'utf8')
  const old = command(info, 'old-geometry-source-r3', 'git', ['show', '9698d2de7:web/src/lib/deliveryFlow.ts'])
  expect(old.status).toBe(0)
  for (const kind of ['avatar', 'cluster'] as const) {
    const control = info.outputPath(`aeon-1698/negative-${kind}-r3`)
    mkdirSync(control, { recursive: true })
    let mutated = source
    if (kind === 'avatar') {
      // The old icon's centre after translate(-13,-27) scale(.8).
      mutated = source.replace('return { y: barY + barH / 2, scale, h: 20 * scale }', 'return { y: barY + 1 - 27 + 17 * 0.8, scale: 0.8, h: 16 }')
    } else {
      const marker = '// ---------- Overview touch targets ----------'
      const a = source.indexOf('export function rowPieces'), b = source.indexOf(marker, a)
      const c = old.log.indexOf('export function rowPieces'), d = old.log.indexOf(marker, c)
      expect(c).toBeGreaterThan(0)
      expect(d).toBeGreaterThan(c)
      mutated = source.slice(0, a) + old.log.slice(c, d) + source.slice(b)
    }
    expect(mutated).not.toBe(source)
    writeFileSync(join(control, 'deliveryFlow.ts'), mutated)
    const replacement = JSON.stringify(join(control, 'deliveryFlow.ts'))
    writeFileSync(join(control, 'vitest.config.mjs'), `export default { resolve: { alias: [{ find: /^.*\\/deliveryFlow(?:\\.ts)?$/, replacement: ${replacement} }] } }`)
    const title = kind === 'avatar' ? 'ten concurrent builders take ten rows' : 'slivers in a row merge'
    const negative = command(info, `negative-${kind}-r3`, process.execPath, ['node_modules/vitest/vitest.mjs', 'run', 'tests/delivery-flow-lanes.unit.test.ts', '--maxWorkers=1', '--config', join(control, 'vitest.config.mjs'), '-t', title])
    expect(negative.status).not.toBe(0)
    expect(negative.log).toContain('AssertionError')
    if (kind === 'cluster') expect(negative.log).toContain('390px live row 3 gap')
    else expect(negative.log).toContain('to be 170.5')
    console.log(`AEON1698 negative ${kind}: PASS (old geometry rejected)`)
  }

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
  const main = packed!.main
  const current = main.steps.find(step => step.facts?.id === main.facts?.currentStepId)!
  expect(current.facts?.open).toBe(true)
  const currentKey = `0:${main.id}:${main.steps.indexOf(current)}`
  const measured: unknown[] = []
  try {
    // A light failure stops this one test immediately, before the dark capture.
    for (const width of [1440, 390]) for (const theme of ['light'] as const) {
      const context = await browser.newContext({ viewport: { width, height: 1100 }, timezoneId: ZONE, reducedMotion: 'reduce' })
      try {
        const page = await context.newPage()
        await page.clock.install({ time: new Date(LIVE_HARNESS_NOW) })
        const work = fixtures()
        work.preferences.theme = { choice: theme }
        work.preferences['delivery:numbers'] = { level: 'expert', window: 7 }
        await mockWork(page, work)
        if (width !== 1440) { const settings = settingsData(); settings.profile.locale = 'de-AT'; await mockSettings(page, settings) }
        await mockDelivery(page, async () => ({ status: 200, body: deliveryMetrics() }), ['delivery.read'])
        await page.route(/\/api\/projects\/[^/]+\/delivery\/flow(\?|$)/, route => route.fulfill({ json: answer }))
        await page.route(/\/api\/projects\/[^/]+\/delivery\/flow\/stream/, route => route.fulfill({ contentType: 'text/event-stream', body: 'retry: 600000\n\n' }))
        await page.goto(`http://127.0.0.1:${port}/p/AEON/delivery?view=flow`)
        const host = page.getByTestId('flow-lanes')
        // AEON-1609 pins the production display language to English; region
        // preferences do not switch it. Narrow captures retain a German region.
        await expect(page.getByRole('radio', { name: 'Expert', exact: true })).toHaveAttribute('aria-checked', 'true')
        await expect(page.getByTestId('flow-modes').getByRole('radio', { name: 'Live', exact: true })).toHaveAttribute('aria-checked', 'true')
        await expect(page.getByTestId('flow-clock')).toHaveText('At 12:06 (now)')
        await expect(host.locator('.ln-lane')).toHaveCount(6)
        const bar = page.getByTestId('flow-bar')
        const fit = bar.getByRole('radio', { name: 'Fit all', exact: true })
        const guard = await controlStability(page, { fit, zoom: bar.getByRole('radiogroup'), follow: page.getByTestId('flow-follow'), readout: page.getByTestId('flow-readout'), lanes: host })
        await guard.check(() => fit.click())
        await guard.check(() => bar.getByRole('radio', { name: '15 min', exact: true }).click())
        await guard.check(() => fit.click())
        guard.done()
        await page.mouse.move(0, 0)
        const moment = page.getByTestId('flow-moment')
        const builder = moment.locator('li:not(.idle)').filter({ hasText: '1674 · Build r2' })
        await expect(builder.locator('b')).toHaveText('Builder')
        await expect(moment.locator('.idle')).not.toContainText('Builder')
        const momentText = await page.getByTestId('flow-moment-head').textContent()
        expect(momentText).toContain('Builder on “Being built · round 2”')
        // A phone can combine the two adjacent tiny historical attempts into
        // the existing +N cluster. All 21 steps must still be represented.
        const clusters = await host.locator('[data-cluster]').evaluateAll(groups => groups.map(group => Number(group.querySelector('.ln-clut')!.textContent!.slice(1))))
        const barCount = await host.locator('[data-step]').count()
        expect(barCount + clusters.reduce((sum, count) => sum + count, 0)).toBe(21)
        if (width === 1440) { expect(barCount).toBe(21); expect(clusters).toEqual([]) }
        const geometry = await host.evaluate(host => {
          const box = (element: Element) => { const r = element.getBoundingClientRect(); return { x: r.x, y: r.y, w: r.width, h: r.height } }
          const lane = box(host.querySelector('.ln-lane[data-lane="build"]')!)
          const bars = Array.from(host.querySelectorAll('[data-step]')).map(group => {
            const rects = Array.from(group.querySelectorAll('.fl-seg')).map(box)
            const x = Math.min(...rects.map(r => r.x)), y = Math.min(...rects.map(r => r.y))
            const label = group.querySelector('.fl-t')
            return { key: group.getAttribute('data-step')!, x, y, w: Math.max(...rects.map(r => r.x + r.w)) - x, h: Math.max(...rects.map(r => r.y + r.h)) - y, label: label ? box(label) : null, labelText: label?.textContent ?? '' }
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
            const run = answer.items.find(item => item.id === bar.key.split(':')[1])!
            expect(bar.labelText.startsWith(run.ref.replace('AEON-', ''))).toBe(true)
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
            const end = (step: typeof s) => step.ended_at == null ? Date.parse(answer.now) + 60_000 : Date.parse(step.ended_at)
            if (Date.parse(s.started_at) < end(t) && Date.parse(t.started_at) < end(s)) timeOverlaps.push(`${a.key} x ${b.key}`)
          }
        }
        await expect(host.getByTestId('flow-lane-fold')).toHaveCount(0)
        const avatar = await host.evaluate((host, key) => {
          const avatar = host.querySelector<SVGGElement>('[data-testid="flow-avatar"]')!
          const matrix = avatar.transform.baseVal.consolidate()!.matrix
          const rect = Array.from(host.querySelectorAll('[data-step]')).find(group => group.getAttribute('data-step') === key)!.querySelector<SVGRectElement>('.fl-seg')!
          const r = avatar.getBoundingClientRect()
          return { x: matrix.e, y: matrix.f, stepY: rect.y.baseVal.value, stepH: rect.height.baseVal.value, playheadX: Number(host.querySelector('.ln-ph')!.getAttribute('x1')), box: { x: r.x, y: r.y, w: r.width, h: r.height } }
        }, currentKey)
        expect(avatar.y).toBeCloseTo(avatar.stepY + avatar.stepH / 2, 4)
        expect(avatar.x).toBeCloseTo(avatar.playheadX, 4)
        const rowH = (lane.h - 4) / 10, currentBar = bars.find(bar => bar.key === currentKey)!
        const rowTop = currentBar.y - 2, rowBottom = rowTop + rowH
        expect(avatar.box.y).toBeGreaterThanOrEqual(rowTop)
        expect(avatar.box.y + avatar.box.h).toBeLessThanOrEqual(rowBottom)
        expect(avatar.box.h).toBeLessThanOrEqual(rowH - 2)
        expect(avatar.box.y + avatar.box.h / 2).toBeCloseTo(currentBar.y + currentBar.h / 2, 2)
        const collides = (a: { x: number; y: number; w: number; h: number }, b: { x: number; y: number; w: number; h: number }) => Math.min(a.x + a.w, b.x + b.w) > Math.max(a.x, b.x) && Math.min(a.y + a.h, b.y + b.h) > Math.max(a.y, b.y)
        const otherBars = bars.filter(bar => bar.key !== currentKey)
        const avatarOverlaps = otherBars.filter(bar => collides(avatar.box, bar)).map(bar => bar.key)
        expect(avatarOverlaps).toEqual([])
        const pieces = await host.locator('[data-step], [data-cluster]').evaluateAll(groups => groups.map(group => {
          const rects = Array.from(group.querySelectorAll('.fl-seg, .ln-cluster')).map(rect => rect.getBoundingClientRect())
          const x = Math.min(...rects.map(r => r.x)), y = Math.min(...rects.map(r => r.y))
          return { x, y, w: Math.max(...rects.map(r => r.right)) - x, h: Math.max(...rects.map(r => r.bottom)) - y, cluster: group.getAttribute('data-cluster'), text: group.querySelector('.ln-clut')?.textContent }
        }))
        const gaps: number[] = []
        for (let row = 0; row < 10; row++) {
          const ordered = pieces.filter(piece => Math.floor((piece.y - lane.y) / rowH) === row).sort((a, b) => a.x - b.x)
          for (let k = 1; k < ordered.length; k++) gaps.push(ordered[k]!.x - ordered[k - 1]!.x - ordered[k - 1]!.w)
        }
        expect(Math.min(...gaps)).toBeGreaterThanOrEqual(2)
        if (width === 390) expect(pieces.filter(piece => piece.cluster).map(piece => piece.text)).toEqual(['+3'])
        // Recreate the exact old SVG transforms on this live fixture. Measure
        // and restore them before the two requested screenshots are taken.
        const negativeAvatar = await host.evaluate((host, old) => {
          const av = host.querySelector<SVGGElement>('[data-testid="flow-avatar"]')!
          const wrapper = av.firstElementChild!, glyph = wrapper.firstElementChild!
          const badges = Array.from(wrapper.querySelectorAll('.av-inc, .av-wait'))
          const elements = [av, wrapper, glyph, ...badges]
          const saved = elements.map(element => element.getAttribute('transform'))
          try {
            av.setAttribute('transform', `translate(${old.x},${old.stepY + 1})`)
            wrapper.removeAttribute('transform')
            glyph.setAttribute('transform', 'translate(-13,-27) scale(.8)')
            for (const badge of badges) badge.setAttribute('transform', 'translate(11,-24)')
            const r = av.getBoundingClientRect()
            return { x: r.x, y: r.y, w: r.width, h: r.height }
          } finally {
            elements.forEach((element, k) => saved[k] == null ? element.removeAttribute('transform') : element.setAttribute('transform', saved[k]!))
          }
        }, avatar)
        expect(negativeAvatar.y).toBeLessThan(rowTop)
        const negativeOverlaps = otherBars.filter(bar => collides(negativeAvatar, bar)).map(bar => bar.key)
        expect(negativeOverlaps.length).toBeGreaterThan(0)
        const rowYs = [...new Set(bars.map(bar => Math.round(bar.y * 2) / 2))].sort((a, b) => a - b)
        const avatarRow = rowYs.indexOf(Math.round(bars.find(bar => bar.key === currentKey)!.y * 2) / 2) + 1
        const record = { width, theme, steps: answer.steps.length, bars: bars.length, clusters, rows: geometry.rows, laneHeight: lane.h, openSteps: answer.steps.filter(step => step.ended_at == null).length, momentText, avatarRow, avatar, rowTop, rowBottom, avatarOverlaps, negativeAvatar, negativeOverlaps, minimumGap: Math.min(...gaps), pieces, currentKey, overlappingBars: overlaps.length, sameRowTimeOverlaps: timeOverlaps.length, labels, labelsOutsideOwnBar: outside.length, geometry }
        measured.push(record)
        const output = info.outputPath('aeon-1698/checks-r3.json')
        writeFileSync(output, JSON.stringify(measured, null, 2))
        await host.locator('.ln-lane[data-lane="build"]').screenshot({ path: info.outputPath(`aeon-1698/flow-live-${width}-${theme}-builder-r3.png`) })
        console.log(`AEON1697 ${JSON.stringify({ ...record, geometry: undefined })}`)
        expect(overlaps).toEqual([])
        expect(timeOverlaps).toEqual([])
        expect(labels).toBeGreaterThan(0)
        expect(outside).toEqual([])
      } finally { await context.close() }
    }
  } finally { await new Promise<void>(resolve => server.close(() => resolve())) }
})
