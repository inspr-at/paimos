// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { codenameOf, releaseAria, rememberCodename, rememberCodenames } from '../src/lib/codenames.ts'

// AEON-430: marketing names front and centre; the lookup names any version the page has heard of.
test('a remembered codename names its version, with or without the v prefix', () => {
  rememberCodename('260930074921.0.0', 'Fresh Flyby')
  assert.equal(codenameOf('260930074921.0.0'), 'Fresh Flyby')
  assert.equal(codenameOf('v260930074921.0.0'), 'Fresh Flyby')
})

test('an unknown, empty or unnamed version has no name, and an empty name never overwrites', () => {
  assert.equal(codenameOf('260101000000.0.0'), '')
  assert.equal(codenameOf(''), '')
  assert.equal(codenameOf(null), '')
  rememberCodename('260930074921.0.0', '')
  rememberCodename('260930074921.0.0', undefined)
  assert.equal(codenameOf('260930074921.0.0'), 'Fresh Flyby')
})

test('the release history fills the lookup, skipping releases without a name', () => {
  rememberCodenames([
    { version: '260930094206.0.0', codename: 'Glossy Glint' },
    { version: '260923134337.0.0' },
  ])
  assert.equal(codenameOf('260930094206.0.0'), 'Glossy Glint')
  assert.equal(codenameOf('260923134337.0.0'), '')
})

test('the accessible line is the name, then the version, and never a release number', () => {
  assert.equal(releaseAria('Hinged Hangar', 'v260930115354.0.0'), 'Hinged Hangar, version 260930115354.0.0')
  assert.equal(releaseAria('', '260930115354.0.0'), 'version 260930115354.0.0')
  assert.ok(!/Release \d/.test(releaseAria('Hinged Hangar', '260930115354.0.0')))
})
