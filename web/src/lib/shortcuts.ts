// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Shared shortcut runtime (AEON-1219). Owners attach with
 * `onAction(id, event => { ...; return false if inapplicable }, { enabled, when })`
 * and dispose the returned function on unmount. `enabled` gates both execution
 * and hints; `when(event)` narrows the entry's scope to the relevant focus/record.
 * AppHeader installs one dispatcher in two phases: app keys and sequences in
 * capture, page single keys in bubble after chat and focused controls. Sequence
 * prefixes pass on to page controls; a completed sequence consumes its final key.
 * `shortcutKeys`, `shortcutHint` and `shortcutHelp` expose the same resolved
 * bindings. Entries without a handler have no hint. Person overrides are
 * intentionally absent in release 130; AEON-1202 owns persistence and editing.
 */
import { shallowRef } from 'vue'
import { releaseChordOpen, SEQUENCE_MS } from './places'

export type ShortcutScope = 'app' | 'app without list search' | 'agents page' | 'agents page, when a split fits' | 'session list' | 'a lead row' | 'a session row' | 'the divider, when focused' | 'project settings'
export interface ShortcutEntry { id: string; keys: readonly string[]; scope: ShortcutScope; label: string; labelParts?: readonly string[]; when?: string }
// Approved transition labels draw an SVG arrow between parts. Plain-text
// consumers retain the same named states, with the direction spoken as "to".
function transitionLabel(...labelParts: string[]) { return { label: labelParts.join(' to '), labelParts } }
export const SHORTCUT_ENTRIES = [
  { id: 'app.palette', keys: ['Mod+K'], scope: 'app', label: 'Search tickets, projects and actions' },
  { id: 'app.search', keys: ['/'], scope: 'app without list search', label: 'Search, on pages without a list' },
  { id: 'app.shortcuts', keys: ['?'], scope: 'app', label: 'This sheet' },
  { id: 'places.projects', keys: ['g then p'], scope: 'app', label: 'Go to Projects' },
  { id: 'places.agents', keys: ['g then a'], scope: 'app', label: 'Go to Agents' },
  { id: 'places.business', keys: ['g then b'], scope: 'app', label: 'Go to Business' },
  { id: 'places.desk', keys: ['g then d'], scope: 'app', label: 'Decision Desk' },
  { id: 'agents.search.focus', keys: ['f', '/'], scope: 'agents page', label: 'Search sessions (this list)' },
  { id: 'agents.layout.preset', keys: ['s'], scope: 'agents page, when a split fits', ...transitionLabel('Jump the divider: Focus', 'Overview', 'List only') },
  { id: 'agents.row.next', keys: ['j', 'ArrowDown'], scope: 'session list', label: 'Next / previous session' },
  { id: 'agents.row.prev', keys: ['k', 'ArrowUp'], scope: 'session list', label: 'Next / previous session' },
  { id: 'agents.group.fold', keys: ['ArrowLeft'], scope: 'a lead row', label: 'Fold / unfold its workers' },
  { id: 'agents.group.unfold', keys: ['ArrowRight'], scope: 'a lead row', label: 'Fold / unfold its workers' },
  { id: 'agents.row.open', keys: ['Enter', 'o'], scope: 'a session row', label: 'Open (in the last split)' },
  { id: 'agents.details.close', keys: ['Escape'], scope: 'agents page', ...transitionLabel('Close the details', 'List only') },
  { id: 'agents.session.pause', keys: ['p'], scope: 'a session row', label: 'Pause / resume' },
  { id: 'agents.session.resume', keys: ['r'], scope: 'a session row', label: 'Pause / resume' },
  { id: 'agents.filter.add', keys: ['Shift+F'], scope: 'agents page', label: 'Add a filter' },
  { id: 'agents.divider.left', keys: ['ArrowLeft'], scope: 'the divider, when focused', label: 'Move the divider (the list’s width)', when: '16 px left' },
  { id: 'agents.divider.right', keys: ['ArrowRight'], scope: 'the divider, when focused', label: 'Move the divider (the list’s width)', when: '16 px right' },
  { id: 'agents.divider.min', keys: ['Home'], scope: 'the divider, when focused', label: 'Move the divider (the list’s width)', when: 'list 280' },
  { id: 'agents.divider.max', keys: ['End'], scope: 'the divider, when focused', label: 'Move the divider (the list’s width)', when: 'details 380' },
  { id: 'agents.groups.toggle_all', keys: [], scope: 'session list', label: 'Collapse all / Expand all' },
  { id: 'project.settings.submit', keys: ['Mod+Enter'], scope: 'project settings', label: 'Save the field or confirm the open project action' },
  { id: 'project.settings.dismiss', keys: ['Escape'], scope: 'project settings', label: 'Leave the field, then close the project overlay' },
] as const satisfies readonly ShortcutEntry[]
export type ShortcutAction = typeof SHORTCUT_ENTRIES[number]['id']
type Handler = (event: KeyboardEvent) => void | false
interface ActionOptions { enabled?: () => boolean; when?: (event: KeyboardEvent) => boolean }
interface Registration { entry: ShortcutEntry; handler: Handler; options: ActionOptions }
interface KeyContext { editing: boolean; layers: readonly string[] }

export function shortcutTyping(target: EventTarget | null): boolean {
  return target instanceof HTMLElement && (target.isContentEditable || !!target.closest('input, textarea, select, [contenteditable=""], [contenteditable="true"]'))
}
function browserContext(event: KeyboardEvent): KeyContext {
  return { editing: shortcutTyping(event.target) || shortcutTyping(document.activeElement), layers: [...document.querySelectorAll('dialog[open], [role="dialog"], [role="alertdialog"], .floating, [role="menu"]')].filter(el => el.getClientRects().length > 0 && !el.closest('[inert]')).map(el => el.getAttribute('aria-label') ?? '') }
}
function steps(binding: string) { return binding.split(' then ') }
function matches(binding: string, event: KeyboardEvent): boolean {
  const parts = binding.split('+'), key = parts.pop()!, mod = parts.includes('Mod'), shift = parts.includes('Shift')
  if (event.altKey || (mod ? !(event.metaKey !== event.ctrlKey) : event.metaKey || event.ctrlKey)) return false
  // Printed punctuation is matched by key (including Shift+7 on German layouts).
  if ((/^[a-z]$/i.test(key) || key.length > 1 || shift) && event.shiftKey !== shift) return false
  if (event.key.toLowerCase() === key.toLowerCase()) return true
  // Only non-Latin letters fall back to physical letter positions.
  return /^[a-z]$/i.test(key) && /^\p{L}$/u.test(event.key) && !/^[a-z]$/i.test(event.key) && event.code === `Key${key.toUpperCase()}`
}
function scopesOverlap(a: ShortcutScope, b: ShortcutScope): boolean {
  if (a === b || a === 'app' || b === 'app') return true
  if (a === 'project settings' || b === 'project settings') return false
  if (a === 'app without list search' || b === 'app without list search') return false
  if (a.startsWith('agents page') || b.startsWith('agents page')) return true
  if (a === 'the divider, when focused' || b === 'the divider, when focused') return false
  return a === 'session list' || b === 'session list' || (a === 'a lead row' && b === 'a session row') || (b === 'a lead row' && a === 'a session row')
}
export function shortcutConflicts(entries: readonly ShortcutEntry[] = SHORTCUT_ENTRIES): string[] {
  const conflicts: string[] = []
  for (let i = 0; i < entries.length; i++) for (const other of entries.slice(i + 1)) {
    const entry = entries[i]
    if (!scopesOverlap(entry.scope, other.scope)) continue
    for (const key of entry.keys) if (other.keys.some(binding => binding.toLowerCase() === key.toLowerCase())) conflicts.push(`${entry.id} / ${other.id}: ${key}`)
  }
  return conflicts
}

export function createShortcutRegistry(context = browserContext, now = () => Date.now()) {
  const registrations = new Map<string, Registration>(), revision = shallowRef(0)
  let pending: { started: number; index: number; candidates: { registration: Registration; keys: string[] }[] } | null = null
  const cancelSequence = () => { pending = null }
  const enabled = (registration: Registration) => registration.options.enabled?.() ?? true
  const available = (registration: Registration, event: KeyboardEvent) => enabled(registration) && (registration.options.when?.(event) ?? true)
  function onAction(id: ShortcutAction, handler: Handler, options: ActionOptions = {}) {
    if (registrations.has(id)) throw new Error(`Shortcut action already attached: ${id}`)
    const registration = { entry: SHORTCUT_ENTRIES.find(entry => entry.id === id)!, handler, options }
    registrations.set(id, registration); revision.value++
    return () => { if (registrations.get(id) === registration) { registrations.delete(id); cancelSequence(); revision.value++ } }
  }
  function keys(id: ShortcutAction): readonly string[] {
    void revision.value
    const registration = registrations.get(id)
    return registration && enabled(registration) ? registration.entry.keys : []
  }
  function invoke(registration: Registration, event: KeyboardEvent) {
    if (registration.handler(event) === false) return false
    event.preventDefault(); event.stopImmediatePropagation()
    return true
  }
  function dispatch(event: KeyboardEvent, phase?: 'capture' | 'bubble') {
    const capture = phase !== 'bubble'
    if (event.defaultPrevented || event.isComposing || event.keyCode === 229 || event.key === 'Process' || event.key === 'Dead' || event.altKey) { if (capture) cancelSequence(); return }
    const current = context(event), time = now()
    // Project forms and overlays opt in with an exact focus/record predicate.
    // Their Enter/Escape bindings may run while typing or inside their own
    // dialog; no other page/global action gains that exception.
    if (capture) for (const registration of registrations.values()) {
      if (registration.entry.scope !== 'project settings' || !registration.options.when || !available(registration, event)) continue
      if (registration.entry.keys.some(binding => matches(binding, event)) && invoke(registration, event)) { cancelSequence(); return }
    }
    if (capture && pending && time - pending.started > SEQUENCE_MS) cancelSequence()
    // Only capture advances sequences; bubble must not process the prefix twice
    // or cancel it when the prefix has opened its existing release menu.
    const sequence = capture ? pending : null
    // The only modifier exception is the app's existing Cmd/Ctrl+K palette.
    if (event.metaKey || event.ctrlKey) {
      if (!capture) return
      cancelSequence()
      const palette = registrations.get('app.palette')
      if (palette && available(palette, event) && matches('Mod+K', event)) invoke(palette, event)
      return
    }
    const releaseLayer = sequence && sequence.candidates.every(candidate => candidate.registration.entry.id.startsWith('places.')) && releaseChordOpen(current.layers, sequence.started, time)
    if (current.editing || (current.layers.length > 0 && !releaseLayer)) { if (capture) cancelSequence(); return }
    if (sequence) {
      if (event.repeat) return
      cancelSequence()
      const candidates = sequence.candidates.filter(candidate => registrations.get(candidate.registration.entry.id) === candidate.registration && available(candidate.registration, event) && matches(candidate.keys[sequence.index], event))
      const completed = candidates.find(candidate => candidate.keys.length === sequence.index + 1)
      if (completed && invoke(completed.registration, event)) return
      if (candidates.length && !completed) { pending = { ...sequence, index: sequence.index + 1, candidates }; return }
      // An unmatched second key may still be a page action, but never re-arms g.
    }
    const candidates: { registration: Registration; keys: string[] }[] = []
    for (const registration of registrations.values()) {
      if (registration.entry.scope === 'project settings') continue
      if (!available(registration, event)) continue
      for (const binding of registration.entry.keys) {
        const keys = steps(binding)
        if (!matches(keys[0], event)) continue
        if (keys.length === 1) {
          const pageKey = !registration.entry.scope.startsWith('app')
          if ((phase === 'capture' && pageKey) || (phase === 'bubble' && !pageKey)) continue
          if (invoke(registration, event)) return
        }
        else if (capture && !sequence && !event.repeat && !current.layers.length) candidates.push({ registration, keys })
      }
    }
    if (candidates.length) pending = { started: time, index: 1, candidates }
  }
  const hint = (id: ShortcutAction) => keys(id).map(shortcutBindingHint).join(' / ')
  function help() {
    void revision.value
    return SHORTCUT_ENTRIES.filter(entry => keys(entry.id).length > 0).map((entry): ShortcutEntry & { id: ShortcutAction } => ({ ...entry, keys: keys(entry.id) }))
  }
  function pointer(event: PointerEvent) {
    if (!pending) return
    const layers = [...document.querySelectorAll('dialog[open], .floating')]
    if (releaseChordOpen(layers.map(el => el.getAttribute('aria-label') ?? ''), pending.started, now()) && event.target instanceof Node && layers[0].contains(event.target)) return
    cancelSequence()
  }
  const focus = (event: FocusEvent) => { if (shortcutTyping(event.target)) cancelSequence() }
  const captureKey = (event: KeyboardEvent) => dispatch(event, 'capture')
  const bubbleKey = (event: KeyboardEvent) => dispatch(event, 'bubble')
  let installed = false
  function install() {
    if (installed) throw new Error('Shortcut dispatcher already installed')
    installed = true
    window.addEventListener('keydown', captureKey, true)
    window.addEventListener('keydown', bubbleKey)
    window.addEventListener('pointerdown', pointer, true)
    window.addEventListener('focusin', focus, true)
    window.addEventListener('blur', cancelSequence)
    return () => {
      installed = false; cancelSequence()
      window.removeEventListener('keydown', captureKey, true)
      window.removeEventListener('keydown', bubbleKey)
      window.removeEventListener('pointerdown', pointer, true)
      window.removeEventListener('focusin', focus, true)
      window.removeEventListener('blur', cancelSequence)
    }
  }
  return { onAction, keys, hint, help, dispatch, install, cancelSequence }
}

export function shortcutBindingHint(binding: string) {
  const hint = binding.replace(/\bMod\b/g, /Mac|iPhone|iPad/.test(typeof navigator === 'undefined' ? '' : navigator.platform || navigator.userAgent) ? 'Command' : 'Ctrl').replace(/\bEscape\b/g, 'Esc')
  return binding.includes(' then ') ? hint : hint.replace(/\b[a-z]\b/g, letter => letter.toUpperCase())
}
// KeyCap input, grouped first by alternatives, then by sequence steps/chords.
export function shortcutCaps(binding: string): string[][] {
  const names: Record<string, string> = { Mod: 'mod', Shift: 'shift', Enter: 'enter', Escape: 'Esc', ArrowLeft: 'left', ArrowRight: 'right', ArrowUp: 'up', ArrowDown: 'down' }
  return steps(binding).map(step => step.split('+').map(key => names[key] ?? (/^[a-z]$/i.test(key) && !binding.includes(' then ') ? key.toUpperCase() : key)))
}
const registry = createShortcutRegistry()
export const onAction = registry.onAction
export const shortcutKeys = registry.keys
export const shortcutHint = registry.hint
export const shortcutHelp = registry.help
export const installShortcutDispatcher = registry.install
export const cancelShortcutSequence = registry.cancelSequence
