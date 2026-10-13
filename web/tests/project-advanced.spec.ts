// SPDX-License-Identifier: AGPL-3.0-only
// Risks: revisioned admin controls and repair previews must stay on the record
// acted on; failures cannot claim success, and project creation must retain a
// key-conflict draft or report partial group assignment without creating twice.
// Admission must use its strict write contract; pre-creation list snapshots
// must not remove a project whose creation has since been confirmed.
import { test, expect, type Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { controlStability } from './control-stability'
import { checkProjectSettings, captureProjectSettings, approvedCheckSHA } from './project-settings-acceptance.mjs'
import { MACHINE_PATHS } from '../src/lib/projectAdvanced'
import { fillProjectSettingsExplainer, PROJECT_SETTINGS_EXPLAINERS } from '../src/lib/projectSettingsExplainers'

const release = '10000000-0000-4000-8000-000000000001'
async function setup(page: Page, options: { admin?: boolean; principalKind?: 'person' | 'agent' } = {}) {
  const data = fixtures(), errors = watchErrors(page)
  const admin = options.admin !== false
  await mockWork(page, data, { admin, principalKind: options.principalKind ?? 'person' })
  const grants = ['project_groups.read', 'project_groups.write', 'delivery.read', 'delivery.manage', 'delivery_queue.read', 'delivery_queue.manage', 'delivery_reviews.read', 'delivery_reviews.manage', 'delivery_ship.read', 'delivery_ship.manage', 'engine.read', 'engine.manage', 'releases.read']
  const control = { conflict: false, failGroup: false, failReviews: false, staleRepair: false, deny: '', held: null as null | { path: string; entered: () => void; until: Promise<void> } }
  const writes: { project: string; path: string; body: Record<string, unknown> }[] = [], creates: Record<string, unknown>[] = [], assignments: Record<string, unknown>[] = []
  const states = new Map<string, Record<string, unknown>>()
  const nodes = new Map(data.projects.map(project => [project.id, { id: project.id, key: project.key, title: project.title, body: project.description, state: project.state, fields: { project_key: project.classic || project.key }, updated_at: project.last, created_at: project.last, parent_id: null, kind_id: 'k-project', position: '0' }]))
  const groups = [{ id: 'g-platform', name: 'Platform', project_ids: ['p-pharos'] }, { id: 'g-product', name: 'Product', project_ids: ['p-aeon'] }]
  await page.route('**/api/me/permissions**', route => {
    const project = new URL(route.request().url()).searchParams.get('project_id') ?? undefined
    const answer = mockEffectivePermissions(admin ? 'admin' : 'member', project)
    if (admin) answer.workspace.permissions.push(...grants.filter(permission => permission !== control.deny))
    return route.fulfill({ json: answer })
  })
  await page.route('**/api/nodes/p-*', route => { const node = nodes.get(new URL(route.request().url()).pathname.split('/').at(-1)!); return node ? route.fulfill({ json: node }) : route.fallback() })
  await page.route('**/api/nodes', route => {
    if (route.request().method() !== 'POST') return route.fallback()
    const body = route.request().postDataJSON(); creates.push(body)
    if (control.conflict) return route.fulfill({ status: 409, json: { error: `${body.fields.project_key} is Website management’s key.` } })
    const id = `p-created-${creates.length}`, at = '2026-10-12T09:00:00Z'
    const node = { ...body, id, key: `${body.key_prefix}-1`, updated_at: at, created_at: at, parent_id: null, position: '0' }
    nodes.set(id, node); data.projects.push({ id, key: node.key, title: body.title, state: 'active', classic: body.fields.project_key, description: '', last: at })
    return route.fulfill({ status: 201, json: node })
  })
  await page.route('**/api/project-groups**', route => {
    if (route.request().method() === 'POST') {
      const body = route.request().postDataJSON(); assignments.push(body)
      if (control.failGroup) return route.fulfill({ status: 403, json: { error: 'Group access changed' } })
      for (const group of groups) group.project_ids = [...group.project_ids.filter(id => !body.project_ids.includes(id)), ...(body.group_id === group.id ? body.project_ids : [])]
      return route.fulfill({ json: { assignments: body.project_ids.map((project_id: string) => ({ project_id, group_id: body.group_id })), event_id: 44 } })
    }
    return route.fulfill({ json: { items: groups } })
  })
  const merged = (applied = false) => ({ preview_event_id: 75, applied, not_merged: [519], items: [
    { ticket_node_id: 'n-5', key: 'PHAROS-9', project_id: 'p-pharos', pull_request: 512, revision: '2026-10-11T09:00:00Z', from: 'done', to: 'delivered', result: applied ? 'applied' : 'ready' },
    { ticket_node_id: 'n-6', key: 'PHAROS-15', project_id: 'p-pharos', pull_request: 514, revision: '2026-10-11T09:00:00Z', from: 'in_progress', to: 'delivered', result: 'refused', reason: 'open review findings' },
  ] })
  await page.route('**/api/projects/*/**', async route => {
    const request = route.request(), match = /^\/api\/projects\/([^/]+)\/(.*)$/.exec(new URL(request.url()).pathname)
    if (!match) return route.fallback()
    const [, project, path] = match
    if (path === 'releases') return route.fulfill({ json: { releases: [{ id: release, title: 'Guided projects', number: 130, state: 'released' }], truncated: false } })
    if (path === 'delivery/merge-backfill') {
      const body = request.postDataJSON(); writes.push({ project, path, body })
      if (control.held?.path === path) { const hold = control.held; control.held = null; hold.entered(); await hold.until }
      if (body.apply && control.staleRepair) return route.fulfill({ status: 409, json: { error: 'preview is stale; run dry-run again' } })
      return route.fulfill({ json: merged(body.apply === true) })
    }
    if (!Object.values(MACHINE_PATHS).includes(path)) return route.fallback()
    const settingsKey = `${project}/${path}`
    if (!states.has(settingsKey)) states.set(settingsKey, path === MACHINE_PATHS.queue ? { mode: 'shadow', revision: 4, freeze: false, release_set: ['pharos-ready'], held_slugs: ['pharos-held'], held_pull_requests: [511] } : path === MACHINE_PATHS.admission ? { project_id: project, shadow_enabled: false, revision: 2 } : path === MACHINE_PATHS.routing ? { mode: 'off' } : { mode: 'off', revision: 3 })
    if (request.method() === 'PUT') {
      const body = request.postDataJSON(); writes.push({ project, path, body })
      if (control.held?.path === path) { const hold = control.held; control.held = null; hold.entered(); await hold.until }
      if (path === MACHINE_PATHS.reviews && control.failReviews) return route.fulfill({ status: 409, json: { error: 'revision changed' } })
      const previous = states.get(settingsKey)!
      if (path === MACHINE_PATHS.admission) {
        if (Object.keys(body).sort().join(',') !== 'expected_revision,shadow_enabled' || !Number.isSafeInteger(body.expected_revision) || Number(body.expected_revision) < 0 || typeof body.shadow_enabled !== 'boolean') return route.fulfill({ status: 400, json: { error: 'Invalid admission settings request' } })
        if (body.expected_revision !== previous.revision) return route.fulfill({ status: 409, json: { error: 'revision changed' } })
        states.set(settingsKey, { project_id: project, shadow_enabled: body.shadow_enabled, revision: Number(previous.revision) + 1 })
      } else {
        if (path !== MACHINE_PATHS.routing && body.revision !== previous.revision) return route.fulfill({ status: 409, json: { error: 'revision changed' } })
        states.set(settingsKey, { ...body, ...(path === MACHINE_PATHS.routing ? {} : { revision: Number(previous.revision) + 1 }) })
      }
    }
    return route.fulfill({ json: states.get(settingsKey) })
  })
  return { data, nodes, groups, writes, creates, assignments, control, errors }
}
async function navigate(page: Page, path: string) {
  await page.evaluate(path => (document.querySelector('#app') as unknown as { __vue_app__: { config: { globalProperties: { $router: { push(path: string): Promise<void> } } } } }).__vue_app__.config.globalProperties.$router.push(path), path)
}
const advanced = (page: Page) => page.locator('#advanced')
const modeButton = (page: Page, machine: string, mode: string) => page.locator(`[data-adv="${machine}:${mode}"]`)

test('AEON-1235: New project retains key conflicts and reports partial group assignment once', async ({ page }) => {
  const world = await setup(page)
  await page.goto('/'); await page.locator('#new-proj').click()
  const sheet = page.getByRole('dialog', { name: 'New project', exact: true }), name = sheet.locator('#np-name'), key = sheet.locator('#np-key'), group = sheet.locator('#np-group'), create = sheet.locator('#np-go')
  await name.focus(); await page.keyboard.press('Escape'); await expect(sheet).toBeVisible(); await expect(name).not.toBeFocused()
  await page.keyboard.press('Escape'); await expect(sheet).toHaveCount(0); await expect(page.locator('#new-proj')).toBeFocused()
  await page.locator('#new-proj').click()
  await expect(group).toBeEnabled()
  const stable = await controlStability(page, { sheet, name, key, group, create, cancel: sheet.getByRole('button', { name: /^Cancel/ }) })
  await stable.check(() => name.fill('Website relaunch')); await expect(key).toHaveValue('WEB')
  await stable.check(() => key.fill('PORTAL')); await stable.check(() => name.fill('Website relaunch with a long German project name'))
  await expect(key).toHaveValue('PORTAL')
  world.control.conflict = true
  await stable.check(async () => { await create.click(); await expect(sheet.getByRole('alert')).toHaveText('PORTAL is Website management’s key.') })
  await expect(name).toHaveValue('Website relaunch with a long German project name'); await expect(create).toBeDisabled()
  world.control.conflict = false; world.control.failGroup = true
  await stable.check(() => key.fill('WEB')); await stable.check(() => group.selectOption('g-platform')); stable.done()
  // Both list snapshots precede creation. Hold the summaries response until
  // the confirmed project is already displayed in General.
  const staleSummary = await page.evaluate(() => {
    const app = (document.querySelector('#app') as unknown as { __vue_app__: { config: { globalProperties: { $pinia: { _s: Map<string, { projects: unknown[] }> } } } } }).__vue_app__
    return app.config.globalProperties.$pinia._s.get('projects')!.projects
  }), staleNodes = structuredClone([...world.nodes.values()])
  let refreshEntered!: () => void, releaseRefresh!: () => void
  const reading = new Promise<void>(resolve => { refreshEntered = resolve }), reply = new Promise<void>(resolve => { releaseRefresh = resolve })
  const staleSummaryReader = async (route: import('@playwright/test').Route) => {
    refreshEntered(); await reply
    await route.fulfill({ json: { items: staleSummary } })
  }
  const staleNodeReader = async (route: import('@playwright/test').Route) => {
    if (new URL(route.request().url()).searchParams.get('kind') !== 'project') return route.fallback()
    await route.fulfill({ json: { items: staleNodes, next_cursor: null } })
  }
  await page.route('**/api/projects?**', staleSummaryReader)
  await page.route('**/api/nodes?**', staleNodeReader)
  await page.evaluate(() => {
    const app = (document.querySelector('#app') as unknown as { __vue_app__: { config: { globalProperties: { $pinia: { _s: Map<string, { load(force: boolean): Promise<void> }> } } } } }).__vue_app__
    Reflect.set(window, '__projectCreationRefresh', app.config.globalProperties.$pinia._s.get('projects')!.load(true))
  })
  await reading
  await create.click()
  await expect(page).toHaveURL(/\/p\/WEB\/settings\?new=p-created-2&group_assignment_failed=1$/)
  await expect(page.getByRole('region', { name: 'Next step' })).toContainText('The selected group was not assigned.')
  expect(world.creates).toHaveLength(2); expect(world.creates[1]).toMatchObject({ kind_id: 'k-project', key_prefix: 'WEB', fields: { project_key: 'WEB' } })
  expect(world.assignments).toEqual([{ group_id: 'g-platform', project_ids: ['p-created-2'] }])
  releaseRefresh(); await page.evaluate(() => Reflect.get(window, '__projectCreationRefresh'))
  await expect(page.locator('#project-title')).toHaveText('Website relaunch with a long German project name')
  await expect(page.getByLabel('Name', { exact: true })).toHaveValue('Website relaunch with a long German project name')
  await page.unroute('**/api/projects?**', staleSummaryReader)
  await page.unroute('**/api/nodes?**', staleNodeReader)
  await page.evaluate(async () => {
    const app = (document.querySelector('#app') as unknown as { __vue_app__: { config: { globalProperties: { $pinia: { _s: Map<string, { load(force: boolean): Promise<void> }> } } } } }).__vue_app__
    await app.config.globalProperties.$pinia._s.get('projects')!.load(true)
  })
  await expect(page.locator('#project-title')).toHaveText('Website relaunch with a long German project name')
  await page.getByRole('link', { name: 'Next: link a repository' }).click(); await expect(page).toHaveURL('/p/WEB/settings/delivery?link=1#repository')
  await navigate(page, '/p/PHAROS/settings'); await expect(page.getByRole('region', { name: 'Next step' })).toHaveCount(0)
  expect(world.errors).toEqual([])
})

test('AEON-1235: Advanced writes preserve revisions, bounds and honest repair refusals', async ({ page }) => {
  const world = await setup(page)
  await page.goto('/p/PHAROS/settings/delivery#advanced')
  // Risk: merging P3 and P4 leaves the temporary repository stub beside P3’s editor.
  const repositoryCards = page.locator('.settings-body .m-card').filter({ has: page.getByRole('heading', { name: /^Repository(?: & delivery)?$/ }) })
  await expect(repositoryCards.locator('.repo-slot')).toBeVisible()
  await expect(repositoryCards).toHaveCount(1)
  const review = modeButton(page, 'reviews', 'shadow')
  await expect(review).toBeEnabled()
  const stable = await controlStability(page, { queueOff: modeButton(page, 'queue', 'off'), queueShadow: modeButton(page, 'queue', 'shadow'), review, shipping: modeButton(page, 'shipping', 'off'), routing: modeButton(page, 'routing', 'off'), admission: page.getByRole('switch', { name: 'Record shadow admission' }) })
  const admission = page.getByRole('switch', { name: 'Record shadow admission' })
  await expect(admission).toBeEnabled()
  await stable.check(async () => { await admission.click(); await expect(admission).toBeChecked(); await expect(admission).toBeEnabled() })
  expect(world.writes.at(-1)).toEqual({ project: 'p-pharos', path: MACHINE_PATHS.admission, body: { expected_revision: 2, shadow_enabled: true } })
  await expect(page.locator('.toast').filter({ hasText: 'Admission saved. Recorded in history.' })).toBeVisible()
  await stable.check(async () => { await admission.click(); await expect(admission).not.toBeChecked(); await expect(admission).toBeEnabled() })
  expect(world.writes.at(-1)).toEqual({ project: 'p-pharos', path: MACHINE_PATHS.admission, body: { expected_revision: 3, shadow_enabled: false } })
  await stable.check(async () => { await review.click(); await expect(review).toHaveAttribute('aria-pressed', 'true') })
  expect(world.writes.at(-1)).toMatchObject({ project: 'p-pharos', body: { mode: 'shadow', revision: 3 } })
  world.control.failReviews = true
  await stable.check(async () => { await modeButton(page, 'reviews', 'off').click(); await expect(advanced(page).getByRole('alert')).toContainText('This changed.') })
  await expect(review).toHaveAttribute('aria-pressed', 'true'); world.control.failReviews = false
  await stable.check(async () => { await modeButton(page, 'queue', 'off').click(); await expect(modeButton(page, 'queue', 'off')).toHaveAttribute('aria-pressed', 'true') })
  await stable.check(async () => { await modeButton(page, 'queue', 'shadow').click(); await expect(modeButton(page, 'queue', 'shadow')).toHaveAttribute('aria-pressed', 'true') }); stable.done()
  await page.getByRole('switch', { name: 'Freeze the queue' }).click()
  await expect(page.getByRole('switch', { name: 'Freeze the queue' })).toBeChecked()
  await expect.poll(() => world.writes.at(-1)?.body.freeze).toBe(true)
  expect(world.writes.at(-1)?.body).toMatchObject({ release_set: ['pharos-ready'], held_slugs: ['pharos-held'], held_pull_requests: [511], revision: 6 })
  await advanced(page).getByRole('button', { name: 'Edit…', exact: true }).click()
  const queueSheet = page.getByRole('dialog', { name: 'Delivery queue', exact: true }), save = queueSheet.getByRole('button', { name: /^Save queue/ })
  await expect(queueSheet).toBeVisible().catch(async error => { throw new Error(`${error}\n${await page.locator('.details').textContent().catch(() => world.errors.join('\n'))}`) })
  const queueStable = await controlStability(page, { queueSheet, save, release: queueSheet.getByLabel('Release set'), holds: queueSheet.getByLabel('Held branch slugs'), pulls: queueSheet.getByLabel('Held pull request numbers') })
  const writesBefore = world.writes.length
  await queueStable.check(() => queueSheet.getByLabel('Held pull request numbers').fill('1,1'))
  await queueStable.check(async () => { await save.click(); await expect(queueSheet.getByRole('alert')).toContainText('unique pull request numbers') })
  expect(world.writes.length).toBe(writesBefore)
  await queueStable.check(() => queueSheet.getByLabel('Held pull request numbers').fill('#514, 520')); queueStable.done()
  const mod = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent) ? 'Meta' : 'Control')
  await queueSheet.getByLabel('Held pull request numbers').press(`${mod}+Enter`); await expect(queueSheet).toHaveCount(0)
  await expect(advanced(page).getByRole('button', { name: 'Edit…', exact: true })).toBeFocused()
  expect(world.writes.at(-1)?.body).toMatchObject({ freeze: true, mode: 'shadow', held_pull_requests: [514, 520], revision: 7 })
  await advanced(page).getByRole('button', { name: 'Preview…', exact: true }).click()
  const form = page.locator('.mbf'), input = form.getByLabel('Pull request numbers'), published = form.getByLabel('Published release'), preview = form.getByRole('button', { name: 'Preview', exact: true }), apply = form.getByRole('button', { name: 'Apply preview', exact: true })
  await published.selectOption(release)
  const repairStable = await controlStability(page, { input, published, preview, apply, cancel: form.getByRole('button', { name: 'Cancel', exact: true }) })
  await repairStable.check(() => input.fill('1,1')); await repairStable.check(async () => { await preview.click(); await expect(form.getByRole('alert')).toContainText('unique pull request numbers') })
  await expect(apply).toBeDisabled()
  await repairStable.check(() => input.fill('#512, 514, 519, 520')); await repairStable.check(async () => { await preview.click(); await expect(form.locator('.mbf-prev')).toContainText('PHAROS-15 · #514 · refused: open review findings') })
  await expect(form.locator('.mbf-prev')).toContainText('#519 · not merged'); await expect(form.locator('.mbf-prev')).toContainText('#520 · no matching ticket')
  await repairStable.check(() => input.fill('512, 514, 519, 520 ')); await expect(apply).toBeDisabled()
  await repairStable.check(async () => { await preview.click(); await expect(apply).toBeEnabled() })
  world.control.staleRepair = true
  await repairStable.check(async () => { await apply.click(); await expect(form.getByRole('alert')).toContainText('Read it again') }); await expect(apply).toBeDisabled()
  world.control.staleRepair = false
  await repairStable.check(async () => { await preview.click(); await expect(apply).toBeEnabled() })
  await repairStable.check(async () => { await apply.click(); await expect(form.locator('.mbf-prev')).toContainText('PHAROS-9 · #512 · marked Delivered') }); repairStable.done()
  expect(world.writes.at(-1)?.body).toEqual({ pull_requests: [512, 514, 519, 520], target: 'delivered', release_id: release, apply: true, preview_event_id: 75 })
  await expect(page.locator('.toast').filter({ hasText: '1 ticket marked Delivered. 1 refused' })).toBeVisible()
  await expect(form.locator('.mbf-prev')).toContainText('refused: open review findings')
  expect(world.errors).toEqual([])
})

test('AEON-1235: stale project responses and missing authority cannot enable admin writes', async ({ page }) => {
  const world = await setup(page)
  let entered!: () => void, releaseHeld!: () => void
  const arrived = new Promise<void>(resolve => { entered = resolve }), until = new Promise<void>(resolve => { releaseHeld = resolve })
  world.control.held = { path: MACHINE_PATHS.reviews, entered, until }
  await page.goto('/p/PHAROS/settings/delivery#advanced'); await expect(modeButton(page, 'reviews', 'shadow')).toBeEnabled()
  await modeButton(page, 'reviews', 'shadow').click(); await arrived
  const aborted = page.waitForEvent('requestfailed', request => request.url().includes('/projects/p-pharos/delivery-reviews/settings') && request.method() === 'PUT')
  await navigate(page, '/p/AEON/settings/delivery#advanced'); await expect(modeButton(page, 'reviews', 'shadow')).toBeEnabled()
  await aborted; releaseHeld()
  await expect(modeButton(page, 'reviews', 'off')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.locator('.toast').filter({ hasText: 'Reviews saved.' })).toHaveCount(0)
  expect(world.writes).toHaveLength(1); expect(world.writes[0].project).toBe('p-pharos')
  world.control.deny = 'delivery_reviews.manage'
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(modeButton(page, 'reviews', 'shadow')).toBeDisabled()
  expect(world.errors).toEqual([])
  const member = await page.context().newPage(), memberWorld = await setup(member, { admin: false })
  await member.goto('/p/PHAROS/settings/delivery#advanced'); await expect(advanced(member)).toHaveCount(0)
  expect(memberWorld.writes).toEqual([]); await member.close()
  const agent = await page.context().newPage(); await setup(agent, { principalKind: 'agent' })
  await agent.goto('/p/PHAROS/settings/delivery#advanced'); await expect(modeButton(agent, 'reviews', 'shadow')).toBeEnabled()
  await expect(agent.getByRole('switch', { name: 'Record shadow admission' })).toBeDisabled(); await agent.close()
})

// Each evidence cell gets fresh state and its own timeout, retaining all flows.
for (const width of [1440, 1024, 400, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`AEON-1235: approved P4 reference states and check.mjs audit against the build at ${width}px ${theme}`, async ({ browser }, testInfo) => {
    test.setTimeout(240_000)
    const lines = [`AEON-1235 P4 evidence · HEAD ${execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()}`, `Approved check.mjs SHA-256 ${approvedCheckSHA}`, 'The approved audit is adapted to the real P4 build and existing application shell. Full P2/P3 repository states are integration dependencies.', 'Design/API difference: Delivered needs release_id and frozen published membership, so maintenance includes a published-release selector. Actions precede growing preview/refusal content to preserve the control-stability rule.', 'Chat-v1 is a deployment opt-in with no readable workspace feature flag. The row describes its real dependency without asserting an invented enabled/disabled value.', 'The mock’s sample PR placeholder is omitted. All rendered records in these screenshots are test fixtures only.']
    expect(Object.keys(PROJECT_SETTINGS_EXPLAINERS)).toHaveLength(21)
    for (const [id, explanation] of Object.entries(PROJECT_SETTINGS_EXPLAINERS)) {
      const parts = [explanation.what, explanation.effect, explanation.who, 'where' in explanation && explanation.where ? explanation.where.label : '']
      for (const projectKey of ['PHAROS', 'AEON', 'PRJ-26']) {
        const rendered = parts.map(value => fillProjectSettingsExplainer(value, { projectKey })).join(' ')
        expect(rendered, `${id}/${projectKey}`).not.toMatch(/<[^>]*number>|\{(?:K|N2?|R|O|C)\}/)
      }
      lines.push(`how-${id}: no unresolved placeholders for PHAROS, AEON or PRJ-26 PASS`)
    }
    const context = await browser.newContext({ viewport: { width, height: width < 600 ? 860 : 1000 }, hasTouch: width < 600, colorScheme: theme, reducedMotion: 'reduce' })
    try {
      const page = await context.newPage(), world = await setup(page)
      await page.route('**/api/preferences/theme', route => route.fulfill({ json: { key: 'theme', value: theme } }))
      async function capture(state: string, fullPage = true) {
        for (const check of await checkProjectSettings(page, `${state}/${width}/${theme}`, width < 600)) { expect(check.passed, check.message).toBe(true); lines.push(`${state}-${width}-${theme}: ${check.message} PASS`) }
        await captureProjectSettings(page, { path: testInfo.outputPath(`${state}-${width}-${theme}.png`), fullPage }, width < 600)
        if (width < 600) expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(true)
      }
      await page.goto('/p/PHAROS/settings/delivery')
      await expect(page.locator('#adv-sum')).toBeVisible(); await capture('advanced-folded')
      await page.locator('#adv-sum').click(); await expect(modeButton(page, 'reviews', 'shadow')).toBeEnabled(); await capture('advanced')
      await page.locator('[data-machine="reviews"]').scrollIntoViewIfNeeded(); await capture('advanced-machines', false)
      await advanced(page).getByRole('button', { name: 'Preview…', exact: true }).scrollIntoViewIfNeeded(); await capture('advanced-maintenance', false)
      const controls = { sum: page.locator('#adv-sum'), queue: modeButton(page, 'queue', 'shadow'), review: modeButton(page, 'reviews', 'shadow'), shipping: modeButton(page, 'shipping', 'shadow') }
      for (const id of ['shadow', 'queue', 'reviews', 'shipping', 'routing', 'admission', 'chat', 'mbf']) {
        const trigger = advanced(page).locator(`[data-how="${id}"]`)
        await trigger.scrollIntoViewIfNeeded()
        const stable = await controlStability(page, { ...controls, trigger })
        await stable.check(() => trigger.click())
        const explanation = page.locator('.explainer')
        await expect(explanation.locator('h3')).toBeFocused()
        await expect(explanation).not.toContainText(/<[^>]*number>|\{(?:K|N2?|R|O|C)\}/)
        if (['shadow', 'queue', 'reviews', 'shipping', 'routing', 'admission', 'mbf'].includes(id)) await expect(explanation).toContainText('a PHAROS ticket')
        if (id === 'queue') await expect(explanation).toContainText('another PHAROS ticket')
        expect((await explanation.innerText()).split(/\s+/).filter(word => /[\p{L}\p{N}]/u.test(word)).length).toBeLessThanOrEqual(120)
        expect(await explanation.evaluate(el => el.classList.contains('as-sheet'))).toBe(width < 600)
        await capture(`how-${id}`, false)
        await stable.check(() => page.keyboard.press('Escape')); await expect(trigger).toBeFocused(); stable.done()
      }
      await advanced(page).getByRole('button', { name: 'Preview…', exact: true }).click()
      const form = page.locator('.mbf'); await form.getByLabel('Published release').selectOption(release); await form.getByLabel('Pull request numbers').fill('#512, #514, 519, 520')
      const repairStable = await controlStability(page, { input: form.getByLabel('Pull request numbers'), preview: form.getByRole('button', { name: 'Preview', exact: true }), apply: form.getByRole('button', { name: 'Apply preview' }) })
      await repairStable.check(async () => { await form.getByRole('button', { name: 'Preview', exact: true }).click(); await expect(form.locator('.mbf-prev')).toBeVisible() }); repairStable.done()
      await capture('advanced-preview')
      await navigate(page, '/')
      await page.locator('#new-proj').click(); await expect(page.locator('#np-name')).toBeFocused()
      const sheet = page.getByRole('dialog', { name: 'New project', exact: true })
      await expect(sheet.locator('#np-group')).toBeEnabled(); await capture('projects-new', false)
      const stable = await controlStability(page, { sheet, name: sheet.locator('#np-name'), key: sheet.locator('#np-key'), group: sheet.locator('#np-group'), create: sheet.locator('#np-go'), cancel: sheet.getByRole('button', { name: /^Cancel/ }) })
      await stable.check(() => sheet.locator('#np-name').fill('Website relaunch'))
      await expect(sheet.locator('#np-key')).toHaveValue('WEB'); await capture('projects-new-suggested', false)
      world.control.conflict = true
      await stable.check(async () => { await sheet.locator('#np-go').click(); await expect(sheet.getByRole('alert')).toContainText('Website management’s key') }); await capture('projects-new-conflict', false)
      world.control.conflict = false
      await stable.check(() => sheet.locator('#np-key').fill('PORTAL')); await stable.check(() => sheet.locator('#np-name').fill('Plattform für gemeinsame Projektverwaltung und verlässliche Zusammenarbeit')); await capture('projects-new-long-text', false); stable.done()
      await sheet.locator('#np-go').click(); await expect(page.getByRole('region', { name: 'Next step' })).toBeVisible(); await expect(page.getByLabel('Name', { exact: true })).toBeEnabled(); await capture('general-new')
      await page.getByRole('link', { name: 'Next: link a repository' }).click(); await expect(page).toHaveURL('/p/PORTAL/settings/delivery?link=1#repository')
      expect(world.errors).toEqual([])
      lines.push(`${width}/${theme}: eight explainers, stable controls, server key-conflict and next-step flow PASS`)
    } finally { await context.close() }
    writeFileSync(testInfo.outputPath('check-result.md'), lines.join('\n') + '\n')
  })
}
