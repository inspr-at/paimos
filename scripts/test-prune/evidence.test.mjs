// SPDX-License-Identifier: AGPL-3.0-only
// Risks: missing/stale checkpoint pixels, unbounded artifacts, lost packing
// work, misleading lower bounds and evidence from an uncommitted source tree.
import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fullWebShardCount, webShardPlan } from '../test-tiers/core.mjs'
import { repackEvidence, sourceProvenance } from './ci-repack.mjs'
import { collectVisualEvidence, writeVisualEvidence, visualEvidenceBounds } from './visual-evidence.mjs'

const sha = 'a'.repeat(40)
const visual = JSON.parse(readFileSync(new URL('./visual-manifest.json', import.meta.url), 'utf8'))
const digest = bytes => createHash('sha256').update(bytes).digest('hex')
const row = (file, tier = 'GATED-FULL') => ({ kind: 'browser', file, name: file, tier, config: 'playwright.ui.config.ts', project: '' })
const policyFor = (rows, seconds = 20) => ({ version: 1, groups: [{ id: 'ui',
  config: 'playwright.ui.config.ts', project: null, flags: [], env: {}, hostedOnly: false,
  specs: [...new Set(rows.map(row => row.file))].map(file => ({ file, weightSeconds: seconds })) }],
  nightlyTiming: { files: Object.fromEntries(rows.map(row => [row.file, { seconds, selectedTests: 1 }])) } })

test('AEON-1730 packing reports the unchanged runner and eight-shard lower bounds without losing work', () => {
  const rows = Array.from({ length: 32 }, (_, i) => row(`tests/repack-${i}.spec.ts`))
  const policy = policyFor(rows, 151)
  const evidence = repackEvidence(rows, policy, { sourceSHA: sha, mainSHA: 'b'.repeat(40) })
  const current = webShardPlan(rows, policy, { mode: 'full' })
  assert.equal(evidence.packing.currentFull.shards, fullWebShardCount)
  assert.deepEqual(evidence.packing.currentFull.projections.map(bin => bin.seconds), current.projections.map(bin => bin.seconds))
  assert.equal(evidence.status, 'blocked-eight-shard-budget')
  assert.equal(evidence.packing.fitsPackingTargets, false)
  assert.equal(evidence.activationAuthorizedByThisReport, false)
  assert.equal(evidence.caseCounts.removedByThisReport, 0)
  assert.equal(evidence.sourceSHA, sha); assert.equal(evidence.mainSHA, 'b'.repeat(40))
  assert.equal(evidence.packing.eightFull.perfectBalanceLowerBoundSeconds, 904)
  assert.equal(evidence.packing.eightFull.fitsBudget, false)
  assert.equal(evidence.packing.eightNightly.budgetSeconds, 960)
  assert.equal(evidence.packing.eightNightly.fitsTarget, false)
  for (const plan of [evidence.packing.currentFull, evidence.packing.eightFull, evidence.packing.eightNightly]) {
    assert.equal(plan.cases, rows.length)
    assert.equal(plan.projections.reduce((sum, bin) => sum + bin.cases, 0), rows.length)
    assert.deepEqual(plan.projections.flatMap(bin => bin.files.map(file => file.file)).sort(), rows.map(row => row.file).sort())
    assert.equal(plan.workRunnerMinutes, 32 * 151 / 60)
    for (const bin of plan.projections) assert.equal(bin.seconds - bin.workSeconds, plan.setupSecondsPerShard)
  }
  const cheap = repackEvidence(rows.slice(0, 1), policyFor(rows), { sourceSHA: sha })
  assert.equal(cheap.status, 'hosted-qualification-pending')
  assert.equal(cheap.activationAuthorizedByThisReport, false)
  assert.ok(cheap.packing.eightFull.projections.filter(bin => bin.cases === 0).every(bin => bin.seconds === 300))
  const heavy = repackEvidence(rows.slice(0, 1), policyFor(rows, 700), { sourceSHA: sha })
  assert.equal(heavy.packing.eightFull.indivisibleFileLowerBoundSeconds, 1000)
  assert.equal(heavy.packing.eightFull.lowerBoundSeconds, 1000)
  assert.throws(() => repackEvidence(rows, policy), /Unbound repack catalogue/)
  assert.throws(() => repackEvidence([...rows, rows[0]], policy, { sourceSHA: sha }), /Invalid repack catalogue/)
  assert.throws(() => repackEvidence(rows, policyFor(rows.slice(1)), { sourceSHA: sha }), /Missing web shard weight/)
})

test('AEON-1730 nightly prices the full retained catalogue and isolates visual work only by execution owner', () => {
  const rows = [row('tests/repack.spec.ts'), { ...row('tests/repack.spec.ts', 'NIGHTLY'), name: 'nightly case' },
    row('tests/visual/unassigned.spec.ts'),
    { ...row('tests/visual/assigned.spec.ts'), executionJob: 'visual-comparison' }]
  const policy = policyFor(rows, 20)
  policy.groups[0].specs[0].tierTiming = { seconds: 10, selectedTests: 1 }
  const evidence = repackEvidence(rows, policy, { sourceSHA: sha })
  assert.equal(evidence.visualIsolation.complete, false)
  assert.equal(evidence.visualIsolation.assignedToDedicatedJob, 1)
  assert.equal(evidence.caseCounts.ordinaryFullGate, 2)
  assert.equal(evidence.caseCounts.ordinaryNightly, 3)
  assert.equal(evidence.packing.eightFull.workRunnerMinutes, 30 / 60)
  assert.equal(evidence.packing.eightNightly.workRunnerMinutes, 60 / 60)
  const files = evidence.packing.eightNightly.projections.flatMap(bin => bin.files)
  assert.deepEqual(files.find(file => file.file === rows[0].file), { file: rows[0].file, cases: 2, workSeconds: 40 })
  assert.ok(files.some(file => file.file === 'tests/visual/unassigned.spec.ts'))
  assert.ok(!files.some(file => file.file === 'tests/visual/assigned.spec.ts'))
})

test('AEON-1730 provenance requires the current committed tree and newest main ancestry', t => {
  const checkout = mkdtempSync(join(tmpdir(), 'aeon-1730-provenance-'))
  t.after(() => rmSync(checkout, { recursive: true, force: true }))
  const git = args => execFileSync('git', args, { cwd: checkout, encoding: 'utf8', timeout: 10_000,
    env: { ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_NOSYSTEM: '1',
      GIT_AUTHOR_NAME: 'Fixture', GIT_AUTHOR_EMAIL: 'fixture@example.invalid',
      GIT_COMMITTER_NAME: 'Fixture', GIT_COMMITTER_EMAIL: 'fixture@example.invalid' } }).trim()
  git(['init', '-q']); git(['config', 'commit.gpgsign', 'false'])
  const path = join(checkout, 'input.json')
  writeFileSync(path, '{}\n'); git(['add', '.']); git(['commit', '-qm', 'base'])
  git(['update-ref', 'refs/remotes/origin/main', 'HEAD'])
  const first = sourceProvenance(checkout)
  assert.equal(first.sourceSHA, first.mainSHA)
  assert.equal(first.sourceTree, git(['rev-parse', 'HEAD^{tree}']))
  writeFileSync(path, '{"changed":true}\n')
  assert.throws(() => sourceProvenance(checkout), /Commit implementation changes/)
  git(['add', '.'])
  assert.throws(() => sourceProvenance(checkout), /Commit implementation changes/)
  git(['commit', '-qm', 'changed'])
  assert.notEqual(sourceProvenance(checkout).sourceSHA, first.sourceSHA)
  writeFileSync(join(checkout, 'untracked.json'), '{}\n')
  assert.throws(() => sourceProvenance(checkout), /Commit untracked inputs/)
  git(['add', '.']); git(['commit', '-qm', 'new input'])
  git(['update-ref', 'refs/remotes/origin/main', 'HEAD'])
  git(['checkout', '-qb', 'divergent', first.sourceSHA])
  assert.throws(() => sourceProvenance(checkout), /Merge newest origin\/main/)
})

function visualFixture(directory) {
  for (const state of visual.states) for (const checkpoint of state.checkpoints) {
    const path = join(directory, checkpoint.id)
    mkdirSync(path)
    const png = Buffer.alloc(25)
    Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]).copy(png)
    png.writeUInt32BE(visual.profiles[checkpoint.profile].width, 16)
    png.writeUInt32BE(visual.profiles[checkpoint.profile].height, 20)
    writeFileSync(join(path, `${checkpoint.id}.png`), png)
    writeFileSync(join(path, `${checkpoint.id}.md`), JSON.stringify({ id: checkpoint.id, sourceSpec: state.owner, captureSHA: sha,
      pngSHA256: digest(png), quality: { status: 'passed' }, comparison: { status: 'matched' } }))
  }
}

test('AEON-1730 review artifacts retain the 80 passing checkpoint PNGs and exclude other screenshots', t => {
  assert.equal(visualEvidenceBounds.totalBytes, 64 * 1024 * 1024)
  const root = mkdtempSync(join(tmpdir(), 'aeon-1684-visual-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const input = join(root, 'input'), output = join(root, 'output')
  mkdirSync(input); visualFixture(input)
  for (const name of ['screenshot.png', 'failed-V-unknown.png', 'V-unknown.png', 'V-projects-390-light-diff.png']) writeFileSync(join(input, name), 'unrelated')
  const guard = join(input, 'guard'); mkdirSync(guard)
  const id = visual.states[0].checkpoints[0].id
  writeFileSync(join(guard, `${id}.md`), JSON.stringify({ id, sourceSpec: 'tests/visual-checkpoint-guard.spec.ts', captureSHA: sha, pngSHA256: '0'.repeat(64) }))
  const result = writeVisualEvidence(input, output, visual, { sha })
  assert.equal(result.checkpoints, 80); assert.deepEqual(result.missing, [])
  assert.equal(readdirSync(output).length, 80)
  assert.ok(readdirSync(output).every(name => /^V-[a-z0-9-]+\.png$/.test(name)))
  assert.deepEqual(readFileSync(join(output, `${id}.png`)), readFileSync(join(input, id, `${id}.png`)))
  assert.throws(() => writeVisualEvidence(input, output, visual, { sha }), /EEXIST/, 'an old artifact cannot fill missing evidence')
})

test('AEON-1730 missing, stale, altered, duplicate or oversized visual captures cannot look complete', t => {
  const root = mkdtempSync(join(tmpdir(), 'aeon-1684-visual-refusals-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const input = join(root, 'input'); mkdirSync(input); visualFixture(input)
  const id = visual.states[0].checkpoints[0].id, report = join(input, id, `${id}.md`), png = join(input, id, `${id}.png`)
  const original = readFileSync(report), data = JSON.parse(original.toString())
  const collect = (bounds = visualEvidenceBounds) => collectVisualEvidence(input, visual, { sha, bounds })
  writeFileSync(report, JSON.stringify({ ...data, captureSHA: 'd'.repeat(40) }))
  assert.throws(collect, /Stale visual checkpoint/)
  writeFileSync(report, JSON.stringify({ ...data, pngSHA256: '0'.repeat(64) }))
  assert.throws(collect, /digest mismatch/)
  writeFileSync(report, JSON.stringify({ ...data, pngSHA256: null }))
  assert.deepEqual(collect().missing, [id], 'a failed setup cannot borrow an earlier capture')
  const failedName = `failed-${id}.png`, failedPNG = readFileSync(png)
  failedPNG[24] = 1
  writeFileSync(join(input, id, failedName), failedPNG)
  writeFileSync(report, JSON.stringify({ ...data, pngSHA256: null, quality: { status: 'failed' },
    failurePNG: { name: failedName, sha256: digest(failedPNG) } }))
  assert.deepEqual(collect().missing, [], 'a failed prerequisite retains its own fresh pixels, with failure provenance')
  assert.deepEqual(collect().captures.get(id).png, failedPNG)
  writeFileSync(report, JSON.stringify({ ...data, pngSHA256: null, quality: { status: 'passed' },
    failurePNG: { name: failedName, sha256: digest(failedPNG) } }))
  assert.throws(collect, /Invalid failed visual capture/, 'failure pixels must not masquerade as a passed checkpoint')
  writeFileSync(report, original)
  const retry = join(input, 'launch-retry'); mkdirSync(retry)
  writeFileSync(join(retry, `${id}.md`), JSON.stringify({ ...data, pngSHA256: digest(failedPNG) }))
  writeFileSync(join(retry, `${id}.png`), failedPNG)
  assert.deepEqual(collect().captures.get(id).png, readFileSync(png), 'a retry cannot replace the first attempt in review evidence')
  rmSync(retry, { recursive: true })
  assert.throws(() => collect({ ...visualEvidenceBounds, entries: 1 }), /entry bound/)
  assert.throws(() => collect({ ...visualEvidenceBounds, pngBytes: 24 }), /bounded regular file/)
  assert.throws(() => collect({ ...visualEvidenceBounds, totalBytes: 1 }), /read byte bound/)
  const duplicate = join(input, 'duplicate'); mkdirSync(duplicate)
  writeFileSync(join(duplicate, `${id}.md`), original); writeFileSync(join(duplicate, `${id}.png`), readFileSync(png))
  assert.throws(collect, /Duplicate visual checkpoint attempt/)
  rmSync(duplicate, { recursive: true })
  symlinkSync(png, join(input, 'link.png'))
  assert.throws(collect, /must not follow symlinks/)
})
