// SPDX-License-Identifier: AGPL-3.0-only
// Browser-side audit rules; exported for synthetic DOM regression tests.
export type Kind = 'horizontal-overflow' | 'interactive-overlap' | 'clipped-text' | 'text-icon' | 'axe' | 'console' | 'unhandled-rejection' | 'layout-shift' | 'small-touch-target' | 'invisible-focus' | 'scenario-error'
export type Raw = { kind: Kind; severity: 'critical' | 'serious' | 'moderate'; selector: string; detail: string }

// These statuses are deliberate outcomes in the offline fixtures. Keep all
// other resource failures visible so a missing mock cannot hide a UI failure.
export function expectedMockConsole(state: string, message: string): boolean {
  const match = message.match(/status of (401|404).*?(https?:\/\/\S+)/)
  if (!match) return false
  let path: string
  try { path = new URL(match[2]!).pathname } catch { return false }
  if (state === 'sign in') return path === '/api/me' && match[1] === '401' || path === '/api/version' && match[1] === '404'
  if (['project', 'project saved views', 'ticket panel'].includes(state)) return /^\/api\/projects\/[^/]+\/journey$/.test(path) && match[1] === '404'
  if (['agents', 'agent session', 'runs redirect', 'approvals redirect', 'pacing redirect'].includes(state)) return path === '/api/models' && match[1] === '404'
  if (['link dialog', 'quote row menu'].includes(state)) return /^\/api\/quotes\/[^/]+\/versions\/\d+\/public-link$/.test(path) && match[1] === '404'
  return false
}

export function decorativeVersionContrast(selector: string, rule: string, labelledVersion: boolean): boolean {
  return rule === 'color-contrast' && /\[aria-hidden="true"\]/.test(selector)
    && labelledVersion
}

export function installLayoutShiftAudit(): void {
  const audit = window as unknown as { auditShift: number; auditShiftArmed: boolean; auditShiftSources: string[] }
  audit.auditShift = 0
  audit.auditShiftArmed = false
  audit.auditShiftSources = []
  new PerformanceObserver(list => {
    for (const entry of list.getEntries() as (PerformanceEntry & { value: number; hadRecentInput: boolean; sources?: { node?: Node; previousRect?: DOMRectReadOnly; currentRect?: DOMRectReadOnly }[] })[]) {
      if (audit.auditShiftArmed && !entry.hadRecentInput) {
        audit.auditShift += entry.value
        audit.auditShiftSources.push(`${entry.value.toFixed(4)}: ${entry.sources?.slice(0, 3).map(source => `${(source.node as Element | undefined)?.className || (source.node as Element | undefined)?.nodeName || '?'} ${Math.round(source.previousRect?.x ?? 0)},${Math.round(source.previousRect?.y ?? 0)} ${Math.round(source.previousRect?.width ?? 0)}×${Math.round(source.previousRect?.height ?? 0)}→${Math.round(source.currentRect?.x ?? 0)},${Math.round(source.currentRect?.y ?? 0)} ${Math.round(source.currentRect?.width ?? 0)}×${Math.round(source.currentRect?.height ?? 0)}`).join(', ')}`)
      }
    }
  }).observe({ type: 'layout-shift', buffered: true })
}

export function armLayoutShiftAudit(): void {
  const audit = window as unknown as { auditShift: number; auditShiftArmed: boolean; auditShiftSources: string[] }
  audit.auditShift = 0
  audit.auditShiftSources = []
  audit.auditShiftArmed = true
}

export function readLayoutShiftAudit(): { score: number; sources: string[] } {
  const audit = window as unknown as { auditShift: number; auditShiftArmed: boolean; auditShiftSources: string[] }
  audit.auditShiftArmed = false
  return { score: audit.auditShift, sources: audit.auditShiftSources }
}

export function domAudit(): Raw[] {
  const result: Raw[] = []
  const add = (kind: Kind, severity: Raw['severity'], selector: string, detail: string) => {
    if (result.filter(f => f.kind === kind).length < 30) result.push({ kind, severity, selector, detail })
  }
  const selector = (el: Element): string => {
    if (el.id) return `#${CSS.escape(el.id)}`
    const parts: string[] = []
    for (let node: Element | null = el; node && parts.length < 5; node = node.parentElement) {
      const cls = [...node.classList].filter(v => /^[\w-]+$/.test(v)).slice(0, 2).map(v => `.${CSS.escape(v)}`).join('')
      const index = node.parentElement ? [...node.parentElement.children].indexOf(node) + 1 : 1
      parts.unshift(`${node.tagName.toLowerCase()}${cls}:nth-child(${index})`)
      if (node.id) { parts[0] = `#${CSS.escape(node.id)}`; break }
    }
    return parts.join(' > ')
  }
  const shown = (el: Element) => { const r = el.getBoundingClientRect(), c = getComputedStyle(el); return r.width > 1 && r.height > 1 && c.visibility === 'visible' && c.display !== 'none' && !el.closest('[aria-hidden="true"], [hidden]') }
  const root = document.documentElement
  if (root.scrollWidth > innerWidth + 1) add('horizontal-overflow', 'serious', 'html', `document ${root.scrollWidth}px wide in ${innerWidth}px viewport`)
  const elements = [...document.querySelectorAll<HTMLElement>('body *')].filter(shown)
  for (const el of elements) {
    const style = getComputedStyle(el), rect = el.getBoundingClientRect()
    let scrollContainer = el.parentElement
    while (scrollContainer && !/auto|scroll|hidden|clip/.test(getComputedStyle(scrollContainer).overflowX)) scrollContainer = scrollContainer.parentElement
    if (scrollContainer && !el.closest('svg')) {
      const pr = scrollContainer.getBoundingClientRect()
      if (rect.width > pr.width + 2 && rect.left < pr.right - 1 && rect.right > pr.right + 2)
        add('horizontal-overflow', 'moderate', selector(el), `${Math.round(rect.width)}px element exceeds ${Math.round(pr.width)}px scroll container ${selector(scrollContainer)}`)
    }
    const directText = [...el.childNodes].some(n => n.nodeType === Node.TEXT_NODE && !!n.textContent?.trim())
    if (directText && el.scrollWidth > el.clientWidth + 2 && /hidden|clip/.test(style.overflowX) && style.textOverflow !== 'ellipsis' && !el.closest('[title], [data-tip], [aria-describedby], [role="tooltip"]') && !el.querySelector('[title], [data-tip]'))
      add('clipped-text', 'moderate', selector(el), `text ${el.scrollWidth}px in ${el.clientWidth}px without ellipsis or tooltip: ${(el.textContent ?? '').trim().slice(0, 70)}`)
    if (/^(BUTTON|A)$/.test(el.tagName) && !el.querySelector('svg, img, [role="img"]')) {
      const own = (el.textContent ?? '').trim()
      if (/^[⋯…×✕✖☰⚙⚲⌄⌃➜→←＋✚✓✔✎✏★☆]{1,3}$/.test(own)) add('text-icon', 'moderate', selector(el), `text glyph used as icon: ${own}`)
    }
  }
  const modal = [...document.querySelectorAll<HTMLElement>('dialog[open], [aria-modal="true"]')].at(-1)
  const receivesPointer = (el: Element, x: number, y: number) => {
    if (x < 0 || x >= innerWidth || y < 0 || y >= innerHeight) return false
    const hit = document.elementFromPoint(x, y)
    return !!hit && (el === hit || el.contains(hit))
  }
  const controls = elements.filter(el => {
    const r = el.getBoundingClientRect(), style = getComputedStyle(el)
    if (!el.matches('button, a[href], input, select, textarea, [role="button"], [role="link"], [role="menuitem"], [role="tab"]') || el.matches(':disabled') || style.pointerEvents === 'none' || style.opacity === '0') return false
    if (modal && !modal.contains(el)) return false
    if (r.bottom <= 0 || r.top >= innerHeight || r.right <= 0 || r.left >= innerWidth) return false
    return receivesPointer(el, Math.max(0, Math.min(innerWidth - 1, (r.left + r.right) / 2)), Math.max(0, Math.min(innerHeight - 1, (r.top + r.bottom) / 2)))
  })
  // A rectangle over any visible text or graphic of a link (hidden and screen-reader-only content aside).
  // Text is measured where it shows: an ellipsised run is cut to the boxes that clip it.
  const coversContent = (link: Element, r: DOMRect) => {
    const visible = (el: Element | null) => !!el && getComputedStyle(el).visibility === 'visible' && !el.closest('.sr-only, [hidden]')
    const shownPart = (q: DOMRect, from: Element | null) => {
      let left = q.left, right = q.right, top = q.top, bottom = q.bottom
      for (let el = from; el && el !== link.parentElement; el = el.parentElement) {
        const style = getComputedStyle(el)
        if (style.overflowX === 'visible' && style.overflowY === 'visible') continue
        const box = el.getBoundingClientRect()
        left = Math.max(left, box.left); right = Math.min(right, box.right); top = Math.max(top, box.top); bottom = Math.min(bottom, box.bottom)
      }
      return { left, right, top, bottom }
    }
    const hit = (q: { left: number; right: number; top: number; bottom: number }) => Math.min(r.right, q.right) - Math.max(r.left, q.left) > 1 && Math.min(r.bottom, q.bottom) - Math.max(r.top, q.top) > 1
    const walker = document.createTreeWalker(link, NodeFilter.SHOW_TEXT)
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      if (!node.textContent?.trim() || !visible(node.parentElement)) continue
      const range = document.createRange()
      range.selectNodeContents(node)
      if ([...range.getClientRects()].some(q => hit(shownPart(q, node.parentElement)))) return true
    }
    return [...link.querySelectorAll('svg, img, [role="img"], progress, .bar')].some(el => visible(el) && hit(shownPart(el.getBoundingClientRect(), el.parentElement)))
  }
  const chipInSlot = (chip: Element, link: Element) => link.matches('.item-link') && chip.matches('.live-chip') && chip.closest('.live')?.parentElement === link.parentElement && !coversContent(link, chip.getBoundingClientRect())
  for (let i = 0; i < controls.length; i++) {
    const a = controls[i], ar = a.getBoundingClientRect()
    const label = a.closest('label') ?? (a instanceof HTMLInputElement ? a.labels?.item(0) : null)
    const lr = label?.getBoundingClientRect()
    const labelArea = !!lr && lr.width >= 44 && lr.height >= 44
    const cx = (ar.left + ar.right) / 2, cy = (ar.top + ar.bottom) / 2
    // An open menu or popover covers the page below it; a tap there closes it, so it
    // does not take reach away from the controls it covers.
    const overlayOf = (el: Element | null) => el?.closest('.floating.pop, .hm-panel, [role="menu"]') ?? null
    const reach = (x: number, y: number) => {
      if (receivesPointer(a, x, y)) return true
      if (x < 0 || x >= innerWidth || y < 0 || y >= innerHeight) return false
      const cover = overlayOf(document.elementFromPoint(x, y))
      return !!cover && !cover.contains(a)
    }
    const hits = [reach(cx - 21.5, cy), reach(cx + 21.5, cy), reach(cx, cy - 21.5), reach(cx, cy + 21.5)]
    // An axis already at least 44px needs no extra reach on that axis.
    const hitArea = (ar.width >= 44 || (hits[0] && hits[1])) && (ar.height >= 44 || (hits[2] && hits[3]))
    // Assessed only where a finger's reach fits the visible part of its scroll area:
    // a control half under the app's footer is assessed once it scrolls into view.
    let clip = { top: 0, bottom: innerHeight }
    for (let p = a.parentElement; p && p !== document.body; p = p.parentElement) {
      // Only an area that scrolls up and down: a sideways strip is assessed as it stands.
      if (/auto|scroll/.test(getComputedStyle(p).overflowY) && p.scrollHeight > p.clientHeight + 1) { const pr = p.getBoundingClientRect(); clip = { top: Math.max(clip.top, pr.top), bottom: Math.min(clip.bottom, pr.bottom) }; break }
    }
    const fullyInViewport = cx - 22 >= 0 && cx + 22 < innerWidth && cy - 22 >= clip.top && cy + 22 < clip.bottom
    // WCAG 2.5.8's inline exception: a date on the quote's paper is a word in the
    // document's running text, sized by its line, like a link in a sentence.
    const inline = !!a.closest('.quote-document .as-paper')
    if (innerWidth <= 390 && fullyInViewport && (ar.width < 44 || ar.height < 44) && !labelArea && !hitArea && !inline && !a.closest('[role="checkbox"], [role="radio"], [role="switch"]'))
      add('small-touch-target', 'moderate', selector(a), `${Math.round(ar.width)}×${Math.round(ar.height)}px touch target at ${Math.round(ar.x)},${Math.round(ar.y)}; blocked ${['left', 'right', 'top', 'bottom'].filter((_, n) => !hits[n]).join(', ')} by ${document.elementFromPoint(cx, cy + 21.5)?.className || '?'} at ${Math.round(document.elementFromPoint(cx, cy + 21.5)?.getBoundingClientRect().x ?? 0)},${Math.round(document.elementFromPoint(cx, cy + 21.5)?.getBoundingClientRect().y ?? 0)}`)
    for (let j = i + 1; j < controls.length; j++) {
      const b = controls[j], br = b.getBoundingClientRect()
      if (a.contains(b) || b.contains(a) || a.closest('label') === b.closest('label') && a.closest('label')) continue
      // The row link deliberately extends behind its separate action button.
      if ((a.matches('.card-link') || b.matches('.card-link')) && a.parentElement === b.parentElement) continue
      // A card's or row's live agents chip (AEON-184) may share its own link's box,
      // in the slot the link leaves empty for it; covering anything the link shows is an overlap.
      if (chipInSlot(a, b) || chipInSlot(b, a)) continue
      // A scrim and a fixed app edge intentionally cover scrolling content.
      if (a.matches('.sheet-scrim') || b.matches('.sheet-scrim') || a.closest('.app-footer, .app-header') !== b.closest('.app-footer, .app-header') && (a.closest('.app-footer') || b.closest('.app-footer'))) continue
      // Open menus cover the page below; their background controls are not peers.
      const overlay = (el: Element) => el.closest('.floating.pop, .hm-panel')
      if (overlay(a) !== overlay(b) && (overlay(a) || overlay(b))) continue
      const overlap = Math.max(0, Math.min(ar.right, br.right) - Math.max(ar.left, br.left)) * Math.max(0, Math.min(ar.bottom, br.bottom) - Math.max(ar.top, br.top))
      if (overlap > 16) add('interactive-overlap', 'serious', selector(a), `overlaps ${selector(b)} by ${Math.round(overlap)}px²`)
    }
  }
  return result
}
