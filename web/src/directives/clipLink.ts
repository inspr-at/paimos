// SPDX-License-Identifier: AGPL-3.0-only
import type { ObjectDirective } from 'vue'

interface Source { name: HTMLElement; text: string }
interface LinkState { identity: string; invalidate: () => void; dispose: () => void }
const states = new WeakMap<HTMLElement, LinkState>()

/** Taps activate immediately; TooltipHost owns long-press disclosure. Keep a
 * pointer gesture bound to its original record/name so a reused link cannot
 * navigate to a record that changed while the pointer was down.
 */
export const vClipLink: ObjectDirective<HTMLElement, string> = {
  mounted(link, binding) {
    let pending: { identity: string; source: Source | null; stale: boolean } | null = null
    const doc = link.ownerDocument
    const source = (event: Event): Source | null => {
      if (!(event.target instanceof Element)) return null
      const name = event.target.closest<HTMLElement>('[data-tip]')
        ?? link.querySelector<HTMLElement>('[data-clip-tip]')
      return name && link.contains(name) && name.hasAttribute('data-clip-tip') && name.dataset.tip
        ? { name, text: name.dataset.tip } : null
    }
    const pointerdown = (event: PointerEvent) => {
      if (!(event.target instanceof Node) || !link.contains(event.target)) { pending = null; return }
      pending = { identity: states.get(link)!.identity, source: source(event), stale: false }
    }
    const click = (event: MouseEvent) => {
      const started = pending
      pending = null
      // Keyboard activation is a fresh action, independent of an earlier hold.
      if (!started || event.detail === 0) return
      const current = source(event)
      if (started.stale || started.identity !== states.get(link)!.identity
        || started.source?.name !== current?.name || started.source?.text !== current?.text) event.preventDefault()
    }
    // The shared host stops clipped-name presses at document capture so an
    // ancestor cannot start selection. Capture alongside it to retain identity
    // checks for both ordinary taps and holds without changing the owner host.
    doc.addEventListener('pointerdown', pointerdown, true)
    link.addEventListener('click', click, true)
    states.set(link, { identity: binding.value, invalidate() { if (pending) pending.stale = true }, dispose() {
      pending = null
      doc.removeEventListener('pointerdown', pointerdown, { capture: true })
      link.removeEventListener('click', click, { capture: true })
    } })
  },
  updated(link, binding) {
    const state = states.get(link)
    if (state && state.identity !== binding.value) { state.invalidate(); state.identity = binding.value }
  },
  beforeUnmount(link) { states.get(link)?.dispose(); states.delete(link) },
}
