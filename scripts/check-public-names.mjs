// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1061: public product sources carry neutral examples or tenant data.
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { lstatSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const roots = ['web/src', 'web/tests', 'internal', 'cmd', 'api', 'docs', 'tests']
const history = 'internal/releasehistory/data/product-notes.json'
const privateNames = /agm[0-9]+|csb[0-9]+|hsb[0-9]+|mbp26[0-9]{2}|augmentoring/gi
const digest = bytes => createHash('sha256').update(bytes).digest('hex')
const isRecord = value => value !== null && typeof value === 'object' && !Array.isArray(value)
// Formatting and object-key order can change when packnotes writes the bundle.
// Array order and every entry value remain part of the published-content pin.
const canonical = value => Array.isArray(value) ? value.map(canonical)
  : isRecord(value) ? Object.fromEntries(Object.keys(value).sort().map(key => [key, canonical(value[key])])) : value
const entryDigest = value => digest(JSON.stringify(canonical(value)))

function scanHistory(bytes, allowlist, failures) {
  const notes = JSON.parse(bytes.toString('utf8'))
  if (!isRecord(notes) || notes.schema !== 'aeon.product-release-notes.v1' || !isRecord(notes.releases)) {
    throw new Error(`Cannot safely scan release-note history: ${history}`)
  }
  const pinned = new Set()
  for (const entry of allowlist) {
    if (!Object.hasOwn(notes.releases, entry.version) || entryDigest(notes.releases[entry.version]) !== entry.sha256) {
      throw new Error(`Published release entry changed or missing: ${history}:releases.${entry.version}`)
    }
    pinned.add(entry.version)
  }
  const scan = (location, value) => {
    privateNames.lastIndex = 0
    if (privateNames.test(JSON.stringify(value))) failures.push(`${history}:${location}: operator host or business name`)
  }
  for (const [key, value] of Object.entries(notes)) {
    if (key !== 'releases') scan(key, { [key]: value })
  }
  for (const [version, entry] of Object.entries(notes.releases)) {
    if (!pinned.has(version)) scan(`releases.${version}`, { [version]: entry })
  }
}

export function checkPublicNames(root) {
  const allowlist = JSON.parse(readFileSync(resolve(root, 'scripts/ci/public-names-allowlist.json'), 'utf8'))
  if (!Array.isArray(allowlist) || allowlist.length > 256 || allowlist.some(entry =>
    !isRecord(entry) || entry.path !== history || !/^[0-9]{12}\.0\.0$/.test(entry.version ?? '') ||
    !/^[a-f0-9]{64}$/.test(entry.sha256 ?? '') || typeof entry.reason !== 'string' || !entry.reason.trim()) ||
    new Set(allowlist.map(entry => entry.version)).size !== allowlist.length) {
    throw new Error('Only version-and-digest-pinned published release entries may be allowlisted')
  }
  const paths = [...new Set(execFileSync('git', ['ls-files', '-z', '--cached', '--others', '--exclude-standard', '--', ...roots], {
    cwd: root, encoding: 'utf8', timeout: 30_000, maxBuffer: 32 << 20,
  }).split('\0').filter(Boolean))].sort()
  const failures = []
  let scannedHistory = false
  for (const path of paths) {
    const full = resolve(root, path)
    let stat
    try { stat = lstatSync(full) } catch (error) { if (error.code === 'ENOENT') continue; throw error }
    if (!stat.isFile() || stat.size > (32 << 20)) throw new Error(`Cannot safely scan public source: ${path}`)
    const bytes = readFileSync(full)
    if (path === history) {
      scanHistory(bytes, allowlist, failures)
      scannedHistory = true
      continue
    }
    for (const [index, line] of bytes.toString('utf8').split('\n').entries()) {
      privateNames.lastIndex = 0
      if (privateNames.test(line)) failures.push(`${path}:${index + 1}: operator host or business name`)
    }
  }
  if (allowlist.length && !scannedHistory) throw new Error(`Published release-note history is missing: ${history}`)
  return failures
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const failures = checkPublicNames(process.cwd())
    if (failures.length) {
      console.error(failures.join('\n'))
      process.exitCode = 1
    } else console.log('Public names: neutral; published history only is allowlisted')
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
