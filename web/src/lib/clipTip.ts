// SPDX-License-Identifier: AGPL-3.0-only
import { nextTick, type Directive } from 'vue'
const observers = new WeakMap<HTMLElement, { value: string; resize: ResizeObserver }>()
function measure(el: HTMLElement) {
  const value = observers.get(el)?.value
  const target = el.parentElement?.matches('button') ? el.parentElement : el
  if (value && el.scrollWidth > el.clientWidth + 1) target.dataset.tip = value
  else delete target.dataset.tip
}
// Use the app's pointer/focus tooltip only where the text is actually clipped.
export const vClipTip: Directive<HTMLElement, string> = {
  mounted(el, binding) {
    const resize = new ResizeObserver(() => measure(el))
    observers.set(el, { value: binding.value, resize }); resize.observe(el); measure(el)
  },
  updated(el, binding) {
    const state = observers.get(el)
    if (state) state.value = binding.value
    void nextTick(() => measure(el))
  },
  beforeUnmount(el) { observers.get(el)?.resize.disconnect(); observers.delete(el) },
}
