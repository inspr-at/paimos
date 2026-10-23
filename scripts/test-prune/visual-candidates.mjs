// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { validateVisual } from './check.mjs'
import { assertRenderer, assertFontEvidence, captureEnabled } from './visual-renderer.mjs'

const hash = bytes => createHash('sha256').update(bytes).digest('hex')
const need = (condition, message) => { if (!condition) throw new Error(message) }
const MAX_PNG = 4 * 1024 * 1024

// Packaging is a review operation, never approval. It neither changes the
// manifest nor lets a browser use its current capture as an approved reference.
export function packCandidates(records, visual) {
  validateVisual(visual)
  const states = visual.states.filter(state => visual.capture.states.includes(state.id) && state.referenceStatus !== 'deferred')
  const expected = states.flatMap(state => state.checkpoints.map(checkpoint => ({
    ...checkpoint, owner: state.owner, fixture: state.fixturesByProfile?.[checkpoint.profile] ?? state.fixture,
  })))
  need(Array.isArray(records) && records.length === expected.length, `Review bundle needs all ${expected.length} quality-checked captures from the non-deferred states`)
  const byId = new Map()
  for (const record of records) {
    need(record?.quality && !byId.has(record.quality.id), 'Duplicate or missing capture identity')
    byId.set(record.quality.id, record)
  }
  let captureSHA, renderer
  const files = [], members = []
  for (const checkpoint of expected) {
    const record = byId.get(checkpoint.id)
    need(record, `Missing review capture: ${checkpoint.id}`)
    const { quality, png } = record, viewport = visual.profiles[checkpoint.profile]
    need(/^[a-f0-9]{40}$/.test(quality.captureSHA ?? ''), `Missing capture source: ${checkpoint.id}`)
    captureSHA ??= quality.captureSHA
    need(quality.captureSHA === captureSHA, 'Review captures must come from one source SHA')
    assertRenderer(quality.renderer, visual.renderer, { capture: true })
    renderer ??= quality.renderer
    assertRenderer(quality.renderer, renderer)
    need(quality.requirementsRun === true && quality.captureMode === true && quality.baselineCompared === false, `Missing font/quality evidence: ${checkpoint.id}`)
    assertFontEvidence(quality.fonts, states.find(state => state.owner === checkpoint.owner).id)
    need(quality.owner === checkpoint.owner && quality.fixture === checkpoint.fixture && quality.theme === checkpoint.theme &&
      quality.viewport?.width === viewport.width && quality.viewport?.height === viewport.height, `Wrong review fixture/profile: ${checkpoint.id}`)
    for (const name of ['layout', 'axe', 'console', 'responses']) {
      need(Array.isArray(quality[name]) && quality[name].length === 0, `Unresolved ${name} findings: ${checkpoint.id}`)
    }
    need(Buffer.isBuffer(png) && png.length > 24 && png.length <= MAX_PNG &&
      png.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])), `Invalid review PNG: ${checkpoint.id}`)
    need(png.readUInt32BE(16) === viewport.width && png.readUInt32BE(20) === viewport.height, `Wrong capture dimensions: ${checkpoint.id}`)
    const sha256 = hash(png), name = `${checkpoint.id}.png`
    need(quality.pngSHA256 === sha256, `Capture digest mismatch: ${checkpoint.id}`)
    files.push({ name, base64: png.toString('base64') })
    members.push({ id: checkpoint.id, attachmentMember: name, sha256 })
  }
  const bytes = Buffer.from(JSON.stringify({ version: 1, captureSHA, renderer, files }) + '\n')
  need(bytes.length <= 48 * 1024 * 1024, 'Review bundle exceeds 48 MiB')
  return { bytes, review: { version: 1, status: 'pending-Tick-review', captureSHA, renderer, comparison: visual.comparison, sha256: hash(bytes),
    states: states.map(state => state.id),
    deferred: visual.states.filter(state => state.referenceStatus === 'deferred').map(state => ({
      id: state.id, checkpoints: state.checkpoints.map(checkpoint => checkpoint.id),
      until: state.deferredUntil, reason: state.deferredReason,
    })), members } }
}

export function collectCandidates(directory) {
  const records = []
  let entries = 0
  function walk(path) {
    for (const entry of readdirSync(path, { withFileTypes: true })) {
      need(++entries <= 20_000, 'Too many review artifact entries')
      const child = join(path, entry.name)
      if (entry.isDirectory()) walk(child)
      else if (entry.isFile() && /^V-[a-z0-9-]+\.md$/.test(entry.name)) {
        need(statSync(child).size <= 256 * 1024, 'Oversized quality report')
        const quality = JSON.parse(readFileSync(child, 'utf8'))
        if (quality.reference?.mode === 'deferred' && quality.captureMode !== true) continue
        const pngPath = join(path, entry.name.replace(/\.md$/, '.png'))
        need(statSync(pngPath).size <= MAX_PNG, 'Oversized review PNG')
        records.push({ quality, png: readFileSync(pngPath) })
      }
    }
  }
  walk(resolve(directory))
  return records
}

export function writeCandidateArchive(records, visual, directory) {
  const { bytes, review } = packCandidates(records, visual)
  mkdirSync(directory, { recursive: false })
  mkdirSync(join(directory, 'quality'))
  const payload = [{ name: 'baseline.json', bytes }]
  for (const record of records) payload.push({ name: `${record.quality.id}.png`, bytes: record.png },
    { name: `quality/${record.quality.id}.json`, bytes: Buffer.from(JSON.stringify(record.quality, null, 2) + '\n') })
  for (const file of payload) writeFileSync(join(directory, file.name), file.bytes, { flag: 'wx' })
  const manifest = { ...review, payload: payload.map(file => ({ name: file.name, bytes: file.bytes.length, sha256: hash(file.bytes) })) }
  const inner = Buffer.from(JSON.stringify(manifest, null, 2) + '\n')
  writeFileSync(join(directory, 'manifest.json'), inner, { flag: 'wx' })
  const archiveName = `AEON-1674-linux-${review.captureSHA.slice(0, 10)}.tar`
  // Only generated, validated member names enter tar. This directory is an
  // artifact, never a reference directory or a publication destination.
  execFileSync('tar', ['-cf', archiveName, ...payload.map(file => file.name), 'manifest.json'],
    { cwd: directory, timeout: 10_000, maxBuffer: 64 * 1024, stdio: ['ignore', 'pipe', 'pipe'] })
  const archivePath = join(directory, archiveName)
  need(statSync(archivePath).size <= 64 * 1024 * 1024, 'Review archive exceeds 64 MiB')
  const archive = readFileSync(archivePath)
  const detached = { ...manifest, archive: { name: archiveName, bytes: archive.length, sha256: hash(archive) },
    manifestSHA256: hash(inner), baselineBundle: { member: 'baseline.json', sha256: hash(bytes) } }
  writeFileSync(`${archivePath}.manifest.json`, JSON.stringify(detached, null, 2) + '\n', { flag: 'wx' })
  writeFileSync(`${archivePath}.sha256`, `${hash(archive)}  ${archiveName}\n`, { flag: 'wx' })
  writeFileSync(join(directory, 'members.sha256'), [...manifest.payload, { name: 'manifest.json', sha256: hash(inner) }]
    .map(file => `${file.sha256}  ${file.name}`).join('\n') + '\n', { flag: 'wx' })
  return detached
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [directory, output] = process.argv.slice(2)
  const visual = JSON.parse(readFileSync(new URL('./visual-manifest.json', import.meta.url), 'utf8'))
  if (directory === 'specs') {
    validateVisual(visual)
    for (const id of visual.capture.states) {
      const state = visual.states.find(state => state.id === id)
      need(state && state.referenceStatus !== 'deferred', 'Capture set includes a missing or deferred state')
      console.log(state.owner)
    }
    process.exit(0)
  }
  need(directory && output, 'Usage: node scripts/test-prune/visual-candidates.mjs <one-run-artifact-directory> <new-artifact-directory>')
  need(captureEnabled(visual), 'Packaging requires the manual capture workflow')
  const root = resolve('web/test-results')
  need(resolve(output).startsWith(`${root}/`) && resolve(directory).startsWith(`${root}/`), 'Capture artifacts must remain under web/test-results')
  need(statSync(dirname(resolve(output))).isDirectory(), 'Review output parent must exist')
  const review = writeCandidateArchive(collectCandidates(directory), visual, output)
  console.log(`Pending review: ${review.members.length} captures at ${review.captureSHA}; bundle SHA-256 ${review.sha256}`)
}
