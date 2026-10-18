// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1196 round 7 design check (Tick's designer). Run only via ../tick-2606.sh <ag dir> check.mjs
// 1440 (desktop) and 400 (the phone frame), light and dark, every state in the mockbar's switcher.
// Checks: zero layout shift (hover ⋯, dial reveal, sticky, fold/unfold, Collapse all, divider drag), no edge accents,
// SVG-only icons, 44 px touch targets (phone), keys F · / · S and nothing in text fields, avatars with a badge shape and
// an aria-label, English only, no horizontal scroll at 390. Screenshots: shots-r5/check/. Result: check.txt.
import { createRequire } from 'node:module'
import { pathToFileURL } from 'node:url'
import fs from 'node:fs'
const req = createRequire(import.meta.url)
let pw; try { pw = req('playwright') } catch { pw = createRequire('/Users/markus/Code/aeon/web/package.json')('playwright') }
const URL0 = pathToFileURL(process.cwd() + '/fragment.html').href
const SHOTS = 'shots-r7/check'
fs.mkdirSync(SHOTS, { recursive: true })
const STATES = ['busy', 'normal', 'nolead', 'stopped', 'lost', 'none', 'loading', 'error']
const out = [], fails = []
const ok = (c, msg) => { out.push(`${c ? 'PASS' : 'FAIL'} ${msg}`); if (!c) fails.push(msg) }
const browser = await pw.chromium.launch({ headless: true })
const errors = []
async function page(w) {
  const p = await browser.newPage({ viewport: { width: w, height: 1000 }, deviceScaleFactor: 1 })
  p.on('pageerror', e => errors.push(String(e)))
  p.on('console', m => { if (m.type() === 'error' && !/fonts\.googleapis/.test(m.text())) errors.push(m.text()) })
  return p
}
const go = async (p, q) => { await p.goto(URL0 + q); await p.waitForTimeout(220) }

// ---------- In-page probes ----------
const PROBES = () => {
  const stageEls = () => [...document.querySelectorAll('#stage *, #popover:not([hidden]) *')]
  const visible = el => { const r = el.getBoundingClientRect(); const cs = getComputedStyle(el); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none' }
  window.__edge = () => {
    const bad = []
    for (const el of stageEls()) {
      const cs = getComputedStyle(el)
      const bw = s => parseFloat(cs[`border${s}Width`]) || 0, bc = s => cs[`border${s}Color`]
      const opaque = c => c && !/rgba\(.*,\s*0\)$/.test(c) && c !== 'transparent'
      for (const [a, b] of [['Left', 'Right'], ['Top', 'Bottom']]) {
        if (bw(a) >= 2 && bw(a) - bw(b) >= 1 && opaque(bc(a))) bad.push(`${el.className?.baseVal ?? el.className} border-${a.toLowerCase()} ${bw(a)}px`)
        if (bw(b) >= 2 && bw(b) - bw(a) >= 1 && opaque(bc(b))) bad.push(`${el.className?.baseVal ?? el.className} border-${b.toLowerCase()} ${bw(b)}px`)
      }
      for (const part of (cs.boxShadow === 'none' ? [] : cs.boxShadow.split(/,(?![^(]*\))/))) {
        if (!/inset/.test(part)) continue
        const n = (part.replace(/rgba?\([^)]*\)|inset/g, '').match(/-?[\d.]+px/g) || []).map(parseFloat)
        const [x = 0, y = 0, blur = 0] = n
        if ((Math.abs(x) >= 2 || Math.abs(y) >= 2) && blur === 0) bad.push(`${el.className?.baseVal ?? el.className} inset shadow ${part.trim()}`)
      }
      for (const pseudo of ['::before', '::after']) {
        const ps = getComputedStyle(el, pseudo)
        if (ps.content === 'none' || ps.position !== 'absolute') continue
        const w = parseFloat(ps.width) || 0, h = parseFloat(ps.height) || 0, bg = ps.backgroundColor
        if (w >= 2 && w <= 6 && h >= 16 && bg && !/rgba\(.*,\s*0\)$/.test(bg) && bg !== 'transparent') bad.push(`${el.className} ${pseudo} bar ${w}px`)
      }
    }
    return bad
  }
  window.__glyphs = () => {
    const re = /[←→↑↓↵⏎✓✔✕✖×▾▸▴▲▼◂▶◀★☆●○◉⚠✗✘➜➔⟶]/
    const hits = []
    const walk = n => { for (const c of n.childNodes) { if (c.nodeType === 3 && re.test(c.textContent)) hits.push(c.textContent.trim().slice(0, 40)); else if (c.nodeType === 1 && c.tagName !== 'SCRIPT' && c.tagName !== 'STYLE') walk(c) } }
    walk(document.getElementById('stage')); const pop = document.getElementById('popover'); if (!pop.hidden) walk(pop)
    return hits
  }
  window.__english = () => {
    const t = document.getElementById('stage').innerText + ' ' + (document.getElementById('popover').hidden ? '' : document.getElementById('popover').innerText)
    const words = t.match(/[äöüÄÖÜß]|\b(und|der|die|das|nicht|mit|für|oder|ist|Sitzung|Agenten|Projekt|Fehler|Einstellungen|bitte)\b/g)
    return words || []
  }
  window.__avatars = () => {
    const LABELS = ['Working', 'Needs something', 'Throttled', 'Problem', 'No heartbeat', 'Idle', 'Done', 'Ended', 'Paused', 'Pausing', 'Awaiting heartbeat', 'Idle · no heartbeat']
    const bad = []; let n = 0
    for (const a of document.querySelectorAll('#stage .ava')) {
      n++
      const mark = a.querySelector('svg.agent-state-mark'), st = a.dataset.state, label = a.getAttribute('aria-label') || ''
      if (a.getAttribute('role') !== 'img') bad.push(`no role img: ${label}`)
      if (!LABELS.some(l => label.endsWith(`: ${l}`))) bad.push(`aria-label without state: ${label}`)
      if (!mark || mark.dataset.mark !== st || mark.children.length < 2) bad.push(`badge shape missing or wrong: ${label}`)
      if (a.querySelectorAll('svg.agent-state-mark').length !== 1 || a.querySelector('.hg')) bad.push(`more than the state badge on the avatar: ${label}`)
    }
    return { n, bad }
  }
  // Harness marks: never twice in a row; compact rows (Focus, phone) open line 1 with the 12 px mark and its aria-label.
  window.__harness = () => {
    const bad = []; let rows = 0, compact = 0
    for (const r of document.querySelectorAll('#stage .row[data-row], #stage .row.ended')) {
      if (!r.getClientRects().length) continue
      rows++
      const marks = [...r.querySelectorAll('.hg')].filter(visible)
      if (marks.length > 1) bad.push(`${r.dataset.row}: ${marks.length} harness marks`)
      if (r.closest('.side, .ph')) { compact++; const op = r.querySelector('.op-mark'); if (!op || !visible(op) || !/·/.test(op.getAttribute('aria-label') || '')) bad.push(`${r.dataset.row || 'ended row'}: no opener mark with an aria-label`) }
    }
    return { rows, compact, bad }
  }
  window.__hscroll = () => {
    const doc = document.documentElement.scrollWidth > innerWidth + 0.5
    const inner = [...document.querySelectorAll('#ph-scroll, .pane.sheet, .pane.sheet .pane-body')].filter(el => el.scrollWidth > el.clientWidth + 0.5).map(el => el.className || el.id)
    return { doc, inner }
  }
  window.__targets = () => {
    const sel = 'button, a[href], input:not([type="checkbox"]), textarea, select, [role="separator"][tabindex="0"]'
    const bad = []; let n = 0
    for (const el of document.querySelectorAll(`#stage :is(${sel}), #popover:not([hidden]) :is(${sel})`)) {
      if (!visible(el) || el.closest('[aria-hidden="true"]') || el.closest('.pane.docked') && document.querySelector('.pane.sheet')) continue
      const r = el.getBoundingClientRect(); let w = r.width, h = r.height
      const a = getComputedStyle(el, '::after')
      if (a.content !== 'none' && a.position === 'absolute') {
        const v = k => { const x = parseFloat(a[k]); return Number.isFinite(x) ? x : 0 }
        w = Math.max(w, r.width - Math.min(0, v('left')) - Math.min(0, v('right')))
        h = Math.max(h, r.height - Math.min(0, v('top')) - Math.min(0, v('bottom')))
      }
      n++
      if (w < 43.5 || h < 43.5) bad.push(`${el.tagName.toLowerCase()}.${String(el.className).split(' ')[0] || ''} "${(el.getAttribute('aria-label') || el.textContent || '').trim().slice(0, 28)}" ${Math.round(w)}×${Math.round(h)}`)
    }
    return { n, bad }
  }
  // Layout positions (transforms excluded: the − / + scale in place by design).
  window.__pos = sel => [...document.querySelectorAll(sel)].filter(el => el.getClientRects().length).map(el => {
    if (el.closest('.rv')) { let x = 0, y = 0, n = el.closest('.rv'); while (n) { x += n.offsetLeft; y += n.offsetTop; n = n.offsetParent } return [x, y, 0, 0] }
    const r = el.getBoundingClientRect(); return [r.x + scrollX, r.y + scrollY, r.width, r.height]
  })
}
const probe = async p => { await p.evaluate(PROBES) }
const maxShift = (a, b) => a.length !== b.length ? Infinity : Math.max(0, ...a.flatMap((r, i) => r.map((v, j) => Math.abs(v - b[i][j]))))

// ---------- 1 · Every state, both widths, both themes: screenshots + static checks ----------
for (const theme of ['light', 'dark']) {
  for (const [w, width] of [[1440, 'desktop'], [400, 'phone']]) {
    const p = await page(w)
    for (const st of STATES) {
      await go(p, `?state=${st}&theme=${theme}&width=${width}`); await probe(p)
      await p.evaluate(() => document.getElementById('stage').scrollIntoView({ block: 'start' }))
      await p.screenshot({ path: `${SHOTS}/${w}-${theme}-${st}.png` })
      const tag = `${w} ${theme} ${st}`
      const edge = await p.evaluate(() => __edge()), gl = await p.evaluate(() => __glyphs()), en = await p.evaluate(() => __english()), av = await p.evaluate(() => __avatars()), hs = await p.evaluate(() => __hscroll())
      ok(!edge.length, `${tag}: no edge accents${edge.length ? ` · ${edge.slice(0, 3).join(' | ')}` : ''}`)
      ok(!gl.length, `${tag}: SVG-only icons${gl.length ? ` · text glyphs: ${gl.slice(0, 3).join(' | ')}` : ''}`)
      ok(!en.length, `${tag}: English only${en.length ? ` · ${en.slice(0, 5).join(', ')}` : ''}`)
      ok(!av.bad.length, `${tag}: ${av.n} avatars, each with exactly one overlay (the state badge) and an aria-label${av.bad.length ? ` · ${av.bad.slice(0, 2).join(' | ')}` : ''}`)
      const hn = await p.evaluate(() => __harness()); ok(!hn.bad.length, `${tag}: no harness mark twice in a row (${hn.rows} rows${hn.compact ? `, ${hn.compact} compact with the opener` : ''})${hn.bad.length ? ` · ${hn.bad.slice(0, 2).join(' | ')}` : ''}`)
      const order = await p.evaluate(() => [...document.querySelectorAll('.places > [data-place]')].map(e => e.dataset.place).join(' · '))
      ok(order === 'projects · agents · business · desk', `${tag}: header order ${order}`)
      if (width === 'phone') ok(!hs.doc && !hs.inner.length, `${tag}: no horizontal scroll at 390${hs.doc || hs.inner.length ? ` · ${hs.inner.join(', ')}` : ''}`)
      if (width === 'phone' && theme === 'light' && ['busy', 'none', 'error'].includes(st)) { const t = await p.evaluate(() => __targets()); ok(!t.bad.length, `${tag}: ${t.n} touch targets ≥ 44 px${t.bad.length ? ` · ${t.bad.slice(0, 4).join(' | ')}` : ''}`) }
    }
    // Extras per theme and width: the split-button preview, the lead panel, side by side, sticky.
    await go(p, `?state=busy&theme=${theme}&width=${width}`); await probe(p)
    await p.evaluate(() => document.getElementById('stage').scrollIntoView({ block: 'start' }))
    await p.click('.place-more'); await p.waitForTimeout(200)
    await p.screenshot({ path: `${SHOTS}/${w}-${theme}-desk-split.png` })
    ok(await p.evaluate(() => !document.getElementById('popover').hidden && /Decision Desk/.test(document.getElementById('popover').innerText)), `${w} ${theme}: the header split button opens the Desk preview`)
    if (width === 'desktop') {
      await p.hover('.place-more'); await p.waitForTimeout(150)
      ok(await p.evaluate(() => document.getElementById('tip').hidden), `${w} ${theme}: the ▾ tooltip stays hidden while its preview is open`)
      await p.keyboard.press('Escape'); await p.click('[data-pop="acc"]'); await p.hover('[data-pop="acc"]'); await p.waitForTimeout(150)
      ok(await p.evaluate(() => document.getElementById('tip').hidden), `${w} ${theme}: the accounts chip tooltip stays hidden while its popover is open`)
      await p.keyboard.press('Escape'); await p.waitForTimeout(80)
    }
    if (width === 'phone' && theme === 'light') { const t = await p.evaluate(() => __targets()); ok(!t.bad.length, `${w} ${theme}: Desk preview, ${t.n} touch targets ≥ 44 px${t.bad.length ? ` · ${t.bad.slice(0, 4).join(' | ')}` : ''}`); const gl = await p.evaluate(() => __glyphs()); ok(!gl.length, `${w} ${theme}: Desk preview SVG-only`) }
    await go(p, `?state=busy&theme=${theme}&width=${width}&open=AEON-LEAD`); await probe(p)
    await p.evaluate(() => document.getElementById('stage').scrollIntoView({ block: 'start' }))
    await p.screenshot({ path: `${SHOTS}/${w}-${theme}-lead-chat.png` })
    ok(await p.evaluate(() => document.querySelector('[role=tab][aria-selected=true]')?.textContent.startsWith('Chat')), `${w} ${theme}: a lead that asks you opens on Chat`)
    if (width === 'phone') {
      const hs = await p.evaluate(() => __hscroll()); ok(!hs.doc && !hs.inner.length, `${w} ${theme}: the lead sheet has no horizontal scroll`)
      const pl = await p.evaluate(() => { const b = document.querySelector('.ph-scroll .row[data-row="AEON-LEAD"] .l1 b'); return b ? b.scrollWidth <= b.clientWidth + 0.5 : true })
      ok(pl, `${w} ${theme}: on the phone the lead name shows in full`)
      if (theme === 'light') { for (const tab of ['lead', 'session', 'chat']) { await p.click(`[data-tab="${tab}"]`); await p.waitForTimeout(120); const t = await p.evaluate(() => __targets()); ok(!t.bad.length, `${w} light: sheet on ${tab}, ${t.n} touch targets ≥ 44 px${t.bad.length ? ` · ${t.bad.slice(0, 4).join(' | ')}` : ''}`) } }
      await p.click('[data-tab="lead"]'); await p.waitForTimeout(100); await p.screenshot({ path: `${SHOTS}/${w}-${theme}-lead-sheet.png` })
    } else {
      await go(p, `?state=busy&theme=${theme}&lay=focus&open=AEON-LEAD`); await probe(p)
      await p.evaluate(() => document.querySelector('.ag-head').scrollIntoView({ block: 'start' })); await p.waitForTimeout(150)
      await p.screenshot({ path: `${SHOTS}/${w}-${theme}-focus.png` })
      const hn = await p.evaluate(() => __harness()); ok(!hn.bad.length && hn.compact > 20, `${w} ${theme} focus: ${hn.compact} compact rows open with the harness mark and its aria-label, never twice`)
      const lead = await p.evaluate(() => { const b = document.querySelector('.ag-main .row[data-row="AEON-LEAD"] .l1 b'); return { full: b.scrollWidth <= b.clientWidth + 0.5, chip: getComputedStyle(document.querySelector('.ag-main .row[data-row="AEON-LEAD"] .q-l2')).display !== 'none' } })
      ok(lead.full && lead.chip, `${w} ${theme} focus: the lead name shows in full; the queue chip starts line 2`)
      const e2 = await p.evaluate(() => __edge()); ok(!e2.length, `${w} ${theme} focus: no edge accents`)
    }
    // Sticky: scroll far; the controls row sits at the header's edge with the same box and the same insides.
    await go(p, `?state=busy&theme=${theme}&width=${width}`); await probe(p)
    const box = () => p.evaluate(() => { const st = document.querySelector('[data-stick]'), r = st.getBoundingClientRect(), hd = document.querySelector('.app-hd').getBoundingClientRect(); return { w: r.width, h: r.height, top: r.top, hd: hd.bottom, kids: [...st.querySelectorAll('*')].filter(e => e.getClientRects().length && !e.closest('.rv')).map(e => { const k = e.getBoundingClientRect(); return [k.x - r.x, k.y - r.y, k.width, k.height] }) } })
    const b0 = await box()
    await p.evaluate(() => { const s = document.getElementById('ph-scroll'); if (s) s.scrollTop = 900; else scrollTo(0, 1100) }); await p.waitForTimeout(250)
    const b1 = await box()
    ok(Math.abs(b1.top - b1.hd) <= 0.5 && Math.abs(b1.w - b0.w) <= 0.5 && Math.abs(b1.h - b0.h) <= 0.5 && maxShift(b0.kids, b1.kids) <= 0.5, `${w} ${theme}: sticky controls row stays at the header edge (gap ${(b1.top - b1.hd).toFixed(1)} px), box and insides unchanged (max shift ${maxShift(b0.kids, b1.kids).toFixed(2)} px)`)
    await p.screenshot({ path: `${SHOTS}/${w}-${theme}-sticky.png` })
    await p.close()
  }
}

// ---------- 2 · Zero shift: hover ⋯, the dial reveal, fold/unfold, Collapse all, the divider drag ----------
for (const theme of ['light', 'dark']) {
  const p = await page(1440)
  // Hover ⋯ (full list and side by side): every row keeps its parts in place.
  for (const q of ['?state=busy&unfold=1', '?state=busy&unfold=1&lay=focus&open=OPS']) {
    await go(p, q + `&theme=${theme}`); await probe(p); await p.mouse.move(2, 990)
    const sel = '.ag-main .row[data-row="AEON-1182"] *'
    const a0 = await p.evaluate(s => __pos(s), sel), op0 = await p.evaluate(() => +getComputedStyle(document.querySelector('.row[data-row="AEON-1182"] .row-more')).opacity)
    await p.hover('.ag-main .row[data-row="AEON-1182"]'); await p.waitForTimeout(200)
    const a1 = await p.evaluate(s => __pos(s), sel), op1 = await p.evaluate(() => +getComputedStyle(document.querySelector('.row[data-row="AEON-1182"] .row-more')).opacity)
    ok(maxShift(a0, a1) <= 0.5 && op0 === 0 && op1 === 1, `${theme} ${q.includes('focus') ? 'focus' : 'full list'}: hover ⋯ appears in place (max shift ${maxShift(a0, a1).toFixed(2)} px, opacity ${op0} → ${op1})`)
  }
  // The dial at 1440: units bound by proximity (no outline, no fill; number within 8 px of its icon); − / + sit in reserved
  // room, muted, and appear on hover with nothing moving.
  await go(p, `?state=busy&theme=${theme}`); await probe(p); await p.mouse.move(2, 990)
  const units = await p.evaluate(() => [...document.querySelectorAll('.du')].map(u => { const cs = getComputedStyle(u), r = u.getBoundingClientRect(), icon = u.querySelector('.du-face > :first-child').getBoundingClientRect(), n = u.querySelector('.du-n').getBoundingClientRect(); return { bare: /rgba\(0, 0, 0, 0\)|transparent/.test(cs.backgroundColor) && cs.boxShadow === 'none' && parseFloat(cs.borderTopWidth) === 0, near: n.left - icon.right <= 8 && n.left - icon.right >= 0, inside: n.left >= r.left - 0.5 && n.right <= r.right + 0.5, hidden: [...u.querySelectorAll('.dv')].every(d => +getComputedStyle(d).opacity === 0 && getComputedStyle(d).position !== 'absolute') } }))
  ok(units.length === 4 && units.every(u => u.bare && u.near && u.inside && u.hidden), `${theme} 1440: ${units.length} dial units without outline or fill, each number within 8 px of its icon, − / + hidden in reserved room`)
  const c0 = await p.evaluate(s => __pos(s), '.cap-row *')
  await p.hover('.du[data-unit="claude"] .du-face'); await p.waitForTimeout(220)
  const c1 = await p.evaluate(s => __pos(s), '.cap-row *'), shown = await p.evaluate(() => [...document.querySelectorAll('.du[data-unit="claude"] .dv')].every(d => +getComputedStyle(d).opacity === 1))
  ok(shown && maxShift(c0, c1) <= 0.5, `${theme} 1440: hovering a unit shows its − / + in place (${maxShift(c0, c1).toFixed(2)} px)`)
  await p.close()
}
// The dial when tight (900): hover or focus grows one unit as an overlay; nothing moves; neighbours are covered, not pushed.
for (const theme of ['light', 'dark']) {
  const p = await page(900)
  await go(p, `?state=busy&theme=${theme}`); await probe(p); await p.mouse.move(2, 990)
  const cap = '.cap-row *, .ag-head *'
  const u0 = await p.evaluate(s => __pos(s), cap)
  const tight = await p.evaluate(() => [...document.querySelectorAll('.du')].every(u => { const r = u.getBoundingClientRect(), n = u.querySelector('.du-n').getBoundingClientRect(); return n.left >= r.left - 0.5 && n.right <= r.right + 0.5 }) && [...document.querySelectorAll('.du .dv')].every(d => +getComputedStyle(d).opacity === 0))
  ok(tight, `${theme} 900: tight row shows only the units, each number inside its unit`)
  for (const k of ['claude', 'codex']) {
    await p.hover(`.du[data-unit="${k}"] .du-face`); await p.waitForTimeout(230)
    const u1 = await p.evaluate(s => __pos(s), cap), st = await p.evaluate(k => { const u = document.querySelector(`.du[data-unit="${k}"]`); return { dv: [...u.querySelectorAll('.dv')].map(d => +getComputedStyle(d).opacity), plate: +getComputedStyle(u, '::before').opacity, z: getComputedStyle(u).zIndex } }, k)
    const noRing = await p.evaluate(k => !/inset/.test(getComputedStyle(document.querySelector(`.du[data-unit="${k}"]`), '::before').boxShadow), k)
    ok(noRing, `${theme} 900: the ${k} plate floats with a shadow, no outline`)
    ok(maxShift(u0, u1) <= 0.5 && st.dv.every(v => v === 1) && st.plate === 1, `${theme} 900: hovering the ${k} unit opens it as an overlay (− / + shown, z ${st.z}); nothing moves (${maxShift(u0, u1).toFixed(2)} px)`)
    await p.mouse.move(2, 990); await p.waitForTimeout(200)
  }
  await p.screenshot({ path: `${SHOTS}/900-${theme}-dial-tight.png` })
  await p.hover('.du[data-unit="claude"] .du-face'); await p.waitForTimeout(230); await p.screenshot({ path: `${SHOTS}/900-${theme}-dial-unit-open.png` })
  await p.close()
}
for (const theme of ['light', 'dark']) {
  const p = await page(1440)
  // Fold / unfold one group: rows above it and the fold button stay put.
  await go(p, `?state=busy&unfold=1&theme=${theme}`); await probe(p)
  await p.evaluate(() => document.querySelector('[data-fold="MOBILE-LEAD"]').scrollIntoView({ block: 'center' })); await p.waitForTimeout(80)
  const above = '.grp:nth-child(-n+3) .row *, .tree-head *'
  const f0 = await p.evaluate(s => __pos(s), above), fb0 = await p.evaluate(() => { const r = document.querySelector('[data-fold="MOBILE-LEAD"]').getBoundingClientRect(); return [r.x, r.y] })
  await p.click('[data-fold="MOBILE-LEAD"]'); await p.waitForTimeout(120)
  const f1 = await p.evaluate(s => __pos(s), above), fb1 = await p.evaluate(() => { const r = document.querySelector('[data-fold="MOBILE-LEAD"]').getBoundingClientRect(); return [r.x, r.y] })
  const hiddenW = await p.evaluate(() => !document.querySelector('.row[data-row="MOBILE-12"]'))
  await p.click('[data-fold="MOBILE-LEAD"]'); await p.waitForTimeout(120)
  const fb2 = await p.evaluate(() => { const r = document.querySelector('[data-fold="MOBILE-LEAD"]').getBoundingClientRect(); return [r.x, r.y] })
  ok(maxShift(f0, f1) <= 0.5 && maxShift([fb0], [fb1]) <= 0.5 && maxShift([fb0], [fb2]) <= 0.5 && hiddenW, `${theme}: fold/unfold MOBILE-LEAD moves nothing above it and keeps its toggle in place`)
  // Collapse all / Expand all: no shift in the list head; every group folds, then unfolds; the label follows.
  await p.evaluate(() => document.querySelector('.tree-head').scrollIntoView({ block: 'center' })); await p.waitForTimeout(80)
  const head = '.tree-head *'
  const h0 = await p.evaluate(s => __pos(s), head), workers0 = await p.evaluate(() => document.querySelectorAll('.row.worker').length), lab0 = await p.evaluate(() => document.querySelector('[data-fold-all]').getAttribute('aria-label'))
  await p.click('[data-fold-all]'); await p.waitForTimeout(120)
  const h1 = await p.evaluate(s => __pos(s), head), st1 = await p.evaluate(() => ({ workers: document.querySelectorAll('.row.worker').length, open: [...document.querySelectorAll('.lead-row[aria-expanded]')].filter(r => r.getAttribute('aria-expanded') === 'true').length, label: document.querySelector('[data-fold-all]').getAttribute('aria-label') }))
  await p.screenshot({ path: `${SHOTS}/1440-${theme}-collapsed-all.png` })
  await p.click('[data-fold-all]'); await p.waitForTimeout(120)
  const h2 = await p.evaluate(s => __pos(s), head), st2 = await p.evaluate(() => ({ workers: document.querySelectorAll('.row.worker').length, closed: [...document.querySelectorAll('.lead-row[aria-expanded]')].filter(r => r.getAttribute('aria-expanded') === 'false').length, label: document.querySelector('[data-fold-all]').getAttribute('aria-label') }))
  ok(lab0 === 'Collapse all' && st1.workers === 0 && st1.open === 0 && st1.label === 'Expand all' && st2.workers === workers0 && st2.closed === 0 && st2.label === 'Collapse all' && maxShift(h0, h1) <= 0.5 && maxShift(h0, h2) <= 0.5,
    `${theme}: Collapse all folds every group (${workers0} → ${st1.workers} workers, label “${st1.label}”), Expand all restores them (${st2.workers}, “${st2.label}”), list head shift ${Math.max(maxShift(h0, h1), maxShift(h0, h2)).toFixed(2)} px`)
  // Layout states: cycling moves nothing above the layout; the details are the full height under the header in both splits.
  await go(p, `?state=busy&theme=${theme}`); await probe(p)
  // In document coordinates: entering a split scrolls the page on purpose (the work area under the header); the sticky app
  // header is checked in the button test.
  const outside = '.ag-head *, .cap-row *'
  const l0 = await p.evaluate(s => __pos(s), outside)
  const seen = []
  for (let i = 0; i < 3; i++) {
    await p.click('[data-layout-cycle]'); await p.waitForTimeout(340)
    const st = await p.evaluate(() => { const pg = document.querySelector('.ag-page'), pane = document.querySelector('.pane.docked'), hd = document.querySelector('.app-hd'); return { lay: ['focus', 'overview', 'list'].find(l => pg.classList.contains(`lay-${l}`)), paneH: pane ? pane.getBoundingClientRect().height : 0, want: innerHeight - hd.getBoundingClientRect().height - 8, btn: document.querySelector('[data-layout-cycle]').getAttribute('aria-label') } })
    const l1 = await p.evaluate(s => __pos(s), outside)
    seen.push(st.lay)
    ok(maxShift(l0, l1) <= 0.5, `${theme}: cycling to ${st.lay} moves nothing above the layout (${maxShift(l0, l1).toFixed(2)} px) · “${st.btn}”`)
    if (st.lay !== 'list') {
      ok(Math.abs(st.paneH - st.want) <= 1, `${theme} ${st.lay}: the details are the full height under the header (${Math.round(st.paneH)} of ${Math.round(st.want)} px)`)
      const acts = await p.evaluate(() => [...document.querySelectorAll('.pane.docked .pane-bar .hd-acts button')].map(b => b.getAttribute('aria-label')))
      const noFoot = await p.evaluate(() => !document.querySelector('.pane .ctl, .pane .pane-foot'))
      ok(acts.length === 3 && acts[0] === 'Interrupt' && /^(Pause|Pause…|Resume)$/.test(acts[1]) && acts[2] === 'Stop…' && noFoot, `${theme} ${st.lay}: Interrupt · ${acts[1]} · Stop… sit in the panel head`)
      await p.screenshot({ path: `${SHOTS}/1440-${theme}-${st.lay}.png` })
    }
  }
  ok(seen.join(' → ') === 'focus → overview → list', `${theme}: the layout button cycles ${seen.join(' → ')}`)
  // Actions in the head on every tab, for a worker too; the NIX lead's details never end short.
  for (const [q, tabs] of [['open=AEON-1160', ['overview', 'chat']], ['open=NIX-LEAD', ['lead', 'session', 'chat']]]) {
    await go(p, `?state=busy&theme=${theme}&${q}`)
    for (const tab of tabs) { await p.click(`[data-tab="${tab}"]`); await p.waitForTimeout(80); const r = await p.evaluate(() => ({ n: document.querySelectorAll('.pane-bar .hd-acts button').length, h: document.querySelector('.pane.docked').getBoundingClientRect().height, want: innerHeight - document.querySelector('.app-hd').getBoundingClientRect().height - 8 })); ok(r.n === 3 && Math.abs(r.h - r.want) <= 1, `${theme} ${q.slice(5)} on ${tab}: actions in the head, details full height (${Math.round(r.h)} px)`) }
  }
  // Divider in both splits: only the two columns change; head, capacity row and the panel's right edge stay.
  for (const lay of ['focus', 'overview']) {
    await go(p, `?state=busy&lay=${lay}&open=AEON-LEAD&theme=${theme}`); await probe(p)
    await p.evaluate(() => document.querySelector('.ag-layout').scrollIntoView({ block: 'center' })); await p.waitForTimeout(100)
    const still = '.ag-head *, .cap-row *'
    const s0 = await p.evaluate(s => __pos(s), still), dv = await p.locator('[data-divider]').boundingBox(), r0 = await p.evaluate(() => [document.querySelector('.ag-main').getBoundingClientRect().width, document.querySelector('.pane').getBoundingClientRect().width, document.querySelector('.pane').getBoundingClientRect().right])
    const dy = Math.max(dv.y + 10, Math.min(dv.y + dv.height - 10, 500)), dx = lay === 'focus' ? 60 : -60
    await p.mouse.move(dv.x + 8, dy); await p.mouse.down()
    for (let i = 1; i <= 6; i++) await p.mouse.move(dv.x + 8 + i * dx / 6, dy)
    const s1 = await p.evaluate(s => __pos(s), still), r1 = await p.evaluate(() => [document.querySelector('.ag-main').getBoundingClientRect().width, document.querySelector('.pane').getBoundingClientRect().width, document.querySelector('.pane').getBoundingClientRect().right])
    await p.mouse.up(); await p.waitForTimeout(80)
    const now = await p.evaluate(() => document.querySelector('[data-divider]').getAttribute('aria-valuenow'))
    ok(maxShift(s0, s1) <= 0.5 && Math.abs(r1[0] - r0[0] - dx) <= 1 && Math.abs(r1[1] - r0[1] + dx) <= 1 && Math.abs(r1[2] - r0[2]) <= 0.5,
      `${theme} ${lay}: divider drag ${dx > 0 ? '+' : ''}${dx} px changes only the two columns (list ${Math.round(r0[0])} → ${Math.round(r1[0])}, details ${Math.round(r0[1])} → ${Math.round(r1[1])}, aria-valuenow ${now}); head and capacity row shift ${maxShift(s0, s1).toFixed(2)} px`)
  }
  await p.close()
}

// ---------- 4 · Round 7: the layout button never moves; the divider is the truth; details start at the top ----------
for (const theme of ['light', 'dark']) {
  const p = await page(1440)
  const btnBox = () => p.evaluate(() => { const r = document.querySelector('[data-layout-cycle]').getBoundingClientRect(); return [r.x, r.y, r.width, r.height] })
  const toWork = () => p.evaluate(() => { const w = document.querySelector('.work').getBoundingClientRect().top, hd = document.querySelector('.app-hd').getBoundingClientRect().height; scrollTo(0, scrollY + w - hd) })
  const layNow = () => p.evaluate(() => ['focus', 'overview', 'list'].find(l => document.querySelector('.ag-page').classList.contains(`lay-${l}`)))
  // The button: the same box in every state, and at every scroll position once the work area has reached the header.
  await go(p, `?state=busy&unfold=1&theme=${theme}`); await probe(p); await toWork(); await p.waitForTimeout(120)
  const b0 = await btnBox(), boxes = []
  for (const dy of [400, 1500]) { await p.evaluate(dy => scrollBy(0, dy), dy); await p.waitForTimeout(100); boxes.push(['list +' + dy, await btnBox()]) }
  await toWork(); await p.waitForTimeout(100)
  for (let i = 0; i < 3; i++) { await p.click('[data-layout-cycle]'); await p.waitForTimeout(360); boxes.push([await layNow(), await btnBox()]) }
  const worst = Math.max(...boxes.map(([, b]) => maxShift([b0], [b])))
  ok(worst <= 0.5, `${theme}: the layout button keeps one box across ${boxes.map(([k]) => k).join(', ')} (max ${worst.toFixed(2)} px)`)
  // Picking the last row of a long list: the details start at their top, under the header; the row stays in view.
  await go(p, `?state=busy&unfold=1&ended=1&theme=${theme}`); await probe(p)
  await p.evaluate(() => scrollTo(0, document.body.scrollHeight)); await p.waitForTimeout(150)
  const lastId = await p.evaluate(() => { const rows = [...document.querySelectorAll('.tree-body .row[data-row]')]; const r = rows[rows.length - 1]; r.scrollIntoView({ block: 'center' }); return r.dataset.row })
  await p.waitForTimeout(100); await p.click(`.row[data-row="${lastId}"]`); await p.waitForTimeout(420)
  const pick = await p.evaluate(id => { const pane = document.querySelector('.pane.docked').getBoundingClientRect(), hd = document.querySelector('.app-hd').getBoundingClientRect(), body = document.querySelector('.pane.docked .pane-body'), row = document.querySelector(`.ag-main .row[data-row="${id}"]`).getBoundingClientRect(), list = document.querySelector('.split .tree-body').getBoundingClientRect(); return { top: pane.top, hd: hd.bottom, scroll: body.scrollTop, rowIn: row.top >= list.top - 0.5 && row.bottom <= list.bottom + 0.5 } }, lastId)
  ok(Math.abs(pick.top - pick.hd) <= 0.5 && pick.scroll === 0 && pick.rowIn, `${theme}: picking the last row (${lastId}) shows the details from their top under the header (gap ${(pick.top - pick.hd).toFixed(1)} px), the picked row in view`)
  await p.screenshot({ path: `${SHOTS}/1440-${theme}-pick-last-row.png` })
  // The details' own scroll resets when the selection changes.
  await p.evaluate(() => { document.querySelector('.pane.docked .pane-body').scrollTop = 300 })
  await p.click('.ag-main .row[data-row="AEON-1160"]'); await p.waitForTimeout(200)
  ok(await p.evaluate(() => document.querySelector('.pane.docked .pane-body').scrollTop === 0), `${theme}: a new selection shows its details from the top`)
  // Drag past 380 px of details: closes on release (List only). Drag far left: the list stops at 280 px.
  await go(p, `?state=busy&open=OPS&lay=overview&top=1&theme=${theme}`); await probe(p)
  let dv = await p.locator('[data-divider]').boundingBox()
  await p.mouse.move(dv.x + 8, 500); await p.mouse.down(); await p.mouse.move(dv.x + 8 + 260, 500, { steps: 8 })
  const dim = await p.evaluate(() => document.querySelector('.ag-page').classList.contains('will-close'))
  await p.mouse.up(); await p.waitForTimeout(380)
  ok(dim && await layNow() === 'list' && !(await p.evaluate(() => !!document.querySelector('.pane.docked'))), `${theme}: dragging the handle so the details would be under 380 px closes them on release (List only)`)
  await go(p, `?state=busy&open=OPS&lay=focus&top=1&theme=${theme}`); await probe(p)
  dv = await p.locator('[data-divider]').boundingBox()
  await p.mouse.move(dv.x + 8, 500); await p.mouse.down(); await p.mouse.move(dv.x - 300, 500, { steps: 8 }); await p.mouse.up(); await p.waitForTimeout(120)
  const minW = await p.evaluate(() => Math.round(document.querySelector('.ag-main').getBoundingClientRect().width))
  ok(minW === 280 && await layNow() === 'focus', `${theme}: dragging left stops at a 280 px list (${minW} px); no details-only state`)
  // Crossing 560 px switches the list's form, and only that: head and capacity rows stay, the details follow the pointer
  // exactly, and the top visible row stays the top visible row.
  await go(p, `?state=busy&open=AEON-LEAD&lw=600&top=1&theme=${theme}`); await probe(p)
  dv = await p.locator('[data-divider]').boundingBox()
  const still = '.ag-head *, .cap-row *'
  const snap = () => p.evaluate(() => { const list = document.querySelector('.split .tree-body').getBoundingClientRect(), rows = [...document.querySelectorAll('.split .tree-body .row[data-row]')], top = rows.find(r => r.getBoundingClientRect().bottom > list.top + 2); const pane = document.querySelector('.pane.docked').getBoundingClientRect(); return { top: top?.dataset.row, left: pane.left, right: pane.right, form: document.querySelector('.ag-page').classList.contains('side') ? 'compact' : 'table' } })
  const s0 = await p.evaluate(s => __pos(s), still), f0 = await snap()
  await p.mouse.move(dv.x + 8, 500); await p.mouse.down(); await p.mouse.move(dv.x + 8 - 30, 500, { steps: 3 })
  const fA = await snap()
  await p.mouse.move(dv.x + 8 - 60, 500, { steps: 3 })
  const fB = await snap(), s1 = await p.evaluate(s => __pos(s), still)
  await p.mouse.up(); await p.waitForTimeout(80)
  ok(f0.form === 'table' && fA.form === 'table' && fB.form === 'compact' && fA.top === fB.top && f0.top === fB.top && Math.abs((fA.left - f0.left) + 30) <= 1 && Math.abs((fB.left - f0.left) + 60) <= 1 && Math.abs(fB.right - f0.right) <= 0.5 && maxShift(s0, s1) <= 0.5,
    `${theme}: crossing 560 px turns the table compact (${f0.form} → ${fA.form} → ${fB.form}); the top row stays ${fB.top}, the details follow the pointer, nothing else moves`)
  await p.close()
}
// No split when both presets do not fit (1000 px window): a session opens as a sheet; the layout button is hidden.
{
  const p = await page(1000)
  await go(p, '?state=busy')
  ok(!(await p.evaluate(() => !!document.querySelector('[data-layout-cycle]'))), '1000: no layout button (no split)')
  await p.evaluate(() => document.querySelector('.ag-main .row[data-row="AEON-LEAD"]').scrollIntoView({ block: 'center' })); await p.click('.ag-main .row[data-row="AEON-LEAD"]'); await p.waitForTimeout(250)
  ok(await p.evaluate(() => !!document.querySelector('.pane.sheet') && !document.querySelector('.pane.docked')), '1000: a session opens as a sheet over the list')
  await p.screenshot({ path: `${SHOTS}/1000-light-sheet.png` })
  await p.keyboard.press('Escape'); await p.waitForTimeout(150)
  ok(await p.evaluate(() => !document.querySelector('.pane.sheet')), '1000: Esc closes the sheet')
  await p.close()
}

// ---------- 3 · Keys: F, / and S; nothing fires inside text fields ----------
for (const [w, width] of [[1440, 'desktop'], [400, 'phone']]) {
  const p = await page(w)
  await go(p, `?state=busy&width=${width}`)
  const side = () => p.evaluate(() => !document.querySelector('.ag-page').classList.contains('lay-list'))
  const active = () => p.evaluate(() => document.activeElement?.id || document.activeElement?.tagName)
  await p.evaluate(() => document.activeElement?.blur())
  await p.keyboard.press('f'); ok(await active() === 'q', `${w}: F focuses the page search`)
  await p.keyboard.type('s/fj'); ok(await p.evaluate(() => document.getElementById('q').value) === 's/fj' && !(await side()), `${w}: typing “s/fj” in the search only types`)
  await p.evaluate(() => { const q = document.getElementById('q'); q.value = ''; q.dispatchEvent(new Event('input', { bubbles: true })); document.activeElement.blur() })
  await p.keyboard.press('/'); ok(await active() === 'q', `${w}: / focuses the page search`)
  await p.evaluate(() => document.activeElement.blur())
  await p.keyboard.press('s'); await p.waitForTimeout(300)
  if (width === 'desktop') {
    const lay = () => p.evaluate(() => ['focus', 'overview', 'list'].find(l => document.querySelector('.ag-page').classList.contains(`lay-${l}`)))
    ok(await lay() === 'focus', `${w}: S from List only goes to Focus (and opens the first lead)`)
    await p.keyboard.press('s'); await p.waitForTimeout(300); ok(await lay() === 'overview', `${w}: S again goes to Overview`)
    await p.keyboard.press('s'); await p.waitForTimeout(380); ok(await lay() === 'list' && !(await p.evaluate(() => !!document.querySelector('.pane.docked'))), `${w}: S again goes to List only (no details)`)
    await p.click('.row[data-row="OPS"]'); await p.waitForTimeout(340); ok(await lay() === 'overview', `${w}: clicking a session in List only opens it in the split used last (Overview)`)
    await p.keyboard.press('Escape'); await p.waitForTimeout(380); ok(await lay() === 'list', `${w}: Esc returns to List only`)
    await go(p, `?state=busy&open=AEON-LEAD&tab=chat`)
    const lay0 = await p.evaluate(() => document.querySelector('.ag-page').className)
    await p.focus('.composer textarea'); await p.keyboard.type('jksf/'); await p.waitForTimeout(300)
    ok(await p.evaluate(() => document.querySelector('.composer textarea').value) === 'jksf/' && await p.evaluate(() => document.querySelector('.ag-page').className) === lay0 && await active() === 'TEXTAREA', `${w}: typing “jksf/” in the chat composer only types (layout unchanged)`)
  } else ok(!(await side()) && !(await p.evaluate(() => !!document.querySelector('[data-layout-cycle]'))), `${w}: S does nothing on the phone; the layout button is hidden (list → detail only)`)
  await p.close()
}

out.push(`page errors: ${errors.length ? errors.slice(0, 5).join(' | ') : 'none'}`)
out.push(fails.length ? `FAILED ${fails.length} of ${out.length - 1}` : `ALL PASS (${out.length - 1} checks)`)
fs.writeFileSync('check.txt', out.join('\n') + '\n')
console.log(out.filter(l => !l.startsWith('PASS')).join('\n') || 'no failures'); console.log(out[out.length - 1])
await browser.close()
