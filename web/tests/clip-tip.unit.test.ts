// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { isTextClipped, vClipTip, type ClipTipValue } from '../src/lib/clipTip'

afterEach(() => vi.unstubAllGlobals())

it('detects horizontal ellipsis and vertical clamps, excluding hidden and fitting text', () => {
  const box = { clientWidth: 100, clientHeight: 40, scrollWidth: 100, scrollHeight: 40 }
  expect(isTextClipped(box)).toBe(false)
  expect(isTextClipped({ ...box, scrollWidth: 160 })).toBe(true)
  expect(isTextClipped({ ...box, scrollHeight: 120 })).toBe(true)
  expect(isTextClipped({ ...box, clientHeight: 0, scrollHeight: 120 })).toBe(false)
  expect(isTextClipped({ ...box, scrollWidth: 101 })).toBe(false)
})

function fixture(initial: Record<string, string> = {}) {
  let resize = () => {}, fontReady!: () => void
  const disconnect = vi.fn(), observe = vi.fn()
  vi.stubGlobal('ResizeObserver', class { constructor(callback: () => void) { resize = callback } observe = observe; disconnect = disconnect })
  vi.stubGlobal('document', { fonts: { ready: new Promise<void>(resolve => { fontReady = resolve }) } })
  const attrs = new Map(Object.entries(initial))
  const element = {
    clientWidth: 100, clientHeight: 40, scrollWidth: 100, scrollHeight: 80, textContent: 'The full question',
    getAttribute: (name: string) => attrs.get(name) ?? null,
    setAttribute: (name: string, value: string) => { attrs.set(name, value) },
    removeAttribute: (name: string) => { attrs.delete(name) },
    matches: () => attrs.has('tabindex'),
  }
  const mounted = vClipTip.mounted as (element: HTMLElement, binding: { value: ClipTipValue }) => void
  const updated = vClipTip.updated as typeof mounted
  const unmounted = vClipTip.unmounted as (element: HTMLElement) => void
  return { attrs, element, disconnect, fontReady, resize: () => resize(),
    mount: (value?: ClipTipValue) => mounted(element as unknown as HTMLElement, { value }),
    update: (value?: ClipTipValue) => updated(element as unknown as HTMLElement, { value }),
    unmount: () => unmounted(element as unknown as HTMLElement) }
}

it('reveals full text to pointer and keyboard users only while clipped, and remeasures on resize and text updates', () => {
  const f = fixture(), onClip = vi.fn()
  f.mount({ onClip })
  expect(f.attrs.get('data-tip')).toBe('The full question')
  expect(f.attrs.get('tabindex')).toBe('0')
  expect(onClip).toHaveBeenLastCalledWith(true)
  f.element.scrollHeight = 40; f.resize()
  expect(f.attrs.has('data-tip')).toBe(false)
  expect(f.attrs.has('tabindex')).toBe(false)
  expect(onClip).toHaveBeenLastCalledWith(false)
  f.element.scrollHeight = 100; f.update({ text: 'A replacement question', onClip })
  expect(f.attrs.get('data-tip')).toBe('A replacement question')
  expect(onClip).toHaveBeenLastCalledWith(true)
})

it('preserves existing focus and tooltip attributes when text fits', () => {
  const f = fixture({ tabindex: '-1', 'data-tip': 'Existing explanation' })
  f.mount('Full text')
  expect(f.attrs.get('data-tip')).toBe('Full text')
  expect(f.attrs.get('tabindex')).toBe('-1')
  f.element.scrollHeight = 40; f.resize()
  expect(f.attrs.get('data-tip')).toBe('Existing explanation')
  expect(f.attrs.get('tabindex')).toBe('-1')
})

it('remeasures after fonts load and disconnects without reacting to late work after unmount', async () => {
  const f = fixture(), onClip = vi.fn()
  f.element.scrollHeight = 40; f.mount({ onClip })
  f.element.scrollHeight = 100; f.fontReady(); await Promise.resolve()
  expect(onClip).toHaveBeenLastCalledWith(true)
  const calls = onClip.mock.calls.length
  f.unmount(); f.resize()
  expect(f.disconnect).toHaveBeenCalledOnce()
  expect(onClip).toHaveBeenCalledTimes(calls)
})
