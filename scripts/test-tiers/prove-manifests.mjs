// SPDX-License-Identifier: AGPL-3.0-only
// Independent conversion proof: do not use the formatter/normalizer under test.
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { collectGo, collectWeb } from './collect.mjs'
import { key, validate, select, shard, measuredWeights } from './core.mjs'
import { tierWeights } from '../../web/scripts/ci-web-shard.mjs'

const root = fileURLToPath(new URL('../../', import.meta.url))
const files = ['scripts/ci/go-test-tiers.json', 'scripts/ci/web-test-tiers.json', 'web/ci-web-shards.json']
const compare = (a, b) => a < b ? -1 : a > b ? 1 : 0
function comparable(value, path, shards) {
  if (Array.isArray(value)) {
    const rows = value.map(row => comparable(row, [...path, '*'], shards))
    const unordered = shards
      ? ['configs', 'ciInventory', 'duplicateCurrentSpecs', 'exclusions', 'groups.*.specs']
      : ['tests', 'postGateCases', 'deleteCandidateGroups']
    return unordered.includes(path.join('.')) ? rows.sort((a, b) => compare(JSON.stringify(a), JSON.stringify(b))) : rows
  }
  if (value !== null && typeof value === 'object') return Object.fromEntries(Object.keys(value).sort(compare).map(k => [k, comparable(value[k], [...path, k], shards)]))
  return value
}
function git(args) {
  const result = spawnSync('git', args, { cwd: root, encoding: 'utf8', maxBuffer: 32 * 1024 * 1024, timeout: 10_000 })
  if (result.error || result.status !== 0) throw new Error(`git ${args[0]} failed: ${result.error?.message ?? result.stderr}`)
  return result.stdout.trim()
}
export function proveConversion(before, after, file) {
  if (!files.includes(file)) throw new Error(`Unsupported proof manifest: ${file}`)
  const shards = file === files[2]
  if (shards) {
    // Check file/weight pairs first so this diagnostic is reachable. The full
    // comparison below also retains them, along with all other row metadata.
    const weights = data => data.groups.flatMap(group => group.specs.map(spec => [spec.file, spec.weightSeconds]))
      .sort((a, b) => compare(a[0], b[0]))
    assert.deepEqual(weights(after), weights(before), `${file}: per-file spec weights changed`)
  }
  // Independent exception conversion oracle: no formatter/normalizer helpers.
  const exceptions = data => {
    assert.ok(data.implicitTier === 'GATED-FULL' || data.defaultNewTier === 'NIGHTLY' ||
      (!Object.hasOwn(data, 'implicitTier') && !Object.hasOwn(data, 'defaultNewTier')), 'Unexpected implicit tier')
    const { defaultNewTier, implicitTier, policy, ...rest } = data
    return { ...rest, tests: data.tests.filter(row => !(row.tier === 'GATED-FULL' && Object.keys(row).every(field =>
      ['kind', row.kind === 'go' ? 'package' : 'file', 'name', 'tier', ...(row.kind === 'browser' ? ['config', 'project'] : [])].includes(field)))) }
  }
  if (!shards) {
    assert.equal(after.policy, before.policy?.replace('GATED-FULL preserves the prior Go/unit/browser gate.', 'GATED-FULL is implicit for every unlisted discovery; manifests contain tier exceptions and scheduling/deletion metadata.')
      .replace('Runtime reconciliation warns, defaults new cases to NIGHTLY and drops stale entries.', 'Runtime reconciliation gates new cases without warnings and drops stale explicit entries with warnings; implicit browser routing follows native discovery.'), `${file}: policy changed unexpectedly`)
  }
  assert.deepEqual(comparable(shards ? after : exceptions(after), [], shards), comparable(shards ? before : exceptions(before), [], shards), `${file}: conversion changed data`)
  return { ...(shards ? { perFileWeightsUnchanged: true } : {}), file, rows: shards ? after.groups.reduce((n, g) => n + g.specs.length, 0) : after.tests.length,
    removed: shards ? 0 : before.tests.length - after.tests.length, tiersWeightsAndMetadataUnchanged: true }
}
export function proveManifests(base = git(['rev-parse', 'HEAD'])) {
  if (!/^[a-f0-9]{40}$/.test(base)) throw new Error('Proof base must be a full local commit SHA')
  return { base, files: files.map((file, i) => {
    const before = JSON.parse(git(['show', `${base}:${file}`]))
    const after = JSON.parse(readFileSync(resolve(root, file), 'utf8'))
    // Set-like list order alone may differ. Duplicates are NOT discarded, and
    // group order, flags, history arrays and every scalar remain significant.
    return proveConversion(before, after, file)
  }) }
}

export async function proveInventory(base, inventories = { go: collectGo(), web: collectWeb() }) {
  const conversion = proveManifests(base)
  const oldSource = git(['show', `${base}:scripts/test-tiers/core.mjs`]).replaceAll("'./migration.mjs'", JSON.stringify(new URL('./migration.mjs', import.meta.url).href))
  const old = await import(`data:text/javascript;base64,${Buffer.from(oldSource).toString('base64')}`)
  const browserPolicy = JSON.parse(readFileSync(resolve(root, 'web/ci-web-shards.json'), 'utf8'))
  const goWeights = new Map(readFileSync(resolve(root, 'scripts/ci/go-shards.txt'), 'utf8').split('\n').flatMap(line => {
    const match = /^\d+ (\d+) github\.com\/inspr-at\/paimos\/(\S+)/.exec(line)
    return match ? [[match[2], Number(match[1]) / 1000]] : []
  }))
  const result = []
  const knownFlaky = JSON.parse(readFileSync(new URL('../ci/known-flaky.json', import.meta.url), 'utf8'))
  for (const kind of ['go', 'web']) {
    const file = `scripts/ci/${kind}-test-tiers.json`
    const before = JSON.parse(git(['show', `${base}:${file}`])), after = JSON.parse(readFileSync(resolve(root, file), 'utf8'))
    const inventory = inventories[kind]
    const previous = old.validate(before, inventory.tests, knownFlaky, { warn: () => {} })
    const next = validate(after, inventory.tests, undefined, { warn: () => {} })
    const explicit = new Set(before.tests.map(key)), newlyGated = []
    previous.forEach((row, i) => {
      assert.equal(key(row), key(next[i]))
      if (row.tier !== next[i].tier) {
        assert.ok(!explicit.has(key(row)) && row.tier === 'NIGHTLY' && next[i].tier === 'GATED-FULL', `Unexpected tier change: ${key(row)}`)
        newlyGated.push(next[i])
      } else assert.deepEqual(next[i], row, `Metadata changed: ${key(row)}`)
    })
    const strengthened = new Set(newlyGated.map(key)), lanes = {}
    for (const [lane, options] of Object.entries({ full: { event: 'merge_group' }, essential: { event: 'pull_request', paths: ['README.md'] }, catalogue: { event: 'schedule' } })) {
      const selectedBefore = old.select(previous, options).tests.map(key).sort()
      const selectedAfter = select(next, options).tests.map(key).sort()
      const additions = selectedAfter.filter(id => !selectedBefore.includes(id))
      assert.deepEqual(selectedAfter.filter(id => !strengthened.has(id)), selectedBefore.filter(id => !strengthened.has(id)), `${kind} ${lane}: selection changed`)
      assert.deepEqual(additions, lane === 'full' ? [...strengthened].sort() : [], `${kind} ${lane}: unexpected additions`)
      lanes[lane] = { before: selectedBefore.length, after: selectedAfter.length, intendedAdditions: additions.length, otherwiseEqual: true }
    }
    const estimate = row => {
      if (row.active === false) return 0
      const owner = row.kind === 'go' ? row.package : row.file
      const timing = before.timingWeights?.owners?.[owner]
      if (timing) return timing.seconds / timing.selectedTests
      if (row.kind === 'go') return goWeights.get(owner) > 0 ? goWeights.get(owner) / next.filter(test => test.package === owner).length : 1
      if (row.kind === 'browser') {
        const spec = browserPolicy.groups.flatMap(group => group.specs).find(spec => spec.file === owner)
        if (spec?.tierTiming) return spec.tierTiming.seconds / spec.tierTiming.selectedTests
        return (spec?.weightSeconds ?? 60) / (spec?.listedTests ?? next.filter(test => test.file === owner).length)
      }
      return 1 // no measured owner: explicit one-second scheduling estimate
    }
    const full = select(next, {event:'merge_group'}).tests
    const pools = kind === 'go'
      ? [['go', full.filter(row=>row.lane!=='timing'), 7], ['timing', full.filter(row=>row.lane==='timing'), 1]]
      : [['unit', full.filter(row=>row.kind!=='browser'), 4], ['browser', full.filter(row=>row.kind==='browser'), 12]]
    const addedShardSeconds = Object.fromEntries(pools.map(([lane, rows, count]) => {
      const weights = lane === 'browser' ? tierWeights(browserPolicy, rows) : measuredWeights(before, rows)
      if (lane === 'go') for (const [owner, seconds] of goWeights) if (!Object.hasOwn(weights,owner))
        weights[owner] = seconds * rows.filter(row=>row.package===owner).length / next.filter(row=>row.package===owner).length
      const bins = Array.from({length:count}, (_, i)=>shard(rows,i+1,count,weights,{firstShardLast:lane==='unit'}))
      return [lane, bins.map(bin=>bin.filter(row=>strengthened.has(key(row))).reduce((sum,row)=>sum+estimate(row),0))]
    }))
    result.push({ kind, discovered: next.length, unchanged: next.length - newlyGated.length, newlyGated: newlyGated.map(key), lanes,
      addedSerialSecondsEstimate: newlyGated.reduce((sum, row) => sum + estimate(row), 0),
      addedShardSecondsEstimate: addedShardSeconds,
      maxAddedShardSecondsEstimate: Math.max(...Object.values(addedShardSeconds).flat()),
      estimateBasis: 'Hosted owner seconds / selectedTests; Go shard weights or browser tierTiming/file weight/count; unmeasured Go/unit case = 1s. Marginal serial/shard scheduling estimate; setup, DB contention, prior shard critical path and unmeasured bodies are not measured.' })
  }
  return { ...conversion, inventories: result }
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    if (process.argv.length > 3) throw new Error('Usage: node scripts/test-tiers/prove-manifests.mjs [FULL_BASE_SHA]')
    console.log(JSON.stringify(await proveInventory(process.argv[2] ?? git(['rev-parse', 'HEAD']))))
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
