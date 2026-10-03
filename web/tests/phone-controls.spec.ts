// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork, type Fixtures } from './work-fixtures'
import { expectStableControls } from './helpers/stable'

const shots = 'test-results/aeon-628-phone-controls'
const rows = (page: Page) => page.locator('tr.ticket-row:not(.ghost)')
function germanWorld(theme: string): Fixtures {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.preferences['list:display'] = { density: 'comfortable', headerGraph: false }
  data.nodes.find(n => n.id === 'n-1')!.title = 'Überprüfung der mandantenübergreifenden Berechtigungsverwaltung für außergewöhnlich lange Projektbezeichnungen'
  data.people[1]!.name = 'Johanna Weber-Oberhauser-Kieslinger'
  data.attachments['n-1'][0]!.caption = 'Vorher: Übersicht der mandantenübergreifenden Berechtigungsverwaltung mit ausführlichen deutschen Bezeichnungen'
  data.attachments['n-1'][1]!.caption = 'Nachher: Berechtigungsverwaltung mit vollständigen Bezeichnungen und nachvollziehbaren Änderungen'
  return data
}
async function capture(page: Page, name: string) {
  await page.evaluate(() => document.fonts.ready)
  mkdirSync(shots, { recursive: true })
  await page.screenshot({ path: join(shots, `${name}.png`) })
}
async function inFrame(control: Locator, frame: Locator) {
  await expect(control).toBeInViewport({ ratio: 1 })
  const a = (await control.boundingBox())!, b = (await frame.boundingBox())!
  expect(a.x).toBeGreaterThanOrEqual(b.x)
  expect(a.x + a.width).toBeLessThanOrEqual(b.x + b.width + .5)
  expect(a.y).toBeGreaterThanOrEqual(b.y)
  expect(a.y + a.height).toBeLessThanOrEqual(b.y + b.height + .5)
}

for (const width of [390, 768, 1024, 1440]) {
  for (const theme of ['light', 'dark'] as const) {
    test(`display preferences are reachable, shared and stable at ${width}px ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 })
      const data = germanWorld(theme)
      const calls = await mockWork(page, data)
      await page.goto('/p/PHAROS?sort=title')
      await expect(rows(page)).toHaveCount(5)
      const sheet = width <= 900
      const opener = page.getByRole('button', { name: sheet ? 'Filters' : 'Display: Display', exact: true })
      await expect(opener).toBeVisible()
      await opener.click()
      const dialog = page.getByRole('dialog', { name: sheet ? 'Filters' : 'Display options', exact: true })
      const scroll = sheet ? dialog.locator('.sheet-scroll') : dialog
      const pinned = sheet ? { frame: dialog, header: dialog.locator('header'), done: dialog.locator('footer button') } : {}
      await expect(dialog.getByText('Row height', { exact: true })).toBeVisible()
      if (sheet) await expect(dialog.getByRole('heading', { name: 'Display' })).toBeVisible()
      const grouping = dialog.getByRole('radiogroup', { name: 'Group by', exact: true })
      await expectStableControls({ controls: { ...pinned, grouping, status: grouping.getByRole('radio', { name: 'Status', exact: true }), none: grouping.getByRole('radio', { name: 'None', exact: true }) }, scrollAreas: { body: scroll }, interactions: ['Status', 'None'].map(name => ({ name: `group by ${name}`, run: async () => {
        await grouping.getByRole('radio', { name, exact: true }).click()
        await expect(grouping.getByRole('radio', { name, exact: true })).toHaveAttribute('aria-checked', 'true')
      } })) })
      const density = dialog.getByRole('radiogroup', { name: 'Row height' })
      await expectStableControls({
        controls: { ...pinned, density, compact: density.getByRole('radio', { name: 'Compact' }), comfortable: density.getByRole('radio', { name: 'Comfortable' }) },
        scrollAreas: { body: scroll },
        interactions: ['Compact', 'Comfortable', 'Compact'].map(name => ({ name, run: async () => {
          await density.getByRole('radio', { name }).click()
          await expect(density.getByRole('radio', { name })).toHaveAttribute('aria-checked', 'true')
          await expect.poll(() => data.preferences['list:display']?.density).toBe(name.toLowerCase())
        } })),
      })
      const columns = dialog.getByRole('list', { name: 'Columns', exact: true })
      const checkbox = columns.getByRole('checkbox', { name: 'Cost unit', exact: true })
      const columnRow = columns.locator('.row').filter({ has: page.getByRole('checkbox', { name: 'Cost unit', exact: true }) })
      await expectStableControls({
        controls: { ...pinned, columns, columnRow, checkbox }, scrollAreas: { body: scroll },
        interactions: [{ name: 'show Cost unit', run: async () => {
          await checkbox.check()
          await expect.poll(() => data.preferences['list:p-pharos']?.visible).toContain('cost')
        } }, { name: 'hide Cost unit', run: async () => {
          await checkbox.uncheck()
          await expect.poll(() => data.preferences['list:p-pharos']?.visible).not.toContain('cost')
        } }],
      })
      await dialog.getByRole('button', { name: 'Automatic', exact: true }).click()
      await expect.poll(() => data.preferences['list:p-pharos']?.visible).toBeUndefined()
      if (sheet) {
        const cost = columns.getByRole('checkbox', { name: 'Cost', exact: true })
        const costRow = columns.locator('.row').filter({ has: page.getByRole('checkbox', { name: 'Cost', exact: true }) })
        await expectStableControls({ controls: { ...pinned, columns, cost, costRow, notes: dialog.locator('.columns .notes') }, scrollAreas: { body: scroll }, interactions: [
          { name: 'show Cost with empty-column explanation', run: async () => { await cost.check(); await expect(dialog.locator('.columns .notes')).toContainText('Nothing reported in this list yet') } },
          { name: 'hide Cost and its explanation', run: async () => { await cost.uncheck(); await expect(dialog.locator('.columns .notes')).toBeEmpty() } },
        ] })
        await dialog.getByRole('button', { name: 'Automatic', exact: true }).click()
      }

      for (const [groupName, names, key, expected] of [
        ['Effort meter', ['Off', 'On'], 'effortMeter', [false, true]],
        ['Model names', ['Full', 'Short'], 'modelNames', ['full', 'short']],
        ['Version', ['Hide', 'Show'], 'modelVersion', ['hide', 'show']],
      ] as const) {
        const group = dialog.getByRole('radiogroup', { name: groupName, exact: true })
        await expectStableControls({
          controls: { ...pinned, group, first: group.getByRole('radio', { name: names[0], exact: true }), second: group.getByRole('radio', { name: names[1], exact: true }) },
          scrollAreas: { body: scroll }, interactions: names.map((name, index) => ({ name: `${groupName} ${name}`, run: async () => {
            await group.getByRole('radio', { name, exact: true }).click()
            await expect.poll(() => data.preferences['list:display']?.[key]).toBe(expected[index])
          } })),
        })
      }
      const graph = dialog.getByRole('checkbox', { name: 'Graph in project header' })
      await expectStableControls({ controls: { ...pinned, graph, graphRow: dialog.locator('.section').filter({ has: page.getByRole('checkbox', { name: 'Graph in project header' }) }) }, scrollAreas: { body: scroll }, interactions: [
        { name: 'enable header graph', run: async () => { await graph.check(); await expect.poll(() => data.preferences['list:display']?.headerGraph).toBe(true) } },
        { name: 'disable header graph', run: async () => { await graph.uncheck(); await expect.poll(() => data.preferences['list:display']?.headerGraph).toBe(false) } },
      ] })

      const select = dialog.getByRole('combobox', { name: 'Sort key 1' })
      await expectStableControls({ controls: { ...pinned, select, sortRow: dialog.locator('[data-sort-row="0"]') }, scrollAreas: { body: scroll }, interactions: [
        { name: 'sort by priority', run: async () => { await select.selectOption('priority'); await expect(page).toHaveURL(/sort=priority/); await expect.poll(() => calls.filter(c => c.path === '/api/nodes' && c.query.get('within')).at(-1)?.query.get('sort')).toMatch(/^priority/) } },
        { name: 'reverse priority', run: async () => { await dialog.getByRole('button', { name: /^Priority: ascending/ }).click(); await expect(page).toHaveURL(/sort=-priority/) } },
      ] })
      if (sheet) {
        const add = dialog.getByRole('button', { name: 'Add sort key' })
        await expectStableControls({ controls: { ...pinned, add, density }, scrollAreas: { body: scroll }, interactions: [
          { name: 'add a second sort key', run: async () => { await add.click(); await expect(dialog.getByRole('combobox', { name: 'Sort key 2' })).toBeVisible() } },
          { name: 'remove the second key', run: async () => { await dialog.locator('[data-sort-row="1"]').getByRole('button', { name: /^Remove/ }).click(); await expect(dialog.getByRole('combobox', { name: 'Sort key 2' })).toHaveCount(0) } },
        ] })
        await select.scrollIntoViewIfNeeded()
        await capture(page, `display-sort-${width}-${theme}`)
        await scroll.evaluate(el => { el.scrollTop = 0 })
        await inFrame(dialog.locator('footer button'), dialog)
        await capture(page, `display-${width}-${theme}`)
        await dialog.locator('footer button').click()
        await expect(opener).toBeFocused()
      } else {
        await capture(page, `display-${width}-${theme}`)
        await page.keyboard.press('Escape')
      }
      await expect(page.locator('.table-card')).toHaveClass(/compact/)
      await page.reload()
      await expect(page.locator('.table-card')).toHaveClass(/compact/)
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
    })

    test(`Compare works by pointer with long German captions at ${width}px ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 })
      await mockWork(page, germanWorld(theme))
      await page.goto('/p/PHAROS/PHAROS-11')
      await page.locator('.tile[data-attachment-id="att-1"]').click()
      const box = page.locator('dialog.lightbox[open]')
      const compare = box.getByRole('button', { name: 'Compare', exact: true })
      const details = box.getByRole('button', { name: 'Details', exact: true })
      const close = box.getByRole('button', { name: 'Close viewer' })
      await inFrame(compare, box)
      await expectStableControls({ controls: { box, compare, details, close }, scrollAreas: { box }, interactions: [
        { name: 'start Compare', run: async () => { await compare.click(); await expect(box.getByRole('slider', { name: 'Compare divide' })).toBeVisible() } },
        { name: 'side by side', run: async () => { await box.getByRole('radio', { name: 'Side by side' }).click(); await expect(box.locator('.side')).toHaveCount(2) } },
        { name: 'stop Compare', run: async () => { await compare.click(); await expect(box.getByRole('radiogroup', { name: 'Compare view' })).toHaveCount(0) } },
      ] })
      await compare.click()
      await expect(box.getByRole('slider', { name: 'Compare divide' })).toBeVisible()
      const modes = box.getByRole('radiogroup', { name: 'Compare view' })
      const side = modes.getByRole('radio', { name: 'Side by side', exact: true })
      const slider = modes.getByRole('radio', { name: 'Slider', exact: true })
      const onion = modes.getByRole('radio', { name: 'Onion skin', exact: true })
      await expectStableControls({ controls: { box, compare, details, close, modes, side, slider, onion, strip: box.getByRole('list', { name: 'All attachments' }) }, scrollAreas: { box, footer: box.locator('footer') }, interactions: [
        { name: 'side by side', run: async () => { await side.click(); await expect(side).toHaveAttribute('aria-checked', 'true'); await expect(box.locator('.side')).toHaveCount(2) } },
        { name: 'onion skin', run: async () => { await onion.click(); await expect(onion).toHaveAttribute('aria-checked', 'true'); await expect(box.getByRole('slider', { name: 'Overlay opacity' })).toBeVisible() } },
        { name: 'adjust opacity', run: async () => { const opacity = box.getByRole('slider', { name: 'Overlay opacity' }); await opacity.fill('0.25'); await expect(box.locator('.compare-frame .over')).toHaveCSS('opacity', '0.25') } },
        { name: 'slider', run: async () => { await slider.click(); await expect(slider).toHaveAttribute('aria-checked', 'true'); await expect(box.getByRole('slider', { name: 'Compare divide' })).toBeVisible(); await expect(box.getByRole('slider', { name: 'Overlay opacity' })).toBeHidden() } },
        { name: 'side again', run: async () => { await side.click(); await expect(side).toHaveAttribute('aria-checked', 'true') } },
      ] })
      await onion.click()
      await capture(page, `compare-onion-${width}-${theme}`)
      await compare.click()
      await expect(modes).toHaveCount(0)
      await compare.click()
      await capture(page, `compare-${width}-${theme}`)
      await close.click()
      await expect(box).toHaveCount(0)
    })
  }
}

test('phone cards render saved column visibility and order at 390px', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 900 })
  const data = germanWorld('light')
  const costUnit = 'Mandantenübergreifende Berechtigungsverwaltung und Qualitätssicherung'
  data.nodes.find(n => n.id === 'n-1')!.fields.cost_unit = costUnit
  await mockWork(page, data)
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  const card = page.locator('#row-n-1')
  await expect(card.locator('.c-cost')).toHaveCount(0)
  const opener = page.getByRole('button', { name: 'Filters', exact: true })
  const sheet = page.getByRole('dialog', { name: 'Filters', exact: true })
  const columns = sheet.getByRole('list', { name: 'Columns', exact: true })
  await opener.click()
  const cost = columns.getByRole('checkbox', { name: 'Cost unit', exact: true })
  await expectStableControls({ controls: { sheet, done: sheet.locator('footer button'), columns, cost }, scrollAreas: { body: sheet.locator('.sheet-scroll') }, interactions: [
    { name: 'show Cost unit on cards', run: async () => { await cost.check(); await expect(card.locator('.c-cost')).toContainText(costUnit) } },
  ] })
  await sheet.locator('footer button').click()
  await inFrame(card.locator('.c-cost'), card)
  await expect(card.locator('.c-cost')).toContainText(costUnit)
  await page.reload()
  await expect(card.locator('.c-cost')).toBeVisible()
  await expect(card.locator('.c-cost')).toContainText(costUnit)
  await opener.click()
  for (const checkbox of await columns.getByRole('checkbox').all()) {
    if (await checkbox.isEnabled()) await checkbox.uncheck()
  }
  await sheet.locator('footer button').click()
  await expect(card.locator('td')).toHaveCount(2)
  await expect(card.locator('.c-key')).toContainText('PHAROS-11')
  await expect(card.locator('.c-title')).toContainText(data.nodes.find(n => n.id === 'n-1')!.title)
  await opener.click()
  await columns.getByRole('checkbox', { name: 'Assignee', exact: true }).check()
  await columns.getByRole('checkbox', { name: 'Cost unit', exact: true }).check()
  const costRow = columns.locator('[data-column-row="cost"]')
  await costRow.focus()
  await page.keyboard.press('Alt+ArrowUp')
  await page.keyboard.press('Alt+ArrowUp')
  await page.keyboard.press('Alt+ArrowUp')
  await page.keyboard.press('Alt+ArrowUp')
  await sheet.locator('footer button').click()
  await inFrame(card.locator('.c-assignee'), card)
  await inFrame(card.locator('.c-cost'), card)
  await expect(card.locator('.c-assignee .person-name')).toContainText(data.people[0]!.name)
  const costBox = (await card.locator('.c-cost').boundingBox())!
  const assigneeBox = (await card.locator('.c-assignee').boundingBox())!
  expect(costBox.y + costBox.height).toBeLessThanOrEqual(assigneeBox.y + .5)
  expect(await page.locator('.table-card').evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
  for (const width of [390, 1024, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    for (const theme of ['light', 'dark']) {
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
      await capture(page, `columns-custom-${width}-${theme}`)
    }
  }
  await page.setViewportSize({ width: 390, height: 900 })
  await opener.click()
  await sheet.getByRole('button', { name: 'Automatic', exact: true }).click()
  await sheet.locator('footer button').click()
  await expect(card.locator('.c-cost, .c-assignee')).toHaveCount(0)
  await expect(card.locator('.c-status')).toBeVisible()
})

for (const width of [390, 768]) {
  test(`select every paginated match without moving bulk actions at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    // A bounded two-row API page exercises the real pagination path, without a huge list.
    let release!: () => void
    const until = new Promise<void>(resolve => { release = resolve })
    await mockWork(page, germanWorld('light'), { listPageSize: 2, hold: ({ path, query }) => path === '/api/nodes' && query.has('cursor') ? { until } : undefined })
    await page.goto('/p/PHAROS')
    await expect(rows(page)).toHaveCount(2)
    if (width <= 720) {
      await page.getByRole('button', { name: 'Select', exact: true }).click()
      await page.getByRole('checkbox', { name: 'Select PHAROS-11', exact: true }).click()
    } else {
      await rows(page).first().hover()
      await page.getByRole('checkbox', { name: 'Select PHAROS-11', exact: true }).check()
    }
    const dock = page.getByRole('toolbar', { name: /selected ticket/ })
    const actions = { status: dock.getByRole('button', { name: 'Status', exact: true }), clear: dock.getByRole('button', { name: 'Clear the selection' }), priority: dock.getByRole('button', { name: 'Priority', exact: true }) }
    await expectStableControls({ controls: actions, scrollAreas: { dock }, interactions: [
      { name: 'select the rest of the loaded page', run: async () => {
        const select = page.getByRole('checkbox', { name: 'Select PHAROS-12', exact: true })
        if (width <= 720) await select.click(); else await select.check()
        await expect(dock.getByRole('button', { name: 'Select all 5', exact: true })).toBeVisible()
      } },
      { name: 'select unloaded matches', run: async () => {
        await dock.getByRole('button', { name: 'Select all 5', exact: true }).click()
        release()
        await expect(dock).toHaveAttribute('aria-label', '5 selected tickets')
        await expect(rows(page)).toHaveCount(5)
        const checks = page.getByRole('checkbox', { name: /^Select PHAROS-/ }).filter({ visible: true })
        await expect(checks).toHaveCount(5)
        for (const check of await checks.all()) await expect(check).toBeChecked()
        await expect(dock.getByRole('button', { name: 'Select all 5' })).toHaveCount(0)
      } },
    ] })
    for (const theme of ['light', 'dark']) {
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
      await capture(page, `bulk-${width}-${theme}`)
    }
    await actions.clear.click()
    await expect(dock).toHaveCount(0)
  })
}
