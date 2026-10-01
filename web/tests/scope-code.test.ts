// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { encodeScopeCode, decodeScopeCode, scopeCodeSelection } from '../src/lib/scopeCode.ts'

const vectors = JSON.parse(readFileSync(new URL('../../internal/scopecode/testdata/v1.json', import.meta.url), 'utf8')) as { name: string; input: string[]; scopes: string[]; code: string }[]
for (const v of vectors) test(`v1 wire vector: ${v.name}`, () => {
  assert.equal(encodeScopeCode(v.input), v.code)
  assert.deepEqual(decodeScopeCode(` \n${v.code}\t`), v.scopes)
})

test('bad checksum, truncation, unsupported version and invalid scopes leave no partial result', () => {
  const valid = vectors.find(v => v.name === 'history')!.code
  for (const code of ['', valid.slice(0, -1), valid.replace('events', 'nodes'), valid + 'x', valid.toUpperCase(), valid.replace(':v1:', ':v2:'), 'a'.repeat(32769)]) assert.throws(() => decodeScopeCode(code))
  for (const keys of [[''], ['nodes.*'], ['nodes.read.extra'], ['node s.read'], Array(257).fill('nodes.read')]) assert.throws(() => encodeScopeCode(keys))
})

test('selection uses all three existing ceilings, replaces rather than adds, and grants nothing', () => {
  const reasons: Record<string, string> = { 'nodes.write': 'Beyond the agent role', 'events.read': 'Not in your permissions', 'keys.manage': 'Unavailable to agent keys', 'unknown.permission': 'Unknown in this workspace' }
  const proposed = ['nodes.read', ...Object.keys(reasons)]
  const result = scopeCodeSelection(encodeScopeCode(proposed), key => reasons[key])
  assert.deepEqual([...result.selected], ['nodes.read'])
  assert.deepEqual(Object.fromEntries(result.skipped.map(s => [s.key, s.reason])), reasons)
  assert.deepEqual([...scopeCodeSelection(encodeScopeCode([]), () => undefined).selected], [])
})
