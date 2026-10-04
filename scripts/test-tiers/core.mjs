// SPDX-License-Identifier: AGPL-3.0-only
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, extname, resolve, relative } from 'node:path'

export const key = row => row.kind === 'go' ? `${row.package}:${row.name}` : `${row.kind}:${row.file}:${row.name}${row.occurrence===undefined?'':`#${row.occurrence}`}`
export const escapeRE = text => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
export const exactPattern = names => `^(?:${names.map(escapeRE).join('|')})$`

export function validate(manifest, discovered, knownFlaky = JSON.parse(readFileSync(new URL('../ci/known-flaky.json', import.meta.url), 'utf8'))) {
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
    if(group.tag!=='delete-candidate'||!manifest.tests.some(row=>row.file===group.file&&row.tier==='NIGHTLY'))throw new Error(`Invalid deletion candidate group: ${group.file}`)
  }
  for (const row of manifest.tests) {
    if (!['ESSENTIAL', 'NIGHTLY'].includes(row.tier) || typeof row.name !== 'string' || !row.name ||
        !['go', 'node', 'vitest', 'browser'].includes(row.kind) ||
        (row.tags ?? []).some(tag => tag !== 'delete-candidate') ||
        (row.tags?.includes('delete-candidate') && row.tier !== 'NIGHTLY')) throw new Error(`Invalid classification: ${key(row)}`)
    if(row.kind==='go' ? !/^(?:internal|cmd|scripts)(?:\/[a-zA-Z0-9_-]+)+$/.test(row.package??'') :
      !/^tests\/(?:[a-zA-Z0-9_-]+\/)*[a-zA-Z0-9_.-]+\.(?:spec|test)\.ts$/.test(row.file??'')) throw new Error(`Invalid test owner: ${key(row)}`)
    if(row.occurrence!==undefined && (!Number.isInteger(row.occurrence)||row.occurrence<1)) throw new Error(`Invalid occurrence: ${key(row)}`)
    if(row.lane!==undefined&&(row.kind!=='go'||row.lane!=='timing'))throw new Error(`Invalid execution lane: ${key(row)}`)
    if (declared.has(key(row))) throw new Error(`Duplicate manifest entry: ${key(row)}`)
    if (row.tier === 'ESSENTIAL' && flaky.has(key(row))) throw new Error(`Known-flaky case cannot be ESSENTIAL: ${key(row)} (${flaky.get(key(row))})`)
    declared.set(key(row), row)
  }
  const found = new Map()
  for (const row of discovered) {
    if (found.has(key(row))) throw new Error(`Duplicate collected identity: ${key(row)}`)
    found.set(key(row), row)
    if (!declared.has(key(row))) throw new Error(`Unclassified test (classify as NIGHTLY): ${key(row)}`)
    const stored = declared.get(key(row))
    if (row.kind === 'browser' && (stored.config !== row.config || stored.project !== row.project)) throw new Error(`Changed browser configuration: ${key(row)}`)
  }
  for (const id of declared.keys()) if (!found.has(id)) throw new Error(`Stale manifest entry: ${id}`)
  return discovered.map(row => ({ ...row, ...declared.get(key(row)), active: row.active ?? true }))
}

export function uncertain(path) {
  return /^(?:go\.(?:mod|sum)|go\.work(?:\.sum)?|api\/|\.github\/|scripts\/|internal\/db\/|internal\/dbtest\/|web\/(?:package.*\.json|.*config.*|playwright.*|scripts\/|ci-web-shards\.json))/.test(path) ||
    /(?:^|\/)(?:testdata|fixtures|helpers)(?:\/|\.)|(?:-fixtures|test_helpers)\./.test(path)
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

// A missing diff, unmapped source, or shared input widens selection. Both PRs
// and merge groups use this same union; neither has an essential-only shortcut.
export function select(tests, { event, paths, imports = {}, webImports = {} }) {
  if (!['pull_request', 'merge_group'].includes(event) || !Array.isArray(paths) || paths.some(uncertain)) {
    return { full: true, reason: 'full event or uncertain impact', tests }
  }
  const packages = new Set([...tests.filter(t => t.kind === 'go').map(t => t.package), ...Object.keys(imports)])
  const hasGo=tests.some(t=>t.kind==='go'),hasWeb=tests.some(t=>t.kind!=='go')
  const changedGo = new Set(), changedWeb = new Set()
  for (const path of paths) {
    if (/^(?:docs\/|README(?:\.|$)|LICENSE(?:\.|$)|CHANGELOG(?:\.|$))/.test(path)) continue
    if (/^(?:internal|cmd)\//.test(path)) {
      if(!hasGo) continue
      const owner = [...packages].filter(pkg => path.startsWith(`${pkg}/`)).sort((a,b) => b.length-a.length)[0]
      if (!owner) return { full: true, reason: `unmapped Go input: ${path}`, tests }
      changedGo.add(owner)
    } else if (/^web\/(?:src|tests|e2e)\//.test(path)) {
      if(hasWeb) changedWeb.add(path.slice(4))
    }
    else return { full: true, reason: `unmapped input: ${path}`, tests }
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
  if ([...changedWeb].some(file => !Object.hasOwn(webImports, file))) return { full: true, reason: 'missing web dependency metadata', tests }
  if([...changedWeb].some(file=>file.startsWith('src/')&&!tests.some(row=>row.kind!=='go'&&web.has(row.file)))) return {full:true,reason:'web module has no mapped test importer',tests}
  return { full: false, reason: boundedGo?`essential plus changed area; optional Go reverse dependencies exceed 300 extra cases (${extraGoCases})`:'essential plus changed area and reverse dependencies',
    tests: tests.filter(t => t.tier === 'ESSENTIAL' || (t.kind === 'go' ? go.has(t.package) : web.has(t.file))) }
}

export function shard(rows, index, count, weights = {}) {
  if (!Number.isInteger(count) || count < 1 || count > 64 || !Number.isInteger(index) || index < 1 || index > count) throw new Error('Invalid shard i/N')
  // Keep packages/files together to avoid compiling or launching a server for
  // each individual test. Weights are old full-file timings, not measured tier costs.
  const groups = new Map()
  for (const row of rows) {
    const owner = row.kind === 'go' ? row.package : row.file
    if (!groups.has(owner)) groups.set(owner, [])
    groups.get(owner).push(row)
  }
  const bins = Array.from({length: count}, () => ({ weight: 0, rows: [] }))
  for (const [owner, entries] of [...groups].sort(([a,ar],[b,br]) => (weights[b] ?? br.length)-(weights[a] ?? ar.length) || a.localeCompare(b))) {
    const bin = bins.reduce((a,b) => b.weight < a.weight ? b : a)
    bin.rows.push(...entries)
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
  return Object.fromEntries(['ESSENTIAL','NIGHTLY'].map(tier => [tier, rows.filter(row => row.tier === tier).length]))
}
