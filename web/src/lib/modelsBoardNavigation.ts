// SPDX-License-Identifier: AGPL-3.0-only
import type { Router, LocationQueryRaw } from 'vue-router'
let returning: { owner: string; scrolls: { selector: string; top: number; left: number }[]; windowTop: number; windowLeft: number } | null = null
function selectorFor(element: HTMLElement): string {
  if (element.id) return `#${CSS.escape(element.id)}`
  const parent = element.parentElement
  if (!parent) return 'html'
  return `${selectorFor(parent)} > ${element.tagName.toLowerCase()}:nth-child(${[...parent.children].indexOf(element) + 1})`
}
export function rememberBoardPosition(button: HTMLElement, owner: string) {
  const scrolls = []
  for (let element = button.parentElement; element; element = element.parentElement) {
    if (element.scrollHeight > element.clientHeight || element.scrollWidth > element.clientWidth) scrolls.push({ selector: selectorFor(element), top: element.scrollTop, left: element.scrollLeft })
  }
  const board = button.closest('[data-model-board]')?.querySelector<HTMLElement>('[data-board-scroll]')
  if (board) scrolls.push({ selector: '[data-model-board] [data-board-scroll]', top: board.scrollTop, left: board.scrollLeft })
  returning = { owner, scrolls, windowTop: window.scrollY, windowLeft: window.scrollX }
}
export async function returnFromBoard(router: Router, query: Record<string, unknown>, owner: () => string) {
  const record = returning; returning = null
  const started = owner()
  await router.push({ path: '/settings/models', query: query as LocationQueryRaw })
  if (owner() !== started) return
  let observer: MutationObserver | undefined, timer: ReturnType<typeof setTimeout> | undefined
  const stop = () => { observer?.disconnect(); clearTimeout(timer) }
  function restore() {
    if (owner() !== started || router.currentRoute.value.path !== '/settings/models') { stop(); return }
    const button = document.querySelector<HTMLElement>('[data-models-fullscreen]')
    if (!button || !document.querySelector('[data-model-board][data-board-ready="true"]')) return
    stop()
    requestAnimationFrame(() => {
      if (owner() !== started || router.currentRoute.value.path !== '/settings/models') return
      if (record?.owner === started) {
        for (const scroll of record.scrolls) { const element = document.querySelector<HTMLElement>(scroll.selector); if (element) { element.scrollTop = scroll.top; element.scrollLeft = scroll.left } }
        window.scrollTo(record.windowLeft, record.windowTop)
      }
      button.focus({ preventScroll: true })
    })
  }
  observer = new MutationObserver(restore); observer.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['data-board-ready'] })
  timer = setTimeout(stop, 5000); restore()
}
