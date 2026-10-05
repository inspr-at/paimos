// SPDX-License-Identifier: AGPL-3.0-only
// OPS-257: a local pre-filter; hosted CI remains the proof.
import { randomUUID } from 'node:crypto'
import { spawn, spawnSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'

const sourceRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const workflowPath = '.github/workflows/ci.yml'
const registryPath = 'scripts/ci/static-checks.json'
const usage = 'Usage: node scripts/ci-static.mjs [--merge-main | --here] [--only id,...] [--list] [--json] [--jobs 1..16] [--base-ref vYYMMDDhhmmss.0.0]'
export class SetupError extends Error {}

// Dependency-free reader for CI's block-style jobs/steps. Reject unsupported
// run scalars rather than silently weakening coverage. Never execute YAML to parse it.
export function parseWorkflow(source) {
  const jobs = new Map()
  let job, step, inJobs = false
  const lines = source.split('\n')
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (line === 'jobs:') { inJobs = true; continue }
    if (/^[a-zA-Z][\w-]*:/.test(line)) { inJobs = false; job = step = undefined }
    if (!inJobs) continue
    const header = /^  ([\w-]+):\s*$/.exec(line)
    if (header) {
      if (jobs.has(header[1])) throw new SetupError(`Duplicate CI job: ${header[1]}`)
      job = []; jobs.set(header[1], job); step = undefined
      continue
    }
    const start = /^      - (.*)$/.exec(line)
    if (start && job) { step = { cwd: '.', name: null, run: '' }; job.push(step) }
    if (!step) continue
    const field = /^(?:      - |        )(name|working-directory|run):\s*(.*)$/.exec(line)
    if (!field) continue
    const [, key, value] = field
    if (key === 'name') step.name = value
    else if (key === 'working-directory') step.cwd = value
    else if (/^\|[-+]?\s*$/.test(value)) {
      const block = []
      while (i + 1 < lines.length && (/^          /.test(lines[i + 1]) || lines[i + 1] === '')) block.push(lines[++i].slice(10))
      step.run = block.join('\n').trimEnd()
    } else {
      if (/^[>|'"{]/.test(value)) throw new SetupError('Unsupported CI run scalar; update static workflow reader')
      step.run = value
    }
  }
  return jobs
}

export function normalize(command) { return command.trim().replace(/\r/g, '') }
export function commands(step) {
  if (step.run.startsWith("python3 - <<'PY'")) return [normalize(step.run)]
  return step.run.split('\n').flatMap(line => {
    const plain = line.trim()
    if (/^(?:node|go|python3|bash|sh|npm|GOMAXPROCS=\d+ go)\s/.test(plain)) return [plain]
    // web-unit has a once-only shard test inside a one-line shell conditional.
    const npm = /\bnpm run ci:web:shard:test\b/.exec(plain)
    if (npm) return [npm[0]]
    if (/^npm run (?:typecheck|lint)$/.test(plain)) return [plain]
    return []
  })
}

// Full static jobs are automatically covered, except the expensive Nix build.
// Mixed jobs select cheap scripts/tests, excluding infrastructure and DB/browser runs.
export function staticSteps(jobs) {
  const result = []
  for (const job of ['go-static', 'release-check-run', 'web-setup', 'migration-compat', 'web-unit']) {
    if (!jobs.has(job)) throw new SetupError(`Missing CI job: ${job}`)
    for (const step of jobs.get(job)) {
      if (job === 'release-check-run' && step.run === 'bash scripts/check-nix-vendor-hash.sh') continue
      let selected = commands(step)
      if (['go-static', 'release-check-run'].includes(job) && step.run.trim() && !selected.length) selected = [normalize(step.run)]
      if (job === 'web-setup') selected = selected.filter(c => c.startsWith('node ') || /^npm run (typecheck|lint)$/.test(c))
      if (job === 'migration-compat') selected = selected.filter(c => c.startsWith('node ') || c.startsWith('GOMAXPROCS=') || c.startsWith('python3 -B -m unittest '))
      if (job === 'web-unit') selected = selected.filter(c => c.startsWith('node --test ') || c === 'npm run ci:web:shard:test')
      for (const command of selected) result.push({ job, step: step.name, command: normalize(command), cwd: step.cwd })
    }
  }
  return result
}

export function validateRegistry(checks, jobs) {
  if (!Array.isArray(checks) || !checks.length) throw new SetupError('Expected nonempty static check registry')
  const ids = new Set()
  for (const check of checks) {
    if (!/^[a-z][a-z0-9-]*$/.test(check.id ?? '') || ids.has(check.id)) throw new SetupError(`Invalid or duplicate check id: ${check.id}`)
    ids.add(check.id)
    if (typeof check.command !== 'string' || !check.command.trim() || !['.', 'web'].includes(check.cwd) ||
        !Array.isArray(check.needs) || check.needs.some(n => !['node', 'go', 'python3', 'npm-installed'].includes(n)) ||
        !Number.isInteger(check.timeout_seconds) || check.timeout_seconds < 1 || check.timeout_seconds > 180 ||
        (check.optional !== undefined && check.optional !== true) ||
        (check.optional && !check.needs.includes('npm-installed'))) throw new SetupError(`Invalid check: ${check.id}`)
    const ci = check.ci
    const found = jobs.get(ci?.job)?.some(step => step.name === ci.step && step.cwd === ci.cwd &&
      (normalize(step.run) === ci.command || commands(step).some(c => normalize(c) === ci.command)))
    if (!found) throw new SetupError(`Stale CI reference: ${check.id} (${ci?.job}/${ci?.step ?? ci?.command})`)
    if (!check.supplement && (check.command !== ci.command || check.cwd !== ci.cwd)) throw new SetupError(`Mirror differs from CI: ${check.id}`)
  }
  for (const ci of staticSteps(jobs)) {
    if (!checks.some(c => c.ci.job === ci.job && c.ci.step === ci.step && c.ci.cwd === ci.cwd && c.ci.command === ci.command)) {
      throw new SetupError(`Unregistered static CI command: ${ci.job}/${ci.step ?? '(unnamed)'}: ${ci.command}`)
    }
  }
  return checks
}

export function parseArgs(args) {
  const options = { mode: 'merge-main', jobs: 4, json: false, list: false }
  let mode
  for (let i = 0; i < args.length; i++) {
    const arg = args[i]
    if (arg === '--here' || arg === '--merge-main') {
      if (mode) throw new SetupError('Choose one of --here and --merge-main')
      mode = options.mode = arg.slice(2)
    } else if (arg === '--json' || arg === '--list') options[arg.slice(2)] = true
    else if (arg === '--only') {
      if (options.only || !args[i + 1] || !/^[a-z0-9-]+(?:,[a-z0-9-]+)*$/.test(args[i + 1])) throw new SetupError('Expected --only id,...')
      options.only = args[++i].split(',')
    } else if (arg === '--jobs') {
      if (!/^(?:[1-9]|1[0-6])$/.test(args[i + 1] ?? '')) throw new SetupError('Expected --jobs 1..16')
      options.jobs = Number(args[++i])
    } else if (arg === '--base-ref') {
      if (!/^v[0-9]{12}\.0\.0$/.test(args[i + 1] ?? '')) throw new SetupError('Expected a calendar release --base-ref')
      options.baseRef = args[++i]
    } else throw new SetupError(`Unknown option: ${arg}\n${usage}`)
  }
  return options
}

function git(root, args) {
  const result = spawnSync('git', args, { cwd: root, encoding: 'utf8', timeout: 30_000, maxBuffer: 1024 * 1024 })
  if (result.error || result.status !== 0) throw new SetupError(`git ${args.join(' ')}: ${result.error?.message ?? result.stderr?.trim()}`)
  return result.stdout.trim()
}

export async function mergedTree(root, action, { signal } = {}) {
  // Resolve once: neither the caller's checkout nor origin/main is moved/fetched.
  const head = git(root, ['rev-parse', 'HEAD'])
  const main = git(root, ['rev-parse', '--verify', 'refs/remotes/origin/main^{commit}'])
  const tree = join(tmpdir(), 'aeon-ci-static-' + randomUUID())
  if (existsSync(tree)) throw new SetupError('Temporary worktree path already exists')
  let added = false
  try {
    git(root, ['worktree', 'add', '--detach', tree, head]); added = true
    const merge = await runCommand('git merge --no-edit ' + main, { cwd: tree, timeout_seconds: 30, signal })
    if (merge.status !== 'passed') return { merge: { ...merge, id: 'merge-main', command: `git merge --no-edit ${main}`, cwd: '.', reason: 'HEAD could not merge with origin/main', head, main } }
    return { head, main, value: await action(tree) }
  } finally {
    // Only this invocation's detached, disposable tree is discarded, including conflicts.
    if (added) git(root, ['worktree', 'remove', '--force', tree])
  }
}

export function runCommand(command, { cwd, timeout_seconds, env = process.env, signal } = {}) {
  return new Promise(resolveResult => {
    const begin = performance.now()
    let output = '', expired = false, interrupted = false, timer, killTimer
    const child = spawn('/bin/sh', ['-c', command], { cwd, env, detached: process.platform !== 'win32', stdio: ['ignore', 'pipe', 'pipe'] })
    const tail = data => { output = (output + data.toString()).slice(-65536) }
    child.stdout.on('data', tail); child.stderr.on('data', tail)
    function kill(sig) {
      try { process.platform === 'win32' ? child.kill(sig) : process.kill(-child.pid, sig) } catch (error) { if (error.code !== 'ESRCH') tail(error.message) }
    }
    function stop() { kill('SIGTERM'); killTimer ??= setTimeout(() => kill('SIGKILL'), 500) }
    const abort = () => { interrupted = true; stop() }
    signal?.addEventListener('abort', abort, { once: true })
    timer = setTimeout(() => { expired = true; stop() }, timeout_seconds * 1000)
    child.on('error', error => tail(error.message))
    child.on('close', (code, sig) => {
      if (expired || interrupted) kill('SIGKILL')
      clearTimeout(timer); clearTimeout(killTimer); signal?.removeEventListener('abort', abort)
      resolveResult({ status: expired ? 'timeout' : interrupted ? 'interrupted' : code === 0 ? 'passed' : 'failed',
        seconds: Math.round((performance.now() - begin) / 10) / 100, code, signal: sig,
        output: output.trimEnd().split('\n').slice(-40).join('\n') })
    })
    if (signal?.aborted) abort()
  })
}

export async function runChecks(checks, root, { jobs = 4, baseRef, signal, run = runCommand, env = process.env } = {}) {
  const results = new Array(checks.length)
  let next = 0
  const tool = name => spawnSync('/bin/sh', ['-c', `command -v ${name}`], { env, encoding: 'utf8' }).status === 0
  const installed = existsSync(join(root, 'web/node_modules/.bin/vue-tsc')) && existsSync(join(root, 'web/node_modules/.bin/eslint'))
  for (const check of checks) for (const need of check.needs) {
    if (need !== 'npm-installed' && !tool(need)) throw new SetupError(`Missing ${need} for ${check.id}`)
  }
  if (checks.some(c => c.id === 'migrations')) {
    if (!baseRef) {
      // CI resolves releases/latest, not the newest local tag (which may be unpublished).
      const release = await run('gh api repos/inspr-at/paimos/releases/latest --jq .tag_name', { cwd: root, timeout_seconds: 15, env, signal })
      if (release.status !== 'passed') throw new SetupError(`Latest published release lookup failed; use --base-ref only with a verified published release. ${release.output}`)
      baseRef = release.output.trim()
    }
    if (!/^v[0-9]{12}\.0\.0$/.test(baseRef)) throw new SetupError('Expected a published calendar release')
    git(root, ['show-ref', '--verify', `refs/tags/${baseRef}`])
  }
  await Promise.all(Array.from({ length: Math.min(jobs, checks.length) }, async () => {
    while (next < checks.length) {
      const i = next++, check = checks[i]
      const command = check.command.replaceAll('"$PREVIOUS_TAG"', `'${baseRef}'`)
      const base = { id: check.id, command, cwd: check.cwd, ci: check.ci }
      if (check.needs.includes('npm-installed') && !installed) {
        if (!check.optional) throw new SetupError(`Missing installed web dependencies for ${check.id}`)
        results[i] = { ...base, status: 'skipped', seconds: 0, reason: 'optional: installed web/node_modules is absent; no dependencies are installed by this pre-filter' }
      } else if (signal?.aborted) results[i] = { ...base, status: 'interrupted', seconds: 0, reason: 'run interrupted' }
      else results[i] = { ...base, ...await run(command, { cwd: resolve(root, check.cwd), timeout_seconds: check.timeout_seconds, env, signal }) }
    }
  }))
  return results
}

export async function main(args, { root = sourceRoot, stdout = console.log } = {}) {
  const begin = performance.now()
  let options, controller = new AbortController()
  const abort = () => controller.abort()
  process.on('SIGINT', abort); process.on('SIGTERM', abort)
  try {
    options = parseArgs(args)
    const selectChecks = tree => {
      const checks = validateRegistry(JSON.parse(readFileSync(join(tree, registryPath), 'utf8')),
        parseWorkflow(readFileSync(join(tree, workflowPath), 'utf8')))
      if (options.only?.some(id => !checks.some(c => c.id === id))) throw new SetupError(`Unknown check id in --only: ${options.only.join(',')}`)
      return options.only ? checks.filter(c => options.only.includes(c.id)) : checks
    }
    if (options.list) {
      const selected = selectChecks(root)
      stdout(options.json ? JSON.stringify(selected) : selected.map(c => `${c.id}: ${c.command} (cwd=${c.cwd}; CI=${c.ci.job}/${c.ci.step ?? '(unnamed)'})`).join('\n'))
      return 0
    }
    const execute = tree => runChecks(selectChecks(tree), tree, { ...options, signal: controller.signal })
    const merged = options.mode === 'merge-main' ? await mergedTree(root, execute, { signal: controller.signal }) : undefined
    const results = merged?.merge ? [merged.merge] : merged ? merged.value : await execute(root)
    const report = { mode: options.mode, head: merged?.head, main: merged?.main,
      seconds: Math.round((performance.now() - begin) / 10) / 100, results }
    const dirty = git(root, ['status', '--porcelain', '--untracked-files=no'])
    if (options.mode === 'merge-main' && dirty) report.warning = 'Only committed HEAD was checked. Uncommitted changes were not included.'
    const failed = results.some(r => !['passed', 'skipped'].includes(r.status))
    report.code = failed ? 1 : 0
    if (options.json) stdout(JSON.stringify(report))
    else {
      if (report.warning) stdout(report.warning)
      for (const result of results) {
        stdout(`${result.status.toUpperCase()} ${result.id} (${result.seconds}s)${result.reason ? ': ' + result.reason : ''}`)
        if (!['passed', 'skipped'].includes(result.status)) stdout(`${result.output ?? ''}\nReproduce (from repo root): cd ${result.cwd} && ${result.command}`)
      }
      stdout(`Static pre-filter: ${report.seconds}s; ${results.filter(r => r.status === 'passed').length} passed, ${results.filter(r => r.status === 'skipped').length} optional skipped. Hosted CI remains the proof.`)
    }
    return report.code
  } catch (error) {
    const report = { code: 2, error: error.message, seconds: Math.round((performance.now() - begin) / 10) / 100, results: [] }
    stdout(options?.json || args.includes('--json') ? JSON.stringify(report) : error.message)
    return 2
  } finally {
    process.removeListener('SIGINT', abort); process.removeListener('SIGTERM', abort)
  }
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) process.exitCode = await main(process.argv.slice(2))
