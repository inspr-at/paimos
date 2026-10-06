// SPDX-License-Identifier: AGPL-3.0-only
// Runs in the fixed script-test lane through test-tiers.test.mjs.
import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, writeFileSync, readdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { formatManifest, manifestPaths } from './manifests.mjs'

const root = fileURLToPath(new URL('../../', import.meta.url))
const standalone = resolve(root, 'scripts/test-tiers/tiers-merge-driver.mjs')
const cli = resolve(root, 'scripts/test-tiers/cli.mjs')
const directory = realpathSync(mkdtempSync(join(tmpdir(), 'ops257-l13-fix2-driver-')))
after(() => rmSync(directory, { recursive: true, force: true }))
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
  return { ...result, before, output: readFileSync(paths[1]), remainingFiles: readdirSync(fixture) }
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
fixture('Go occurrence is not a registration identity', [base, manifest([{ ...row('TestZ'), occurrence: 1 }]), base], go, 2)
fixture('web shard duplicate spec identity', [shardBase, shardManifest([group('first', [spec('tests/a.spec.ts'), spec('tests/a.spec.ts')])]), shardBase], shards, 2)

for (const { name, inputs, file, status, expected } of cases) test(`OPS-257 standalone/in-repo parity: ${name}`, () => {
  // Reversing the sides proves deterministic clean merges. Conflicts retain
  // both sides under markers; setup errors retain the exact original OURS.
  for (const sides of [inputs, [inputs[0], inputs[2], inputs[1]]]) {
    const external = run('standalone', [standalone], sides, file)
    const internal = run('in-repo', [cli, 'manifests', 'merge-driver'], sides, file)
    assert.equal(external.status, status, external.stderr)
    assert.equal(internal.status, external.status, internal.stderr)
    assert.deepEqual(external.output, internal.output)
    assert.equal(external.stderr, internal.stderr)
    if (status === 0) {
      assert.equal(external.output.toString(), formatManifest(expected, file))
    } else if (status === 1) {
      assert.ok(external.stderr.trim())
      const versions = external.output.toString().match(/^<<<<<<< ours\n([\s\S]*)\n\|{7} base\n([\s\S]*)\n=======\n([\s\S]*)\n>>>>>>> theirs\n$/)
      assert.ok(versions, 'conflict contains every side under diff3 markers')
      assert.throws(() => JSON.parse(external.output), SyntaxError)
      assert.deepEqual(versions.slice(1), [sides[1], sides[0], sides[2]].map(data => formatManifest(typeof data === 'string' ? JSON.parse(data) : data, file).trimEnd()))
    } else {
      assert.ok(external.stderr.trim())
      assert.deepEqual(external.output, external.before)
      assert.deepEqual(internal.output, internal.before)
    }
    assert.deepEqual(external.remainingFiles.sort(), ['base', 'ours', 'theirs'])
    assert.deepEqual(internal.remainingFiles.sort(), ['base', 'ours', 'theirs'])
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
  // Node's permission mode disables fsync even with a write allowlist. Use
  // module/read guards instead, so this isolation test executes the real fsync.
  const guard = join(directory, 'isolation-guard.mjs')
  writeFileSync(guard, `import fs from 'node:fs'
import { registerHooks, syncBuiltinESMExports } from 'node:module'
import { fileURLToPath } from 'node:url'
import { dirname, resolve, sep } from 'node:path'
const allowed = fs.realpathSync(dirname(fileURLToPath(import.meta.url))) + sep
function check(path) {
  if (!fs.realpathSync(resolve(path instanceof URL ? fileURLToPath(path) : path)).startsWith(allowed)) throw new Error('ERR_ACCESS_DENIED: fixture read allowlist')
}
for (const name of ['readFileSync', 'statSync']) {
  const original = fs[name]
  fs[name] = (path, ...args) => { check(path); return original(path, ...args) }
}
syncBuiltinESMExports()
registerHooks({ resolve(specifier, context, next) {
  const result = next(specifier, context)
  if (result.url.startsWith('file:')) check(fileURLToPath(result.url))
  return result
} })
`)
  const permissions = ['--import', guard]
  const denied = spawnSync(process.execPath, [...permissions, '-e', "require('node:fs').readFileSync(process.argv[1])", standalone], { cwd: oldCheckout, encoding: 'utf8', timeout: 10_000 })
  assert.ifError(denied.error)
  assert.equal(denied.status, 1)
  assert.match(denied.stderr, /ERR_ACCESS_DENIED/)
  const deniedImport = spawnSync(process.execPath, [...permissions, standalone], { cwd: oldCheckout, encoding: 'utf8', timeout: 10_000 })
  assert.ifError(deniedImport.error)
  assert.equal(deniedImport.status, 1)
  assert.match(deniedImport.stderr, /ERR_ACCESS_DENIED/)
  const result = run('installed', [installed], [base, ours, theirs], go, permissions, oldCheckout)
  assert.equal(result.status, 0, result.stderr)
  assert.equal(result.output.toString(), formatManifest(combined, go))
})


for (const code of [undefined, 'ERR_INVALID_ARG_TYPE', 'ERR_OUT_OF_RANGE', 'UNKNOWN_ERROR']) {
  test(`OPS-257 both driver CLIs report unexpected ${code ?? 'uncoded'} crashes as 3 and preserve OURS`, () => {
    const injected = join(directory, `crash-${code ?? 'uncoded'}.mjs`)
    writeFileSync(injected, `import fs from 'node:fs'
  import { syncBuiltinESMExports } from 'node:module'
  fs.statSync = () => { throw Object.assign(new TypeError('injected unexpected crash'), ${code ? `{ code: '${code}' }` : '{}'}) }
  syncBuiltinESMExports()
  `)
    for (const program of [[standalone], [cli, 'manifests', 'merge-driver']]) {
      const result = run('crash', program, [base, ours, theirs], go, ['--import', injected])
      assert.equal(result.status, 3, result.stderr)
      assert.match(result.stderr, /injected unexpected crash/)
      assert.deepEqual(result.output, result.before)
      assert.deepEqual(result.remainingFiles.sort(), ['base', 'ours', 'theirs'])
    }
  })
}

for (const code of ['ENOENT', 'EACCES', 'EISDIR', 'ENOSPC', 'EROFS', 'EPERM', 'EMFILE', 'ENFILE', 'ENOTDIR', 'ELOOP', 'ENAMETOOLONG', 'EIO']) {
  test(`OPS-257 both driver CLIs report filesystem ${code} as 2 and preserve OURS`, () => {
    const injected = join(directory, `filesystem-${code}.mjs`)
    writeFileSync(injected, `import fs from 'node:fs'
import { syncBuiltinESMExports } from 'node:module'
fs.statSync = () => { throw Object.assign(new Error('injected filesystem ${code}'), { code: '${code}' }) }
syncBuiltinESMExports()
`)
    for (const program of [[standalone], [cli, 'manifests', 'merge-driver']]) {
      const result = run('filesystem', program, [base, ours, theirs], go, ['--import', injected])
      assert.equal(result.status, 2, result.stderr)
      assert.match(result.stderr, new RegExp(`injected filesystem ${code}`))
      assert.deepEqual(result.output, result.before)
      assert.deepEqual(result.remainingFiles.sort(), ['base', 'ours', 'theirs'])
    }
  })
}

for (const operation of ['writeFileSync', 'fsyncSync', 'renameSync']) {
  test(`OPS-257 atomic ${operation} failure preserves OURS on clean and conflict paths`, () => {
    const injected = join(directory, `fail-${operation}.mjs`)
    writeFileSync(injected, `import fs from 'node:fs'
import { syncBuiltinESMExports } from 'node:module'
const original = fs.${operation}
fs.${operation} = (...args) => {
  ${operation === 'writeFileSync' ? "original(args[0], 'partial temporary bytes')" : ''}
  throw Object.assign(new Error('injected ${operation} failure'), { code: 'EIO' })
}
syncBuiltinESMExports()
`)
    for (const program of [[standalone], [cli, 'manifests', 'merge-driver']]) {
      for (const inputs of [[base, ours, theirs], [base, manifest([row('TestZ', 'ESSENTIAL')]), manifest([row('TestZ', 'NIGHTLY')])]]) {
        const result = run('atomic', program, inputs, go, ['--import', injected])
        assert.equal(result.status, 2, result.stderr)
        assert.match(result.stderr, new RegExp(`injected ${operation} failure`))
        assert.deepEqual(result.output, result.before)
        assert.deepEqual(result.remainingFiles.sort(), ['base', 'ours', 'theirs'])
      }
    }
  })
}

test('OPS-257 both drivers apply one-sided group reorder alongside the other side spec edits', () => {
  const base = shardManifest(['a', 'b', 'c'].map(id => group(id, [spec(`tests/${id}.spec.ts`)])))
  const ours = structuredClone(base), theirs = structuredClone(base)
  ours.groups.reverse()
  theirs.groups[0].specs.push(spec('tests/new.spec.ts', 17))
  const expected = structuredClone(theirs); expected.groups.reverse()
  for (const inputs of [[base, ours, theirs], [base, theirs, ours]]) {
    for (const program of [[standalone], [cli, 'manifests', 'merge-driver']]) {
      const result = run('reorder', program, inputs, shards)
      assert.equal(result.status, 0, result.stderr)
      assert.equal(result.output.toString(), formatManifest(expected, shards))
    }
  }
})

test('OPS-257 standalone full-size old/canonical merges retain the union idempotently', () => {
  const rows = Array.from({ length: 3_800 }, (_, i) => row(`TestExisting${String(i).padStart(4, '0')}`, i % 2 ? 'NIGHTLY' : 'GATED-FULL'))
  const base = manifest([...rows].reverse())
  const ours = manifest([row('TestAddedOurs', 'ESSENTIAL'), ...base.tests])
  const theirs = manifest([row('TestAddedTheirs'), ...base.tests])
  const expected = formatManifest(manifest([ours.tests[0], theirs.tests[0], ...rows]), go)
  const result = run('full-size', [standalone], [base, ours, formatManifest(theirs, go)], go)
  assert.equal(result.status, 0, result.stderr)
  assert.equal(result.output.toString(), expected)
  const again = run('full-size-idempotent', [standalone], [expected, expected, expected], go)
  assert.equal(again.status, 0, again.stderr)
  assert.equal(again.output.toString(), expected)
})

test('OPS-257 both drivers merge real pre-L13 data with canonical sides idempotently', t => {
  const commit = '5b74bc113c0ebc2c124fd7ecd0aa17b09deb9190'
  const available = spawnSync('git', ['cat-file', '-e', `${commit}^{commit}`], { cwd: root, encoding: 'utf8', timeout: 10_000 })
  assert.ifError(available.error)
  if (available.status !== 0) {
    assert.match(available.stderr, /Not a valid object name|could not get object info|bad object/i)
    t.skip('historical pre-L13 commit is absent from the local clone')
    return
  }
  const historical = spawnSync('git', ['show', `${commit}:${go}`], { cwd: root, encoding: 'utf8', timeout: 10_000, maxBuffer: 32 * 1024 * 1024 })
  assert.ifError(historical.error)
  assert.equal(historical.status, 0, historical.stderr)
  const base = JSON.parse(historical.stdout)
  assert.ok(base.tests.length > 3_000, 'fixture uses the full historical inventory')
  const ours = { ...base, tests: [row('TestOPS257HistoricalAddedOurs', 'ESSENTIAL'), ...base.tests] }
  const theirs = { ...base, tests: [row('TestOPS257HistoricalAddedTheirs', 'NIGHTLY'), ...base.tests] }
  const expected = formatManifest({ ...base, tests: [ours.tests[0], theirs.tests[0], ...base.tests] }, go)
  for (const program of [[standalone], [cli, 'manifests', 'merge-driver']]) {
    for (const inputs of [[historical.stdout, ours, formatManifest(theirs, go)], [historical.stdout, formatManifest(theirs, go), ours]]) {
      const result = run('historical', program, inputs, go)
      assert.equal(result.status, 0, result.stderr)
      assert.equal(result.output.toString(), expected)
      assert.equal(JSON.parse(result.output).tests.length, base.tests.length + 2)
      const again = run('historical-idempotent', program, [expected, expected, expected], go)
      assert.equal(again.status, 0, again.stderr)
      assert.equal(again.output.toString(), expected)
    }
  }
})
