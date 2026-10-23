// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { collectCandidates, packCandidates } from './visual-candidates.mjs'
import { verifyReferenceArchive } from './visual-baselines.mjs'
import { boundedFile, decodePNG, pinCheckpoints, pinManifest, pngTools, sha256 } from './visual-pin.mjs'
import { pixelChanges } from './visual-review.mjs'
import { assertRenderer, preparedRenderer } from './visual-renderer.mjs'

const need = (condition, message) => { if (!condition) throw new Error(message) }

export function compareCaptureRecords(first, second) {
  need(first.length === 72 && second.length === 72, 'Identity proof needs exactly 72 checkpoints in each capture')
  const byId = new Map(second.map(record => [record.quality.id, record]))
  need(byId.size === 72 && new Set(first.map(record => record.quality.id)).size === 72, 'Duplicate identity checkpoint')
  return first.map(record => {
    const other = byId.get(record.quality.id)
    need(other, `Missing identity checkpoint: ${record.quality.id}`)
    const changes = pixelChanges(record.png, other.png)
    return { id: record.quality.id, byteIdentical: record.png.equals(other.png), ...changes }
  })
}

// Verify the original archive, detached manifest, every PNG and quality record
// before using a capture as proof input. This temporary map grants no authority.
export function verifyCapture(directory, visual, { archiveSHA256, manifestSHA256, captureSHA } = {}) {
  const records = collectCandidates(join(directory, 'capture-run'))
  const policy = archiveSHA256 ? { ...visual, renderer: records[0]?.quality.renderer } : visual
  const { review } = packCandidates(records, policy)
  need(records.every(record => record.quality.quality?.status === 'passed'), 'Identity capture has unresolved quality findings')
  const path = join(directory, 'linux-candidate', `AEON-1674-linux-${review.captureSHA.slice(0, 10)}.tar`)
  const archive = boundedFile(path, 64 * 1024 * 1024), detached = boundedFile(`${path}.manifest.json`)
  const candidate = JSON.parse(detached)
  need(!archiveSHA256 || sha256(archive) === archiveSHA256, 'Reviewed runner archive SHA-256 mismatch')
  need(!manifestSHA256 || sha256(detached) === manifestSHA256, 'Reviewed runner detached manifest SHA-256 mismatch')
  need(!captureSHA || review.captureSHA === captureSHA, 'Reviewed runner capture SHA mismatch')
  need(candidate.baselineBundle?.sha256 === review.sha256 && JSON.stringify(candidate.members) === JSON.stringify(review.members),
    'Identity archive does not contain these quality-checked captures')
  const receipt = { reviewer: 'Tick', status: 'approved', dryRun: true, approvedAt: '2026-10-23T00:00:00Z',
    evidence: 'DRY RUN ONLY; no product approval', captureSHA: candidate.captureSHA, archiveSHA256: candidate.archive.sha256,
    checkpoints: pinCheckpoints(visual) }
  // Verification of a proof input is not pinning. In particular it does not
  // demand the later cross-capture proof that this operation is constructing.
  // Archive verification remains usable after set 3 is active. The temporary
  // awaiting policy is confined to this dry-run transport check; it is never
  // returned, persisted or used by a browser or publication command.
  const transportPolicy = { ...policy, comparison: { ...policy.comparison, mode: 'report-only' },
    approval: { reviewer: 'Tick', status: 'awaiting-set-3', states: [], checkpoints: [] } }
  const pinned = pinManifest(transportPolicy, candidate, detached, receipt, {}, { dryRun: true, proofInput: true })
  verifyReferenceArchive(archive, detached, pinned)
  return { records, candidate, archiveSHA256: sha256(archive), manifestSHA256: sha256(detached) }
}

export function proveCaptureIdentity(first, second, reviewed, visual) {
  assertRenderer(first.candidate.renderer, visual.renderer)
  assertRenderer(second.candidate.renderer, visual.renderer)
  const sha = first.candidate.captureSHA
  need(sha === second.candidate.captureSHA, 'Repeat captures must render the SAME commit')
  need(first.candidate.renderer.version === 2 && second.candidate.renderer.version === 2 &&
    first.candidate.renderer.container.digest === visual.renderer.container.digest &&
    second.candidate.renderer.container.digest === visual.renderer.container.digest, 'Repeat captures must use the pinned container')
  const repeat = compareCaptureRecords(first.records, second.records)
  const runner = compareCaptureRecords(reviewed.records, first.records)
  const selected = pinCheckpoints(visual), selectedSet = new Set(selected)
  const selectedRepeat = repeat.filter(row => selectedSet.has(row.id)), selectedRunner = runner.filter(row => selectedSet.has(row.id))
  need(selectedRepeat.length === selected.length && selectedRunner.length === selected.length, 'Incomplete non-deferred identity coverage')
  const stable = selectedRepeat.every(row => row.byteIdentical && row.pixels === 0)
  const reviewRequired = selectedRunner.filter(row => row.pixels !== 0).map(row => row.id)
  const reviewAllowed = reviewRequired.every(id => (visual.capture.identityProof.relookCheckpoints ?? []).includes(id))
  const ready = stable && reviewAllowed
  return { version: 2, status: ready ? reviewRequired.length ? 'stable-awaiting-Tick-review' : 'identical' : 'STOP-before-pinning',
    captureSHA: sha, containerDigest: visual.renderer.container.digest,
    archiveSHA256: first.archiveSHA256, repeatArchiveSHA256: second.archiveSHA256,
    reviewedRunnerArchiveSHA256: reviewed.archiveSHA256,
    pinCheckpoints: selected, reviewRequired,
    deferred: visual.states.flatMap(state => state.checkpoints.filter(checkpoint =>
      checkpoint.referenceStatus === 'deferred' || state.referenceStatus === 'deferred').map(checkpoint => ({
      id: checkpoint.id, until: checkpoint.deferredUntil ?? state.deferredUntil, reason: checkpoint.deferredReason ?? state.deferredReason }))),
    pinnable: { total: selected.length, byteIdentical: selectedRepeat.filter(row => row.byteIdentical).length,
      pixelIdentical: selectedRepeat.filter(row => row.pixels === 0).length, runnerPixelIdentical: selectedRunner.filter(row => row.pixels === 0).length },
    repeat: { byteIdentical: repeat.filter(row => row.byteIdentical).length, pixelIdentical: repeat.filter(row => row.pixels === 0).length, checkpoints: repeat },
    runner: { byteIdentical: runner.filter(row => row.byteIdentical).length, pixelIdentical: runner.filter(row => row.pixels === 0).length, checkpoints: runner },
    approvalCoverage: ready ? `${selected.length}/${selected.length} non-deferred repeat PNGs are byte-identical. ` +
      (reviewRequired.length ? `Tick re-look required for: ${reviewRequired.join(', ')}; activation requires its explicit receipt.` :
        'The reviewed runner pixels cover this subset; Tick must still identify the exact container archive in the receipt.') :
      'No container pin is authorized: resolve non-deferred repeat instability or unexpected runner differences first.' }
}

export function provePreparedIdentity(first, second, visual, setting) {
  const renderer = preparedRenderer(visual, setting)
  need(renderer.rasterSetting, 'Candidate identity requires an explicitly prepared renderer')
  assertRenderer(first.candidate.renderer, renderer)
  assertRenderer(second.candidate.renderer, renderer)
  need(first.candidate.captureSHA === second.candidate.captureSHA && /^[a-f0-9]{40}$/.test(first.candidate.captureSHA), 'Candidate captures must render the SAME commit')
  const cpus = [first, second].map(capture => {
    const cpu = capture.records[0]?.quality.renderer?.runner?.cpu
    need(cpu?.model && cpu.model !== 'unidentified' && /^[a-f0-9-]{36}$/.test(cpu.bootId ?? '') &&
      Array.isArray(cpu.flags) && cpu.flags.length > 0 && cpu.flags.length <= 512 &&
      cpu.flags.every(flag => /^[a-z0-9_]{1,64}$/.test(flag)) && sha256(Buffer.from(cpu.flags.join(' '))) === cpu.flagsSHA256 && capture.records.every(record =>
        JSON.stringify(record.quality.renderer?.runner?.cpu) === JSON.stringify(cpu)), 'Missing/inconsistent candidate CPU evidence')
    return cpu
  })
  if (!renderer.machine) need(cpus[0].bootId !== cpus[1].bootId, 'Hosted candidate proof requires two different machines, not two repeats on one runner')
  else need(cpus.every(cpu => cpu.model === renderer.machine.cpuModel && cpu.flagsSHA256 === renderer.machine.cpuFlagsSHA256), 'Candidate fixed-machine CPU differs from OPS pin')
  const selected = new Set(pinCheckpoints(visual)), rows = compareCaptureRecords(first.records, second.records).filter(row => selected.has(row.id))
  need(rows.length === 60 && selected.size === 60, 'Prepared switch requires exactly the 60 set-3 checkpoint identities')
  return { version: 1, kind: 'prepared-renderer-identity', setting, renderer, captureSHA: first.candidate.captureSHA, cpus,
    archiveSHA256: first.archiveSHA256, repeatArchiveSHA256: second.archiveSHA256,
    status: rows.every(row => row.byteIdentical) ? 'stable-awaiting-Tick-review' : 'STOP-before-pinning',
    pinCheckpoints: [...selected], checkpoints: rows,
    authority: 'Prepared evidence only. Set-3 approval does not cover new renderer bytes. Tick must inspect every changed checkpoint before test-references-4 publication and activation.' }
}

// All unequal RGBA pixels are visible in the diff. The edge statistic is an
// explicit heuristic for Tick's review, never an anti-alias mask or approval.
export function rendererPixelDiff(before, after) {
  const a = decodePNG(before), b = decodePNG(after), { PNG } = pngTools()
  need(a.width === b.width && a.height === b.height, 'Renderer diff dimensions differ')
  const diff = new PNG({ width: a.width, height: a.height })
  let pixels = 0, nonEdgePixels = 0
  const edge = (image, x, y) => {
    const offset = (y * image.width + x) * 4
    for (let dy = -1; dy <= 1; dy++) for (let dx = -1; dx <= 1; dx++) {
      if (x + dx < 0 || y + dy < 0 || x + dx >= image.width || y + dy >= image.height) continue
      const other = ((y + dy) * image.width + x + dx) * 4
      if ([0, 1, 2, 3].some(channel => Math.abs(image.data[offset + channel] - image.data[other + channel]) >= 16)) return true
    }
    return false
  }
  for (let y = 0; y < a.height; y++) for (let x = 0; x < a.width; x++) {
    const offset = (y * a.width + x) * 4, changed = a.data.readUInt32LE(offset) !== b.data.readUInt32LE(offset)
    diff.data[offset] = changed ? 220 : 245; diff.data[offset + 1] = changed ? 0 : 245
    diff.data[offset + 2] = changed ? 150 : 245; diff.data[offset + 3] = 255
    if (changed) { pixels++; if (!edge(a, x, y) && !edge(b, x, y)) nonEdgePixels++ }
  }
  return { png: PNG.sync.write(diff), pixels, nonEdgePixels,
    assessment: !pixels ? 'identical' : nonEdgePixels ? 'Includes non-edge pixels; investigate more than anti-aliasing.' :
      'Edge-localized; compatible with anti-aliasing differences, attribution unproven. Tick must inspect before/after/diff.' }
}

export function writePreparedReview(firstDirectory, secondDirectory, set3Directory, output, visual, setting) {
  need(resolve(firstDirectory) !== resolve(secondDirectory), 'Candidate proof requires separate capture directories')
  need(resolve(output).startsWith(resolve('web/test-results') + '/'), 'Candidate review must stay under web/test-results')
  const candidateVisual = { ...visual, renderer: preparedRenderer(visual, setting) }
  const first = verifyCapture(firstDirectory, candidateVisual), second = verifyCapture(secondDirectory, candidateVisual)
  const proof = provePreparedIdentity(first, second, visual, setting), source = visual.baselineAuthority.source
  const { references } = verifyReferenceArchive(boundedFile(join(set3Directory, source.name), 64 * 1024 * 1024),
    boundedFile(join(set3Directory, source.manifest.name)), visual)
  mkdirSync(output, { recursive: false })
  mkdirSync(join(output, 'diffs'))
  const selected = new Set(proof.pinCheckpoints), changes = []
  for (const record of first.records.filter(record => selected.has(record.quality.id))) {
    const id = record.quality.id, before = references.get(`${id}.png`)
    need(before, `Set 3 is missing ${id}`)
    const change = pixelChanges(before, record.png), diff = rendererPixelDiff(before, record.png)
    changes.push({ id, ...change, nonEdgePixels: diff.nonEdgePixels, assessment: diff.assessment,
      set3SHA256: sha256(before), candidateSHA256: sha256(record.png) })
    if (!change.pixels) continue
    for (const [suffix, bytes] of [['set3', before], ['candidate', record.png], ['diff', diff.png]])
      writeFileSync(join(output, 'diffs', `${id}-${suffix}.png`), bytes, { flag: 'wx' })
  }
  proof.changesVsSet3 = changes
  writeFileSync(join(output, 'candidate-identity.json'), JSON.stringify(proof, null, 2) + '\n', { flag: 'wx' })
  const lines = ['# Prepared renderer — full 60-checkpoint proof and Tick handoff', '',
    `${proof.status}; setting: ${setting}; source: ${proof.captureSHA}.`, '', proof.authority, '',
    ...proof.cpus.map((cpu, index) => `Runner ${index + 1}: ${cpu.model}; flags SHA-256 ${cpu.flagsSHA256}; kernel boot ${cpu.bootId}.`), '',
    `Byte-identical across machines: ${proof.checkpoints.filter(row => row.byteIdentical).length}/60.`, '',
    '| Checkpoint | Changed RGBA pixels vs set 3 | Non-edge pixels (heuristic) | Assessment | Before / after / exact diff |',
    '| --- | ---: | ---: | --- | --- |',
    ...changes.map(row => `| ${row.id} | ${row.pixels} | ${row.nonEdgePixels} | ${row.assessment} | ${row.pixels ?
      ['set3', 'candidate', 'diff'].map(suffix => `[${suffix}](diffs/${row.id}-${suffix}.png)`).join(' / ') : 'identical'} |`), '',
    'The 3×3 edge statistic checks a ≥16 channel gradient in either image. It cannot prove that changes are anti-aliasing-only. Flat-area changes are explicitly separated; all changes require Tick’s assessment. Diff images show every unequal RGBA pixel; no pixel tolerance or masks affect identity or comparison.', '',
    'STOP if the full 60 are unstable. Otherwise record Tick’s actual receipt for the exact archive and changed checkpoint list, publish immutable test-references-4, attach both original assets to AEON-1670, then use a separately reviewed activation change. Never reuse the set-3 publication/activation helper for this candidate.']
  writeFileSync(join(output, 'candidate-review.md'), lines.join('\n') + '\n', { flag: 'wx' })
  return proof
}

async function main() {
  if (process.argv[2] === 'candidate') {
    const [, setting, first, second, set3, output] = process.argv.slice(2)
    need(setting && first && second && set3 && output, 'Usage: visual-identity.mjs candidate SETTING FIRST_CAPTURE SECOND_CAPTURE VERIFIED_SET3_DIR NEW_REVIEW_DIR')
    const visual = JSON.parse(readFileSync(new URL('./visual-manifest.json', import.meta.url), 'utf8'))
    const proof = writePreparedReview(first, second, set3, output, visual, setting)
    console.log(`Prepared renderer: ${proof.status}; see ${output}/candidate-review.md`)
    need(proof.status === 'stable-awaiting-Tick-review', 'STOP before pinning: prepared full captures are not byte-identical')
    return
  }
  const [firstDirectory, secondDirectory, reviewedDirectory, output] = process.argv.slice(2)
  need(firstDirectory && secondDirectory && reviewedDirectory && output, 'Usage: visual-identity.mjs <container-run-1> <container-run-2> <reviewed-runner-artifact> <new-proof-dir>')
  need(resolve(firstDirectory) !== resolve(secondDirectory), 'Repeat identity proof needs two separate capture directories')
  const visual = JSON.parse(readFileSync(new URL('./visual-manifest.json', import.meta.url), 'utf8'))
  const expected = visual.capture.identityProof
  const first = verifyCapture(firstDirectory, visual), second = verifyCapture(secondDirectory, visual)
  const reviewed = verifyCapture(reviewedDirectory, visual, { archiveSHA256: expected.reviewedRunnerArchiveSHA256,
    manifestSHA256: expected.reviewedRunnerManifestSHA256, captureSHA: expected.reviewedRunnerCaptureSHA })
  const proof = proveCaptureIdentity(first, second, reviewed, visual)
  mkdirSync(output, { recursive: false })
  writeFileSync(join(output, 'identity-proof.json'), JSON.stringify(proof, null, 2) + '\n', { flag: 'wx' })
  const lines = ['# Container identity proof', '', proof.approvalCoverage, '',
    `Same commit twice: **${proof.repeat.byteIdentical}/72 byte-identical**, **${proof.repeat.pixelIdentical}/72 pixel-identical**.`,
    `Reviewed runner → container: **${proof.runner.pixelIdentical}/72 pixel-identical**.`, '',
    `Pin selection: **${proof.pinnable.byteIdentical}/${proof.pinnable.total} byte-identical**; **${proof.pinnable.runnerPixelIdentical}/${proof.pinnable.total} reviewed runner matches**.`, '',
    '| Comparison | Checkpoint | Different pixels | Difference % | Regions (x,y,width,height) |', '| --- | --- | ---: | ---: | --- |']
  for (const [label, result] of [['repeat', proof.repeat], ['runner', proof.runner]])
    for (const row of result.checkpoints.filter(row => row.pixels || !row.byteIdentical))
      lines.push(`| ${label} | ${row.id} | ${row.pixels} | ${row.percent.toFixed(8)} | ${row.regions.map(box => `${box.x},${box.y},${box.width},${box.height}`).join('; ') || 'PNG encoding bytes only'} |`)
  lines.push('', '| Deferred checkpoint | Awaiting | Reason |', '| --- | --- | --- |',
    ...proof.deferred.map(row => `| ${row.id} | ${row.until.join(', ')} | ${row.reason} |`))
  writeFileSync(join(output, 'identity-proof.md'), lines.join('\n') + '\n', { flag: 'wx' })
  console.log(lines.join('\n'))
  need(proof.status !== 'STOP-before-pinning', 'STOP before pinning: non-deferred container captures are unstable or have unexpected runner differences; see identity-proof.json/.md')
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main().catch(error => {
  console.error(error.message); process.exitCode = 1
})
