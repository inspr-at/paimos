// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { vClipTip } from '../src/lib/clipTip.ts'

test('clip tips follow measured width/line clamps, text changes, visibility and cleanup', () => {
  const observers: { callback: () => void; observed: unknown; disconnected: boolean; options?: unknown }[] = []
  class Observer {
    callback: () => void
    observed: unknown
    disconnected = false
    options?: unknown
    constructor(callback: () => void) { this.callback = callback; observers.push(this) }
    observe(el: unknown, options?: unknown) { this.observed = el; this.options = options }
    disconnect() { this.disconnected = true }
  }
  const resize = globalThis.ResizeObserver, mutation = globalThis.MutationObserver
  globalThis.ResizeObserver = Observer as unknown as typeof ResizeObserver
  globalThis.MutationObserver = Observer as unknown as typeof MutationObserver
  try {
    const element = { clientWidth: 100, scrollWidth: 100, clientHeight: 20, scrollHeight: 20, textContent: ' Kurzer Name ', dataset: {} as Record<string, string> }
    const el = element as unknown as HTMLElement
    const results: boolean[] = []
    const value = { onClip: (clipped: boolean) => results.push(clipped) }
    vClipTip.mounted!(el, { value } as never, null as never, null as never)
    assert.equal(element.dataset.tip, undefined, 'unclipped text needs no tip')
    assert.equal(observers.length, 2)
    assert.ok(observers.every(observer => observer.observed === element))
    assert.deepEqual(observers[1]!.options, { childList: true, characterData: true, subtree: true })
    element.scrollWidth = 220
    observers[0]!.callback()
    assert.equal(element.dataset.tip, 'Kurzer Name')
    element.textContent = ' Vollständiger neuer Name '
    observers[1]!.callback()
    assert.equal(element.dataset.tip, 'Vollständiger neuer Name', 'mutation updates the reveal')
    element.scrollWidth = 100
    element.scrollHeight = 60
    observers[0]!.callback()
    assert.equal(element.dataset.tip, 'Vollständiger neuer Name', 'vertical line clamps count as clipping')
    vClipTip.updated!(el, { value: 'Explicit full text' } as never, null as never, null as never)
    assert.equal(element.dataset.tip, 'Explicit full text')
    element.clientHeight = 0
    observers[0]!.callback()
    assert.equal(element.dataset.tip, undefined, 'hidden elements have no tip')
    element.clientHeight = 60
    vClipTip.updated!(el, { value } as never, null as never, null as never)
    assert.equal(element.dataset.tip, undefined, 'expanded text clears its tip')
    assert.deepEqual(results, [false, true, true, true, false])
    vClipTip.unmounted!(el, null as never, null as never, null as never)
    assert.ok(observers.every(observer => observer.disconnected))
    assert.equal(element.dataset.tip, undefined)
  } finally {
    globalThis.ResizeObserver = resize
    globalThis.MutationObserver = mutation
  }
})
