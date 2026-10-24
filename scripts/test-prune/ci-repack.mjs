// SPDX-License-Identifier: AGPL-3.0-only
// Read-only packing evidence. This does not change the runner or authorize cuts.
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { fullGateTests, key, shard, tierWeights, webShardPlan } from '../test-tiers/core.mjs'
import { loadManifest, nightlyWeights } from '../../web/scripts/ci-web-shard.mjs'
import { boundedText } from '../test-tiers/inputs.mjs'

const root = fileURLToPath(new URL('../../', import.meta.url))
const hash = value => createHash('sha256').update(JSON.stringify(value)).digest('hex')
const need = (condition, message) => { if (!condition) throw new Error(message) }
const ordinary = rows => rows.filter(row => row.kind === 'browser' && row.executionJob !== 'visual-comparison')

// Use the runner's whole-file packing and weights, charging setup once per bin.
function projection(rows, weights, count, setupSeconds, budgetSeconds, targetSeconds) {
  const files = [...new Set(rows.map(row => row.file))].sort()
  for (const file of files) need(Number.isFinite(weights[file]) && weights[file] > 0, `Missing packing weight: ${file}`)
  const projections = Array.from({ length: count }, (_, i) => {
    const tests = shard(rows, i + 1, count, weights)
    const files = [...new Set(tests.map(row => row.file))].sort()
      .map(file => ({ file, cases: tests.filter(row => row.file === file).length, workSeconds: weights[file] }))
    const workSeconds = files.reduce((sum, file) => sum + file.workSeconds, 0)
    return { shard: i + 1, cases: tests.length, workSeconds, seconds: setupSeconds + workSeconds, files }
  })
  const totalWorkSeconds = files.reduce((sum, file) => sum + weights[file], 0)
  const perfectBalanceLowerBoundSeconds = setupSeconds + totalWorkSeconds / count
  const indivisibleFileLowerBoundSeconds = setupSeconds + Math.max(0, ...files.map(file => weights[file]))
  const projectedMaxSeconds = Math.max(...projections.map(bin => bin.seconds))
  need(projections.reduce((sum, bin) => sum + bin.cases, 0) === rows.length, 'Packing lost catalogue cases')
  return { shards: count, cases: rows.length, files: files.length, setupSecondsPerShard: setupSeconds,
    workRunnerMinutes: totalWorkSeconds / 60, setupRunnerMinutes: count * setupSeconds / 60,
    projectedRunnerMinutes: (totalWorkSeconds + count * setupSeconds) / 60,
    perfectBalanceLowerBoundSeconds, indivisibleFileLowerBoundSeconds,
    lowerBoundSeconds: Math.max(perfectBalanceLowerBoundSeconds, indivisibleFileLowerBoundSeconds),
    projectedMaxSeconds, budgetSeconds, targetSeconds, fitsBudget: projectedMaxSeconds <= budgetSeconds,
    fitsTarget: projectedMaxSeconds <= targetSeconds, projections }
}

export function repackEvidence(tests, policy, { sourceSHA, mainSHA = sourceSHA, sourceTree } = {}) {
  need(Array.isArray(tests) && tests.length > 0 && tests.length <= 20_000 &&
    [sourceSHA, mainSHA].every(sha => /^[a-f0-9]{40}$/.test(sha ?? '')), 'Unbound repack catalogue')
  const browser = tests.filter(row => row.kind === 'browser')
  need(new Set(browser.map(key)).size === browser.length && browser.every(row =>
    typeof row.name === 'string' && /^tests\/[a-zA-Z0-9_./-]+\.spec\.ts$/.test(row.file) &&
    ['ESSENTIAL', 'GATED-FULL', 'NIGHTLY'].includes(row.tier)), 'Invalid repack catalogue')
  const gate = fullGateTests(tests).filter(row => row.kind === 'browser')
  const fullRows = ordinary(gate), nightlyRows = ordinary(browser)
  const current = webShardPlan(fullRows, policy, { mode: 'full' })
  const fullWeights = tierWeights(policy, fullRows)
  const currentFull = projection(fullRows, fullWeights, current.shards.length, current.overheadSeconds, current.budgetSeconds, 860)
  // P12's proposed 300s setup/collection allowance is evidence only. The live
  // planner still owns its existing count, overhead, timeouts and headroom.
  const eightFull = projection(fullRows, fullWeights, 8, 300, 900, 860)
  const eightNightly = projection(nightlyRows, nightlyWeights(policy, nightlyRows), 8, 300, 960, 840)
  const fitsPackingTargets = eightFull.fitsTarget && eightNightly.fitsTarget
  const visuals = browser.filter(row => row.file.startsWith('tests/visual/'))
  return { version: 1, ticket: 'AEON-1730', sourceSHA, mainSHA, sourceTree,
    status: fitsPackingTargets ? 'hosted-qualification-pending' : 'blocked-eight-shard-budget',
    policySHA256: hash(policy),
    catalogueSHA256: hash(browser.map(row => [row.file, row.config, row.project, row.name, row.occurrence, row.tier, row.executionJob]).sort()),
    caseCounts: { browser: browser.length, fullGate: gate.length, ordinaryFullGate: fullRows.length,
      ordinaryNightly: nightlyRows.length, removedByThisReport: 0 },
    visualIsolation: { registrations: visuals.length,
      assignedToDedicatedJob: visuals.filter(row => row.executionJob === 'visual-comparison').length,
      complete: visuals.every(row => row.executionJob === 'visual-comparison') },
    packing: { currentFull, eightFull, eightNightly, fitsPackingTargets,
      note: 'The same retained catalogue is priced with existing weights, not new runner measurements. Dedicated visual work is excluded by execution owner and is not a saving. Eight-shard projections assume 300s setup/collection per shard; the current full planner remains unchanged. Nightly uses nightly weights; unmeasured files retain the runner fallback.' },
    activationAuthorizedByThisReport: false }
}

// A report always describes this committed tree, never an imported P12/Wave B
// snapshot or a fingerprint of main paired with another tree's catalogue.
export function sourceProvenance(checkout = root) {
  const git = args => execFileSync('git', args, { cwd: checkout, encoding: 'utf8', timeout: 10_000,
    maxBuffer: 4 * 1024 * 1024, stdio: ['ignore', 'pipe', 'pipe'] }).trim()
  const sourceSHA = git(['rev-parse', 'HEAD']), mainSHA = git(['rev-parse', 'origin/main'])
  try { git(['merge-base', '--is-ancestor', mainSHA, sourceSHA]) }
  catch { throw new Error('Merge newest origin/main before collecting packing evidence') }
  try { git(['diff', '--quiet', 'HEAD', '--', '.']) }
  catch { throw new Error('Commit implementation changes before collecting packing evidence') }
  need(!git(['ls-files', '--others', '--exclude-standard']), 'Commit untracked inputs before collecting packing evidence')
  return { sourceSHA, mainSHA, sourceTree: git(['rev-parse', 'HEAD^{tree}']) }
}

export function main(args = process.argv.slice(2)) {
  need(args.length <= 2 && (args.length === 0 || ['report', '--report', '--check'].includes(args[0])) &&
    (args.length < 2 || args[0] === '--check'), 'Usage: ci-repack.mjs [report|--report|--check [report.json]]')
  const provenance = sourceProvenance()
  const manifest = JSON.parse(boundedText(root, 'scripts/ci/web-test-tiers.json', 4 * 1024 * 1024))
  const report = repackEvidence(manifest.tests, loadManifest(), provenance)
  if (args[0] === '--check' && args[1]) {
    const previous = JSON.parse(boundedText(root, args[1], 4 * 1024 * 1024))
    need(JSON.stringify(previous) === JSON.stringify(report), 'Packing evidence drift; regenerate on the current committed catalogue')
  }
  console.log(JSON.stringify(args[0] === '--check' ? { checked: true, ...provenance,
    caseCounts: report.caseCounts, status: report.status, activationAuthorizedByThisReport: false } : report, null, 2))
  return 0
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = main() } catch (error) { console.error(error.message); process.exitCode = 1 }
}
