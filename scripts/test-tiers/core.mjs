// SPDX-License-Identifier: AGPL-3.0-only
import { existsSync, readFileSync, readdirSync, lstatSync } from 'node:fs'
import { dirname, extname, resolve, relative } from 'node:path'
import { migrationObjects } from './migration.mjs'
import { inputBounds, inputMetadata, readInput } from './inputs.mjs'
import { browserImpact } from './browser-impact.mjs'
import { validateManifest as validateBrowserPolicy } from '../../web/scripts/ci-web-shard.mjs'

export const key = row => row.kind === 'go' ? `${row.package}:${row.name}` : `${row.kind}:${row.file}:${row.name}${row.occurrence===undefined?'':`#${row.occurrence}`}`
export const escapeRE = text => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
export const exactPattern = names => `^(?:${names.map(escapeRE).join('|')})$`
export const tiers = ['ESSENTIAL', 'GATED-FULL', 'NIGHTLY']

export function knownFlakyOwners(knownFlaky = JSON.parse(readFileSync(new URL('../ci/known-flaky.json', import.meta.url), 'utf8'))) {
  if (knownFlaky.version !== 1 || !Array.isArray(knownFlaky.entries)) throw new Error('Expected known-flaky registry version 1')
  const flaky = new Map()
  for (const entry of knownFlaky.entries) {
    if (typeof entry.key !== 'string' || !entry.key || !/^AEON-\d+$/.test(entry.owner ?? '')) throw new Error('Known-flaky entries need a case key and owner ticket')
    if (flaky.has(entry.key)) throw new Error(`Duplicate known-flaky entry: ${entry.key}`)
    flaky.set(entry.key, entry.owner)
  }
  return flaky
}

export function validate(manifest, discovered, knownFlaky, { strict = false, warn = console.warn } = {}) {
  if (manifest.version !== 1 || !Array.isArray(manifest.tests) || !manifest.tests.length) throw new Error('Expected nonempty tier manifest version 1')
  const flaky = knownFlakyOwners(knownFlaky)
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
// Per-spec shard records are weight manifests, not the planner (AEON-1389).
export const specShardRecordPattern = /^web\/scripts\/ci-web-shard-specs\/.+\.spec\.ts\.json$/
export const machineryPattern = /^(?:\.github\/|scripts\/ci-[^/]*$|scripts\/ci-[^/]+\/|scripts\/ci\/static-checks\.json$|scripts\/test-tiers\/|scripts\/test-tier-go\/|scripts\/releaseworkflow\/|go\.(?:mod|sum)$|go\.work(?:\.sum)?$|web\/package[^/]*\.json$|web\/(?:tsconfig|vite|playwright)[^/]*\.(?:json|ts|js|mjs)$|web\/scripts\/(?!(?:(?:ci-web-shard|aeon-676-ci|aeon-681-ci)\.test\.mjs|ci-web-shard-specs\/.+\.spec\.ts\.json)$))/
const generatedPattern = /(?:^|[/.])(?:generated|codegen)(?:\/|\.)|\.(?:gen|pb)\.|(?:^|\/)package[^/]*\.json$|(?:^|\/)(?:tsconfig|vite|playwright)[^/]*\.(?:json|ts|js|mjs)$/
// R1: tier/shard/weight/policy manifests plus the regression tests that pin
// them; every lane that reaches web-unit shard 1 and go-static re-validates them.
const manifestPattern = /^(?:scripts\/ci\/(?!static-checks\.json$)[^/]+\.(?:json|txt)|web\/ci-web-shards\.json|web\/scripts\/ci-web-shard-specs\/.+\.spec\.ts\.json|scripts\/migration-policy[^/]*\.json|scripts\/audit\/[^/]+\.json|web\/scripts\/(?:ci-web-shard|aeon-676-ci|aeon-681-ci)\.test\.mjs)$/
export const tierManifestPattern = /^scripts\/ci\/(?:go|web)-test-tiers\.json$/
export const semanticRegistryPattern = /^(?:scripts\/ci\/(?:go|web)-test-tiers\.json|web\/ci-web-shards\.json)$/
// R9: regression tests of the unconditional migration-compat job.
const alwaysOnTestPattern = /^scripts\/(?:check-migrations\.test\.mjs|migration_compat_probe_test\.py)$/
// Established documentation only. Fixture, testdata, server-delivered and
// test-pinned Markdown widen; AGENTS.md and CLAUDE.md are instruction inputs.
const docsPattern = /^(?:docs\/|README(?:\.|$)|LICENSE(?:\.|$)|CHANGELOG(?:\.|$)|web\/README(?:\.|$))/
const instructionPattern = /(?:^|\/)(?:AGENTS|CLAUDE)\.md$/i
export const isDocsLike = path => !instructionPattern.test(path) && docsPattern.test(path)
// Research allowlist stays empty. Timing-only registry edits narrow by content
// comparison, never because their path is listed here.
export const browserScopeNoneAllowlist = []
const driftObligations = JSON.parse(readFileSync(new URL('./drift-obligations.json', import.meta.url), 'utf8'))
const driftGoKeys = new Set(driftObligations.goKeys ?? [])
const componentSpecs = driftObligations.componentSpecs ?? {}
const appearancePackage = 'internal/themes'
const appearancePrefix = 'TestAppearanceMigration'
const sharedSchemaPattern = /^(?:internal\/(?:auth|principallink|profile|themes|fieldschema)\/|api\/areas\/|api\/openapi|internal\/db\/(?:migrations|schema)(?:\/|$))/
// The recorded titles that caught drift, by kind and file. The corpus replay
// resolves a title the same way: exact, or across a describe prefix.
export const driftCases = new Map()
for (const { kind, file, name } of driftObligations.cases ?? []) {
  if (!['browser', 'vitest', 'node'].includes(kind) || typeof file !== 'string' || typeof name !== 'string' || !name)
    throw new Error('invalid drift obligation case')
  const owner = `${kind}:${file}`
  if (!driftCases.has(owner)) driftCases.set(owner, [])
  driftCases.get(owner).push(name)
}
const sameCase = (name, recorded) => name === recorded || name.endsWith(` › ${recorded}`) || recorded.endsWith(` › ${name}`)

// Recorded drift cases for the full gate. A recorded title that no current
// case resolves (renamed or split) widens to its whole file, so a rename
// cannot silently drop the obligation.
function driftCaseKeys(tests) {
  const rows = new Map()
  for (const row of tests) if (row.kind !== 'go' && driftCases.has(`${row.kind}:${row.file}`)) {
    const owner = `${row.kind}:${row.file}`
    if (!rows.has(owner)) rows.set(owner, [])
    rows.get(owner).push(row)
  }
  const wanted = new Set()
  for (const [owner, names] of driftCases) {
    const candidates = rows.get(owner) ?? []
    for (const name of names) {
      const found = candidates.filter(row => sameCase(row.name, name))
      for (const row of found.length ? found : candidates) wanted.add(key(row))
    }
  }
  return wanted
}

// Cases whose omission let real drift reach main. A full gate runs every
// recorded behaviour case; a narrowed diff runs the component's specs and the
// old-schema appearance fixtures when principal, auth or schema changes.
export function behaviourObligation(tests, paths, { allDrift = false } = {}) {
  const files = new Set()
  if (!allDrift && Array.isArray(paths)) for (const path of paths) for (const file of componentSpecs[path] ?? []) files.add(file)
  const shared = !allDrift && Array.isArray(paths) && paths.some(path => sharedSchemaPattern.test(path))
  const wanted = allDrift ? driftCaseKeys(tests) : new Set()
  for (const row of tests) {
    if (row.kind !== 'go' && files.has(row.file)) wanted.add(key(row))
    else if (row.kind === 'go' && allDrift && driftGoKeys.has(key(row))) wanted.add(key(row))
    else if (row.kind === 'go' && row.package === appearancePackage && row.name.startsWith(appearancePrefix) && (allDrift || shared)) wanted.add(key(row))
  }
  return wanted
}

// AEON-1431: this nightly case is the proof that a workflow diff still defers
// browser cases outside the drift files. A planner or fixture edit has to run
// it on the PR lane. Every other full gate leaves it nightly.
const plannerRegressionName = 'native planning regression reuses one collected snapshot for unchanged-tree CLI checks'
const plannerRegressionPath = path => typeof path === 'string' && (path.startsWith('scripts/test-tiers/') ||
  path === 'web/tests/test-tier-failures.test.ts' || path === 'web/scripts/aeon-681-ci.test.mjs')
function plannerRegressionKeys(tests, paths) {
  if (!Array.isArray(paths) || !paths.some(plannerRegressionPath)) return new Set()
  const wanted = new Set()
  for (const row of tests) {
    if (row.kind === 'node' && row.file === 'tests/test-tier-failures.test.ts' && row.name === plannerRegressionName)
      wanted.add(key(row))
  }
  return wanted
}

export function fullGateTests(tests, paths) {
  const extra = behaviourObligation(tests, paths, { allDrift: true })
  const regression = plannerRegressionKeys(tests, paths)
  return tests.filter(row => row.tier === 'ESSENTIAL' || row.tier === 'GATED-FULL' || extra.has(key(row)) || regression.has(key(row)))
}
export const tierRank = { NIGHTLY: 0, 'GATED-FULL': 1, ESSENTIAL: 2 }
// Two essential Go shards carry about one full hosted shard each; wider
// consumer sets are cheaper in the seven-shard full layout.
export const consumerCaseBound = 1000
export const browserDataInput = path => /^web\/(?:src|tests|e2e)\//.test(path) && !/\.(?:ts|js|mjs|vue)$/.test(path)

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

const stableRegistry = value => JSON.stringify(value, (_, item) => item && typeof item === 'object' && !Array.isArray(item)
  ? Object.fromEntries(Object.entries(item).sort(([a], [b]) => a.localeCompare(b))) : item)
const registryObject = value => value !== null && typeof value === 'object' && !Array.isArray(value)
const timingEntry = (entry, positive = false) => {
  if (!registryObject(entry) || !Number.isFinite(entry.seconds) || (positive ? entry.seconds <= 0 : entry.seconds < 0) ||
      !Number.isSafeInteger(entry.selectedTests) || entry.selectedTests < 1) throw new Error('invalid measured timing')
  const { seconds, selectedTests, caseSeconds, split, ...policy } = entry
  if (caseSeconds !== undefined) {
    if (!registryObject(caseSeconds) || Object.values(caseSeconds).some(seconds => !Number.isFinite(seconds) || seconds < 0))
      throw new Error('invalid case timings')
  }
  if (split !== undefined) {
    // Two remains the AEON-1024 balancer fixture. Three is the recorded
    // AEON-1338 measurement. Any other count, or a field beside the
    // measurements, is policy and fails wide.
    if (!registryObject(split) || ![2, 3].includes(split.count) || !Number.isFinite(split.overheadSeconds) || split.overheadSeconds < 0 ||
        !registryObject(split.caseSeconds) || Object.values(split.caseSeconds).some(seconds => !Number.isFinite(seconds) || seconds < 0))
      throw new Error('invalid split timing')
    const { overheadSeconds, caseSeconds, count, ...splitPolicy } = split
    if (Object.keys(splitPolicy).length) policy.split = splitPolicy
  }
  return Object.keys(policy).length ? policy : undefined
}
const timingMap = entries => {
  if (!registryObject(entries)) throw new Error('invalid timing owners')
  return Object.fromEntries(Object.entries(entries).flatMap(([owner, entry]) => {
    const policy = timingEntry(entry)
    return policy ? [[owner, policy]] : []
  }))
}

// Strip only known scheduling measurements at their schema locations. A flag
// named "seconds" or an unknown field inside timing data remains policy.
function registryPolicy(file, registry) {
  const result = structuredClone(registry)
  if (tierManifestPattern.test(file)) {
    result.tests = result.tests.map(row => ({ ...row, tier: undefined })).sort((a, b) => key(a).localeCompare(key(b)))
    if (result.timingWeights !== undefined) {
      if (!registryObject(result.timingWeights)) throw new Error('invalid timing weights')
      result.timingWeights.owners = timingMap(result.timingWeights.owners)
      // These bind measurements, not execution. Unknown provenance stays policy.
      delete result.timingWeights.runId
      delete result.timingWeights.sha
      if (!Object.keys(result.timingWeights.owners ?? {}).length) delete result.timingWeights.owners
      if (!Object.keys(result.timingWeights).length) delete result.timingWeights
    }
  } else {
    for (const group of result.groups ?? []) for (const spec of group.specs ?? []) {
      if (spec.tierTiming !== undefined) {
        const policy = timingEntry(spec.tierTiming, true)
        if (policy) spec.tierTiming = policy
        else delete spec.tierTiming
      }
      delete spec.weightSeconds
    }
    if (result.nightlyTiming !== undefined) {
      if (result.nightlyTiming.files !== undefined) {
        result.nightlyTiming.files = timingMap(result.nightlyTiming.files)
        if (!Object.keys(result.nightlyTiming.files).length) delete result.nightlyTiming.files
      }
    }
  }
  return result
}

// Content semantics replace R1's blanket registry exemption. Native inventory
// checks still run; this comparison cannot approve removal or demotion.
export function registryImpact(file, before, after) {
  const full = (reason, requiresApproval = false) => ({ full: true, reason, requiresApproval, promoted: [] })
  try {
    if (!semanticRegistryPattern.test(file) || before?.version !== 1 || after?.version !== 1)
      return full('registry inputs missing, invalid or unclassified')
    if (tierManifestPattern.test(file)) {
      for (const manifest of [before, after]) {
        if (manifest.tests?.length > 20_000) return full('registry case count bound')
        validate(manifest, manifest.tests, { version: 1, entries: [] }, { strict: true })
        if (manifest.tests.some(row => file.includes('/go-') ? row.kind !== 'go' : row.kind === 'go')) return full('registry kind changed')
      }
      const previous = new Map(before.tests.map(row => [key(row), row]))
      const demoted = after.tests.some(row => previous.has(key(row)) && tierRank[row.tier] < tierRank[previous.get(key(row)).tier])
      if (demoted) return full('tier demotion requires explicit person approval and full validation', true)
      if (before.tests.length !== after.tests.length || after.tests.some(row => !previous.has(key(row))))
        return full('catalogue membership changed')
    } else if (before.specSource !== undefined || after.specSource !== undefined) {
      // Per-spec records hold membership. The source document keeps empty spec
      // lists, so the assembled nonempty-specs check does not apply here.
      if (before.specSource !== after.specSource || typeof before.specSource !== 'string')
        return full('registry ownership, exclusions, configuration or execution policy changed')
    } else {
      validateBrowserPolicy(before); validateBrowserPolicy(after)
    }
    if (stableRegistry(registryPolicy(file, before)) !== stableRegistry(registryPolicy(file, after)))
      return full('registry ownership, exclusions, configuration or execution policy changed')
    const promoted = tierManifestPattern.test(file)
      ? manifestPromotions({ go: before }, { go: after }).keys : new Set()
    const rows = (after.tests ?? []).filter(row => promoted.has(key(row)))
    return { full: false, requiresApproval: false, promoted: rows,
      reason: rows.length ? 'tier promotions execute' : 'timing-only or semantically unchanged registry' }
  } catch { return full('invalid registry or timing data') }
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
  if (instructionPattern.test(path)) return { full: true, reason: `test-pinned instruction input: ${path}` }
  if (generatedPattern.test(path)) return { full: true, reason: `unnarrowed generated/configuration risk: ${path}` }
  if (machineryPattern.test(path)) return { full: true, reason: `CI machinery stays full: ${path}` }
  if (isDocsLike(path)) return { full: false, rule: 'docs', layout: 'static', skip: true, reason: `docs-like: ${path}` }
  if (manifestPattern.test(path)) {
    if (semanticRegistryPattern.test(path)) return { full: false, rule: 'R1', layout: 'static', skip: true, registry: true, reason: `R1: semantic registry checks: ${path}` }
    if (specShardRecordPattern.test(path) || !/\.(?:json|txt)$/.test(path)) return { full: false, rule: 'R1', layout: 'static', skip: true, reason: `R1: manifest/static checks: ${path}` }
    return { full: true, reason: `unclassified registry or execution policy stays full: ${path}` }
  }
  if (/^scripts\/audit\//.test(path)) return { full: false, rule: 'R5', layout: 'static', skip: true, reason: `R5: audit static checks: ${path}` }
  if (alwaysOnTestPattern.test(path)) return { full: false, rule: 'R9', layout: 'static', skip: true, reason: `R9: migration-compat regression test: ${path}` }
  // Sources of the generated bundle use the bundle's reader scan, never their
  // own rarely-mentioned filename. Keep generic owner selection for modules.
  if (/^api\/(?:areas\/[^/]+\.yaml|openapi\.base\.yaml)$/.test(path) || /^internal\/.+\/openapi\.yaml$/.test(path))
    return { full: false, rule: 'R2', skip: path.startsWith('api/'), go: { substrings: ['api/openapi.yaml'] }, reason: `R2: contract consumers: ${path}` }
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
export function impactRisk(paths, { event, affectedLane, webImports = {}, tree, registryChanges, readFile, tests = [] }) {
  const reasons = [], handled = new Set(), goSeeds = new Set(), webSeeds = new Set()
  const full = reason => ({ full: true, reasons: [reason], layout: 'full', handled, goSeeds, webSeeds, promoted: new Set() })
  // Instruction inputs require the full gate even when affected narrowing is off.
  const instruction = paths.find(path => instructionPattern.test(path))
  if (instruction) return full(`test-pinned instruction input: ${instruction}`)
  // Only the literal repository-variable value "on" enables new rules.
  if (event !== 'pull_request' || affectedLane !== 'on')
    return { full: paths.some(uncertain), reasons: [], layout: 'full', handled: new Set(), goSeeds: new Set(), webSeeds: new Set(), promoted: new Set() }
  if (tree?.complete === false) return full(`incomplete consumer scan: ${tree.reason}`)
  const rules = paths.map(path => [path, affectedRisk(path, webImports)])
  const failed = rules.find(([, rule]) => rule.full)
  if (failed) return full(failed[1].reason)
  if (rules.some(([, rule]) => rule.rule === 'R2') && paths.some(path => path.startsWith('internal/reportercontract/')))
    return full('R2 full: OpenAPI lint package changed with the contract')
  // An empty diff proves nothing about static sufficiency; keep the shards.
  let layout = paths.length ? 'static' : 'full', promoted = new Set(), promotedKinds = new Set()
  const sqlNeedles = new Set()
  for (const [path, rule] of rules) {
    if (rule.layout !== 'static') layout = 'full'
    if (rule.skip) handled.add(path)
    if (rule.reason && rule.rule !== 'docs') reasons.push(rule.reason)
    if (rule.rule === 'R2' && /^internal\/.+\/openapi\.yaml$/.test(path)) goSeeds.add(dirname(path))
    if (/^api\/areas\/[^/]+\.yaml$/.test(path)) {
      let fragment
      try { fragment = readFile?.(path) } catch { return full(`R2 full: fragment unreadable: ${path}`) }
      if (typeof fragment !== 'string') return full(`R2 full: fragment unreadable: ${path}`)
      const owners = [...fragment.matchAll(/^# aeon:owner ((?:internal|cmd)(?:\/[a-zA-Z0-9_-]+)+)$/gm)].map(match => match[1])
      // Shared schemas have no handler owner. An unclassified route fragment
      // stays full; missing metadata never narrows the old contract gate.
      if (!owners.length && /^  \//m.test(fragment)) return full(`R2 full: fragment owner unavailable: ${path}`)
      for (const owner of owners) {
        if (![...(tree?.go?.keys() ?? [])].some(file => dirname(file) === owner)) return full(`R2 full: fragment owner missing from scan: ${owner}`)
        goSeeds.add(owner)
      }
    }
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
    if (rule.registry) {
      const change = registryChanges?.get(path)
      if (!change || change.full) return full(`R1 full: ${change?.reason ?? 'semantic registry comparison unavailable'}: ${path}`)
      for (const row of change.promoted) { promoted.add(key(row)); promotedKinds.add(row.kind) }
      reasons.push(`R1: ${change.reason}: ${path}`)
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
    const kinds = [...promotedKinds].sort().join('/')
    reasons.push(`R1: ${promoted.size} promoted ${kinds} cases execute`)
    if (promotedKinds.has('go') || promotedKinds.has('browser')) layout = 'full'
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

// Over-cap is uncertainty, never permission to omit dependent packages.
export function goImpact(tests, paths, imports = {}, seeds = new Set()) {
  const packages = new Set([...tests.filter(t => t.kind === 'go').map(t => t.package), ...Object.keys(imports)])
  const changed = new Set(seeds)
  for (const path of paths.filter(path => /^(?:internal|cmd)\//.test(path))) {
    const owner = [...packages].filter(pkg => path.startsWith(`${pkg}/`)).sort((a,b) => b.length-a.length)[0]
    if (owner) changed.add(owner)
  }
  const dependants = reverseDependants(changed, imports)
  const extra = tests.filter(t => t.kind === 'go' && dependants.has(t.package) && !changed.has(t.package) && t.tier !== 'ESSENTIAL').length
  return { dependants, extra, full: extra > 300 }
}

// A missing diff, unmapped source, or shared input widens selection. The merge
// queue always proves the full gate, independently of the affected PR switch.
export function select(tests, { event, paths, imports = {}, webImports = {}, browserMap, baseTree, fingerprint, impact, affectedLane, forceFull = false, forceAll = false, tree, registryChanges, readFile }) {
  const full = reason => {
    // Full CI retains the established gate plus the behaviour cases that caught
    // real drift. Only nightly/--all widens to the entire catalogue.
    const catalogue = forceAll || event === 'schedule'
    const selected = catalogue ? tests : fullGateTests(tests, paths)
    return { full: true, reason, scope: catalogue ? 'catalogue' : 'gated-full',
      deferredBrowserCases: tests.filter(row => row.kind === 'browser').length - selected.filter(row => row.kind === 'browser').length, tests: selected }
  }
  if (impact === 'off') return full('CI_IMPACT=off')
  if (forceAll || forceFull || event !== 'pull_request' || !Array.isArray(paths)) {
    return full('full event or uncertain impact')
  }
  if (!validImpactPaths(paths)) return full('invalid or oversized impact diff')
  const browserData = paths.find(browserDataInput)
  if (tests.some(row => row.kind === 'browser') && browserData) return full(`browser CSS, asset or data input: ${browserData}`)
  const risk = impactRisk(paths, { event, affectedLane, webImports, tree, registryChanges, readFile, tests })
  if (risk.full) return full(risk.reasons.join('; ') || 'full event or uncertain impact')
  const packages = new Set([...tests.filter(t => t.kind === 'go').map(t => t.package), ...Object.keys(imports)])
  const hasGo=tests.some(t=>t.kind==='go'),hasWeb=tests.some(t=>t.kind!=='go')
  if (hasWeb && paths.some(path => path.startsWith('web/')) && webImports[graphIncomplete]) return full(`incomplete web dependency graph: ${webImports[graphIncomplete]}`)
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
  const go = goImpact(tests, paths, imports, changedGo)
  if (go.full) return full(`Go reverse dependencies exceed 300 extra cases (${go.extra})`)
  // Deleted/unknown files cannot be analysed using only the candidate graph.
  if ([...changedWeb].some(file => !Object.hasOwn(webImports, file))) return full('missing web dependency metadata')
  if (hasWeb) for (const file of risk.webSeeds) if (Object.hasOwn(webImports, file)) changedWeb.add(file)
  const coverage = browserImpact([...changedWeb], webImports, browserMap, baseTree, fingerprint)
  if (tests.some(row => row.kind === 'browser') && coverage.full) return full(coverage.reason)
  const web = new Set([...reverseDependants(changedWeb, webImports), ...coverage.specs])
  if([...changedWeb].some(file=>file.startsWith('src/')&&!tests.some(row=>row.kind!=='go'&&web.has(row.file)))) return full('web module has no mapped test importer')
  if (tests.filter(t => t.kind === 'browser' && web.has(t.file)).length > 300) return full('browser fan-out exceeds 300 cases')
  const reason = 'essential plus changed area and reverse dependencies'
  const extra = affectedLane === 'on' ? behaviourObligation(tests, paths) : new Set()
  const regression = plannerRegressionKeys(tests, paths)
  return { full: false, reason: risk.reasons.length ? `${reason}; ${risk.reasons.join('; ')}` : reason,
    tests: tests.filter(t => t.tier === 'ESSENTIAL' || regression.has(key(t)) || risk.promoted.has(key(t)) || extra.has(key(t)) || (t.kind === 'go' ? go.dependants.has(t.package) : web.has(t.file))) }
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

// Split only explicitly measured Go owners. Unknown/new cases retain an
// average-case estimate and still execute; overhead is charged to each launch.
// AEON-1338 (LEAD 2026-10-15): a recorded count of three is legal. Two stays
// legal for the AEON-1024 balancer fixture. Any other count is still invalid.
export function splitOwner(rows, timing, limit) {
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > 64) throw new Error('Invalid split shard limit')
  const split = timing?.split
  if (!split || ![2, 3].includes(split.count) || !Number.isFinite(split.overheadSeconds) || split.overheadSeconds < 0 ||
      !split.caseSeconds || typeof split.caseSeconds !== 'object' || Array.isArray(split.caseSeconds) ||
      !Number.isFinite(timing.seconds) || timing.seconds <= 0 || !Number.isSafeInteger(timing.selectedTests) || timing.selectedTests < 1 ||
      Object.entries(split.caseSeconds).some(([name, seconds]) => !/^(?:Test|Fuzz)\w+$/.test(name) || !Number.isFinite(seconds) || seconds < 0))
    throw new Error('Invalid measured owner split')
  if (rows.some(row => row.kind !== 'go' || row.package !== rows[0].package) || new Set(rows.map(key)).size !== rows.length)
    throw new Error('Owner split requires unique cases in one Go package')
  const estimate = Math.max(0, timing.seconds - split.overheadSeconds) / timing.selectedTests
  const cost = row => split.caseSeconds[row.name] ?? estimate
  const parts = Array.from({ length: Math.min(split.count, limit, rows.length) }, () => ({ rows: [], weight: split.overheadSeconds }))
  for (const row of [...rows].sort((a, b) => cost(b) - cost(a) || key(a).localeCompare(key(b)))) {
    const part = parts.reduce((a, b) => b.weight < a.weight || (b.weight === a.weight && b.rows.length < a.rows.length) ? b : a)
    part.rows.push(row)
    part.weight += cost(row)
  }
  return parts
}

export function shard(rows, index, count, weights = {}, { firstShardLast = false, ownerTimings = {} } = {}) {
  if (!Number.isInteger(count) || count < 1 || count > 64 || !Number.isInteger(index) || index < 1 || index > count) throw new Error('Invalid shard i/N')
  // Keep owners together except for explicitly measured package splits.
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
  const batches = [...groups].flatMap(([owner, entries]) => ownerTimings[owner]?.split && entries[0].lane !== 'timing'
    ? splitOwner(entries, ownerTimings[owner], count).map((part, i) => ({ ...part, owner, part: i }))
    : [{ owner, rows: entries, weight: weights[owner] ?? entries.length, part: 0 }])
  const assigned = new Map()
  for (const { owner, rows: entries, weight, part } of batches.sort((a, b) => b.weight - a.weight || a.owner.localeCompare(b.owner) || a.part - b.part)) {
    // Zero-duration owners still need a shard. On equal loads prefer fewer
    // owners, then retain shard order for a deterministic final tie-break.
    // Parts of one package must use different shards, even if one bin is
    // still cheapest after placing the first part.
    const used = assigned.get(owner) ?? new Set()
    const bin = preference.filter(bin => !used.has(bin)).reduce((a,b) => b.weight < a.weight || (b.weight === a.weight && b.owners < a.owners) ? b : a)
    used.add(bin); assigned.set(owner, used)
    bin.rows.push(...entries)
    bin.owners++
    bin.weight += weight
  }
  return bins[index-1].rows
}

export const graphIncomplete = Symbol('incomplete web graph')
export function webGraph(root, { bounds = inputBounds, read = readInput } = {}) {
  const graph = {}
  const files = []
  let entries = 0, bytes = 0
  const visit = directory => {
    const stat = lstatSync(resolve(root, directory))
    if (!stat.isDirectory() || stat.isSymbolicLink()) throw new Error('non-directory or symlink in web graph')
    for (const entry of readdirSync(resolve(root, directory), { withFileTypes: true })) {
      if (++entries >= bounds.entries) throw new Error('web graph entry count bound reached')
      const file = `${directory}/${entry.name}`
      if (entry.isSymbolicLink()) throw new Error('symlink in web graph')
      if (entry.isDirectory()) visit(file)
      else if (/\.(?:ts|js|mjs|vue|json|css|html)$/.test(file)) {
        const metadata = inputMetadata(root, file, bounds.fileBytes)
        if (files.length + 1 >= bounds.files) throw new Error('web graph file count bound reached')
        bytes += metadata.size
        if (bytes >= bounds.totalBytes) throw new Error('web graph total byte bound reached')
        files.push({ file, metadata })
        graph[file] = []
      }
    }
  }
  try {
    for (const dir of ['src', 'tests', 'e2e']) visit(dir)
    // Admit every size before reading anything. Partial graphs never narrow.
    for (const { file, metadata } of files) {
      const source = read(metadata, bounds.fileBytes)
      for (const match of source.matchAll(/(?:\b(?:import|export)\s+(?:[\s\S]*?\s+from\s*)?|\bimport\s*\()\s*['"]([^'"]+)['"]/g)) {
        const imported = match[1].split('?')[0]
        if (!imported.startsWith('.') && !imported.startsWith('@/')) continue
        const base = imported.startsWith('@/') ? resolve(root, 'src', imported.slice(2)) : resolve(root, dirname(file), imported)
        const candidates = [base, ...['.ts','.js','.mjs','.vue','.json','.css','/index.ts','/index.js'].map(s => base+s)]
        const target = candidates.find(path => existsSync(path) && Object.hasOwn(graph, relative(root,path)))
        if (target) graph[file].push(relative(root,target))
        else if (extname(imported) === '') graph[file].push(relative(root,base)) // changed/deleted import remains a conservative edge
      }
    }
    return graph
  } catch (error) { return { [graphIncomplete]: error.message } }
}

export function counts(rows) {
  return Object.fromEntries(tiers.map(tier => [tier, rows.filter(row => row.tier === tier).length]))
}

// The tier runner executes with one worker, while some historical measured
// steps used two. Convert those file allocations to serial scheduling estimates
// without changing the original weights used by the exact-spec runner. Local
// serial timings and unmeasured/test-count estimates already use a serial basis.
export function tierWeights(manifest, rows) {
  const sources = new Map((manifest.ciInventory ?? []).map(source => [source.id, source]))
  return Object.fromEntries(manifest.groups.flatMap(group => group.specs.map(spec => {
    if (spec.tierTiming) {
      const { seconds, selectedTests } = spec.tierTiming
      if (!Number.isFinite(seconds) || seconds <= 0 || !Number.isSafeInteger(selectedTests) || selectedTests < 1) throw new Error(`Invalid tier timing: ${spec.file}`)
      // Hosted reports measure the gated slice. Other selections are estimates
      // proportional to that slice, without pretending nightly was measured.
      const selected = rows ? rows.filter(row => row.file === spec.file).length : selectedTests
      return [spec.file, seconds * selected / selectedTests]
    }
    const source = sources.get(spec.weightSource)
    const workers = source?.flags?.map(flag => /^--workers=(\d+)$/.exec(flag)).find(Boolean)
    const factor = source?.kind === 'browser-test' && source.measuredSeconds > 0 && workers ? Number(workers[1]) : 1
    if (!Number.isSafeInteger(factor) || factor < 1 || factor > 64) throw new Error(`Invalid measured worker count: ${spec.file}`)
    return [spec.file, spec.weightSeconds * factor]
  })))
}

// Seventeen is the smallest full-gate bin count whose measured tierTiming pack
// stays inside the 15-minute budget (25% of the 20-minute hang guard, including
// setup). Sixteen leaves shard 14 at 900.2s: the spare on the other bins is
// smaller than any whole file, so another bin is the fit (AEON-1453).
// Essential PR matrices grow from four up to this cap. Full mode and non-PR
// events stay fixed here so the merge-queue literal matches the plan.
export const fullWebShardCount = 17
export const fullWebShards = Array.from({ length: fullWebShardCount }, (_, index) => index + 1)

// Fail when any projected shard crosses the budget. The plan itself still
// returns fitsBudget false for an indivisible file, so callers can fail wide.
export function assertWebShardBudget(plan) {
  const over = plan?.projections?.filter(bin => !(bin.seconds <= plan.budgetSeconds)) ?? []
  if (!plan?.fitsBudget || over.length) {
    const worst = over.reduce((best, bin) => bin.seconds > (best?.seconds ?? -Infinity) ? bin : best, over[0])
    throw new Error(`Web shard ${worst?.shard ?? '?'} predicts ${Number(worst?.seconds).toFixed(1)}s, over the ${plan?.budgetSeconds}s budget`)
  }
  return plan
}

// Size the same whole-file LPT packing used by the runner. Reserve 25% of
// the 20-minute job cap, including two minutes for setup and collection.
// An indivisible heavy file cannot be fixed by adding bins: fail wide to the
// established full gate and expose its projection rather than dropping cases.
export function webShardPlan(rows, policy, { mode = 'essential', timeoutSeconds = 1200,
  headroom = 0.25, overheadSeconds = 120, minimum = 4, maximum = fullWebShardCount } = {}) {
  if (!Number.isFinite(timeoutSeconds) || timeoutSeconds <= 0 || !Number.isFinite(headroom) || headroom < 0 || headroom >= 1 ||
      !Number.isFinite(overheadSeconds) || overheadSeconds < 0 || !Number.isInteger(minimum) || minimum < 1 ||
      !Number.isInteger(maximum) || maximum < minimum || maximum > 64) throw new Error('Invalid web shard budget')
  const selected = rows.filter(row => row.kind === 'browser')
  const weights = tierWeights(policy, selected)
  const files = new Set(selected.map(row => row.file))
  for (const file of files) if (!Number.isFinite(weights[file]) || weights[file] <= 0) throw new Error(`Missing web shard weight: ${file}`)
  const budgetSeconds = timeoutSeconds * (1 - headroom)
  const project = count => Array.from({ length: count }, (_, i) => {
    const tests = shard(selected, i + 1, count, weights)
    return { shard: i + 1, cases: tests.length,
      seconds: overheadSeconds + [...new Set(tests.map(row => row.file))].reduce((sum, file) => sum + weights[file], 0) }
  })
  let count = mode === 'essential' ? minimum : maximum, projections = project(count)
  while (mode === 'essential' && count < maximum && Math.max(...projections.map(bin => bin.seconds)) > budgetSeconds) projections = project(++count)
  const projectedMaxSeconds = Math.max(...projections.map(bin => bin.seconds))
  return { shards: projections.map(bin => bin.shard), projections, projectedMaxSeconds, budgetSeconds, timeoutSeconds,
    headroom, overheadSeconds, fitsBudget: projectedMaxSeconds <= budgetSeconds }
}
