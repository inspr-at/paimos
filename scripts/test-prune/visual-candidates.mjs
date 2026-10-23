// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { validateVisual } from './check.mjs'

const hash = bytes => createHash('sha256').update(bytes).digest('hex')
const need = (condition, message) => { if (!condition) throw new Error(message) }
const MAX_PNG = 4 * 1024 * 1024

// Packaging is a review operation, never approval. It neither changes the
// manifest nor lets a browser use its current capture as an approved reference.
export function packCandidates(records, visual) {
  validateVisual(visual)
  const states = visual.states.filter(state => state.referenceStatus !== 'deferred')
  const expected = states.flatMap(state => state.checkpoints.map(checkpoint => ({
    ...checkpoint, owner: state.owner, fixture: state.fixturesByProfile?.[checkpoint.profile] ?? state.fixture,
  })))
  need(Array.isArray(records) && records.length === expected.length, `Review bundle needs all ${expected.length} quality-checked captures from the non-deferred states`)
  const byId = new Map()
  for (const record of records) {
    need(record?.quality && !byId.has(record.quality.id), 'Duplicate or missing capture identity')
    byId.set(record.quality.id, record)
  }
  let captureSHA
  const files = [], members = []
  for (const checkpoint of expected) {
    const record = byId.get(checkpoint.id)
    need(record, `Missing review capture: ${checkpoint.id}`)
    const { quality, png } = record, viewport = visual.profiles[checkpoint.profile]
    need(/^[a-f0-9]{40}$/.test(quality.captureSHA ?? ''), `Missing capture source: ${checkpoint.id}`)
    captureSHA ??= quality.captureSHA
    need(quality.captureSHA === captureSHA, 'Review captures must come from one source SHA')
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
  const bytes = Buffer.from(JSON.stringify({ version: 1, captureSHA, files }) + '\n')
  need(bytes.length <= 48 * 1024 * 1024, 'Review bundle exceeds 48 MiB')
  return { bytes, review: { status: 'pending-Tick-review', captureSHA, sha256: hash(bytes),
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
        const pngPath = join(path, entry.name.replace(/\.md$/, '.png'))
        need(statSync(pngPath).size <= MAX_PNG, 'Oversized review PNG')
        records.push({ quality: JSON.parse(readFileSync(child, 'utf8')), png: readFileSync(pngPath) })
      }
    }
  }
  walk(resolve(directory))
  return records
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [directory, output] = process.argv.slice(2)
  need(directory && output, 'Usage: node scripts/test-prune/visual-candidates.mjs <one-run-artifact-directory> <review-bundle.json>')
  const visual = JSON.parse(readFileSync(new URL('./visual-manifest.json', import.meta.url), 'utf8'))
  const { bytes, review } = packCandidates(collectCandidates(directory), visual)
  need(statSync(dirname(resolve(output))).isDirectory(), 'Review output directory must exist')
  writeFileSync(output, bytes, { flag: 'wx' })
  writeFileSync(`${output}.review.json`, JSON.stringify(review, null, 2) + '\n', { flag: 'wx' })
  console.log(`Pending review: ${review.members.length} captures at ${review.captureSHA}; bundle SHA-256 ${review.sha256}`)
}
