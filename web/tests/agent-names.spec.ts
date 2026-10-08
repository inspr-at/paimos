// SPDX-License-Identifier: AGPL-3.0-only
// AEON-791: Settings › Vocabulary › Agent names. The names share the work
// vocabulary's revision, so the risks are a card wiping or conflicting with the
// other's save, and a saved word that never reaches the screens naming the lead.
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { controlStability } from './control-stability'
import { mockLeadFlow } from './lead-flow-fixtures'

type Vocabulary = { revision: number; leaf: { name: string; icon: string }; levels: { name: string; icon: string }[]; lead?: { singular: string; plural: string } }
async function vocabularyServer(page: Page, initial: Vocabulary) {
  let vocabulary = initial
  const puts: Record<string, unknown>[] = []
  await page.route('**/api/settings/work-vocabulary', async route => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON()
      puts.push(body)
      if (body.revision !== vocabulary.revision) return route.fulfill({ status: 409, json: { error: 'work vocabulary changed; reload before saving' } })
      // Like the server: a write without lead keeps the saved names; blank names clear them.
      const lead = 'lead' in body ? (body.lead.singular || body.lead.plural ? body.lead : undefined) : vocabulary.lead
      vocabulary = { revision: vocabulary.revision + 1, leaf: body.leaf, levels: body.levels, ...(lead ? { lead } : {}) }
    }
    return route.fulfill({ json: vocabulary })
  })
  return { puts, current: () => vocabulary }
}

for (const width of [1440, 390]) {
  test(`agent names save beside the work levels without wiping or conflicting at ${width}`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const errors = watchErrors(page)
    await mockWork(page, fixtures(), { admin: true })
    await mockBusiness(page, businessData({ role: 'admin' }), { role: 'admin' })
    await mockSettings(page, settingsData())
    const server = await vocabularyServer(page, { revision: 4, leaf: { name: 'Arbeitsschritt', icon: 'check' }, levels: [{ name: 'Vorhaben', icon: 'tree' }] })
    await page.goto('/settings/vocabulary')
    const card = page.locator('#agent-names'), work = page.locator('#work-vocabulary')
    const one = card.getByLabel('Name, one'), several = card.getByLabel('Name, several'), save = card.getByRole('button', { name: /Save names/ })
    await expect(one).toBeEnabled()
    await expect(card.getByRole('status')).toHaveText('Blank names use “Lead” and “Leads”.')
    const guard = await controlStability(page, { actions: card.getByLabel('Agent name actions'), save, one, several, row: card.locator('.names-row'), suggest: card.getByRole('button', { name: 'Supervisor' }) })
    await guard.check(() => card.getByRole('button', { name: 'Coordinator' }).click())
    await expect(several).toHaveValue('Coordinators')
    await guard.check(async () => { await one.fill('Dirigent'); await several.fill('Dirigenten') })
    await expect(card.getByRole('status')).toHaveText('Not saved yet: “Dirigent” and “Dirigenten”.')
    await expect(card.locator('.preview')).toContainText('AEON dirigent · Working · Start dirigent')
    await expect(card.locator('.preview')).toContainText('Dirigenten · One per project')
    await guard.check(async () => { await save.click(); await expect(card.getByRole('status')).toHaveText('Agent names saved.') })
    guard.done()
    expect(server.puts[0]).toEqual({ revision: 4, leaf: { name: 'Arbeitsschritt', icon: 'check' }, levels: [{ name: 'Vorhaben', icon: 'tree' }], lead: { singular: 'Dirigent', plural: 'Dirigenten' } })
    await page.screenshot({ path: info.outputPath(`agent-names-${width}.png`), fullPage: true })

    // The work levels save next, on the revision the names advanced, and never send the names back.
    await work.getByLabel('Leaf (work item)', { exact: true }).fill('Schritt')
    await work.getByRole('button', { name: /Save names/ }).click()
    await expect(work.getByRole('status')).toContainText('Workspace names saved')
    expect(server.puts[1]).not.toHaveProperty('lead')
    expect(server.puts[1]).toMatchObject({ revision: 5, leaf: { name: 'Schritt' } })
    expect(server.current()).toMatchObject({ revision: 6, lead: { singular: 'Dirigent', plural: 'Dirigenten' } })
    await expect(one).toHaveValue('Dirigent')
    expect(errors).toEqual([])
  })
}

test('the saved word names the lead on the project band', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await mockLeadFlow(page, { lead: 'none' })
  await vocabularyServer(page, { revision: 1, leaf: { name: '', icon: '' }, levels: [], lead: { singular: 'Dirigent', plural: 'Dirigenten' } })
  await page.goto('/p/PHAROS')
  const band = page.locator('section.lead')
  await expect(band.getByRole('heading', { name: 'No dirigent in PHAROS' })).toBeVisible()
  await expect(band.getByRole('button', { name: 'Start dirigent' })).toBeVisible()
})
