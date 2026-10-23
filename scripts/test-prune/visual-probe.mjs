// SPDX-License-Identifier: AGPL-3.0-only
import { spawnSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { collectCandidates } from './visual-candidates.mjs'
import { boundedFile, decodePNG, sha256 } from './visual-pin.mjs'
import { assertRenderer, assertVisualContainer, captureEnabled } from './visual-renderer.mjs'

const need = (condition, message) => { if (!condition) throw new Error(message) }
const root = fileURLToPath(new URL('../../', import.meta.url))
export const PROBE_REPEATS = 6
const raster = '--num-raster-threads=1', compositor = '--run-all-compositor-stages-before-draw', partial = '--disable-partial-raster'
export const PROBE_SETTINGS = [
  { id: 'baseline', args: [] },
  { id: 'single-raster-thread', args: [raster] },
  { id: 'all-compositor-stages', args: [compositor] },
  { id: 'no-partial-raster', args: [partial] },
  { id: 'combined', args: [raster, compositor, partial] },
]

export function probeCheckpoints(input, visual) {
  need(typeof input === 'string' && input.length > 0 && input.length <= 2048, 'Probe needs a bounded comma-separated checkpoint list')
  const ids = input.split(',').map(id => id.trim())
  const candidates = visual.states.filter(state => visual.capture.states.includes(state.id)).flatMap(state => state.checkpoints.map(checkpoint => checkpoint.id))
  need(ids.length <= 8 && new Set(ids).size === ids.length && ids.every(id => candidates.includes(id)),
    'Probe needs 1–8 distinct checkpoints from the manual capture set')
  return ids
}

// Only the manual capture job can use these flags. Default capture and blocking
// comparison never read candidate flags from arbitrary command-line text.
export function visualProbe(visual, env = process.env) {
  if (!env.AEON_VISUAL_PROBE) {
    need(!env.AEON_VISUAL_PROBE_SETTING, 'Probe setting requires an explicit checkpoint list')
    return null
  }
  need(captureEnabled(visual, env), 'Probe requires manual capture mode')
  const checkpoints = probeCheckpoints(env.AEON_VISUAL_PROBE, visual)
  const setting = PROBE_SETTINGS.find(setting => setting.id === env.AEON_VISUAL_PROBE_SETTING)
  need(setting, 'Unknown raster probe setting')
  return { ...setting, checkpoints }
}

export function summarizeProbe(records, checkpoints, setting, visual, { exitCode = 0, error = null } = {}) {
  const expected = { ...visual.renderer, browser: { ...visual.renderer.browser, args: [...visual.renderer.browser.args, ...setting.args] } }
  const rows = checkpoints.map(id => {
    const samples = records.filter(record => record.quality.id === id).sort((a, b) => (a.quality.diagnosticProbe?.repeatIndex ?? -1) - (b.quality.diagnosticProbe?.repeatIndex ?? -1))
    const indices = samples.map(record => record.quality.diagnosticProbe?.repeatIndex)
    const checkpoint = visual.states.flatMap(state => state.checkpoints).find(checkpoint => checkpoint.id === id)
    const viewport = visual.profiles[checkpoint.profile]
    const complete = samples.length === PROBE_REPEATS && indices.every((index, i) => index === i)
    const failures = []
    for (const { quality, png } of samples) {
      try {
        need(quality.diagnosticProbe.setting === setting.id && quality.captureMode === false && quality.baselineCompared === false &&
          quality.requirementsRun === true && quality.quality?.status === 'passed', 'Missing diagnostic-only quality evidence')
        need(JSON.stringify(quality.diagnosticProbe.args) === JSON.stringify(expected.browser.args), 'Probe launch arguments differ')
        assertRenderer(quality.renderer, expected)
        const image = decodePNG(png)
        need(image.width === viewport.width && image.height === viewport.height && quality.viewport?.width === viewport.width &&
          quality.viewport?.height === viewport.height, 'Wrong probe viewport')
        need(sha256(png) === quality.pngSHA256, 'Probe PNG digest mismatch')
      } catch (error) { failures.push(error.message) }
    }
    const digests = samples.map(record => sha256(record.png))
    return { id, repeats: samples.length, complete, failures, digests,
      byteIdentical: complete && failures.length === 0 && samples.every(record => record.png.equals(samples[0].png)) }
  })
  const sources = new Set(records.map(record => record.quality.captureSHA))
  const complete = exitCode === 0 && !error && sources.size === 1 && /^[a-f0-9]{40}$/.test([...sources][0] ?? '') &&
    records.length === checkpoints.length * PROBE_REPEATS && records.every(record => checkpoints.includes(record.quality.id)) &&
    rows.every(row => row.complete && row.failures.length === 0)
  return { setting: setting.id, args: expected.browser.args, captureSHA: [...sources][0] ?? null,
    exitCode, error, complete, allByteIdentical: complete && rows.every(row => row.byteIdentical), checkpoints: rows }
}

export async function runProbe(input, output, visual, { env = process.env, run = spawnSync, platform = process.platform, arch = process.arch } = {}) {
  need(captureEnabled(visual, env), 'Probe requires manual capture mode')
  assertVisualContainer(visual, { env, platform, arch })
  const checkpoints = probeCheckpoints(input, visual)
  output = resolve(root, output)
  need(output.startsWith(resolve(root, 'web/test-results') + '/'), 'Probe artifacts must stay under web/test-results')
  mkdirSync(output, { recursive: false })
  const specs = [...new Set(visual.states.filter(state => state.checkpoints.some(checkpoint => checkpoints.includes(checkpoint.id))).map(state => state.owner))]
  const settings = []
  for (const setting of PROBE_SETTINGS) {
    const directory = join(output, setting.id), logPath = join(output, `${setting.id}.log`)
    const result = run('npm', ['--prefix', 'web', 'test', '--', ...specs, '--workers=1', '--retries=0',
      `--repeat-each=${PROBE_REPEATS}`, `--grep=(?:${checkpoints.join('|')})$`, '--reporter=line',
      `--output=${resolve(directory)}`], {
      cwd: root, env: { ...env, AEON_VISUAL_PROBE: checkpoints.join(','), AEON_VISUAL_PROBE_SETTING: setting.id },
      timeout: 9 * 60_000, encoding: 'utf8', maxBuffer: 4 * 1024 * 1024,
    })
    writeFileSync(logPath, `${result.stdout ?? ''}\n${result.stderr ?? ''}`, { flag: 'wx' })
    let records = [], error = result.error?.message ?? null
    try { records = collectCandidates(directory, { diagnosticProbe: true }) }
    catch (failure) { error = [error, failure.message].filter(Boolean).join('; ') }
    settings.push(summarizeProbe(records, checkpoints, setting, visual, { exitCode: result.status, error }))
  }
  const sources = new Set(settings.map(setting => setting.captureSHA))
  const report = { version: 1, kind: 'raster-determinism-probe', authority: 'diagnostic only; cannot package, publish or activate references',
    repeats: PROBE_REPEATS, containerDigest: visual.renderer.container.digest, threshold: 0, maxDiffPixels: 0,
    checkpoints, complete: sources.size === 1 && settings.every(setting => setting.complete), settings }
  writeFileSync(join(output, 'probe-report.json'), JSON.stringify(report, null, 2) + '\n', { flag: 'wx' })
  const lines = ['# Raster determinism probe', '', `Diagnostic only; ${PROBE_REPEATS} fresh fixture/browser repeats per setting. No masks or tolerance.`, '',
    '| Setting | Checkpoint | Complete repeats | All PNG bytes identical |', '| --- | --- | ---: | --- |']
  for (const setting of settings) for (const row of setting.checkpoints)
    lines.push(`| ${setting.setting} | ${row.id} | ${row.repeats}/${PROBE_REPEATS} | ${!setting.complete ? 'INCOMPLETE (see JSON/log)' : row.byteIdentical ? 'yes' : 'no'} |`)
  writeFileSync(join(output, 'probe-report.md'), lines.join('\n') + '\n', { flag: 'wx' })
  return report
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [output] = process.argv.slice(2)
    need(output, 'Usage: VISUAL_PROBE_CHECKPOINTS=<IDs> node scripts/test-prune/visual-probe.mjs <new-output-directory>')
    const visual = JSON.parse(boundedFile(new URL('./visual-manifest.json', import.meta.url)))
    const report = await runProbe(process.env.VISUAL_PROBE_CHECKPOINTS, output, visual)
    console.log(readFileSync(join(output, 'probe-report.md'), 'utf8'))
    if (!report.complete) process.exitCode = 1
  } catch (error) { console.error(`Raster probe failed: ${error.message}`); process.exitCode = 1 }
}
