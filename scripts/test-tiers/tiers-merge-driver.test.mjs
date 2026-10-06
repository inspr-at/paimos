// SPDX-License-Identifier: AGPL-3.0-only
// Runs in the fixed script-test lane through test-tiers.test.mjs.
import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { formatManifest, manifestPaths } from './manifests.mjs'

const root = fileURLToPath(new URL('../../', import.meta.url))
const standalone = resolve(root, 'scripts/test-tiers/tiers-merge-driver.mjs')
const cli = resolve(root, 'scripts/test-tiers/cli.mjs')
const directory = realpathSync(mkdtempSync(join(tmpdir(), 'ops257-l13-fix1-driver-')))
after(() => {
  const result = spawnSync('trash', [directory], { encoding: 'utf8', timeout: 10_000 })
  if (result.error || result.status !== 0) console.warn(`Trash unavailable; retained driver fixtures: ${directory}`)
})
const [go, web, shards] = manifestPaths
const row = (name, tier = 'GATED-FULL', packageName = 'internal/auth') => ({ kind: 'go', package: packageName, name, tier })
const manifest = tests => ({ version: 1, defaultNewTier: 'NIGHTLY', tests })
const spec = (file, weightSeconds = 10) => ({ file, weightSeconds })
const group = (id, specs) => ({ id, config: 'playwright.ui.config.ts', project: null, flags: ['--workers=1'], env: {}, specs })
const shardManifest = groups => ({ version: 1, groups })
const source = data => typeof data === 'string' ? data : JSON.stringify(data, null, 2) + '\n'

function run(label, program, inputs, file, extra = [], cwd = directory) {
  const fixture = mkdtempSync(join(directory, `${label}-`))
  const paths = ['base', 'ours', 'theirs'].map(name => join(fixture, name))
  inputs.forEach((data, i) => writeFileSync(paths[i], source(data)))
  const before = readFileSync(paths[1])
  const result = spawnSync(process.execPath, [...extra, ...program, ...paths, file], { cwd, encoding: 'utf8', timeout: 10_000 })
  assert.ifError(result.error)
  assert.equal(result.signal, null)
  return { ...result, before, output: readFileSync(paths[1]) }
}

const cases = []
function fixture(name, inputs, file = go, status = 0, expected) { cases.push({ name, inputs, file, status, expected }) }
const base = manifest([row('TestZ')])
const ours = manifest([row('TestA'), ...base.tests])
const theirs = manifest([row('TestB'), ...base.tests])
const combined = manifest([row('TestA'), row('TestB'), row('TestZ')])
fixture('old layouts with both sides prepending in one package', [base, ours, theirs], go, 0, combined)
fixture('canonical base/ours and old theirs', [formatManifest(base, go), formatManifest(ours, go), theirs], go, 0, combined)
fixture('old base/ours and canonical theirs', [base, ours, formatManifest(theirs, go)], go, 0, combined)
fixture('all canonical layouts', [base, ours, theirs].map(data => formatManifest(data, go)), go, 0, combined)
fixture('unchanged old layout is still written canonically', [base, base, base], go, 0, base)
fixture('unilateral tier edit and unchanged removal', [manifest([row('TestA'), row('TestB')]), manifest([row('TestA', 'NIGHTLY'), row('TestB')]), manifest([row('TestA')])], go, 0, manifest([row('TestA', 'NIGHTLY')]))
fixture('both sides remove a row', [base, manifest([]), manifest([])], go, 0, manifest([]))
fixture('both sides add an identical row', [manifest([]), ours, ours], go, 0, ours)
fixture('removal versus tier change', [base, manifest([]), manifest([row('TestZ', 'NIGHTLY')])], go, 1)
fixture('divergent edits to one row', [base, manifest([row('TestZ', 'ESSENTIAL')]), manifest([row('TestZ', 'NIGHTLY')])], go, 1)
fixture('divergent concurrent new rows', [manifest([]), manifest([row('TestA', 'ESSENTIAL')]), manifest([row('TestA', 'NIGHTLY')])], go, 1)
const timing = { ...base, timingWeights: { owners: { a: { seconds: 1, selectedTests: 2 } } } }
fixture('owner records remain atomic', [timing, { ...base, timingWeights: { owners: { a: { seconds: 3, selectedTests: 2 } } } }, { ...base, timingWeights: { owners: { a: { seconds: 1, selectedTests: 4 } } } }], go, 1)
const webRow = (name, occurrence) => ({ kind: 'node', file: 'tests/a.test.ts', name, occurrence, tier: 'NIGHTLY' })
const webBase = manifest([webRow('case:#2', 1)])
fixture('web registration occurrences retain distinct identities', [webBase, manifest([webRow('case:#2', 2), ...webBase.tests]), manifest([webRow('case:#2', 3), ...webBase.tests])], web, 0, manifest([webRow('case:#2', 1), webRow('case:#2', 2), webRow('case:#2', 3)]))
const shardBase = shardManifest([group('z-first', [spec('tests/z.spec.ts')]), group('a-second', [spec('tests/other.spec.ts')])])
const shardOurs = structuredClone(shardBase), shardTheirs = structuredClone(shardBase)
shardOurs.groups[0].specs.unshift(spec('tests/a.spec.ts', 7))
shardTheirs.groups[0].specs.unshift(spec('tests/b.spec.ts', 8))
const shardExpected = structuredClone(shardBase)
shardExpected.groups[0].specs.unshift(spec('tests/a.spec.ts', 7), spec('tests/b.spec.ts', 8))
fixture('web shard spec sets merge while group order is retained', [shardBase, shardOurs, formatManifest(shardTheirs, shards)], shards, 0, shardExpected)
fixture('web shard removal versus weight change', [shardManifest([group('first', [spec('tests/a.spec.ts')])]), shardManifest([group('first', [])]), shardManifest([group('first', [spec('tests/a.spec.ts', 12)])])], shards, 1)
fixture('web shard divergent weight changes', [shardManifest([group('first', [spec('tests/a.spec.ts')])]), shardManifest([group('first', [spec('tests/a.spec.ts', 11)])]), shardManifest([group('first', [spec('tests/a.spec.ts', 12)])])], shards, 1)
fixture('web shard divergent launch flags', [shardBase, { ...shardBase, groups: [{ ...shardBase.groups[0], flags: ['--workers=2'] }] }, { ...shardBase, groups: [{ ...shardBase.groups[0], flags: ['--workers=4'] }] }], shards, 1)
fixture('spec duplicates introduced across merged groups', [shardManifest([]), shardManifest([group('first', [spec('tests/a.spec.ts')])]), shardManifest([group('second', [spec('tests/a.spec.ts')])])], shards, 1)
fixture('unsupported manifest path', [base, ours, theirs], 'api/openapi.yaml', 2)
for (const [name, invalid] of [
  ['identical duplicate keys', manifest([row('TestZ'), row('TestZ')])],
  ['divergent duplicate keys', manifest([row('TestZ'), row('TestZ', 'NIGHTLY')])],
  ['malformed JSON', '{bad JSON'],
  ['invalid row identity', manifest([{ kind: 'go', name: 'TestA' }])],
]) for (let i = 0; i < 3; i++) {
  const inputs = [base, base, base]; inputs[i] = invalid
  fixture(`${name} in ${['BASE', 'OURS', 'THEIRS'][i]}`, inputs, go, 2)
}
fixture('web shard duplicate spec identity', [shardBase, shardManifest([group('first', [spec('tests/a.spec.ts'), spec('tests/a.spec.ts')])]), shardBase], shards, 2)

for (const { name, inputs, file, status, expected } of cases) test(`OPS-257 standalone/in-repo parity: ${name}`, () => {
  // Reversing the sides also proves determinism and that either failure leaves
  // the exact original OURS bytes alone, including malformed OURS input.
  for (const sides of [inputs, [inputs[0], inputs[2], inputs[1]]]) {
    const external = run('standalone', [standalone], sides, file)
    const internal = run('in-repo', [cli, 'manifests', 'merge-driver'], sides, file)
    assert.equal(external.status, status, external.stderr)
    assert.equal(internal.status, external.status, internal.stderr)
    assert.deepEqual(external.output, internal.output)
    assert.equal(external.stderr, internal.stderr)
    if (status === 0) {
      assert.equal(external.output.toString(), formatManifest(expected, file))
    } else {
      assert.ok(external.stderr.trim())
      assert.deepEqual(external.output, external.before)
      assert.deepEqual(internal.output, internal.before)
    }
  }
})

test('OPS-257 both driver CLIs reject missing/extra arguments without touching OURS', () => {
  const fixture = mkdtempSync(join(directory, 'usage-'))
  const paths = ['base', 'ours', 'theirs'].map(name => join(fixture, name))
  paths.forEach(path => writeFileSync(path, source(base)))
  const before = readFileSync(paths[1])
  for (const program of [[standalone], [cli, 'manifests', 'merge-driver']]) {
    for (const args of [[], paths, [...paths, go, 'extra'], [paths[0], paths[1], join(fixture, 'missing'), go]]) {
      const result = spawnSync(process.execPath, [...program, ...args], { cwd: directory, encoding: 'utf8', timeout: 10_000 })
      assert.ifError(result.error)
      assert.equal(result.status, 2, result.stderr)
      assert.ok(result.stderr.trim())
      assert.deepEqual(readFileSync(paths[1]), before)
    }
  }
})

test('OPS-257 installed standalone driver reads no repo files from an old external checkout', () => {
  const installed = join(directory, 'tiers-merge-driver.mjs')
  copyFileSync(standalone, installed)
  const oldCheckout = join(directory, 'old-checkout')
  mkdirSync(oldCheckout)
  writeFileSync(join(oldCheckout, 'core.mjs'), "throw new Error('old core must not be imported')\n")
  // Node itself enforces the read allowlist. The copied driver has no access
  // to the repository, even through its original absolute path or symlinks.
  const permissions = ['--permission', `--allow-fs-read=${directory}`, `--allow-fs-write=${directory}`]
  const denied = spawnSync(process.execPath, [...permissions, '-e', "require('node:fs').readFileSync(process.argv[1])", standalone], { cwd: oldCheckout, encoding: 'utf8', timeout: 10_000 })
  assert.ifError(denied.error)
  assert.equal(denied.status, 1)
  assert.match(denied.stderr, /ERR_ACCESS_DENIED/)
  const result = run('installed', [installed], [base, ours, theirs], go, permissions, oldCheckout)
  assert.equal(result.status, 0, result.stderr)
  assert.equal(result.output.toString(), formatManifest(combined, go))
})
