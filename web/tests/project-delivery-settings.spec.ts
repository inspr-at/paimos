// SPDX-License-Identifier: AGPL-3.0-only
// Risks: moving repository controls, false successful writes, overwriting inherited
// settings, invented history progress, overflowing banners, duplicate repository
// cards, and late responses modifying a different project/person.
import { expect, test, type Page } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { controlStability } from './control-stability'
import { deliveryMetrics } from './delivery-numbers-fixtures'
import { checkProjectSettings, captureProjectSettings, approvedCheckSHA } from './project-settings-acceptance.mjs'
import type { MetricSource } from '../src/lib/deliveryNumbers'
import type { DeliverySettings } from '../src/lib/projectDeliverySettings'

const NOW = '2026-10-12T12:00:00Z'
const states = ['none', 'loading', 'error', 'reading', 'done', 'noactions', 'appoff', 'stale'] as const
type State = typeof states[number]
const defaults: DeliverySettings = { required_workflow: 'CI', required_checks: ['go test', 'web check'], deadlines: { reviewed: 30, pushed: 60, ci_green: 20, in_queue: 60, queue_failed: 120 } }
function source(state: State): MetricSource | null {
  if (state === 'none') return null
  return { repository: 'inspr-at/aeon', ci_workflow: '.github/workflows/ci.yml', nightly_workflow: '.github/workflows/nightly-full.yml', app_connected: state !== 'appoff', backfill: state === 'reading' || state === 'noactions' ? 'running' : 'done', backfill_since: '2026-09-12T00:00:00Z', backfill_done_at: state === 'reading' || state === 'noactions' ? null : '2026-10-12T10:00:00Z', covered_since: '2026-09-12T00:00:00Z', last_event_at: state === 'stale' ? '2026-10-03T12:00:00Z' : '2026-10-12T11:00:00Z', last_read_error: state === 'noactions' ? { reason: 'actions_read_missing', permission: 'actions:read', at: '2026-10-12T11:58:00Z' } : null, retry_at: null }
}
async function setup(page: Page) {
  const data = fixtures(), errors = watchErrors(page)
  await mockWork(page, data)
  const world = { state: 'none' as State, readOnly: false, fail: false, actionsReadable: false, leadReason: false, metrics: [] as unknown[], source: null as MetricSource | null, overrides: { deadlines: { ci_green: 30 } } as DeliverySettings, revision: 1, writes: [] as { id: string; body: DeliverySettings; revision: string }[], links: [] as Record<string, unknown>[], history: [] as Record<string, unknown>[], checks: [] as { id: string; body: string | null }[], held: null as null | { entered: () => void; until: Promise<void> }, pendingLoad: null as null | { entered: () => void; until: Promise<void> } }
  const historyPosition = () => ({ state: 'running', since: world.source?.backfill_since, phase: 'pulls', day: '2026-09-30', calls: 4, runs: 412, pulls: 96, jobs: 0, truncated: false })
  const otherSettings = new Map<string, { overrides: DeliverySettings; revision: number }>()
  await page.route('**/api/me/permissions**', route => {
    const id = new URL(route.request().url()).searchParams.get('project_id') ?? undefined
    const answer = mockEffectivePermissions(world.readOnly ? 'viewer' : 'admin', id)
    answer.workspace.permissions.push('delivery.read')
    if (!world.readOnly) answer.workspace.permissions.push('delivery.manage')
    return route.fulfill({ json: answer })
  })
  await page.route('**/api/projects/*/delivery/metrics', async route => {
    const selected = world.state, shown = structuredClone(world.source)
    if (world.pendingLoad) { const hold = world.pendingLoad; hold.entered(); await hold.until }
    if (selected === 'error') return route.fulfill({ status: 503, json: { error: 'read unavailable' } })
    return route.fulfill({ json: { project_id: new URL(route.request().url()).pathname.split('/')[3], generated_at: NOW, source: shown, metrics: world.metrics } })
  })
  await page.route('**/api/projects/*/delivery/metrics/source', route => {
    const body = route.request().postDataJSON(); world.links.push(body)
    if (world.readOnly || world.fail) return route.fulfill({ status: world.readOnly ? 403 : 503, json: { error: 'The repository was not saved.' } })
    world.state = 'noactions'; world.source = { ...source('noactions')!, repository: body.repository, ci_workflow: body.ci_workflow, nightly_workflow: body.nightly_workflow, backfill_since: new Date(Date.parse('2026-10-12T00:00:00Z') - body.days * 86_400_000).toISOString(), retry_at: '2026-10-12T12:28:00Z' }
    return route.fulfill({ json: world.source })
  })
  await page.route('**/api/projects/*/delivery/metrics/backfill', route => {
    const body = route.request().postDataJSON(); world.history.push(body)
    if (!body.restart && world.source?.retry_at && Date.parse(world.source.retry_at) > Date.parse(NOW)) return route.fulfill({ json: { ...historyPosition(), retry_at: world.source.retry_at } })
    if (world.state === 'noactions') return route.fulfill({ status: 502, json: { error: 'GitHub could not be read; the server retries.' } })
    return route.fulfill({ json: historyPosition() })
  })
  await page.route('**/api/projects/*/delivery/metrics/check-again', route => {
    world.checks.push({ id: new URL(route.request().url()).pathname.split('/')[3]!, body: route.request().postData() })
    if (!world.actionsReadable) return route.fulfill({ status: 502, json: { error: 'GitHub could not be read; the server retries.' } })
    world.state = 'reading'; world.source = { ...world.source!, last_read_error: null, retry_at: null }
    return route.fulfill({ json: historyPosition() })
  })
  await page.route('**/api/projects/*/delivery-settings**', async route => {
    const url = new URL(route.request().url()), method = route.request().method()
    const id = url.pathname.split('/')[3]!
    if (id !== 'p-aeon' && !otherSettings.has(id)) otherSettings.set(id, { overrides: {}, revision: 1 })
    const target = id === 'p-aeon' ? world : otherSettings.get(id)!
    if (method === 'PUT') {
      const body = route.request().postDataJSON(), revision = route.request().headers()['if-match']
      world.writes.push({ id: url.pathname.split('/')[3], body, revision })
      if (world.held) { const hold = world.held; world.held = null; hold.entered(); await hold.until }
      if (world.readOnly) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
      if (world.fail || revision !== `"${target.revision}"`) return route.fulfill({ status: 412, json: { error: 'Delivery settings changed; reload before saving.' } })
      target.overrides = structuredClone(body); target.revision++
    }
    const effective = { ...defaults, ...target.overrides, deadlines: { ...defaults.deadlines, ...target.overrides.deadlines } }
    return route.fulfill({ json: url.searchParams.get('view') === 'overrides' ? target.overrides : effective, headers: { ETag: `"${target.revision}"` } })
  })
  await page.route('**/api/nodes/*/delivery**', route => route.fulfill({ json: { items: [], next_cursor: null, available: true } }))
  await page.route('**/api/projects/p-aeon/lead', route => world.leadReason ? route.fulfill({ json: { project_id: 'p-aeon', revision: 0, generation: 0, session_id: null, state: 'none', reason: '', process_active: false, automatic_launch_enabled: false } }) : route.fallback())
  await page.route('**/api/projects/p-aeon/lead-settings', route => world.leadReason ? route.fulfill({ json: { revision: 1, details_redacted: false, automatic_launch_enabled: false, owner_person_id: null, overrides: {}, effective: {}, wait_reason: 'acceptance_pending' } }) : route.fallback())
  return { world, errors }
}
async function navigate(page: Page, path: string) {
  await page.evaluate(path => (document.querySelector('#app') as unknown as { __vue_app__: { config: { globalProperties: { $router: { push(path: string): Promise<void> } } } } }).__vue_app__.config.globalProperties.$router.push(path), path)
}
async function choose(page: Page, world: Awaited<ReturnType<typeof setup>>['world'], state: State, readOnly = false) {
  world.state = state; world.source = source(state); world.readOnly = readOnly
  await page.goto('/p/AEON/settings/delivery')
  await expect(page.locator('#checks')).toBeVisible()
  await expect(page.locator('#checks .frow').first()).toBeVisible()
  await expect(page.locator('.repo-slot')).toHaveAttribute('data-state', state)
  // P3/P4 can merge in either order; the legacy stub must never accompany the editor.
  await expect(page.locator('.settings-body .m-card').filter({ has: page.getByRole('heading', { name: /^Repository(?: & delivery)?$/ }) })).toHaveCount(1)
}

async function bannerContainsAction(page: Page, name: string) {
  const action = page.locator('.dl-status .banner .btn')
  await expect(action).toBeVisible()
  const bounds = await action.evaluate(element => {
    const button = element.getBoundingClientRect(), banner = element.closest('.banner')!.getBoundingClientRect()
    return { width: button.width, height: button.height, gaps: [button.left - banner.left, button.top - banner.top, banner.right - button.right, banner.bottom - button.bottom] }
  })
  expect(bounds.width).toBeGreaterThan(0); expect(bounds.height).toBeGreaterThan(0)
  for (const gap of bounds.gaps) expect(gap, `${name}: action stays inside the banner`).toBeGreaterThanOrEqual(-.5)
  return { passed: true, message: `${name}: banner contains its ${bounds.height}px action on all four sides` }
}

test('AEON-1253: repository shows the history source without moving its controls', async ({ page }, testInfo) => {
  // Risk: a successful least-privilege history still asks for Actions, conceals
  // incomplete metrics, or shifts controls when the source changes.
  test.setTimeout(120_000)
  const { world, errors } = await setup(page)
  world.state = 'done'
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await page.route('**/api/preferences/theme', route => route.fulfill({ json: { key: 'theme', value: { choice: theme } } }))
    world.source = { ...source('done')!, history_source: 'checks' }
    await page.goto('/p/AEON/settings/delivery')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    const card = page.locator('#repository'), line = card.locator('[data-history-source]')
    await expect(line).toContainText('GitHub checks · partial history; attempt and runner wait metrics unavailable.')
    await expect(card.getByText('accept Actions: Read', { exact: false })).toHaveCount(0)
    const more = card.locator('[data-act=repo-more]')
    const stable = await controlStability(page, { change: card.locator('[data-act=change]'), more, actions: card.locator('.repo-acts'), historyActions: card.locator('.ds-acts') })
    await stable.check(async () => {
      await more.click()
      await expect(page.getByRole('menuitem', { name: /^Read history again/ })).toBeVisible()
      await page.keyboard.press('Escape')
    })
    await stable.check(async () => {
      world.source!.history_source = 'actions'
      await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
      await expect(line).toHaveText('History source: GitHub Actions')
    })
    await stable.check(async () => {
      world.source!.history_source = 'checks'
      await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
      await expect(line).toContainText('GitHub checks')
    })
    // Stress variable text at every evidence width without changing the UI copy.
    await stable.check(() => line.evaluate(element => { element.textContent = 'Quelle der Lieferhistorie: GitHub-Prüfungen · Die Historie ist unvollständig; Kennzahlen zu ersten Versuchen und Wartezeiten auf Runner sind nicht verfügbar.' }))
    stable.done()
    await card.scrollIntoViewIfNeeded()
    await page.screenshot({ path: testInfo.outputPath(`history-source-${width}-${theme}.png`), fullPage: true })
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  }
  expect(errors).toEqual([])
})

test('AEON-1226: repository states stay put and delivery edits preserve authority, inheritance and identity', async ({ page }) => {
  test.setTimeout(180_000)
  const { world, errors } = await setup(page)
  for (const width of [1440, 400]) {
    await page.setViewportSize({ width, height: 1000 })
    for (const readOnly of [false, true]) {
      let baseline: number[] | undefined
      for (const state of states) {
        let release!: () => void
        if (state === 'loading') world.pendingLoad = { entered: () => {}, until: new Promise<void>(resolve => { release = resolve }) }
        await choose(page, world, state, readOnly)
        const bounds = await page.locator('.repo-slot').evaluate(el => [el.getBoundingClientRect().height, document.getElementById('checks')!.getBoundingClientRect().top + scrollY])
        expect(bounds[0]).toBe(width === 1440 ? 252 : 460)
        if (baseline) for (const i of [0, 1]) expect(Math.abs(bounds[i]! - baseline[i]!)).toBeLessThanOrEqual(.5)
        else baseline = bounds
        if (readOnly) await expect(page.locator('[data-act="change"], [data-act="check"], [data-act="link"], #add-check')).toHaveCount(0)
        if (state === 'reading') {
          // GET metrics has no phase or cursor; no progress may be inferred from it.
          await expect(page.locator('.phases')).toHaveCount(0)
          await expect(page.getByRole('progressbar', { name: 'History read' })).toHaveCount(0)
        }
        if (state === 'loading') { world.pendingLoad = null; release(); await expect(page.locator('.repo-slot')).not.toHaveAttribute('data-state', 'loading') }
      }
    }
  }
  await page.setViewportSize({ width: 1440, height: 1000 }); await choose(page, world, 'none')
  await navigate(page, '/p/AEON/delivery')
  await page.getByRole('link', { name: 'Link repository', exact: true }).click()
  await expect(page).toHaveURL(/\/p\/AEON\/settings\/delivery\?link=1#repository/)
  await expect(page.locator('#link-form')).toBeVisible()
  await expect(page.locator('#lf-ci')).toHaveValue('ci.yml'); await expect(page.locator('#lf-nightly')).toHaveValue('nightly-full.yml')
  const stable = await controlStability(page, { repo: page.locator('#lf-repo'), ci: page.locator('#lf-ci'), nightly: page.locator('#lf-nightly'), days: page.locator('#lf-days'), submit: page.locator('#link-form button[type=submit]') })
  await stable.check(() => page.locator('#lf-repo').fill('inspr-at/aeon'))
  await stable.check(() => page.locator('#lf-days').selectOption('60'))
  world.fail = true
  await stable.check(async () => { await page.locator('#link-form button[type=submit]').click(); await expect(page.locator('#repository .feedback')).toHaveText('The repository was not saved.') })
  await expect(page.locator('#lf-repo')).toHaveValue('inspr-at/aeon'); stable.done()
  world.fail = false; await page.locator('#link-form button[type=submit]').click()
  await expect(page.locator('.repo-slot')).toHaveAttribute('data-state', 'noactions')
  await expect(page.locator('.ds-h')).toHaveText('Linked. History can’t be read: the GitHub App lacks Actions read access.')
  expect(world.links.at(-1)).toEqual({ repository: 'inspr-at/aeon', ci_workflow: '.github/workflows/ci.yml', nightly_workflow: '.github/workflows/nightly-full.yml', days: 60 })
  await expect.poll(() => world.history.at(-1)).toEqual({ days: 60, restart: false })
  // Risk: Check again silently waits for retry_at after Actions access is granted.
  // The fixed API clock keeps the retry in the future throughout both clicks.
  expect(Date.parse(world.source!.retry_at!)).toBeGreaterThan(Date.parse(NOW))
  const historyBeforeCheck = structuredClone(world.history), linkedSince = world.source!.backfill_since
  await page.setViewportSize({ width: 400, height: 1000 })
  const linked = await controlStability(page, { change: page.locator('[data-act=change]'), check: page.locator('[data-act=check]'), checks: page.locator('#checks') })
  await linked.check(async () => { await page.locator('[data-act=check]').click(); await expect(page.locator('[data-act=check]')).toBeEnabled() })
  linked.done(); expect(world.checks).toEqual([{ id: 'p-aeon', body: null }])
  expect(world.history).toEqual(historyBeforeCheck)
  await expect(page.locator('.repo-slot')).toHaveAttribute('data-state', 'noactions')
  expect(world.source!.last_read_error?.reason).toBe('actions_read_missing')
  expect(Date.parse(world.source!.retry_at!)).toBeGreaterThan(Date.parse(NOW))
  world.actionsReadable = true
  const retry = await controlStability(page, { change: page.locator('[data-act=change]'), more: page.locator('[data-act=repo-more]'), slot: page.locator('.repo-slot'), checks: page.locator('#checks') })
  await retry.check(async () => { await page.locator('[data-act=check]').click(); await expect(page.locator('.repo-slot')).toHaveAttribute('data-state', 'reading') })
  retry.done(); expect(world.checks).toEqual([{ id: 'p-aeon', body: null }, { id: 'p-aeon', body: null }])
  expect(world.history).toEqual(historyBeforeCheck)
  expect(world.source!.retry_at).toBeNull(); expect(world.source!.last_read_error).toBeNull()
  expect(world.source!.backfill_since).toBe(linkedSince)
  await expect(page.locator('.phases .now')).toHaveText('Pull requests')
  await expect(page.getByRole('progressbar', { name: 'History read' })).toHaveAttribute('aria-valuenow', '48')
  await expect(page.locator('.phases svg')).toHaveCount(3)
  await expect(page.locator('.phases svg path')).toHaveCount(0)
  await page.locator('[data-act=repo-more]').click(); await page.getByRole('menuitem', { name: /Read history again/ }).click()
  await expect.poll(() => world.history.at(-1)).toEqual({ days: 60, restart: true })
  await page.setViewportSize({ width: 1440, height: 1000 })

  const wf = page.locator('#f-wf'), deadline = page.locator('#dl-ci_green')
  await wf.fill('Release CI'); await wf.press('Enter'); await expect(wf).toBeEnabled()
  expect(world.writes.at(-1)?.body).toEqual({ required_workflow: 'Release CI', deadlines: { ci_green: 30 } })
  await page.locator('#checks .frow').first().getByRole('button', { name: 'Reset', exact: true }).click(); await expect(wf).toHaveValue('CI')
  expect(world.writes.at(-1)?.body).toEqual({ deadlines: { ci_green: 30 } })
  world.fail = true; await deadline.fill('45'); await deadline.press('Enter')
  await expect(page.locator('#checks .feedback')).toContainText('Delivery settings changed'); await expect(deadline).toHaveValue('45')
  expect(world.overrides.deadlines?.ci_green).toBe(30); world.fail = false
  await deadline.press('Enter'); await expect(deadline).toBeEnabled(); expect(world.overrides.deadlines?.ci_green).toBe(45)

  // A background revalidation cannot attach a newer revision to an older draft.
  await wf.fill('Retained workflow draft')
  const draftRevision = `"${world.revision}"`
  world.overrides.required_workflow = 'Another writer’s workflow'; world.revision++
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(page.locator('#checks .feedback')).toContainText('Reload saved settings')
  await wf.press('Enter'); await expect(wf).toBeEnabled()
  expect(world.writes.at(-1)?.revision).toBe(draftRevision)
  expect(world.overrides.required_workflow).toBe('Another writer’s workflow')
  await page.getByRole('button', { name: 'Reload saved settings; keep this draft', exact: true }).click()
  await expect(page.locator('#checks .feedback')).toHaveText('Saved values reloaded; the draft is kept.')
  await expect(wf).toHaveValue('Retained workflow draft')
  await wf.press('Enter'); await expect(wf).toBeEnabled(); expect(world.overrides.required_workflow).toBe('Retained workflow draft')

  // The response is released after navigation; it must not populate PHAROS.
  let entered!: () => void, release!: () => void
  const reached = new Promise<void>(resolve => { entered = resolve })
  world.held = { entered, until: new Promise<void>(resolve => { release = resolve }) }
  await wf.fill('Late AEON workflow'); await wf.press('Enter'); await reached
  await navigate(page, '/p/PHAROS/settings/delivery'); await expect(page.locator('#project-title')).toHaveText('Pharos')
  release(); await expect(page.locator('#f-wf')).toHaveValue('CI')
  expect(world.writes.at(-1)?.id).toBe('p-aeon'); expect(errors).toEqual([])
})

// Keep every state, with a separate context and timeout for each evidence cell.
for (const width of [1440, 1024, 400, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`AEON-1226: approved repository reference states and check.mjs audit against the build at ${width}px ${theme}`, async ({ browser }, testInfo) => {
    test.setTimeout(240_000)
    const results: unknown[] = [], failures: string[] = []
    const context = await browser.newContext({ viewport: { width, height: width > 600 ? 1000 : 860 }, hasTouch: width < 600, reducedMotion: 'reduce', colorScheme: theme })
    const page = await context.newPage(), { world, errors } = await setup(page)
    const touchSession = width < 600 ? await context.newCDPSession(page) : undefined
    await page.route('**/api/preferences/theme', route => route.fulfill({ json: { key: 'theme', value: { choice: theme } } }))
    for (const readOnly of [false, true]) for (const state of states) {
      let release!: () => void
      if (state === 'loading') world.pendingLoad = { entered: () => {}, until: new Promise<void>(resolve => { release = resolve }) }
      await choose(page, world, state, readOnly)
      if (state === 'reading') await expect(page.locator('.phases')).toHaveCount(0)
      const name = `delivery-${state}${readOnly ? '-readonly' : ''}-${width}-${theme}`
      results.push(...await checkProjectSettings(page, name, width < 600))
      await page.locator('#repository').scrollIntoViewIfNeeded()
      await captureProjectSettings(page, { path: testInfo.outputPath(`${name}.png`), fullPage: true }, touchSession)
      if (state === 'loading') { world.pendingLoad = null; release(); await expect(page.locator('.repo-slot')).not.toHaveAttribute('data-state', 'loading') }
    }
    await choose(page, world, 'none')
    await page.getByRole('button', { name: 'Link repository', exact: true }).click()
    await captureProjectSettings(page, { path: testInfo.outputPath(`delivery-linking-${width}-${theme}.png`), fullPage: true }, touchSession)
    results.push(...await checkProjectSettings(page, `delivery-linking-${width}-${theme}`, width < 600))
    await choose(page, world, 'noactions'); const how = page.locator('[data-how=actions]')
    const stable = await controlStability(page, { slot: page.locator('.repo-slot'), checks: page.locator('#checks') })
    await stable.check(async () => { await how.click(); await expect(page.getByRole('heading', { name: 'The Actions read permission' })).toBeFocused() })
    await captureProjectSettings(page, { path: testInfo.outputPath(`how-actions-${width}-${theme}.png`), fullPage: true }, touchSession)
    results.push(...await checkProjectSettings(page, `how-actions-${width}-${theme}`, width < 600))
    await page.keyboard.press('Escape'); await expect(how).toBeFocused(); stable.done()

    // A real backfill response may name the active phase; its other phases stay neutral.
    world.actionsReadable = true
    const progress = await controlStability(page, { change: page.locator('[data-act=change]'), more: page.locator('[data-act=repo-more]'), slot: page.locator('.repo-slot'), checks: page.locator('#checks') })
    await progress.check(async () => { await page.locator('[data-act=check]').click(); await expect(page.locator('.repo-slot')).toHaveAttribute('data-state', 'reading') })
    progress.done()
    await expect(page.locator('.phases .now')).toHaveText('Pull requests')
    await expect(page.locator('.phases svg path')).toHaveCount(0)
    // Audit the real progress before capture; the capture helper restores touch
    // emulation for subsequent interactions as well as the next navigation.
    results.push(...await checkProjectSettings(page, `delivery-reading-progress-${width}-${theme}`, width < 600))
    await captureProjectSettings(page, { path: testInfo.outputPath(`delivery-reading-progress-${width}-${theme}.png`), fullPage: true }, touchSession)

    world.state = 'none'; world.source = null
    await page.goto('/p/AEON/delivery'); await expect(page.getByRole('link', { name: 'Link repository', exact: true })).toBeVisible()
    await expect(page.locator('.dl-status .banner')).toContainText('history is then read once from the GitHub API')
    results.push(await bannerContainsAction(page, `delivery-banner-none-${width}-${theme}`))
    const windows = page.locator('.dl-head').getByRole('radiogroup', { name: 'Chart window' }), action = page.getByRole('link', { name: 'Link repository', exact: true })
    const banner = await controlStability(page, { action, windows, levels: page.locator('.dl-head').getByRole('radiogroup', { name: 'Level of detail' }), firstLearn: page.locator('.s-tile .learn').first() })
    await banner.check(() => action.hover())
    await banner.check(async () => { await windows.getByRole('radio', { name: '90 days' }).click(); await expect(action).toBeVisible() })
    await banner.check(async () => { await windows.getByRole('radio', { name: '7 days' }).click(); await expect(action).toBeVisible() })
    banner.done()
    const refresh = await controlStability(page, { windows, status: page.locator('.dl-status'), firstLearn: page.locator('.s-tile .learn').first() })
    await refresh.check(async () => { world.state = 'error'; await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange'))); await expect(page.getByTestId('delivery-load-error')).toBeVisible() })
    await refresh.check(async () => { world.state = 'none'; await page.getByRole('button', { name: 'Retry', exact: true }).click(); await expect(action).toBeVisible() })
    refresh.done()
    await captureProjectSettings(page, { path: testInfo.outputPath(`delivery-banner-none-${width}-${theme}.png`), fullPage: true }, touchSession)

    // The app remains English; German is test-only length stress data here.
    await action.evaluate(element => {
      for (const child of element.childNodes) if (child.nodeType === Node.TEXT_NODE && child.textContent?.trim()) child.textContent = 'Repository verknüpfen'
      element.closest('.banner')!.querySelector('.grow')!.textContent = 'Noch kein Repository verknüpft. Eine Person mit Lieferverwaltung verknüpft das GitHub-Repository dieses Projekts; die Historie wird anschließend einmal über die GitHub-API gelesen.'
    })
    results.push(await bannerContainsAction(page, `delivery-banner-none-long-de-${width}-${theme}`))
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
    await captureProjectSettings(page, { path: testInfo.outputPath(`delivery-banner-none-long-de-${width}-${theme}.png`), fullPage: true }, touchSession)
    await page.goto('/p/AEON/delivery'); await expect(action).toBeVisible()
    await page.getByRole('link', { name: 'Link repository', exact: true }).click(); await expect(page.locator('#link-form')).toBeVisible()
    world.state = 'appoff'; world.source = source('appoff'); world.metrics = deliveryMetrics().metrics
    await page.goto('/p/AEON/delivery'); await expect(page.locator('.go-src').first()).toBeVisible()
    await captureProjectSettings(page, { path: testInfo.outputPath(`delivery-problem-chips-${width}-${theme}.png`), fullPage: true }, touchSession)
    await page.locator('.go-src').first().click(); await expect(page).toHaveURL('/p/AEON/settings/delivery#repository')
    world.state = 'none'; world.source = null; world.metrics = []
    await page.goto('/p/AEON/AEON-1'); await expect(page.locator('.delivery .settings-link')).toBeVisible()
    await captureProjectSettings(page, { path: testInfo.outputPath(`ticket-delivery-none-${width}-${theme}.png`), fullPage: true }, touchSession)
    await page.locator('.delivery .settings-link').click(); await expect(page).toHaveURL('/p/AEON/settings/delivery#repository')
    await page.goto('/'); await page.getByRole('button', { name: /^Actions for AEON / }).click()
    await expect(page.getByRole('menuitem', { name: 'Project settings', exact: true })).toBeVisible()
    await captureProjectSettings(page, { path: testInfo.outputPath(`projects-row-menu-${width}-${theme}.png`), fullPage: true }, touchSession)
    await page.getByRole('menuitem', { name: 'Project settings', exact: true }).click(); await expect(page).toHaveURL('/p/AEON/settings')
    world.leadReason = true
    await page.goto('/p/AEON/tickets'); await expect(page.locator('#lead-launch-reason-band .settings-link')).toBeVisible()
    await expect(page.locator('#lead-launch-reason-band [data-launch-reason]')).toHaveText('Automatic launch is not enabled for this workspace yet.')
    await expect(page.locator('#lead-launch-reason-band .settings-link')).toHaveText('Settings Agents')
    await expect(page.locator('#lead-launch-reason-band .settings-link svg')).toHaveCount(2)
    await captureProjectSettings(page, { path: testInfo.outputPath(`lead-launch-reason-${width}-${theme}.png`), fullPage: true }, touchSession)
    await page.locator('#lead-launch-reason-band .settings-link').click(); await expect(page).toHaveURL('/p/AEON/settings/agents#lead')
    failures.push(...errors); await context.close()
    const head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
    writeFileSync(testInfo.outputPath('check-result.txt'), JSON.stringify({ head, approvedCheckSHA, results, failures, limitations: ['The approved check.mjs audits a file mock. Its unchanged audit function is adapted to the P1/P3 build selectors; P4–P6 flows remain outside P3.', 'P2 GET metrics/source contains no cursor phase or running total. Progress details are rendered only from an actual backfill response.', 'The GitHub API does not identify individual organisation owners. The fix line names the repository owner, without guessing a person.'] }, null, 2))
    writeFileSync(testInfo.outputPath('check-result.md'), `AEON-1226 · HEAD ${head}\n\nApproved check.mjs SHA-256: ${approvedCheckSHA}\n\n${results.length} audit checks; ${results.filter(result => !(result as { passed: boolean }).passed).length} failures. Browser errors: ${failures.length}.\n\nProgress details use real backfill responses; the source API has no cursor or running total. The owner line names the GitHub repository owner and does not invent a person.\n\n${JSON.stringify(results, null, 2)}\n`)
    expect(results.filter(result => !(result as { passed: boolean }).passed)).toEqual([])
    expect(failures).toEqual([])
  })
}
