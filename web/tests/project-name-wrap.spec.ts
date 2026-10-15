// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { headerStorageKey } from '../src/lib/projectHeader'

const NAME = 'Operations Infrastructure Platform'
const DESCRIPTION = 'Überprüfung der mandantenübergreifenden Berechtigungsverwaltung und der Betriebsabläufe für sämtliche verantwortlichen Personen und Agenten. '.repeat(40)
const WIDTHS = [390, 768, 1024, 1440] as const
const DENSITIES = ['comfortable', 'compact'] as const

interface Box { x: number; y: number; width: number; height: number }
interface Reading {
  title: Box
  frame: Box
  main: Box
  block: Box
  more: Box | null
  whiteSpace: string
  lineHeight: number
  clipped: boolean
  neighbours: { name: string; box: Box }[]
  overflow: number
  spill: string
  rootFont: number
  stacked: boolean
  mainSlack: number
}

const overlaps = (a: Box, b: Box) => a.width > 0 && b.width > 0 && a.height > 0 && b.height > 0
  && a.x < b.x + b.width - 0.5 && b.x < a.x + a.width - 0.5
  && a.y < b.y + b.height - 0.5 && b.y < a.y + a.height - 0.5
const near = (actual: number, expected: number, label: string) => {
  expect(Math.abs(actual - expected), `${label}: ${actual} vs ${expected}`).toBeLessThanOrEqual(0.5)
}

async function settled(page: Page) {
  await page.evaluate(async () => {
    await document.fonts.ready
    await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
  })
}

async function readHeader(page: Page): Promise<Reading> {
  await settled(page)
  return page.evaluate(() => {
    const box = (el: Element | null) => {
      if (!el) return null
      const r = el.getBoundingClientRect()
      return { x: r.x, y: r.y, width: r.width, height: r.height }
    }
    const title = document.getElementById('project-title')!
    const style = getComputedStyle(title)
    const flex = document.querySelector('.head-flex')!
    const frame = flex.getBoundingClientRect()
    const flexStyle = getComputedStyle(flex)
    const mainEl = document.querySelector('.head-main')
    const mainStyle = mainEl ? getComputedStyle(mainEl) : null
    const stacked = flexStyle.display === 'flex' && flexStyle.flexDirection === 'column' && mainStyle?.display !== 'contents'
    let mainSlack = 0
    if (stacked && mainEl) {
      const rect = mainEl.getBoundingClientRect()
      let contentBottom = rect.top
      for (const child of mainEl.children) {
        const childStyle = getComputedStyle(child)
        if (childStyle.display === 'none' || childStyle.position === 'absolute' || childStyle.position === 'fixed') continue
        contentBottom = Math.max(contentBottom, child.getBoundingClientRect().bottom)
      }
      mainSlack = rect.bottom - contentBottom
    }
    const pageBox = document.querySelector('.project-page')!.getBoundingClientRect()
    const rel = (r: { x: number; y: number; width: number; height: number } | null) => r && r.width > 0 && r.height > 0
      ? { x: r.x - pageBox.x, y: r.y - pageBox.y, width: r.width, height: r.height } : null
    const selectors = [
      ['.key-badge', 'key'], ['.head-edit', 'edit'], ['#project-description', 'description'],
      ['.description-more', 'more'], ['.project-status-counts', 'counts'], ['.head-work', 'work'],
      ['.header-activity', 'activity'], ['.lead', 'lead'], ['.project-navigation', 'navigation'],
      ['[role="toolbar"]', 'toolbar'],
    ] as const
    const neighbours = selectors.flatMap(([selector, name]) => {
      const placed = rel(box(document.querySelector(selector)))
      return placed ? [{ name, box: placed }] : []
    })
    return {
      title: rel(box(title))!,
      frame: { x: frame.x - pageBox.x, y: frame.y - pageBox.y, width: frame.width, height: frame.height },
      main: rel(box(document.querySelector('.head-main'))) ?? { x: 0, y: 0, width: 0, height: 0 },
      block: rel(box(document.querySelector('.description-block')))!,
      more: rel(box(document.querySelector('.description-more'))),
      whiteSpace: style.whiteSpace,
      lineHeight: parseFloat(style.lineHeight) || 0,
      clipped: title.scrollWidth > title.clientWidth + 1,
      neighbours,
      overflow: document.querySelector('.project-page')!.scrollWidth - document.querySelector('.project-page')!.clientWidth,
      stacked,
      mainSlack,
      spill: (() => {
        const pageEl = document.querySelector('.project-page')!
        const edge = pageEl.getBoundingClientRect().right
        const found: { over: number; name: string }[] = []
        const label = (el: Element) => {
          const id = el.id ? `#${el.id}` : ''
          const classes = String(el.className).split(' ').filter(Boolean).slice(0, 3).join('.')
          return `${el.tagName.toLowerCase()}${id}${classes ? `.${classes}` : ''}`
        }
        const see = (el: Element) => {
          for (const child of el.children) {
            const rect = child.getBoundingClientRect()
            const style = getComputedStyle(child)
            const boxOver = rect.width > 0 ? rect.right - edge : 0
            const inkOver = style.overflowX === 'visible' && child.scrollWidth > child.clientWidth + 1 ? rect.left + child.scrollWidth - edge : 0
            const over = Math.max(boxOver, inkOver)
            if (over > 1) found.push({ over, name: `${label(child)}+${Math.round(over)}` })
            see(child)
          }
        }
        see(pageEl)
        return found.sort((a, b) => b.over - a.over).slice(0, 6).map(item => item.name).join(' ')
      })(),
      rootFont: parseFloat(getComputedStyle(document.documentElement).fontSize) || 16,
    }
  })
}

function assertName(reading: Reading) {
  expect(reading.whiteSpace, 'name stays on one line').toBe('nowrap')
  expect(reading.lineHeight).toBeGreaterThan(0)
  expect(reading.title.height).toBeGreaterThanOrEqual(reading.lineHeight * 0.7)
  expect(reading.title.height).toBeLessThanOrEqual(reading.lineHeight * 1.35)
  expect(reading.title.width, 'name keeps a readable width').toBeGreaterThanOrEqual(Math.min(160, reading.frame.width * 0.45))
  expect(reading.block.y, 'description stays below the name').toBeGreaterThanOrEqual(reading.title.y + reading.title.height - 0.5)
  const counts = reading.neighbours.find(item => item.name === 'counts')
  const beside = !!counts && counts.box.y < reading.title.y + reading.title.height - 0.5 && counts.box.y + counts.box.height > reading.title.y + 0.5
  const floor = 20 * reading.rootFont
  if (reading.main.width > 0 && beside) expect(reading.main.width, 'name column holds its floor while counts sit beside it').toBeGreaterThanOrEqual(Math.min(floor, reading.frame.width) - 1)
  expect(reading.more, 'More sits with the description').not.toBeNull()
  expect(reading.more!.y, 'More sits below the name').toBeGreaterThanOrEqual(reading.title.y + reading.title.height - 0.5)
  expect(reading.more!.x).toBeGreaterThanOrEqual(reading.block.x - 0.5)
  expect(reading.more!.x + reading.more!.width).toBeLessThanOrEqual(reading.block.x + reading.block.width + 0.5)
  for (const neighbour of reading.neighbours) {
    expect(overlaps(reading.title, neighbour.box), `name overlaps ${neighbour.name}`).toBe(false)
  }
  expect(reading.overflow, `header does not spill sideways${reading.spill ? ` (${reading.spill})` : ''}`).toBeLessThanOrEqual(1)
  if (reading.stacked) expect(reading.mainSlack, 'stacked header stays content-sized').toBeLessThanOrEqual(8)
}

async function shot(page: Page, width: number, theme: string, density: string, panel: string) {
  await page.mouse.move(0, 0)
  await page.locator('#project-title').evaluate(el => (el as HTMLElement).blur())
  await page.screenshot({ path: test.info().outputPath(`${width}-${theme}-${density}-${panel}.png`) })
}

for (const width of WIDTHS) for (const theme of ['light', 'dark'] as const) {
  test(`long project name stays one line at ${width}px ${theme}`, async ({ page }) => {
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    const data = fixtures()
    data.preferences.theme = { choice: theme }
    data.preferences['list:display'] = { headerGraph: false }
    data.projects[0]!.title = NAME
    data.projects[0]!.description = DESCRIPTION
    await mockWork(page, data)
    // Registered after mockWork: its catch-all would otherwise answer /api/queue with 404.
    await page.route('**/api/queue?*', route => route.fulfill({ json: { items: [], manual_order: false, capacity: { queued_hours: 0, parallel_runs: 0, work_hours: 0, warning: false } } }))
    const row = page.getByRole('grid', { name: 'Tickets' }).locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-11$/ }) })
    const close = page.getByRole('button', { name: 'Close ticket details', exact: true })
    for (const density of DENSITIES) {
      await page.addInitScript(({ key, density: next }) => {
        localStorage.setItem(key, JSON.stringify({ density: next, roomy: next }))
      }, { key: headerStorageKey('t1', me.id), density })
      await page.goto('/p/PHAROS/tickets')
      await expect(page.locator('.project-page')).toHaveClass(new RegExp(`header-${density}`))
      const title = page.locator('#project-title')
      await expect(title).toHaveText(NAME)
      await expect(title).toHaveAttribute('title', NAME)
      await expect(title).toHaveAttribute('aria-label', NAME)
      await expect(page.locator('.description-more')).toBeVisible()
      const closed = await readHeader(page)
      assertName(closed)
      if (density === 'comfortable' && width === 768) expect(closed.stacked, '768 comfortable header stacks').toBe(true)
      if (closed.clipped) {
        await expect(title).toHaveAttribute('data-tip', NAME)
        await page.keyboard.press('Tab')
        await title.focus()
        await expect(page.locator('.tooltip')).toHaveText(NAME)
        await title.evaluate(el => (el as HTMLElement).blur())
        await page.mouse.move(0, 0)
        await title.hover()
        await expect(page.locator('.tooltip')).toHaveText(NAME)
        await page.mouse.move(0, 0)
      }
      await shot(page, width, theme, density, 'closed')
      await expect(row).toBeVisible()
      await row.evaluate(el => (el as HTMLElement).click())
      await expect(close).toBeVisible()
      const opened = await readHeader(page)
      assertName(opened)
      if (density === 'comfortable' && (width === 768 || width === 1024)) expect(opened.stacked, `${width} comfortable header stacks once the list is narrow`).toBe(true)
      near(opened.title.x, closed.title.x, `${density} name x while the panel is open`)
      near(opened.title.y, closed.title.y, `${density} name y while the panel is open`)
      near(opened.title.height, closed.title.height, `${density} name height while the panel is open`)
      if (width < 900) near(opened.title.width, closed.title.width, `${density} name width while the panel is open`)
      await shot(page, width, theme, density, 'open')
      await close.click()
      await expect(close).toBeHidden()
      const restored = await readHeader(page)
      assertName(restored)
      near(restored.title.x, closed.title.x, `${density} name x after close`)
      near(restored.title.y, closed.title.y, `${density} name y after close`)
      near(restored.title.width, closed.title.width, `${density} name width after close`)
      near(restored.title.height, closed.title.height, `${density} name height after close`)
      await shot(page, width, theme, density, 'restored')
    }
    expect(errors, 'no browser runtime errors').toEqual([])
  })
}
