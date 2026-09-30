// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import test from 'node:test'
import { balance, testGroups } from './playwright-ui-shards.mjs'

function spec(file, title, projects = ['ui']) {
  return { id: `${file}:${title}`, file, title, tests: projects.map(projectName => ({ projectName })) }
}

test('files retain stable and quarantine tests, while independent audit states split', () => {
  const groups = testGroups({ suites: [
    { title: 'example.spec.ts', line: 0, specs: [spec('example.spec.ts', 'ordinary')], suites: [
      { title: 'nested', line: 5, specs: [spec('example.spec.ts', 'known flake @quarantine', ['quarantine'])] },
    ] },
    { title: 'ui-audit.spec.ts', line: 0, specs: [spec('ui-audit.spec.ts', 'projects', ['audit']), spec('ui-audit.spec.ts', 'settings', ['audit'])] },
  ] })
  assert.equal(groups.length, 3)
  assert.deepEqual(groups[0].selectors, ['[ui] › example.spec.ts', '[quarantine] › example.spec.ts'])
  assert.equal(groups[0].ids.length, 2)
  assert.deepEqual(groups[1].selectors, ['[audit] › ui-audit.spec.ts › projects'])
  const bins = balance(groups, { 'example.spec.ts': 40, 'ui-audit.spec.ts::projects': 30, 'ui-audit.spec.ts::settings': 10 }, 2)
  assert.deepEqual(bins.map(bin => bin.durationMs), [40, 40])
  assert.deepEqual(bins.flatMap(bin => bin.groups.flatMap(group => group.ids)).sort(), groups.flatMap(group => group.ids).sort())
})

test('new files without timings run, stale timings cannot add tests, and order is deterministic', () => {
  const groups = testGroups({ suites: [{ line: 0, specs: [spec('new.spec.ts', 'new'), spec('old.spec.ts', 'old')] }] })
  const timing = { 'old.spec.ts': 2000, 'deleted.spec.ts': 9000 }
  const forward = balance(groups, timing, 2)
  assert.deepEqual(balance([...groups].reverse(), timing, 2), forward)
  assert.deepEqual(forward.map(bin => bin.groups[0].key), ['old.spec.ts', 'new.spec.ts'])
  assert.equal(forward[1].durationMs, 1000)
  assert.throws(() => balance(groups, timing, 3), /Invalid shard count/)
  assert.throws(() => balance(groups, { 'old.spec.ts': -1 }, 2), /Invalid timing/)
  assert.throws(() => testGroups({ errors: ['spec failed to load'], suites: [] }), /spec failed to load/)
})
