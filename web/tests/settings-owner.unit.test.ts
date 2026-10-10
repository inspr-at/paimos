// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import * as settings from '../src/lib/settings'
import * as footerProviders from '../src/lib/footerProviders'
import * as footerSummary from '../src/lib/footerSummary'
import * as preferences from '../src/lib/preferences'
import { displayLanguage } from '../src/lib/displayLanguage'
import { textForKinds } from '../src/lib/workKindsCopy'
import { scopeOwner } from '../src/lib/identityScope'
import { flush } from './record-source'

type Owner = { tenant: { id: string }; principal: { id: string } }
type Node = { tag: string; text: string; props: Record<string, unknown>; children: Node[]; parent: Node | null }
const node = (tag: string): Node => ({ tag, text: '', props: {}, children: [], parent: null })
function detach(child: Node) { const siblings = child.parent?.children; if (siblings) siblings.splice(siblings.indexOf(child), 1); child.parent = null }
const renderer = Vue.createRenderer<Node, Node>({
  createElement: node, createText: text => ({ ...node('#text'), text }), createComment: () => node('#comment'),
  setText: (el, text) => { el.text = text }, setElementText: (el, text) => { el.text = text; el.children = [] },
  parentNode: el => el.parent, nextSibling: el => el.parent?.children[el.parent.children.indexOf(el) + 1] ?? null,
  patchProp: (el, key, _old, value) => { el.props[key] = value }, remove: detach,
  insert: (child, parent, anchor) => { detach(child); child.parent = parent; const at = anchor ? parent.children.indexOf(anchor) : -1; if (at < 0) parent.children.push(child); else parent.children.splice(at, 0, child) },
})
const person = (principal = 'person-a', tenant = 'tenant-a'): Owner => ({ tenant: { id: tenant }, principal: { id: principal } })
const session = Vue.reactive<{ identity: Owner | null }>({ identity: person() })
const grants = Vue.reactive({ revoked: false, access: true, admin: true })
const route = Vue.reactive({ params: { section: 'access' }, query: {}, hash: '' })
const mounted = vi.fn(), unmounted = vi.fn()
const drafts: Vue.Ref<string>[] = [], links: Vue.Ref<string>[] = [], apps: Vue.App[] = []
const section = Vue.defineComponent({
  setup() {
    // Child state models a draft and a one-time link: neither can be reloaded
    // after an unmount. Exercise the actual Settings frame and component key.
    const draft = Vue.ref(''), link = Vue.ref('')
    drafts.push(draft); links.push(link)
    Vue.onMounted(mounted); Vue.onBeforeUnmount(unmounted)
    return () => Vue.h('section', { 'data-section': 'child' }, [Vue.h('input', { value: draft.value }), Vue.h('output', link.value), ...(!grants.revoked ? [Vue.h('button', 'Save')] : [])])
  },
})
function component() {
  const baseline = process.env.AEON_RECORD_BASELINE
  const source = baseline ? execFileSync('git', ['show', `${baseline === '1' ? 'HEAD' : baseline}:web/src/views/SettingsView.vue`], { encoding: 'utf8' }) : readFileSync(new URL('../src/views/SettingsView.vue', import.meta.url), 'utf8')
  const { descriptor } = parse(source)
  const { content } = compileScript(descriptor, { id: 'settings-owner', inlineTemplate: true, templateOptions: { compilerOptions: { hoistStatic: false } } })
  const modules: Record<string, unknown> = {
    vue: Vue, 'vue-router': { useRoute: () => route, useRouter: () => ({ push: async () => {} }) }, '../lib/settings': settings,
    '../lib/footerProviders': footerProviders, '../lib/footerSummary': footerSummary, '../lib/preferences': preferences, '../lib/identityScope': { scopeOwner },
    '../stores/session': { useSession: () => session }, '../lib/doctrineInbox': { doctrineInbox: {} },
    '../stores/profile': { useProfile: () => ({ profile: null }) }, '../lib/workKindsCopy': { textForKinds }, '../lib/displayLanguage': { displayLanguage },
    '../lib/authz': { can: (permission: string) => !grants.revoked && (permission === 'settings.manage' ? grants.admin : grants.access), permissionsKnown: () => true, permissionsRevoked: () => grants.revoked, refreshPermissions: async () => {} },
  }
  const exports: { default?: Vue.Component } = {}
  new Function('require', 'exports', ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText)((id: string) => {
    if (id.endsWith('.css')) return {}
    if (id.endsWith('Section.vue')) return { __esModule: true, default: section }
    if (id.endsWith('.vue')) return { __esModule: true, default: Vue.defineComponent({ render: () => null }) }
    if (!(id in modules)) throw new Error(`Missing dependency ${id}`)
    return modules[id]
  }, exports)
  return exports.default!
}
const Settings = component()
function mount() {
  const root = node('root'), app = renderer.createApp(Settings)
  app.component('RouterLink', Vue.defineComponent({ setup: (_, { slots }) => () => Vue.h('a', slots.default?.()) }))
  app.directive('clip-tip', {})
  apps.push(app); app.mount(root)
  return root
}
const all = (root: Node): Node[] => [root, ...root.children.flatMap(all)]
const input = (root: Node) => all(root).find(el => el.tag === 'input')
beforeEach(() => {
  session.identity = person(); grants.revoked = false; grants.access = true; grants.admin = true; route.params.section = 'access'; route.hash = ''
  drafts.length = 0; links.length = 0; mounted.mockClear(); unmounted.mockClear()
  vi.stubGlobal('document', { documentElement: { lang: 'en' }, addEventListener() {}, removeEventListener() {} })
  vi.stubGlobal('window', { addEventListener() {}, removeEventListener() {} })
})
afterEach(() => { for (const app of apps.splice(0)) app.unmount(); vi.unstubAllGlobals(); vi.useRealTimers() })

it.each(['access', 'personal'])('%s retains the same child, draft and one-time link on expiry, with writes inert', async current => {
  route.params.section = current
  const root = mount()
  await flush()
  drafts[0]!.value = 'Unsaved draft'; links[0]!.value = 'https://example.invalid/join/one-time'
  await flush()
  const before = input(root)
  expect(before?.props.value).toBe('Unsaved draft')
  session.identity = null; grants.revoked = true
  await flush()
  expect(input(root) === before, 'expiry keeps the original input node').toBe(true)
  expect(input(root)?.props.value).toBe('Unsaved draft')
  expect(all(root).find(el => el.tag === 'output')?.text).toBe('https://example.invalid/join/one-time')
  expect(all(root).filter(el => el.tag === 'button' && el.text === 'Save')).toHaveLength(0)
  expect(mounted).toHaveBeenCalledTimes(1)
  expect(unmounted).not.toHaveBeenCalled()
})

it('a section opened in the same frame also keeps its draft on expiry', async () => {
  route.params.section = 'personal'
  const root = mount()
  await flush()
  route.params.section = 'access'; await flush()
  drafts.at(-1)!.value = 'Access draft'; await flush()
  const before = input(root)
  expect(before?.props.value).toBe('Access draft')
  session.identity = null; grants.revoked = true; await flush()
  expect(input(root) === before, 'expiry keeps the original input node').toBe(true)
  expect(input(root)?.props.value).toBe('Access draft')
})

it.each([person('person-b'), person('person-a', 'tenant-b')])('a different authenticated owner replaces the frozen child with fresh state (%j)', async next => {
  const root = mount()
  await flush()
  drafts[0]!.value = 'Previous owner draft'; links[0]!.value = 'Previous owner link'
  session.identity = null; grants.revoked = true; await flush()
  expect(input(root)?.props.value).toBe('Previous owner draft')
  session.identity = next; grants.revoked = false; await flush()
  expect(input(root)?.props.value).toBe('')
  expect(all(root).find(el => el.tag === 'output')?.text).toBe('')
  expect(mounted).toHaveBeenCalledTimes(2)
  expect(unmounted).toHaveBeenCalledTimes(1)
  drafts.at(-1)!.value = 'New owner draft'
  session.identity = null; grants.revoked = true; await flush()
  expect(input(root)?.props.value).toBe('New owner draft')
})

it('same-owner refresh keeps mounted state, but lost permission closes Access', async () => {
  const root = mount()
  await flush()
  drafts[0]!.value = 'Kept draft'; await flush()
  session.identity = person(); await flush()
  expect(input(root)?.props.value).toBe('Kept draft')
  expect(mounted).toHaveBeenCalledTimes(1)
  grants.access = false; await flush()
  expect(input(root)).toBeUndefined()
  expect(unmounted).toHaveBeenCalledTimes(1)
})

it('a new owner without Access cannot resurrect the previous owner’s shown section on expiry', async () => {
  const root = mount()
  await flush()
  session.identity = null; grants.revoked = true; await flush()
  session.identity = person('person-b'); grants.revoked = false; grants.access = false; await flush()
  expect(input(root)).toBeUndefined()
  session.identity = null; grants.revoked = true; await flush()
  expect(input(root)).toBeUndefined()
})

it('an incoming owner clears frozen Access before fresh permissions arrive', async () => {
  const root = mount()
  await flush()
  session.identity = null; grants.revoked = true; await flush()
  expect(input(root)).toBeDefined()
  session.identity = person('person-b'); await flush()
  expect(input(root) === undefined, 'a new owner has no frozen Access while grants are pending').toBe(true)
})

it.each(['agent-activity', 'estimates', 'silent-sessions'])('legacy #%s highlights its consolidated field until the arrival expires', async anchor => {
  vi.useFakeTimers()
  const classes = new Set<string>(['frow'])
  const target = {
    classList: { contains: (name: string) => classes.has(name), add: (name: string) => classes.add(name), remove: (name: string) => classes.delete(name) },
    querySelector: vi.fn(() => null), scrollIntoView: vi.fn(),
  }
  Object.assign(document, { getElementById: (id: string) => id === anchor ? target : null })
  Object.assign(window, { matchMedia: () => ({ matches: true }) })
  route.params.section = 'agents'; route.hash = `#${anchor}`
  mount(); await flush()
  expect(target.scrollIntoView).toHaveBeenCalledExactlyOnceWith({ block: 'start', behavior: 'auto' })
  expect(classes.has('arrived'), 'the bookmarked field has a visible arrival ring').toBe(true)
  await vi.advanceTimersByTimeAsync(1799)
  expect(classes.has('arrived')).toBe(true)
  await vi.advanceTimersByTimeAsync(1)
  expect(classes.has('arrived')).toBe(false)
})
