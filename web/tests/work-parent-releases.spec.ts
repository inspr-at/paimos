// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { journeyWorld, mockJourney } from './journey-fixtures'
import { expectStableControls } from './helpers/stable'

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`parent ships-in and release choice ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await page.addInitScript(value => {
      const apply = () => { if (document.documentElement) document.documentElement.dataset.theme = value }
      if (document.documentElement) apply()
      else document.addEventListener('DOMContentLoaded', apply, { once: true })
    }, theme)
    const errors = watchErrors(page)
    const data = fixtures()
    for (const node of data.nodes.filter(n => ['epic', 'ticket', 'task'].includes(n.kind_slug))) node.kind_slug = 'work'
    data.nodes.find(n => n.id === 'n-2')!.state = 'done'
    data.nodes.find(n => n.id === 'n-2')!.title = 'Wiederkehrende Sicherheitsprüfung und Veröffentlichung der abgestimmten Verbesserungen'
    await mockWork(page, data)
    const world = journeyWorld('plan')
    const marketingName = 'Kobaltkomet · Abgestimmte Verbesserungen der wiederkehrenden Sicherheitsprüfungen'
    world.releases.find(r => r.id === world.journey.current_release_id)!.title = marketingName
    await mockJourney(page, world)
    let split = false
    let backlog = false
    await page.route('**/api/projects/*/release-memberships*', async route => {
      const ids = new URL(route.request().url()).searchParams.getAll('ticket_node_id')
      await route.fulfill({ json: { tickets: ids.map(id => ({
        ...(id === 'n-3' && backlog ? { ticket_node_id: id, is_parent: false, release_count: 0, release_node_id: null, release_title: null, release_state: null, leaf_node_ids: [id], inheritance_note: 'parent_release_closed' } : {
        ticket_node_id: id, is_parent: id === 'n-2', release_count: id === 'n-2' && split ? 2 : 1,
        release_node_id: id === 'n-2' && split ? null : world.journey.current_release_id,
        release_title: id === 'n-2' && split ? null : marketingName,
        release_state: id === 'n-2' && split ? null : 'planning', leaf_node_ids: id === 'n-2' ? ['n-3'] : [id],
      }) })) } })
    })
    const writes: unknown[] = []
    await page.route('**/api/projects/*/releases/*/membership', async route => {
      writes.push(route.request().postDataJSON())
      const walker = world.walkers[world.journey.current_release_id!]!
      await route.fulfill({ json: { walker, event_id: 0, leaf_node_ids: ['n-3'] } })
    })
    await page.goto('/p/PHAROS/PHAROS-12')
    const panel = page.getByRole('complementary', { name: 'Ticket details' })
    const release = panel.getByRole('button', { name: /Release: Ships in/ })
    await expect(release).toContainText(`Ships in ${marketingName}`)
    const shots = resolve('test-results/aeon-653-wn')
    mkdirSync(shots, { recursive: true })
    await page.screenshot({ path: resolve(shots, `parent-ships-in-${width}-${theme}.png`), animations: 'disabled' })
    await expectStableControls({
      controls: { release, status: panel.getByRole('button', { name: /^Status:/ }) },
      interactions: [{ name: 'open and close release picker', run: async () => {
        await release.click()
        await expect(page.getByRole('listbox', { name: 'Releases' })).toBeVisible()
        await page.keyboard.press('Escape')
        await expect(page.getByRole('listbox', { name: 'Releases' })).toHaveCount(0)
      } }],
    })
    await release.click()
    const picker = page.getByRole('dialog', { name: 'Release for PHAROS-12' })
    const choices = picker.getByRole('listbox', { name: 'Releases' })
    const current = choices.getByRole('option').first()
    await expect(current).toBeVisible()
    await expectStableControls({
      controls: width === 390 ? { choices, current, close: picker.getByRole('button', { name: 'Close Esc' }), frame: picker } : { choices, current },
      scrollAreas: { picker },
      interactions: [{ name: 'keyboard selection', run: async () => { await choices.focus(); await choices.press('ArrowDown'); await choices.press('ArrowUp') } }],
    })
    await page.screenshot({ path: resolve(shots, `parent-release-picker-${width}-${theme}.png`), animations: 'disabled' })
    await current.click()
    await expect(picker).toHaveCount(0)
    await expect(page.locator('.toast').filter({ hasText: `Added 1 leaf under PHAROS-12 to ${marketingName}.` })).toBeVisible()
    expect(writes).toHaveLength(1)
    expect(writes[0]).toMatchObject({ ticket_node_ids: ['n-2'], confirm_move: false })
    // Re-fetch on navigation: the parent summary follows actual leaf releases.
    split = true
    data.nodes.find(n => n.id === 'n-2')!.state = 'backlog'
    await page.reload()
    await expect(panel.getByRole('button', { name: /Release: Ships in 2 releases/ })).toBeVisible()
    await page.screenshot({ path: resolve(shots, `parent-split-releases-${width}-${theme}.png`), animations: 'disabled' })
    await page.goto('/p/PHAROS?cols=key,title,release')
    const row = page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-12$/ }) })
    const cellRelease = row.getByRole('button', { name: /Ships in 2 releases.*Change release/ })
    await expect(cellRelease).toBeVisible()
    await expectStableControls({
      controls: { cellRelease, row },
      scrollAreas: { row },
      interactions: [{ name: 'list release picker', run: async () => {
        await cellRelease.click()
        await expect(page.getByRole('listbox', { name: 'Releases' })).toBeVisible()
        await page.keyboard.press('Escape')
        await expect(page.getByRole('listbox', { name: 'Releases' })).toHaveCount(0)
      } }],
    })
    await page.screenshot({ path: resolve(shots, `parent-release-list-${width}-${theme}.png`), animations: 'disabled' })
    backlog = true
    const fresh = data.nodes.find(n => n.id === 'n-3')!
    fresh.fields.release_inheritance_note = 'parent_release_closed'
    fresh.state = 'backlog'
    await page.goto('/p/PHAROS/PHAROS-13')
    await expect(panel.getByText('This leaf started in the backlog because the parent release was frozen or released.')).toBeVisible()
    await expect(panel.getByRole('button', { name: 'Release: none. Change release' })).toBeVisible()
    await page.screenshot({ path: resolve(shots, `leaf-backlog-note-${width}-${theme}.png`), animations: 'disabled' })
    expect(errors).toEqual([])
  })
}
