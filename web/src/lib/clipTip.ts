// SPDX-License-Identifier: AGPL-3.0-only
import type { ObjectDirective } from 'vue'

export type ClipTipValue = string | { text?: string; onClip?: (clipped: boolean) => void } | undefined

/** Both ellipsis and line clamps expose their hidden extent through scroll size. */
export function isTextClipped(element: Pick<HTMLElement, 'clientWidth' | 'clientHeight' | 'scrollWidth' | 'scrollHeight'>): boolean {
  return element.clientWidth > 0 && element.clientHeight > 0 &&
    (element.scrollWidth > element.clientWidth + 1 || element.scrollHeight > element.clientHeight + 1)
}

type State = { value: ClipTipValue; measure: () => void; observer?: ResizeObserver; tip: string | null; addedTab: boolean; live: boolean }
const states = new WeakMap<HTMLElement, State>()
// Local imports expose v-clip-tip without changing global app registration.
export const vClipTip: ObjectDirective<HTMLElement, ClipTipValue> = {
  mounted(element, binding) {
    const state: State = { value: binding.value, measure: () => {}, tip: element.getAttribute('data-tip'), addedTab: false, live: true }
    state.measure = () => {
      if (!state.live) return
      const clipped = isTextClipped(element)
      const options = typeof state.value === 'string' ? { text: state.value } : state.value
      const text = options?.text ?? element.textContent?.trim() ?? ''
      if (clipped && text) {
        element.setAttribute('data-tip', text)
        if (!element.matches('button, input, textarea, select, a[href], [tabindex]')) {
          element.setAttribute('tabindex', '0'); state.addedTab = true
        }
      } else {
        if (state.tip === null) element.removeAttribute('data-tip')
        else element.setAttribute('data-tip', state.tip)
        if (state.addedTab) { element.removeAttribute('tabindex'); state.addedTab = false }
      }
      options?.onClip?.(clipped)
    }
    states.set(element, state)
    state.observer = new ResizeObserver(state.measure)
    state.observer.observe(element)
    state.measure()
    // Font loading can change clipping without changing the clamped box.
    void document.fonts?.ready.then(state.measure)
  },
  updated(element, binding) {
    const state = states.get(element)
    if (state) { state.value = binding.value; state.measure() }
  },
  unmounted(element) {
    const state = states.get(element)
    if (!state) return
    state.live = false; state.observer?.disconnect(); states.delete(element)
  },
}
