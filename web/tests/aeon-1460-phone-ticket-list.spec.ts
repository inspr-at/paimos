// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1460, Tick spec v1 / D11. Risks: compact chrome consumes the list; controls
// move under a finger during search/filter/menu/live changes; saved desktop columns
// leak into phone rows; overflow actions disappear; desktop column geometry drifts.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { dirname } from 'node:path'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { expectStableControls } from './helpers/stable'
import { mockLeadFlow } from './lead-flow-fixtures'

type Size = { width: number; height: number }
type Theme = 'light' | 'dark'
type Box = { x: number; y: number; width: number; height: number }
type Check = { assert: string; state: string; width: number; height: number; theme: Theme; locale: string; value: unknown; expected: string; pass: boolean }
const NOW = Date.parse('2026-09-23T12:00:00Z')
const ROWS = 'tr.ticket-row:not(.ghost)'
const rows = (page: Page) => page.locator(ROWS)
const toolbar = (page: Page) => page.getByRole('toolbar', { name: 'Ticket list controls', exact: true })
const box = (el: Locator) => el.evaluate(element => { const r = element.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } })
const delta = (a: Box, b: Box) => Math.max(...(['x', 'y', 'width', 'height'] as const).map(key => Math.abs(a[key] - b[key])))
const settle = (page: Page) => page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))

// One behaviour test owns the whole matrix and ONE checks array. Every assert
// carries size, theme and locale; finally preserves failures too. Markdown carries
// the same JSON through remote-spec's artifact collector; JSON remains a CI artifact.
test('AEON-1460 compact ticket list keeps controls still and all actions reachable; desktop stays unchanged', async ({ browser }, info) => {
  test.setTimeout(360_000)
  const started = performance.now()
  const checks: Check[] = []
  const matrix: { size: Size; themes: Theme[] }[] = [
    { size: { width: 390, height: 664 }, themes: ['light', 'dark'] },
    { size: { width: 844, height: 340 }, themes: ['light', 'dark'] },
    { size: { width: 932, height: 380 }, themes: ['light'] },
    { size: { width: 320, height: 568 }, themes: ['light'] },
    { size: { width: 1024, height: 900 }, themes: ['light', 'dark'] },
    { size: { width: 1440, height: 900 }, themes: ['light', 'dark'] },
  ]
  try {
    // Each size, theme and locale uses its own browser context, so several run at once.
    // Every assertion is unchanged.
    const passes: { size: Size; theme: Theme; locale: string }[] = []
    for (const entry of matrix) for (const theme of entry.themes) for (const locale of ['en-US', 'de-AT']) passes.push({ size: entry.size, theme, locale })
    let cursor = 0
    const visit = async () => {
      for (;;) {
        const pass = passes[cursor++]
        if (!pass) return
        const { size, theme, locale } = pass
        const context = await browser.newContext({ viewport: size, locale, colorScheme: theme, reducedMotion: 'reduce', hasTouch: size.width < 1000, isMobile: size.width < 1000 })
      const page = await context.newPage()
      page.setDefaultTimeout(10_000)
      const desktop = size.height > 500 && size.width >= 720
      const name = `${size.width}x${size.height}-${theme}`
      console.info(`AEON-1460 ${name} ${locale}`)
      const add = (assert: string, value: unknown, expected: string, pass: boolean, state = 'T1') => {
        checks.push({ assert, value, expected, pass, state, ...size, theme, locale })
        expect(pass, `${name} ${locale} ${assert}: ${JSON.stringify(value)}; expected ${expected}`).toBe(true)
      }
      const shot = (state: string) => page.screenshot({ path: info.outputPath(`AEON-1460/aeon1460-${name}-${state}${locale === 'en-US' ? '' : '-de'}.png`) })
      try {
        await page.clock.setSystemTime(NOW)
        const errors = watchErrors(page)
        const data = fixtures({ bigProject: 40 })
        data.preferences.theme = { choice: theme }
        if (!desktop) {
          // The same real row shapes, with enough rows to measure four scroll offsets.
          const example = data.nodes.find(node => node.id === 'n-1')!
          for (let n = 0; n < 35; n++) data.nodes.push({ ...structuredClone(example), id: `n-phone-${n}`, key: `PHAROS-${100 + n}`, parent_id: 'p-pharos', title: `Ticket ${100 + n}`, updated_at: new Date(NOW - n * 60_000).toISOString() })
          example.title = locale === 'de-AT' ? 'Überprüfung der mandantenübergreifenden Berechtigungsverwaltung mit außergewöhnlich langen Bezeichnungen' : 'A long ticket title that remains readable by opening the whole row and never grows the list row'
          data.preferences['list:p-pharos'] = { visible: ['status', 'priority', 'assignee', 'tags', 'estimate', 'progress', 'updated'] }
        }
        await mockWork(page, data)
        await page.goto(desktop ? '/p/PHAROS?group=none' : '/p/PHAROS?group=none&sort=key')
        await expect(rows(page).first()).toBeVisible()
        await page.evaluate(() => document.fonts.ready)
        await settle(page)
        if (desktop) {
          // Captured on main 6db3aab5d with this exact fixture before product edits.
          const y = size.width === 1440 ? 163.25 : 199.25
          // Linux reserves a scrollbar gutter; macOS uses overlay scrollbars.
          const contentWidth = await page.locator('main').evaluate(el => el.clientWidth)
          const expected = [{ x: 0, y, width: contentWidth, height: 46 }, { x: 28, y, width: contentWidth - 56, height: 46 }, { x: 29, y: y + 47, width: contentWidth - 58, height: 34 }, { x: 29, y: y + 81, width: contentWidth - 58, height: 36 }]
          const actual = await page.locator('.toolbar-wrap, .toolbar-wrap .toolbar, .tickets thead, tr.ticket-row').evaluateAll(items => items.slice(0, 4).map(el => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } }))
          add('main toolbar, column head and row geometry ±1 px', actual, JSON.stringify(expected), actual.length === expected.length && actual.every((b, i) => delta(b, expected[i]!) <= 1), 'T8')
          await shot('T8')
          continue
        }
        const bar = toolbar(page)
        await shot('T1')
        const search = bar.getByRole('searchbox', { name: 'Search tickets in this project' })
        const views = bar.getByRole('tablist', { name: 'Ticket views' })
        await expect(search).toBeVisible()
        await expect(views).toBeVisible()
        await expect(bar.getByRole('button', { name: 'New ticket', exact: true })).toBeVisible()
        // Wide short windows keep the table, not the phone card. Full-width
        // sheets start at 600px (FloatingPanel); 844 still opens Filters.
        const phoneCard = size.width <= 720
        if (phoneCard) {
          const keyBox = await box(rows(page).first().locator('.c-key'))
          const titleBox = await box(rows(page).first().locator('.c-title'))
          add('key sits above the title', { keyY: keyBox.y, titleY: titleBox.y }, 'title below the key', titleBox.y > keyBox.y + keyBox.height - 1)
        }
        if (size.width <= 900) {
          const filters = bar.getByRole('button', { name: 'Filters', exact: true })
          await expect(filters).toBeVisible()
          // 320×568 de-AT wraps the restored toolbar below the fold. Scroll it
          // into view before measuring, so the click's own scroll is not movement.
          await filters.scrollIntoViewIfNeeded()
          const anchor = await box(bar)
          await filters.click()
          const sheet = page.getByRole('dialog', { name: 'Filters', exact: true })
          await expect(sheet).toBeVisible()
          if (size.width <= 600) add('Filter is a full-width phone sheet', await box(sheet), 'full viewport width', (await box(sheet)).width === size.width)
          await page.keyboard.press('Escape')
          await expect(sheet).toHaveCount(0)
          add('toolbar movementPx on filter close', delta(anchor, await box(bar)), '0', delta(anchor, await box(bar)) === 0)
        }
        await shot('T5')
        const main = page.locator('main')
        // A non-structural live change: sort stays Key and the ticket remains open.
        await main.evaluate(el => { el.scrollTop = 0 })
        const row = page.locator('#row-n-1'), before = await box(row)
        const node = data.nodes.find(node => node.id === 'n-1')!
        node.state = 'blocked'; node.updated_at = new Date(NOW + 1000).toISOString()
        await page.evaluate(({ id, revision }) => (window as unknown as { fixtureQuietStreams: { emit: (type: string, data: string, id: number) => void } }).fixtureQuietStreams.emit('node.updated', JSON.stringify({ id: 501, type: 'node.updated', actor_principal_id: '99999999-9999-4999-8999-999999999999', node_changes: [{ id, project_id: 'p-pharos', change: 'updated', fields: ['state'], revision }] }), 501), { id: node.id, revision: node.updated_at })
        await expect(row.locator('.status-btn')).toHaveAttribute('aria-label', /Blocked/)
        add('row movementPx on live status change', delta(before, await box(row)), '0', delta(before, await box(row)) === 0, 'T7')
        await row.locator('.title-link').click()
        await expect(page).toHaveURL(/PHAROS-11(?:\?|$)/)
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)
        add('no horizontal overflow', overflow, '0', overflow === 0)
        add('no browser errors', errors, '[]', errors.length === 0)
        } finally { await context.close() }
      }
    }
    await Promise.all(Array.from({ length: Math.min(4, passes.length) }, () => visit()))
  } finally {
    const path = info.outputPath('AEON-1460/checks.json')
    await mkdir(dirname(path), { recursive: true })
    const json = JSON.stringify(checks, null, 2) + '\n'
    await writeFile(path, json)
    await writeFile(info.outputPath('AEON-1460/checks.md'), json)
    await writeFile(info.outputPath('AEON-1460/runtime.md'), JSON.stringify({ seconds: Math.ceil((performance.now() - started) / 1000) }) + '\n')
    await info.attach('checks', { path, contentType: 'application/json' })
  }
})

// Each risk has an independent negative control on 94902251.
async function compactScene(page: Page, withLead = false) {
  await page.setViewportSize({ width: 390, height: 844 })
  const data = withLead ? (await mockLeadFlow(page, { lead: 'none', queue: [] })).data : fixtures()
  const title = 'Überprüfung der mandantenübergreifenden Berechtigungen und sämtlicher zuständiger Arbeitsbereiche'
  data.nodes.find(node => node.id === 'n-1')!.title = title
  if (!withLead) await mockWork(page, data)
  await page.goto('/p/PHAROS?group=none')
  await expect(page.locator('#row-n-1')).toBeVisible()
  return title
}

test('AEON-1460 compact titles retain native links and keyboard full-text disclosure', async ({ page }) => {
  await page.addInitScript(() => {
    const calls: string[] = []
    const native = window.open.bind(window)
    window.open = ((url?: string | URL, target?: string, features?: string) => {
      calls.push(String(url ?? ''))
      return native(url, target, features)
    }) as typeof window.open
    ;(window as unknown as { __titleTabs: string[] }).__titleTabs = calls
  })
  const title = await compactScene(page)
  await expect(page.locator('#row-n-1 .title-text')).toHaveAttribute('data-tip', title)
  await expect(page.locator('#row-n-1 .title-link')).toHaveAttribute('href', /PHAROS-11/)
  const bar = toolbar(page)
  await expectStableControls({ controls: { toolbar: bar, row: page.locator('#row-n-1') }, interactions: [
    { name: 'keyboard reads the clipped title', run: async () => {
      const grid = page.getByRole('grid', { name: 'Tickets', exact: true })
      await grid.focus()
      for (let i = 0; i < 12 && await grid.getAttribute('aria-activedescendant') !== 'row-n-1'; i++) await grid.press('ArrowDown')
      await expect(grid).toHaveAttribute('aria-activedescendant', 'row-n-1')
      await expect(page.locator('.tooltip')).toHaveText(title)
      await page.keyboard.press('Escape')
    } },
    { name: 'a modified link click opens exactly one native tab', run: async () => {
      const sourceURL = page.url(), link = page.locator('#row-n-1 .title-link')
      // The row links to the ticket's canonical project route (PRJ-17 for Pharos).
      const target = new URL((await link.getAttribute('href'))!, sourceURL).pathname
      expect(target).toBe('/p/PRJ-17/PHAROS-11')
      const openedBefore = await page.evaluate(() => (window as unknown as { __titleTabs: string[] }).__titleTabs.length)
      const [tab] = await Promise.all([
        page.context().waitForEvent('page'),
        link.click({ modifiers: ['ControlOrMeta'] }),
      ])
      await tab.waitForLoadState('domcontentloaded')
      expect(new URL(tab.url()).pathname).toBe(target)
      expect(page.context().pages()).toHaveLength(2)
      const opened = await page.evaluate(() => (window as unknown as { __titleTabs: string[] }).__titleTabs)
      expect(opened).toHaveLength(openedBefore + 1)
      expect(new URL(opened[opened.length - 1], sourceURL).pathname).toBe(target)
      await tab.close()
      await expect(page).toHaveURL(sourceURL)
    } },
  ] })
})

test('AEON-1460 project-head disclosure persists the shared header density', async ({ page }) => {
  await compactScene(page, true)
  await expect(page.locator('#project-header-fold')).toBeVisible()
  await expect(page.locator('.project-page')).toHaveClass(/header-compact/)
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await expect(page.locator('section.lead')).toBeVisible()
  const preference = () => page.evaluate(() => localStorage.getItem('aeon:project-header:t1:11111111-1111-4111-8111-111111111111'))
  expect(await preference()).toBeNull()
  await page.locator('.crumb-sheet').click()
  const choice = page.getByRole('menuitemcheckbox', { name: 'Show the project head above the tickets', exact: true })
  await expect(choice).toBeChecked()
  await choice.click()
  await expect(page.locator('#project-header-fold')).toBeHidden()
  await expect(page.locator('.project-page')).toHaveClass(/header-collapsed/)
  expect(await preference()).toContain('"density":"collapsed"')
  await page.goto('/p/AEON')
  await expect(page.locator('#row-n-a1')).toBeVisible()
  await expect(page.locator('#project-header-fold')).toBeHidden()
  await expect(page.locator('.project-page')).toHaveClass(/header-collapsed/)
})

test('AEON-1460 choosing Outline retains its expand and status actions on phones', async ({ page }) => {
  await compactScene(page)
  await toolbar(page).getByRole('tab', { name: 'Outline', exact: true }).click()
  const outline = page.getByRole('treegrid', { name: 'Ticket outline', exact: true })
  const twisty = outline.locator('.twisty').first(), row = twisty.locator('xpath=ancestor::tr')
  await expect(twisty).toBeVisible()
  await expect(outline.locator('thead')).toBeHidden()
  await expect(row.locator('.status-btn')).toBeVisible()
  const target = await box(twisty)
  expect(target.width).toBe(36); expect(target.height).toBe(36)
  const hits = await twisty.evaluate(el => {
    const b = el.getBoundingClientRect(), x = b.x + b.width / 2, y = b.y + b.height / 2
    return [[-21.99, 0], [21.99, 0], [0, -21.99], [0, 21.99]].map(([dx, dy]) => el.contains(document.elementFromPoint(x + dx!, y + dy!)))
  })
  expect(hits).toEqual([true, true, true, true])
  await expectStableControls({ controls: { row, twisty, title: row.locator('.title-link'), toolbar: toolbar(page) }, interactions: [
    { name: 'expand and collapse the parent', run: async () => { await twisty.click(); await expect(twisty).toHaveAttribute('aria-expanded', 'true'); await twisty.click(); await expect(twisty).toHaveAttribute('aria-expanded', 'false') } },
  ] })
  await row.locator('.title-link').click()
  await expect(page.getByRole('complementary', { name: 'Ticket details' }).getByRole('button', { name: /^Status:/ })).toBeVisible()
})

test('AEON-1460 phone rows keep the status button and the keyboard status action', async ({ page }) => {
  await compactScene(page)
  const row = page.locator('#row-n-1'), bar = toolbar(page)
  await expect(row.locator('.status-btn')).toBeVisible()
  const grid = page.getByRole('grid', { name: 'Tickets', exact: true })
  await grid.focus()
  for (let i = 0; i < 12 && await grid.getAttribute('aria-activedescendant') !== 'row-n-1'; i++) await grid.press('ArrowDown')
  await expect(grid).toHaveAttribute('aria-activedescendant', 'row-n-1')
  await expectStableControls({ controls: { row, toolbar: bar }, interactions: [
    { name: 'open and dismiss the status choices', run: async () => { await grid.press('s'); await expect(page.getByRole('menu', { name: 'Status of PHAROS-11', exact: true })).toBeVisible(); await page.keyboard.press('Escape'); await expect(page.getByRole('menu', { name: 'Status of PHAROS-11', exact: true })).toHaveCount(0) } },
  ] })
})
