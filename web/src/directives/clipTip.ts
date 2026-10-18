// SPDX-License-Identifier: AGPL-3.0-only
import type { ObjectDirective } from 'vue'

/** Both ellipsis and multi-line clamps can hide text. Hidden rows have no tip. */
export function isClipped(element: HTMLElement): boolean {
  return element.clientWidth > 0 && element.clientHeight > 0
    && (element.scrollWidth > element.clientWidth + 1 || element.scrollHeight > element.clientHeight + 1)
}

interface ClipState {
  value: string | undefined
  measure: () => void
  resize: ResizeObserver
  mutation: MutationObserver
  frame: number
  tip: string | null
  tabindex: string | null
  addedTabstop: boolean
}
const states = new WeakMap<HTMLElement, ClipState>()

function restore(element: HTMLElement, name: string, value: string | null) {
  if (value === null) element.removeAttribute(name)
  else element.setAttribute(name, value)
}

/** Use on the text that clips, with an optional full-text string. TooltipHost
 * finds that text from its owning button/link/label on focus and long press.
 * Standalone names become a Tab stop only while clipped. No layout is changed.
 */
export const vClipTip: ObjectDirective<HTMLElement, string | undefined> = {
  mounted(element, binding) {
    const schedule = () => {
      if (!state.frame) state.frame = requestAnimationFrame(() => { state.frame = 0; state.measure() })
    }
    const state: ClipState = {
      value: binding.value, frame: 0, tip: element.getAttribute('data-tip'),
      tabindex: element.getAttribute('tabindex'), addedTabstop: false,
      resize: new ResizeObserver(schedule), mutation: new MutationObserver(schedule),
      measure() {
        const text = (state.value ?? element.textContent ?? '').trim()
        if (text && isClipped(element)) {
          element.setAttribute('data-tip', text)
          element.setAttribute('data-clip-tip', '')
          if (state.tabindex === null && !element.closest('button, a[href], label, [role="option"]')) {
            element.setAttribute('tabindex', '0'); state.addedTabstop = true
          }
        } else {
          restore(element, 'data-tip', state.tip)
          element.removeAttribute('data-clip-tip')
          if (state.addedTabstop) { restore(element, 'tabindex', state.tabindex); state.addedTabstop = false }
        }
      },
    }
    states.set(element, state)
    state.resize.observe(element)
    state.mutation.observe(element, { childList: true, characterData: true, subtree: true })
    state.measure()
  },
  updated(element, binding) {
    const state = states.get(element)
    if (state) { state.value = binding.value; state.measure() }
  },
  beforeUnmount(element) {
    const state = states.get(element)
    if (!state) return
    state.resize.disconnect(); state.mutation.disconnect(); cancelAnimationFrame(state.frame)
    restore(element, 'data-tip', state.tip)
    element.removeAttribute('data-clip-tip')
    if (state.addedTabstop) restore(element, 'tabindex', state.tabindex)
    states.delete(element)
  },
}
