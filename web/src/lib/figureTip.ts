// SPDX-License-Identifier: AGPL-3.0-only
// One rich figure tip for the whole app (AEON-1358, design r4b §3.1–§3.2): the work bar's counts and the column header
// figures. It is the app tooltip's look (TooltipHost) with richer content, opaque, and only one is open at a time:
// moving along the header row switches popups at once. Mouse hover opens it after the app's 380 ms; keyboard focus at
// once; a tap only where the owner allows it (the work bar, pinned). Esc, blur, scroll and resize close it.
import { reactive } from 'vue'

export type TipPlace = 'above' | 'below'
export const TIP_DELAY = 380
export const FIGURE_TIP_ID = 'figure-tip'

export const figureTip = reactive({
  anchor: null as HTMLElement | null,
  // Who renders the content: 'work' (the header work bar) or 'col:<column id>'.
  owner: '',
  place: 'above' as TipPlace,
  pinned: false,
})
let timer: ReturnType<typeof setTimeout> | undefined

export function openFigureTip(anchor: HTMLElement, owner: string, place: TipPlace, pinned = false) {
  clearTimeout(timer)
  Object.assign(figureTip, { anchor, owner, place, pinned })
}
// Hover: after the delay, or at once while another tip of the same family is already open.
export function hoverFigureTip(anchor: HTMLElement, owner: string, place: TipPlace, family: string) {
  if (figureTip.anchor === anchor) return
  clearTimeout(timer)
  if (figureTip.anchor && figureTip.owner.startsWith(family) && !figureTip.pinned) { openFigureTip(anchor, owner, place); return }
  timer = setTimeout(() => openFigureTip(anchor, owner, place), TIP_DELAY)
}
export function cancelFigureTip() { clearTimeout(timer) }
export function closeFigureTip(anchor?: HTMLElement) {
  clearTimeout(timer)
  if (anchor && figureTip.anchor !== anchor) return
  Object.assign(figureTip, { anchor: null, owner: '', pinned: false })
}
export const figureTipOpen = (owner: string) => !!figureTip.anchor && figureTip.owner === owner
// Esc closes the tip and gives focus back to its anchor; that returned focus must not open it again.
let escaped: HTMLElement | null = null
export function escapeFigureTip() {
  const anchor = figureTip.anchor
  closeFigureTip()
  if (!anchor) return
  escaped = anchor
  try { anchor.focus({ preventScroll: true }) } finally { escaped = null }
}
export const focusOpensTip = (anchor: HTMLElement) => escaped !== anchor && anchor.matches(':focus-visible')
