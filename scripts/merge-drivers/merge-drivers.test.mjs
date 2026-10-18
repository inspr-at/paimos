// SPDX-License-Identifier: AGPL-3.0-only
// Registered in the fixed script-test lane by test-tiers.test.mjs.
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, writeFileSync, mkdirSync, copyFileSync, readdirSync, symlinkSync, truncateSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { spawnSync } from 'node:child_process'
import { registryPaths, mergeRegistries, normalizeRegistry, formatRegistry, parseRegistry, validateRegistry } from './registries.mjs'
import { regenerate, main } from '../merge-main.mjs'
import { manifestPaths, formatManifest } from '../test-tiers/manifests.mjs'

const root = fileURLToPath(new URL('../../', import.meta.url))
const driver = fileURLToPath(new URL('./registries.mjs', import.meta.url))
const license = 'SPDX-License-Identifier: AGPL-3.0-only'
function temporary(t) {
  const path = mkdtempSync(join(tmpdir(), 'aeon-981-'))
  t.after(() => rmSync(path, { recursive: true, force: true }))
  return path
}
const grant = (key, agent = false) => ({ key, grantable_at: ['workspace'], agent_grantable: agent })
const row = name => ({ name, workspace: null, projects: [], want: [] })
const table = (name, columns = { tenant_id: 'metadata' }) => ({ table: name, classification: 'personal', locator: { tenant_column: 'tenant_id', person_column: 'actor_id' }, columns })
const fixtures = [
  { file: registryPaths[0], doc: rows => ({ _license: license, permissions: rows }), entry: name => `${name}.manage`, rows: doc => doc.permissions },
  { file: registryPaths[1], doc: rows => ({ resources: Object.fromEntries(rows.map(name => [name, name])), actions: {}, special: {} }), entry: name => name, rows: doc => Object.keys(doc.resources) },
  { file: registryPaths[2], doc: rows => ({ _license: license, registry: rows, cases: [row('base')] }), entry: name => grant(`${name}.manage`), rows: doc => doc.registry.map(value => value.key) },
  { file: registryPaths[3], doc: rows => ({ _license: license, version: 1, tables: rows }), entry: name => table(name), rows: doc => doc.tables.map(value => value.table) },
  { file: registryPaths[4], doc: rows => ({ _license: license, permissions: rows }), entry: name => `${name}.manage`, rows: doc => doc.permissions },
]
function invoke(t, inputs, file, { program = driver, extra = [] } = {}) {
  const directory = temporary(t)
  const paths = ['base', 'ours', 'theirs'].map(name => join(directory, name))
  inputs.forEach((value, i) => writeFileSync(paths[i], typeof value === 'string' ? value : JSON.stringify(value)))
  const before = readFileSync(paths[1], 'utf8')
  const result = spawnSync(process.execPath, [...extra, program, ...paths, file], { encoding: 'utf8', timeout: 10_000, cwd: directory })
  assert.ifError(result.error)
  assert.equal(result.signal, null)
  return { ...result, directory, before, output: readFileSync(paths[1], 'utf8') }
}

for (const fixture of fixtures) {
  test(`${fixture.file}: independent additions survive sorted and valid; unilateral deletion is honoured`, t => {
    const { file, doc, entry, rows } = fixture
    const base = doc([entry('z')]), ours = doc([entry('z'), entry('b')]), theirs = doc([entry('a'), entry('z')])
    const merged = invoke(t, [base, ours, theirs], file)
    assert.equal(merged.status, 0, merged.stderr)
    const data = JSON.parse(merged.output)
    validateRegistry(data, file)
    assert.deepEqual(rows(data), rows(normalizeRegistry(doc(['a', 'b', 'z'].map(entry)), file)))
    assert.equal(merged.output, formatRegistry(doc(['a', 'b', 'z'].map(entry)), file))
    for (const sides of [[doc([]), base], [base, doc([])], [doc([]), doc([])]]) {
      const deleted = invoke(t, [base, ...sides], file)
      assert.equal(deleted.status, 0, deleted.stderr)
      assert.deepEqual(rows(JSON.parse(deleted.output)), [])
    }
  })
}

test('permission and fixture records never combine changes into an unreviewed wider policy', t => {
  const file = registryPaths[2]
  const base = { _license: license, registry: [grant('nodes.write')], cases: [row('base')] }
  const ours = structuredClone(base), theirs = structuredClone(base)
  ours.registry[0].grantable_at.push('project')
  theirs.registry[0].agent_grantable = true
  const conflict = invoke(t, [base, ours, theirs], file)
  assert.equal(conflict.status, 1)
  assert.match(conflict.stderr, /Registry merge conflict:.*registry.nodes.write/)
  assert.match(conflict.output, /<<<<<<< ours/)
  assert.match(conflict.output, /\|\|\|\|\|\|\| base/)
  assert.match(conflict.output, />>>>>>> theirs/)
  assert.throws(() => JSON.parse(conflict.output))
  ours.registry = base.registry; theirs.registry = base.registry
  ours.cases[0].want = ['nodes.read']; theirs.cases[0].want = ['nodes.write']
  assert.equal(invoke(t, [base, ours, theirs], file).status, 1)
  const addedBase = { ...base, registry: [] }
  assert.equal(invoke(t, [addedBase, { ...base }, { ...base, registry: [grant('nodes.write', true)] }], file).status, 1)
})

test('privacy columns union by column; classification, locator and deletion disagreements stop', t => {
  const file = registryPaths[3], doc = columns => ({ _license: license, version: 1, tables: [table('records', columns)] })
  const base = doc({ tenant_id: 'metadata', id: 'metadata' })
  const ours = doc({ ...base.tables[0].columns, actor_id: 'personal' })
  const theirs = doc({ ...base.tables[0].columns, payload: 'review' })
  assert.deepEqual(mergeRegistries(base, ours, theirs, file).tables[0].columns,
    { actor_id: 'personal', id: 'metadata', payload: 'review', tenant_id: 'metadata' })
  // Two migrations can independently classify columns of an older table that
  // was absent from the base inventory. Its shared safety header must agree.
  const empty = { ...base, tables: [] }
  assert.deepEqual(mergeRegistries(empty, ours, theirs, file).tables[0].columns,
    { actor_id: 'personal', id: 'metadata', payload: 'review', tenant_id: 'metadata' })
  const conflictingNew = structuredClone(theirs)
  conflictingNew.tables[0].classification = 'secret'
  assert.equal(invoke(t, [empty, ours, conflictingNew], file).status, 1)
  for (const change of [
    value => { value.tables[0].columns.id = 'personal' },
    value => { value.tables[0].classification = 'metadata' },
    value => { value.tables[0].locator.person_column = 'owner_id' },
  ]) {
    const changed = structuredClone(base); change(changed)
    const deleted = { ...base, tables: [] }
    const conflict = invoke(t, [base, deleted, changed], file)
    assert.equal(conflict.status, 1, conflict.stderr)
    assert.match(conflict.stderr, /Registry merge conflict/)
  }
  const secret = doc({ tenant_id: 'metadata', id: 'secret' })
  const personal = doc({ tenant_id: 'metadata', id: 'personal' })
  assert.equal(invoke(t, [base, personal, secret], file).status, 1)
  const a = structuredClone(base), b = structuredClone(base)
  a.tables[0].locator.person_column = 'owner_id'; b.tables[0].locator.tenant_column = 'workspace_id'
  assert.equal(invoke(t, [base, a, b], file).status, 1)
  const changedClass = structuredClone(base)
  changedClass.tables[0].classification = 'metadata'
  assert.equal(invoke(t, [base, changedClass, a], file).status, 1)
})

test('labels with divergent values conflict; matching additions and one-sided edits survive', t => {
  const file = registryPaths[1], doc = label => ({ resources: { nodes: label }, actions: {}, special: {} })
  assert.equal(invoke(t, [doc('work'), doc('tasks'), doc('nodes')], file).status, 1)
  assert.equal(invoke(t, [doc('work'), doc('tasks'), doc('work')], file).status, 0)
  assert.equal(invoke(t, [{ resources: {}, actions: {}, special: {} }, doc('work'), doc('work')], file).status, 0)
})

test('malformed, duplicate and unknown-schema inputs fail before replacing OURS', t => {
  const base = { _license: license, permissions: ['nodes.write'] }
  for (const bad of [
    '{bad JSON', '{"_license":"SPDX-License-Identifier: AGPL-3.0-only","permissions":[],"permissio\\u006es":["nodes.write"]}',
    { ...base, permissions: ['nodes.write', 'nodes.write'] }, { ...base, permissions: ['*'] },
    { ...base, grants: [] }, { ...base, _license: 'wrong' },
  ]) for (const i of [0, 1, 2]) {
    const inputs = [base, base, base]; inputs[i] = bad
    const failure = invoke(t, inputs, registryPaths[0])
    assert.equal(failure.status, 2, failure.stderr)
    assert.equal(failure.output, failure.before)
  }
  assert.equal(invoke(t, [base, base, base], 'internal/authz/registry.go').status, 2)
  assert.throws(() => parseRegistry('['.repeat(65) + '0' + ']'.repeat(65)), /nesting/)
  const dir = temporary(t), file = join(dir, 'large')
  writeFileSync(file, '{}'); truncateSync(file, 4 * 1024 * 1024 + 1)
  const p = spawnSync(process.execPath, [driver, file, file, file, registryPaths[0]], { encoding: 'utf8', timeout: 10_000 })
  assert.equal(p.status, 2); assert.match(p.stderr, /4 MiB/)
})

test('installed driver runs without a checkout and failed atomic writes retain OURS', t => {
  const installed = join(temporary(t), 'reviewed-driver.mjs')
  copyFileSync(driver, installed)
  const base = { _license: license, permissions: ['nodes.write'] }, ours = { ...base, permissions: ['nodes.read', 'nodes.write'] }
  const clean = invoke(t, [base, ours, base], registryPaths[0], { program: installed })
  assert.equal(clean.status, 0)
  assert.equal(clean.output, formatRegistry(ours, registryPaths[0]))
  for (const operation of ['writeFileSync', 'fsyncSync', 'renameSync']) {
    const fault = join(temporary(t), 'fault.mjs')
    writeFileSync(fault, `import fs from 'node:fs'; import { syncBuiltinESMExports } from 'node:module'; fs.${operation} = () => { throw new Error('injected ${operation} failure') }; syncBuiltinESMExports();`)
    const failure = invoke(t, [base, ours, base], registryPaths[0], { program: installed, extra: ['--import', fault] })
    assert.equal(failure.status, 2)
    assert.match(failure.stderr, new RegExp(`injected ${operation} failure`))
    assert.equal(failure.output, failure.before)
    assert.deepEqual(readdirSync(failure.directory).sort(), ['base', 'ours', 'theirs'])
    const labels = name => ({ resources: { nodes: name }, actions: {}, special: {} })
    const conflict = invoke(t, [labels('base'), labels('ours'), labels('theirs')], registryPaths[1], { program: installed, extra: ['--import', fault] })
    assert.equal(conflict.status, 2)
    assert.match(conflict.stderr, new RegExp(`injected ${operation} failure`))
    assert.equal(conflict.output, conflict.before)
    assert.deepEqual(readdirSync(conflict.directory).sort(), ['base', 'ours', 'theirs'])
  }
})

function checkout(t) {
  const directory = temporary(t)
  for (const file of registryPaths) {
    mkdirSync(resolve(directory, file, '..'), { recursive: true })
    copyFileSync(resolve(root, file), resolve(directory, file))
  }
  for (const file of manifestPaths) {
    mkdirSync(resolve(directory, file, '..'), { recursive: true })
    const data = file === manifestPaths[2] ? { version: 1, groups: [] } : { version: 1, tests: [] }
    writeFileSync(resolve(directory, file), formatManifest(data, file))
  }
  return directory
}

test('regeneration is idempotent and invalid documents or symlinks prevent every write', t => {
  const directory = checkout(t)
  const semantic = registryPaths.map(file => normalizeRegistry(JSON.parse(readFileSync(resolve(directory, file), 'utf8')), file))
  // Only the exact global capability metadata may omit its tenant locator.
  for (const change of [r => { r.table = 'another_global_table' }, r => { r.classification = 'personal' },
    r => { r.columns.capability = 'personal' }, r => { r.locator.person_column = 'principal_id' }]) {
    const invalid = structuredClone(semantic[3])
    const capability = invalid.tables.find(r => r.table === 'aeon_required_capabilities')
    assert.ok(capability)
    change(capability)
    assert.throws(() => normalizeRegistry(invalid, registryPaths[3]), /Invalid tenant\/person locator/)
  }
  regenerate(directory)
  const first = [...registryPaths, ...manifestPaths].map(file => readFileSync(resolve(directory, file), 'utf8'))
  regenerate(directory)
  assert.deepEqual([...registryPaths, ...manifestPaths].map(file => readFileSync(resolve(directory, file), 'utf8')), first)
  assert.deepEqual(registryPaths.map(file => JSON.parse(readFileSync(resolve(directory, file), 'utf8'))), semantic)
  writeFileSync(resolve(directory, registryPaths[0]), JSON.stringify(semantic[0]))
  const before = readFileSync(resolve(directory, registryPaths[0]), 'utf8')
  writeFileSync(resolve(directory, manifestPaths[2]), '{bad')
  assert.throws(() => regenerate(directory))
  assert.equal(readFileSync(resolve(directory, registryPaths[0]), 'utf8'), before)
  const second = checkout(t), outside = join(temporary(t), 'outside')
  writeFileSync(outside, JSON.stringify(semantic[0]))
  rmSync(resolve(second, registryPaths[0]))
  symlinkSync(outside, resolve(second, registryPaths[0]))
  assert.throws(() => regenerate(second))
  assert.equal(readFileSync(outside, 'utf8'), JSON.stringify(semantic[0]))
})

test('merge-main refuses an unmerged index before writes and checks regenerated working bytes', t => {
  const directory = checkout(t), calls = [], log = [], initial = readFileSync(resolve(directory, registryPaths[0]), 'utf8')
  const result = (status = 0, stdout = '') => ({ status, stdout, stderr: '' })
  assert.equal(main(['--regenerate'], { root: directory, execute: () => result(0, 'unmerged index'), log: text => log.push(text) }), 1)
  assert.equal(readFileSync(resolve(directory, registryPaths[0]), 'utf8'), initial)
  const execute = (bin, args) => {
    calls.push([bin, args])
    if (args.includes('--here')) for (const file of registryPaths) assert.equal(readFileSync(resolve(directory, file), 'utf8'), formatRegistry(JSON.parse(readFileSync(resolve(directory, file), 'utf8')), file))
    return result()
  }
  assert.equal(main(['--regenerate'], { root: directory, execute, log: () => {} }), 0)
  assert.deepEqual(calls.at(-1)[1], ['scripts/ci-static.mjs', '--here', '--json'])
  assert.ok(calls.every(([, args]) => !args.includes('add') && !args.includes('commit') && !args.includes('merge')))
  for (const code of [1, 2, 3]) {
    assert.equal(main(['--regenerate'], { root: directory, execute: (bin, args) => result(args.includes('--here') ? code : 0), log: () => {} }), code)
  }
  assert.equal(main([], { root: directory, log: () => {} }), 2)
})
