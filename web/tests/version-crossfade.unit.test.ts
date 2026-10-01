// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { renderVersion } from '../src/vendor/calendar-version-display/version.js'
import { duration, hoverOpacity } from '../src/vendor/calendar-version-display/version-interaction.js'
import display from '../src/vendor/calendar-version-display/display.json'
import { attachVersionCrossfade } from '../src/lib/version-reveal'
import { CALENDAR_DISPLAY_SCHEME, copyRenderedVersion } from '../src/lib/version-copy'

// A small DOM host exercises the actual pinned renderer and event helper in
// Node, including the Clipboard API and live reduced-motion changes.
class Element extends EventTarget {
  children: Element[] = []
  dataset: Record<string, string> = {}
  style: Record<string, unknown> = { opacity: '', getPropertyValue: (key: string) => this.style[key] ?? '', setProperty: (key: string, value: string) => { this.style[key] = value }, removeProperty: (key: string) => { delete this.style[key] } }
  attrs = new Map<string, string>()
  className = ''
  classList = { add: (name: string) => { this.className = name } }
  ownerDocument = doc
  private text = ''
  get textContent(): string { return this.children.length ? this.children.map(child => child.textContent).join('') : this.text }
  set textContent(value: string) { this.text = value; this.children = [] }
  setAttribute(key: string, value: string) { this.attrs.set(key, value) }
  getAttribute(key: string) { return this.attrs.get(key) ?? null }
  removeAttribute(key: string) { this.attrs.delete(key) }
  append(...nodes: Element[]) { this.children.push(...nodes) }
  replaceChildren(...nodes: Element[]) { this.children = nodes; this.text = '' }
  querySelector(selector: string): Element | null {
    for (const child of this.children) {
      if (`.${child.className}` === selector) return child
      const nested = child.querySelector(selector)
      if (nested) return nested
    }
    return null
  }
  contains(element: Element) { return element === this || this.children.some(child => child.contains(element)) }
  matches() { return true }
}
const doc = new class extends EventTarget {
  activeElement: Element | null = null
  createElement() { return new Element() }
}()
let media: EventTarget & { matches: boolean }
let view: Window
const written = vi.fn(async (_text: string) => {})
const pointer = (host: Element, type: string, pointerType = 'mouse') => {
  const event = new Event(type)
  Object.assign(event, { pointerType })
  host.dispatchEvent(event)
}
const VERSION = '261001130110.0.0'
beforeEach(() => {
  vi.stubGlobal('HTMLElement', Element)
  vi.stubGlobal('document', doc)
  vi.stubGlobal('MutationObserver', class { observe() {} disconnect() {} })
  doc.activeElement = null
  media = Object.assign(new EventTarget(), { matches: false })
  view = { matchMedia: () => media, navigator: { clipboard: { writeText: written } } } as unknown as Window
  written.mockClear()
})
afterEach(() => vi.unstubAllGlobals())
const asHTML = (node: Element) => node as unknown as HTMLElement
function layers() {
  const pretty = new Element(), full = new Element(), trigger = new Element()
  for (const [host, mode] of [[pretty, 'pretty'], [full, 'reduced']] as const) {
    renderVersion(asHTML(host), VERSION, CALENDAR_DISPLAY_SCHEME, { config: display, mode, interactive: false, brand: '#9a6b12' })
  }
  const stop = attachVersionCrossfade(asHTML(pretty), asHTML(full), asHTML(trigger), view)
  const chars = (host: Element) => host.children.flatMap(child => child.className === 'separator' ? child.querySelector('.separator-glyph')!.children : child.children)
  return { pretty, full, trigger, stop, chars }
}

it('copies the canonical renderer value with its suffix, without a decorative v or Pretty separators', async () => {
  const pretty = new Element()
  renderVersion(asHTML(pretty), `v${VERSION}`, CALENDAR_DISPLAY_SCHEME, { config: display, mode: 'pretty', interactive: false, brand: '#9a6b12' })
  expect(pretty.textContent).not.toBe(VERSION)
  expect(await copyRenderedVersion(asHTML(pretty), view)).toBe(true)
  expect(written).toHaveBeenCalledExactlyOnceWith(VERSION)
})

it('refuses to copy a host without a canonical renderer value', async () => {
  expect(await copyRenderedVersion(asHTML(new Element()), view)).toBe(false)
  expect(written).not.toHaveBeenCalled()
})

it('crossfades every character using the shared total duration and segment opacities, preserving renderer output', () => {
  const { pretty, full, trigger, stop, chars } = layers()
  try {
    expect(full.textContent).toBe(VERSION)
    expect(pretty.textContent).toBe('26·10·01 13:01:10')
    expect(chars(full)).toHaveLength(VERSION.length)
    expect(chars(full).every(node => node.style.opacity === '0')).toBe(true)
    pointer(trigger, 'pointerenter')
    expect(trigger.dataset.versionView).toBe('revealed')
    expect(chars(full).every(node => node.style.opacity === '1')).toBe(true)
    expect(chars(pretty).every(node => node.style.opacity === '0')).toBe(true)
    expect(chars(full)[0]!.style.transition).toBe(`opacity ${duration * .42}ms ease-in-out 0ms`)
    expect(chars(full).at(-1)!.style.transition).toBe(`opacity ${duration * .42}ms ease-in-out ${duration * .58}ms`)
    for (const key of ['yy', 'hh', 'mi', 'ss'] as const) expect(full.querySelector(`.${key}`)!.style.opacity).toBe(String(hoverOpacity(display.weights[key])))
    pointer(trigger, 'pointerleave')
    expect(chars(full).every(node => node.style.opacity === '0')).toBe(true)
    expect(chars(pretty).every(node => node.style.opacity === '1')).toBe(true)
  } finally { stop() }
  expect(full.textContent).toBe(VERSION)
  expect(pretty.querySelector('.yy')!.children).toHaveLength(0)
  expect(full.querySelector('.ss')!.style.opacity).toBe('')
  expect(trigger.dataset.versionView).toBeUndefined()
})

it('reduced motion switches both ways immediately, including a preference change during a crossfade', () => {
  media.matches = true
  const { pretty, full, trigger, stop, chars } = layers()
  try {
    for (const type of ['pointerenter', 'pointerleave']) {
      pointer(trigger, type)
      expect([...chars(pretty), ...chars(full)].every(node => node.style.transition === 'none')).toBe(true)
    }
    media.matches = false
    pointer(trigger, 'pointerenter')
    expect(chars(full)[0]!.style.transition).not.toBe('none')
    media.matches = true
    media.dispatchEvent(new Event('change'))
    expect([...chars(pretty), ...chars(full)].every(node => node.style.transition === 'none')).toBe(true)
    expect(chars(full).every(node => node.style.opacity === '1')).toBe(true)
  } finally { stop() }
})

it('touch does not hover; keyboard focus survives pointer exit and leaves on blur; disposal removes listeners', async () => {
  const { trigger, stop } = layers()
  pointer(trigger, 'pointerenter', 'touch')
  expect(trigger.dataset.versionView).toBe('pretty')
  doc.activeElement = trigger
  doc.dispatchEvent(new Event('focusin'))
  expect(trigger.dataset.versionView).toBe('revealed')
  pointer(trigger, 'pointerleave')
  expect(trigger.dataset.versionView).toBe('revealed')
  doc.activeElement = null
  doc.dispatchEvent(new Event('focusout'))
  await Promise.resolve()
  expect(trigger.dataset.versionView).toBe('pretty')
  doc.dispatchEvent(new Event('focusout'))
  stop()
  stop()
  await Promise.resolve()
  pointer(trigger, 'pointerenter')
  expect(trigger.dataset.versionView).toBeUndefined()
})
