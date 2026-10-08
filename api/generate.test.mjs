// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, symlinkSync, truncateSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { bundleOpenAPI, generateOpenAPI } from './generate.mjs'
import { splitOpenAPI } from './openapi-source.mjs'

const base = 'openapi: 3.1.0\ninfo: {title: Fixture, version: dev}\nservers: [{url: /api}]\npaths:\ncomponents:\n  schemas:\n  responses:\n'
const a = '# SPDX-License-Identifier: AGPL-3.0-only\npaths:\n  /a:\n    get:\n      security: &auth [{session: []}]\n      description: |+\n        components:\n          schemas:\n        Keep the trailing blank line.\n\ncomponents:\n  schemas:\n    A: {type: string}\n'
const z = 'paths:\n  # Travels with /z\n  /z:\n    servers: [{url: /}]\n    get:\n      security: *auth\n      description: "Keep: exactly; unchanged"\ncomponents:\n  responses:\n    Z: {description: "Z: response"}\n'
function fixture(t) {
  const dir = mkdtempSync(join(tmpdir(), 'aeon-openapi-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  mkdirSync(join(dir, 'areas'))
  writeFileSync(join(dir, 'openapi.base.yaml'), base)
  // Directory/file order must not affect source ownership or output order.
  writeFileSync(join(dir, 'areas/z.yaml'), z)
  writeFileSync(join(dir, 'areas/a.yaml'), a)
  return dir
}

test('area assembly preserves scalar bytes, comments, server overrides and cross-area anchor bindings', t => {
  const dir = fixture(t)
  const expected = base.split('paths:')[0] + 'paths:\n' + a.split('paths:\n')[1].split('components:\n  schemas:')[0] +
    z.split('paths:\n')[1].split('components:\n')[0] + 'components:\n  schemas:\n    A: {type: string}\n  responses:\n    Z: {description: "Z: response"}\n'
  assert.equal(bundleOpenAPI(dir), expected)
  const result = generateOpenAPI(dir)
  assert.equal(readFileSync(join(dir, 'openapi.yaml'), 'utf8'), expected)
  writeFileSync(join(dir, 'openapi.yaml'), 'stale output')
  assert.equal(generateOpenAPI(dir), result)
  assert.equal(readFileSync(join(dir, 'openapi.yaml'), 'utf8'), expected)
  assert.deepEqual([...splitOpenAPI(result).paths.keys()], ['/a', '/z'])
})

test('area assembly fails closed on duplicate ownership, metadata overrides and unsupported source shapes', t => {
  const dir = fixture(t), file = join(dir, 'areas/z.yaml')
  for (const [source, reason] of [
    [z.replace('/z:', '/a:'), /Duplicate paths:\/a/],
    [z.replace('  responses:\n    Z:', '  schemas:\n    A:'), /Duplicate components.schemas:A/],
    ['servers: [{url: /}]\n' + z, /Fragment metadata belongs/],
    [z + 'security: []\n', /Unsupported top-level field/],
    [z.replace('  responses:', '  unknown:'), /Unknown component section/],
    [z.replace('*auth', '*missing'), /precedes its definition/],
    [z.trimEnd(), /final newline/],
  ]) {
    writeFileSync(file, source)
    assert.throws(() => bundleOpenAPI(dir), reason)
  }
})

test('area assembly bounds inputs and rejects symlinks before reading or writing', t => {
  const dir = fixture(t), file = join(dir, 'areas/z.yaml')
  truncateSync(file, 17 * 1024 * 1024)
  assert.throws(() => bundleOpenAPI(dir), /16 MiB/)
  writeFileSync(file, z)
  symlinkSync(file, join(dir, 'areas/alias.yaml'))
  assert.throws(() => bundleOpenAPI(dir), /regular file/)
  // A separate checkout proves an output link cannot redirect generation.
  const second = fixture(t), target = join(second, 'untouched')
  writeFileSync(target, 'sentinel')
  symlinkSync(target, join(second, 'openapi.yaml'))
  assert.throws(() => generateOpenAPI(second), /regular file/)
  assert.equal(readFileSync(target, 'utf8'), 'sentinel')
})
