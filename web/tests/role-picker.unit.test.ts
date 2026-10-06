// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import * as Access from '../src/lib/access'
import { grantablePermissions } from '../src/lib/authz'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'

function picker() {
  const roles: Access.Role[] = [
    { id: 'member', key: 'member', name: 'Member', builtin: true, permissions: ['nodes.read'], member_count: 1 },
    { id: 'viewer', key: 'viewer', name: 'Viewer', builtin: true, permissions: [], member_count: 1 },
  ]
  const source = readFileSync(new URL('../src/components/access/RolePicker.vue', import.meta.url), 'utf8')
  const { descriptor } = parse(source)
  const { content } = compileScript(descriptor, { id: 'role-picker-test' })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const modules: Record<string, unknown> = {
    vue: { ...Vue, onMounted: () => {}, useId: () => 'test-role' },
    '../../lib/access': Access,
    '../../lib/authz': { grantablePermissions },
  }
  const exports: { default?: { setup: (props: unknown, context: unknown) => { keys: (event: unknown) => void; picked: Vue.Ref<string> } } } = {}
  new Function('require', 'exports', outputText)((id: string) => {
    if (id.endsWith('.vue')) return { default: {} }
    if (!(id in modules)) throw new Error(`Unexpected picker dependency: ${id}`)
    return modules[id]
  }, exports)
  const emit = vi.fn()
  const state = exports.default!.setup({ anchor: null, subject: 'Jonas', roles, current: 'member', registry: [], mine: new Set(['nodes.read']), scope: 'workspace', canApply: true, roleDetails: new Map() }, { expose: () => {}, emit })
  state.picked.value = 'viewer'
  return { state, emit }
}

for (const control of ['Cancel', 'Apply', 'preview']) it(`Enter preserves native activation on ${control}`, () => {
  const { state, emit } = picker()
  const preventDefault = vi.fn()
  state.keys({ key: 'Enter', target: { closest: () => null }, preventDefault })
  expect(preventDefault).not.toHaveBeenCalled()
  expect(emit).not.toHaveBeenCalled()
})

it('Enter on a role submits its changed choice once', () => {
  const { state, emit } = picker()
  const preventDefault = vi.fn()
  state.keys({ key: 'Enter', target: { closest: () => ({}) }, preventDefault })
  expect(preventDefault).toHaveBeenCalledOnce()
  expect(emit.mock.calls).toEqual([['choose', 'viewer']])
})

for (const modifier of ['altKey', 'ctrlKey', 'metaKey', 'shiftKey']) it(`modified Enter (${modifier}) stays native`, () => {
  const { state, emit } = picker()
  const preventDefault = vi.fn()
  state.keys({ key: 'Enter', [modifier]: true, target: { closest: () => ({}) }, preventDefault })
  expect(preventDefault).not.toHaveBeenCalled()
  expect(emit).not.toHaveBeenCalled()
})
