// SPDX-License-Identifier: AGPL-3.0-only
import { execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { collectCandidates, packCandidates } from './visual-candidates.mjs'
import { verifyReferenceArchive } from './visual-baselines.mjs'
import { boundedFile, decodePNG, sha256 } from './visual-pin.mjs'

const need = (condition, message) => { if (!condition) throw new Error(message) }
const cell = value => String(value ?? '').replaceAll('|', '\\|').replace(/[\r\n]+/g, ' ')

// Exact RGBA changes, clustered by adjacent 32px cells. Every changed pixel is
// counted and belongs to one region; reported boxes use actual pixel extents.
export function pixelChanges(before, after) {
  const a = decodePNG(before), b = decodePNG(after)
  need(a.width === b.width && a.height === b.height && a.width <= 1440 && a.height <= 900, 'Historical/candidate image dimensions differ or exceed the matrix')
  const columns = Math.ceil(a.width / 32), cells = new Map()
  let pixels = 0
  for (let y = 0; y < a.height; y++) for (let x = 0; x < a.width; x++) {
    const offset = (y * a.width + x) * 4
    if (a.data.readUInt32LE(offset) === b.data.readUInt32LE(offset)) continue
    pixels++
    const key = Math.floor(y / 32) * columns + Math.floor(x / 32)
    const box = cells.get(key) ?? { x, y, right: x, bottom: y, pixels: 0 }
    box.x = Math.min(box.x, x); box.y = Math.min(box.y, y)
    box.right = Math.max(box.right, x); box.bottom = Math.max(box.bottom, y); box.pixels++
    cells.set(key, box)
  }
  const regions = []
  while (cells.size) {
    const first = cells.keys().next().value, stack = [first], box = { ...cells.get(first) }
    cells.delete(first)
    while (stack.length) {
      const key = stack.pop(), x = key % columns, y = Math.floor(key / columns)
      for (let dy = -1; dy <= 1; dy++) for (let dx = -1; dx <= 1; dx++) {
        if (x + dx < 0 || x + dx >= columns || y + dy < 0) continue
        const next = (y + dy) * columns + x + dx, part = cells.get(next)
        if (!part) continue
        cells.delete(next); stack.push(next)
        box.x = Math.min(box.x, part.x); box.y = Math.min(box.y, part.y)
        box.right = Math.max(box.right, part.right); box.bottom = Math.max(box.bottom, part.bottom); box.pixels += part.pixels
      }
    }
    regions.push({ x: box.x, y: box.y, width: box.right - box.x + 1, height: box.bottom - box.y + 1, pixels: box.pixels })
  }
  return { pixels, total: a.width * a.height, percent: pixels / (a.width * a.height) * 100,
    regionPolicy: '8-connected changed 32x32 cells; boxes enclose exact changed-pixel extents; no pixel tolerance',
    regions: regions.sort((a, b) => a.y - b.y || a.x - b.x) }
}

function whyChanged(state, current, previous, change) {
  if (!change.pixels) return { why: 'Identical decoded pixels to set 2.', unexplained: false }
  const reasons = []
  const body = evidence => evidence?.fonts?.body?.renderedFamilies?.join(', ') ?? 'not recorded'
  if (body(current) !== body(previous)) reasons.push(`AEON-1674 final body-font guard: ${body(previous)} → ${body(current)}`)
  if (JSON.stringify(current.renderer) !== JSON.stringify(previous.renderer)) reasons.push('AEON-1674 final renderer metadata changed (see quality records)')
  if (previous.clock?.now !== current.clock?.now || !previous.clock?.samples?.length)
    reasons.push(`AEON-1674 frozen fixture clock: ${current.clock.now}; prior clock evidence ${previous.clock?.now ?? 'absent'}`)
  if (state === 'account-use-matrix') reasons.push('AEON-1689 desktop toast layout; AEON-1674 mocks the normal quota-threshold line')
  if (state === 'delivery-numbers' && current.viewport.width === 390)
    reasons.push('AEON-1692 requested 12px heading/header gap and complete on-target headline; both assertions already present on capture main')
  // A plausible renderer/UI reason does not prove every changed region. Keep
  // that limit explicit; Tick must review all changed candidate bytes.
  return { why: `${reasons.join('; ') || 'No evidenced renderer, clock or merged UI explanation'}. FLAG: complete pixel attribution remains unproven.`, unexplained: true }
}

function contactSheet(state, records, output, visual) {
  const font = fileURLToPath(new URL('../../web/tests/visual/fonts/inter-v4.1.ttf', import.meta.url))
  const rows = []
  for (const profile of state.profiles) {
    const pair = visual.themes.map(theme => records.find(record => record.quality.id === `V-${state.id}-${visual.profiles[profile].width}-${theme}`))
    if (pair.some(record => !record)) continue
    const inputs = pair.flatMap(record => ['-i', join(output, 'captures', `${record.quality.id}.png`)])
    const tiles = pair.map((record, index) => `[${index}:v]pad=iw:ih+44:0:0:color=0xf4f4f0,drawtext=fontfile='${font}':text='${state.id} / ${visual.profiles[profile].width}px / ${record.quality.theme}':x=8:y=h-32:fontsize=18:fontcolor=0x111111[t${index}]`)
    const name = join(output, 'rows', `${state.id}-${profile}.png`)
    execFileSync('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-nostdin', '-threads', '1', '-filter_complex_threads', '1', ...inputs,
      '-filter_complex', `${tiles.join(';')};[t0][t1]hstack=inputs=2,pad=2880:ih:0:0:color=0xf4f4f0[out]`,
      '-map', '[out]', '-frames:v', '1', '-threads', '1', name], { timeout: 30_000, maxBuffer: 64 * 1024, stdio: ['ignore', 'pipe', 'pipe'] })
    rows.push(name)
  }
  need(rows.length === state.profiles.length, `Incomplete contact sheet: ${state.id}`)
  execFileSync('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-nostdin', '-threads', '1', '-filter_complex_threads', '1',
    ...rows.flatMap(path => ['-i', path]), '-filter_complex', `${rows.map((_, i) => `[${i}:v]`).join('')}vstack=inputs=${rows.length}[out]`,
    '-map', '[out]', '-frames:v', '1', '-threads', '1', join(output, `${state.id}.png`)],
  { timeout: 30_000, maxBuffer: 64 * 1024, stdio: ['ignore', 'pipe', 'pipe'] })
}

export function writeReviewPack(records, visual, historyDirectory, output) {
  const { review } = packCandidates(records, visual)
  need(records.every(record => record.quality.quality?.status === 'passed'), 'Review pack requires recorded quality passes')
  const historical = visual.history.find(row => row.referenceSet === 2)
  const source = historical.baselineAuthority.source
  const archive = boundedFile(join(historyDirectory, source.name), 64 * 1024 * 1024)
  const detachedBytes = boundedFile(join(historyDirectory, source.manifest.name))
  const { references } = verifyReferenceArchive(archive, detachedBytes, visual)
  const tarQuality = id => JSON.parse(execFileSync('tar', ['-xOf', '-', `quality/${id}.json`],
    { input: archive, timeout: 10_000, maxBuffer: 256 * 1024, stdio: ['pipe', 'pipe', 'pipe'] }))
  mkdirSync(output, { recursive: false })
  for (const name of ['captures', 'quality', 'changes', 'rows']) mkdirSync(join(output, name))
  const fonts = ['# FONT EVIDENCE — read first', '',
    `Main capture: ${review.captureSHA}, CI run 39949629594. Candidate bytes require Tick review; no set-3 authority is active.`, '',
    `Vendored Inter 4.1 SHA-256: \`${visual.renderer.fonts[0].sha256}\`.`, '',
    '| State / checkpoint | Computed body stack | Rendered body face (CDP font guard) | Painted faces | Clock |',
    '| --- | --- | --- | --- | --- |' ]
  const changes = [], index = ['# Reference set 3 — Tick review pack', '',
    '[FONT EVIDENCE FIRST](font-evidence.md). Then review one contact sheet per captured state. Light is left, dark is right; each profile is a separate row at native resolution with labels beneath each tile.', '',
    `Source: main ${review.captureSHA}, [CI run 39949629594](https://github.com/inspr-at/paimos/actions/runs/39949629594).`, '',
    'Set 2 is historical comparison input only. Current manifest remains report-only and unpinned. “In this set” means candidate, never approval.', '',
    'Delivery 390: capture main already includes the heading top ≥ header bottom +12px assertion beside the full “of 13 numbers are on target.” headline-in-view assertion. A strict quality pass proves both; Tick authorized pinning these two without another look.', '',
    'Changed percentages count every unequal decoded RGBA pixel. Region boxes use 8-connected changed 32px cells and exact pixel extents. Possible explanations are evidence-based hypotheses; unresolved attribution is explicitly flagged.', '',
    '| State | Contact sheet | Change list vs set 2 |', '| --- | --- | --- |' ]
  for (const state of visual.states) {
    const captured = records.filter(record => state.checkpoints.some(checkpoint => checkpoint.id === record.quality.id))
    if (!captured.length) continue
    const stateChanges = []
    for (const record of captured) {
      const { quality, png } = record, id = quality.id, previous = tarQuality(id)
      const change = pixelChanges(references.get(`${id}.png`), png)
      const reason = whyChanged(state.id, quality, previous, change)
      const row = { id, state: state.id, ...change, ...reason, candidateSHA256: sha256(png),
        historicalSHA256: sha256(references.get(`${id}.png`)), previousFonts: previous.fonts, fonts: quality.fonts,
        previousClock: previous.clock ?? null, clock: quality.clock }
      changes.push(row); stateChanges.push(row)
      writeFileSync(join(output, 'captures', `${id}.png`), png, { flag: 'wx' })
      writeFileSync(join(output, 'quality', `${id}.json`), JSON.stringify(quality, null, 2) + '\n', { flag: 'wx' })
      fonts.push(`| ${id} | ${cell(quality.fonts.body.computedFamily)} | ${cell(quality.fonts.body.renderedFamilies.join(', '))} | ${cell(quality.fonts.painted.map(face => face.family).join(', '))} | ${quality.clock.now} |`)
    }
    contactSheet(state, captured, output, visual)
    index.push(`| ${state.id} | [contact sheet](${state.id}.png) | [changes](changes/${state.id}.md) |`)
    writeFileSync(join(output, 'changes', `${state.id}.json`), JSON.stringify(stateChanges, null, 2) + '\n', { flag: 'wx' })
    writeFileSync(join(output, 'changes', `${state.id}.md`), [`# ${state.id} — changes vs set 2`, '',
      '| Checkpoint | Changed pixels | Changed % | Bounding boxes (x,y,w,h) | Why / unresolved attribution |', '| --- | ---: | ---: | --- | --- |',
      ...stateChanges.map(row => `| ${row.id} | ${row.pixels} / ${row.total} | ${row.percent.toFixed(6)}% | ${row.regions.map(box => `(${box.x},${box.y},${box.width},${box.height})`).join('; ') || 'none'} | ${cell(row.why)} |`), '',
      'The adjacent JSON retains every region and its changed-pixel count, old/new font evidence, clock records and image digests.', '',
    ].join('\n'), { flag: 'wx' })
  }
  const counts = { inSet: records.length, awaiting: 0, deferred: 0, unexplained: changes.filter(row => row.unexplained).length }
  index.push('', '| Checkpoint | Disposition | Reason |', '| --- | --- | --- |')
  for (const state of visual.states) for (const checkpoint of state.checkpoints) {
    const included = records.some(record => record.quality.id === checkpoint.id)
    const disposition = included ? 'in this set' : state.id === 'goals' ? 'awaiting' : 'deferred'
    if (!included) counts[disposition]++
    const reason = included ? checkpoint.id.startsWith('V-delivery-numbers-390-') ?
      'Both capture assertions passed; standing Tick consent, no additional look required.' : 'Quality passed; normal Tick review pending.' :
      state.id === 'goals' ? 'Awaiting AEON-1621 / AEON-1625 v6 canvas before capture and normal Tick review.' : state.deferredReason
    index.push(`| ${checkpoint.id} | ${disposition} | ${cell(reason)} |`)
  }
  index.push('', `Counts: **${counts.inSet} in this set / ${counts.awaiting} awaiting / ${counts.deferred} deferred**.`, '',
    `**${counts.unexplained} changed checkpoints have unresolved complete pixel attribution**, visibly flagged in their change lists. These are review findings, not suppressed or automatically accepted.`, '',
    'Source assertion evidence: `web/tests/visual/fixtures.ts` in the capture SHA (Delivery phone geometry and headline); normal quota thresholds are mocked by `web/tests/visual/chrome-fixture.ts`. AEON-1689 is merged in this capture. The quality files record passed font, fixed-clock, layout, axe, console and response checks.', '')
  writeFileSync(join(output, 'font-evidence.md'), fonts.join('\n') + '\n', { flag: 'wx' })
  writeFileSync(join(output, 'index.md'), index.join('\n'), { flag: 'wx' })
  writeFileSync(join(output, 'changes.json'), JSON.stringify({ captureSHA: review.captureSHA, historicalSource: source, counts, checkpoints: changes }, null, 2) + '\n', { flag: 'wx' })
  return counts
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [captures, history, output] = process.argv.slice(2)
    need(captures && history && output, 'Usage: visual-review.mjs <capture-artifact-directory> <verified-set-2-directory> <new-review-pack-directory>')
    const visual = JSON.parse(readFileSync(new URL('./visual-manifest.json', import.meta.url), 'utf8'))
    console.log(JSON.stringify(writeReviewPack(collectCandidates(captures), visual, history, output)))
  } catch (error) { console.error(`Review pack failed: ${error.message}`); process.exitCode = 1 }
}
