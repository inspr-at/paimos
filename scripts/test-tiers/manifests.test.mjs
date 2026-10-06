// SPDX-License-Identifier: AGPL-3.0-only
// Classified in the fixed go-static/nightly script-test lane via the import
// in test-tiers.test.mjs; root scripts are not Go/web native registrations.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, mkdtempSync, mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { manifestPaths, formatManifest, normalizeManifest, checkManifests, writeManifests, mergeManifests, classifyManifest, fixCommand } from './manifests.mjs'
import { classifyArgs, classify } from './cli.mjs'
import { proveManifests, proveConversion } from './prove-manifests.mjs'

const root = fileURLToPath(new URL('../../', import.meta.url))
const goFile = manifestPaths[0], webFile = manifestPaths[1], shardsFile = manifestPaths[2]
const row = (name, tier = 'GATED-FULL', pkg = 'internal/auth') => ({ kind: 'go', package: pkg, name, tier })
const manifest = tests => ({ version: 1, defaultNewTier: 'NIGHTLY', tests })
const spec = (file, weightSeconds = 10) => ({ file, weightSeconds })
const group = (id, specs) => ({ id, config: 'playwright.ui.config.ts', project: null, flags: ['--workers=1'], hostedOnly: true, env: {}, specs })
const shards = groups => ({ version: 1, groups })
const roundtrip = (data, file = goFile) => JSON.parse(formatManifest(data, file))
function fixtureDirectory(t, prefix) {
  const directory = mkdtempSync(resolve(tmpdir(), prefix))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  return directory
}
const fixtures = t => {
  const directory = fixtureDirectory(t, 'ops257-manifest-test-')
  for (const [file, data] of [[goFile, manifest([row('TestZ'), row('TestA')])], [webFile, manifest([{ kind: 'node', file: 'tests/a.test.ts', name: 'case', tier: 'NIGHTLY' }])], [shardsFile, shards([group('first', [spec('tests/z.spec.ts'), spec('tests/a.spec.ts')])])]]) {
    mkdirSync(resolve(directory, file, '..'), { recursive: true })
    writeFileSync(resolve(directory, file), JSON.stringify(data, null, 2))
  }
  return directory
}
function driver(t, base, ours, theirs, file = goFile) {
  const directory = fixtureDirectory(t, 'ops257-driver-test-')
  const paths = ['base', 'ours', 'theirs'].map(name => resolve(directory, name))
  for (const [i, data] of [base, ours, theirs].entries()) writeFileSync(paths[i], typeof data === 'string' ? data : JSON.stringify(data))
  const before = readFileSync(paths[1], 'utf8')
  const result = spawnSync(process.execPath, [resolve(root, 'scripts/test-tiers/cli.mjs'), 'manifests', 'merge-driver', ...paths, file], { encoding: 'utf8', timeout: 10_000 })
  assert.ifError(result.error)
  return { ...result, before, output: readFileSync(paths[1], 'utf8') }
}

function assertUnresolved(result) {
  assert.match(result.output, /^<<<<<<< ours\n/m)
  assert.match(result.output, /\n\|{7} base\n/)
  assert.match(result.output, /\n=======\n/)
  assert.match(result.output, /\n>>>>>>> theirs\n/m)
  assert.throws(() => JSON.parse(result.output), SyntaxError)
}

test('OPS-257 committed CI manifests are canonical; failures name the fix command', t => {
  checkManifests(root)
  const directory = fixtures(t)
  assert.throws(() => checkManifests(directory), error => error.message.includes(goFile) && error.message.includes(fixCommand))
  writeManifests(directory)
  checkManifests(directory)
  const before = manifestPaths.map(file => readFileSync(resolve(directory, file), 'utf8'))
  writeManifests(directory)
  assert.deepEqual(manifestPaths.map(file => readFileSync(resolve(directory, file), 'utf8')), before)
})

test('OPS-257 independent equality proof is tied to a local commit and preserves every scalar', t => {
  const before = manifest([row('TestZ'), row('TestA')]), after = manifest([...before.tests].reverse())
  assert.equal(proveConversion(before, after, goFile).rows, 2)
  assert.throws(() => proveConversion(before, manifest([row('TestA', 'ESSENTIAL'), row('TestZ')]), goFile), /conversion changed data/)
  assert.throws(() => proveConversion(before, manifest([...after.tests, row('TestA')]), goFile), /conversion changed data/)
  const policy = shards([group('first', [spec('tests/a.spec.ts')])]), changed = structuredClone(policy)
  changed.groups[0].specs[0].weightSeconds++
  assert.throws(() => proveConversion(policy, changed, shardsFile), /per-file spec weights changed/)
  changed.groups[0].specs[0].weightSeconds--
  changed.groups[0].flags = ['--workers=2']
  assert.throws(() => proveConversion(policy, changed, shardsFile), /conversion changed data/)
  assert.throws(() => proveManifests('HEAD:api/openapi.yaml'), /full local commit SHA/)
})

test('OPS-257 normalization retains values, sorts rows/owner weights and preserves group/flag order', t => {
  const data = { ...manifest([row('TestZ'), row('TestA')]), postGateCases: ['z', 'a'], timingWeights: { owners: { z: { selectedTests: 4, seconds: 7 }, a: { seconds: 3, selectedTests: 1 } } } }
  assert.deepEqual(roundtrip(data), { ...data, tests: [row('TestA'), row('TestZ')], postGateCases: ['a', 'z'] })
  assert.deepEqual(Object.keys(roundtrip(data).timingWeights.owners), ['a', 'z'])
  const source = shards([group('z', [spec('tests/z.spec.ts', 4), spec('tests/a.spec.ts', 8)]), group('a', [spec('tests/b.spec.ts')])])
  source.groups[0].flags = ['--workers=1', '--forbid-only']
  const expected = structuredClone(source); expected.groups[0].specs.reverse()
  assert.deepEqual(roundtrip(source, shardsFile), expected)
  assert.deepEqual(source.groups[0].specs.map(s => s.file), ['tests/z.spec.ts', 'tests/a.spec.ts'], 'inputs remain unchanged')
})

test('OPS-257 stable keys disambiguate occurrences and row field order independently of input order', t => {
  const first = { tier: 'NIGHTLY', occurrence: 1, name: 'NaN#2', file: 'tests/a.test.ts', kind: 'node', extra: { z: 1, a: 2 } }
  const second = { ...first, occurrence: 2 }
  const data = manifest([second, first])
  assert.deepEqual(roundtrip(data, webFile).tests.map(row => row.occurrence), [1, 2])
  const text = formatManifest(data, webFile)
  assert.equal(text, formatManifest(manifest([{ ...first }, { ...second }]), webFile))
  assert.match(text, /\{"kind":"node","file":"tests\/a.test.ts","name":"NaN#2","occurrence":1,"extra":\{"a":2,"z":1\},"tier":"NIGHTLY"\}/)
})

test('OPS-257 row insertions at the start, middle, end and empty list never edit neighboring lines', t => {
  for (const initial of [[], [row('TestB')], [row('TestB'), row('TestD')]]) {
    for (const name of ['TestA', 'TestC', 'TestZ']) {
      const before = formatManifest(manifest(initial), goFile).split('\n')
      const after = formatManifest(manifest([...initial, row(name)]), goFile).split('\n')
      // The old text is a subsequence of the new text; there are exactly one
      // new row and (unless previously empty) one new delimiter line.
      let i = 0
      for (const line of after) if (line === before[i]) i++
      assert.equal(i, before.length)
      assert.equal(after.length - before.length, initial.length ? 2 : 1)
      const restored = formatManifest(manifest(roundtrip(manifest([...initial, row(name)])).tests.filter(r => r.name !== name)), goFile)
      assert.equal(restored, before.join('\n'))
    }
  }
})

test('OPS-257 canonical text merges separated edits in one package; same-gap insertions still need the driver', t => {
  const base = manifest(['TestA', 'TestC', 'TestE', 'TestG', 'TestI'].map(name => row(name)))
  const directory = fixtureDirectory(t, 'ops257-text-merge-')
  const paths = ['ours', 'base', 'theirs'].map(name => resolve(directory, name))
  function textMerge(oursName, theirsName, canonical) {
    for (const [i, data] of [manifest([...base.tests, row(oursName)]), base, manifest([...base.tests, row(theirsName)])].entries()) {
      // Match old prepending layout instead of merely changing whitespace.
      if (!canonical && i !== 1) data.tests = [data.tests.at(-1), ...base.tests]
      const source = canonical ? formatManifest(data, goFile) : JSON.stringify(data, null, 2) + '\n'
      writeFileSync(paths[i], source)
    }
    const result = spawnSync('git', ['merge-file', '-p', ...paths], { encoding: 'utf8', timeout: 10_000 })
    assert.ifError(result.error)
    return result
  }
  assert.equal(textMerge('TestB', 'TestH', false).status, 1, 'old top-prepends collide')
  const separated = textMerge('TestB', 'TestH', true)
  assert.equal(separated.status, 0, separated.stderr)
  assert.equal(JSON.parse(separated.stdout).tests.length, base.tests.length + 2)
  assert.equal(textMerge('TestB1', 'TestB2', true).status, 1, 'two insertions into the same textual gap collide')
  assert.equal(driver(t, base, manifest([...base.tests, row('TestB1')]), manifest([...base.tests, row('TestB2')])).status, 0)
})

test('OPS-257 --write deduplicates exact rows but rejects conflicting duplicate identities before any write', t => {
  assert.deepEqual(roundtrip(manifest([row('TestA'), row('TestA')])).tests, [row('TestA')])
  assert.throws(() => formatManifest(manifest([row('TestA'), row('TestA', 'NIGHTLY')]), goFile), /Duplicate key/)
  const directory = fixtures(t), paths = manifestPaths.map(file => resolve(directory, file))
  writeFileSync(paths[2], '{malformed')
  const before = paths.map(path => readFileSync(path, 'utf8'))
  assert.throws(() => writeManifests(directory), SyntaxError)
  assert.deepEqual(paths.map(path => readFileSync(path, 'utf8')), before)
})

test('OPS-257 classify selects only new matching identities, strips discovery data and preserves existing tiers', t => {
  const source = manifest([row('TestKnown', 'ESSENTIAL')])
  const discovered = [row('TestKnown', 'NIGHTLY'), { ...row('TestNew'), file: 'source_test.go', line: 9, active: true }, row('TestElse', 'NIGHTLY', 'internal/nodes')]
  const before = structuredClone(source)
  const result = classifyManifest(source, discovered, { tier: 'NIGHTLY', only: 'internal/auth:' })
  assert.deepEqual(result.added, [row('TestNew', 'NIGHTLY')])
  assert.deepEqual(result.manifest.tests, [row('TestKnown', 'ESSENTIAL'), row('TestNew', 'NIGHTLY')])
  assert.deepEqual(source, before)
  const browser = { kind: 'browser', file: 'tests/a.spec.ts', name: 'case', config: 'playwright.ui.config.ts', project: '', occurrence: 2, id: 'native', line: 11, leaf: 'case', active: true }
  assert.deepEqual(classifyManifest(manifest([]), [browser], { tier: 'GATED-FULL' }).added, [{ kind: 'browser', file: browser.file, name: 'case', config: browser.config, project: '', occurrence: 2, tier: 'GATED-FULL' }])
  assert.throws(() => classifyManifest(source, discovered, { tier: 'UNKNOWN' }), /requires --tier/)
  assert.throws(() => classifyManifest(source, [discovered[0], discovered[0]], { tier: 'NIGHTLY' }), /Duplicate key/)
  assert.throws(() => classifyManifest(source, discovered, { tier: 'NIGHTLY', only: '' }), /--only pattern/)
})

test('OPS-257 classify CLI requires an explicit tier in new syntax while retaining legacy NIGHTLY', t => {
  assert.deepEqual(classifyArgs(['--tier', 'ESSENTIAL', '--kind', 'web', '--only', 'case']), { tier: 'ESSENTIAL', kind: 'web', only: 'case', strict: false })
  assert.deepEqual(classifyArgs(['go']), { kind: 'go', tier: 'NIGHTLY', strict: true })
  for (const flags of [[], ['--tier'], ['--tier', 'OTHER'], ['--tier', 'NIGHTLY', '--kind', 'unknown'], ['--tier', 'NIGHTLY', '--tier', 'ESSENTIAL'], ['--wat'], ['--only', '--tier']]) assert.throws(() => classifyArgs(flags))
  const result = spawnSync(process.execPath, [resolve(root, 'scripts/test-tiers/cli.mjs'), 'classify', '--tier', 'OTHER'], { encoding: 'utf8' })
  assert.equal(result.status, 1); assert.match(result.stderr, /requires --tier/)
})

test('AEON-588 classification records only newly added NIGHTLY identities as post-gate cases', () => {
  const source = {
    ...manifest([row('TestCore', 'ESSENTIAL'), row('TestLegacy', 'NIGHTLY'), row('TestRecorded', 'NIGHTLY')]),
    postGateCases: ['internal/auth:TestRecorded'],
  }
  const before = structuredClone(source)
  const discovered = [...source.tests, row('TestNew'), row('TestElse', 'NIGHTLY', 'internal/nodes')]
  const result = classifyManifest(source, discovered, { tier: 'NIGHTLY', only: 'internal/auth:' })
  assert.deepEqual(result.manifest.postGateCases, ['internal/auth:TestRecorded', 'internal/auth:TestNew'])
  assert.deepEqual(result.manifest.tests, [...source.tests, row('TestNew', 'NIGHTLY')])
  assert.deepEqual(source, before, 'classification must not mutate its input')
  const again = classifyManifest(result.manifest, discovered, { tier: 'NIGHTLY', only: 'internal/auth:' })
  assert.deepEqual(again.added, [])
  assert.deepEqual(again.manifest, result.manifest, 'repeated classification must not duplicate post-gate entries')
  for (const tier of ['ESSENTIAL', 'GATED-FULL']) {
    const gated = classifyManifest(source, discovered, { tier, only: 'internal/auth:' })
    assert.deepEqual(gated.manifest.postGateCases, source.postGateCases)
    assert.deepEqual(gated.added, [row('TestNew', tier)])
  }
})

test('AEON-588 classification initializes post-gate identities for Go and native web occurrences', () => {
  const cases = [
    [row('TestNew'), 'internal/auth:TestNew'],
    [{ kind: 'node', file: 'tests/a.test.ts', name: 'case' }, 'node:tests/a.test.ts:case'],
    [{ kind: 'vitest', file: 'tests/a.unit.test.ts', name: 'case', occurrence: 2 }, 'vitest:tests/a.unit.test.ts:case#2'],
    [{ kind: 'browser', file: 'tests/a.spec.ts', name: 'case', occurrence: 2, config: 'playwright.ui.config.ts', project: '' }, 'browser:tests/a.spec.ts:case#2'],
  ]
  for (const [entry, id] of cases) {
    const source = manifest([])
    const result = classifyManifest(source, [entry], { tier: 'NIGHTLY' })
    assert.deepEqual(result.manifest.postGateCases, [id])
    assert.deepEqual(source, manifest([]))
    assert.deepEqual(classifyManifest(source, [entry], { tier: 'NIGHTLY', only: 'no match' }).manifest, source)
    for (const tier of ['ESSENTIAL', 'GATED-FULL']) {
      assert.equal(Object.hasOwn(classifyManifest(source, [entry], { tier }).manifest, 'postGateCases'), false)
    }
  }
})

test('AEON-588 legacy classify CLI saves NIGHTLY rows and post-gate identities together', () => {
  const source = manifest([row('TestKnown')]), saves = []
  assert.equal(classify(['go'], {
    collect: () => ({ tests: [row('TestKnown'), row('TestNew')] }),
    read: () => source,
    save: (path, data) => saves.push({ path, data: roundtrip(data) }),
  }), 0)
  assert.equal(saves.length, 1)
  assert.deepEqual(saves[0].data.tests, [row('TestKnown'), row('TestNew', 'NIGHTLY')])
  assert.deepEqual(saves[0].data.postGateCases, ['internal/auth:TestNew'])
})

test('OPS-257 classify CLI saves canonical rows through discovery and validates both kinds before writing', t => {
  const initial = manifest([row('TestKnown')]), saves = []
  const deps = { collect: () => ({ tests: [row('TestKnown'), row('TestNew')] }), read: () => structuredClone(initial), save: (path, data) => saves.push({ path, data: roundtrip(data) }) }
  assert.equal(classify(['--tier', 'GATED-FULL', '--kind', 'go'], deps), 0)
  assert.deepEqual(saves[0].data.tests, [row('TestKnown'), row('TestNew')])
  saves.length = 0
  assert.throws(() => classify(['--tier', 'NIGHTLY'], { ...deps, collect: kind => { if (kind === 'web') throw new Error('collection failed'); return deps.collect() } }), /collection failed/)
  assert.deepEqual(saves, [])
  const known = row('TestSourceRequestCapAndDelay', 'ESSENTIAL', 'internal/importer')
  assert.throws(() => classify(['--tier', 'ESSENTIAL', '--kind', 'go'], { ...deps, collect: () => ({ tests: [row('TestKnown'), known] }) }), /Known-flaky case cannot be ESSENTIAL/)
  assert.deepEqual(saves, [])
})

test('OPS-257 driver keeps both top-prepended additions in the same package and accepts old/new layouts', t => {
  const base = manifest([row('TestZ')]), ours = manifest([row('TestA'), ...base.tests]), theirs = manifest([row('TestB'), ...base.tests])
  for (const input of [[base, ours, theirs], [formatManifest(base, goFile), formatManifest(ours, goFile), theirs], [base, ours, formatManifest(theirs, goFile)]]) {
    const result = driver(t, ...input)
    assert.equal(result.status, 0, result.stderr)
    assert.deepEqual(JSON.parse(result.output).tests, [row('TestA'), row('TestB'), row('TestZ')])
    assert.equal(result.output, formatManifest(JSON.parse(result.output), goFile))
  }
})

test('OPS-257 driver merges additions, unchanged removals and unilateral tier changes deterministically', t => {
  const base = manifest([row('TestA'), row('TestB')])
  const ours = manifest([row('TestA', 'NIGHTLY'), row('TestB'), row('TestO')])
  const theirs = manifest([row('TestA'), row('TestT')])
  const expected = roundtrip(manifest([row('TestA', 'NIGHTLY'), row('TestO'), row('TestT')]))
  assert.deepEqual(mergeManifests(base, ours, theirs, goFile), expected)
  assert.deepEqual(mergeManifests(base, theirs, ours, goFile), expected)
  assert.deepEqual(mergeManifests(base, manifest([]), base, goFile).tests, [])
  assert.deepEqual(mergeManifests(base, manifest([]), manifest([]), goFile).tests, [])
  assert.deepEqual(mergeManifests(manifest([]), manifest([row('TestA')]), manifest([row('TestA')]), goFile).tests, [row('TestA')])
})

test('OPS-257 driver conflicts on remove/change, divergent tiers or additions and writes unresolved conflict markers', t => {
  for (const [base, ours, theirs] of [
    [manifest([row('TestA')]), manifest([]), manifest([row('TestA', 'ESSENTIAL')])],
    [manifest([row('TestA')]), manifest([row('TestA', 'NIGHTLY')]), manifest([row('TestA', 'ESSENTIAL')])],
    [manifest([]), manifest([row('TestA', 'NIGHTLY')]), manifest([row('TestA', 'ESSENTIAL')])],
  ]) for (const sides of [[base, ours, theirs], [base, theirs, ours]]) {
    const result = driver(t, ...sides)
    assert.equal(result.status, 1); assert.match(result.stderr, /merge conflict:.*tests.*TestA/)
    assertUnresolved(result)
  }
})

test('OPS-257 driver merges postGateCases, candidate lists and timing owners by owner field', t => {
  const base = { ...manifest([row('TestA')]), postGateCases: ['x', 'z'], timingWeights: { owners: { a: { seconds: 1, selectedTests: 1 } } } }
  const ours = { ...structuredClone(base), postGateCases: ['o', 'z'], deleteCandidateGroups: [{ file: 'tests/a.spec.ts', tag: 'delete-candidate' }] }
  const theirs = { ...structuredClone(base), postGateCases: ['t', 'x', 'z'], deleteCandidateGroups: [{ file: 'tests/b.spec.ts', tag: 'delete-candidate' }] }
  theirs.timingWeights.owners.b = { seconds: 2, selectedTests: 2 }
  const result = mergeManifests(base, ours, theirs, goFile)
  assert.deepEqual(result.postGateCases, ['o', 't', 'z'])
  assert.deepEqual(result.deleteCandidateGroups.map(row => row.file), ['tests/a.spec.ts', 'tests/b.spec.ts'])
  assert.deepEqual(result.timingWeights.owners, theirs.timingWeights.owners)
  ours.timingWeights.owners.a.seconds = 2
  theirs.timingWeights.owners.a.selectedTests = 3
  assert.deepEqual(mergeManifests(base, ours, theirs, goFile).timingWeights.owners.a, { seconds: 2, selectedTests: 3 })
})

test('OPS-257 shard driver merges both spec additions in one group and retains group policy/order', t => {
  const base = shards([group('z-first', [spec('tests/z.spec.ts')]), group('a-second', [spec('tests/other.spec.ts')])])
  const ours = structuredClone(base), theirs = structuredClone(base)
  ours.groups[0].specs.unshift(spec('tests/a.spec.ts', 7))
  theirs.groups[0].specs.unshift(spec('tests/b.spec.ts', 8))
  ours.groups[0].gate = false
  const result = driver(t, base, ours, formatManifest(theirs, shardsFile), shardsFile)
  assert.equal(result.status, 0, result.stderr)
  const data = JSON.parse(result.output)
  assert.deepEqual(data.groups.map(g => g.id), ['z-first', 'a-second'])
  assert.deepEqual(data.groups[0], { ...ours.groups[0], specs: [spec('tests/a.spec.ts', 7), spec('tests/b.spec.ts', 8), spec('tests/z.spec.ts')] })
})

test('OPS-257 shard driver rejects divergent weights and removal/change; duplicates cannot escape across groups', t => {
  const base = shards([group('first', [spec('tests/a.spec.ts')])])
  const ours = shards([group('first', [spec('tests/a.spec.ts', 11)])])
  const theirs = shards([group('first', [spec('tests/a.spec.ts', 12)])])
  for (const o of [ours, shards([group('first', [])])]) {
    const result = driver(t, base, o, theirs, shardsFile)
    assert.equal(result.status, 1); assert.match(result.stderr, /merge conflict:.*specs.*a.spec.ts/)
    assertUnresolved(result)
  }
  assert.throws(() => normalizeManifest(shards([group('first', [spec('tests/a.spec.ts')]), group('second', [spec('tests/a.spec.ts')])]), shardsFile), /Duplicate spec across groups/)
  const empty = shards([]), one = shards([group('first', [spec('tests/a.spec.ts')])]), two = shards([group('second', [spec('tests/a.spec.ts')])])
  const result = driver(t, empty, one, two, shardsFile)
  assert.equal(result.status, 1); assert.match(result.stderr, /merge conflict:[\s\S]*Duplicate spec across groups/)
  assertUnresolved(result)
})

test('OPS-257 driver rejects duplicate keys, malformed JSON and unsupported paths on every input without writing', t => {
  const valid = manifest([row('TestA')])
  for (const invalid of [manifest([row('TestA'), row('TestA')]), manifest([row('TestA'), row('TestA', 'NIGHTLY')]), '{bad JSON', manifest([{ kind: 'go', name: 'TestA' }])]) {
    for (let i = 0; i < 3; i++) {
      const inputs = [valid, valid, valid]; inputs[i] = invalid
      const result = driver(t, ...inputs)
      assert.equal(result.status, 2); assert.ok(result.stderr.trim())
      assert.equal(result.output, result.before)
    }
  }
  const result = driver(t, valid, valid, valid, 'api/openapi.yaml')
  assert.equal(result.status, 2); assert.match(result.stderr, /Unsupported manifest/)
  assert.equal(result.output, result.before)
})

test('OPS-257 concurrent new group/note order is deterministic and conflicting metadata stays unresolved', t => {
  const base = { ...shards([group('first', [spec('tests/a.spec.ts')])]), integrationNotes: ['historical'] }
  const ours = structuredClone(base), theirs = structuredClone(base)
  ours.groups.push(group('z', [spec('tests/z.spec.ts')]))
  theirs.groups.push(group('b', [spec('tests/b.spec.ts')]))
  ours.integrationNotes.push('z'); theirs.integrationNotes.push('b')
  const result = mergeManifests(base, ours, theirs, shardsFile)
  assert.deepEqual(result.groups.map(g => g.id), ['first', 'b', 'z'])
  assert.deepEqual(result.integrationNotes, ['historical', 'b', 'z'])
  assert.deepEqual(result, mergeManifests(base, theirs, ours, shardsFile))
  ours.groups[0].flags = ['--workers=2']; theirs.groups[0].flags = ['--workers=4']
  assert.throws(() => mergeManifests(base, ours, theirs, shardsFile), /merge conflict:.*flags/)
})


test('OPS-257 non-Go row order uses file identity even with extra package metadata', () => {
  const rows = ['tests/z.test.ts', 'tests/a.test.ts'].map(file => ({ kind: 'node', file, package: 'same-extra-package', name: 'same title', tier: 'NIGHTLY' }))
  const first = formatManifest(manifest(rows), webFile)
  assert.equal(first, formatManifest(manifest([...rows].reverse()), webFile))
  assert.deepEqual(JSON.parse(first).tests.map(row => row.file), ['tests/a.test.ts', 'tests/z.test.ts'])
  assert.throws(() => formatManifest(manifest([{ ...row('TestA'), occurrence: 1 }]), goFile), /Invalid test row identity/)
})

test('OPS-257 shard order applies unilateral or matching reorder and conflicts on divergent reorder', t => {
  const base = shards(['a', 'b', 'c'].map(id => group(id, [spec(`tests/${id}.spec.ts`)])))
  const ours = structuredClone(base), theirs = structuredClone(base)
  ours.groups.reverse()
  theirs.groups[0].specs.push(spec('tests/new.spec.ts', 17))
  const expected = structuredClone(theirs); expected.groups.reverse()
  for (const sides of [[base, ours, theirs], [base, theirs, ours]]) {
    const result = driver(t, ...sides, shardsFile)
    assert.equal(result.status, 0, result.stderr)
    assert.equal(result.output, formatManifest(expected, shardsFile))
  }
  theirs.groups.reverse()
  assert.deepEqual(mergeManifests(base, ours, theirs, shardsFile).groups.map(g => g.id), ['c', 'b', 'a'])
  theirs.groups = [theirs.groups[1], theirs.groups[2], theirs.groups[0]]
  const result = driver(t, base, ours, theirs, shardsFile)
  assert.equal(result.status, 1)
  assert.match(result.stderr, /groups\.<order>/)
  assertUnresolved(result)
})

test('OPS-257 full-size old prepended manifests merge into the canonical union idempotently', t => {
  // Synthetic offline pre-L13 layout at production scale, with both sides
  // prepending rows to the same package and preserving tier/timing metadata.
  const rows = Array.from({ length: 3_800 }, (_, i) => row(`TestExisting${String(i).padStart(4, '0')}`, i % 2 ? 'NIGHTLY' : 'GATED-FULL'))
  const base = { ...manifest([...rows].reverse()), timingWeights: { owners: { 'internal/auth': { seconds: 128, selectedTests: 3_800 } } } }
  const ours = { ...base, tests: [row('TestAddedOurs', 'ESSENTIAL'), ...base.tests] }
  const theirs = { ...base, tests: [row('TestAddedTheirs', 'NIGHTLY'), ...base.tests] }
  const expected = formatManifest({ ...base, tests: [ours.tests[0], theirs.tests[0], ...rows] }, goFile)
  for (const inputs of [[base, ours, formatManifest(theirs, goFile)], [base, formatManifest(theirs, goFile), ours]]) {
    const result = driver(t, ...inputs)
    assert.equal(result.status, 0, result.stderr)
    assert.equal(result.output, expected)
    assert.equal(JSON.parse(result.output).tests.length, 3_802)
    const repeated = driver(t, result.output, result.output, result.output)
    assert.equal(repeated.status, 0, repeated.stderr)
    assert.equal(repeated.output, expected)
  }
})

test('OPS-257 conversion proof preserves each spec weight rather than only the weight multiset', () => {
  const before = shards([group('first', [spec('tests/a.spec.ts', 7), spec('tests/b.spec.ts', 13)])])
  const after = structuredClone(before); after.groups[0].specs.reverse()
  assert.equal(proveConversion(before, after, shardsFile).perFileWeightsUnchanged, true)
  after.groups[0].specs[0].weightSeconds = 7
  after.groups[0].specs[1].weightSeconds = 13
  assert.throws(() => proveConversion(before, after, shardsFile), /per-file spec weights changed/)
})


test('OPS-257 conflict markers fail the canonical checker before staging', t => {
  const base = manifest([row('TestA')])
  const result = driver(t, base, manifest([row('TestA', 'ESSENTIAL')]), manifest([row('TestA', 'NIGHTLY')]))
  assert.equal(result.status, 1)
  const directory = fixtures(t)
  writeManifests(directory)
  writeFileSync(resolve(directory, goFile), result.output)
  assert.throws(() => checkManifests(directory), error => error.message.includes(goFile) && error.message.includes('JSON'))
  assert.throws(() => writeManifests(directory), SyntaxError)
  assert.equal(readFileSync(resolve(directory, goFile), 'utf8'), result.output)
})
