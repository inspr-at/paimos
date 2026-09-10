import { expect, test, type Page } from '@playwright/test'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { extname, resolve } from 'node:path'
import { habitatFixture } from '../src/components/habitat/__fixtures__/orchestration'
import deliveryFixture from '../../backend/contracts/fixtures/agent-mode/snapshot-v1-1.json' with { type: 'json' }

// The controller runs this against the exact compiled bundle. Every HTTP route
// is intercepted; this spec never starts or contacts a Paimos runtime.
const SELF_HOST = process.env.PAI927_SELF_HOST_DIST === '1'
const ORIGIN = 'https://pai927.local'
const DIST = resolve(process.cwd(), 'dist')
const SHOTS = process.env.PAI927_SHOT_DIR ?? '/tmp/pai927-shots'
test.skip(!SELF_HOST, 'Set PAI927_SELF_HOST_DIST=1 after building the production bundle')
test.beforeAll(async () => {
  if (SELF_HOST) await mkdir(SHOTS, { recursive: true })
})

async function installFixture(page: Page) {
  const state = {
    empty: false,
    stopped: false,
    outsideRequests: [] as string[],
    denied: false,
    offline: false,
    runtimeOffline: true,
    conflict: false,
    epoch: '1',
    revision: 8,
    controls: 0,
    rootRevision: 1,
    rootLabel: 'Fixture coordinator',
    intent: null as Record<string, unknown> | null,
    failedStart: false,
  }
  await page.addInitScript(() => {
    localStorage.setItem('paimos:habitat-theme', 'day')
    class QuietEventSource extends EventTarget {
      readyState = 1
      onerror = null
      constructor(_url: string) {
        super()
      }
      close() {
        this.readyState = 2
      }
    }
    Object.defineProperty(window, 'EventSource', { value: QuietEventSource, configurable: true })
  })
  await page.route('**/*', async (route) => {
    const url = new URL(route.request().url())
    if (url.origin !== ORIGIN) {
      state.outsideRequests.push(url.origin)
      return route.abort()
    }
    const path = url.pathname
    const fulfill = (json: unknown, status = 200) =>
      route.fulfill({
        status,
        json,
        headers: { 'X-Permissions-Epoch': state.epoch, 'Cache-Control': 'private, no-store' },
      })
    if (path === '/api/auth/me')
      return fulfill({
        user: {
          id: 7,
          username: 'fixture-user',
          nickname: 'Fixture User',
          role: 'super_admin',
          is_super_admin: true,
          status: 'active',
          avatar_path: '',
          locale: 'en',
          timezone: 'auto',
        },
        access: { all_projects: true, levels: {} },
        suppress_security_nags: true,
      })
    if (path === '/api/auth/logout') return fulfill({ ok: true })
    if (path === '/api/branding')
      return fulfill({ name: 'PAIMOS', company: 'PAIMOS', product: 'PAIMOS', logo: '/logo.svg' })
    if (path === '/api/instance')
      return fulfill({
        label: 'FIXTURE',
        hostname: 'pai927.local',
        attachments_enabled: false,
        live_updates_enabled: false,
      })
    if (path === '/api/health')
      return fulfill({
        agent_bus_identity_enforced: true,
        deployment_instance: 'fixture',
        agent_bus_instance: 'fixture',
      })
    if (path === '/api/projects') return fulfill([{ id: 1, key: 'PAI', name: 'Paimos' }])
    if (path.endsWith('/agents')) return fulfill([{ project_id: 1, name: 'coordinator' }])
    if (path.endsWith('/orchestration/v1')) {
      if (state.denied) return fulfill({ error: 'unavailable' }, 404)
      if (state.offline) return fulfill({ error: 'offline' }, 503)
      const snapshot = habitatFixture(
        url.searchParams.get('zoom') ?? '10',
        path.includes('/projects/1/') ? 1 : null,
        state.empty,
      )
      snapshot.fleet.observed_at = new Date().toISOString()
      for (const worker of snapshot.fleet.workers) {
        worker.liveness.observed_at = snapshot.fleet.observed_at
        worker.revision = state.revision
        if (state.stopped) {
          worker.phase = 'stopped'
          worker.liveness.state = 'dead'
          worker.liveness.reason = 'stopped'
          worker.liveness.closed_reason = 'stopped'
          worker.delivery_trust.reason = 'ticket_unbound'
          worker.recent_communication = []
        }
      }
      if (state.stopped) {
        snapshot.instance_root.active_generation = {
          state: 'unset',
          reason: 'no_active_root_generation',
          session_id: null,
        }
        snapshot.project_coordination[0].coordinator = {
          state: 'unset',
          reason: 'no_active_coordinator',
          session_id: null,
        }
        snapshot.fleet.projects[0].orchestrator = {
          ...snapshot.project_coordination[0].coordinator,
        }
      }
      return fulfill(snapshot)
    }
    if (path === '/api/projects/1/session-home/zoom/v1')
      return fulfill({
        schema_version: 2,
        project_id: 1,
        zoom: url.searchParams.get('zoom') ?? '1',
        band: 'detail',
        sample_limit: 1,
        sample_truncated: false,
        sessions: [],
        selected_session: null,
        totals: {
          sessions: 0,
          unread: 0,
          attention_sessions: 0,
          exception_messages: 0,
          action_requests: 0,
          exception_targets: 0,
          sampled_exception_targets: 0,
        },
      })
    if (path === '/api/agent-mode/deliveries') return fulfill(deliveryFixture)
    if (path === '/api/orchestrator/v1/config') {
      if (route.request().method() === 'PUT') {
        const body = route.request().postDataJSON()
        if (state.conflict || body.expected_revision !== state.rootRevision)
          return fulfill({ error: 'revision_conflict' }, 409)
        state.rootRevision++
        state.rootLabel = body.orchestrator.display_label
      }
      return fulfill({
        schema_version: 1,
        revision: state.rootRevision,
        orchestrator: {
          project_id: 1,
          project_key: 'PAI',
          project_agent_id: 10,
          key: 'coordinator',
          display_label: state.rootLabel,
        },
        updated_at: '2026-09-06T10:00:00Z',
      })
    }
    if (path === '/api/ai/execution-options')
      return fulfill({ dispatch_profiles: [habitatFixture().fleet.workers[0].dispatch_profile] })
    if (path === '/api/projects/1/lifecycle/v1/runtimes') {
      const profile = habitatFixture().fleet.workers[0].dispatch_profile!
      return fulfill({
        schema_version: 1,
        runtimes: state.runtimeOffline
          ? []
          : [
              {
                id: '00000000-0000-4000-8000-000000000010',
                project_id: 1,
                generation: '00000000-0000-4000-8000-000000000011',
                machine_id: 'fixture-machine',
                account_label: 'chatgpt',
                workspaces: [
                  { handle: '00000000-0000-4000-8000-000000000012', identity: 'a'.repeat(64) },
                ],
                profiles: [{ id: profile.id, version: profile.version }],
                sessions: [],
                expires_at: new Date(Date.now() + 120_000).toISOString(),
              },
            ],
      })
    }
    if (path === '/api/projects/1/lifecycle/v1/runtime-health')
      return fulfill({ schema_version: 1, observed_at: new Date().toISOString(), runtimes: [] })
    if (path === '/api/projects/1/lifecycle/v1/intents' && route.request().method() === 'POST') {
      state.intent = {
        schema_version: 1,
        id: '00000000-0000-4000-8000-000000000013',
        project_id: 1,
        request: route.request().postDataJSON(),
        state: 'requested',
        revision: 1,
        reason: '',
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        expires_at: new Date(Date.now() + 120_000).toISOString(),
        new_generation: '00000000-0000-4000-8000-000000000014',
      }
      return fulfill(state.intent, 201)
    }
    if (path.includes('/lifecycle/v1/intents/') && state.intent) {
      if (state.failedStart)
        Object.assign(state.intent, { state: 'failed', reason: 'unsupported', revision: 2 })
      return fulfill(state.intent)
    }
    if (/\/harness-sessions\/[^/]+\/controls\/v1\/stop$/.test(path)) {
      const body = route.request().postDataJSON()
      if (body.expected_revision !== state.revision)
        return fulfill({ error: 'revision_conflict' }, 409)
      state.controls++
      return fulfill(
        {
          schema_version: 1,
          state: 'requested',
          control: {
            sequence: 1,
            requested_by_user_id: 7,
            requested_at: new Date().toISOString(),
            id: '00000000-0000-4000-8000-000000000099',
            harness_session_id: path.split('/')[5],
            kind: 'stop',
            state: 'pending',
          },
        },
        201,
      )
    }
    if (path.includes('/controls/00000000-'))
      return fulfill({
        id: '00000000-0000-4000-8000-000000000099',
        project_id: 1,
        harness_session_id: '00000000-0000-4000-8000-000000000001',
        correlation_id: '00000000-0000-4000-8000-000000000099',
        kind: 'stop',
        sequence: 1,
        state: 'applied',
        outcome: 'applied',
        reason: 'applied',
        completed_at: new Date().toISOString(),
        requested_at: new Date().toISOString(),
      })
    if (path.endsWith('/assignment-history/v1'))
      return fulfill({
        schema_version: 1,
        session_id: path.split('/')[5],
        events: [],
        next_after_revision: null,
      })
    if (path.startsWith('/api/')) return fulfill([])
    // Production reserves this prefix for administrator-uploaded branding.
    // Bundled originals must use /assets/brand, never the upload route.
    if (path.startsWith('/brand/')) return fulfill({ error: 'not found' }, 404)
    const relative =
      path.startsWith('/assets/') || ['/logo.svg', '/favicon.svg', '/app-icon.svg'].includes(path)
        ? path.slice(1)
        : 'index.html'
    const types: Record<string, string> = {
      '.html': 'text/html',
      '.js': 'text/javascript',
      '.css': 'text/css',
      '.svg': 'image/svg+xml',
      '.png': 'image/png',
      '.woff': 'font/woff',
      '.woff2': 'font/woff2',
    }
    return route.fulfill({
      body: await readFile(resolve(DIST, relative)),
      contentType: types[extname(relative)] ?? 'application/octet-stream',
    })
  })
  return state
}
async function assertFits(page: Page) {
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1),
  ).toBe(true)
  expect(
    await page
      .locator('.habitat-card, .habitat-inspector')
      .evaluateAll((elements) =>
        elements.every((element) => element.scrollWidth <= element.clientWidth + 1),
      ),
  ).toBe(true)
}

test('the original brand assets and local fonts render intact in bright and dark setup', async ({
  page,
}) => {
  const fixture = await installFixture(page)
  fixture.empty = true
  const fontResponses: number[] = []
  page.on('response', (response) => {
    if (new URL(response.url()).pathname.endsWith('.woff2')) fontResponses.push(response.status())
  })
  await page.goto(`${ORIGIN}/?view=home`)
  await expect(page.locator('.habitat-welcome')).toBeVisible()
  await expect(page.locator('.habitat-welcome-art')).toHaveCount(0)
  await expect(
    page.getByRole('heading', { name: 'Start with your coordinator.', exact: true }),
  ).toBeVisible()
  await page.evaluate(() => document.fonts.ready)
  expect(await page.evaluate(() => document.fonts.check('14px "DM Sans"'))).toBe(true)
  expect(fontResponses.length).toBeGreaterThan(0)
  expect(fontResponses.every((status) => status === 200)).toBe(true)

  const assets = []
  for (const [path, expected] of [
    ['/logo.svg', '970f026a738885b60bf9fac122b3563fe2b9fe242199845920acdc3975628396'],
    ['/favicon.svg', '970f026a738885b60bf9fac122b3563fe2b9fe242199845920acdc3975628396'],
    ['/app-icon.svg', '970f026a738885b60bf9fac122b3563fe2b9fe242199845920acdc3975628396'],
    [
      '/assets/brand/paimos-logo.svg',
      '970f026a738885b60bf9fac122b3563fe2b9fe242199845920acdc3975628396',
    ],
    [
      '/assets/brand/paimos-hero.png',
      'f9467ce93d4d076a9dd9e555ae76047077171a401048fdca828634ff379588ba',
    ],
  ]) {
    const asset = await page.evaluate(async (path) => {
      const response = await fetch(path)
      const digest = await crypto.subtle.digest('SHA-256', await response.arrayBuffer())
      return {
        path,
        status: response.status,
        sha256: [...new Uint8Array(digest)]
          .map((byte) => byte.toString(16).padStart(2, '0'))
          .join(''),
      }
    }, path)
    expect(asset.status).toBe(200)
    expect(asset.sha256).toBe(expected)
    assets.push(asset)
  }
  const renders = []
  for (const [width, height] of [
    [1440, 900],
    [320, 740],
    [390, 844],
  ]) {
    await page.setViewportSize({ width, height })
    for (const theme of ['day', 'night'] as const) {
      if ((await page.locator('.habitat-shell').getAttribute('data-theme')) !== theme)
        await page
          .getByRole('button', {
            name: theme === 'night' ? 'Switch to dark mode' : 'Switch to bright mode',
          })
          .click()
      const mark = page.locator('.habitat-brand img')
      await expect(mark).toBeVisible()
      const render = await mark.evaluate((img: HTMLImageElement) => {
        const style = getComputedStyle(img)
        const box = img.getBoundingClientRect()
        return {
          naturalWidth: img.naturalWidth,
          naturalHeight: img.naturalHeight,
          width: box.width,
          height: box.height,
          filter: style.filter,
          opacity: style.opacity,
          backing: style.backgroundColor,
        }
      })
      expect(render.naturalWidth).toBeGreaterThan(0)
      expect(render.naturalWidth).toBe(render.naturalHeight)
      expect(render.filter).toBe('none')
      expect(render.opacity).toBe('1')
      expect(render.backing).toBe('rgb(247, 246, 242)')
      renders.push({ viewportWidth: width, theme, ...render })
      if (width === 1440) {
        await expect(page.locator('.habitat-welcome-art')).toHaveCount(0)
        await expect(
          page.getByRole('heading', { name: 'Start with your coordinator.', exact: true }),
        ).toBeVisible()
      }
      const primary = page.getByRole('button', { name: 'Set up coordinator', exact: true })
      await primary.focus()
      await expect(primary).toBeFocused()
      await assertFits(page)
      await page.screenshot({ path: `${SHOTS}/setup-welcome-${width}-${theme}.png`, fullPage: true })
    }
  }
  await page.goto(`${ORIGIN}/?view=assign&project=1`)
  await expect(page.getByRole('heading', { name: 'Project & canonical agent' })).toBeVisible()
  for (const [width, height] of [
    [1440, 900],
    [390, 844],
  ]) {
    await page.setViewportSize({ width, height })
    for (const theme of ['day', 'night'] as const) {
      if ((await page.locator('.habitat-shell').getAttribute('data-theme')) !== theme)
        await page
          .getByRole('button', {
            name: theme === 'night' ? 'Switch to dark mode' : 'Switch to bright mode',
          })
          .click()
      await assertFits(page)
      await page.screenshot({ path: `${SHOTS}/setup-form-${width}-${theme}.png`, fullPage: true })
    }
  }
  expect(fixture.outsideRequests).toEqual([])
  await writeFile(
    `${SHOTS}/brand-assets.json`,
    JSON.stringify(
      { assets, renders, fontResponses, outsideRequests: fixture.outsideRequests },
      null,
      2,
    ),
  )
})

test('Home is truthful and fits rich and empty workspaces in both themes at desktop and phone widths', async ({
  page,
}) => {
  const fixture = await installFixture(page)
  for (const scenario of ['rich', 'empty', 'stopped'] as const) {
    fixture.empty = scenario === 'empty'
    fixture.stopped = scenario === 'stopped'
    await page.goto(`${ORIGIN}/?view=home`)
    await expect(page.getByRole('heading', { name: 'Your projects', exact: false })).toBeVisible()
    const sample = habitatFixture('10', null, fixture.empty)
    await expect(page.locator('.habitat-overview > button').first()).toContainText(
      `${sample.fleet.totals.workers}`,
    )
    if (fixture.empty) {
      await expect(
        page.getByRole('button', { name: 'Set up coordinator', exact: true }),
      ).toBeVisible()
      await expect(page.locator('.habitat-roster-row')).toHaveCount(0)
      await expect(page.locator('.habitat-project-table')).toContainText('No workers yet')
    } else {
      await expect(page.locator('.habitat-roster-row')).toHaveCount(
        Math.min(4, sample.fleet.workers.length),
      )
      await expect(page.getByRole('heading', { name: 'Needs you', level: 2 })).toBeVisible()
      await expect(page.locator('.habitat-welcome')).toHaveCount(0)
      if (fixture.stopped) {
        await expect(page.locator('.habitat-worker-request')).toHaveCount(0)
        await expect(page.locator('.habitat-roster-state')).toHaveText(['Stopped', 'Stopped'])
        await expect(page.getByRole('button', { name: 'Review start', exact: true })).toBeVisible()
      }
    }
    for (const [label, width, height] of [
      ['desktop', 1440, 900],
      ['phone', 320, 740],
      ['phone-390', 390, 844],
    ] as const) {
      await page.setViewportSize({ width, height })
      for (const theme of ['day', 'night'] as const) {
        if ((await page.locator('.habitat-shell').getAttribute('data-theme')) !== theme)
          await page
            .getByRole('button', {
              name: theme === 'night' ? 'Switch to dark mode' : 'Switch to bright mode',
            })
            .click()
        await assertFits(page)
        await page.screenshot({
          path: `${SHOTS}/home-${scenario}-${label}-${theme}.png`,
          fullPage: true,
        })
      }
    }
  }
})

test('Habitat selected design: bright/dark, worker tree, phone, short laptop, 200% and reduced motion', async ({
  page,
}) => {
  await installFixture(page)
  await page.goto(`${ORIGIN}/?view=workers`)
  await expect(page.getByRole('heading', { name: 'Workers', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Expand coordinator descendants' }).click()
  await page
    .locator('[data-worker-id="00000000-0000-4000-8000-000000000001"] .habitat-worker-select')
    .click()
  const workerSelect = page.locator(
    '[data-worker-id="00000000-0000-4000-8000-000000000001"] .habitat-worker-select',
  )
  for (const [label, width, height] of [
    ['desktop', 1440, 900],
    ['short', 1280, 650],
    ['phone', 320, 740],
    ['phone-390', 390, 844],
  ] as const) {
    await page.setViewportSize({ width, height })
    for (const theme of ['day', 'night'] as const) {
      if (await page.getByRole('button', { name: 'Close inspector' }).count())
        await page.getByRole('button', { name: 'Close inspector' }).click()
      if ((await page.locator('.habitat-shell').getAttribute('data-theme')) !== theme)
        await page
          .getByRole('button', {
            name: theme === 'night' ? 'Switch to dark mode' : 'Switch to bright mode',
          })
          .click()
      await assertFits(page)
      await page.screenshot({ path: `${SHOTS}/workers-${label}-${theme}.png`, fullPage: true })
      await workerSelect.click()
      if (width <= 390) {
        await expect(page.getByRole('dialog', { name: 'Inspector' })).toHaveAttribute(
          'aria-modal',
          'true',
        )
        await expect(page.locator('.habitat-stage')).toHaveAttribute('inert', '')
        await expect(page.getByRole('button', { name: 'Close inspector' })).toBeFocused()
        await page.keyboard.press('Shift+Tab')
        expect(
          await page
            .locator('[aria-label="Inspector"]')
            .evaluate((el) => el.contains(document.activeElement)),
        ).toBe(true)
        await page.keyboard.press('Tab')
        await expect(page.getByRole('button', { name: 'Close inspector' })).toBeFocused()
      }
      await assertFits(page)
      await page.screenshot({ path: `${SHOTS}/${label}-${theme}.png`, fullPage: true })
    }
  }
  await page.keyboard.press('Escape')
  await expect(workerSelect).toBeFocused()
  await expect(page.locator('.habitat-stage')).not.toHaveAttribute('inert', '')
  await page.setViewportSize({ width: 1280, height: 720 })
  await page.evaluate(() => {
    document.documentElement.style.zoom = '2'
  })
  await assertFits(page)
  await page.screenshot({ path: `${SHOTS}/zoom-200.png`, fullPage: true })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  expect(
    await page
      .locator('.habitat-orb')
      .evaluateAll((elements) =>
        elements.every((element) => getComputedStyle(element).animationName === 'none'),
      ),
  ).toBe(true)
  await page.evaluate(() => {
    document.documentElement.style.zoom = '1'
  })
  const selected = page.locator(
    '[data-worker-id="00000000-0000-4000-8000-000000000001"] .habitat-worker-select',
  )
  await selected.focus()
  await page.waitForTimeout(16_000)
  await expect(selected).toBeFocused()
})

test('empty, offline and concealed permission states have recovery and no stale worker claims', async ({
  page,
}) => {
  const fixture = await installFixture(page)
  fixture.empty = true
  await page.goto(`${ORIGIN}/?view=workers`)
  await expect(page.getByText('A root identity is not configured.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Set up a worker' })).toBeVisible()
  await page.screenshot({ path: `${SHOTS}/empty.png`, fullPage: true })
  fixture.empty = false
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  await expect(page.getByText('Fixture coordinator', { exact: true })).toBeVisible()
  fixture.offline = true
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  await expect(page.getByText('The control room could not be refreshed.')).toBeVisible()
  await expect(page.locator('[data-worker-id]')).toHaveCount(0)
  fixture.offline = false
  fixture.denied = true
  await page.getByRole('button', { name: 'Retry', exact: true }).click()
  await expect(page.getByText('This scope is not available to your account.')).toBeVisible()
  await expect(page.getByRole('link', { name: 'Account settings', exact: true })).toBeVisible()
})

test('owned controls require confirmation; stale revisions and unmanaged generations cannot mutate', async ({
  page,
}) => {
  const fixture = await installFixture(page)
  await page.goto(`${ORIGIN}/?view=workers`)
  await page
    .locator('[data-worker-id="00000000-0000-4000-8000-000000000001"] .habitat-worker-select')
    .click()
  await page.getByRole('button', { name: 'Stop', exact: true }).click()
  expect(fixture.controls).toBe(0)
  fixture.revision++
  await page.getByRole('button', { name: 'Confirm stop', exact: true }).click()
  await expect(
    page.getByText('Worker revision or recipient changed. Refresh and review before trying again.'),
  ).toBeVisible()
  expect(fixture.controls).toBe(0)
  await page.getByRole('button', { name: 'Refresh status', exact: true }).click()
  await page.getByRole('button', { name: 'Stop', exact: true }).click()
  await page.getByRole('button', { name: 'Confirm stop', exact: true }).click()
  await expect(
    page.getByText(
      'Control requested. Waiting for owned runtime evidence; completion is not yet confirmed.',
    ),
  ).toBeVisible()
  expect(fixture.controls).toBe(1)
  await page.getByRole('button', { name: 'Check control outcome' }).click()
  await expect(page.getByText('Control evidence refreshed.')).toBeVisible()
  await page.getByRole('button', { name: 'Expand coordinator descendants' }).click()
  await page
    .locator('[data-worker-id="00000000-0000-4000-8000-000000000002"] .habitat-worker-select')
    .click()
  await expect(page.getByRole('button', { name: 'Stop', exact: true })).toBeDisabled()
})

test('browser setup uses CAS and lifecycle shows disconnect/reconnect and a failed start honestly', async ({
  page,
}) => {
  const fixture = await installFixture(page)
  await page.goto(`${ORIGIN}/?view=assign&project=1`)
  await page
    .getByRole('combobox', { name: 'Canonical agent', exact: true })
    .selectOption('coordinator')
  await page.getByLabel('Root display label').fill('Reviewed fixture root')
  await page.getByRole('button', { name: 'Review root binding' }).click()
  fixture.conflict = true
  await page.getByRole('button', { name: 'Confirm root binding' }).click()
  await expect(
    page.getByText(
      'The root binding changed. Refresh setup and review the newer revision before saving.',
    ),
  ).toBeVisible()
  await expect(page.getByText('No live advertised runtime')).toBeVisible()
  fixture.runtimeOffline = false
  await page.getByRole('button', { name: 'Refresh runtimes', exact: true }).click()
  await page
    .getByRole('combobox', { name: 'Profile for a new worker', exact: true })
    .selectOption({ index: 1 })
  await page.getByRole('combobox', { name: 'Runtime', exact: true }).selectOption({ index: 1 })
  await page.getByRole('combobox', { name: 'Workspace', exact: true }).selectOption({ index: 1 })
  await page.getByRole('button', { name: 'Review start request' }).click()
  expect(fixture.intent).toBeNull()
  await page.getByRole('button', { name: 'Confirm exact request' }).click()
  await expect(page.getByRole('heading', { name: 'requested', exact: true })).toBeVisible()
  fixture.failedStart = true
  await page.getByRole('button', { name: 'Refresh intent evidence' }).click()
  await expect(
    page.getByText('The intent failed. Review the reported reason and runtime before recovery.'),
  ).toBeVisible()
  await page.screenshot({ path: `${SHOTS}/failed-start.png`, fullPage: true })
})
