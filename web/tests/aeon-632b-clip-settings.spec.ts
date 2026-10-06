// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { businessData, mockBusiness } from './business-fixtures'
import { mockProfiles, profileWorld, PROFILE } from './profile-fixtures'
import { mockRules, RULE_PERSON } from './rules-fixtures'
import { NAME, setup, capture, disclosure } from './aeon-632b-clip-fixtures'

async function portal(page: Page) {
  const kinds = ['portal_product', 'portal_feature', 'portal_wish'].map(slug => ({ id: `k-${slug}`, slug, label: slug, short_prefix: 'PRT', icon: 'box', allowed_child_kinds: null, field_schema: {} }))
  await page.route('**/api/kinds', route => route.fulfill({ json: { items: kinds } }))
  await page.route('**/api/nodes?**', route => {
    const kind = new URL(route.request().url()).searchParams.get('kind')
    if (!kind?.startsWith('portal_')) return route.fallback()
    const states = kind === 'portal_wish' ? ['published', 'hidden', 'pending'] : [kind === 'portal_product' ? 'published' : 'planned']
    return route.fulfill({ json: { items: states.map((state, i) => ({
      id: `${kind}-${i}`, key: `PRT-${i + 1}`, kind_id: `k-${kind}`, kind_slug: kind, kind_label: kind,
      title: NAME + ` · ${state}`, body: 'Synthetische Beschreibung.', fields: {}, state,
      parent_id: kind === 'portal_product' ? null : 'portal_product-0', position: String(i),
      created_at: '2026-10-03T10:00:00Z', updated_at: '2026-10-03T10:00:00Z', deleted_at: null,
      priority: null, assignee: null, parent: null, children_count: 0, project: null,
    })), next_cursor: '' } })
  })
  await page.route('**/api/portal/market', route => route.fulfill({ json: { competitors: [], aspects: [], cells: [], history: [], corrections: [] } }))
  await page.route('**/api/portal/pace', route => route.fulfill({ json: { fulfillments: [], release_history: false } }))
}

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`settings names ${width} ${theme}`, async ({ page }) => {
    test.setTimeout(90_000)
    await page.setViewportSize({ width, height: 900 })
    await setup(page, theme)
    const change = { event_id: 632, node_id: 'n-1', key: 'PHAROS-11', title: NAME, actor: 'Status autopilot', rule: 'new', reason: 'Synthetischer Grund.', from: 'new', to: 'triage_list', at: '2026-10-03T10:00:00Z', undone: false, undoable: false, changed_since: false, applicable: true }
    await page.route('**/api/status-autopilot/changes**', route => route.fulfill({ json: { items: [change] } }))
    await page.route('**/api/status-autopilot/proposals', route => route.fulfill({ json: { items: [change] } }))
    await page.goto('/settings/autopilot')
    await disclosure(page, page.locator('.proj-name > span:last-child').first(), { modes: page.locator('.proj-ctl .seg').first() })
    await capture(page, 'autopilot-projects', width, theme)
    for (const name of await page.locator('.change-title').all()) await disclosure(page, name)
    await capture(page, 'autopilot', width, theme)

    await mockBusiness(page, businessData({ role: 'admin' }), { role: 'admin' })
    const profiles = profileWorld()
    for (const profile of profiles.profiles) for (const revision of profile.revisions) revision.name = `${NAME} · ${profile.id.slice(-1)}`
    await mockProfiles(page, profiles)
    await page.goto('/settings/business')
    await disclosure(page, page.locator('.profile-row .row-name').first())
    await capture(page, 'quote-profiles', width, theme)
    await page.goto(`/settings/business/profiles/${PROFILE.steel}`)
    if (width >= 1200) {
      await disclosure(page, page.locator('.rail .item-name').first(), { create: page.locator('.rail-head button') })
      await page.locator('.rail').getByRole('button', { name: /^Archived/ }).click()
      await disclosure(page, page.locator('.rail .archived-row .item-name'))
    }
    else await expect(page.locator('.profile-switch')).toBeVisible()
    await capture(page, 'profile-rail', width, theme)

    await portal(page)
    await page.goto('/settings/portal')
    const names = page.locator('.section .name')
    await expect(names).toHaveCount(5)
    for (const [index, name] of (await names.all()).entries()) {
      await disclosure(page, name)
      await capture(page, `portal-name-${index + 1}`, width, theme)
    }
    await capture(page, 'portal', width, theme)
  })

  test(`rules names ${width} ${theme}`, async ({ page }) => {
    test.setTimeout(90_000)
    await page.setViewportSize({ width, height: 900 })
    await setup(page, theme)
    await mockRules(page, { draftFailAt: 2 })
    await page.goto('/settings/agent-rules')
    await page.getByRole('button', { name: 'Actions for Scope' }).click()
    await page.getByRole('menuitem', { name: 'Edit rules' }).click()
    await page.getByRole('textbox', { name: 'Set name' }).fill(NAME)
    await page.getByRole('button', { name: 'Save draft' }).click()
    await disclosure(page, page.locator('.set h4').filter({ hasText: NAME }).first())
    await capture(page, 'rule-set', width, theme)
    await page.getByRole('button', { name: 'Review and publish (1 set)' }).click()
    const publish = page.getByRole('dialog', { name: 'Review and publish' })
    await disclosure(page, publish.locator('.name'), { publish: publish.getByRole('button', { name: 'Publish 1 set' }), cancel: publish.getByRole('button', { name: 'Cancel' }) })
    await capture(page, 'rules-publish', width, theme)
    await publish.getByRole('button', { name: 'Cancel' }).click()

    await page.getByRole('button', { name: 'Import', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Import rules' })
    await dialog.locator('#draft-import-file').setInputFiles({ name: 'synthetic-rules.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify({
      schema: 'aeon.rules-draft-import.v1', tenant_id: 't1', layers: [{ scope: { layer: 'person', owner_id: RULE_PERSON }, sets: [1, 2].map(i => ({ name: `${NAME} ${i}`, rules: [{ identity: `clip-${i}`, text: 'Synthetische Regel.', why: 'Test der Namensanzeige.', strength: 'normal', enabled: true, roles: ['builder'], harnesses: ['cursor'], source: { reference: 'AEON-632', edited_here: false } }] })) }],
    })) })
    const summaries = dialog.locator('.set summary')
    await expect(summaries).toHaveCount(2)
    await disclosure(page, summaries.first().locator('.set-name'), { import: dialog.getByRole('button', { name: 'Import 2 sets as drafts' }), cancel: dialog.getByRole('button', { name: 'Cancel' }) })
    await capture(page, 'rules-import', width, theme)
    await dialog.getByRole('button', { name: 'Import 2 sets as drafts' }).click()
    await expect(dialog.locator('.outcomes .set-name')).toHaveCount(2)
    await disclosure(page, dialog.locator('.outcomes .set-name').first(), { close: dialog.getByRole('button', { name: 'Close', exact: true }) })
    await capture(page, 'rules-import-results', width, theme)
  })
}
