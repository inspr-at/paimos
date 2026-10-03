// SPDX-License-Identifier: AGPL-3.0-only
import type { ObjectDirective } from 'vue'

type ClipTipValue = string | { text?: string; onClip?: (clipped: boolean) => void } | undefined
interface ClipTipState {
  value: ClipTipValue
  measure: () => void
  resize: ResizeObserver
  mutation: MutationObserver
}
const states = new WeakMap<HTMLElement, ClipTipState>()

/** Reuse TooltipHost for text that is actually cut off, including line clamps.
 * Call sites opt into keyboard focus; this directive never changes tab order.
 */
export const vClipTip: ObjectDirective<HTMLElement, ClipTipValue> = {
  mounted(el, binding) {
    const state = {} as ClipTipState
    state.value = binding.value
    state.measure = () => {
      const clipped = el.clientWidth > 0 && el.clientHeight > 0 &&
        (el.scrollWidth > el.clientWidth + 1 || el.scrollHeight > el.clientHeight + 1)
      const options = typeof state.value === 'object' ? state.value : undefined
      const text = (typeof state.value === 'string' ? state.value : options?.text) ?? el.textContent ?? ''
      if (clipped && text.trim()) el.dataset.tip = text.trim()
      else delete el.dataset.tip
      options?.onClip?.(clipped)
    }
    state.resize = new ResizeObserver(state.measure)
    state.mutation = new MutationObserver(state.measure)
    states.set(el, state)
    state.resize.observe(el)
    state.mutation.observe(el, { childList: true, characterData: true, subtree: true })
    state.measure()
  },
  updated(el, binding) {
    const state = states.get(el)
    if (state) { state.value = binding.value; state.measure() }
  },
  unmounted(el) {
    const state = states.get(el)
    state?.resize.disconnect()
    state?.mutation.disconnect()
    states.delete(el)
    delete el.dataset.tip
  },
}
