// SPDX-License-Identifier: AGPL-3.0-only
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const need = (condition, message) => { if (!condition) throw new Error(message) }
export function checkContainerPin(workflow, visual, previous, nightly, fixedCapture) {
  const container = visual.renderer.container
  need(visual.renderer.version === 2 && /^sha256:[a-f0-9]{64}$/.test(container?.digest ?? ''), 'Visual renderer must pin a container digest')
  for (const name of ['visual-reference-capture', 'visual-comparison']) {
    const job = workflow.split(`\n  ${name}:`)[1]?.split(/\n  [a-z][a-z0-9-]*:/)[0]
    need(job?.includes(`image: ${container.image}@${container.digest}`) &&
      job.includes(`AEON_VISUAL_CONTAINER_DIGEST: ${container.digest}`), `Visual container pin differs from manifest: ${name}`)
    need(!/playwright install|bounded-apt/.test(job), `Visual container must use its bundled browser and libraries: ${name}`)
  }
  if (nightly) {
    const job = nightly.split('\n  visual-comparison:')[1]?.split(/\n  [a-z][a-z0-9-]*:/)[0]
    need(job?.includes(`image: ${container.image}@${container.digest}`) &&
      job.includes(`AEON_VISUAL_CONTAINER_DIGEST: ${container.digest}`), 'Nightly visual container pin differs from manifest')
  }
  if (fixedCapture !== undefined) {
    const job = fixedCapture.split('\n  visual-reference-capture:')[1]?.split(/\n  [a-z][a-z0-9-]*:/)[0]
    need(job?.includes(`image: ${container.image}@${container.digest}`) &&
      job.includes(`AEON_VISUAL_CONTAINER_DIGEST: ${container.digest}`), 'Fixed capture container pin differs from manifest')
    need(!/playwright install|bounded-apt/.test(job), 'Fixed capture must use its bundled browser and libraries')
  }
  const before = previous?.renderer?.container?.digest
  const rasterChanged = previous?.renderer && ['rasterSetting', 'machine', 'browser', 'deviceScaleFactor'].some(key =>
    JSON.stringify(previous.renderer[key]) !== JSON.stringify(visual.renderer[key]))
  if (before && (before !== container.digest || rasterChanged)) {
    need(visual.baselineAuthority.source.sha256 !== previous.baselineAuthority.source.sha256 &&
      visual.baselineAuthority.source.captureSHA !== previous.baselineAuthority.source.captureSHA &&
      visual.approval.status === 'approved' && visual.baselineAuthority.status === 'approved' && visual.comparison.mode === 'blocking',
    `${before !== container.digest ? 'Container digest' : 'Raster setting or fixed machine'} changed without regenerated, approved baselines and capture evidence in the manifest; automated renderer bumps are forbidden`)
  }
  return true
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const path = 'scripts/test-prune/visual-manifest.json'
    const visual = JSON.parse(readFileSync(path, 'utf8'))
    const base = process.env.AEON_VISUAL_BASE_SHA || execFileSync('git', ['merge-base', 'HEAD', 'origin/main'], { encoding: 'utf8', timeout: 10_000 }).trim()
    need(/^[a-f0-9]{40}$/.test(base) && !/^0+$/.test(base), 'Missing visual container update base SHA')
    const previous = JSON.parse(execFileSync('git', ['show', `${base}:${path}`], { encoding: 'utf8', timeout: 10_000, maxBuffer: 1024 * 1024 }))
    checkContainerPin(readFileSync('.github/workflows/ci.yml', 'utf8'), visual, previous,
      readFileSync('.github/workflows/nightly-full.yml', 'utf8'), readFileSync('.github/workflows/visual-capture.yml', 'utf8'))
    console.log(`Visual container pin matches manifest; digest changes require regenerated baselines (base ${base}).`)
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
