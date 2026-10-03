// SPDX-License-Identifier: AGPL-3.0-only
import type { ObjectDirective } from 'vue'

interface Source { name: HTMLElement; text: string }
interface LinkState { identity: string; clear: () => void; dispose: () => void }
const states = new WeakMap<HTMLElement, LinkState>()

/** Navigation waits for a second touch on the same disclosed name. Leave the
 * click bubbling so the shared TooltipHost can reveal it; RouterLink respects
 * preventDefault. Mouse, keyboard and modified activation remain native.
 */
export const vClipLink: ObjectDirective<HTMLElement, string> = {
  mounted(link, binding) {
    let revealed: Source | null = null
    let pending: Source | null = null
    let ready = false
    let stale = false
    const doc = link.ownerDocument
    const same = (a: Source | null, b: Source | null) => !!a && !!b && a.name === b.name && a.text === b.text
    const source = (event: Event): Source | null => {
      if (!(event.target instanceof Element)) return null
      const name = event.target.closest<HTMLElement>('[data-tip]')
        ?? link.querySelector<HTMLElement>('[data-clip-tip]')
      return name && link.contains(name) && name.hasAttribute('data-clip-tip') && name.dataset.tip
        ? { name, text: name.dataset.tip } : null
    }
    const clear = () => {
      if (pending) stale = true
      revealed = null; pending = null; ready = false
      doc.removeEventListener('pointerdown', outside, { capture: true })
      doc.removeEventListener('keydown', clear, { capture: true })
      doc.removeEventListener('scroll', clear, { capture: true })
      doc.removeEventListener('focusout', clear, { capture: true })
    }
    const outside = (event: PointerEvent) => {
      if (!(event.target instanceof Node) || !link.contains(event.target) || event.pointerType !== 'touch') clear()
    }
    const pointerdown = (event: PointerEvent) => {
      stale = false
      pending = event.pointerType === 'touch' ? source(event) : null
      // Snapshot before TooltipHost hides its decoration on pointerdown.
      ready = same(revealed, pending) && doc.querySelector('.tooltip')?.textContent === pending?.text
    }
    const click = (event: MouseEvent) => {
      const current = source(event)
      if (!('pointerType' in event) || event.pointerType !== 'touch' || event.button !== 0
        || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey || event.defaultPrevented) { clear(); return }
      // A reused record must not turn a gesture on A into navigation to B.
      if (stale || (pending && !same(pending, current))) { event.preventDefault(); clear(); return }
      if (!pending) { clear(); return }
      if (ready) { clear(); return }
      event.preventDefault()
      revealed = current
      pending = null
      doc.addEventListener('pointerdown', outside, true)
      doc.addEventListener('keydown', clear, true)
      doc.addEventListener('scroll', clear, true)
      doc.addEventListener('focusout', clear, true)
    }
    link.addEventListener('pointerdown', pointerdown, true)
    link.addEventListener('click', click, true)
    states.set(link, { identity: binding.value, clear, dispose() {
      clear()
      link.removeEventListener('pointerdown', pointerdown, { capture: true })
      link.removeEventListener('click', click, { capture: true })
    } })
  },
  updated(link, binding) {
    const state = states.get(link)
    if (state && state.identity !== binding.value) { state.clear(); state.identity = binding.value }
  },
  beforeUnmount(link) { states.get(link)?.dispose(); states.delete(link) },
}
