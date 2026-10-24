// SPDX-License-Identifier: AGPL-3.0-only
// Keep the deliberate checkpoints, including passing captures, apart from
// traces, fixture screenshots and the comparator's baseline/diff images.
import { createHash } from 'node:crypto'
import { existsSync, lstatSync, mkdirSync, opendirSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { validateVisual } from './check.mjs'

export const visualEvidenceBounds = Object.freeze({ entries: 20_000, depth: 24, reportBytes: 256 * 1024,
  pngBytes: 4 * 1024 * 1024, totalBytes: 64 * 1024 * 1024, checkpoints: 80 })
const need = (condition, message) => { if (!condition) throw new Error(message) }
const hash = bytes => createHash('sha256').update(bytes).digest('hex')

function regularFile(path, maximum) {
  const stat = lstatSync(path)
  need(stat.isFile() && !stat.isSymbolicLink() && stat.size > 0 && stat.size <= maximum, 'Visual evidence is not a bounded regular file')
  return stat
}

export function collectVisualEvidence(directory, visual, { sha, bounds = visualEvidenceBounds } = {}) {
  validateVisual(visual)
  need(/^[a-f0-9]{40}$/.test(sha ?? ''), 'Visual evidence needs the checkout SHA')
  const checkpoints = new Map(visual.states.flatMap(state => state.checkpoints.map(checkpoint =>
    [checkpoint.id, { owner: state.owner, viewport: visual.profiles[checkpoint.profile] }])))
  need(checkpoints.size === bounds.checkpoints, 'Visual evidence needs the exact 80-checkpoint inventory')
  const captures = new Map(), pending = [{ path: resolve(directory), depth: 0 }]
  let entries = 0, bytes = 0, scanBytes = 0
  if (!existsSync(directory)) return { captures, bytes, missing: [...checkpoints.keys()].sort() }
  while (pending.length) {
    const { path, depth } = pending.pop()
    const stat = lstatSync(path)
    need(stat.isDirectory() && !stat.isSymbolicLink() && depth <= bounds.depth, 'Visual evidence directory or depth bound')
    const handle = opendirSync(path)
    try {
      let entry
      while ((entry = handle.readSync()) !== null) {
        need(++entries <= bounds.entries, 'Visual evidence entry bound')
        const child = join(path, entry.name)
        need(!entry.isSymbolicLink(), 'Visual evidence must not follow symlinks')
        if (entry.isDirectory()) { pending.push({ path: child, depth: depth + 1 }); continue }
        const id = entry.name.replace(/\.md$/, '')
        if (!entry.isFile() || !entry.name.endsWith('.md') || !checkpoints.has(id)) continue
        scanBytes += regularFile(child, bounds.reportBytes).size
        need(scanBytes <= bounds.totalBytes, 'Visual evidence read byte bound')
        const report = JSON.parse(readFileSync(child, 'utf8'))
        const expected = checkpoints.get(id)
        // Adversarial visual guard specs use real IDs; they are not product
        // checkpoint owners and must never supply the review artifact.
        if (report.id !== id || report.sourceSpec !== expected.owner) continue
        need(report.captureSHA === sha, `Stale visual checkpoint: ${id}`)
        const failedCapture = report.pngSHA256 == null && report.failurePNG
        if (report.pngSHA256 == null && !failedCapture) continue
        if (failedCapture) need(failedCapture.name === `failed-${id}.png` && report.quality?.status === 'failed', `Invalid failed visual capture: ${id}`)
        const pngDigest = failedCapture ? failedCapture.sha256 : report.pngSHA256
        need(/^[a-f0-9]{64}$/.test(pngDigest), `Invalid visual PNG digest: ${id}`)
        const pngPath = join(path, failedCapture ? failedCapture.name : `${id}.png`), pngStat = regularFile(pngPath, bounds.pngBytes)
        bytes += pngStat.size
        scanBytes += pngStat.size
        need(scanBytes <= bounds.totalBytes, 'Visual evidence exceeds 64 MiB')
        const png = readFileSync(pngPath)
        need(png.length > 24 && png.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])) &&
          png.readUInt32BE(16) === expected.viewport.width && png.readUInt32BE(20) === expected.viewport.height,
        `Invalid visual checkpoint PNG: ${id}`)
        need(hash(png) === pngDigest, `Visual checkpoint digest mismatch: ${id}`)
        // Preserve the first attempt. A permitted merge-queue retry lives in
        // another launch directory and cannot replace the original evidence.
        const retry = path.split(/[\\/]/).some(part => part.endsWith('-retry'))
        const previous = captures.get(id)
        need(!previous || previous.retry !== retry, `Duplicate visual checkpoint attempt: ${id}`)
        if (!previous || previous.retry && !retry) captures.set(id, { png, retry })
      }
    } finally { handle.closeSync() }
  }
  return { captures, bytes, missing: [...checkpoints.keys()].filter(id => !captures.has(id)).sort() }
}

export function writeVisualEvidence(directory, output, visual, options) {
  const result = collectVisualEvidence(directory, visual, options)
  // A fresh output directory prevents a previous run from filling a missing
  // checkpoint. It contains PNGs only, with manifest-owned member names.
  mkdirSync(output, { recursive: false })
  for (const [id, { png }] of result.captures) writeFileSync(join(output, `${id}.png`), png, { flag: 'wx' })
  return { checkpoints: result.captures.size, bytes: [...result.captures.values()].reduce((sum, row) => sum + row.png.length, 0), missing: result.missing }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [directory, output] = process.argv.slice(2)
    need(directory && output && process.argv.length === 4, 'Usage: visual-evidence.mjs <test-output> <new-artifact-directory>')
    const visual = JSON.parse(readFileSync(new URL('./visual-manifest.json', import.meta.url), 'utf8'))
    const result = writeVisualEvidence(directory, output, visual, { sha: process.env.GITHUB_SHA })
    console.log(JSON.stringify(result))
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
