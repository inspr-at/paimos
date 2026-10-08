// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync, realpathSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { listTracked, trackedBytecode } from './check-tracked-bytecode.mjs'

const root = fileURLToPath(new URL('..', import.meta.url))

function repository() {
  const dir = realpathSync(mkdtempSync(join(tmpdir(), 'aeon-bytecode-')))
  const env = { ...process.env, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null' }
  const git = args => execFileSync('git', args, { cwd: dir, env, encoding: 'utf8' }).trim()
  git(['init', '-q'])
  git(['config', 'user.name', 'Fixture'])
  git(['config', 'user.email', 'fixture@example.invalid'])
  writeFileSync(join(dir, 'keep.txt'), 'keep\n')
  git(['add', 'keep.txt'])
  git(['commit', '-qm', 'fixture'])
  return { dir, git }
}

test('bytecode paths are __pycache__ segments and .pyc files only', () => {
  assert.deepEqual(trackedBytecode([
    'scripts/audit/__pycache__/test_audit.cpython-314.pyc',
    'loose.pyc',
    'pkg/__pycache__/module.py',
    'notes/pycache/readme.md',
    'file.pyc.txt',
    'file.py',
    'keep.txt',
  ]), [
    'scripts/audit/__pycache__/test_audit.cpython-314.pyc',
    'loose.pyc',
    'pkg/__pycache__/module.py',
  ])
})

test('a committed bytecode path fails and an untracked cache does not', t => {
  const repo = repository()
  t.after(() => rmSync(repo.dir, { recursive: true, force: true }))
  assert.deepEqual(trackedBytecode(listTracked(repo.dir)), [])
  mkdirSync(join(repo.dir, 'pkg/__pycache__'), { recursive: true })
  writeFileSync(join(repo.dir, 'pkg/__pycache__/mod.cpython-314.pyc'), 'cache')
  writeFileSync(join(repo.dir, 'loose.pyc'), 'cache')
  assert.deepEqual(trackedBytecode(listTracked(repo.dir)), [])
  repo.git(['add', '-A'])
  repo.git(['commit', '-qm', 'track bytecode'])
  assert.deepEqual(trackedBytecode(listTracked(repo.dir)), ['loose.pyc', 'pkg/__pycache__/mod.cpython-314.pyc'])
})

test('gitignore names the bytecode cache and this checkout tracks none', () => {
  const lines = new Set(readFileSync(new URL('../.gitignore', import.meta.url), 'utf8').split('\n').map(line => line.trim()))
  assert.ok(lines.has('__pycache__/'))
  assert.ok(lines.has('*.pyc'))
  assert.deepEqual(trackedBytecode(listTracked(root)), [])
})
