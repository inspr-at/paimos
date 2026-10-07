// SPDX-License-Identifier: AGPL-3.0-only
// Risks: one-write edits, immutable inherited rules, stale identity/Undo, and AEON-541 controls.
import { expect, test, type Page } from '@playwright/test'
import { boardCards, boardFixture, boardPerson, boardPin } from './models-board-fixtures'
import { controlStability } from './control-stability'
import type { BoardCard, ModelRule, OrderBody } from '../src/lib/modelsBoard'

async function mockBoard(page: Page, options: { manage?: boolean; fail?: number; german?: boolean; noRoute?: boolean } = {}) {
  let document = boardFixture(options.german), rules = [structuredClone(boardPin)], ruleRevision = 2
  const writes: { path: string; body: Record<string, unknown>; person?: string; method: string }[] = []
  const reads: string[] = []
  let fail = options.fail, hold: (() => Promise<void>) | undefined
  const original = structuredClone(document)
  const orders = new Map<string, OrderBody>()
  await page.route('**/api/**', async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname.replace(/^\/api/, '')
    if (path === '/me/permissions') return route.fulfill({ json: { workspace: { id: 'board-tenant', role: 'member', permissions: ['models.read', ...(options.manage === false ? [] : ['model_prefs.manage', 'work_kinds.manage'])] }, project: url.searchParams.get('project_id') ? { id: url.searchParams.get('project_id'), role: 'member', permissions: [] } : null } })
    if (path === '/projects') return route.fulfill({ json: { items: [{ id: 'project-a', title: 'AEON', state: 'active', archived: false }], next_cursor: null } })
    if (request.method() === 'GET') {
      if (path === '/model-rules') return route.fulfill({ json: { revision: ruleRevision, rules } })
      if (path === '/model-preferences/board') {
        reads.push(url.search)
        const view = structuredClone(document), project = url.searchParams.get('project_id'), layer = url.searchParams.get('layer') ?? 'mine'
        view.layer = layer as typeof view.layer
        const applicable = rules.filter(rule => rule.scope === 'workspace' || rule.project_id === project)
        for (const column of view.columns) {
          const cards = [...boardCards, ...document.tray].map(card => column.column.startsWith('review:') ? { ...card, effort: 'xhigh' } : card)
          const bans = column.not.filter(card => card.lock?.kind === 'cross_family')
          const own = orders.get(column.column)
          const rank = own?.rank ?? original.columns.find(value => value.column === column.column)!.list.map(card => card.line)
          const free = rank.filter(line => !applicable.some(rule => rule.column === column.column && rule.line === line) && !bans.some(card => card.line === line) && !column.cant.some(item => item.line === line)).map(line => ({ ...cards.find(card => card.line === line)! }))
          const pins = (lock: string) => applicable.filter(rule => rule.column === column.column && rule.lock === lock).sort((a, b) => Number(b.scope === 'project') - Number(a.scope === 'project') || a.position - b.position).map(rule => ({ ...cards.find(card => card.line === rule.line)!, lock: { kind: 'rule' as const, value: rule.lock, who: rule.set_by, why: rule.why, at: rule.set_at, scope: rule.scope } }))
          column.top = pins('top'); column.bottom = pins('bottom'); column.free = free
          column.list = [...column.top, ...free, ...column.bottom].filter(card => !column.cant.some(item => item.line === card.line))
          column.not = [...bans, ...pins('not'), ...(own?.not ?? []).filter(line => !bans.some(card => card.line === line) && !applicable.some(rule => rule.column === column.column && rule.line === line)).map(line => ({ ...cards.find(card => card.line === line)! }))]
          if (own) column.source = 'own'
          column.hidden = view.profile.hidden_kinds.includes(column.column)
          if (options.noRoute) { column.not = column.list.map(card => ({ ...card, lock: { kind: 'residency' as const, value: 'not' as const, who: null, why: 'No qualifying route', at: null, scope: 'workspace' } })).concat(column.not); column.list = [] }
        }
        if (options.noRoute) view.residency.effective = 'eu'
        return route.fulfill({ json: view })
      }
      return route.fulfill({ json: { items: [] } })
    }
    const body = request.postDataJSON() ?? {}, person = request.headers()['if-prefs-person']
    writes.push({ path, body, person, method: request.method() })
    if (hold) await hold()
    if (fail) return route.fulfill({ status: fail, json: { error: fail === 409 ? 'stale_revision' : 'looser_than_workspace' } })
    if (path.startsWith('/model-rules/')) {
      const scope = path.split('/')[2] as 'workspace' | 'project', column = decodeURIComponent(path.split('/')[3]!)
      const desired = [...body.top.map((pin: { line: string; why: string }, position: number) => ({ ...pin, lock: 'top', position })), ...body.bottom.map((pin: { line: string; why: string }, position: number) => ({ ...pin, lock: 'bottom', position })), ...Object.entries(body.not).map(([line, why]) => ({ line, why, lock: 'not', position: 0 }))]
      if (scope === 'project' && rules.filter(rule => rule.scope === 'workspace' && rule.column === column).some(rule => !desired.some(pin => pin.line === rule.line && (pin.lock === rule.lock || pin.lock === 'not')))) return route.fulfill({ status: 422, json: { error: 'looser_than_workspace' } })
      rules = rules.filter(rule => rule.scope !== scope || rule.column !== column).concat(desired.filter(pin => scope !== 'project' || !rules.some(rule => rule.scope === 'workspace' && rule.column === column && rule.line === pin.line && rule.lock === pin.lock)).map(pin => ({ ...pin, column, scope, project_id: scope === 'project' ? url.searchParams.get('project_id') : null, set_by: 'Markus', set_at: '2026-10-07T13:00:00Z' }) as ModelRule))
      return route.fulfill({ json: { rules, revision: ++ruleRevision } })
    }
    if (url.searchParams.get('dry_run') === 'true') return route.fulfill({ json: { person_id: boardPerson, revision: document.revision, profile: document.profile, dry_run: true, moved: [{ column: 'backend', before: ['openai:sol'], after: ['openai:astra'] }] } })
    if (path.startsWith('/model-preferences/orders/')) {
      const column = decodeURIComponent(path.split('/')[3]!)
      if (request.method() === 'DELETE') orders.delete(column)
      else orders.set(column, { rank: body.rank, not: body.not })
    } else if (path === '/model-preferences/profile') {
      for (const field of ['residency', 'hidden_kinds', 'dismissed_lines', 'template']) if (field in body) Object.assign(document.profile, { [field]: body[field] })
      document.residency.own = document.profile.residency; document.residency.effective = document.profile.residency ?? 'any'
    } else if (path.includes('/tray/')) document.profile.dismissed_lines.push(decodeURIComponent(path.split('/')[3]!))
    document.revision++; document.profile.revision = document.revision
    return route.fulfill({ json: { person_id: boardPerson, revision: document.revision, profile: document.profile, dry_run: false, moved: [] } })
  })
  return { writes, reads, setFail: (status?: number) => { fail = status }, holdNext: () => {
    let release!: () => void; const barrier = new Promise<void>(resolve => { release = resolve }); hold = () => barrier
    return () => { hold = undefined; release() }
  }, addProjectRule: (rule: ModelRule) => { rules.push(rule) }, clearTray: () => { document.tray = [] } }
}
async function open(page: Page, query = '') { await page.goto(`/tests/models-board-harness.html${query}`); await expect(page.locator('[data-board-ready="true"]')).toBeVisible() }
const col = (page: Page, column = 'backend') => page.locator(`[data-column="${column}"]`)
const card = (page: Page, line: string, column = 'backend') => col(page, column).locator(`[data-line="${line}"]`)
const headerControls = (page: Page) => ({ head: page.locator('.bhead'), show: page.locator('[data-board-show]'), providers: page.locator('[data-board-providers]'), full: page.locator('[data-models-fullscreen]') })
async function layer(page: Page, name: string) { await page.locator('[data-board-show]').click(); await page.getByRole('menuitem', { name, exact: true }).click(); await expect(page.locator('[data-board-ready="true"]')).toBeVisible() }

test('keyboard moves write once with person and revision, keep pins and other controls still, and cross Not allowed', async ({ page }) => {
  const state = await mockBoard(page); await open(page)
  const guard = await controlStability(page, { ...headerControls(page), pin: card(page, boardPin.line, 'frontend'), firstSlot: col(page).locator('[data-zone="list"] li').first(), frame: page.locator('[data-board-scroll]') })
  await guard.check(async () => { await card(page, 'anthropic:sonnet').focus(); await page.keyboard.press('Alt+ArrowUp'); await expect(col(page).locator('[data-zone="list"] li').first()).toHaveAttribute('data-card', 'anthropic:sonnet') })
  expect(state.writes).toHaveLength(1); expect(state.writes[0]).toMatchObject({ person: boardPerson, body: { revision: 3, rank: ['anthropic:sonnet', 'openai:sol', 'anthropic:opus', 'openai:astra', 'anthropic:fable'], not: [] } })
  await expect(card(page, 'anthropic:sonnet')).toBeFocused()
  await guard.check(async () => { await card(page, 'anthropic:fable').focus(); await page.keyboard.press('Alt+ArrowDown'); await expect(col(page).locator('[data-zone="not"] [data-line="anthropic:fable"]')).toBeVisible() })
  expect(state.writes).toHaveLength(2)
  await guard.check(async () => { await card(page, boardPin.line, 'frontend').focus(); await page.keyboard.press('Alt+ArrowDown'); expect(state.writes).toHaveLength(2) })
  guard.done()
  await card(page, 'anthropic:fable').press('Enter'); await page.getByRole('menuitem', { name: 'Allow again', exact: true }).click()
  await expect(col(page).locator('[data-zone="list"] li').last()).toHaveAttribute('data-card', 'anthropic:fable')
})

test('pointer drag keeps a placeholder, pins and surrounding columns still and sends only the dropped order', async ({ page }) => {
  const state = await mockBoard(page); await open(page)
  const source = card(page, 'anthropic:sonnet'), destination = card(page, 'openai:sol')
  const guard = await controlStability(page, { ...headerControls(page), pin: card(page, boardPin.line, 'frontend'), adjacent: col(page, 'frontend'), firstSlot: col(page).locator('[data-zone="list"] li').first() })
  const a = (await source.boundingBox())!, b = (await destination.boundingBox())!
  await page.mouse.move(a.x + 40, a.y + 20); await page.mouse.down()
  await guard.check(async () => { await page.mouse.move(a.x + 48, a.y + 22); await expect(source).toHaveClass(/placeholder/); expect(state.writes).toHaveLength(0) })
  await guard.check(async () => { await page.mouse.move(b.x + 40, b.y + 8, { steps: 4 }); await page.mouse.up(); await expect(col(page).locator('[data-zone="list"] li').first()).toHaveAttribute('data-card', 'anthropic:sonnet') })
  expect(state.writes).toHaveLength(1); await expect(page.locator('[data-board-ghost]')).toHaveCount(0); guard.done()
})

test('project rules retain workspace pins, require a reason, preserve submit controls, and surface a server refusal', async ({ page }) => {
  const state = await mockBoard(page); await open(page); await layer(page, 'Project rules')
  const inherited = card(page, boardPin.line, 'frontend')
  await inherited.press('Alt+ArrowDown'); expect(state.writes).toHaveLength(0)
  await inherited.press('Enter'); await expect(page.getByRole('menuitem', { name: /Locked/ })).toContainText(boardPin.why); await page.keyboard.press('Escape')
  await card(page, 'openai:sol', 'frontend').press('Enter'); await page.getByRole('menuitem', { name: 'Pin to bottom', exact: true }).click()
  const field = page.getByPlaceholder('One sentence'), save = page.getByRole('button', { name: /Save reason/ })
  const guard = await controlStability(page, { save, field, pin: inherited, head: page.locator('.bhead') })
  await guard.check(async () => { await save.click(); await expect(page.getByRole('dialog').getByRole('alert')).toContainText('One sentence'); expect(state.writes).toHaveLength(0) })
  await guard.check(() => field.fill('Keep this account for complex work'))
  await field.press('Enter'); expect(state.writes).toHaveLength(0)
  guard.done()
  const modifier = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent) ? 'Meta' : 'Control')
  await field.press(`${modifier}+Enter`)
  await expect(col(page, 'frontend').locator('[data-zone="bottom"] [data-line="openai:sol"]')).toBeVisible()
  expect(state.writes).toHaveLength(1); expect(state.writes[0]!.body).toMatchObject({ revision: 2, top: [{ line: boardPin.line, why: boardPin.why }], bottom: [{ line: 'openai:sol', why: 'Keep this account for complex work' }] })
  state.setFail(422)
  await card(page, 'openai:sol', 'frontend').press('Enter'); await page.getByRole('menuitem', { name: 'No rule', exact: true }).click()
  await expect(page.locator('.feedback [role="alert"]')).toContainText('looser_than_workspace')
  await expect(col(page, 'frontend').locator('[data-zone="bottom"] [data-line="openai:sol"]')).toBeVisible()
})

test('capabilities stay separate from locks, rules may name incapable models, and reviews keep the author family excluded', async ({ page }) => {
  const state = await mockBoard(page); await open(page)
  await expect(col(page, 'design').getByLabel('Can’t do this here')).toContainText('Grok')
  await expect(col(page, 'design').locator('[data-line="xai:grok"]')).toHaveCount(0)
  await expect(col(page, 'review:openai').locator('[data-zone="not"] [data-line="openai:sol"]')).toHaveAttribute('aria-disabled', 'true')
  await expect(col(page, 'review:openai').getByRole('link', { name: 'Cross-family review rule' })).toHaveAttribute('href', '/settings/policies')
  await layer(page, 'Workspace rules')
  await col(page, 'design').getByRole('button', { name: /column menu/ }).click(); await page.getByRole('menuitem', { name: 'Add a rule…', exact: true }).click(); await page.getByRole('menuitem', { name: 'Grok 4.7', exact: true }).click(); await page.getByRole('menuitem', { name: 'Pin to bottom', exact: true }).click()
  await page.getByPlaceholder('One sentence').fill('Use when tools become available'); await page.getByRole('button', { name: /Save reason/ }).click()
  await expect(col(page, 'design').locator('[data-zone="bottom"] [data-line="xai:grok"]')).toBeVisible()
  await expect(col(page, 'design')).toContainText('The rule is kept')
  expect(state.writes).toHaveLength(1)
})

test('new catalog lines stay unplaced until one order places them and the tray and board height stay still', async ({ page }) => {
  const state = await mockBoard(page); await open(page)
  await expect(page.locator('.board [data-line="openai:nova"]')).toHaveCount(0)
  const tray = page.locator('.tray [data-line="openai:nova"]')
  const guard = await controlStability(page, { ...headerControls(page), board: page.locator('[data-board-scroll]'), pin: card(page, boardPin.line, 'frontend'), tray })
  await guard.check(async () => { await tray.press('Enter'); await page.getByRole('menuitem', { name: 'Backend build', exact: true }).click(); await expect(col(page).locator('[data-zone="list"] li').last()).toHaveAttribute('data-card', 'openai:nova') })
  expect(state.writes).toHaveLength(1); expect(state.writes[0]!.body.rank).toEqual(['openai:sol', 'anthropic:sonnet', 'anthropic:opus', 'openai:astra', 'anthropic:fable', 'openai:nova']); guard.done()
})

test('column visibility persists and restores fixed ordering while mandatory columns stay visible', async ({ page }) => {
  const state = await mockBoard(page); await open(page)
  const originalOrder = await page.locator('[data-column]').evaluateAll(elements => elements.map(element => element.getAttribute('data-column')))
  await col(page).getByRole('button', { name: /column menu/ }).click(); await page.getByRole('menuitem', { name: /^Hide this column/ }).click()
  await expect(col(page)).toHaveCount(0); expect(state.writes[0]!.body).toMatchObject({ hidden_kinds: ['backend'], revision: 3 })
  await expect(col(page, 'other').getByRole('button', { name: /column menu/ })).toHaveCount(0)
  await page.locator('[data-board-columns]').click(); await page.getByRole('menuitem', { name: 'Backend build', exact: true }).click()
  await expect(col(page)).toBeVisible(); expect(await page.locator('[data-column]').evaluateAll(elements => elements.map(element => element.getAttribute('data-column')))).toEqual(originalOrder)
  expect(state.writes).toHaveLength(2)
})

test('failed writes are honest, old Undo cannot overwrite a later edit, and an identity change discards a held result', async ({ page }) => {
  const state = await mockBoard(page, { fail: 409 }); await open(page)
  await card(page, 'anthropic:sonnet').press('Alt+ArrowUp'); await expect(page.getByRole('alert')).toContainText('not saved'); await expect(page.locator('.test-toasts button')).toHaveCount(0)
  state.setFail()
  await card(page, 'anthropic:sonnet').press('Alt+ArrowUp'); await expect(page.locator('.test-toasts button')).toHaveCount(1)
  const undo = page.locator('.test-toasts button').first()
  await card(page, 'anthropic:fable').press('Alt+ArrowDown'); await expect(page.locator('.test-toasts button')).toHaveCount(2)
  await undo.click(); expect(state.writes).toHaveLength(3); await expect(page.locator('.test-toasts')).toContainText('Undo is no longer available')
  const release = state.holdNext()
  await card(page, 'openai:sol').press('Alt+ArrowDown'); await expect.poll(() => state.writes.length).toBe(4)
  await page.locator('[data-change-person]').click(); await expect(page.locator('.test-toasts button')).toHaveCount(0)
  const reads = state.reads.length; release(); await expect(page.locator('[data-model-board]')).toHaveAttribute('aria-busy', 'false')
  expect(state.reads.length).toBe(reads); await expect(page.locator('.test-toasts button')).toHaveCount(0)
})

test('template preview writes no settings, shows Now to After, keeps personal columns and applies with guarded Undo', async ({ page }) => {
  const state = await mockBoard(page); await open(page)
  await page.locator('[data-template]').click(); await expect(page.getByRole('dialog')).toContainText('Now → After')
  expect(state.writes).toHaveLength(1); expect(state.writes[0]!.body).toEqual({ template: 'best', revision: 3 }); expect(state.writes[0]!.path).toBe('/model-preferences/profile')
  await expect(page.getByRole('dialog')).toContainText('Columns you ordered yourself stay.')
  const guard = await controlStability(page, { apply: page.getByRole('button', { name: /^Apply/ }), head: page.locator('.bhead') })
  await guard.check(() => page.getByRole('button', { name: 'Cancel', exact: true }).focus()); guard.done()
  await page.getByRole('button', { name: /^Apply/ }).click(); await expect(page.getByRole('dialog')).toHaveCount(0)
  expect(state.writes).toHaveLength(2); expect(state.writes[1]!.body).toEqual({ template: 'best', revision: 3 })
  await page.getByRole('button', { name: 'Undo', exact: true }).click(); await expect.poll(() => state.writes.length).toBe(3)
  expect(state.writes[2]!.body).toEqual({ template: null, revision: 4 })
})

test('agent readers can inspect all layers but have no write controls or move actions', async ({ page }) => {
  const state = await mockBoard(page, { manage: false }); await open(page, '?agent=1')
  await expect(page.locator('[data-board-columns]')).toHaveCount(0); await expect(page.locator('.bc-r1 button')).toHaveCount(0)
  await card(page, 'anthropic:sonnet').press('Alt+ArrowUp'); expect(state.writes).toHaveLength(0)
  await layer(page, 'Workspace default'); await expect(page.locator('.layer-note')).toContainText('Read only')
  await layer(page, 'Workspace rules'); await expect(col(page).locator('[data-zone]')).toHaveCount(4)
  await expect(col(page).locator('[data-line="openai:sol"]')).toHaveAttribute('aria-disabled', 'true'); expect(state.writes).toHaveLength(0)
})

test('provider writes keep selectors still and support Undo', async ({ page }) => {
  const state = await mockBoard(page); await open(page)
  const guard = await controlStability(page, headerControls(page))
  await guard.check(async () => { await page.locator('[data-board-providers]').click(); await page.getByRole('menuitem', { name: 'EU-hosted only', exact: true }).click(); await expect(page.locator('[data-board-providers]')).toContainText('EU-hosted only') })
  expect(state.writes).toHaveLength(1); expect(state.writes[0]).toMatchObject({ person: boardPerson, body: { residency: 'eu', revision: 3 } })
  await guard.check(async () => { await page.getByRole('button', { name: 'Undo', exact: true }).click(); await expect(page.locator('[data-board-providers]')).toContainText('As the workspace') }); guard.done()
  expect(state.writes[1]!.body).toEqual({ residency: null, revision: 4 })
})

test('an empty eligible list explains the provider wait and keeps held cards locked', async ({ page }) => {
  const state = await mockBoard(page, { noRoute: true }); await open(page)
  await expect(page.getByRole('status').filter({ hasText: 'no route qualifies today' })).toBeVisible()
  await expect(col(page).locator('[data-zone="list"]')).toBeEmpty()
  await card(page, 'openai:sol').press('Alt+ArrowUp'); expect(state.writes).toHaveLength(0)
})

test('full screen is a route with inert surroundings; popovers own Esc and Done restores scroll and focus', async ({ page }) => {
  await mockBoard(page); await open(page)
  const main = page.locator('.app-shell > main')
  await main.evaluate(element => { element.scrollTop = 60 })
  await page.locator('[data-board-scroll]').evaluate(element => { element.scrollLeft = 120 })
  const top = await main.evaluate(element => element.scrollTop)
  await page.locator('[data-models-fullscreen]').click(); await expect(page).toHaveURL(/\/settings\/models\/board/)
  await expect(page.locator('.fullboard')).toBeVisible(); await expect(page.locator('dialog')).toHaveCount(0)
  expect(await main.evaluate(element => (element as HTMLElement).inert)).toBe(true)
  const guard = await controlStability(page, { done: page.locator('[data-board-done]'), selectors: page.locator('.fullboard .bhead'), frame: page.locator('.fullboard') })
  await guard.check(async () => { await card(page, 'openai:sol').press('Enter'); await expect(page.getByRole('menu')).toBeVisible(); await page.keyboard.press('Escape'); await expect(page.getByRole('menu')).toHaveCount(0) }); guard.done()
  await page.locator('[data-board-done]').click(); await expect(page).toHaveURL(/\/settings\/models\?/)
  await expect(page.locator('[data-models-fullscreen]')).toBeFocused()
  expect(await main.evaluate(element => element.scrollTop)).toBe(top)
  expect(await page.locator('[data-board-scroll]').evaluate(element => element.scrollLeft)).toBe(120)
  expect(await main.evaluate(element => (element as HTMLElement).inert)).toBe(false)
  await page.locator('[data-models-fullscreen]').click(); await expect(page.locator('.fullboard [data-board-ready="true"]')).toBeVisible(); await page.keyboard.press('Escape'); await expect(page.locator('[data-models-fullscreen]')).toBeFocused()
})

for (const width of [390, 1024, 1280, 1440]) for (const theme of ['light', 'dark']) for (const lang of (width >= 1280 ? ['en', 'de'] : ['de'])) {
  test(`board and full-screen evidence ${width} ${theme} ${lang} text`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1100 }); await page.emulateMedia({ colorScheme: theme as 'light' | 'dark' })
    await page.addInitScript(mode => { document.addEventListener('DOMContentLoaded', () => { document.documentElement.dataset.theme = mode }) }, theme)
    await mockBoard(page, { german: lang === 'de' }); await open(page, `?lang=${lang}`)
    const guard = await controlStability(page, { ...headerControls(page), pin: card(page, boardPin.line, 'frontend'), board: page.locator('[data-board-scroll]') })
    await guard.check(async () => { await card(page, 'anthropic:sonnet').press('Enter'); await page.keyboard.press('Escape') }); guard.done()
    await expect(page.locator('[data-line]').first()).toHaveCSS('height', '48px')
    await page.locator('[data-board-show]').focus(); await page.mouse.move(0, 0)
    await expect(page.getByRole('tooltip')).toHaveCount(0)
    await page.screenshot({ path: testInfo.outputPath(`board-${width}-${theme}-${lang}.png`), fullPage: false })
    await page.locator('[data-models-fullscreen]').click(); await expect(page.locator('.fullboard [data-board-ready="true"]')).toBeVisible()
    expect(await page.locator('.bc-head').first().evaluate(element => element.getBoundingClientRect().height)).toBe(120)
    await page.screenshot({ path: testInfo.outputPath(`full-board-${width}-${theme}-${lang}.png`), fullPage: false })
  })
}
