// SPDX-License-Identifier: AGPL-3.0-only
import { onBeforeUnmount, onMounted, watch, type Ref } from 'vue'

// Phone sheets follow the visual viewport, so the on-screen keyboard (iOS Safari
// does not resize the layout viewport) never covers the bottom of the sheet. The
// element reads --vv-top and --vv-h; wider screens and pinch zoom leave them unset.
export function useVisualViewport(target: Ref<HTMLElement | undefined>, maxWidth = 720, onChange?: () => void) {
  let frame = 0
  const apply = () => {
    frame = 0
    const el = target.value, vv = typeof window === 'undefined' ? undefined : window.visualViewport
    if (!el) return
    if (!vv || window.innerWidth > maxWidth || Math.abs(vv.scale - 1) > 0.01) {
      el.style.removeProperty('--vv-h'); el.style.removeProperty('--vv-top')
      onChange?.()
      return
    }
    el.style.setProperty('--vv-h', `${Math.round(vv.height)}px`)
    el.style.setProperty('--vv-top', `${Math.max(0, Math.round(vv.offsetTop))}px`)
    onChange?.()
  }
  const schedule = () => { if (!frame) frame = requestAnimationFrame(apply) }
  // Teleported overlays can acquire their element after the owner mounts.
  watch(target, schedule, { flush: 'post' })
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
