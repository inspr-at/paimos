// SPDX-License-Identifier: AGPL-3.0-only
// OPS-257: the manifest owns spec selection, launch policy and timing weights.
import { readFileSync, readdirSync, existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { resolve, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { runPlaywright } from '../../scripts/playwright-safe.mjs'
import { validatePath } from '../../scripts/ci-pr-plan.mjs'

export const webRoot = fileURLToPath(new URL('../', import.meta.url))
export const manifestPath = resolve(webRoot, 'ci-web-shards.json')
const compare = (a, b) => a < b ? -1 : a > b ? 1 : 0
const specPath = /^(?:tests|e2e)\/[a-zA-Z0-9_/-]+\.spec\.ts$/

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
    if (group.gate !== undefined && typeof group.gate !== 'boolean') throw new Error(`Invalid gate in ${group.id}`)
    if (typeof group.hostedOnly !== 'boolean' || !Array.isArray(group.specs) || !group.specs.length) throw new Error(`Invalid specs/policy in ${group.id}`)
    for (const spec of group.specs) {
      if (!specPath.test(spec.file ?? '')) throw new Error(`Invalid spec path: ${spec.file}`)
      if (specs.has(spec.file)) throw new Error(`Duplicate spec: ${spec.file}`)
      specs.add(spec.file)
      if (!Number.isFinite(spec.weightSeconds) || spec.weightSeconds <= 0) throw new Error(`Invalid weight: ${spec.file}`)
    }
  }
  if (manifest.exclusions !== undefined && !Array.isArray(manifest.exclusions)) throw new Error('Expected an exclusions array')
  const excluded = new Set()
  for (const exclusion of manifest.exclusions ?? []) {
    if (!specPath.test(exclusion?.file ?? '')) throw new Error(`Invalid exclusion path: ${exclusion?.file}`)
    if (!/^[A-Z][A-Z0-9]*-[1-9][0-9]*$/.test(exclusion.ticket ?? '')) throw new Error(`Exclusion needs a ticket key: ${exclusion.file}`)
    if (typeof exclusion.reason !== 'string' || !exclusion.reason.trim()) throw new Error(`Exclusion needs a reason: ${exclusion.file}`)
    if (specs.has(exclusion.file)) throw new Error(`Spec both declared and excluded: ${exclusion.file}`)
    if (excluded.has(exclusion.file)) throw new Error(`Duplicate exclusion: ${exclusion.file}`)
    excluded.add(exclusion.file)
  }
  return manifest
}

// Check the source manifest BEFORE reconciliation, so a new spec cannot quietly
// become an ungated synthetic entry. Unit CI requires an owner or ticketed exclusion.
export function checkSpecInventory(manifest, discovered) {
  validateManifest(manifest)
  const found = new Set(discovered)
  const declared = new Set(manifest.groups.flatMap(group => group.specs.map(spec => spec.file)))
  const excluded = new Set((manifest.exclusions ?? []).map(exclusion => exclusion.file))
  for (const file of found) if (!declared.has(file) && !excluded.has(file)) throw new Error(`Spec missing from manifest or ticketed exclusions: ${file}`)
  for (const file of [...declared, ...excluded]) if (!found.has(file)) throw new Error(`Stale manifest spec or exclusion: ${file}`)
  return { specs: declared.size, excluded: excluded.size }
}

export function loadManifest(path = manifestPath) {
  const manifest = validateManifest(JSON.parse(readFileSync(path, 'utf8')))
  // `unlisted` is the synthetic group reconcileManifest adds for specs the manifest does not know.
  if (manifest.groups.some(group => group.id === 'unlisted')) throw new Error('Manifest must not declare the reserved group id: unlisted')
  return manifest
}

// Longest-processing-time scheduling, then bounded moves/swaps to reduce its
// longest shard. File names break weight ties, and the lowest shard index
// breaks load ties; manifest order cannot change assignment.
// Groups with gate:false are declared (so drift is still caught) but only run with all:true.
export const gatedGroups = (manifest, all = false) => manifest.groups.filter(group => all || group.gate !== false)

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

export function balanceShards(manifest, count, { all = false } = {}) {
  validateManifest(manifest)
  parseShard(`1/${count}`)
  const shards = Array.from({ length: count }, (_, i) => ({ index: i + 1, weightSeconds: 0, specs: [] }))
  const specs = gatedGroups(manifest, all).flatMap(group => group.specs.map(spec => ({ ...spec, groupId: group.id })))
    .sort((a, b) => b.weightSeconds - a.weightSeconds || compare(a.file, b.file))
  for (const spec of specs) {
    const shard = shards.reduce((best, candidate) => candidate.weightSeconds < best.weightSeconds ? candidate : best)
    shard.specs.push(spec)
    shard.weightSeconds += spec.weightSeconds
  }
  // Indivisible files can leave LPT just over the gate budget even when there
  // is room. Each move/swap lowers the affected pair's maximum; no other shard
  // grows. Bound refinement independently of convergence and keep all weights,
  // owning groups and launch policies intact.
  for (let pass = 0; pass < Math.min(specs.length, 256); pass++) {
    const high = shards.reduce((best, candidate) => candidate.weightSeconds > best.weightSeconds ? candidate : best)
    let best, bestMaximum = high.weightSeconds
    for (const low of shards) {
      if (low === high) continue
      for (let a = 0; a < high.specs.length; a++) {
        // b=-1 moves a file; otherwise exchange it with a lighter file.
        for (let b = -1; b < low.specs.length; b++) {
          const delta = high.specs[a].weightSeconds - (b < 0 ? 0 : low.specs[b].weightSeconds)
          if (delta <= 0) continue
          const maximum = Math.max(high.weightSeconds - delta, low.weightSeconds + delta)
          if (maximum < bestMaximum - 1e-9) {
            bestMaximum = maximum
            best = { low, a, b, delta }
          }
        }
      }
    }
    if (!best) break
    const { low, a, b, delta } = best, spec = high.specs[a]
    if (b < 0) {
      high.specs.splice(a, 1)
      low.specs.push(spec)
    } else {
      high.specs[a] = low.specs[b]
      low.specs[b] = spec
    }
    high.weightSeconds -= delta
    low.weightSeconds += delta
  }
  for (const shard of shards) {
    shard.specs.sort((a, b) => compare(a.file, b.file))
    shard.weightSeconds = shard.specs.reduce((total, spec) => total + spec.weightSeconds, 0)
  }
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

// At execution time, undeclared specs fall back to an ungated `unlisted` group
// with a default weight; declared specs/exclusions that no longer exist are
// dropped. --strict reports drift, while source-inventory unit CI rejects it
// before this fallback can silently admit an unaccounted-for spec.
export function reconcileManifest(manifest, discovered, { defaultWeightSeconds = 60 } = {}) {
  const found = new Set(discovered)
  const declared = new Set(manifest.groups.flatMap(group => group.specs.map(spec => spec.file)))
  const exclusions = manifest.exclusions ?? []
  const excluded = new Set(exclusions.map(exclusion => exclusion.file))
  const unlisted = discovered.filter(file => !declared.has(file) && !excluded.has(file)).sort(compare)
  const stale = [...declared, ...excluded].filter(file => !found.has(file)).sort(compare)
  if (unlisted.length && manifest.groups.some(group => group.id === 'unlisted')) throw new Error('Manifest must not declare the reserved group id: unlisted')
  const groups = manifest.groups.map(group => ({ ...group, specs: group.specs.filter(spec => found.has(spec.file)) })).filter(group => group.specs.length)
  if (unlisted.length) {
    // Ungated, like remaining-ui: the old CI never ran a spec that no workflow step named, and a new spec
    // has no hosted-runner evidence yet (clip-tip.spec.ts failed there on its first run). Add it to the
    // manifest to gate it; --all runs it meanwhile.
    groups.push({ id: 'unlisted', gate: false, config: 'playwright.ui.config.ts', project: null, flags: ['--workers=2'], env: {}, hostedOnly: true,
      specs: unlisted.map(file => ({ file, weightSeconds: defaultWeightSeconds })) })
  }
  return { manifest: { ...manifest, groups, ...(manifest.exclusions ? { exclusions: exclusions.filter(exclusion => found.has(exclusion.file)) } : {}) }, unlisted, stale }
}

export function checkCoverage(manifest, shards, expectedSpecs, { all = false } = {}) {
  validateManifest(manifest)
  const expected = new Set(expectedSpecs)
  const declared = new Set(manifest.groups.flatMap(group => group.specs.map(spec => spec.file)))
  const excluded = new Set((manifest.exclusions ?? []).map(exclusion => exclusion.file))
  const required = new Set(gatedGroups(manifest, all).flatMap(group => group.specs.map(spec => spec.file)))
  const owners = new Map(manifest.groups.flatMap(group => group.specs.map(spec => [spec.file, group.id])))
  const assigned = new Set()
  for (const shard of shards) {
    for (const spec of shard.specs) {
      if (assigned.has(spec.file)) throw new Error(`Spec appears in two shards: ${spec.file}`)
      if (!declared.has(spec.file)) throw new Error(`Undeclared shard spec: ${spec.file}`)
      if (owners.get(spec.file) !== spec.groupId) throw new Error(`Wrong group for shard spec: ${spec.file}`)
      if (!required.has(spec.file)) throw new Error(`Ungated spec in a gate shard: ${spec.file}`)
      assigned.add(spec.file)
    }
  }
  for (const file of required) if (!assigned.has(file)) throw new Error(`Spec missing from shards: ${file}`)
  for (const file of expected) if (!declared.has(file) && !excluded.has(file)) throw new Error(`Spec missing from manifest: ${file}`)
  for (const file of [...declared, ...excluded]) if (!expected.has(file)) throw new Error(`Stale manifest spec: ${file}`)
  const ungated = declared.size - required.size
  return { specs: assigned.size, shards: shards.length, ...(ungated ? { ungated } : {}), ...(excluded.size ? { excluded: excluded.size } : {}) }
}

export function retryCount(env) {
  const value = env.PW_RETRIES ?? '0'
  if (!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value))) throw new Error('PW_RETRIES must be a nonnegative integer')
  return Number(value)
}

// Spec-only PRs include ungated and newly discovered specs, using their owning
// group's config, project, flags and evidence paths rather than a generic launch.
export function selectChangedSpecs(manifest, json) {
  if (typeof json !== 'string' || Buffer.byteLength(json) > 2 * 1024 * 1024) throw new Error('Changed spec list too large')
  const files = JSON.parse(json)
  if (!Array.isArray(files) || !files.length || files.length > 10000) throw new Error('Expected nonempty changed spec list')
  const selected = new Set(files.map(file => {
    validatePath(file)
    if (!/^web\/tests\/[^/]+\.spec\.ts$/.test(file)) throw new Error('Expected top-level UI spec')
    return file.slice('web/'.length)
  }))
  const known = new Set(manifest.groups.flatMap(group => group.specs.map(spec => spec.file)))
  for (const file of selected) if (!known.has(file)) throw new Error(`Changed spec missing from checkout: ${file}`)
  return { ...manifest, groups: manifest.groups.map(group => ({ ...group, gate: true,
    specs: group.specs.filter(spec => selected.has(spec.file)),
  })).filter(group => group.specs.length) }
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

// OPS-257: scripts/ci-flake-guard.mjs retries one failed test by exporting
// CI_FLAKE_PLAYWRIGHT_TESTS (JSON [{file,line,title,project}]) and PW_GREP.
// Select exactly that test in its original config group, or fail closed: a
// retry must never rerun (and so silently green-wash) a whole shard.
export function planFlakeRetry(commands, env, root = webRoot) {
  let tests
  try { tests = JSON.parse(env.CI_FLAKE_PLAYWRIGHT_TESTS) } catch { throw new Error('CI_FLAKE_PLAYWRIGHT_TESTS must be JSON') }
  if (!Array.isArray(tests) || tests.length !== 1) throw new Error('CI_FLAKE_PLAYWRIGHT_TESTS must hold exactly one test')
  const [test] = tests
  if (typeof test?.file !== 'string' || !test.file || typeof test.title !== 'string' || !test.title) throw new Error('Flake retry needs file and title')
  if (!env.PW_GREP) throw new Error('Flake retry needs PW_GREP')
  // The reporter path is relative to the config testDir or to web/; accept both.
  const wanted = test.file.replace(/^(?:\.\/)?(?:web\/)?/, '')
  const find = test => commands.flatMap(command => command.files.filter(test).map(file => ({ command, file })))
  // Prefer the exact spec path (or testDir + path); a bare suffix could match two specs.
  let matches = find(file => file === wanted || file === `tests/${wanted}`)
  if (!matches.length) matches = find(file => file.endsWith(`/${wanted}`))
  if (matches.length !== 1) throw new Error(`Flake retry file ${test.file} matches ${matches.length} specs in this shard`)
  const { command, file } = matches[0]
  const projectAt = command.args.indexOf('--project')
  if (projectAt >= 0 && test.project && command.args[projectAt + 1] !== test.project) {
    throw new Error(`Flake retry project ${test.project} differs from group project ${command.args[projectAt + 1]}`)
  }
  // Playwright clears its output directory on start: give the retry its own, so
  // the first attempt's traces survive next to the retry's.
  const slug = `${file.replace(/\.spec\.ts$/, '').replace(/[^a-zA-Z0-9_-]+/g, '-')}-${Number.isInteger(test.line) ? test.line : 'x'}`
  const kept = command.args.slice(0, command.args.length - command.files.length).map((arg, i, all) =>
    all[i - 1] === '--output' ? `${arg}-retry-${slug}` : arg)
  const anchored = `^${resolve(root, file).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`
  return [{ ...command, files: [file], args: [...kept,
    ...(projectAt < 0 && test.project ? ['--project', test.project] : []),
    '--grep', env.PW_GREP, anchored] }]
}

export function parseArgs(args, defaultCount = 12) {
  let shard, mode = 'run', count = parseShard(`1/${defaultCount}`).count
  let lastFailed = false, testList, reporter, all = false, strict = false
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
    } else if (arg === '--all') {
      all = true
    } else if (arg === '--strict') {
      strict = true
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
    ...(all ? { all } : {}), ...(strict ? { strict } : {}), ...(lastFailed ? { lastFailed } : {}), ...(testList ? { testList } : {}), ...(reporter ? { reporter } : {}),
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
  let expected = discoverSpecs(root, manifest.specDirectories ?? ['tests'])
  const reconciled = reconcileManifest(manifest, expected)
  if (options.strict && (reconciled.unlisted.length || reconciled.stale.length)) {
    throw new Error(`Manifest drift: ${reconciled.unlisted.length} unlisted, ${reconciled.stale.length} stale spec(s): ${[...reconciled.unlisted, ...reconciled.stale].join(', ')}`)
  }
  manifest = reconciled.manifest
  if (env.CI_CHANGED_SPECS !== undefined) {
    if (options.count !== 1 || options.shard?.index !== 1) throw new Error('Changed specs require shard 1/1')
    manifest = selectChangedSpecs(manifest, env.CI_CHANGED_SPECS)
    expected = manifest.groups.flatMap(group => group.specs.map(spec => spec.file))
  }
  const drift = { ...(reconciled.unlisted.length ? { unlisted: reconciled.unlisted } : {}), ...(reconciled.stale.length ? { stale: reconciled.stale } : {}) }
  const shards = balanceShards(manifest, options.count, { all: options.all })
  const coverage = checkCoverage(manifest, shards, expected, { all: options.all })
  for (const group of manifest.groups) if (!existsSync(resolve(root, group.config))) throw new Error(`Missing config: ${group.config}`)
  const plans = (options.shard ? [shards[options.shard.index - 1]] : shards).map(shard => ({ ...shard, commands: planCommands(manifest, shard, env, root) }))
  if (options.mode === 'check') {
    out(JSON.stringify({ ...coverage, ...drift, weightSeconds: shards.map(s => Number(s.weightSeconds.toFixed(2))) }))
    return 0
  }
  if (options.mode === 'list') { out(JSON.stringify(plans, null, 2)); return 0 }
  if (Object.keys(drift).length) out(`::warning::ci-web-shards.json drift: ${drift.unlisted?.length ?? 0} unlisted spec(s) ${env.CI_CHANGED_SPECS !== undefined ? 'included when changed in this PR' : 'are not gated (add them to the manifest)'}, ${drift.stale?.length ?? 0} stale entr${drift.stale?.length === 1 ? 'y' : 'ies'} ignored; update the manifest (ci:web:shard -- --check --strict)`)
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
  if (env.CI_FLAKE_PLAYWRIGHT_TESTS) {
    if (options.lastFailed || options.testList) throw new Error('CI_FLAKE_PLAYWRIGHT_TESTS cannot combine with --last-failed or --test-list')
    commands = planFlakeRetry(commands, env, root)
  }
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
