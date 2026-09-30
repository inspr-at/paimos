// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import test from 'node:test'
import { findMutations, scanTests } from './check-fixture-mutation.mjs'

const lines = source => findMutations(source, 'sample.spec.ts').map(finding => finding.line)

test('flags a write through an imported fixture', () => {
  const source = `
    import { me } from './work-fixtures'
    me.name = 'Ada'
    me.roles.push('admin')
    delete me.extra
    Object.assign(me, { name: 'Bea' })
  `
  assert.deepEqual(lines(source), [3, 4, 5, 6])
})

test('flags an alias and a destructured binding', () => {
  const source = `
    import { me, fixture } from './work-fixtures'
    const alias = me
    alias.roles.push('admin')
    const { permissions } = fixture
    permissions.push('account.read')
    const { workspace: { grants } } = fixture
    grants.push('nodes.write')
  `
  assert.deepEqual(lines(source), [4, 6, 8])
})

test('allows a push on a fresh factory result, the AEON-373 call shape', () => {
  const source = `
    import { mockEffectivePermissions } from './authz-fixtures'
    const effective = mockEffectivePermissions('admin')
    effective.workspace.permissions.push('account.read')
    const again = mockEffectivePermissions('admin')
    again.workspace.permissions = again.workspace.permissions.filter(item => item !== 'x')
  `
  assert.deepEqual(lines(source), [])
})

test('allows reads, non-relative imports, and a shadowing local', () => {
  const source = `
    import { test } from '@playwright/test'
    import { me } from './work-fixtures'
    const name = me.name
    test('local', () => {
      const me = { roles: [] as string[] }
      me.roles.push('admin')
    })
    void name
  `
  assert.deepEqual(lines(source), [])
})

test('flags a mutation of an element yielded by an imported fixture', () => {
  const source = `
    import { nodes } from './work-fixtures'
    nodes.forEach(node => { node.tags.push('x') })
    for (const node of nodes) node.title = 'y'
  `
  assert.deepEqual(lines(source), [3, 4])
})

test('the UI suite does not mutate imported fixtures', () => {
  const findings = scanTests()
  assert.deepEqual(findings, [])
})
