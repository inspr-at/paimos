// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { createRenderer, h, nextTick, reactive, type Component } from 'vue'
import * as Vue from 'vue'
import * as Avatar from '../src/lib/avatar'
import { readFileSync } from 'node:fs'
import { parse, compileScript } from '@vue/compiler-sfc'
import ts from 'typescript'

// Node-mode Vite compiles SFCs for SSR. Compile their client render functions
// explicitly so the real watchers and SVG tree run in this in-memory host.
function component(filename: string): Component {
  const source = readFileSync(new URL(`../src/components/indicators/${filename}.vue`, import.meta.url), 'utf8')
  const { descriptor } = parse(source)
  const { content } = compileScript(descriptor, { id: filename, inlineTemplate: true, templateOptions: { compilerOptions: { hoistStatic: false } } })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: { default?: Component } = {}
  new Function('require', 'exports', outputText)((id: string) => {
    if (id === '../../lib/avatar') return Avatar
    if (id === './AgentStateMark.vue') return component('AgentStateMark')
    if (id.endsWith('.css')) return {}
    if (id !== 'vue') throw new Error(`Unexpected renderer dependency: ${id}`)
    return Vue
  }, exports)
  return exports.default!
}
const Pulse = component('Pulse'), Robot1 = component('Robot1'), Robot5 = component('Robot5')

// An in-memory Vue host exercises reactive presentation without a browser or API.
type Node = { tag: string; props: Record<string, unknown>; children: Node[]; parent: Node | null }
const node = (tag: string): Node => ({ tag, props: {}, children: [], parent: null })
function detach(child: Node) {
  const siblings = child.parent?.children
  if (siblings) siblings.splice(siblings.indexOf(child), 1)
  child.parent = null
}
const renderer = createRenderer<Node, Node>({
  createElement: tag => node(tag), createText: () => node('#text'), createComment: () => node('#comment'),
  setText() {}, setElementText() {},
  parentNode: child => child.parent,
  nextSibling: child => child.parent?.children[(child.parent.children.indexOf(child) ?? 0) + 1] ?? null,
  patchProp: (el, key, _previous, value) => { el.props[key] = value },
  insert: (child, parent, anchor) => {
    detach(child)
    child.parent = parent
    const index = anchor ? parent.children.indexOf(anchor) : -1
    if (index < 0) parent.children.push(child)
    else parent.children.splice(index, 0, child)
  },
  remove: detach,
})
function count(root: Node, className: string): number {
  return Number(String(root.props.class ?? '').split(' ').includes(className)) + root.children.reduce((total, child) => total + count(child, className), 0)
}
afterEach(() => vi.useRealTimers())

it.each<[string, Component]>([['Pulse', Pulse], ['Robot 1', Robot1], ['Robot 5', Robot5]])('%s uses advancing evidence only, including across stale state and counter replay', async (_name, component) => {
  vi.useFakeTimers()
  const props = reactive({ state: 'working' as 'working' | 'waiting' | 'stale', pulse: 9, seed: 'agent-one', lead: true })
  const root = node('root')
  const app = renderer.createApp({ render: () => h(component, props) })
  app.mount(root)
  try {
    expect(root.children[0]?.props).toMatchObject({ 'aria-hidden': 'true' })
    if (component === Robot5) expect(root.children[0]?.props.style).toMatchObject({ '--size': '26px' })
    else expect(root.children[0]?.props).toMatchObject({ width: 26, height: 26, focusable: 'false' })
    expect(count(root, 'glint')).toBe(0)
    props.pulse = 10
    await nextTick()
    expect(count(root, 'glint')).toBe(1)
    await vi.advanceTimersByTimeAsync(600)
    expect(count(root, 'glint')).toBe(0)
    props.state = 'waiting'
    await nextTick()
    expect(count(root, 'clock')).toBe(1)
    expect(count(root, 'glint')).toBe(0)
    props.pulse = 9
    await nextTick()
    props.pulse = 10
    await nextTick()
    expect(count(root, 'glint')).toBe(0)
    props.pulse = 11
    await nextTick()
    expect(count(root, 'glint')).toBe(0)
    props.state = 'stale'
    await nextTick()
    expect(count(root, 'glint')).toBe(0)
    expect(count(root, 'clock')).toBe(0)
    props.pulse = 12
    await nextTick()
    props.state = 'working'
    await nextTick()
    expect(count(root, 'glint')).toBe(0)
    props.pulse = 13
    await nextTick()
    expect(count(root, 'glint')).toBe(1)
  } finally { app.unmount() }
  expect(vi.getTimerCount()).toBe(0)
})
