// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockBusiness, businessData } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { controlStability } from './control-stability'
const shots = 'test-results/aeon-655-wn'
const sizes = [390, 1024, 1440]
async function world(page: Page, theme: string) {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  for (const n of data.nodes) n.kind_slug = 'work'
  for (const n of data.nodes) {
    n.is_leaf = !data.nodes.some(c => c.parent_id === n.id)
    n.work_children_count = data.nodes.filter(c => c.parent_id === n.id).length
    let parent = data.nodes.find(p => p.id === n.parent_id), depth = 1
    while (parent) { depth++; parent = data.nodes.find(p => p.id === parent!.parent_id) }
    n.depth = depth; n.level_name = n.is_leaf ? 'Arbeitsschritt' : depth === 1 ? 'Vorhaben' : 'Geschichte'; n.level_icon = n.is_leaf ? 'check' : 'tree'; n.status_derived = !n.is_leaf
  }
  data.nodes.find(n => n.id === 'n-epic')!.title = 'Arbeitsvorhaben mit ausführlicher Beschreibung für die gemeinsame Umsetzung'
  const calls = await mockWork(page, data, { admin: true })
  await mockBusiness(page, businessData({ role: 'admin' }), { role: 'admin' })
  await mockSettings(page, settingsData())
  // Queue behavior has its own specs. An unmocked 404 introduces an unrelated
  // error row that access refresh clears during search, moving every control.
  await page.route('**/api/queue?*', route => route.fulfill({ json: { items: [], manual_order: false, capacity: { hours: null, total: 0 } } }))
  // Business fixtures freeze Date.now; Vue's event fence needs advancing time.
  await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z'))
  let vocabulary = { revision: 0, leaf: { name: 'Arbeitsschritt', icon: 'check' }, levels: [{ name: 'Arbeitsvorhaben mit ausführlicher Beschreibung', icon: 'tree' }, { name: 'Geschichte', icon: 'layers' }] }
  let fail = false
  await page.route('**/api/settings/work-vocabulary', async route => {
    if (route.request().method() === 'PUT') {
      if (fail) return route.fulfill({ status: 409, json: { error: 'Work vocabulary changed; reload before saving.' } })
      vocabulary = { ...route.request().postDataJSON(), revision: vocabulary.revision + 1 }
    }
    return route.fulfill({ json: vocabulary })
  })
  await page.route('**/api/tickets/graph?*', route => route.fulfill({ json: {
    nodes: data.nodes.filter(n => n.project === 'p-pharos').map(n => ({ ...n, type: 'work', status: n.state, status_category: n.state === 'done' ? 'done' : 'open', priority: n.fields.priority ?? null, release_id: null, link_count: 0 })),
    links: data.nodes.filter(n => n.project === 'p-pharos' && data.nodes.some(p => p.id === n.parent_id)).map(n => ({ source: n.id, target: n.parent_id, kind: 'parent' })), truncated: false,
  } }))
  return { data, calls, fail: () => { fail = true } }
}
for (const width of sizes) for (const theme of ['light', 'dark']) {
  test(`work vocabulary and surfaces ${width} ${theme}`, async ({ page }) => {
    await mkdir(shots, { recursive: true }); await page.setViewportSize({ width, height: 1000 })
    const w = await world(page, theme), errors = watchErrors(page)
    await page.goto('/settings/vocabulary')
    const screenshot = async (surface: string) => { await expect(page.locator('html')).toHaveAttribute('data-theme', theme); await page.screenshot({ path: `${shots}/${surface}-${width}-${theme}.png`, fullPage: true }) }
    const card = page.locator('#work-vocabulary')
    await expect(card.getByLabel('Leaf name')).toHaveValue('Arbeitsschritt')
    await card.scrollIntoViewIfNeeded()
    const stability = await controlStability(page, {
      actions: card.getByLabel('Vocabulary actions'), save: card.getByRole('button', { name: /Save names/ }),
      leaf: card.getByLabel('Leaf name', { exact: true }), icon: card.getByLabel('Leaf icon'), clickedRow: card.locator('.vocab-row').first(),
    })
    await stability.check(() => card.getByLabel('Leaf name', { exact: true }).fill('Schritt mit ausführlicher deutscher Beschreibung'))
    await stability.check(() => card.getByLabel('Leaf icon').selectOption('ticket'))
    await stability.check(() => card.getByRole('button', { name: /Save names/ }).click())
    await expect(card.getByRole('status')).toContainText('Workspace names saved')
    w.fail(); await stability.check(() => card.getByRole('button', { name: /Save names/ }).click())
    await expect(card.getByRole('status')).toContainText('reload before saving')
    await stability.check(() => card.getByRole('button', { name: 'Add level' }).click()); stability.done()
    await screenshot('workspace')
    await page.goto('/p/PHAROS/tickets?view=outline&closed=1')
    const outline = page.getByRole('treegrid', { name: 'Ticket outline' })
    await expect(outline.locator('.key').filter({ hasText: /^PHAROS-10$/ })).toBeVisible()
    await expect(outline.getByText('No epic', { exact: true })).toHaveCount(0)
    const outlineGuard = await controlStability(page, { actions: page.getByRole('button', { name: 'New Schritt mit ausführlicher deutscher Beschreibung', exact: true }), clickedRow: outline.locator('#row-n-epic') })
    await outlineGuard.check(() => outline.getByRole('button', { name: 'Expand PHAROS-10', exact: true }).click()); outlineGuard.done()
    await expect(outline.getByText('PHAROS-11', { exact: true })).toBeVisible()
    const rootReads = w.calls.filter(c => c.path === '/api/nodes' && c.method === 'GET' && c.query.get('parent_id') === 'p-pharos')
    expect(rootReads.length).toBeGreaterThan(0)
    expect(rootReads.some(c => c.query.get('kind') === 'epic')).toBe(false)
    await screenshot('outline')
    const create = page.getByRole('button', { name: 'New Schritt mit ausführlicher deutscher Beschreibung', exact: true })
    await create.click()
    const title = page.getByLabel('New Schritt mit ausführlicher deutscher Beschreibung title')
    await expect(title).toBeVisible()
    const creation = await controlStability(page, { title, type: page.getByRole('button', { name: 'Type: Schritt mit ausführlicher deutscher Beschreibung' }), parent: page.getByRole('button', { name: /^Parent:/ }) })
    await creation.check(() => title.fill('Ein neuer Arbeitsschritt mit ausführlichem deutschen Titel'))
    await page.getByRole('button', { name: /^Parent:/ }).click()
    const picker = page.getByRole('dialog', { name: 'Parent for the new Schritt mit ausführlicher deutscher Beschreibung' })
    const parentOption = picker.getByRole('option').filter({ hasText: 'PHAROS-10' })
    await expect(parentOption).toBeVisible()
    const pickerGuard = await controlStability(page, { search: picker.getByLabel('Find a parent'), selectors: picker.getByRole('listbox'), clickedRow: parentOption, ...(width <= 720 ? { frame: picker, cancel: picker.getByRole('button', { name: 'Cancel', exact: true }) } : {}) })
    await pickerGuard.check(() => parentOption.hover()); pickerGuard.done()
    await screenshot('parent-picker')
    await creation.check(() => parentOption.click()); creation.done()
    await screenshot('quick-create')
    await page.goto('/p/PHAROS/PHAROS-10?view=outline&closed=1')
    const panel = page.getByRole('complementary', { name: 'Ticket details' })
    await expect(panel).toBeVisible()
    await expect(panel.getByRole('button', { name: /^Status:/ })).toContainText('Follows its')
    await expect(panel.getByRole('button', { name: /Queue PHAROS-10/ })).toHaveCount(0)
    const children = panel.getByRole('region', { name: 'Children in this Vorhaben' })
    const add = children.getByRole('button', { name: 'Add Schritt mit ausführlicher deutscher Beschreibung', exact: true })
    await expect(add).toBeVisible()
    const childGuard = await controlStability(page, { add })
    await childGuard.check(() => add.hover()); childGuard.done()
    await screenshot('parent')
    await panel.getByRole('button', { name: /^Status:/ }).click()
    const explanation = page.getByRole('dialog', { name: 'Status of PHAROS-10' })
    await expect(explanation.getByRole('status')).toContainText('Follows its 2 children')
    await expect(explanation.getByRole('menuitemradio')).toHaveCount(0)
    await screenshot('parent-status')
    await page.keyboard.press('Escape')
    await page.goto('/p/PHAROS/tickets?shape=leaf&depth=2&closed=1')
    await expect(page).toHaveURL(/shape=leaf/)
    if (width <= 720) {
      await page.getByRole('button', { name: 'Filters', exact: true }).click()
      const sheet = page.getByRole('dialog', { name: 'Filters', exact: true })
      const choices = sheet.locator('.sheet-section').filter({ has: page.getByText('Parents / Leaves', { exact: true }) })
      const leaves = choices.getByRole('checkbox', { name: /Leaves/ })
      await leaves.scrollIntoViewIfNeeded()
      const filterGuard = await controlStability(page, { frame: sheet, actions: sheet.getByRole('button', { name: /^Show/ }), selectors: choices.getByRole('group'), clickedRow: choices.locator('.facet-option').filter({ has: page.getByText('Leaves', { exact: true }) }) })
      await filterGuard.check(() => leaves.uncheck()); await filterGuard.check(() => leaves.check()); filterGuard.done()
      await screenshot('filters')
      await sheet.getByRole('button', { name: /^Show/ }).click()
    } else {
      const shapeFilter = page.getByRole('button', { name: /Parents \/ Leaves/ }).first()
      await expect(shapeFilter).toBeVisible()
      const filterGuard = await controlStability(page, { shapeFilter, actions: page.getByRole('button', { name: 'New Schritt mit ausführlicher deutscher Beschreibung', exact: true }) })
      await filterGuard.check(() => shapeFilter.hover()); filterGuard.done()
      await screenshot('filters')
    }
    await page.goto('/p/PHAROS/tickets?view=graph&closed=1')
    await expect(page.getByText(/leaves.*parents.*links/).first()).toBeVisible()
    const graphSearch = page.getByRole('searchbox', { name: 'Search tickets in this project' })
    const graphGuard = await controlStability(page, { graphSearch, selectors: page.getByRole('tablist', { name: 'Ticket views' }) })
    await graphGuard.check(() => graphSearch.fill('connector'))
    await expect(page.getByText(/^0 leaves · 1 parent · 0 links$/).first()).toBeVisible()
    await graphGuard.check(() => graphSearch.fill('')); graphGuard.done()
    await expect(page.getByText(/leaves.*parents.*links/).first()).toBeVisible()
    await screenshot('graph')
    expect(errors).toEqual([])
  })
}

for (const width of sizes) for (const theme of ['light', 'dark']) for (const custom of [false, true]) {
  test(`quick creation preserves ${custom ? 'custom' : 'default'} workspace names ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const data = fixtures(); data.preferences.theme = { choice: theme }
    const calls = await mockWork(page, data), errors = watchErrors(page)
    const name = custom ? 'Arbeitsschritt mit ausführlicher deutscher Beschreibung' : 'Ticket'
    const noun = custom ? name : 'ticket'
    await page.route('**/api/settings/work-vocabulary', route => route.fulfill({ json: { revision: 0, leaf: { name: custom ? name : '', icon: custom ? 'check' : '' }, levels: [] } }))
    await page.goto('/p/PHAROS/tickets')
    await page.getByRole('button', { name: `New ${noun}`, exact: true }).click()
    const title = page.getByLabel(`New ${noun} title`, { exact: true }), type = page.getByRole('button', { name: `Type: ${name}`, exact: true })
    await expect(title).toHaveAttribute('placeholder', `${name} title`)
    await expect(type).toContainText(name)
    const stable = await controlStability(page, { title, type, status: page.getByRole('button', { name: 'Status: New', exact: true }), priority: page.getByRole('button', { name: 'Priority: No priority', exact: true }), parent: page.getByRole('button', { name: 'Parent: none', exact: true }) })
    await stable.check(() => type.click())
    const menu = page.getByRole('menu', { name: `Type of the new ${noun}`, exact: true })
    await expect(menu.getByRole('menuitemradio', { name, exact: true })).toHaveAttribute('aria-checked', 'true')
    await stable.check(() => menu.getByRole('menuitemradio', { name, exact: true }).click())
    await stable.check(() => title.fill('Workspace naming regression'))
    stable.done()
    await mkdir('test-results/aeon-648-int-fix19', { recursive: true })
    await page.screenshot({ path: `test-results/aeon-648-int-fix19/quick-${custom ? 'custom' : 'default'}-${width}-${theme}.png`, fullPage: true })
    await title.press('Enter')
    expect(calls.filter(c => c.method === 'POST' && c.path === '/api/nodes')).toHaveLength(0)
    const mac = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent))
    await title.press(mac ? 'Meta+Enter' : 'Control+Enter')
    await expect(title).toHaveValue('')
    const writes = calls.filter(c => c.method === 'POST' && c.path === '/api/nodes')
    expect(writes).toHaveLength(1)
    expect(writes[0].body).toMatchObject({ kind_id: 'k-work', parent_id: 'p-pharos', title: 'Workspace naming regression' })
    await expect(type).toHaveAttribute('aria-label', `Type: ${name}`)
    expect(errors).toEqual([])
  })
}
