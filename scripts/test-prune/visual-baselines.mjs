// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { validateVisual } from './check.mjs'
import { assertRenderer } from './visual-renderer.mjs'

const MAX_BUNDLE = 48 * 1024 * 1024
const MAX_ARCHIVE = 64 * 1024 * 1024
const MAX_MANIFEST = 1024 * 1024
const MAX_PNG = 4 * 1024 * 1024
const sha256 = bytes => createHash('sha256').update(bytes).digest('hex')
const need = (condition, message) => { if (!condition) throw new Error(message) }

// A reviewed attachment can pin one area while other areas await their own
// product/design work. Every member of each pinned state is still mandatory.
const pinnedStates = visual => visual.states.filter(state => state.checkpoints.some(checkpoint =>
  checkpoint.reference?.attachmentMember || checkpoint.reference?.sha256)).map(state => state.id)

// AEON-1670 remains the approval record. The GitHub prerelease is only a
// read-only transport for the same pinned bytes, never a browser-generated input.
export function readBaselineBundle(path, visual, { stateIds = pinnedStates(visual) } = {}) {
  validateVisual(visual, { cuts: true, stateIds })
  need(stateIds.length > 0, 'Missing approved visual reference states')
  need(typeof path === 'string' && path.length > 0, 'Missing approved visual baseline attachment (AEON_VISUAL_BASELINE_FILE)')
  let stat
  try { stat = statSync(path) } catch { throw new Error('Approved visual baseline attachment is missing or unreadable') }
  need(stat.isFile() && stat.size > 0 && stat.size <= MAX_BUNDLE, 'Visual baseline bundle exceeds 48 MiB')
  return verifyBaselineBundle(readFileSync(path), visual, { stateIds })
}

export function verifyBaselineBundle(bytes, visual, { stateIds = pinnedStates(visual) } = {}) {
  validateVisual(visual, { cuts: true, stateIds })
  need(stateIds.length > 0, 'Missing approved visual reference states')
  need(bytes.length > 0 && bytes.length <= MAX_BUNDLE, 'Visual baseline bundle exceeds 48 MiB')
  const source = visual.baselineAuthority.source
  const expectedDigest = source.format === 'tar' ? source.baselineBundle?.sha256 : source.sha256
  need(sha256(bytes) === expectedDigest, 'Visual baseline attachment SHA-256 mismatch')
  const bundle = JSON.parse(bytes)
  need(bundle.version === 1 && /^[a-f0-9]{40}$/.test(bundle.captureSHA ?? ''), 'Invalid visual baseline bundle provenance')
  assertRenderer(bundle.renderer, visual.renderer)
  if (source.format === 'tar') need(bundle.captureSHA === source.captureSHA, 'Visual baseline capture SHA mismatch')
  const pinned = pinnedStates(visual)
  validateVisual(visual, { cuts: true, stateIds: pinned })
  const expected = new Map(visual.states.filter(state => pinned.includes(state.id)).flatMap(state =>
    state.checkpoints.map(checkpoint => [checkpoint.reference.attachmentMember, checkpoint.reference.sha256])))
  need(Array.isArray(bundle.files) && bundle.files.length === expected.size,
    `Visual baseline bundle must contain exactly ${expected.size} pinned references`)
  const files = new Map()
  for (const file of bundle.files) {
    need(typeof file.name === 'string' && /^V-[a-z0-9-]+\.png$/.test(file.name) && expected.has(file.name) && !files.has(file.name), 'Unknown or duplicate visual baseline member')
    need(typeof file.base64 === 'string' && file.base64.length <= Math.ceil(MAX_PNG / 3) * 4, 'Invalid or oversized visual baseline PNG')
    const png = Buffer.from(file.base64, 'base64')
    need(png.toString('base64') === file.base64, 'Invalid visual baseline base64')
    need(png.length > 24 && png.length <= MAX_PNG && png.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])), 'Visual baseline member is not a bounded PNG')
    need(sha256(png) === expected.get(file.name), `Visual baseline member SHA-256 mismatch: ${file.name}`)
    files.set(file.name, png)
  }
  return files
}

// Partial pins or a missing pin on a declared approved state must fail closed,
// rather than silently changing that state's gate to "awaiting reference".
export function referenceGates(visual) {
  validateVisual(visual)
  return visual.states.map(state => {
    const approved = visual.approval.states?.includes(state.id) || state.approval?.decision === 'ok-to-pin' ||
      state.checkpoints.some(checkpoint => checkpoint.reference?.attachmentMember || checkpoint.reference?.sha256)
    if (approved) validateVisual(visual, { cuts: true, stateIds: [state.id] })
    return { state: state.id, checkpoints: state.checkpoints.length, mode: approved ? 'compare' : state.referenceStatus === 'deferred' ? 'deferred' : 'awaiting-reference',
      status: approved ? 'approved' : state.referenceStatus === 'deferred' ? 'deferred' : 'changes-requested',
      reason: approved ? 'Tick-approved, digest-pinned reference' : state.referenceStatus === 'deferred' ? `deferred until ${state.deferredUntil.join(' and ')}: ${state.deferredReason}` :
        state.approval?.reason ?? 'Changes requested; awaiting AEON-1688 and the next reviewed reference round' }
  })
}

export function referenceGate(visual, checkpointId) {
  const state = visual.states.find(state => state.checkpoints.some(checkpoint => checkpoint.id === checkpointId))
  need(state, `Unknown visual checkpoint: ${checkpointId}`)
  return referenceGates(visual).find(gate => gate.state === state.id)
}

export function referenceReport(visual) {
  const gates = referenceGates(visual)
  return { gates, markdown: ['Visual reference gates (deferred states report only; other states require quality checks).', '',
    '| State | Checkpoints | Reference gate |', '| --- | ---: | --- |',
    ...gates.map(gate => `| ${gate.state} | ${gate.checkpoints} | ${gate.mode === 'deferred' ? gate.reason : gate.mode === 'compare' ? 'strict image comparison + quality' :
      `awaiting reference (${gate.status}); quality required — ${gate.reason}`} |`), ''].join('\n') }
}

function boundedFile(path, limit, label) {
  try {
    const stat = statSync(path)
    need(stat.isFile() && stat.size > 0 && stat.size <= limit, `${label} exceeds its size bound`)
    return readFileSync(path)
  } catch (error) {
    throw new Error(`${label} is missing, unreadable or outside its size bound`, { cause: error })
  }
}

// Only read named members to stdout. Never extract paths, links or permissions
// onto the runner's filesystem. Both transport digests precede even tar listing.
export function verifyReferenceArchive(archive, detachedBytes, visual, { tar = execFileSync } = {}) {
  const source = visual.baselineAuthority.source
  need(source.format === 'tar', 'Visual reference source must be a pinned tar archive')
  need(archive.length > 0 && archive.length <= MAX_ARCHIVE && archive.length === source.bytes,
    'Visual reference archive size mismatch')
  need(sha256(archive) === source.sha256, 'Visual reference archive SHA-256 mismatch')
  need(detachedBytes.length > 0 && detachedBytes.length <= MAX_MANIFEST && detachedBytes.length === source.manifest.bytes,
    'Visual detached manifest size mismatch')
  need(sha256(detachedBytes) === source.manifest.sha256, 'Visual detached manifest SHA-256 mismatch')
  const detached = JSON.parse(detachedBytes)
  assertRenderer(detached.renderer, visual.renderer)
  const gates = referenceGates(visual)
  const approved = gates.filter(gate => gate.mode === 'compare').map(gate => gate.state)
  need(detached.version === 1 && detached.captureSHA === source.captureSHA &&
    detached.archive?.sha256 === source.sha256 && detached.archive.bytes === archive.length &&
    detached.manifestSHA256 === source.archiveManifest.sha256 &&
    detached.baselineBundle?.member === source.baselineBundle.attachmentMember &&
    detached.baselineBundle.sha256 === source.baselineBundle.sha256 &&
    JSON.stringify([...detached.states].sort()) === JSON.stringify([...approved].sort()), 'Visual detached manifest authority mismatch')
  const pinned = visual.states.filter(state => approved.includes(state.id)).flatMap(state => state.checkpoints.map(checkpoint =>
    ({ id: checkpoint.id, attachmentMember: checkpoint.reference.attachmentMember, sha256: checkpoint.reference.sha256 })))
  const sorted = rows => [...rows].sort((a, b) => a.id.localeCompare(b.id))
  need(JSON.stringify(sorted(detached.members)) === JSON.stringify(sorted(pinned)), 'Visual detached manifest reference pins mismatch')
  need(Array.isArray(detached.payload) && detached.payload.length === pinned.length * 2 + 1,
    'Visual detached manifest payload count mismatch')
  const expected = new Map(detached.payload.map(row => [row.name, row]))
  const expectedNames = new Set(['baseline.json', ...pinned.flatMap(row => [row.attachmentMember, `quality/${row.id}.json`])])
  need(expected.size === expectedNames.size && [...expected.keys()].every(name => expectedNames.has(name)),
    'Unsafe or duplicate visual archive member')
  const readTar = (args, maxBuffer) => {
    try { return tar('tar', args, { input: archive, maxBuffer, timeout: 10_000, stdio: ['pipe', 'pipe', 'pipe'] }) }
    catch (error) { throw new Error('Visual reference archive is unreadable or exceeds its extraction bound', { cause: error }) }
  }
  const members = readTar(['-tf', '-'], 64 * 1024).toString('utf8').trim().split('\n')
  need(members.length === expected.size + 1 && new Set(members).size === members.length &&
    members.every(name => expected.has(name) || name === source.archiveManifest.attachmentMember),
  'Visual reference archive has missing, duplicate or unexpected members')
  const innerBytes = readTar(['-xOf', '-', source.archiveManifest.attachmentMember], MAX_MANIFEST)
  need(sha256(innerBytes) === source.archiveManifest.sha256, 'Visual archive manifest SHA-256 mismatch')
  const inner = JSON.parse(innerBytes)
  assertRenderer(inner.renderer, visual.renderer)
  need(JSON.stringify(inner.payload) === JSON.stringify(detached.payload), 'Visual archive payload manifest mismatch')
  const baseline = readTar(['-xOf', '-', source.baselineBundle.attachmentMember], MAX_BUNDLE)
  const baselineEntry = expected.get(source.baselineBundle.attachmentMember)
  need(baselineEntry && baseline.length === baselineEntry.bytes && sha256(baseline) === baselineEntry.sha256,
    'Visual archived baseline SHA-256 mismatch')
  const references = verifyBaselineBundle(baseline, visual)
  // The archive includes review evidence as well as the baseline. Every named
  // payload must match its digest, not only the decoded reference PNG bundle.
  for (const [name, entry] of expected) {
    need(Number.isSafeInteger(entry.bytes) && entry.bytes > 0 && entry.bytes <= MAX_BUNDLE, 'Invalid visual payload size')
    const bytes = name === source.baselineBundle.attachmentMember ? baseline : readTar(['-xOf', '-', name], name.endsWith('.png') ? MAX_PNG : MAX_MANIFEST)
    need(bytes.length === entry.bytes && sha256(bytes) === entry.sha256, `Visual archive payload SHA-256 mismatch: ${name}`)
  }
  return { baseline, references }
}

export function materializeBaseline(directory, visual, { download = false, run = execFileSync } = {}) {
  const report = referenceReport(visual)
  mkdirSync(directory, { recursive: true })
  writeFileSync(join(directory, 'reference-gates.json'), JSON.stringify(report.gates, null, 2) + '\n')
  writeFileSync(join(directory, 'reference-gates.md'), report.markdown)
  if (!report.gates.some(gate => gate.mode === 'compare')) {
    console.log('No active visual comparison authority; Linux capture awaits Tick approval and explicit pins. Historical assets are not downloaded.')
    console.log(report.markdown)
    return null
  }
  const { source } = visual.baselineAuthority
  need(/^[A-Za-z0-9._-]+\.tar$/.test(source.name) && source.manifest.name === `${source.name}.manifest.json`, 'Invalid visual reference asset names')
  mkdirSync(directory, { recursive: true })
  if (download) {
    const mirror = source.mirror
    need(mirror?.kind === 'github-prerelease' && mirror.repository === 'inspr-at/paimos' &&
      /^test-references-\d+$/.test(mirror.tag), 'Missing approved GitHub visual reference mirror')
    try {
      run('gh', ['release', 'download', mirror.tag, '--repo', mirror.repository, '--pattern', source.name,
        '--pattern', source.manifest.name, '--dir', directory], { timeout: 120_000, maxBuffer: 64 * 1024, stdio: ['ignore', 'pipe', 'pipe'] })
    } catch (error) {
      throw new Error('Approved visual reference release assets are missing or unreadable; contents:read access is required', { cause: error })
    }
  }
  const archive = boundedFile(join(directory, source.name), MAX_ARCHIVE, 'Approved visual reference archive')
  const detached = boundedFile(join(directory, source.manifest.name), MAX_MANIFEST, 'Approved visual detached manifest')
  const { baseline, references } = verifyReferenceArchive(archive, detached, visual)
  // No output is written until the archive, both manifests and bundle verify.
  const path = join(directory, 'baseline.json')
  writeFileSync(path, baseline)
  console.log(`Verified approved visual references: ${references.size} PNGs; archive SHA-256 ${source.sha256}`)
  console.log(report.markdown)
  return path
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [mode, directory] = process.argv.slice(2)
    need(['download', 'verify'].includes(mode) && directory, 'Usage: node scripts/test-prune/visual-baselines.mjs <download|verify> <directory>')
    const visual = JSON.parse(readFileSync(new URL('./visual-manifest.json', import.meta.url), 'utf8'))
    materializeBaseline(directory, visual, { download: mode === 'download' })
  } catch (error) {
    console.error(`Visual reference materialization failed: ${error.message}`)
    process.exitCode = 1
  }
}
