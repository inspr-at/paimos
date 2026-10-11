// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1210 (design AEON-1196 r7, package P2): Sessions grouped by project. Risk: a group moves
// with activity, a lead does not head its group, a project that waits for a lead says nothing,
// the queue chip leads nowhere, a fold is forgotten or moves the row's controls, Collapse all
// changes the head, a filter or grouping is lost on reload, a phone scrolls sideways or gets a
// row target under 44 px.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { capacityWorld, NOW } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { mockDailyPlan } from './agents-daily-fixtures'
import { expectStableControls } from './helpers/stable'
import type { ProjectLead } from '../src/lib/lead'

const sid = (n: number) => `5e000000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`
const LEAD = sid(1)
async function setup(page: Page, options: { folded?: string[]; workers?: number; german?: boolean } = {}) {
  await page.clock.setSystemTime(NOW)
  const work = fixtures()
  // WEB has queued work and no lead; AEON first in the person's own project order.
  work.projects.push({ id: 'p-web', key: 'PRJ-40', title: 'Website', state: 'active', classic: 'WEB', description: '', last: new Date(NOW).toISOString() })
  work.preferences['projects:order'] = { ids: ['p-aeon'] }
  work.preferences['ui.agents.sections'] = { dial: false }
  if (options.german) {
    const frozen = work.projects.find(p => p.id === 'p-frozen')!
    frozen.title = 'Studio-Infrastruktur für Datenbankänderungen und Wiederherstellungsprüfungen der Kundenportale'
  }
  if (options.folded) work.preferences['agents:layout'] = { folded_projects: options.folded }
  const calls = await mockWork(page, work, { admin: true })
  const data = agentData({ me: me.id, now: NOW, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  // PHAROS: the lead (session 1) with its workers; extra workers make the page long enough to stick.
  for (const session of data.sessions) if ([2, 5].includes(Number(session.id.slice(-2)))) (session as { parent_harness_session_id: string | null }).parent_harness_session_id = LEAD
  const base = data.sessions[1]!
  for (let i = 0; i < (options.workers ?? 0); i++) data.sessions.push({ ...base, id: `5e100000-0000-4000-8000-${String(i).padStart(12, '0')}`, display_label: `Long list worker w${i + 1}x`, parent_harness_session_id: LEAD as unknown as null })
  if (options.german) Object.assign(data.sessions[0]!, { brief: 'Datenbankänderungen prüfen und die Wiederherstellungsprüfung für alle Kundenportale abschließen', display_label: 'Projektverantwortlicher Wiederherstellung' })
  const capacity = capacityWorld()
  data.accounts = (capacity.accounts as unknown as typeof data.accounts).filter(a => ['codex', 'claude', 'cursor'].includes(a.harness)).map(a => ({ ...a, registered_by_principal_id: me.id }))
  await mockAgents(page, data, { capacity })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions.push('run.create', 'account.read')
    return route.fulfill({ json: answer })
  })
  await mockDailyPlan(page, { example: 'limit' })
  const lead = (project: string, state: ProjectLead['state'], session: string | null = null): ProjectLead => ({ project_id: project, state, reason: '', revision: state === 'none' ? 0 : 3, generation: state === 'none' ? 0 : 1, session_id: session, process_active: !!session, automatic_launch_enabled: true })
  const leads: Record<string, ProjectLead> = { 'p-pharos': lead('p-pharos', 'working', LEAD), 'p-aeon': lead('p-aeon', 'waiting_for_room'), 'p-frozen': lead('p-frozen', 'none'), 'p-web': lead('p-web', 'none') }
  await page.route(/\/api\/projects\/[^/]+\/lead$/, route => {
    const id = decodeURIComponent(new URL(route.request().url()).pathname.split('/')[3]!)
    return leads[id] ? route.fulfill({ json: leads[id] }) : route.fulfill({ status: 404, json: { error: 'not found' } })
  })
  const queued = [...Array(4).fill('p-pharos'), ...Array(2).fill('p-aeon'), ...Array(3).fill('p-web')]
  await page.route('**/api/queue', route => route.fulfill({ json: { items: queued.map((project, i) => ({ node_id: `q-${i}`, project_id: project, key: `Q-${i}`, title: 'Queued work', state: 'open', priority: 'normal', estimate_hours: 1, queued: { run_id: `r-${i}`, position: i + 1, by: { ...me, kind: 'person' }, at: new Date(NOW).toISOString(), target_agent_id: null, expected_agent_id: null, model_profile_id: null, expected_start_at: null, waiting: false, wait_reason: '' } })), manual_order: false, capacity: { queued_hours: 9, parallel_runs: 4, work_hours: null, warning: false } } }))
  return { data, work, calls }
}

const groups = (page: Page) => page.locator('.sessions .grp')
const row = (page: Page, id: string) => page.locator(`.sessions [data-row="s:${id}"]`)
const leadRow = (page: Page) => row(page, LEAD)
const stick = (page: Page) => page.locator('.sessions [data-stick]')
const layoutWrites = (calls: { method: string; path: string; body?: unknown }[]) => calls.filter(c => c.method === 'PUT' && c.path === '/api/preferences/agents:layout').map(c => (c.body as { value: { folded_projects: string[] } }).value.folded_projects)
// Layout boxes in the page's own coordinates: main's scroll is added back, so scrolling a row into view is not a move.
const box = (page: Page, selector: string) => page.locator(selector).first().evaluate(el => {
  const r = el.getBoundingClientRect(), top = document.querySelector('main')!.scrollTop
  return [r.x, r.y + top, r.width, r.height].map(v => Math.round(v * 2) / 2)
})

for (const theme of ['light', 'dark'] as const) test(`${theme} 1440: projects in their fixed order, the lead heads its group, the no-lead line and the queue chip`, async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
  const errors = watchErrors(page)
  await setup(page)
  await page.goto('/agents')
  await expect(leadRow(page)).toBeVisible()
  // AEON first (the person's own order), then by key; Ended last. Groups part with a hairline.
  await expect(groups(page)).toHaveCount(5)
  expect(await groups(page).evaluateAll(list => list.map(el => el.getAttribute('aria-label')))).toEqual(['AEON · Aeon', 'PHAROS · Pharos', 'PRJ-26 · Studio infrastructure', 'WEB · Website', 'Ended'])
  expect(await groups(page).nth(1).evaluate(el => getComputedStyle(el).borderTopWidth)).toBe('1px')
  // PHAROS: the lead row heads the group, its workers nested below it.
  const pharos = groups(page).nth(1)
  await expect(pharos.locator('[role="row"]').first()).toHaveAttribute('data-row', `s:${LEAD}`)
  await expect(leadRow(page)).toHaveClass(/lead-row/)
  await expect(leadRow(page).locator('.role')).toHaveText('Lead')
  for (const n of [2, 5]) await expect(row(page, sid(n))).toHaveAttribute('data-depth', '1')
  // AEON's lead waits for room: it still heads the group, from the project's lead state.
  const pending = page.locator('.sessions [data-row="l:p-aeon"]')
  await expect(groups(page).nth(0).locator('[role="row"]').first()).toHaveAttribute('data-row', 'l:p-aeon')
  await expect(pending.locator('.c-state')).toContainText('Waiting')
  await expect(pending.locator('.result')).toHaveText('AEON lead')
  // PRJ-26 has sessions and no lead: a plain header names it. WEB: queued work and no lead, one slim line.
  await expect(groups(page).nth(2).locator('.group-row')).toContainText('PRJ-26')
  const nolead = page.locator('.sessions .row.nolead[data-project="WEB"]')
  await expect(nolead).toContainText('3 queued · no lead')
  await expect(nolead.getByRole('button', { name: 'Start a lead for WEB' })).toBeVisible()
  // The queue chip on the lead's title line opens the lead panel at Next up.
  const chip = leadRow(page).locator('.qchip.q-l1')
  await expect(chip).toBeVisible()
  await expect(chip).toHaveAccessibleName('4 queued in PHAROS: open Next up')
  await expect(leadRow(page).locator('.qchip.q-l2')).toBeHidden()
  await page.screenshot({ path: info.outputPath(`1440-${theme}-groups.png`) })
  await chip.click()
  const panel = page.getByRole('complementary', { name: 'PHAROS lead' })
  await expect(panel).toBeVisible()
  await expect(panel.locator('#next-up')).toBeFocused()
  await page.screenshot({ path: info.outputPath(`1440-${theme}-next-up.png`) })
  expect(errors).toEqual([])
})

test('a fold is the person’s, survives a reload, sums up the workers and moves none of the row’s controls', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const { calls } = await setup(page)
  await page.goto('/agents')
  await expect(row(page, sid(2))).toBeVisible()
  await expect(page.locator('.sessions [data-row="l:p-aeon"]')).toBeVisible()
  const fold = leadRow(page).locator('.tree-fold')
  await expect(fold).toHaveAttribute('aria-label', /^Fold .+’s workers: 2 workers, \d+ working$/)
  const before = { fold: await box(page, `.sessions [data-row="s:${LEAD}"] .tree-fold`), row: await box(page, `.sessions [data-row="s:${LEAD}"]`), chip: await box(page, `.sessions [data-row="s:${LEAD}"] .qchip.q-l1`) }
  await fold.click()
  await expect(row(page, sid(2))).toHaveCount(0)
  await expect(leadRow(page).locator('.current-activity')).toContainText('2 workers')
  await expect.poll(() => layoutWrites(calls).at(-1)).toEqual(['p-pharos'])
  // Folding changes the line's words only: the row, its fold button and its chip keep their boxes.
  expect(await box(page, `.sessions [data-row="s:${LEAD}"] .tree-fold`)).toEqual(before.fold)
  expect(await box(page, `.sessions [data-row="s:${LEAD}"]`)).toEqual(before.row)
  expect(await box(page, `.sessions [data-row="s:${LEAD}"] .qchip.q-l1`)).toEqual(before.chip)
  await page.screenshot({ path: info.outputPath('1440-light-folded.png') })
  await page.reload()
  await expect(leadRow(page)).toBeVisible()
  await expect(leadRow(page)).toHaveAttribute('aria-expanded', 'false')
  await expect(row(page, sid(2))).toHaveCount(0)
  // Right arrow on the lead row unfolds it, and that is remembered too.
  await leadRow(page).focus()
  await page.keyboard.press('ArrowRight')
  await expect(row(page, sid(2))).toBeVisible()
  await expect.poll(() => layoutWrites(calls).at(-1)).toEqual([])
})

test('Collapse all folds every lead, Expand all opens them; the icon swaps in its fixed slot', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  const { calls } = await setup(page)
  await page.goto('/agents')
  await expect(row(page, sid(2))).toBeVisible()
  // Every lead has been read: AEON's waiting lead heads its group before anything is measured.
  await expect(page.locator('.sessions [data-row="l:p-aeon"]')).toBeVisible()
  await expect(page.locator('.sessions .row.nolead[data-project="WEB"]')).toBeVisible()
  const head = stick(page)
  await expectStableControls({
    controls: { title: head.locator('#sessions-title'), slot: head.locator('.fold-all-slot'), search: head.getByRole('searchbox', { name: 'Search sessions in this list' }), filter: head.getByRole('button', { name: 'Filter', exact: true }), lead: leadRow(page).locator('.tree-fold') },
    interactions: [
      { name: 'collapse all', run: async () => { await head.getByRole('button', { name: 'Collapse all' }).click(); await expect(row(page, sid(2))).toHaveCount(0); await expect(head.getByRole('button', { name: 'Expand all' })).toBeVisible() } },
      { name: 'expand all', run: async () => { await head.getByRole('button', { name: 'Expand all' }).click(); await expect(row(page, sid(2))).toBeVisible(); await expect(head.getByRole('button', { name: 'Collapse all' })).toBeVisible() } },
    ],
  })
  expect(layoutWrites(calls)).toEqual([['p-pharos'], []])
})

test('Filter by project, harness and computer: pills under the head, the address keeps them, Clear all ends them', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await setup(page)
  await page.goto('/agents')
  await expect(leadRow(page)).toBeVisible()
  const head = stick(page), filter = head.getByRole('button', { name: 'Filter', exact: true })
  await filter.click()
  const by = page.getByRole('menu', { name: 'Filter by' })
  await expect(by.getByRole('menuitem')).toHaveText(['State', 'Project', 'Harness', 'Computer'])
  await page.screenshot({ path: info.outputPath('1440-light-filter-by.png') })
  await by.getByRole('menuitem', { name: 'Project' }).click()
  const projects = page.getByRole('group', { name: 'Project' })
  await projects.getByRole('checkbox', { name: /AEON/ }).check()
  await expect(page).toHaveURL(/project=AEON/)
  await page.screenshot({ path: info.outputPath('1440-light-filter-project.png') })
  await page.keyboard.press('Escape')
  const pill = head.locator('.pills-row [data-facet="project"] .chip-main')
  await expect(pill).toHaveAccessibleName('Project: AEON. Change the filter')
  // Only AEON's sessions stay; its pending lead keeps heading the group; other projects' lines go.
  await expect(groups(page).first()).toHaveAttribute('aria-label', 'AEON · Aeon')
  await expect(leadRow(page)).toHaveCount(0)
  await expect(page.locator('.sessions .row.nolead')).toHaveCount(0)
  await expect(page.locator('.sessions [data-row="l:p-aeon"]')).toBeVisible()
  await page.reload()
  await expect(head.locator('.pills-row [data-facet="project"]')).toBeVisible()
  await expect(leadRow(page)).toHaveCount(0)
  // Harness narrows further; its pill joins; Clear all ends both and the search.
  await filter.click()
  await page.getByRole('menu', { name: 'Filter by' }).getByRole('menuitem', { name: 'Harness' }).click()
  await page.getByRole('group', { name: 'Harness' }).getByRole('checkbox').first().check()
  await page.keyboard.press('Escape')
  await expect(head.locator('.pills-row [data-facet="harness"]')).toBeVisible()
  await expect(page).toHaveURL(/harness=/)
  await head.locator('.pills-row').getByRole('button', { name: 'Clear all' }).click()
  await expect(head.locator('.pills-row')).toHaveCount(0)
  await expect(page).not.toHaveURL(/project=|harness=/)
  await expect(leadRow(page)).toBeVisible()
  // A pill's × removes that filter alone.
  await page.goto('/agents?computer=imac0')
  await expect(head.locator('.pills-row [data-facet="computer"]')).toBeVisible()
  await expect(leadRow(page)).toBeVisible()
  await head.getByRole('button', { name: 'Remove the Computer filter' }).click()
  await expect(head.locator('.pills-row')).toHaveCount(0)
  await expect(filter).toBeFocused()
})

test('Display groups by harness, state or none; the address keeps it and Project is back with one click', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await setup(page)
  await page.goto('/agents')
  await expect(leadRow(page)).toBeVisible()
  const display = stick(page).getByRole('button', { name: /^Display: / })
  await display.click()
  const dialog = page.getByRole('dialog', { name: 'Display' })
  const options = dialog.getByRole('radiogroup', { name: 'Group by' }).getByRole('radio')
  await expect(options).toHaveText(['Project', 'Lead', 'Harness', 'State', 'Computer', 'None'])
  await expect(options.first()).toHaveAttribute('aria-checked', 'true')
  const boxes = await options.evaluateAll(list => list.map(el => { const r = el.getBoundingClientRect(); return [r.x, r.y, r.width, r.height] }))
  await options.nth(2).click()
  await expect(page).toHaveURL(/group=harness/)
  await expect(display).toContainText('by harness')
  await expect(page.locator('.sessions .group-row.keyed').first()).toBeVisible()
  // The options and the sort below never move when the choice changes.
  await options.nth(3).click()
  await expect(dialog.locator('.group-note')).toContainText('By state')
  expect(await options.evaluateAll(list => list.map(el => { const r = el.getBoundingClientRect(); return [r.x, r.y, r.width, r.height] }))).toEqual(boxes)
  await page.screenshot({ path: info.outputPath('1440-light-group-state.png') })
  await options.nth(5).click()
  await expect(page.locator('.sessions .group-row.keyed')).toHaveCount(0)
  await expect(page).toHaveURL(/group=none/)
  // Without groups a lead line still speaks only where the current lead has no running session: AEON waits; PHAROS's lead row is on the list.
  await expect(page.locator('.sessions [data-row="l:p-aeon"]')).toBeVisible()
  await expect(page.locator('.sessions [data-row="l:p-pharos"]')).toHaveCount(0)
  await page.keyboard.press('Escape')
  await page.reload()
  await expect(display).toContainText('no groups')
  await display.click()
  await page.getByRole('dialog', { name: 'Display' }).getByRole('radio', { name: 'Project' }).click()
  await expect(page).not.toHaveURL(/group=/)
  await expect(display).not.toContainText('·')
})

test('a queue or questions that can’t be read say so below the list instead of dropping chips and lines in silence', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await setup(page)
  await page.route('**/api/queue', route => route.fulfill({ status: 500, json: { error: 'queue down' } }))
  await page.route(/\/api\/decision-desk\?state=open/, route => route.fulfill({ status: 500, json: { error: 'desk down' } }))
  await page.goto('/agents')
  await expect(leadRow(page)).toBeVisible()
  await expect(page.locator('.sessions [data-source-note="queue"]')).toHaveText('Queued work can’t be read right now, so queue counts and no-lead lines are missing here.')
  await expect(page.locator('.sessions [data-source-note="questions"]').getByRole('link')).toHaveAttribute('href', '/decision-desk')
  // The counts are unknown: no chip and no no-lead line claims a number.
  await expect(page.locator('.sessions .qchip')).toHaveCount(0)
  await expect(page.locator('.sessions .row.nolead')).toHaveCount(0)
  // The lead that waits still heads its group: its state never needed the queue.
  await expect(page.locator('.sessions [data-row="l:p-aeon"] .c-state')).toContainText('Waiting')
})

// The design's gate (AEON-1196 check.mjs) on the grouped list: no coloured edge accents, SVG icons
// only, English only, no sideways scroll; at 390 every row target is at least 44 px.
const PROBES = () => {
  const parts = () => [...document.querySelectorAll('.agents-page .sessions, .floating')]
  const all = () => parts().flatMap(root => [root, ...root.querySelectorAll('*')])
  const edge: string[] = []
  for (const el of all()) {
    const cs = getComputedStyle(el)
    const bw = (side: string) => parseFloat(cs.getPropertyValue(`border-${side}-width`)) || 0
    const opaque = (side: string) => { const c = cs.getPropertyValue(`border-${side}-color`); return !!c && !/rgba\(.*,\s*0\)$/.test(c) && c !== 'transparent' }
    for (const [a, b] of [['left', 'right'], ['top', 'bottom']]) for (const [x, y] of [[a, b], [b, a]]) if (bw(x!) >= 2 && bw(x!) - bw(y!) >= 1 && opaque(x!)) edge.push(`${el.className} border-${x}`)
    for (const part of cs.boxShadow === 'none' ? [] : cs.boxShadow.split(/,(?![^(]*\))/)) {
      if (!/inset/.test(part)) continue
      const [x = 0, y = 0, blur = 0] = (part.replace(/rgba?\([^)]*\)|inset/g, '').match(/-?[\d.]+px/g) || []).map(parseFloat)
      if ((Math.abs(x) >= 2 || Math.abs(y) >= 2) && blur === 0) edge.push(`${el.className} inset ${part.trim()}`)
    }
    for (const pseudo of ['::before', '::after']) {
      const ps = getComputedStyle(el, pseudo)
      if (ps.content === 'none' || ps.position !== 'absolute') continue
      const w = parseFloat(ps.width) || 0, h = parseFloat(ps.height) || 0, bg = ps.backgroundColor
      if (w >= 2 && w <= 6 && h >= 16 && bg && !/rgba\(.*,\s*0\)$/.test(bg) && bg !== 'transparent') edge.push(`${el.className} ${pseudo} bar`)
    }
  }
  const glyphs: string[] = [], re = /[←→↑↓↵⏎✓✔✕✖×▾▸▴▲▼◂▶◀★☆●○◉⚠✗✘➜➔⟶]/
  const walk = (n: Node) => { for (const c of n.childNodes) { if (c.nodeType === 3 && re.test(c.textContent ?? '')) glyphs.push((c.textContent ?? '').trim().slice(0, 40)); else if (c.nodeType === 1 && !['SCRIPT', 'STYLE'].includes((c as Element).tagName)) walk(c) } }
  parts().forEach(walk)
  const text = parts().map(p => (p as HTMLElement).innerText).join(' ')
  const german = text.match(/[äöüÄÖÜß]|\b(und|der|die|das|nicht|mit|für|oder|ist|Sitzung|Agenten|Projekt|Fehler|Einstellungen|bitte)\b/g) ?? []
  const main = document.querySelector('main')!
  return { edge, glyphs, german, hscroll: Math.max(document.documentElement.scrollWidth - innerWidth, main.scrollWidth - main.clientWidth) }
}
for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`design gate at ${width} ${theme}: the grouped list, its menus and its folds`, async ({ page }, info) => {
    test.setTimeout(90_000)
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await setup(page)
    await page.goto('/agents')
    await expect(leadRow(page)).toBeVisible()
    const gate = async (state: string) => {
      const r = await page.evaluate(PROBES)
      expect(r.edge, `${state}: edge accents`).toEqual([])
      expect(r.glyphs, `${state}: text glyphs`).toEqual([])
      expect(r.german, `${state}: German words`).toEqual([])
      expect(r.hscroll, `${state}: sideways scroll`).toBeLessThanOrEqual(0.5)
      await page.screenshot({ path: info.outputPath(`${width}-${theme}-${state}.png`), fullPage: false })
    }
    await gate('groups')
    if (width === 390) await expect(leadRow(page).locator('.qchip.q-l2')).toBeVisible()
    else await expect(leadRow(page).locator('.qchip.q-l1')).toBeVisible()
    await stick(page).getByRole('button', { name: 'Filter', exact: true }).click()
    await expect(page.getByRole('menu', { name: 'Filter by' })).toBeVisible(); await gate('filter-by')
    await page.getByRole('menu', { name: 'Filter by' }).getByRole('menuitem', { name: 'Computer' }).click()
    await expect(page.getByRole('group', { name: 'Computer' })).toBeVisible(); await gate('filter-computer')
    await page.getByRole('group', { name: 'Computer' }).getByRole('checkbox').first().check()
    await page.keyboard.press('Escape'); await expect(stick(page).locator('.pills-row')).toBeVisible(); await gate('pill')
    await stick(page).locator('.pills-row').getByRole('button', { name: 'Clear all' }).click()
    await stick(page).getByRole('button', { name: /^Display: / }).click(); await expect(page.getByRole('dialog', { name: 'Display' })).toBeVisible()
    await gate('display'); await page.keyboard.press('Escape')
    await leadRow(page).locator('.tree-fold').click(); await expect(row(page, sid(2))).toHaveCount(0)
    await gate('folded')
  })
}

// Long German names (Markus's real projects are often German): titles clip on one line with the full
// text on hover, group names wrap, and nothing scrolls sideways.
for (const width of [390, 1024, 1440]) test(`${width}: long German names stay readable and never widen the page`, async ({ page }, info) => {
  await page.setViewportSize({ width, height: 1000 })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await setup(page, { german: true })
  await page.goto('/agents')
  await expect(leadRow(page)).toBeVisible()
  const title = leadRow(page).locator('.result')
  await expect(title).toHaveAttribute('title', /Wiederherstellungsprüfung für alle Kundenportale abschließen/)
  await expect(page.locator('.sessions .grp[aria-label^="PRJ-26 · Studio-Infrastruktur"] .group-row')).toContainText('Kundenportale')
  const wide = await page.evaluate(() => { const m = document.querySelector('main')!; return Math.max(document.documentElement.scrollWidth - innerWidth, m.scrollWidth - m.clientWidth) })
  expect(wide).toBeLessThanOrEqual(0.5)
  // The queue chip and the row's actions stay inside the row.
  const inside = await leadRow(page).evaluate(row => { const r = row.getBoundingClientRect(); return [...row.querySelectorAll<HTMLElement>('.qchip, .c-actions button')].filter(el => el.getClientRects().length).every(el => { const b = el.getBoundingClientRect(); return b.left >= r.left - 0.5 && b.right <= r.right + 0.5 }) })
  expect(inside).toBe(true)
  await page.screenshot({ path: info.outputPath(`${width}-light-german.png`) })
})

test.describe('phone', () => {
  test.use({ viewport: { width: 390, height: 900 }, hasTouch: true, isMobile: true })
  for (const theme of ['light', 'dark'] as const) test(`${theme} 390: the lead line, the queue chip and every row target reach 44 px`, async ({ page }, info) => {
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await setup(page)
    await page.goto('/agents')
    await expect(leadRow(page)).toBeVisible()
    const wide = await page.evaluate(() => { const m = document.querySelector('main')!; return { doc: document.documentElement.scrollWidth - innerWidth, main: m.scrollWidth - m.clientWidth } })
    expect(wide.doc).toBeLessThanOrEqual(0.5); expect(wide.main).toBeLessThanOrEqual(0.5)
    const small = await page.evaluate(() => {
      const bad: string[] = []
      for (const el of document.querySelectorAll<HTMLElement>('.sessions .grp :is(.tree-fold, .qchip, .nl-btn, .group-toggle, .c-actions button), .sessions .fold-all')) {
        const r = el.getBoundingClientRect(), cs = getComputedStyle(el)
        if (!r.width || !r.height || cs.visibility === 'hidden' || Number(cs.opacity) === 0) continue
        const a = getComputedStyle(el, '::after'); let w = r.width, h = r.height
        if (a.content !== 'none' && a.position === 'absolute') {
          const v = (k: string) => { const x = parseFloat(a.getPropertyValue(k)); return Number.isFinite(x) ? x : 0 }
          w = Math.max(w, r.width - Math.min(0, v('left')) - Math.min(0, v('right'))); h = Math.max(h, r.height - Math.min(0, v('top')) - Math.min(0, v('bottom')))
        }
        if (w < 43.5 || h < 43.5) bad.push(`${el.className} "${(el.getAttribute('aria-label') ?? el.textContent ?? '').trim().slice(0, 30)}" ${Math.round(w)}x${Math.round(h)}`)
      }
      return bad
    })
    expect(small).toEqual([])
    await page.screenshot({ path: info.outputPath(`390-${theme}-groups.png`), fullPage: true })
    // The chip on the state line opens Next up on a phone too.
    await leadRow(page).locator('.qchip.q-l2').tap()
    await expect(page.getByRole('complementary', { name: 'PHAROS lead' }).locator('#next-up')).toBeFocused()
  })
})
