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

type VisualDeliveryMode = 'ready' | 'loading' | 'empty' | 'offline' | 'forbidden' | 'not-found' | 'malformed'

interface VisualApiControl {
  setDeliveryMode(mode: VisualDeliveryMode): void
  releaseLoading(): void
  readonly deliveryRequests: number
  readonly controlTransitions: readonly string[]
  readonly issueMutations: number
  readonly commentPosts: number
  readonly attachmentUploads: number
}

async function installApiFixtures(page: Page): Promise<VisualApiControl> {
  let deliveryMode: VisualDeliveryMode = 'ready'
  let deliveryRequests = 0
  let releaseLoading: (() => void) | null = null
  const controlTransitions: string[] = []
  let controlCommand: Record<string, unknown> | null = null
  let issueFixture = { ...visualIssue }
  let issueMutations = 0
  let commentPosts = 0
  let attachmentUploads = 0
  const comments: Array<Record<string, unknown>> = []
  await page.addInitScript(() => {
    const sources: EventTarget[] = []
    class VisualEventSource extends EventTarget {
      static readonly CONNECTING = 0
      static readonly OPEN = 1
      static readonly CLOSED = 2
      readonly url: string
      readonly withCredentials = false
      readyState = VisualEventSource.OPEN
      onopen: ((event: Event) => void) | null = null
      onmessage: ((event: MessageEvent) => void) | null = null
      onerror: ((event: Event) => void) | null = null

      constructor(url: string | URL) {
        super()
        this.url = String(url)
        sources.push(this)
        queueMicrotask(() => this.dispatchEvent(new Event('open')))
      }

      close() {
        this.readyState = VisualEventSource.CLOSED
      }
    }
    Object.defineProperty(window, 'EventSource', { configurable: true, value: VisualEventSource })
    Object.defineProperty(window, '__pai811EventSources', { configurable: true, value: sources })
  })
  await page.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    const fulfill = (json: unknown, status = 200) => route.fulfill({
      status,
      json,
      headers: { 'X-Permissions-Epoch': '1' },
    })
    if (path === '/api/auth/me') return fulfill(me)
    if (path === '/api/branding') {
      return fulfill({ name: 'PAIMOS', company: 'PAIMOS', product: 'PAIMOS', logo: '/logo.svg' })
    }
    if (path === '/api/instance') {
      return fulfill({ label: 'STAGING', attachments_enabled: true, live_updates_enabled: false })
    }
    if (path === '/api/agent-mode/deliveries') {
      deliveryRequests += 1
      if (new URL(route.request().url()).searchParams.get('q') === 'visual-forbidden') {
        return fulfill({ message: 'fixture: forbidden' }, 403)
      }
      if (deliveryMode === 'loading') {
        await new Promise<void>((resolveLoading) => { releaseLoading = resolveLoading })
      }
      if (deliveryMode === 'empty') return fulfill(makeFixtureSnapshot(0))
      if (deliveryMode === 'offline') return route.abort('failed')
      if (deliveryMode === 'forbidden') return fulfill({ message: 'fixture: revoked' }, 403)
      if (deliveryMode === 'not-found') return fulfill({ message: 'fixture: missing' }, 404)
      if (deliveryMode === 'malformed') {
        return route.fulfill({
          status: 200,
          body: '{"schema_version":',
          contentType: 'application/json',
          headers: { 'X-Permissions-Epoch': '1' },
        })
      }
      return fulfill(makeFixtureSnapshot(10))
    }
    if (/^\/api\/agent-mode\/deliveries\/dlv-\d+\/control-capability-grants$/.test(path)) {
      const deliveryKey = path.split('/')[4]
      const selected = makeFixtureSnapshot(10).rows?.find((delivery) => delivery.delivery_id === deliveryKey)
      return fulfill({
        grant_id: '11111111-1111-4111-8111-111111111111',
        revision: 1,
        delivery_key: deliveryKey,
        issue_key: selected?.issue_key ?? deliveryKey.replace(/^dlv-/, 'PAI-'),
        actions: ['run.cancel.running'],
        targets: [{ action: 'run.cancel.running', run_id: 42 }],
        expires_at: '2027-01-02T03:04:05Z',
      })
    }
    if (/^\/api\/agent-mode\/deliveries\/dlv-\d+\/control-commands$/.test(path)) {
      const deliveryKey = path.split('/')[4]
      const selected = makeFixtureSnapshot(10).rows?.find((delivery) => delivery.delivery_id === deliveryKey)
      controlCommand = {
        command_id: '22222222-2222-4222-8222-222222222222',
        status_revision: 1,
        action: 'run.cancel.running',
        status: 'pending_confirmation',
        challenge_template: 'run_cancel_running',
        expires_at: '2027-01-02T03:04:05Z',
        display: { issue_key: selected?.issue_key ?? 'REL-820', delivery_key: deliveryKey, run_id: 42 },
      }
      return fulfill(controlCommand)
    }
    if (/^\/api\/agent-mode\/control-commands\/[0-9a-f-]+$/.test(path)) {
      if (!controlCommand) return fulfill({ message: 'fixture: command missing' }, 404)
      if (route.request().method() === 'POST') {
        const body = route.request().postDataJSON() as { operation?: unknown }
        const operation = typeof body.operation === 'string' ? body.operation : ''
        controlTransitions.push(operation)
        controlCommand = operation === 'confirm'
          ? { ...controlCommand, status_revision: 2, status: 'accepted' }
          : { ...controlCommand, status_revision: 2, status: 'rejected', outcome: 'rejected', reason: 'withdrawn' }
      }
      return fulfill(controlCommand)
    }
    if (path === '/api/issues/5008') {
      if (route.request().method() !== 'GET') {
        const body = route.request().postDataJSON() as Record<string, unknown>
        issueMutations += 1
        issueFixture = { ...issueFixture, ...body, updated_at: '2026-08-22 06:00:00' }
      }
      return fulfill(issueFixture)
    }
    if (path === '/api/issues/5008/activity') {
      return fulfill({ undo_rows: [], redo_rows: [], history_rows: [], stack_depth: 0 })
    }
    if (path === '/api/issues/5008/ai-activity') return fulfill({ rows: [], count: 0, last_week_count: 0 })
    if (path === '/api/issues/5008/comments') {
      if (route.request().method() === 'GET') return fulfill(comments)
      const body = route.request().postDataJSON() as { body?: unknown; visibility?: unknown }
      const comment = {
        id: 901 + comments.length,
        issue_id: 5008,
        author_id: 7,
        author: 'mba',
        avatar_path: null,
        body: typeof body.body === 'string' ? body.body : '',
        visibility: body.visibility === 'external' ? 'external' : 'internal',
        created_at: '2026-08-22T06:00:00Z',
      }
      comments.push(comment)
      commentPosts += 1
      return fulfill(comment)
    }
    if (path === '/api/issues/5008/attachments') {
      if (route.request().method() === 'GET') return fulfill([])
      attachmentUploads += 1
      return fulfill({
        id: 701,
        issue_id: 5008,
        object_key: 'fixture/pai811-proof.txt',
        filename: 'pai811-proof.txt',
        content_type: 'text/plain',
        size_bytes: 17,
        uploaded_by: 7,
        uploader: 'mba',
        created_at: '2026-08-22T06:00:00Z',
      })
    }
    if (path === '/api/issues/5008/time-entries') return fulfill([])
    if (path === '/api/time-entries/today-summary') return fulfill({ total_hours: 0, count: 0 })
    if (path === '/api/dev/test-reports/summary') return fulfill({ failures: 0 })
    return fulfill([])
  })
  return {
    setDeliveryMode(mode) { deliveryMode = mode },
    releaseLoading() {
      deliveryMode = 'ready'
      releaseLoading?.()
      releaseLoading = null
    },
    get deliveryRequests() { return deliveryRequests },
    get controlTransitions() { return [...controlTransitions] },
    get issueMutations() { return issueMutations },
    get commentPosts() { return commentPosts },
    get attachmentUploads() { return attachmentUploads },
  }
}

async function emitVisualStreamEvent(page: Page, type: 'refetch' | 'reset') {
  await page.evaluate((eventType) => {
    const target = window as Window & { __pai811EventSources?: EventTarget[] }
    const source = target.__pai811EventSources?.at(-1)
    if (!source) throw new Error('visual EventSource is not open')
    source.dispatchEvent(new MessageEvent(eventType, {
      data: JSON.stringify(eventType === 'reset'
        ? { schema_version: 1, reason: 'resync_required' }
        : { schema_version: 1, hints: [] }),
      lastEventId: 'A'.repeat(211),
    }))
  }, type)
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
      cardIntersections: cards.filter((card) => {
        const painted = visibleCardRect(card)
        return painted.width > 0 && painted.height > 0 && intersects(painted, dock)
      }).length,
    }
  })
  expect(result.canvasIntersectsDock).toBe(false)
  expect(result.cardIntersections).toBe(0)
}

async function expectControlVoiceGeometry(page: Page) {
  const result = await page.evaluate(() => {
    const controls = document.querySelector<HTMLElement>('.am-controls')!
    const voice = document.querySelector<HTMLElement>('.am-voice')!
    const host = document.querySelector<HTMLElement>('.am-conv-controls')!
    const controlRect = controls.getBoundingClientRect()
    const voiceRect = voice.getBoundingClientRect()
    const hostRect = host.getBoundingClientRect()
    const intersects = controlRect.left < voiceRect.right && controlRect.right > voiceRect.left
      && controlRect.top < voiceRect.bottom && controlRect.bottom > voiceRect.top
    return {
      ordered: !!(controls.compareDocumentPosition(voice) & Node.DOCUMENT_POSITION_FOLLOWING),
      intersects,
      controlsWithinHost: controlRect.left >= hostRect.left && controlRect.right <= hostRect.right,
      voiceWithinHost: voiceRect.left >= hostRect.left && voiceRect.right <= hostRect.right,
      horizontalOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth,
    }
  })
  expect(result).toEqual({
    ordered: true,
    intersects: false,
    controlsWithinHost: true,
    voiceWithinHost: true,
    horizontalOverflow: false,
  })
}

async function expectMobileCompactFlow(page: Page) {
  const result = await page.evaluate(() => {
    const canvas = document.querySelector<HTMLElement>('.am-canvas')!.getBoundingClientRect()
    const conversation = document.querySelector<HTMLElement>('.am-conv--compact')!.getBoundingClientRect()
    const root = document.querySelector<HTMLElement>('.am-root')!.getBoundingClientRect()
    const selected = document.querySelector<HTMLElement>('[data-selected="true"]')!.getBoundingClientRect()
    const renderedLines = [...document.querySelectorAll<HTMLElement>('.am-conv-line')]
      .filter((line) => getComputedStyle(line).display !== 'none')
    const feedDock = document.querySelector<HTMLElement>('.am-conv-dock')!
    return {
      renderedLines: renderedLines.length,
      feedDockDisplay: getComputedStyle(feedDock).display,
      conversationWidth: conversation.width,
      rootWidth: root.width,
      canvasHeight: canvas.height,
      selectedWithinCanvas: selected.top >= canvas.top && selected.bottom <= canvas.bottom,
      horizontalOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth,
    }
  })
  expect(result.renderedLines).toBe(1)
  expect(result.feedDockDisplay).toBe('none')
  expect(result.conversationWidth).toBeGreaterThanOrEqual(result.rootWidth - 29)
  expect(result.canvasHeight).toBeGreaterThanOrEqual(420)
  expect(result.selectedWithinCanvas).toBe(true)
  expect(result.horizontalOverflow).toBe(false)
}

async function openReady(page: Page, width: number, height: number, extra = '') {
  await page.setViewportSize({ width, height })
  await page.goto(`${APP_ORIGIN}/agent-mode?delivery=dlv-820${extra}`, { waitUntil: 'networkidle' })
  await expect(page.locator('[data-selected="true"]')).toBeVisible()
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
  await expectControlVoiceGeometry(page)
  const logo = page.locator('.aml-brand-logo')
  await expect(logo).toHaveAttribute('src', '/logo.svg')
  expect(await logo.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true)
  await page.screenshot({ path: `${SHOT_DIR}/desktop-10.png` })

  for (const [width, height] of [[900, 800], [736, 900], [520, 900], [390, 844]] as const) {
    await openReady(page, width, height)
    await expectControlVoiceGeometry(page)
    await expectDockClear(page)
    if (width === 390) await expectMobileCompactFlow(page)
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
  const errorsBeforeForbidden = consoleErrors.length
  await page.goto(`${APP_ORIGIN}/agent-mode?q=visual-forbidden`, { waitUntil: 'networkidle' })
  await expect(page.locator('.am-state--forbidden')).toBeVisible()
  await page.screenshot({ path: `${SHOT_DIR}/desktop-forbidden.png` })
  const forbiddenConsoleErrors = consoleErrors.splice(errorsBeforeForbidden)
  expect(forbiddenConsoleErrors).toHaveLength(1)
  expect(forbiddenConsoleErrors[0]).toContain('403 (Forbidden)')
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
  expect(colors.ink).toBe('rgb(26, 38, 54)')
  const moving = await page.locator('.aml-shell').evaluate((shell) =>
    [...shell.querySelectorAll<HTMLElement>('*')]
      .filter((element) => {
        const rect = element.getBoundingClientRect()
        if (rect.width === 0 || rect.height === 0) return false
        const style = getComputedStyle(element)
        const nonZero = (value: string) => value.split(',').some((part) => Number.parseFloat(part) > 0)
        const spatial = new Set([
          'all', 'transform', 'translate', 'scale', 'rotate', 'width', 'height', 'max-width', 'max-height',
          'min-width', 'min-height', 'top', 'right', 'bottom', 'left', 'margin', 'padding', 'inset',
        ])
        const properties = style.transitionProperty.split(',').map((property) => property.trim())
        const durations = style.transitionDuration.split(',').map((duration) => Number.parseFloat(duration))
        const hasSpatialTransition = properties.some((property, index) =>
          spatial.has(property) && (durations[index % durations.length] ?? 0) > 0)
        return (style.animationName !== 'none' && nonZero(style.animationDuration))
          || hasSpatialTransition
      })
      .map((element) => {
        const style = getComputedStyle(element)
        return {
          tag: element.tagName,
          id: element.id,
          className: element.className,
          animation: `${style.animationName} ${style.animationDuration}`,
          transition: `${style.transitionProperty} ${style.transitionDuration}`,
        }
      })
  )
  expect(moving).toEqual([])
  await page.screenshot({ path: `${SHOT_DIR}/desktop-10-reduced-dark-os.png` })
})

test('PAI-811 200% effective zoom keeps mobile flow reachable and non-occluding', async ({ page }) => {
  await installStaticHost(page)
  await installApiFixtures(page)
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('Emulation.setDeviceMetricsOverride', {
    width: 512,
    height: 450,
    deviceScaleFactor: 2,
    mobile: false,
  })
  await page.goto(`${APP_ORIGIN}/agent-mode?delivery=dlv-820`, { waitUntil: 'networkidle' })
  await expect(page.locator('[data-selected="true"]')).toBeVisible()
  await expectControlVoiceGeometry(page)
  await expectDockClear(page)
  const geometry = await page.evaluate(() => ({
    devicePixelRatio: window.devicePixelRatio,
    innerWidth: window.innerWidth,
    horizontalOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth,
    canvasScrollable: document.querySelector<HTMLElement>('.am-canvas')!.scrollHeight
      > document.querySelector<HTMLElement>('.am-canvas')!.clientHeight,
  }))
  expect(geometry).toEqual({
    devicePixelRatio: 2,
    innerWidth: 512,
    horizontalOverflow: false,
    canvasScrollable: true,
  })
})

test('PAI-811 browser states stay honest and retry without retaining unauthorized truth', async ({ page }) => {
  await installStaticHost(page)
  const api = await installApiFixtures(page)

  api.setDeliveryMode('loading')
  await page.goto(`${APP_ORIGIN}/agent-mode?delivery=dlv-820`, { waitUntil: 'domcontentloaded' })
  await expect(page.locator('.am-state--loading')).toBeVisible()
  api.releaseLoading()
  await expect(page.locator('[data-selected="true"]')).toBeVisible()

  for (const [mode, selector] of [
    ['empty', '.am-state--empty'],
    ['not-found', '.am-state--not-found'],
    ['malformed', '.am-state--error'],
    ['offline', '.am-state--offline'],
  ] as const) {
    api.setDeliveryMode(mode)
    await page.reload({ waitUntil: 'networkidle' })
    await expect(page.locator(selector)).toBeVisible()
    await expect(page.locator('[data-selected="true"]')).toHaveCount(0)
  }

  const requestsBeforeRetry = api.deliveryRequests
  api.setDeliveryMode('ready')
  await page.locator('.am-state--offline .am-state-retry').click()
  await expect(page.locator('[data-selected="true"]')).toBeVisible()
  expect(api.deliveryRequests).toBeGreaterThan(requestsBeforeRetry)

  api.setDeliveryMode('offline')
  await emitVisualStreamEvent(page, 'refetch')
  await expect(page.locator('.am-banner')).toContainText('Last known state')
  await expect(page.locator('[data-selected="true"]')).toBeVisible()
  await expect(page.locator('.am-card-eta--withheld').first()).toBeVisible()
})

test('PAI-811 live ACL revocation clears every authoritative delivery surface', async ({ page }) => {
  await installStaticHost(page)
  const api = await installApiFixtures(page)
  await openReady(page, 1440, 1000, '&detail=1')
  await expect(page.locator('.am-conv')).toContainText('REL-820')
  await expect(page.locator('.am-controls')).toBeVisible()
  await expect(page.locator('.am-stage-chain')).toBeVisible()

  api.setDeliveryMode('forbidden')
  await emitVisualStreamEvent(page, 'reset')
  await expect(page.locator('.am-state--forbidden')).toBeVisible()
  await expect(page.locator('[data-selected="true"]')).toHaveCount(0)
  await expect(page.locator('.am-conv')).not.toContainText('REL-820')
  await expect(page.locator('.am-controls')).toHaveCount(0)
  await expect(page.locator('.am-stage-chain')).toHaveCount(0)
  await expect(page.locator('.am-card-eta')).toHaveCount(0)
  await expect(page.getByText('REL-820', { exact: false })).toHaveCount(0)

  await page.keyboard.press('ArrowDown')
  await expect(page.locator('[data-selected="true"]')).toHaveCount(0)
})

test('PAI-811 controls require an explicit second phase for withdrawal and confirmation', async ({ page }) => {
  await installStaticHost(page)
  const api = await installApiFixtures(page)
  await openReady(page, 1440, 1000)

  const cancel = page.locator('.am-controls-target > button', { hasText: 'Cancel running run' })
  await cancel.click()
  const challenge = page.locator('.am-controls-challenge')
  await expect(challenge).toBeVisible()
  await expect(challenge).toContainText('Confirmation required')
  expect(api.controlTransitions).toEqual([])

  await challenge.locator('button', { hasText: 'Back' }).click()
  await expect(challenge).toHaveCount(0)
  expect(api.controlTransitions).toEqual(['withdraw'])
  await page.locator('.am-controls-dismiss').click()

  await cancel.click()
  await expect(challenge).toBeVisible()
  await challenge.locator('button.is-consequential').click()
  await expect(challenge).toHaveCount(0)
  await expect(page.locator('.am-controls-live')).toContainText('Accepted')
  expect(api.controlTransitions).toEqual(['withdraw', 'confirm'])
})

test('PAI-811 voice uses the same exact two-utterance control boundary', async ({ page }) => {
  await installStaticHost(page)
  const api = await installApiFixtures(page)
  await openReady(page, 1440, 1000)

  const input = page.locator('#am-voice-command')
  await input.fill('Cancel running run REL-820')
  await input.press('Enter')
  await expect(page.locator('.am-controls-challenge')).toBeVisible()
  expect(api.controlTransitions).toEqual([])

  await input.fill('Cancel running run REL-820')
  await input.press('Enter')
  await expect(page.locator('.am-controls-challenge')).toHaveCount(0)
  await expect(page.locator('.am-controls-live')).toContainText('Accepted')
  expect(api.controlTransitions).toEqual(['confirm'])
})

test('PAI-811 Detail 1 browser flow edits, uploads, and posts an internal note', async ({ page }) => {
  await installStaticHost(page)
  const api = await installApiFixtures(page)
  await openReady(page, 1440, 1000, '&detail=1')
  await page.getByRole('button', { name: 'Open ticket' }).click()
  const panel = page.locator('.side-panel--embedded')
  await expect(panel).toBeVisible()

  await panel.locator('.comment-textarea').fill('PAI-811 browser acceptance note')
  await panel.getByRole('button', { name: 'Post', exact: true }).click()
  await expect(panel.locator('.comment-text')).toContainText('PAI-811 browser acceptance note')
  expect(api.commentPosts).toBe(1)

  await panel.getByTitle('Quick Edit').click()
  const title = panel.locator('.sp-form input[type="text"]').first()
  await title.fill('Release 5.11.0 live acceptance')
  await panel.locator('.att-file-input').setInputFiles({
    name: 'pai811-proof.txt',
    mimeType: 'text/plain',
    buffer: Buffer.from('browser proof file'),
  })
  await expect(panel.locator('.att-item--done')).toContainText('pai811-proof.txt')
  await panel.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(panel).toContainText('Release 5.11.0 live acceptance')
  expect(api.attachmentUploads).toBe(1)
  expect(api.issueMutations).toBe(1)
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
      const paintedWithinCanvas = (target: DOMRect): DOMRect => DOMRect.fromRect({
        x: Math.max(target.left, canvas.left),
        y: Math.max(target.top, canvas.top),
        width: Math.max(0, Math.min(target.right, canvas.right) - Math.max(target.left, canvas.left)),
        height: Math.max(0, Math.min(target.bottom, canvas.bottom) - Math.max(target.top, canvas.top)),
      })
      const paintedIntersects = (target: DOMRect, boundary: DOMRect): boolean => {
        const painted = paintedWithinCanvas(target)
        return painted.width > 0 && painted.height > 0 && intersects(boundary, painted)
      }
      return {
        canvasPanel: intersects(canvas, panel),
        dockPanel: intersects(dock, panel),
        // Focus content can continue below the scrollport geometrically, but
        // only its canvas-clipped rectangle can paint underneath the dock.
        dockStage: paintedIntersects(stage, dock),
        dockEstimate: paintedIntersects(estimate, dock),
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
