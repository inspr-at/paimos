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

afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

async function focusedTooltip() {
  const listeners = new Map<string, (event: unknown) => void>()
  const observers: (() => void)[] = []
  const source = new ElementStub()
  source.dataset.tip = 'Vollständiger Entscheidungskontext für die Rolle'
  const document = { body: new ElementStub(), activeElement: source,
    addEventListener: (name: string, callback: (event: unknown) => void) => listeners.set(name, callback),
    removeEventListener: (name: string) => listeners.delete(name),
  }
  for (const [name, value] of Object.entries({ Element: ElementStub, HTMLElement: ElementStub, Node: ElementStub,
    document, window: document, innerWidth: 1024, innerHeight: 768,
    MutationObserver: class { constructor(callback: () => void) { observers.push(callback) } observe() {} disconnect() {} },
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
  return { state, source, document, listeners, observers, cleanup: () => cleanup.forEach(fn => fn()) }
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

it.each(['nine pixels', 'scroll', 'pointer cancellation', 'changed record', 'removed record', 'second contact'] as const)('P02 a pending touch disclosure is cancelled by %s', async reason => {
  vi.useFakeTimers()
  const fixture = await focusedTooltip()
  const pointer = (extra: Record<string, unknown> = {}) => ({ target: fixture.source, pointerType: 'touch', pointerId: 42,
    isPrimary: true, button: 0, clientX: 100, clientY: 100, stopPropagation: vi.fn(), ...extra })
  try {
    fixture.listeners.get('keydown')!({ key: 'Escape' }); await Vue.nextTick()
    fixture.listeners.get('pointerdown')!(pointer())
    await vi.advanceTimersByTimeAsync(250)
    if (reason === 'nine pixels') fixture.listeners.get('pointermove')!(pointer({ clientX: 109 }))
    else if (reason === 'scroll') fixture.listeners.get('scroll')!({ target: fixture.document.body })
    else if (reason === 'pointer cancellation') fixture.listeners.get('pointercancel')!(pointer())
    else if (reason === 'changed record') fixture.source.dataset.tip = 'A different record'
    else if (reason === 'removed record') fixture.source.isConnected = false
    else fixture.listeners.get('pointerdown')!(pointer({ pointerId: 43, isPrimary: false }))
    await vi.advanceTimersByTimeAsync(500)
    expect(fixture.state.text.value).toBe('')
  } finally { fixture.cleanup() }
  expect(vi.getTimerCount()).toBe(0)
})

it('P02 an eight-pixel hold reveals at 500 ms and suppresses only its own compatibility click', async () => {
  vi.useFakeTimers()
  const fixture = await focusedTooltip(), prevented = vi.fn(), stopped = vi.fn()
  const pointer = (pointerId: number, clientX = 100) => ({ target: fixture.source, pointerType: 'touch', pointerId,
    isPrimary: true, button: 0, clientX, clientY: 100, stopPropagation: vi.fn() })
  try {
    fixture.listeners.get('keydown')!({ key: 'Escape' }); await Vue.nextTick()
    fixture.listeners.get('pointerdown')!(pointer(42))
    fixture.listeners.get('pointermove')!(pointer(42, 108))
    await vi.advanceTimersByTimeAsync(499); expect(fixture.state.text.value).toBe('')
    await vi.advanceTimersByTimeAsync(1); expect(fixture.state.text.value).toBe(fixture.source.dataset.tip)
    fixture.listeners.get('pointerup')!(pointer(42, 108))
    const click = { target: fixture.source, detail: 1, preventDefault: prevented, stopImmediatePropagation: stopped }
    fixture.listeners.get('click')!({ ...click, detail: 0 })
    expect(prevented).not.toHaveBeenCalled()
    fixture.listeners.get('click')!(click)
    expect(prevented).toHaveBeenCalledOnce(); expect(stopped).toHaveBeenCalledOnce()
    fixture.listeners.get('pointerdown')!(pointer(43)); await vi.advanceTimersByTimeAsync(100)
    fixture.listeners.get('pointerup')!(pointer(43)); fixture.listeners.get('click')!(click)
    expect(prevented).toHaveBeenCalledOnce()
  } finally { fixture.cleanup() }
  expect(vi.getTimerCount()).toBe(0)
})

it('P02 focused disclosure follows a changed record and discards a disconnected anchor and late observer', async () => {
  const fixture = await focusedTooltip()
  try {
    fixture.source.dataset.tip = 'A replacement identity with its complete explanation'
    fixture.observers.at(-1)!(); await Vue.nextTick()
    expect(fixture.state.text.value).toBe(fixture.source.dataset.tip)
    fixture.source.isConnected = false
    fixture.observers.at(-1)!(); await Vue.nextTick()
    expect(fixture.state.text.value).toBe('')
    fixture.source.dataset.tip = 'A late callback cannot revive the removed record'
    fixture.observers.at(-1)!(); await Vue.nextTick()
    expect(fixture.state.text.value).toBe('')
  } finally { fixture.cleanup() }
})
