// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1327: a ctrl/cmd press on a ticket link opens one real tab even when the
// row moves and the click never lands. A plain click still opens the peek.
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { afterEach, expect, it, vi } from 'vitest'

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

const { descriptor } = parse(readFileSync(new URL('../src/components/TicketPeekLink.vue', import.meta.url), 'utf8'))
const { content } = compileScript(descriptor, { id: 'ticket-peek-link-test', inlineTemplate: true })
const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
const peekKey = Symbol('ticket-peek')
const push = vi.fn()
const modules: Record<string, unknown> = {
  vue: Vue,
  'vue-router': { useRouter: () => ({ push }) },
  '../lib/ticketPeek': { TICKET_PEEK: peekKey },
  '../lib/ticketLinks': { normalKey: (key: string) => key.trim().toUpperCase() },
}
const compiled: { default?: Vue.Component } = {}
new Function('require', 'exports', outputText)((id: string) => {
  if (!(id in modules)) throw new Error(`Unexpected dependency ${id}`)
  return modules[id]
}, compiled)

const apps: Vue.App[] = []
const opened: string[] = []
const peek = { open: vi.fn(), openKey: Vue.ref<string | null>(null) }

function mount() {
  const root = node('root')
  const app = renderer.createApp({
    render: () => Vue.h(compiled.default!, { ticketKey: 'pharos-11', href: '/p/PHAROS/PHAROS-11', tip: 'Connect Hetzner' }),
  })
  app.provide(peekKey, peek)
  apps.push(app)
  app.mount(root)
  const link = root.children[0]
  if (!link || link.tag !== 'a') throw new Error(`ticket link rendered ${link?.tag ?? 'nothing'}`)
  return link
}

function handler(link: Node, name: string) {
  const fn = link.props[name]
  if (typeof fn !== 'function') throw new Error(`missing ${name}; props: ${Object.keys(link.props).join(',')}`)
  return fn as (event: Record<string, unknown>) => void
}

function pointer(partial: Record<string, unknown> = {}) {
  const event: Record<string, unknown> = {
    button: 0, metaKey: false, ctrlKey: true, shiftKey: false, altKey: false, defaultPrevented: false,
    clientX: 10, clientY: 20, pointerId: 1, currentTarget: null,
    preventDefault() { event.defaultPrevented = true },
    ...partial,
  }
  return event
}

const flush = () => Promise.resolve()

afterEach(() => {
  for (const app of apps.splice(0)) app.unmount()
  opened.length = 0
  peek.open.mockClear()
  peek.openKey.value = null
  push.mockClear()
})

it('a ctrl or cmd press opens one ticket tab when the click never lands', async () => {
  const original = globalThis.window
  globalThis.window = { open: (url: string) => { opened.push(String(url)); return null } } as unknown as Window & typeof globalThis
  try {
    const link = mount()
    const down = pointer()
    down.currentTarget = link
    handler(link, 'onPointerdown')(down)
    handler(link, 'onPointerup')(pointer())
    await flush()
    expect(opened).toEqual(['/p/PHAROS/PHAROS-11'])
    expect(peek.open).not.toHaveBeenCalled()
    expect(push).not.toHaveBeenCalled()
  } finally { globalThis.window = original }
})

it('a ctrl or cmd click opens one ticket tab and does not open the peek', async () => {
  const original = globalThis.window
  globalThis.window = { open: (url: string) => { opened.push(String(url)); return null } } as unknown as Window & typeof globalThis
  try {
    const link = mount()
    const down = pointer({ metaKey: true, ctrlKey: false })
    down.currentTarget = link
    handler(link, 'onPointerdown')(down)
    handler(link, 'onPointerup')(pointer({ metaKey: true, ctrlKey: false }))
    const click = pointer({ metaKey: true, ctrlKey: false })
    handler(link, 'onClick')(click)
    await flush()
    expect(click.defaultPrevented).toBe(true)
    expect(opened).toEqual(['/p/PHAROS/PHAROS-11'])
    expect(peek.open).not.toHaveBeenCalled()
  } finally { globalThis.window = original }
})

it('a plain click opens the peek and leaves the tab closed', () => {
  const original = globalThis.window
  globalThis.window = { open: (url: string) => { opened.push(String(url)); return null } } as unknown as Window & typeof globalThis
  try {
    const link = mount()
    const click = pointer({ ctrlKey: false })
    handler(link, 'onClick')(click)
    expect(click.defaultPrevented).toBe(true)
    expect(peek.open).toHaveBeenCalledTimes(1)
    expect(peek.open.mock.calls[0][0]).toBe('pharos-11')
    expect(opened).toEqual([])
    expect(push).not.toHaveBeenCalled()
  } finally { globalThis.window = original }
})

it('a drag away from a modified press does not open a tab', async () => {
  const original = globalThis.window
  globalThis.window = { open: (url: string) => { opened.push(String(url)); return null } } as unknown as Window & typeof globalThis
  try {
    const link = mount()
    const down = pointer()
    down.currentTarget = link
    handler(link, 'onPointerdown')(down)
    handler(link, 'onPointerup')(pointer({ clientX: 40, clientY: 20 }))
    await flush()
    expect(opened).toEqual([])
    expect(peek.open).not.toHaveBeenCalled()
  } finally { globalThis.window = original }
})
