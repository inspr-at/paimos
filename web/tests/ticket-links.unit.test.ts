// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import * as links from '../src/lib/ticketLinks'
import { ticketPath } from '../src/lib/work'

const mocks = vi.hoisted(() => ({ lookup: vi.fn(), access: (_change: 'reset' | 'refresh') => {} }))
vi.mock('../src/lib/api.ts', () => ({ lookupNodeKeys: mocks.lookup }))
vi.mock('../src/lib/authz.ts', () => ({ onAccessChange: (callback: typeof mocks.access) => { mocks.access = callback } }))

type Node = { tag: string; text: string; props: Record<string, unknown>; children: Node[]; parent: Node | null }
const node = (tag: string): Node => ({ tag, text: '', props: {}, children: [], parent: null })
function detach(el: Node) {
  if (el.parent) el.parent.children.splice(el.parent.children.indexOf(el), 1)
  el.parent = null
}
const renderer = Vue.createRenderer<Node, Node>({
  createElement: node, createText: text => ({ ...node('#text'), text }), createComment: () => node('#comment'),
  setText: (el, text) => { el.text = text }, setElementText: (el, text) => { el.text = text },
  parentNode: el => el.parent, nextSibling: el => el.parent?.children[el.parent.children.indexOf(el) + 1] ?? null,
  patchProp: (el, key, _old, value) => { el.props[key] = value }, remove: detach,
  insert: (child, parent, anchor) => {
    detach(child); child.parent = parent
    const at = anchor ? parent.children.indexOf(anchor) : -1
    if (at < 0) parent.children.push(child); else parent.children.splice(at, 0, child)
  },
})
// Compile the actual client component: Node-mode Vite otherwise emits SSR.
const { descriptor } = parse(readFileSync(new URL('../src/components/releases/TicketLink.vue', import.meta.url), 'utf8'))
const { content } = compileScript(descriptor, { id: 'ticket-link-test', inlineTemplate: true })
const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
const modules: Record<string, unknown> = {
  vue: Vue, 'vue-router': { useRouter: () => ({ push: vi.fn() }) },
  '../../lib/brand': { brand: Vue.ref({ short_name: 'AEON' }) },
  '../../lib/ticketPeek': { TICKET_PEEK: Symbol('peek') }, '../../lib/ticketLinks': links,
  '../../lib/work': { ticketPath },
  '../../stores/projects': { useProjects: () => ({ load: async () => {}, byId: () => ({ routeKey: 'AEON' }) }) },
}
const compiled: { default?: Vue.Component } = {}
new Function('require', 'exports', outputText)((id: string) => {
  if (!(id in modules)) throw new Error(`Unexpected dependency ${id}`)
  return modules[id]
}, compiled)

type Answer = { items: { requested_key: string; key: string; id: string; title: string; state: string; project_id: string }[] }
const answer = (keys: string[]): Answer => ({ items: keys.map(key => ({ requested_key: key, key, id: key, title: key, state: 'open', project_id: 'project' })) })
const requests: { keys: string[]; release: (answer: Answer) => void }[] = []
const apps: Vue.App[] = []
const keys = Array.from({ length: 250 }, (_, i) => `AEON-${i + 1}`)
// Drain the finite promise/Vue queue, with network replies held independently.
const flush = async () => { for (let i = 0; i < 12; i++) { await Promise.resolve(); await Vue.nextTick() } }
function mount(initial: string[]) {
  const shown = Vue.ref(initial)
  const root = node('root')
  const app = renderer.createApp({ render: () => Vue.h('div', shown.value.map((key, i) => Vue.h(compiled.default!, { key: i, ticketKey: key }))) })
  apps.push(app); app.mount(root)
  const pills = () => root.children[0]!.children.filter(el => el.tag === 'a' || el.tag === 'span').map(el => ({ key: el.text, tag: el.tag, class: el.props.class }))
  return { shown, pills, app }
}
beforeEach(() => {
  links.forgetTicketKeys(); requests.length = 0
  mocks.lookup.mockReset().mockImplementation((keys: string[]) => new Promise<Answer>(release => requests.push({ keys, release })))
})
afterEach(() => { for (const app of apps.splice(0)) app.unmount(); links.forgetTicketKeys() })

it('250 mounted links resolve once in bounded batches and stay stable, including missing keys', async () => {
  const view = mount(keys)
  await flush()
  expect(requests.map(r => r.keys.length)).toEqual([100, 100, 50])
  const resolved = new Set<string>()
  for (const request of [...requests]) {
    const found = request.keys.filter(key => Number(key.split('-')[1]) <= 240)
    request.release(answer(found))
    for (const key of found) resolved.add(key)
    await flush()
    expect(view.pills().filter(pill => resolved.has(pill.key)).every(pill => pill.tag === 'a')).toBe(true)
  }
  expect(requests).toHaveLength(3)
  expect(view.pills().map(pill => pill.tag)).toEqual(keys.map((_, i) => i < 240 ? 'a' : 'span'))
  const stable = view.pills()
  // A rerender and repeated demand must neither lose answers nor ask again.
  view.shown.value = [...keys]
  for (const key of keys) links.wantTicketKey(key)
  await flush()
  expect(view.pills()).toEqual(stable)
  expect(requests).toHaveLength(3)
  expect(requests.flatMap(r => r.keys)).toEqual(keys)
})

it('access refresh keeps links until the whole replacement arrives, then removes lost access', async () => {
  const view = mount(keys)
  await flush()
  for (const request of [...requests]) request.release(answer(request.keys))
  await flush()
  mocks.access('refresh')
  await flush()
  expect(view.pills().every(pill => pill.tag === 'a')).toBe(true)
  // Refresh batches are sequential and replacement is atomic.
  for (let i = 3; i < 6; i++) {
    expect(requests).toHaveLength(i + 1)
    requests[i]!.release(answer(requests[i]!.keys.filter(key => key !== 'AEON-1')))
    await flush()
    if (i < 5) expect(view.pills().every(pill => pill.tag === 'a')).toBe(true)
  }
  expect(view.pills().filter(pill => pill.tag === 'span').map(pill => pill.key)).toEqual(['AEON-1'])
  expect(requests).toHaveLength(6)
  expect(links.ticketRef('AEON-1')).toBeNull()
})

it('reset drops earlier answers and a stale response cannot restore them', async () => {
  const view = mount(['AEON-1'])
  await flush()
  const stale = requests[0]!
  mocks.access('reset')
  links.wantTicketKey('AEON-1')
  await flush()
  requests[1]!.release({ items: [] })
  await flush()
  stale.release(answer(stale.keys))
  await flush()
  expect(links.ticketRef('AEON-1')).toBeNull()
  expect(view.pills()[0]!.tag).toBe('span')
})

it('unmount trims to 200 off-screen answers and access refresh without links forgets them', async () => {
  const view = mount(keys)
  await flush()
  for (const request of [...requests]) request.release(answer(request.keys))
  await flush()
  view.app.unmount()
  apps.splice(apps.indexOf(view.app), 1)
  expect(keys.filter(key => links.ticketRef(key) !== undefined)).toEqual(keys.slice(50))
  mocks.access('refresh')
  expect(keys.every(key => links.ticketRef(key) === undefined)).toBe(true)
  expect(requests).toHaveLength(3)
})

it('duplicate normalized keys stay pinned until the last link leaves, including prop changes', async () => {
  const list = mount(keys), duplicates = mount([' aeon-1 ', 'AEON-1'])
  await flush()
  for (const request of [...requests]) request.release(answer(request.keys))
  await flush()
  list.app.unmount()
  apps.splice(apps.indexOf(list.app), 1)
  mocks.lookup.mockImplementation(async (keys: string[]) => answer(keys))
  const offscreen = Array.from({ length: 250 }, (_, i) => `AEON-${i + 1001}`)
  await links.resolveTicketKeys(offscreen)
  expect(links.ticketRef('AEON-1')?.key).toBe('AEON-1')
  expect(offscreen.filter(key => links.ticketRef(key) !== undefined)).toEqual(offscreen.slice(50))
  duplicates.shown.value = ['AEON-1']
  await flush()
  expect(duplicates.pills()[0]!.tag).toBe('a')
  duplicates.shown.value = ['AEON-2']
  await flush()
  expect(links.ticketRef('AEON-1')).toBeUndefined()
  expect(duplicates.pills()[0]).toMatchObject({ key: 'AEON-2', tag: 'a' })
  await links.resolveTicketKeys(['AEON-2000'])
  expect(links.ticketRef('AEON-2')?.key).toBe('AEON-2')
})
