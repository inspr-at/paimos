// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1211 (design AEON-1196 r7, package P3): state in the avatar, the Time column, compact rows
// and calm enter/leave. Risk: an avatar loses its state badge or its spoken state, or carries the
// harness again; a refresh adds, moves or removes a row under the pointer; held changes never
// apply; an ended session vanishes at once instead of keeping its place for 10 minutes; a phone
// scrolls sideways or gets a target under 44 px. Time is the page clock, never a sleep.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

const NOW = Date.parse('2026-10-11T10:00:00Z')
const ago = (minutes: number) => new Date(NOW - minutes * 60_000).toISOString()
const STATE_WORDS = ['Working', 'Needs something', 'Throttled', 'Problem', 'No heartbeat', 'Idle', 'Done', 'Ended', 'Paused', 'Pausing', 'Awaiting heartbeat', 'Idle · no heartbeat']
const DONE = '5e000000-0000-4000-8000-000000000031'

async function setup(page: Page, theme: 'light' | 'dark' = 'light') {
  await page.clock.install({ time: NOW })
  const work = fixtures(); work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  const data = agentData({
    now: NOW, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
    nodes: { 'n-1': { key: 'PHAROS-7', title: 'Fleet list' }, 'n-2': { key: 'PHAROS-12', title: 'Wiederherstellung der Sicherungskopien für alle Produktionsserver überprüfen' } },
  })
  const worker = data.sessions[1]!
  Object.assign(worker, { model: 'gpt-6.1-sol', reasoning_effort: 'xhigh', account_label: 'ChatGPT Pro', agent_activity_mode: 'agent_summary', current_activity: { text: 'Überprüfe die Wiederherstellungsprotokolle auf allen Rechnern im Rechenzentrum', source: 'auto', at: ago(1) } })
  Object.assign(data.sessions[0]!, { model: 'claude-opus-5-5', reasoning_effort: 'xhigh', account_label: 'Claude Max' })
  // Done three minutes ago: it keeps its place until 10:07.
  data.sessions.push({ ...data.sessions[0]!, id: DONE, role: 'worker', display_label: 'Release notes worker', ticket_node_id: null, ticket: null, run_id: null, phase: 'stopped', activity: 'idle', heartbeat_at: ago(3.2), stopped_at: ago(3), stop_reason: 'completed', finished: true, created_at: ago(40) })
  await mockAgents(page, data)
  return data
}

const rows = (page: Page) => page.locator('.sessions .row[data-row]')
const row = (page: Page, id: string) => page.locator(`.sessions [data-row="s:${id}"]`)
const note = (page: Page) => page.locator('.sessions [data-held-note]')
// Layout boxes of every row (document coordinates: main's scroll added back).
const boxes = (page: Page) => page.locator('.sessions .row[data-row]').evaluateAll(els => {
  const main = document.querySelector('main')!
  return els.map(el => { const r = el.getBoundingClientRect(); return [el.getAttribute('data-row'), Math.round(r.x), Math.round(r.y + main.scrollTop), Math.round(r.width), Math.round(r.height)] })
})

// The design check's probes (design/ag-approved/check.mjs), limited to the Sessions card.
async function probe(page: Page) {
  return page.locator('.sessions').evaluate(card => {
    const visible = (el: Element) => { const r = el.getBoundingClientRect(); const cs = getComputedStyle(el); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none' }
    const edges: string[] = []
    for (const el of card.querySelectorAll('*')) {
      const cs = getComputedStyle(el)
      const bw = (s: string) => parseFloat(cs.getPropertyValue(`border-${s}-width`)) || 0
      for (const [a, b] of [['left', 'right'], ['top', 'bottom']] as const) {
        if (bw(a) >= 2 && bw(a) - bw(b) >= 1) edges.push(`${el.className} border-${a}`)
        if (bw(b) >= 2 && bw(b) - bw(a) >= 1) edges.push(`${el.className} border-${b}`)
      }
      for (const pseudo of ['::before', '::after']) {
        const ps = getComputedStyle(el, pseudo)
        if (ps.content === 'none' || ps.position !== 'absolute') continue
        const w = parseFloat(ps.width) || 0, h = parseFloat(ps.height) || 0
        if (w >= 2 && w <= 6 && h >= 16 && !/rgba\(.*,\s*0\)$/.test(ps.backgroundColor) && ps.backgroundColor !== 'transparent') edges.push(`${el.className} ${pseudo} bar`)
      }
    }
    const glyphs: string[] = []
    const walk = (n: Node) => { for (const c of n.childNodes) { if (c.nodeType === 3 && /[←→↑↓↵⏎✓✔✕✖×▾▸▴▲▼◂▶◀★☆●○◉⚠✗✘➜➔⟶]/.test(c.textContent ?? '')) glyphs.push((c.textContent ?? '').trim()); else if (c.nodeType === 1) walk(c) } }
    walk(card)
    const avatars = [...card.querySelectorAll('.row .ava')].filter(visible).map(a => ({
      role: a.getAttribute('role'), label: a.getAttribute('aria-label') ?? '', state: (a as HTMLElement).dataset.state,
      marks: [...a.querySelectorAll('svg.agent-state-mark')].map(m => ({ mark: (m as SVGElement).dataset.mark, parts: m.children.length })),
      harness: a.querySelectorAll('.harness-badge, .hg').length,
    }))
    const marks = [...card.querySelectorAll('.row[data-row]')].filter(r => r.getClientRects().length).map(r => [...r.querySelectorAll('.op-mark, .exec-icon')].filter(visible).length)
    const hscroll = document.documentElement.scrollWidth > innerWidth + 0.5 || card.scrollWidth > card.clientWidth + 0.5
    const english = (card.querySelector('.th-stick') as HTMLElement).innerText.match(/[äöüÄÖÜß]|\b(und|der|die|das|nicht|mit|für|oder|ist|Sitzung|Agenten|Projekt|Fehler|bitte)\b/g) ?? []
    return { edges, glyphs, avatars, marks, hscroll, english }
  })
}
function expectAvatars(avatars: Awaited<ReturnType<typeof probe>>['avatars']) {
  expect(avatars.length).toBeGreaterThan(3)
  for (const a of avatars) {
    expect(a.role, a.label).toBe('img')
    expect(STATE_WORDS.some(word => a.label.endsWith(`: ${word}`)), `spoken state: ${a.label}`).toBe(true)
    expect(a.marks, `one state badge on ${a.label}`).toHaveLength(1)
    expect(a.marks[0]!.mark, `badge shape follows the state: ${a.label}`).toBe(a.state)
    expect(a.marks[0]!.parts).toBeGreaterThanOrEqual(2)
    expect(a.harness, `no harness on the avatar: ${a.label}`).toBe(0)
  }
}

for (const theme of ['light', 'dark'] as const) {
  for (const width of [1440, 1024]) {
    test(`${theme} ${width}: the state lives in the avatar, Time sits beside the name`, async ({ page }, info) => {
      await page.setViewportSize({ width, height: 1000 })
      await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
      const errors = watchErrors(page)
      const data = await setup(page, theme)
      await page.goto('/agents')
      await expect(row(page, data.sessions[0]!.id)).toBeVisible()
      await expect(page.locator('.sessions .thead [role="columnheader"]')).toHaveText(['Name', 'Time', 'Ticket', 'Execution', 'Tier', 'Host', 'Heartbeat', 'Actions'])
      const lead = row(page, data.sessions[0]!.id)
      await expect(lead.locator('.ava')).toHaveAttribute('aria-label', /^Lead .+: Working$/)
      await expect(lead.locator('.c-time')).toHaveText('1 h 12 min')
      await expect(lead.locator('.c-time')).toHaveAttribute('data-tip', 'Running 1 h 12 min')
      // Wide rows name the model in Execution; no opener mark, no harness twice.
      await expect(lead.locator('.op-mark')).toBeHidden()
      const p = await probe(page)
      expectAvatars(p.avatars)
      expect(p.marks.every(n => n === 1), 'one harness mark per row').toBe(true)
      expect(p.edges).toEqual([])
      expect(p.glyphs).toEqual([])
      expect(p.english).toEqual([])
      expect(p.hscroll).toBe(false)
      // Two lines whatever the state: every row with no tools line has one height.
      const heights = await page.locator('.sessions .row[data-row]:not(:has(.worker-tools, .lineage))').evaluateAll(els => [...new Set(els.map(el => Math.round(el.getBoundingClientRect().height)))])
      expect(heights).toHaveLength(1)
      await page.screenshot({ path: info.outputPath(`${width}-${theme}-rows.png`), fullPage: true })
      expect(errors).toEqual([])
    })
  }

  test(`${theme} 390: compact rows open with the harness mark and end with the time`, async ({ page }, info) => {
    await page.setViewportSize({ width: 390, height: 900 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    const data = await setup(page, theme)
    await page.goto('/agents')
    const worker = row(page, data.sessions[1]!.id)
    await expect(worker).toBeVisible()
    await expect(page.locator('.sessions .thead')).toBeHidden()
    await expect(worker.locator('.op-mark')).toBeVisible()
    await expect(worker.locator('.op-mark')).toHaveAttribute('aria-label', 'gpt-6.1-sol · xhigh · Codex · ChatGPT Pro')
    await expect(worker.locator('.t-inline')).toHaveText('26m')
    await expect(worker.locator('.key-inline')).toHaveText('PHAROS-12 ·')
    // The long German line 2 ends in an ellipsis inside the row; the row stays two lines.
    const fit = await worker.locator('.l2t').evaluate(el => ({ clipped: el.scrollWidth > el.clientWidth, inside: el.getBoundingClientRect().right <= el.closest('.row')!.getBoundingClientRect().right }))
    expect(fit).toEqual({ clipped: true, inside: true })
    const p = await probe(page)
    expectAvatars(p.avatars)
    expect(p.marks.every(n => n === 1), 'compact rows: the opener only').toBe(true)
    expect(p.edges).toEqual([])
    expect(p.glyphs).toEqual([])
    expect(p.hscroll, 'no sideways scroll at 390').toBe(false)
    await page.screenshot({ path: info.outputPath(`390-${theme}-rows.png`), fullPage: true })
  })
}

test.describe('touch', () => {
  test.use({ hasTouch: true, isMobile: true, viewport: { width: 390, height: 900 } })
  test('390: every target in the Sessions card is 44 px', async ({ page }) => {
    await setup(page)
    await page.goto('/agents')
    await expect(rows(page).first()).toBeVisible()
    const small = await page.locator('.sessions').evaluate(card => {
      const out: string[] = []
      for (const el of card.querySelectorAll<HTMLElement>('button, a[href], input:not([type="checkbox"]), select')) {
        const r = el.getBoundingClientRect(), cs = getComputedStyle(el)
        if (!r.width || !r.height || cs.visibility === 'hidden' || el.closest('[aria-hidden="true"]') || el.classList.contains('agent-link')) continue
        const a = getComputedStyle(el, '::after')
        let w = r.width, h = r.height
        if (a.content !== 'none' && a.position === 'absolute') {
          const v = (k: 'left' | 'right' | 'top' | 'bottom') => Math.min(0, parseFloat(a[k]) || 0)
          w = r.width - v('left') - v('right'); h = r.height - v('top') - v('bottom')
        }
        if (w < 43.5 || h < 43.5) out.push(`${el.tagName.toLowerCase()}.${el.className} "${(el.getAttribute('aria-label') || el.textContent || '').trim().slice(0, 28)}" ${Math.round(w)}×${Math.round(h)}`)
      }
      return out
    })
    expect(small).toEqual([])
  })
})

test('a refresh never adds, moves or removes a row under the pointer; the changes wait, then apply', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const data = await setup(page)
  await page.goto('/agents')
  const first = row(page, data.sessions[0]!.id)
  await expect(first).toBeVisible()
  await expect(note(page)).toContainText('Live · updated')
  const noteBox = await note(page).boundingBox()
  // The pointer rests on a row; a new worker arrives and a working one leaves the list.
  await first.locator('.l2').hover()
  const before = await boxes(page)
  const gone = data.sessions.splice(2, 1)[0]!
  data.sessions.push({ ...data.sessions[1]!, id: '5e000000-0000-4000-8000-000000000032', display_label: 'Fresh worker', created_at: new Date(NOW + 1000).toISOString(), heartbeat_at: new Date(NOW + 1000).toISOString() })
  await page.clock.runFor(21_000)
  await expect(note(page)).toContainText('2 changes wait while you point here')
  await expect(note(page).getByRole('button', { name: 'Show now' })).toBeVisible()
  expect(await boxes(page), 'nothing moved, arrived or left under the pointer').toEqual(before)
  await expect(row(page, gone.id)).toBeVisible()
  // The note keeps its box whichever text it shows (AEON-541).
  const heldBox = await note(page).boundingBox()
  expect(Math.abs(heldBox!.x - noteBox!.x) + Math.abs(heldBox!.width - noteBox!.width) + Math.abs(heldBox!.y - noteBox!.y)).toBeLessThanOrEqual(1)
  await page.screenshot({ path: info.outputPath('1440-light-held.png') })
  // The pointer leaves: still held for 1.5 s, then the list follows.
  await page.mouse.move(700, 8)
  await page.clock.runFor(1400)
  await expect(row(page, gone.id)).toBeVisible()
  await page.clock.runFor(200)
  await expect(row(page, gone.id)).toHaveCount(0)
  await expect(row(page, '5e000000-0000-4000-8000-000000000032')).toBeVisible()
  await expect(note(page)).toContainText('Live · updated')
  // Show now applies at once, while the pointer is still on the list.
  await first.locator('.l2').hover()
  data.sessions.push({ ...data.sessions[1]!, id: '5e000000-0000-4000-8000-000000000033', display_label: 'Another worker', created_at: new Date(NOW + 30_000).toISOString() })
  await page.clock.runFor(21_000)
  await expect(note(page)).toContainText('1 change waits while you point here')
  await expect(row(page, '5e000000-0000-4000-8000-000000000033')).toHaveCount(0)
  await note(page).getByRole('button', { name: 'Show now' }).click()
  await expect(row(page, '5e000000-0000-4000-8000-000000000033')).toBeVisible()
})

test('an ended session keeps its place and end state for 10 minutes, then moves to Ended', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await setup(page)
  await page.goto('/agents')
  const done = row(page, DONE)
  await expect(done).toBeVisible()
  await expect(done.locator('.ava')).toHaveAttribute('aria-label', /: Done$/)
  await expect(done.locator('.l2t')).toContainText(/moves to Ended at \d{1,2}:07/)
  await expect(done.locator('.c-time')).toHaveText('3 min ago')
  // It sits among the live rows, not in the folded Ended group.
  await expect(page.locator('.sessions .group-row.stopped .group-toggle')).toHaveAttribute('aria-expanded', 'false')
  await page.mouse.move(700, 8)
  await page.clock.runFor(6 * 60_000)
  await expect(done).toBeVisible()
  await page.clock.runFor(60_000 + 2_000)
  await expect(done).toHaveCount(0)
})

test('keyboard focus in a row holds changes like the pointer does', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  const data = await setup(page)
  await page.goto('/agents')
  await expect(rows(page).first()).toBeVisible()
  await page.mouse.move(700, 8)
  await page.keyboard.press('j')
  await expect(page.locator('.sessions .row[data-row]:focus')).toHaveCount(1)
  data.sessions.push({ ...data.sessions[1]!, id: '5e000000-0000-4000-8000-000000000034', display_label: 'Keyboard worker', created_at: new Date(NOW + 1000).toISOString() })
  await page.clock.runFor(21_000)
  await expect(note(page)).toContainText('1 change waits')
  await expect(row(page, '5e000000-0000-4000-8000-000000000034')).toHaveCount(0)
  // Focus leaves the rows (to the search): 1.5 s later the row arrives.
  await page.keyboard.press('f')
  await page.clock.runFor(1600)
  await expect(row(page, '5e000000-0000-4000-8000-000000000034')).toBeVisible()
})
