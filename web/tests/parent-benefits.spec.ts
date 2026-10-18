// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { expectStableControls } from './helpers/stable'
import { mockSettings, settingsData } from './settings-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import type { ParentBenefitGeneration } from '../src/lib/api'

const generation = '11111111-2222-4333-8444-555555555555'
const nextGeneration = '11111111-2222-4333-8444-666666666666'
const longGerman = 'Abgeschlossene Blätter erklären den gemeinsam erreichten Nutzen auf Deutsch und Englisch, damit auch umfangreiche Änderungen verständlich bleiben und der Abschluss des Elternknotens ohne Wartezeit erfolgt.'
async function prepare(page: Page, readOnly = false) {
  await page.clock.install()
  const data = fixtures()
  const node = data.nodes.find(n => n.key === 'PHAROS-12')!
  node.kind_slug = 'work'; node.state = 'done'
  node.estimate = { is_parent: true, hours: 4, estimated_children: 1, open_children: 0 }
  node.fields = { ...node.fields, pill_en: 'Clear parent benefits', pill_de: 'Verständlicher gemeinsamer Nutzen', benefit_en: 'The completed leaves explain the feature.', benefit_de: longGerman }
  const calls = await mockWork(page, data, { readOnly })
  const errors = watchErrors(page)
  const status: ParentBenefitGeneration = { is_parent: true, status: 'failed', generation, revision: node.updated_at, generated: false, error: 'Benefit generation failed. Check the workspace model provider and retry.' }
  let rejectRetry = false
  const retries: unknown[] = []
  await page.route('**/api/nodes/*/benefit-generation**', async route => {
    const request = route.request()
    if (request.method() === 'POST') {
      retries.push(request.postDataJSON())
      if (rejectRetry) return route.fulfill({ status: 409, json: { error: 'Parent changed' } })
      status.status = 'queued'; status.generation = nextGeneration; status.error = ''
      return route.fulfill({ status: 202, json: status })
    }
    return route.fulfill({ json: status })
  })
  return { data, node, status, calls, errors, retries, rejectRetry: () => { rejectRetry = true } }
}

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`parent retry and generated text keep controls stable ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const world = await prepare(page)
    await page.goto('/p/PHAROS/PHAROS-12')
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    const region = page.getByRole('region', { name: 'User benefit' })
    const actions = region.getByRole('group', { name: 'Parent benefit actions' })
    const retry = actions.getByRole('button', { name: 'Retry generation', exact: true })
    const edit = actions.getByRole('button', { name: 'Edit benefit', exact: true })
    await expect(retry).toBeEnabled()
    await retry.scrollIntoViewIfNeeded()
    await expectStableControls({
      controls: { retry, edit, actions }, scrollAreas: { benefits: region },
      interactions: [
        { name: 'retry queues without changing Done', run: async () => {
          await retry.click()
          await expect(region.getByRole('status')).toContainText('generation is queued')
          await expect(retry).toBeDisabled()
          expect(world.node.state).toBe('done')
        } },
        { name: 'running state', run: async () => {
          world.status.status = 'running'
          await page.clock.fastForward(5001)
          await expect(region.getByRole('status')).toContainText('being generated')
        } },
        { name: 'generated long bilingual text', run: async () => {
          world.status.status = 'generated'; world.status.generated = true
          world.node.fields.benefit_de = longGerman.repeat(3)
          world.node.updated_at = new Date(Date.parse(world.node.updated_at) + 1000).toISOString()
          await page.clock.fastForward(5001)
          await expect(region.getByRole('status')).toContainText('Generated')
          await expect(region.locator('[lang=de]')).toContainText(longGerman.repeat(3))
        } },
      ],
    })
    expect(world.retries).toEqual([{ expected_generation: generation, expected_revision: world.status.revision }])
    await page.screenshot({ path: `test-results/aeon-654-wn/parent-${width}-${theme}.png` })
    await edit.click()
    await expect(page.getByLabel('Benefit · Deutsch')).toHaveValue(longGerman.repeat(3))
    await page.getByLabel('Benefit · Deutsch').fill('Von einer Person bearbeiteter Nutzen bleibt erhalten.')
    await page.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(region.locator('[lang=de]')).toContainText('Von einer Person bearbeiteter Nutzen bleibt erhalten.')
    expect(world.errors).toEqual([])
  })
}

test('failed retry stays actionable and flag-off hides generation controls', async ({ page }) => {
  const world = await prepare(page); world.rejectRetry()
  await page.goto('/p/PHAROS/PHAROS-12')
  const region = page.getByRole('region', { name: 'User benefit' })
  await region.getByRole('button', { name: 'Retry generation' }).click()
  await expect(region.getByRole('alert')).toContainText('could not be queued')
  await expect(region.getByRole('button', { name: 'Retry generation' })).toBeEnabled()
  await page.reload()
  world.status.is_parent = false
  await page.clock.fastForward(5001)
  await expect(region.getByRole('group', { name: 'Parent benefit actions' })).toHaveCount(0)
})

test('read-only generation status remains visible', async ({ page }) => {
  await prepare(page, true)
  await page.goto('/p/PHAROS/PHAROS-12')
  const region = page.getByRole('region', { name: 'User benefit' })
  await expect(region.getByRole('status')).toContainText('failed')
  await expect(region.getByRole('button', { name: 'Retry generation' })).toHaveCount(0)
  await expect(region.getByRole('button', { name: 'Edit benefit' })).toHaveCount(0)
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`workspace parent benefit opt-in ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await mockWork(page, fixtures(), { admin: true })
    await mockBusiness(page, businessData({ role: 'admin' }), { role: 'admin' })
    await mockSettings(page, settingsData())
    const state = { enabled: true, base_url: 'http://localhost:11434/v1', chat_model: 'fixture', embedding_model: '', features: { parent_benefits: false, crm_note_rewrite: false, embeddings: false }, revision: 1, has_api_key: false }
    let written: unknown
    await page.route('**/api/settings/model-provider', async route => {
      if (route.request().method() === 'PUT') { written = route.request().postDataJSON(); state.features.parent_benefits = true; state.revision++ }
      await route.fulfill({ json: state })
    })
    await page.goto('/settings/workspace')
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    const card = page.getByRole('region', { name: 'In-app AI' })
    const option = card.getByLabel('Generate parent benefits from leaves')
    const save = card.getByRole('button', { name: 'Save settings' })
    await expect(option).not.toBeChecked()
    await option.scrollIntoViewIfNeeded()
    await expectStableControls({ controls: { option, save }, scrollAreas: { card }, interactions: [{ name: 'opt in', run: async () => { await option.check(); await expect(save).toBeEnabled() } }] })
    await save.click()
    await expect(card.getByRole('status')).toContainText('Saved')
    expect(written).toMatchObject({ features: { parent_benefits: true }, expected_revision: 1 })
    await option.scrollIntoViewIfNeeded()
    await page.screenshot({ path: `test-results/aeon-654-wn/settings-${width}-${theme}.png` })
  })
}

for (const [state, category, completed] of [['custom-complete', 'done', true], ['done', 'open', false]] as const) {
  // Independent fixtures keep the complete six-layout matrix inside the
  // existing per-test budget, including save and reopen assertions in each one.
  const contexts = completed
    ? [390, 1024, 1440].flatMap(width => ['light', 'dark'].map(theme => ({ width, theme })))
    : [{ width: 1440, theme: 'light' }]
  for (const [index, { width, theme }] of contexts.entries()) {
    const layout = index === 0 ? '' : ` at ${width} ${theme}`
    test(`leaf benefit guidance resolves ${state} as ${category}${layout}`, async ({ page }, testInfo) => {
      await page.setViewportSize({ width, height: 1000 })
      const data = fixtures()
      const node = data.nodes.find(n => n.key === 'PHAROS-12')!
      node.kind_slug = 'work'; node.state = state
      node.estimate = { is_parent: false, hours: null, estimated_children: 0, open_children: 0 }
      node.fields = { ...node.fields, pill_en: '', pill_de: '', benefit_en: '', benefit_de: '' }
      const child = data.nodes.find(n => n.parent_id === node.id)!
      child.parent_id = node.parent_id
      const errors = watchErrors(page)
      await mockWork(page, data)
      await page.route('**/api/kinds', route => route.fulfill({ json: { items: [{ id: 'k-work', slug: 'work', label: 'Work', short_prefix: 'WRK', icon: 'work', allowed_child_kinds: null, field_schema: { states: [{ state, category }] } }] } }))
      await page.goto('/p/PHAROS/PHAROS-12')
      const region = page.getByRole('region', { name: 'User benefit' })
      const guidance = completed ? 'This completed ticket has incomplete benefit fields.' : 'Before Done'
      await expect(region).toContainText(guidance)
      await page.getByRole('button', { name: 'Edit', exact: true }).click()
      await expect(region).toContainText(guidance)
      await expect(region).toContainText('Each non-empty pill needs 2–4 words to save.')
      if (completed) {
        // Q1 (AEON-1248): completed context is retained, while the configured
        // entry checks decide completion requirements rather than a second gate.
        await expect(region).toContainText('The project’s entry checks determine what is needed before Done.')
        await expect(region).not.toContainText('Before Done')
      } else await expect(region).not.toContainText('This completed ticket')

      // Risk: restoring completed-ticket context must not move the fields as the
      // draft becomes valid, or describe the next complete edit as incomplete.
      const englishPill = region.getByLabel('Pill · English')
      const germanPill = region.getByLabel('Pill · Deutsch')
      const englishBenefit = region.getByLabel('Benefit · English')
      const germanBenefit = region.getByLabel('Benefit · Deutsch')
      const save = page.getByRole('button', { name: 'Save', exact: true })
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
      await englishPill.fill('')
      await germanPill.fill('')
      await englishBenefit.fill('')
      await germanBenefit.fill('')
      await expectStableControls({
        controls: { englishPill, germanPill, englishBenefit, germanBenefit, save },
        scrollAreas: { region },
        interactions: [
          { name: 'invalid pill', run: async () => { await englishPill.fill('Word'); await expect(englishPill).toHaveAttribute('aria-invalid', 'true') } },
          { name: 'valid English pill', run: async () => { await englishPill.fill('Clear leaf benefits'); await expect(englishPill).not.toHaveAttribute('aria-invalid', 'true') } },
          { name: 'valid German pill', run: async () => { await germanPill.fill('Verständlicher gemeinsamer Nutzen') } },
          { name: 'English benefit', run: async () => { await englishBenefit.fill('Completed work explains the benefit.') } },
          { name: 'German benefit', run: async () => { await germanBenefit.fill(longGerman); await expect(region).toContainText(guidance) } },
        ],
      })
      if (completed) await region.screenshot({ path: testInfo.outputPath(`completed-edit-${width}-${theme}.png`) })
      await save.click()
      await expect(region).toContainText(longGerman)
      await expect(region).not.toContainText('incomplete benefit fields')
      await page.getByRole('button', { name: 'Edit', exact: true }).click()
      await expect(region).toContainText('Each non-empty pill needs 2–4 words to save.')
      await expect(region).not.toContainText('incomplete benefit fields')
      if (completed) await expect(region).toContainText('The project’s entry checks determine what is needed before Done.')
      expect(errors).toEqual([])
    })
  }
}
