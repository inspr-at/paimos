// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1028: the project header's collapsed view keeps Display beside New, and
// that menu carries what the collapsed header hides. Risks: a section, saved view
// or Hide closed that cannot be reached once the header is folded; a menu that
// scrolls, or a control that moves when the header folds or an option is chosen;
// the other two header views changing; the removed ticket count coming back.
import { mkdirSync } from 'node:fs'
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork, mockView } from './work-fixtures'
import { expectStableControls } from './helpers/stable'

const shots = 'test-results/aeon-1028-projheader'
const VIEW = '44444444-4444-4444-8444-444444444444'
const errors = new WeakMap<Page, string[]>()
test.beforeEach(async ({ page }) => {
  const failures: string[] = []; errors.set(page, failures)
  page.on('pageerror', error => failures.push(error.message))
  await page.route('**/api/queue?*', route => route.fulfill({ json: { items: [], manual_order: false, capacity: { queued_hours: 0, parallel_runs: 0, work_hours: 0, warning: false } } }))
})
test.afterEach(async ({ page }) => { expect(errors.get(page), 'no browser runtime errors').toEqual([]) })

const toolbar = (page: Page) => page.getByRole('toolbar', { name: 'Ticket list controls' })
const display = (page: Page) => toolbar(page).getByRole('button', { name: /^Display:/ })
const create = (page: Page) => toolbar(page).getByRole('button', { name: 'New ticket', exact: true })
const search = (page: Page) => page.getByRole('searchbox', { name: 'Search tickets in this project' })
const menu = (page: Page) => page.getByRole('dialog', { name: 'Display options' })
const density = (page: Page, name: string) => page.getByRole('radio', { name: `${name} project header`, exact: true })
const projectPage = (page: Page) => page.locator('.project-page')
const mac = (page: Page) => page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent))
async function settled(page: Page) { await page.evaluate(async () => { await document.fonts.ready; await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))) }) }
async function capture(page: Page, name: string) { await settled(page); mkdirSync(shots, { recursive: true }); await page.screenshot({ path: `${shots}/${name}.png` }) }

// Without a live stream the footer says updates are paused instead of counting.
const connectedStream = (page: Page) => page.addInitScript(() => {
  class Stream extends EventTarget {
    readyState = 1
    onopen: (() => void) | null = null
    onerror: (() => void) | null = null
    close() { this.readyState = 2 }
    constructor(public url: string) {
      super()
      queueMicrotask(() => { this.onopen?.(); this.dispatchEvent(new MessageEvent('stream.ready', { data: JSON.stringify({ after: 40, resumed: false }), lastEventId: '40' })) })
    }
  }
  Object.assign(window, { EventSource: Stream })
})

// Long German names, a shared view and a filtered list: the longest realistic bar.
async function open(page: Page, width: number, theme = 'light', height = 900, url = '/p/PHAROS?status=new,backlog,open,blocked,in_progress&type=ticket', extraViews = 0) {
  await connectedStream(page)
  await page.setViewportSize({ width, height })
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.preferences['list:display'] = { density: 'comfortable', headerGraph: false }
  data.views.push(mockView({ id: VIEW, name: 'Laufende Betriebsprüfung und Berechtigungsverwaltung', shared: true }))
  for (let i = 0; i < extraViews; i++) data.views.push(mockView({ id: `aaaaaaaa-aaaa-4aaa-8aaa-${String(i).padStart(12, '0')}`, name: `Saved view ${String(i + 1).padStart(2, '0')} Betriebsprüfung` }))
  await mockWork(page, data)
  await page.goto(url)
  await expect(projectPage(page)).toHaveClass(/header-compact/)
  await expect(search(page)).toBeVisible()
  await settled(page)
}
// The phone folds the header with the crumb button; wider windows with the header switch.
async function fold(page: Page, width: number, to: 'collapsed' | 'compact' | 'comfortable' = 'collapsed') {
  if (width <= 600) { if (await projectPage(page).evaluate((el, name) => el.classList.contains(`header-${name}`), to)) return; await page.locator('.phone-header-fold').click() }
  else await density(page, to === 'collapsed' ? 'Collapsed' : to === 'compact' ? 'Compact' : 'Comfortable').click()
  await expect(projectPage(page)).toHaveClass(new RegExp(`header-${to}`))
  await settled(page)
}
async function openMenu(page: Page) {
  await display(page).click()
  await expect(menu(page)).toBeVisible()
  // The menu counts its columns when it opens, before it is placed.
  await expect(menu(page).locator('.menu-body')).toHaveAttribute('data-columns', /\d/)
  await settled(page)
}

// Each control's box inside the toolbar: folding the header moves the whole bar
// up or down, but nothing may move within it (AEON-541). Page coordinates cannot
// say that, so these are measured from the toolbar's own corner.
async function within(container: Locator, controls: Record<string, Locator>) {
  const frame = (await container.boundingBox())!
  const result: Record<string, { x: number; y: number; width: number; height: number }> = {}
  for (const [name, control] of Object.entries(controls)) {
    await expect(control, `${name} is shown`).toBeVisible()
    const rect = (await control.boundingBox())!
    expect(rect.width, `${name} has width`).toBeGreaterThan(0); expect(rect.height, `${name} has height`).toBeGreaterThan(0)
    result[name] = { x: rect.x - frame.x, y: rect.y - frame.y, width: rect.width, height: rect.height }
  }
  return result
}
async function stillWithin(page: Page, container: Locator, controls: Record<string, Locator>, steps: { name: string; run: () => Promise<unknown> }[]) {
  expect(steps.length).toBeGreaterThan(0)
  const before = await within(container, controls)
  for (const step of steps) {
    await step.run(); await settled(page)
    const after = await within(container, controls)
    for (const name of Object.keys(before)) for (const axis of ['x', 'y', 'width', 'height'] as const) {
      expect(Math.abs(after[name]![axis] - before[name]![axis]), `${step.name}: ${name}.${axis} (${before[name]![axis]} → ${after[name]![axis]})`).toBeLessThanOrEqual(.5)
    }
  }
}

test.describe('the All tickets pill', () => {
  for (const width of [1440, 400]) {
    test(`ends with the same room as it starts with at ${width}px`, async ({ page }) => {
      await open(page, width)
      const pill = page.locator('.view-bar .view-tab.plain')
      await expect(pill).toBeVisible()
      const { left, right } = await pill.evaluate(el => { const style = getComputedStyle(el); return { left: parseFloat(style.paddingLeft), right: parseFloat(style.paddingRight) } })
      expect(right, 'right padding').toBeGreaterThanOrEqual(12)
      expect(Math.abs(right - left), 'matches the left padding').toBeLessThanOrEqual(2)
      // The words end well inside the pill, not at its edge.
      const room = await pill.evaluate(el => el.getBoundingClientRect().right - el.querySelector('.name')!.getBoundingClientRect().right)
      expect(room).toBeGreaterThanOrEqual(12)
      // A saved view keeps its own options button at the end: the plain list's padding change leaves it alone.
      const saved = page.locator('.view-bar .tab .view-tab').first()
      await expect(saved).toBeVisible()
      expect(await saved.evaluate(el => parseFloat(getComputedStyle(el).paddingRight))).toBeLessThanOrEqual(4)
    })
  }
})

test.describe('the ticket count', () => {
  for (const [width, modes] of [[1440, ['comfortable', 'compact', 'collapsed']], [400, ['compact', 'collapsed']]] as const) {
    test(`is gone from the toolbar in every header view at ${width}px; the footer still says it`, async ({ page }) => {
      await open(page, width)
      for (const mode of modes) {
        await fold(page, width, mode)
        await expect(toolbar(page).locator('.count-live'), `${mode}: no count slot`).toHaveCount(0)
        await expect(toolbar(page), `${mode}: no count text`).not.toContainText(/\d+ tickets?/)
        await expect(page.locator('footer.app-footer .sum'), `${mode}: the footer keeps the count`).toHaveAttribute('aria-label', /\d+ tickets?/)
      }
    })
  }
  test('is gone from the Outline and Graph toolbars too', async ({ page }) => {
    await open(page, 1440)
    for (const view of ['outline', 'graph']) {
      await page.goto(`/p/PHAROS?view=${view}`)
      await expect(toolbar(page).getByRole('tab', { name: view === 'outline' ? 'Outline' : 'Graph', selected: true })).toBeVisible()
      await expect(toolbar(page).locator('.count-live')).toHaveCount(0)
      await expect(toolbar(page)).not.toContainText(/\d+ tickets?/)
    }
  })
})

test.describe('collapsed header: Display beside New', () => {
  for (const [width, height] of [[1440, 900], [400, 800]] as const) {
    for (const theme of ['light', 'dark']) {
      test(`shows Display next to New, with the switch and the filters still there, at ${width}px ${theme}`, async ({ page }) => {
        await open(page, width, theme, height)
        await expect(display(page), 'compact header: the toolbar shows no Display of its own').toHaveCount(0)
        await fold(page, width)
        await expect(page.locator('#project-header-fold')).toBeHidden()
        await expect(display(page)).toBeVisible()
        const shown = (await display(page).boundingBox())!, added = (await create(page).boundingBox())!
        // Immediately left of New, on the same line.
        expect(shown.x + shown.width, 'Display ends before New starts').toBeLessThanOrEqual(added.x)
        expect(added.x - (shown.x + shown.width), 'nothing between them').toBeLessThanOrEqual(16)
        expect(Math.abs(shown.y + shown.height / 2 - (added.y + added.height / 2)), 'same line').toBeLessThanOrEqual(1)
        if (width <= 600) { expect(shown.width).toBeGreaterThanOrEqual(44); expect(shown.height).toBeGreaterThanOrEqual(44) }
        // The List · Outline · Graph switch and the filter row stay as they were.
        await expect(toolbar(page).getByRole('tablist', { name: 'Ticket views' })).toBeVisible()
        for (const name of ['List', 'Outline', 'Graph']) await expect(toolbar(page).getByRole('tab', { name })).toBeVisible()
        if (width > 600) for (const name of ['Status', 'Priority', 'Assignee']) await expect(toolbar(page).locator(`.facet-btn[data-dim="${name.toLowerCase()}"]`)).toBeVisible()
        else await expect(toolbar(page).getByRole('button', { name: 'Filters', exact: true })).toBeVisible()
        await capture(page, `${width}-${theme}-collapsed`)
        await openMenu(page)
        await capture(page, `${width}-${theme}-collapsed-menu`)
      })
    }
  }

  test('stays beside New in the Graph view, where New is absent', async ({ page }) => {
    await open(page, 1440)
    await fold(page, 1440)
    await page.goto('/p/PHAROS?view=graph')
    await expect(toolbar(page).getByRole('tab', { name: 'Graph', selected: true })).toBeVisible()
    await expect(display(page)).toBeVisible()
    await openMenu(page)
    await expect(menu(page).getByRole('navigation', { name: 'Project sections' })).toBeVisible()
    // The list-only options do not apply to the graph; the header's own size does.
    await expect(menu(page).getByRole('radiogroup', { name: 'Group by' })).toHaveCount(0)
    await expect(menu(page).getByRole('radiogroup', { name: 'Project header density' })).toBeVisible()
  })

  test('stays on the toolbar at tablet width, where the header bar has no Display', async ({ page }) => {
    await open(page, 800)
    await fold(page, 800)
    await expect(display(page)).toBeVisible()
    const shown = (await display(page).boundingBox())!, added = (await create(page).boundingBox())!
    expect(added.x - (shown.x + shown.width)).toBeLessThanOrEqual(16)
    await openMenu(page)
    // A narrow window gets the sheet, not columns.
    await expect(menu(page).locator('.display-menu.as-sheet')).toBeVisible()
  })
})

test.describe('collapsed header: what the menu carries', () => {
  test('lists the sections, then the saved views with Hide closed and its gear, then the usual options', async ({ page }) => {
    await open(page, 1440)
    await fold(page, 1440)
    await openMenu(page)
    const sections = menu(page).getByRole('navigation', { name: 'Project sections' })
    await expect(sections.locator(':scope > *')).toHaveText(['Tickets', 'Knowledge', 'Settings', 'Needs attention'])
    await expect(sections.getByRole('button', { name: 'Tickets' })).toHaveAttribute('aria-current', 'page')
    const views = menu(page).getByRole('navigation', { name: 'Saved views' })
    await expect(views.getByRole('link', { name: 'All tickets' })).toHaveAttribute('aria-current', 'page')
    await expect(views.getByRole('link', { name: /Laufende Betriebsprüfung/ })).toBeVisible()
    await expect(menu(page).getByRole('checkbox', { name: /^Hide / })).toBeChecked()
    await expect(menu(page).getByRole('button', { name: 'Choose what Hide hides' })).toBeVisible()
    for (const name of ['Group by', 'Row height', 'Effort meter']) await expect(menu(page).getByRole('radiogroup', { name })).toBeVisible()
    await expect(menu(page).getByRole('list', { name: 'Columns' })).toBeVisible()
    // Top to bottom, in that order.
    const ordered = await menu(page).evaluate(el => {
      const marks = ['nav[aria-label="Project sections"]', 'nav[aria-label="Saved views"]', '.menu-hide', '[role="radiogroup"][aria-label="Group by"]', '.sort-editor', '[role="radiogroup"][aria-label="Row height"]', '.columns']
      const nodes = marks.map(selector => el.querySelector(selector))
      return nodes.map((node, index) => !!node && (index === 0 || !!(nodes[index - 1]!.compareDocumentPosition(node) & Node.DOCUMENT_POSITION_FOLLOWING)))
    })
    expect(ordered).toEqual(Array(7).fill(true))
  })

  test('opens focused on the current section, and Escape closes it back onto the button', async ({ page }) => {
    await open(page, 1440)
    await fold(page, 1440)
    await openMenu(page)
    await expect(menu(page).getByRole('navigation', { name: 'Project sections' }).getByRole('button', { name: 'Tickets' })).toBeFocused()
    // Tab keeps to the menu: there is a lot of it, and every part must be reachable by keyboard.
    for (let step = 0; step < 40; step++) {
      await page.keyboard.press('Tab')
      expect(await menu(page).evaluate(el => el.contains(document.activeElement)), `Tab ${step + 1} stays inside`).toBe(true)
    }
    await page.keyboard.press('Escape')
    await expect(menu(page)).toBeHidden()
    await expect(display(page)).toBeFocused()
  })

  test('choosing a section goes there and closes the menu', async ({ page }) => {
    await open(page, 1440)
    await fold(page, 1440)
    await openMenu(page)
    await menu(page).getByRole('navigation', { name: 'Project sections' }).getByRole('button', { name: 'Knowledge' }).click()
    await expect(menu(page)).toBeHidden()
    await expect(page).toHaveURL(/\/p\/PHAROS\/knowledge/)
  })

  test('Needs attention opens this project\'s list of suggestions', async ({ page }) => {
    await open(page, 1440)
    await fold(page, 1440)
    await openMenu(page)
    await menu(page).getByRole('link', { name: 'Needs attention' }).click()
    await expect(page).toHaveURL(/\/tickets\?.*view=needs-attention/)
    await expect(page).toHaveURL(/project_id=p-pharos/)
  })

  test('choosing a saved view opens it and says so the next time the menu opens', async ({ page }) => {
    await open(page, 1440)
    await fold(page, 1440)
    await openMenu(page)
    await menu(page).getByRole('navigation', { name: 'Saved views' }).getByRole('link', { name: /Laufende Betriebsprüfung/ }).click()
    await expect(menu(page)).toBeHidden()
    await expect(page).toHaveURL(new RegExp(`v=${VIEW}`))
    await expect(display(page)).toBeFocused()
    await openMenu(page)
    const views = menu(page).getByRole('navigation', { name: 'Saved views' })
    await expect(views.getByRole('link', { name: /Laufende Betriebsprüfung/ })).toHaveAttribute('aria-current', 'page')
    await expect(views.getByRole('link', { name: 'All tickets' })).not.toHaveAttribute('aria-current', 'page')
    // The open view's own options open under the Display button, once the menu has closed.
    await views.getByRole('button', { name: /^Options for view / }).click()
    await expect(menu(page)).toBeHidden()
    await expect(page.getByRole('menu', { name: /^View Laufende/ })).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(display(page)).toBeFocused()
  })

  test('saving the list as a view works from the menu', async ({ page }) => {
    await open(page, 1440)
    await fold(page, 1440)
    await openMenu(page)
    await menu(page).getByRole('button', { name: 'Save view' }).click()
    await expect(menu(page)).toBeHidden()
    const panel = page.getByRole('dialog', { name: 'Save view', exact: true })
    await expect(panel).toBeVisible()
    // It opens beside the Display button, not in a corner of the window.
    const button = (await display(page).boundingBox())!, opened = (await panel.boundingBox())!
    expect(opened.y).toBeGreaterThanOrEqual(button.y)
    expect(opened.x + opened.width).toBeGreaterThan(button.x)
  })

  test('Hide closed switches in place, and the gear opens its choices where Display is', async ({ page }) => {
    await open(page, 1440)
    await fold(page, 1440)
    await openMenu(page)
    const hide = menu(page).getByRole('checkbox', { name: /^Hide / })
    await expect(hide).toBeChecked()
    await hide.uncheck()
    await expect(page).toHaveURL(/closed=1/)
    await expect(menu(page), 'the menu stays open for the next choice').toBeVisible()
    await expect(hide).not.toBeChecked()
    await menu(page).getByRole('button', { name: 'Choose what Hide hides' }).click()
    await expect(menu(page)).toBeHidden()
    const choices = page.getByRole('dialog', { name: 'What Hide hides' })
    await expect(choices).toBeVisible()
    const button = (await display(page).boundingBox())!, opened = (await choices.boundingBox())!
    expect(Math.abs(opened.x + opened.width - (button.x + button.width)), 'right edges line up').toBeLessThanOrEqual(2)
    await page.keyboard.press('Escape')
    await expect(choices).toBeHidden()
    await expect(display(page)).toBeFocused()
  })

  test('a display choice keeps the menu open and shows on the button', async ({ page }) => {
    await open(page, 1440)
    await fold(page, 1440)
    await openMenu(page)
    await menu(page).getByRole('radio', { name: 'Status', exact: true }).click()
    await expect(page).toHaveURL(/group=status/)
    await expect(menu(page)).toBeVisible()
    await expect(display(page)).toHaveAccessibleName('Display: Grouped by status')
  })

  test('unfolding the header closes the menu instead of leaving it on a hidden button', async ({ page }) => {
    await open(page, 1440)
    await fold(page, 1440)
    await openMenu(page)
    const isMac = await mac(page)
    await page.keyboard.press(isMac ? 'Meta+Shift+Period' : 'Control+Shift+Period')
    await expect(projectPage(page)).toHaveClass(/header-compact/)
    await expect(menu(page)).toBeHidden()
    await expect(display(page)).toHaveCount(0)
  })

  test('choosing a header size from the menu unfolds the header', async ({ page }) => {
    await open(page, 1440)
    await fold(page, 1440)
    await openMenu(page)
    await menu(page).getByRole('radiogroup', { name: 'Project header density' }).getByRole('radio', { name: 'Comfortable' }).click()
    await expect(projectPage(page)).toHaveClass(/header-comfortable/)
    await expect(menu(page)).toBeHidden()
  })
})

test.describe('collapsed header: the menu never scrolls', () => {
  async function noScroll(page: Page, label: string) {
    const frame = await menu(page).evaluate(el => ({ down: el.scrollHeight - el.clientHeight, across: el.scrollWidth - el.clientWidth, bottom: el.getBoundingClientRect().bottom, right: el.getBoundingClientRect().right, left: el.getBoundingClientRect().left, top: el.getBoundingClientRect().top, width: innerWidth, height: innerHeight }))
    expect(frame.down, `${label}: nothing hides below`).toBeLessThanOrEqual(1)
    expect(frame.across, `${label}: nothing hides to the side`).toBeLessThanOrEqual(1)
    expect(frame.top, `${label}: starts inside the window`).toBeGreaterThanOrEqual(0)
    expect(frame.bottom, `${label}: ends inside the window`).toBeLessThanOrEqual(frame.height)
    expect(frame.left, `${label}: starts inside the window`).toBeGreaterThanOrEqual(0)
    expect(frame.right, `${label}: ends inside the window`).toBeLessThanOrEqual(frame.width)
  }

  for (const [width, height] of [[1440, 900], [1280, 800], [1024, 800], [1100, 1000]] as const) {
    test(`flows into columns at ${width}×${height} and fits`, async ({ page }) => {
      await open(page, width, 'light', height)
      await fold(page, width)
      await openMenu(page)
      expect(Number(await menu(page).locator('.menu-body').getAttribute('data-columns')), 'too tall for one column').toBeGreaterThanOrEqual(2)
      await noScroll(page, `${width}×${height}`)
    })
  }

  test('lets the long column list break between rows only when the window is too short for whole blocks', async ({ page }) => {
    await open(page, 1440, 'light', 700)
    await fold(page, 1440)
    await openMenu(page)
    await expect(menu(page).locator('.menu-body')).toHaveAttribute('data-split', '')
    await noScroll(page, '1440×700')
    // Whole blocks are kept when they fit.
    await page.setViewportSize({ width: 1440, height: 900 })
    await expect(menu(page).locator('.menu-body')).not.toHaveAttribute('data-split', '')
    await noScroll(page, 'back at 1440×900')
  })

  test('stays one column when the window is tall enough, and fits', async ({ page }) => {
    await open(page, 1440, 'light', 2400)
    await fold(page, 1440)
    await openMenu(page)
    await expect(menu(page).locator('.menu-body')).toHaveAttribute('data-columns', '1')
    await noScroll(page, '1440×2400')
    // One column is the width of one column, not of three.
    expect((await menu(page).boundingBox())!.width).toBeLessThan(400)
  })

  test('keeps sort actions and the other blocks still when a sort key is added or removed (±0.5px)', async ({ page }) => {
    await open(page, 1440)
    await fold(page, 1440)
    await openMenu(page)
    const add = menu(page).getByRole('button', { name: 'Add sort key' })
    const sections = menu(page).getByRole('navigation', { name: 'Project sections' })
    const group = menu(page).getByRole('radiogroup', { name: 'Group by' })
    await expectStableControls({
      controls: {
        add, sections, tickets: sections.getByRole('button', { name: 'Tickets' }),
        all: menu(page).getByRole('link', { name: 'All tickets' }),
        hide: menu(page).getByRole('checkbox', { name: /^Hide / }),
        group, status: group.getByRole('radio', { name: 'Status', exact: true }),
        rows: menu(page).getByRole('radiogroup', { name: 'Row height' }),
        columns: menu(page).getByRole('list', { name: 'Columns' }),
        effort: menu(page).getByRole('radiogroup', { name: 'Effort meter' }),
      },
      interactions: [
        { name: 'add a sort key', run: async () => { await add.click(); await expect(menu(page).getByRole('combobox', { name: 'Sort key 1' })).toBeVisible() } },
        { name: 'add another sort key', run: async () => { await add.click(); await expect(menu(page).getByRole('combobox', { name: 'Sort key 2' })).toBeVisible() } },
        { name: 'remove the second sort key', run: async () => { await menu(page).locator('[data-sort-row="1"]').getByRole('button', { name: /^Remove / }).click(); await expect(menu(page).getByRole('combobox', { name: 'Sort key 2' })).toHaveCount(0) } },
        { name: 'remove the first sort key', run: async () => { await menu(page).getByRole('button', { name: /^Remove / }).click(); await expect(menu(page).getByRole('combobox', { name: 'Sort key 1' })).toHaveCount(0) } },
      ],
    })
  })

  test('lets a couple dozen saved views flow across columns without the panel scrolling', async ({ page }) => {
    await open(page, 1440, 'light', 900, '/p/PHAROS?status=new,backlog,open,blocked,in_progress&type=ticket', 24)
    await fold(page, 1440)
    await openMenu(page)
    const rows = menu(page).locator('nav[aria-label="Saved views"] .row')
    await expect(rows).toHaveCount(26)
    const intact = await rows.evaluateAll(els => els.every(el => el.getClientRects().length === 1 && el.getBoundingClientRect().height <= 40 && el.getBoundingClientRect().height >= 28))
    expect(intact, 'each saved-view row stays whole').toBe(true)
    const columnsUsed = await rows.evaluateAll(els => new Set(els.map(el => Math.round(el.getBoundingClientRect().left / 80))).size)
    expect(columnsUsed, 'rows flow into more than one column').toBeGreaterThan(1)
    await noScroll(page, '24 saved views at 1440×900')
  })

  test('recounts its columns when the window changes height while open', async ({ page }) => {
    await open(page, 1440, 'light', 2400)
    await fold(page, 1440)
    await openMenu(page)
    await expect(menu(page).locator('.menu-body')).toHaveAttribute('data-columns', '1')
    await page.setViewportSize({ width: 1440, height: 900 })
    await expect(menu(page).locator('.menu-body')).not.toHaveAttribute('data-columns', '1')
    await settled(page)
    await noScroll(page, 'after resizing to 1440×900')
  })

  test('is a full-height sheet on a phone, with its title and Done pinned', async ({ page }) => {
    await open(page, 400, 'light', 800)
    await fold(page, 400)
    await openMenu(page)
    const sheet = menu(page).locator('.display-menu.as-sheet')
    await expect(sheet).toBeVisible()
    const frame = (await menu(page).boundingBox())!
    expect([frame.x, frame.y, frame.width, frame.height]).toEqual([0, 0, 400, 800])
    const done = menu(page).getByRole('button', { name: /^Done/ })
    expect((await done.boundingBox())!.height, 'a 44px target').toBeGreaterThanOrEqual(44)
    // Longer than the screen: the body scrolls, the title and Done stay.
    const body = menu(page).locator('.menu-body')
    expect(await body.evaluate(el => el.scrollHeight - el.clientHeight), 'the body has more than it shows').toBeGreaterThan(0)
    await expectStableControls({
      controls: { frame: menu(page), title: menu(page).getByRole('heading', { name: 'Display' }), done },
      scrollAreas: { body },
      interactions: [{ name: 'scroll the body', run: async () => { await body.evaluate(el => { el.scrollTop = el.scrollHeight }) } }, { name: 'scroll back', run: async () => { await body.evaluate(el => { el.scrollTop = 0 }) } }],
    })
    await done.click()
    await expect(menu(page)).toBeHidden()
    await expect(display(page)).toBeFocused()
  })

  test('phone targets are 44px', async ({ page }) => {
    await open(page, 400, 'light', 800)
    await fold(page, 400)
    await openMenu(page)
    for (const target of [menu(page).getByRole('button', { name: 'Knowledge' }), menu(page).getByRole('link', { name: 'Needs attention' }), menu(page).getByRole('link', { name: 'All tickets' }), menu(page).getByRole('button', { name: 'Choose what Hide hides' }), menu(page).getByRole('radio', { name: 'Status', exact: true })]) {
      const rect = (await target.boundingBox())!
      expect(rect.height).toBeGreaterThanOrEqual(43.5)
    }
  })
})

test.describe('the other two header views', () => {
  for (const mode of ['compact', 'comfortable'] as const) {
    test(`${mode}: Display keeps its place in the header bar and its menu is the one it was`, async ({ page }) => {
      await open(page, 1440)
      await fold(page, 1440, mode)
      const inBar = page.locator('.project-navigation').getByRole('button', { name: /^Display:/ })
      await expect(inBar).toBeVisible()
      // The toolbar's twin is there only to hold the place: unseen, unreachable, unnamed.
      const twin = toolbar(page).locator('.collapsed-display')
      await expect(twin).toHaveAttribute('inert', '')
      await expect(twin).toHaveAttribute('aria-hidden', 'true')
      await expect(twin).toHaveCSS('visibility', 'hidden')
      await expect(display(page)).toHaveCount(0)
      await inBar.click()
      await expect(menu(page)).toBeVisible()
      await expect(menu(page).locator('.menu-body')).toHaveCount(0)
      await expect(menu(page).getByRole('navigation')).toHaveCount(0)
      await expect(menu(page).getByRole('checkbox', { name: /^Hide / })).toHaveCount(0)
      expect((await menu(page).boundingBox())!.width).toBeLessThanOrEqual(320)
      // Sections, saved views and Hide closed stay in the header bar, as before.
      await page.keyboard.press('Escape')
      await expect(page.getByRole('tablist', { name: 'Project sections' })).toBeVisible()
      await expect(page.getByRole('navigation', { name: 'Saved views' })).toBeVisible()
      await expect(page.locator('.closed-switch')).toBeVisible()
    })
  }
})

test.describe('folding and unfolding the header', () => {
  for (const width of [1440, 1024, 390]) {
    test(`moves nothing in the toolbar at ${width}px (±0.5px)`, async ({ page }) => {
      await open(page, width)
      const bar = toolbar(page)
      const controls: Record<string, Locator> = {
        views: bar.getByRole('tablist', { name: 'Ticket views' }), search: search(page), new: create(page),
        ...(width <= 600 ? { filters: bar.getByRole('button', { name: 'Filters', exact: true }) } : { status: bar.locator('.facet-control[data-dim="status"]'), facets: bar.locator('.facets') }),
      }
      await fold(page, width)
      // The folded header is the baseline; every other view and the way back must match it.
      const steps = width <= 600
        ? ['compact', 'collapsed', 'compact'] as const
        : ['compact', 'comfortable', 'collapsed', 'comfortable', 'compact', 'collapsed'] as const
      await stillWithin(page, bar, controls, steps.map(mode => ({ name: `header ${mode}`, run: () => fold(page, width, mode) })))
    })
  }

  test('keeps the header switch and the bar below it where they are (±0.5px)', async ({ page }) => {
    await open(page, 1440)
    await fold(page, 1440)
    const group = page.getByRole('radiogroup', { name: 'Project header', exact: true })
    await expectStableControls({
      controls: { switch: group, comfortable: density(page, 'Comfortable'), compact: density(page, 'Compact'), collapsed: density(page, 'Collapsed'), app: page.locator('.app-header') },
      interactions: ['Compact', 'Comfortable', 'Collapsed', 'Compact', 'Collapsed'].map(name => ({ name, run: async () => { await density(page, name).click(); await expect(projectPage(page)).toHaveClass(new RegExp(`header-${name.toLowerCase()}`)) } })),
    })
  })

  test('keeps the collapsed toolbar still while the menu opens, closes and takes choices (±0.5px)', async ({ page }) => {
    await open(page, 1440)
    await fold(page, 1440)
    const bar = toolbar(page)
    const trigger = display(page)
    const controls = { views: bar.getByRole('tablist', { name: 'Ticket views' }), search: search(page), status: bar.locator('.facet-control[data-dim="status"]'), trigger, new: create(page) }
    await expectStableControls({
      controls,
      interactions: [{ name: 'open the menu', run: () => openMenu(page) }],
    })
    // Every grouping label, including the long ones ("By assignee"). The trigger's footprint is the widest of them.
    const labels = await menu(page).getByRole('radiogroup', { name: 'Group by' }).getByRole('radio').evaluateAll(els => els.map(el => el.textContent?.trim() ?? '').filter(Boolean))
    expect(labels, 'every grouping is offered').toEqual(['None', 'Status', 'Assignee', 'Priority', 'Project', 'Type', 'Epic', 'Label'])
    const group = menu(page).getByRole('radiogroup', { name: 'Group by' })
    await expectStableControls({
      controls,
      interactions: [
        ...labels.map(label => ({ name: `group by ${label}`, run: async () => { await group.getByRole('radio', { name: label, exact: true }).click(); await expect(group.getByRole('radio', { name: label, exact: true })).toHaveAttribute('aria-checked', 'true') } })),
        { name: 'switch Hide closed', run: async () => { await menu(page).getByRole('checkbox', { name: /^Hide / }).uncheck(); await expect(page).toHaveURL(/closed=1/) } },
        { name: 'close the menu', run: async () => { await page.keyboard.press('Escape'); await expect(menu(page)).toBeHidden() } },
      ],
    })
  })

  for (const [width, height] of [[1440, 900], [400, 800]] as const) {
    test(`keeps the menu's own controls still through every choice at ${width}px (±0.5px)`, async ({ page }) => {
      await open(page, width, 'light', height)
      await fold(page, width)
      await openMenu(page)
      const sections = menu(page).getByRole('navigation', { name: 'Project sections' })
      const group = menu(page).getByRole('radiogroup', { name: 'Group by' })
      const hide = menu(page).getByRole('checkbox', { name: /^Hide / })
      await expectStableControls({
        controls: {
          frame: menu(page), sections, tickets: sections.getByRole('button', { name: 'Tickets' }), knowledge: sections.getByRole('button', { name: 'Knowledge' }), attention: sections.getByRole('link', { name: 'Needs attention' }),
          all: menu(page).getByRole('link', { name: 'All tickets' }), hide, gear: menu(page).getByRole('button', { name: 'Choose what Hide hides' }),
          group, none: group.getByRole('radio', { name: 'None', exact: true }), status: group.getByRole('radio', { name: 'Status', exact: true }),
        },
        scrollAreas: width <= 600 ? { body: menu(page).locator('.menu-body') } : {},
        interactions: [
          { name: 'group by status', run: async () => { await group.getByRole('radio', { name: 'Status', exact: true }).click(); await expect(page).toHaveURL(/group=status/) } },
          { name: 'group by none', run: async () => { await group.getByRole('radio', { name: 'None', exact: true }).click(); await expect(page).not.toHaveURL(/group=/) } },
          { name: 'show closed', run: async () => { await hide.uncheck(); await expect(page).toHaveURL(/closed=1/) } },
          { name: 'hide closed', run: async () => { await hide.check(); await expect(page).not.toHaveURL(/closed=1/) } },
        ],
      })
    })
  }
})
