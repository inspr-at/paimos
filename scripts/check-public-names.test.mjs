// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFileSync, spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { checkPublicNames } from './check-public-names.mjs'

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
  const published = '[{"note":"live on csb1"}]\n'
  write(path, published)
  assert(checkPublicNames(root).some(failure => failure.startsWith(path)))
  const entry = { path, sha256: createHash('sha256').update(published).digest('hex'), reason: 'Keep published release-note text immutable' }
  allow([entry])
  assert.deepEqual(checkPublicNames(root), [])
  assert.equal(cli().status, 0)
  write(path, published + 'new csb2 note\n')
  assert.throws(() => checkPublicNames(root), /Published history changed/)
  allow([{ ...entry, path: 'web/src/example.ts' }])
  assert.throws(() => checkPublicNames(root), /Only digest-pinned published/)
})
