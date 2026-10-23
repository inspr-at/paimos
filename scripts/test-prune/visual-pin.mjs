// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { createRequire } from 'node:module'
import { existsSync, mkdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { collectCandidates, packCandidates, writeCandidateArchive } from './visual-candidates.mjs'
import { verifyReferenceArchive, referenceGates } from './visual-baselines.mjs'
import { validateVisual, visualCheckResult } from './check.mjs'

const need = (condition, message) => { if (!condition) throw new Error(message) }
export const sha256 = bytes => createHash('sha256').update(bytes).digest('hex')
const require = createRequire(new URL('../../web/package.json', import.meta.url))
// Use the exact comparator and PNG decoder bundled with the locked Playwright
// runner. This offline replay launches no browser and changes no references.
export const pngTools = () => ({ PNG: require('playwright-core/lib/utilsBundle').PNG,
  compare: require('playwright-core/lib/coreBundle').utils.getComparator('image/png') })
export function decodePNG(bytes) {
  need(Buffer.isBuffer(bytes) && bytes.length > 24 && bytes.length <= 4 * 1024 * 1024 &&
    bytes.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])) &&
    bytes.readUInt32BE(16) > 0 && bytes.readUInt32BE(16) <= 1440 && bytes.readUInt32BE(20) > 0 && bytes.readUInt32BE(20) <= 900,
  'PNG exceeds the bounded visual matrix')
  return pngTools().PNG.sync.read(bytes)
}
export function boundedFile(path, limit = 1024 * 1024) {
  const stat = statSync(path)
  need(stat.isFile() && stat.size > 0 && stat.size <= limit, `Invalid or oversized evidence: ${path}`)
  return readFileSync(path)
}

export function pinManifest(visual, candidate, detachedBytes, approval, attachments, { dryRun = false } = {}) {
  validateVisual(visual)
  need(visual.comparison.mode === 'report-only' && visual.approval.status === 'awaiting-set-3', 'Set 3 preparation requires the unpinned report-only manifest')
  need(candidate.status === 'pending-Tick-review' && candidate.members?.length === 72 && candidate.states?.length === 16,
    'Set 3 needs all 72 main-capture candidates from sixteen states')
  need(approval?.reviewer === 'Tick' && approval.status === 'approved' && Number.isFinite(Date.parse(approval.approvedAt)) &&
    typeof approval.evidence === 'string' && approval.evidence.trim().length > 0 && approval.captureSHA === candidate.captureSHA &&
    approval.archiveSHA256 === candidate.archive.sha256, 'A Tick approval receipt must identify these exact capture and archive bytes')
  need(Array.isArray(approval.checkpoints) && approval.checkpoints.length > 0 && new Set(approval.checkpoints).size === approval.checkpoints.length &&
    approval.checkpoints.every(id => candidate.members.some(member => member.id === id)), 'Approval must explicitly enumerate candidate checkpoint IDs')
  need(dryRun || (approval.dryRun !== true && approval.evidence !== 'DRY RUN ONLY; no product approval'), 'A dry-run approval cannot pin product references')
  const uuid = /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/
  need(dryRun || (uuid.test(attachments?.archive) && uuid.test(attachments?.manifest) && attachments.archive !== attachments.manifest),
    'Real, distinct AEON-1670 archive and manifest attachment IDs are required')
  const pinned = structuredClone(visual), approved = new Set(approval.checkpoints)
  pinned.referenceSet = 3
  pinned.renderer = structuredClone(candidate.renderer)
  pinned.approval = { reviewer: 'Tick', status: 'approved', approvedAt: approval.approvedAt, evidence: approval.evidence,
    states: [], checkpoints: candidate.members.filter(member => approved.has(member.id)).map(member => member.id) }
  if (dryRun) pinned.approval.dryRun = true
  for (const state of pinned.states) for (const checkpoint of state.checkpoints) {
    if (!approved.has(checkpoint.id)) continue
    const member = candidate.members.find(member => member.id === checkpoint.id)
    need(state.referenceStatus !== 'deferred', `Product deferral cannot be lifted by pin preparation: ${state.id}`)
    checkpoint.reference = { attachmentMember: member.attachmentMember, sha256: member.sha256 }
    delete checkpoint.referenceStatus; delete checkpoint.deferredUntil; delete checkpoint.deferredReason
  }
  const source = { kind: 'existing-ppm-ticket-attachment', ticket: 'AEON-1670', instance: 'PPM / INSPR',
    attachmentId: dryRun ? 'DRY-RUN-NO-ATTACHMENT' : attachments.archive,
    name: candidate.archive.name, bytes: candidate.archive.bytes, sha256: candidate.archive.sha256, format: 'tar',
    captureSHA: candidate.captureSHA, captureRun: '39949629594',
    manifest: { attachmentId: dryRun ? 'DRY-RUN-NO-MANIFEST' : attachments.manifest,
      name: `${candidate.archive.name}.manifest.json`, bytes: detachedBytes.length, sha256: sha256(detachedBytes) },
    baselineBundle: { attachmentMember: 'baseline.json', sha256: candidate.baselineBundle.sha256 },
    archiveManifest: { attachmentMember: 'manifest.json', sha256: candidate.manifestSHA256 },
    unapprovedMembers: candidate.members.filter(member => !approved.has(member.id)),
    unapprovedMemberPolicy: 'Transport evidence only; only the explicit Tick-approved checkpoint list supplies comparison authority.',
  }
  const releaseURL = 'https://github.com/inspr-at/paimos/releases'
  source.mirror = { kind: 'github-prerelease', repository: 'inspr-at/paimos', tag: 'test-references-3',
    releaseURL: `${releaseURL}/tag/test-references-3`,
    archive: { name: source.name, url: `${releaseURL}/download/test-references-3/${source.name}` },
    manifest: { name: source.manifest.name, url: `${releaseURL}/download/test-references-3/${source.manifest.name}` },
    authority: 'Read-only, byte-identical transport of the explicitly approved set-3 AEON-1670 attachments; sets 1 and 2 remain history.' }
  pinned.baselineAuthority = { ...pinned.baselineAuthority, status: 'approved', referenceSet: 3, source,
    retention: { ...pinned.baselineAuthority.retention,
      availability: 'Permanent AEON-1670 attachments and immutable test-references-3 prerelease assets, verified before any comparison.',
      verifiedAt: approval.approvedAt, verificationScope: dryRun ? 'DRY RUN ONLY: local transport and comparator replay' : approval.evidence,
      ciVerifiedAt: null } }
  // This is the only policy switch. History is deliberately never promoted.
  pinned.comparison.mode = 'blocking'
  validateVisual(pinned, { cuts: true, checkpointIds: pinned.approval.checkpoints })
  need(JSON.stringify(pinned.history) === JSON.stringify(visual.history), 'Historical reference sets changed')
  referenceGates(pinned)
  return pinned
}

export async function replayCandidates(records, pinned, references, output) {
  const { PNG, compare } = pngTools()
  const results = []
  for (const record of records) {
    const id = record.quality.id
    if (!pinned.approval.checkpoints.includes(id)) continue
    const reference = references.get(`${id}.png`)
    need(reference, `Missing replay reference: ${id}`)
    decodePNG(record.png); decodePNG(reference)
    const result = await visualCheckResult(pinned, async () => {
      const diff = compare(record.png, reference, pinned.comparison)
      need(!diff, `${id}: ${diff?.errorMessage}`)
      return 'matched'
    })
    results.push({ id, status: result.status, comparison: result.value })
  }
  need(results.length === pinned.approval.checkpoints.length, 'Incomplete blocking replay')
  const record = records.find(record => pinned.approval.checkpoints.includes(record.quality.id))
  const changed = decodePNG(record.png), original = decodePNG(record.png)
  // Change one opaque corner pixel, away from text antialiasing.
  for (let channel = 0; channel < 3; channel++) changed.data[channel] = original.data[channel] < 128 ? 255 : 0
  changed.data[3] = 255
  const planted = PNG.sync.write(changed)
  let changedPixels = 0
  for (let offset = 0; offset < changed.data.length; offset += 4)
    if (!changed.data.subarray(offset, offset + 4).equals(original.data.subarray(offset, offset + 4))) changedPixels++
  need(changedPixels === 1, 'Negative control must plant exactly one pixel')
  const diff = compare(planted, record.png, pinned.comparison)
  need(diff && /^1 pixels /.test(diff.errorMessage), 'Playwright must detect exactly the planted pixel')
  const fail = async () => { const found = compare(planted, record.png, pinned.comparison); need(!found, found?.errorMessage) }
  const reporting = { ...pinned, comparison: { ...pinned.comparison, mode: 'report-only' } }
  const reported = await visualCheckResult(reporting, fail)
  need(reported.status === 'failed', 'Report-only negative control lost its failure')
  let rejected = false
  try { await visualCheckResult(pinned, fail) } catch (error) { rejected = error.message === diff.errorMessage }
  need(rejected, 'Blocking mode failed to reject the planted pixel')
  const negative = { id: record.quality.id, changedPixels, x: 0, y: 0, reportOnly: reported.status,
    blocking: 'rejected', error: diff.errorMessage }
  if (output) {
    writeFileSync(join(output, 'negative-control.png'), planted, { flag: 'wx' })
    writeFileSync(join(output, 'negative-control-diff.png'), diff.diff, { flag: 'wx' })
  }
  return { kind: 'offline replay of main capture through the locked Playwright screenshot comparator; no fresh browser capture',
    captureSHA: record.quality.captureSHA, mode: pinned.comparison.mode, threshold: 0, maxDiffPixels: 0,
    passed: results.length, results, negative }
}

export function publicationCommands(directory, candidate) {
  const q = value => `'${String(value).replaceAll("'", "'\\''")}'`
  const files = [candidate.archive.name, `${candidate.archive.name}.manifest.json`, `${candidate.archive.name}.sha256`].map(name => join(directory, name))
  return [
    '# LEAD only, after Tick approves these exact bytes. A pre-existing tag/release must stop this flow.',
    `gh release create test-references-3 --repo inspr-at/paimos --target ${candidate.captureSHA} --prerelease --title 'Visual reference set 3' --notes 'Tick-approved reference set 3; AEON-1670 approval record; immutable assets.' ${files.map(q).join(' ')}`,
    ...files.slice(0, 2).map(path => `/Users/markus/.aeon/lead/aeon/pm --instance ppm --agent-name aeon-lead-claude --json attach AEON-1670 ${q(path)}`),
    '# Record returned attachment IDs, then use an actual Tick receipt (captureSHA, archiveSHA256, approvedAt, evidence, checkpoints).',
    `node scripts/test-prune/visual-pin.mjs activate ${q(directory)} tick-set3-approval.json ARCHIVE_ATTACHMENT_UUID MANIFEST_ATTACHMENT_UUID`,
    '# Activation is local only. LEAD owns the commit, publication verification, review, merge and CI.',
  ].join('\n') + '\n'
}

async function main() {
  const [mode, directory, argument, archiveID, manifestID] = process.argv.slice(2)
  const visualPath = fileURLToPath(new URL('./visual-manifest.json', import.meta.url))
  const visual = JSON.parse(boundedFile(visualPath))
  need(['prepare', 'activate'].includes(mode) && directory && argument, 'Usage: visual-pin.mjs prepare <capture-dir> <new-plan-dir> | activate <plan-dir> <Tick-receipt.json> <archive-attachment-UUID> <manifest-attachment-UUID>')
  if (mode === 'prepare') {
    need(resolve(directory).startsWith(resolve('web/test-results') + '/') && resolve(argument).startsWith(resolve('web/test-results') + '/'),
      'Dry-run artifacts must stay under web/test-results')
    const records = collectCandidates(directory)
    const { review } = packCandidates(records, visual)
    need(review.captureSHA === 'ecb819ba898f3fcb944f395388fb2c24a8b7ce83', 'This brief requires the dispatched main capture')
    need(records.every(record => record.quality.quality?.status === 'passed'), 'Every candidate needs a recorded quality pass')
    mkdirSync(argument, { recursive: false })
    const archiveDirectory = join(argument, 'publication')
    const candidate = writeCandidateArchive(records, visual, archiveDirectory)
    const detached = boundedFile(join(archiveDirectory, `${candidate.archive.name}.manifest.json`))
    const approval = { reviewer: 'Tick', status: 'approved', dryRun: true, approvedAt: new Date().toISOString(),
      evidence: 'DRY RUN ONLY; no product approval', captureSHA: candidate.captureSHA, archiveSHA256: candidate.archive.sha256,
      checkpoints: candidate.members.map(member => member.id) }
    const pinned = pinManifest(visual, candidate, detached, approval, {}, { dryRun: true })
    const verified = verifyReferenceArchive(boundedFile(join(archiveDirectory, candidate.archive.name), 64 * 1024 * 1024), detached, pinned)
    const proof = await replayCandidates(records, pinned, verified.references, argument)
    writeFileSync(join(argument, 'visual-manifest.dry-run.json'), JSON.stringify(pinned, null, 2) + '\n', { flag: 'wx' })
    writeFileSync(join(argument, 'proof.json'), JSON.stringify(proof, null, 2) + '\n', { flag: 'wx' })
    writeFileSync(join(argument, 'tick-approval.template.json'), JSON.stringify({ ...approval, status: 'pending-Tick-review', dryRun: false,
      approvedAt: null, evidence: null, checkpoints: [] }, null, 2) + '\n', { flag: 'wx' })
    writeFileSync(join(argument, 'lead-commands.sh'), publicationCommands(archiveDirectory, candidate).replace(
      `activate '${archiveDirectory}'`, `activate '${argument}'`), { flag: 'wx' })
    console.log(`DRY RUN ONLY: ${proof.passed} blocking matches; one-pixel negative rejected. No live manifest, approval, release or attachment changed.\n${publicationCommands(archiveDirectory, candidate).replace(`activate '${archiveDirectory}'`, `activate '${argument}'`)}`)
  } else {
    const archiveDirectory = join(directory, 'publication')
    const names = (await import('node:fs')).readdirSync(archiveDirectory).filter(name => name.endsWith('.tar.manifest.json'))
    need(names.length === 1, 'Pin plan must contain exactly one detached manifest')
    const detached = boundedFile(join(archiveDirectory, names[0]))
    const candidate = JSON.parse(detached), approval = JSON.parse(boundedFile(argument))
    const pinned = pinManifest(visual, candidate, detached, approval, { archive: archiveID, manifest: manifestID })
    verifyReferenceArchive(boundedFile(join(archiveDirectory, candidate.archive.name), 64 * 1024 * 1024), detached, pinned)
    need(!existsSync(join(directory, 'activation.json')), 'This pin plan was already activated')
    writeFileSync(visualPath, JSON.stringify(pinned, null, 2) + '\n')
    writeFileSync(join(directory, 'activation.json'), JSON.stringify({ attachmentIDs: [archiveID, manifestID],
      manifestSHA256: sha256(readFileSync(visualPath)), approval }, null, 2) + '\n', { flag: 'wx' })
    console.log('Set 3 pinned locally with blocking comparisons. No publication, push or deployment performed.')
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main().catch(error => {
  console.error(`Set 3 preparation failed: ${error.message}`); process.exitCode = 1
})
