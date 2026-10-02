// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import * as Access from '../src/lib/access'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'

// Execute the sheet's real setup in Node, with its API answer and caller grants
// injected. No browser is needed to test the unavailable-scope explanation.
async function sheetReason(options: { builtin?: boolean; permissions?: string[]; mine?: string[]; agentGrantable?: boolean; role?: boolean; projectRole?: boolean; projectPermissions?: string[] } = {}) {
  vi.stubGlobal('navigator', { platform: 'MacIntel' })
  const role: Access.Role = { id: 'admin', key: 'admin', name: 'Admin', builtin: options.builtin ?? true, permissions: options.permissions ?? ['nodes.read', 'recurrences.manage'], member_count: 1 }
  const registry: Access.Permission[] = [{ key: 'recurrences.manage', group: 'Recurrences', description: 'Manage recurrence automation', risk: 'high', grantable_at: ['workspace', 'project'], agent_grantable: options.agentGrantable ?? true }]
  const agent: Access.Agent = { principal_id: 'agent', name: 'Worker', has_avatar: false, workspace_role: options.role === false ? null : role, key_count: 1, last_seen_at: null, service: false }
  const roles = [role]
  if (options.projectRole) {
    const projectRole: Access.Role = { ...role, id: 'project-role', key: 'recurrence-operator', name: 'Recurrence operator', builtin: false, permissions: options.projectPermissions ?? ['recurrences.manage'] }
    roles.push(projectRole)
    agent.project_roles = [{ project_id: 'project', project_key: 'AEON', project_title: 'Aeon', role: projectRole }]
  }
  const agentKey = { id: 'key', principal_id: agent.principal_id, prefix: 'test', scopes: ['nodes.read'], expires_at: null }
  const source = readFileSync(new URL('../src/components/access/EditKeyScopesSheet.vue', import.meta.url), 'utf8')
  const { descriptor } = parse(source)
  const { content } = compileScript(descriptor, { id: 'edit-scopes-test' })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  let load!: () => Promise<void>
  const modules: Record<string, unknown> = {
    vue: { ...Vue, onMounted: (callback: typeof load) => { load = callback } },
    '../../lib/access': { ...Access, getAgentKeyScopes: async () => ({ key: agentKey, grantable_scopes: ['nodes.read'], agent_role: options.role === false ? null : role, role_grantable_scopes: [] }) },
    '../../lib/authz': { can: () => true, myPermissions: () => new Set(options.mine ?? ['recurrences.manage']) },
    '../../stores/access': { useAccess: () => ({ registry, roles, roleById: new Map(roles.map(r => [r.id, r])), agent: () => agent }) },
    './accessText': { problem: () => 'Unexpected load failure' },
  }
  const exports: { default?: { setup: (props: unknown, context: unknown) => { unavailableReason: (key: string) => string; loaded: Vue.Ref<boolean> } } } = {}
  new Function('require', 'exports', outputText)((id: string) => {
    if (id.endsWith('.vue')) return { default: {} }
    if (!(id in modules)) throw new Error(`Unexpected sheet dependency: ${id}`)
    return modules[id]
  }, exports)
  const state = exports.default!.setup({ agent, agentKey }, { expose: () => {}, emit: () => {} })
  await load()
  expect(state.loaded.value).toBe(true)
  return state.unavailableReason('recurrences.manage')
}

afterEach(() => vi.unstubAllGlobals())

it('names the built-in Admin agent role as the recurrence ceiling', async () => {
  expect(await sheetReason()).toBe("Not in this agent's role (Admin)")
})

it('keeps an explicit custom-role recurrence grant and names the creator ceiling', async () => {
  expect(await sheetReason({ builtin: false })).toBe("Not in the key creator's current permissions")
})

it('names the creator ceiling when a custom project role grants recurrence alongside built-in Admin', async () => {
  expect(await sheetReason({ projectRole: true })).toBe("Not in the key creator's current permissions")
})

it('names the creator ceiling when a custom project role alone grants recurrence', async () => {
  expect(await sheetReason({ role: false, projectRole: true })).toBe("Not in the key creator's current permissions")
})

it('retains the other unavailable reasons and their precedence', async () => {
  expect(await sheetReason({ mine: [] })).toBe('Not in your permissions')
  expect(await sheetReason({ mine: [], agentGrantable: false })).toBe('Unavailable to agent keys')
  expect(await sheetReason({ builtin: false, permissions: ['nodes.read'] })).toBe("Not in this agent's role (Admin)")
  expect(await sheetReason({ role: false })).toBe("Not in this agent's project roles")
  expect(await sheetReason({ projectRole: true, projectPermissions: ['nodes.read'] })).toBe("Not in this agent's role (Admin)")
  expect(await sheetReason({ role: false, projectRole: true, projectPermissions: ['nodes.read'] })).toBe("Not in this agent's project roles")
})
