// SPDX-License-Identifier: AGPL-3.0-only
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdirSync, writeFileSync, mkdtempSync, readdirSync, rmSync } from 'node:fs'
import { resolve } from 'node:path'
import { inputMetadata, readInput, inputBounds } from './inputs.mjs'

export const browserIdentity = row => JSON.stringify([row.file, row.project ?? '', row.config ?? '', row.name, row.occurrence ?? null])
const sourcePath = path => /^src\/.+\.(?:ts|js|mjs|vue)$/.test(path) && !path.split('/').some(part => ['', '.', '..'].includes(part))
const specPath = path => /^(?:tests|e2e)\/.+\.spec\.ts$/.test(path) && !path.split('/').some(part => ['', '.', '..'].includes(part))

// Git blob identities cover dependency inputs without reading their contents.
// Unrelated backend/docs commits may reuse observations; web source, routes,
// harnesses, fixtures, configuration, policy and catalogue changes may not.
export function dependencyFingerprint(checkout, revision = 'HEAD', exec = execFileSync) {
  try {
    if (revision !== 'HEAD' && !/^[a-f0-9]{40}$/.test(revision ?? '')) return undefined
    const inputs = exec('git', ['ls-tree', '-r', '-z', revision, '--', 'web', 'scripts/test-tiers',
      'scripts/ci/web-test-tiers.json', 'scripts/ci/known-flaky.json', 'scripts/playwright-safe.mjs',
      'scripts/playwright-global-setup.mjs', 'scripts/playwright-lock.mjs', 'scripts/playwright-processes.mjs',
      'api', 'version.json', '.github/workflows/nightly-full.yml'],
    { cwd: checkout, encoding: 'utf8', timeout: 10_000, maxBuffer: 4 * 1024 * 1024 })
    if (!inputs || !inputs.endsWith('\0') || inputs.split('\0').length > 20_000 ||
        inputs.split('\0').filter(Boolean).some(row => !/^100(?:644|755) blob [a-f0-9]{40}\t[^\x00-\x1f]+$/.test(row))) return undefined
    return `browser-deps-v1:${createHash('sha256').update(inputs).digest('hex')}`
  } catch { return undefined }
}

// Vite serves individual source modules, including Vue's query-suffixed parts.
// Parsed scripts are retained as a conservative superset of V8 execution:
// cross-document navigation cannot erase a previously observed dependency.
export function coverageSource(url) {
  try {
    const path = decodeURIComponent(new URL(url).pathname).replace(/^\//, '')
    return sourcePath(path) ? path : undefined
  } catch { return undefined }
}

export function buildBrowserMap(reports, { tree, catalogue, runId, attempt, fingerprint }) {
  if (!Array.isArray(catalogue) || catalogue.length > 20_000 || !Array.isArray(reports) || reports.length > 64) return undefined
  const expected = new Set(catalogue.filter(row => row.kind === 'browser' && row.active !== false).map(browserIdentity))
  const observed = new Set(), modules = {}, specs = new Set()
  if (!/^[a-f0-9]{40}$/.test(tree ?? '') || !expected.size || !reports.length) return undefined
  for (const report of reports) {
    if (report?.version !== 1 || report.tree !== tree || report.runId !== runId || report.attempt !== attempt || !report.complete || !Array.isArray(report.cases) || report.cases.length > 20_000) return undefined
    if (fingerprint !== undefined && (report.fingerprint !== fingerprint || !/^browser-deps-v1:[a-f0-9]{64}$/.test(fingerprint))) return undefined
    for (const row of report.cases) {
      const identity = browserIdentity(row)
      if (!expected.has(identity) || observed.has(identity) || !row.complete || row.status !== 'passed' || !specPath(row.file) || !Array.isArray(row.sources) || row.sources.length > 20_000 || !row.sources.every(sourcePath)) return undefined
      observed.add(identity); specs.add(row.file)
      for (const source of row.sources) {
        modules[source] ??= new Set()
        modules[source].add(row.file)
      }
    }
  }
  if (observed.size !== expected.size) return undefined
  return { version: 1, tree, ...(fingerprint === undefined ? {} : { fingerprint }), complete: true, runId, attempt, catalogue: [...expected].sort(), specs: [...specs].sort(),
    modules: Object.fromEntries(Object.entries(modules).sort().map(([source, owners]) => [source, [...owners].sort()])) }
}

export function browserImpact(files, graph, map, baseTree, fingerprint) {
  const specs = new Set()
  for (const file of files) {
    if (!file.startsWith('src/')) continue
    if (!sourcePath(file)) return { full: true, specs, reason: `browser source is CSS, asset or data: web/${file}` }
    if (!Object.hasOwn(graph, file)) return { full: true, specs, reason: `new, renamed or unmapped browser source: web/${file}` }
    const matching = map?.tree === baseTree || /^browser-deps-v1:[a-f0-9]{64}$/.test(fingerprint ?? '') && map?.fingerprint === fingerprint
    if (map?.version !== 1 || map.complete !== true || !/^[a-f0-9]{40}$/.test(baseTree ?? '') || !matching)
      return { full: true, specs, reason: 'complete browser-impact map unavailable or stale for base tree' }
    const owners = map.modules?.[file]
    if (!Array.isArray(owners) || !owners.length || !owners.every(owner => specPath(owner) && Object.hasOwn(graph, owner) && map.specs?.includes(owner)))
      return { full: true, specs, reason: `browser source has no complete coverage mapping: web/${file}` }
    for (const owner of owners) specs.add(owner)
  }
  return { full: false, specs }
}

export function gitTree(checkout, revision = 'HEAD', exec = execFileSync) {
  try {
    if (revision !== 'HEAD' && !/^[a-f0-9]{40}$/.test(revision)) return undefined
    const tree = exec('git', ['rev-parse', `${revision}^{tree}`], { cwd: checkout, encoding: 'utf8', timeout: 10_000, maxBuffer: 1024 }).trim()
    return /^[a-f0-9]{40}$/.test(tree) ? tree : undefined
  } catch { return undefined }
}

const maps = new Map()
// Fetch only successful main nightlies matching trusted dependency inputs (or
// the exact tree for legacy evidence). Candidate
// files and caller-supplied maps are never trusted by the Actions planner.
// No result cache: observations here describe dependencies, never test passes.
export function nightlyBrowserMap(checkout, base, catalogue, { exec = execFileSync, compatible = false } = {}) {
  const deadline = Date.now() + 20_000
  const boundedExec = (command, args, options) => {
    const remaining = deadline - Date.now()
    if (remaining <= 0) throw new Error('browser impact fetch time bound')
    return exec(command, args, { ...options, timeout: Math.min(options.timeout, remaining) })
  }
  const tree = gitTree(checkout, base, boundedExec)
  if (!tree || !/^[a-f0-9]{40}$/.test(base ?? '')) return { baseTree: tree }
  const fingerprint = dependencyFingerprint(checkout, base, boundedExec)
  const cacheKey = `${checkout}:${base}:${compatible}`
  if (exec === execFileSync && maps.has(cacheKey)) return maps.get(cacheKey)
  const api = (path, maxBuffer = 2 * 1024 * 1024) => boundedExec('gh', ['api', path], { cwd: checkout, timeout: 15_000, maxBuffer })
  const json = path => JSON.parse(api(path).toString())
  let browserMap, directory
  try {
    const repo = JSON.parse(boundedExec('gh', ['repo', 'view', '--json', 'nameWithOwner'], { cwd: checkout, encoding: 'utf8', timeout: 10_000, maxBuffer: 4096 })).nameWithOwner
    if (!/^[\w.-]+\/[\w.-]+$/.test(repo)) throw new Error('invalid repository')
    const runs = json(`repos/${repo}/actions/workflows/nightly-full.yml/runs?${compatible ? 'branch=main' : `head_sha=${base}`}&status=success&per_page=5`).workflow_runs
    if (!Array.isArray(runs) || runs.length > 5) throw new Error('invalid nightly list')
    let run, observedTree
    for (const candidate of runs) {
      if (candidate.conclusion !== 'success' || !['schedule', 'workflow_dispatch'].includes(candidate.event) ||
          candidate.head_branch !== 'main' || candidate.repository?.full_name !== repo || candidate.head_repository?.full_name !== repo ||
          !/^[a-f0-9]{40}$/.test(candidate.head_sha ?? '') || !Number.isSafeInteger(candidate.id) || candidate.id < 1 ||
          !Number.isSafeInteger(candidate.run_attempt) || candidate.run_attempt < 1) continue
      if (!compatible && candidate.head_sha !== base) continue
      // Fetch immutable main history; a candidate's artifact cannot nominate
      // its own compatible fingerprint. Recompute it from trusted git inputs.
      boundedExec('git', ['fetch', '--no-tags', '--depth=1', 'origin', candidate.head_sha], { cwd: checkout, timeout: 10_000, maxBuffer: 4096 })
      const candidateTree = gitTree(checkout, candidate.head_sha, boundedExec)
      if (candidateTree !== tree && (!fingerprint || dependencyFingerprint(checkout, candidate.head_sha, boundedExec) !== fingerprint)) continue
      run = candidate; observedTree = candidateTree; break
    }
    if (!run) throw new Error('no compatible successful main nightly')
    const artifacts = json(`repos/${repo}/actions/runs/${run.id}/artifacts?per_page=100`)
    if (artifacts.total_count > 100 || !Array.isArray(artifacts.artifacts)) throw new Error('incomplete artifact listing')
    const selected = artifacts.artifacts.filter(row => /^web-shard-\d+-tier-measurements$/.test(row.name))
    if (!selected.length || selected.length > 64 || selected.some(row => row.expired || !Number.isSafeInteger(row.id) || row.id < 1 || !Number.isSafeInteger(row.size_in_bytes) || row.size_in_bytes < 1 || row.size_in_bytes > 16 * 1024 * 1024) || selected.reduce((sum,row) => sum + row.size_in_bytes, 0) > inputBounds.totalBytes) throw new Error('missing or oversized coverage artifacts')
    mkdirSync(resolve(checkout, 'tmp'), { recursive: true })
    directory = mkdtempSync(resolve(checkout, 'tmp/browser-impact-'))
    const reports = []
    let reportBytes = 0, archiveBytes = 0
    for (const artifact of selected) {
      const archive = resolve(directory, `${artifact.id}.zip`)
      const bytes = api(`repos/${repo}/actions/artifacts/${artifact.id}/zip`, 16 * 1024 * 1024)
      archiveBytes += Buffer.byteLength(bytes)
      if (archiveBytes >= inputBounds.totalBytes) throw new Error('coverage archives total byte bound')
      writeFileSync(archive, bytes)
      const names = boundedExec('unzip', ['-Z1', archive], { timeout: 10_000, encoding: 'utf8', maxBuffer: 64 * 1024 }).trim().split('\n')
      const matches = names.filter(name => name === `browser-impact-${artifact.name.replace(/-tier-measurements$/, '')}.json`)
      if (matches.length !== 1) throw new Error('coverage summary missing')
      const text = boundedExec('unzip', ['-p', archive, matches[0]], { timeout: 10_000, encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 })
      reportBytes += Buffer.byteLength(text)
      if (reportBytes >= inputBounds.totalBytes) throw new Error('coverage reports total byte bound')
      reports.push(JSON.parse(text))
    }
    browserMap = buildBrowserMap(reports, { tree: observedTree, catalogue, runId: String(run.id), attempt: String(run.run_attempt),
      ...(observedTree === tree && reports.every(report => report.fingerprint === undefined) ? {} : { fingerprint }) })
  } catch { /* Missing, stale, truncated or inaccessible evidence stays full. */ }
  finally { if (directory) rmSync(directory, { recursive: true, force: true }) }
  const result = { baseTree: tree, browserMap, ...(fingerprint ? { fingerprint } : {}) }
  if (exec === execFileSync) maps.set(cacheKey, result)
  return result
}

// Shard summaries are already uploaded by the nightly measurement step.
export function coverageSummary(directory, rows, metadata) {
  const cases = []
  try {
    const names = readdirSync(directory)
    if (names.length > 20_000) throw new Error('coverage file bound')
    let bytes = 0
    const files = names.map(name => {
      if (!/^[a-f0-9]{64}\.json$/.test(name)) throw new Error('unexpected coverage file')
      const file = inputMetadata(directory, name)
      bytes += file.size
      if (bytes >= inputBounds.totalBytes) throw new Error('coverage byte bound')
      return file
    })
    for (const file of files) cases.push(JSON.parse(readInput(file)))
    const expected = rows.map(browserIdentity).sort()
    const actual = cases.map(browserIdentity).sort()
    return { version: 1, ...metadata, complete: JSON.stringify(expected) === JSON.stringify(actual) && cases.every(row => row.complete && row.status === 'passed'), cases }
  } catch { return { version: 1, ...metadata, complete: false, cases: [] } }
}
