// SPDX-License-Identifier: AGPL-3.0-only
// Local Git reads only. This is research, never an Actions planner entry point.
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { readFileSync, mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { candidateBounds, candidateGraph, candidateSelection, candidateID } from './browser-candidate.mjs'
import { key } from './core.mjs'
import { boundedText } from './inputs.mjs'

const root = fileURLToPath(new URL('../../', import.meta.url))
export const corpusPin = 'd960c3419d4522a04f90c4b61ffa12eb96fc09d3b44413826c44410e57c51d59'
const proofConfidence = new Set(['source-supported', 'bisected', 'ticket-confirmed', 'fix-diff-confirmed', 'recorded-failure'])
const rejectedConfidence = new Set(['unresolved', 'inherited-source-attribution'])
const oid = /^[a-f0-9]{40}$/
const digest = text => createHash('sha256').update(text).digest('hex')
const stable = value => JSON.stringify(value, null, 2) + '\n'
const countBy = (rows, field) => Object.fromEntries([...new Set(rows.flatMap(field))].sort().map(value =>
  [value, rows.filter(row => field(row).includes(value)).length]))
const s1Fallback = row => {
  const reason = row.s1.reasons.find(reason => !reason.startsWith('documentation allowlist:')) ?? 'unclassified'
  if (reason.startsWith('unknown or shared browser input:')) return 'shared-or-unknown-input'
  if (reason.includes('catalogue membership changed')) return 'registry-membership-change'
  if (reason.startsWith('new, renamed, deleted or unmapped input:')) return 'unmapped-input'
  if (reason.startsWith('complete browser-impact map')) return 'missing-runtime-map'
  if (reason.startsWith('affected browser fan-out')) return 'fan-out-over-budget'
  return reason
}

export const causeRules = Object.freeze({
  'shared-components': file => /^web\/src\/components\/[^/]+\.vue$/.test(file),
  'feature-components': file => /^web\/src\/components\/[^/]+\//.test(file),
  'stores': file => file.startsWith('web/src/stores/'),
  'css-tokens': file => /^web\/src\/.*\.css$/.test(file),
  'router-entry': file => ['web/src/router.ts', 'web/src/main.ts', 'web/src/App.vue', 'web/index.html'].includes(file),
  'shared-libraries': file => file.startsWith('web/src/lib/'),
  'fixtures-helpers': file => /^web\/(?:tests|e2e)\//.test(file) && /(?:fixture|helper|harness)/i.test(file),
  'spec-edits': file => /^web\/(?:tests|e2e)\/.+\.spec\.ts$/.test(file),
  'backend-contract': file => /^(?:api|internal|cmd)\//.test(file),
  'registries-ci-rules': file => /^(?:scripts\/|\.github\/|AGENTS\.md$|web\/ci-web-shards\.json$|web\/scripts\/)/.test(file),
})

// Preflight ls-tree sizes before reading any blob; only the named source and
// catalogue paths are admitted, never environment or credential files. Missing
// history fails the replay, rather than manufacturing a zero-miss result.
export function historicalSnapshots(checkout = root, { exec = execFileSync } = {}) {
  const blobs = new Map(), snapshots = new Map()
  let cacheBytes = 0
  const git = (args, options = {}) => exec('git', args, { cwd: checkout, timeout: 30_000,
    maxBuffer: 4 * 1024 * 1024, stdio: 'pipe', env: { ...process.env, GIT_NO_LAZY_FETCH: '1', GIT_OPTIONAL_LOCKS: '0' }, ...options })
  return sha => {
    if (!oid.test(sha ?? '')) throw new Error('historical snapshot requires an immutable SHA')
    if (snapshots.has(sha)) return snapshots.get(sha)
    const listing = git(['ls-tree', '-r', '-l', '-z', sha, '--', 'web/src', 'web/tests', 'web/e2e', 'scripts/ci/web-test-tiers.json']).toString()
    if (!listing.endsWith('\0')) throw new Error(`missing or truncated historical tree: ${sha}`)
    const lines = listing.split('\0').filter(Boolean)
    if (lines.length > candidateBounds.files) throw new Error('historical tree entry count bound')
    const entries = [], seen = new Set()
    let total = 0
    for (const line of lines) {
      const match = /^(100(?:644|755)) blob ([a-f0-9]{40})\s+(\d+)\t(.+)$/.exec(line)
      const file = line.slice(line.indexOf('\t') + 1)
      if (file !== 'scripts/ci/web-test-tiers.json' && !/^web\/(?:src|tests|e2e)\/.+\.(?:ts|js|mjs|vue|json|css|html)$/.test(file)) continue
      if (!match || seen.has(file)) throw new Error('non-regular or duplicate historical input')
      const size = Number(match[3]), limit = file.startsWith('scripts/') ? 4 * 1024 * 1024 : candidateBounds.fileBytes
      if (!Number.isSafeInteger(size) || size >= limit) throw new Error('historical per-file byte bound')
      total += size
      if (total >= candidateBounds.totalBytes) throw new Error('historical total byte bound')
      seen.add(file); entries.push({ id: match[2], file, size })
    }
    const missing = [...new Map(entries.filter(entry => !blobs.has(entry.id)).map(entry => [entry.id, entry])).values()]
    if (missing.length) {
      const batch = git(['cat-file', '--batch'], { input: missing.map(entry => entry.id).join('\n') + '\n', maxBuffer: candidateBounds.totalBytes + 4 * 1024 * 1024 })
      let at = 0
      for (const entry of missing) {
        const nl = batch.indexOf(10, at)
        if (nl < 0 || batch.subarray(at, nl).toString() !== `${entry.id} blob ${entry.size}` || batch[nl + entry.size + 1] !== 10)
          throw new Error('missing or truncated historical blob')
        const source = new TextDecoder('utf-8', { fatal: true }).decode(batch.subarray(nl + 1, nl + 1 + entry.size))
        blobs.set(entry.id, source); cacheBytes += entry.size
        at = nl + entry.size + 2
      }
      if (at !== batch.length) throw new Error('unexpected historical batch output')
    }
    const files = new Map(entries.map(entry => [entry.file, blobs.get(entry.id)]))
    // Some causing bases predate tier manifests. Their graph is available,
    // but an absent registration must remain visible in the evidence.
    const registry = files.has('scripts/ci/web-test-tiers.json') ? JSON.parse(files.get('scripts/ci/web-test-tiers.json')) : { tests: [] }
    if (!Array.isArray(registry.tests) || registry.tests.length > candidateBounds.cases) throw new Error('invalid historical catalogue')
    const sources = new Map([...files].filter(([file]) => file.startsWith('web/')).map(([file, text]) => [file.slice(4), text]))
    const result = { graph: candidateGraph(sources), tests: registry.tests }
    snapshots.set(sha, result)
    while (snapshots.size > 2) snapshots.delete(snapshots.keys().next().value)
    while (cacheBytes > candidateBounds.totalBytes) {
      const id = blobs.keys().next().value
      cacheBytes -= Buffer.byteLength(blobs.get(id)); blobs.delete(id)
    }
    return result
  }
}

export function replayCandidate(text, policy, snapshot) {
  if (typeof text !== 'string' || Buffer.byteLength(text) > 16 * 1024 * 1024 || digest(text) !== corpusPin || policy.corpus_sha256 !== corpusPin)
    throw new Error('research corpus pin mismatch')
  const fixture = JSON.parse(text), all = fixture.historicalRegressions
  if (!Array.isArray(all) || all.length > 20_000) throw new Error('invalid historical rows')
  const browser = all.filter(row => row.observed?.kind === 'browser')
  if (browser.some(row => !proofConfidence.has(row.causalConfidence) && !rejectedConfidence.has(row.causalConfidence))) throw new Error('unknown causal confidence')
  const proof = browser.filter(row => proofConfidence.has(row.causalConfidence))
  const cases = [], changes = new Map(), seen = new Set(), plans = new Map(), graphs = new Map()
  for (const row of proof) {
    // The pinned legacy PR-REPLAY-1 row records its tested tree only in
    // treeBeforeFix. Preserve that limitation rather than rewriting the pin.
    const testedIdentity = row.observed.testedSHA === row.treeBeforeFix || row.id === 'PR-REPLAY-1' &&
      row.causalConfidence === 'recorded-failure' && row.observed.testedSHA === undefined
    if (seen.has(row.id) || !oid.test(row.treeBeforeFix ?? '') || !testedIdentity ||
        !oid.test(row.causingChange?.base ?? '') || !oid.test(row.causingChange?.commit ?? '') ||
        row.replay?.trustedGraphSHA !== row.causingChange.base || typeof row.replay?.s1?.selected !== 'boolean')
      throw new Error(`invalid or duplicate historical identity: ${row.id}`)
    seen.add(row.id)
    const change = row.causingChange
    if (changes.has(change.commit) && stable(changes.get(change.commit)) !== stable(change)) throw new Error('inconsistent historical diff')
    changes.set(change.commit, change)
    const planKey = JSON.stringify([change.base, row.treeBeforeFix, change.paths])
    let plan = plans.get(planKey)
    if (!plan) {
      const base = snapshot(change.base), tested = snapshot(row.treeBeforeFix)
      const selection = candidateSelection({ paths: change.paths, tests: tested.tests, graph: base.graph, impact: 'on', experiment: true })
      const issues = [...(base.graph.issues ?? [])]
      graphs.set(change.base, { sha: change.base, complete: base.graph.complete, issueFiles: issues.length,
        computedImportFiles: issues.filter(([, reasons]) => reasons.has('computed module ownership')).length,
        unresolvedLocalFiles: issues.filter(([, reasons]) => [...reasons].some(reason => reason.startsWith('unresolved local reference:'))).length,
        samples: issues.slice(0, 3).map(([file, reasons]) => ({ file, reasons: [...reasons].slice(0, 2) })) })
      plan = { ...selection, selectedKeys: new Set(selection.selected.map(key)), tested,
        baseKeys: new Set(base.tests.map(key)), baseSpecs: new Set(base.tests.filter(item => item.kind === 'browser').map(item => item.file)) }
      plans.set(planKey, plan)
    }
    const observedFile = row.observed.file.replace(/^web\//, '')
    const registered = plan.tested.tests.find(item => key(item) === row.expectedFailureKey)
    if (!registered) throw new Error(`failure identity absent from tested catalogue: ${row.id}`)
    const categories = Object.entries(causeRules).filter(([, matches]) => change.paths.some(matches)).map(([id]) => id)
    const mappingSelected = plan.mappedSpecs.includes(observedFile) || registered.tier === 'ESSENTIAL'
    cases.push({ id: row.id, commit: change.commit, testedSHA: row.treeBeforeFix, observedTestedSHARecorded: row.observed.testedSHA !== undefined, spec: row.observed.file,
      failureKey: row.expectedFailureKey, causalConfidence: row.causalConfidence, classification: row.classification,
      tier: registered.tier, registeredAtBase: plan.baseKeys.has(row.expectedFailureKey), specAtBase: plan.baseSpecs.has(observedFile),
      directlyChangedSpec: change.paths.includes(row.observed.file), categories,
      areas: policy.areas.filter(area => change.paths.some(file => area.prefixes.some(prefix => file.startsWith(prefix)))).map(area => area.id),
      s1: { scope: row.replay.s1.scope, selected: row.replay.s1.selected, reasons: row.replay.s1.reasons },
      candidate: { scope: plan.scope, selected: plan.selectedKeys.has(row.expectedFailureKey), mappingSelected,
        baselinePlusMappingSelected: row.replay.s1.selected || mappingSelected,
        baselineSelectedCases: row.replay.s1.count,
        selectedCases: plan.selected.length, catalogueCases: plan.tested.tests.filter(item => item.kind === 'browser' && item.active !== false).length,
        mappedSpecCount: plan.mappedSpecs.length, sharedInputs: plan.sharedInputs, reasons: plan.reasons } })
  }
  const summary = rows => ({ cases: rows.length, beforeMisses: rows.filter(row => !row.s1.selected).length,
    ownershipMisses: rows.filter(row => !row.candidate.mappingSelected).length,
    baselinePlusOwnershipMisses: rows.filter(row => !row.candidate.baselinePlusMappingSelected).length,
    afterMisses: rows.filter(row => !row.candidate.selected).length,
    affectedCases: rows.filter(row => row.candidate.scope === 'affected').length,
    catalogueFallbackCases: rows.filter(row => row.candidate.scope === 'catalogue').length,
    qualified: rows.length > 0 && rows.every(row => row.candidate.scope === 'affected' && row.candidate.selected) })
  const misses = cases.filter(row => !row.s1.selected)
  return { version: 1, selector: candidateID, corpus_sha256: corpusPin, activation: 'disabled; offline research only',
    proofCases: cases.length, rejectedBrowserRows: browser.length - proof.length,
    uniqueFailureIdentities: new Set(cases.map(row => row.failureKey)).size,
    causingCommits: changes.size, trustedBases: new Set(proof.map(row => row.causingChange.base)).size,
    testedTrees: new Set(proof.map(row => row.treeBeforeFix)).size,
    overall: summary(cases), areas: Object.fromEntries(policy.areas.map(area => [area.id, summary(cases.filter(row => row.areas.includes(area.id)))])),
    missCategories: countBy(misses, row => row.categories),
    missClassifications: countBy(misses, row => [row.classification]),
    missS1Reasons: countBy(misses, row => row.s1.reasons),
    missS1Fallbacks: countBy(misses, row => [s1Fallback(row)]),
    missTiers: countBy(misses, row => [row.tier]),
    missesWithChangedSpec: misses.filter(row => row.directlyChangedSpec).length,
    missesWithoutRegisteredCaseAtBase: misses.filter(row => !row.registeredAtBase).length,
    missesWithoutSpecAtBase: misses.filter(row => !row.specAtBase).length,
    fallbackReasons: countBy(cases, row => row.candidate.reasons),
    graphs: [...graphs.values()].sort((a, b) => a.sha.localeCompare(b.sha)),
    changes: [...changes.values()].sort((a, b) => a.commit.localeCompare(b.commit)), cases: cases.sort((a, b) => a.id.localeCompare(b.id)) }
}

export function main(args = process.argv.slice(2)) {
  if (args.length !== 1 || !['--write', '--check'].includes(args[0])) throw new Error('Usage: node scripts/test-tiers/browser-candidate-replay.mjs --write|--check')
  const corpus = boundedText(root, 'scripts/test-tiers/affected-replay.json', 16 * 1024 * 1024)
  const policy = JSON.parse(boundedText(root, 'scripts/ci/browser-area-rollout.json', 1024 * 1024))
  const report = replayCandidate(corpus, policy, historicalSnapshots())
  report.implementation_sha256 = Object.fromEntries(['browser-candidate.mjs', 'browser-candidate-replay.mjs', 'core.mjs'].map(file =>
    [file, digest(boundedText(root, `scripts/test-tiers/${file}`))]))
  const directory = resolve(root, 'research/ci-selector'), output = resolve(directory, 'measurement.json')
  if (args[0] === '--write') { mkdirSync(directory, { recursive: true }); writeFileSync(output, stable(report)) }
  else if (readFileSync(output, 'utf8') !== stable(report)) throw new Error('selector research measurement drift; rerun --write and inspect')
  console.log(stable({ corpus_sha256: report.corpus_sha256, overall: report.overall, areas: report.areas }))
  return 0
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = main() } catch (error) { console.error(error.message); process.exitCode = 1 }
}
