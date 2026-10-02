// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import test from 'node:test'

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

test('full inventory runs in five hosted shards without retries, filters or secret access', () => {
  assert.equal((workflow.match(/runs-on: ubuntu-latest/g) ?? []).length, 3)
  assert.equal((workflow.match(/persist-credentials: false/g) ?? []).length, 2)
  assert.match(workflow, /permissions:\n  contents: read\n/)
  assert.doesNotMatch(workflow, /secrets\.|: write\b|pull_request_target:|self-hosted|continue-on-error|environment:/)
  assert.match(shard, /fail-fast: false/)
  assert.match(shard, /shard: \[1, 2, 3, 4, 5\]/)
  assert.match(shard, /npx playwright test -c playwright.ui.config.ts\n\s+--shard=\$\{\{ matrix.shard \}\}\/5 --workers=1 --retries=0 --forbid-only/)
  assert.doesNotMatch(shard, /--(?:grep|project|only-changed|test-list|pass-with-no-tests)|tests\/.*\.spec\.ts/)
  assert.match(shard, /--trace=retain-on-failure --reporter=github,line,json/)
  assert.match(shard, /PLAYWRIGHT_JSON_OUTPUT_FILE: test-results\/full-ui-results.json/)
  assert.match(shard, /if: always\(\)/)
  assert.match(shard, /path: web\/test-results\//)
  assert.match(shard, /retention-days: 7/)
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
      assert.ok(content.includes(`All five Playwright shards: **${shard}**`))
    }
  }
})
