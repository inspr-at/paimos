// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { test } from 'node:test'
import { STREAM_CHECK_MS, STREAM_SILENCE_MS } from '../src/lib/streamHealth.ts'

const doc = readFileSync(new URL('../doc.go', import.meta.url), 'utf8')

test('package docs reference existing frontend modules', () => {
  const references = [...doc.matchAll(/src\/lib\/\w+\.ts/g)].map(match => match[0])
  assert.ok(references.length > 0, 'the module references must be present')
  for (const path of references) {
    assert.ok(existsSync(new URL(`../${path}`, import.meta.url)), `${path} does not exist`)
  }
  assert.match(doc, /src\/lib\/agents\.ts owns the agent SSE connection/)
})

test('package docs describe the implemented conditional write', () => {
  assert.match(doc, /If-Unmodified-Since/)
  assert.match(doc, /HTTP 412/)
  assert.doesNotMatch(doc, /no conditional-write token/)
  const client = readFileSync(new URL('../src/lib/api.ts', import.meta.url), 'utf8')
  assert.match(client, /'If-Unmodified-Since': options\.ifUnmodifiedSince/)
})

test('documented stream intervals match the shared health watcher', () => {
  const check = doc.match(/at a (\d+)-second interval/)
  const silence = doc.match(/after (\d+)-second silence/)
  assert.ok(check, 'the check interval must be documented')
  assert.ok(silence, 'the silence threshold must be documented')
  assert.equal(Number(check[1]) * 1000, STREAM_CHECK_MS)
  assert.equal(Number(silence[1]) * 1000, STREAM_SILENCE_MS)
})
