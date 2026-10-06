// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, mkdtempSync, mkdirSync, writeFileSync, existsSync, realpathSync, rmSync, chmodSync, watchFile, unwatchFile } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'
import { execFileSync } from 'node:child_process'
import { parseWorkflow, staticSteps, validateRegistry, parseArgs, runCommand, runChecks, mergedTree, main, fixedEnvironment, reuseDependencies, versionWarnings } from './ci-static.mjs'

const workflow = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8')
const registry = JSON.parse(readFileSync(new URL('./ci/static-checks.json', import.meta.url), 'utf8'))
const root = new URL('../', import.meta.url).pathname
function temporary(t) {
  const dir = realpathSync(mkdtempSync(join(tmpdir(), 'aeon-static-test-')))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  return dir
}
function repository(t) {
  const dir = temporary(t), home = join(dir, 'fixture-home')
  mkdirSync(home)
  const env = { ...process.env, HOME: home, XDG_CONFIG_HOME: home, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null' }
  const git = args => execFileSync('git', args, { cwd: dir, env, encoding: 'utf8' }).trim()
  git(['init', '-q']); git(['config', 'user.name', 'Fixture']); git(['config', 'user.email', 'fixture@example.invalid'])
  const write = content => { writeFileSync(join(dir, 'content.txt'), content); git(['add', 'content.txt']); git(['commit', '-qm', 'fixture']) }
  write('base\n'); git(['update-ref', 'refs/remotes/origin/main', 'HEAD'])
  return { dir, git, write }
}

// The real workflow is the contract, independently of registry generation.
test('static CI commands are registered and every origin still exists', () => {
  assert.equal(validateRegistry(registry, parseWorkflow(workflow)), registry.checks)
  assert.ok(staticSteps(parseWorkflow(workflow), registry).length >= 30)
  assert.equal(registry.checks.filter(c => c.optional).length, 6)
})

test('drift guard catches newly added commands in each full static job', () => {
  for (const job of ['go-static', 'release-check-run', 'web-setup', 'migration-compat', 'web-unit']) for (const command of ['node --test scripts/new.test.mjs', 'bash scripts/new-check.sh', 'go run ./scripts/new', 'python3 scripts/new.py', './scripts/x.sh', 'FOO=1 node scripts/new.mjs']) {
    const mutated = workflow.replace(`  ${job}:\n`, `  ${job}:\n    steps:\n      - name: New static check\n        run: ${command}\n`)
    assert.throws(() => validateRegistry(registry, parseWorkflow(mutated)), /Unregistered static CI command/)
  }
})

test('every primary mirror is required even when a supplement shares its origin', () => {
  for (const row of registry.checks.filter(c => !c.supplement)) {
    const copy = { ...registry, checks: registry.checks.filter(c => c.id !== row.id) }
    assert.throws(() => validateRegistry(copy, parseWorkflow(workflow)), /Unregistered static CI command/, row.id)
  }
})

test('new jobs, new excluded-job steps and unknown multiline commands require classification', () => {
  assert.throws(() => validateRegistry(registry, parseWorkflow(workflow + '\n  brand-new:\n    steps:\n      - run: true\n')), /Unclassified CI job/)
  const excluded = workflow.replace('  cache-prime:\n', '  cache-prime:\n    steps:\n      - run: node scripts/new.mjs\n')
  assert.throws(() => validateRegistry(registry, parseWorkflow(excluded)), /Unclassified CI run in not-static job/)
  for (const command of ['./scripts/x.sh', 'FOO=1 node scripts/new.mjs', 'echo surprising']) {
    const changed = workflow.replace('          go run ./scripts/ci-go-shards check -count 4', `          go run ./scripts/ci-go-shards check -count 4\n          ${command}`)
    assert.throws(() => validateRegistry(registry, parseWorkflow(changed)), /Unregistered static CI command/)
    const changedExcluded = workflow.replace('        run: npm ci', `        run: |\n          npm ci\n          ${command}`)
    assert.throws(() => validateRegistry(registry, parseWorkflow(changedExcluded)), /Unclassified CI run|Unregistered static CI command/)
  }
})

test('drift guard rejects deleted origins, changed commands/cwd, duplicate ids and missing mirrors', () => {
  const jobs = parseWorkflow(workflow)
  for (const row of registry.checks) {
    const copy = structuredClone(registry)
    copy.checks.find(c => c.id === row.id).ci.command += ' --changed'
    assert.throws(() => validateRegistry(copy, jobs), /Stale CI reference/, row.id)
  }
  assert.throws(() => validateRegistry({ ...registry, checks: [...registry.checks, registry.checks[0]] }, jobs), /duplicate check id/)
  assert.throws(() => validateRegistry({ ...registry, checks: registry.checks.slice(1) }, jobs), /Unregistered static CI command/)
  const copy = structuredClone(registry); copy.checks[1].command = 'true'
  assert.throws(() => validateRegistry(copy, jobs), /Mirror differs/)
  const changed = workflow.replace('        run: npm run typecheck', '        run: npm run typecheck --changed')
  assert.throws(() => validateRegistry(registry, parseWorkflow(changed)), /Stale CI reference/)
  const moved = workflow.replace('      - working-directory: web\n        run: npm run typecheck', '      - working-directory: .\n        run: npm run typecheck')
  assert.throws(() => validateRegistry(registry, parseWorkflow(moved)), /Stale CI reference/)
})

test('workflow reader retains cwd, multiline runs and inline conditional shard tests', () => {
  const jobs = parseWorkflow(workflow)
  assert.equal(jobs.get('web-setup').find(s => s.run === 'npm run lint').cwd, 'web')
  const steps = staticSteps(jobs, registry)
  assert.match(steps.find(s => s.command.startsWith('python3 - <<')).command, /volatile = re.compile/)
  assert.ok(steps.some(s => s.command === 'npm run ci:web:shard:test' && s.cwd === 'web'))
  assert.throws(() => parseWorkflow('jobs:\n  job:\n    steps:\n      - run: >\n          node scripts/check.mjs'), /Unsupported CI run/)
})

test('options default to merge-main and reject ambiguity, missing values and unsafe refs', () => {
  assert.equal(parseArgs([]).mode, 'merge-main')
  assert.deepEqual(parseArgs(['--here', '--only', 'go-vet,web-lint', '--jobs', '2', '--json']).only, ['go-vet', 'web-lint'])
  for (const args of [['--here', '--merge-main'], ['--only'], ['--only', 'bad;cmd'], ['--jobs', '0'], ['--jobs', '17'], ['--jobs', '1.5'], ['--base-ref', 'main'], ['--bad']]) assert.throws(() => parseArgs(args))
})

test('list and usage errors produce a single JSON value without executing checks', async () => {
  const lines = []
  assert.equal(await main(['--list', '--only', 'go-vet', '--json'], { root, stdout: s => lines.push(s) }), 0)
  assert.equal(JSON.parse(lines.pop())[0].id, 'go-vet')
  assert.equal(await main(['--here', '--only', 'nonexistent', '--json'], { root, stdout: s => lines.push(s) }), 2)
  assert.match(JSON.parse(lines.pop()).error, /Unknown check id/)
})

test('command results preserve nonzero exit codes and bound the diagnostic tail', async t => {
  const cwd = temporary(t)
  const result = await runCommand("node -e 'for(let i=0;i<80;i++) console.log(i); process.exit(7)'", { cwd, timeout_seconds: 5 })
  assert.equal(result.status, 'failed'); assert.equal(result.code, 7)
  assert.equal(result.output.split('\n').length, 40)
  assert.equal(result.output.split('\n')[0], '40')
  assert.equal(result.output.split('\n').at(-1), '79')
})

test('timeouts and cancellation terminate descendants before returning', async t => {
  const cwd = temporary(t), marker = join(cwd, 'child-started')
  const command = `node -e 'require("node:fs").writeFileSync(${JSON.stringify(marker)}, String(process.pid)); setInterval(()=>{},1000)'`
  const result = await runCommand(command, { cwd, timeout_seconds: 1 })
  assert.equal(result.status, 'timeout'); assert.ok(existsSync(marker))
  const pid = Number(readFileSync(marker, 'utf8'))
  assert.ok(pid > 0)
  assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' })
  const controller = new AbortController(), cancelledMarker = join(cwd, 'cancelled-child')
  // Stat polling observes an explicit child-ready barrier; no assumed sleep or
  // elapsed-time assertion. It also works where sandboxed fs.watch is denied.
  const ready = () => {
    if (existsSync(cancelledMarker) && readFileSync(cancelledMarker, 'utf8')) controller.abort()
  }
  watchFile(cancelledMarker, { interval: 10 }, ready)
  t.after(() => unwatchFile(cancelledMarker, ready))
  const cancelled = await runCommand(`node -e 'require("node:fs").writeFileSync(${JSON.stringify(cancelledMarker)}, String(process.pid)); setInterval(()=>{},1000)'`, { cwd, timeout_seconds: 5, signal: controller.signal })
  unwatchFile(cancelledMarker, ready)
  assert.equal(cancelled.status, 'interrupted')
  const cancelledPid = Number(readFileSync(cancelledMarker, 'utf8'))
  assert.ok(cancelledPid > 0)
  assert.throws(() => process.kill(cancelledPid, 0), { code: 'ESRCH' })
})

test('bounded pool overlaps independent checks and optional skips retain reasons', async t => {
  const cwd = temporary(t)
  const checks = Array.from({ length: 6 }, (_, i) => ({ id: `check-${i}`, command: 'true', cwd: '.', needs: [], timeout_seconds: 5 }))
  let running = 0, maximum = 0, arrivals = 0, release
  const barrier = new Promise(r => { release = r })
  const results = await runChecks(checks, cwd, { jobs: 2, run: async () => {
    maximum = Math.max(maximum, ++running)
    if (++arrivals === 2) release()
    await barrier; running--
    return { status: 'passed', seconds: 0 }
  } })
  assert.equal(maximum, 2); assert.equal(results.length, 6); assert.equal(arrivals, 6)
  const skipped = await runChecks([{ id: 'web', command: 'npm run lint', cwd: 'web', needs: ['npm-installed'], optional: true }], cwd)
  assert.equal(skipped[0].status, 'skipped'); assert.match(skipped[0].reason, /optional.*absent/)
})

function saveFixtureCheck(repo, { command = 'true', needs = [], optional } = {}) {
  mkdirSync(join(repo.dir, 'scripts/ci'), { recursive: true })
  mkdirSync(join(repo.dir, '.github/workflows'), { recursive: true })
  writeFileSync(join(repo.dir, '.github/workflows/ci.yml'), `jobs:\n  fixture:\n    steps:\n      - run: ${command}\n`)
  writeFileSync(join(repo.dir, 'scripts/ci/static-checks.json'), JSON.stringify({
    jobs: { fixture: { kind: 'static' } }, exclusions: [], checks: [{ id: 'fixture', command, cwd: '.',
      ci: { job: 'fixture', step: null, command, cwd: '.' }, needs, optional, timeout_seconds: 5 }]
  }))
}

test('skips report INCOMPLETE and code 3; allow-skips remains explicitly incomplete', async t => {
  const repo = repository(t), lines = []
  saveFixtureCheck(repo, { needs: ['npm-installed'], optional: true })
  assert.equal(await main(['--here'], { root: repo.dir, stdout: s => lines.push(s) }), 3)
  assert.match(lines.at(-1), /INCOMPLETE: skipped fixture/)
  lines.length = 0
  assert.equal(await main(['--here', '--json'], { root: repo.dir, stdout: s => lines.push(s) }), 3)
  assert.equal(JSON.parse(lines[0]).incomplete, true)
  lines.length = 0
  assert.equal(await main(['--here', '--json', '--allow-skips'], { root: repo.dir, stdout: s => lines.push(s) }), 0)
  assert.equal(JSON.parse(lines[0]).incomplete, true)
  saveFixtureCheck(repo, { command: 'false' })
  assert.equal(await main(['--here', '--json', '--allow-skips'], { root: repo.dir, stdout: () => {} }), 1)
})

test('fixed check environment removes caller flags and identity but supplies CI values', async t => {
  const cwd = temporary(t), source = { ...process.env, GOFLAGS: '-skip', AEON_TEST_TIER_MODE: 'essential', AEON_TEST_DATABASE_URL: 'fixture', RANDOM_EXTRA: 'fixture' }
  await runChecks([{ id: 'env', command: 'true', cwd: '.', needs: [], timeout_seconds: 5 }], cwd, { env: source, run: async (_command, { env }) => {
    assert.equal(env.GOFLAGS, undefined); assert.equal(env.AEON_TEST_DATABASE_URL, undefined)
    assert.equal(env.RANDOM_EXTRA, undefined); assert.equal(env.AEON_TEST_TIER_MODE, 'full')
    assert.equal(env.CI, 'true'); assert.equal(env.CI_LANE, 'full')
    assert.ok(env.npm_config_cache.startsWith(tmpdir()))
    return { status: 'passed', seconds: 0 }
  } })
  assert.equal(fixedEnvironment({ PATH: '/bin', GIT_CONFIG_COUNT: '1' }).GIT_CONFIG_COUNT, undefined)
})

test('version warnings compare installed versions with workflow pins', t => {
  const cwd = temporary(t)
  for (const [name, version] of [['node', 'v23.0.0'], ['go', 'go version go1.25.0 darwin/arm64']]) {
    const path = join(cwd, name); writeFileSync(path, `#!/bin/sh\necho '${version}'\n`); chmodSync(path, 0o755)
  }
  assert.equal(versionWarnings('node-version: "24"\ngo-version: "1.26"', { PATH: cwd }).length, 2)
  assert.equal(versionWarnings('node-version: "23"\ngo-version: "1.25"', { PATH: cwd }).length, 0)
})

test('dependency sharing requires byte-identical locks and redirects typecheck caches', async t => {
  const dir = temporary(t), root = join(dir, 'caller'), tree = join(dir, 'merged')
  for (const path of [root, tree]) {
    mkdirSync(join(path, 'web'), { recursive: true }); writeFileSync(join(path, 'web/package-lock.json'), 'identical')
  }
  mkdirSync(join(root, 'web/node_modules/.bin'), { recursive: true })
  for (const name of ['vue-tsc', 'eslint']) writeFileSync(join(root, 'web/node_modules/.bin', name), 'fixture')
  writeFileSync(join(tree, 'web/package-lock.json'), 'changed')
  assert.equal(reuseDependencies(root, tree), false)
  writeFileSync(join(tree, 'web/package-lock.json'), 'identical')
  assert.equal(reuseDependencies(root, tree), true)
  assert.equal(realpathSync(join(tree, 'web/node_modules')), realpathSync(join(root, 'web/node_modules')))
  let configDir
  const results = await runChecks([{ id: 'web-typecheck', command: 'npm run typecheck', cwd: 'web', needs: ['npm-installed'], optional: true, timeout_seconds: 5 }], tree, { run: async (cmd, { env }) => {
    configDir = /'([^']+\/tsconfig.json)'$/.exec(cmd)[1]
    assert.match(cmd, /^npm run typecheck /)
    const config = JSON.parse(readFileSync(configDir))
    assert.equal(config.references.length, 2)
    const app = JSON.parse(readFileSync(resolve(configDir, '../app.json')))
    assert.ok(app.compilerOptions.tsBuildInfoFile.startsWith(tmpdir()))
    assert.ok(env.npm_config_cache.startsWith(tmpdir()))
    return { status: 'passed', seconds: 0 }
  } })
  assert.equal(results[0].status, 'passed'); assert.ok(!existsSync(configDir))
  assert.equal(readFileSync(join(root, 'web/node_modules/.bin/vue-tsc'), 'utf8'), 'fixture')
})

test('migration lookup uses published latest, verifies the tag and binds the checker', async t => {
  const repo = repository(t), tag = 'v261005070923.0.0'
  repo.git(['tag', tag])
  const calls = [], checks = [{ id: 'migrations', command: 'node scripts/check-migrations.mjs --base-ref "$PREVIOUS_TAG"', cwd: '.', needs: [], timeout_seconds: 5 }]
  await runChecks(checks, repo.dir, { run: async cmd => { calls.push(cmd); return { status: 'passed', output: tag, seconds: 0 } } })
  assert.equal(calls[0], 'gh api repos/inspr-at/paimos/releases/latest --jq .tag_name')
  assert.equal(calls[1], `node scripts/check-migrations.mjs --base-ref '${tag}'`)
  await assert.rejects(runChecks(checks, repo.dir, { run: async () => ({ status: 'failed', output: 'offline' }) }), /Latest published release lookup failed/)
  await assert.rejects(runChecks(checks, repo.dir, { baseRef: 'v261006070923.0.0' }), /show-ref/)
})

test('merge-main combines committed HEAD with main and leaves caller files and refs untouched', async t => {
  const repo = repository(t)
  repo.git(['checkout', '-qb', 'main-fixture'])
  writeFileSync(join(repo.dir, 'from-main.txt'), 'main\n'); repo.git(['add', 'from-main.txt']); repo.git(['commit', '-qm', 'main fixture'])
  repo.git(['update-ref', 'refs/remotes/origin/main', 'HEAD'])
  repo.git(['checkout', '-qb', 'worker', 'HEAD~1']); repo.write('worker\n')
  const before = repo.git(['rev-parse', 'HEAD']), worktrees = repo.git(['worktree', 'list', '--porcelain'])
  writeFileSync(join(repo.dir, 'content.txt'), 'uncommitted\n')
  let path
  const result = await mergedTree(repo.dir, async tree => {
    path = tree
    assert.equal(readFileSync(join(tree, 'content.txt'), 'utf8'), 'worker\n')
    assert.equal(readFileSync(join(tree, 'from-main.txt'), 'utf8'), 'main\n')
    return 'checked'
  })
  assert.equal(result.value, 'checked'); assert.ok(!existsSync(path))
  assert.equal(repo.git(['rev-parse', 'HEAD']), before)
  assert.equal(readFileSync(join(repo.dir, 'content.txt'), 'utf8'), 'uncommitted\n')
  assert.equal(repo.git(['worktree', 'list', '--porcelain']), worktrees)
})

test('merge conflicts fail usefully and remove only the detached throwaway worktree', async t => {
  const repo = repository(t)
  repo.git(['checkout', '-qb', 'main-fixture']); repo.write('main\n'); repo.git(['update-ref', 'refs/remotes/origin/main', 'HEAD'])
  repo.git(['checkout', '-qb', 'worker', 'HEAD~1']); repo.write('worker\n')
  const before = repo.git(['worktree', 'list', '--porcelain'])
  const result = await mergedTree(repo.dir, () => assert.fail('conflicts must prevent checks'))
  assert.equal(result.merge.status, 'failed'); assert.match(result.merge.output, /CONFLICT/)
  assert.equal(repo.git(['worktree', 'list', '--porcelain']), before)
})

test('check failures still clean up a merged temporary worktree', async t => {
  const repo = repository(t), before = repo.git(['worktree', 'list', '--porcelain'])
  await assert.rejects(mergedTree(repo.dir, () => { throw new Error('checker setup failed') }), /checker setup failed/)
  assert.equal(repo.git(['worktree', 'list', '--porcelain']), before)
})

test('cleanup failure reports residue, prunes and preserves the original check error', async t => {
  const repo = repository(t), warnings = [], calls = []
  let tree
  await assert.rejects(mergedTree(repo.dir, path => { tree = path; throw new Error('original check error') }, {
    warn: s => warnings.push(s), cleanup: args => {
      calls.push(args)
      if (args[1] === 'remove') throw new Error('fixture removal failure')
      return repo.git(args)
    }
  }), /original check error/)
  assert.match(warnings[0], /Cleanup failed.*residue/); assert.ok(warnings[0].includes(tree))
  assert.deepEqual(calls.at(-1), ['worktree', 'prune'])
  repo.git(['worktree', 'remove', '--force', tree]); repo.git(['worktree', 'prune'])
})

test('interrupt during worktree add cleans registered residue before checks', async t => {
  const repo = repository(t), controller = new AbortController(), before = repo.git(['worktree', 'list', '--porcelain'])
  await assert.rejects(mergedTree(repo.dir, () => assert.fail('interrupted add must not execute checks'), {
    signal: controller.signal, run: async (cmd, options) => {
      const result = await runCommand(cmd, options)
      assert.equal(result.status, 'passed')
      controller.abort()
      return result
    }
  }), /Interrupted during worktree add/)
  assert.equal(repo.git(['worktree', 'list', '--porcelain']), before)
})

test('caller hooks, signing and fast-forward-only configuration cannot affect disposable merges', async t => {
  const repo = repository(t), hookDir = join(repo.dir, 'hostile-hooks'), marker = join(repo.dir, 'hook-fired')
  repo.git(['checkout', '-qb', 'main-fixture'])
  writeFileSync(join(repo.dir, 'main.txt'), 'main'); repo.git(['add', 'main.txt']); repo.git(['commit', '-qm', 'main'])
  repo.git(['update-ref', 'refs/remotes/origin/main', 'HEAD'])
  repo.git(['checkout', '-qb', 'worker', 'HEAD~1']); repo.write('worker')
  mkdirSync(hookDir)
  for (const hook of ['post-checkout', 'pre-merge-commit', 'prepare-commit-msg', 'commit-msg']) {
    const path = join(hookDir, hook)
    writeFileSync(path, `#!/bin/sh\ntouch '${marker}'\nexit 1\n`); chmodSync(path, 0o755)
  }
  repo.git(['config', 'core.hooksPath', hookDir]); repo.git(['config', 'commit.gpgSign', 'true'])
  repo.git(['config', 'merge.ff', 'only'])
  const result = await mergedTree(repo.dir, tree => {
    const author = execFileSync('git', ['show', '-s', '--format=%an <%ae>'], { cwd: tree, encoding: 'utf8' }).trim()
    assert.equal(author, 'Static CI fixture <ci-static@example.invalid>')
    return 'merged'
  })
  assert.equal(result.value, 'merged'); assert.ok(!existsSync(marker))
})


test('merge-main selects the merged registry and workflow, including checks changed on main', async t => {
  const repo = repository(t)
  mkdirSync(join(repo.dir, 'scripts/ci'), { recursive: true })
  mkdirSync(join(repo.dir, '.github/workflows'), { recursive: true })
  const save = code => {
    const command = `node -e 'process.exit(${code})'`
    writeFileSync(join(repo.dir, '.github/workflows/ci.yml'), 'jobs:\n  go-static:\n    steps:\n      - run: ' + command + '\n  release-check-run:\n  web-setup:\n  migration-compat:\n  web-unit:\n')
    writeFileSync(join(repo.dir, 'scripts/ci/static-checks.json'), JSON.stringify({ jobs: Object.fromEntries(['go-static', 'release-check-run', 'web-setup', 'migration-compat', 'web-unit'].map(job => [job, { kind: 'static' }])), exclusions: [], checks: [{ id: 'fixture-check', command, cwd: '.', ci: { job: 'go-static', step: null, command, cwd: '.' }, needs: ['node'], timeout_seconds: 5 }] }))
    repo.git(['add', 'scripts', '.github']); repo.git(['commit', '-qm', 'registry fixture'])
  }
  save(0)
  repo.git(['checkout', '-qb', 'main-fixture']); save(7)
  repo.git(['update-ref', 'refs/remotes/origin/main', 'HEAD'])
  repo.git(['checkout', '-qb', 'worker', 'HEAD~1']); repo.write('worker\n')
  const before = repo.git(['worktree', 'list', '--porcelain']), lines = []
  assert.equal(await main(['--merge-main', '--only', 'fixture-check', '--json'], { root: repo.dir, stdout: s => lines.push(s) }), 1)
  const report = JSON.parse(lines[0])
  assert.equal(report.results[0].code, 7)
  assert.match(report.results[0].command, /process.exit\(7\)/)
  assert.equal(repo.git(['worktree', 'list', '--porcelain']), before)
  assert.match(readFileSync(join(repo.dir, 'scripts/ci/static-checks.json'), 'utf8'), /process.exit\(0\)/)
})
