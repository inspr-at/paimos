// SPDX-License-Identifier: AGPL-3.0-only
import { spawnSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { collectCandidates } from './visual-candidates.mjs'
import { boundedFile, decodePNG, sha256 } from './visual-pin.mjs'
import { assertRenderer, assertVisualContainer, captureEnabled, cpuEvidence, preparedRenderer } from './visual-renderer.mjs'

const need = (condition, message) => { if (!condition) throw new Error(message) }
const root = fileURLToPath(new URL('../../', import.meta.url))
export const PROBE_REPEATS = 6
export const PROBE_SLOTS = [1, 2, 3, 4, 5, 6]
export const PROBE_SETTINGS = [
  { id: 'baseline', args: [] },
  { id: 'skia-runtime-disabled', args: ['--disable-skia-runtime-opts', '--force-color-profile=srgb',
    '--disable-lcd-text', '--font-render-hinting=none', '--force-device-scale-factor=1'] },
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

export function summarizeProbe(records, checkpoints, setting, visual, { exitCode = 0, error = null, cpu = null } = {}) {
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
        need(cpu && JSON.stringify(quality.renderer.runner?.cpu) === JSON.stringify(cpu), 'Missing or inconsistent runner CPU evidence')
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

export async function runProbe(input, output, visual, { env = process.env, run = spawnSync, platform = process.platform, arch = process.arch,
  cpu = cpuEvidence({ platform }) } = {}) {
  need(captureEnabled(visual, env), 'Probe requires manual capture mode')
  need(!env.AEON_VISUAL_RENDERER_SETTING || env.AEON_VISUAL_RENDERER_SETTING === 'baseline', 'Probe controls its own settings; leave visual_renderer at baseline')
  assertVisualContainer(visual, { env, platform, arch })
  const checkpoints = probeCheckpoints(input, visual)
  need(JSON.stringify(preparedRenderer(visual, 'skia-runtime-disabled').browser.args) ===
    JSON.stringify([...visual.renderer.browser.args, ...PROBE_SETTINGS[1].args]), 'Prepared renderer and probe arguments differ')
  output = resolve(root, output)
  need(output.startsWith(resolve(root, 'web/test-results') + '/'), 'Probe artifacts must stay under web/test-results')
  // The manual probe runs before Playwright on a fresh container checkout.
  // Create only the parent; an existing probe output must still be refused.
  mkdirSync(dirname(output), { recursive: true })
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
    settings.push(summarizeProbe(records, checkpoints, setting, visual, { exitCode: result.status, error, cpu }))
  }
  const sources = new Set(settings.map(setting => setting.captureSHA))
  const report = { version: 2, kind: 'raster-determinism-probe', authority: 'diagnostic only; cannot package, publish or activate references',
    slot: Number(env.AEON_VISUAL_PROBE_SLOT), run: env.GITHUB_RUN_ID, attempt: env.GITHUB_RUN_ATTEMPT,
    runner: env.RUNNER_NAME ?? 'unidentified', cpu,
    repeats: PROBE_REPEATS, containerDigest: visual.renderer.container.digest, threshold: 0, maxDiffPixels: 0,
    checkpoints, complete: sources.size === 1 && settings.every(setting => setting.complete), settings }
  writeFileSync(join(output, 'probe-report.json'), JSON.stringify(report, null, 2) + '\n', { flag: 'wx' })
  const lines = ['# Raster determinism probe', '', `CPU: ${cpu.model}; flags: ${cpu.flags.join(' ')}; kernel boot: ${cpu.bootId}.`, '',
    `Diagnostic only; ${PROBE_REPEATS} fresh fixture/browser repeats per setting on this runner. Cross-machine proof is in the matrix summary. No masks or tolerance.`, '',
    '| Setting | Checkpoint | Complete repeats | All PNG bytes identical |', '| --- | --- | ---: | --- |']
  for (const setting of settings) for (const row of setting.checkpoints)
    lines.push(`| ${setting.setting} | ${row.id} | ${row.repeats}/${PROBE_REPEATS} | ${!setting.complete ? 'INCOMPLETE (see JSON/log)' : row.byteIdentical ? 'yes' : 'no'} |`)
  writeFileSync(join(output, 'probe-report.md'), lines.join('\n') + '\n', { flag: 'wx' })
  return report
}

// A complete per-runner report is only input to the cross-machine proof.
// Missing, duplicate, failed or stale matrix members can never mean "stable".
export function summarizeProbeMatrix(reports, checkpoints, visual, { captureSHA, run, attempt } = {}) {
  const failures = [], check = (condition, message) => { if (!condition) failures.push(message) }
  check(/^[a-f0-9]{40}$/.test(captureSHA ?? '') && /^[1-9][0-9]*$/.test(run ?? '') && /^[1-9][0-9]*$/.test(attempt ?? ''), 'Missing expected workflow source/run/attempt')
  check(reports.length === PROBE_SLOTS.length, 'Need all six runner reports')
  const slots = reports.map(report => report.slot).sort((a, b) => a - b)
  check(JSON.stringify(slots) === JSON.stringify(PROBE_SLOTS), 'Missing, duplicate or unknown runner slot')
  check(new Set(reports.map(report => report.cpu?.bootId)).size === PROBE_SLOTS.length, 'Probe jobs must use six different kernel boot identities')
  for (const report of reports) {
    check(report.version === 2 && report.kind === 'raster-determinism-probe' && report.complete === true &&
      report.repeats === PROBE_REPEATS && report.threshold === 0 && report.maxDiffPixels === 0 &&
      report.containerDigest === visual.renderer.container.digest && report.run === run && report.attempt === attempt &&
      typeof report.runner === 'string' && report.runner !== 'unidentified' &&
      JSON.stringify(report.checkpoints) === JSON.stringify(checkpoints), `Invalid, failed or stale runner report: ${report.slot}`)
    const cpu = report.cpu
    check(typeof cpu?.model === 'string' && cpu.model !== 'unidentified' && cpu.model.length > 0 && cpu.model.length <= 256 &&
      Array.isArray(cpu.flags) && cpu.flags.length > 0 && cpu.flags.length <= 512 &&
      cpu.flags.every(flag => typeof flag === 'string' && /^[a-z0-9_]{1,64}$/.test(flag)) &&
      sha256(Buffer.from(cpu.flags.join(' '))) === cpu.flagsSHA256 && /^[a-f0-9-]{36}$/.test(cpu.bootId ?? ''), `Invalid runner CPU evidence: ${report.slot}`)
    check(report.settings?.length === PROBE_SETTINGS.length && new Set(report.settings?.map(setting => setting.setting)).size === PROBE_SETTINGS.length,
      `Missing or duplicate settings: ${report.slot}`)
  }
  const settings = PROBE_SETTINGS.map(setting => {
    const args = [...visual.renderer.browser.args, ...setting.args]
    const rows = checkpoints.map(id => {
      const jobs = reports.map(report => {
        const result = report.settings?.find(row => row.setting === setting.id)
        const row = result?.checkpoints?.find(row => row.id === id)
        check(result?.complete === true && result.captureSHA === captureSHA && result.exitCode === 0 && !result.error &&
          JSON.stringify(result.args) === JSON.stringify(args) && result.checkpoints?.length === checkpoints.length &&
          new Set(result.checkpoints?.map(row => row.id)).size === checkpoints.length &&
          row?.complete === true && row.repeats === PROBE_REPEATS && row.failures?.length === 0 &&
          row.digests?.length === PROBE_REPEATS && row.digests.every(digest => /^[a-f0-9]{64}$/.test(digest)), `Incomplete ${setting.id}/${id} on runner ${report.slot}`)
        return { slot: report.slot, cpuModel: report.cpu?.model, digests: row?.digests ?? [] }
      })
      const digests = jobs.flatMap(job => job.digests)
      return { id, jobs, allByteIdentical: digests.length === PROBE_SLOTS.length * PROBE_REPEATS && new Set(digests).size === 1 }
    })
    return { setting: setting.id, args, checkpoints: rows, allByteIdentical: rows.every(row => row.allByteIdentical) }
  })
  const complete = failures.length === 0
  for (const setting of settings) {
    setting.allByteIdentical = complete ? setting.allByteIdentical : null
    if (!complete) for (const row of setting.checkpoints) row.allByteIdentical = null
  }
  const cpuModels = [...new Set(reports.map(report => report.cpu?.model))]
  return { version: 1, kind: 'cross-machine-raster-probe', complete, failures, captureSHA, run, attempt, cpuModels,
    byteIdentityBasis: 'SHA-256 of each entire PNG file; zero tolerance, no masks',
    runners: reports.map(({ slot, runner, cpu }) => ({ slot, runner, cpu })), settings,
    hypothesis: !complete ? 'INCOMPLETE' : cpuModels.length < 2 ? 'Inconclusive: fewer than two CPU models were sampled; use a new run to seek CPU diversity.' :
      !settings[0].allByteIdentical && settings[1].allByteIdentical ?
        'Supports CPU-dependent rasterization: baseline varies, disabled Skia runtime opts plus determinism flags are stable. The bundle does not isolate which flag caused stability. Full 60-checkpoint cross-machine captures and Tick review are still required.' :
        'The CPU/Skia hypothesis is not confirmed by this run. If both settings vary, investigate the prepared fixed-machine fallback and prove all 60 checkpoints there.' }
}

export function writeProbeMatrix(directory, output, input, visual, expected) {
  need(resolve(directory).startsWith(resolve(root, 'web/test-results') + '/') &&
    resolve(output).startsWith(resolve(root, 'web/test-results') + '/'), 'Matrix evidence must stay under web/test-results')
  const checkpoints = probeCheckpoints(input, visual), reports = []
  for (const slot of PROBE_SLOTS) {
    try { reports.push(JSON.parse(boundedFile(join(directory, `runner-${slot}`, 'probe-report.json'), 1024 * 1024))) }
    catch (error) { if (error.code !== 'ENOENT') throw error }
  }
  const report = summarizeProbeMatrix(reports, checkpoints, visual, expected)
  mkdirSync(output, { recursive: false })
  writeFileSync(join(output, 'matrix-report.json'), JSON.stringify(report, null, 2) + '\n', { flag: 'wx' })
  const cell = value => String(value ?? '').replace(/[|\r\n]/g, ' ')
  const lines = ['# Cross-machine raster probe', '', `Complete: ${report.complete}. ${report.hypothesis}`, '',
    '| Slot | Runner | CPU model | AVX2 | AVX512 flags | Kernel boot identity |', '| --- | --- | --- | --- | --- | --- |',
    ...report.runners.map(row => `| ${row.slot} | ${cell(row.runner)} | ${cell(row.cpu?.model)} | ${row.cpu?.flags?.includes('avx2')} | ${cell(row.cpu?.flags?.filter(flag => flag.startsWith('avx512')).join(' '))} | ${cell(row.cpu?.bootId)} |`), '',
    '| Setting | ALL jobs byte-identical (every selected checkpoint) |', '| --- | --- |',
    ...report.settings.map(setting => `| ${setting.setting} | ${setting.allByteIdentical === null ? 'INCOMPLETE' : setting.allByteIdentical ? 'yes' : 'no'} |`), '',
    '| Setting | Checkpoint | ALL six jobs / all repeats byte-identical |', '| --- | --- | --- |',
    ...report.settings.flatMap(setting => setting.checkpoints.map(row => `| ${setting.setting} | ${row.id} | ${row.allByteIdentical === null ? 'INCOMPLETE' : row.allByteIdentical ? 'yes' : 'no'} |`)), '',
    ...report.failures.map(failure => `- ${failure}`), '', 'Diagnostic evidence only; the six selected checkpoints do not establish stability of all 60 pins. No reference or renderer was activated.']
  writeFileSync(join(output, 'matrix-report.md'), lines.join('\n') + '\n', { flag: 'wx' })
  return report
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [output, matrixInput, matrixOutput] = process.argv.slice(2)
    need(output, 'Usage: VISUAL_PROBE_CHECKPOINTS=<IDs> node scripts/test-prune/visual-probe.mjs <new-output-directory>')
    const visual = JSON.parse(boundedFile(new URL('./visual-manifest.json', import.meta.url)))
    const report = output === 'matrix' ? writeProbeMatrix(matrixInput, matrixOutput, process.env.VISUAL_PROBE_CHECKPOINTS, visual,
      { captureSHA: process.env.GITHUB_SHA, run: process.env.GITHUB_RUN_ID, attempt: process.env.GITHUB_RUN_ATTEMPT }) :
      await runProbe(process.env.VISUAL_PROBE_CHECKPOINTS, output, visual)
    console.log(readFileSync(join(output === 'matrix' ? matrixOutput : output, output === 'matrix' ? 'matrix-report.md' : 'probe-report.md'), 'utf8'))
    if (!report.complete) process.exitCode = 1
  } catch (error) { console.error(`Raster probe failed: ${error.message}`); process.exitCode = 1 }
}
