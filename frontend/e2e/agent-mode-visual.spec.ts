import { expect, test, type Page, type Request } from '@playwright/test'
import { mkdir, readFile } from 'node:fs/promises'
import { extname, resolve } from 'node:path'

import { makeFixtureSnapshot, rebuildFixtureAggregates } from '../src/services/agentModeFixtures'

const SHOT_DIR = process.env.PAI805_SHOT_DIR ?? '/tmp/pai805-shots'
const SELF_HOST = process.env.PAI805_SELF_HOST_DIST === '1'
const APP_ORIGIN = SELF_HOST ? 'http://pai805.local' : ''
const DIST_DIR = resolve(process.cwd(), 'dist')
const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i

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
type VisualDeliverySnapshot = ReturnType<typeof makeFixtureSnapshot>

interface VisualApiControl {
  setDeliveryMode(mode: VisualDeliveryMode): void
  setDeliverySnapshot(snapshot: VisualDeliverySnapshot): void
  releaseLoading(): void
  readonly deliveryRequests: number
  readonly deliveryQueries: readonly string[]
  readonly controlTransitions: readonly string[]
  readonly issueMutations: number
  readonly commentPosts: number
  readonly attachmentUploads: number
}

async function installApiFixtures(page: Page, respectDeliveryFilters = false): Promise<VisualApiControl> {
  let deliveryMode: VisualDeliveryMode = 'ready'
  let deliverySnapshot = makeFixtureSnapshot(10)
  let deliveryRequests = 0
  const deliveryQueries: string[] = []
  let releaseLoading: (() => void) | null = null
  const controlTransitions: string[] = []
  const controlCommands = new Map<string, Record<string, unknown>>()
  const controlIdempotencyKeys = new Map<string, string | null>()
  let controlCommandSequence = 0
  let issueFixture = { ...visualIssue }
  let issueMutations = 0
  let commentPosts = 0
  let attachmentUploads = 0
  const comments: Array<Record<string, unknown>> = []
  const requireMethod = (request: Request, ...allowed: string[]) => {
    const method = request.method()
    if (!allowed.includes(method)) {
      throw new Error(`fixture: ${request.url()} requires ${allowed.join(' or ')}, received ${method}`)
    }
    return method
  }
  const requireExactJSON = (request: Request, expected: Record<string, unknown>) => {
    expect(request.postDataJSON(), `exact request body for ${request.url()}`).toEqual(expected)
  }
  const requireControlMutation = (
    request: Request,
    expected: Record<string, unknown>,
    reusableScope?: string,
  ) => {
    requireMethod(request, 'POST')
    requireExactJSON(request, expected)
    const idempotencyKey = request.headers()['idempotency-key']
    expect(idempotencyKey, `Idempotency-Key for ${request.url()}`).toMatch(UUID_V4)
    if (controlIdempotencyKeys.has(idempotencyKey)) {
      expect(reusableScope, `fresh Idempotency-Key for ${request.url()}`).toBeTruthy()
      expect(controlIdempotencyKeys.get(idempotencyKey), `same-scope Idempotency-Key retry for ${request.url()}`).toBe(reusableScope)
    } else {
      controlIdempotencyKeys.set(idempotencyKey, reusableScope ?? null)
    }
  }
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
    if (path === '/api/projects') {
      return fulfill([
        { id: 6, key: 'PAI', name: 'PAIMOS Core platform', status: 'active' },
        { id: 9, key: 'RUN', name: 'Agent runtime', status: 'active' },
        { id: 12, key: 'REL', name: 'Release operations', status: 'active' },
        { id: 77, key: 'VAC', name: 'Vacation planning', status: 'active' },
      ])
    }
    if (path === '/api/agent-mode/deliveries') {
      deliveryRequests += 1
      deliveryQueries.push(new URL(route.request().url()).search)
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
      if (respectDeliveryFilters) {
        const requestUrl = new URL(route.request().url())
        const wire = structuredClone(deliverySnapshot)
        const allRows = wire.rows ?? []
        const projectId = Number(requestUrl.searchParams.get('project_id')) || null
        const selectedDelivery = requestUrl.searchParams.get('selected_delivery')
        const rows = projectId == null
          ? allRows
          : allRows.filter((row) => row.project_id === projectId)
        wire.rows = rows
        if (selectedDelivery) {
          wire.selected_delivery = selectedDelivery
          const outside = allRows.find((row) => row.delivery_id === selectedDelivery && !rows.includes(row))
          if (outside) wire.selected_outside = { reason: 'filter_excluded', row: outside }
          else delete wire.selected_outside
        }
        rebuildFixtureAggregates(wire)
        return fulfill(wire)
      }
      return fulfill(deliverySnapshot)
    }
    if (/^\/api\/agent-mode\/deliveries\/dlv-\d+\/control-capability-grants$/.test(path)) {
      const deliveryKey = path.split('/')[4]
      requireControlMutation(route.request(), {}, respectDeliveryFilters ? `grant:${deliveryKey}` : undefined)
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
      requireControlMutation(route.request(), {
        grant_id: '11111111-1111-4111-8111-111111111111',
        grant_revision: 1,
        action: 'run.cancel.running',
        run_id: 42,
      })
      const deliveryKey = path.split('/')[4]
      const selected = makeFixtureSnapshot(10).rows?.find((delivery) => delivery.delivery_id === deliveryKey)
      controlCommandSequence += 1
      const commandId = `22222222-2222-4222-8222-${String(controlCommandSequence).padStart(12, '0')}`
      const controlCommand = {
        command_id: commandId,
        status_revision: 1,
        action: 'run.cancel.running',
        status: 'pending_confirmation',
        challenge_template: 'run_cancel_running',
        expires_at: '2027-01-02T03:04:05Z',
        display: { issue_key: selected?.issue_key ?? 'REL-820', delivery_key: deliveryKey, run_id: 42 },
      }
      controlCommands.set(commandId, controlCommand)
      return fulfill(controlCommand)
    }
    if (/^\/api\/agent-mode\/control-commands\/[0-9a-f-]+$/.test(path)) {
      const request = route.request()
      const method = requireMethod(request, 'GET', 'POST')
      const commandId = path.split('/').at(-1) ?? ''
      let controlCommand = controlCommands.get(commandId)
      if (!controlCommand) return fulfill({ message: 'fixture: command missing' }, 404)
      if (method === 'POST') {
        expect(controlCommand.status, `pending command ${commandId}`).toBe('pending_confirmation')
        const body = request.postDataJSON() as { operation?: unknown; status_revision?: unknown }
        expect(body.operation === 'confirm' || body.operation === 'withdraw', 'exact control operation').toBe(true)
        const operation = body.operation as 'confirm' | 'withdraw'
        requireControlMutation(request, { operation, status_revision: 1 })
        controlTransitions.push(operation)
        controlCommand = operation === 'confirm'
          ? { ...controlCommand, status_revision: 2, status: 'accepted' }
          : { ...controlCommand, status_revision: 2, status: 'expired', reason: 'withdrawn' }
        controlCommands.set(commandId, controlCommand)
      }
      return fulfill(controlCommand)
    }
    if (path === '/api/issues/5008') {
      const request = route.request()
      const method = requireMethod(request, 'GET', 'PUT')
      if (method === 'PUT') {
        expect(request.headers()['if-match'], 'editor If-Match').toBe('"issue-5008-2026-08-20T12:00:00"')
        const body = request.postDataJSON() as Record<string, unknown>
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
      const request = route.request()
      const method = requireMethod(request, 'GET', 'POST')
      if (method === 'GET') return fulfill(comments)
      requireExactJSON(request, { body: 'PAI-811 browser acceptance note', visibility: 'internal' })
      const body = request.postDataJSON() as { body: string; visibility: 'internal' }
      const comment = {
        id: 901 + comments.length,
        issue_id: 5008,
        author_id: 7,
        author: 'mba',
        avatar_path: null,
        body: body.body,
        visibility: body.visibility,
        created_at: '2026-08-22T06:00:00Z',
      }
      comments.push(comment)
      commentPosts += 1
      return fulfill(comment)
    }
    if (path === '/api/issues/5008/attachments') {
      const request = route.request()
      const method = requireMethod(request, 'GET', 'POST')
      if (method === 'GET') return fulfill([])
      const contentType = request.headers()['content-type'] ?? ''
      const boundaryMatch = /boundary=(?:"([^"]+)"|([^;]+))/i.exec(contentType)
      const boundary = boundaryMatch?.[1] ?? boundaryMatch?.[2]
      expect(contentType, 'attachment Content-Type').toMatch(/^multipart\/form-data;\s*boundary=/i)
      expect(boundary, 'attachment multipart boundary').toBeTruthy()
      const multipart = request.postDataBuffer()
      expect(multipart, 'attachment multipart body').not.toBeNull()
      expect(multipart!.byteLength, 'non-empty attachment multipart body').toBeGreaterThan(Buffer.byteLength('browser proof file'))
      const multipartText = multipart!.toString('utf8')
      expect(multipartText).toContain(`--${boundary}`)
      expect(multipartText).toContain('Content-Disposition: form-data; name="file"; filename="pai811-proof.txt"')
      expect(multipartText).toContain('Content-Type: text/plain')
      expect(multipartText).toContain('\r\n\r\nbrowser proof file\r\n')
      expect(multipartText).toContain(`--${boundary}--`)
      attachmentUploads += 1
      return fulfill({
        id: 701,
        issue_id: 5008,
        object_key: 'fixture/pai811-proof.txt',
        filename: 'pai811-proof.txt',
        content_type: 'text/plain',
        size_bytes: Buffer.byteLength('browser proof file'),
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
    setDeliverySnapshot(snapshot) { deliverySnapshot = snapshot },
    releaseLoading() {
      deliveryMode = 'ready'
      releaseLoading?.()
      releaseLoading = null
    },
    get deliveryRequests() { return deliveryRequests },
    get deliveryQueries() { return deliveryQueries },
    get controlTransitions() { return [...controlTransitions] },
    get issueMutations() { return issueMutations },
    get commentPosts() { return commentPosts },
    get attachmentUploads() { return attachmentUploads },
  }
}

function makeVerifiedDeliverySnapshot(): VisualDeliverySnapshot {
  const snapshot = makeFixtureSnapshot(10)
  const delivery = snapshot.rows?.find((row) => row.delivery_id === 'dlv-820')
  if (!delivery) throw new Error('fixture: dlv-820 is required')

  const verifiedAt = '2026-08-20T13:47:00Z'
  const trustRevision = `tr1_${'f'.repeat(64)}`
  delivery.attempt_status = 'completed'
  delivery.delivery_revision = 'delivery:820:2'
  delivery.trust_revision = trustRevision
  delivery.activity = { kind: 'idle', text: 'Exact production artifact verified', since: verifiedAt }
  delivery.stage = { key: 'verification', label: 'Verification', index: 5, total: 5 }
  delivery.stages = delivery.stages?.map((stage) => ({
    ...stage,
    status: 'succeeded',
    activity: stage.key === 'verification' ? 'Exact production artifact verified' : undefined,
    blockers: [],
    evidence: [{ kind: 'stage_result', status: 'passed', reported_at: verifiedAt }],
    started_at: stage.started_at ?? '2026-08-20T13:40:00Z',
    completed_at: stage.completed_at ?? verifiedAt,
  })) ?? []
  delivery.evidence = delivery.stages.flatMap((stage) => stage.evidence ?? [])
  delivery.health = 'healthy'
  delivery.attention = { level: 0, since: null }
  delivery.blockers = []
  delivery.progress = {
    percent: 100,
    trusted: true,
    confidence: 'high',
    source: 'stage_evidence',
    basis: 'exact production artifact verified',
    revision: trustRevision,
  }
  delivery.eta = null
  delivery.trust = {
    schema_version: 1,
    trust_revision: trustRevision,
    progress_known: true,
    progress_percent: 100,
    confidence_label: 'high',
    reporter_kind: 'external',
    source_kind: 'stage_evidence',
    basis: 'exact production artifact verified',
    optimistic_landing_at: null,
    pessimistic_landing_at: null,
    landing_at: null,
    range_only: false,
    suppression: 'terminal_complete',
    scope: {
      attempt_id: 'attempt-820-1',
      plan_id: 'plan:820:1',
      execution_id: 'execution:820:1',
      authority_id: 'authority:820:1',
      reset_id: 'reset:820:0',
    },
    flags: [],
  }
  delivery.status_text = 'Verified exact production artifact'
  delivery.updated_at = verifiedAt
  return rebuildFixtureAggregates(snapshot)
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
    const dock = document.querySelector<HTMLElement>('.am-conv')!.getBoundingClientRect()
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

async function expectCompactFeedAuthority(page: Page, expected: RegExp) {
  const result = await page.evaluate(() => {
    const dock = document.querySelector<HTMLElement>('.am-conv-dock')!
    const chip = document.querySelector<HTMLElement>('.am-live-chip')!
    const rect = chip.getBoundingClientRect()
    return {
      dockDisplay: getComputedStyle(dock).display,
      chipDisplay: getComputedStyle(chip).display,
      chipText: chip.textContent?.trim() ?? '',
      chipWidth: rect.width,
      chipHeight: rect.height,
    }
  })
  expect(result.dockDisplay).toBe('none')
  expect(result.chipDisplay).not.toBe('none')
  expect(result.chipText).toMatch(expected)
  expect(result.chipWidth).toBeGreaterThan(0)
  expect(result.chipHeight).toBeGreaterThan(0)
}

async function expectMobileCompactFlow(page: Page, requireInitialCanvas = true) {
  const result = await page.evaluate(() => {
    const canvas = document.querySelector<HTMLElement>('.am-canvas')!.getBoundingClientRect()
    const conversation = document.querySelector<HTMLElement>('.am-conv--compact')!.getBoundingClientRect()
    const root = document.querySelector<HTMLElement>('.am-root')!.getBoundingClientRect()
    const selected = document.querySelector<HTMLElement>('[data-selected="true"]')!.getBoundingClientRect()
    const renderedLines = [...document.querySelectorAll<HTMLElement>('.am-conv-line')]
      .filter((line) => getComputedStyle(line).display !== 'none')
    const feedDock = document.querySelector<HTMLElement>('.am-conv-dock')!
    const essentialText = [
      document.querySelector<HTMLElement>('[data-selected="true"] .am-card-title')!,
      document.querySelector<HTMLElement>('[data-selected="true"] .am-card-now-text')!,
    ].map((element) => {
      const style = getComputedStyle(element)
      return {
        text: element.textContent?.trim(),
        whiteSpace: style.whiteSpace,
        textOverflow: style.textOverflow,
        overflowX: style.overflowX,
        clipped: element.scrollWidth > element.clientWidth || element.scrollHeight > element.clientHeight,
      }
    })
    const essentialFacts = [...document.querySelectorAll<HTMLElement>('[data-selected="true"] .am-card-facts dd')]
      .map((element) => {
        const style = getComputedStyle(element)
        return {
          text: element.textContent?.trim(),
          whiteSpace: style.whiteSpace,
          textOverflow: style.textOverflow,
          overflowX: style.overflowX,
          overflowWrap: style.overflowWrap,
          clipped: element.scrollWidth > element.clientWidth || element.scrollHeight > element.clientHeight,
        }
      })
    return {
      renderedLines: renderedLines.length,
      feedDockDisplay: getComputedStyle(feedDock).display,
      conversationWidth: conversation.width,
      rootWidth: root.width,
      canvasHeight: canvas.height,
      selectedWithinCanvas: selected.top >= canvas.top && selected.bottom <= canvas.bottom,
      horizontalOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth,
      essentialText,
      essentialFacts,
    }
  })
  expect(result.renderedLines).toBe(1)
  expect(result.feedDockDisplay).toBe('none')
  await expectCompactFeedAuthority(page, /^Live(?:\s|$)/)
  expect(result.conversationWidth).toBeGreaterThanOrEqual(result.rootWidth - 29)
  expect(result.canvasHeight).toBeGreaterThanOrEqual(420)
  if (requireInitialCanvas) expect(result.selectedWithinCanvas).toBe(true)
  expect(result.horizontalOverflow).toBe(false)
  expect(result.essentialText).toEqual([
    {
      text: 'Release 5.11.0 smoke suite',
      whiteSpace: 'normal',
      textOverflow: 'clip',
      overflowX: 'visible',
      clipped: false,
    },
    {
      text: 'Smoke-testing the production release',
      whiteSpace: 'normal',
      textOverflow: 'clip',
      overflowX: 'visible',
      clipped: false,
    },
  ])
  expect(result.essentialFacts).toEqual([
    {
      text: 'Codex · agent', whiteSpace: 'normal', textOverflow: 'clip', overflowX: 'visible',
      overflowWrap: 'anywhere', clipped: false,
    },
    {
      text: 'Deployment · 4/5', whiteSpace: 'normal', textOverflow: 'clip', overflowX: 'visible',
      overflowWrap: 'anywhere', clipped: false,
    },
    {
      text: '1 minute ago', whiteSpace: 'normal', textOverflow: 'clip', overflowX: 'visible',
      overflowWrap: 'anywhere', clipped: false,
    },
  ])
}

async function expectReducedMotionStatic(page: Page, state: string) {
  const moving = await page.locator('.aml-shell').evaluate((shell) => {
    const seconds = (value: string): number => {
      const trimmed = value.trim().toLowerCase()
      const parsed = Number.parseFloat(trimmed)
      if (!Number.isFinite(parsed)) return 0
      return trimmed.endsWith('ms') ? parsed / 1000 : parsed
    }
    const values = (value: string): string[] => value.split(',').map((part) => part.trim())
    // Color-only transitions do not move, resize, fade, or occlude content.
    // Everything else, including `all`, opacity, shadow, and transform, is a
    // reduced-motion violation.
    const chromaticOnly = new Set([
      'color', 'background-color', 'border-color', 'border-top-color', 'border-right-color',
      'border-bottom-color', 'border-left-color', 'outline-color', 'text-decoration-color', 'fill', 'stroke',
    ])
    const findings: Array<Record<string, string>> = []
    const elements = [shell as HTMLElement, ...shell.querySelectorAll<HTMLElement>('*')]
    for (const element of elements) {
      const rect = element.getBoundingClientRect()
      const base = getComputedStyle(element)
      if (rect.width <= 0 || rect.height <= 0 || base.display === 'none' || base.visibility === 'hidden') continue
      for (const pseudo of ['', '::before', '::after'] as const) {
        const style = getComputedStyle(element, pseudo || null)
        if (pseudo && (style.content === 'none' || style.content === 'normal')
          && style.backgroundImage === 'none' && style.boxShadow === 'none') continue
        const animationNames = values(style.animationName)
        const animationDurations = values(style.animationDuration).map(seconds)
        const animated = animationNames.some((name, index) =>
          name !== 'none' && (animationDurations[index % animationDurations.length] ?? 0) > 0)
        const properties = values(style.transitionProperty)
        const durations = values(style.transitionDuration).map(seconds)
        const transitioned = properties.some((property, index) =>
          property !== 'none' && !chromaticOnly.has(property)
          && (durations[index % durations.length] ?? 0) > 0)
        if (animated || transitioned) {
          findings.push({
            tag: element.tagName,
            id: element.id,
            className: String(element.className),
            pseudo,
            animation: `${style.animationName} ${style.animationDuration}`,
            transition: `${style.transitionProperty} ${style.transitionDuration}`,
          })
        }
      }
    }
    return findings
  })
  expect(moving, `nonzero reduced-motion animation/transition in ${state}`).toEqual([])
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
  await page.locator('.am-project-picker__trigger').click()
  await expect(page.locator('.am-project-picker__options [role="option"]')).toHaveCount(5)
  await expect(page.locator('[data-project-id="77"]')).toContainText('No active deliveries')
  await page.keyboard.press('Escape')
  await expect(page.locator('.am-project-picker__popover')).toBeHidden()
  await expect(page.locator('.am-hints-current')).toBeVisible()
  const logo = page.locator('.aml-brand-logo')
  await expect(logo).toHaveAttribute('src', '/logo.svg')
  expect(await logo.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true)
  await page.screenshot({ path: `${SHOT_DIR}/desktop-10.png` })

  for (const [width, height] of [[1024, 900], [900, 800], [736, 900], [640, 900], [520, 900], [390, 844]] as const) {
    await openReady(page, width, height)
    await expectControlVoiceGeometry(page)
    await expectDockClear(page)
    if (width <= 640) await expectCompactFeedAuthority(page, /^Live(?:\s|$)/)
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
      await page.screenshot({ path: `${SHOT_DIR}/phone-390.png` })
    }
  }

  await openReady(page, 1440, 1000, '&detail=1')
  await page.screenshot({ path: `${SHOT_DIR}/desktop-1.png` })
  await openReady(page, 1440, 1000, '&detail=100')
  await page.screenshot({ path: `${SHOT_DIR}/desktop-100.png` })
  for (const [detail, filename] of [['1', 'phone-1.png'], ['100', 'phone-100.png']] as const) {
    await openReady(page, 390, 844, `&detail=${detail}`)
    await expectControlVoiceGeometry(page)
    await expectDockClear(page)
    await expectMobileCompactFlow(page, false)
    await page.screenshot({ path: `${SHOT_DIR}/${filename}` })
  }
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
  for (const [detail, label] of [['1', 'desktop Detail 1'], ['', 'desktop Detail 10'], ['100', 'desktop Detail 100']] as const) {
    await openReady(page, 1440, 1000, detail ? `&detail=${detail}` : '')
    await expectSelectedAnchorInInitialCanvas(page)
    const colors = await page.locator('.am-root').evaluate((el) => {
      const style = getComputedStyle(el)
      return { background: style.backgroundColor, ink: style.color }
    })
    expect(colors.background).toBe('rgb(242, 245, 248)')
    expect(colors.ink).toBe('rgb(26, 38, 54)')
    await expectReducedMotionStatic(page, label)
    if (!detail) await page.screenshot({ path: `${SHOT_DIR}/desktop-10-reduced-dark-os.png` })
  }

  await openReady(page, 390, 844, '&detail=1')
  await page.getByRole('button', { name: 'Open ticket' }).click()
  await expect(page.locator('.side-panel--embedded')).toBeVisible()
  await expectReducedMotionStatic(page, 'phone Detail 1 with embedded ticket')

  await openReady(page, 390, 844)
  await page.locator('.am-controls-target > button', { hasText: 'Cancel running run' }).click()
  await expect(page.locator('.am-controls-challenge')).toBeVisible()
  await expectReducedMotionStatic(page, 'phone Detail 10 control challenge')

  await openReady(page, 390, 844, '&detail=100')
  await expectReducedMotionStatic(page, 'phone Detail 100')
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

  await page.setViewportSize({ width: 390, height: 844 })

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
  await expectCompactFeedAuthority(page, /^Offline(?:\s|$)/)
})

test('PAI-811 browser replaces deployed-unverified truth with later exact-artifact verification', async ({ page }) => {
  await installStaticHost(page)
  const api = await installApiFixtures(page)
  await openReady(page, 1440, 1000)

  const selected = page.locator('[data-selected="true"]')
  await expect(selected).toHaveCount(1)
  await expect(selected.locator('.am-card-reason')).toHaveText('Deployed — verification needed')
  await expect(page.locator('body')).not.toContainText('deployed_unverified')

  const requestsBeforeVerification = api.deliveryRequests
  api.setDeliverySnapshot(makeVerifiedDeliverySnapshot())
  await emitVisualStreamEvent(page, 'refetch')

  await expect.poll(() => api.deliveryRequests).toBeGreaterThan(requestsBeforeVerification)
  await expect(selected).toHaveCount(1)
  await expect(page.locator('[aria-current="true"]')).toHaveCount(1)
  await expect(selected.locator('.am-card-reason')).toHaveCount(0)
  await expect(selected.locator('.am-card-facts')).toContainText('Verification · 5/5')
  await expect(selected.locator('.am-card-status')).toHaveText('Verified exact production artifact')
  await expect(selected).not.toContainText('Deployed — verification needed')
  await expect(page.locator('body')).not.toContainText('deployed_unverified')
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
  await expect(page.locator('.am-controls-live')).toContainText('The pending command was withdrawn.')
  await expect(page.locator('.am-controls-dismiss')).toBeVisible()
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

test('PAI-806 final QA switches deliveries and projects by mouse, keyboard, and typed voice', async ({ page }) => {
  await installStaticHost(page)
  const api = await installApiFixtures(page, true)

  const requireId = (id: string | null, label: string) => {
    if (!id) throw new Error(`${label} must expose a delivery id`)
    return id
  }
  const selectedId = async () => requireId(
    await page.locator('[data-selected="true"]').getAttribute('data-delivery-id'),
    'selected card',
  )
  const submitVoice = async (command: string) => {
    const input = page.locator('#am-voice-command')
    await input.fill(command)
    await input.press('Enter')
  }

  await openReady(page, 1440, 1000)
  const initialId = await selectedId()

  // Mouse: choose another visible delivery card.
  const mouseTarget = page.locator('.am-lanes [data-card-hit]').first()
  const mouseTargetId = requireId(await mouseTarget.getAttribute('data-card-hit'), 'mouse target')
  expect(mouseTargetId).not.toBe(initialId)
  await mouseTarget.click()
  await expect.poll(selectedId).toBe(mouseTargetId)
  await expect.poll(() => new URL(page.url()).searchParams.get('delivery')).toBe(mouseTargetId)

  // Keyboard: the selected card owns roving focus and arrow travel.
  const beforeKeyboard = await selectedId()
  await page.locator(`[data-card-hit="${beforeKeyboard}"]`).focus()
  await page.keyboard.press('ArrowRight')
  await expect.poll(selectedId).not.toBe(beforeKeyboard)
  const afterKeyboard = await selectedId()
  await expect(page.locator(`[data-card-hit="${afterKeyboard}"]`)).toBeFocused()
  await expect.poll(() => new URL(page.url()).searchParams.get('delivery')).toBe(afterKeyboard)

  // Typed voice uses the same selection state machine.
  const beforeVoice = await selectedId()
  await submitVoice('next')
  await expect.poll(selectedId).not.toBe(beforeVoice)
  const afterVoice = await selectedId()
  await expect.poll(() => new URL(page.url()).searchParams.get('delivery')).toBe(afterVoice)

  // Mouse: switch to another project, then select one of its issues.
  await page.locator('.am-project-picker__trigger').click()
  await page.locator('[data-project-id="9"]').click()
  await expect.poll(() => new URL(page.url()).searchParams.get('project')).toBe('9')
  await expect(page.locator('.am-lanes .am-project')).toHaveCount(1)
  await expect(page.locator('.am-lanes .am-project')).toHaveAttribute('aria-labelledby', 'am-project-9')
  const runIssue = page.locator('.am-lanes [data-card-hit]').first()
  const runIssueId = requireId(await runIssue.getAttribute('data-card-hit'), 'RUN issue target')
  await runIssue.click()
  await expect.poll(selectedId).toBe(runIssueId)
  await expect.poll(() => new URL(page.url()).searchParams.get('delivery')).toBe(runIssueId)

  // Keyboard: search the project picker and choose the sole match with Enter.
  await page.locator('.am-project-picker__trigger').focus()
  await page.keyboard.press('Enter')
  const projectSearch = page.locator('.am-project-picker__search input')
  await projectSearch.fill('Release operations')
  await projectSearch.press('Enter')
  await expect.poll(() => new URL(page.url()).searchParams.get('project')).toBe('12')
  await expect(page.locator('.am-lanes .am-project')).toHaveAttribute('aria-labelledby', 'am-project-12')

  // Typed voice: switch projects and select an exact issue within that project.
  await submitVoice('project PAIMOS Core platform')
  await expect.poll(() => new URL(page.url()).searchParams.get('project')).toBe('6')
  await submitVoice('select PAI-821')
  await expect.poll(selectedId).toBe('dlv-821')
  await expect.poll(() => new URL(page.url()).searchParams.get('delivery')).toBe('dlv-821')

  const finalQuery = new URLSearchParams(api.deliveryQueries.at(-1))
  expect(finalQuery.get('project_id')).toBe('6')
  expect(finalQuery.get('selected_delivery')).toBe('dlv-821')
})
