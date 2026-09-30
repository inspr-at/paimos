// SPDX-License-Identifier: AGPL-3.0-only
import { spawnSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const web = resolve(root, 'web')
const executable = resolve(web, 'node_modules/.bin/playwright')
const projects = ['--project=ui', '--project=quarantine', '--project=audit']

// Keep ordinary files together so their setup and teardown remain serial. The
// audit already creates a fresh context for each state, so its states can be
// distributed alongside files instead of needing more CI jobs.
export function testGroups(report) {
  if (report.errors?.length) throw new Error(JSON.stringify(report.errors))
  const groups = new Map()
  function visit(suite, parents = []) {
    const titles = suite.line ? [...parents, suite.title] : parents
    for (const spec of suite.specs ?? []) {
      const audit = spec.file === 'ui-audit.spec.ts'
      const key = audit ? `${spec.file}::${spec.title}` : spec.file
      let group = groups.get(key)
      if (!group) {
        group = { key, ids: [], selectors: new Set() }
        groups.set(key, group)
      }
      for (const test of spec.tests) {
        group.ids.push(`${spec.id}:${test.projectName}`)
        group.selectors.add(`[${test.projectName}] › ${spec.file}${audit ? ` › ${[...titles, spec.title].join(' › ')}` : ''}`)
      }
    }
    for (const child of suite.suites ?? []) visit(child, titles)
  }
  for (const suite of report.suites) visit(suite)
  return [...groups.values()].map(group => ({ ...group, selectors: [...group.selectors] }))
}

export function balance(groups, timings, count) {
  if (!Number.isInteger(count) || count < 1 || count > groups.length) throw new Error('Invalid shard count')
  const bins = Array.from({ length: count }, () => ({ groups: [], durationMs: 0 }))
  const weighted = groups.map(group => ({ ...group, durationMs: timings[group.key] ?? group.ids.length * 1000 }))
  for (const group of weighted) {
    if (!Number.isFinite(group.durationMs) || group.durationMs <= 0) throw new Error(`Invalid timing for ${group.key}`)
  }
  weighted.sort((a, b) => b.durationMs - a.durationMs || a.key.localeCompare(b.key, 'en'))
  for (const group of weighted) {
    const target = bins.reduce((lightest, bin) => bin.durationMs < lightest.durationMs ? bin : lightest)
    target.groups.push(group)
    target.durationMs += group.durationMs
  }
  return bins
}

function playwright(args, capture = false) {
  const result = spawnSync(executable, ['test', '-c', 'playwright.ui.config.ts', ...projects, ...args], {
    cwd: web, encoding: 'utf8', stdio: capture ? 'pipe' : 'inherit', maxBuffer: 64 * 1024 * 1024,
  })
  if (result.error) throw result.error
  if (capture && result.status !== 0) throw new Error(result.stderr || result.stdout)
  return result
}

function listed(args = []) {
  return testGroups(JSON.parse(playwright([...args, '--list', '--reporter=json'], true).stdout))
}

function selection(bin, index) {
  const directory = resolve(root, 'tmp/playwright-ui')
  mkdirSync(directory, { recursive: true })
  const path = resolve(directory, `shard-${index}.txt`)
  writeFileSync(path, `${bin.groups.flatMap(group => group.selectors).join('\n')}\n`)
  return path
}

function verify(bin, path) {
  const actual = listed([`--test-list=${path}`]).flatMap(group => group.ids).sort()
  const expected = bin.groups.flatMap(group => group.ids).sort()
  if (JSON.stringify(actual) !== JSON.stringify(expected)) throw new Error(`Playwright test selection differs for ${path}`)
}

function main() {
  const args = process.argv.slice(2)
  const shard = args.find(arg => arg.startsWith('--shard='))?.slice(8) ?? '1/8'
  if (!/^\d+\/\d+$/.test(shard)) throw new Error(`Invalid shard: ${shard}`)
  const [index, count] = shard.split('/').map(Number)
  if (index < 1 || index > count) throw new Error(`Invalid shard: ${shard}`)
  if (args.some(arg => !arg.startsWith('--shard=') && arg !== '--plan' && arg !== '--check')) throw new Error('Unknown argument')
  const groups = listed()
  const { durationMs } = JSON.parse(readFileSync(resolve(web, 'playwright.ui.weights.json'), 'utf8'))
  const bins = balance(groups, durationMs, count)
  bins.forEach((bin, i) => console.log(`UI shard ${i + 1}/${count}: ${bin.groups.reduce((sum, group) => sum + group.ids.length, 0)} tests; estimated ${(bin.durationMs / 1000).toFixed(1)}s`))
  if (args.includes('--check')) {
    bins.forEach((bin, i) => verify(bin, selection(bin, i + 1)))
    console.log(`Verified exact coverage: ${groups.reduce((sum, group) => sum + group.ids.length, 0)} tests, once each across ${count} shards`)
    return
  }
  if (args.includes('--plan')) return
  const bin = bins[index - 1]
  const path = selection(bin, index)
  verify(bin, path)
  const retries = process.env.PW_NIGHTLY === '1' ? ['--retries=0'] : []
  process.exitCode = playwright([`--test-list=${path}`, '--workers=1', ...retries]).status ?? 1
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main()
