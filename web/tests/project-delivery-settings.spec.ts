// SPDX-License-Identifier: AGPL-3.0-only
// Risks: moving repository controls, false successful writes, overwriting inherited
// settings, and late responses modifying a different project/person.
import { expect, test, type Page } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { controlStability } from './control-stability'
import { checkProjectSettings, approvedCheckSHA } from './project-settings-acceptance.mjs'
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
  const world = { state: 'none' as State, readOnly: false, fail: false, source: null as MetricSource | null, overrides: { deadlines: { ci_green: 30 } } as DeliverySettings, revision: 1, writes: [] as { id: string; body: DeliverySettings; revision: string }[], links: [] as Record<string, unknown>[], history: [] as Record<string, unknown>[], held: null as null | { entered: () => void; until: Promise<void> }, pendingLoad: null as null | { entered: () => void; until: Promise<void> } }
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
    return route.fulfill({ json: { project_id: new URL(route.request().url()).pathname.split('/')[3], generated_at: NOW, source: shown, metrics: [] } })
  })
  await page.route('**/api/projects/*/delivery/metrics/source', route => {
    const body = route.request().postDataJSON(); world.links.push(body)
    if (world.readOnly || world.fail) return route.fulfill({ status: world.readOnly ? 403 : 503, json: { error: 'The repository was not saved.' } })
    world.state = 'noactions'; world.source = { ...source('noactions')!, ...body }
    return route.fulfill({ json: world.source })
  })
  await page.route('**/api/projects/*/delivery/metrics/backfill', route => {
    world.history.push(route.request().postDataJSON())
    if (world.state === 'noactions') return route.fulfill({ status: 502, json: { error: 'GitHub could not be read; the server retries.' } })
    return route.fulfill({ json: { state: 'running', since: '2026-09-12T00:00:00Z', phase: 'pulls', day: '2026-09-30', calls: 4, runs: 412, pulls: 96, jobs: 0, truncated: false } })
  })
  await page.route('**/api/projects/*/delivery-settings**', async route => {
    const url = new URL(route.request().url()), method = route.request().method()
    if (method === 'PUT') {
      const body = route.request().postDataJSON(), revision = route.request().headers()['if-match']
      world.writes.push({ id: url.pathname.split('/')[3], body, revision })
      if (world.held) { const hold = world.held; world.held = null; hold.entered(); await hold.until }
      if (world.readOnly) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
      if (world.fail || revision !== `"${world.revision}"`) return route.fulfill({ status: 412, json: { error: 'Delivery settings changed; reload before saving.' } })
      world.overrides = structuredClone(body); world.revision++
    }
    const effective = { ...defaults, ...world.overrides, deadlines: { ...defaults.deadlines, ...world.overrides.deadlines } }
    return route.fulfill({ json: url.searchParams.get('view') === 'overrides' ? world.overrides : effective, headers: { ETag: `"${world.revision}"` } })
  })
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
}

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
  const linked = await controlStability(page, { change: page.locator('[data-act=change]'), check: page.locator('[data-act=check]'), checks: page.locator('#checks') })
  await linked.check(async () => { await page.locator('[data-act=check]').click(); await expect(page.locator('[data-act=check]')).toBeEnabled() })
  linked.done(); expect(world.history.at(-1)).toEqual({ days: 60, restart: false })
  await page.locator('[data-act=repo-more]').click(); await page.getByRole('menuitem', { name: /Read history again/ }).click()
  await expect.poll(() => world.history.at(-1)).toEqual({ days: 60, restart: true })

  const wf = page.locator('#f-wf'), deadline = page.locator('#dl-ci_green')
  await wf.fill('Release CI'); await wf.press('Enter'); await expect(wf).toBeEnabled()
  expect(world.writes.at(-1)?.body).toEqual({ required_workflow: 'Release CI', deadlines: { ci_green: 30 } })
  await page.locator('#checks .frow').first().getByRole('button', { name: 'Reset', exact: true }).click(); await expect(wf).toHaveValue('CI')
  expect(world.writes.at(-1)?.body).toEqual({ deadlines: { ci_green: 30 } })
  world.fail = true; await deadline.fill('45'); await deadline.press('Enter')
  await expect(page.locator('#checks .feedback')).toContainText('Delivery settings changed'); await expect(deadline).toHaveValue('45')
  expect(world.overrides.deadlines?.ci_green).toBe(30); world.fail = false
  await deadline.press('Enter'); await expect(deadline).toBeEnabled(); expect(world.overrides.deadlines?.ci_green).toBe(45)

  // The response is released after navigation; it must not populate PHAROS.
  let entered!: () => void, release!: () => void
  const reached = new Promise<void>(resolve => { entered = resolve })
  world.held = { entered, until: new Promise<void>(resolve => { release = resolve }) }
  await wf.fill('Late AEON workflow'); await wf.press('Enter'); await reached
  await navigate(page, '/p/PHAROS/settings/delivery'); await expect(page.locator('#project-title')).toHaveText('Pharos')
  release(); await expect(page.locator('#f-wf')).toHaveValue('CI')
  expect(world.writes.at(-1)?.id).toBe('p-aeon'); expect(errors).toEqual([])
})

test('AEON-1226: approved repository reference states and check.mjs audit against the build', async ({ browser }, testInfo) => {
  test.setTimeout(240_000)
  const results: unknown[] = [], failures: string[] = []
  for (const width of [1440, 1024, 400, 390]) for (const theme of ['light', 'dark'] as const) {
    const context = await browser.newContext({ viewport: { width, height: width > 600 ? 1000 : 860 }, hasTouch: width < 600, reducedMotion: 'reduce', colorScheme: theme })
    const page = await context.newPage(), { world, errors } = await setup(page)
    await page.route('**/api/preferences/theme', route => route.fulfill({ json: { key: 'theme', value: { choice: theme } } }))
    for (const readOnly of [false, true]) for (const state of states) {
      let release!: () => void
      if (state === 'loading') world.pendingLoad = { entered: () => {}, until: new Promise<void>(resolve => { release = resolve }) }
      await choose(page, world, state, readOnly)
      const name = `delivery-${state}${readOnly ? '-readonly' : ''}-${width}-${theme}`
      results.push(...await checkProjectSettings(page, name, width < 600))
      await page.screenshot({ path: testInfo.outputPath(`${name}.png`), fullPage: true })
      if (state === 'loading') { world.pendingLoad = null; release(); await expect(page.locator('.repo-slot')).not.toHaveAttribute('data-state', 'loading') }
    }
    await choose(page, world, 'none')
    await page.getByRole('button', { name: 'Link repository', exact: true }).click()
    await page.screenshot({ path: testInfo.outputPath(`delivery-linking-${width}-${theme}.png`), fullPage: true })
    results.push(...await checkProjectSettings(page, `delivery-linking-${width}-${theme}`, width < 600))
    await choose(page, world, 'noactions'); const how = page.locator('[data-how=actions]')
    const stable = await controlStability(page, { slot: page.locator('.repo-slot'), checks: page.locator('#checks') })
    await stable.check(async () => { await how.click(); await expect(page.getByRole('heading', { name: 'The Actions read permission' })).toBeFocused() })
    await page.screenshot({ path: testInfo.outputPath(`how-actions-${width}-${theme}.png`), fullPage: true })
    results.push(...await checkProjectSettings(page, `how-actions-${width}-${theme}`, width < 600))
    await page.keyboard.press('Escape'); await expect(how).toBeFocused(); stable.done()
    failures.push(...errors); await context.close()
  }
  const head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
  writeFileSync(testInfo.outputPath('check-result.txt'), JSON.stringify({ head, approvedCheckSHA, results, failures, limitations: ['The approved check.mjs audits a file mock. Its unchanged audit function is adapted to the P1/P3 build selectors; P4–P6 flows remain outside P3.', 'P2 GET metrics/source contains no cursor phase or running total. Progress details are rendered only from an actual backfill response.', 'The GitHub API does not identify individual organisation owners. The fix line names the repository owner, without guessing a person.'] }, null, 2))
  expect(results.filter(result => !(result as { passed: boolean }).passed)).toEqual([])
  expect(failures).toEqual([])
})
