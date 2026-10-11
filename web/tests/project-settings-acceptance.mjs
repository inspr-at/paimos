// SPDX-License-Identifier: AGPL-3.0-only
// P1 adapter for AEON-1227 approved check.mjs: the original audit is retained
// with its scope narrowed to the built P1 surface, rather than the mock shell.
// P4 also reuses this approved audit for Advanced and the shared project sheet.
export const approvedCheckSHA = '5f3e1a708081b52a1915d691ce80f300fa67338e4fc9172d02d2dc9392f35429'
export async function checkProjectSettings(page, name, touch) {
  const results = []
  const ok = (passed, message) => results.push({ passed, message })
async function audit(page, name, touch) {
  const r = await page.evaluate(touch => {
    const vis = e => e.offsetParent !== null || getComputedStyle(e).position === 'fixed'
    const scope = e => !!e.closest('.project-settings, .explainer, .life-pop, .delete-sheet, .project-settings-sheet') && !e.closest('[hidden]') && vis(e)
    const over = document.documentElement.scrollWidth - innerWidth
    const unnamed = [...document.querySelectorAll('button, [role="switch"], input, select, textarea')].filter(scope).filter(b => !(b.getAttribute('aria-label') || b.getAttribute('aria-labelledby') || b.textContent.trim() || b.closest('label') || b.title || (b.id && document.querySelector(`label[for="${b.id}"]`)))).map(b => b.outerHTML.slice(0, 80))
    const wrapped = [...document.querySelectorAll('.btn, .seg button, .link-label, .how, .ptabs button, .src b, .key-badge')].filter(scope).filter(e => { const lh = parseFloat(getComputedStyle(e).lineHeight) || 18; return e.getBoundingClientRect().height > Math.max(lh * 1.6, 46) }).map(e => e.textContent.trim().slice(0, 30))
    const small = []
    if (touch) for (const e of document.querySelectorAll('button, a, select, input, textarea, [role="switch"]')) {
      if (!scope(e) || e.closest('.popover[hidden]') || e.disabled && e.type === 'checkbox') continue
      const box = (e.type === 'checkbox' || e.type === 'radio') ? (e.closest('label') || e) : e
      const r = box.getBoundingClientRect()
      if (r.width && r.height && (r.width < 43.5 || r.height < 43.5)) small.push(`${(box.className || box.tagName).toString().slice(0, 26)} "${(box.textContent || box.getAttribute('aria-label') || '').trim().slice(0, 22)}" ${Math.round(r.width)}×${Math.round(r.height)}`)
    }
    const outside = []
    for (const card of document.querySelectorAll('.m-card, .glass-card, .popover, .floating, .sheet')) {
      if (!scope(card) || card.hidden) continue; const cr = card.getBoundingClientRect()
      for (const e of card.querySelectorAll('button, p, small, input, select, label, textarea, .chk, li')) {
        if (!vis(e)) continue; const r = e.getBoundingClientRect(); if (!r.width) continue
        if (r.right > cr.right + 1 || r.left < cr.left - 1) outside.push(`${(e.className || e.tagName).toString().slice(0, 24)} "${e.textContent.trim().slice(0, 24)}"`)
      }
    }
    const wide = over > 0 ? [...document.querySelectorAll('body *')].filter(e => e.getBoundingClientRect().right > innerWidth + 0.5 && !e.closest('.tw, [hidden]')).slice(0, 3).map(e => `${e.tagName}.${(e.className || '').toString().slice(0, 30)}`) : []
    return { over, unnamed, wrapped, small, outside, wide, coarse: matchMedia('(pointer: coarse)').matches }
  }, touch)
  ok(r.over <= 0, `${name}: horizontal overflow ${r.over}px ${JSON.stringify(r.wide)}`)
  ok(!r.unnamed.length, `${name}: unnamed controls ${JSON.stringify(r.unnamed.slice(0, 3))}`)
  ok(!r.wrapped.length, `${name}: wrapped one-line labels ${JSON.stringify(r.wrapped.slice(0, 4))}`)
  ok(!r.outside.length, `${name}: content outside its card ${JSON.stringify([...new Set(r.outside)].slice(0, 5))}`)
  if (touch) { ok(r.coarse, `${name}: touch emulation did not give pointer: coarse`); ok(!r.small.length, `${name}: touch targets under 44 px ${JSON.stringify([...new Set(r.small)].slice(0, 6))}`) }
}

  await audit(page, name, touch)
  return results
}
