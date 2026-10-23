// SPDX-License-Identifier: AGPL-3.0-only
// Temporary worker proof transport: remote-spec.sh is the approved remote Node
// entry point. This case launches no browser and is removed after the proof.
import { test, expect } from '@playwright/test'
import { spawnSync } from 'node:child_process'
import { readFileSync, writeFileSync } from 'node:fs'
import { performance } from 'node:perf_hooks'

test('AEON-1682 worker runtime and negative-control proof', async ({}, testInfo) => {
  test.setTimeout(240_000)
  const files = ['business-periods.unit.test.ts', 'quote-editor.unit.test.ts', 'quote-profile-normalize.unit.test.ts',
    'quote-inspector.unit.test.ts', 'quote-list.unit.test.ts', 'quote-workspace-recovery.unit.test.ts', 'row-actions.unit.test.ts',
    'profile-form.unit.test.ts', 'import-draft-routing.unit.test.ts', 'quote-switch-race.unit.test.ts', 'crm-undo-receipt.unit.test.ts']
  const results: object[] = []
  function run(label: string, args: string[]) {
    const start = performance.now()
    const result = spawnSync(process.execPath, args, { cwd: process.cwd(), encoding: 'utf8', timeout: 120_000, maxBuffer: 4 * 1024 * 1024 })
    const output = `${result.stdout ?? ''}\n${result.stderr ?? ''}`
    results.push({ label, status: result.status, signal: result.signal, seconds: (performance.now() - start) / 1000, output })
    return { ...result, output }
  }
  const vitest = (files: string[], pattern?: string) => ['node_modules/vitest/vitest.mjs', 'run', ...files.map(file => `tests/${file}`),
    '--maxWorkers=1', '--no-fileParallelism', '--retry=0', ...(pattern ? ['--testNamePattern', pattern] : [])]
  try {
    const positive = run('runtime-positive', vitest(files))
    expect(positive.output, 'runtime suites must pass before any negative control').toMatch(/Test Files.*passed/)
    expect(positive.status, positive.output).toBe(0)
    const node = run('node-positive', ['--experimental-strip-types', '--test', '--test-concurrency=1', 'tests/business.test.ts', 'tests/crm.test.ts', 'tests/business-setup-owner.test.ts', 'tests/test-prune-ledger.test.ts'])
    expect(node.status, node.output).toBe(0)
    const mutations = [
      { file: 'src/stores/business.ts', before: 'Math.min(6, pending.length)', after: 'Math.min(7, pending.length)', suite: 'business-periods.unit.test.ts', name: '200 units use at most six', error: /to be called 6 times|expected.*6/i },
      { file: 'src/stores/business.ts', before: 'rate.effective_until > day', after: 'rate.effective_until >= day', suite: 'business-periods.unit.test.ts', name: 'effective rates use the selected day', error: /AssertionError|expected/ },
      { file: 'src/views/business/CostUnitsView.vue', before: 'if (problem.value || busy.value)', after: 'if (busy.value)', suite: 'business-periods.unit.test.ts', name: 'rate form validates before writing', error: /AssertionError|expected/ },
      { file: 'src/lib/quotes/api.ts', before: "'If-Match': `\"qd-${draftRevision}\"`", after: "'If-Match': '\"qd-1\"'", suite: 'quote-editor.unit.test.ts', name: 'sends CAS and mutation identity', error: /AssertionError|expected/ },
      { file: 'src/lib/quotes/prose.ts', before: "if (node.list_start && node.list_start > 0) values[depth] = node.list_start", after: "if (node.list_start && node.list_start > 0) values[depth] = 1", suite: 'quote-editor.unit.test.ts', name: 'numbers restart, explicit start', error: /AssertionError|expected/ },
      { file: 'src/lib/quotes/profile.ts', before: 'if (!profile) return profile', after: 'return profile; if (!profile) return profile', suite: 'quote-profile-normalize.unit.test.ts', name: 'reads null lists and maps', error: /AssertionError|expected/ },
      { file: 'src/lib/quoteSession.ts', before: "if(this.disposed || generation!==this.generation || edits!==this.editSequence)return 'stale'", after: "if(this.disposed || generation!==this.generation)return 'stale'", suite: 'quote-workspace-recovery.unit.test.ts', name: 'loading refuses writes and a held reload', error: /AssertionError|expected/ },
      { file: 'src/lib/quoteSession.ts', before: 'this.checkDelay*2', after: 'this.checkDelay', suite: 'quote-workspace-recovery.unit.test.ts', name: 'revision watch backs off on the injected clock', error: /AssertionError|expected/ },
      { file: 'src/lib/quoteSession.ts', before: "if(kind==='server')this.view.remote='unavailable'", after: "if(false)this.view.remote='unavailable'", suite: 'quote-workspace-recovery.unit.test.ts', name: 'refused saves preserve work', error: /AssertionError|expected/ },
      { file: 'src/lib/quotes/list.ts', before: 'for (let page = 0; page < 50; page++)', after: 'for (let page = 0; page < 49; page++)', suite: 'business-periods.unit.test.ts', name: 'a cursor at every directory cap', error: /AssertionError|expected/ },
      { file: 'src/lib/quoteMerge.ts', before: 'conflicts.push({ path, reason, mine:', after: 'if (false) conflicts.push({ path, reason, mine:', suite: 'quote-workspace-recovery.unit.test.ts', name: 'merge review preserves text overlaps', error: /AssertionError|expected/ },
    ]
    for (const mutation of mutations) {
      const original = readFileSync(mutation.file, 'utf8')
      expect(original.split(mutation.before)).toHaveLength(2)
      try {
        writeFileSync(mutation.file, original.replace(mutation.before, mutation.after))
        const negative = run(`negative:${mutation.name}`, vitest([mutation.suite], mutation.name))
        expect(negative.status, negative.output).toBe(1)
        expect(negative.output, 'negative control must fail its named behavior assertion').toMatch(mutation.error)
        expect(negative.output).not.toMatch(/Failed to load|Transform failed|No test files found|SyntaxError/)
      } finally { writeFileSync(mutation.file, original) }
    }
  } finally {
    writeFileSync(testInfo.outputPath('p10-runtime-proof.md'), JSON.stringify({ version: 1, results }, null, 2))
  }
})
