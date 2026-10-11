// SPDX-License-Identifier: AGPL-3.0-only
// Risk: overlapping bindings can fire the wrong session action; global keys
// must never take input, IME or native shortcuts away from their current owner.
import { expect, it, vi } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from '@vue/server-renderer'
import ShortcutSheet from '../src/components/work/ShortcutSheet.vue'
import { createShortcutRegistry, onAction, SHORTCUT_ENTRIES, shortcutConflicts, type ShortcutEntry } from '../src/lib/shortcuts'

it('project settings owns only its focused form or overlay and retains native shortcuts', () => {
  const registry = createShortcutRegistry(() => ({ editing: true, layers: ['Delete project'] }))
  let owns = false
  const submit = vi.fn(), dismiss = vi.fn(), global = vi.fn()
  registry.onAction('project.settings.submit', submit, { when: () => owns })
  registry.onAction('project.settings.dismiss', dismiss, { when: () => owns })
  registry.onAction('agents.row.open', global)
  const send = (key: string, extra: Partial<KeyboardEvent> = {}) => {
    const event = { key, shiftKey: false, metaKey: false, ctrlKey: false, altKey: false, isComposing: false, defaultPrevented: false, preventDefault: vi.fn(), stopImmediatePropagation: vi.fn(), ...extra } as unknown as KeyboardEvent
    registry.dispatch(event, 'capture'); return event
  }
  send('Enter', { metaKey: true }); send('Escape')
  expect(submit).not.toHaveBeenCalled(); expect(dismiss).not.toHaveBeenCalled()
  owns = true
  send('Enter', { metaKey: true }); send('Escape')
  expect(submit).toHaveBeenCalledOnce(); expect(dismiss).toHaveBeenCalledOnce()
  for (const key of ['s', 'r', 'd', 'p', 'a', 'Enter']) expect(send(key).preventDefault).not.toHaveBeenCalled()
  for (const key of ['s', 'r', 'd', 'p', 'a']) expect(send(key, { ctrlKey: true }).preventDefault).not.toHaveBeenCalled()
  expect(send('Enter', { metaKey: true, isComposing: true }).preventDefault).not.toHaveBeenCalled()
  expect(global).not.toHaveBeenCalled()
})

it('shortcut help preserves approved transition labels with SVG arrows and accessible words', async () => {
  // Risk: approved transition copy must keep its arrows without text glyphs,
  // and screen readers must still hear the direction between the named states.
  const disposers = [
    onAction('agents.layout.preset', () => {}),
    onAction('agents.details.close', () => {}),
    onAction('agents.row.next', () => {}),
    onAction('agents.row.prev', () => {}),
  ]
  try {
    const html = await renderToString(createSSRApp(ShortcutSheet))
    const labels = [...html.matchAll(/<dd\b[^>]*>([\s\S]*?)<\/dd>/g)].map(match => match[1]!.replace(/<!--[\s\S]*?-->/g, ''))
    for (const [start, words, arrows] of [
      ['Jump the divider:', 'Jump the divider: Focus to Overview to List only', 2],
      ['Close the details', 'Close the details to List only', 1],
    ] as const) {
      const label = labels.find(text => text.startsWith(start))!
      expect(label).toBeDefined()
      expect(label.match(/<svg\b/g) ?? []).toHaveLength(arrows)
      expect(label).not.toContain('→')
      expect(label.replace(/<[^>]*>/g, '').replace(/\s+/g, ' ').trim()).toBe(words)
    }
    expect(labels.filter(label => label === 'Next / previous session')).toHaveLength(1)
  } finally {
    for (const dispose of disposers) dispose()
  }
})

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

it.each(['chat Escape', 'stepper arrows'])('installed dispatcher respects %s ownership and app capture priority', owner => {
  // Model the event path through window capture, the focused control, and
  // window bubble. Use real cancellable events to preserve defaultPrevented.
  const listeners: { type: string; listener: EventListener; capture: boolean }[] = []
  vi.stubGlobal('window', {
    addEventListener(type: string, listener: EventListener, capture = false) { listeners.push({ type, listener, capture }) },
    removeEventListener(type: string, listener: EventListener, capture = false) {
      const index = listeners.findIndex(row => row.type === type && row.listener === listener && row.capture === capture)
      if (index !== -1) listeners.splice(index, 1)
    },
  })
  const registry = createShortcutRegistry(() => ({ editing: false, layers: [] }), () => 0)
  const previous = vi.fn(), next = vi.fn(), close = vi.fn(), palette = vi.fn(), sheet = vi.fn(), desk = vi.fn()
  registry.onAction('agents.row.prev', previous)
  registry.onAction('agents.row.next', next)
  registry.onAction('agents.details.close', close)
  registry.onAction('app.palette', palette)
  registry.onAction('app.shortcuts', sheet)
  registry.onAction('places.desk', desk)
  const uninstall = registry.install()
  const interrupt = vi.fn((event: Event) => { event.preventDefault(); event.stopPropagation() })
  const chatKeys: EventListener = event => { if ((event as KeyboardEvent).key === 'Escape') interrupt(event) }
  // Chat mounts after AppHeader installed the registry, as on /agents/:id.
  window.addEventListener('keydown', chatKeys, true)
  const send = (key: string, control?: (event: KeyboardEvent) => void, extra: Partial<KeyboardEvent> = {}) => {
    const event = Object.assign(new Event('keydown', { cancelable: true }), { key, code: '', metaKey: false, ctrlKey: false, altKey: false, shiftKey: false, repeat: false, isComposing: false, keyCode: 0, ...extra }) as KeyboardEvent
    const stopped = vi.spyOn(event, 'stopImmediatePropagation')
    for (const row of listeners.filter(row => row.type === 'keydown' && row.capture)) {
      row.listener(event)
      if (stopped.mock.calls.length) break
    }
    if (!event.cancelBubble) control?.(event)
    if (!event.cancelBubble) for (const row of listeners.filter(row => row.type === 'keydown' && !row.capture)) {
      row.listener(event)
      if (stopped.mock.calls.length) break
    }
    return event
  }
  try {
    if (owner === 'chat Escape') {
      send('Escape')
      expect(interrupt).toHaveBeenCalledOnce()
      expect(close).not.toHaveBeenCalled()
    } else {
      const step = vi.fn((event: KeyboardEvent) => { event.preventDefault() })
      send('ArrowUp', step)
      send('ArrowDown', step)
      expect(step).toHaveBeenCalledTimes(2)
      expect(previous).not.toHaveBeenCalled()
      expect(next).not.toHaveBeenCalled()
    }
    window.removeEventListener('keydown', chatKeys, true)
    send('Escape'); send('ArrowUp'); send('ArrowDown')
    expect(close).toHaveBeenCalledOnce()
    expect(previous).toHaveBeenCalledOnce()
    expect(next).toHaveBeenCalledOnce()
    const local = vi.fn((event: KeyboardEvent) => { event.preventDefault() })
    send('k', local, { ctrlKey: true }); send('?', local)
    send('g'); send('d', local)
    expect(palette).toHaveBeenCalledOnce()
    expect(sheet).toHaveBeenCalledOnce()
    expect(desk).toHaveBeenCalledOnce()
    expect(local).not.toHaveBeenCalled()
    uninstall()
    expect(listeners).toEqual([])
    expect(send('ArrowUp').defaultPrevented).toBe(false)
    expect(previous).toHaveBeenCalledOnce()
  } finally {
    uninstall()
    vi.unstubAllGlobals()
  }
})
