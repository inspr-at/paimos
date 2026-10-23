// SPDX-License-Identifier: AGPL-3.0-only
// Hours: the week grid, fast entry, people and agents, period review and approval.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { writeFile } from 'node:fs/promises'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { addHistoricalPeriod, businessData, mira, mockBusiness, nova, WEEK39, type BusinessMockOptions } from './business-fixtures'
import { colourContrast } from '../src/lib/themeEngine'
import { expectStableControls } from './helpers/stable'
import { domAudit } from './ui-audit-rules'
import { sampleRenderedFont } from '../../scripts/test-prune/visual-renderer.mjs'

test.use({ timezoneId: 'Europe/Vienna' })

async function setup(page: Page, options: BusinessMockOptions = {}) {
  await mockWork(page, fixtures())
  const data = businessData(options)
  const calls = await mockBusiness(page, data, options)
  return { data, calls }
}
async function openWeek(page: Page, path = '/business/hours') {
  await page.goto(path)
  await expect(page.getByRole('heading', { name: 'Hours', level: 1 })).toBeVisible()
  await expect(page.getByRole('table', { name: 'Hours per ticket and day' })).toBeVisible()
}
const grid = (page: Page) => page.getByRole('table', { name: 'Hours per ticket and day' })
const form = (page: Page) => page.getByRole('form', { name: 'Log time' })

test('an older week shows existing hours and logs into its existing period', async ({ page }) => {
  const { data, calls } = await setup(page)
  const period = addHistoricalPeriod(data)
  await openWeek(page, '/business/hours?week=2024-09-16')
  await expect(grid(page).locator('tfoot td').last()).toHaveText('6:15')
  await expect(page.getByRole('region', { name: 'Entries' }).getByText('Provisioning spike')).toBeVisible()
  const read = calls.find(c => c.path === '/api/time-periods' && c.query.get('principal_id') === me.id)!
  expect(read.query.get('since')).toBe(period.starts_at)
  expect(read.query.get('until')).toBe(period.ends_at)
  await form(page).getByRole('combobox', { name: 'Cost unit' }).selectOption({ label: 'Development' })
  await form(page).getByRole('button', { name: /^Ticket:/ }).click()
  await page.getByRole('combobox', { name: 'Ticket' }).fill('PHAROS-11')
  await expect(page.getByRole('option', { name: /PHAROS-11/ })).toBeVisible()
  await page.keyboard.press('Enter')
  await form(page).getByRole('radio', { name: 'Tue 17' }).click()
  await form(page).getByRole('textbox', { name: 'Duration' }).fill('15m')
  await form(page).getByRole('textbox', { name: 'Duration' }).press('Enter')
  await expect(page.getByText('Logged 15m on PHAROS-11.')).toBeVisible()
  await expect(grid(page).locator('tfoot td').last()).toHaveText('6:30')
  expect(calls.filter(c => c.path === '/api/time-periods' && c.method === 'POST')).toEqual([])
  expect(calls.find(c => c.path === '/api/time-entries' && c.method === 'POST')?.body).toMatchObject({ period_id: period.id })
  expect(data.entries.filter(e => e.period_id === period.id)).toHaveLength(3)
})

test('open periods older than a year appear on a direct approvals load', async ({ page }) => {
  const { data } = await setup(page)
  addHistoricalPeriod(data)
  await page.goto('/business/hours?view=approvals')
  const waiting = page.getByRole('region', { name: 'Waiting for approval' })
  await expect(waiting.getByRole('button')).toHaveCount(2)
  await expect(waiting.getByRole('button').first()).toContainText('16–22 Sep 2024')
  await expect(waiting.getByRole('button').first()).toContainText('6h 15m')
})

test('open periods older than a year appear in the home waiting count and week badge', async ({ page }) => {
  const { data } = await setup(page)
  addHistoricalPeriod(data)
  await page.goto('/business')
  await expect(page.getByText('8h 30m logged this week · 2 periods to approve · 3 rates in force')).toBeVisible()
  const queue = page.getByRole('list', { name: 'Periods waiting for approval' })
  await expect(queue.getByRole('link')).toHaveCount(2)
  await expect(queue.getByRole('link').first()).toContainText('16–22 Sep 2024')
  await expect(queue.getByRole('link').first()).toContainText('6h 15m')
  await openWeek(page)
  await expect(page.getByRole('radio', { name: /Approvals/ })).toContainText('2')
  // AEON-870: the count stays readable on its gold fill in both modes (WCAG AA, 4.5:1).
  const badge = page.getByRole('radio', { name: /Approvals/ }).locator('.badge')
  for (const mode of ['light', 'dark'] as const) {
    await page.evaluate(mode => { document.documentElement.dataset.theme = mode }, mode)
    const { color, background } = await badge.evaluate(el => ({ color: getComputedStyle(el).color, background: getComputedStyle(el).backgroundColor }))
    const hex = (rgb: string) => '#' + rgb.match(/\d+/g)!.slice(0, 3).map(channel => Number(channel).toString(16).padStart(2, '0')).join('')
    expect(colourContrast(hex(color), hex(background)), `${mode}: ${color} on ${background}`).toBeGreaterThanOrEqual(4.5)
  }
})

test('the week grid sums per ticket and day with exact amounts', async ({ page }, info) => {
  const errors = watchErrors(page)
  const { data } = await setup(page)
  // Adjacent five-character days expose run-together values, and a fully
  // logged week exercises the widest realistic weekly total (168:00).
  const fullDay = data.entries.find(entry => entry.id === 'e-2')!
  for (const [i, id] of ['e-1', 'e-2', 'e-3', 'e-4'].entries()) {
    const entry = data.entries.find(entry => entry.id === id)!
    entry.started_at = new Date(Date.parse(WEEK39.starts_at) + i * 86400_000).toISOString()
    entry.duration_seconds = (i === 0 ? 10.5 : 24) * 3600
    entry.ended_at = new Date(Date.parse(entry.started_at) + entry.duration_seconds * 1000).toISOString()
    entry.amount = i === 0 ? '997.5000' : i === 3 ? '2040.0000' : '2280.0000'
  }
  for (const i of [4, 5, 6]) {
    const started = Date.parse(WEEK39.starts_at) + i * 86400_000
    data.entries.push({ ...fullDay, id: `e-full-${i}`, started_at: new Date(started).toISOString(),
      ended_at: new Date(started + 86400_000).toISOString() })
  }
  data.entries.push({ ...data.entries.find(entry => entry.id === 'e-3')!, id: 'e-monday-rest',
    started_at: data.entries.find(entry => entry.id === 'e-1')!.ended_at,
    ended_at: fullDay.started_at, duration_seconds: 13.5 * 3600, amount: '1282.5000' })
  await openWeek(page)
  await expect(page.getByText('Week 39 · 21–27 Sep 2026 · 168h logged')).toBeVisible()
  const rows = grid(page).locator('tbody tr')
  await expect(rows).toHaveCount(3)
  await expect(rows.first()).toContainText('PHAROS-11')
  await expect(rows.first().locator('td')).toHaveText(['10:30', '24:00', '·', '·', '24:00', '24:00', '24:00', '106:30'])
  await expect(grid(page).locator('tfoot td')).toHaveText(['24:00', '24:00', '24:00', '24:00', '24:00', '24:00', '24:00', '168:00'])
  await expect(grid(page).locator('thead th.today')).toContainText('Thu')
  // 144h × 95 + 24h × 85, exactly.
  await expect(page.locator('.week-foot')).toContainText('15,720.00 EUR')
  await expect(page.locator('.period-chip')).toHaveText('21–27 Sep 2026')
  const entries = page.getByRole('region', { name: 'Entries' })
  await expect(entries.getByText('Thu 24')).toBeVisible()
  const thursdayEntry = entries.locator('.entry').filter({ hasText: 'Connector sketch' })
  await expect(thursdayEntry).toContainText('00:00–00:00')
  await expect(thursdayEntry).toContainText('2,040.00')
  // AEON-1687: the complete seven-day grid must fit its own container on a
  // phone, rather than concealing a wider table in a horizontal scroller.
  const measurements = []
  for (const width of [390, 320, 520, 560, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
    await page.setViewportSize({ width, height: 900 })
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await page.evaluate(() => document.fonts.ready)
    const dayFont = await sampleRenderedFont(page, '.week-grid tbody .c-day')
    expect(dayFont.renderedFamilies, `${width} ${theme}: day values render in the bundled font`).toEqual(['JetBrains Mono'])
    const scroll = page.getByRole('region', { name: 'Weekly hours', exact: true })
    await scroll.focus()
    // AEON-1699 fix1: the focused region's outline also paints the header's
    // top edge. Checking only the table borders would miss the teal rule.
    const headerEdge = await scroll.evaluate(element => {
      const probe = document.createElement('span')
      probe.style.borderTop = '1px solid var(--line)'
      element.append(probe)
      const neutral = getComputedStyle(probe).borderTopColor
      probe.remove()
      const style = getComputedStyle(element)
      return { neutral, outline: { width: parseFloat(style.outlineWidth), color: style.outlineColor },
        shadow: style.boxShadow,
        borders: [element, ...element.querySelectorAll('thead, thead tr, thead th')].map(edge => {
          const style = getComputedStyle(edge)
          return { width: parseFloat(style.borderTopWidth), color: style.borderTopColor }
        }) }
    })
    for (const border of headerEdge.borders.filter(border => border.width > 0)) {
      expect(border.color, `${width} ${theme}: header top border uses the neutral line token`).toBe(headerEdge.neutral)
      expect(border.width, `${width} ${theme}: header top border is a hairline`).toBe(1)
    }
    if (headerEdge.outline.width > 0) {
      expect(headerEdge.outline.color, `${width} ${theme}: focused header top edge uses the neutral line token`).toBe(headerEdge.neutral)
      expect(headerEdge.outline.width, `${width} ${theme}: focus outline is a hairline`).toBe(1)
    }
    expect(headerEdge.shadow, `${width} ${theme}: no accent shadow around the header`).toBe('none')
    const dimensions = await scroll.evaluate(element => ({
      overflow: element.scrollWidth - element.clientWidth, width: element.clientWidth,
      descendants: Array.from(element.querySelectorAll('*')).flatMap(child => {
        const box = child.getBoundingClientRect(), bounds = element.getBoundingClientRect()
        const range = document.createRange(); range.selectNodeContents(child)
        const text = range.getBoundingClientRect()
        return box.right > bounds.right || text.right > bounds.right || child.scrollWidth > child.clientWidth
          ? [{ tag: child.tagName, class: child.className, right: box.right - bounds.right,
            textRight: text.right - bounds.right, overflow: child.scrollWidth - child.clientWidth }] : []
      }),
    }))
    expect(dimensions.overflow, `${width} ${theme}: 0 px horizontal overflow; ${JSON.stringify(dimensions)}`).toBe(0)
    // AEON-1699: a fitting table can still press its text against the card.
    // Measure the actual ink, including keys and totals, before row geometry
    // so the AEON-1691 negative control fails specifically on the inset.
    const phoneGrid = width <= 520 ? await scroll.evaluate(element => {
      const bounds = element.getBoundingClientRect()
      const ink = Array.from(element.querySelectorAll('tbody .ticket-chip, tfoot th, .c-day, tbody .c-total, tfoot .c-total')).map(cell => {
        const range = document.createRange(); range.selectNodeContents(cell)
        const text = range.getBoundingClientRect()
        return { value: cell.textContent?.trim(), width: text.width, height: text.height,
          left: text.left - bounds.left, right: bounds.right - text.right }
      })
      const headers = Array.from(element.querySelectorAll('thead .c-day')).map(cell => cell.getBoundingClientRect().toJSON())
      const rows = Array.from(element.querySelectorAll('tbody tr, tfoot tr')).map(row => {
        const key = row.querySelector('.c-ticket')!.getBoundingClientRect()
        const total = row.querySelector('.c-total')!.getBoundingClientRect()
        const days = Array.from(row.querySelectorAll('.c-day')).map(cell => cell.getBoundingClientRect().toJSON())
        const link = row.querySelector('a.ticket-chip')
        const box = row.getBoundingClientRect()
        return { height: box.height, key: key.toJSON(), total: total.toJSON(), days,
          // Both lines, including the row's outer padding, activate one link.
          taps: link ? [key.top + key.height / 2, days[0].top + days[0].height / 2].flatMap(y =>
            [box.left + 1, box.right - 1].map(x => link.contains(document.elementFromPoint(x, y)))) : [] }
      })
      const today = Array.from(element.querySelectorAll('.c-day.today')).map(cell => getComputedStyle(cell).backgroundColor)
      const weekend = Array.from(element.querySelectorAll('.c-day.weekend:not(.today)')).map(cell => getComputedStyle(cell).backgroundColor)
      const headerLabels = Array.from(element.querySelectorAll('thead .c-ticket, thead .c-total')).map(cell => cell.getBoundingClientRect().width)
      return { ink, headers, rows, today, weekend, headerLabels,
        documentOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth }
    }) : null
    if (phoneGrid) {
      expect(phoneGrid.ink.every(text => text.width > 0 && text.height > 0), 'phone ink has positive-size Range boxes').toBe(true)
      for (const text of phoneGrid.ink) {
        expect(text.left, `${width} ${theme}: ${text.value} left inner inset ≥ 12 px`).toBeGreaterThanOrEqual(12)
        expect(text.right, `${width} ${theme}: ${text.value} right inner inset ≥ 12 px`).toBeGreaterThanOrEqual(12)
      }
      expect(phoneGrid.documentOverflow, `${width} ${theme}: no document horizontal overflow`).toBe(0)
      expect(phoneGrid.headerLabels, 'Ticket and Total occupy no visible header column').toEqual([1, 1])
      expect(phoneGrid.headers).toHaveLength(7)
      expect(phoneGrid.rows).toHaveLength(4)
      for (const row of phoneGrid.rows) {
        expect(row.height, 'each two-line row is at least 44 px tall').toBeGreaterThanOrEqual(44)
        expect(row.key.top, 'key and total share the first line').toBe(row.total.top)
        expect(row.days).toHaveLength(7)
        for (const [i, day] of row.days.entries()) {
          expect(day.top, 'day values sit below the key and total').toBeGreaterThanOrEqual(Math.max(row.key.bottom, row.total.bottom))
          expect(day.x, 'day columns line up with their headers').toBe(phoneGrid.headers[i].x)
          expect(day.width, 'day columns keep the header width').toBe(phoneGrid.headers[i].width)
        }
        expect(row.taps.every(Boolean), `both lines of a ticket row share its link target: ${JSON.stringify(row)}`).toBe(true)
      }
      expect(phoneGrid.rows.flatMap(row => row.taps)).toHaveLength(12)
      expect(phoneGrid.today).toHaveLength(5)
      expect(new Set(phoneGrid.today).size, 'today keeps one full cell tint').toBe(1)
      expect(phoneGrid.weekend).toHaveLength(10)
      expect(new Set(phoneGrid.weekend).size, 'weekend shading stays consistent').toBe(1)
      expect(phoneGrid.today[0], 'today stays distinct from weekends').not.toBe(phoneGrid.weekend[0])
      expect(phoneGrid.today[0]).not.toBe('rgba(0, 0, 0, 0)')
      expect(phoneGrid.weekend[0]).not.toBe('rgba(0, 0, 0, 0)')
      await expect(rows.first().getByRole('rowheader')).toContainText('PHAROS-11')
      await expect(rows.first().getByRole('cell')).toHaveCount(8)
      await expect(rows.first().getByRole('cell').last()).toHaveAccessibleName('Total 106:30')
    }
    const controls = { navigation: page.getByRole('group', { name: 'Week', exact: true }),
      person: page.getByRole('button', { name: 'Hours of You. Choose a person or agent' }),
      row: rows.first(), ticket: rows.first().getByRole('link'), grid: scroll }
    const boxes = await Promise.all(Object.values(controls).map(control => control.boundingBox()))
    await expectStableControls({ controls, scrollAreas: { grid: scroll }, interactions: [
      { name: 'read the last day with the keyboard', run: async () => { await scroll.press('ArrowRight') } },
      { name: 'return to the first day', run: async () => { await scroll.press('ArrowLeft') } },
      { name: 'focus the ticket without moving its two-line row', run: async () => {
        await controls.ticket.focus()
        await expect(controls.ticket).toBeFocused()
        if (phoneGrid) expect(await rows.first().evaluate(row => getComputedStyle(row).boxShadow), 'the whole row shows keyboard focus').not.toBe('none')
      } },
      { name: 'return focus to the grid', run: async () => { await scroll.focus() } },
    ] })
    const dayCells = await grid(page).locator('.c-day').evaluateAll(cells => cells.map(cell => {
      const style = getComputedStyle(cell), range = document.createRange()
      range.selectNodeContents(cell)
      return { text: cell.textContent, scrollWidth: cell.scrollWidth, clientWidth: cell.clientWidth,
        value: cell.tagName === 'TD',
        textWidth: range.getBoundingClientRect().width,
        contentWidth: cell.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight) }
    }))
    expect(dayCells.some(cell => cell.text === '10:30'), 'the grid includes a five-character day value').toBe(true)
    expect(dayCells.filter(cell => cell.scrollWidth > cell.clientWidth), `${width} ${theme}: every day value fits its cell`).toEqual([])
    expect(dayCells.filter(cell => cell.textWidth > cell.contentWidth + 0.5), `${width} ${theme}: day text stays inside the content box`).toEqual([])
    expect(dayCells.filter(cell => cell.value && cell.contentWidth - cell.textWidth < 1),
      `${width} ${theme}: every day value has at least 1 px slack`).toEqual([])
    // At 521–600px the Total cell can fit while its text spills into Sunday.
    // Check the text edges against the padded content box in every grid row.
    const totalCells = await grid(page).locator(width <= 520 ? 'tbody .c-total, tfoot .c-total' : '.c-total').evaluateAll(cells => cells.map(cell => {
      const style = getComputedStyle(cell), box = cell.getBoundingClientRect(), range = document.createRange()
      range.selectNodeContents(cell)
      const text = range.getBoundingClientRect()
      return { value: cell.textContent?.trim(), text: { left: text.left, right: text.right, width: text.width, height: text.height },
        content: { left: box.left + parseFloat(style.borderLeftWidth) + parseFloat(style.paddingLeft),
          right: box.right - parseFloat(style.borderRightWidth) - parseFloat(style.paddingRight) } }
    }))
    expect(totalCells).toHaveLength(width <= 520 ? 4 : 5) // Phone totals share the key line; the header is screen-reader only.
    expect(totalCells.some(cell => cell.value === '168:00'), 'Total includes the widest weekly clock').toBe(true)
    expect(totalCells.every(cell => cell.text.width > 0 && cell.text.height > 0), `${width} ${theme}: Total text boxes have positive size`).toBe(true)
    expect(totalCells.filter(cell => cell.text.left < cell.content.left - 0.5 || cell.text.right > cell.content.right + 0.5),
      `${width} ${theme}: every Total text box stays inside its content box`).toEqual([])
    expect(totalCells.filter(cell => cell.content.right - cell.content.left - cell.text.width < 1),
      `${width} ${theme}: every Total has at least 1 px slack`).toEqual([])
    // Measure actual text, including both lines of the header. Cell boxes can
    // fit perfectly while the adjacent time values still read as one number.
    const textRows = await grid(page).locator('tr').evaluateAll((rows, phone) => rows.map(row => {
      const header = row.parentElement?.tagName === 'THEAD'
      const cells = Array.from(row.querySelectorAll(phone ? '.c-day' : '.c-day, .c-total'))
      const text = cells.map(cell => {
        const walker = document.createTreeWalker(cell, NodeFilter.SHOW_TEXT)
        const rects: DOMRect[] = []
        while (walker.nextNode()) {
          if (!walker.currentNode.textContent?.trim()) continue
          const range = document.createRange()
          range.selectNodeContents(walker.currentNode)
          rects.push(...Array.from(range.getClientRects()))
        }
        return { value: cell.textContent, left: Math.min(...rects.map(rect => rect.left)),
          right: Math.max(...rects.map(rect => rect.right)),
          visible: rects.length > 0 && rects.every(rect => rect.width > 0 && rect.height > 0) }
      })
      return { label: row.querySelector('th')?.textContent?.trim(), header, text,
        gaps: text.slice(1).map((next, i) => next.left - text[i].right) }
    }), width <= 520)
    expect(textRows).toHaveLength(5) // Header, all three tickets and per-day totals.
    for (const row of textRows) {
      expect(row.text).toHaveLength(width <= 520 ? 7 : 8)
      expect(row.text.every(text => text.visible), `${width} ${theme}: ${row.label} has positive text boxes`).toBe(true)
      expect(row.gaps).toHaveLength(width <= 520 ? 6 : 7)
      for (const [i, gap] of row.gaps.entries()) {
        expect(gap, `${width} ${theme}: ${row.label}, ${row.text[i].value} → ${row.text[i + 1].value}`).toBeGreaterThanOrEqual(6)
      }
    }
    // Disabling only this round's compact rule restores the pre-fix layout.
    // Tablet and desktop must retain exactly the same control and grid boxes.
    const unchangedWideLayout = await page.evaluate(() => {
      const compact = Array.from(document.styleSheets).flatMap(sheet => Array.from(sheet.cssRules))
        .find((rule): rule is CSSMediaRule => rule instanceof CSSMediaRule
          && rule.conditionText === '(max-width: 520px)' && rule.cssText.includes('.week-grid'))
      if (!compact) throw new Error('Compact Hours rule not found')
      const measure = () => Array.from(document.querySelectorAll('.toolbar, .week-card, .week-grid th, .week-grid td, .week-foot'))
        .map(element => element.getBoundingClientRect().toJSON())
      const measureTotals = () => Array.from(document.querySelectorAll('.week-grid .c-total')).map(element => ({
        box: element.getBoundingClientRect().toJSON(), fontSize: getComputedStyle(element).fontSize,
      }))
      const fixed = measure(), fixedTotals = measureTotals(), condition = compact.media.mediaText
      try {
        compact.media.mediaText = 'not all'
        return { fixed, fixedTotals, previous: measure(), previousTotals: measureTotals() }
      } finally { compact.media.mediaText = condition }
    })
    if (width > 520) expect(unchangedWideLayout.fixedTotals, `${width} ${theme}: Total keeps its position, size and font`).toEqual(unchangedWideLayout.previousTotals)
    if (width >= 1024) expect(unchangedWideLayout.fixed, `${width} ${theme}: 0 px change from the pre-fix layout`).toEqual(unchangedWideLayout.previous)
    measurements.push({ width, theme, headerEdge, dayFont, dayCells, phoneGrid, textRows, totalCells, unchangedWideLayout })
    const crowdedTickets = await grid(page).locator('tbody th').evaluateAll(cells => cells.flatMap(cell => {
      const chip = cell.querySelector('.ticket-chip')!, edge = chip.getBoundingClientRect().right
      const neighbour = window.matchMedia('(max-width: 520px)').matches ? cell.parentElement!.querySelector('.c-total')! : cell.nextElementSibling!
      const range = document.createRange(); range.selectNodeContents(neighbour)
      const gap = range.getBoundingClientRect().left - edge
      return edge > cell.getBoundingClientRect().right || gap < 6 ? [{ ticket: chip.textContent, gap }] : []
    }))
    expect(crowdedTickets, 'ticket names stay inside their column, clear of the next value').toEqual([])
    expect(await Promise.all(Object.values(controls).map(control => control.boundingBox())), 'week controls and rows move by 0 px').toEqual(boxes)
    const layout = (await page.evaluate(domAudit)).filter(row => ['horizontal-overflow', 'interactive-overlap', 'clipped-text'].includes(row.kind))
    expect(layout, `${width} ${theme}: week descendants fit without clipping or overlap`).toEqual([])
    const axe = await new AxeBuilder({ page }).include('.biz-page')
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
    expect(axe.violations, `${width} ${theme}: Hours WCAG A/AA`).toEqual([])
    await page.screenshot({ path: info.outputPath(`AEON-1699-hours-${width}-${theme}-fix3.png`) })
  }
  await writeFile(info.outputPath('AEON-1699-hours-checks.json'), JSON.stringify(measurements, null, 2))
  expect(errors).toEqual([])
})

test('logging time: ticket, duration like 1h30, Enter', async ({ page }) => {
  const { data, calls } = await setup(page)
  await openWeek(page)
  await form(page).getByRole('combobox', { name: 'Cost unit' }).selectOption({ label: 'Development' })
  await form(page).getByRole('button', { name: /^Ticket:/ }).click()
  await page.getByRole('combobox', { name: 'Ticket' }).fill('oracle')
  await expect(page.getByRole('option', { name: /PHAROS-12/ })).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(form(page).getByRole('textbox', { name: 'Duration' })).toBeFocused()
  await page.keyboard.type('1h30')
  await expect(form(page)).toContainText('1h 30m · 142.50 EUR')
  await page.keyboard.press('Enter')
  await expect(page.getByText('Logged 1h 30m on PHAROS-12.')).toBeVisible()
  const post = calls.find(c => c.path === '/api/time-entries' && c.method === 'POST')!
  // Thursday, after the day's last entry (08:00–09:30), in the open week period.
  expect(post.body).toEqual({
    period_id: 'p-me-39', cost_unit_node_id: 'cu-dev', currency: 'EUR', principal_id: me.id, node_id: 'n-2',
    started_at: '2026-09-24T07:30:00.000Z', ended_at: '2026-09-24T09:00:00.000Z', note: '', source: 'manual',
  })
  await expect(grid(page).locator('tfoot td').last()).toHaveText('10:00')
  // The ticket, cost unit and day stay; the duration is cleared for the next entry.
  await expect(form(page).getByRole('textbox', { name: 'Duration' })).toHaveValue('')
  await expect(form(page).getByRole('button', { name: /^Ticket: PHAROS-12/ })).toBeVisible()
  expect(data.entries.length).toBe(8)
})

test('a new week opens its period with the first entry; bad input never posts', async ({ page }) => {
  const { calls } = await setup(page)
  await openWeek(page, '/business/hours?week=2026-09-28')
  await expect(page.getByText('No period for this week yet. The first entry opens one, Monday to Sunday.')).toBeVisible()
  await form(page).getByRole('button', { name: /^Ticket:/ }).click()
  await page.getByRole('combobox', { name: 'Ticket' }).fill('PHAROS-11')
  await expect(page.getByRole('option', { name: /PHAROS-11/ })).toBeVisible()
  await page.keyboard.press('Enter')
  await page.keyboard.type('45')
  await page.keyboard.press('Enter')
  await expect(form(page)).toContainText('Choose a cost unit.')
  await form(page).getByRole('combobox', { name: 'Cost unit' }).selectOption({ label: 'Design' })
  await form(page).getByRole('textbox', { name: 'Duration' }).fill('45')
  await form(page).getByRole('textbox', { name: 'Duration' }).press('Enter')
  await expect(form(page)).toContainText('Use a duration like 1h30, 90m or 1.5')
  expect(calls.some(c => c.method === 'POST')).toBe(false)
  await form(page).getByRole('radio', { name: 'Mon 28' }).click()
  await form(page).getByRole('textbox', { name: 'Start time (optional)' }).fill('14:00')
  await form(page).getByRole('textbox', { name: 'Duration' }).fill('45m')
  await form(page).getByRole('textbox', { name: 'Duration' }).press('Enter')
  await expect(page.getByText('Logged 45m on PHAROS-11.')).toBeVisible()
  const period = calls.find(c => c.path === '/api/time-periods' && c.method === 'POST')!
  expect(period.body).toEqual({ principal_id: me.id, starts_at: '2026-09-27T22:00:00.000Z', ends_at: '2026-10-04T22:00:00.000Z' })
  const entry = calls.find(c => c.path === '/api/time-entries' && c.method === 'POST')!
  expect(entry.body).toMatchObject({ started_at: '2026-09-28T12:00:00.000Z', ended_at: '2026-09-28T12:45:00.000Z', cost_unit_node_id: 'cu-design' })
})

test('weeks move with the arrow keys; l focuses the entry line', async ({ page }) => {
  await setup(page)
  await openWeek(page)
  await page.keyboard.press('ArrowLeft')
  await expect(page).toHaveURL(/week=2026-09-14/)
  await expect(page.getByText('Week 38 · 14–20 Sep 2026 · 6h 15m logged')).toBeVisible()
  await page.keyboard.press('t')
  await expect(page).not.toHaveURL(/week=/)
  await expect(page.getByText('Week 39 · 21–27 Sep 2026 · 8h 30m logged')).toBeVisible()
  await page.keyboard.press('l')
  await expect(form(page).getByRole('button', { name: /^Ticket:/ })).toBeFocused()
})

test('admins switch to a colleague or an agent; agents have no manual entry', async ({ page }) => {
  await setup(page)
  await openWeek(page)
  await page.getByRole('button', { name: 'Hours of You. Choose a person or agent' }).click()
  const picker = page.getByRole('combobox', { name: 'Hours of' })
  await expect(page.getByRole('listbox', { name: 'Hours of' }).getByRole('option')).toHaveText([/You/, /Mira Holm/, /Nova/])
  await picker.fill('mira')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(new RegExp(`person=${mira.id}`))
  await expect(page.getByText(/Approved by Markus Barta\. Closed for new entries/)).toHaveCount(0)
  await page.goto(`/business/hours?person=${mira.id}&week=2026-09-14`)
  await expect(page.getByText('Approved by Markus Barta. Closed for new entries and corrections.')).toBeVisible()
  await expect(form(page)).toHaveCount(0)
  await page.goto(`/business/hours?person=${nova.id}`)
  await expect(page.getByText('An agent’s time comes from its finished runs, one entry per run.')).toBeVisible()
  await expect(form(page)).toHaveCount(0)
})

test('members see only their own week, without approvals', async ({ page }) => {
  await setup(page, { role: 'member' })
  await openWeek(page, `/business/hours?person=${mira.id}`)
  await expect(page.getByRole('radio', { name: /Approvals/ })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /^Hours of/ })).toHaveCount(0)
  await expect(grid(page).locator('tbody tr')).toHaveCount(3)
})

test('approving a period sends the reviewed revision and digest', async ({ page }) => {
  const errors = watchErrors(page)
  const { data, calls } = await setup(page)
  await page.goto('/business/hours?view=approvals')
  const waiting = page.getByRole('region', { name: 'Waiting for approval' })
  await expect(waiting.getByRole('button')).toHaveCount(1)
  await expect(waiting.getByRole('button').first()).toContainText('6h 15m')
  await expect(page.getByRole('radio', { name: /Approvals/ })).toContainText('1')
  await waiting.getByRole('button').first().click()
  const panel = page.getByRole('complementary', { name: 'Period review' })
  await expect(panel.getByRole('heading', { name: 'Markus Barta' })).toBeVisible()
  await expect(panel.getByText('14–20 Sep 2026')).toBeVisible()
  await expect(panel.locator('.metric').first()).toContainText('6h 15m')
  await expect(panel.locator('.metric').last()).toContainText('593.75 EUR')
  await panel.getByRole('button', { name: 'Approve period' }).click()
  const dialog = page.getByRole('dialog', { name: /Approve Markus Barta’s hours for 14–20 Sep 2026\?/ })
  await expect(dialog).toContainText('2 entries, 6h 15m')
  await dialog.getByRole('button', { name: 'Approve period' }).click()
  await expect(page.getByText('Approved Markus Barta’s hours for 14–20 Sep 2026.')).toBeVisible()
  const approve = calls.find(c => c.path.endsWith('/approve'))!
  expect(approve.body).toEqual({ expected_revision: 3, expected_entries_sha256: '03'.repeat(32) })
  expect(data.periods.find(p => p.id === 'p-me-38')?.state).toBe('approved')
  await expect(panel.getByText('Approved', { exact: true })).toBeVisible()
  await expect(panel.getByRole('button', { name: 'Approve period' })).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(panel).toHaveCount(0)
  expect(errors).toEqual([])
})

test('a changed period refuses the approval and says so', async ({ page }) => {
  await setup(page, { approveConflict: true })
  await page.goto('/business/hours?view=approvals&period=p-me-38')
  const panel = page.getByRole('complementary', { name: 'Period review' })
  await panel.getByRole('button', { name: 'Approve period' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Approve period' }).click()
  await expect(page.getByText('Entries changed since you opened this period. Review the latest entries again.')).toBeVisible()
  await expect(panel.getByRole('button', { name: 'Approve period' })).toBeVisible()
})

test('a running period can be reviewed from the week strip', async ({ page }) => {
  await setup(page)
  await openWeek(page)
  // AEON-1214's header preview has its own descriptive accessible name.
  await page.getByRole('button', { name: 'Review', exact: true }).click()
  const panel = page.getByRole('complementary', { name: 'Period review' })
  await expect(panel.getByText('Running', { exact: true })).toBeVisible()
  await expect(panel.getByText('Still running: approving closes it now.')).toBeVisible()
  expect(WEEK39.starts_at).toBe('2026-09-20T22:00:00.000Z')
})
