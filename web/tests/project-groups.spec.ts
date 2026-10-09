// SPDX-License-Identifier: AGPL-3.0-only
// AEON-136: the Projects page with groups (personal and shared), the chip row and
// the hidden line, list columns, the card view, moving projects by keyboard, menu
// and drag and drop, with undo; count cells that line up; axe in both themes.
import { test, expect, type Locator, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork, watchErrors, type Call, type Fixtures } from './work-fixtures'
import { CLIENTS, groupsWorld, mockProjectGroups, type GroupsWorld } from './project-groups-fixtures'

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

const projects = (page: Page) => page.getByRole('list', { name: 'Projects', exact: true })
const group = (page: Page, name: string) => page.getByRole('list', { name, exact: true })
const chip = (page: Page, name: RegExp) => page.getByRole('group', { name: 'Groups' }).getByRole('button', { name })
const fold = (page: Page, name: string, expanded?: boolean) => page.locator('.group-head').getByRole('button', { name: new RegExp(`^${name}`), expanded })
const lastPut = (calls: Call[], key: string) => calls.findLast(call => call.method === 'PUT' && call.path === `/api/preferences/${key}`)?.body as { value: Record<string, unknown> } | undefined
async function open(page: Page, options: { prefs?: Record<string, unknown>; view?: 'list' | 'cards'; admin?: boolean; world?: GroupsWorld } = {}) {
  const data: Fixtures = fixtures()
  if (options.prefs) data.preferences['project-groups'] = options.prefs
  if (options.view) data.preferences['projects'] = { view: options.view }
  const calls = await mockWork(page, data, { admin: options.admin })
  const world = options.world ?? groupsWorld()
  await mockProjectGroups(page, data, world, { admin: options.admin })
  await page.goto('/')
  await expect(projects(page)).toBeVisible()
  return { data, calls, world }
}
const box = async (locator: Locator) => (await locator.boundingBox())!

test('Open, Doing and Done cells: icons hold the left edge, numbers the right, three equal columns', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const errors = watchErrors(page)
  await open(page)
  const head = page.locator('.projects-head')
  await expect(head.locator('.h-open')).toHaveText('Open')
  await expect(head.locator('.h-doing')).toHaveText('Doing')
  await expect(head.locator('.h-doing')).toHaveAttribute('data-tip', /^In progress/)
  await expect(head.locator('.h-done')).toHaveText('Done')
  await expect(projects(page).getByRole('link').first()).toHaveAttribute('aria-label', /2 in progress/)
  const widths: number[] = []
  for (const kind of ['open', 'doing', 'done']) {
    const cells = projects(page).locator(`.project-row > .stat-count.k-${kind}`)
    await expect(cells).toHaveCount(3)
    const lefts = new Set<number>(), rights = new Set<number>()
    for (const cell of await cells.all()) {
      const outer = await box(cell), icon = await box(cell.locator('svg')), number = await box(cell.locator('.stat-num'))
      // Icon at the cell's left edge, number at its right edge.
      expect(Math.abs(icon.x - outer.x)).toBeLessThanOrEqual(1)
      expect(Math.abs(number.x + number.width - (outer.x + outer.width))).toBeLessThanOrEqual(1)
      lefts.add(Math.round(icon.x)); rights.add(Math.round(number.x + number.width))
      widths.push(Math.round(outer.width))
    }
    // One straight line of icons and one of number ends, down the column.
    expect(lefts.size).toBe(1)
    expect(rights.size).toBe(1)
    // The header label starts where the icons do.
    expect(Math.abs((await box(head.locator(`.h-${kind}`))).x - [...lefts][0]!)).toBeLessThanOrEqual(1)
  }
  expect(new Set(widths).size).toBe(1)
  expect(errors).toEqual([])
})

test('m opens Move to group with type-ahead; a new name creates the group; the toast undoes the move', async ({ page }) => {
  const { data } = await open(page)
  await page.keyboard.press('j')
  await expect(projects(page).getByRole('link', { name: /^PHAROS/ })).toBeFocused()
  await page.keyboard.press('m')
  const dialog = page.getByRole('dialog', { name: 'Move Pharos to a group' })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByRole('combobox', { name: 'Find or name a group' })).toBeFocused()
  await page.keyboard.type('Focus')
  await expect(dialog.getByRole('option', { name: 'Create group “Focus”' })).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('Enter')
  await expect(dialog).toHaveCount(0)
  await expect(group(page, 'Focus').getByRole('link')).toHaveText(/PHAROS/)
  await expect(group(page, 'No group').getByRole('link')).toHaveCount(2)
  await expect(page.getByText('Moved Pharos to Focus')).toBeVisible()
  // Focus followed the project to its new group.
  await expect(group(page, 'Focus').getByRole('link')).toBeFocused()
  await expect.poll(() => data.preferences['project-groups']).toMatchObject({ groups: [{ name: 'Focus' }], place: { 'p-pharos': expect.stringMatching(/^g:/) } })
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(group(page, 'No group').getByRole('link')).toHaveCount(3)
  await expect(page.getByText('No projects here yet.')).toBeVisible()
  await expect.poll(() => (data.preferences['project-groups'] as { place?: Record<string, string> } | undefined)?.place ?? {}).toEqual({})
})

test('chips show and hide groups; hidden groups fold into one quiet line', async ({ page }) => {
  const { calls } = await open(page, { prefs: { groups: [{ id: 'g:paused', name: 'Paused' }], place: { 'p-aeon': 'g:paused' } } })
  await expect(group(page, 'Paused').getByRole('link')).toHaveText(/AEON/)
  const paused = chip(page, /^Paused, 1 project$/)
  await expect(paused).toHaveAttribute('aria-pressed', 'true')
  await expect(paused.locator('.marker')).not.toHaveClass(/hollow/)
  await paused.click()
  await expect(paused).toHaveAttribute('aria-pressed', 'false')
  await expect(paused.locator('.marker')).toHaveClass(/hollow/)
  await expect(group(page, 'Paused')).toHaveCount(0)
  const line = page.locator('.hidden-line')
  await expect(line).toHaveText(/Hidden:\s*Paused 1\s*·\s*Archived 1\s*·\s*show/)
  await expect.poll(() => lastPut(calls, 'project-groups')?.value.hidden).toEqual(['archived', 'g:paused'])
  await line.getByRole('button', { name: 'Show Paused, 1 project' }).click()
  await expect(group(page, 'Paused')).toBeVisible()
  await expect(line).toHaveText(/Hidden:\s*Archived 1\s*·\s*show/)
  await line.getByRole('button', { name: 'Show Archived', exact: true }).click()
  await expect(group(page, 'Archived').getByRole('link')).toHaveText(/GLINT/)
  await expect(line).toHaveCount(0)
})

test('a group header folds, renames in place, hides, reorders by keyboard and deletes with undo', async ({ page }) => {
  const { calls } = await open(page, { prefs: { groups: [{ id: 'g:paused', name: 'Paused' }, { id: 'g:focus', name: 'Focus' }], place: { 'p-aeon': 'g:paused', 'p-pharos': 'g:focus' } } })
  await fold(page, 'Paused', true).click()
  await expect(fold(page, 'Paused', false)).toBeVisible()
  await expect(group(page, 'Paused')).toHaveCount(0)
  await fold(page, 'Paused').click()
  await expect(group(page, 'Paused').getByRole('link')).toHaveCount(1)
  // Alt and the arrow keys move the group; focus stays on it.
  await fold(page, 'Paused').focus()
  await page.keyboard.press('Alt+ArrowDown')
  await expect(page.locator('.group-head .name')).toHaveText(['Focus', 'Paused', 'No group'])
  await expect(fold(page, 'Paused')).toBeFocused()
  await expect.poll(() => (lastPut(calls, 'project-groups')?.value.order as string[] | undefined)?.slice(0, 2)).toEqual(['g:focus', 'g:paused'])
  // Rename in place.
  await page.getByRole('button', { name: 'Actions for group Paused' }).click()
  await page.getByRole('menuitem', { name: 'Rename' }).click()
  const input = page.getByRole('textbox', { name: 'Rename Paused' })
  await expect(input).toBeFocused()
  await input.fill('Focus')
  await input.press('Enter')
  await expect(page.getByRole('alert')).toHaveText('There is already a group called “Focus”.')
  await input.fill('Later')
  await input.press('Enter')
  await expect(group(page, 'Later')).toBeVisible()
  // Hide from the menu; the toast shows it again.
  await page.getByRole('button', { name: 'Actions for group Later' }).click()
  await page.getByRole('menuitem', { name: 'Hide' }).click()
  await expect(group(page, 'Later')).toHaveCount(0)
  await page.getByRole('button', { name: 'Show', exact: true }).click()
  await expect(group(page, 'Later')).toBeVisible()
  // Delete: its projects go to No group; Undo brings the group and them back.
  await page.getByRole('button', { name: 'Actions for group Later' }).click()
  await page.getByRole('menuitem', { name: 'Delete group' }).click()
  await expect(page.getByText('Deleted Later · 1 project moved to No group')).toBeVisible()
  await expect(group(page, 'No group').getByRole('link', { name: /^AEON/ })).toBeVisible()
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(group(page, 'Later').getByRole('link', { name: /^AEON/ })).toBeVisible()
  await expect(page.getByText('Later is back')).toBeVisible()
})

test('columns: choose and reorder them, remembered per person, and back to the default', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { calls } = await open(page)
  const head = page.locator('.projects-head > span')
  await expect(head).toHaveText(['Project', 'Open', 'Doing', 'Done', 'Progress', 'Last activity', ''])
  await page.getByRole('button', { name: /^Display/ }).click()
  const panel = page.getByRole('dialog', { name: 'Display options' })
  await expect(panel.getByRole('checkbox', { name: 'Key (always shown)' })).toBeDisabled()
  await panel.getByRole('checkbox', { name: 'People' }).check()
  await panel.getByRole('checkbox', { name: 'Progress' }).uncheck()
  await expect(head).toHaveText(['Project', 'Open', 'Doing', 'Done', 'People', 'Last activity', ''])
  await panel.getByRole('checkbox', { name: 'Open' }).focus()
  await page.keyboard.press('Alt+ArrowDown')
  await expect(panel.getByRole('checkbox', { name: 'Open' })).toBeFocused()
  await expect(head).toHaveText(['Project', 'Doing', 'Open', 'Done', 'People', 'Last activity', ''])
  await expect.poll(() => lastPut(calls, 'projects')?.value).toEqual({ columns: { order: ['doing', 'open', 'done', 'progress', 'people', 'activity'], visible: ['open', 'doing', 'done', 'people', 'activity'] } })
  // The people most recently active show in their column, named for screen readers.
  await expect(projects(page).getByRole('link', { name: /^PHAROS/ }).locator('.people-cell')).toContainText('Recently active: Mira Holm, Markus Barta')
  // Remembered: a fresh load keeps them.
  await page.reload()
  await expect(head).toHaveText(['Project', 'Doing', 'Open', 'Done', 'People', 'Last activity', ''])
  await page.getByRole('button', { name: /^Display/ }).click()
  await page.getByRole('dialog', { name: 'Display options' }).getByRole('button', { name: 'Default' }).click()
  await expect(head).toHaveText(['Project', 'Open', 'Doing', 'Done', 'Progress', 'Last activity', ''])
})

test('cards: the switch is remembered; a card shows its ring, aligned counts, people and last activity', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { calls } = await open(page)
  await page.getByRole('radio', { name: 'Cards view' }).click()
  await expect(page.getByRole('radio', { name: 'Cards view' })).toHaveAttribute('aria-checked', 'true')
  await expect.poll(() => lastPut(calls, 'projects')?.value).toEqual({ view: 'cards' })
  const card = projects(page).locator('.card').filter({ hasText: 'PHAROS' })
  await expect(card.locator('.ring .pct')).toHaveText('17%')
  await expect(card.locator('.counts .stat-label')).toHaveText(['Open', 'Doing', 'Done'])
  await expect(card.locator('.counts .stat-num')).toHaveText(['3', '2', '1'])
  await expect(card.locator('.people')).toHaveAttribute('data-tip', 'Recently active: Mira Holm, Markus Barta')
  await expect(card.locator('time')).toHaveText('2 hours ago')
  await expect(card.getByRole('link')).toHaveAttribute('href', '/p/PHAROS')
  // The icons of the three counts form one vertical line; the numbers end on one.
  const icons = await card.locator('.counts svg').evaluateAll(els => els.map(el => Math.round(el.getBoundingClientRect().left)))
  const ends = await card.locator('.counts .stat-num').evaluateAll(els => els.map(el => Math.round(el.getBoundingClientRect().right)))
  expect(new Set(icons).size).toBe(1)
  expect(new Set(ends).size).toBe(1)
  await page.reload()
  await expect(projects(page).locator('.card')).toHaveCount(3)
  // The … menu is the list's.
  await card.hover()
  await card.getByRole('button', { name: 'Actions for PHAROS Pharos' }).click()
  await expect(page.getByRole('menu', { name: 'Project Pharos' }).getByRole('menuitem')).toHaveText(['OpenEnter', 'Move to group…m', 'Selectx', 'Move earlierIt is already first.', 'Move later', 'Copy link', 'Archive'])
})

test('cards: arrows walk the cards; x, Shift and Command clicks select; m moves the selection; undo', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { data } = await open(page, { view: 'cards', prefs: { groups: [{ id: 'g:paused', name: 'Paused' }] } })
  const links = group(page, 'No group').locator('.card-link')
  await expect(links).toHaveCount(3)
  await links.nth(0).focus()
  await page.keyboard.press('ArrowRight')
  await expect(links.nth(1)).toBeFocused()
  await page.keyboard.press('ArrowLeft')
  await expect(links.nth(0)).toBeFocused()
  await page.keyboard.press('x')
  await expect(links.nth(0)).toHaveAttribute('aria-label', /, selected$/)
  await links.nth(2).click({ modifiers: ['ControlOrMeta'] })
  await expect(page).toHaveURL('/')
  const bar = page.getByRole('toolbar', { name: '2 selected projects' })
  await expect(bar).toBeVisible()
  await page.keyboard.press('m')
  const dialog = page.getByRole('dialog', { name: 'Move 2 projects to a group' })
  await expect(dialog).toBeVisible()
  await page.keyboard.type('pau')
  await page.keyboard.press('Enter')
  await expect(page.getByText('Moved 2 projects to Paused')).toBeVisible()
  await expect(group(page, 'Paused').locator('.card')).toHaveCount(2)
  await expect(bar).toHaveCount(0)
  await expect.poll(() => data.preferences['project-groups']?.place).toEqual({ 'p-pharos': 'g:paused', 'p-frozen': 'g:paused' })
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(group(page, 'No group').locator('.card')).toHaveCount(3)
  await expect.poll(() => (data.preferences['project-groups'] as { place?: Record<string, string> } | undefined)?.place ?? {}).toEqual({})
  // Shift-click selects a range from the last selected card; Escape clears.
  await links.nth(0).click({ modifiers: ['ControlOrMeta'] })
  await links.nth(2).click({ modifiers: ['Shift'] })
  await expect(page.getByRole('toolbar', { name: '3 selected projects' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('toolbar', { name: /selected projects/ })).toHaveCount(0)
})

test('drag and drop moves rows and cards between groups; dropping on a chip works too', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await open(page, { prefs: { groups: [{ id: 'g:paused', name: 'Paused' }] } })
  // A drop where the project already is does nothing.
  await projects(page).locator('[data-project-id="p-aeon"]').dragTo(page.locator('.group-section[data-group-drop="none"] .group-head'))
  await expect(page.locator('.toast')).toHaveCount(0)
  await projects(page).locator('[data-project-id="p-pharos"]').dragTo(page.locator('.group-section[data-group-drop="g:paused"]'))
  await expect(group(page, 'Paused').getByRole('link')).toHaveText(/PHAROS/)
  await expect(page.getByText('Moved Pharos to Paused')).toBeVisible()
  await page.getByRole('radio', { name: 'Cards view' }).click()
  await projects(page).locator('[data-project-id="p-aeon"]').dragTo(chip(page, /^Paused/))
  await expect(group(page, 'Paused').locator('.card')).toHaveCount(2)
  // Groups reorder by dragging their header.
  await page.locator('.group-head[data-group-handle="none"]').dragTo(page.locator('.group-head[data-group-handle="g:paused"]'), { targetPosition: { x: 20, y: 4 } })
  await expect(page.locator('.group-head .name')).toHaveText(['No group', 'Paused'])
})

test('archive and restore from the row menu, each undoable', async ({ page }) => {
  const { world } = await open(page)
  const row = projects(page).locator('[data-project-id="p-pharos"]')
  await row.hover()
  await row.getByRole('button', { name: 'Actions for PHAROS Pharos' }).click()
  await page.getByRole('menuitem', { name: 'Archive' }).click()
  await expect(page.getByText('Archived Pharos (hidden)')).toBeVisible()
  await expect(projects(page).getByRole('link', { name: /^PHAROS/ })).toHaveCount(0)
  expect(world.calls.find(call => call.method === 'PATCH')?.body).toEqual({ state: 'archived' })
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(projects(page).getByRole('link', { name: /^PHAROS/ })).toBeVisible()
  await expect.poll(() => world.calls.filter(call => call.method === 'PATCH').at(-1)?.body).toEqual({ state: 'active' })
  // Restoring an archived project brings it back to its group.
  await chip(page, /^Archived/).click()
  const glint = group(page, 'Archived').locator('[data-project-id="p-glint"]')
  await glint.hover()
  await glint.getByRole('button', { name: 'Actions for GLINT Glint' }).click()
  await page.getByRole('menuitem', { name: 'Restore from the archive' }).click()
  await expect(page.getByText('Restored Glint to No group')).toBeVisible()
  // With no groups and nothing archived, the list is plain again.
  await expect(projects(page).getByRole('link', { name: /^GLINT/ })).toBeVisible()
  await expect(page.locator('.group-head')).toHaveCount(0)
})

test('admins share a group with the workspace and can undo it; others see it and cannot add to it', async ({ page }) => {
  const { world } = await open(page, { admin: true, prefs: { groups: [{ id: 'g:focus', name: 'Focus' }], place: { 'p-pharos': 'g:focus' } } })
  await page.getByRole('button', { name: 'Actions for group Focus' }).click()
  await page.getByRole('menuitem', { name: 'Share with the workspace' }).click()
  await expect(page.getByText('Focus is shared with the workspace')).toBeVisible()
  expect(world.calls.find(call => call.method === 'POST' && call.path === '/api/project-groups')?.body).toEqual({ name: 'Focus', project_ids: ['p-pharos'], position: 0 })
  await expect(fold(page, 'Focus.*shared with the workspace')).toBeVisible()
  await expect(group(page, 'Focus').getByRole('link')).toHaveText(/PHAROS/)
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect.poll(() => world.calls.some(call => call.path === '/api/events/5000/undo')).toBe(true)
  await expect(fold(page, 'Focus.*shared')).toHaveCount(0)
  await expect(group(page, 'Focus').getByRole('link')).toHaveText(/PHAROS/)
})

test('a member sees shared groups, may not add to them, and has no Share or Delete', async ({ page }) => {
  await open(page, { world: groupsWorld({ clients: ['p-aeon'] }) })
  await expect(group(page, 'Clients').getByRole('link')).toHaveText(/AEON/)
  await page.getByRole('button', { name: 'Actions for group Clients' }).click()
  const menu = page.getByRole('menu', { name: 'Group Clients' })
  await expect(menu.getByRole('menuitem', { name: 'Share with the workspace' })).toHaveCount(0)
  await expect(menu.getByRole('menuitem', { name: /Delete group/ })).toHaveAttribute('aria-disabled', 'true')
  await page.keyboard.press('Escape')
  await projects(page).getByRole('link', { name: /^PHAROS/ }).focus()
  await page.keyboard.press('m')
  const option = page.getByRole('option', { name: /Clients/ })
  await expect(option).toHaveAttribute('aria-disabled', 'true')
  await expect(option).toHaveAttribute('aria-description', 'Only a workspace admin can add projects to a shared group.')
  await option.hover()
  await expect(page.locator('.option-note')).toHaveText('Only a workspace admin can add projects to a shared group.')
})

test('a server without shared groups still has personal ones', async ({ page }) => {
  const errors = watchErrors(page)
  await open(page, { admin: true, world: groupsWorld({ available: false }), prefs: { groups: [{ id: 'g:focus', name: 'Focus' }] } })
  await expect(fold(page, 'Focus')).toBeVisible()
  await page.getByRole('button', { name: 'Actions for group Focus' }).click()
  await expect(page.getByRole('menuitem', { name: 'Share with the workspace' })).toHaveCount(0)
  expect(errors).toEqual([])
})

test('the chip row makes new groups; names are checked', async ({ page }) => {
  await open(page)
  await page.getByRole('button', { name: 'Group', exact: true }).click()
  const panel = page.getByRole('dialog', { name: 'New group' })
  await panel.getByRole('textbox', { name: 'Group name' }).fill('no group')
  await panel.getByRole('button', { name: 'Create group' }).click()
  await expect(panel.getByRole('alert')).toHaveText('There is already a group called “no group”.')
  await panel.getByRole('textbox', { name: 'Group name' }).fill('Clients')
  await page.keyboard.press('Enter')
  await expect(panel).toHaveCount(0)
  await expect(fold(page, 'Clients', true)).toBeFocused()
  await expect(chip(page, /^Clients, 0 projects$/)).toBeVisible()
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`axe: grouped list, cards and the move dialog in ${colorScheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme })
    await page.setViewportSize({ width: 1280, height: 900 })
    await open(page, { admin: true, world: groupsWorld({ clients: ['p-aeon'] }), prefs: { groups: [{ id: 'g:focus', name: 'Focus' }], place: { 'p-pharos': 'g:focus' }, hidden: [] } })
    const scan = async () => {
      const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
      expect(result.violations.filter(v => v.impact === 'serious' || v.impact === 'critical').map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
    }
    await scan()
    await page.getByRole('radio', { name: 'Cards view' }).click()
    await expect(projects(page).locator('.card')).toHaveCount(4)
    await scan()
    await projects(page).locator('.card-link').first().focus()
    await page.keyboard.press('x')
    await page.keyboard.press('m')
    await expect(page.getByRole('dialog', { name: /^Move .* to a group$/ })).toBeVisible()
    await scan()
  })
}

for (const colorScheme of ['light', 'dark'] as const) {
  test(`390: cards reflow to one column and nothing is clipped (${colorScheme})`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.emulateMedia({ colorScheme })
    await open(page, { view: 'cards', prefs: { groups: [{ id: 'g:focus', name: 'Focus' }], place: { 'p-pharos': 'g:focus' }, hidden: [] } })
    const cards = projects(page).locator('.card')
    await expect(cards).toHaveCount(4)
    const boxes = await cards.evaluateAll(els => els.map(el => { const r = el.getBoundingClientRect(); return { x: Math.round(r.left), right: Math.round(r.right) } }))
    expect(new Set(boxes.map(b => b.x)).size).toBe(1)
    for (const b of boxes) expect(b.right).toBeLessThanOrEqual(390 - 12)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    // Nothing inside a card spills over its edge.
    const spills = await cards.evaluateAll(els => els.flatMap(card => {
      const edge = card.getBoundingClientRect()
      return [...card.querySelectorAll<HTMLElement>('.key-badge, .card-name, .card-desc, .counts, .card-foot, .ring')].filter(el => el.getBoundingClientRect().right > edge.right + 0.5).map(el => el.className)
    }))
    expect(spills).toEqual([])
    await page.getByRole('radio', { name: 'List view' }).click()
    await expect(projects(page).getByRole('link')).toHaveCount(4)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await expect(projects(page).getByRole('link', { name: /^PHAROS/ }).locator('.stats-line')).toHaveText(/3\s*open\s*2\s*doing\s*1\s*done/)
  })
}

test('shared group from the server lands in its own section, and members keep a personal placement', async ({ page }) => {
  await open(page, { world: groupsWorld({ clients: ['p-aeon', 'p-pharos'] }), prefs: { groups: [{ id: 'g:mine', name: 'Mine' }], place: { 'p-pharos': 'g:mine' } } })
  await expect(group(page, 'Clients').getByRole('link')).toHaveText(/AEON/)
  await expect(group(page, 'Mine').getByRole('link')).toHaveText(/PHAROS/)
  // Returning a project to its shared group is allowed for anyone.
  await projects(page).getByRole('link', { name: /^PHAROS/ }).focus()
  await page.keyboard.press('m')
  await page.getByRole('option', { name: /Clients/ }).click()
  await expect(group(page, 'Clients').getByRole('link')).toHaveCount(2)
  expect(CLIENTS).toBeTruthy()
})
