// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { fixtures, mockWork } from './work-fixtures'
import { journeyWorld, mockJourney } from './journey-fixtures'
import { expectStableControls } from './helpers/stable'

function migrated() {
  const data = fixtures()
  for (const row of data.nodes) {
    if (!['ticket', 'task', 'epic'].includes(row.kind_slug)) continue
    row.is_leaf = !data.nodes.some(child => child.parent_id === row.id && ['ticket', 'task', 'epic', 'work'].includes(child.kind_slug))
    row.level_name = row.is_leaf ? 'Ticket' : 'Epic'; row.level_icon = row.is_leaf ? 'ticket' : 'epic'
    row.kind_slug = 'work'
  }
  return data
}

async function shot(page: Page, view: string, width: number, theme: string) {
  mkdirSync('test-results/aeon-648-int-fix2', { recursive: true })
  await page.screenshot({ path: `test-results/aeon-648-int-fix2/${view}-${width}-${theme}.png`, fullPage: true })
}

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`canonical work palette and relation key/title search at ${width}px ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.emulateMedia({ colorScheme: theme })
    const calls = await mockWork(page, migrated())
    await page.goto('/p/PHAROS/PHAROS-12')
    await expect(page.getByRole('complementary', { name: 'Ticket details' }).getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    await page.keyboard.press('Control+k')
    const palette = page.getByRole('dialog', { name: 'Search and commands' })
    const search = palette.getByRole('combobox')
    await search.fill('PHAROS-11')
    await expect(palette.getByRole('option').first()).toContainText('PHAROS-11')
    await expectStableControls({ controls: { search }, interactions: [{ name: 'title query', run: async () => {
      await search.fill('Hetzner'); await expect(palette.getByRole('group', { name: 'Tickets' }).getByRole('option')).toHaveCount(2)
    } }] })
    await shot(page, 'palette', width, theme)
    await page.keyboard.press('Escape')
    const region = page.getByRole('region', { name: 'Relations' }).locator('visible=true')
    await region.getByRole('button', { name: 'Link', exact: true }).click()
    const picker = page.getByRole('dialog', { name: 'Link PHAROS-12 to another ticket' })
    const relationSearch = picker.getByRole('combobox')
    await relationSearch.fill('PHAROS-11')
    await expect(picker.getByRole('option').first()).toContainText('PHAROS-11')
    await expectStableControls({ controls: { relationSearch, types: picker.getByRole('radiogroup') }, interactions: [{ name: 'title query', run: async () => {
      await relationSearch.fill('Hetzner'); await expect(picker.getByRole('option')).toHaveCount(2)
    } }] })
    await shot(page, 'relations', width, theme)
    await picker.getByRole('option').first().click()
    await expect(picker).toBeHidden()
    const searchCalls = calls.filter(c => c.path === '/api/nodes' && c.query.get('q') && c.query.get('limit') === '8')
    expect(searchCalls).toHaveLength(4)
    expect(searchCalls.every(c => c.query.get('kind')?.split(',').includes('work'))).toBe(true)
    expect(calls.filter(c => c.method === 'POST' && c.path === '/api/relations')).toHaveLength(1)
  })

  test(`canonical leaf classification renders, confirms and saves at ${width}px ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 }); await page.emulateMedia({ colorScheme: theme })
    const data = migrated(), row = data.nodes.find(n => n.id === 'n-1')!
    Object.assign(row.fields, { area: 'backend', area_source: 'suggested', complexity: 'M', complexity_source: 'suggested' })
    row.title = 'Hetzner Cloud: Verwaltungszugang und nachvollziehbare Bereitstellung für besonders umfangreiche Projektanforderungen prüfen'
    const revision = row.updated_at, calls = await mockWork(page, data)
    await page.route('**/api/work-kinds?**', route => route.fulfill({ json: { items: ['backend', 'security'].map(slug => ({ id: slug, slug, label: slug, position: 0 })), next_cursor: null } }))
    await page.goto('/p/PHAROS/PHAROS-11')
    const panel = page.getByRole('complementary', { name: 'Ticket details' })
    const area = panel.getByRole('combobox', { name: 'Kind of work', exact: true }), complexity = panel.getByRole('combobox', { name: 'Complexity', exact: true })
    await expect(area).toBeEnabled()
    await expectStableControls({ controls: { area, complexity, areaRow: area.locator('..'), complexityRow: complexity.locator('..') }, scrollAreas: { properties: panel.locator('.ws-props') }, interactions: [
      { name: 'confirm suggestion', run: async () => { await area.locator('..').getByRole('button', { name: 'Confirm' }).click(); await expect(area.locator('..').getByRole('button')).toHaveCount(0); await expect(area).toBeEnabled() } },
      { name: 'choose security', run: async () => { await area.selectOption('security'); await expect(area).toBeEnabled(); await expect(area).toHaveValue('security') } },
      { name: 'choose complexity', run: async () => { await complexity.selectOption('L'); await expect(complexity).toBeEnabled(); await expect(complexity).toHaveValue('L') } },
    ] })
    const writes = calls.filter(c => c.method === 'PATCH' && c.path === '/api/nodes/n-1')
    expect(writes).toHaveLength(3); expect(writes[0]!.headers['if-unmodified-since']).toBe(revision)
    expect((writes[0]!.body as { fields: object }).fields).not.toHaveProperty('area_source')
    expect((writes[1]!.body as { fields: { area: string } }).fields.area).toBe('security')
    expect((writes[2]!.body as { fields: { complexity: string } }).fields.complexity).toBe('L')
    await shot(page, 'classification', width, theme)
    await page.goto('/p/PHAROS/PHAROS-10')
    await expect(page.getByRole('complementary', { name: 'Ticket details' }).getByRole('combobox', { name: 'Kind of work', exact: true })).toHaveCount(0)
  })

  test(`canonical project journey shows leaves and parent groups at ${width}px ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 }); await page.emulateMedia({ colorScheme: theme })
    const calls = await mockWork(page, migrated())
    const world = journeyWorld('open', { derived: true }); world.projectBody = 'Ein übernommenes Projekt mit nachvollziehbaren Arbeitspaketen und eindeutig zugeordneten Verantwortlichkeiten. Die Freigabe einer Bereitstellung setzt eine bestätigte Preisprüfung voraus; nach Abschluss bleiben überprüfbare Ergebnisse und keine unbemerkten Ressourcen zurück.'
    await mockJourney(page, world)
    for (const stage of ['inspire', 'requirements', 'plan', 'build']) {
      await page.goto(`/p/PHAROS?view=journey&stage=${stage}`)
      await expect(page.locator('.journey-view .skeleton')).toHaveCount(0)
      if (stage === 'inspire') {
        const brought = page.getByRole('list', { name: 'What the project brought' })
        await expect(brought).toContainText('2epics'); await expect(brought).toContainText('4tickets · 1 done')
      } else if (stage === 'requirements') {
        const features = page.getByRole('region', { name: /Features · 2 · imported epics/ })
        await expect(features).toContainText('0 of 2 tickets done'); await expect(features).toContainText('2 tickets are not tied to an epic.')
      } else if (stage === 'plan') {
        const backlog = page.getByRole('region', { name: 'Backlog · what release 1 is chosen from' })
        await expect(backlog).toContainText('3 open tickets'); await expect(backlog).toContainText('Guarded multi-cloud provisioning')
        await expect(backlog.getByRole('button', { name: 'Beacon health probes' })).toHaveCount(0)
      } else {
        await expect(page.getByRole('region', { name: 'Later: Not yet' })).toContainText('3 open tickets wait in the backlog')
      }
      const tabs = page.getByRole('navigation', { name: 'Project journey' })
      await expectStableControls({ controls: { tabs, clickedStage: tabs.getByRole('button').nth(4) }, interactions: [{ name: 'navigate to Build and back', run: async () => {
        await tabs.getByRole('button').nth(4).click(); await expect(page).toHaveURL(/stage=build/)
        await tabs.getByRole('button').nth(['inspire', 'shape', 'requirements', 'plan', 'build'].indexOf(stage)).click(); await expect(page).toHaveURL(new RegExp(`stage=${stage}`))
      } }] })
      await shot(page, `journey-${stage}`, width, theme)
    }
    expect(calls.filter(c => c.path === '/api/nodes' && c.query.get('within') === 'p-pharos').some(c => c.query.get('kind')?.split(',').includes('work'))).toBe(true)
  })
}
