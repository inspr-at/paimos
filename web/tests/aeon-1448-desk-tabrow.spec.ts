// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1448 (Tick's design decision): "Answer one by one" lives at the end of the Open/Decided tab row.
// Risks: an empty band above the tabs while nothing is open (the old reserved slot, ~80 px on a phone);
// a question arriving or leaving moves the tabs, the stamp filter or the list card; the button wraps the
// row or crowds the tabs on a narrow phone; its icon-only form loses its name or its 44 px touch target.
import { expect, test, type Page } from '@playwright/test'
import { writeFile } from 'node:fs/promises'
import type { Question } from '../src/lib/decisionDeskApi'
import { mockEffectivePermissions } from './authz-fixtures'
import { controlStability } from './control-stability'
import { fixtures, mockWork } from './work-fixtures'

const question: Question = {
  id: 'question-1448', project_id: 'p-aeon', revision: 1, state: 'open', suggested_outcome: 'once', suggestion_reason: 'ticket_default',
  created_at: '2026-10-16T08:00:00Z', updated_at: '2026-10-16T08:00:00Z',
  input: { request_id: 'question-1448', question: 'Which migration should carry the index?', context: 'The ticket asks for fast lookup without changing tenant isolation.', findings: '',
    options: [{ id: 'partial', title: 'Use the partial index', description: 'Keep the common path small.', answer: 'Use the partial index.' }, { id: 'full', title: 'Use a full index', description: 'Cover every row.', answer: 'Use a full index.' }],
    recommend: 'partial', why: 'It matches the query that runs most often.', meanwhile: 'parked', meanwhile_text: 'The agent is writing the read tests.' },
  outcomes: [{ outcome: 'once', available: true, why: '' }, { outcome: 'always', available: true, why: '' }, { outcome: 'requirement', available: true, why: '' }, { outcome: 'doctrine', available: false, why: 'Doctrine needs an authorized source.' }],
  askers: [], pending: [],
}

// Every Desk source answers empty until the question arrives; then the projection and the open read carry it.
// Kept here rather than in decision-desk-fixtures.ts, whose importers sit at the helper impact bound.
async function mockDesk(page: Page, theme: 'light' | 'dark') {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  await mockWork(page, data)
  const world = { arrived: false, reads: [] as string[] }
  await page.route('**/api/**', route => {
    const url = new URL(route.request().url()), path = url.pathname, read = route.request().method() === 'GET'
    if (read) world.reads.push(path + url.search)
    const open = world.arrived ? [question] : []
    if (path === '/api/decision-desk/projection') return route.fulfill({ json: { items: open.map(row => ({ id: row.id, kind: 'question', project_id: row.project_id, revision: row.revision, title: row.input.question, created_at: row.created_at, held: true, href: `/decision-desk?item=q:${row.id}`, source: `/api/questions/${row.id}` })), counts: { open: open.length, held: open.length, chores: 0 }, has_more: false, as_of: new Date().toISOString() } })
    if (path === '/api/decision-desk') return route.fulfill({ json: { items: url.searchParams.get('state') === 'open' ? open : [], has_more: false } })
    if (read && path === `/api/questions/${question.id}`) return route.fulfill({ json: question })
    if (path === '/api/me/permissions') {
      const permissions = mockEffectivePermissions('admin', url.searchParams.get('project_id') ?? undefined)
      permissions.workspace.permissions.push('questions.read', 'questions.decide')
      return route.fulfill({ json: permissions })
    }
    if (path === '/api/me/phone-approvals') return route.fulfill({ status: 404, json: { error: 'Not found' } })
    if (path === '/api/approvals') return route.fulfill({ json: [] })
    if (path === '/api/rules/doctrine/inbox') return route.fulfill({ json: { items: [], pending: 0 } })
    if (read && (path === '/api/stepup-requests' || path === '/api/key-trim-proposals')) return route.fulfill({ json: { items: [], has_more: false } })
    if (path === '/api/harness-sessions') return route.fulfill({ json: { items: [], next_cursor: null } })
    if (read && path.endsWith('/messages')) return route.fulfill({ json: { items: [], next_after: 0 } })
    return route.fallback()
  })
  return world
}

// A focus refresh, awaited by its question read; one already in flight would swallow the event.
async function refreshDesk(page: Page, world: { reads: string[] }) {
  const count = () => world.reads.filter(path => path.startsWith('/api/decision-desk?state=open')).length
  await expect(page.getByTestId('desk-list')).toHaveAttribute('aria-busy', 'false')
  const before = count()
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect.poll(count).toBeGreaterThan(before)
  await expect(page.getByTestId('desk-list')).toHaveAttribute('aria-busy', 'false')
}

const button = (page: Page) => page.getByTestId('desk-one-by-one')
const tabs = (page: Page) => page.getByRole('radiogroup', { name: 'Show' })

// Records every transition the round action runs, so the fade is proven by its own events, not by timing.
async function watchFade(page: Page) {
  await page.evaluate(() => {
    const seen: { property: string; duration: string }[] = []
    Object.assign(window, { aeon1448Fade: seen })
    document.addEventListener('transitionrun', event => {
      const target = event.target
      if (target instanceof HTMLElement && target.dataset.testid === 'desk-one-by-one') seen.push({ property: event.propertyName, duration: getComputedStyle(target).transitionDuration })
    }, true)
  })
  return () => page.evaluate(() => (window as unknown as { aeon1448Fade: { property: string; duration: string }[] }).aeon1448Fade)
}

// The row as drawn: the tab group, its two tabs, the button (when rendered), the subtitle and the stamp filter.
function rowGeometry(page: Page) {
  return page.evaluate(() => {
    const rect = (element: Element | null | undefined) => { if (!element) return null; const r = element.getBoundingClientRect(); return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width, height: r.height } }
    const group = document.querySelector('[role="radiogroup"][aria-label="Show"]')!
    const action = document.querySelector<HTMLElement>('[data-testid="desk-one-by-one"]')
    const label = action?.querySelector('.label')
    const stamps = document.querySelector('[role="radiogroup"][aria-label="Stamp"]'), desk = group.closest('main')!
    const note = [...document.querySelectorAll('p')].find(p => p.textContent === 'Agents keep working unless something is held up.')!
    return {
      row: rect(group.parentElement), tabs: rect(group), tabTops: [...group.querySelectorAll('button')].map(tab => Math.round(tab.getBoundingClientRect().top)),
      tabsOverflow: group.scrollWidth - group.clientWidth, action: rect(action), labelShown: !!label && getComputedStyle(label).display !== 'none',
      tip: action?.getAttribute('data-tip') ?? null, note: rect(note), section: parseFloat(getComputedStyle(note.closest('header')!).marginBottom),
      filter: rect(stamps), filterOverflow: stamps ? stamps.scrollWidth - stamps.clientWidth : 0, sideways: desk.scrollWidth - desk.clientWidth, documentSideways: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    }
  })
}

// One row at every width: tabs at its start on one line, the button at its end, never taller, never overlapping.
function expectOneRow(geometry: Awaited<ReturnType<typeof rowGeometry>>, step: string) {
  expect(new Set(geometry.tabTops).size, `${step}: the tabs stay on one line`).toBe(1)
  expect(geometry.tabsOverflow, `${step}: the tabs are not squeezed`).toBeLessThanOrEqual(0)
  // The Desk page itself; the stability guards below also hold the whole document to no sideways scroll.
  expect(geometry.sideways, `${step}: the Desk page does not scroll sideways`).toBeLessThanOrEqual(0)
  expect(Math.abs(geometry.tabs!.left - geometry.row!.left), `${step}: the tabs start the row`).toBeLessThanOrEqual(0.5)
  const action = geometry.action
  if (!action) return
  expect(Math.abs(action.right - geometry.row!.right), `${step}: the button ends the row`).toBeLessThanOrEqual(0.5)
  expect(action.left - geometry.tabs!.right, `${step}: the button keeps clear of the tabs`).toBeGreaterThanOrEqual(10)
  expect(action.height, `${step}: the button is no taller than the tabs`).toBeLessThanOrEqual(geometry.tabs!.height + 0.5)
  expect(Math.abs((action.top + action.bottom) / 2 - (geometry.tabs!.top + geometry.tabs!.bottom) / 2), `${step}: the button sits on the tabs' centre line`).toBeLessThanOrEqual(0.5)
}

async function deskRow(page: Page, locale: string, widths: number[]) {
  for (const width of widths) for (const theme of ['light', 'dark'] as const) {
    const step = `${locale} ${width} ${theme}`
    await page.unrouteAll({ behavior: 'ignoreErrors' })
    await page.setViewportSize({ width, height: width < 400 ? 844 : 1000 })
    const world = await mockDesk(page, theme)
    await page.goto('/decision-desk')
    await expect(page.getByRole('heading', { level: 1, name: 'Decision Desk' })).toBeVisible()
    await expect(page.getByTestId('desk-tab-open')).toHaveText(/Open\s*0$/)
    await expect(page.getByTestId('desk-list')).toHaveAttribute('aria-busy', 'false')

    // Open 0: nothing rendered, nothing reserved. The tabs follow the subtitle at the header's own section gap.
    await expect(button(page)).toHaveCount(0)
    const empty = await rowGeometry(page)
    expect(Math.abs(empty.tabs!.top - empty.note!.bottom - empty.section), `${step}: no band, the gap is the section gap`).toBeLessThanOrEqual(0.5)
    expectOneRow(empty, `${step} open 0`)
    await page.screenshot({ path: test.info().outputPath(`open0-${width}-${theme}-${locale}.png`) })

    // Arrival and leave on the Decided tab: no box but the button's own changes, the card included.
    const fade = await watchFade(page)
    await page.getByTestId('desk-tab-decided').click()
    await expect(page.getByRole('radiogroup', { name: 'Stamp' })).toBeVisible()
    const decided = await controlStability(page, {
      open: page.getByTestId('desk-tab-open'), decided: page.getByTestId('desk-tab-decided'), tabs: tabs(page),
      stamps: page.getByRole('radiogroup', { name: 'Stamp' }), card: page.getByTestId('desk-list'),
      heading: page.getByRole('heading', { level: 1, name: 'Decision Desk' }),
    })
    await decided.check(async () => {
      world.arrived = true
      await refreshDesk(page, world)
      await expect(button(page)).toBeVisible()
      await expect(button(page)).toHaveCSS('opacity', '1')
      await expect(page.getByTestId('desk-tab-open')).toHaveText(/Open\s*1$/)
    })
    // The arrival is a 120 ms opacity fade and nothing else.
    await expect.poll(fade).toContainEqual({ property: 'opacity', duration: '0.12s' })
    expect((await fade()).every(entry => entry.property === 'opacity'), `${step}: the fade animates opacity only`).toBe(true)
    const arrived = await rowGeometry(page)
    expectOneRow(arrived, `${step} decided with a question`)
    await expect(button(page)).toHaveAccessibleName('Answer one by one')
    // On phones below about 384 px the arrow alone, named by its label and, on fine pointers, a tip.
    if (width < 384) { expect(arrived.labelShown, `${step}: icon only`).toBe(false); expect(arrived.tip).toBe('Answer one by one') }
    else { expect(arrived.labelShown, `${step}: the label shows`).toBe(true); expect(arrived.tip).toBeNull() }
    await page.screenshot({ path: test.info().outputPath(`decided-${width}-${theme}-${locale}.png`) })
    await decided.check(async () => {
      world.arrived = false
      await refreshDesk(page, world)
      await expect(button(page)).toHaveCount(0)
      await expect(page.getByTestId('desk-tab-open')).toHaveText(/Open\s*0$/)
    })
    decided.done()

    // Arrival on the Open tab: the tabs keep their box; the card keeps its place and width and grows downward.
    await page.getByTestId('desk-tab-open').click()
    const card = (await page.getByTestId('desk-list').boundingBox())!
    const open = await controlStability(page, { open: page.getByTestId('desk-tab-open'), decided: page.getByTestId('desk-tab-decided'), tabs: tabs(page), heading: page.getByRole('heading', { level: 1, name: 'Decision Desk' }) })
    await open.check(async () => {
      world.arrived = true
      await refreshDesk(page, world)
      await expect(page.getByTestId(`desk-row-q:${question.id}`)).toBeVisible()
      await expect(button(page)).toHaveCSS('opacity', '1')
    })
    open.done()
    const after = (await page.getByTestId('desk-list').boundingBox())!
    for (const axis of ['x', 'y', 'width'] as const) expect(Math.abs(after[axis] - card[axis]), `${step}: card ${axis}`).toBeLessThanOrEqual(0.5)
    const withQuestion = await rowGeometry(page)
    expectOneRow(withQuestion, `${step} open with a question`)
    expect(Math.abs(withQuestion.tabs!.top - withQuestion.note!.bottom - withQuestion.section), `${step}: the same gap with a question`).toBeLessThanOrEqual(0.5)
    await page.screenshot({ path: test.info().outputPath(`open1-${width}-${theme}-${locale}.png`) })

    // The button still starts the round from its new place, icon-only included.
    if (width === 320 || width === 1440) {
      await button(page).click()
      await expect(page.getByTestId('desk-paper').getByRole('heading', { level: 2 })).toHaveText(question.input.question)
      await page.getByTestId('desk-paper').press('Escape')
      await expect(page.getByRole('dialog', { name: 'Decision Desk memo' })).not.toBeVisible()
    }
  }
}

// The Desk speaks one language (English); a German browser must get the same row. 1024 is evidence only.
for (const [locale, widths] of [['en-US', [1440, 1024, 390, 320]], ['de-DE', [1440, 390, 320]]] as const) test.describe(locale, () => {
  test.use({ locale, reducedMotion: 'no-preference' })
  test('AEON-1448: the round action sits at the tab row\'s end and arrives without moving anything', async ({ page }) => {
    test.setTimeout(240_000)
    await deskRow(page, locale, [...widths])
  })
})

// The row across widths, with the stamp filter (the widest case): one line of tabs, the button at the end,
// the label down to a 384 px phone, the filter on the first line only when it fits beside the button.
test('AEON-1448: one tab row from 320 to 1440 px, the stamp filter never pushed by the button', async ({ page }) => {
  test.setTimeout(120_000)
  const world = await mockDesk(page, 'light')
  world.arrived = true
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.goto('/decision-desk')
  await expect(button(page)).toBeVisible()
  await page.getByTestId('desk-tab-decided').click()
  // The document column is information only: right after a live resize the app header may still be re-fitting.
  const lines: string[] = ['| viewport | row | tabs | button | filter | label | filter line | document sideways |', '|---|---|---|---|---|---|---|---|']
  const samples: [number, Awaited<ReturnType<typeof rowGeometry>>][] = []
  for (const width of [1440, 1280, 1024, 960, 900, 768, 600, 414, 390, 384, 383, 375, 360, 340, 320]) {
    await page.setViewportSize({ width, height: 900 })
    const geometry = await rowGeometry(page)
    samples.push([width, geometry])
    lines.push(`| ${width} | ${geometry.row!.width.toFixed(1)} | ${geometry.tabs!.width.toFixed(1)} | ${geometry.action!.width.toFixed(1)} | ${geometry.filter!.width.toFixed(1)} | ${geometry.labelShown} | ${Math.abs(geometry.filter!.top - geometry.tabs!.top) < geometry.tabs!.height ? 'first' : 'own'} | ${geometry.documentSideways} |`)
  }
  // The measurements first, so a failure still leaves the numbers behind.
  await writeFile(test.info().outputPath('row-widths.md'), lines.join('\n') + '\n')
  for (const [width, geometry] of samples) {
    expectOneRow(geometry, `${width}`)
    const filter = geometry.filter!, action = geometry.action!, tabsBox = geometry.tabs!
    const sameLine = Math.abs(filter.top - tabsBox.top) < tabsBox.height
    if (sameLine) {
      expect(filter.left - tabsBox.right, `${width}: the filter follows the tabs`).toBeGreaterThanOrEqual(10)
      expect(action.left - filter.right, `${width}: the button keeps clear of the filter`).toBeGreaterThanOrEqual(10)
      expect(geometry.filterOverflow, `${width}: the filter is not squeezed`).toBeLessThanOrEqual(0)
    } else expect(filter.top, `${width}: the filter takes its own line below`).toBeGreaterThanOrEqual(tabsBox.bottom)
    expect(geometry.labelShown, `${width}: the label down to 384 px`).toBe(width >= 384)
  }
})

// Coarse pointers: a 44 px tall button (44 × 44 when icon-only) that still fits inside the tab row's height.
test.describe('touch', () => {
  test.use({ hasTouch: true, isMobile: true })
  test('AEON-1448: the round action keeps a 44 px target and adds no height to the row', async ({ page }) => {
    test.setTimeout(120_000)
    for (const width of [390, 320]) {
      await page.unrouteAll({ behavior: 'ignoreErrors' })
      await page.setViewportSize({ width, height: 844 })
      const world = await mockDesk(page, 'light')
      await page.goto('/decision-desk')
      await expect(page.getByTestId('desk-tab-open')).toHaveText(/Open\s*0$/)
      await expect(button(page)).toHaveCount(0)
      const empty = await rowGeometry(page)
      expect(Math.abs(empty.tabs!.top - empty.note!.bottom - empty.section), `${width}: no band`).toBeLessThanOrEqual(0.5)
      const guard = await controlStability(page, { open: page.getByTestId('desk-tab-open'), decided: page.getByTestId('desk-tab-decided'), tabs: tabs(page) })
      await guard.check(async () => {
        world.arrived = true
        await refreshDesk(page, world)
        await expect(button(page)).toBeVisible()
      })
      guard.done()
      const geometry = await rowGeometry(page)
      expectOneRow(geometry, `touch ${width}`)
      expect(geometry.action!.height, `${width}: 44 px tall`).toBeGreaterThanOrEqual(44)
      if (width < 360) expect(geometry.action!.width, `${width}: 44 px wide when icon-only`).toBeGreaterThanOrEqual(44)
      await page.screenshot({ path: test.info().outputPath(`touch-${width}.png`) })
    }
  })
})
