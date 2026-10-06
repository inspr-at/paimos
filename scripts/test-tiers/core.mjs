// SPDX-License-Identifier: AGPL-3.0-only
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, extname, resolve, relative } from 'node:path'

export const key = row => row.kind === 'go' ? `${row.package}:${row.name}` : `${row.kind}:${row.file}:${row.name}${row.occurrence===undefined?'':`#${row.occurrence}`}`
export const escapeRE = text => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
export const exactPattern = names => `^(?:${names.map(escapeRE).join('|')})$`
export const tiers = ['ESSENTIAL', 'GATED-FULL', 'NIGHTLY']

export function validate(manifest, discovered, knownFlaky = JSON.parse(readFileSync(new URL('../ci/known-flaky.json', import.meta.url), 'utf8')), { strict = false, warn = console.warn } = {}) {
  if (manifest.version !== 1 || !Array.isArray(manifest.tests) || !manifest.tests.length) throw new Error('Expected nonempty tier manifest version 1')
  if (knownFlaky.version !== 1 || !Array.isArray(knownFlaky.entries)) throw new Error('Expected known-flaky registry version 1')
  const flaky = new Map()
  for (const entry of knownFlaky.entries) {
    if (typeof entry.key !== 'string' || !entry.key || !/^AEON-\d+$/.test(entry.owner ?? '')) throw new Error('Known-flaky entries need a case key and owner ticket')
    if (flaky.has(entry.key)) throw new Error(`Duplicate known-flaky entry: ${entry.key}`)
    flaky.set(entry.key, entry.owner)
  }
  const declared = new Map()
  for(const group of manifest.deleteCandidateGroups??[]) {
    if(group.tag!=='delete-candidate'||!manifest.tests.some(row=>row.file===group.file&&row.tier!=='ESSENTIAL'))throw new Error(`Invalid deletion candidate group: ${group.file}`)
  }
  for (const row of manifest.tests) {
    if (!tiers.includes(row.tier) || typeof row.name !== 'string' || !row.name ||
        !['go', 'node', 'vitest', 'browser'].includes(row.kind) ||
        (row.tags ?? []).some(tag => tag !== 'delete-candidate') ||
        (row.tags?.includes('delete-candidate') && row.tier === 'ESSENTIAL')) throw new Error(`Invalid classification: ${key(row)}`)
    if(row.kind==='go' ? !/^(?:internal|cmd|scripts)(?:\/[a-zA-Z0-9_-]+)+$/.test(row.package??'') :
      !/^tests\/(?:[a-zA-Z0-9_-]+\/)*[a-zA-Z0-9_.-]+\.(?:spec|test)\.ts$/.test(row.file??'')) throw new Error(`Invalid test owner: ${key(row)}`)
    if(row.occurrence!==undefined && (!Number.isInteger(row.occurrence)||row.occurrence<1)) throw new Error(`Invalid occurrence: ${key(row)}`)
    if(row.lane!==undefined&&(row.kind!=='go'||row.lane!=='timing'))throw new Error(`Invalid execution lane: ${key(row)}`)
    if (declared.has(key(row))) throw new Error(`Duplicate manifest entry: ${key(row)}`)
    if (row.tier === 'ESSENTIAL' && flaky.has(key(row))) throw new Error(`Known-flaky case cannot be ESSENTIAL: ${key(row)} (${flaky.get(key(row))})`)
    declared.set(key(row), row)
  }
  const found = new Map()
  const drift = []
  for (const row of discovered) {
    if (found.has(key(row))) throw new Error(`Duplicate collected identity: ${key(row)}`)
    found.set(key(row), row)
    if (!declared.has(key(row))) {
      drift.push(`Unclassified test (defaults to NIGHTLY; classify these): ${key(row)}`)
      continue
    }
    const stored = declared.get(key(row))
    if (row.kind === 'browser' && (stored.config !== row.config || stored.project !== row.project)) throw new Error(`Changed browser configuration: ${key(row)}`)
  }
  for (const id of declared.keys()) if (!found.has(id)) drift.push(`Stale manifest entry (dropped): ${id}`)
  if (strict && drift.length) throw new Error(`Tier manifest drift; classify these:\n${drift.join('\n')}`)
  // Reconcile in memory: unrelated PRs must not edit the large allowlists.
  // Escape Actions command data so parameterized titles stay one warning each.
  for (const message of drift) warn(`::warning::${message.replaceAll('%', '%25').replaceAll('\r', '%0D').replaceAll('\n', '%0A')}`)
  return discovered.map(row => ({ ...row, ...(declared.get(key(row)) ?? { tier: 'NIGHTLY' }), active: row.active ?? true }))
}

export function uncertain(path) {
  return /^(?:go\.(?:mod|sum)|go\.work(?:\.sum)?|api\/|\.github\/|scripts\/|internal\/db\/|internal\/dbtest\/|web\/(?:package.*\.json|.*config.*|playwright.*|scripts\/|ci-web-shards\.json))/.test(path) ||
    /(?:^|\/)(?:testdata|fixtures|helpers)(?:\/|\.)|(?:-fixtures|test_helpers)\./.test(path)
}

export function validImpactPaths(paths) {
  return Array.isArray(paths) && paths.length <= 10000 && paths.every(path =>
    typeof path === 'string' && path.length > 0 && path.length <= 4096 &&
    !path.startsWith('/') && !/^[a-zA-Z]:/.test(path) && !path.includes('\\') &&
    !/[\x00-\x1f\x7f]/.test(path) && !path.split('/').some(part => ['', '.', '..'].includes(part)))
}

// The opt-in allowlist is deliberately smaller than uncertain(). Rules whose
// execution/mapping cannot be proved retain the full gate, with a plan reason.
export function affectedRisk(path, webImports = {}) {
  if (/^(?:scripts\/ci\/.*\.json|web\/ci-web-shards\.json|scripts\/migration-policy.*\.json|scripts\/audit\/.*\.json)$/.test(path))
    return { full: true, reason: 'R1 deferred: static-only jobs and manifest registration diff required' }
  if (/^api\//.test(path)) return { full: true, reason: 'R2 deferred: no generated API dependency mapping' }
  if (/^internal\/db\/migrations\//.test(path)) return { full: true, reason: 'R4 deferred: no migration-to-browser dependency mapping' }
  if (/^scripts\/audit\//.test(path)) return { full: true, reason: 'R5 deferred: static-only execution required' }
  if (/^(?:web\/tests\/)/.test(path) && uncertain(path) && !/\.(?:spec|test)\.ts$/.test(path) && /\.(?:ts|js|mjs|vue|json|css)$/.test(path)) {
    const file = path.slice(4)
    if (!Object.hasOwn(webImports, file)) return { full: true, reason: `R3 full: missing helper dependency metadata: ${path}` }
    const dependants = reverseDependants(new Set([file]), webImports)
    const importers = [...dependants].filter(owner => owner !== file && /^tests\/.*\.(?:spec|test)\.ts$/.test(owner))
    if (!importers.length) return { full: true, reason: `R3 full: helper has no mapped test importer: ${path}` }
    const specs = importers.filter(owner => owner.endsWith('.spec.ts')).length
    if (specs > 15) return { full: true, reason: `R3 full: helper imports exceed 15 specs (${specs}): ${path}` }
    return { full: false, reason: `R3: helper reverse dependants (${specs} specs): ${path}` }
  }
  return { full: true, reason: `unnarrowed risk: ${path}` }
}

export function impactRisk(paths, { event, affectedLane, webImports = {} }) {
  // Only the literal repository-variable value "on" enables new rules.
  if (event !== 'pull_request' || affectedLane !== 'on')
    return { full: paths.some(uncertain), reasons: [] }
  const reasons = []
  for (const path of paths) {
    // Generated inputs/configuration stay full even if a helper name matches.
    if (/(?:^|[/.])(?:generated|codegen)(?:\/|\.)|\.(?:gen|pb)\.|(?:^|\/)package[^/]*\.json$|(?:^|\/)(?:tsconfig|vite|playwright)[^/]*\.(?:json|ts|js|mjs)$/.test(path))
      return { full: true, reasons: [`unnarrowed generated/configuration risk: ${path}`] }
    if (!uncertain(path)) continue
    const rule = affectedRisk(path, webImports)
    if (rule.full) return { full: true, reasons: [rule.reason] }
    reasons.push(rule.reason)
  }
  return { full: false, reasons }
}

export function reverseDependants(changed, imports) {
  const picked = new Set(changed)
  let more = true
  while (more) {
    more = false
    for (const [owner, deps] of Object.entries(imports)) {
      if (!picked.has(owner) && deps.some(dep => picked.has(dep))) { picked.add(owner); more = true }
    }
  }
  return picked
}

// A missing diff, unmapped source, or shared input widens selection. The merge
// queue always proves the full gate, independently of the affected PR switch.
export function select(tests, { event, paths, imports = {}, webImports = {}, affectedLane, forceFull = false, forceAll = false }) {
  const full = reason => {
    // Full CI retains the established gate. Only nightly/--all widens to the
    // entire catalogue; missing impact data must not promote ungated cases.
    const catalogue = forceAll || event === 'schedule'
    const selected = catalogue ? tests : tests.filter(row => row.tier === 'ESSENTIAL' || row.tier === 'GATED-FULL')
    return { full: true, reason, scope: catalogue ? 'catalogue' : 'gated-full',
      deferredBrowserCases: tests.filter(row => row.kind === 'browser').length - selected.filter(row => row.kind === 'browser').length, tests: selected }
  }
  if (forceAll || forceFull || event !== 'pull_request' || !Array.isArray(paths)) {
    return full('full event or uncertain impact')
  }
  if (!validImpactPaths(paths)) return full('invalid or oversized impact diff')
  const risk = impactRisk(paths, { event, affectedLane, webImports })
  if (risk.full) return full(risk.reasons.join('; ') || 'full event or uncertain impact')
  const packages = new Set([...tests.filter(t => t.kind === 'go').map(t => t.package), ...Object.keys(imports)])
  const hasGo=tests.some(t=>t.kind==='go'),hasWeb=tests.some(t=>t.kind!=='go')
  const changedGo = new Set(), changedWeb = new Set()
  for (const path of paths) {
    if (/^(?:docs\/|README(?:\.|$)|LICENSE(?:\.|$)|CHANGELOG(?:\.|$))/.test(path)) continue
    if (/^(?:internal|cmd)\//.test(path)) {
      if(!hasGo) continue
      const owner = [...packages].filter(pkg => path.startsWith(`${pkg}/`)).sort((a,b) => b.length-a.length)[0]
      if (!owner) return full(`unmapped Go input: ${path}`)
      changedGo.add(owner)
    } else if (/^web\/(?:src|tests|e2e)\//.test(path)) {
      if(hasWeb) changedWeb.add(path.slice(4))
    }
    else return full(`unmapped input: ${path}`)
  }
  const dependants = reverseDependants(changedGo, imports)
  // Test-import cycles can turn a tiny package edit into almost the whole
  // repository. The approved cheap-dependency policy retains every changed
  // package but limits the optional reverse-dependency expansion.
  const extraGoCases=tests.filter(t=>t.kind==='go'&&dependants.has(t.package)&&!changedGo.has(t.package)&&t.tier!=='ESSENTIAL').length
  const boundedGo=extraGoCases>300
  const go=boundedGo?changedGo:dependants
  const web = reverseDependants(changedWeb, webImports)
  // Deleted/unknown files cannot be analysed using only the candidate graph.
  if ([...changedWeb].some(file => !Object.hasOwn(webImports, file))) return full('missing web dependency metadata')
  if([...changedWeb].some(file=>file.startsWith('src/')&&!tests.some(row=>row.kind!=='go'&&web.has(row.file)))) return full('web module has no mapped test importer')
  const reason = boundedGo?`essential plus changed area; optional Go reverse dependencies exceed 300 extra cases (${extraGoCases})`:'essential plus changed area and reverse dependencies'
  return { full: false, reason: risk.reasons.length ? `${reason}; ${risk.reasons.join('; ')}` : reason,
    tests: tests.filter(t => t.tier === 'ESSENTIAL' || (t.kind === 'go' ? go.has(t.package) : web.has(t.file))) }
}

export function measuredWeights(manifest, rows) {
  const weights = {}
  for (const [owner, timing] of Object.entries(manifest.timingWeights?.owners ?? {})) {
    if (!Number.isFinite(timing.seconds) || timing.seconds < 0 || !Number.isSafeInteger(timing.selectedTests) || timing.selectedTests < 1) throw new Error(`Invalid measured timing: ${owner}`)
    const selected = rows.filter(row => (row.kind === 'go' ? row.package : row.file) === owner && row.active !== false).length
    weights[owner] = timing.seconds * selected / timing.selectedTests
  }
  return weights
}

export function shard(rows, index, count, weights = {}, { firstShardLast = false } = {}) {
  if (!Number.isInteger(count) || count < 1 || count > 64 || !Number.isInteger(index) || index < 1 || index > count) throw new Error('Invalid shard i/N')
  // Keep packages/files together to avoid compiling or launching a server for
  // each individual test. Measured tier costs override historical/count estimates.
  const groups = new Map()
  for (const row of rows) {
    const owner = row.kind === 'go' ? row.package : row.file
    if (!groups.has(owner)) groups.set(owner, [])
    groups.get(owner).push(row)
  }
  const bins = Array.from({length: count}, () => ({ weight: 0, owners: 0, rows: [] }))
  // Unit shard 1 also runs workflow and browser-supervisor regressions. Give
  // equally loaded peers first choice without inventing a hosted overhead cost.
  const preference = firstShardLast ? [...bins.slice(1), bins[0]] : bins
  for (const [owner, entries] of [...groups].sort(([a,ar],[b,br]) => (weights[b] ?? br.length)-(weights[a] ?? ar.length) || a.localeCompare(b))) {
    // Zero-duration owners still need a shard. On equal loads prefer fewer
    // owners, then retain shard order for a deterministic final tie-break.
    const bin = preference.reduce((a,b) => b.weight < a.weight || (b.weight === a.weight && b.owners < a.owners) ? b : a)
    bin.rows.push(...entries)
    bin.owners++
    bin.weight += weights[owner] ?? entries.length
  }
  return bins[index-1].rows
}

export function webGraph(root) {
  const graph = {}
  const visit = directory => {
    for (const entry of readdirSync(resolve(root, directory), { withFileTypes: true })) {
      const file = `${directory}/${entry.name}`
      if (entry.isDirectory()) visit(file)
      else if (/\.(?:ts|js|mjs|vue|json|css)$/.test(file)) graph[file] = []
    }
  }
  for (const dir of ['src', 'tests', 'e2e']) visit(dir)
  for (const file of Object.keys(graph)) {
    const source = readFileSync(resolve(root, file), 'utf8')
    for (const match of source.matchAll(/(?:\b(?:import|export)\s+(?:[\s\S]*?\s+from\s*)?|\bimport\s*\()\s*['"]([^'"]+)['"]/g)) {
      const imported = match[1]
      if (!imported.startsWith('.')) continue
      const base = resolve(root, dirname(file), imported)
      const candidates = [base, ...['.ts','.js','.mjs','.vue','.json','.css','/index.ts','/index.js'].map(s => base+s)]
      const target = candidates.find(path => existsSync(path) && Object.hasOwn(graph, relative(root,path)))
      if (target) graph[file].push(relative(root,target))
      else if (extname(imported) === '') graph[file].push(relative(root,base)) // changed/deleted import remains a conservative edge
    }
  }
  return graph
}

export function counts(rows) {
  return Object.fromEntries(tiers.map(tier => [tier, rows.filter(row => row.tier === tier).length]))
}
