// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1460, Tick spec v1 / D11. Risks: compact chrome consumes the list; controls
// move under a finger during search/filter/menu/live changes; saved desktop columns
// leak into phone rows; overflow actions disappear; desktop column geometry drifts.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { dirname } from 'node:path'
import { fixtures, mockWork, watchErrors, showProjectHead } from './work-fixtures'
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
const inside = (b: Box, size: Size) => b.x >= 0 && b.y >= 0 && b.x + b.width <= size.width + .5 && b.y + b.height <= size.height + .5
const settle = (page: Page) => page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))

async function opaque(page: Page) {
  const clip = await box(toolbar(page))
  const before = await page.screenshot({ clip, animations: 'disabled', caret: 'hide' })
  const probe = await page.addStyleTag({ content: '.table-card { visibility: hidden !important; }' })
  const after = await page.screenshot({ clip, animations: 'disabled', caret: 'hide' })
  await probe.evaluate(el => el.remove())
  return before.equals(after)
}
async function fullyVisible(page: Page) {
  return page.locator(ROWS).evaluateAll(items => items.filter(row => {
    const r = row.getBoundingClientRect(), x = r.left + r.width / 2
    if (r.top < 0 || r.bottom > innerHeight) return false
    const top = document.elementFromPoint(x, r.top + 1), bottom = document.elementFromPoint(x, r.bottom - 1)
    return top && bottom && row.contains(top) && row.contains(bottom)
  }).length)
}
async function targets(page: Page) {
  return page.locator('.app-header button, .app-header a[href], .compact-ticket-toolbar button, .phone-rows button').evaluateAll(items => items.filter(el => el.checkVisibility()).map(el => {
    const b = el.getBoundingClientRect()
    return { name: el.getAttribute('aria-label'), width: b.width, height: b.height }
  }).filter(b => b.width > 0 && b.height > 0 && (b.width < 44 || b.height < 44)))
}

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
      const landscape = !desktop && size.width >= 720
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
        const bar = toolbar(page), controls = bar.locator('.phone-ticket-tools > button')
        await shot('T1')
        const barBox = await box(bar), rowBox = await box(rows(page).first())
        add('toolbar one opaque 44 px line', barBox.height, '44', barBox.height === 44)
        const names = await controls.evaluateAll(items => items.map(el => el.getAttribute('aria-label')))
        add('visible controls exactly view, search, filter, new, more', names, 'five controls in that order', names.length === 5 && /^View:/.test(names[0]!) && names[1] === 'Search tickets' && names[2] === 'Filter tickets' && names[3] === 'New ticket' && names[4] === 'More view actions')
        const controlBoxes = await controls.evaluateAll(items => items.map(el => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } }))
        add('controls do not wrap or overlap', controlBoxes, 'same y and 44 px; no overlapping neighbours', controlBoxes.every((b, i) => b.y === barBox.y && b.height === 44 && (!i || b.x >= controlBoxes[i - 1]!.x + controlBoxes[i - 1]!.width)))
        add('phone row height', rowBox.height, landscape ? '44 ±1' : '56 ±1', Math.abs(rowBox.height - (landscape ? 44 : 56)) <= 1)
        const facts = await rows(page).first().evaluate(el => {
          const title = el.querySelector('.phone-title')!.getBoundingClientRect(), key = el.querySelector('.key')!.getBoundingClientRect(), when = el.querySelector('time')!.getBoundingClientRect()
          return { titleY: title.y, keyY: key.y, whenY: when.y, titleHeight: title.height }
        })
        add('one line landscape / two lines portrait', facts, landscape ? 'key, title and when share one line' : 'key and when share second line below the one-line title', facts.titleHeight === 20 && (landscape ? Math.abs(facts.titleY - facts.keyY) <= 4 && Math.abs(facts.titleY - facts.whenY) <= 4 : facts.keyY > facts.titleY && facts.keyY === facts.whenY))
        const forbidden = await rows(page).locator('.c-prio, .c-tags, .c-estimate, .c-progress, .status-btn, [aria-haspopup="dialog"]').count()
        add('no priority, labels, estimate, progress or status menu in rows', forbidden, '0 elements', forbidden === 0)
        const assigned = page.locator('#row-n-1 .phone-assignee'), unassigned = page.locator('#row-n-2 .phone-assignee')
        const [a, u] = await Promise.all([box(assigned), box(unassigned)])
        add('avatar slots identical for assigned and unassigned', [a.x, u.x, a.width, u.width], 'same x; 44 px targets', a.x === u.x && a.width === 44 && u.width === 44)
        const avatar = await box(assigned.locator('.avatar'))
        add('avatar 24 px; unassigned slot empty', { avatar, unassigned: await unassigned.textContent() }, '24×24; no glyph', avatar.width === 24 && avatar.height === 24 && !(await unassigned.textContent())?.trim())
        const whenText = await page.locator('#row-n-1 time').textContent()
        add('when joins numbers and units with NBSP', whenText, 'number NBSP unit', /\d\u00a0[a-z]/.test(whenText ?? ''))
        add('count placement', await bar.locator('.phone-count').isVisible(), landscape ? 'toolbar' : 'list end', (await bar.locator('.phone-count').isVisible()) === landscape)
        if (!landscape) add('list-end count copy uses NBSP', await page.locator('.list-foot.end').textContent(), 'N NBSP tickets', /^\d+\u00a0tickets$/.test((await page.locator('.list-foot.end').textContent()) ?? ''))
        add('no saved-view strip or separate select row on compact', await page.locator('.project-navigation, .phone-pick').evaluateAll(items => items.filter(el => el.checkVisibility()).length), '0 visible', (await page.locator('.project-navigation, .phone-pick').evaluateAll(items => items.filter(el => el.checkVisibility()).length)) === 0)
        if (size.width <= 390) add('every chrome/row target at least 44×44', await targets(page), '[]', (await targets(page)).length === 0)
        await shot('T1')
        const main = page.locator('main')
        const scrollBase = await bar.evaluate(el => { const main = document.querySelector('main')!; return el.getBoundingClientRect().top - main.getBoundingClientRect().top + main.scrollTop + 280 })
        const samples = []
        for (const offset of [0, 13, 27, 41]) {
          await main.evaluate((el, top) => { el.scrollTop = top }, scrollBase + offset)
          await settle(page)
          samples.push({ offset: await main.evaluate(el => el.scrollTop), rows: await fullyVisible(page), toolbarY: (await box(bar)).y })
        }
        add('four distinct scroll offsets', samples, 'four different offsets', new Set(samples.map(s => s.offset)).size === 4, 'T2')
        if (size.width !== 320) add('fully visible rows at each of four offsets', samples, `each ≥ ${landscape ? size.width === 932 ? 6 : 5 : 9}`, samples.every(s => s.rows >= (landscape ? size.width === 932 ? 6 : 5 : 9)), 'T2')
        add('toolbar sticks while reading', samples, landscape ? 'y = 0' : 'y = 44', samples.every(s => s.toolbarY === (landscape ? 0 : 44)), 'T2')
        const opaquePixels = await opaque(page)
        add('toolbar pixel check with rows behind', opaquePixels, 'same pixels with rows shown and hidden', opaquePixels, 'T2')
        await shot('T2')
        await main.evaluate(el => { el.scrollTop = 0 })
        await settle(page)
        const search = bar.getByRole('button', { name: 'Search tickets', exact: true }), more = bar.getByRole('button', { name: 'More view actions', exact: true }), view = bar.getByRole('button', { name: /^View:/ })
        const anchor = await box(bar)
        if (locale === 'en-US' && theme === 'light' && size.width !== 932) {
        await expectStableControls({ controls: { toolbar: bar, view, search, more }, scrollAreas: { page: main }, interactions: [
          { name: 'open/close search overlay', run: async () => { await search.click(); await expect(bar.getByRole('searchbox')).toBeFocused(); await shot('T6'); add('toolbar movementPx on search open', delta(anchor, await box(bar)), '0', delta(anchor, await box(bar)) === 0, 'T6'); await page.keyboard.press('Escape'); await expect(bar.getByRole('searchbox')).not.toBeFocused(); await expect(bar.getByRole('search')).toBeVisible(); await page.keyboard.press('Escape'); await expect(bar.getByRole('search')).toHaveCount(0) } },
          { name: 'open/close view control', run: async () => { await view.click(); const menu = page.getByRole('menu', { name: 'Ticket views', exact: true }); await expect(menu).toBeVisible(); for (const text of ['My open', 'All open', 'In QA', 'Recently done', 'List', 'Outline', 'Graph', 'Needs attention']) await expect(menu.getByText(text, { exact: true })).toBeVisible(); add('view menu inside viewport', await box(page.getByRole('dialog', { name: 'Ticket views', exact: true })), 'inside viewport', inside(await box(page.getByRole('dialog', { name: 'Ticket views', exact: true })), size), 'T3'); add('toolbar movementPx on view menu', delta(anchor, await box(bar)), '0', delta(anchor, await box(bar)) === 0, 'T3'); await shot('T3'); await page.keyboard.press('Escape') } },
          { name: 'open/close overflow', run: async () => { await more.click(); const menu = page.getByRole('menu', { name: 'More view actions', exact: true }); await expect(menu).toBeVisible(); expect(await menu.locator('button').allTextContents()).toEqual(['Display…', 'Hide done', 'Select tickets', 'Save view as…', 'Copy link', 'Export CSV']); add('overflow inside viewport', await box(page.getByRole('dialog', { name: 'More view actions', exact: true })), 'inside viewport', inside(await box(page.getByRole('dialog', { name: 'More view actions', exact: true })), size), 'T4'); add('toolbar movementPx on overflow', delta(anchor, await box(bar)), '0', delta(anchor, await box(bar)) === 0, 'T4'); await shot('T4'); await page.keyboard.press('Escape') } },
        ] })
        add('toolbar movementPx after search/menu close', delta(anchor, await box(bar)), '0', delta(anchor, await box(bar)) === 0, 'T6')
        if (size.width === 390 || size.width === 320 || landscape) {
          const filter = bar.getByRole('button', { name: 'Filter tickets', exact: true })
          await filter.click()
          const sheet = page.getByRole('dialog', { name: 'Filters', exact: true })
          await expect(sheet).toBeVisible()
          add('Filter is a full-width phone sheet', await box(sheet), 'full viewport width', (await box(sheet)).width === size.width, 'T5')
          const option = sheet.getByRole('checkbox', { name: /^High/ }).first()
          await expectStableControls({ controls: { frame: sheet, done: sheet.locator('footer button'), clear: sheet.getByRole('button', { name: 'Clear all', exact: true }), option }, scrollAreas: { body: sheet.locator('.sheet-scroll') }, interactions: [
            { name: 'apply priority', run: async () => { await option.check(); await expect(option).toBeChecked() } },
          ] })
          await sheet.locator('footer button').click()
          await expect(bar.locator('.filter-count')).toHaveText('1')
          add('filter badge and teal icon', await bar.locator('.phone-filter').getAttribute('class'), 'on; badge 1', (await bar.locator('.phone-filter').getAttribute('class'))?.includes(' on') === true, 'T5')
          add('toolbar movementPx on filter apply', delta(anchor, await box(bar)), '0', delta(anchor, await box(bar)) === 0, 'T5')
          await shot('T5')
          await bar.locator('.phone-filter').click(); await sheet.getByRole('button', { name: 'Clear all', exact: true }).click(); await sheet.locator('footer button').click()
          await expect(bar.locator('.filter-count')).toHaveCount(0)
          await more.click(); await page.getByRole('menuitem', { name: 'Display…', exact: true }).click()
          const display = page.getByRole('dialog', { name: 'Display options', exact: true })
          await expect(display).toBeVisible()
          add('Display is full-width phone sheet', await box(display), 'full viewport width', (await box(display)).width === size.width, 'T4')
          await display.locator('footer button').click()
          await more.click(); await page.getByRole('menuitem', { name: 'Select tickets', exact: true }).click()
          await page.locator('#row-n-1').click(); await expect(page.locator('#row-n-1 .phone-check')).toBeChecked()
          await page.getByRole('toolbar', { name: /selected ticket/ }).getByRole('button', { name: 'Clear the selection', exact: true }).click()
          await expect(page.locator('#row-n-1 .phone-check')).toHaveCount(0)
          await more.click(); await page.getByRole('menuitemcheckbox', { name: 'Hide done', exact: true }).click()
          await expect(page.locator('#row-n-5')).toBeAttached()
          await shot('T7')
          await more.click(); await page.getByRole('menuitemcheckbox', { name: 'Hide done', exact: true }).click()
        }
        if (landscape) {
          await more.click(); await page.getByRole('menuitemcheckbox', { name: 'Hide done', exact: true }).click()
          await expect(page.locator('#row-n-5')).toBeAttached()
          await shot('T7')
          await more.click(); await page.getByRole('menuitemcheckbox', { name: 'Hide done', exact: true }).click()
        }
        if (size.width === 390) {
          // Every item in the new menus does its real job, through existing routes.
          for (const preset of ['My open', 'In QA', 'Recently done', 'All open']) {
            await view.click(); await page.getByRole('menuitemradio', { name: preset, exact: true }).click()
            await expect(view).toContainText(preset)
            const query = new URL(page.url()).searchParams
            if (preset === 'My open') expect(query.get('assignee')).toContain('none')
            if (preset === 'In QA') expect(query.get('status')).toBe('qa')
            if (preset === 'Recently done') { expect(query.get('closed')).toBe('1'); expect(query.get('date')).toContain('updated:') }
          }
          await search.click()
          await bar.getByRole('searchbox').fill('Ticket 102')
          await expect(rows(page)).toHaveCount(1)
          await expect(rows(page).first()).toContainText('PHAROS-102')
          add('toolbar movementPx while searching', delta(anchor, await box(bar)), '0', delta(anchor, await box(bar)) === 0, 'T6')
          await bar.getByRole('searchbox').fill('')
          await expect.poll(() => rows(page).count()).toBeGreaterThan(1)
          await bar.getByRole('button', { name: 'Cancel', exact: true }).click()
          await more.click(); await page.getByRole('menuitem', { name: 'Save view as…', exact: true }).click()
          const save = page.getByRole('dialog', { name: 'Save view', exact: true })
          await save.getByRole('textbox', { name: 'View name', exact: true }).fill('Mobile follow-up')
          await save.getByRole('button', { name: 'Save view', exact: true }).click()
          await expect(save).toHaveCount(0)
          await expect(view).toContainText('Mobile follow-up')
          await view.click(); await page.getByRole('menuitemradio', { name: 'All open', exact: true }).click()
          await view.click(); await page.getByRole('menuitemradio', { name: 'Mobile follow-up', exact: true }).click()
          await expect(view).toContainText('Mobile follow-up')
          await view.click(); await page.getByRole('menuitemradio', { name: 'All open', exact: true }).click()
          await expect(page).toHaveURL(url => !url.searchParams.has('v'))
          await page.evaluate(() => Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async (value: string) => { Object.assign(window, { aeon1460Copied: value }) } } }))
          const link = page.url()
          await more.click(); await page.getByRole('menuitem', { name: 'Copy link', exact: true }).click()
          await expect.poll(() => page.evaluate(() => (window as unknown as { aeon1460Copied?: string }).aeon1460Copied)).toBe(link)
          // The export must walk cursors, rather than downloading just the visible page.
          await mockWork(page, data, { nativeEvents: true, listPageSize: 3 })
          await more.click()
          const downloading = page.waitForEvent('download')
          await page.getByRole('menuitem', { name: 'Export CSV', exact: true }).click()
          const download = await downloading, path = await download.path()
          expect(path).toBeTruthy()
          const csv = await readFile(path!, 'utf8')
          expect(csv).toContain('PHAROS-134')
          expect(csv).toContain('"Key","Title","Status","Assignee","Updated"')
          const failExport = (route: import('@playwright/test').Route) => route.fulfill({ status: 503, json: { error: 'Export read refused' } })
          await page.route('**/api/nodes?*', failExport)
          await more.click(); await page.getByRole('menuitem', { name: 'Export CSV', exact: true }).click()
          await expect(page.getByText(/CSV was not exported:/)).toBeVisible()
          await page.unroute('**/api/nodes?*', failExport)
          add('saved views, presets, copy link and complete CSV work', true, 'successful actions; failed CSV is explicit', true, 'T4')
          await page.goto('/p/PHAROS?group=none&sort=key')
          await expect(page.locator('#row-n-1')).toBeVisible()
        }
        }
        if (size.width === 390 && theme === 'dark') {
          await more.click(); await page.getByRole('menuitemcheckbox', { name: 'Hide done', exact: true }).click()
          await expect(page.locator('#row-n-5')).toBeAttached()
          await shot('T7')
          await more.click(); await page.getByRole('menuitemcheckbox', { name: 'Hide done', exact: true }).click()
        }
        // A non-structural live change: sort stays Key and the ticket remains open.
        await main.evaluate(el => { el.scrollTop = 0 })
        const row = page.locator('#row-n-1'), before = await box(row)
        const node = data.nodes.find(node => node.id === 'n-1')!
        node.state = 'blocked'; node.updated_at = new Date(NOW + 1000).toISOString()
        await page.evaluate(({ id, revision }) => (window as unknown as { fixtureQuietStreams: { emit: (type: string, data: string, id: number) => void } }).fixtureQuietStreams.emit('node.updated', JSON.stringify({ id: 501, type: 'node.updated', actor_principal_id: '99999999-9999-4999-8999-999999999999', node_changes: [{ id, project_id: 'p-pharos', change: 'updated', fields: ['state'], revision }] }), 501), { id: node.id, revision: node.updated_at })
        await expect(row.locator('.phone-state [role="img"]')).toHaveAttribute('aria-label', 'Blocked')
        add('row movementPx on live status change', delta(before, await box(row)), '0', delta(before, await box(row)) === 0, 'T7')
        await row.locator('time').click()
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
  const title = await compactScene(page)
  await expect(page.locator('#row-n-1 .title-text')).toHaveAttribute('data-tip', title)
  await expect(page.locator('#row-n-1 .title-link')).toHaveAttribute('href', /PHAROS-11/)
  const bar = toolbar(page), more = bar.getByRole('button', { name: 'More view actions' })
  await expectStableControls({ controls: { toolbar: bar, more, row: page.locator('#row-n-1') }, interactions: [
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
      const [tab] = await Promise.all([
        page.context().waitForEvent('page'),
        link.click({ modifiers: ['ControlOrMeta'] }),
      ])
      await tab.waitForLoadState('domcontentloaded')
      expect(new URL(tab.url()).pathname).toBe(target)
      expect(page.context().pages()).toHaveLength(2)
      await tab.close()
      await expect(page).toHaveURL(sourceURL)
    } },
  ] })
})

test('AEON-1460 compact project-head disclosure works without changing the desktop preference', async ({ page }) => {
  await compactScene(page, true)
  await expect(page.locator('#project-header-fold')).toBeHidden()
  const preference = () => page.evaluate(() => localStorage.getItem('aeon:project-header:t1:11111111-1111-4111-8111-111111111111'))
  const stored = await preference()
  await showProjectHead(page)
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await expect(page.locator('section.lead')).toBeVisible()
  await page.locator('.crumb-sheet').click()
  const choice = page.getByRole('menuitemcheckbox', { name: 'Show the project head above the tickets', exact: true })
  await expect(choice).toBeChecked()
  await choice.click()
  await expect(page.locator('#project-header-fold')).toBeHidden()
  expect(await preference()).toBe(stored)
  await showProjectHead(page)
  await page.goto('/p/AEON')
  await expect(page.locator('#row-n-a1')).toBeVisible()
  await expect(page.locator('#project-header-fold')).toBeHidden()
})

test('AEON-1460 choosing Outline retains its expand and status actions on phones', async ({ page }) => {
  await compactScene(page)
  await toolbar(page).getByRole('button', { name: /^View:/ }).click()
  await page.getByRole('menuitemradio', { name: 'Outline', exact: true }).click()
  const outline = page.getByRole('treegrid', { name: 'Ticket outline', exact: true })
  const twisty = outline.locator('.phone-fold').first(), row = twisty.locator('xpath=ancestor::tr')
  await expect(page.locator('.table-card')).toHaveClass(/phone-rows/)
  await expect(outline.locator('.status-btn, .c-prio, .c-estimate')).toHaveCount(0)
  const target = await box(twisty)
  expect(target.width).toBe(44); expect(target.height).toBe(44)
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

test('AEON-1460 compact rows retain the keyboard status action without a row status button', async ({ page }) => {
  await compactScene(page)
  const row = page.locator('#row-n-1'), bar = toolbar(page)
  await expect(row.locator('.status-btn')).toHaveCount(0)
  const grid = page.getByRole('grid', { name: 'Tickets', exact: true })
  await grid.focus()
  for (let i = 0; i < 12 && await grid.getAttribute('aria-activedescendant') !== 'row-n-1'; i++) await grid.press('ArrowDown')
  await expect(grid).toHaveAttribute('aria-activedescendant', 'row-n-1')
  await expectStableControls({ controls: { row, toolbar: bar }, interactions: [
    { name: 'open and dismiss the status choices', run: async () => { await grid.press('s'); await expect(page.getByRole('menu', { name: 'Status of PHAROS-11', exact: true })).toBeVisible(); await page.keyboard.press('Escape'); await expect(page.getByRole('menu', { name: 'Status of PHAROS-11', exact: true })).toHaveCount(0) } },
  ] })
})
