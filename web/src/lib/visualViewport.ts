// SPDX-License-Identifier: AGPL-3.0-only
import { onBeforeUnmount, onMounted, type Ref } from 'vue'

// Phone sheets follow the visual viewport, so the on-screen keyboard (iOS Safari
// does not resize the layout viewport) never covers the bottom of the sheet. The
// element reads --vv-top and --vv-h; wider screens and pinch zoom leave them unset.
export function useVisualViewport(target: Ref<HTMLElement | undefined>, maxWidth = 720) {
  let frame = 0
  const apply = () => {
    frame = 0
    const el = target.value, vv = typeof window === 'undefined' ? undefined : window.visualViewport
    if (!el) return
    if (!vv || window.innerWidth > maxWidth || Math.abs(vv.scale - 1) > 0.01) {
      el.style.removeProperty('--vv-h'); el.style.removeProperty('--vv-top')
      return
    }
    el.style.setProperty('--vv-h', `${Math.round(vv.height)}px`)
    el.style.setProperty('--vv-top', `${Math.max(0, Math.round(vv.offsetTop))}px`)
  }
  const schedule = () => { if (!frame) frame = requestAnimationFrame(apply) }
  onMounted(() => {
    window.visualViewport?.addEventListener('resize', schedule)
    window.visualViewport?.addEventListener('scroll', schedule)
    window.addEventListener('resize', schedule)
    apply()
  })
  onBeforeUnmount(() => {
    cancelAnimationFrame(frame)
    window.visualViewport?.removeEventListener('resize', schedule)
    window.visualViewport?.removeEventListener('scroll', schedule)
    window.removeEventListener('resize', schedule)
  })
}
