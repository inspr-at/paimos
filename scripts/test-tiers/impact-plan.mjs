// SPDX-License-Identifier: AGPL-3.0-only
// S1 publishes observations only. No execution job consumes this plan.
import { execFileSync } from 'node:child_process'
import { mkdirSync, realpathSync, writeFileSync } from 'node:fs'
import { dirname, resolve, posix } from 'node:path'
import { fileURLToPath } from 'node:url'
import { browserIdentity, browserImpact, nightlyBrowserMap, gitTree } from './browser-impact.mjs'
import { boundedText } from './inputs.mjs'
import { validImpactPaths, isDocsLike, webGraph, graphIncomplete, reverseDependants, shard, validate, specShardRecordPattern, registryImpact as schemaRegistryImpact } from './core.mjs'
import { tierWeights, loadManifest } from '../../web/scripts/ci-web-shard.mjs'

const root = fileURLToPath(new URL('../../', import.meta.url))
const browserRows = manifest => manifest.tests.filter(row => row.kind === 'browser' && row.active !== false)
const stable = value => JSON.stringify(value, (_, item) => item && typeof item === 'object' && !Array.isArray(item)
  ? Object.fromEntries(Object.entries(item).sort(([a], [b]) => a.localeCompare(b))) : item)
// Schema-located comparison. Unknown fields inside timing stay policy. The
// phrases stay the ones the shadow observations already publish.
export function registryImpact(file, before, after) {
  if (file !== 'web/ci-web-shards.json' && file !== 'scripts/ci/web-test-tiers.json')
    return { full: true, promoted: [], reason: 'unmapped registry' }
  if (!before || !after || before.version !== 1 || after.version !== 1)
    return { full: true, promoted: [], reason: 'registry inputs missing or invalid' }
  const rule = schemaRegistryImpact(file, before, after)
  if (file === 'web/ci-web-shards.json') {
    if (rule.full && rule.reason === 'invalid registry or timing data')
      return { full: true, promoted: [], reason: 'invalid browser launch policy' }
    if (rule.full) return { full: true, promoted: [], reason: 'browser ownership, exclusion or execution policy changed' }
    return { full: false, promoted: [], reason: 'timing-only browser registry' }
  }
  if (!Array.isArray(before.tests) || !Array.isArray(after.tests)) return { full: true, promoted: [], reason: 'unmapped registry' }
  if (rule.full) {
    if (/demotion/.test(rule.reason)) return { full: true, promoted: [], reason: 'tier demotion requires full validation and approval', requiresApproval: true }
    if (/membership/.test(rule.reason)) return { full: true, promoted: [], reason: 'catalogue membership changed' }
    if (rule.reason === 'registry ownership, exclusions, configuration or execution policy changed')
      return { full: true, promoted: [], reason: 'tier execution policy changed' }
    return { full: true, promoted: [], reason: 'case identity, ownership or configuration changed' }
  }
  const promoted = (rule.promoted ?? []).filter(row => row.kind === 'browser')
  return { full: false, promoted, reason: promoted.length ? 'browser tier promotions execute' : 'timing-only tier registry' }
}

// Observations augment imports, never replace them. Navigation owns the
// application entry/router closure; URL-loaded HTML owns its script imports.
// Computed imports/navigation conservatively own all possible local inputs.
export function ownershipGraph(checkout, imports, read = file => boundedText(checkout, file), { maxEdges = 250_000 } = {}) {
  if (imports[graphIncomplete]) return imports
  if (Object.keys(imports).length > 20_000) return { [graphIncomplete]: 'ownership file count bound' }
  const graph = Object.fromEntries(Object.entries(imports).map(([file, deps]) => [file, [...deps]]))
  const broad = Object.keys(graph).filter(file => !/\.(?:spec|test)\.ts$/.test(file))
  let edges = Object.values(graph).reduce((sum, deps) => sum + deps.length, 0)
  const edge = (owner, target) => {
    if (!Object.hasOwn(graph, target)) throw new Error(`unmapped route/harness input: ${target}`)
    if (!graph[owner].includes(target)) {
      if (++edges > maxEdges) throw new Error('ownership edge count bound')
      graph[owner].push(target)
    }
  }
  try {
    for (const file of Object.keys(graph)) {
      const source = read(file)
      if (typeof source !== 'string') throw new Error(`unreadable ownership input: ${file}`)
      for (const match of source.matchAll(/(?:["'`])(\/(?:src|tests|e2e)\/[a-zA-Z0-9_./-]+\.(?:ts|js|mjs|vue|html))(?:[?"'`])/g)) {
        const target = match[1].slice(1)
        if (Object.hasOwn(graph, target)) edge(file, target)
        // Some tests fulfil synthetic harness URLs from inline HTML. Their
        // unmapped references own the entire local input set conservatively.
        else for (const possible of broad) if (possible !== file) edge(file, possible)
      }
      if (file.endsWith('.html')) for (const match of source.matchAll(/<script\b[^>]*\bsrc=["']([^"']+)["']/g)) {
        const target = match[1].split('?')[0]
        if (/^https?:/.test(target)) throw new Error(`external harness script: ${file}`)
        edge(file, target.startsWith('/') ? target.slice(1) : posix.normalize(posix.join(dirname(file), target)))
      }
      if (/\.goto\s*\(/.test(source)) {
        for (const entry of ['src/main.ts', 'src/router.ts']) edge(file, entry)
        // URL parameters and helper variables may resolve to any harness.
        for (const target of broad.filter(target => target.endsWith('.html'))) edge(file, target)
      }
      if (/\bimport\s*\(\s*(?!["'])|\bimport\.meta\.glob/.test(source)) for (const target of broad) if (target !== file) edge(file, target)
    }
    return graph
  } catch (error) { return { [graphIncomplete]: error.message } }
}

export function shadowPlan({ event = 'pull_request', impact, paths, identity = {}, tests = [], graph = {}, browserMap, baseTree,
  fingerprint, registries = {}, policy, execution = { mode: 'full', layout: 'full', lane: 'full' }, shardCount = 12 }) {
  const gated = tests.filter(row => row.kind === 'browser' && row.active !== false && row.tier !== 'NIGHTLY')
  let selected = gated, scope = 'gated-full', reasons = [], fallback = []
  const widen = reason => { fallback.push(reason); scope = 'gated-full'; selected = gated }
  if (impact === 'off') widen('CI_IMPACT=off: planning disabled; full execution required')
  else if (event !== 'pull_request') widen('merge queue, main and manual events retain the full gate')
  else if (!validImpactPaths(paths) || !paths.length) widen('missing, empty or invalid diff')
  else {
    const changed = [], promoted = []
    // One assembled before/after pair covers the shared policy and every
    // per-spec record. Timing stays reduced; group or ownership changes widen.
    let browserPolicy
    const browserPolicyImpact = () => browserPolicy ??= registryImpact('web/ci-web-shards.json', registries['web/ci-web-shards.json']?.before, registries['web/ci-web-shards.json']?.after)
    for (const file of paths) {
      if (isDocsLike(file)) { reasons.push(`documentation allowlist: ${file}`); continue }
      if (file === 'scripts/ci/web-test-tiers.json') {
        const rule = registryImpact(file, registries[file]?.before, registries[file]?.after)
        if (rule.full) { widen(`${file}: ${rule.reason}`); break }
        promoted.push(...rule.promoted); reasons.push(`${file}: ${rule.reason}`); continue
      }
      if (file === 'web/ci-web-shards.json' || specShardRecordPattern.test(file)) {
        const rule = browserPolicyImpact()
        if (rule.full) { widen(`${file}: ${rule.reason}`); break }
        promoted.push(...rule.promoted); reasons.push(`${file}: ${rule.reason}`); continue
      }
      if (!/^web\/(?:src|tests|e2e)\/.+\.(?:ts|js|mjs|vue|html)$/.test(file)) { widen(`unknown or shared browser input: ${file}`); break }
      const owner = file.slice(4)
      if (!Object.hasOwn(graph, owner)) { widen(`new, renamed, deleted or unmapped input: ${file}`); break }
      changed.push(owner)
    }
    if (!fallback.length) {
      if (!changed.length && !promoted.length) { scope = 'none'; selected = [] }
      else if (graph[graphIncomplete]) widen(`incomplete import/route/harness ownership: ${graph[graphIncomplete]}`)
      else {
        const coverage = browserImpact(changed, graph, browserMap, baseTree, fingerprint)
        if (coverage.full) widen(coverage.reason)
        else {
          const impacted = new Set([...reverseDependants(new Set(changed), graph), ...coverage.specs])
          if (changed.some(file => !tests.some(row => row.kind === 'browser' && impacted.has(row.file)))) widen('changed input has no browser owner')
          else {
            scope = 'affected'
            const promotedIds = new Set(promoted.map(browserIdentity))
            selected = tests.filter(row => row.kind === 'browser' && row.active !== false &&
              (row.tier === 'ESSENTIAL' || impacted.has(row.file) || promotedIds.has(browserIdentity(row))))
            if (selected.length - tests.filter(row => row.kind === 'browser' && row.tier === 'ESSENTIAL').length > 300) widen('affected browser fan-out exceeds 300 additional cases')
            else reasons.push('essential cases plus imports, route/harness owners, observations and promotions')
          }
        }
      }
    }
  }
  const picked = new Set(selected.map(browserIdentity)), skipped = gated.filter(row => !picked.has(browserIdentity(row)))
  const weights = policy ? tierWeights(policy, selected) : {}
  const assignments = Array.from({ length: shardCount }, (_, index) => ({ shard: index + 1, cases: shard(selected, index + 1, shardCount, weights).map(browserIdentity).sort() }))
  return { version: 1, policy: 'impact-ci-s1', mode: impact === 'off' ? 'off' : 'shadow', complete: true, identity, execution,
    browser_scope: scope, reasons, fallback_reasons: fallback, dependency_fingerprint: fingerprint ?? null,
    selected_cases: selected.map(browserIdentity).sort(), shards: assignments,
    would_have_skipped: { count: skipped.length, cases: skipped.map(browserIdentity).sort() },
    baseline: { browser: Object.fromEntries(['ESSENTIAL', 'GATED-FULL', 'NIGHTLY'].map(tier => [tier, tests.filter(row => row.kind === 'browser' && row.tier === tier).length])) } }
}

export function main(env = process.env, { trustedRoot = root, browserCoverage = nightlyBrowserMap } = {}) {
  const checkout = env.AEON_IMPACT_CHECKOUT ?? root, base = env.PR_BASE_SHA
  // The current catalogue exceeds the source module's 2 MiB bound. Keep an
  // explicit 4 MiB registry bound rather than silently falling back on every PR.
  const registry = (directory, file) => {
    const value = JSON.parse(boundedText(directory, file, 4 * 1024 * 1024))
    if (Array.isArray(value.tests) && value.tests.length > 20_000) throw new Error('catalogue case count bound')
    return value
  }
  const git = args => execFileSync('git', args, { cwd: checkout, encoding: 'utf8', timeout: 15_000, maxBuffer: 4 * 1024 * 1024 }).trim()
  if (!/^[a-f0-9]{40}$/.test(base ?? '')) throw new Error('unbound shadow base')
  const manifest = registry(trustedRoot, 'scripts/ci/web-test-tiers.json')
  const flaky = registry(trustedRoot, 'scripts/ci/known-flaky.json')
  validate(manifest, manifest.tests, flaky, { strict: true })
  const policy = loadManifest(resolve(trustedRoot, 'web/ci-web-shards.json'))
  const owners = new Map(policy.groups.flatMap(group => group.specs.map(spec => [spec.file, group])))
  for (const row of browserRows(manifest)) {
    const owner = owners.get(row.file)
    if (!owner || owner.config !== row.config || (owner.project ?? '') !== row.project) throw new Error(`unmapped browser launch owner: ${row.file}`)
  }
  const registries = {}
  let tests = browserRows(manifest), paths, catalogueComplete = true
  const output = execFileSync('git', ['diff', '--name-only', '-z', '--no-renames', base, 'HEAD'], { cwd: checkout, encoding: 'utf8', timeout: 15_000, maxBuffer: 2 * 1024 * 1024 })
  if (!output || output.endsWith('\0')) paths = output.split('\0').filter(Boolean)
  for (const file of paths ?? []) if (['scripts/ci/web-test-tiers.json', 'web/ci-web-shards.json'].includes(file)) {
    try {
      registries[file] = file === 'web/ci-web-shards.json'
        ? { before: policy, after: loadManifest(resolve(checkout, file)) }
        : { before: registry(trustedRoot, file), after: registry(checkout, file) }
      if (file.endsWith('web-test-tiers.json')) {
        const head = registries[file].after
        validate(head, head.tests, flaky, { strict: true }); tests = browserRows(head)
      }
    } catch { registries[file] = {}; if (file.endsWith('web-test-tiers.json')) catalogueComplete = false }
  }
  // A record-only diff leaves the shared policy file untouched. Compare the
  // assembled base and head policies, which already include every record.
  if ((paths ?? []).some(file => specShardRecordPattern.test(file)) && registries['web/ci-web-shards.json'] === undefined) {
    try {
      registries['web/ci-web-shards.json'] = { before: policy, after: loadManifest(resolve(checkout, 'web/ci-web-shards.json')) }
    } catch { registries['web/ci-web-shards.json'] = {} }
  }
  const identity = { base_sha: base, base_tree: gitTree(checkout, base), head_sha: env.PR_HEAD_SHA,
    checkout_sha: git(['rev-parse', 'HEAD']), checkout_tree: gitTree(checkout), run_id: env.GITHUB_RUN_ID, attempt: env.GITHUB_RUN_ATTEMPT }
  if (![identity.base_sha, identity.base_tree, identity.head_sha, identity.checkout_sha, identity.checkout_tree].every(value => /^[a-f0-9]{40}$/.test(value ?? '')) || identity.checkout_sha !== env.GITHUB_SHA) throw new Error('unbound shadow identity')
  for (const ancestor of [base, identity.head_sha]) git(['merge-base', '--is-ancestor', ancestor, 'HEAD'])
  const graph = env.CI_IMPACT === 'off' ? {} : ownershipGraph(resolve(trustedRoot, 'web'), webGraph(resolve(trustedRoot, 'web')))
  const coverage = env.CI_IMPACT !== 'off' && paths?.some(file => file.startsWith('web/src/'))
    ? browserCoverage(checkout, base, browserRows(manifest), { compatible: true }) : { baseTree: identity.base_tree }
  const plan = shadowPlan({ event: env.GITHUB_EVENT_NAME, impact: env.CI_IMPACT, paths, identity, tests, graph, policy, registries, ...coverage,
    execution: { mode: env.EXECUTION_MODE, layout: env.EXECUTION_LAYOUT, lane: env.EXECUTION_LANE } })
  plan.complete = catalogueComplete
  const directory = resolve(checkout, 'tmp/test-tiers')
  mkdirSync(directory, { recursive: true })
  writeFileSync(resolve(directory, 'impact-plan.json'), JSON.stringify(plan, null, 2) + '\n')
  const summary = `Impact CI S1 (${plan.mode}): browser_scope=${plan.browser_scope}; ${plan.selected_cases.length} selected; ${plan.would_have_skipped.count} gated cases would be skipped. Execution: ${stable(plan.execution)}. Reasons: ${[...plan.reasons, ...plan.fallback_reasons].join('; ')}\n`
  console.log(summary)
  if (env.GITHUB_STEP_SUMMARY) writeFileSync(env.GITHUB_STEP_SUMMARY, summary, { flag: 'a' })
  return plan
}

// Node realpaths import.meta.url. argv keeps the given path, so a macOS tmpdir
// symlink (/var -> /private/var) or any other symlink skips main and exits 0.
function invokedDirectly(argv = process.argv[1]) {
  if (!argv) return false
  try { return realpathSync(argv) === realpathSync(fileURLToPath(import.meta.url)) }
  catch { return false }
}
if (invokedDirectly()) {
  try { main() } catch (error) { console.error(error.message); process.exitCode = 1 }
}
