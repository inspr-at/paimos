// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { baseline, browserIdentity, provePartition, proveSlowOwners } from './slow-owners.mjs'
import { splitOwner, shard, exactPattern, key } from './core.mjs'
import { load } from './cli.mjs'

test('AEON-1024 measured splits preserve native baseline cases, tiers and full shard coverage', () => {
  const result = proveSlowOwners(load('web').tests, load('go').tests)
  assert.equal(result.browserCases, baseline.browserCases.length)
  assert.equal(result.goCases, baseline.goCases.length)
  assert.equal(result.nodeSeconds.length, 2)
  assert.ok(Math.max(...result.nodeSeconds) < 250)
})

test('AEON-1024 equality proof rejects omissions, duplicates, renamed cases and changed policies', () => {
  const rows = baseline.browserCases.slice(0, 2)
  assert.equal(provePartition(rows, [rows], browserIdentity), 2)
  assert.throws(() => provePartition(rows, [rows.slice(1)], browserIdentity), /changed/)
  assert.throws(() => provePartition(rows, [[rows[0], rows[0]]], browserIdentity), /Duplicate/)
  for (const field of ['name', 'tier', 'config', 'project']) {
    assert.throws(() => provePartition(rows, [[{ ...rows[0], [field]: 'changed' }, rows[1]]], browserIdentity), /changed/)
  }
})

test('AEON-1024 owner splitter balances measured cases, charges both launches and retains unknown cases', () => {
  const rows = ['TestLarge', 'TestMedium', 'TestSmall', 'TestFuture'].map(name => ({ kind: 'go', package: 'internal/nodes', name }))
  const timing = { seconds: 30, selectedTests: 3, split: { count: 2, overheadSeconds: 3, caseSeconds: { TestLarge: 14, TestMedium: 10, TestSmall: 3 } } }
  const parts = splitOwner(rows, timing, 2)
  provePartition(rows, parts.map(part => part.rows))
  assert.deepEqual(parts.map(part => part.weight), [20, 22])
  const bins = Array.from({ length: 2 }, (_, i) => shard(rows, i + 1, 2, {}, { ownerTimings: { 'internal/nodes': timing } }))
  assert.ok(bins.every(bin => bin.length))
  const patterns = bins.map(bin => new RegExp(exactPattern(bin.map(row => row.name))))
  for (const row of rows) assert.equal(patterns.filter(pattern => pattern.test(row.name)).length, 1)
  assert.ok(patterns.every(pattern => !pattern.test('TestLargeSuffix')))
  assert.equal(splitOwner(rows, timing, 1).length, 1)
  assert.equal(splitOwner(rows.slice(0, 1), timing, 2).length, 1)
  assert.throws(() => splitOwner([...rows, rows[0]], timing, 2), /unique/)
  assert.throws(() => splitOwner(rows, timing, 0), /limit/)
  for (const split of [{ ...timing.split, count: 3 }, { ...timing.split, overheadSeconds: -1 },
    { ...timing.split, caseSeconds: { TestLarge: NaN } }, { ...timing.split, caseSeconds: { 'TestA/sub': 1 } }])
    assert.throws(() => splitOwner(rows, { ...timing, split }, 2), /Invalid/)
  const timingRows = rows.map(row => ({ ...row, lane: 'timing' }))
  const timingBins = Array.from({ length: 2 }, (_, i) => shard(timingRows, i + 1, 2, {}, { ownerTimings: { 'internal/nodes': timing } }))
  assert.equal(timingBins.filter(bin => bin.length).length, 1)
  assert.deepEqual(timingBins.flat().map(key).sort(), rows.map(key).sort())
})
