// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFileSync, spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { checkPublicNames } from './check-public-names.mjs'

const historyPath = 'internal/releasehistory/data/product-notes.json'
const allowlistPath = 'scripts/ci/public-names-allowlist.json'

function publishedFixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'aeon-published-names-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  execFileSync('git', ['init', '-q', root])
  for (const path of [historyPath, allowlistPath]) {
    mkdirSync(dirname(join(root, path)), { recursive: true })
    writeFileSync(join(root, path), readFileSync(new URL(`../${path}`, import.meta.url)))
  }
  const history = JSON.parse(readFileSync(join(root, historyPath), 'utf8'))
  const save = () => writeFileSync(join(root, historyPath), JSON.stringify(history, null, 2) + '\n')
  return { root, history, save }
}

test('public name guard permits neutral new releases without refreshing published pins', t => {
  // Risk: reserving the next release breaks CI despite unchanged published history.
  const { root, history, save } = publishedFixture(t)
  assert.deepEqual(checkPublicNames(root), [])
  history.releases['261010120000.0.0'] = { items: [{ benefit_en: 'Live on prod-1' }] }
  save()
  assert.deepEqual(checkPublicNames(root), [])
  // packnotes may reserialize objects; only content changes invalidate a pin.
  for (const [version, entry] of Object.entries(history.releases)) {
    history.releases[version] = Object.fromEntries(Object.entries(entry).reverse())
  }
  save()
  assert.deepEqual(checkPublicNames(root), [])
})

test('public name guard scans new release notes while retaining published exceptions', t => {
  // Risk: an exception for published notes silently exempts the next release's copy.
  const { root, history, save } = publishedFixture(t)
  assert.deepEqual(checkPublicNames(root), [])
  const version = '261010120000.0.0'
  history.releases[version] = { items: [{ benefit_en: 'Live on csb2' }] }
  save()
  assert.deepEqual(checkPublicNames(root), [`${historyPath}:releases.${version}: operator host or business name`])
})

test('public name guard rejects planted code and limits exceptions to frozen published history', t => {
  // Risk: a source/fixture/docs leak passes CI, or a broad exception admits new leaks.
  const root = mkdtempSync(join(tmpdir(), 'aeon-public-names-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  execFileSync('git', ['init', '-q', root])
  const write = (path, content) => {
    mkdirSync(dirname(join(root, path)), { recursive: true })
    writeFileSync(join(root, path), content)
  }
  const allow = entries => write('scripts/ci/public-names-allowlist.json', JSON.stringify(entries))
  const cli = () => spawnSync(process.execPath, [fileURLToPath(new URL('./check-public-names.mjs', import.meta.url))], { cwd: root, encoding: 'utf8' })
  allow([])
  write('web/src/example.ts', 'export const target = "prod-1"\n')
  assert.deepEqual(checkPublicNames(root), [])
  assert.equal(cli().status, 0)
  write('web/src/example.ts', 'export const target = "csb1"\n')
  assert.deepEqual(checkPublicNames(root), ['web/src/example.ts:1: operator host or business name'])
  assert.equal(cli().status, 1)
  write('web/src/example.ts', 'export const target = "prod-1"\n')
  for (const [index, scope] of ['internal', 'cmd', 'api', 'docs', 'tests', 'web/tests'].entries()) {
    const path = `${scope}/probe.txt`
    write(path, ['AGM1', 'csb9', 'HSB8', 'mbp2607', 'Augmentoring', 'mbp2606'][index])
    assert(checkPublicNames(root).some(failure => failure.startsWith(path + ':1:')))
    write(path, 'neutral fixture\n')
  }
  const path = 'internal/releasehistory/data/product-notes.json'
  const version = '261001072608.0.0'
  const release = { note: 'live on csb1' }
  const notes = { schema: 'aeon.product-release-notes.v1', releases: { [version]: release } }
  const published = JSON.stringify(notes) + '\n'
  write(path, published)
  assert(checkPublicNames(root).some(failure => failure.startsWith(path)))
  const entry = { path, version, sha256: createHash('sha256').update(JSON.stringify(release)).digest('hex'), reason: 'Keep published release-note text immutable' }
  allow([entry])
  assert.deepEqual(checkPublicNames(root), [])
  assert.equal(cli().status, 0)
  write(path, JSON.stringify({ ...notes, releases: { [version]: { note: 'new csb2 note' } } }))
  assert.throws(() => checkPublicNames(root), /Published release entry changed or missing/)
  // Neither bundle metadata nor a copy of a pinned entry under another version is exempt.
  write(path, JSON.stringify({ ...notes, product: 'csb2' }))
  assert.deepEqual(checkPublicNames(root), [`${path}:product: operator host or business name`])
  write(path, JSON.stringify({ ...notes, releases: { ...notes.releases, '261010120000.0.0': release } }))
  assert.deepEqual(checkPublicNames(root), [`${path}:releases.261010120000.0.0: operator host or business name`])
  write(path, JSON.stringify({ ...notes, releases: {} }))
  assert.throws(() => checkPublicNames(root), /Published release entry changed or missing/)
  write(path, published)
  allow([entry, entry])
  assert.throws(() => checkPublicNames(root), /Only version-and-digest-pinned published/)
  allow([{ ...entry, path: 'web/src/example.ts' }])
  assert.throws(() => checkPublicNames(root), /Only version-and-digest-pinned published/)
})
