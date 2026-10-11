// SPDX-License-Identifier: AGPL-3.0-only
// Risk: settings, Undo and destructive confirmations must stay on the project
// and revision acted on; failed writes cannot claim success or move controls.
import { test, expect, type Page } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { controlStability } from './control-stability'
import { checkProjectSettings, approvedCheckSHA } from './project-settings-acceptance.mjs'

async function setup(page: Page, readOnly = false) {
  const data = fixtures(), errors = watchErrors(page)
  await mockWork(page, data, { admin: !readOnly, readOnly })
  const writes: { id: string; body: Record<string, unknown>; expected: string }[] = [], assignments: unknown[] = []
  const nodes = new Map(data.projects.map(p => [p.id, { id: p.id, key: p.key, title: p.title, body: p.description, state: p.state, fields: { classic: { key: p.classic, description: p.description }, preserved: 'keep me' }, updated_at: p.last, created_at: p.last, parent_id: null, kind_id: 'k-project', position: '0', deleted_at: null }]))
  const groups = [{ id: 'g-platform', name: 'Platform', project_ids: ['p-pharos'] }, { id: 'g-product', name: 'Product', project_ids: ['p-aeon'] }]
  const control = { fail: false, contextFail: false, held: null as null | { entered: () => void; until: Promise<void> } }
  await page.route('**/api/nodes/p-*', async route => {
    const request = route.request(), id = new URL(request.url()).pathname.split('/').at(-1)!, node = nodes.get(id)
    if (!node) return route.fulfill({ status: 404, json: { error: 'Not found' } })
    if (request.method() === 'PATCH') {
      const body = request.postDataJSON(), expected = request.headers()['if-unmodified-since']
      writes.push({ id, body, expected })
      if (control.held) { const hold = control.held; control.held = null; hold.entered(); await hold.until }
      if (readOnly) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
      if (control.fail) return route.fulfill({ status: 412, json: { error: 'node has changed' } })
      if (expected !== node.updated_at) return route.fulfill({ status: 412, json: { error: 'node has changed' } })
      Object.assign(node, body, { updated_at: new Date(Date.parse(node.updated_at) + 1000).toISOString() })
      const p = data.projects.find(p => p.id === id)!
      Object.assign(p, { title: node.title, description: node.body, state: node.state, last: node.updated_at })
    }
    await route.fulfill({ json: node })
  })
  let inverse = structuredClone(groups)
  await page.route('**/api/project-groups**', async route => {
    if (route.request().method() === 'POST') {
      const body = route.request().postDataJSON(); assignments.push(body); inverse = structuredClone(groups)
      for (const group of groups) group.project_ids = [...group.project_ids.filter(id => !body.project_ids.includes(id)), ...(body.group_id === group.id ? body.project_ids : [])]
      return route.fulfill({ json: { assignments: body.project_ids.map((project_id: string) => ({ project_id, group_id: body.group_id })), event_id: 41 } })
    }
    return route.fulfill({ json: { items: groups } })
  })
  await page.route('**/api/events/41/undo', route => { groups.splice(0, groups.length, ...inverse); return route.fulfill({ json: { event_id: 42 } }) })
  await page.route('**/api/recurrences**', route => route.fulfill({ json: { items: [], next_cursor: null } }))
  await page.route('**/api/projects/*/routine-execution-settings', route => route.fulfill({ status: 404, json: { error: 'not configured' } }))
  await page.route('**/api/me/permissions**', route => {
    const project = new URL(route.request().url()).searchParams.get('project_id') ?? undefined
    const answer = mockEffectivePermissions(readOnly ? 'viewer' : 'admin', project)
    if (!readOnly) answer.workspace.permissions.push('account.use.manage')
    return route.fulfill({ json: answer })
  })
  // The existing Work context card remains the owner of this API.
  await page.route('**/api/projects/*/work-context', route => route.fulfill({ status: 404, json: { error: 'not assigned' } }))
  await page.route('**/api/account-use**', route => control.contextFail
    ? route.fulfill({ status: 503, json: { error: 'context read unavailable' } })
    : route.fulfill({ json: { rules: { revision: 1, new_accounts: 'ask', new_contexts: 'ask', new_projects: 'holding', new_models: 'shipped_only', confirmation_required: false, enforced_at: null, confirmed_at: null }, contexts: [], accounts: [], cells: [], running_outside: [], running_outside_truncated: false, next_account: null, next_context: null } }))
  return { data, nodes, writes, assignments, control, errors }
}
async function navigate(page: Page, path: string) {
  await page.evaluate(path => (document.querySelector('#app') as unknown as { __vue_app__: { config: { globalProperties: { $router: { push(path: string): Promise<void> } } } } }).__vue_app__.config.globalProperties.$router.push(path), path)
}

test('AEON-1233: General edits, lifecycle and legacy links stay on the acted-on project and revision', async ({ page }) => {
  test.setTimeout(120_000)
  const world = await setup(page)
  await page.goto('/p/PHAROS/tickets')
  await page.getByRole('link', { name: 'Edit name and description in Settings' }).click()
  await expect(page).toHaveURL('/p/PHAROS/settings#f-name')
  const name = page.getByLabel('Name', { exact: true }), description = page.getByLabel('Description', { exact: true })
  await expect(name).toBeEnabled(); await expect(name).toBeFocused()
  const initialRevision = world.nodes.get('p-pharos')!.updated_at
  // A list response snapshots the old revision before the edit, then returns
  // only after the confirmed write. It must not put the old header back.
  const staleNodes = structuredClone([...world.nodes.values()])
  let refreshEntered!: () => void, releaseRefresh!: () => void
  const reading = new Promise<void>(resolve => { refreshEntered = resolve }), reply = new Promise<void>(resolve => { releaseRefresh = resolve })
  const staleReader = async (route: import('@playwright/test').Route) => {
    if (new URL(route.request().url()).searchParams.get('kind') !== 'project') return route.fallback()
    refreshEntered(); await reply
    await route.fulfill({ json: { items: staleNodes, next_cursor: null } })
  }
  await page.route('**/api/nodes?**', staleReader)
  await page.evaluate(() => {
    const app = (document.querySelector('#app') as unknown as { __vue_app__: { config: { globalProperties: { $pinia: { _s: Map<string, { load(force: boolean): Promise<void> }> } } } } }).__vue_app__
    Reflect.set(window, '__projectSettingsRefresh', app.config.globalProperties.$pinia._s.get('projects')!.load(true))
  })
  await reading
  const stable = await controlStability(page, { name, description, group: page.getByLabel('Group', { exact: true }), key: page.locator('.project-settings .key-show') })
  await stable.check(() => name.fill('Pharos renamed'))
  await name.press('Enter')
  await expect(page.locator('#project-title')).toHaveText('Pharos renamed')
  expect(world.writes[0]).toEqual({ id: 'p-pharos', body: { title: 'Pharos renamed' }, expected: initialRevision })
  releaseRefresh(); await page.evaluate(() => Reflect.get(window, '__projectSettingsRefresh'))
  await page.unroute('**/api/nodes?**', staleReader)
  await expect(page.locator('#project-title')).toHaveText('Pharos renamed')
  await expect(name).toHaveValue('Pharos renamed')
  await page.getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(name).toHaveValue('Pharos')
  await stable.check(() => description.fill('A complete description.\nA second line stays in the editor.'))
  const mod = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent) ? 'Meta' : 'Control')
  await description.press(`${mod}+Enter`)
  await expect(page.getByText('Description saved.', { exact: true })).toBeVisible()
  expect(world.nodes.get('p-pharos')!.fields.classic.description).toBe('A complete description.\nA second line stays in the editor.')
  expect(world.nodes.get('p-pharos')!.fields.preserved).toBe('keep me')
  await stable.check(() => page.getByLabel('Group', { exact: true }).selectOption('g-product'))
  await expect(page.getByText('Group saved.', { exact: true })).toBeVisible()
  expect(world.assignments).toEqual([{ group_id: 'g-product', project_ids: ['p-pharos'] }])
  await stable.check(() => page.locator('.toast').filter({ hasText: 'Group saved.' }).getByRole('button', { name: 'Undo', exact: true }).click())
  await expect(page.getByLabel('Group', { exact: true })).toHaveValue('g-platform')
  stable.done()
  world.control.fail = true
  await name.fill('Draft awaiting a reload'); await name.press('Enter')
  await expect(page.getByText('The project changed. Reload it before saving; your draft stays here.', { exact: true })).toBeVisible()
  await expect(name).toHaveValue('Draft awaiting a reload'); await expect(page.locator('#project-title')).toHaveText('Pharos')
  world.control.fail = false
  await page.getByRole('button', { name: 'Reload project', exact: true }).click()
  await expect(name).toBeEnabled(); await expect(name).toHaveValue('Draft awaiting a reload')
  await name.fill('Pharos'); await name.press('Enter')
  const freeze = page.locator('[data-life="freeze"]'), archive = page.locator('[data-life="archive"]'), del = page.locator('[data-life="delete"]')
  const lifeStable = await controlStability(page, { freeze, archive, del })
  await lifeStable.check(() => freeze.click())
  const popup = page.getByRole('dialog', { name: 'Freeze Pharos?' })
  await expect(popup).toBeVisible()
  await popup.getByRole('button', { name: 'Freeze', exact: true }).click()
  await expect(freeze).toHaveAccessibleName('Unfreeze')
  await lifeStable.check(() => freeze.click())
  await expect(freeze).toHaveAccessibleName('Freeze…')
  lifeStable.done()
  await archive.click()
  await page.getByRole('dialog', { name: 'Archive Pharos?' }).getByRole('button', { name: 'Archive', exact: true }).click()
  await expect(archive).toHaveAccessibleName('Restore')
  await archive.click(); await expect(archive).toHaveAccessibleName('Archive…')
  await del.click()
  const sheet = page.getByRole('dialog', { name: 'Delete Pharos?' }), deleteAction = sheet.getByRole('button', { name: /^Delete project/ })
  await expect(sheet).toBeVisible()
  const sheetStable = await controlStability(page, { sheet, cancel: sheet.getByRole('button', { name: /^Cancel/ }), deleteAction, key: sheet.getByLabel('Type PHAROS to confirm') })
  await sheetStable.check(() => sheet.getByLabel('Type PHAROS to confirm').fill('AEON'))
  await expect(deleteAction).toBeDisabled()
  await sheetStable.check(() => sheet.getByLabel('Type PHAROS to confirm').fill('PHAROS'))
  await expect(deleteAction).toBeEnabled()
  world.control.fail = true
  await sheetStable.check(() => deleteAction.click())
  await expect(sheet.getByRole('alert')).toContainText('The project changed')
  expect(world.nodes.get('p-pharos')!.state).toBe('active')
  await sheet.getByLabel('Type PHAROS to confirm').focus()
  await page.keyboard.press('Escape'); await expect(sheet).toBeVisible()
  await page.keyboard.press('Escape'); await expect(sheet).toHaveCount(0); await expect(del).toBeFocused()
  sheetStable.done(); world.control.fail = false
  // A barrier proves the write is in flight before the mounted project changes.
  let entered!: () => void, release!: () => void
  const requested = new Promise<void>(resolve => { entered = resolve }), allowed = new Promise<void>(resolve => { release = resolve })
  world.control.held = { entered, until: allowed }
  await name.fill('Pharos slow save'); await name.press('Enter'); await requested
  const count = world.writes.length
  await name.blur(); expect(world.writes.length).toBe(count)
  await navigate(page, '/p/AEON/settings')
  await expect(page.locator('#project-title')).toHaveText('Aeon')
  const replied = page.waitForResponse(response => response.url().endsWith('/api/nodes/p-pharos') && response.request().method() === 'PATCH')
  release(); await replied
  await expect(page.getByLabel('Name', { exact: true })).toHaveValue('Aeon')
  expect(world.nodes.get('p-aeon')!.title).toBe('Aeon')
  await navigate(page, '/p/PHAROS/settings')
  await expect(page.getByLabel('Name', { exact: true })).toHaveValue('Pharos slow save')
  await expect(page.locator('#project-title')).toHaveText('Pharos slow save')
  await navigate(page, '/p/AEON/settings#project-work-context'); await expect(page).toHaveURL('/p/AEON/settings/agents#project-work-context')
  await expect(page.locator('#project-work-context')).toBeVisible()
  await navigate(page, '/p/AEON/settings#recurring-work'); await expect(page).toHaveURL('/p/AEON/settings/routines#recurring-work')
  await expect(page.locator('#recurring-work')).toBeVisible()
  await navigate(page, '/p/AEON/settings?recurrence=r-1&consent=1'); await expect(page).toHaveURL('/p/AEON/settings/routines?recurrence=r-1&consent=1')
  await navigate(page, '/p/AEON/settings/access')
  await expect(page.getByRole('link', { name: 'Settings › Access › Projects' })).toHaveAttribute('href', '/settings/access/projects/p-aeon')
  await navigate(page, '/p/AEON/settings')
  await expect(page.getByLabel('Name', { exact: true })).toBeEnabled()
  const deleteRevision = world.nodes.get('p-aeon')!.updated_at
  await page.locator('[data-life="delete"]').click()
  await page.getByLabel('Type AEON to confirm').fill('AEON')
  await page.getByRole('button', { name: /^Delete project/ }).click()
  await expect(page).toHaveURL('/')
  expect(world.writes.at(-1)).toEqual({ id: 'p-aeon', body: { state: 'deleted' }, expected: deleteRevision })
  expect(world.errors).toEqual([])
})

test('AEON-1233: approved P1 reference states and check.mjs audit against the build', async ({ browser }, testInfo) => {
  test.setTimeout(240_000)
  const lines = [`AEON-1233 P1 evidence · HEAD ${execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()}`, `Approved check.mjs SHA-256 ${approvedCheckSHA}`, 'The approved audit runs on the built P1 surface. Full mock flows for P2–P6 are outside this package.', 'The public-source guard requires the approved Lead example’s operator host to become {C}, filled from the selected computer; other approved explainer text is retained.', 'Freeze dates/actors are absent from the project API: show the confirmed Frozen state without inventing a date or person. The key uses a ticket-number pattern when no actual ticket number is supplied.']
  for (const width of [1440, 1024, 400, 390]) for (const theme of ['light', 'dark'] as const) {
    const context = await browser.newContext({ viewport: { width, height: width < 600 ? 860 : 1000 }, hasTouch: width < 600, colorScheme: theme, reducedMotion: 'reduce' })
    try {
      const page = await context.newPage()
      page.setDefaultTimeout(10_000)
      const world = await setup(page)
      await page.route('**/api/preferences/theme', route => route.fulfill({ json: { key: 'theme', value: theme } }))
      await page.goto('/p/PHAROS/settings')
      await expect(page.getByLabel('Name', { exact: true })).toBeEnabled()
      await expect(page.getByLabel('Group', { exact: true })).toBeEnabled()
      async function capture(state: string) {
        const checks = await checkProjectSettings(page, `${state}/${width}/${theme}`, width < 600)
        for (const check of checks) expect(check.passed, check.message).toBe(true)
        lines.push(`${state}-${width}-${theme}: ${checks.length} approved audit checks PASS`)
        await page.screenshot({ path: testInfo.outputPath(`${state}-${width}-${theme}.png`), fullPage: !/sheet|how|popover/.test(state) })
      }
      await capture('general-pharos')
      const nav = width <= 1000 ? page.getByRole('button', { name: /^Section:/ }) : page.getByRole('navigation', { name: 'Project settings sections' })
      const stable = await controlStability(page, { nav, name: page.getByLabel('Name', { exact: true }), description: page.getByLabel('Description', { exact: true }) })
      for (const id of ['key', 'lifecycle']) {
        const trigger = page.locator(`[data-how="${id}"]`)
        await stable.check(() => trigger.click())
        const explainer = page.locator('.explainer')
        await expect(explainer.locator('h3')).toBeFocused()
        expect(await explainer.evaluate(el => el.classList.contains('as-sheet'))).toBe(width < 600)
        expect((await explainer.innerText()).split(/\s+/).filter(word => /[\p{L}\p{N}]/u.test(word)).length).toBeLessThanOrEqual(120)
        const box = (await explainer.boundingBox())!
        expect(box.x).toBeGreaterThanOrEqual(0); expect(box.x + box.width).toBeLessThanOrEqual(width + .5)
        expect(box.y).toBeGreaterThanOrEqual(0); expect(box.y + box.height).toBeLessThanOrEqual((width < 600 ? 860 : 1000) + .5)
        await capture(`how-${id}`)
        await stable.check(() => page.keyboard.press('Escape'))
        await expect(trigger).toBeFocused()
      }
      if (width >= 600) await stable.check(() => page.locator('[data-how="key"]').hover())
      stable.done()
      await page.locator('[data-life="freeze"]').click(); await capture('freeze-popover'); await page.keyboard.press('Escape')
      await page.locator('[data-life="archive"]').click(); await capture('archive-popover'); await page.keyboard.press('Escape')
      await page.locator('[data-life="delete"]').click(); await capture('delete-sheet'); await page.keyboard.press('Escape'); await page.keyboard.press('Escape')
      const freeze = page.locator('[data-life="freeze"]'), archive = page.locator('[data-life="archive"]'), del = page.locator('[data-life="delete"]')
      const lifecycleStable = await controlStability(page, { freeze, archive, del })
      await lifecycleStable.check(async () => {
        await freeze.click(); await page.getByRole('dialog', { name: 'Freeze Pharos?' }).getByRole('button', { name: 'Freeze', exact: true }).click()
        await expect(freeze).toHaveAccessibleName('Unfreeze')
      })
      await lifecycleStable.check(async () => { await freeze.click(); await expect(freeze).toHaveAccessibleName('Freeze…') })
      await lifecycleStable.check(async () => {
        await archive.click(); await page.getByRole('dialog', { name: 'Archive Pharos?' }).getByRole('button', { name: 'Archive', exact: true }).click()
        await expect(archive).toHaveAccessibleName('Restore')
      })
      await lifecycleStable.check(async () => { await archive.click(); await expect(archive).toHaveAccessibleName('Archive…') })
      lifecycleStable.done()
      // Dismiss completed action messages through their real controls before
      // opening the next reference state; no timers or hidden DOM edits.
      await page.locator('.toast-close').evaluateAll(buttons => { for (const button of buttons) (button as HTMLButtonElement).click() })
      await expect(page.locator('.toast')).toHaveCount(0)
      if (width <= 1000) { await nav.click(); await capture('nav-open'); await page.keyboard.press('Escape') }
      await navigate(page, '/p/AEON/settings'); await expect(page.getByLabel('Name', { exact: true })).toBeEnabled(); await expect(page.getByLabel('Group', { exact: true })).toBeEnabled(); await capture('general-aeon')
      await navigate(page, '/p/PRJ-26/settings'); await expect(page.getByLabel('Name', { exact: true })).toBeEnabled(); await expect(page.getByLabel('Group', { exact: true })).toBeEnabled(); await capture('general-prj26')
      await navigate(page, '/p/PHAROS/settings/agents'); await expect(page.getByText('This project has no context yet, so no account may work on it. Choose one.', { exact: true })).toBeVisible(); await capture('agents-pharos')
      world.control.contextFail = true
      await navigate(page, '/p/PHAROS/settings/access'); await navigate(page, '/p/PHAROS/settings/agents')
      await expect(page.locator('#project-work-context').getByRole('alert')).toContainText('The context could not be read.'); await capture('agents-error')
      world.control.contextFail = false
      await navigate(page, '/p/PHAROS/settings/access'); await capture('access-pharos')
      await navigate(page, '/p/PHAROS/settings/routines'); await expect(page.locator('#recurring-work')).toBeVisible(); await capture('routines-pharos')
      expect(world.errors).toEqual([])
      await page.close()
      const viewer = await context.newPage(); await setup(viewer, true)
      await viewer.route('**/api/preferences/theme', route => route.fulfill({ json: { key: 'theme', value: theme } }))
      await viewer.goto('/p/PHAROS/settings')
      await expect(viewer.getByRole('heading', { name: 'Lifecycle', exact: true })).toBeVisible()
      await expect(viewer.locator('[data-life]')).toHaveCount(0); await expect(viewer.locator('#f-name, #f-desc')).toHaveCount(0)
      await expect(viewer.getByRole('link', { name: 'Edit name and description in Settings' })).toHaveCount(0)
      await expect(viewer.getByText('Reading shared groups…', { exact: true })).toHaveCount(0)
      for (const check of await checkProjectSettings(viewer, `readonly/${width}/${theme}`, width < 600)) expect(check.passed, check.message).toBe(true)
      await viewer.screenshot({ path: testInfo.outputPath(`general-readonly-${width}-${theme}.png`), fullPage: true })
      const longPage = await context.newPage(), longWorld = await setup(longPage)
      const longName = 'Plattform für gemeinsame Projektverwaltung und verlässliche Zusammenarbeit'
      longWorld.data.projects[0]!.title = longName; longWorld.nodes.get('p-pharos')!.title = longName
      await longPage.route('**/api/preferences/theme', route => route.fulfill({ json: { key: 'theme', value: theme } }))
      await longPage.goto('/p/PHAROS/settings')
      await expect(longPage.getByLabel('Name', { exact: true })).toBeEnabled()
      await expect(longPage.getByLabel('Name', { exact: true })).toHaveValue(longName)
      await expect(longPage.getByLabel('Group', { exact: true })).toBeEnabled()
      for (const check of await checkProjectSettings(longPage, `long-text/${width}/${theme}`, width < 600)) expect(check.passed, check.message).toBe(true)
      await longPage.screenshot({ path: testInfo.outputPath(`general-long-text-${width}-${theme}.png`), fullPage: true })
    } finally { await context.close() }
  }
  writeFileSync(testInfo.outputPath('check-result.md'), `${lines.join('\n')}\n`)
})
