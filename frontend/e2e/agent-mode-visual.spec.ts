import { expect, test, type Page } from '@playwright/test'
import { mkdir, readFile } from 'node:fs/promises'
import { extname, resolve } from 'node:path'

import { makeFixtureSnapshot } from '../src/services/agentModeFixtures'

const SHOT_DIR = process.env.PAI805_SHOT_DIR ?? '/tmp/pai805-shots'
const SELF_HOST = process.env.PAI805_SELF_HOST_DIST === '1'
const APP_ORIGIN = SELF_HOST ? 'http://pai805.local' : ''
const DIST_DIR = resolve(process.cwd(), 'dist')

test.beforeAll(async () => {
  await mkdir(SHOT_DIR, { recursive: true })
})

const user = {
  id: 7,
  username: 'mba',
  role: 'admin',
  nickname: 'Markus',
  first_name: 'Markus',
  last_name: 'Barta',
  email: 'm@example.com',
  avatar_path: '',
  locale: 'en',
  timezone: 'auto',
}
const me = { user, access: { all_projects: true, levels: {} }, via_dev_login: false, suppress_security_nags: true }
const visualIssue = {
  id: 5008, project_id: 12, issue_number: 820, issue_key: 'REL-820', type: 'ticket', parent_id: null,
  title: 'Release 5.11.0 smoke suite', description: 'Verify the production release.', acceptance_criteria: '- [ ] Smoke tests pass',
  notes: '', report_summary: '', status: 'in-progress', priority: 'high', cost_unit: null, release: null,
  billing_type: null, total_budget: null, rate_hourly: null, rate_lp: null, estimate_hours: 2, estimate_lp: 3,
  ar_hours: null, ar_lp: null, time_override: null, start_date: null, end_date: null, group_state: null,
  sprint_state: null, jira_id: null, jira_version: null, jira_text: null, color: null, sprint_ids: [], archived: false,
  assignee_id: null, assignee: null, tags: [], created_at: '2026-08-20 10:00:00', updated_at: '2026-08-20 12:00:00',
  created_by: 7, created_by_name: 'mba', last_changed_by_name: 'mba', booked_hours: 0, time_logged: 0,
  time_rollup: 0, time_total: 0, accepted_at: null, accepted_by: null, invoiced_at: null, invoice_number: '',
}

async function installApiFixtures(page: Page) {
  await page.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/auth/me') return route.fulfill({ json: me })
    if (path === '/api/branding') {
      return route.fulfill({ json: { name: 'PAIMOS', company: 'PAIMOS', product: 'PAIMOS', logo: '/logo.svg' } })
    }
    if (path === '/api/instance') {
      return route.fulfill({ json: { label: 'STAGING', attachments_enabled: true, live_updates_enabled: false } })
    }
    if (path === '/api/agent-mode/deliveries') {
      if (new URL(page.url()).searchParams.get('visualState') === 'forbidden') {
        return route.fulfill({ status: 403, json: { message: 'fixture: forbidden' } })
      }
      return route.fulfill({ json: makeFixtureSnapshot(10, new Date().toISOString()) })
    }
    if (path === '/api/issues/5008') return route.fulfill({ json: visualIssue })
    if (path === '/api/issues/5008/activity') {
      return route.fulfill({ json: { undo_rows: [], redo_rows: [], history_rows: [], stack_depth: 0 } })
    }
    if (path === '/api/issues/5008/ai-activity') return route.fulfill({ json: { rows: [], count: 0, last_week_count: 0 } })
    if (/^\/api\/issues\/5008\/(attachments|comments|time-entries)$/.test(path)) return route.fulfill({ json: [] })
    if (path === '/api/time-entries/today-summary') return route.fulfill({ json: { total_hours: 0, count: 0 } })
    if (path === '/api/dev/test-reports/summary') return route.fulfill({ json: { failures: 0 } })
    return route.fulfill({ json: [] })
  })
}

async function installStaticHost(page: Page) {
  if (!SELF_HOST) return
  await page.route('http://pai805.local/**', async (route) => {
    const pathname = new URL(route.request().url()).pathname
    const relative = pathname.startsWith('/assets/') || pathname === '/logo.svg' || pathname === '/favicon.svg'
      ? pathname.slice(1)
      : 'index.html'
    const file = resolve(DIST_DIR, relative)
    const types: Record<string, string> = {
      '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml',
      '.woff': 'font/woff', '.woff2': 'font/woff2',
    }
    await route.fulfill({ body: await readFile(file), contentType: types[extname(file)] ?? 'application/octet-stream' })
  })
}

async function expectSelectedAnchorInInitialCanvas(page: Page) {
  await expect(page.locator('[data-selected="true"]')).toHaveCount(1)
  await expect(page.locator('[aria-current="true"]')).toHaveCount(1)
  const geometry = await page.evaluate(() => {
    const selected = document.querySelector<HTMLElement>('[data-selected="true"]')!.getBoundingClientRect()
    const canvas = document.querySelector<HTMLElement>('.am-canvas')!.getBoundingClientRect()
    return {
      selected: { top: selected.top, bottom: selected.bottom, left: selected.left, right: selected.right },
      canvas: { top: canvas.top, bottom: canvas.bottom, left: canvas.left, right: canvas.right },
      viewportHeight: window.innerHeight,
    }
  })
  expect(geometry.selected.top).toBeGreaterThanOrEqual(geometry.canvas.top)
  expect(geometry.selected.bottom).toBeLessThanOrEqual(geometry.canvas.bottom)
  expect(geometry.selected.bottom).toBeLessThanOrEqual(geometry.viewportHeight)
  expect(geometry.selected.left).toBeGreaterThanOrEqual(geometry.canvas.left)
  expect(geometry.selected.right).toBeLessThanOrEqual(geometry.canvas.right)
}

async function expectDockClear(page: Page) {
  const result = await page.evaluate(() => {
    const intersects = (a: DOMRect, b: DOMRect) =>
      a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top
    const dock = document.querySelector<HTMLElement>('.am-conv--compact')!.getBoundingClientRect()
    const canvas = document.querySelector<HTMLElement>('.am-canvas')!.getBoundingClientRect()
    const cards = [...document.querySelectorAll<HTMLElement>('.am-card, .am-selection-anchor')]
    const visibleCardRect = (card: HTMLElement): DOMRect => {
      const rect = card.getBoundingClientRect()
      return DOMRect.fromRect({
        x: Math.max(rect.left, canvas.left),
        y: Math.max(rect.top, canvas.top),
        width: Math.max(0, Math.min(rect.right, canvas.right) - Math.max(rect.left, canvas.left)),
        height: Math.max(0, Math.min(rect.bottom, canvas.bottom) - Math.max(rect.top, canvas.top)),
      })
    }
    return {
      canvasIntersectsDock: intersects(canvas, dock),
      // DOMRects are clipped to the canvas paint box: off-scroll content can
      // geometrically pass behind the overflow boundary but cannot be painted
      // there, which is the visual-occlusion condition under test.
      cardIntersections: cards.filter((card) => intersects(visibleCardRect(card), dock)).length,
    }
  })
  expect(result.canvasIntersectsDock).toBe(false)
  expect(result.cardIntersections).toBe(0)
}

async function openReady(page: Page, width: number, height: number, extra = '') {
  await page.setViewportSize({ width, height })
  await page.goto(`${APP_ORIGIN}/agent-mode?delivery=dlv-820${extra}`, { waitUntil: 'networkidle' })
  await expect(page.locator('.am-selection-anchor')).toBeVisible()
}

test('PAI-805 final visual and geometry gate', async ({ page }) => {
  const consoleErrors: string[] = []
  const failedRequests: string[] = []
  const failedResponses: string[] = []
  page.on('console', (message) => { if (message.type() === 'error') consoleErrors.push(message.text()) })
  page.on('pageerror', (error) => consoleErrors.push(error.message))
  page.on('requestfailed', (request) => failedRequests.push(`${request.method()} ${request.url()}`))
  page.on('response', (response) => {
    const expectedForbidden = response.status() === 403 && response.url().includes('/api/agent-mode/deliveries')
    if (response.status() >= 400 && !expectedForbidden) failedResponses.push(`${response.status()} ${response.url()}`)
  })
  await installStaticHost(page)
  await installApiFixtures(page)

  await openReady(page, 1440, 1000)
  await expectSelectedAnchorInInitialCanvas(page)
  const logo = page.locator('.aml-brand-logo')
  await expect(logo).toHaveAttribute('src', '/logo.svg')
  expect(await logo.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true)
  await page.screenshot({ path: `${SHOT_DIR}/desktop-10.png` })

  for (const [width, height] of [[900, 800], [736, 900], [520, 900], [390, 844]] as const) {
    await openReady(page, width, height)
    await expectDockClear(page)
    await page.locator('.am-canvas').evaluate((canvas) => { canvas.scrollTop = canvas.scrollHeight })
    await expectDockClear(page)
    if (width === 900) await page.screenshot({ path: `${SHOT_DIR}/narrow-10.png` })
    if (width === 390) {
      const widths = await page.evaluate(() => {
        const html = document.documentElement
        const canvas = document.querySelector<HTMLElement>('.am-canvas')!.getBoundingClientRect()
        const widest = Math.max(...[...document.querySelectorAll<HTMLElement>('.am-card, .am-selection-anchor')].map((el) => el.getBoundingClientRect().width))
        return { scrollWidth: html.scrollWidth, clientWidth: html.clientWidth, canvasWidth: canvas.width, widest }
      })
      expect(widths.scrollWidth).toBeLessThanOrEqual(widths.clientWidth)
      expect(widths.canvasWidth).toBeLessThanOrEqual(widths.clientWidth - 52)
      expect(widths.widest).toBeLessThanOrEqual(widths.canvasWidth)
      await page.locator('.am-canvas').evaluate((canvas) => { canvas.scrollTop = 0 })
      await page.screenshot({ path: `${SHOT_DIR}/phone-10.png` })
    }
  }

  await openReady(page, 1440, 1000, '&detail=1')
  await page.screenshot({ path: `${SHOT_DIR}/desktop-1.png` })
  await openReady(page, 1440, 1000, '&detail=100')
  await page.screenshot({ path: `${SHOT_DIR}/desktop-100.png` })
  await page.goto(`${APP_ORIGIN}/agent-mode?visualState=forbidden`, { waitUntil: 'networkidle' })
  await expect(page.locator('.am-state--forbidden')).toBeVisible()
  await page.screenshot({ path: `${SHOT_DIR}/desktop-forbidden.png` })
  await page.goto(`${APP_ORIGIN}/`, { waitUntil: 'networkidle' })
  await expect(page.locator('.brand-logo')).toHaveAttribute('src', '/logo.svg')
  await expect(page.getByRole('heading', { name: /Good |Hello|Welcome/i })).toBeVisible()
  await page.screenshot({ path: `${SHOT_DIR}/desktop-standard-home.png` })

  expect(consoleErrors).toEqual([])
  expect(failedRequests).toEqual([])
  expect(failedResponses).toEqual([])
})

test('Agent Mode keeps the light app palette under dark OS and reduced motion', async ({ page }) => {
  await installStaticHost(page)
  await installApiFixtures(page)
  await page.emulateMedia({ colorScheme: 'dark', reducedMotion: 'reduce' })
  await openReady(page, 1440, 1000)
  await expectSelectedAnchorInInitialCanvas(page)
  const colors = await page.locator('.am-root').evaluate((el) => {
    const style = getComputedStyle(el)
    return { background: style.backgroundColor, ink: style.color }
  })
  expect(colors.background).toBe('rgb(242, 245, 248)')
  expect(colors.ink).toBe('rgb(30, 41, 59)')
  await page.screenshot({ path: `${SHOT_DIR}/desktop-10-reduced-dark-os.png` })
})

test('PAI-806 Detail 1 ticket panel geometry at 390 / 736 / 1024', async ({ page }) => {
  await installStaticHost(page)
  await installApiFixtures(page)
  for (const [width, height] of [[390, 844], [736, 900], [1024, 900]] as const) {
    await page.setViewportSize({ width, height })
    await page.goto(`${APP_ORIGIN}/agent-mode?delivery=dlv-820&detail=1`, { waitUntil: 'networkidle' })
    await page.getByRole('button', { name: 'Open ticket' }).click()
    await expect(page.locator('.side-panel--embedded')).toBeVisible()
    const layout = await page.evaluate(() => {
      const rect = (selector: string) => document.querySelector<HTMLElement>(selector)!.getBoundingClientRect()
      const intersects = (a: DOMRect, b: DOMRect) => a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top
      const canvas = rect('.am-canvas')
      const panel = rect('.side-panel--embedded')
      const dock = rect('.am-conv--compact')
      const stage = rect('.am-stage-chain')
      const estimate = rect('.am-focus-detail-grid')
      return {
        canvasPanel: intersects(canvas, panel),
        dockPanel: intersects(dock, panel),
        dockStage: intersects(dock, stage),
        dockEstimate: intersects(dock, estimate),
        horizontalOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth,
      }
    })
    expect(layout).toEqual({
      canvasPanel: false,
      dockPanel: false,
      dockStage: false,
      dockEstimate: false,
      horizontalOverflow: false,
    })
  }
})
