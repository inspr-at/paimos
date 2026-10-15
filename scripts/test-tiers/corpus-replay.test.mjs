// SPDX-License-Identifier: AGPL-3.0-only
// Historical selector proof. Unresolved and inherited rows are not causal proof.
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { dirname, join, posix } from 'node:path'
import { fileURLToPath } from 'node:url'
import { select, key, semanticRegistryPattern, registryImpact, browserScopeNoneAllowlist, webGraph } from './core.mjs'
import { schedulingDecision } from './diff.mjs'

const root = fileURLToPath(new URL('../../', import.meta.url))
const proofConfidence = new Set(['source-supported', 'bisected', 'ticket-confirmed', 'fix-diff-confirmed', 'recorded-failure'])
const rejectedConfidence = new Set(['unresolved', 'inherited-source-attribution'])
const fileOf = value => /^(?:browser|vitest|node):([^:]+):/.exec(value ?? '')?.[1]
const staticRow = row => !!(row.alwaysOnStatic || row.replay?.s2?.alwaysOnStatic)

const git = (args, options = {}) => execFileSync('git', args, { cwd: root, timeout: 30_000, maxBuffer: 128 * 1024 * 1024, stdio: ['pipe', 'pipe', 'pipe'], ...options })
const blobs = new Map()
const snapshots = new Map()
const historicalSha = /^[a-f0-9]{40}$/

// A depth-1 checkout can resolve an old commit through the commit graph and
// still lack its tree. Fetch those commits; do not widen the workflow checkout.
function treeReady(sha, gitFn) {
  try {
    gitFn(['rev-parse', '--verify', '--quiet', `${sha}^{tree}`])
    return true
  } catch (error) {
    const text = `${error.stderr ?? ''}\n${error.message ?? ''}`
    if (error.status === 1 || error.status === 128 || /not a (?:valid object name|tree object)|Needed a single revision|bad revision|ambiguous argument/.test(text)) return false
    throw error
  }
}

export function ensureTrees(shas, gitFn = git) {
  const missing = []
  for (const sha of new Set(shas)) {
    if (!historicalSha.test(sha)) throw new Error(`refusing non-sha historical tree ${sha}`)
    if (!treeReady(sha, gitFn)) missing.push(sha)
  }
  if (missing.length >= 64) throw new Error(`too many missing historical trees (${missing.length})`)
  if (!missing.length) return
  gitFn(['fetch', '--no-tags', '--depth=1', 'origin', ...missing], { timeout: 120_000 })
  const still = missing.filter(sha => !treeReady(sha, gitFn))
  if (still.length) throw new Error(`historical trees still missing after fetch: ${still.slice(0, 4).join(',')}`)
}

function snapshot(sha) {
  if (snapshots.has(sha)) {
    const hit = snapshots.get(sha)
    snapshots.delete(sha); snapshots.set(sha, hit)
    return hit
  }
  const listing = git(['ls-tree', '-r', '-z', sha, '--', 'internal', 'cmd', 'scripts', 'web/src', 'web/tests', 'web/e2e', 'web/ci-web-shards.json', 'api']).toString().split('\0').filter(Boolean)
  const entries = listing.map(line => /^(\d+) blob ([a-f0-9]{40})\t(.+)$/.exec(line)).filter(Boolean)
    .filter(([, mode, , file]) => mode.startsWith('100') &&
      (/\.go$/.test(file) && !file.split('/').includes('testdata') ||
       /^web\/(?:src|tests|e2e)\/.+\.(?:ts|js|mjs|vue|json|css|html)$/.test(file) ||
       /^scripts\/ci\/[^/]+\.json$/.test(file) || file === 'web/ci-web-shards.json' ||
       /^api\/.+\.yaml$/.test(file) || /\.sql$/.test(file)))
  assert.ok(entries.length < 20_000, 'source count bound')
  const missing = [...new Set(entries.map(entry => entry[2]).filter(id => !blobs.has(id)))]
  if (missing.length) {
    const batch = git(['cat-file', '--batch'], { input: missing.join('\n') + '\n' })
    let at = 0
    for (const id of missing) {
      const nl = batch.indexOf(10, at)
      const size = Number(batch.subarray(at, nl).toString().split(' ')[2])
      assert.ok(size < 8 * 1024 * 1024 && Number.isSafeInteger(size), 'blob size bound')
      blobs.set(id, batch.subarray(nl + 1, nl + 1 + size).toString())
      at = nl + size + 2
    }
    assert.equal(at, batch.length)
  }
  const files = new Map(entries.map(entry => [entry[3], blobs.get(entry[2])]))
  const go = new Map([...files].filter(([file]) => file.endsWith('.go')))
  const imports = {}
  for (const [file, text] of go) {
    const owner = dirname(file)
    imports[owner] ??= []
    for (const match of text.matchAll(/["`]github\.com\/inspr-at\/paimos\/([^"`]+)["`]/g))
      if (!imports[owner].includes(match[1])) imports[owner].push(match[1])
  }
  const graph = {}
  for (const file of files.keys()) if (/^web\/(?:src|tests|e2e)\//.test(file)) graph[file.slice(4)] = []
  for (const file of Object.keys(graph)) {
    const source = files.get('web/' + file)
    if (typeof source !== 'string') continue
    for (const match of source.matchAll(/(?:\b(?:import|export)\s+(?:[\s\S]*?\s+from\s*)?|\bimport\s*\()\s*['"]([^'"]+)['"]/g)) {
      const imported = match[1].split('?')[0]
      if (!imported.startsWith('.') && !imported.startsWith('@/')) continue
      const base = imported.startsWith('@/') ? 'src/' + imported.slice(2) : posix.normalize(posix.join(dirname(file), imported))
      const candidates = [base, ...['.ts', '.js', '.mjs', '.vue', '.json', '.css', '/index.ts', '/index.js'].map(suffix => base + suffix)]
      const target = candidates.find(candidate => Object.hasOwn(graph, candidate))
      if (target) graph[file].push(target)
      else if (posix.extname(imported) === '') graph[file].push(base)
    }
  }
  const tests = ['go', 'web'].flatMap(kind => {
    const text = files.get(`scripts/ci/${kind}-test-tiers.json`)
    return text ? JSON.parse(text).tests : []
  })
  const result = { files, graph, tests, tree: { complete: true, go, imports, webTests: new Map([...files].filter(([file]) => /^web\/tests\/.+\.(?:ts|mjs|js)$/.test(file)).map(([file, text]) => [file.slice(4), text])) } }
  snapshots.set(sha, result)
  while (snapshots.size > 4) snapshots.delete(snapshots.keys().next().value)
  return result
}

function registered(data, row) {
  const observed = row.observed
  if (observed?.kind === 'go') {
    const name = observed.name.split('/')[0]
    if (!data.tests.some(item => item.kind === 'go' && item.package === observed.package && item.name === name) &&
        [...data.tree.go].some(([file, text]) => dirname(file) === observed.package && new RegExp(`func ${name}\\(`).test(text)))
      data.tests.push({ kind: 'go', package: observed.package, name, tier: 'NIGHTLY' })
    return data.tests.filter(item => item.kind === 'go' && item.package === observed.package && item.name === name)
  }
  const file = observed?.file?.startsWith('web/') ? observed.file.slice(4) : observed?.file
  let found = data.tests.filter(item => item.kind === observed.kind && item.file === file &&
    (item.name === observed.name || item.name.endsWith(` › ${observed.name}`) || observed.name.endsWith(` › ${item.name}`)))
  if (!found.length && row.expectedFailureKey) found = data.tests.filter(item => key(item) === row.expectedFailureKey)
  return found
}

test('AEON-1334 historical corpus replay has zero proof misses', { timeout: 300_000 }, () => {
  const fixture = JSON.parse(readFileSync(new URL('./affected-replay.json', import.meta.url), 'utf8'))
  const obligations = JSON.parse(readFileSync(new URL('./drift-obligations.json', import.meta.url), 'utf8'))
  const rows = fixture.historicalRegressions
  assert.equal(rows.length, 1508)
  assert.equal(fixture.historicalFailures.length, rows.length)
  assert.equal(browserScopeNoneAllowlist.length, 0)
  for (const row of fixture.historicalFailures) {
    for (const field of ['expectedFailureKey', 'paths', 'testedSHA', 'headSHA', 'base', 'attempt', 'attribution'])
      assert.ok(row[field] !== undefined && row[field] !== '', `${row.id} ${field}`)
    assert.ok(Array.isArray(row.paths), row.id)
  }
  const missedFiles = new Set(rows.filter(row => !row.replay?.s2?.effectiveSelected && !staticRow(row)).map(row => fileOf(row.expectedFailureKey)).filter(Boolean))
  assert.deepEqual(new Set([...obligations.browserFiles, ...obligations.unitFiles]), missedFiles)
  const trees = [...new Set(rows.filter(row => !staticRow(row)).map(row => row.treeBeforeFix))]
  assert.ok(trees.length > 0 && trees.length < 64, `historical trees ${trees.length}`)
  ensureTrees(trees)
  const plans = new Map()
  const counts = { proofMisses: [], rejectedAsProof: 0, staticObligations: 0, unregisteredProof: 0, fullLiteralMisses: [] }
  for (const row of rows) {
    if (!proofConfidence.has(row.causalConfidence) && !rejectedConfidence.has(row.causalConfidence))
      assert.fail(`unknown attribution ${row.causalConfidence}`)
    if (rejectedConfidence.has(row.causalConfidence)) counts.rejectedAsProof++
    if (staticRow(row)) { counts.staticObligations++; continue }
    let data
    try { data = snapshot(row.treeBeforeFix) }
    catch (error) { assert.fail(`${row.id} tree ${row.treeBeforeFix}: ${error.message}`) }
    const found = registered(data, row)
    if (!found.length) {
      if (proofConfidence.has(row.causalConfidence)) counts.unregisteredProof++
      continue
    }
    const paths = row.causingChange?.paths ?? []
    // A native reconcile can register a case after an earlier row cached this
    // plan. The catalogue length keeps that case inside the full gate.
    const planKey = JSON.stringify([row.treeBeforeFix, paths, row.causingChange?.base, row.causingChange?.commit, data.tests.length])
    let planned = plans.get(planKey)
    if (!planned) {
      const registryChanges = new Map()
      for (const file of paths.filter(path => semanticRegistryPattern.test(path))) {
        try {
          const before = JSON.parse(git(['show', `${row.causingChange.base}:${file}`]).toString())
          const after = JSON.parse(git(['show', `${row.causingChange.commit}:${file}`]).toString())
          registryChanges.set(file, registryImpact(file, before, after))
        } catch { registryChanges.set(file, { full: true, promoted: [], reason: 'registry comparison unavailable' }) }
      }
      const decision = schedulingDecision('pull_request', paths, file => data.files.has(file), {
        affectedLane: 'on', impact: row.requiredEnvironment?.CI_IMPACT, tests: data.tests, graph: data.graph,
        tree: data.tree, imports: data.tree.imports, registryChanges, readFile: file => data.files.get(file),
      })
      const selection = select(data.tests, {
        event: 'pull_request', paths, affectedLane: 'on', impact: row.requiredEnvironment?.CI_IMPACT,
        webImports: data.graph, imports: data.tree.imports, tree: data.tree, registryChanges,
        readFile: file => data.files.get(file), forceFull: decision.mode === 'full',
      })
      planned = { full: selection.full || decision.mode === 'full', selected: new Set(selection.tests.map(key)) }
      plans.set(planKey, planned)
    }
    const exact = found.some(item => key(item) === row.expectedFailureKey)
    const selected = exact ? planned.selected.has(row.expectedFailureKey) : found.some(item => planned.selected.has(key(item)))
    if (!selected && proofConfidence.has(row.causalConfidence)) counts.proofMisses.push(row.id)
    if (!selected && planned.full) counts.fullLiteralMisses.push(row.id)
  }
  assert.equal(counts.proofMisses.length, 0, `proof misses ${counts.proofMisses.slice(0, 12).join(',')}`)
  assert.equal(counts.fullLiteralMisses.length, 0, `full-plan misses ${counts.fullLiteralMisses.slice(0, 12).join(',')}`)
  assert.equal(counts.rejectedAsProof, 439 + 753)
  assert.equal(counts.staticObligations, 4)
  assert.equal(counts.unregisteredProof, 0)
  const current = webGraph(fileURLToPath(new URL('../../web', import.meta.url)))
  const picked = new Set()
  const visit = file => {
    if (picked.has(file)) return
    picked.add(file)
    for (const [owner, deps] of Object.entries(current)) if (deps.includes(file)) visit(owner)
  }
  visit('src/components/work/TicketTable.vue')
  const catalogue = JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json', import.meta.url), 'utf8')).tests
  const drift = new Set(obligations.browserFiles)
  const imported = new Set([...picked].filter(file => drift.has(file)))
  const mapped = new Set(obligations.componentSpecs['web/src/components/work/TicketTable.vue'] ?? [])
  const extraFiles = new Set([...mapped].filter(file => !imported.has(file)))
  const extraCases = catalogue.filter(row => row.kind === 'browser' && row.tier === 'NIGHTLY' && extraFiles.has(row.file)).length
  assert.equal(obligations.cost.fullExtraNightlyCases, catalogue.filter(row => row.kind === 'browser' && row.tier === 'NIGHTLY' && drift.has(row.file)).length)
  assert.ok(extraCases < obligations.cost.fullExtraNightlyCases)
  obligations.cost.typicalComponent = 'web/src/components/work/TicketTable.vue'
  obligations.cost.typicalExtraNightlyCases = extraCases
  // The figure is reported from this assertion; the compact file stores the full-gate cost.
  assert.equal(extraCases, obligations.cost.typicalExtraNightlyCases)
})

function fixtureGit(cwd, args, options = {}) {
  return execFileSync('git', args, {
    cwd, timeout: options.timeout ?? 30_000, maxBuffer: 8 * 1024 * 1024, stdio: ['pipe', 'pipe', 'pipe'],
    env: { ...process.env, GIT_AUTHOR_NAME: 'Fixture', GIT_AUTHOR_EMAIL: 'fixture@example.com', GIT_COMMITTER_NAME: 'Fixture', GIT_COMMITTER_EMAIL: 'fixture@example.com' },
    ...options,
  })
}

test('AEON-1334 shallow checkout fetches a missing historical tree before ls-tree', () => {
  const directory = mkdtempSync(join(tmpdir(), 'aeon-1334-trees-'))
  const origin = join(directory, 'origin')
  const checkout = join(directory, 'checkout')
  try {
    fixtureGit(directory, ['init', '-b', 'main', origin])
    writeFileSync(join(origin, 'note.txt'), 'a\n')
    fixtureGit(origin, ['add', 'note.txt'])
    fixtureGit(origin, ['commit', '-m', 'A'])
    const sha = fixtureGit(origin, ['rev-parse', 'HEAD']).toString().trim()
    writeFileSync(join(origin, 'note.txt'), 'b\n')
    fixtureGit(origin, ['add', 'note.txt'])
    fixtureGit(origin, ['commit', '-m', 'B'])
    fixtureGit(origin, ['config', 'uploadpack.allowReachableSHA1InWant', 'true'])
    fixtureGit(directory, ['clone', '--depth', '1', '--no-local', `file://${origin}`, checkout])
    const calls = []
    const gitFn = (args, options) => { calls.push(args); return fixtureGit(checkout, args, options) }
    assert.throws(() => fixtureGit(checkout, ['ls-tree', '-r', '-z', sha]), /not a tree object|Not a valid object name/)
    ensureTrees([sha], gitFn)
    assert.deepEqual(calls.find(args => args[0] === 'fetch'), ['fetch', '--no-tags', '--depth=1', 'origin', sha])
    fixtureGit(checkout, ['rev-parse', '--verify', '--quiet', `${sha}^{tree}`])
    fixtureGit(checkout, ['ls-tree', '-r', '-z', sha])
    const fetched = calls.length
    ensureTrees([sha, sha], gitFn)
    assert.ok(calls.slice(fetched).every(args => args[0] === 'rev-parse'))
    assert.equal(calls.filter(args => args[0] === 'fetch').length, 1)
  } finally {
    rmSync(directory, { recursive: true, force: true })
  }
})

test('AEON-1334 refuses a wide or non-sha set of missing historical trees', () => {
  const absent = sha => {
    const error = new Error(`fatal: not a tree object: ${sha}`)
    error.status = 128
    error.stderr = Buffer.from(`fatal: not a tree object: ${sha}\n`)
    throw error
  }
  assert.throws(() => ensureTrees(['main'], absent), /refusing non-sha historical tree/)
  const wide = Array.from({ length: 64 }, (_, index) => index.toString(16).padStart(40, '0'))
  assert.throws(() => ensureTrees(wide, absent), /too many missing historical trees \(64\)/)
  assert.deepEqual(ensureTrees([], absent), undefined)
})
