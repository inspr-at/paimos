// SPDX-License-Identifier: AGPL-3.0-only
// INSPR-CalVer3 reveal for a version that sits inside another control (a row
// that navigates, the footer's release-history button). The shared renderer's
// own interaction turns the version into a copy button, which cannot nest in a
// button or option, so here the enclosing control drives the same reveal: its
// hover, its keyboard focus, or being the keyboard's active option. Opacity and
// timing come from the pinned bundle; reduced motion switches at once (AEON-309).
import { duration, hoverOpacity } from '../vendor/calendar-version-display/version-interaction.js'

const CONTROL = 'a[href], button, summary, [role="button"], [role="link"], [role="option"], [role="menuitem"], [tabindex]:not([tabindex="-1"])'

// The nearest control around a rendered version, or null when it stands alone.
export function revealControl(host: HTMLElement): HTMLElement | null {
  return host.parentElement?.closest<HTMLElement>(CONTROL) ?? null
}

// Makes `id` (the element that holds a version's stamp) part of what the control
// is described by, next to any description it already has, for as long as the
// caller keeps it. A screen reader lands on the control (a button, menu item or
// option), not on the name inside it, so the control carries the description.
// Returns a dispose that takes only `id` back out.
export function describeControl(control: HTMLElement, id: string): () => void {
  const tokens = () => (control.getAttribute('aria-describedby') ?? '').split(/\s+/).filter(Boolean)
  const apply = () => { if (!tokens().includes(id)) control.setAttribute('aria-describedby', [...tokens(), id].join(' ')) }
  apply()
  // The owner of the control may rewrite its own description; ours is put back.
  const observer = new MutationObserver(apply)
  observer.observe(control, { attributes: true, attributeFilter: ['aria-describedby'] })
  return () => {
    observer.disconnect()
    const rest = tokens().filter(token => token !== id)
    if (rest.length) control.setAttribute('aria-describedby', rest.join(' ')); else control.removeAttribute('aria-describedby')
  }
}

const touchLike = (type: string) => type === 'touch' || type === 'pen'
const focusVisible = (el: Element) => { try { return el.matches(':focus-visible') } catch { return true } }

// Tells `onChange` when `trigger` is pointer-hovered, keyboard-focused or (for a
// listbox option) the keyboard's active option; calls it once at the start.
// Touch and pen pointers never count as hover. Returns a dispose.
export function watchTrigger(trigger: HTMLElement, onChange: (on: boolean) => void): () => void {
  const doc = trigger.ownerDocument
  let hovered = false, last: boolean | null = null
  function keyboard() {
    const active = doc.activeElement
    if (!active || !focusVisible(active)) return false
    if (trigger.contains(active)) return true
    // A listbox keeps focus itself and points at its current option.
    return Boolean(trigger.id) && active.getAttribute('aria-activedescendant') === trigger.id
  }
  const settle = () => { const on = hovered || keyboard(); if (on !== last) { last = on; onChange(on) } }
  const offs: Array<() => void> = []
  const listen = (target: EventTarget, type: string, fn: (e: Event) => void) => { target.addEventListener(type, fn); offs.push(() => target.removeEventListener(type, fn)) }
  listen(trigger, 'pointerenter', e => { hovered = !touchLike((e as PointerEvent).pointerType); settle() })
  listen(trigger, 'pointerleave', () => { hovered = false; settle() })
  listen(doc, 'focusin', settle)
  listen(doc, 'focusout', () => queueMicrotask(settle))
  // Keys make a pointer-focused control focus-visible and move a listbox's option.
  listen(doc, 'keyup', settle)
  const observer = new MutationObserver(settle)
  observer.observe(trigger, { attributes: true, attributeFilter: ['aria-selected'] })
  settle()
  return () => { offs.forEach(off => off()); observer.disconnect() }
}

// Reveals the collapsed segments of `host` (as drawn by renderVersion) while
// `trigger` is hovered or keyboard-focused. Returns a dispose that restores rest.
export function attachVersionReveal(host: HTMLElement, trigger: HTMLElement, view: Window = window): () => void {
  const items = [...host.children].filter((n): n is HTMLElement => n instanceof HTMLElement).map(node => ({
    node, separator: node.className === 'separator', collapsed: node.dataset.collapsed === 'true',
    rest: { opacity: node.style.opacity, maxWidth: node.style.maxWidth, transition: node.style.transition },
  }))
  if (!items.some(item => item.collapsed)) return () => {}
  const media = view.matchMedia?.('(prefers-reduced-motion: reduce)') as MediaQueryList | undefined
  let revealed = false, disposed = false

  function motion(target: boolean) {
    if (disposed || target === revealed) return
    revealed = target
    host.dataset.versionView = target ? 'revealed' : 'pretty'
    const transition = media?.matches ? 'none' : `opacity ${duration}ms ease-in-out, max-width ${duration}ms ease-in-out`
    for (const { node, separator, collapsed, rest } of items) {
      node.style.transition = transition
      if (separator) { if (collapsed) node.style.opacity = target ? '1' : rest.opacity }
      else node.style.opacity = target ? String(hoverOpacity(rest.opacity === '' ? 1 : Number(rest.opacity))) : rest.opacity
      // Grow to the measured natural width so the width change can transition.
      if (collapsed && rest.maxWidth) node.style.maxWidth = target ? (node.scrollWidth > 0 ? `${node.scrollWidth}px` : 'none') : rest.maxWidth
    }
  }
  host.dataset.versionView = 'pretty'
  const offs: Array<() => void> = [watchTrigger(trigger, on => motion(on))]
  const listen = (target: EventTarget, type: string, fn: (e: Event) => void) => { target.addEventListener(type, fn); offs.push(() => target.removeEventListener(type, fn)) }
  if (media) listen(media, 'change', () => { if (revealed) { revealed = false; motion(true) } })

  return () => {
    if (disposed) return
    disposed = true
    offs.forEach(off => off())
    for (const { node, rest } of items) Object.assign(node.style, rest)
    delete host.dataset.versionView
  }
}

// Presentation layer for the release dock (AEON-488). Both text layers come
// from renderVersion: Pretty keeps its separators/geometry, reduced supplies
// canonical text. Split only leaf glyphs, preserving every renderer style.
// The pinned interaction provides the total timing and revealed opacities;
// it does not currently offer this character-by-character crossfade.
export function attachVersionCrossfade(pretty: HTMLElement, canonical: HTMLElement, trigger: HTMLElement, view: Window = window): () => void {
  const restorers: Array<() => void> = []
  function characters(host: HTMLElement, layer: string) {
    const chars: HTMLElement[] = []
    for (const segment of host.children) {
      if (!(segment instanceof HTMLElement) || segment.dataset.collapsed === 'true') continue
      const leaf = segment.querySelector<HTMLElement>('.separator-glyph') ?? segment
      const text = leaf.textContent ?? ''
      const nodes = Array.from(text, ch => {
        const node = host.ownerDocument.createElement('span')
        node.textContent = ch
        node.dataset.versionCharacter = layer
        node.style.display = 'inline-block'
        return node
      })
      leaf.replaceChildren(...nodes)
      restorers.push(() => { leaf.textContent = text })
      chars.push(...nodes)
    }
    return chars
  }
  for (const segment of canonical.children) {
    if (!(segment instanceof HTMLElement)) continue
    const original = segment.style.opacity
    const source = [...pretty.children].find(node => node.className === segment.className) as HTMLElement | undefined
    segment.style.opacity = String(hoverOpacity(source?.style.opacity ? Number(source.style.opacity) : 1))
    restorers.push(() => { segment.style.opacity = original })
  }
  const layers = [characters(pretty, 'pretty'), characters(canonical, 'canonical')]
  const media = view.matchMedia?.('(prefers-reduced-motion: reduce)')
  let revealed = false, disposed = false
  function motion(target: boolean, immediate = false) {
    if (disposed) return
    revealed = target
    pretty.dataset.versionView = trigger.dataset.versionView = target ? 'revealed' : 'pretty'
    layers.forEach((chars, layer) => chars.forEach((node, index) => {
      const fade = duration * .42
      const delay = (duration - fade) * index / Math.max(1, chars.length - 1)
      node.style.transition = immediate || media?.matches ? 'none' : `opacity ${fade}ms ease-in-out ${delay}ms`
      node.style.opacity = Number(target === (layer === 1)).toString()
    }))
  }
  motion(false, true)
  const off = watchTrigger(trigger, motion)
  const preference = () => motion(revealed, true)
  media?.addEventListener('change', preference)
  const priorDuration = trigger.style.getPropertyValue('--version-reveal-duration')
  trigger.style.setProperty('--version-reveal-duration', `${duration}ms`)
  return () => {
    if (disposed) return
    disposed = true
    off()
    media?.removeEventListener('change', preference)
    restorers.forEach(restore => restore())
    if (priorDuration) trigger.style.setProperty('--version-reveal-duration', priorDuration)
    else trigger.style.removeProperty('--version-reveal-duration')
    delete pretty.dataset.versionView
    delete trigger.dataset.versionView
  }
}
