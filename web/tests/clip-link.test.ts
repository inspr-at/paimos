// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { getEventListeners } from 'node:events'
import { vClipLink } from '../src/directives/clipLink.ts'

class Box extends EventTarget {
  dataset: { tip?: string } = {}
  clip = false
  parent: Box | null = null
  child: Box | null = null
  ownerDocument!: Doc
  get element() { return this as unknown as HTMLElement }
  contains(node: Box): boolean { return node === this || !!this.child?.contains(node) }
  hasAttribute(name: string) { return name === 'data-clip-tip' && this.clip }
  closest(): Box | null { return this.dataset.tip ? this : this.parent?.closest() ?? null }
  querySelector() { return this.child?.clip ? this.child : null }
}
class Doc extends EventTarget {
  tip: { textContent: string } | null = null
  querySelector() { return this.tip }
}
const hooks = vClipLink as {
  mounted: (el: HTMLElement, binding: { value: string }) => void
  updated: (el: HTMLElement, binding: { value: string }) => void
  beforeUnmount: (el: HTMLElement) => void
}
const original = { Element: globalThis.Element, Node: globalThis.Node }
beforeEach(() => Object.assign(globalThis, { Element: Box, Node: Box }))
afterEach(() => Object.assign(globalThis, original))

function fixture() {
  const doc = new Doc(), link = new Box(), name = new Box()
  link.child = name; name.parent = link; link.ownerDocument = doc
  name.dataset.tip = 'Vollständiger Projektname'; name.clip = true
  hooks.mounted(link.element, { value: 'project-a:route-a:revision-a' })
  function click(target = name, pointerType = 'touch', options: Record<string, unknown> = {}, beforeClick = () => {}) {
    const down = new Event('pointerdown')
    Object.defineProperties(down, { target: { value: target }, pointerType: { value: pointerType } })
    doc.dispatchEvent(down)
    link.dispatchEvent(down)
    beforeClick()
    // TooltipHost clears after the link captures pointerdown and reveals after
    // click. Navigation respects defaultPrevented, without stopping bubbling.
    doc.tip = null
    const event = new Event('click', { cancelable: true, bubbles: true })
    for (const [key, value] of Object.entries({ target, pointerType, button: 0, ...options })) Object.defineProperty(event, key, { value })
    link.dispatchEvent(event)
    if (name.clip && name.dataset.tip) doc.tip = { textContent: name.dataset.tip }
    return event
  }
  return { doc, link, name, click, dispose: () => hooks.beforeUnmount(link.element) }
}

test('first touch cancels navigation while bubbling; the second permits it', () => {
  const f = fixture()
  try {
    const first = f.click()
    assert.equal(first.defaultPrevented, true)
    assert.equal(first.cancelBubble, false)
    assert.equal(f.click().defaultPrevented, false)
  } finally { f.dispose() }
})

test('mouse, keyboard, modified clicks and unclipped names keep native activation', () => {
  const f = fixture()
  try {
    assert.equal(f.click(f.name, 'mouse').defaultPrevented, false)
    assert.equal(f.click(f.name, '').defaultPrevented, false)
    for (const modifier of ['metaKey', 'ctrlKey', 'altKey', 'shiftKey']) assert.equal(f.click(f.name, 'touch', { [modifier]: true }).defaultPrevented, false)
    f.name.clip = false
    assert.equal(f.click().defaultPrevented, false)
  } finally { f.dispose() }
})

for (const dismissal of ['keydown', 'scroll', 'focusout', 'outside', 'hidden']) {
  test(`${dismissal} requires disclosure again before navigation`, () => {
    const f = fixture()
    try {
      assert.equal(f.click().defaultPrevented, true)
      if (dismissal === 'outside') {
        const down = new Event('pointerdown')
        Object.defineProperties(down, { target: { value: new Box() }, pointerType: { value: 'touch' } })
        f.doc.dispatchEvent(down)
        // Another source can have exactly the same text.
      } else if (dismissal === 'hidden') f.doc.tip = null
      else f.doc.dispatchEvent(new Event(dismissal))
      assert.equal(f.click().defaultPrevented, true)
      assert.equal(f.click().defaultPrevented, false)
    } finally { f.dispose() }
  })
}

test('a changed name, route, record or replaced source cannot reuse disclosure', () => {
  const f = fixture()
  try {
    assert.equal(f.click().defaultPrevented, true)
    f.name.dataset.tip = 'Anderer vollständiger Name'
    assert.equal(f.click().defaultPrevented, true)
    hooks.updated(f.link.element, { value: 'project-b:route-b:revision-b' })
    assert.equal(f.click().defaultPrevented, true)
    const replacement = new Box()
    replacement.clip = true; replacement.dataset.tip = f.name.dataset.tip; replacement.parent = f.link
    f.link.child = replacement
    assert.equal(f.click(replacement).defaultPrevented, true)
  } finally { f.dispose() }
})

test('unmount removes navigation guards', () => {
  const f = fixture()
  assert.equal(f.click().defaultPrevented, true)
  f.dispose()
  for (const type of ['pointerdown', 'click']) assert.equal(getEventListeners(f.link, type).length, 0, `${type} guard removed`)
  for (const type of ['pointerdown', 'keydown', 'scroll', 'focusout']) assert.equal(getEventListeners(f.doc, type).length, 0, `${type} dismissal removed`)
  assert.equal(f.click().defaultPrevented, false)
})

test('second touch releases temporary document dismissal listeners', () => {
  const f = fixture()
  try {
    assert.equal(f.click().defaultPrevented, true)
    assert.equal(f.click().defaultPrevented, false)
    for (const type of ['pointerdown', 'keydown', 'scroll', 'focusout']) assert.equal(getEventListeners(f.doc, type).length, 0, `${type} dismissal removed`)
  } finally { f.dispose() }
})

test('record or name changes between pointerdown and click discard stale navigation', () => {
  const f = fixture()
  try {
    assert.equal(f.click(f.name, 'touch', {}, () => hooks.updated(f.link.element, { value: 'project-b:route-b:revision-b' })).defaultPrevented, true)
    assert.equal(f.click(f.name, 'touch', {}, () => { f.name.dataset.tip = 'Neuer Datensatz' }).defaultPrevented, true)
  } finally { f.dispose() }
})
