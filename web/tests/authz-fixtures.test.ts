// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mockEffectivePermissions, mockGuestPermissions } from './authz-fixtures.ts'

test('a pushed grant stays on that answer', () => {
  const first = mockEffectivePermissions('admin', 'p-1')
  first.workspace.permissions.push('account.read')
  assert.equal(first.workspace.permissions.includes('account.read'), true)
  assert.equal(first.project?.permissions.includes('account.read'), true)
  const second = mockEffectivePermissions('admin', 'p-1')
  assert.equal(second.workspace.permissions.includes('account.read'), false)
  assert.equal(second.project?.permissions.includes('account.read'), false)
})

test('a guest grant stays on that answer', () => {
  const first = mockGuestPermissions('p-1', ['p-1'])
  first.project?.permissions.push('nodes.write')
  const second = mockGuestPermissions('p-1', ['p-1'])
  assert.equal(second.project?.permissions.includes('nodes.write'), false)
})
