// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork, mockView, me } from './work-fixtures'
import { expectStableControls } from './helpers/stable'

const shots = 'test-results/aeon-639'
const errors = new WeakMap<Page, string[]>()
test.beforeEach(async ({ page }) => {
  const failures: string[] = []; errors.set(page, failures)
  page.on('pageerror', error => failures.push(error.message))
  await page.route('**/api/queue?*', route => route.fulfill({ json: { items: [], manual_order: false, capacity: { queued_hours: 0, parallel_runs: 0, work_hours: 0, warning: false } } }))
})
test.afterEach(async ({ page }) => { expect(errors.get(page), 'no browser runtime errors').toEqual([]) })
const search = (page: Page) => page.getByRole('searchbox', { name: 'Search tickets in this project' })
const fold = (page: Page) => page.locator('.phone-header-fold')
const density = (page: Page, name: string) => page.getByRole('radio', { name: `${name} project header`, exact: true })
async function settled(page: Page) { await page.evaluate(async () => { await document.fonts.ready; await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))) }) }
async function samples(controls: Record<string, Locator>) {
  const result: Record<string, { x: number; width: number; height: number }> = {}
  for (const [name, control] of Object.entries(controls)) {
    await expect(control).toBeVisible()
    const rect = (await control.boundingBox())!
    expect(rect.width).toBeGreaterThan(0); expect(rect.height).toBeGreaterThan(0)
    result[name] = rect
  }
  return result
}
async function noSideways(controls: Record<string, Locator>, before: Awaited<ReturnType<typeof samples>>) {
  const after = await samples(controls)
  for (const name of Object.keys(before)) {
    expect(after[name]!.x - before[name]!.x, `${name} sideways movement`).toBe(0)
    expect(after[name]!.width - before[name]!.width, `${name} width change`).toBe(0)
    expect(after[name]!.height - before[name]!.height, `${name} height change`).toBe(0)
  }
}
async function capture(page: Page, name: string) { await settled(page); mkdirSync(shots, { recursive: true }); await page.screenshot({ path: `${shots}/${name}.png` }) }
async function phoneRoomy(page: Page, name: string) {
  await page.getByRole('button', { name: 'Filters', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Filters', exact: true })
  const group = sheet.getByRole('radiogroup', { name: 'Project header density' })
  await group.getByRole('radio', { name, exact: true }).click()
  await expect(group.getByRole('radio', { name, exact: true })).toHaveAttribute('aria-checked', 'true')
  await sheet.locator('footer button').click()
  await settled(page)
}
for (const width of [390, 1024, 1440]) {
  for (const theme of ['light', 'dark']) {
    test(`three densities preserve controls at ${width}px ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 })
      const data = fixtures()
      data.preferences.theme = { choice: theme }
      data.preferences['list:display'] = { density: 'comfortable', headerGraph: false }
      data.projects[0]!.last = new Date(Date.now() - 3 * 60_000).toISOString()
      data.projects[0]!.title = 'Pharos · Betriebsübersicht'
      data.projects[0]!.description = 'Überprüfung der mandantenübergreifenden Berechtigungsverwaltung und außergewöhnlich langer Projektbeschreibungen mit nachvollziehbaren Änderungen für sämtliche verantwortlichen Personen und Agenten.'
      data.views.push(mockView({ id: '44444444-4444-4444-8444-444444444444', name: 'Laufende Betriebsprüfung und Berechtigungsverwaltung' }))
      await mockWork(page, data)
      await page.goto('/p/PHAROS?status=new,backlog,open,blocked,in_progress&type=ticket')
      await expect(page.locator('.project-page')).toHaveClass(/header-compact/)
      await expect(search(page)).toBeVisible()
      await settled(page)
      const toolbar = page.getByRole('toolbar', { name: 'Ticket list controls' })
      const controls: Record<string, Locator> = {
        views: toolbar.getByRole('tablist', { name: 'Ticket views' }), search: search(page), new: page.getByRole('button', { name: 'New ticket', exact: true }),
        ...(width === 390 ? { filters: page.getByRole('button', { name: 'Filters', exact: true }) } : { status: toolbar.locator('.facet-control[data-dim="status"]'), filters: toolbar.locator('.facets'), clear: toolbar.getByRole('button', { name: 'Clear all', exact: true }) }),
      }
      const before = await samples(controls)
      await capture(page, `${width}-${theme}-compact`)
      if (width === 390) {
        await expect(density(page, 'Compact')).toBeHidden()
        await phoneRoomy(page, 'Comfortable')
      } else {
        await expect(fold(page)).toBeHidden()
        await expectStableControls({ controls: { switch: page.getByRole('radiogroup', { name: 'Project header', exact: true }), comfortable: density(page, 'Comfortable'), compact: density(page, 'Compact'), collapsed: density(page, 'Collapsed') },
          interactions: ['Comfortable', 'Collapsed', 'Compact', 'Comfortable'].map(name => ({ name, run: async () => { await density(page, name).click(); await expect(page.locator('.project-page')).toHaveClass(new RegExp(`header-${name.toLowerCase()}`)) } })) })
      }
      await expect(page.locator('.project-page')).toHaveClass(/header-comfortable/)
      await settled(page); await noSideways(controls, before)
      await capture(page, `${width}-${theme}-comfortable`)
      if (width === 390) {
        await expectStableControls({ controls: { fold: fold(page), appbar: page.locator('.app-header') }, interactions: ['collapsed', 'comfortable', 'collapsed'].map(mode => ({ name: mode, run: async () => { await fold(page).click(); await expect(page.locator('.project-page')).toHaveClass(new RegExp(`header-${mode}`)) } })) })
      } else await density(page, 'Collapsed').click()
      await expect(page.locator('#project-header-fold')).toBeHidden()
      await settled(page); await noSideways(controls, before)
      await capture(page, `${width}-${theme}-collapsed`)
      await search(page).focus(); await search(page).press('Escape')
      await expect(page.locator('.project-page')).toHaveClass(/header-collapsed/)
      // Modified shortcut also works while typing, matched by physical key.
      const mac = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent))
      await search(page).focus(); await search(page).press(mac ? 'Meta+Shift+Period' : 'Control+Shift+Period')
      await expect(page.locator('.project-page')).toHaveClass(/header-comfortable/)
      await settled(page); await noSideways(controls, before)
      if (width === 390) await phoneRoomy(page, 'Compact'); else await density(page, 'Compact').click()
      await settled(page); await noSideways(controls, before)
      await expect(page.locator('.chips')).toHaveCount(0)
      if (width !== 390) {
        await expect(page.locator('.project-navigation').getByRole('button', { name: 'Display: Display', exact: true })).toBeVisible()
        await expect(toolbar.getByRole('button', { name: 'Display: Display', exact: true })).toHaveCount(0)
        if (width === 1024) { await expect(toolbar.locator('.facet-value').first()).toBeHidden(); await expect(toolbar.locator('.count-live')).toHaveCount(0) }
        else { await expect(toolbar.locator('.facet-value').first()).toContainText('New, Backlog +3'); await expect(page.locator('.description')).toHaveAttribute('data-clip-tip', '') }
      }
      expect(await page.locator('.project-page').evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
      expect(await page.locator('.app-header').evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
    })
  }
}

test('filter triggers stay still until the popover closes and removals clear real filters', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const data = fixtures(); data.preferences['list:display'] = { headerGraph: false }
  await mockWork(page, data); await page.goto('/p/PHAROS')
  const toolbar = page.getByRole('toolbar', { name: 'Ticket list controls' })
  const status = toolbar.locator('.facet-btn[data-dim="status"]')
  await status.click()
  const dialog = page.getByRole('dialog', { name: 'Filter by Status', exact: true })
  await expect(dialog.getByRole('checkbox', { name: /New/ })).toBeVisible()
  await expectStableControls({ controls: { trigger: status, newRow: dialog.locator('.facet-option').filter({ has: page.getByRole('checkbox', { name: /New/ }) }), new: dialog.getByRole('checkbox', { name: /New/ }), backlog: dialog.getByRole('checkbox', { name: /Backlog/ }) },
    interactions: ['New', 'Backlog'].map(name => ({ name, run: async () => { await dialog.getByRole('checkbox', { name: new RegExp(name) }).check(); await expect(page).toHaveURL(/status=/) } })) })
  await page.keyboard.press('Escape'); await expect(dialog).toBeHidden()
  await expect(status).toHaveAccessibleName(/Edit Status filter: New, Backlog/)
  await toolbar.getByRole('button', { name: 'Remove Status filter' }).click()
  await expect(page).not.toHaveURL(/status=/)
  await expect(toolbar.getByRole('button', { name: 'Clear all', exact: true })).toBeHidden()
  await toolbar.getByRole('button', { name: 'Filter by more' }).click()
  await page.getByRole('menuitem', { name: /Labels/ }).click()
  const labels = page.getByRole('dialog', { name: 'Filter by Labels' })
  await labels.getByRole('checkbox', { name: /BUG/ }).check()
  await page.keyboard.press('Escape')
  await expect(toolbar.getByRole('button', { name: 'Remove Labels filter' })).toBeVisible()
  await toolbar.getByRole('button', { name: 'Remove Labels filter' }).click()
  await expect(page).not.toHaveURL(/tag=/)
})

for (const dimension of ['assignee', 'epic'] as const) {
  test(`delayed ${dimension} labels stay frozen until the filter popover closes`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    const data = fixtures()
    data.preferences['list:display'] = { headerGraph: false }
    const person = data.people.find(person => person.id !== me.id)!
    person.name = 'Verantwortliche für mandantenübergreifende Berechtigungsverwaltung'
    // Keep the assigned record for the name resolver, outside the visible list.
    data.nodes.find(node => node.id === 'n-3')!.state = 'done'
    const epic = data.nodes.find(node => node.id === 'n-epic')!
    epic.title = 'Mandantenübergreifende Berechtigungsverwaltung und Betriebsprüfung'
    const value = dimension === 'assignee' ? person.id : epic.id
    const title = dimension === 'assignee' ? 'Assignee' : 'Epic'
    const placeholder = dimension === 'assignee' ? 'Someone' : 'An epic'
    const resolved = dimension === 'assignee' ? person.name : epic.title
    let release!: () => void, requested!: () => void
    const response = new Promise<void>(resolve => { release = resolve })
    const request = new Promise<void>(resolve => { requested = resolve })
    await mockWork(page, data, { hold: ({ path, query }) => {
      const resolver = dimension === 'assignee'
        ? query.get('assignee') === value && query.get('limit') === '1'
        : query.get('kind') === 'epic'
      if (path === '/api/nodes' && resolver) return { until: response, computed: requested }
    } })
    try {
      await page.goto(`/p/PHAROS?${dimension}=${value}`)
      const toolbar = page.getByRole('toolbar', { name: 'Ticket list controls' })
      const trigger = toolbar.locator(`.facet-btn[data-dim="${dimension}"]`)
      await expect(trigger).toHaveAccessibleName(`Edit ${title} filter: ${placeholder}`)
      await trigger.click()
      const dialog = page.getByRole('dialog', { name: `Filter by ${title}`, exact: true })
      await expect(dialog).toBeVisible()
      await request
      await expectStableControls({
        controls: { trigger, search: search(page), priority: toolbar.locator('.facet-btn[data-dim="priority"]'), more: toolbar.getByRole('button', { name: 'Filter by more' }), clear: toolbar.getByRole('button', { name: 'Clear all', exact: true }), new: toolbar.getByRole('button', { name: 'New ticket', exact: true }) },
        interactions: [{ name: 'resolve the selected label while its menu is open', run: async () => {
          release()
          // The option proves the response reached Vue; no sleep or timing guess.
          await expect(dialog.getByRole('checkbox', { name: new RegExp(resolved) })).toBeVisible()
        } }],
      })
      await expect(trigger).toHaveAccessibleName(`Edit ${title} filter: ${placeholder}`)
      await expect(trigger).toHaveAttribute('data-tip', `${title}: ${placeholder}`)
      await expect(trigger.locator('.facet-value')).toHaveText(` · ${placeholder}`)
      await page.keyboard.press('Escape')
      await expect(dialog).toBeHidden()
      await expect(trigger).toHaveAccessibleName(`Edit ${title} filter: ${resolved}`)
      await expect(trigger).toHaveAttribute('data-tip', `${title}: ${resolved}`)
      await expect(trigger.locator('.facet-value')).toHaveText(` · ${resolved}`)
      await trigger.click()
      await expect(dialog.getByRole('checkbox', { name: new RegExp(resolved) })).toBeChecked()
    } finally { release() }
  })
}

test.describe('phone fold touch target', () => {
  test.use({ hasTouch: true, isMobile: true })
  for (const theme of ['light', 'dark']) {
    test(`the whole 44px phone fold target accepts taps in ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width: 390, height: 900 })
      const data = fixtures()
      data.preferences.theme = { choice: theme }
      await mockWork(page, data)
      await page.goto('/p/PHAROS')
      await expect(page.locator('.project-page')).toHaveClass(/header-compact/)
      const button = fold(page)
      await expect(button).toBeVisible()
      await expectStableControls({ controls: { fold: button, breadcrumb: page.getByRole('navigation', { name: 'Breadcrumb' }), search: page.locator('.search-pill'), appbar: page.locator('.app-header') },
        interactions: [1, 43].map((offset, index) => ({ name: `tap ${index === 0 ? 'top' : 'bottom'} of the target`, run: async () => {
          const rect = (await button.boundingBox())!
          expect(rect.width).toBeGreaterThanOrEqual(44)
          expect(rect.height).toBeGreaterThanOrEqual(44)
          const point = { x: rect.x + rect.width / 2, y: rect.y + offset }
          expect(await button.evaluate((element, point) => element.contains(document.elementFromPoint(point.x, point.y)), point), 'the edge is hittable, not clipped by an ancestor').toBe(true)
          await page.touchscreen.tap(point.x, point.y)
          await expect(page.locator('.project-page')).toHaveClass(index === 0 ? /header-collapsed/ : /header-compact/)
        } })),
      })
      await button.focus()
      await expect(button).toBeFocused()
      expect(await button.evaluate(element => {
        const rect = element.getBoundingClientRect()
        for (let ancestor = element.parentElement; ancestor; ancestor = ancestor.parentElement) {
          const style = getComputedStyle(ancestor)
          if (style.overflowX === 'visible' && style.overflowY === 'visible') continue
          const clip = ancestor.getBoundingClientRect()
          if (clip.top > rect.top - 3 || clip.bottom < rect.bottom + 3 || clip.left > rect.left - 3 || clip.right < rect.right + 3) return false
        }
        return true
      }), 'ancestors leave room for the keyboard focus ring').toBe(true)
    })
  }
})

test('device preference persists across projects and reloads, but separates people and tenants', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures()); await page.goto('/p/PHAROS')
  await density(page, 'Comfortable').click(); await density(page, 'Collapsed').click()
  await page.goto('/p/AEON'); await expect(page.locator('.project-page')).toHaveClass(/header-collapsed/)
  await page.reload(); await expect(page.locator('.project-page')).toHaveClass(/header-collapsed/)
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: '22222222-2222-4222-8222-222222222222', name: 'Mira Holm', kind: 'person', roles: ['member'] }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
  await page.reload(); await expect(page.locator('.project-page')).toHaveClass(/header-compact/)
  await density(page, 'Comfortable').click()
  await page.route('**/api/me', route => route.fulfill({ json: { principal: { ...me, kind: 'person', roles: ['member'] }, tenant: { id: 'other-workspace', name: 'Other workspace' } } }))
  await page.reload(); await expect(page.locator('.project-page')).toHaveClass(/header-compact/)
})

test('phone Display choices and pinned actions stay still through density changes', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 900 })
  await mockWork(page, fixtures()); await page.goto('/p/PHAROS')
  await page.getByRole('button', { name: 'Filters', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Filters', exact: true })
  const group = sheet.getByRole('radiogroup', { name: 'Project header density' })
  await expectStableControls({ controls: { frame: sheet, header: sheet.locator('header'), action: sheet.locator('footer button'), group, comfortable: group.getByRole('radio', { name: 'Comfortable' }), compact: group.getByRole('radio', { name: 'Compact' }) }, scrollAreas: { body: sheet.locator('.sheet-scroll') },
    interactions: ['Comfortable', 'Compact', 'Comfortable'].map(name => ({ name, run: async () => { await group.getByRole('radio', { name }).click(); await expect(group.getByRole('radio', { name })).toHaveAttribute('aria-checked', 'true') } })) })
})


test('scrolling leaves density alone and section changes keep the view settings usable', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 700 })
  const data = fixtures({ bigProject: 100 }); data.preferences['list:display'] = { headerGraph: false }
  await mockWork(page, data); await page.goto('/p/AEON')
  const main = page.locator('#main')
  await expect(page.locator('.project-page')).toHaveClass(/header-compact/)
  await main.evaluate(el => { el.scrollTop = 400 })
  await expect.poll(() => main.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
  await expect(page.locator('.project-page')).toHaveClass(/header-compact/)
  await density(page, 'Collapsed').click()
  await main.evaluate(el => { el.scrollTop = 100 })
  await expect.poll(() => main.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
  await expect(page.locator('.project-page')).toHaveClass(/header-collapsed/)
  await density(page, 'Compact').click()
  await main.evaluate(el => { el.scrollTop = 0 })
  await page.getByRole('tab', { name: 'Knowledge', exact: true }).click()
  await expect(page.getByRole('radiogroup', { name: 'Project header', exact: true })).toBeHidden()
  await page.getByRole('tab', { name: 'Tickets', exact: true }).click()
  await page.getByRole('button', { name: 'Display: Display', exact: true }).click()
  await expect(page.getByRole('dialog', { name: 'Display options' })).toBeVisible()
})

test('phone Graph retains header density in the Display sheet', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 900 })
  await mockWork(page, fixtures()); await page.goto('/p/PHAROS?view=graph')
  await page.getByRole('button', { name: 'Filters', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Filters', exact: true })
  const group = sheet.getByRole('radiogroup', { name: 'Project header density' })
  await expectStableControls({ controls: { group, compact: group.getByRole('radio', { name: 'Compact' }), comfortable: group.getByRole('radio', { name: 'Comfortable' }), action: sheet.locator('footer button') }, scrollAreas: { body: sheet.locator('.sheet-scroll') }, interactions: ['Comfortable', 'Compact'].map(name => ({ name, run: async () => { await group.getByRole('radio', { name }).click(); await expect(group.getByRole('radio', { name })).toHaveAttribute('aria-checked', 'true') } })) })
})
