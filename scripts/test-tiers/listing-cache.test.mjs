// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { execFileSync, spawn } from 'node:child_process'
import { cachedListing, cachedGoList, cachedGoSource, cacheEnabled, inputSnapshot, listingKey, goPackageKeys, withListingLock } from './listing-cache.mjs'
import { classify } from './cli.mjs'
import { fixedEnvironment } from '../ci-static.mjs'

const temporary = t => {
  const directory = mkdtempSync(resolve(tmpdir(), 'aeon-1200-'))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  return directory
}
const command = (bin, args, { cwd } = {}) => execFileSync(bin, args, { cwd, encoding: 'utf8' })
function tree(directory) {
  mkdirSync(resolve(directory, 'internal/a'), { recursive: true })
  mkdirSync(resolve(directory, 'internal/b'), { recursive: true })
  writeFileSync(resolve(directory, 'go.mod'), 'module fixture\n')
  for (const owner of ['a', 'b']) {
    writeFileSync(resolve(directory, `internal/${owner}/source.go`), `package ${owner}\n`)
    writeFileSync(resolve(directory, `internal/${owner}/source_test.go`), `package ${owner}\n// test\n`)
  }
  command('git', ['init', '-q'], { cwd: directory })
  command('git', ['add', '.'], { cwd: directory })
}

test('AEON-1200 cached and uncached classify produce identical manifests across trees and preserve tiers', t => {
  const first = temporary(t), second = temporary(t), directory = temporary(t)
  tree(first); tree(second)
  const before = inputSnapshot(first, command), same = inputSnapshot(second, command)
  assert.deepEqual(before, same, 'checkout location and mtime are absent from the key')
  const stamp = listingKey(before, 'go-source', { version: 'fixture' })
  let calls = 0
  const native = { tests: [{ kind: 'go', package: 'internal/a', name: 'TestKnown', active: true },
    { kind: 'go', package: 'internal/b', name: 'TestAdded', active: true }], imports: {} }
  const collect = () => { calls++; return structuredClone(native) }
  const cold = cachedListing(stamp, collect, { directory })
  cold.tests[0].name = 'mutated by caller'
  const warm = cachedListing(listingKey(same, 'go-source', { version: 'fixture' }), collect, { directory })
  assert.equal(calls, 1)
  assert.deepEqual(warm, native, 'a caller cannot mutate the persisted inventory')
  const source = { version: 1, tests: [{ kind: 'go', package: 'internal/a', name: 'TestKnown', tier: 'ESSENTIAL' }] }
  const outputs = []
  for (const inventory of [warm, cachedListing(stamp, collect, { directory, enabled: false })]) {
    assert.equal(classify(['--tier', 'NIGHTLY', '--kind', 'go'], {
      collect: () => inventory, read: () => structuredClone(source), save: (_file, value) => outputs.push(value),
    }), 0)
  }
  assert.deepEqual(outputs[0], outputs[1], 'golden cached/uncached classify output')
  assert.equal(outputs[0].tests[0].tier, 'ESSENTIAL')
  assert.deepEqual(outputs[0].postGateCases, ['internal/b:TestAdded'])
  writeFileSync(resolve(first, 'internal/a/new_test.go'), '// untracked test\n')
  const added = inputSnapshot(first, command)
  assert.notEqual(listingKey(added, 'go-source', {}), listingKey(before, 'go-source', {}))
  rmSync(resolve(first, 'internal/a/new_test.go'))
  rmSync(resolve(first, 'internal/a/source_test.go'))
  assert.notDeepEqual(inputSnapshot(first, command), before, 'deleted tracked tests invalidate discovery')
})

test('AEON-1200 native Go cache refreshes only the changed test owner and its changed source consumers', t => {
  const base = temporary(t), directory = temporary(t)
  tree(base)
  const inventory = { imports: { 'internal/a': [], 'internal/b': ['internal/a'] } }, identity = { module: 'fixture' }
  const initial = inputSnapshot(base, command)
  const keys = goPackageKeys(initial, inventory, identity), calls = []
  const list = owners => { calls.push(owners); return Object.fromEntries(owners.map(owner => [owner, [`Test${owner.at(-1)}`]])) }
  const golden = cachedGoList(keys, list, { directory, enabled: false })
  assert.deepEqual(cachedGoList(keys, list, { directory }), golden)
  assert.deepEqual(cachedGoList(keys, () => { throw new Error('warm cache compiled a test binary') }, { directory }), golden)
  writeFileSync(resolve(base, 'internal/a/source_test.go'), 'package a\n// changed test\n')
  const testKeys = goPackageKeys(inputSnapshot(base, command), inventory, identity)
  assert.notEqual(testKeys['internal/a'], keys['internal/a'])
  assert.equal(testKeys['internal/b'], keys['internal/b'], 'dependency tests are not part of the consumer binary')
  const refreshed = cachedGoList(testKeys, list, { directory })
  assert.deepEqual(refreshed, golden)
  assert.deepEqual(Object.keys(refreshed), Object.keys(keys), 'partial hits retain the cold listing order')
  assert.deepEqual(calls.at(-1), ['internal/a'])
  writeFileSync(resolve(base, 'internal/a/source.go'), 'package a\n// changed source\n')
  const sourceKeys = goPackageKeys(inputSnapshot(base, command), inventory, identity)
  cachedGoList(sourceKeys, list, { directory })
  assert.deepEqual(calls.at(-1), ['internal/a', 'internal/b'])
  assert.notEqual(goPackageKeys(initial, inventory, { ...identity, GOOS: 'other' })['internal/a'], keys['internal/a'])
})

test('AEON-1200 native Go cache survives tier registration and web edits while tracking package assets', t => {
  const base = temporary(t), directory = temporary(t)
  tree(base)
  const files = {
    'scripts/ci/go-test-tiers.json': '{}', 'scripts/ci/web-test-tiers.json': '{}', 'web/ci-web-shards.json': '{}',
    'web/src/view.ts': '// view', 'docs/example.txt': 'documentation',
    'internal/a/assets/template.txt': 'embedded template', 'internal/a/native.c': '// native source',
    'internal/b/native.s': '// assembly source', 'internal/ab/assets/template.txt': 'unrelated asset',
    'go.sum': '', 'go.work': 'go 1.26', 'go.work.sum': '', 'root.go': 'package fixture',
    'scripts/test-tiers/collect.mjs': '// collector', 'scripts/test-tier-go/main.go': 'package main',
  }
  for (const [file, body] of Object.entries(files)) {
    mkdirSync(resolve(base, file, '..'), { recursive: true })
    writeFileSync(resolve(base, file), body)
  }
  command('git', ['add', '.'], { cwd: base })
  const inventory = { imports: { 'internal/a': [], 'internal/b': ['internal/a'], 'internal/c': [] } }
  const identity = { module: 'fixture' }, initial = inputSnapshot(base, command)
  const keys = goPackageKeys(initial, inventory, identity), calls = []
  const list = owners => {
    calls.push(owners)
    return Object.fromEntries(owners.map(owner => [owner, [`Test${owner.at(-1)}`]]))
  }
  const golden = cachedGoList(keys, list, { directory })
  for (const file of ['scripts/ci/go-test-tiers.json', 'scripts/ci/web-test-tiers.json', 'web/ci-web-shards.json',
    'web/src/view.ts', 'docs/example.txt', 'internal/ab/assets/template.txt']) {
    writeFileSync(resolve(base, file), `${files[file]}\nchanged`)
    assert.deepEqual(goPackageKeys(inputSnapshot(base, command), inventory, identity), keys, `${file} does not change a Go test binary`)
  }
  assert.deepEqual(cachedGoList(goPackageKeys(inputSnapshot(base, command), inventory, identity), () => {
    throw new Error('tier registration or unrelated content compiled a Go test binary')
  }, { directory }), golden)
  writeFileSync(resolve(base, 'internal/a/source_test.go'), 'package a\n// changed test\n')
  cachedGoList(goPackageKeys(inputSnapshot(base, command), inventory, identity), list, { directory })
  assert.deepEqual(calls, [['internal/a', 'internal/b', 'internal/c'], ['internal/a']], 'test edit plus registration refreshes only its owner')
  for (const [file, owners] of [['internal/a/assets/template.txt', ['internal/a', 'internal/b']],
    ['internal/a/native.c', ['internal/a', 'internal/b']], ['internal/b/native.s', ['internal/b']]]) {
    writeFileSync(resolve(base, file), `${files[file]}\nchanged`)
    assert.deepEqual(cachedGoList(goPackageKeys(inputSnapshot(base, command), inventory, identity), list, { directory }), golden)
    assert.deepEqual(calls.at(-1), owners, `${file} refreshes its owner and transitive consumers`)
  }
  for (const file of ['go.mod', 'go.sum', 'go.work', 'go.work.sum', 'root.go',
    'scripts/test-tiers/collect.mjs', 'scripts/test-tier-go/main.go']) {
    const changed = initial.map(row => row[0] === file ? [file, 'changed', row[2]] : row)
    const next = goPackageKeys(changed, inventory, identity)
    for (const owner of Object.keys(keys)) assert.notEqual(next[owner], keys[owner], `${file} remains a global Go listing input`)
  }
})

test('AEON-1200 static typecheck scratch configs cannot invalidate a concurrent native Go listing', t => {
  const base = temporary(t), directory = temporary(t)
  tree(base)
  mkdirSync(resolve(base, 'web/src'), { recursive: true })
  writeFileSync(resolve(base, 'web/static_stub.go'), 'package web\n')
  writeFileSync(resolve(base, 'web/src/view.ts'), '// view\n')
  command('git', ['add', '.'], { cwd: base })
  const inventory = { imports: { web: [], 'internal/a': ['web'], 'internal/b': [] } }, identity = { module: 'fixture' }
  const initial = inputSnapshot(base, command), keys = goPackageKeys(initial, inventory, identity)
  const calls = [], list = owners => {
    calls.push(owners)
    return Object.fromEntries(owners.map(owner => [owner, ['TestKnown']]))
  }
  const golden = cachedGoList(keys, list, { directory })
  // ci-static deliberately keeps these generated configs below web/ for
  // TypeScript module resolution; they are not source or Go build inputs.
  const scratch = resolve(base, 'web/.ci-static-typecheck-fixture')
  mkdirSync(scratch)
  for (const name of ['app', 'node', 'tsconfig']) writeFileSync(resolve(scratch, `${name}.json`), '{}')
  const during = inputSnapshot(base, command)
  assert.deepEqual(during, initial, 'reserved typecheck output is absent from the source snapshot')
  assert.deepEqual(cachedGoList(goPackageKeys(during, inventory, identity), list, { directory }), golden)
  assert.equal(calls.length, 1, 'creating static-check output does not compile a Go test binary')
  rmSync(scratch, { recursive: true })
  assert.deepEqual(inputSnapshot(base, command), initial, 'scratch cleanup also leaves discovery stable')
  writeFileSync(resolve(base, 'web/src/view.ts'), '// changed view\n')
  cachedGoList(goPackageKeys(inputSnapshot(base, command), inventory, identity), list, { directory })
  assert.deepEqual(calls.at(-1), ['internal/a', 'web'], 'ordinary web package content still invalidates its consumer')
})

test('AEON-1200 source discovery refreshes only an edited package and retains current active imports', t => {
  const base = temporary(t), directory = temporary(t)
  tree(base)
  const inventory = { tests: [{ package: 'internal/a', file: 'internal/a/source_test.go', name: 'TestOld', active: true },
    { package: 'internal/b', file: 'internal/b/source_test.go', name: 'TestRetained', active: false }],
    imports: { 'internal/a': [], 'internal/b': ['internal/a'] } }
  const updated = { tests: [{ package: 'internal/a', file: 'internal/a/source_test.go', name: 'TestNew', active: true }], imports: ['internal/b'] }
  const calls = []
  const collectors = { collectAll: () => { calls.push('all'); return structuredClone(inventory) },
    collectPackage: owner => { calls.push(owner); return updated } }
  const snapshot = inputSnapshot(base, command)
  assert.deepEqual(cachedGoSource(snapshot, {}, collectors, { directory }), inventory)
  assert.deepEqual(cachedGoSource(snapshot, {}, collectors, { directory }), inventory)
  assert.deepEqual(calls, ['all'])
  writeFileSync(resolve(base, 'internal/a/source_test.go'), 'package a\n// new tests and imports\n')
  const next = inputSnapshot(base, command)
  const actual = cachedGoSource(next, {}, collectors, { directory })
  assert.deepEqual(actual, { tests: [inventory.tests[1], ...updated.tests], imports: { 'internal/a': ['internal/b'], 'internal/b': ['internal/a'] } })
  assert.deepEqual(calls, ['all', 'internal/a'])
  cachedGoSource(next, {}, collectors, { directory })
  assert.deepEqual(calls, ['all', 'internal/a'])
})

test('AEON-1200 corrupt and failed listings cannot become cache hits; CI bypass survives ci-static sanitizing', t => {
  const directory = temporary(t), key = 'a'.repeat(64), file = resolve(directory, `${key}.json`)
  writeFileSync(file, '{broken')
  assert.deepEqual(cachedListing(key, () => ({ tests: [] }), { directory }), { tests: [] })
  const entry = JSON.parse(readFileSync(file, 'utf8'))
  entry.value.tests.push({ name: 'corrupted' }); writeFileSync(file, JSON.stringify(entry))
  let refreshed = 0
  assert.deepEqual(cachedListing(key, () => { refreshed++; return { tests: [] } }, { directory }), { tests: [] })
  assert.equal(refreshed, 1)
  assert.throws(() => cachedListing(key, () => { throw new Error('native listing failed') }, { directory, fresh: true }), /native listing failed/)
  assert.deepEqual(cachedListing(key, () => { throw new Error('must remain cached') }, { directory }), { tests: [] })
  const missing = 'b'.repeat(64)
  assert.throws(() => cachedGoList({ owner: missing }, () => { throw new Error('compile failed') }, { directory }), /compile failed/)
  assert.deepEqual(cachedGoList({ owner: missing }, () => ({ owner: [] }), { directory }), { owner: [] })
  assert.equal(cacheEnabled({ GITHUB_ACTIONS: 'true' }), false)
  assert.equal(cacheEnabled(fixedEnvironment({ GITHUB_ACTIONS: 'true' })), false)
  assert.equal(cacheEnabled(fixedEnvironment({ AEON_TEST_TIER_CACHE: '0' })), false)
  assert.equal(cacheEnabled({}), true)
  mkdirSync(resolve(directory, 'listing.lock'))
  writeFileSync(resolve(directory, 'listing.lock/owner.json'), JSON.stringify({ pid: process.pid }))
  assert.throws(() => withListingLock(directory, () => {}, { timeout: 0 }), /Timed out/)
  rmSync(resolve(directory, 'listing.lock'), { recursive: true })
})

test('AEON-1200 parallel processes share one listing under an established lock overlap', async t => {
  const directory = temporary(t), module = new URL('./listing-cache.mjs', import.meta.url).href
  const script = `
    import { cachedListing } from ${JSON.stringify(module)};
    import { readFileSync } from 'node:fs';
    const result = cachedListing('c'.repeat(64), () => {
      process.send({ type: 'compute' });
      readFileSync(0, 'utf8');
      return { tests: ['native inventory'] };
    }, { directory: process.argv[1], lockOptions: { onWait: () => process.send({ type: 'waiting' }) } });
    process.send({ type: 'done', result });
    process.disconnect();
  `
  const messages = [], children = []
  let waitFor
  const event = type => new Promise(resolveEvent => {
    if (messages.some(message => message.type === type)) resolveEvent()
    else waitFor = { type, resolve: resolveEvent }
  })
  const start = () => {
    const child = spawn(process.execPath, ['--input-type=module', '-e', script, directory], { stdio: ['pipe', 'ignore', 'pipe', 'ipc'] })
    children.push(child)
    let stderr = ''
    child.stderr.on('data', data => { stderr += data })
    child.on('message', message => {
      messages.push(message)
      if (message.type === waitFor?.type) { const waiter = waitFor; waitFor = undefined; waiter.resolve() }
    })
    child.completion = new Promise((resolveChild, reject) => {
      child.on('error', reject)
      child.on('exit', code => code === 0 ? resolveChild() : reject(new Error(`listing child exited ${code}: ${stderr}`)))
    })
    return child
  }
  const timer = setTimeout(() => { for (const child of children) child.kill() }, 10_000)
  t.after(() => { clearTimeout(timer); for (const child of children) if (child.exitCode === null) child.kill() })
  const first = start()
  await Promise.race([event('compute'), first.completion.then(() => { throw new Error('first exited before collecting') })])
  const second = start()
  await Promise.race([event('waiting'), second.completion.then(() => { throw new Error('second did not contend for lock') })])
  first.stdin.end('release'); second.stdin.end('release')
  await Promise.all(children.map(child => child.completion))
  assert.equal(messages.filter(message => message.type === 'compute').length, 1)
  assert.deepEqual(messages.filter(message => message.type === 'done').map(message => message.result),
    [{ tests: ['native inventory'] }, { tests: ['native inventory'] }])
})
