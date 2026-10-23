// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { verifyBaselineBundle, readBaselineBundle, verifyReferenceArchive, referenceGate, referenceGates, referenceReport, materializeBaseline, checkpointReport } from '../../scripts/test-prune/visual-baselines.mjs'
import { collectCandidates, packCandidates, writeCandidateArchive } from '../../scripts/test-prune/visual-candidates.mjs'

import { assertRenderer, assertFontEvidence, assertClockEvidence, visualClock, verifyFontAssets, captureEnabled, rendererIdentity, installRendererFonts } from '../../scripts/test-prune/visual-renderer.mjs'
import { validateVisual, visualCheckResult } from '../../scripts/test-prune/check.mjs'
import { parseWorkflow } from '../../scripts/ci-static.mjs'

const hash = (bytes: Uint8Array) => createHash('sha256').update(bytes).digest('hex')
const manifest = JSON.parse(readFileSync(new URL('../../scripts/test-prune/visual-manifest.json', import.meta.url), 'utf8'))

test('visual references require approval, immutable attachment bytes and every exact member', () => {
  // A tiny synthetic PNG is enough for the transport proof. The browser guard
  // independently proves that actual differing pixels reject the comparison.
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jS1cAAAAASUVORK5CYII=', 'base64')
  const visual = structuredClone(manifest)
  visual.comparison.mode = 'blocking'
  visual.renderer.runner.imageVersion = 'synthetic-image'
  for (const state of visual.states) {
    delete state.referenceStatus; delete state.deferredUntil; delete state.deferredReason
    delete state.approval
    for (const checkpoint of state.checkpoints) {
      delete checkpoint.referenceStatus; delete checkpoint.deferredUntil; delete checkpoint.deferredReason
    }
  }
  visual.approval = { reviewer: 'Tick', status: 'approved', approvedAt: 'synthetic-proof', evidence: 'Synthetic fixture, never a product approval' }
  Object.assign(visual.baselineAuthority, { status: 'approved' })
  Object.assign(visual.baselineAuthority.source, { attachmentId: 'synthetic-test-only' })
  visual.baselineAuthority.source.format = 'json'
  visual.baselineAuthority.retention.verifiedAt = 'synthetic-proof'
  const files = visual.states.flatMap((state: { checkpoints: { id: string; reference: { attachmentMember: string; sha256: string } }[] }) => state.checkpoints.map(checkpoint => {
    checkpoint.reference = { attachmentMember: `${checkpoint.id}.png`, sha256: hash(png) }
    return { name: checkpoint.reference.attachmentMember, base64: png.toString('base64') }
  }))
  const bytes = Buffer.from(JSON.stringify({ version: 1, captureSHA: 'a'.repeat(40), renderer: visual.renderer, files }))
  visual.baselineAuthority.source.sha256 = hash(bytes)
  assert.equal(verifyBaselineBundle(bytes, visual).size, 80)
  const historyOnly = structuredClone(visual)
  historyOnly.comparison.mode = 'report-only'
  historyOnly.history = [] // Synthetic transport, never real reference history.
  historyOnly.approval.status = 'historical-only'
  historyOnly.baselineAuthority.status = 'historical-only'
  assert.equal(verifyBaselineBundle(bytes, historyOnly).size, 80)
  assert.throws(() => verifyBaselineBundle(Buffer.concat([bytes, Buffer.from(' ')]), historyOnly), /attachment SHA-256 mismatch/)
  assert.throws(() => validateVisual(historyOnly, { cuts: true, stateIds: ['agents'] }), /Protected visual cuts stay closed/)
  const rehash = (files: unknown[]) => {
    const changed = Buffer.from(JSON.stringify({ version: 1, captureSHA: 'a'.repeat(40), renderer: visual.renderer, files }))
    return { changed, pinned: { ...visual, baselineAuthority: { ...visual.baselineAuthority, source: { ...visual.baselineAuthority.source, sha256: hash(changed) } } } }
  }
  // Keep this negative control independent of later real attachment approvals.
  const unpinned = structuredClone(visual)
  for (const state of unpinned.states) {
    for (const checkpoint of state.checkpoints) checkpoint.reference = { attachmentMember: null, sha256: null }
  }
  assert.throws(() => verifyBaselineBundle(bytes, unpinned), /Missing approved visual reference states/)
  assert.throws(() => readBaselineBundle('', visual), /Missing approved visual baseline attachment/)
  assert.throws(() => verifyBaselineBundle(Buffer.concat([bytes, Buffer.from(' ')]), visual), /attachment SHA-256 mismatch/)
  const absent = rehash(files.slice(1))
  assert.throws(() => verifyBaselineBundle(absent.changed, absent.pinned), /exactly 80/)
  const duplicate = rehash([...files.slice(0, -1), files[0]])
  assert.throws(() => verifyBaselineBundle(duplicate.changed, duplicate.pinned), /duplicate/)
  const mismatch = rehash([{ ...files[0], base64: Buffer.concat([png, Buffer.from('changed')]).toString('base64') }, ...files.slice(1)])
  assert.throws(() => verifyBaselineBundle(mismatch.changed, mismatch.pinned), /member SHA-256 mismatch/)

  // Risk: a mutable release tag, a corrupt download or an incorrect digest
  // must never reach tar, even when the reviewed baseline itself is valid.
  const mirrored = structuredClone(manifest)
  mirrored.comparison.mode = 'blocking'
  mirrored.approval = structuredClone(manifest.history[0].approval)
  mirrored.baselineAuthority = structuredClone(manifest.history[0].baselineAuthority)
  mirrored.renderer.runner.imageVersion = 'synthetic-image'
  for (const state of mirrored.states) {
    delete state.approval
    for (const checkpoint of state.checkpoints) {
      delete checkpoint.referenceStatus; delete checkpoint.deferredUntil; delete checkpoint.deferredReason
      checkpoint.reference = { attachmentMember: null, sha256: null }
    }
  }
  for (const historical of manifest.history[0].states) Object.assign(mirrored.states.find((state: { id: string }) => state.id === historical.id), structuredClone(historical))
  const approved = mirrored.states.filter((state: { approval?: { decision: string } }) => state.approval?.decision === 'ok-to-pin')
  const approvedFiles = approved.flatMap((state: { checkpoints: { id: string; reference: { attachmentMember: string; sha256: string } }[] }) => state.checkpoints.map(checkpoint => {
    checkpoint.reference.sha256 = hash(png)
    return { name: checkpoint.reference.attachmentMember, base64: png.toString('base64') }
  }))
  const source = mirrored.baselineAuthority.source
  const baseline = Buffer.from(JSON.stringify({ version: 1, captureSHA: source.captureSHA, renderer: mirrored.renderer, files: approvedFiles }))
  source.baselineBundle.sha256 = hash(baseline)
  const payload = [...approvedFiles.flatMap((file: { name: string }) => [
    { name: file.name, bytes: png }, { name: `quality/${file.name.replace('.png', '.json')}`, bytes: Buffer.from('{}') },
  ]), { name: 'baseline.json', bytes: baseline }]
  const members = approved.flatMap((state: { checkpoints: { id: string; reference: { attachmentMember: string; sha256: string } }[] }) =>
    state.checkpoints.map(checkpoint => ({ id: checkpoint.id, ...checkpoint.reference })))
  const inner = { version: 1, captureSHA: source.captureSHA, renderer: mirrored.renderer, states: mirrored.approval.states, members,
    payload: payload.map(file => ({ name: file.name, bytes: file.bytes.length, sha256: hash(file.bytes) })) }
  const innerBytes = Buffer.from(JSON.stringify(inner))
  source.archiveManifest.sha256 = hash(innerBytes)
  // A bounded ustar fixture exercises the real system tar reader on both macOS
  // and Linux, without depending on uncommitted review assets or any network.
  const entry = ({ name, bytes }: { name: string; bytes: Buffer }) => {
    const header = Buffer.alloc(512)
    header.write(name)
    header.write('0000644\0', 100); header.write('0000000\0', 108); header.write('0000000\0', 116)
    header.write(bytes.length.toString(8).padStart(11, '0') + '\0', 124)
    header.write('00000000000\0', 136); header.fill(32, 148, 156); header.write('0', 156)
    header.write('ustar\0', 257); header.write('00', 263)
    const checksum = header.reduce((sum, byte) => sum + byte, 0)
    header.write(checksum.toString(8).padStart(6, '0') + '\0 ', 148)
    return Buffer.concat([header, bytes, Buffer.alloc((512 - bytes.length % 512) % 512)])
  }
  const archive = Buffer.concat([...payload, { name: 'manifest.json', bytes: innerBytes }].map(entry).concat(Buffer.alloc(1024)))
  Object.assign(source, { sha256: hash(archive), bytes: archive.length })
  const detached = Buffer.from(JSON.stringify({ ...inner, archive: { sha256: source.sha256, bytes: source.bytes },
    baselineBundle: { member: 'baseline.json', sha256: source.baselineBundle.sha256 }, manifestSHA256: source.archiveManifest.sha256 }))
  Object.assign(source.manifest, { sha256: hash(detached), bytes: detached.length })
  assert.equal(verifyReferenceArchive(archive, detached, mirrored).references.size, 30)
  const noExtraction = { tar: () => assert.fail('Unverified bytes reached tar') }
  const tampered = Buffer.from(archive); tampered[600] ^= 1
  assert.throws(() => verifyReferenceArchive(tampered, detached, mirrored, noExtraction), { message: 'Visual reference archive SHA-256 mismatch' })
  const wrongDigest = structuredClone(mirrored); wrongDigest.baselineAuthority.source.sha256 = '0'.repeat(64)
  assert.throws(() => verifyReferenceArchive(archive, detached, wrongDigest, noExtraction), { message: 'Visual reference archive SHA-256 mismatch' })
  const wrongManifest = Buffer.from(detached); wrongManifest[20] ^= 1
  assert.throws(() => verifyReferenceArchive(archive, wrongManifest, mirrored, noExtraction), { message: 'Visual detached manifest SHA-256 mismatch' })
  const wrongManifestDigest = structuredClone(mirrored); wrongManifestDigest.baselineAuthority.source.manifest.sha256 = '0'.repeat(64)
  assert.throws(() => verifyReferenceArchive(archive, detached, wrongManifestDigest, noExtraction), { message: 'Visual detached manifest SHA-256 mismatch' })
  assert.throws(() => verifyReferenceArchive(Buffer.alloc(0), detached, mirrored, noExtraction), /archive size mismatch/)

  const gates = referenceGates(manifest)
  assert.equal(gates.length, 80)
  assert.ok(gates.every(gate => gate.mode === 'report-only' && gate.status === 'awaiting-re-capture'))
  assert.equal(gates.filter(gate => gate.historicalReference).length, 72)
  assert.equal(gates.filter(gate => !gate.historicalReference).length, 8)
  assert.match(referenceReport(manifest).markdown, /awaiting re-capture under the final renderer/)
  for (const state of manifest.states) for (const checkpoint of state.checkpoints)
    assert.deepEqual(checkpoint.reference, { attachmentMember: null, sha256: null })
  assert.equal(manifest.history[0].states.flatMap((state: { checkpoints: unknown[] }) => state.checkpoints).length, 30)
  const linuxHistory = manifest.history.find(row => row.referenceSet === 2)
  assert.equal(linuxHistory.status, 'historical-only')
  assert.equal(linuxHistory.approval.checkpoints.length, 53)
  assert.equal(linuxHistory.baselineAuthority.source.unapprovedMembers.length, 19)
  assert.equal(linuxHistory.states.find(state => state.id === 'delivery-numbers').checkpoints[0].referenceStatus, 'deferred')
  const noDownload = mkdtempSync(join(tmpdir(), 'aeon-1674-no-authority-'))
  try {
    const awaiting = structuredClone(manifest)
    awaiting.comparison.mode = 'blocking'
    awaiting.approval = { states: [], checkpoints: [] }
    for (const state of awaiting.states) {
      delete state.approval
      for (const checkpoint of state.checkpoints) checkpoint.reference = { attachmentMember: null, sha256: null }
    }
    assert.equal(materializeBaseline(noDownload, awaiting, { download: true, run: () => assert.fail('Historical assets must not be downloaded') }), null)
  } finally { rmSync(noDownload, { recursive: true, force: true }) }
  const lostPin = structuredClone(mirrored)
  lostPin.states.find((state: { id: string }) => state.id === 'projects').checkpoints[0].reference.sha256 = null
  assert.throws(() => referenceGates(lostPin), { message: 'Unpinned visual reference: V-projects-390-light' })

  // Every renderer field is a rejection boundary. The version is deliberately
  // synthetic; current runner metadata cannot authorize any product reference.
  const renderer = visual.renderer
  assertRenderer(renderer, renderer)
  for (const key of Object.keys(renderer)) {
    const changed = { ...structuredClone(renderer), [key]: 'changed' }
    assert.throws(() => assertRenderer(changed, renderer), /renderer differs from reference/)
  }
  const missingImage = structuredClone(renderer); missingImage.runner.imageVersion = null
  assert.throws(() => assertRenderer(renderer, missingImage), /renderer differs from reference: reference has no pinned runner ImageVersion/)
  assertRenderer(renderer, missingImage, { capture: true })
  const actual = rendererIdentity(manifest, manifest.renderer.browser.version, { env: { AEON_VISUAL_RUNNER: 'ubuntu-24.04', ImageOS: 'ubuntu24', ImageVersion: 'synthetic-image' }, platform: 'linux', arch: 'x64' })
  assertRenderer(actual, renderer)
  assert.equal(captureEnabled(manifest, {}), false)
  assert.throws(() => captureEnabled(manifest, { AEON_VISUAL_CAPTURE: '1', GITHUB_EVENT_NAME: 'pull_request' }), /only by the manual/)
  assert.throws(() => captureEnabled(manifest, { AEON_VISUAL_CAPTURE: '1', GITHUB_EVENT_NAME: 'push' }), /only by the manual/)
  assert.equal(captureEnabled(manifest, { AEON_VISUAL_CAPTURE: '1', GITHUB_EVENT_NAME: 'workflow_dispatch', GITHUB_WORKFLOW: 'CI', GITHUB_JOB: 'visual-reference-capture' }), true)
  const changedBrowser = rendererIdentity(manifest, manifest.renderer.browser.version, { env: { AEON_VISUAL_RUNNER: 'ubuntu-24.04', ImageOS: 'ubuntu24', ImageVersion: 'synthetic-image' },
    platform: 'linux', arch: 'x64', browserOptions: { browserName: 'chromium', headless: true, launchOptions: { args: [] } } })
  assert.throws(() => assertRenderer(changedBrowser, renderer), /renderer differs from reference: browser/)
  const workflow = readFileSync(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8')
  const captureJob = workflow.slice(workflow.indexOf('\n  visual-reference-capture:'), workflow.indexOf('\n  # Read-only proof.'))
  const compareJob = workflow.slice(workflow.indexOf('\n  web-shard:'), workflow.indexOf('\n  # Preserve the ruleset'))
  assert.match(captureJob, /if: github.event_name == 'workflow_dispatch' && inputs.visual_capture == true/)
  assert.match(captureJob, /runs-on: ubuntu-24\.04/)
  assert.doesNotMatch(captureJob, /\n    (needs|strategy|continue-on-error):/)
  assert.match(compareJob, /runs-on: ubuntu-latest/)
  assert.doesNotMatch(compareJob, /AEON_VISUAL_CAPTURE/)
  for (const job of [captureJob, compareJob]) {
    assert.match(job, /AEON_VISUAL_RUNNER: ubuntu-24\.04/)
    assert.doesNotMatch(job, /install-fonts|FONTCONFIG/)
  }
  // Risk: provisioning a private renderer must not mutate the worker/job env.
  // Its subprocesses see fontconfig; an ordinary browser inherits no override.
  const fontconfigBefore = process.env.FONTCONFIG_FILE
  const installDirectory = mkdtempSync(join(tmpdir(), 'aeon-1674-font-isolation-'))
  try {
    const calls: string[] = []
    const fontconfig = installRendererFonts(manifest, installDirectory, { run: (command, args, options) => {
      calls.push(command)
      assert.equal(options.env.FONTCONFIG_FILE, join(installDirectory, 'fonts.conf'))
      assert.equal(process.env.FONTCONFIG_FILE, fontconfigBefore)
      return command === 'fc-match' ? manifest.renderer.fontconfig.aliases[args[1].slice(':family='.length)] : Buffer.alloc(0)
    } })
    assert.equal(fontconfig, join(installDirectory, 'fonts.conf'))
    assert.deepEqual(calls, ['fc-cache', 'fc-match', 'fc-match', 'fc-match', 'fc-match'])
    assert.equal(process.env.FONTCONFIG_FILE, fontconfigBefore)
  } finally { rmSync(installDirectory, { recursive: true, force: true }) }
  const rendererSource = readFileSync(new URL('../../scripts/test-prune/visual-renderer.mjs', import.meta.url), 'utf8')
  assert.doesNotMatch(rendererSource, /GITHUB_ENV/)
  const changedImage = structuredClone(actual)
  changedImage.runner.imageOS = 'different-image'
  assert.throws(() => assertRenderer(changedImage, renderer), /renderer differs from reference: runner/)
  changedImage.runner = { ...actual.runner, imageVersion: 'different-version' }
  assert.throws(() => assertRenderer(changedImage, renderer), /renderer differs from reference: runner/)
  assert.match(captureJob, /retention-days: 7/)
  assert.doesNotMatch(captureJob, /contents: write|gh release|visual-baselines\.mjs/)
  const steps = parseWorkflow(workflow).get('visual-reference-capture')
  assert.ok(steps.some((step: { run?: string }) => step.run?.includes('visual-candidates.mjs web/test-results/capture-run web/test-results/linux-candidate')))

  // One pinned area does not need reference bytes from unrelated pending or
  // deferred states. The required state's own missing member still rejects.
  const partial = structuredClone(visual)
  for (const state of partial.states) if (state.id !== 'projects') {
    for (const checkpoint of state.checkpoints) checkpoint.reference = { attachmentMember: null, sha256: null }
  }
  const goals = partial.states.find((state: { id: string }) => state.id === 'goals')
  Object.assign(goals, { referenceStatus: 'deferred', deferredUntil: ['AEON-1621', 'AEON-1625'], deferredReason: 'Synthetic future canvas' })
  const partialFiles = files.filter((file: { name: string }) => file.name.startsWith('V-projects-'))
  const partialBytes = Buffer.from(JSON.stringify({ version: 1, captureSHA: 'a'.repeat(40), renderer: visual.renderer, files: partialFiles }))
  partial.baselineAuthority.source.sha256 = hash(partialBytes)
  assert.equal(verifyBaselineBundle(partialBytes, partial, { stateIds: ['projects'] }).size, 4)
  assert.throws(() => verifyBaselineBundle(partialBytes, partial, { stateIds: ['agents'] }), { message: 'Unpinned visual reference: V-agents-390-light' })
  assert.throws(() => verifyBaselineBundle(partialBytes, partial, { stateIds: ['goals'] }), /Deferred visual reference: V-goals-390-light/)
  const missingPartialBytes = Buffer.from(JSON.stringify({ version: 1, captureSHA: 'a'.repeat(40), renderer: visual.renderer, files: partialFiles.slice(1) }))
  partial.baselineAuthority.source.sha256 = hash(missingPartialBytes)
  assert.throws(() => verifyBaselineBundle(missingPartialBytes, partial), /exactly 4 pinned references/)

  const records = manifest.states.filter((state: { id: string }) => manifest.capture.states.includes(state.id)).flatMap((state: { owner: string; fixture: string; fixturesByProfile?: Record<string, string>; checkpoints: { id: string; profile: string; theme: string }[] }) => state.checkpoints.map(checkpoint => {
    const viewport = manifest.profiles[checkpoint.profile]
    // Only the bounded PNG header is consumed by this packaging proof. Actual
    // raster decoding and pixel differences remain in the browser negative test.
    const sized = Buffer.from(png)
    sized.writeUInt32BE(viewport.width, 16); sized.writeUInt32BE(viewport.height, 20)
    return { png: sized, quality: { id: checkpoint.id, owner: state.owner,
      fixture: state.fixturesByProfile?.[checkpoint.profile] ?? state.fixture, theme: checkpoint.theme, viewport,
      captureSHA: 'a'.repeat(40), renderer, requirementsRun: true, captureMode: true, baselineCompared: false,
      clock: { mode: 'fixed', now: visualClock(manifest, state.id).now,
        samples: ['after-fixture', 'before-capture', 'before-comparison'].map(phase => ({ phase,
          now: Date.parse(visualClock(manifest, state.id).now), iso: visualClock(manifest, state.id).now })) },
      fonts: {
        body: { computedFamily: 'system-ui', renderedFamilies: ['Inter Variable'] },
        painted: [{ family: 'Inter Variable', custom: false }],
        profile: [],
        ...(['public-quote', 'quote-editor'].includes(state.id) ? {
          profile: [400, 600, 700].flatMap(weight => ['normal', 'italic'].map(style => ({ family: 'QuoteProfilebody', role: 'body', weight: String(weight), style, status: 'loaded', checked: true })))
            .concat([{ family: 'QuoteProfiledisplay', role: 'display', weight: '400', style: 'normal', status: 'loaded', checked: true }]),
          document: { body: { computedFamily: 'QuoteProfilebody', renderedFamilies: ['Source Sans 3'] }, display: { computedFamily: 'QuoteProfiledisplay', renderedFamilies: ['Source Serif 4 Display'] } },
        } : {}),
      }, pngSHA256: hash(sized), layout: [], axe: [], console: [], responses: [] } }
  }))
  verifyFontAssets(manifest)
  const badFontAsset = structuredClone(manifest); badFontAsset.renderer.fonts[0].sha256 = '0'.repeat(64)
  assert.throws(() => verifyFontAssets(badFontAsset), /font asset SHA-256/)
  const monoEvidence = { body: { computedFamily: 'system-ui', renderedFamilies: ['JetBrains Mono'] }, painted: [{ family: 'JetBrains Mono', custom: true }], profile: [] }
  assert.throws(() => assertFontEvidence(monoEvidence, 'projects'), /body text must resolve to Inter/)
  const monoRecords = records.map((record, index) => index ? record : { ...record, quality: { ...record.quality, fonts: monoEvidence } })
  assert.throws(() => packCandidates(monoRecords, manifest), /body text must resolve to Inter/)
  const quoteEvidence = structuredClone(records.find(record => record.quality.id.startsWith('V-public-quote'))!.quality.fonts)
  quoteEvidence.profile[0]!.status = 'unloaded'
  assert.throws(() => assertFontEvidence(quoteEvidence, 'public-quote'), /profile faces must all be loaded/)
  const packed = packCandidates(records, manifest)
  assert.equal(packed.review.clocks.capacity.now, '2026-09-29T12:02:00.000Z')
  assert.equal(packed.review.clocks.hours.now, '2026-09-24T08:00:00.000Z')
  const missingClock = records.map((record, index) => index ? record : { ...record, quality: { ...record.quality, clock: undefined } })
  assert.throws(() => packCandidates(missingClock, manifest), /fixture clock changed or missing/)
  const driftedClock = structuredClone(records[0]!.quality.clock)
  driftedClock.samples[2]!.now += 1
  assert.throws(() => assertClockEvidence(driftedClock, visualClock(manifest, 'projects')), /fixture clock changed or missing/)
  const driftedRecords = records.map((record, index) => index ? record : { ...record, quality: { ...record.quality, clock: driftedClock } })
  assert.throws(() => packCandidates(driftedRecords, manifest), /fixture clock changed or missing/)
  const missingPolicy = structuredClone(manifest)
  delete missingPolicy.states[0].clock
  assert.throws(() => visualClock(missingPolicy, 'projects'), /missing fixed fixture clock/)
  assert.equal(packed.review.status, 'pending-Tick-review')
  assert.equal(packed.review.members.length, 72)
  assert.deepEqual(packed.review.deferred.map((state: { id: string }) => state.id), ['goals', 'ticket-workspace'])
  assert.throws(() => packCandidates(records.slice(1), manifest), /all 72/)
  assert.throws(() => packCandidates([...records.slice(0, -1), records[0]], manifest), /Duplicate/)
  const changedSource = records.map((record: { png: Buffer; quality: Record<string, unknown> }) => ({ png: record.png, quality: { ...record.quality } }))
  changedSource[1].quality.captureSHA = 'b'.repeat(40)
  assert.throws(() => packCandidates(changedSource, manifest), /one source SHA/)
  const wrongFixture = records.map((record: { quality: Record<string, unknown> }, index: number) => index ? record : { ...record, quality: { ...record.quality, owner: 'tests/visual/unrelated.spec.ts' } })
  assert.throws(() => packCandidates(wrongFixture, manifest), /Wrong review fixture\/profile: V-projects-390-light/)
  const deferredCapture = records.map((record: { quality: Record<string, unknown> }, index: number) => index ? record : { ...record, quality: { ...record.quality, id: 'V-goals-390-light' } })
  assert.throws(() => packCandidates(deferredCapture, manifest), /Missing review capture: V-projects-390-light/)
  const wrongSize = records.map((record: { png: Buffer }, index: number) => {
    if (index) return record
    const png = Buffer.from(record.png); png.writeUInt32BE(1, 16)
    return { ...record, png }
  })
  assert.throws(() => packCandidates(wrongSize, manifest), /Wrong capture dimensions: V-projects-390-light/)
  for (const name of ['layout', 'axe', 'console', 'responses']) {
    const failedQuality = records.map((record: { quality: Record<string, unknown> }, index: number) => index ? record : { ...record, quality: { ...record.quality, [name]: ['Unresolved finding'] } })
    assert.throws(() => packCandidates(failedQuality, manifest), new RegExp(`Unresolved ${name}`))
  }
  const wrongBytes = records.map((record: { png: Buffer }, index: number) => index ? record : { ...record, png: Buffer.concat([record.png, Buffer.from('changed')]) })
  assert.throws(() => packCandidates(wrongBytes, manifest), /Capture digest mismatch/)
  const wrongRenderer = records.map((record: { quality: Record<string, unknown> }, index: number) => index ? record : { ...record, quality: { ...record.quality, renderer: { ...renderer, locale: 'de-DE' } } })
  assert.throws(() => packCandidates(wrongRenderer, manifest), /renderer differs from reference: locale/)
  const fallback = records.map((record: { quality: Record<string, unknown> }, index: number) => index ? record : { ...record, quality: { ...record.quality, fonts: [{ family: 'Arial', custom: false }] } })
  assert.throws(() => packCandidates(fallback, manifest), /Visual font guard/)
  const directory = mkdtempSync(join(tmpdir(), 'aeon-1674-candidate-'))
  try {
    const captureDirectory = join(directory, 'captures')
    mkdirSync(captureDirectory)
    const phoneRecords = records.filter(record => record.quality.id.startsWith('V-delivery-numbers-390-'))
    for (const record of phoneRecords) {
      writeFileSync(join(captureDirectory, `${record.quality.id}.md`), JSON.stringify({ ...record.quality, reference: { mode: 'deferred' } }))
      writeFileSync(join(captureDirectory, `${record.quality.id}.png`), record.png)
    }
    writeFileSync(join(captureDirectory, 'V-goals-390-light.md'), JSON.stringify({ reference: { mode: 'deferred' }, requirementsRun: false }))
    assert.equal(collectCandidates(captureDirectory).length, 2, 'Manual capture keeps deferred phone candidates; ordinary deferred reports have no PNG')
    const output = join(directory, 'artifact')
    const candidate = writeCandidateArchive(records, manifest, output)
    assert.equal(candidate.status, 'pending-Tick-review')
    assert.equal(candidate.members.length, 72)
    assert.equal(candidate.archive.sha256, hash(readFileSync(join(output, candidate.archive.name))))
    assert.equal(candidate.payload.length, 145)
    assert.match(readFileSync(join(output, 'members.sha256'), 'utf8'), /quality\/V-projects-390-light.json/)
    // The generated archive can be consumed only after explicit synthetic
    // approval and pins. This proves next-round transport compatibility too.
    const pinned = structuredClone(manifest)
    pinned.comparison.mode = 'blocking'
    pinned.renderer = renderer
    pinned.approval = { reviewer: 'Tick', status: 'approved', approvedAt: 'synthetic-only', evidence: 'Synthetic approval for transport test', states: candidate.states }
    pinned.baselineAuthority = structuredClone(mirrored.baselineAuthority)
    const source = pinned.baselineAuthority.source
    const detached = readFileSync(join(output, `${candidate.archive.name}.manifest.json`))
    Object.assign(source, { sha256: candidate.archive.sha256, bytes: candidate.archive.bytes, captureSHA: candidate.captureSHA,
      baselineBundle: candidate.baselineBundle, archiveManifest: { attachmentMember: 'manifest.json', sha256: candidate.manifestSHA256 },
      manifest: { bytes: detached.length, sha256: hash(detached) } })
    pinned.baselineAuthority.source.baselineBundle.attachmentMember = 'baseline.json'
    for (const state of pinned.states) {
      delete state.approval
      for (const checkpoint of state.checkpoints) {
        delete checkpoint.referenceStatus; delete checkpoint.deferredUntil; delete checkpoint.deferredReason
        const member = candidate.members.find((member: { id: string }) => member.id === checkpoint.id)
        if (member) checkpoint.reference = { attachmentMember: member.attachmentMember, sha256: member.sha256 }
      }
    }
    const archive = readFileSync(join(output, candidate.archive.name))
    assert.equal(verifyReferenceArchive(archive, detached, pinned).references.size, 72)
    // Risk: a byte-identical 72-capture archive is mistaken for approval of
    // rejected phone crops. Verify every byte, expose only the approved 70,
    // and keep the approved desktop half of this state independently usable.
    const subset = structuredClone(pinned)
    const rejected = ['V-delivery-numbers-390-light', 'V-delivery-numbers-390-dark']
    subset.approval.states = subset.approval.states.filter((id: string) => id !== 'delivery-numbers')
    subset.approval.checkpoints = candidate.members.filter(member => !rejected.includes(member.id)).map(member => member.id)
    subset.baselineAuthority.source.unapprovedMembers = candidate.members.filter(member => rejected.includes(member.id))
    const delivery = subset.states.find((state: { id: string }) => state.id === 'delivery-numbers')
    for (const checkpoint of delivery.checkpoints) if (rejected.includes(checkpoint.id)) {
      checkpoint.reference = { attachmentMember: null, sha256: null }
      Object.assign(checkpoint, { referenceStatus: 'deferred', deferredUntil: ['AEON-1674'], deferredReason: 'Synthetic phone crop changes requested' })
    }
    const verified = verifyReferenceArchive(archive, detached, subset)
    assert.equal(verified.references.size, 70)
    for (const id of rejected) {
      assert.equal(verified.references.has(`${id}.png`), false)
      assert.equal(referenceGate(subset, id).mode, 'deferred')
    }
    assert.equal(referenceGate(subset, 'V-delivery-numbers-1440-light').mode, 'compare')
    assert.equal(verifyBaselineBundle(verified.baseline, subset, { checkpointIds: ['V-delivery-numbers-1440-light'] }).size, 70)
    assert.throws(() => verifyBaselineBundle(verified.baseline, subset, { stateIds: ['delivery-numbers'] }), /Deferred visual reference: V-delivery-numbers-390-light/)
    const missingDesktop = structuredClone(subset)
    missingDesktop.states.find((state: { id: string }) => state.id === 'delivery-numbers').checkpoints[2].reference.sha256 = null
    assert.throws(() => referenceGates(missingDesktop), { message: 'Unpinned visual reference: V-delivery-numbers-1440-light' })
    const wronglyPinned = structuredClone(subset)
    wronglyPinned.states.find((state: { id: string }) => state.id === 'delivery-numbers').checkpoints[0].reference = candidate.members.find(member => member.id === rejected[0])
    assert.throws(() => referenceGates(wronglyPinned), /deferred reference is pinned/)
    const changedExcluded = JSON.parse(verified.baseline.toString())
    changedExcluded.files.find((file: { name: string }) => file.name === `${rejected[0]}.png`).base64 = Buffer.concat([png, Buffer.from('changed')]).toString('base64')
    const changedExcludedBytes = Buffer.from(JSON.stringify(changedExcluded))
    const repinnedTransport = structuredClone(subset)
    repinnedTransport.baselineAuthority.source.baselineBundle.sha256 = hash(changedExcludedBytes)
    assert.throws(() => verifyBaselineBundle(changedExcludedBytes, repinnedTransport), { message: `Visual baseline member SHA-256 mismatch: ${rejected[0]}.png` })
    const duplicatedExcluded = JSON.parse(verified.baseline.toString())
    duplicatedExcluded.files[duplicatedExcluded.files.findIndex((file: { name: string }) => file.name === `${rejected[1]}.png`)] =
      duplicatedExcluded.files.find((file: { name: string }) => file.name === `${rejected[0]}.png`)
    const duplicatedBytes = Buffer.from(JSON.stringify(duplicatedExcluded))
    repinnedTransport.baselineAuthority.source.baselineBundle.sha256 = hash(duplicatedBytes)
    assert.throws(() => verifyBaselineBundle(duplicatedBytes, repinnedTransport), { message: 'Unknown or duplicate visual baseline member' })
    const tampered = Buffer.from(archive); tampered[600] ^= 1
    assert.throws(() => verifyReferenceArchive(tampered, detached, pinned, noExtraction), /archive SHA-256 mismatch/)
  } finally { rmSync(directory, { recursive: true, force: true }) }
})

// Risk: switching visual CI to reports could hide failures or accidentally
// authorize protected pruning with historical set-1/set-2 approvals.
test('visual report-only switch records failures and restores blocking without granting historical cut authority', async () => {
  const failure = new Error('synthetic differing pixels')
  const run = async () => { throw failure }
  assert.deepEqual(await visualCheckResult(manifest, run), { status: 'failed', error: String(failure) })
  assert.deepEqual(await visualCheckResult(manifest, async () => 'comparison ran'), { status: 'passed', value: 'comparison ran' })
  const blocking = structuredClone(manifest)
  blocking.comparison.mode = 'blocking'
  await assert.rejects(visualCheckResult(blocking, run), error => error === failure)
  assert.deepEqual(await visualCheckResult(blocking, async () => 'comparison ran'), { status: 'passed', value: 'comparison ran' })
  assert.throws(() => validateVisual(manifest, { cuts: true, stateIds: ['agents'] }), /Protected visual cuts stay closed/)
  assert.throws(() => validateVisual(blocking, { cuts: true, stateIds: ['agents'] }), /Unpinned visual reference/)
  const revivedHistory = { ...blocking, ...structuredClone(manifest.history.find(row => row.referenceSet === 2)), comparison: blocking.comparison }
  revivedHistory.approval.status = 'historical-only'
  assert.throws(() => validateVisual(revivedHistory, { cuts: true, stateIds: ['agents'] }), /Cuts require Tick approval/)
  const missingMode = structuredClone(manifest)
  delete missingMode.comparison.mode
  assert.throws(() => validateVisual(missingMode), /Missing visual comparison mode/)
  const directory = mkdtempSync(join(tmpdir(), 'aeon-1674-reports-'))
  try {
    const id = 'V-projects-390-light'
    writeFileSync(join(directory, `${id}.md`), JSON.stringify({ id, requirementsRun: true,
      quality: { status: 'failed', error: 'synthetic font guard failure' }, comparison: { status: 'different', error: 'synthetic pixel diff', diffs: [`${id}-diff.png`] } }))
    const summary = checkpointReport(directory, manifest)
    assert.equal(summary.split('\n').filter(line => line.startsWith('| V-')).length, 80)
    assert.match(summary, /V-projects-390-light.*failed.*different.*synthetic font guard failure/)
    assert.match(summary, /V-projects-390-light-diff\.png/)
    assert.match(summary, /V-tickets-390-light.*not run in this shard/)
    assert.match(summary, /awaiting re-capture under the final renderer/)
    assert.equal(checkpointReport(join(directory, 'absent'), manifest).split('\n').filter(line => line.startsWith('| V-')).length, 80)
  } finally { rmSync(directory, { recursive: true, force: true }) }
})
