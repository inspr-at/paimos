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
    if (!down.cancelBubble) link.dispatchEvent(down)
    beforeClick()
    // The shared host no longer reveals on click. A native tap activates;
    // only a completed long press is consumed by its document capture handler.
    doc.tip = null
    const event = new Event('click', { cancelable: true, bubbles: true })
    for (const [key, value] of Object.entries({ target, pointerType, button: 0, detail: 1, ...options })) Object.defineProperty(event, key, { value })
    link.dispatchEvent(event)
    return event
  }
  return { doc, link, name, click, dispose: () => hooks.beforeUnmount(link.element) }
}

test('every fresh touch activates immediately without a disclosure prerequisite', () => {
  const f = fixture()
  try {
    const first = f.click()
    assert.equal(first.defaultPrevented, false)
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
  test(`${dismissal} does not block the next fresh tap`, () => {
    const f = fixture()
    try {
      assert.equal(f.click().defaultPrevented, false)
      if (dismissal === 'outside') {
        const down = new Event('pointerdown')
        Object.defineProperties(down, { target: { value: new Box() }, pointerType: { value: 'touch' } })
        f.doc.dispatchEvent(down)
        // Another source can have exactly the same text.
      } else if (dismissal === 'hidden') f.doc.tip = null
      else f.doc.dispatchEvent(new Event(dismissal))
      assert.equal(f.click().defaultPrevented, false)
      assert.equal(f.click().defaultPrevented, false)
    } finally { f.dispose() }
  })
}

test('fresh gestures on changed records and replaced sources activate immediately', () => {
  const f = fixture()
  try {
    assert.equal(f.click().defaultPrevented, false)
    f.name.dataset.tip = 'Anderer vollständiger Name'
    assert.equal(f.click().defaultPrevented, false)
    hooks.updated(f.link.element, { value: 'project-b:route-b:revision-b' })
    assert.equal(f.click().defaultPrevented, false)
    const replacement = new Box()
    replacement.clip = true; replacement.dataset.tip = f.name.dataset.tip; replacement.parent = f.link
    f.link.child = replacement
    assert.equal(f.click(replacement).defaultPrevented, false)
  } finally { f.dispose() }
})

test('unmount removes navigation guards', () => {
  const f = fixture()
  assert.equal(f.click().defaultPrevented, false)
  f.dispose()
  for (const type of ['pointerdown', 'click']) assert.equal(getEventListeners(f.link, type).length, 0, `${type} guard removed`)
  for (const type of ['pointerdown', 'keydown', 'scroll', 'focusout']) assert.equal(getEventListeners(f.doc, type).length, 0, `${type} dismissal removed`)
  assert.equal(f.click().defaultPrevented, false)
})

test('native taps never install document dismissal listeners', () => {
  const f = fixture()
  try {
    assert.equal(getEventListeners(f.doc, 'pointerdown').length, 1, 'one persistent gesture identity guard')
    assert.equal(f.click().defaultPrevented, false)
    assert.equal(f.click().defaultPrevented, false)
    assert.equal(getEventListeners(f.doc, 'pointerdown').length, 1, 'taps add no temporary document guards')
    for (const type of ['keydown', 'scroll', 'focusout']) assert.equal(getEventListeners(f.doc, type).length, 0, `${type} dismissal removed`)
  } finally { f.dispose() }
})

test('document disclosure capture cannot bypass the project gesture identity guard', () => {
  const f = fixture()
  const disclosure = (event: Event) => event.stopPropagation()
  f.doc.addEventListener('pointerdown', disclosure, true)
  try {
    assert.equal(f.click().defaultPrevented, false, 'a fresh clipped-name tap remains native')
    assert.equal(f.click(f.name, 'touch', {}, () => hooks.updated(f.link.element, { value: 'project-b:route-b:revision-b' })).defaultPrevented, true, 'a record changed during captured disclosure cannot navigate')
    assert.equal(f.click(f.name, 'touch', {}, () => { f.name.dataset.tip = 'Changed while pressed' }).defaultPrevented, true, 'a captured press retains its original name')
  } finally {
    f.doc.removeEventListener('pointerdown', disclosure, { capture: true })
    f.dispose()
    assert.equal(getEventListeners(f.doc, 'pointerdown').length, 0, 'unmount removes the persistent identity guard')
  }
})

test('record or name changes between pointerdown and click discard stale navigation', () => {
  const f = fixture()
  try {
    assert.equal(f.click(f.name, 'touch', {}, () => hooks.updated(f.link.element, { value: 'project-b:route-b:revision-b' })).defaultPrevented, true)
    assert.equal(f.click(f.name, 'touch', {}, () => { f.name.dataset.tip = 'Neuer Datensatz' }).defaultPrevented, true)
  } finally { f.dispose() }
})

for (const pointerType of ['touch', 'mouse']) {
  test(`${pointerType} navigation rejects replaced and unclipped sources during the gesture`, () => {
    const f = fixture()
    try {
      assert.equal(f.click(f.name, pointerType, {}, () => {
        const replacement = new Box()
        replacement.clip = true; replacement.dataset.tip = f.name.dataset.tip; replacement.parent = f.link
        f.link.child = replacement
      }).defaultPrevented, true)
    } finally { f.dispose() }
    const g = fixture()
    try {
      assert.equal(g.click(g.name, pointerType, {}, () => { g.name.clip = false }).defaultPrevented, true)
    } finally { g.dispose() }
  })
}

test('keyboard activation after a changed pointer record is a fresh action', () => {
  const f = fixture()
  try {
    assert.equal(f.click(f.name, '', { detail: 0 }, () => hooks.updated(f.link.element, { value: 'project-b:route-b:revision-b' })).defaultPrevented, false)
  } finally { f.dispose() }
})

test('a record changed away and back still invalidates the in-flight gesture', () => {
  const f = fixture()
  try {
    assert.equal(f.click(f.name, 'touch', {}, () => {
      hooks.updated(f.link.element, { value: 'project-b:route-b:revision-b' })
      hooks.updated(f.link.element, { value: 'project-a:route-a:revision-a' })
    }).defaultPrevented, true)
  } finally { f.dispose() }
})
