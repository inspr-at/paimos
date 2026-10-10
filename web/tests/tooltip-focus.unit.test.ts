// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'

class ElementStub {
  dataset: Record<string, string> = {}
  isConnected = true
  top = 200
  offsetWidth = 200
  offsetHeight = 40
  clientHeight = 40
  scrollHeight = 40
  getAttribute() { return null }
  hasAttribute(name: string) { return name === 'data-clip-tip' && !!this.dataset.tip }
  matches(selector: string) { return selector === ':focus-visible' }
  closest(selector: string) { return selector.includes('[data-tip]') && this.dataset.tip ? this : null }
  contains(node: unknown) { return node === this }
  showPopover() {}
  getBoundingClientRect() { return { left: 20, top: this.top, width: 200, height: 30, bottom: this.top + 30, right: 220 } }
}

afterEach(() => vi.unstubAllGlobals())

async function focusedTooltip() {
  const listeners = new Map<string, (event: unknown) => void>()
  const source = new ElementStub()
  source.dataset.tip = 'Vollständiger Entscheidungskontext für die Rolle'
  const document = { body: new ElementStub(), activeElement: source,
    addEventListener: (name: string, callback: (event: unknown) => void) => listeners.set(name, callback),
    removeEventListener: (name: string) => listeners.delete(name),
  }
  for (const [name, value] of Object.entries({ Element: ElementStub, HTMLElement: ElementStub, Node: ElementStub,
    document, window: document, innerWidth: 1024, innerHeight: 768,
    MutationObserver: class { observe() {} disconnect() {} },
  })) vi.stubGlobal(name, value)
  const mounted: (() => void)[] = [], cleanup: (() => void)[] = []
  const { descriptor } = parse(readFileSync(new URL('../src/components/TooltipHost.vue', import.meta.url), 'utf8'))
  const { content } = compileScript(descriptor, { id: 'focused-tooltip-test' })
  const { outputText } = ts.transpileModule(content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  type State = { text: Vue.Ref<string>; y: Vue.Ref<number>; tip: Vue.Ref<ElementStub> }
  const exports: { default?: { setup: (props: unknown, context: unknown) => State } } = {}
  new Function('require', 'exports', outputText)((id: string) => {
    if (id !== 'vue') throw new Error(`Unexpected tooltip dependency: ${id}`)
    return { ...Vue, onMounted: (fn: () => void) => mounted.push(fn), onBeforeUnmount: (fn: () => void) => cleanup.push(fn) }
  }, exports)
  const state = exports.default!.setup({}, { expose() {} })
  state.tip.value = new ElementStub()
  mounted.forEach(fn => fn())
  listeners.get('focusin')!({ target: source })
  await Vue.nextTick()
  expect(state.text.value).toBe(source.dataset.tip)
  return { state, source, document, listeners, cleanup: () => cleanup.forEach(fn => fn()) }
}

it('pointer movement keeps the complete label owned by keyboard focus', async () => {
  const fixture = await focusedTooltip()
  try {
    fixture.listeners.get('pointerover')!({ target: fixture.document.body, pointerType: 'mouse' })
    await Vue.nextTick()
    expect(fixture.document.activeElement).toBe(fixture.source)
    expect(fixture.state.text.value).toBe(fixture.source.dataset.tip)
  } finally { fixture.cleanup() }
})

it('a nested scroll retains the focused label and repositions its disclosure', async () => {
  const fixture = await focusedTooltip()
  try {
    const before = fixture.state.y.value
    fixture.source.top -= 32
    fixture.listeners.get('scroll')!({ target: fixture.document.body })
    await Vue.nextTick()
    expect(fixture.document.activeElement).toBe(fixture.source)
    expect(fixture.state.text.value).toBe(fixture.source.dataset.tip)
    expect(fixture.state.y.value).toBe(before - 32)
  } finally { fixture.cleanup() }
})
