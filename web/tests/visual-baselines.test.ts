// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { verifyBaselineBundle, readBaselineBundle } from '../../scripts/test-prune/visual-baselines.mjs'
import { packCandidates } from '../../scripts/test-prune/visual-candidates.mjs'

const hash = (bytes: Uint8Array) => createHash('sha256').update(bytes).digest('hex')
const manifest = JSON.parse(readFileSync(new URL('../../scripts/test-prune/visual-manifest.json', import.meta.url), 'utf8'))

test('visual references require approval, immutable attachment bytes and every exact member', () => {
  // A tiny synthetic PNG is enough for the transport proof. The browser guard
  // independently proves that actual differing pixels reject the comparison.
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jS1cAAAAASUVORK5CYII=', 'base64')
  const visual = structuredClone(manifest)
  visual.approval = { reviewer: 'Tick', status: 'approved', approvedAt: 'synthetic-proof', evidence: 'Synthetic fixture, never a product approval' }
  Object.assign(visual.baselineAuthority, { status: 'approved' })
  Object.assign(visual.baselineAuthority.source, { attachmentId: 'synthetic-test-only' })
  visual.baselineAuthority.retention.verifiedAt = 'synthetic-proof'
  const files = visual.states.flatMap((state: { checkpoints: { id: string; reference: { attachmentMember: string; sha256: string } }[] }) => state.checkpoints.map(checkpoint => {
    checkpoint.reference = { attachmentMember: `${checkpoint.id}.png`, sha256: hash(png) }
    return { name: checkpoint.reference.attachmentMember, base64: png.toString('base64') }
  }))
  const bytes = Buffer.from(JSON.stringify({ version: 1, captureSHA: 'a'.repeat(40), files }))
  visual.baselineAuthority.source.sha256 = hash(bytes)
  assert.equal(verifyBaselineBundle(bytes, visual).size, 80)
  const rehash = (files: unknown[]) => {
    const changed = Buffer.from(JSON.stringify({ version: 1, captureSHA: 'a'.repeat(40), files }))
    return { changed, pinned: { ...visual, baselineAuthority: { ...visual.baselineAuthority, source: { ...visual.baselineAuthority.source, sha256: hash(changed) } } } }
  }
  assert.throws(() => verifyBaselineBundle(bytes, manifest), /Unpinned visual reference|Cuts require Tick approval/)
  assert.throws(() => readBaselineBundle('', visual), /Missing approved visual baseline attachment/)
  assert.throws(() => verifyBaselineBundle(Buffer.concat([bytes, Buffer.from(' ')]), visual), /attachment SHA-256 mismatch/)
  const absent = rehash(files.slice(1))
  assert.throws(() => verifyBaselineBundle(absent.changed, absent.pinned), /exactly 80/)
  const duplicate = rehash([...files.slice(0, -1), files[0]])
  assert.throws(() => verifyBaselineBundle(duplicate.changed, duplicate.pinned), /duplicate/)
  const mismatch = rehash([{ ...files[0], base64: Buffer.concat([png, Buffer.from('changed')]).toString('base64') }, ...files.slice(1)])
  assert.throws(() => verifyBaselineBundle(mismatch.changed, mismatch.pinned), /member SHA-256 mismatch/)

  const records = manifest.states.flatMap((state: { owner: string; fixture: string; fixturesByProfile?: Record<string, string>; checkpoints: { id: string; profile: string; theme: string }[] }) => state.checkpoints.map(checkpoint => {
    const viewport = manifest.profiles[checkpoint.profile]
    // Only the bounded PNG header is consumed by this packaging proof. Actual
    // raster decoding and pixel differences remain in the browser negative test.
    const sized = Buffer.from(png)
    sized.writeUInt32BE(viewport.width, 16); sized.writeUInt32BE(viewport.height, 20)
    return { png: sized, quality: { id: checkpoint.id, owner: state.owner,
      fixture: state.fixturesByProfile?.[checkpoint.profile] ?? state.fixture, theme: checkpoint.theme, viewport,
      captureSHA: 'a'.repeat(40), pngSHA256: hash(sized), layout: [], axe: [], console: [], responses: [] } }
  }))
  assert.equal(packCandidates(records, manifest).review.status, 'pending-Tick-review')
  assert.throws(() => packCandidates(records.slice(1), manifest), /all 80/)
  assert.throws(() => packCandidates([...records.slice(0, -1), records[0]], manifest), /Duplicate/)
  const changedSource = records.map((record: { png: Buffer; quality: Record<string, unknown> }) => ({ png: record.png, quality: { ...record.quality } }))
  changedSource[1].quality.captureSHA = 'b'.repeat(40)
  assert.throws(() => packCandidates(changedSource, manifest), /one source SHA/)
  const failedQuality = records.map((record: { quality: Record<string, unknown> }, index: number) => index ? record : { ...record, quality: { ...record.quality, console: ['Unmocked route'] } })
  assert.throws(() => packCandidates(failedQuality, manifest), /Unresolved console/)
  const wrongBytes = records.map((record: { png: Buffer }, index: number) => index ? record : { ...record, png: Buffer.concat([record.png, Buffer.from('changed')]) })
  assert.throws(() => packCandidates(wrongBytes, manifest), /Capture digest mismatch/)
})
