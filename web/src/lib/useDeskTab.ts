// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1473: wires the Decision Desk count into the browser tab (see deskTab.ts).
// Call once, in App.vue. The count is the header badge's store; this adds no poll
// while the header polls it, only a read when nobody has read it for a minute: in a
// background tab, or on a page that folds the header away.
import { computed, onScopeDispose, watch } from 'vue'
import { useSession } from '../stores/session'
import { useDecisionDesk } from '../stores/decisionDesk'
import { setTitleNotice, setTitlePrefix } from './brand'
import { dark } from './theme'
import { createDeskTab, DEFAULT_DOT_STYLE, drawTabIcon, tabCount, type DotStyle } from './deskTab'

export const DESK_TAB_STALE_MS = 60_000
const READ_TICK_MS = 15_000

/** The device pixels of a 16 px tab icon on this screen. */
export const tabIconSize = (ratio: number) => Math.min(64, Math.max(16, Math.round(16 * (ratio || 1))))

// The badge's tokens as the canvas reads them; a value it cannot paint falls back to the shipped amber.
function dotStyle(ctx: CanvasRenderingContext2D): DotStyle {
  const css = getComputedStyle(document.documentElement)
  const read = (name: string, fallback: string) => css.getPropertyValue(name).trim() || fallback
  const colour = (name: string, fallback: string) => { ctx.fillStyle = fallback; ctx.fillStyle = read(name, fallback); return String(ctx.fillStyle) }
  return { fill: colour('--gold-2', DEFAULT_DOT_STYLE.fill), ink: colour('--gold-on', DEFAULT_DOT_STYLE.ink), family: read('--mono', DEFAULT_DOT_STYLE.family) }
}

// The page's own icon, drawn again with the dot on a canvas. The plain icon comes back unchanged.
function faviconPainter() {
  const link = document.querySelector<HTMLLinkElement>('link[rel~="icon"]')
  const ctx = document.createElement('canvas').getContext('2d')
  if (!link || !ctx) return { paint: () => {}, repaint: () => {}, stop: () => {} }
  const original = { href: link.getAttribute('href') ?? '', type: link.getAttribute('type') }
  let icon: HTMLImageElement | null = null, loading: Promise<void> | undefined, broken = false
  let want = { count: null as number | null, scale: 1 }, painted = ''
  function restore() {
    if (!painted) return
    painted = ''
    if (original.type === null) link!.removeAttribute('type'); else link!.type = original.type
    link!.href = original.href
    delete link!.dataset.deskCount
  }
  function draw() {
    if (broken || !tabCount(want.count)) { restore(); return }
    const size = tabIconSize(window.devicePixelRatio), style = dotStyle(ctx!)
    const key = [size, tabCount(want.count), want.scale, style.fill, style.ink].join('|')
    if (key === painted) return
    ctx!.canvas.width = ctx!.canvas.height = size
    drawTabIcon(ctx!, size, icon, want.count, want.scale, style)
    try {
      const url = ctx!.canvas.toDataURL('image/png')
      link!.type = 'image/png'; link!.href = url; painted = key
      link!.dataset.deskCount = tabCount(want.count)
    } catch {
      broken = true; restore() // A browser that will not export the canvas keeps the plain icon.
    }
  }
  function paint(count: number | null, scale: number) {
    want = { count, scale }
    if (!tabCount(count)) { restore(); return }
    if (icon || broken || !original.href) { draw(); return }
    loading ??= (async () => {
      const image = new Image()
      image.src = original.href
      try { await image.decode(); icon = image } catch { /* the dot still shows on its own */ }
    })()
    void loading.then(draw)
  }
  // Moving the window to a screen with another pixel ratio draws the icon for that screen.
  let ratio: MediaQueryList | undefined
  function followRatio() {
    ratio?.removeEventListener('change', onRatio)
    ratio = typeof matchMedia === 'function' ? matchMedia(`(resolution: ${window.devicePixelRatio || 1}dppx)`) : undefined
    ratio?.addEventListener('change', onRatio)
  }
  function onRatio() { followRatio(); repaint() }
  function repaint() { if (painted) draw() }
  followRatio()
  return { paint, repaint, stop: () => { ratio?.removeEventListener('change', onRatio); restore() } }
}

export function useDeskTab() {
  if (typeof document === 'undefined') return
  const session = useSession(), desk = useDecisionDesk()
  const owner = computed(() => session.identity?.principal.kind === 'person' ? `${session.identity.tenant.id}:${session.identity.principal.id}` : '')
  const favicon = faviconPainter()
  const reduce = typeof matchMedia === 'function' ? matchMedia('(prefers-reduced-motion: reduce)') : null
  const tab = createDeskTab({
    prefix: setTitlePrefix, notice: setTitleNotice, paint: favicon.paint,
    hidden: () => document.visibilityState === 'hidden', reducedMotion: () => reduce?.matches ?? false,
    clock: { setTimeout: (run, ms) => window.setTimeout(run, ms), clearTimeout: id => window.clearTimeout(id as number) },
  })
  watch(() => [owner.value, desk.count, !!desk.error] as const, ([who, count, error]) => tab.update({ owner: who, count, error }), { immediate: true, flush: 'sync' })
  watch(dark, () => favicon.repaint(), { flush: 'post' })
  const visibility = () => tab.visibility(document.visibilityState === 'hidden')
  document.addEventListener('visibilitychange', visibility)

  // The header's poll keeps the count fresh while it is on screen. Without it the
  // same store is read again once the last answer, or the last try, is a minute old.
  let lastAsked = Date.now()
  watch(() => desk.projection, page => { if (page) lastAsked = Date.now() }, { flush: 'sync' })
  async function readAgain() {
    lastAsked = Date.now()
    await desk.refresh()
    // The header's poll clears the store when it wakes from a throttled timer, which
    // drops a read in flight; that read is asked for once more.
    if (owner.value && !desk.projection && !desk.error) await desk.refresh()
  }
  const reader = window.setInterval(() => {
    if (owner.value && Date.now() - lastAsked >= DESK_TAB_STALE_MS) void readAgain()
  }, READ_TICK_MS)

  onScopeDispose(() => {
    window.clearInterval(reader)
    document.removeEventListener('visibilitychange', visibility)
    tab.dispose(); favicon.stop()
    setTitleNotice('')
  })
}
