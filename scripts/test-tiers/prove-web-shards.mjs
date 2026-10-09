// SPDX-License-Identifier: AGPL-3.0-only
import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { collectWeb, command, evidence, flattenBrowser, saveJSON, web } from './collect.mjs'
import { browserList, plan } from './cli.mjs'
import { key } from './core.mjs'
import { browserBatches } from './web-batches.mjs'
import { loadManifest, parseShard } from '../../web/scripts/ci-web-shard.mjs'

// Native collection proof for an entire full web layout. No browser cases run:
// --list resolves every old/new test-list against Playwright registration. CI
// measurement browserCases separately records the actual execution identities.
export function proveWebShards({ count = 12, all = false, env = process.env } = {}) {
  parseShard(`1/${count}`)
  if (count > 64) throw new Error('Tier layouts support at most 64 shards')
  const start = performance.now(), before = collectWeb({ fresh: true })
  const beforeCollectSeconds = (performance.now() - start) / 1000
  const afterStart = performance.now(), after = collectWeb({ fresh: true, browserOnly: true })
  const afterCollectSeconds = (performance.now() - afterStart) / 1000
  const identity = row => JSON.stringify([key(row), row.id, row.project, row.config])
  const same = (left, right, label) => {
    const sorted = rows => rows.map(identity).sort()
    if (new Set(left.map(identity)).size !== left.length || new Set(right.map(identity)).size !== right.length ||
        JSON.stringify(sorted(left)) !== JSON.stringify(sorted(right))) throw new Error(`Web shard set equality failed: ${label}`)
  }
  same(before.tests.filter(row => row.kind === 'browser'), after.tests, 'catalogue')
  const policy = loadManifest(), shards = [], beforeRun = [], afterRun = []
  const nativeRows = (group, rows, catalogue, label) => {
    const stem = resolve(evidence, `web-shard-proof-${label}-${group.id}`), path = `${stem}.txt`
    saveJSON(`${stem}.expected.json`, rows.map(identity))
    writeFileSync(path, browserList(rows, catalogue).join('\n') + '\n')
    const groupEnv = Object.fromEntries(Object.entries(group.env).map(([name, value]) => [name, value.replaceAll('${RUNNER_TEMP}', env.RUNNER_TEMP ?? evidence)]))
    const begin = performance.now()
    const listed = JSON.parse(command(process.execPath, ['node_modules/@playwright/test/cli.js', 'test',
      '--config', group.config, ...group.flags, ...(group.project ? ['--project', group.project] : []),
      '--test-list', path, '--workers=1', '--retries=0', '--forbid-only', '--list', '--reporter=json'],
    { cwd: web, env: { ...env, ...groupEnv, PW_RETRIES: '0' } }))
    const seconds = (performance.now() - begin) / 1000
    const found = flattenBrowser(listed, group.config)
    same(rows, found, label)
    return { rows: found, seconds }
  }
  for (let index = 1; index <= count; index++) {
    const options = { full: true, all, event: 'merge_group', count, index }
    const oldPlan = plan('web', { ...options, inventory: before })
    const newPlan = plan('web', { ...options, inventory: after })
    same(oldPlan.tests, newPlan.tests, `selection ${index}/${count}`)
    let beforeListSeconds = 0, afterListSeconds = 0, beforeLaunches = 0
    const oldRows = [], newRows = []
    for (const group of policy.groups) {
      const files = new Set(group.specs.map(spec => spec.file))
      const rows = oldPlan.tests.filter(row => files.has(row.file))
      if (!rows.length) continue
      beforeLaunches++
      const listed = nativeRows(group, rows, oldPlan.all, `before-${index}`)
      oldRows.push(...listed.rows)
      beforeListSeconds += listed.seconds
    }
    const batches = browserBatches(newPlan.tests, policy)
    for (const group of batches) {
      const listed = nativeRows(group, group.rows, newPlan.all, `after-${index}`)
      newRows.push(...listed.rows)
      afterListSeconds += listed.seconds
    }
    same(oldRows, newRows, `native lists ${index}/${count}`)
    beforeRun.push(...oldRows)
    afterRun.push(...newRows)
    shards.push({ index, cases: newRows.length, specs: new Set(newRows.map(row => row.file)).size,
      beforeLaunches, afterLaunches: batches.length, beforeListSeconds, afterListSeconds })
  }
  same(beforeRun, afterRun, 'entire full layout')
  const report = { version: 1, proof: 'native-collection-set-equality', casesExecuted: false, all,
    catalogueCases: after.tests.length, selectedCases: afterRun.length,
    selectedSpecs: new Set(afterRun.map(row => row.file)).size, count,
    beforeCollectSeconds, afterCollectSeconds, shards,
    note: 'Full native inventory versus browser-only inventory; exact old/new native test-list identities across every shard. Times cover discovery only; use actual webShardTiming for execution overhead.' }
  saveJSON(resolve(evidence, 'web-shard-set-equality.json'), report)
  return report
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const args = process.argv.slice(2)
    if (args.some(arg => arg !== '--all')) throw new Error('Usage: prove-web-shards.mjs [--all]')
    console.log(JSON.stringify(proveWebShards({ all: args.includes('--all') })))
  } catch (error) { console.error(error.message); process.exitCode = 1 }
}
