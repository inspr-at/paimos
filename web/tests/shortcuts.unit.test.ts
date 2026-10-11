// SPDX-License-Identifier: AGPL-3.0-only
// Risk: overlapping bindings can fire the wrong session action; global keys
// must never take input, IME or native shortcuts away from their current owner.
import { expect, it, vi } from 'vitest'
import { createShortcutRegistry, SHORTCUT_ENTRIES, shortcutConflicts, type ShortcutEntry } from '../src/lib/shortcuts'

it('rejects shared keys in overlapping scopes and permits disjoint row/divider bindings', () => {
  expect(shortcutConflicts()).toEqual([])
  for (const scope of ['agents page', 'session list', 'a session row', 'app'] as const) {
    const duplicate: ShortcutEntry = { id: 'accidental-action', keys: ['J'], scope, label: 'Wrong action' }
    expect(shortcutConflicts([...SHORTCUT_ENTRIES, duplicate])).toContain('agents.row.next / accidental-action: j')
  }
  const duplicate: ShortcutEntry = { id: 'second-place', keys: ['g then d'], scope: 'app', label: 'Wrong destination' }
  expect(shortcutConflicts([...SHORTCUT_ENTRIES, duplicate])).toContain('places.desk / second-place: g then d')
})

it('dispatches only attached eligible actions, preserves native/input/IME keys, and cancels stale sequences', () => {
  let time = 0, editing = false, layers: string[] = [], enabled = true
  const registry = createShortcutRegistry(() => ({ editing, layers }), () => time)
  const search = vi.fn(), filter = vi.fn(), layout = vi.fn(), palette = vi.fn(), desk = vi.fn(), next = vi.fn()
  expect(registry.keys('agents.search.focus')).toEqual([])
  expect(registry.hint('agents.layout.preset')).toBe('')
  const dispose = registry.onAction('agents.search.focus', search, { enabled: () => enabled })
  registry.onAction('agents.filter.add', filter)
  registry.onAction('agents.layout.preset', layout)
  registry.onAction('agents.row.next', next, { when: () => !layers.length })
  registry.onAction('app.palette', palette)
  const stopDesk = registry.onAction('places.desk', desk)
  expect(registry.keys('agents.search.focus')).toEqual(['f', '/'])
  expect(registry.help().find(entry => entry.id === 'agents.search.focus')?.keys).toEqual(['f', '/'])
  const send = (key: string, extra: Partial<KeyboardEvent> = {}) => {
    const event = { key, code: '', shiftKey: false, metaKey: false, ctrlKey: false, altKey: false, repeat: false, isComposing: false, keyCode: 0, defaultPrevented: false, preventDefault: vi.fn(), stopImmediatePropagation: vi.fn(), ...extra } as unknown as KeyboardEvent
    registry.dispatch(event)
    return event
  }
  expect(send('f').preventDefault).toHaveBeenCalledOnce()
  send('/', { code: 'NumpadDivide' })
  send('/', { code: 'Digit7', shiftKey: true })
  send('а', { code: 'KeyF' }) // Non-Latin letter uses the physical F position.
  expect(search).toHaveBeenCalledTimes(4)
  send('F', { shiftKey: true }); expect(filter).toHaveBeenCalledOnce()
  send('s'); expect(layout).toHaveBeenCalledOnce()
  for (const extra of [{ metaKey: true }, { ctrlKey: true }, { altKey: true }, { isComposing: true }, { keyCode: 229 }, { defaultPrevented: true }]) {
    expect(send('f', extra).preventDefault).not.toHaveBeenCalled()
  }
  for (const key of ['s', 'r', 'd', 'p', 'a']) {
    expect(send(key, { metaKey: true }).preventDefault).not.toHaveBeenCalled()
    expect(send(key, { ctrlKey: true }).preventDefault).not.toHaveBeenCalled()
  }
  editing = true
  expect(send('f').preventDefault).not.toHaveBeenCalled()
  send('k', { metaKey: true }); expect(palette).toHaveBeenCalledOnce()
  editing = false; layers = ['Menu']
  expect(send('f').preventDefault).not.toHaveBeenCalled()
  layers = []
  expect(send('g').preventDefault).not.toHaveBeenCalled()
  layers = ['Release for AEON-1219']
  expect(send('d').stopImmediatePropagation).toHaveBeenCalledOnce()
  expect(desk).toHaveBeenCalledOnce()
  for (const interrupt of ['expired', 'menu', 'field', 'composition', 'modifier', 'owner'] as const) {
    layers = []; editing = false; send('g')
    if (interrupt === 'expired') time += 1501
    if (interrupt === 'menu') { layers = ['Menu']; send('x'); layers = [] }
    if (interrupt === 'field') { editing = true; send('x'); editing = false }
    if (interrupt === 'composition') send('x', { isComposing: true })
    if (interrupt === 'modifier') send('x', { ctrlKey: true })
    if (interrupt === 'owner') registry.cancelSequence()
    expect(send('d').preventDefault).not.toHaveBeenCalled()
  }
  expect(desk).toHaveBeenCalledOnce()
  send('g'); send('g'); send('d'); expect(desk).toHaveBeenCalledOnce()
  send('g'); stopDesk(); send('d'); expect(desk).toHaveBeenCalledOnce()
  enabled = false
  expect(registry.keys('agents.search.focus')).toEqual([])
  expect(send('f').preventDefault).not.toHaveBeenCalled()
  enabled = true; dispose()
  expect(registry.hint('agents.search.focus')).toBe('')
  expect(registry.help().some(entry => entry.id === 'agents.search.focus')).toBe(false)
  expect(send('f').preventDefault).not.toHaveBeenCalled()
  expect(search).toHaveBeenCalledTimes(4)
})
