// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { createRenderer, h, nextTick, reactive, type Component } from 'vue'
import * as Vue from 'vue'
import * as Avatar from '../src/lib/avatar'
import * as Variants from '../src/lib/indicatorVariants'
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
    if (id === '../../lib/indicatorVariants') return Variants
    if (id === './parts/RobotExpression.vue') return component('parts/RobotExpression')
    if (id === './parts/IndicatorRing.vue') return component('parts/IndicatorRing')
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

it.each(['Robot1', 'Robot2', 'Robot3', 'Robot4', 'Robot5', 'Sprite'])('%s has distinct nonworking expressions and never smiles during a problem', async name => {
  const props = reactive({ state: 'problem', pulse: 0, seed: 'test', lead: true })
  const root = node('root')
  const art = component(name)
  const app = renderer.createApp({ render: () => h(art, props) })
  app.mount(root)
  try {
    const shapes = new Set<string>()
    const geometry = (n: Node): string => JSON.stringify([n.tag, n.props.d, n.props.cx, n.props.cy, n.props.r, n.props.rx, n.props.ry, n.children.map(geometry)])
    for (const state of ['problem', 'unresponsive', 'waiting', 'awaiting', 'idle', 'stopped']) {
      props.state = state
      await nextTick()
      expect(count(root, 'smile')).toBe(0)
      expect(count(root, 'happy')).toBe(0)
      expect(count(root, 'state-expression')).toBe(1)
      if (state === 'problem') expect(count(root, 'frown')).toBe(1)
      shapes.add(geometry(root))
    }
    expect(shapes.size).toBe(6)
  } finally { app.unmount() }
})

// AEON-242: ring modes and inner-artwork size are layers, never a scaled component.
const all = ['Pulse', 'Robot1', 'Robot2', 'Robot3', 'Robot4', 'Robot5', 'Orbit', 'Quill', 'Sprite'].map(name => [name, component(name)] as const)
const ringParts = ['rim', 'ring-track', 'ring-sweep', 'track', 'ticks', 'sweep', 'orbit-track', 'trail', 'point']
const ringCount = (root: Node) => ringParts.reduce((total, part) => total + count(root, part), 0)
function find(root: Node, className: string): Node | undefined {
  if (String(root.props.class ?? '').split(' ').includes(className)) return root
  for (const child of root.children) { const found = find(child, className); if (found) return found }
}
const innerArt = (root: Node) => find(root, 'inner-art') ?? find(root, 'robot') ?? find(root, 'bot')!
function mount(art: Component, props: Record<string, unknown>) {
  const root = node('root')
  const app = renderer.createApp({ render: () => h(art, props) })
  app.mount(root)
  return { root, app }
}

it.each(all)('%s: off removes only the ring; still keeps it without a moving layer; moving adds one', async (name, art) => {
  const props = reactive<Record<string, unknown>>({ state: 'working', pulse: 0, seed: 'ring', lead: true, ring: 'off' })
  const { root, app } = mount(art, props)
  try {
    for (const state of ['working', 'waiting', 'problem', 'stale']) {
      props.state = state
      props.ring = 'off'
      await nextTick()
      if (name === 'Robot5') expect(count(root, 'ring-off')).toBe(1)
      else expect(ringCount(root)).toBe(0)
      expect(count(root, 'agent-state-mark')).toBe(1)
      if (!['Pulse', 'Orbit', 'Quill'].includes(name) && state !== 'working') expect(count(root, 'state-expression')).toBe(1)
      props.ring = 'still'
      await nextTick()
      expect(count(root, 'ring-moving')).toBe(0)
      expect(count(root, 'ring-off')).toBe(0)
      if (name !== 'Robot5') expect(ringCount(root)).toBeGreaterThan(0)
      if (!['Pulse', 'Robot1'].includes(name)) expect(count(root, 'ring-sweep')).toBe(0)
      expect(count(root, 'agent-state-mark')).toBe(1)
      props.ring = 'moving'
      await nextTick()
      expect(count(root, 'agent-state-mark')).toBe(1)
      if (state !== 'stale') expect(count(root, 'ring-moving') + count(root, 'ring-sweep')).toBeGreaterThan(0)
    }
  } finally { app.unmount() }
})

it.each(all)('%s: inner size scales only the artwork layer and ignores invalid values', async (name, art) => {
  const props = reactive<Record<string, unknown>>({ state: 'working', pulse: 0, seed: 'size', lead: true, artScale: 1 })
  const { root, app } = mount(art, props)
  try {
    const outer = { ...root.children[0]!.props }
    const inner = () => innerArt(root)
    if (name === 'Robot5') expect(inner().props.width).toBe(29)
    else expect(inner().props.style).toBeFalsy()
    for (const [value, scale] of [[1.2, 1.2], [0.4, 0.4], [NaN, 1], [Infinity, 1], [0, 0.312], [40, 1.9], ['1.2', 1]] as const) {
      props.artScale = value
      await nextTick()
      if (name === 'Robot5') expect(inner().props.width).toBe(Math.round(26 * 1.1 * scale))
      else if (scale === 1) expect(inner().props.style).toBeFalsy()
      else expect(inner().props.style).toMatchObject({ scale: String(scale), transformOrigin: '16px 16px', '--art-stroke': (1 / Math.sqrt(scale)).toFixed(3) })
      if (name === 'Robot5') expect(inner().props.style).toEqual(scale === 1 ? undefined : { '--art-stroke': (1 / Math.sqrt(scale)).toFixed(3) })
      const { style: _style, ...sameOuter } = root.children[0]!.props
      const { style: _before, ...before } = outer
      expect(sameOuter).toEqual(before)
      const mark = find(root, 'agent-state-mark')!
      expect(mark.props).toMatchObject({ width: name === 'Robot5' ? 12 : 11 })
      expect(find(inner(), 'agent-state-mark')).toBeUndefined()
    }
  } finally { app.unmount() }
})
