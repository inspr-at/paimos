// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import * as Vue from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

type Node = { tag: string; props: Record<string, unknown>; children: Node[]; parent: Node | null }
const node = (tag: string): Node => ({ tag, props: {}, children: [], parent: null })
function detach(el: Node) {
  if (el.parent) el.parent.children.splice(el.parent.children.indexOf(el), 1)
  el.parent = null
}
const renderer = Vue.createRenderer<Node, Node>({
  createElement: node, createText: () => node('#text'), createComment: () => node('#comment'),
  setText: () => {}, setElementText: () => {},
  parentNode: el => el.parent, nextSibling: el => el.parent?.children[el.parent.children.indexOf(el) + 1] ?? null,
  patchProp: (el, key, _old, value) => { el.props[key] = value }, remove: detach,
  insert: (child, parent, anchor) => {
    detach(child); child.parent = parent
    const i = anchor ? parent.children.indexOf(anchor) : -1
    if (i < 0) parent.children.push(child); else parent.children.splice(i, 0, child)
  },
})
const flatten = (el: Node): Node[] => [el, ...el.children.flatMap(flatten)]
const apps: Vue.App[] = []
beforeEach(() => vi.useFakeTimers())
afterEach(() => {
  for (const app of apps.splice(0)) app.unmount()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

function mount(reduced = false) {
  // Keep matches and event delivery independent: Chromium's matches getter can
  // update its cache before MediaFeaturesChanged checks whether to send change.
  const media = { matches: reduced, addEventListener: vi.fn(), removeEventListener: vi.fn() }
  vi.stubGlobal('window', { matchMedia: () => media })
  vi.stubGlobal('document', { hidden: false })
  const stub = { render: () => Vue.h('svg') }
  const modules: Record<string, unknown> = { vue: Vue, '../AppIcon.vue': { __esModule: true, default: stub }, './StatViz.vue': { __esModule: true, default: stub } }
  const source = readFileSync(new URL('../src/components/releases/ReleaseStatCard.vue', import.meta.url), 'utf8')
  const { descriptor } = parse(source)
  const { content } = compileScript(descriptor, { id: 'release-stat-card-test', inlineTemplate: true })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: { default?: Vue.Component } = {}
  new Function('require', 'exports', outputText)((id: string) => {
    if (!(id in modules)) throw new Error(`Unexpected dependency ${id}`)
    return modules[id]
  }, exports)
  const stats = ['perweek', 'features'].map((key, i) => ({ key, label: `Stat ${i + 1}`, value: '1', sub: 'per week', chips: [], viz: {} }))
  const app = renderer.createApp(exports.default!, { stats })
  const root = node('root'); apps.push(app); app.mount(root)
  const button = () => flatten(root).find(el => el.tag === 'button' && /automatic rotation/.test(String(el.props['aria-label'])))!
  const slide = () => flatten(root).find(el => el.props.role === 'group')!
  const click = async () => { (button().props.onClick as () => void)(); await Vue.nextTick() }
  return { media, button, slide, click }
}

it('a timer observes reduced motion before a deferred change event, updates the control and stops rotation', async () => {
  const { media, button, slide, click } = mount()
  expect(button().props['aria-label']).toBe('Pause automatic rotation')
  vi.advanceTimersByTime(7100)
  await Vue.nextTick()
  expect(slide().props['aria-label']).toBe('2 of 2: Stat 2')
  media.matches = true
  vi.advanceTimersByTime(100)
  await Vue.nextTick()
  expect(button().props['aria-label']).toBe('Resume automatic rotation')
  expect(slide().props['aria-live']).toBe('polite')
  vi.advanceTimersByTime(15_000)
  await Vue.nextTick()
  expect(slide().props['aria-label']).toBe('2 of 2: Stat 2')
  // Disabling the preference never restarts a stopped carousel implicitly.
  media.matches = false
  vi.advanceTimersByTime(15_000)
  await Vue.nextTick()
  expect(slide().props['aria-label']).toBe('2 of 2: Stat 2')
  await click()
  expect(button().props['aria-label']).toBe('Pause automatic rotation')
  vi.advanceTimersByTime(7100)
  await Vue.nextTick()
  expect(slide().props['aria-label']).toBe('1 of 2: Stat 1')
})

it('Resume uses the live preference even when its change event has not arrived', async () => {
  const { media, button, slide, click } = mount(true)
  await click()
  expect(button().props['aria-label']).toBe('Resume automatic rotation')
  media.matches = false
  await click()
  expect(button().props['aria-label']).toBe('Pause automatic rotation')
  expect(slide().props['aria-live']).toBe('off')
  vi.advanceTimersByTime(7100)
  await Vue.nextTick()
  expect(slide().props['aria-label']).toBe('2 of 2: Stat 2')
})
