// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1211 (design AEON-1196 r7, package P3): state in the avatar, the Time column, compact rows
// and calm enter/leave. Risk: an avatar loses its state badge or its spoken state, or carries the
// harness again; a refresh adds, moves or removes a row under the pointer; held changes never
// apply; an ended session vanishes at once instead of keeping its place for 10 minutes; a phone
// scrolls sideways or gets a target under 44 px. Time is the page clock, never a sleep.
// Fix round 3 (Tick, 2026-10-11): the columns are exactly Session · Time · Execution · Computer · Beat;
// Execution names models as the registry does, never by raw id; line 1 is the name only (no host);
// rows carry no listening cue, which the details say in words, with the tier.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { controlStability } from './control-stability'
import type { ProjectLead } from '../src/lib/lead'

const NOW = Date.parse('2026-10-11T10:00:00Z')
const ago = (minutes: number) => new Date(NOW - minutes * 60_000).toISOString()
const STATE_WORDS = ['Working', 'Needs something', 'Throttled', 'Problem', 'No heartbeat', 'Idle', 'Done', 'Ended', 'Paused', 'Pausing', 'Awaiting heartbeat', 'Idle · no heartbeat']
const DONE = '5e000000-0000-4000-8000-000000000031'
// Synthetic registry lines (never a production catalog) for the fixture's models and run slugs.
const line = (id: string, slug: string, harness: string, family: string, model: string, effort: string, short: string, version: string) =>
  ({ id, slug, harness, family, model, effort, tier: 'standard', enabled: true, display_name: short, short_name: short, model_version: version })
const REGISTRY = [
  line('m-opus', 'claude-opus-xhigh', 'claude', 'anthropic', 'claude-opus-5-5', 'xhigh', 'Opus', '5.5'),
  line('m-fable', 'claude-fable-high', 'claude', 'anthropic', 'claude-fable', 'high', 'Fable', ''),
  line('m-fable-x', 'claude-fable-xhigh', 'claude', 'anthropic', 'claude-fable', 'xhigh', 'Fable', ''),
  line('m-sol', 'codex-sol-xhigh', 'codex', 'openai', 'gpt-6.1-sol', 'xhigh', 'Sol', '6.1'),
  line('m-astra', 'codex-astra-xhigh', 'codex', 'openai', 'gpt-6.1-astra', 'xhigh', 'Astra', '6.1'),
  line('m-luna', 'codex-luna-high', 'codex', 'openai', 'gpt-6.1-luna', 'high', 'Luna', '6.1'),
  line('m-sonnet', 'pi-anthropic-sonnet-high', 'pi', 'anthropic', 'anthropic/claude-sonnet-4-6', 'high', 'Sonnet', '4.6'),
  line('m-composer', 'cursor-composer-2-5-fast', 'cursor', 'cursor', 'composer-2.5-fast', 'default', 'Composer fast', '2.5'),
]

async function setup(page: Page, theme: 'light' | 'dark' = 'light', headings = false) {
  await page.clock.install({ time: NOW })
  // Heading regressions advance time only by hand: cold-start/measurement work must
  // not age all fixture heartbeats into unrelated structural changes.
  if (headings) await page.clock.pauseAt(NOW + 1000)
  const work = fixtures(); work.preferences.theme = { choice: theme }
  work.preferences['ui.agents.sections'] = { dial: false }
  if (headings) {
    work.projects.push({ id: 'p-heading', key: 'AHEAD', title: 'Wiederherstellungsprüfung der Produktionsserver', state: 'active', classic: 'AHEAD', description: '', last: new Date(NOW).toISOString() })
    work.preferences['projects:order'] = { ids: ['p-heading', 'p-pharos'] }
  }
  await mockWork(page, work, { admin: true })
  const data = agentData({
    now: NOW, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
    nodes: { 'n-1': { key: 'PHAROS-7', title: 'Fleet list' }, 'n-2': { key: 'PHAROS-12', title: 'Wiederherstellung der Sicherungskopien für alle Produktionsserver überprüfen' } },
  })
  const worker = data.sessions[1]!
  Object.assign(worker, { model: 'gpt-6.1-sol', reasoning_effort: 'xhigh', account_label: 'ChatGPT Pro', agent_activity_mode: 'agent_summary', current_activity: { text: 'Prüfe die Sicherungen auf allen Rechnern im Rechenzentrum und vergleiche die Protokolle mit gestern', source: 'auto', at: ago(1) } })
  Object.assign(data.sessions[0]!, { model: 'claude-opus-5-5', reasoning_effort: 'xhigh', account_label: 'Claude Max' })
  // Done three minutes ago: it keeps its place until 10:07.
  data.sessions.push({ ...data.sessions[0]!, id: DONE, role: 'worker', display_label: 'Release notes worker', ticket_node_id: null, ticket: null, run_id: null, phase: 'stopped', activity: 'idle', heartbeat_at: ago(3.2), stopped_at: ago(3), stop_reason: 'completed', finished: true, created_at: ago(40) })
  await mockAgents(page, data)
  await page.route('**/api/models', route => route.request().method() === 'GET' ? route.fulfill({ json: REGISTRY }) : route.fallback())
  return data
}

const rows = (page: Page) => page.locator('.sessions .row[data-row]')
const row = (page: Page, id: string) => page.locator(`.sessions [data-row="s:${id}"]`)
const note = (page: Page) => page.locator('.sessions [data-held-note]')
const showList = (page: Page) => page.locator('.sessions').evaluate(el => el.scrollIntoView({ block: 'start' }))
const rowIds = (page: Page) => rows(page).evaluateAll(els => els.map(el => el.getAttribute('data-row')))
// Every row on screen and the held note under the shared stability guard: fractional boxes in
// scroll-container coordinates, ±0.5 px (docs/ui-conventions.md).
async function rowGuard(page: Page) {
  const ids = (await rowIds(page)).map(String)
  return controlStability(page, { note: note(page), ...Object.fromEntries(ids.map(id => [id, page.locator(`.sessions [data-row="${id}"]`)])) })
}

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
      await expect(page.locator('.sessions .thead [role="columnheader"]')).toHaveText(['Session', 'Time', 'Execution', 'Computer', 'Beat', 'Actions'])
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
      await showList(page)
      await page.screenshot({ path: info.outputPath(`avatars-${width}-${theme}.png`) })
      // Two lines whatever the state: every row with no tools line has one height.
      const heights = await page.locator('.sessions .row[data-row]:not(:has(.worker-tools, .lineage))').evaluateAll(els => els.map(el => `${el.getAttribute('data-row')} ${Math.round(el.getBoundingClientRect().height)}`))
      expect(new Set(heights.map(h => h.split(' ')[1])).size, heights.join(', ')).toBe(1)
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
    await expect(worker.locator('.op-mark')).toHaveAttribute('aria-label', 'GPT-6.1 Sol · xhigh · Codex · ChatGPT Pro')
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
    await showList(page)
    await page.screenshot({ path: info.outputPath(`compact-390-${theme}.png`) })
  })
}

for (const theme of ['light', 'dark'] as const) for (const width of [1440, 400]) {
  test(`${theme} ${width}: the approved columns, registry model names, a name-only line 1; listening and tier in the details`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    const errors = watchErrors(page)
    const data = await setup(page, theme)
    await page.goto('/agents')
    const lead = row(page, data.sessions[0]!.id), worker = row(page, data.sessions[1]!.id)
    await expect(worker).toBeVisible()
    // Line 1 is the name only: the worker's ticket key opens it, and no row repeats its computer there.
    await expect(worker.locator('.l1 .key-inline')).toHaveText('PHAROS-12 ·')
    for (const session of data.sessions) {
      const one = row(page, String(session.id)).locator('.l1')
      if (await one.count()) expect(await one.innerText(), `line 1 of ${session.id}`).not.toContain(String(session.host))
    }
    // No listening or inbox cue, Tier, ticket chip or estimate on any row.
    await expect(page.locator('.sessions .row :is([data-listening], .no-inbox, [data-tier], .ticket-chip, .session-estimate)')).toHaveCount(0)
    if (width > 600) {
      await expect(page.locator('.sessions .thead [role="columnheader"]')).toHaveText(['Session', 'Time', 'Execution', 'Computer', 'Beat', 'Actions'])
      await expect(lead.locator('.exec-model')).toHaveText('Opus 5.5 · xhigh')
      await expect(lead.locator('.exec-account')).toHaveText('Claude Max')
      await expect(worker.locator('.exec-model')).toHaveText('GPT-6.1 Sol · xhigh')
      await expect(worker.locator('.exec-account')).toHaveText('ChatGPT Pro')
      await expect(lead.locator('.c-host')).toContainText('imac0')
      const models = await page.locator('.sessions .row .exec-model:visible').allInnerTexts()
      expect(models.length).toBeGreaterThan(3)
      for (const text of models) expect(text, 'a registry name, never a raw model id').not.toMatch(/[a-z0-9]-[a-z0-9]/)
    } else {
      await expect(page.locator('.sessions .thead')).toBeHidden()
      await expect(worker.locator('.op-mark')).toHaveAttribute('aria-label', 'GPT-6.1 Sol · xhigh · Codex · ChatGPT Pro')
    }
    expect((await probe(page)).hscroll).toBe(false)
    await showList(page)
    await page.screenshot({ path: info.outputPath(`rows-${width}-${theme}.png`) })
    // The details say in words whether the session listens, and hold its tier.
    await lead.locator('.agent-link').click()
    const panel = page.getByRole('complementary', { name: 'Session details' })
    await expect(panel.locator('.now-listen')).toContainText(/listening/i)
    await expect(panel.locator('.service-tier [data-tier]')).toHaveCount(1)
    await page.screenshot({ path: info.outputPath(`details-${width}-${theme}.png`) })
    expect(errors).toEqual([])
  })
}

test.describe('touch', () => {
  test.use({ hasTouch: true, isMobile: true, viewport: { width: 390, height: 900 } })
  test('390: every target in the Sessions card is 44 px', async ({ page }, info) => {
    await setup(page)
    await page.goto('/agents')
    await expect(rows(page).first()).toBeVisible()
    await showList(page)
    await page.screenshot({ path: info.outputPath('touch-390-light.png') })
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
  await showList(page)
  // The pointer rests on a row; a new worker arrives and a working one leaves the list. Every row
  // and the note keep their boxes, whichever text the note shows (AEON-541).
  await first.locator('.l2').hover()
  const before = await rowIds(page)
  const guard = await rowGuard(page)
  const gone = data.sessions.splice(2, 1)[0]!
  await guard.check(async () => {
    data.sessions.push({ ...data.sessions[1]!, id: '5e000000-0000-4000-8000-000000000032', display_label: 'Fresh worker', created_at: new Date(NOW + 1000).toISOString(), heartbeat_at: new Date(NOW + 1000).toISOString() })
    await page.clock.runFor(21_000)
    await expect(note(page)).toContainText('2 changes wait while you point here')
  })
  guard.done()
  await expect(note(page).getByRole('button', { name: /Show now$/ })).toBeVisible()
  expect(await rowIds(page), 'nothing arrived or left under the pointer').toEqual(before)
  await expect(row(page, gone.id)).toBeVisible()
  await page.screenshot({ path: info.outputPath('held-1440-light.png') })
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
  await note(page).getByRole('button', { name: /Show now$/ }).click()
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
  // AEON-1240: it says when it finished and when it moves, never its last activity or an estimate.
  await expect(done.locator('.l2t')).toHaveText(/^Finished \d{1,2}:57 · moves to Ended at \d{1,2}:07$/)
  await expect(done.locator('.c-time')).toHaveText('3 min ago')
  // It sits among the live rows, not in the folded Ended group.
  await expect(page.locator('.sessions .ended-head .group-toggle')).toHaveAttribute('aria-expanded', 'false')
  await page.mouse.move(700, 8)
  await page.clock.runFor(6 * 60_000)
  await expect(done).toBeVisible()
  await page.clock.runFor(60_000 + 2_000)
  await expect(done).toHaveCount(0)
})

// AEON-1240 evidence: a finished row that keeps its place, at three widths, light and dark.
for (const width of [1440, 1024, 390]) for (const colorScheme of ['light', 'dark'] as const) {
  test(`a finished row says when it finished and when it moves at ${width} ${colorScheme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 }); await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
    await setup(page, colorScheme)
    await page.goto('/agents')
    const done = row(page, DONE)
    await expect(done.locator('.l2t')).toHaveText(/^Finished \d{1,2}:57 · moves to Ended at \d{1,2}:07$/)
    await done.scrollIntoViewIfNeeded()
    await page.screenshot({ path: testInfo.outputPath(`AEON-1240-finished-row-${width}-${colorScheme}.png`) })
  })
}

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

// Fix round 2 (gate on 87e23cad). Risk: the hold keeps a row's group but lets its membership,
// its filter match, the empty list, its sibling order or its line 2 follow the live copy, so a
// row still leaves or moves under the pointer, or says what it did before.
const WORKER = (n: number) => `5e000000-0000-4000-8000-0000000000${n}`
const APPROVAL = { kind: 'approval', scope: 'session', actor: 'person', count: 1, blocking: true, location: 'approvals' }
type Data = Awaited<ReturnType<typeof setup>>
// A working worker under the lead, without a run, so its own fields decide its state.
const worker = (data: Data, n: number, fields: Record<string, unknown>) => ({ ...data.sessions[0]!, id: WORKER(n), role: 'worker', run_id: null, ticket_node_id: null, ticket: null, parent_harness_session_id: data.sessions[0]!.id, heartbeat_at: ago(0.1), ...fields })
const childIds = (page: Page, parent: string) => page.locator(`.sessions .row[data-parent="${parent}"]`).evaluateAll(els => els.map(el => el.getAttribute('data-row')!.slice(2)))

test('a nested ended worker stays under the pointer past its 10 minutes, then joins its lead\'s stopped count', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const data = await setup(page)
  const lead = data.sessions[0]!.id
  // Done nine minutes ago under the lead: it keeps its place until 10:01.
  Object.assign(data.sessions.find(s => s.id === DONE)!, { parent_harness_session_id: lead, stopped_at: ago(9), heartbeat_at: ago(9.2) })
  await page.goto('/agents')
  const done = row(page, DONE)
  await expect(done).toHaveAttribute('data-parent', lead)
  await expect(done.locator('.l2t')).toContainText(/moves to Ended at \d{1,2}:01/)
  await showList(page)
  await row(page, lead).locator('.l2').hover()
  const before = await rowIds(page)
  await page.clock.runFor(2 * 60_000)
  await expect(note(page)).toContainText(/wait/)
  await expect(done, 'the pointer holds the row past 10:01').toBeVisible()
  expect(await rowIds(page), 'no row left or moved').toEqual(before)
  await expect(row(page, lead).locator('.history-toggle')).toHaveCount(0)
  // The pointer leaves: 1.5 s later it joins the stopped workers.
  await page.mouse.move(700, 8)
  await page.clock.runFor(1600)
  await expect(done).toHaveCount(0)
  await expect(row(page, lead).locator('.history-toggle')).toHaveText('1 stopped')
})

test('a filtered row that stops matching waits under the pointer like any other change', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const data = await setup(page)
  // One row starts asking (another group); one ends (it keeps its group, but no longer works).
  data.sessions.push(worker(data, 44, { display_label: 'Ending worker', parent_harness_session_id: null, created_at: ago(30) }))
  await page.goto('/agents')
  await page.locator('.page-head [data-filter="working"]').click()
  const lead = row(page, data.sessions[0]!.id), ending = row(page, WORKER(44))
  await expect(lead).toBeVisible()
  await expect(ending).toBeVisible()
  await showList(page)
  await lead.locator('.l2').hover()
  const before = await rowIds(page)
  Object.assign(data.sessions[0]!, { needs_attention: true, attention_reasons: [APPROVAL] })
  Object.assign(data.sessions.find(s => s.id === WORKER(44))!, { phase: 'stopped', activity: 'idle', stopped_at: ago(0), stop_reason: 'completed', finished: true })
  await page.clock.runFor(21_000)
  await expect(lead.locator('.ava')).toHaveAttribute('aria-label', /: Needs something$/)
  await expect(ending.locator('.ava')).toHaveAttribute('aria-label', /: Done$/)
  await expect(note(page)).toContainText('2 changes wait')
  expect(await rowIds(page), 'both stay while the pointer is here').toEqual(before)
  await page.mouse.move(700, 8)
  await page.clock.runFor(1600)
  await expect(lead).toHaveCount(0)
  await expect(ending).toHaveCount(0)
})

test('the last row stays under the pointer when the live list empties', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const data = await setup(page)
  data.sessions.splice(1)
  await page.goto('/agents')
  const only = row(page, data.sessions[0]!.id)
  await expect(only).toBeVisible()
  await showList(page)
  await only.locator('.l2').hover()
  data.sessions.splice(0)
  await page.clock.runFor(21_000)
  await expect(note(page)).toContainText('1 change waits')
  await expect(only, 'the table stays with its held row').toBeVisible()
  await expect(page.locator('.sessions .connect')).toHaveCount(0)
  await page.mouse.move(700, 8)
  await page.clock.runFor(1600)
  await expect(only).toHaveCount(0)
  await expect(page.locator('.sessions .connect')).toBeVisible()
})

test('an ended worker keeps its place among its siblings for 10 minutes', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const data = await setup(page)
  const lead = data.sessions[0]!.id
  data.sessions.push(worker(data, 41, { display_label: 'Older worker', created_at: ago(60) }), worker(data, 42, { display_label: 'Younger worker', created_at: ago(20) }))
  await page.goto('/agents')
  await page.mouse.move(700, 8)
  await expect.poll(() => childIds(page, lead)).toEqual([WORKER(41), WORKER(42)])
  // The older one ends with the pointer away: no hold, yet it keeps its place.
  Object.assign(data.sessions.find(s => s.id === WORKER(41))!, { phase: 'stopped', activity: 'idle', stopped_at: ago(0), stop_reason: 'completed', finished: true })
  await page.clock.runFor(21_000)
  await expect(row(page, WORKER(41)).locator('.ava')).toHaveAttribute('aria-label', /: Done$/)
  expect(await childIds(page, lead), 'still ahead of its younger sibling').toEqual([WORKER(41), WORKER(42)])
  await page.clock.runFor(10 * 60_000)
  await expect(row(page, WORKER(41))).toHaveCount(0)
})

test('a held move shows the newest line 2: a working row that starts asking says its request', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const data = await setup(page)
  data.sessions.push(worker(data, 43, { display_label: 'Asking worker', parent_harness_session_id: null, created_at: ago(30), agent_activity_mode: 'agent_summary', current_activity: { text: 'Editing the fleet list', source: 'auto', at: ago(1) } }))
  await page.goto('/agents')
  const asking = row(page, WORKER(43))
  await expect(asking.locator('.l2t')).toHaveText('Editing the fleet list')
  await showList(page)
  await asking.locator('.l2').hover()
  Object.assign(data.sessions.find(s => s.id === WORKER(43))!, { current_activity: null, needs_attention: true, attention_reasons: [APPROVAL] })
  await page.clock.runFor(21_000)
  // Its move to Needs attention waits; its badge and line 2 are the newest.
  await expect(note(page)).toContainText('1 change waits')
  await expect(asking.locator('.ava')).toHaveAttribute('aria-label', /: Needs something$/)
  await expect(asking.locator('.l2t')).toContainText(/1 permission request awaiting a person.s decision/)
  await expect(asking.locator('.l2t')).not.toContainText('Editing the fleet list')
})

// Fix round 4 (gate on f3acf3b1). Risk: rows name models as the registry does, but the list's search
// still reads raw ids only, so searching what a row says ("Opus 5.5", "Composer fast 2.5") hides that
// row. A raw reported or launch id still finds it.
test('the search finds a row by the model name it shows, and still by its raw ids', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const data = await setup(page)
  await page.goto('/agents')
  const lead = String(data.sessions[0]!.id), worker = String(data.sessions[1]!.id), cursor = String(data.sessions[3]!.id)
  await expect(row(page, cursor).locator('.exec-model')).toHaveText('Composer fast 2.5')
  const box = page.getByRole('searchbox', { name: 'Search sessions in this list' })
  // The rows a search matches; ancestors it keeps as dimmed context do not count. Each query follows
  // one that matches nothing, so the rows it finds can only come from that query.
  const matched = page.locator('.sessions .row[data-row]:not(.context-only)')
  const matchedIds = () => matched.evaluateAll(els => els.map(el => el.getAttribute('data-row')!.slice(2)).sort())
  async function search(query: string, ids: string[]) {
    await box.fill('no session matches this')
    await expect(matched).toHaveCount(0)
    await box.fill(query)
    await expect.poll(matchedIds, { message: `${query} finds exactly these rows` }).toEqual([...ids].sort())
  }
  for (const [query, ids, shows] of [
    ['Opus 5.5', [lead, DONE], 'Opus 5.5 · xhigh'],
    ['gpt-6.1 sol', [worker], 'GPT-6.1 Sol · xhigh'],
    ['Composer fast 2.5', [cursor], 'Composer fast 2.5'],
  ] as const) {
    await search(query, [...ids])
    await expect(matched.locator('.exec-model'), `${query} matches rows that show it`).toHaveText(ids.map(() => shows))
  }
  // Raw ids keep matching: the model the session reports and the run's launch profile.
  await search('claude-opus-5-5', [lead, DONE])
  await search('gpt-6.1-sol', [worker])
  await search('codex-astra-xhigh', [worker])
})

// Fix round 5 (gate on 49f7335a). Risk: the hold keeps each row's place but judges its search match on
// the newest registry names, so a model renamed in Settings › Models adds or removes a row under the pointer.
test('a model renamed in the registry while the pointer rests never adds or removes a searched row', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const data = await setup(page)
  // A custom registry name that no raw id carries, so only the name a row shows can match it.
  const named = (renames: Record<string, string>) => REGISTRY.map(m => renames[m.id] ? { ...m, display_name: renames[m.id]!, short_name: renames[m.id]! } : m)
  let registry = named({ 'm-opus': 'Zephyr' })
  await page.route('**/api/models', route => route.request().method() === 'GET' ? route.fulfill({ json: registry }) : route.fallback())
  await page.goto('/agents')
  const lead = String(data.sessions[0]!.id), cursor = String(data.sessions[3]!.id)
  await page.getByRole('searchbox', { name: 'Search sessions in this list' }).fill('zephyr')
  const matched = page.locator('.sessions .row[data-row]:not(.context-only)')
  const matchedIds = () => matched.evaluateAll(els => els.map(el => el.getAttribute('data-row')!.slice(2)).sort())
  await expect.poll(matchedIds).toEqual([lead, DONE].sort())
  await expect(row(page, lead).locator('.exec-model')).toHaveText('Zephyr 5.5 · xhigh')
  await showList(page)
  await row(page, lead).locator('.l2').hover()
  const before = await rowIds(page)
  const guard = await rowGuard(page)
  // Zephyr becomes Lyra and Composer fast becomes Zephyr: on the newest names the search would drop
  // both Zephyr rows and add the Cursor one.
  await guard.check(async () => {
    registry = named({ 'm-opus': 'Lyra', 'm-composer': 'Zephyr' })
    await page.clock.runFor(21_000)
    await expect(row(page, lead).locator('.exec-model'), 'the new name reached the row').toHaveText('Lyra 5.5 · xhigh')
    await expect(note(page)).toContainText('3 changes wait')
  })
  guard.done()
  expect(await rowIds(page), 'no row arrived or left under the pointer').toEqual(before)
  expect(await matchedIds()).toEqual([lead, DONE].sort())
  // The pointer leaves: 1.5 s later the search follows the new names.
  await page.mouse.move(700, 8)
  await page.clock.runFor(1600)
  await expect.poll(matchedIds).toEqual([cursor])
  await expect(row(page, cursor).locator('.exec-model')).toHaveText('Zephyr 2.5')
})

// Fix6: headings from the shared lead/queue projection belong to the held structure too.
// A project with no sessions gains queued work; an existing lead hands over before its
// replacement session arrives. Neither read may insert a row above the pointed session.
async function headingSetup(page: Page, theme: 'light' | 'dark') {
  const data = await setup(page, theme, true)
  // Cross a minute boundary on the first poll, so live time updates are observable.
  data.sessions[0]!.created_at = ago(71.9)
  const lead = (id: string, session: string | null = null): ProjectLead => ({ project_id: id, state: session ? 'working' : 'none', reason: '', revision: 1, generation: 1, session_id: session, process_active: !!session, automatic_launch_enabled: true })
  const leads: Record<string, ProjectLead> = {
    'p-heading': lead('p-heading'), 'p-pharos': lead('p-pharos', data.sessions[0]!.id), 'p-aeon': lead('p-aeon'), 'p-frozen': lead('p-frozen'),
  }
  await page.route(/\/api\/projects\/[^/]+\/lead$/, route => {
    const id = decodeURIComponent(new URL(route.request().url()).pathname.split('/')[3]!)
    return leads[id] ? route.fulfill({ json: leads[id] }) : route.fulfill({ status: 404, json: { error: 'not found' } })
  })
  const queued = ['p-pharos']
  await page.route(url => url.pathname === '/api/queue', route => {
    const project = new URL(route.request().url()).searchParams.get('project_id')
    const items = queued.map((project_id, i) => ({ node_id: `q-heading-${i}`, project_id, key: `QUEUED-${i + 1}`, title: 'Restore verification', state: 'open', priority: 'normal', estimate_hours: 1,
      queued: { run_id: `r-heading-${i}`, position: i + 1, by: me, at: ago(1), target_agent_id: null, expected_agent_id: null, model_profile_id: null, expected_start_at: null, waiting: false, wait_reason: '' },
    })).filter(item => !project || item.project_id === project)
    return route.fulfill({ json: { items, count: items.length, manual_order: false, capacity: { queued_hours: items.length, parallel_runs: 4, work_hours: null, warning: false } } })
  })
  return { data, leads, queued }
}
const headingRows = (page: Page) => page.locator('.sessions .grp').evaluateAll(groups => groups.map(group => ({
  group: group.getAttribute('aria-label'),
  rows: [...group.querySelectorAll('[role="row"]')].map(el => el.getAttribute('data-row') ?? (el.classList.contains('nolead') ? `no-lead:${el.getAttribute('data-project')}` : 'heading')),
})))

for (const theme of ['light', 'dark'] as const) for (const width of [1440, 1024, 390]) {
  for (const update of ['queue', 'handover'] as const) test(`${theme} ${width}: held ${update} headings never add a row or move its controls`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    const errors = watchErrors(page)
    const { data, leads, queued } = await headingSetup(page, theme)
    await page.goto('/agents')
    const first = row(page, data.sessions[0]!.id)
    const chip = first.locator(width === 390 ? '.qchip.q-l2' : '.qchip.q-l1')
    await expect(chip).toContainText('1 queued')
    await expect(page.locator('.sessions .row.nolead[data-project="AHEAD"]')).toHaveCount(0)
    await expect(page.locator('.sessions [data-row="l:p-pharos"]')).toHaveCount(0)
    await showList(page)
    await first.locator('.l2').hover()
    const before = await headingRows(page), beforeIds = await rowIds(page)
    const time = first.locator(width === 390 ? '.t-inline' : '.c-time'), beforeTime = await time.innerText()
    const guard = await rowGuard(page)
    const controls = await controlStability(page, { name: first.locator('.agent-link'), actions: first.locator('.c-actions'), queue: chip })
    const updateProjection = async () => {
      if (update === 'queue') queued.push('p-heading')
      else leads['p-pharos'] = { ...leads['p-pharos']!, state: 'starting', session_id: WORKER(70), revision: 2, generation: 2 }
      data.sessions[1]!.current_activity = { text: 'Live activity while headings wait', source: 'auto', at: new Date(NOW).toISOString() }
      await page.clock.runFor(21_000)
      await expect(note(page), 'a heading-only change is counted').toContainText('1 change waits')
      await expect(time, 'time evidence updates during the hold').not.toHaveText(beforeTime)
      await expect(row(page, data.sessions[1]!.id).locator('.l2t')).toHaveText('Live activity while headings wait')
      expect(await headingRows(page), 'section and heading membership stay frozen').toEqual(before)
      expect(await rowIds(page), 'no session or pending lead row arrives').toEqual(beforeIds)
    }
    await guard.check(() => controls.check(updateProjection))
    guard.done(); controls.done()
    await page.screenshot({ path: info.outputPath(`held-${update}-${width}-${theme}.png`) })
    const newHeading = page.locator(update === 'queue' ? '.sessions .row.nolead[data-project="AHEAD"]' : '.sessions [data-row="l:p-pharos"]')
    await expect(newHeading).toHaveCount(0)
    if (update === 'queue') await note(page).getByRole('button', { name: /Show now$/ }).click()
    else {
      await page.mouse.move(700, 8)
      await page.clock.runFor(1400)
      await expect(newHeading).toHaveCount(0)
      await page.clock.runFor(200)
    }
    await expect(newHeading, 'the allowed moment applies the heading').toBeVisible()
    await expect(note(page)).toContainText('Live · updated')
    expect(errors).toEqual([])
  })
}
