// SPDX-License-Identifier: AGPL-3.0-only
// Full QA owns this assignment independently of ci.yml's tiered web shards.
import { spawn } from 'node:child_process'
import { readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { shard as assignShard } from './test-tiers/core.mjs'
import { discoverSpecs, parseShard, webRoot } from '../web/scripts/ci-web-shard.mjs'

export const assignmentPath = fileURLToPath(new URL('./ci/full-ui-qa-shards.json', import.meta.url))
export const loadAssignment = () => JSON.parse(readFileSync(assignmentPath, 'utf8'))
export const formatAssignment = assignment => JSON.stringify({ ...assignment, specs: [] }, null, 2)
  .replace('"specs": []', `"specs": [\n${assignment.specs.map(spec => `    ${JSON.stringify(spec)}`).join(',\n')}\n  ]`) + '\n'

export function fullUIInventory(root = webRoot) {
  const config = readFileSync(resolve(root, 'playwright.ui.config.ts'), 'utf8')
  // Fail closed if the config's file selection changes. This guard deliberately
  // includes nested visual specs and excludes only the production perf suite.
  for (const [key, value] of Object.entries({ testDir: './tests', testMatch: '**/*.spec.ts', testIgnore: '**/performance.spec.ts' })) {
    const values = [...config.matchAll(new RegExp(`^\\s*${key}: ([^\\n]+)`, 'gm'))].map(match => match[1])
    if (values.length !== 1 || values[0] !== `'${value}',`) throw new Error(`Full QA inventory must track config ${key}`)
  }
  return discoverSpecs(root).filter(file => !file.endsWith('/performance.spec.ts'))
}

export function checkAssignment(assignment, expected = fullUIInventory()) {
  if (assignment.version !== 1 || assignment.shards !== 5 || !Array.isArray(assignment.specs) ||
      !assignment.specs.length || assignment.specs.length > 10_000 ||
      !Number.isFinite(assignment.setupSeconds) || assignment.setupSeconds < 0 || assignment.targetMinutes !== 60) {
    throw new Error('Expected five Full QA shards with a setup reserve and 60-minute target')
  }
  const found = new Set(expected), seen = new Set()
  for (const spec of assignment.specs) {
    if (seen.has(spec.file)) throw new Error(`Duplicate Full QA spec: ${spec.file}`)
    if (!found.has(spec.file)) throw new Error(`Stale or unexpected Full QA spec: ${spec.file}`)
    if (!Number.isInteger(spec.shard) || spec.shard < 1 || spec.shard > 5 ||
        !Number.isFinite(spec.seconds) || spec.seconds < 0) throw new Error(`Invalid Full QA assignment: ${spec.file}`)
    seen.add(spec.file)
  }
  for (const file of expected) if (!seen.has(file)) throw new Error(`Unassigned Full QA spec: ${file}`)
  const projected = Array.from({ length: 5 }, (_, index) => {
    const specs = assignment.specs.filter(spec => spec.shard === index + 1)
    if (!specs.length) throw new Error(`Empty Full QA shard: ${index + 1}`)
    const seconds = assignment.setupSeconds + specs.reduce((sum, spec) => sum + spec.seconds, 0)
    return { shard: index + 1, files: specs.length, minutes: Number((seconds / 60).toFixed(3)),
      withinTarget: seconds <= assignment.targetMinutes * 60,
      audits: specs.filter(spec => /^tests\/ui-audit(?:-(?:dark|light)-\d+)?\.spec\.ts$/.test(spec.file)).map(spec => spec.file) }
  })
  return { specs: seen.size, projected }
}

// Reuse the canonical whole-owner writer, with Full QA's own measured weights.
// No gate, tier, executionJob or exclusion policy can narrow this inventory.
export function rebalance(assignment) {
  const weights = Object.fromEntries(assignment.specs.map(spec => [spec.file, spec.seconds]))
  const owners = assignment.specs.map(spec => ({ kind: 'browser', file: spec.file }))
  const placements = new Map()
  for (let index = 1; index <= 5; index++) {
    for (const spec of assignShard(owners, index, 5, weights)) placements.set(spec.file, index)
  }
  return { ...assignment, specs: assignment.specs.map(spec => ({ ...spec, shard: placements.get(spec.file) }))
    .sort((a, b) => a.file.localeCompare(b.file)) }
}

export function shardArgs(assignment, partition, root = webRoot) {
  const { index, count } = parseShard(partition)
  if (count !== assignment.shards) throw new Error('Full QA requires shard i/5')
  return ['test', '--config=playwright.ui.config.ts', '--workers=1', '--retries=0', '--forbid-only',
    '--trace=retain-on-failure', '--reporter=github,line,json',
    ...assignment.specs.filter(spec => spec.shard === index).map(spec =>
      `^${resolve(root, spec.file).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`)]
}

function runHosted(args, { env, root }) {
  return new Promise((done, reject) => {
    const child = spawn(process.execPath, [resolve(root, 'node_modules/@playwright/test/cli.js'), ...args],
      { cwd: root, env, stdio: 'inherit' })
    child.once('error', reject)
    child.once('exit', (code, signal) => done(code ?? ({ SIGINT: 130, SIGTERM: 143, SIGHUP: 129 }[signal] ?? 1)))
  })
}

export async function main(args, { assignment = loadAssignment(), expected, root = webRoot, env = process.env,
  out = console.log, run = runHosted } = {}) {
  const report = checkAssignment(assignment, expected ?? fullUIInventory(root))
  if (args.length === 1 && args[0] === '--check') { out(JSON.stringify(report)); return 0 }
  if (args.length === 1 && args[0] === '--rebalance') {
    const next = rebalance(assignment)
    checkAssignment(next, expected ?? fullUIInventory(root))
    writeFileSync(assignmentPath, formatAssignment(next))
    return 0
  }
  if (args.length !== 1) throw new Error('Usage: full-ui-qa-shard.mjs i/5 | --check | --rebalance')
  const command = shardArgs(assignment, args[0], root)
  // Never launch a browser on a worker's Mac through this CI-only entry point.
  if (!env.CI || ['0', 'false'].includes(env.CI) || env.RUNNER_ENVIRONMENT !== 'github-hosted') {
    throw new Error('Full QA shards require GitHub-hosted CI; use --check locally')
  }
  out(JSON.stringify(report.projected[parseShard(args[0]).index - 1]))
  return run(command, { env, root })
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = await main(process.argv.slice(2)) }
  catch (error) { console.error(error.message); process.exitCode = 1 }
}
