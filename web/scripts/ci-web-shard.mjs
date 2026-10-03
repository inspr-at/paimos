// SPDX-License-Identifier: AGPL-3.0-only
// OPS-257: the manifest owns spec selection, launch policy and timing weights.
import { readFileSync, readdirSync, existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { resolve, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { runPlaywright } from '../../scripts/playwright-safe.mjs'

export const webRoot = fileURLToPath(new URL('../', import.meta.url))
export const manifestPath = resolve(webRoot, 'ci-web-shards.json')
const compare = (a, b) => a < b ? -1 : a > b ? 1 : 0

export function parseShard(value) {
  const match = /^(\d+)\/(\d+)$/.exec(value ?? '')
  if (!match) throw new Error('Expected a 1-based shard i/N, for example 1/12')
  const [, i, n] = match.map(Number)
  if (!Number.isSafeInteger(i) || !Number.isSafeInteger(n) || i < 1 || i > n || n > 256) {
    throw new Error('Shard must satisfy 1 <= i <= N <= 256')
  }
  return { index: i, count: n }
}

export function validateManifest(manifest) {
  if (manifest.version !== 1 || !Array.isArray(manifest.groups) || !manifest.groups.length) {
    throw new Error('Expected manifest version 1 with nonempty groups')
  }
  const specs = new Set(), ids = new Set()
  for (const group of manifest.groups) {
    if (!/^[a-z0-9-]+$/.test(group.id ?? '') || ids.has(group.id)) throw new Error(`Invalid or duplicate group: ${group.id}`)
    ids.add(group.id)
    if (!/^playwright(?:\.[a-z]+)?\.config\.ts$/.test(group.config ?? '')) throw new Error(`Invalid config in ${group.id}`)
    if (group.project !== null && typeof group.project !== 'string') throw new Error(`Invalid project in ${group.id}`)
    if (!Array.isArray(group.flags) || group.flags.some(flag => typeof flag !== 'string')) throw new Error(`Invalid flags in ${group.id}`)
    if (group.flags.includes('--')) throw new Error(`Unexpected npm argument separator in ${group.id}`)
    // These options could silently widen, omit or repartition the selected files.
    if (group.flags.some(flag => /^(?:--(?:shard|config|project|grep|grep-invert|list|last-failed|test-list)|-[cg])(?:=|$)/.test(flag))) {
      throw new Error(`Selection flags belong in the manifest fields, not flags: ${group.id}`)
    }
    if (typeof group.env !== 'object' || group.env === null || Object.values(group.env).some(v => typeof v !== 'string')) throw new Error(`Invalid env in ${group.id}`)
    if (typeof group.hostedOnly !== 'boolean' || !Array.isArray(group.specs) || !group.specs.length) throw new Error(`Invalid specs/policy in ${group.id}`)
    for (const spec of group.specs) {
      if (!/^(?:tests|e2e)\/[a-zA-Z0-9_/-]+\.spec\.ts$/.test(spec.file ?? '')) throw new Error(`Invalid spec path: ${spec.file}`)
      if (specs.has(spec.file)) throw new Error(`Duplicate spec: ${spec.file}`)
      specs.add(spec.file)
      if (!Number.isFinite(spec.weightSeconds) || spec.weightSeconds <= 0) throw new Error(`Invalid weight: ${spec.file}`)
    }
  }
  return manifest
}

export function loadManifest(path = manifestPath) {
  return validateManifest(JSON.parse(readFileSync(path, 'utf8')))
}

// Longest-processing-time scheduling. File names break weight ties, and the
// lowest shard index breaks load ties; manifest order cannot change assignment.
export function balanceShards(manifest, count) {
  validateManifest(manifest)
  parseShard(`1/${count}`)
  const shards = Array.from({ length: count }, (_, i) => ({ index: i + 1, weightSeconds: 0, specs: [] }))
  const specs = manifest.groups.flatMap(group => group.specs.map(spec => ({ ...spec, groupId: group.id })))
    .sort((a, b) => b.weightSeconds - a.weightSeconds || compare(a.file, b.file))
  for (const spec of specs) {
    const shard = shards.reduce((best, candidate) => candidate.weightSeconds < best.weightSeconds ? candidate : best)
    shard.specs.push(spec)
    shard.weightSeconds += spec.weightSeconds
  }
  for (const shard of shards) shard.specs.sort((a, b) => compare(a.file, b.file))
  return shards
}

export function discoverSpecs(root = webRoot, directories = ['tests']) {
  const files = []
  const visit = dir => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = resolve(dir, entry.name)
      if (entry.isDirectory()) visit(path)
      else if (entry.isFile() && entry.name.endsWith('.spec.ts')) files.push(relative(root, path).replaceAll('\\', '/'))
    }
  }
  for (const dir of directories) visit(resolve(root, dir))
  return files.sort(compare)
}

export function checkCoverage(manifest, shards, expectedSpecs) {
  validateManifest(manifest)
  const expected = new Set(expectedSpecs)
  const declared = new Set(manifest.groups.flatMap(group => group.specs.map(spec => spec.file)))
  const owners = new Map(manifest.groups.flatMap(group => group.specs.map(spec => [spec.file, group.id])))
  const assigned = new Set()
  for (const shard of shards) {
    for (const spec of shard.specs) {
      if (assigned.has(spec.file)) throw new Error(`Spec appears in two shards: ${spec.file}`)
      if (!declared.has(spec.file)) throw new Error(`Undeclared shard spec: ${spec.file}`)
      if (owners.get(spec.file) !== spec.groupId) throw new Error(`Wrong group for shard spec: ${spec.file}`)
      assigned.add(spec.file)
    }
  }
  for (const file of declared) if (!assigned.has(file)) throw new Error(`Spec missing from shards: ${file}`)
  for (const file of expected) if (!declared.has(file)) throw new Error(`Spec missing from manifest: ${file}`)
  for (const file of declared) if (!expected.has(file)) throw new Error(`Stale manifest spec: ${file}`)
  return { specs: assigned.size, shards: shards.length }
}

export function retryCount(env) {
  const value = env.PW_RETRIES ?? '0'
  if (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value))) throw new Error('PW_RETRIES must be a nonnegative integer')
  return Number(value)
}

export function planCommands(manifest, shard, env = process.env, root = webRoot) {
  const retries = retryCount(env)
  return [...manifest.groups].sort((a, b) => compare(a.id, b.id)).flatMap(group => {
    const files = shard.specs.filter(spec => spec.groupId === group.id).map(spec => spec.file)
    if (!files.length) return []
    const groupEnv = Object.fromEntries(Object.entries(group.env).map(([key, value]) => [key,
      value.replaceAll('${RUNNER_TEMP}', env.RUNNER_TEMP ?? resolve(webRoot, 'test-results', 'ci-web-evidence')),
    ]))
    return [{ group: group.id, hostedOnly: group.hostedOnly, files, env: groupEnv,
      // Playwright positional args are regexes over absolute file paths. Anchor
      // the entire path so nested tests/tests/foo cannot also match tests/foo.
      args: ['--config', group.config, ...(group.project ? ['--project', group.project] : []),
        ...group.flags, `--retries=${retries}`,
        '--output', `test-results/ci-web/shard-${shard.index}/${group.id}`,
        '--last-failed-file', resolve(root, 'test-results', 'ci-web', `shard-${shard.index}`, group.id, '.last-run.json'),
        ...files.map(file => `^${resolve(root, file).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`)],
    }]
  })
}

export function parseArgs(args, defaultCount = 12) {
  let shard, mode = 'run', count = parseShard(`1/${defaultCount}`).count
  let lastFailed = false, testList, reporter
  for (let i = 0; i < args.length; i++) {
    const arg = args[i]
    if (arg === '--list' || arg === '--check') {
      if (mode !== 'run') throw new Error('Choose only one of --list and --check')
      mode = arg.slice(2)
    } else if (arg === '--shard') {
      if (shard) throw new Error('Specify one shard')
      shard = parseShard(args[++i])
    } else if (arg === '--shards') {
      const value = args[++i]
      if (!/^\d+$/.test(value ?? '')) throw new Error('--shards requires N')
      count = parseShard(`1/${value}`).count
    } else if (arg === '--last-failed') {
      lastFailed = true
    } else if (arg === '--test-list' || arg.startsWith('--test-list=')) {
      testList = arg.includes('=') ? arg.slice('--test-list='.length) : args[++i]
      if (!testList || testList.startsWith('--')) throw new Error('--test-list requires a file')
    } else if (arg === '--reporter' || arg.startsWith('--reporter=')) {
      reporter = arg.includes('=') ? arg.slice('--reporter='.length) : args[++i]
      if (!reporter || reporter.split(',').some(r => !['line', 'list', 'json', 'github', 'dot'].includes(r))) throw new Error('Unsupported --reporter')
    } else if (/^\d+\/\d+$/.test(arg)) {
      if (shard) throw new Error('Specify one shard')
      shard = parseShard(arg)
    } else throw new Error(`Unknown argument: ${arg}`)
  }
  if (mode === 'run' && !shard) throw new Error('Running requires --shard i/N (or positional i/N)')
  if (lastFailed && testList) throw new Error('Choose one of --last-failed and --test-list')
  if (mode !== 'run' && (lastFailed || testList || reporter)) throw new Error('Retry/reporter options require run mode')
  return { mode, shard, count: shard?.count ?? count,
    ...(lastFailed ? { lastFailed } : {}), ...(testList ? { testList } : {}), ...(reporter ? { reporter } : {}),
  }
}

// A shard can span configs and policies. Preserve the standard Playwright JSON
// reporter interface for a wrapper instead of losing all but the last group.
export function mergeReports(reports) {
  if (!reports.length) return { config: {}, suites: [], errors: [], stats: {} }
  return {
    config: reports[0].config,
    suites: reports.flatMap(r => r.suites ?? []),
    errors: reports.flatMap(r => r.errors ?? []),
    stats: { startTime: reports[0].stats?.startTime,
      ...Object.fromEntries(['duration', 'expected', 'skipped', 'unexpected', 'flaky'].map(key => [key,
        reports.reduce((sum, r) => sum + (r.stats?.[key] ?? 0), 0),
      ])),
    },
  }
}

export async function main(args, { manifest = loadManifest(), env = process.env, run = runPlaywright, out = console.log, root = webRoot, reportDirectory,
  readLastRun = path => JSON.parse(readFileSync(path, 'utf8')),
} = {}) {
  const options = parseArgs(args, manifest.defaultShards ?? 12)
  const shards = balanceShards(manifest, options.count)
  const expected = discoverSpecs(root, manifest.specDirectories ?? ['tests'])
  const coverage = checkCoverage(manifest, shards, expected)
  for (const group of manifest.groups) if (!existsSync(resolve(root, group.config))) throw new Error(`Missing config: ${group.config}`)
  const plans = (options.shard ? [shards[options.shard.index - 1]] : shards).map(shard => ({ ...shard, commands: planCommands(manifest, shard, env, root) }))
  if (options.mode === 'check') {
    out(JSON.stringify({ ...coverage, weightSeconds: shards.map(s => Number(s.weightSeconds.toFixed(2))) }))
    return 0
  }
  if (options.mode === 'list') { out(JSON.stringify(plans, null, 2)); return 0 }
  // A full shard is a CI-only entry point. Local inspection remains browser-free.
  if (!env.CI || ['0', 'false'].includes(env.CI)) throw new Error('Run CI shards on hosted CI; use --list or --check locally')
  if (env.RUNNER_ENVIRONMENT !== 'github-hosted') throw new Error('CI web shards require RUNNER_ENVIRONMENT=github-hosted')
  const jsonReport = !!env.PLAYWRIGHT_JSON_OUTPUT_FILE || options.reporter?.split(',').includes('json')
  const aggregatePath = env.PLAYWRIGHT_JSON_OUTPUT_FILE ? resolve(root, env.PLAYWRIGHT_JSON_OUTPUT_FILE) : undefined
  if (aggregatePath) {
    mkdirSync(resolve(aggregatePath, '..'), { recursive: true })
    writeFileSync(aggregatePath, '')
  }
  const reports = []
  let commands = plans[0].commands
  if (options.lastFailed) {
    // Playwright falls back to the entire selection if its last-run file is
    // absent or malformed. Preflight every group, fail closed, and skip green
    // groups rather than accidentally retrying the complete shard.
    commands = commands.filter(command => {
      const path = command.args[command.args.indexOf('--last-failed-file') + 1]
      let info
      try { info = readLastRun(path) }
      catch { throw new Error(`Missing last-run state for ${command.group}; use --test-list for known failures`) }
      if (!info || !['passed', 'failed', 'timedout', 'interrupted'].includes(info.status) || !Array.isArray(info.failedTests) || info.failedTests.some(id => typeof id !== 'string')) {
        throw new Error(`Invalid last-run state for ${command.group}`)
      }
      if (!info.failedTests.length && info.status !== 'passed') throw new Error(`No selectable failed tests for ${command.group}`)
      return info.failedTests.length > 0
    })
  }
  let code = 0
  for (const command of commands) {
    out(`CI web shard ${options.shard.index}/${options.count}: ${command.group} (${command.files.join(', ')})`)
    const childEnv = { ...env, ...command.env }
    let reporter = options.reporter ?? (jsonReport ? 'line,json' : undefined)
    if (jsonReport && !reporter.split(',').includes('json')) reporter += ',json'
    const extraArgs = [
      ...(options.lastFailed ? ['--last-failed', '--pass-with-no-tests'] : []),
      ...(options.testList ? ['--test-list', resolve(root, options.testList), '--pass-with-no-tests'] : []),
      ...(reporter ? ['--reporter', reporter] : []),
    ]
    let reportPath
    if (jsonReport) {
      reportPath = resolve(reportDirectory ?? resolve(root, 'test-results', 'ci-web-reports'), `shard-${options.shard.index}`, `${command.group}.json`)
      mkdirSync(resolve(reportPath, '..'), { recursive: true })
      // Invalidate prior output before launch; startup failures must not inherit
      // a green report from a previous attempt.
      writeFileSync(reportPath, '')
      childEnv.PLAYWRIGHT_JSON_OUTPUT_FILE = reportPath
    }
    const result = await run([...command.args, ...extraArgs], { cwd: root, env: childEnv })
    if (result.code) code = result.code
    if (reportPath) {
      try {
        const report = JSON.parse(readFileSync(reportPath, 'utf8'))
        if (!report || !Array.isArray(report.suites) || !Array.isArray(report.errors) || !report.stats || typeof report.stats !== 'object') throw new Error('Invalid report schema')
        reports.push(report)
      }
      catch {
        code ||= 1
        reports.push({ suites: [], errors: [{ message: `Missing or invalid Playwright JSON report for ${command.group}` }], stats: {} })
      }
    }
    // Failed groups must not suppress later groups; interrupted jobs must stop.
    if ([130, 143, 129].includes(result.code)) return result.code
  }
  if (jsonReport) {
    const report = JSON.stringify(mergeReports(reports))
    if (aggregatePath) writeFileSync(aggregatePath, `${report}\n`)
    else out(report)
  }
  return code
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { process.exitCode = await main(process.argv.slice(2)) }
  catch (error) { console.error(error.message); process.exitCode = 1 }
}
