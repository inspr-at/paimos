// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from 'vitest'
import { permissionLabel } from '../src/lib/access'

test('profile permissions describe the caller’s personal profile', () => {
  expect(permissionLabel('profile.read')).toBe('See their own profile')
  expect(permissionLabel('profile.write')).toBe('Edit their own profile')
})
