// SPDX-License-Identifier: AGPL-3.0-only
// INSPR-CalVer3 (AEON-309): new reservations declare inspr-calver-3; every version
// already reserved under inspr-calendar-v2 stays valid history and keeps parsing.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { CALVER2, LAST_CALVER2, SCHEME, schemeError, validCalendarVersion } from '../../scripts/verify-release.mjs'
import { parts } from '../src/vendor/calendar-version-display/version.js'

const repo = new URL('../../', import.meta.url)
const schemes = JSON.parse(readFileSync(new URL('web/src/vendor/calendar-version-display/schemes.json', repo), 'utf8'))

test('the current scheme is inspr-calver-3, labelled from the pinned label table', () => {
  assert.equal(SCHEME, 'inspr-calver-3')
  assert.equal(schemes.current, SCHEME)
  assert.equal(schemes.labels[SCHEME], 'INSPR-CalVer3')
  assert.equal(schemes.labels[CALVER2], 'INSPR-CalVer2')
  assert.equal(schemes.labels['inspr-calendar-v1'], 'INSPR-CalVer1')
  assert.deepEqual(schemes.deprecated, [CALVER2])
})

test('a new reservation must declare inspr-calver-3; inspr-calendar-v2 stays valid for history', () => {
  const next = '260929120000.0.0'
  assert.equal(schemeError(SCHEME, next), '')
  assert.match(schemeError(CALVER2, next), /new reservations declare inspr-calver-3/)
  assert.equal(schemeError(CALVER2, LAST_CALVER2), '')
  assert.equal(schemeError(CALVER2, '260923134337.0.0'), '')
  assert.match(schemeError(SCHEME, LAST_CALVER2), /must be later/)
  assert.match(schemeError('legacy', next), /unknown version scheme/)
})

test('every published Aeon tag is a valid calendar version that both calendar schemes parse', () => {
  let tags: string[] = []
  try { tags = execFileSync('git', ['tag', '-l', 'v*'], { cwd: repo, encoding: 'utf8' }).split('\n').filter(Boolean) } catch { /* no git: nothing to check */ }
  if (tags.length === 0) return
  for (const tag of tags) {
    const version = tag.slice(1)
    assert.ok(validCalendarVersion(version), `${tag} is a calendar version`)
    assert.ok(version <= LAST_CALVER2 || schemeError(SCHEME, version) === '', `${tag} fits its era`)
    for (const scheme of [SCHEME, CALVER2]) assert.ok(parts(version, scheme), `${tag} renders as ${scheme}`)
  }
  assert.ok(tags.includes(`v${LAST_CALVER2}`), 'the last CalVer2 release is tagged')
})

test('version.json declares a scheme the reservation rules allow', () => {
  const ver = JSON.parse(readFileSync(new URL('version.json', repo), 'utf8'))
  assert.equal(schemeError(ver.version_scheme, ver.version), '')
})
