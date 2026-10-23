// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { verifyBaselineBundle, readBaselineBundle, verifyReferenceArchive, referenceGate, referenceGates, referenceReport, materializeBaseline, checkpointReport } from '../../scripts/test-prune/visual-baselines.mjs'
import { collectCandidates, packCandidates, writeCandidateArchive } from '../../scripts/test-prune/visual-candidates.mjs'
import { pinCheckpoints, pinManifest, assertPinIdentity, pngTools, replayCandidates, publicationCommands } from '../../scripts/test-prune/visual-pin.mjs'
import { compareCaptureRecords, proveCaptureIdentity } from '../../scripts/test-prune/visual-identity.mjs'
import { PROBE_REPEATS, PROBE_SETTINGS, probeCheckpoints, visualProbe, summarizeProbe, runProbe } from '../../scripts/test-prune/visual-probe.mjs'
import { checkContainerPin } from '../../scripts/test-prune/visual-container-check.mjs'
import { pixelChanges } from '../../scripts/test-prune/visual-review.mjs'

import { assertVisualContainer, assertRenderer, assertFontEvidence, assertClockEvidence, visualClock, verifyFontAssets, captureEnabled, rendererIdentity, installRendererFonts } from '../../scripts/test-prune/visual-renderer.mjs'
import { validateVisual, visualCheckResult } from '../../scripts/test-prune/check.mjs'
import { parseWorkflow } from '../../scripts/ci-static.mjs'

const hash = (bytes: Uint8Array) => createHash('sha256').update(bytes).digest('hex')
const manifest = JSON.parse(readFileSync(new URL('../../scripts/test-prune/visual-manifest.json', import.meta.url), 'utf8'))
// Tick's set-3 receipt covers these exact 60 checkpoints from run 39975623900.
// Keep this expectation independent of the manifest's editable approval list.
const approvedSet3Checkpoints = [
  'V-projects-390-light', 'V-projects-390-dark', 'V-projects-1440-light', 'V-projects-1440-dark',
  'V-tickets-390-light', 'V-tickets-390-dark', 'V-tickets-1440-light', 'V-tickets-1440-dark',
  'V-agents-390-light', 'V-agents-390-dark', 'V-agents-1024-light', 'V-agents-1024-dark', 'V-agents-1440-light', 'V-agents-1440-dark',
  'V-session-chat-390-light', 'V-session-chat-390-dark', 'V-session-chat-1440-light', 'V-session-chat-1440-dark',
  'V-account-use-matrix-390-light', 'V-account-use-matrix-390-dark', 'V-account-use-matrix-1024-light', 'V-account-use-matrix-1024-dark', 'V-account-use-matrix-1440-dark',
  'V-models-390-light', 'V-models-390-dark', 'V-models-1440-light', 'V-models-1440-dark',
  'V-access-390-light', 'V-access-390-dark', 'V-access-1440-light', 'V-access-1440-dark',
  'V-knowledge-390-light', 'V-knowledge-390-dark', 'V-knowledge-1440-light', 'V-knowledge-1440-dark',
  'V-delivery-numbers-390-light', 'V-delivery-numbers-390-dark', 'V-delivery-numbers-1440-light', 'V-delivery-numbers-1440-dark',
  'V-quote-editor-390-light', 'V-quote-editor-390-dark', 'V-quote-editor-1024-light', 'V-quote-editor-1024-dark', 'V-quote-editor-1440-light', 'V-quote-editor-1440-dark',
  'V-public-quote-390-light', 'V-public-quote-390-dark', 'V-public-quote-1440-light', 'V-public-quote-1440-dark',
  'V-start-agent-390-light', 'V-start-agent-390-dark', 'V-start-agent-1440-light', 'V-start-agent-1440-dark',
  'V-agent-rules-390-light', 'V-agent-rules-1440-light', 'V-agent-rules-1440-dark',
  'V-capacity-390-light', 'V-capacity-390-dark', 'V-capacity-1440-light', 'V-capacity-1440-dark',
]

function reportOnlyManifest() {
  const visual = structuredClone(manifest)
  visual.comparison.mode = 'report-only'
  visual.approval = { reviewer: 'Tick', status: 'awaiting-set-3', approvedAt: null,
    evidence: 'Synthetic preparation fixture; no product approval', states: [], checkpoints: [] }
  visual.baselineAuthority.status = 'awaiting-set-3'
  for (const state of visual.states) {
    delete state.approval
    for (const checkpoint of state.checkpoints) {
      checkpoint.reference = { attachmentMember: null, sha256: null }
      if (checkpoint.referenceStatus !== 'deferred') Object.assign(checkpoint, {
        referenceStatus: 'needs-re-capture', deferredUntil: ['AEON-1692'], deferredReason: 'Synthetic fixture awaiting set-3 preparation',
      })
    }
  }
  return visual
}

test('visual references require approval, immutable attachment bytes and every exact member', async () => {
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
  assert.equal(manifest.referenceSet, 3)
  assert.equal(manifest.comparison.mode, 'blocking')
  assert.equal(manifest.approval.reviewer, 'Tick')
  assert.equal(manifest.approval.status, 'approved')
  assert.deepEqual(manifest.approval.states, [])
  assert.equal(approvedSet3Checkpoints.length, 60)
  assert.deepEqual(manifest.approval.checkpoints, approvedSet3Checkpoints)
  const authority = manifest.baselineAuthority
  assert.equal(authority.referenceSet, 3)
  assert.equal(authority.status, 'approved')
  assert.equal(authority.source.attachmentId, 'c0570f26-fe2d-496a-a2f0-d6c441bfa005')
  assert.equal(authority.source.manifest.attachmentId, '4347d3b2-536c-4c3b-b735-a53c5bf590d7')
  assert.equal(authority.source.sha256, '04130acb4361fb62fafcaa1ec4e201f72017383ef9dd12942b728e1e05ca6d64')
  assert.equal(authority.source.captureRun, '39975623900')
  assert.equal(authority.source.mirror.tag, 'test-references-3')
  const compared = gates.filter(gate => gate.mode === 'compare')
  const deferred = gates.filter(gate => gate.mode === 'deferred')
  assert.equal(gates.reduce((count, gate) => count + gate.checkpoints, 0), 80)
  assert.deepEqual(compared.flatMap(gate => gate.checkpointIds), approvedSet3Checkpoints)
  assert.ok(compared.every(gate => gate.status === 'approved'))
  assert.equal(deferred.flatMap(gate => gate.checkpointIds).length, 20)
  assert.ok(deferred.every(gate => gate.status === 'deferred'))
  assert.equal(compared.length + deferred.length, gates.length)
  const report = referenceReport(manifest).markdown
  assert.match(report, /deferred checkpoints report only/)
  assert.match(report, /strict image comparison \+ quality/)
  for (const state of manifest.states) for (const checkpoint of state.checkpoints) {
    const gate = gates.find(gate => gate.checkpointIds.includes(checkpoint.id))!
    if (approvedSet3Checkpoints.includes(checkpoint.id)) {
      assert.equal(gate.mode, 'compare')
      assert.equal(checkpoint.reference.attachmentMember, `${checkpoint.id}.png`)
      assert.match(checkpoint.reference.sha256, /^[a-f0-9]{64}$/)
      assert.equal(checkpoint.referenceStatus, undefined)
    } else {
      assert.equal(gate.mode, 'deferred')
      assert.equal(checkpoint.referenceStatus, 'deferred')
      assert.deepEqual(checkpoint.reference, { attachmentMember: null, sha256: null })
      assert.ok(checkpoint.deferredUntil.length > 0 && checkpoint.deferredReason.length > 0)
      assert.equal(gate.reason, `deferred until ${checkpoint.deferredUntil.join(' and ')}: ${checkpoint.deferredReason}`)
      assert.ok(report.includes(checkpoint.deferredReason))
    }
  }
  assert.ok(manifest.history.every(row => row.status === 'historical-only'))
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

  // Every pixel-determining field rejects changes. Hosted metadata is recorded
  // without binding the renderer to GitHub's next image rollout.
  const renderer = visual.renderer
  assertRenderer(renderer, renderer)
  for (const key of Object.keys(renderer).filter(key => key !== 'runner')) {
    const changed = { ...structuredClone(renderer), [key]: 'changed' }
    assert.throws(() => assertRenderer(changed, renderer), /renderer differs from reference|visual = CI only/)
  }
  const environment = { AEON_VISUAL_CONTAINER_DIGEST: renderer.container.digest, ImageOS: 'ubuntu26', ImageVersion: 'new-hosted-image' }
  const identityOptions = { env: environment, platform: 'linux', arch: 'x64',
    fontconfigFile: new URL('../../scripts/test-prune/visual-fonts.conf', import.meta.url) }
  const actual = rendererIdentity(manifest, manifest.renderer.browser.version, identityOptions)
  assertRenderer(actual, renderer)
  assert.equal(actual.runner.imageVersion, 'new-hosted-image')
  assertVisualContainer(manifest, identityOptions)
  for (const options of [{ ...identityOptions, env: {} }, { ...identityOptions, env: { AEON_VISUAL_CONTAINER_DIGEST: 'sha256:' + '0'.repeat(64) } },
    { ...identityOptions, platform: 'darwin' }, { ...identityOptions, arch: 'arm64' }])
    assert.throws(() => assertVisualContainer(manifest, options), /visual = CI only/)
  const missingContainer = structuredClone(actual); delete missingContainer.container.digest
  assert.throws(() => assertRenderer(missingContainer, renderer), /visual = CI only/)
  const wrongConfig = structuredClone(actual); wrongConfig.fontconfig.sha256 = '0'.repeat(64)
  assert.throws(() => assertRenderer(wrongConfig, renderer), /renderer differs from reference: fontconfig/)
  assert.equal(captureEnabled(manifest, {}), false)
  assert.throws(() => captureEnabled(manifest, { AEON_VISUAL_CAPTURE: '1', GITHUB_EVENT_NAME: 'pull_request' }), /only by the manual/)
  assert.throws(() => captureEnabled(manifest, { AEON_VISUAL_CAPTURE: '1', GITHUB_EVENT_NAME: 'push' }), /only by the manual/)
  assert.equal(captureEnabled(manifest, { AEON_VISUAL_CAPTURE: '1', GITHUB_EVENT_NAME: 'workflow_dispatch', GITHUB_WORKFLOW: 'CI', GITHUB_JOB: 'visual-reference-capture' }), true)
  const changedBrowser = rendererIdentity(manifest, manifest.renderer.browser.version, { ...identityOptions,
    browserOptions: { browserName: 'chromium', headless: true, launchOptions: { args: [] } } })
  assert.throws(() => assertRenderer(changedBrowser, renderer), /renderer differs from reference: browser/)
  const workflow = readFileSync(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8')
  const captureJob = workflow.slice(workflow.indexOf('\n  visual-reference-capture:'), workflow.indexOf('\n  # Read-only proof.'))
  const compareJob = workflow.slice(workflow.indexOf('\n  visual-comparison:'), workflow.indexOf('\n  # Preserve the ruleset'))
  const ordinaryJob = workflow.slice(workflow.indexOf('\n  web-shard:'), workflow.indexOf('\n  visual-comparison:'))
  assert.match(captureJob, /if: github.event_name == 'workflow_dispatch' && \(inputs.visual_capture == true \|\| inputs.visual_probe != ''\)/)
  assert.doesNotMatch(captureJob, /\n    (needs|strategy|continue-on-error):/)
  assert.match(ordinaryJob, /runs-on: ubuntu-latest/)
  assert.doesNotMatch(ordinaryJob, /AEON_VISUAL_|visual-baselines/)
  assert.doesNotMatch(compareJob, /AEON_VISUAL_CAPTURE/)
  for (const job of [captureJob, compareJob]) {
    assert.ok(job.includes(`${renderer.container.image}@${renderer.container.digest}`))
    assert.doesNotMatch(job, /playwright install|bounded-apt|install-fonts|FONTCONFIG/)
  }
  assert.equal(checkContainerPin(workflow, manifest, manifest), true)
  const bumped = structuredClone(manifest); bumped.renderer.container.digest = 'sha256:' + '1'.repeat(64)
  const bumpedWorkflow = workflow.replaceAll(renderer.container.digest, bumped.renderer.container.digest)
  assert.throws(() => checkContainerPin(bumpedWorkflow, bumped, manifest), /without regenerated, approved baselines/)
  assert.throws(() => checkContainerPin(bumpedWorkflow, manifest, manifest), /pin differs from manifest/)
  Object.assign(bumped.baselineAuthority.source, { sha256: '2'.repeat(64), captureSHA: '3'.repeat(40) })
  bumped.approval.status = bumped.baselineAuthority.status = 'approved'; bumped.comparison.mode = 'blocking'
  assert.equal(checkContainerPin(bumpedWorkflow, bumped, manifest), true)
  // A private, path-independent fontconfig keeps the same confined font set,
  // aliases and subprocess-only environment as P02.
  const fontconfigBefore = process.env.FONTCONFIG_FILE
  const installDirectory = mkdtempSync(join(tmpdir(), 'aeon-1692-font-isolation-'))
  try {
    const calls: string[] = []
    const fontconfig = installRendererFonts(manifest, installDirectory, { run: (command, args, options) => {
      calls.push(command)
      assert.equal(options.env.FONTCONFIG_FILE, join(installDirectory, 'fonts.conf'))
      assert.equal(options.env.XDG_CACHE_HOME, join(installDirectory, 'cache'))
      assert.equal(process.env.FONTCONFIG_FILE, fontconfigBefore)
      return command === 'fc-match' ? manifest.renderer.fontconfig.aliases[args[1].slice(':family='.length)] : Buffer.alloc(0)
    } })
    assert.equal(hash(readFileSync(fontconfig)), renderer.fontconfig.sha256)
    assert.doesNotMatch(readFileSync(fontconfig, 'utf8'), /<include|<dir>\//)
    assert.deepEqual(calls, ['fc-cache', 'fc-match', 'fc-match', 'fc-match', 'fc-match'])
    assert.equal(process.env.FONTCONFIG_FILE, fontconfigBefore)
  } finally { rmSync(installDirectory, { recursive: true, force: true }) }
  const rendererSource = readFileSync(new URL('../../scripts/test-prune/visual-renderer.mjs', import.meta.url), 'utf8')
  assert.doesNotMatch(rendererSource, /GITHUB_ENV/)
  const changedImage = structuredClone(actual)
  changedImage.runner.imageOS = 'different-image'; changedImage.runner.imageVersion = 'different-version'
  assertRenderer(changedImage, renderer)
  assert.match(captureJob, /retention-days: 7/)
  assert.doesNotMatch(captureJob, /contents: write|gh release|visual-baselines\.mjs/)
  const steps = parseWorkflow(workflow).get('visual-reference-capture')
  assert.ok(steps.some((step: { run?: string }) => step.run?.includes('visual-candidates.mjs web/test-results/container-run-1/capture-run web/test-results/container-run-1/linux-candidate')))
  assert.ok(steps.some((step: { run?: string }) => step.run?.includes('visual-candidates.mjs web/test-results/container-run-2/capture-run web/test-results/container-run-2/linux-candidate')))

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
    // Risk: a rehearsal or an approval for different capture bytes could
    // activate blocking references, revive history, or lift product deferrals.
    const preparation = reportOnlyManifest()
    const receipt = { reviewer: 'Tick', status: 'approved', dryRun: true, approvedAt: '2026-10-23T12:00:00Z',
      evidence: 'DRY RUN ONLY; no product approval', captureSHA: candidate.captureSHA, archiveSHA256: candidate.archive.sha256,
      checkpoints: pinCheckpoints(manifest) }
    const identityRows = candidate.members.map(member => ({ id: member.id, byteIdentical: true, pixels: 0, total: 1, percent: 0, regions: [] }))
    const identityProof = { version: 2, status: 'identical', captureSHA: candidate.captureSHA, archiveSHA256: candidate.archive.sha256,
      repeatArchiveSHA256: 'f'.repeat(64), containerDigest: renderer.container.digest,
      pinCheckpoints: pinCheckpoints(manifest), reviewRequired: [],
      reviewedRunnerArchiveSHA256: manifest.capture.identityProof.reviewedRunnerArchiveSHA256,
      repeat: { byteIdentical: 72, pixelIdentical: 72, checkpoints: identityRows }, runner: { pixelIdentical: 72, checkpoints: identityRows } }
    const options = { dryRun: true, identityProof }
    assert.throws(() => pinManifest(manifest, candidate, detached, receipt, {}, options), /Set 3 preparation requires the unpinned report-only manifest/)
    assert.throws(() => pinManifest(preparation, candidate, detached, receipt, {}, { dryRun: true }), /60\/60 byte-identical/)
    const unstable = structuredClone(identityProof); unstable.repeat.checkpoints[0].pixels = 1
    assert.throws(() => assertPinIdentity(unstable, candidate, manifest), /STOP before pinning/)
    const encodingOnly = structuredClone(identityProof); encodingOnly.repeat.checkpoints[0].byteIdentical = false
    assert.throws(() => assertPinIdentity(encodingOnly, candidate, manifest), /STOP before pinning/)
    assert.throws(() => assertPinIdentity({ ...identityProof, pinCheckpoints: identityProof.pinCheckpoints.slice(1) }, candidate, manifest), /STOP before pinning/)
    const wrongArchiveProof = { ...identityProof, archiveSHA256: '0'.repeat(64) }
    assert.throws(() => assertPinIdentity(wrongArchiveProof, candidate, manifest), /STOP before pinning/)
    const draft = pinManifest(preparation, candidate, detached, receipt, {}, options)
    assert.equal(draft.referenceSet, 3)
    assert.equal(draft.comparison.mode, 'blocking')
    assert.equal(draft.approval.checkpoints.length, 60)
    assert.deepEqual(draft.history, manifest.history)
    for (const id of ['goals', 'ticket-workspace']) {
      assert.deepEqual(draft.states.find(state => state.id === id), manifest.states.find(state => state.id === id))
      assert.equal(referenceGate(draft, `V-${id}-390-light`).mode, 'deferred')
    }
    assert.equal(verifyReferenceArchive(readFileSync(join(output, candidate.archive.name)), detached, draft).references.size, 60)
    // Risk: deferred noise passes through a complete archive and accidentally
    // becomes blocking authority. All bytes remain verified, only 60 enter the map.
    const deferredIDs = manifest.states.flatMap(state => state.checkpoints.filter(checkpoint => checkpoint.referenceStatus === 'deferred').map(checkpoint => checkpoint.id))
    assert.equal(deferredIDs.length, 20)
    for (const id of deferredIDs) {
      assert.equal(referenceGate(draft, id).mode, 'deferred')
      assert.equal(draft.approval.checkpoints.includes(id), false)
      assert.throws(() => pinManifest(preparation, candidate, detached, { ...receipt, checkpoints: [...receipt.checkpoints, id] }, {}, options),
        /Product deferral|enumerate candidate checkpoint IDs/)
    }
    assert.throws(() => pinManifest(preparation, candidate, detached, receipt, {}, { identityProof }), /dry-run approval cannot pin/)
    const realReceipt = { ...receipt, dryRun: false, evidence: 'Synthetic isolated test receipt' }
    assert.throws(() => pinManifest(preparation, candidate, detached, realReceipt, {}, { identityProof }), /Real, distinct AEON-1670/)
    assert.throws(() => pinManifest(preparation, candidate, detached, { ...receipt, archiveSHA256: '0'.repeat(64) }, {}, options), /exact capture and archive bytes/)
    assert.throws(() => pinManifest(preparation, candidate, detached, { ...receipt, checkpoints: ['V-goals-390-light'] }, {}, options), /enumerate candidate checkpoint IDs/)
    const partialDraft = pinManifest(preparation, candidate, detached, { ...receipt, checkpoints: receipt.checkpoints.slice(1) }, {}, options)
    const partialRefs = verifyReferenceArchive(readFileSync(join(output, candidate.archive.name)), detached, partialDraft).references
    assert.equal(partialRefs.size, 59)
    assert.equal(partialRefs.has(`${receipt.checkpoints[0]}.png`), false)
    assert.equal(preparation.comparison.mode, 'report-only')
    assert.equal(preparation.approval.checkpoints.length, 0)
    assert.equal(manifest.comparison.mode, 'blocking')
    assert.deepEqual(manifest.approval.checkpoints, approvedSet3Checkpoints)
    assert.match(publicationCommands(output, candidate), /gh release create test-references-3.*--prerelease/)
    assert.doesNotMatch(publicationCommands(output, candidate), /--clobber|--latest|release upload/)
    // Independent decoded-pixel proof: counts and boxes must identify exactly
    // the changed pixel, and the locked comparator must reject that change.
    const { PNG } = pngTools(), image = new PNG({ width: 16, height: 16 })
    image.data.fill(255)
    const white = PNG.sync.write(image)
    image.data[0] = 0; image.data[1] = 0; image.data[2] = 0
    const blackCorner = PNG.sync.write(image), change = pixelChanges(white, blackCorner)
    assert.equal(change.pixels, 1)
    assert.equal(change.percent, 100 / 256)
    assert.deepEqual(change.regions, [{ x: 0, y: 0, width: 1, height: 1, pixels: 1 }])
    // Risk: a probe can report false determinism with missing/duplicate
    // repeats or leak diagnostic baseline bytes into reference authority.
    const probeID = 'V-agent-rules-390-dark'
    const probeEnv = { AEON_VISUAL_CAPTURE: '1', GITHUB_EVENT_NAME: 'workflow_dispatch', GITHUB_WORKFLOW: 'CI', GITHUB_JOB: 'visual-reference-capture',
      AEON_VISUAL_PROBE: probeID, AEON_VISUAL_PROBE_SETTING: 'baseline' }
    assert.equal(visualProbe(manifest, {}), null)
    assert.throws(() => visualProbe(manifest, { ...probeEnv, GITHUB_EVENT_NAME: 'pull_request' }), /only by the manual/)
    assert.throws(() => visualProbe(manifest, { ...probeEnv, AEON_VISUAL_CAPTURE: '0' }), /requires manual capture/)
    assert.throws(() => visualProbe(manifest, { ...probeEnv, AEON_VISUAL_PROBE_SETTING: '--arbitrary-flag' }), /Unknown raster probe setting/)
    assert.throws(() => visualProbe(manifest, { AEON_VISUAL_PROBE_SETTING: 'baseline' }), /explicit checkpoint list/)
    for (const input of ['', probeID + ',' + probeID, 'V-not-real', 'x'.repeat(2049)])
      assert.throws(() => probeCheckpoints(input, manifest), /Probe needs/)
    assert.deepEqual(probeCheckpoints(probeID + ',V-account-use-matrix-1440-light', manifest), [probeID, 'V-account-use-matrix-1440-light'])
    assert.equal(PROBE_REPEATS, 6)
    assert.deepEqual(PROBE_SETTINGS.map(setting => setting.args), [[], ['--num-raster-threads=1'], ['--run-all-compositor-stages-before-draw'],
      ['--disable-partial-raster'], ['--num-raster-threads=1', '--run-all-compositor-stages-before-draw', '--disable-partial-raster']])
    const probeVisual = structuredClone(manifest)
    probeVisual.profiles.phone = { width: 16, height: 16 }
    for (const setting of PROBE_SETTINGS) {
      const probeRenderer = { ...renderer, browser: { ...renderer.browser, args: [...renderer.browser.args, ...setting.args] } }
      const probeRecords = Array.from({ length: PROBE_REPEATS }, (_, repeatIndex) => ({ png: white, quality: {
        id: probeID, captureSHA: candidate.captureSHA, captureMode: false, baselineCompared: false, requirementsRun: true,
        quality: { status: 'passed' }, viewport: { width: 16, height: 16 }, pngSHA256: hash(white), renderer: probeRenderer,
        diagnosticProbe: { setting: setting.id, args: probeRenderer.browser.args, repeatIndex },
      } }))
      assert.deepEqual(visualProbe(manifest, { ...probeEnv, AEON_VISUAL_PROBE_SETTING: setting.id }).args, setting.args)
      const summarize = (rows, options = {}) => summarizeProbe(rows, [probeID], setting, probeVisual, options)
      assert.equal(summarize(probeRecords).allByteIdentical, true)
      assert.equal(summarize(probeRecords.slice(1)).complete, false)
      const duplicate = [...probeRecords.slice(0, -1), probeRecords[0]]
      assert.equal(summarize(duplicate).complete, false)
      const changed = probeRecords.map((record, i) => i ? record : { ...record, png: blackCorner, quality: { ...record.quality, pngSHA256: hash(blackCorner) } })
      assert.equal(summarize(changed).complete, true)
      assert.equal(summarize(changed).allByteIdentical, false)
      // PNG encoding bytes alone also fail, with no pixel-tolerance escape.
      const encoded = Buffer.concat([white, Buffer.from('encoding-only')])
      assert.equal(summarize(probeRecords.map((record, i) => i ? record : { ...record, png: encoded, quality: { ...record.quality, pngSHA256: hash(encoded) } })).allByteIdentical, false)
      assert.equal(summarize(probeRecords, { exitCode: 1 }).complete, false)
      assert.equal(summarize(probeRecords, { error: 'timeout' }).complete, false)
      assert.equal(summarize(probeRecords.map((record, i) => i ? record : { ...record, quality: { ...record.quality, captureSHA: 'b'.repeat(40) } })).complete, false)
      const diagnosticCandidates = records.map(record => record.quality.id === probeID ? { ...record, quality: { ...record.quality,
        diagnosticProbe: probeRecords[0].quality.diagnosticProbe } } : record)
      assert.throws(() => packCandidates(diagnosticCandidates, manifest), /Raster probe is diagnostic only/)
    }
    const probeSteps = captureJob.split('\n      - ')
    const normalCaptureSteps = probeSteps.filter(step => /^name: (Capture the same|Download Tick|Prove non-deferred|Rehearse blocking)/.test(step))
    assert.equal(normalCaptureSteps.length, 4)
    assert.ok(normalCaptureSteps.every(step => step.includes("\n        if: inputs.visual_probe == ''\n")))
    const probeStep = probeSteps.find(step => step.includes('visual-probe.mjs web/test-results/raster-probe'))
    assert.ok(probeStep.includes("\n        if: inputs.visual_probe != ''\n"))
    assert.ok(probeStep.includes('VISUAL_PROBE_CHECKPOINTS: ${{ inputs.visual_probe }}'))
    const probeOutputRoot = fileURLToPath(new URL('../test-results/', import.meta.url))
    mkdirSync(probeOutputRoot, { recursive: true })
    const probeOutput = mkdtempSync(join(probeOutputRoot, 'raster-probe-unit-'))
    try {
      const calls: string[] = []
      // A fresh CI checkout has no test-results parent yet. Keep this parent
      // absent so the probe's first write exercises the same startup path.
      const probeRun = join(probeOutput, 'fresh-parent/run')
      const report = await runProbe(probeID, probeRun, probeVisual, {
        platform: 'linux', arch: 'x64',
        env: { ...probeEnv, AEON_VISUAL_CONTAINER_DIGEST: renderer.container.digest },
        run: (command, args, options) => {
          assert.equal(command, 'npm')
          assert.ok(args.includes('--repeat-each=6') && args.includes('--workers=1') && args.includes('--retries=0'))
          assert.ok(args.includes('tests/visual/agent-rules.spec.ts'))
          const setting = PROBE_SETTINGS.find(setting => setting.id === options.env.AEON_VISUAL_PROBE_SETTING)!
          calls.push(setting.id)
          const output = args.find(arg => arg.startsWith('--output=')).slice('--output='.length)
          for (let repeatIndex = 0; repeatIndex < PROBE_REPEATS; repeatIndex++) {
            const target = join(output, String(repeatIndex))
            mkdirSync(target, { recursive: true })
            const probeRenderer = { ...renderer, browser: { ...renderer.browser, args: [...renderer.browser.args, ...setting.args] } }
            writeFileSync(join(target, `${probeID}.png`), white)
            writeFileSync(join(target, `${probeID}.md`), JSON.stringify({ id: probeID, captureSHA: candidate.captureSHA,
              captureMode: false, baselineCompared: false, requirementsRun: true, reference: { mode: 'deferred' },
              quality: { status: 'passed' }, viewport: { width: 16, height: 16 }, pngSHA256: hash(white), renderer: probeRenderer,
              diagnosticProbe: { setting: setting.id, args: probeRenderer.browser.args, repeatIndex } }))
          }
          return { status: 0, stdout: '', stderr: '' }
        },
      })
      assert.deepEqual(calls, PROBE_SETTINGS.map(setting => setting.id))
      assert.equal(report.complete, true)
      assert.ok(report.settings.every(setting => setting.allByteIdentical))
      assert.equal(JSON.parse(readFileSync(join(probeRun, 'probe-report.json'), 'utf8')).kind, 'raster-determinism-probe')
      assert.throws(() => collectCandidates(join(probeRun, 'baseline')), /Raster probe is diagnostic only/)
      await assert.rejects(runProbe(probeID, probeRun, probeVisual, {
        platform: 'linux', arch: 'x64',
        env: { ...probeEnv, AEON_VISUAL_CONTAINER_DIGEST: renderer.container.digest },
        run: () => assert.fail('An existing probe output must never be reused'),
      }), { code: 'EEXIST' })
      assert.deepEqual(calls, PROBE_SETTINGS.map(setting => setting.id))
    } finally { rmSync(probeOutput, { recursive: true, force: true }) }
    const smallRecords = candidate.members.map(member => ({ quality: { id: member.id }, png: white }))
    const baseCapture = { candidate, records: smallRecords, archiveSHA256: candidate.archive.sha256 }
    const reviewedCapture = { ...baseCapture, archiveSHA256: manifest.capture.identityProof.reviewedRunnerArchiveSHA256 }
    const repeatProof = proveCaptureIdentity(baseCapture, baseCapture, reviewedCapture, manifest)
    assert.equal(repeatProof.status, 'identical')
    assert.equal(repeatProof.repeat.byteIdentical, 72)
    assert.equal(repeatProof.runner.pixelIdentical, 72)
    assertPinIdentity(repeatProof, candidate, manifest)
    assert.equal(repeatProof.pinnable.total, 60)
    assert.equal(repeatProof.pinnable.byteIdentical, 60)
    const deferredRecords = smallRecords.map(record => receipt.checkpoints.includes(record.quality.id) ? record : { ...record, png: blackCorner })
    const deferredNoise = proveCaptureIdentity(baseCapture, { ...baseCapture, records: deferredRecords }, reviewedCapture, manifest)
    assert.equal(deferredNoise.repeat.byteIdentical, 60)
    assert.equal(deferredNoise.status, 'identical')
    assertPinIdentity(deferredNoise, candidate, manifest)
    // Round 5: both rounded-outline checkpoints and all Hours widths remain
    // deferred even if runner noise is stable. No re-look exception is active.
    for (const id of ['V-agent-rules-390-dark', 'V-account-use-matrix-1440-light', 'V-hours-1440-light', 'V-hours-1440-dark']) {
      assert.equal(receipt.checkpoints.includes(id), false)
      assert.equal(referenceGate(draft, id).mode, 'deferred')
    }
    assert.deepEqual(manifest.capture.identityProof.relookCheckpoints, [])
    const accountID = 'V-account-use-matrix-1440-light'
    const accountRecords = smallRecords.map(record => record.quality.id === accountID ? { ...record, png: blackCorner } : record)
    const deferredAccount = proveCaptureIdentity(baseCapture, { ...baseCapture, records: accountRecords }, { ...reviewedCapture, records: accountRecords }, manifest)
    assert.equal(deferredAccount.status, 'identical')
    assert.deepEqual(deferredAccount.reviewRequired, [])
    assertPinIdentity(deferredAccount, candidate, manifest)
    const realAttachments = { archive: '11111111-1111-4111-8111-111111111111', manifest: '22222222-2222-4222-8222-222222222222' }
    // Preserve the receipt regression for a deliberately configured exception;
    // the production selection above permits no runner differences at all.
    const relookVisual = structuredClone(preparation), relookID = receipt.checkpoints[0]
    relookVisual.capture.identityProof.relookCheckpoints = [relookID]
    const relookRecords = smallRecords.map(record => record.quality.id === relookID ? { ...record, png: blackCorner } : record)
    const relook = proveCaptureIdentity(baseCapture, baseCapture, { ...reviewedCapture, records: relookRecords }, relookVisual)
    assert.equal(relook.status, 'stable-awaiting-Tick-review')
    assert.deepEqual(relook.reviewRequired, [relookID])
    assertPinIdentity(relook, candidate, relookVisual)
    assert.throws(() => pinManifest(relookVisual, candidate, detached, realReceipt, realAttachments, { identityProof: relook }), /Tick must explicitly re-review/)
    assert.throws(() => pinManifest(relookVisual, candidate, detached, realReceipt, {}, { dryRun: true, identityProof: relook }), /Tick must explicitly re-review/)
    assert.equal(pinManifest(relookVisual, candidate, detached, { ...realReceipt, reReviewedCheckpoints: [relookID] }, realAttachments, { identityProof: relook }).approval.checkpoints.length, 60)
    assert.throws(() => pinManifest(preparation, candidate, detached, { ...realReceipt, checkpoints: receipt.checkpoints.slice(1) }, realAttachments, { identityProof }), /exactly the stable non-deferred/)
    const differentRecords = smallRecords.map((record, index) => index ? record : { ...record, png: blackCorner })
    const unstableProof = proveCaptureIdentity(baseCapture, { ...baseCapture, records: differentRecords }, reviewedCapture, manifest)
    assert.equal(unstableProof.status, 'STOP-before-pinning')
    assert.equal(unstableProof.repeat.byteIdentical, 71)
    assert.equal(unstableProof.repeat.checkpoints[0].pixels, 1)
    assert.deepEqual(unstableProof.repeat.checkpoints[0].regions, change.regions)
    const runnerDifference = proveCaptureIdentity(baseCapture, baseCapture, { ...reviewedCapture, records: differentRecords }, manifest)
    assert.equal(runnerDifference.repeat.byteIdentical, 72)
    assert.equal(runnerDifference.runner.pixelIdentical, 71)
    assert.equal(runnerDifference.status, 'STOP-before-pinning')
    assert.throws(() => assertPinIdentity(runnerDifference, candidate, manifest), /STOP before pinning/)
    assert.throws(() => proveCaptureIdentity(baseCapture, { ...baseCapture, candidate: { ...candidate, captureSHA: 'b'.repeat(40) } }, reviewedCapture, manifest), /SAME commit/)
    assert.throws(() => compareCaptureRecords(smallRecords.slice(1), smallRecords), /exactly 72/)
    const single = { ...draft, approval: { ...draft.approval, checkpoints: [receipt.checkpoints[0]] } }
    const replay = await replayCandidates([{ png: white, quality: { id: receipt.checkpoints[0], captureSHA: candidate.captureSHA } }],
      single, new Map([[`${receipt.checkpoints[0]}.png`, white]]))
    assert.equal(replay.passed, 1)
    assert.equal(replay.negative.changedPixels, 1)
    assert.equal(replay.negative.blocking, 'rejected')
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
  const reporting = reportOnlyManifest()
  const reportingGates = referenceGates(reporting)
  assert.equal(reportingGates.length, 80)
  assert.ok(reportingGates.every(gate => gate.mode === 'report-only' && gate.status === 'awaiting-re-capture'))
  assert.equal(reportingGates.filter(gate => gate.historicalReference).length, 72)
  assert.equal(reportingGates.filter(gate => !gate.historicalReference).length, 8)
  assert.match(referenceReport(reporting).markdown, /awaiting re-capture under the final renderer/)
  const failure = new Error('synthetic differing pixels')
  const run = async () => { throw failure }
  assert.deepEqual(await visualCheckResult(reporting, run), { status: 'failed', error: String(failure) })
  assert.deepEqual(await visualCheckResult(reporting, async () => 'comparison ran'), { status: 'passed', value: 'comparison ran' })
  const blocking = structuredClone(reporting)
  blocking.comparison.mode = 'blocking'
  await assert.rejects(visualCheckResult(blocking, run), error => error === failure)
  assert.deepEqual(await visualCheckResult(blocking, async () => 'comparison ran'), { status: 'passed', value: 'comparison ran' })
  blocking.comparison.mode = 'report-only'
  assert.deepEqual(await visualCheckResult(blocking, run), { status: 'failed', error: String(failure) })
  blocking.comparison.mode = 'blocking'
  assert.throws(() => validateVisual(reporting, { cuts: true, stateIds: ['agents'] }), /Protected visual cuts stay closed/)
  assert.throws(() => validateVisual(blocking, { cuts: true, stateIds: ['agents'] }), /Unpinned visual reference/)
  const revivedHistory = { ...blocking, ...structuredClone(manifest.history.find(row => row.referenceSet === 2)), comparison: blocking.comparison }
  revivedHistory.approval.status = 'historical-only'
  assert.throws(() => validateVisual(revivedHistory, { cuts: true, stateIds: ['agents'] }), /Cuts require Tick approval/)
  assert.equal(manifest.comparison.mode, 'blocking')
  await assert.rejects(visualCheckResult(manifest, run), error => error === failure)
  assert.equal(validateVisual(manifest, { cuts: true, checkpointIds: approvedSet3Checkpoints }), 80)
  for (const state of manifest.states) {
    const unapproved = state.checkpoints.find(checkpoint => !approvedSet3Checkpoints.includes(checkpoint.id))
    if (!unapproved)
      assert.equal(validateVisual(manifest, { cuts: true, stateIds: [state.id] }), 80)
    else
      assert.throws(() => validateVisual(manifest, { cuts: true, stateIds: [state.id] }),
        { message: `Deferred visual reference: ${unapproved.id} (${unapproved.deferredUntil.join(', ')})` })
  }
  const missingMode = structuredClone(manifest)
  delete missingMode.comparison.mode
  assert.throws(() => validateVisual(missingMode), /Missing visual comparison mode/)
  const directory = mkdtempSync(join(tmpdir(), 'aeon-1674-reports-'))
  try {
    const id = 'V-projects-390-light'
    writeFileSync(join(directory, `${id}.md`), JSON.stringify({ id, sourceSpec: 'tests/visual/projects.spec.ts', requirementsRun: true,
      quality: { status: 'failed', error: 'synthetic font guard failure' }, comparison: { status: 'different', error: 'synthetic pixel diff', diffs: [`${id}-diff.png`] } }))
    mkdirSync(join(directory, 'synthetic-guard'))
    writeFileSync(join(directory, 'synthetic-guard', `${id}.md`), JSON.stringify({ id, sourceSpec: 'tests/visual/reference-guard.spec.ts',
      quality: { status: 'passed' }, comparison: { status: 'matched' } }))
    const summary = checkpointReport(directory, reporting)
    assert.equal(summary.split('\n').filter(line => line.startsWith('| V-')).length, 80)
    assert.match(summary, /V-projects-390-light.*failed.*different.*synthetic font guard failure/)
    assert.match(summary, /V-projects-390-light-diff\.png/)
    assert.match(summary, /V-tickets-390-light.*not run in this shard/)
    assert.match(summary, /awaiting re-capture under the final renderer/)
    const activatedSummary = checkpointReport(directory, manifest)
    assert.match(activatedSummary, /comparison.mode=blocking/)
    assert.match(activatedSummary, /V-projects-390-light.*Tick-approved, digest-pinned reference.*failed.*different/)
    assert.match(activatedSummary, /V-hours-390-light.*deferred until AEON-1699/)
    assert.equal(checkpointReport(join(directory, 'absent'), manifest).split('\n').filter(line => line.startsWith('| V-')).length, 80)
  } finally { rmSync(directory, { recursive: true, force: true }) }
})
