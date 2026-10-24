// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import test from 'node:test'
import { checkAssignment, fullUIInventory, loadAssignment, main, rebalance, shardArgs } from './full-ui-qa-shard.mjs'

const workflow = readFileSync(new URL('../.github/workflows/full-ui-qa.yml', import.meta.url), 'utf8')
const source = workflow.split('  qa-source:\n')[1].split('\n  qa-shard:')[0]
const shard = workflow.split('  qa-shard:\n')[1].split('\n  qa-full:')[0]
const gate = workflow.split('  qa-full:\n')[1]
const sourceCondition = source.match(/    if: >-\n([\s\S]*?)    runs-on:/)[1]

// Evaluate the actual Actions expressions, including its wildcard projection.
function evaluate(expression, github, inputs = {}, needs = {}) {
  const js = expression.replace(/^\s*\$\{\{/, '').replace(/\}\}\s*$/, '')
    .replaceAll('github.event.pull_request.labels.*.name', 'github.event.pull_request.labels.map(label => label.name)')
    .replaceAll('needs.qa-source', 'needs["qa-source"]')
  return Function('github', 'inputs', 'needs', 'contains', 'always', `return (${js});`)(
    github, inputs, needs, (values, value) => values.includes(value), () => true,
  )
}

function event(event_name, action = 'synchronize', labels = [], label = 'full-qa') {
  return {
    event_name, ref: 'refs/heads/main', sha: 'merge-commit',
    event: { action, label: { name: label }, pull_request: { labels: labels.map(name => ({ name })) } },
  }
}

test('only main schedules, manual dispatch and opted-in PR changes run full QA', () => {
  assert.equal(evaluate(sourceCondition, event('schedule')), true)
  assert.equal(evaluate(sourceCondition, { ...event('schedule'), ref: 'refs/heads/work/x' }), false)
  for (const ref of ['refs/heads/main', 'refs/heads/work/x', 'refs/tags/v1']) {
    assert.equal(evaluate(sourceCondition, { ...event('workflow_dispatch'), ref }), true)
  }
  for (const action of ['opened', 'reopened', 'synchronize', 'labeled']) {
    assert.equal(evaluate(sourceCondition, event('pull_request', action, ['full-qa'])), true)
    assert.equal(evaluate(sourceCondition, event('pull_request', action)), false)
  }
  assert.equal(evaluate(sourceCondition, event('pull_request', 'labeled', ['full-qa', 'docs'], 'docs')), false)
  for (const name of ['push', 'pull_request_target', 'merge_group', 'workflow_run']) {
    assert.equal(evaluate(sourceCondition, event(name, 'labeled', ['full-qa'])), false)
  }
  assert.match(workflow, /types: \[opened, reopened, synchronize, labeled\]/)
  assert.match(workflow, /cron: '37 2 \* \* \*'/)
})

test('checkout selects the intended ref and binds shards to its immutable SHA', () => {
  const refExpression = source.match(/^          ref: (.+)$/m)[1]
  assert.equal(evaluate(refExpression, event('schedule'), { ref: 'ignored' }), 'main')
  for (const ref of [undefined, '', 'main', 'work/branch', 'v1', 'a'.repeat(40), '$(touch injected)']) {
    assert.equal(evaluate(refExpression, event('workflow_dispatch'), { ref }), ref || 'main')
  }
  assert.equal(evaluate(refExpression, event('pull_request'), { ref: 'ignored' }), 'merge-commit')
  assert.match(source, /sha=\$\(git rev-parse HEAD\)/)
  assert.match(shard, /ref: \$\{\{ needs.qa-source.outputs.sha \}\}/)
  // User-provided refs enter an action input, never shell interpolation.
  assert.equal((workflow.match(/inputs\.ref/g) ?? []).length, 1)
})

test('non-visual inventory runs in eight hosted shards without retries or secret access', () => {
  assert.equal((workflow.match(/runs-on: ubuntu-latest/g) ?? []).length, 3)
  assert.equal((workflow.match(/persist-credentials: false/g) ?? []).length, 2)
  assert.match(workflow, /permissions:\n  contents: read\n/)
  assert.doesNotMatch(workflow, /secrets\.|: write\b|pull_request_target:|self-hosted|continue-on-error|environment:/)
  assert.match(shard, /fail-fast: false/)
  const matrix = [...shard.matchAll(/- \{ shard: (\w+), config: ([\w.]+), partition: (\d+\/\d+) \}/g)]
    .map(([, shard, config, partition]) => ({ shard, config, partition }))
  assert.deepEqual(matrix, [
    ...[1, 2, 3, 4, 5, 6, 7, 8].map(n => ({ shard: String(n), config: 'playwright.ui.config.ts', partition: `${n}/8` })),
    { shard: 'performance', config: 'playwright.perf.config.ts', partition: '1/1' },
  ])
  assert.match(shard, /npx playwright test -c \$\{\{ matrix.config \}\}\n\s+--shard=\$\{\{ matrix.partition \}\} --workers=1 --retries=0 --forbid-only/)
  // The cold-load cases move to their production config, and remain required
  // by this same aggregate. Neither inventory can silently drop them.
  const ui = readFileSync(new URL('../web/playwright.ui.config.ts', import.meta.url), 'utf8')
  const perf = readFileSync(new URL('../web/playwright.perf.config.ts', import.meta.url), 'utf8')
  assert.match(ui, /testMatch: '\*\*\/\*.spec.ts'/)
  assert.match(ui, /testIgnore: '\*\*\/performance.spec.ts'/)
  assert.match(perf, /testMatch: '\*\*\/performance.spec.ts'/)
  assert.match(perf, /command: 'npm run build && npm run preview /)
  assert.match(perf, /reuseExistingServer: false/)
  assert.doesNotMatch(shard, /--(?:grep|project|only-changed|test-list|pass-with-no-tests)|tests\/.*\.spec\.ts/)
  assert.match(shard, /--trace=retain-on-failure --reporter=github,line,json/)
  assert.match(shard, /ci.yml's visual-comparison owns visual checkpoints in the pinned renderer/)
  const ci = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8')
  const visual = ci.split('  visual-comparison:\n')[1].split('\n  web:')[0]
  assert.match(visual, /container:\n\s+image: mcr.microsoft.com\/playwright:[^\n]+@sha256:[a-f0-9]{64}/)
  assert.match(visual, /node scripts\/test-tiers\/cli.mjs run web --full --visual --job visual-comparison/)
  assert.match(shard, /PLAYWRIGHT_JSON_OUTPUT_FILE: test-results\/full-ui-results.json/)
  assert.match(shard, /if: always\(\)/)
  assert.match(shard, /path: web\/test-results\//)
  assert.match(shard, /retention-days: 7/)
  const audit = readFileSync(new URL('../web/tests/ui-audit-fixtures.ts', import.meta.url), 'utf8')
  const auditTimeoutMs = Number(audit.match(/test.setTimeout\(([\d_]+)\)/)[1].replaceAll('_', ''))
  const shardTimeoutMs = Number(shard.match(/timeout-minutes: (\d+)/)[1]) * 60_000
  assert.equal(shardTimeoutMs, 90 * 60_000)
  assert.ok(shardTimeoutMs >= auditTimeoutMs + 15 * 60_000, 'allow the existing audit timeout plus setup and other specs')
})

// Risk: an explicit list could silently omit a new nested spec, or execute an
// existing file twice, or wrongly exclude a non-visual spec. Negative controls
// corrupt the actual checked-in list; every visual exclusion is explicit.
test('Full QA assignment covers the config exactly once or explicitly excludes visual specs', () => {
  const assignment = loadAssignment(), inventory = fullUIInventory()
  const report = checkAssignment(assignment, inventory)
  assert.equal(report.specs + report.excluded.length, inventory.length)
  assert.equal(report.excluded.length, 19)
  assert.deepEqual(report.excluded, inventory.filter(file => file.startsWith('tests/visual/'))
    .map(file => ({ file, status: 'EXCLUDED' })))
  assert.ok(inventory.includes('tests/visual/tickets.spec.ts'))
  assert.ok(assignment.excluded.includes('tests/visual/reference-guard.spec.ts'))
  assert.ok(!inventory.includes('tests/performance.spec.ts'))
  const missing = structuredClone(assignment), removed = missing.specs.pop()
  assert.throws(() => checkAssignment(missing, inventory), { message: `Unassigned Full QA spec: ${removed.file}` })
  const duplicate = structuredClone(assignment), repeated = { ...duplicate.specs[0], shard: duplicate.specs[0].shard % assignment.shards + 1 }
  duplicate.specs.push(repeated)
  assert.throws(() => checkAssignment(duplicate, inventory), { message: `Duplicate Full QA spec: ${repeated.file}` })
  const wronglyExcluded = structuredClone(assignment), nonVisual = wronglyExcluded.specs.shift()
  wronglyExcluded.excluded.push(nonVisual.file)
  assert.throws(() => checkAssignment(wronglyExcluded, inventory),
    { message: `Only visual Full QA specs may be excluded: ${nonVisual.file}` })
  for (const [mutation, message] of [
    ['assignment.specs.pop()', `Unassigned Full QA spec: ${removed.file}`],
    ['assignment.specs.push({ ...assignment.specs[0] })', `Duplicate Full QA spec: ${repeated.file}`],
    ['assignment.excluded.push(assignment.specs.shift().file)', `Only visual Full QA specs may be excluded: ${nonVisual.file}`],
  ]) {
    const result = spawnSync(process.execPath, ['--input-type=module', '-e', `
      import { loadAssignment, main } from ${JSON.stringify(new URL('./full-ui-qa-shard.mjs', import.meta.url).href)}
      const assignment = loadAssignment()
      ${mutation}
      try { process.exitCode = await main(['--check'], { assignment }) }
      catch (error) { console.error(error.message); process.exitCode = 1 }
    `], { encoding: 'utf8' })
    assert.equal(result.status, 1, result.stderr)
    assert.equal(result.stderr.trim(), message)
    assert.equal(result.stdout, '')
  }
  assert.throws(() => checkAssignment(assignment, [...inventory, 'tests/nested/new.spec.ts']),
    { message: 'Unassigned Full QA spec: tests/nested/new.spec.ts' })
  assert.throws(() => checkAssignment(assignment, inventory.slice(1)), /Stale or unexpected Full QA spec:/)
  const unlistedVisual = structuredClone(assignment), visual = unlistedVisual.excluded.pop()
  assert.throws(() => checkAssignment(unlistedVisual, inventory), { message: `Unassigned Full QA spec: ${visual}` })
  unlistedVisual.specs.push({ file: visual, shard: 1, seconds: 0 })
  assert.throws(() => checkAssignment(unlistedVisual, inventory), { message: `Visual Full QA spec must be EXCLUDED: ${visual}` })
  const both = structuredClone(assignment)
  both.specs.push({ file: visual, shard: 1, seconds: 0 })
  assert.throws(() => checkAssignment(both, inventory), { message: `Full QA spec both assigned and excluded: ${visual}` })
  const duplicateExclusion = structuredClone(assignment)
  duplicateExclusion.excluded.push(visual)
  assert.throws(() => checkAssignment(duplicateExclusion, inventory), { message: `Duplicate Full QA exclusion: ${visual}` })
  assert.throws(() => checkAssignment(assignment, inventory.filter(file => file !== visual)),
    { message: `Stale Full QA exclusion: ${visual}` })
  assert.throws(() => checkAssignment(assignment, [...inventory, 'tests/visual/new.spec.ts']),
    { message: 'Unassigned Full QA spec: tests/visual/new.spec.ts' })
})

test('Full QA uses the canonical whole-file writer and spreads all ten audit files', () => {
  const assignment = loadAssignment()
  assert.deepEqual(rebalance(assignment), assignment)
  assert.deepEqual(rebalance({ ...assignment, specs: [...assignment.specs].reverse() }), assignment)
  const report = checkAssignment(assignment)
  assert.equal(report.specs, 378)
  assert.equal(report.projected.length, 8)
  assert.equal(report.projected.reduce((sum, row) => sum + row.audits.length, 0), 10)
  for (const row of report.projected) {
    assert.ok(row.audits.length > 0, `shard ${row.shard} must share the audit`)
    assert.equal(row.withinTarget, true, `shard ${row.shard} must project at most 60 minutes including setup`)
  }
  assert.equal(assignment.setupSeconds, 120)
  const minutes = report.projected.map(row => row.minutes)
  assert.ok(Math.max(...minutes) - Math.min(...minutes) < 0.1, 'duration balancing keeps the eight projections within six seconds')
})

test('Full QA launch selects each exact file once, keeps launch policy and propagates failure', async () => {
  const assignment = loadAssignment(), inventory = fullUIInventory(), calls = [], reports = []
  // A regex metacharacter in a checkout path must not widen file selection.
  const root = '/checkout/aeon.[qa]+/web'
  const env = { CI: 'true', RUNNER_ENVIRONMENT: 'github-hosted', PLAYWRIGHT_JSON_OUTPUT_FILE: 'test-results/full-ui-results.json' }
  assert.match(shard, /if \[ "\$\{\{ matrix.config \}\}" = playwright.ui.config.ts \]; then\n\s+node ..\/scripts\/full-ui-qa-shard.mjs '\$\{\{ matrix.partition \}\}';\n\s+else/)
  for (let i = 1; i <= 8; i++) {
    const code = await main([`${i}/8`], { assignment, expected: inventory, root, env, out: line => reports.push(JSON.parse(line)),
      run: async (args, options) => { calls.push({ args, options }); return i === 3 ? 17 : 0 } })
    assert.equal(code, i === 3 ? 17 : 0)
  }
  for (const report of reports) assert.deepEqual(report.excluded, checkAssignment(assignment, inventory).excluded)
  for (const { args, options } of calls) {
    assert.deepEqual(args.slice(0, 7), ['test', '--config=playwright.ui.config.ts', '--workers=1', '--retries=0',
      '--forbid-only', '--trace=retain-on-failure', '--reporter=github,line,json'])
    assert.equal(options.env, env)
    assert.ok(args.every(arg => !/^--(?:shard|grep|test-list|project|pass-with-no-tests)/.test(arg)))
  }
  for (const file of inventory.filter(file => !assignment.excluded.includes(file))) {
    const matches = calls.flatMap(call => call.args.slice(7)).filter(pattern => new RegExp(pattern).test(`${root}/${file}`))
    assert.equal(matches.length, 1, `${file} must run once`)
    assert.equal(new RegExp(matches[0]).test(`${root}/${file}.extra`), false)
    assert.equal(new RegExp(matches[0]).test(`${root}/tests/${file}`), false)
  }
  const patterns = calls.flatMap(call => call.args.slice(7))
  for (const file of assignment.excluded) {
    assert.equal(patterns.filter(pattern => new RegExp(pattern).test(`${root}/${file}`)).length, 0,
      `${file} must never reach a hosted shard`)
  }
  assert.equal(patterns.length, inventory.length - assignment.excluded.length)
  for (const partition of ['1/5', '1/7']) assert.throws(() => shardArgs(assignment, partition), /requires shard i\/8/)
  assert.throws(() => shardArgs(assignment, '0/8'), /Shard must satisfy/)
  await assert.rejects(main(['1/8'], { assignment, expected: inventory, env: {}, run() { assert.fail('local browser launch') } }),
    /Full QA shards require GitHub-hosted CI/)
  const missing = { ...assignment, specs: assignment.specs.slice(1) }
  await assert.rejects(main(['1/8'], { assignment: missing, expected: inventory, env, run() { assert.fail('launch before guard') } }),
    /Unassigned Full QA spec:/)
})

test('aggregate check reports source failures and every shard failure or cancellation', () => {
  const condition = gate.match(/^    if: (.+)$/m)[1]
  assert.match(gate, /needs: \[qa-source, qa-shard\]/)
  assert.match(gate, /name: full-ui-qa/)
  for (const result of ['success', 'failure', 'cancelled', 'skipped']) {
    assert.equal(evaluate(condition, {}, {}, { 'qa-source': { result } }), result !== 'skipped')
  }
  assert.match(gate, /test "\$SOURCE_RESULT" = success/)
  assert.match(gate, /test "\$SHARD_RESULT" = success/)
  assert.match(gate, />> "\$GITHUB_STEP_SUMMARY"/)
  assert.match(readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8'), /node --test scripts\/full-ui-qa.test.mjs/)
})

test('the checked-in aggregate shell fails closed for every non-success dependency', () => {
  const script = gate.split('        run: |\n')[1].split('\n').map(line => line.slice(10)).join('\n')
  const directory = mkdtempSync(join(tmpdir(), 'aeon-full-ui-qa-'))
  for (const source of ['success', 'failure', 'cancelled', 'skipped']) {
    for (const shard of ['success', 'failure', 'cancelled', 'skipped']) {
      const summary = join(directory, `${source}-${shard}`)
      const result = spawnSync('bash', ['-c', script], {
        encoding: 'utf8', env: {
          SOURCE_RESULT: source, SHARD_RESULT: shard,
          TESTED_SHA: 'a'.repeat(40), GITHUB_STEP_SUMMARY: summary,
        },
      })
      assert.equal(result.status === 0, source === 'success' && shard === 'success', result.stderr)
      const content = readFileSync(summary, 'utf8')
      assert.ok(content.includes('a'.repeat(40)))
      assert.ok(content.includes(`Source resolution: **${source}**`))
      assert.ok(content.includes(`All eight UI shards and the production performance suite: **${shard}**`))
    }
  }
})
