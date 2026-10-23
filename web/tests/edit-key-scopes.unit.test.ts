// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import * as Access from '../src/lib/access'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'
import { deferred, flush, sourceModule } from './record-source'
import { scopeOwner } from '../src/lib/identityScope'

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

async function adoptionTab() {
  const session = Vue.reactive({ identity: { tenant: { id: 'tenant-a' }, principal: { id: 'person-a', kind: 'person' } } })
  const key = { id: 'legacy', principal_id: 'agent', name: 'Legacy', created_by_principal_id: null, prefix: 'test', scopes: [], expires_at: null, revoked_at: null, created_at: '', last_used_at: null }
  const calls: string[] = [], toasts: string[] = []
  let confirm!: (ok: boolean) => void, respond!: (result: unknown) => void, unmount!: () => void
  // The Agents tab reads the shared key cache of the access store, so the store stands in with the same shape.
  const store = Vue.reactive({
    agents: [], keys: [{ ...key }] as Array<typeof key> | null, load: async () => {}, loadKeys: async () => {},
    noteKey: (next: typeof key) => { store.keys = store.keys?.map(k => k.id === next.id ? next : k) ?? null },
  })
  const modules: Record<string, unknown> = {
    vue: { ...Vue, onMounted: () => {}, onUnmounted: (callback: () => void) => { unmount = callback } },
    'vue-router': { useRoute: () => ({ query: {} }), useRouter: () => ({ replace: async () => {} }) },
    '../../stores/session': { useSession: () => session },
    '../../stores/access': { useAccess: () => store },
    '../../lib/access': { ...Access, adoptAgentKey: (id: string) => { calls.push(id); return new Promise(resolve => { respond = resolve }) } },
    '../../lib/settings': { keyState: () => 'active' },
    '../../lib/confirm': { confirmAction: () => new Promise(resolve => { confirm = resolve }) },
    '../../lib/toast': { toast: (message: string) => toasts.push(message) },
    '../../lib/authz': { can: () => true, myPermissions: () => new Set() },
    '../../lib/brand': { brand: {} }, '../../lib/work': { relativeTime: () => '' },
    './accessText': { problem: () => 'failed', undoing: () => {} },
  }
  const { descriptor } = parse(readFileSync(new URL('../src/components/access/AgentsTab.vue', import.meta.url), 'utf8'))
  const { content } = compileScript(descriptor, { id: 'adoption-test' })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: { default?: { setup: (props: unknown, context: unknown) => { adopt: (target: typeof key) => Promise<void>; keys: Vue.Ref<Array<typeof key> | null> } } } = {}
  new Function('require', 'exports', outputText)((id: string) => {
    if (id.endsWith('.vue')) return { default: {} }
    if (!(id in modules)) throw new Error(`Unexpected adoption dependency: ${id}`)
    return modules[id]
  }, exports)
  const scope = Vue.effectScope()
  const state = scope.run(() => exports.default!.setup({}, { expose: () => {} }))!
  return { session, key, state, store, calls, toasts, confirm: (ok: boolean) => confirm(ok), respond: () => respond({ ...key, created_by_principal_id: 'person-a' }), unmount: () => unmount(), stop: () => scope.stop() }
}

it('adoption confirmation is bound to the acting person and key', async () => {
  for (const changed of ['person', 'key'] as const) {
    const tab = await adoptionTab()
    const pending = tab.state.adopt(tab.key)
    if (changed === 'person') { tab.session.identity.principal.id = 'person-b'; await Vue.nextTick() }
    else tab.store.keys = []
    tab.confirm(true)
    await pending
    expect(tab.calls).toEqual([])
    expect(tab.toasts).toEqual([])
    tab.stop()
  }
})

it('late adoption results cannot restore another person or a departed key', async () => {
  for (const changed of ['person', 'key', 'unmount'] as const) {
    const tab = await adoptionTab()
    const pending = tab.state.adopt(tab.key)
    tab.confirm(true)
    await Vue.nextTick()
    expect(tab.calls).toEqual(['legacy'])
    if (changed === 'person') { tab.session.identity.principal.id = 'person-b'; await Vue.nextTick() }
    else if (changed === 'key') tab.store.keys = []
    else tab.unmount()
    tab.respond()
    await pending
    expect(tab.toasts).toEqual([])
    expect(tab.store.keys?.every(k => k.created_by_principal_id === null) ?? true).toBe(true)
    tab.stop()
  }
})

// P03 / AEON-1604: these are ticket edit/comment intents, separate from API-key
// scope editing above. Run the production owner with held permission answers.
function ticketShortcuts() {
  const listeners = new Map<string, (event: { target: unknown }) => void>()
  const observers = new Set<(records: MutationRecord[]) => void>()
  let active: Element | null = null, dialog = false
  class Element {
    tagName = 'DIV'
    isContentEditable = false
    isConnected = true
    closest() { return null }
    matches() { return false }
    querySelector() { return null }
    contains(target: unknown) { return target === field }
    focus() { active = this; listeners.get('focusin')?.({ target: this }) }
  }
  class Input extends Element { tagName = 'INPUT'; type = 'text' }
  class Textarea extends Element { tagName = 'TEXTAREA'; disabled = true }
  class Dialog extends Element {}
  class Observer {
    constructor(private callback: (records: MutationRecord[]) => void) {}
    observe() { observers.add(this.callback) }
    disconnect() { observers.delete(this.callback) }
  }
  const field = new Textarea(), root = new Element()
  root.querySelector = () => field as never
  vi.stubGlobal('HTMLElement', Element); vi.stubGlobal('Element', Element)
  vi.stubGlobal('HTMLInputElement', Input); vi.stubGlobal('HTMLTextAreaElement', Textarea)
  vi.stubGlobal('HTMLDialogElement', Dialog); vi.stubGlobal('MutationObserver', Observer)
  vi.stubGlobal('window', { addEventListener() {}, removeEventListener() {} })
  vi.stubGlobal('document', {
    body: root, get activeElement() { return active }, querySelector: () => dialog ? {} : null,
    addEventListener: (name: string, listener: (event: { target: unknown }) => void) => listeners.set(name, listener),
    removeEventListener: (name: string) => listeners.delete(name),
  })
  const known = Vue.ref(false), permission = deferred<'known' | 'unavailable'>()
  const ensurePermissions = vi.fn(() => permission.promise)
  const session = Vue.reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'person' } }, authenticationCurrent: () => true })
  const startEdit = vi.fn(), focusComposer = vi.fn(() => { if (!field.disabled) field.focus() })
  const workspace = { el: root, startEdit, focusComposer }
  const state = {
    ticketKey: Vue.ref('PHAROS-12'), projectId: Vue.ref('project'), session,
    knowledgeActive: Vue.ref(false), settingsActive: Vue.ref(false), deliveryActive: Vue.ref(false), graphActive: Vue.ref(false), outlineActive: Vue.ref(false),
    toolbar: Vue.ref(null), table: Vue.ref(null), panel: Vue.shallowRef(workspace), cursorId: Vue.ref(null), creating: Vue.ref(false), fullView: Vue.ref(true),
    writable: Vue.ref(false), commentable: Vue.ref(false),
  }
  const pane = { panelItem: Vue.ref<{ key: string } | null>({ key: 'PHAROS-12' }), trail: Vue.ref([]) }
  const data = { list: {}, outline: {}, activeLive: Vue.ref({}), liveActive: Vue.ref(false), sequence: Vue.ref([]) }
  const selection = { selectable: Vue.ref(false), selected: Vue.ref(new Set()), phonePicking: Vue.ref(false) }
  const { useProjectKeyboard } = sourceModule<typeof import('../src/views/project/useProjectKeyboard')>('views/project/useProjectKeyboard.ts', {
    vue: { ...Vue, onMounted() {}, onBeforeUnmount: Vue.onScopeDispose },
    '../../lib/authz': { ensurePermissions, permissionsKnown: () => known.value }, '../../lib/identityScope': { scopeOwner },
  })
  const scope = Vue.effectScope()
  const keys = scope.run(() => useProjectKeyboard(state as never, data as never, pane as never, selection as never, {} as never, {} as never))!
  const press = (key: string, target: unknown = null) => keys.keydown({ key, target, preventDefault() {} } as KeyboardEvent)
  const grant = async (write = true, comment = true) => {
    state.writable.value = write; state.commentable.value = comment; field.disabled = !comment
    known.value = true; permission.resolve('known'); await flush()
  }
  return { state, pane, workspace, known, press, grant, permission, ensurePermissions, startEdit, focusComposer,
    enterField() { new Input().focus(); active = null },
    openDialog() {
      dialog = true
      for (const callback of observers) callback([{ type: 'childList', addedNodes: [new Dialog()] }] as unknown as MutationRecord[])
      dialog = false
    },
    stop: () => scope.stop(),
  }
}

it('P03: deferred e waits for both grants and the captured ticket, in either order', async () => {
  for (const first of ['ticket', 'permissions']) {
    const h = ticketShortcuts()
    try {
      h.pane.panelItem.value = null
      h.press('e')
      expect(h.ensurePermissions).toHaveBeenCalledExactlyOnceWith('project')
      expect(h.startEdit).not.toHaveBeenCalled()
      if (first === 'permissions') await h.grant()
      else { h.pane.panelItem.value = { key: 'PHAROS-12' }; await flush() }
      expect(h.startEdit).not.toHaveBeenCalled()
      if (first === 'permissions') { h.pane.panelItem.value = { key: 'PHAROS-12' }; await flush() }
      else await h.grant()
      expect(h.startEdit).toHaveBeenCalledTimes(1)
      h.state.panel.value = { ...h.workspace }; await flush()
      expect(h.startEdit).toHaveBeenCalledTimes(1)
    } finally { h.stop() }
  }
})

it('P03: deferred ticket shortcuts are spent by changed ownership, fields, dialogs and denial', async () => {
  for (const key of ['e', 'c']) for (const interruption of ['ticket', 'project', 'person', 'tenant', 'field', 'dialog', 'denial', 'unmount']) {
    const h = ticketShortcuts()
    try {
      h.press(key)
      expect(h.ensurePermissions).toHaveBeenCalledTimes(1)
      if (interruption === 'ticket') h.state.ticketKey.value = 'PHAROS-11'
      if (interruption === 'project') h.state.projectId.value = 'other-project'
      if (interruption === 'person') h.state.session.identity.principal.id = 'other-person'
      if (interruption === 'tenant') h.state.session.identity.tenant.id = 'other-tenant'
      if (interruption === 'field') h.enterField()
      if (interruption === 'dialog') h.openDialog()
      if (interruption === 'denial') { await h.grant(false, false); h.known.value = false; await flush() }
      if (interruption === 'unmount') h.stop()
      await flush(); await h.grant()
      expect(h.startEdit, `${key}: ${interruption}`).not.toHaveBeenCalled()
      expect(h.focusComposer, `${key}: ${interruption}`).not.toHaveBeenCalled()
    } finally { h.stop() }
  }
})

it('P03: the latest e/c intent alone runs once, while denied readers gain no intent', async () => {
  for (const last of ['e', 'c']) {
    const h = ticketShortcuts()
    try {
      h.press(last === 'e' ? 'c' : 'e'); h.press(last)
      await h.grant()
      expect(h.startEdit).toHaveBeenCalledTimes(last === 'e' ? 1 : 0)
      expect(h.focusComposer).toHaveBeenCalledTimes(last === 'c' ? 1 : 0)
    } finally { h.stop() }
  }
  const h = ticketShortcuts()
  try {
    await h.grant(false, false)
    h.press('e'); h.press('c'); await h.grant()
    expect(h.ensurePermissions).not.toHaveBeenCalled()
    expect(h.startEdit).not.toHaveBeenCalled()
    expect(h.focusComposer).not.toHaveBeenCalled()
  } finally { h.stop() }
})
