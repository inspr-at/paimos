// SPDX-License-Identifier: AGPL-3.0-only
// AEON-174: every Display sort has its icon and the Display button shows the
// current one; cards are arranged into a Custom order by dragging, or with Alt and
// the arrow keys, remembered per person on the server; group labels and card
// titles keep their descenders; axe in both themes.
import { test, expect, type Locator, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork, watchErrors, type Call, type Fixtures } from './work-fixtures'
import { groupsWorld, mockProjectGroups } from './project-groups-fixtures'

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

const projects = (page: Page) => page.getByRole('list', { name: 'Projects', exact: true })
const display = (page: Page) => page.getByRole('button', { name: /^Display/ })
const panel = (page: Page) => page.getByRole('dialog', { name: 'Display options' })
const names = (page: Page) => projects(page).locator('.card-name')
const card = (page: Page, id: string) => page.locator(`.card[data-project-id="${id}"]`)
const lastOrder = (calls: Call[]) => (calls.findLast(call => call.method === 'PUT' && call.path === '/api/preferences/projects:order')?.body as { value: { ids?: string[] } } | undefined)?.value.ids
const iso = (hours: number) => new Date(Date.parse('2026-09-23T12:00:00Z') - hours * 3_600_000).toISOString()

async function open(page: Page, options: { view?: 'list' | 'cards'; groups?: Record<string, unknown>; order?: string[]; more?: boolean; path?: string } = {}) {
  const data: Fixtures = fixtures()
  if (options.more) data.projects.push(
    { id: 'p-quay', key: 'PRJ-41', title: 'Quay yard logging', state: 'active', classic: 'QUAY', description: 'Jetty, pylon and gauge upkeep.', last: iso(5) },
    { id: 'p-jig', key: 'PRJ-42', title: 'Pygmy jig catalogue', state: 'active', classic: 'JIGGY', description: 'Typography, glyphs and spacing.', last: iso(50) },
  )
  if (options.groups) data.preferences['project-groups'] = options.groups
  data.preferences['projects'] = { view: options.view ?? 'cards' }
  if (options.order) data.preferences['projects:order'] = { ids: options.order }
  const calls = await mockWork(page, data)
  await mockProjectGroups(page, data, groupsWorld())
  await page.goto(options.path ?? '/')
  await expect(projects(page)).toBeVisible()
  return { data, calls }
}
// Carries `from` over `to` with the mouse; `release: false` leaves it mid-drag.
async function drag(page: Page, from: Locator, to: Locator, release = true) {
  const a = (await from.boundingBox())!, b = (await to.boundingBox())!
  await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2)
  await page.mouse.down()
  await page.mouse.move(a.x + a.width / 2 + 14, a.y + a.height / 2 + 6, { steps: 3 })
  await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 8 })
  if (release) await page.mouse.up()
}

test('every sort has its icon, centred on its label; the Display button shows the current sort', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const errors = watchErrors(page)
  await open(page)
  await expect(display(page)).toHaveAccessibleName('Display, sorted by last activity')
  await expect(display(page)).toHaveText('Display')
  await display(page).click()
  const options = panel(page).getByRole('radio')
  await expect(options).toHaveText(['Last activity', 'Name', 'Open tickets', 'Progress', 'Custom'])
  for (const option of await options.all()) {
    await expect(option.locator('svg')).toHaveCount(1)
    const icon = (await option.locator('svg').boundingBox())!, label = (await option.locator('span').boundingBox())!, box = (await option.boundingBox())!
    expect(Math.abs(icon.y + icon.height / 2 - (label.y + label.height / 2))).toBeLessThanOrEqual(1)
    expect(Math.abs(icon.y + icon.height / 2 - (box.y + box.height / 2))).toBeLessThanOrEqual(1)
  }
  const iconOf = (locator: Locator) => locator.locator('svg').first().innerHTML()
  // Each sort draws a different icon, and the button wears the chosen one.
  expect(new Set(await Promise.all((await options.all()).map(iconOf))).size).toBe(5)
  expect(await display(page).locator('svg.sort-icon').innerHTML()).toBe(await iconOf(options.nth(0)))
  await panel(page).getByRole('radio', { name: 'Name' }).click()
  await expect(display(page)).toHaveAccessibleName('Display, sorted by name')
  expect(await display(page).locator('svg.sort-icon').innerHTML()).toBe(await iconOf(panel(page).getByRole('radio', { name: 'Name' })))
  await expect(panel(page).getByText('Drag a card, or press Alt and an arrow key, to arrange your own order.')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(display(page)).toHaveAttribute('data-tip', 'Sorted by name · sort')
  expect(errors).toEqual([])
})

test('dragging a card switches to Custom at once and keeps that order across a reload and other sorts', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const { calls } = await open(page)
  await expect(names(page)).toHaveText(['Pharos', 'Aeon', 'Studio infrastructure'])
  await drag(page, card(page, 'p-frozen'), card(page, 'p-pharos'), false)
  // Mid-drag: a lifted copy follows the pointer, the open place shows where it lands, the sort already reads Custom.
  await expect(page.locator('body > .card-ghost')).toHaveCount(1)
  await expect(page.locator('.cards-view .card.dragging')).toHaveCount(1)
  await expect(display(page)).toHaveAttribute('data-sort', 'custom')
  await expect(names(page)).toHaveText(['Studio infrastructure', 'Pharos', 'Aeon'])
  await page.mouse.up()
  await expect(page.locator('.card-ghost')).toHaveCount(0)
  await expect(page).toHaveURL('/?sort=custom')
  await expect(page.getByText('Now sorted by custom order')).toBeVisible()
  await expect(page.getByText('Studio infrastructure moved to position 1 of 3')).toBeAttached()
  await expect.poll(() => lastOrder(calls)).toEqual(['p-frozen', 'p-pharos', 'p-aeon', 'p-glint'])
  // The order has its own key: the page's other settings were not rewritten.
  expect(calls.filter(call => call.method === 'PUT' && call.path === '/api/preferences/projects')).toEqual([])
  // The drop opened nothing.
  await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toBeVisible()
  await page.reload()
  await expect(names(page)).toHaveText(['Studio infrastructure', 'Pharos', 'Aeon'])
  await expect(display(page)).toHaveAccessibleName('Display, sorted by custom order')
  // Another sort leaves the custom order for later.
  await display(page).click()
  await panel(page).getByRole('radio', { name: 'Name' }).click()
  await expect(names(page)).toHaveText(['Aeon', 'Pharos', 'Studio infrastructure'])
  await panel(page).getByRole('radio', { name: 'Custom' }).click()
  await expect(names(page)).toHaveText(['Studio infrastructure', 'Pharos', 'Aeon'])
  await page.keyboard.press('Escape')
  // The list follows the same order.
  await page.getByRole('radio', { name: 'List view' }).click()
  await expect(projects(page).locator('.name')).toHaveText(['Studio infrastructure', 'Pharos', 'Aeon'])
  await display(page).click()
  await expect(panel(page).getByText('Your own order: drag cards in Cards view, or press Alt and an arrow key.')).toBeVisible()
})

test('a new project joins after the arranged ones; undo brings the old sort and order back; Escape cancels a drag', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const { calls } = await open(page, { order: ['p-aeon', 'p-frozen'], path: '/?sort=custom' })
  await expect(names(page)).toHaveText(['Aeon', 'Studio infrastructure', 'Pharos'])
  await drag(page, card(page, 'p-pharos'), card(page, 'p-aeon'), false)
  await expect(names(page)).toHaveText(['Pharos', 'Aeon', 'Studio infrastructure'])
  await page.keyboard.press('Escape')
  await expect(names(page)).toHaveText(['Aeon', 'Studio infrastructure', 'Pharos'])
  await expect(page.locator('.card-ghost, .card.dragging')).toHaveCount(0)
  await page.mouse.up()
  expect(lastOrder(calls)).toBeUndefined()
  // From another sort, the first arrangement says so and can be undone.
  await display(page).click()
  await panel(page).getByRole('radio', { name: 'Name' }).click()
  await page.keyboard.press('Escape')
  await expect(names(page)).toHaveText(['Aeon', 'Pharos', 'Studio infrastructure'])
  await drag(page, card(page, 'p-frozen'), card(page, 'p-aeon'))
  await expect(names(page)).toHaveText(['Studio infrastructure', 'Aeon', 'Pharos'])
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(page).toHaveURL('/?sort=name')
  await expect.poll(() => lastOrder(calls)).toEqual(['p-aeon', 'p-frozen'])
  await expect(names(page)).toHaveText(['Aeon', 'Pharos', 'Studio infrastructure'])
})

test('Alt and the arrow keys move a card, announced; the card menu moves it too; the list moves with Alt up and down', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const { calls } = await open(page)
  const link = card(page, 'p-pharos').locator('.card-link')
  await expect(link).toHaveAttribute('aria-keyshortcuts', /Alt\+ArrowRight/)
  await link.focus()
  await page.keyboard.press('Alt+ArrowRight')
  await expect(names(page)).toHaveText(['Aeon', 'Pharos', 'Studio infrastructure'])
  await expect(page.locator('[aria-live="polite"].sr-only')).toHaveText('Pharos moved to position 2 of 3')
  await expect(link).toBeFocused()
  await expect(display(page)).toHaveAttribute('data-sort', 'custom')
  await expect.poll(() => lastOrder(calls)?.slice(0, 3)).toEqual(['p-aeon', 'p-pharos', 'p-frozen'])
  await page.keyboard.press('Alt+ArrowLeft')
  await expect(names(page)).toHaveText(['Pharos', 'Aeon', 'Studio infrastructure'])
  await page.keyboard.press('Alt+ArrowLeft')
  await expect(page.locator('[aria-live="polite"].sr-only')).toHaveText('Pharos is already first')
  // The actions menu: Move earlier, Move later.
  const studio = card(page, 'p-frozen')
  await studio.hover()
  await studio.getByRole('button', { name: /^Actions for/ }).click()
  await expect(page.getByRole('menuitem', { name: 'Move later' })).toBeDisabled()
  await page.getByRole('menuitem', { name: 'Move earlier' }).click()
  await expect(names(page)).toHaveText(['Pharos', 'Studio infrastructure', 'Aeon'])
  await expect(studio.locator('.card-link')).toBeFocused()
  // The list: Alt and down moves a row one place.
  await page.getByRole('radio', { name: 'List view' }).click()
  const rows = projects(page).locator('.name')
  await expect(rows).toHaveText(['Pharos', 'Studio infrastructure', 'Aeon'])
  await projects(page).getByRole('link', { name: /^PHAROS/ }).focus()
  await page.keyboard.press('Alt+ArrowDown')
  await expect(rows).toHaveText(['Studio infrastructure', 'Pharos', 'Aeon'])
})

test('with groups, a card is arranged within its own group', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const { calls } = await open(page, { more: true, groups: { groups: [{ id: 'g:pq', name: 'Paying jiggy queue' }], place: { 'p-quay': 'g:pq', 'p-jig': 'g:pq' }, hidden: [] } })
  const inGroup = page.getByRole('list', { name: 'Paying jiggy queue', exact: true }).locator('.card-name')
  await expect(inGroup).toHaveText(['Quay yard logging', 'Pygmy jig catalogue'])
  await drag(page, card(page, 'p-jig'), card(page, 'p-quay'))
  await expect(inGroup).toHaveText(['Pygmy jig catalogue', 'Quay yard logging'])
  await expect(page.getByText('Pygmy jig catalogue moved to position 1 of 2 in Paying jiggy queue')).toBeAttached()
  await expect(page.getByRole('list', { name: 'No group', exact: true }).locator('.card-name')).toHaveText(['Pharos', 'Aeon', 'Studio infrastructure'])
  await expect.poll(() => { const order = lastOrder(calls) ?? []; return order.includes('p-jig') && order.indexOf('p-jig') < order.indexOf('p-quay') }).toBe(true)
})

test('the saved order drops projects that are gone, also later; a failed save says so', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const { calls, data } = await open(page, { order: ['p-gone', 'p-aeon', 'p-aeon', 'p-frozen', 'p-pharos'], path: '/?sort=custom' })
  await expect(names(page)).toHaveText(['Aeon', 'Studio infrastructure', 'Pharos'])
  await expect.poll(() => lastOrder(calls)).toEqual(['p-aeon', 'p-frozen', 'p-pharos'])
  // Later a project is deleted elsewhere; the next time the list loads (here after an
  // archive), its place goes. The archived one keeps its place.
  data.projects.splice(data.projects.findIndex(p => p.id === 'p-frozen'), 1)
  const aeon = card(page, 'p-aeon')
  await aeon.hover()
  await aeon.getByRole('button', { name: /^Actions for/ }).click()
  await page.getByRole('menuitem', { name: 'Archive' }).click()
  await expect(names(page)).toHaveText(['Pharos'])
  await expect.poll(() => lastOrder(calls)).toEqual(['p-aeon', 'p-pharos'])
  const writes = calls.filter(call => call.method === 'PUT' && call.path === '/api/preferences/projects:order').length
  await page.route('**/api/preferences/projects%3Aorder', route => route.request().method() === 'PUT' ? route.fulfill({ status: 500, json: { error: 'down' } }) : route.fallback())
  expect(calls.filter(call => call.method === 'PUT' && call.path === '/api/preferences/projects:order').length).toBe(writes)
  await page.getByRole('group', { name: 'Groups' }).getByRole('button', { name: /^Archived/ }).click()
  await expect(names(page)).toHaveText(['Pharos', 'Aeon', 'Glint'])
  await drag(page, card(page, 'p-glint'), card(page, 'p-aeon'))
  await expect(names(page)).toHaveText(['Pharos', 'Glint', 'Aeon'])
  await expect(page.getByText('Your project order could not be saved. It stays here until you reload.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Try again' })).toBeVisible()
})

test('under Custom, a card dropped on another group lands at the end of that group as shown', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  // Shown Jiggy then Quay, although the server lists Quay first.
  const { calls } = await open(page, { more: true, order: ['p-jig', 'p-quay'], path: '/?sort=custom', groups: { groups: [{ id: 'g:pq', name: 'Paying jiggy queue' }], place: { 'p-quay': 'g:pq', 'p-jig': 'g:pq' }, hidden: [] } })
  const inGroup = page.getByRole('list', { name: 'Paying jiggy queue', exact: true }).locator('.card-name')
  await expect(inGroup).toHaveText(['Pygmy jig catalogue', 'Quay yard logging'])
  await drag(page, card(page, 'p-pharos'), page.locator('.card-section[data-group-drop="g:pq"] .group-head'))
  await expect(page.getByText('Moved Pharos to Paying jiggy queue')).toBeVisible()
  await expect(inGroup).toHaveText(['Pygmy jig catalogue', 'Quay yard logging', 'Pharos'])
  await expect(display(page)).toHaveAttribute('data-sort', 'custom')
  await expect.poll(() => lastOrder(calls)?.slice(0, 3)).toEqual(['p-jig', 'p-quay', 'p-pharos'])
})

// A synthetic press on a card, then what the browser may send instead of a release.
async function press(page: Page, id: string, then: 'cancel' | 'blur') {
  await card(page, id).locator('.card-link').evaluate((link, then) => {
    const box = link.getBoundingClientRect(), x = box.left + box.width / 2, y = box.top + box.height / 2
    const base = { bubbles: true, cancelable: true, pointerId: 7, pointerType: 'mouse', button: 0, buttons: 1 }
    link.dispatchEvent(new PointerEvent('pointerdown', { ...base, clientX: x, clientY: y }))
    if (then === 'cancel') {
      window.dispatchEvent(new PointerEvent('pointermove', { ...base, clientX: x + 40, clientY: y + 10 }))
      window.dispatchEvent(new PointerEvent('pointercancel', { ...base, clientX: x + 40, clientY: y + 10 }))
    } else window.dispatchEvent(new Event('blur'))
  }, then)
}

test('a cancelled pointer ends the drag and the next card click still opens its project', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await open(page)
  await press(page, 'p-pharos', 'cancel')
  await expect(page.locator('.card-ghost, .card.dragging')).toHaveCount(0)
  await expect(display(page)).toHaveAttribute('data-sort', 'activity')
  await expect(names(page)).toHaveText(['Pharos', 'Aeon', 'Studio infrastructure'])
  await card(page, 'p-aeon').locator('.card-link').click()
  await expect(page).toHaveURL('/p/AEON/tickets')
})

test('a blur before the drag starts forgets the press, so the next drag works', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await open(page)
  await press(page, 'p-pharos', 'blur')
  await drag(page, card(page, 'p-frozen'), card(page, 'p-pharos'))
  await expect(names(page)).toHaveText(['Studio infrastructure', 'Pharos', 'Aeon'])
  await expect(display(page)).toHaveAttribute('data-sort', 'custom')
})

// Truncating labels must leave room for descenders (g, j, p, q, y): the text's box
// stays inside the label and every clipping box around it.
// A label that clips its overflow also needs a line-height of at least 1.2 times its
// font size (a line-height of 1 cuts the ink of g and y while the text box fits)
// and no overflow of its own.
const clippedText = (page: Page, selector: string) => page.locator(selector).evaluateAll(els => els.flatMap(el => {
  const own = getComputedStyle(el)
  if (own.overflowY !== 'visible' || own.overflowX !== 'visible') {
    const line = own.lineHeight === 'normal' ? Infinity : parseFloat(own.lineHeight)
    if (line < parseFloat(own.fontSize) * 1.2 || el.scrollHeight > el.clientHeight) return [`${el.className}: ${el.textContent?.trim()} (line-height ${own.lineHeight} for ${own.fontSize})`]
  }
  const range = document.createRange(); range.selectNodeContents(el)
  const text = range.getBoundingClientRect()
  if (!text.height) return []
  for (let box: HTMLElement | null = el as HTMLElement, i = 0; box && i < 6; box = box.parentElement, i++) {
    const style = getComputedStyle(box)
    if (style.overflowX === 'visible' && style.overflowY === 'visible') continue
    const edge = box.getBoundingClientRect()
    if (text.bottom > edge.bottom + 0.5 || text.top < edge.top - 0.5) return [`${el.className}: ${el.textContent?.trim()}`]
  }
  return []
}))
for (const width of [1024, 390]) {
  for (const colorScheme of ['light', 'dark'] as const) {
    test(`descenders are never clipped at ${width}px (${colorScheme})`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 })
      await page.emulateMedia({ colorScheme })
      await open(page, { more: true, groups: { groups: [{ id: 'g:pq', name: 'Paying jiggy queue' }], place: { 'p-quay': 'g:pq', 'p-jig': 'g:pq' }, hidden: [] } })
      const labels = '.group-head .name, .group-head .count, .card-name, .card-desc, .key-badge, .state-chip, .chip-name, .chip-count'
      await expect(page.locator('.group-head .name').first()).toHaveText('Paying jiggy queue')
      expect(await clippedText(page, labels)).toEqual([])
      await page.locator('.group-head').getByRole('button', { name: /^Paying jiggy queue/ }).click()
      expect(await clippedText(page, labels)).toEqual([])
      await display(page).click()
      expect(await clippedText(page, '.sort-option span, .sort-hint')).toEqual([])
      await page.keyboard.press('Escape')
      await page.getByRole('radio', { name: 'List view' }).click()
      expect(await clippedText(page, '.group-head .name, .project-item .name, .project-desc, .key-badge')).toEqual([])
      const row = projects(page).locator('[data-project-id="p-pharos"]')
      await row.hover()
      await row.getByRole('button', { name: /^Actions for/ }).click()
      expect(await clippedText(page, '.row-menu-label')).toEqual([])
      await page.getByRole('menuitem', { name: 'Move to group…' }).click()
      await expect(page.getByRole('dialog', { name: /^Move .* to a group$/ })).toBeVisible()
      expect(await clippedText(page, '.floating .name, .floating .menu-title')).toEqual([])
    })
  }
}

for (const colorScheme of ['light', 'dark'] as const) {
  test(`axe: the Display menu with icons, and arranged cards in ${colorScheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme })
    await page.setViewportSize({ width: 1280, height: 900 })
    await open(page, { order: ['p-frozen', 'p-pharos', 'p-aeon'], path: '/?sort=custom' })
    const scan = async () => {
      const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
      expect(result.violations.filter(v => v.impact === 'serious' || v.impact === 'critical').map(v => `${v.id}: ${v.nodes.map(n => n.target.join(' ')).join(', ')}`)).toEqual([])
    }
    await expect(names(page)).toHaveText(['Studio infrastructure', 'Pharos', 'Aeon'])
    await scan()
    await display(page).click()
    await expect(panel(page).getByRole('radio', { name: 'Custom' })).toHaveAttribute('aria-checked', 'true')
    await scan()
  })
}
