// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { collectCandidates, packCandidates } from './visual-candidates.mjs'
import { verifyReferenceArchive } from './visual-baselines.mjs'
import { boundedFile, pinManifest, sha256 } from './visual-pin.mjs'
import { pixelChanges } from './visual-review.mjs'
import { assertRenderer } from './visual-renderer.mjs'

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
    checkpoints: candidate.members.map(member => member.id) }
  // Verification of a proof input is not pinning. In particular it does not
  // demand the later cross-capture proof that this operation is constructing.
  const pinned = pinManifest(policy, candidate, detached, receipt, {}, { dryRun: true, proofInput: true })
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
  const stable = repeat.every(row => row.byteIdentical && row.pixels === 0)
  const identical = runner.every(row => row.pixels === 0)
  return { version: 1, status: stable && identical ? 'identical' : 'STOP-before-pinning',
    captureSHA: sha, containerDigest: visual.renderer.container.digest,
    archiveSHA256: first.archiveSHA256, repeatArchiveSHA256: second.archiveSHA256,
    reviewedRunnerArchiveSHA256: reviewed.archiveSHA256,
    repeat: { byteIdentical: repeat.filter(row => row.byteIdentical).length, pixelIdentical: repeat.filter(row => row.pixels === 0).length, checkpoints: repeat },
    runner: { byteIdentical: runner.filter(row => row.byteIdentical).length, pixelIdentical: runner.filter(row => row.pixels === 0).length, checkpoints: runner },
    approvalCoverage: stable && identical ? 'Tick review of the runner capture covers all 72 pixel-identical container checkpoints.' :
      'No container pin is authorized: resolve repeat instability first; LEAD decides re-review for any runner differences.' }
}

async function main() {
  const [firstDirectory, secondDirectory, reviewedDirectory, output] = process.argv.slice(2)
  need(firstDirectory && secondDirectory && reviewedDirectory && output, 'Usage: visual-identity.mjs <container-run-1> <container-run-2> <reviewed-runner-artifact> <new-proof-dir>')
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
    '| Comparison | Checkpoint | Different pixels | Difference % | Regions (x,y,width,height) |', '| --- | --- | ---: | ---: | --- |']
  for (const [label, result] of [['repeat', proof.repeat], ['runner', proof.runner]])
    for (const row of result.checkpoints.filter(row => row.pixels || !row.byteIdentical))
      lines.push(`| ${label} | ${row.id} | ${row.pixels} | ${row.percent.toFixed(8)} | ${row.regions.map(box => `${box.x},${box.y},${box.width},${box.height}`).join('; ') || 'PNG encoding bytes only'} |`)
  writeFileSync(join(output, 'identity-proof.md'), lines.join('\n') + '\n', { flag: 'wx' })
  console.log(lines.join('\n'))
  need(proof.status === 'identical', 'STOP before pinning: container captures are unstable or differ from Tick-reviewed runner pixels; see identity-proof.json/.md')
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main().catch(error => {
  console.error(error.message); process.exitCode = 1
})
