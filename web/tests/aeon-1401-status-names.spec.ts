// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1401 (Tick spec v1): the workspace's own status names. Risk: a surface a
// person reads keeps the built-in word, a stored or sent state stops being
// canonical, the card lets two statuses read alike, or the card or an open list
// moves while names change. Shots and a checks file go to testInfo.outputPath.
import { writeFileSync } from 'node:fs'
import { expect, test, type Browser, type Locator, type Page, type TestInfo } from '@playwright/test'
import { fixtures, mockWork, type Fixtures } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { expectStableControls } from './helpers/stable'

type Theme = 'light' | 'dark'
interface Check { assert: string; width: number; theme: Theme; ok: boolean; detail?: string }
const BUILDING = { in_progress: { en: 'Building', de: 'In Umsetzung' } }

async function open(browser: Browser, testInfo: TestInfo, width: number, theme: Theme, data: Fixtures = fixtures()) {
  const context = await browser.newContext({ baseURL: testInfo.project.use.baseURL, viewport: { width, height: width <= 600 ? 844 : 900 }, hasTouch: width <= 600, reducedMotion: 'reduce' })
  const page = await context.newPage()
  data.preferences.theme = { choice: theme }
  const calls = await mockWork(page, data, { admin: true })
  await page.route('**/api/queue?*', route => route.fulfill({ json: { items: [], manual_order: false, capacity: { queued_hours: 0, parallel_runs: 0, work_hours: 0, warning: false } } }))
  return { context, page, calls, data }
}
async function autopilotRoutes(page: Page) {
  await mockBusiness(page, businessData({ role: 'admin' }), { role: 'admin' })
  await mockSettings(page, settingsData())
  await page.clock.setSystemTime(new Date('2026-10-19T12:00:00Z'))
  await page.route('**/api/settings/model-provider', route => route.fulfill({ json: { enabled: false, base_url: '', chat_model: '', embedding_model: '', features: { crm_note_rewrite: false, embeddings: false }, provider_id: '', revision: 0, has_api_key: false } }))
  const settings = { enabled: true, revision: 0, rules: { new: { enabled: true, days: 7 }, backlog: { enabled: true, days: 90 }, blocked: { enabled: true, days: 14 }, progress: { enabled: true, days: 3 }, done: { enabled: true, days: 14 }, publish: { enabled: true }, accept: { enabled: true, days: 30 } } }
  await page.route('**/api/settings/status-autopilot', route => route.fulfill({ json: settings }))
  await page.route('**/api/status-autopilot/projects*', route => route.fulfill({ json: { items: [], inherited_count: 2, next_cursor: null } }))
  await page.route('**/api/status-autopilot/attention*', route => route.fulfill({ json: { counts: { proposed: 0, triage: 0, cancel: 0, blocked: 0, missed: 0 }, total: 0, items: [], next_cursor: null, facets: { projects: [], assignees: [] } } }))
  await page.route('**/api/status-autopilot/changes*', route => route.fulfill({ json: { items: [] } }))
}
const shot = (page: Page, testInfo: TestInfo, width: number, theme: Theme, state: string, target?: Locator) =>
  (target ?? page).screenshot({ path: testInfo.outputPath(`aeon1401-${width}-${theme}-${state}.png`), ...(target ? {} : { fullPage: true }) })
// Text a person can see: rendered, visible, opaque and larger than a screen-reader-only clip.
const visibleText = (locator: Locator) => locator.evaluate(root => {
  const out: string[] = []
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const element = node.parentElement, rect = element?.getBoundingClientRect()
    if (!element || !rect || !node.textContent?.trim() || rect.width <= 1 || rect.height <= 1) continue
    if (element.checkVisibility({ opacityProperty: true, visibilityProperty: true })) out.push(node.textContent)
  }
  return out.join(' ')
})
function record(checks: Check[], testInfo: TestInfo, file: string) { writeFileSync(testInfo.outputPath(file), `${JSON.stringify(checks, null, 2)}\n`) }

test('AEON-1401 the Status names card renames, refuses ambiguous names and keeps its controls still', async ({ browser }, testInfo) => {
  test.setTimeout(150_000)
  const checks: Check[] = []
  for (const [width, theme] of [[1440, 'light'], [1440, 'dark'], [390, 'light']] as const) {
    const { context, page, calls, data } = await open(browser, testInfo, width, theme)
    try {
      await autopilotRoutes(page)
      await page.goto('/settings/autopilot')
      const card = page.locator('#status-names')
      await expect(card.getByRole('heading', { name: 'Status names' })).toBeVisible()
      const rows = card.locator('.sn-row')
      await expect(rows).toHaveCount(12)
      // The card sits directly above Status autopilot.
      expect(await page.evaluate(() => document.querySelector('#status-names')?.nextElementSibling?.id)).toBe('status-autopilot')
      const field = (status: string, locale: 'en' | 'de') => card.locator(`.sn-row[data-status="${status}"] .sn-field[data-locale="${locale}"] input`)
      await expect(field('in_progress', 'en')).toHaveAttribute('placeholder', 'In progress')
      await expect(field('in_progress', 'de')).toHaveAttribute('placeholder', 'In Arbeit')
      await expect(card.locator('.sn-reset .icon-btn.hidden')).toHaveCount(12)
      const targets = await field('qa', 'en').evaluate(el => el.getBoundingClientRect().height)
      checks.push({ assert: 'N1 card with nothing renamed: 12 rows, built-in placeholders, no Reset shown', width, theme, ok: true })
      if (width === 390) {
        expect(targets).toBeGreaterThanOrEqual(44)
        const [canon, en, de] = await Promise.all(['.sn-canon', '.sn-field[data-locale="en"]', '.sn-field[data-locale="de"]'].map(selector => card.locator(`.sn-row[data-status="qa"] ${selector}`).evaluate(el => el.getBoundingClientRect().top)))
        expect(canon < en && en < de, 'phone rows stack: built-in, English, German').toBe(true)
        checks.push({ assert: 'Phone rows stack canonical, EN, DE with 44 px targets', width, theme, ok: true, detail: `field height ${targets}` })
      }
      await shot(page, testInfo, width, theme, 'N1', card)
      if (width !== 1440 || theme !== 'light') continue

      const puts = () => calls.filter(call => call.path === '/api/settings/status-names' && call.method === 'PUT')
      const reset = (status: string) => card.locator(`.sn-row[data-status="${status}"] .sn-reset`)
      const error = (status: string) => card.locator(`.sn-row[data-status="${status}"] .sn-error`)
      // Reset appearing and a refusal line appearing move nothing in the card.
      await expectStableControls({
        controls: { progressDe: field('in_progress', 'de'), progressReset: reset('in_progress'), qaEn: field('qa', 'en'), doneEn: field('done', 'en'), archivedRow: card.locator('.sn-row[data-status="archived"]'), foot: card.locator('.sn-foot') },
        interactions: [
          { name: 'rename In progress (Reset appears)', run: async () => {
            await field('in_progress', 'en').fill('Building'); await field('in_progress', 'en').press('Enter')
            await expect.poll(() => puts().length).toBe(1)
            await expect(card.locator('.sn-foot')).toContainText('Saved')
            await expect(reset('in_progress').locator('.icon-btn')).not.toHaveClass(/hidden/)
          } },
          { name: 'German name', run: async () => { await field('in_progress', 'de').fill('In Umsetzung'); await field('in_progress', 'de').blur(); await expect.poll(() => puts().length).toBe(2) } },
          { name: 'a duplicate of another rename is refused in its reserved line', run: async () => {
            await field('qa', 'en').fill('building'); await field('qa', 'en').blur()
            await expect(error('qa')).toHaveText('“building” is already the name of another status.')
          } },
        ],
      })
      expect(puts()).toHaveLength(2)
      expect(data.statusNames).toEqual({ revision: 2, names: BUILDING })
      checks.push({ assert: 'Reset appearing moves nothing (±0.5 px)', width, theme, ok: true }, { assert: 'A validation line appearing moves nothing (±0.5 px)', width, theme, ok: true })
      checks.push({ assert: 'Duplicate of another rename refused with the copy', width, theme, ok: true })
      await shot(page, testInfo, width, theme, 'N2', card)

      await field('qa', 'en').fill(''); await field('qa', 'en').blur()
      await expect(error('qa')).toHaveText('')
      await field('open', 'en').fill('x'.repeat(25)); await field('open', 'en').blur()
      await expect(error('open')).toHaveText('At most 24 characters.')
      await field('open', 'en').fill(''); await field('open', 'en').blur()
      checks.push({ assert: '25 characters refused with “At most 24 characters.”', width, theme, ok: true })
      await field('in_progress', 'en').fill('Done'); await field('in_progress', 'en').blur()
      await expect(error('in_progress')).toHaveText('“Done” is already the name of another status.')
      expect(puts()).toHaveLength(2)
      checks.push({ assert: '“Done” for In progress refused with the copy', width, theme, ok: true })
      // Empty falls back to the built-in name: only the German rename stays.
      await field('in_progress', 'en').fill('  '); await field('in_progress', 'en').blur()
      await expect.poll(() => puts().length).toBe(3)
      expect(data.statusNames.names).toEqual({ in_progress: { de: 'In Umsetzung' } })
      await expect(field('in_progress', 'en')).toHaveValue('')
      await expect(field('in_progress', 'en')).toHaveAttribute('placeholder', 'In progress')
      checks.push({ assert: 'Empty name falls back to the built-in', width, theme, ok: true })
      // Reset clears the row and hides itself again.
      await reset('in_progress').locator('.icon-btn').click()
      await expect.poll(() => puts().length).toBe(4)
      expect(data.statusNames.names).toEqual({})
      await expect(reset('in_progress').locator('.icon-btn')).toHaveClass(/hidden/)
      checks.push({ assert: 'Reset returns the row to the built-in names', width, theme, ok: true })
    } finally { await context.close() }
  }
  record(checks, testInfo, 'aeon1401-checks-card.json')
})

test('AEON-1401 lists, the status menu, the ticket header, Desk and activity read the workspace name; writes stay canonical', async ({ browser }, testInfo) => {
  test.setTimeout(150_000)
  const checks: Check[] = []
  for (const [width, theme] of [[1440, 'light'], [1440, 'dark'], [1024, 'light'], [390, 'light']] as const) {
    const data = fixtures()
    data.statusNames.revision = 1; data.statusNames.names = structuredClone(BUILDING)
    data.nodes.find(node => node.id === 'n-4')!.state = 'open'
    const { context, page, calls } = await open(browser, testInfo, width, theme, data)
    try {
      await page.goto('/p/PHAROS?group=status&status=in_progress,qa,open')
      const grid = page.getByRole('grid', { name: 'Tickets' })
      const row = grid.locator('tr.ticket-row', { hasText: 'PHAROS-11' })
      await expect(row).toBeVisible()
      await page.evaluate(() => document.fonts.ready)
      await expect(row).toContainText('Building')
      await expect(grid.locator('tr.group-row .group-label', { hasText: /^Building$/ })).toHaveCount(1)
      expect(await visibleText(grid)).not.toContain('In progress')
      const toolbar = page.getByRole('toolbar', { name: 'Ticket list controls' })
      if (width > 600) await expect(toolbar).toContainText('Building')
      expect(await visibleText(toolbar)).not.toContain('In progress')
      checks.push({ assert: 'DOM sweep (list, group header, filter chips): “Building”, no “In progress”', width, theme, ok: true })
      await shot(page, testInfo, width, theme, width === 390 ? 'N6' : 'N3')

      if (width === 1440 && theme === 'light') {
        // A live rename re-renders the words in place: no header cell or row moves.
        const head = grid.locator('thead th[data-column-id="status"]'), title = grid.locator('thead th[data-column-id="title"]')
        await expectStableControls({
          controls: { head, title, row, next: grid.locator('tr.ticket-row', { hasText: 'PHAROS-13' }), group: grid.locator('tr.group-row').first() },
          interactions: [{ name: 'live rename event', run: async () => {
            data.statusNames.revision = 2; data.statusNames.names = { in_progress: { en: 'Under construction' } }
            await page.evaluate(() => (window as unknown as { fixtureQuietStreams: { emit(type: string, data: string, id: number): void } }).fixtureQuietStreams.emit('tenant.status_names_updated', '{}', 9001))
            await expect(row).toContainText('Under construction')
          } }],
        })
        checks.push({ assert: 'A live rename event moves no header cell or row (movementPx 0)', width, theme, ok: true })
        data.statusNames.revision = 3; data.statusNames.names = structuredClone(BUILDING)
        await page.evaluate(() => (window as unknown as { fixtureQuietStreams: { emit(type: string, data: string, id: number): void } }).fixtureQuietStreams.emit('tenant.status_names_updated', '{}', 9002))
        await expect(row).toContainText('Building')

        // N4: the status menu names the rename and says what it is stored as.
        const openRow = grid.locator('tr.ticket-row', { hasText: 'PHAROS-14' })
        await openRow.getByRole('button', { name: /^Status: Open/ }).click()
        const menu = page.getByRole('menu', { name: 'Status of PHAROS-14' })
        const building = menu.getByRole('menuitemradio', { name: /Building/ })
        await expect(building).toBeVisible()
        expect(await visibleText(menu)).not.toContain('In progress')
        await expect(building).toHaveAttribute('data-tip', /^Stored as In progress\n/)
        await building.hover()
        await expect(page.locator('.tooltip')).toContainText('Stored as In progress')
        checks.push({ assert: 'Status menu shows “Building” with the “Stored as In progress” tip', width, theme, ok: true })
        await shot(page, testInfo, width, theme, 'N4')
        await building.click()
        await expect.poll(() => calls.filter(call => call.method === 'PATCH' && call.path === '/api/nodes/n-4').length).toBe(1)
        const patch = calls.find(call => call.method === 'PATCH' && call.path === '/api/nodes/n-4')!.body as { state?: string }
        expect(patch.state).toBe('in_progress')
        checks.push({ assert: 'Choosing “Building” stores the canonical in_progress (transitions unchanged)', width, theme, ok: true })

        // The ticket header and its activity line.
        await page.goto('/p/PHAROS/PHAROS-11')
        await expect(page.locator('.field-pick .pick-value', { hasText: /^Building$/ }).first()).toBeVisible()
        const activity = page.locator('section.activity').first()
        await expect(activity).toContainText('Building')
        expect(await visibleText(activity)).not.toContain('In progress')
        checks.push({ assert: 'Ticket header and activity line (“… to Building”), no “In progress”', width, theme, ok: true })
        await shot(page, testInfo, width, theme, 'N5-activity')
      }
    } finally { await context.close() }
  }
  record(checks, testInfo, 'aeon1401-checks-surfaces.json')
})

test('AEON-1401 a Decision Desk line reads the workspace name', async ({ browser }, testInfo) => {
  const data = fixtures()
  data.statusNames.revision = 1; data.statusNames.names = structuredClone(BUILDING)
  const { context, page } = await open(browser, testInfo, 1440, 'light', data)
  try {
    const node = data.nodes.find(n => n.id === 'n-4')!
    const step = { id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', node_id: node.id, check_id: 'manual:design-reviewed', label: 'Design review signed off', to_status: 'in_progress', owner: { id: '11111111-1111-4111-8111-111111111111', name: 'Markus Barta' }, checked: false, active: true, can_tick: true, revision: node.updated_at, definition_id: null, definition_revision: null, created_at: '2026-10-19T09:00:00Z', ticket_key: node.key, ticket_title: node.title, project_id: 'p-pharos' }
    await page.route('**/api/**', async route => {
      const path = new URL(route.request().url()).pathname, json = (body: unknown, status = 200) => route.fulfill({ status, json: body })
      if (route.request().method() === 'GET') {
        if (['/api/decision-desk', '/api/key-trim-proposals', '/api/stepup-requests'].includes(path)) return json({ items: [], has_more: false })
        if (path === '/api/rules/doctrine/inbox') return json({ items: [], pending: 0 })
        if (path === '/api/approvals') return json([])
        if (path.endsWith('/messages')) return json({ items: [], next_after: 0 })
        if (path === '/api/me/phone-approvals') return json({ error: 'Not found' }, 404)
      }
      if (path === '/api/decision-desk/projection') return json({ items: [{ id: step.id, kind: 'action_request', revision: 1, project_id: 'p-pharos', title: `Confirm: ${step.label}`, created_at: step.created_at, held: false, can_decide: true, source: `/api/nodes/${node.id}/person-steps/${step.check_id}`, href: `/decision-desk?item=m:${step.id}`, person_step: step }], counts: { open: 1, held: 0, chores: 0 }, has_more: false, as_of: '2026-10-19T09:00:00Z' })
      return route.fallback()
    })
    await page.goto('/decision-desk')
    const line = page.getByTestId(`desk-row-m:${step.id}`)
    await expect(line).toContainText('before Building')
    expect(await visibleText(line)).not.toContain('In progress')
    await shot(page, testInfo, 1440, 'light', 'N5-desk')
    record([{ assert: 'Desk line reads “before Building”', width: 1440, theme: 'light', ok: true }], testInfo, 'aeon1401-checks-desk.json')
  } finally { await context.close() }
})

test('AEON-1401 the public portal and roadmap read the workspace name per language', async ({ page }, testInfo) => {
  let names: Record<string, { en?: string; de?: string }> = { in_progress: { en: 'Building' } }
  const product = { key: 'PPR-1', title: 'Harbour office', summary: 'Work that is ready before the morning opens.' }
  await page.route('**/api/**', async route => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/me') return route.fulfill({ status: 401, json: {} })
    if (url.pathname.startsWith('/api/public/portal/')) {
      if (url.pathname.endsWith('/roadmap') || url.pathname.endsWith('/roadmap.json')) return route.fulfill({ json: { schema: 'inspr.release-roadmap.v1', product, items: [{ pill_en: 'Clear morning notes', pill_de: 'Klare Morgennotizen', benefit_en: 'You can see what is coming.', benefit_de: 'Sichtbar, was als Nächstes kommt.', target: 'r16', status: 'in_progress' }], status_names: names } })
      return route.fulfill({ json: { product, catalog: [{ key: 'F-1', title: 'Morning notes', summary: 'Notes before the office opens.', status: 'in_progress' }], wishes: [], roadmap: true, status_names: names } })
    }
    return route.fulfill({ status: 404, json: {} })
  })
  const checks: Check[] = []
  await page.goto('/portal/harbour')
  await expect(page.locator('.status.in_progress')).toHaveText('Building')
  checks.push({ assert: 'Public portal catalog shows “Building”', width: 1280, theme: 'light', ok: true })
  await page.goto('/portal/harbour/roadmap')
  await expect(page.locator('.status').first()).toHaveText('Building')
  await page.getByRole('button', { name: 'Deutsch' }).click()
  // German with only an English rename: the German built-in name.
  await expect(page.locator('.status').first()).toHaveText('In Arbeit')
  names = { in_progress: { en: 'Building', de: 'In Umsetzung' } }
  await page.reload()
  await expect(page.locator('.status').first()).toHaveText('In Umsetzung')
  checks.push({ assert: 'DE with only an EN rename shows the DE built-in; with a DE rename the DE name', width: 1280, theme: 'light', ok: true })
  record(checks, testInfo, 'aeon1401-checks-portal.json')
})
