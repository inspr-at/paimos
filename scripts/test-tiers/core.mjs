// SPDX-License-Identifier: AGPL-3.0-only
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, extname, resolve, relative } from 'node:path'
import { migrationObjects } from './migration.mjs'

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

// CI machinery never narrows the PR lane: workflows, the planners and their
// tests, module/package manifests and build/test configuration. A PR can edit
// its own workflow anyway, so the merge queue's full gate is the protection;
// keeping these full just avoids debugging a lane change through a lane change.
export const machineryPattern = /^(?:\.github\/|scripts\/ci-[^/]*$|scripts\/ci-[^/]+\/|scripts\/ci\/static-checks\.json$|scripts\/test-tiers\/|scripts\/test-tier-go\/|scripts\/releaseworkflow\/|go\.(?:mod|sum)$|go\.work(?:\.sum)?$|web\/package[^/]*\.json$|web\/(?:tsconfig|vite|playwright)[^/]*\.(?:json|ts|js|mjs)$|web\/scripts\/(?!(?:ci-web-shard|aeon-676-ci|aeon-681-ci)\.test\.mjs$))/
const generatedPattern = /(?:^|[/.])(?:generated|codegen)(?:\/|\.)|\.(?:gen|pb)\.|(?:^|\/)package[^/]*\.json$|(?:^|\/)(?:tsconfig|vite|playwright)[^/]*\.(?:json|ts|js|mjs)$/
// R1: tier/shard/weight/policy manifests plus the regression tests that pin
// them; every lane that reaches web-unit shard 1 and go-static re-validates them.
const manifestPattern = /^(?:scripts\/ci\/(?!static-checks\.json$)[^/]+\.(?:json|txt)|web\/ci-web-shards\.json|scripts\/migration-policy[^/]*\.json|scripts\/audit\/[^/]+\.json|web\/scripts\/(?:ci-web-shard|aeon-676-ci|aeon-681-ci)\.test\.mjs)$/
export const tierManifestPattern = /^scripts\/ci\/(?:go|web)-test-tiers\.json$/
// R9: regression tests of the unconditional migration-compat job.
const alwaysOnTestPattern = /^scripts\/(?:check-migrations\.test\.mjs|migration_compat_probe_test\.py)$/
const docsPattern = /^(?:docs\/|README(?:\.|$)|LICENSE(?:\.|$)|CHANGELOG(?:\.|$))/
export const isDocsLike = path => docsPattern.test(path) || (/\.md$/i.test(path) && !/(?:^|\/)testdata\//.test(path) && !/^(?:internal|cmd)\//.test(path))
export const tierRank = { NIGHTLY: 0, 'GATED-FULL': 1, ESSENTIAL: 2 }
// Two essential Go shards carry about one full hosted shard each; wider
// consumer sets are cheaper in the seven-shard full layout.
export const consumerCaseBound = 1000

// Rows that this diff registers at, or promotes to, a gated tier. A promoted
// case must execute in the lane; new NIGHTLY rows only need manifest checks.
export function manifestPromotions(base = {}, head = {}) {
  const promoted = []
  for (const kind of ['go', 'web']) {
    const before = new Map((base[kind]?.tests ?? []).map(row => [key(row), row]))
    for (const row of head[kind]?.tests ?? []) {
      if (!Object.hasOwn(tierRank, row.tier)) throw new Error(`Invalid tier in ${kind} manifest: ${key(row)}`)
      const previous = before.get(key(row))
      if (row.tier !== 'NIGHTLY' && (tierRank[previous?.tier] ?? 0) < tierRank[row.tier]) promoted.push(row)
    }
  }
  return { keys: new Set(promoted.map(key)), kinds: new Set(promoted.map(row => row.kind)), count: promoted.length }
}

export { migrationObjects } from './migration.mjs'

// Packages and web test files whose source mentions a needle. A source tree is
// {go: Map(file -> text), webTests: Map(file -> text)}; package = directory.
export function consumers(tree, { substrings = [], words = [] } = {}) {
  const matches = text => substrings.some(needle => text.includes(needle)) ||
    words.some(word => new RegExp(`(?<![A-Za-z0-9_])${escapeRE(word)}(?![A-Za-z0-9_])`).test(text))
  const goPackages = new Set(), webFiles = new Set()
  if (substrings.length || words.length) {
    for (const [file, text] of tree?.go ?? []) if (matches(text)) goPackages.add(dirname(file))
    for (const [file, text] of tree?.webTests ?? []) if (/^tests\/.*\.(?:spec|test)\.ts$/.test(file) && matches(text)) webFiles.add(file)
  }
  return { goPackages, webFiles }
}

// Per-path rule for the opt-in affected PR lane. Rules whose execution or
// mapping cannot be shown retain the full gate, with a plan reason.
export function affectedRisk(path, webImports = {}) {
  if (generatedPattern.test(path)) return { full: true, reason: `unnarrowed generated/configuration risk: ${path}` }
  if (machineryPattern.test(path)) return { full: true, reason: `CI machinery stays full: ${path}` }
  if (isDocsLike(path)) return { full: false, rule: 'docs', layout: 'static', skip: true, reason: `docs-like: ${path}` }
  if (manifestPattern.test(path)) return { full: false, rule: 'R1', layout: 'static', skip: true, manifest: tierManifestPattern.test(path), reason: `R1: manifest/static checks: ${path}` }
  if (/^scripts\/audit\//.test(path)) return { full: false, rule: 'R5', layout: 'static', skip: true, reason: `R5: audit static checks: ${path}` }
  if (alwaysOnTestPattern.test(path)) return { full: false, rule: 'R9', layout: 'static', skip: true, reason: `R9: migration-compat regression test: ${path}` }
  if (/^api\//.test(path)) return { full: false, rule: 'R2', skip: true, go: { substrings: [path] }, reason: `R2: contract consumers: ${path}` }
  if (/^internal\/db\/migrations\/[^/]+\.sql$/.test(path)) return { full: false, rule: 'R4', sql: path, reason: `R4: migration schema consumers: ${path}` }
  if (path === 'version.json') return { full: false, rule: 'R6', skip: true, go: { substrings: ['version.json'] }, web: { substrings: ['version.json'] }, reason: `R6: release manifest consumers: ${path}` }
  if (/^(?:internal|cmd)\/(?:[^/]+\/)+testdata\//.test(path)) {
    const base = path.slice(path.lastIndexOf('/') + 1)
    return { full: false, rule: 'R7', go: { substrings: [base] }, web: { substrings: [base] }, reason: `R7: test data owner and readers: ${path}` }
  }
  if (/^web\/tests\/.*\.html$/.test(path)) {
    const base = path.slice(path.lastIndexOf('/') + 1)
    return { full: false, rule: 'R8', skip: true, web: { substrings: [base] }, reason: `R8: harness page readers: ${path}` }
  }
  if (/^(?:web\/tests\/)/.test(path) && uncertain(path) && !/\.(?:spec|test)\.ts$/.test(path) && /\.(?:ts|js|mjs|vue|json|css)$/.test(path)) {
    const file = path.slice(4)
    if (!Object.hasOwn(webImports, file)) return { full: true, reason: `R3 full: missing helper dependency metadata: ${path}` }
    const dependants = reverseDependants(new Set([file]), webImports)
    const importers = [...dependants].filter(owner => owner !== file && /^tests\/.*\.(?:spec|test)\.ts$/.test(owner))
    if (!importers.length) return { full: true, reason: `R3 full: helper has no mapped test importer: ${path}` }
    const specs = importers.filter(owner => owner.endsWith('.spec.ts')).length
    if (specs > 15) return { full: true, reason: `R3 full: helper imports exceed 15 specs (${specs}): ${path}` }
    return { full: false, rule: 'R3', reason: `R3: helper reverse dependants (${specs} specs): ${path}` }
  }
  if (/^(?:internal|cmd)\/.*\.md$/i.test(path) && !/(?:^|\/)testdata\//.test(path)) return { full: false, rule: 'go' }
  if (uncertain(path)) return { full: true, reason: `unnarrowed risk: ${path}` }
  if (/^(?:internal|cmd)\//.test(path)) return { full: false, rule: 'go' }
  if (/^web\/(?:src|tests|e2e)\//.test(path) && /\.(?:ts|js|mjs|vue|json|css)$/.test(path)) return { full: false, rule: 'web' }
  return { full: true, reason: `unmapped input: ${path}` }
}

const goCaseCount = (tests, packages) => tests.filter(row => row.kind === 'go' && row.tier === 'GATED-FULL' && packages.has(row.package)).length

// Whole-diff decision. layout "static" keeps only the static, unit, release
// and migration jobs; "full" keeps the essential shard layout. The returned
// seeds extend the changed area; handled paths skip the generic mapping.
export function impactRisk(paths, { event, affectedLane, webImports = {}, tree, promotions, readFile, tests = [] }) {
  // Only the literal repository-variable value "on" enables new rules.
  if (event !== 'pull_request' || affectedLane !== 'on')
    return { full: paths.some(uncertain), reasons: [], layout: 'full', handled: new Set(), goSeeds: new Set(), webSeeds: new Set(), promoted: new Set() }
  const reasons = [], handled = new Set(), goSeeds = new Set(), webSeeds = new Set()
  const full = reason => ({ full: true, reasons: [reason], layout: 'full', handled, goSeeds, webSeeds, promoted: new Set() })
  if (tree?.complete === false) return full(`incomplete consumer scan: ${tree.reason}`)
  const rules = paths.map(path => [path, affectedRisk(path, webImports)])
  const failed = rules.find(([, rule]) => rule.full)
  if (failed) return full(failed[1].reason)
  if (rules.some(([, rule]) => rule.rule === 'R2') && paths.some(path => path.startsWith('internal/reportercontract/')))
    return full('R2 full: OpenAPI lint package changed with the contract')
  // An empty diff proves nothing about static sufficiency; keep the shards.
  let layout = paths.length ? 'static' : 'full', promoted = new Set()
  const sqlNeedles = new Set()
  for (const [path, rule] of rules) {
    if (rule.layout !== 'static') layout = 'full'
    if (rule.skip) handled.add(path)
    if (rule.reason && rule.rule !== 'docs') reasons.push(rule.reason)
    if (rule.sql) {
      let sql
      try { sql = readFile?.(path) } catch { return full(`R4 full: migration unreadable: ${path}`) }
      if (typeof sql !== 'string') return full(`R4 full: migration unreadable: ${path}`)
      const objects = migrationObjects(sql)
      if (!objects) return full(`R4 full: unsupported or incomplete SQL: ${path}`)
      if (!objects.size) return full(`R4 full: no schema object recognised: ${path}`)
      for (const object of objects) sqlNeedles.add(object)
    }
    if (rule.go || rule.web) {
      const found = consumers(tree, { substrings: [...(rule.go?.substrings ?? []), ...(rule.web?.substrings ?? [])] })
      if (rule.go) {
        // Test data is owned by its package through the generic mapping, so
        // zero readers elsewhere is fine; contracts and release data need one.
        if (!found.goPackages.size && rule.rule !== 'R7') return full(`${rule.rule} full: no Go consumer found: ${path}`)
        for (const pkg of found.goPackages) goSeeds.add(pkg)
      }
      if (rule.web) for (const file of found.webFiles) webSeeds.add(file)
    }
    if (rule.manifest) {
      if (!promotions) return full(`R1 full: manifest promotions unavailable: ${path}`)
      promoted = new Set([...promoted, ...promotions.keys])
    }
  }
  if (sqlNeedles.size) {
    const found = consumers(tree, { words: [...sqlNeedles] })
    if (!found.goPackages.size) return full(`R4 full: no Go package references ${[...sqlNeedles].join(', ')}`)
    for (const pkg of found.goPackages) goSeeds.add(pkg)
  }
  const extra = goCaseCount(tests, goSeeds)
  if (extra > consumerCaseBound) return full(`consumer fan-out exceeds ${consumerCaseBound} gated Go cases (${extra} in ${goSeeds.size} packages)`)
  if (promoted.size) {
    const kinds = [...promotions.kinds].sort().join('/')
    reasons.push(`R1: ${promoted.size} promoted ${kinds} cases execute`)
    if (promotions.kinds.has('go') || promotions.kinds.has('browser')) layout = 'full'
  }
  if (goSeeds.size) reasons.push(`consumer packages: ${[...goSeeds].sort().join(', ')}`)
  if (webSeeds.size) reasons.push(`consumer web tests: ${[...webSeeds].sort().join(', ')}`)
  return { full: false, reasons, layout, handled, goSeeds, webSeeds, promoted }
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
export function select(tests, { event, paths, imports = {}, webImports = {}, affectedLane, forceFull = false, forceAll = false, tree, promotions, readFile }) {
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
  const risk = impactRisk(paths, { event, affectedLane, webImports, tree, promotions, readFile, tests })
  if (risk.full) return full(risk.reasons.join('; ') || 'full event or uncertain impact')
  const packages = new Set([...tests.filter(t => t.kind === 'go').map(t => t.package), ...Object.keys(imports)])
  const hasGo=tests.some(t=>t.kind==='go'),hasWeb=tests.some(t=>t.kind!=='go')
  const changedGo = new Set(), changedWeb = new Set()
  for (const path of paths) {
    if (risk.handled.has(path)) continue
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
  if (hasGo) for (const pkg of risk.goSeeds) if (packages.has(pkg)) changedGo.add(pkg)
  const dependants = reverseDependants(changedGo, imports)
  // Test-import cycles can turn a tiny package edit into almost the whole
  // repository. The approved cheap-dependency policy retains every changed
  // package but limits the optional reverse-dependency expansion.
  const extraGoCases=tests.filter(t=>t.kind==='go'&&dependants.has(t.package)&&!changedGo.has(t.package)&&t.tier!=='ESSENTIAL').length
  const boundedGo=extraGoCases>300
  const go=boundedGo?changedGo:dependants
  // Deleted/unknown files cannot be analysed using only the candidate graph.
  if ([...changedWeb].some(file => !Object.hasOwn(webImports, file))) return full('missing web dependency metadata')
  if (hasWeb) for (const file of risk.webSeeds) if (Object.hasOwn(webImports, file)) changedWeb.add(file)
  const web = reverseDependants(changedWeb, webImports)
  if([...changedWeb].some(file=>file.startsWith('src/')&&!tests.some(row=>row.kind!=='go'&&web.has(row.file)))) return full('web module has no mapped test importer')
  const reason = boundedGo?`essential plus changed area; optional Go reverse dependencies exceed 300 extra cases (${extraGoCases})`:'essential plus changed area and reverse dependencies'
  return { full: false, reason: risk.reasons.length ? `${reason}; ${risk.reasons.join('; ')}` : reason,
    tests: tests.filter(t => t.tier === 'ESSENTIAL' || risk.promoted.has(key(t)) || (t.kind === 'go' ? go.has(t.package) : web.has(t.file))) }
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
