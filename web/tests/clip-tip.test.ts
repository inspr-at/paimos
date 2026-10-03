// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { isClipped, vClipTip } from '../src/directives/clipTip.ts'

class TextBox {
  clientWidth = 100; scrollWidth = 100; clientHeight = 20; scrollHeight = 20
  textContent = 'Vollständiger Name'; owner: object | null = null
  attributes = new Map<string, string>()
  getAttribute(name: string) { return this.attributes.get(name) ?? null }
  setAttribute(name: string, value: string) { this.attributes.set(name, value) }
  removeAttribute(name: string) { this.attributes.delete(name) }
  closest() { return this.owner }
  get element() { return this as unknown as HTMLElement }
}
const observers: Observer[] = []
class Observer {
  disconnected = false
  notify: () => void
  constructor(notify: () => void) { this.notify = notify; observers.push(this) }
  observe() {}
  disconnect() { this.disconnected = true }
}
const frames = new Map<number, FrameRequestCallback>()
let sequence = 0
const original = { ResizeObserver: globalThis.ResizeObserver, MutationObserver: globalThis.MutationObserver, requestAnimationFrame: globalThis.requestAnimationFrame, cancelAnimationFrame: globalThis.cancelAnimationFrame }
const hooks = vClipTip as {
  mounted: (el: HTMLElement, binding: { value?: string }) => void
  updated: (el: HTMLElement, binding: { value?: string }) => void
  beforeUnmount: (el: HTMLElement) => void
}
beforeEach(() => {
  observers.length = 0; frames.clear()
  Object.assign(globalThis, { ResizeObserver: Observer, MutationObserver: Observer,
    requestAnimationFrame: (callback: FrameRequestCallback) => { const id = ++sequence; frames.set(id, callback); return id },
    cancelAnimationFrame: (id: number) => frames.delete(id),
  })
})
afterEach(() => Object.assign(globalThis, original))
function flush() { for (const [id, callback] of frames) { frames.delete(id); callback(0) } }

test('measures horizontal ellipsis, line clamps, hidden boxes and rounding tolerance', () => {
  const box = new TextBox()
  assert.equal(isClipped(box.element), false)
  box.scrollWidth = 101; assert.equal(isClipped(box.element), false)
  box.scrollWidth = 150; assert.equal(isClipped(box.element), true)
  box.scrollWidth = 100; box.scrollHeight = 60; assert.equal(isClipped(box.element), true)
  box.clientWidth = 0; assert.equal(isClipped(box.element), false)
})

test('resize and text mutations add, update and remove a standalone name disclosure', () => {
  const box = new TextBox()
  hooks.mounted(box.element, {})
  assert.equal(box.getAttribute('data-tip'), null)
  assert.equal(box.getAttribute('tabindex'), null)
  box.scrollWidth = 200; observers[0]!.notify(); flush()
  assert.equal(box.getAttribute('data-tip'), box.textContent)
  assert.equal(box.getAttribute('tabindex'), '0')
  box.textContent = 'Neuer vollständiger Name'; observers[1]!.notify(); flush()
  assert.equal(box.getAttribute('data-tip'), box.textContent)
  box.scrollWidth = 100; observers[0]!.notify(); flush()
  assert.equal(box.getAttribute('data-tip'), null)
  assert.equal(box.getAttribute('data-clip-tip'), null)
  assert.equal(box.getAttribute('tabindex'), null)
  hooks.beforeUnmount(box.element)
})

test('binding changes follow reused rows, retaining action focus and original attributes', () => {
  const box = new TextBox(); box.owner = {}; box.scrollHeight = 60
  box.setAttribute('data-tip', 'Existing context'); box.setAttribute('tabindex', '-1')
  hooks.mounted(box.element, { value: 'Name A' })
  assert.equal(box.getAttribute('data-tip'), 'Name A')
  assert.equal(box.getAttribute('tabindex'), '-1')
  hooks.updated(box.element, { value: 'Name B' })
  assert.equal(box.getAttribute('data-tip'), 'Name B')
  observers[0]!.notify(); observers[1]!.notify()
  assert.equal(frames.size, 1)
  hooks.beforeUnmount(box.element)
  assert.equal(frames.size, 0)
  assert.equal(observers.every(observer => observer.disconnected), true)
  assert.equal(box.getAttribute('data-tip'), 'Existing context')
  assert.equal(box.getAttribute('tabindex'), '-1')
})

test('a clipped name inside an action does not add another Tab stop', () => {
  const box = new TextBox(); box.owner = {}; box.scrollWidth = 200
  hooks.mounted(box.element, {})
  assert.equal(box.getAttribute('data-tip'), box.textContent)
  assert.equal(box.getAttribute('tabindex'), null)
  hooks.beforeUnmount(box.element)
})
