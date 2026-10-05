// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, mkdtempSync, mkdirSync, writeFileSync, existsSync, realpathSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'
import { execFileSync } from 'node:child_process'
import { parseWorkflow, staticSteps, validateRegistry, parseArgs, runCommand, runChecks, mergedTree, main } from './ci-static.mjs'

const workflow = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8')
const registry = JSON.parse(readFileSync(new URL('./ci/static-checks.json', import.meta.url), 'utf8'))
const root = new URL('../', import.meta.url).pathname
function temporary(t) {
  const dir = realpathSync(mkdtempSync(join(tmpdir(), 'aeon-static-test-')))
  // Like ci-pr-plan fixtures, OS-temporary test repositories stay outside the worktree.
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
  assert.equal(validateRegistry(registry, parseWorkflow(workflow)), registry)
  assert.ok(staticSteps(parseWorkflow(workflow)).length >= 30)
  assert.equal(registry.filter(c => c.optional).length, 5)
})

test('drift guard catches newly added commands in each full static job', () => {
  for (const job of ['go-static', 'release-check-run']) for (const command of ['node --test scripts/new.test.mjs', 'bash scripts/new-check.sh']) {
    const mutated = workflow.replace(`  ${job}:\n`, `  ${job}:\n    steps:\n      - name: New static check\n        run: ${command}\n`)
    assert.throws(() => validateRegistry(registry, parseWorkflow(mutated)), /Unregistered static CI command/)
  }
})

test('drift guard rejects deleted origins, changed commands/cwd, duplicate ids and missing mirrors', () => {
  const jobs = parseWorkflow(workflow)
  for (const row of registry) {
    const copy = structuredClone(registry)
    copy.find(c => c.id === row.id).ci.command += ' --changed'
    assert.throws(() => validateRegistry(copy, jobs), /Stale CI reference/, row.id)
  }
  assert.throws(() => validateRegistry([...registry, registry[0]], jobs), /duplicate check id/)
  assert.throws(() => validateRegistry(registry.slice(1), jobs), /Unregistered static CI command/)
  const copy = structuredClone(registry); copy[1].command = 'true'
  assert.throws(() => validateRegistry(copy, jobs), /Mirror differs/)
  const changed = workflow.replace('        run: npm run typecheck', '        run: npm run typecheck --changed')
  assert.throws(() => validateRegistry(registry, parseWorkflow(changed)), /Stale CI reference/)
  const moved = workflow.replace('      - working-directory: web\n        run: npm run typecheck', '      - working-directory: .\n        run: npm run typecheck')
  assert.throws(() => validateRegistry(registry, parseWorkflow(moved)), /Stale CI reference/)
})

test('workflow reader retains cwd, multiline runs and inline conditional shard tests', () => {
  const jobs = parseWorkflow(workflow)
  assert.equal(jobs.get('web-setup').find(s => s.run === 'npm run lint').cwd, 'web')
  const steps = staticSteps(jobs)
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
  const command = `node -e 'require("node:fs").writeFileSync(${JSON.stringify(marker)}, "ready"); setInterval(()=>{},1000)'`
  const result = await runCommand(command, { cwd, timeout_seconds: 1 })
  assert.equal(result.status, 'timeout'); assert.ok(existsSync(marker))
  const controller = new AbortController(); controller.abort()
  const cancelled = await runCommand('node -e "setInterval(()=>{},1000)"', { cwd, timeout_seconds: 5, signal: controller.signal })
  assert.equal(cancelled.status, 'interrupted')
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


test('merge-main selects the merged registry and workflow, including checks changed on main', async t => {
  const repo = repository(t)
  mkdirSync(join(repo.dir, 'scripts/ci'), { recursive: true })
  mkdirSync(join(repo.dir, '.github/workflows'), { recursive: true })
  const save = code => {
    const command = `node -e 'process.exit(${code})'`
    writeFileSync(join(repo.dir, '.github/workflows/ci.yml'), 'jobs:\n  go-static:\n    steps:\n      - run: ' + command + '\n  release-check-run:\n  web-setup:\n  migration-compat:\n  web-unit:\n')
    writeFileSync(join(repo.dir, 'scripts/ci/static-checks.json'), JSON.stringify([{ id: 'fixture-check', command, cwd: '.', ci: { job: 'go-static', step: null, command, cwd: '.' }, needs: ['node'], timeout_seconds: 5 }]))
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
