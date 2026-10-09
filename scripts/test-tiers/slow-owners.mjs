// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { collectGo, command, flattenBrowser, web } from './collect.mjs'
import { key, exactPattern, select, shard, splitOwner } from './core.mjs'
import { load, plan } from './cli.mjs'
import { loadManifest, tierWeights } from '../../web/scripts/ci-web-shard.mjs'

export const baseline = JSON.parse(readFileSync(new URL('../ci/slow-owner-baseline.json', import.meta.url), 'utf8'))

// Compare multisets and reject duplicates independently: a duplicate may not
// compensate for a missing case. Browser identities retain policy and tier.
export function provePartition(before, bins, identity = key) {
  const expected = before.map(identity).sort(), actual = bins.flat().map(identity).sort()
  if (new Set(expected).size !== expected.length || new Set(actual).size !== actual.length)
    throw new Error('Duplicate case in split inventory')
  if (JSON.stringify(expected) !== JSON.stringify(actual)) throw new Error('Split inventory changed')
  return actual.length
}

export function browserIdentity(row) {
  return JSON.stringify([baseline.browserOwners[row.file] ?? row.file, row.name, row.occurrence,
    row.config, row.project, row.tier, row.tags ?? []])
}

export function proveSlowOwners(browserRows, goRows) {
  const browser = browserRows.filter(row => Object.hasOwn(baseline.browserOwners, row.file))
  const browserCases = provePartition(baseline.browserCases, [browser], browserIdentity)
  const nodes = goRows.filter(row => row.package === 'internal/nodes' && row.active !== false)
  const goCases = provePartition(baseline.goCases, [nodes], row => JSON.stringify([key(row), row.tier, row.lane]))
  const manifest = load('go'), policy = loadManifest(), browserLoads = []
  for (const [file, original] of Object.entries(baseline.browserOwners)) {
    const group = policy.groups.find(group => group.specs.some(spec => spec.file === file))
    if (!group) throw new Error(`Missing split launch policy: ${file}`)
    const launch = Object.fromEntries(['config', 'project', 'flags', 'env', 'hostedOnly'].map(field => [field, group[field]]))
    launch.gate = group.gate !== false
    if (JSON.stringify(launch) !== JSON.stringify(baseline.browserPolicies[original])) throw new Error(`Changed split launch policy: ${file}`)
    const spec = group.specs.find(spec => spec.file === file)
    if (spec.listedTests !== browser.filter(row => row.file === file).length || spec.tierTiming.selectedTests !== spec.listedTests)
      throw new Error(`Split timing count differs from native cases: ${file}`)
  }
  // Exercise the production planner, including one shard and partial/essential
  // selections. Timing-lane cases remain on their separate unchanged lane.
  for (const event of ['pull_request', 'merge_group', 'push', 'schedule']) {
    for (const count of [1, 2, 7]) {
      const options = { event, paths: ['.github/workflows/ci.yml'], inventory: { tests: goRows, imports: {} }, count }
      const expected = select(goRows, options).tests.filter(row => row.lane !== 'timing')
      const bins = Array.from({ length: count }, (_, i) => plan('go', { ...options, index: i + 1 }).tests)
      provePartition(expected, bins)
      const halves = bins.map(bin => bin.filter(row => row.package === 'internal/nodes')).filter(bin => bin.length)
      if (count > 1 && halves.length !== 2) throw new Error('Nodes must occupy exactly two shards')
      const selectedNodes = expected.filter(row => row.package === 'internal/nodes')
      // Go -run is a real regex with slash semantics; all selected names are
      // top-level names, and these disjoint exact patterns retain all subtests.
      const patterns = halves.map(rows => new RegExp(exactPattern(rows.map(row => row.name))))
      for (const row of nodes.filter(row => row.lane !== 'timing')) {
        const matches = patterns.filter(pattern => pattern.test(row.name)).length
        if (matches !== Number(selectedNodes.some(selected => key(selected) === key(row))))
          throw new Error(`Incorrect -run coverage: ${row.name}`)
      }
    }
    const selected = select(browserRows, { event, paths: ['.github/workflows/ci.yml'] }).tests.filter(row => row.kind === 'browser')
    const weights = tierWeights(policy, selected)
    const bins = Array.from({ length: 12 }, (_, i) => shard(selected, i + 1, 12, weights))
    provePartition(selected, bins)
    const loads = bins.map(bin => [...new Set(bin.map(row => row.file))].reduce((n, file) => n + weights[file], 0))
    const sorted = [...loads].sort((a, b) => a - b)
    if (event !== 'schedule' && sorted.at(-1) > 1.25 * (sorted[5] + sorted[6]) / 2) throw new Error('Browser shard weight imbalance')
    browserLoads.push({ event, seconds: loads.map(seconds => +seconds.toFixed(3)) })
  }
  const selectedNodes = nodes.filter(row => row.lane !== 'timing' && row.tier !== 'NIGHTLY')
  const nodeLoads = splitOwner(selectedNodes, manifest.timingWeights.owners['internal/nodes'], 2).map(part => +part.weight.toFixed(3))
  return { browserCases, goCases, nodeFullGateCases: selectedNodes.length, nodeSeconds: nodeLoads, browserLoads }
}

export function main() {
  const files = Object.keys(baseline.browserOwners)
  const native = flattenBrowser(JSON.parse(command(process.execPath, ['node_modules/@playwright/test/cli.js', 'test',
    '-c', 'playwright.ui.config.ts', ...files, '--list', '--reporter=json'], { cwd: web })), 'playwright.ui.config.ts')
  const manifest = load('web')
  const rows = native.map(row => {
    const stored = manifest.tests.find(test => key(test) === key(row))
    if (!stored) throw new Error(`Missing native browser classification: ${key(row)}`)
    return { ...row, ...stored }
  })
  const goManifest = load('go'), goRows = collectGo().tests.map(row => {
    const stored = goManifest.tests.find(test => key(test) === key(row))
    if (!stored && row.package === 'internal/nodes') throw new Error(`Missing native Go classification: ${key(row)}`)
    return { ...row, ...(stored ?? { tier: 'NIGHTLY' }) }
  })
  // Whole browser ledger checks the twelve-shard weight balance as well as
  // the targeted native inventory equality above.
  const rest = manifest.tests.filter(row => !Object.hasOwn(baseline.browserOwners, row.file))
  console.log(JSON.stringify(proveSlowOwners([...rest, ...rows], goRows)))
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { main() } catch (error) { console.error(error.message); process.exitCode = 1 }
}
