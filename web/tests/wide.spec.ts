// SPDX-License-Identifier: AGPL-3.0-only
// U11: wide screens (columns, picker, widths, splitter, context column), the
// whole-ticket edit mode and the panel's back trail.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors, type Call } from './work-fixtures'

const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })
const headers = (page: Page) => page.locator('table.tickets thead th')
const rows = (page: Page) => page.locator('tr.ticket-row:not(.ghost)')
const puts = (calls: Call[], key: string) => calls.filter(c => c.method === 'PUT' && c.path === `/api/preferences/${key}`)
const patches = (calls: Call[]) => calls.filter(c => c.method === 'PATCH' && c.path.startsWith('/api/nodes/'))

test.describe('wide lists', () => {
  test('columns grow with the width: Epic, Release, Tags and Created join from about 1800px', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await mockWork(page, fixtures())
    await page.goto('/p/PHAROS')
    await expect(rows(page)).toHaveCount(5)
    await expect(headers(page)).toHaveText(['Key', 'Title', 'Status', 'Priority', 'Assignee', 'Updated'])
    await page.setViewportSize({ width: 2560, height: 1440 })
    await expect(headers(page)).toHaveText(['Key', 'Title', 'Status', 'Priority', 'Assignee', 'Epic', 'Release', 'Tags', 'Created', 'Updated'])
    const row = (key: string) => rows(page).filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
    await expect(row('PHAROS-11').locator('.c-epic')).toHaveText('Guarded multi-cloud provisioning')
    await expect(row('PHAROS-11').locator('.c-release')).toHaveText('—')
    await expect(row('PHAROS-11').locator('.c-release')).not.toHaveText('v4.7.8')
    await expect(row('PHAROS-11').locator('.c-tags .tag-chip')).toHaveText(['CUSTOMERPORTAL', 'hsb8'])
    // A task shows its ticket's epic in the column and its ticket as the chip by the title.
    await expect(row('PHAROS-13').locator('.c-epic')).toHaveText('Guarded multi-cloud provisioning')
    await expect(row('PHAROS-13').locator('.parent-chip')).toHaveText('PHAROS-12')
    await expect(row('PHAROS-11').locator('.parent-chip')).toHaveCount(0)
    await expect(row('PHAROS-14').locator('.c-epic')).toHaveText('—')
    // Title stops near 960px; the spare width goes to the text columns beside it.
    const title = (await page.locator('thead th.c-title').boundingBox())!
    expect(title.width).toBeGreaterThan(900)
    expect(title.width).toBeLessThan(1140)
    expect((await page.locator('thead th.c-epic').boundingBox())!.width).toBeGreaterThan(260)
    // The page has no width cap; the gutter grows to 48px.
    const table = (await page.locator('.table-card').boundingBox())!
    expect(table.x).toBeGreaterThanOrEqual(46)
    expect(table.x + table.width).toBeGreaterThan(2560 - 70)
    // Title resizes too: its width becomes the target, and is saved.
    const handle = page.getByRole('separator', { name: 'Resize Title column' })
    const box = (await handle.boundingBox())!
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
    await page.mouse.down()
    await page.mouse.move(box.x - 200, box.y + box.height / 2, { steps: 4 })
    await page.mouse.up()
    await expect.poll(async () => Math.round((await page.locator('thead th.c-title').boundingBox())!.width)).toBeLessThan(title.width - 150)
  })

  test('the Display menu shows, hides and reorders columns, saved per project on the server', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    const data = fixtures()
    const calls = await mockWork(page, data)
    await page.goto('/p/PHAROS')
    await expect(rows(page)).toHaveCount(5)
    await page.getByRole('button', { name: /^Display/ }).click()
    const menu = page.getByRole('dialog', { name: 'Display options' })
    await menu.getByRole('checkbox', { name: 'Priority' }).uncheck()
    await expect(headers(page)).toHaveText(['Key', 'Title', 'Status', 'Assignee', 'Updated'])
    // Alt+Up moves Updated before Assignee.
    await menu.getByRole('checkbox', { name: 'Updated' }).focus()
    for (let i = 0; i < 7; i++) await page.keyboard.press('Alt+ArrowUp')
    await expect(headers(page)).toHaveText(['Key', 'Title', 'Status', 'Updated', 'Assignee'])
    await menu.getByRole('checkbox', { name: 'Estimate' }).check()
    await expect(headers(page)).toHaveText(['Key', 'Title', 'Status', 'Updated', 'Assignee', 'Estimate'])
    await expect.poll(() => puts(calls, 'list:p-pharos').length).toBeGreaterThan(0)
    await expect.poll(() => (data.preferences['list:p-pharos'] as { visible?: string[] } | undefined)?.visible ?? []).toEqual(expect.arrayContaining(['status', 'updated', 'assignee', 'estimate']))
    expect((data.preferences['list:p-pharos'] as { visible: string[] }).visible).not.toContain('priority')
    // A new visit reads the choice back.
    await page.reload()
    await expect(rows(page)).toHaveCount(5)
    await expect(headers(page)).toHaveText(['Key', 'Title', 'Status', 'Updated', 'Assignee', 'Estimate'])
    await page.getByRole('button', { name: /^Display/ }).click()
    await page.getByRole('dialog', { name: 'Display options' }).getByRole('button', { name: 'Automatic' }).click()
    await expect(headers(page)).toHaveText(['Key', 'Title', 'Status', 'Priority', 'Assignee', 'Updated'])
  })

  test('columns resize by dragging the header edge and fit their content on double-click', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    const data = fixtures()
    await mockWork(page, data)
    await page.goto('/p/PHAROS')
    await expect(rows(page)).toHaveCount(5)
    const status = page.locator('thead th.c-status')
    const before = (await status.boundingBox())!.width
    const handle = page.getByRole('separator', { name: 'Resize Status column' })
    const box = (await handle.boundingBox())!
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
    await page.mouse.down()
    await page.mouse.move(box.x + 60, box.y + box.height / 2, { steps: 4 })
    await page.mouse.up()
    await expect.poll(async () => Math.round((await status.boundingBox())!.width)).toBeGreaterThan(before + 50)
    await expect.poll(() => (data.preferences['list:p-pharos'] as { widths?: Record<string, number> } | undefined)?.widths?.status ?? 0).toBeGreaterThan(before + 50)
    await handle.dblclick()
    await expect.poll(async () => Math.round((await status.boundingBox())!.width)).toBeLessThan(before + 10)
    // Keyboard: arrows on the handle.
    await handle.focus()
    const fitted = (await status.boundingBox())!.width
    await page.keyboard.press('ArrowRight')
    await expect.poll(async () => Math.round((await status.boundingBox())!.width)).toBe(Math.round(fitted + 16))
  })

  test('the docked list fills everything left of the panel; the splitter resizes it and is saved', async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width: 1920, height: 1080 })
    const data = fixtures()
    await mockWork(page, data)
    await page.goto('/p/PHAROS/PHAROS-12')
    await expect(panel(page).getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    const table = (await page.locator('.table-card').boundingBox())!, dock = (await panel(page).boundingBox())!
    expect(dock.x - (table.x + table.width)).toBeLessThan(40)
    expect(dock.width).toBeGreaterThan(860)
    // Wide panels get a context column beside the reading column.
    await expect(panel(page).getByRole('complementary', { name: 'Attachments, relations and activity' })).toBeVisible()
    const splitter = page.getByRole('separator', { name: 'Resize the panel' })
    const box = (await splitter.boundingBox())!
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
    await page.mouse.down()
    await page.mouse.move(box.x + box.width / 2 + 240, box.y + box.height / 2, { steps: 6 })
    await page.mouse.up()
    await expect.poll(async () => Math.round((await panel(page).boundingBox())!.width)).toBeLessThan(dock.width - 200)
    await expect.poll(() => (data.preferences.layout as { panel?: number } | undefined)?.panel ?? 0).toBeGreaterThan(0)
    const table2 = (await page.locator('.table-card').boundingBox())!, dock2 = (await panel(page).boundingBox())!
    expect(dock2.x - (table2.x + table2.width)).toBeLessThan(40)
    // Narrow now: no context column; attachments move under the properties.
    await expect(panel(page).getByRole('complementary', { name: 'Attachments, relations and activity' })).toHaveCount(0)
    await splitter.dblclick()
    await expect.poll(async () => Math.round((await panel(page).boundingBox())!.width)).toBeGreaterThan(860)
    expect(errors).toEqual([])
  })
})

test.describe('edit mode', () => {
  test('Edit (or e) edits title, text and properties together with one Save', async ({ page }) => {
    const calls = await mockWork(page, fixtures())
    await page.goto('/p/PHAROS/PHAROS-12')
    const ws = panel(page)
    await expect(ws.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    await ws.getByRole('button', { name: 'Edit' }).click()
    const form = ws.getByRole('form', { name: 'Edit PHAROS-12' })
    await expect(form.getByLabel('Title')).toBeFocused()
    await form.getByLabel('Title').fill('Add an Oracle Cloud Always Free connector')
    // Status, priority and assignee use the app's own menus, styled as fields.
    await expect(form.locator('select')).toHaveCount(0)
    await form.getByRole('button', { name: 'Priority Medium' }).click()
    const priority = page.getByRole('menu', { name: 'Priority of PHAROS-12' })
    await expect(priority.getByRole('menuitemradio', { name: 'Medium' }).locator('svg.medium')).toHaveAttribute('stroke-width', '1.8')
    await expect(priority.getByRole('menuitemradio', { name: 'Medium' }).locator('svg.medium path')).toHaveAttribute('d', 'M7 3.2 10.8 7 7 10.8 3.2 7Z')
    await priority.getByRole('menuitemradio', { name: 'High' }).click()
    await expect(form.getByRole('button', { name: 'Priority High' })).toBeFocused()
    // From the keyboard: ArrowDown opens, digits pick.
    await form.getByRole('button', { name: 'Status Backlog' }).focus()
    await page.keyboard.press('ArrowDown')
    await expect(page.getByRole('menu', { name: 'Status of PHAROS-12' })).toBeVisible()
    await page.keyboard.press('4')
    await expect(form.getByRole('button', { name: 'Status In progress' })).toBeFocused()
    await form.getByRole('button', { name: 'Assignee Unassigned' }).click()
    await page.getByRole('textbox', { name: 'Find assignee' }).fill('mira')
    await page.keyboard.press('Enter')
    await expect(form.getByRole('button', { name: 'Assignee Mira Holm' })).toBeVisible()
    // Escape in a menu closes only the menu.
    await form.getByRole('button', { name: 'Assignee Mira Holm' }).click()
    await page.keyboard.press('Escape')
    await expect(page.getByRole('menu', { name: 'Assignee of PHAROS-12' })).toHaveCount(0)
    await expect(form).toBeVisible()
    await form.getByLabel('Description, Markdown').fill('Use the **Always Free** tier.')
    await form.getByLabel('Acceptance criteria, Markdown').fill('- [ ] Connector registered')
    await expect(ws.getByText('Unsaved')).toBeVisible()
    await page.keyboard.press('Control+Enter')
    await expect(ws.getByRole('heading', { name: 'Add an Oracle Cloud Always Free connector' })).toBeVisible()
    const writes = patches(calls)
    expect(writes).toHaveLength(1)
    expect(writes[0].body).toMatchObject({ title: 'Add an Oracle Cloud Always Free connector', body: 'Use the **Always Free** tier.', state: 'in_progress', fields: { priority: 'high', assignee: '22222222-2222-4222-8222-222222222222', acceptance_criteria: '- [ ] Connector registered' } })
    expect(writes[0].headers['if-unmodified-since']).toBeTruthy()
    await expect(ws.locator('.md-section').first()).toContainText('Use the Always Free tier.')
    // e opens it again; Escape without changes simply leaves.
    await ws.focus()
    await page.keyboard.press('e')
    await expect(form.getByLabel('Title')).toBeFocused()
    await page.keyboard.press('Escape')
    await expect(form).toHaveCount(0)
    expect(patches(calls)).toHaveLength(1)
  })

  test('Cancel with changes asks first; a conflict keeps the draft', async ({ page }) => {
    const calls = await mockWork(page, fixtures(), { conflictOn: 'n-2' })
    await page.goto('/p/PHAROS/PHAROS-12')
    const ws = panel(page)
    await expect(ws.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    await page.keyboard.press('e')
    const form = ws.getByRole('form', { name: 'Edit PHAROS-12' })
    await form.getByLabel('Title').fill('Draft title')
    await ws.getByRole('button', { name: 'Cancel' }).click()
    const dialog = page.getByRole('dialog', { name: 'Discard your changes?' })
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(form.getByLabel('Title')).toHaveValue('Draft title')
    await ws.getByRole('button', { name: 'Save' }).click()
    await expect(page.locator('.toast').filter({ hasText: 'was changed elsewhere' })).toBeVisible()
    await expect(form.getByLabel('Title')).toHaveValue('Draft title')
    expect(patches(calls)).toHaveLength(1)
    // Saving again goes against the newer version.
    await ws.getByRole('button', { name: 'Save' }).click()
    await expect(ws.getByRole('heading', { name: 'Draft title' })).toBeVisible()
  })

  test('the full page edits in a writing layout with a live preview', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await mockWork(page, fixtures())
    await page.goto('/p/PHAROS/PHAROS-12?view=full')
    await expect(page.getByRole('article', { name: 'Ticket details' }).getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    await page.keyboard.press('e')
    const editor = page.locator('.edit-form .md-editor').first()
    await expect(editor).toHaveClass(/split/)
    await editor.getByLabel('Description, Markdown').fill('## Plan\n\nShip it.')
    await expect(editor.getByLabel('Description preview')).toContainText('Plan')
    const area = (await editor.getByLabel('Description, Markdown').boundingBox())!, preview = (await editor.getByLabel('Description preview').boundingBox())!
    expect(preview.x).toBeGreaterThan(area.x + area.width - 1)
  })
})

test.describe('back trail', () => {
  test('following links inside the panel builds a trail with Back and Alt+Left; list moves clear it', async ({ page, context }) => {
    await page.setViewportSize({ width: 1920, height: 1080 })
    await mockWork(page, fixtures())
    await page.goto('/p/PHAROS/PHAROS-12')
    const ws = panel(page)
    await expect(ws.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    await ws.locator('.epic-chip').click()
    await expect(page).toHaveURL('/p/PHAROS/PHAROS-10')
    const trail = ws.getByRole('navigation', { name: 'Followed tickets' })
    await expect(trail).toHaveText(/PHAROS-12/)
    await ws.getByRole('button', { name: /PHAROS-11/ }).first().click()
    await expect(page).toHaveURL('/p/PHAROS/PHAROS-11')
    await expect(trail.getByRole('button')).toHaveText(['PHAROS-12', 'PHAROS-10'])
    // A narrow panel keeps the last step and shows that there is more.
    await page.setViewportSize({ width: 1280, height: 800 })
    await expect(trail.getByRole('button')).toHaveText(['PHAROS-10'])
    await expect(trail.locator('.trail-more')).toBeVisible()
    await page.setViewportSize({ width: 1920, height: 1080 })
    await ws.focus()
    await page.keyboard.press('Alt+ArrowLeft')
    await expect(page).toHaveURL('/p/PHAROS/PHAROS-10')
    await ws.getByRole('button', { name: 'Back to PHAROS-12' }).click()
    await expect(page).toHaveURL('/p/PHAROS/PHAROS-12')
    await expect(ws.getByRole('navigation', { name: 'Followed tickets' })).toHaveCount(0)
    // Browser forward returns along the same steps.
    await page.goForward()
    await expect(page).toHaveURL('/p/PHAROS/PHAROS-10')
    await expect(ws.getByRole('navigation', { name: 'Followed tickets' })).toHaveText(/PHAROS-12/)
    // k moves in the list, replacing the entry and clearing the trail.
    await ws.focus()
    await page.keyboard.press('k')
    await expect(page).not.toHaveURL('/p/PHAROS/PHAROS-10')
    await expect(ws.getByRole('navigation', { name: 'Followed tickets' })).toHaveCount(0)
    // A modified click opens the linked ticket in a new tab.
    await page.goto('/p/PHAROS/PHAROS-12')
    await expect(ws.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    const [tab] = await Promise.all([context.waitForEvent('page'), ws.locator('.epic-chip').click({ modifiers: ['ControlOrMeta'] })])
    expect(new URL(tab.url()).pathname).toBe('/p/PHAROS/PHAROS-10')
    await expect(page).toHaveURL('/p/PHAROS/PHAROS-12')
  })
})
