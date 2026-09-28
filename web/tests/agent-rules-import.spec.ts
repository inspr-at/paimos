// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { RULE_PERSON, mockRules } from './rules-fixtures'

const TENANT = 't1'

function rule(identity: string, text: string, details = '') {
  return {
    identity, text, why: 'Synthetic reason for the import test.', ...(details ? { details } : {}), strength: 'normal', enabled: true,
    roles: ['builder'], harnesses: ['cursor'], source: { reference: 'AEON-252', edited_here: false },
  }
}
function draftFile(layers: unknown, tenant = TENANT) {
  return {
    name: 'drafts.json',
    mimeType: 'application/json',
    buffer: Buffer.from(JSON.stringify({ schema: 'aeon.rules-draft-import.v1', tenant_id: tenant, layers })),
  }
}
function personSets(sets: { name: string; rules: ReturnType<typeof rule>[] }[]) {
  return [{ scope: { layer: 'person', owner_id: RULE_PERSON }, sets }]
}
async function setup(page: Page, options: Parameters<typeof mockRules>[1] = {}) {
  await mockWork(page, fixtures())
  await mockSettings(page, settingsData())
  const rules = await mockRules(page, options)
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('heading', { name: 'Secrets' })).toBeVisible()
  return rules
}
async function openImport(page: Page) {
  await page.getByRole('button', { name: 'Import' }).click()
  return page.getByRole('dialog', { name: 'Import rules' })
}
const writes = (calls: { method: string; path: string }[]) => calls.filter(call => (call.method === 'POST' || call.method === 'PUT') && call.path.startsWith('/api/rules'))

test('a person reviews a draft file and nothing is written or published before confirm', async ({ page }) => {
  const errors = watchErrors(page)
  const rules = await setup(page)
  const dialog = await openImport(page)
  await expect(dialog.getByText('nothing reaches agents until you publish')).toBeVisible()
  await expect(dialog.getByRole('button', { name: 'Import as drafts' })).toBeDisabled()
  await dialog.locator('#draft-import-file').setInputFiles(draftFile(personSets([
    { name: 'Desk', rules: [rule('desk-clear', 'Keep <script>alert(1)</script> as text.')] },
  ])))
  await expect(dialog.getByRole('heading', { name: 'Your rules · Markus Barta' })).toBeVisible()
  await expect(dialog.getByText('New · 1 rule')).toBeVisible()
  await expect(dialog.getByText('1 set · 1 rule')).toBeVisible()
  await expect(dialog.getByText(RULE_PERSON)).toHaveCount(0)
  await dialog.getByText('Desk', { exact: true }).click()
  await expect(dialog.getByText('Keep <script>alert(1)</script> as text.')).toBeVisible()
  await expect(dialog.locator('script')).toHaveCount(0)
  expect(writes(rules.calls)).toHaveLength(0)
  await dialog.getByRole('button', { name: 'Import 1 set as drafts' }).click()
  await expect(page.getByText(/Imported 1 set as drafts. Nothing is live until you publish./)).toBeVisible()
  await expect(page.getByRole('region', { name: 'Your rules' }).getByRole('heading', { name: 'Desk' })).toBeVisible()
  expect(rules.calls.some(call => call.path.endsWith('/publish'))).toBe(false)
  expect(rules.calls.filter(call => call.method === 'POST' && call.path === '/api/rules/sets')).toHaveLength(1)
  const draft = rules.calls.find(call => call.method === 'PUT' && call.path.endsWith('/draft'))
  expect(draft?.body).toMatchObject({ expected_revision: 1, name: 'Desk' })
  expect(errors).toEqual([])
})

test('a file for another workspace is refused before any write', async ({ page }) => {
  const rules = await setup(page)
  const dialog = await openImport(page)
  await dialog.locator('#draft-import-file').setInputFiles(draftFile(personSets([
    { name: 'Desk', rules: [rule('desk-clear', 'Keep the desk clear.')] },
  ]), '99999999-9999-4999-8999-999999999999'))
  await expect(dialog.getByRole('alert')).toContainText('different workspace')
  await expect(dialog.getByText('drafts.json')).toBeVisible()
  await expect(dialog.getByText('Keep the desk clear.')).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: /Import/ })).toBeDisabled()
  expect(writes(rules.calls)).toHaveLength(0)
})

test('a file that writes company rules is refused for a member without publish', async ({ page }) => {
  const rules = await setup(page, { publish: false })
  const dialog = await openImport(page)
  await dialog.locator('#draft-import-file').setInputFiles(draftFile([{ scope: { layer: 'company' }, sets: [{ name: 'Floor', rules: [rule('floor-one', 'One.')] }] }]))
  await expect(dialog.getByRole('alert')).toContainText('needs the workspace permission to publish rules')
  expect(writes(rules.calls)).toHaveLength(0)
})

test('a failed draft stops the import and reports every set truthfully', async ({ page }) => {
  const rules = await setup(page, { draftFailAt: 2, draftFailStatus: 422 })
  const dialog = await openImport(page)
  await dialog.locator('#draft-import-file').setInputFiles(draftFile(personSets([
    { name: 'Desk', rules: [rule('desk-clear', 'Keep the desk clear.')] },
    { name: 'Hours', rules: [rule('hours-log', 'Log the hours.')] },
    { name: 'Later', rules: [rule('later-note', 'Leave a note.')] },
  ])))
  await dialog.getByRole('button', { name: 'Import 3 sets as drafts' }).click()
  await expect(dialog.getByRole('alert')).toContainText('1 of 3 sets were saved as drafts')
  await expect(dialog.getByRole('alert')).toContainText('Nothing was published or rolled back')
  await expect(dialog.getByRole('listitem').filter({ hasText: 'Desk' })).toContainText('Saved as draft')
  await expect(dialog.getByRole('listitem').filter({ hasText: 'Hours' })).toContainText('Not saved')
  await expect(dialog.getByRole('listitem').filter({ hasText: 'Later' })).toContainText('Not started')
  await dialog.getByRole('button', { name: 'Close', exact: true }).click()
  const person = page.getByRole('region', { name: 'Your rules' })
  await expect(person.getByRole('heading', { name: 'Desk' })).toBeVisible()
  await expect(person.getByRole('heading', { name: 'Hours' })).toBeVisible()
  await expect(person.getByRole('heading', { name: 'Later' })).toHaveCount(0)
  expect(rules.calls.filter(call => call.method === 'POST' && call.path === '/api/rules/sets')).toHaveLength(2)
  expect(rules.calls.filter(call => call.method === 'PUT' && call.path.includes('/draft'))).toHaveLength(2)
  expect(rules.calls.some(call => call.path.endsWith('/publish'))).toBe(false)
})

test('a lost reply stops the import and says the set is unconfirmed', async ({ page }) => {
  const rules = await setup(page, { setAbortAt: 2 })
  const dialog = await openImport(page)
  await dialog.locator('#draft-import-file').setInputFiles(draftFile(personSets([
    { name: 'Desk', rules: [rule('desk-clear', 'Keep the desk clear.')] },
    { name: 'Hours', rules: [rule('hours-log', 'Log the hours.')] },
  ])))
  await dialog.getByRole('button', { name: 'Import 2 sets as drafts' }).click()
  await expect(dialog.getByRole('alert')).toContainText('did not answer')
  await expect(dialog.getByRole('listitem').filter({ hasText: 'Hours' })).toContainText('Not confirmed: check before importing again')
  await dialog.getByText('Technical details').click()
  await expect(dialog.getByText(/Check the server before trying again/)).toBeVisible()
  await dialog.getByRole('button', { name: 'Close', exact: true }).click()
  await expect(page.getByRole('region', { name: 'Your rules' }).getByRole('heading', { name: 'Desk' })).toBeVisible()
  expect(rules.calls.filter(call => call.method === 'PUT' && call.path.includes('/draft'))).toHaveLength(1)
  expect(rules.calls.some(call => call.path.endsWith('/publish'))).toBe(false)
})

test('importing the same file again updates changed sets and skips identical ones', async ({ page }) => {
  const rules = await setup(page)
  const file = draftFile([{ scope: { layer: 'company' }, sets: [
    { name: 'Secrets', rules: [rule('keep-secrets', 'Never print the environment.'), rule('new-one', 'A new rule.')] },
  ] }])
  const dialog = await openImport(page)
  await dialog.locator('#draft-import-file').setInputFiles(file)
  await expect(dialog.getByText(/1 new · 1 changed · 1 removed/)).toBeVisible()
  await dialog.getByRole('button', { name: 'Import 1 set as drafts' }).click()
  await expect(page.getByText(/Imported 1 set as drafts/)).toBeVisible()
  expect(rules.calls.filter(call => call.method === 'POST' && call.path === '/api/rules/sets')).toHaveLength(0)
  const put = rules.calls.find(call => call.method === 'PUT' && call.path.endsWith('/draft'))
  expect(put?.body).toMatchObject({ expected_revision: 3, name: 'Secrets' })
})

test('rule details in the review stay collapsed until opened', async ({ page }) => {
  const rules = await setup(page)
  const dialog = await openImport(page)
  const details = `{"source":"bundle","meta":"${'x'.repeat(240)}"}`
  await dialog.locator('#draft-import-file').setInputFiles(draftFile(personSets([
    { name: 'Desk', rules: [rule('desk-clear', 'Keep the desk clear.', details)] },
  ])))
  await dialog.getByText('Desk', { exact: true }).click()
  await expect(dialog.getByText('Keep the desk clear.')).toBeVisible()
  await expect(dialog.getByText('Synthetic reason for the import test.')).toHaveCount(0)
  await expect(dialog.getByText(details)).toHaveCount(0)
  await dialog.getByRole('button', { name: 'Show details for Keep the desk clear.' }).click()
  await expect(dialog.getByText('Synthetic reason for the import test.')).toBeVisible()
  await expect(dialog.getByText(details)).toBeVisible()
  expect(writes(rules.calls)).toHaveLength(0)
})

test('import stays off while a set is being edited', async ({ page }) => {
  const rules = await setup(page)
  await page.getByRole('button', { name: 'Actions for Secrets' }).click()
  await page.getByRole('menuitem', { name: 'Edit rules' }).click()
  await page.getByRole('checkbox', { name: 'Record the source. is on' }).uncheck()
  const importButton = page.getByRole('button', { name: 'Import' })
  await expect(importButton).toHaveAttribute('aria-disabled', 'true')
  await expect(importButton).toHaveAttribute('data-tip', 'Save or cancel the set you are editing before importing.')
  await importButton.click({ force: true })
  await expect(page.getByRole('dialog', { name: 'Import rules' })).toHaveCount(0)
  expect(writes(rules.calls)).toHaveLength(0)
  await page.getByRole('button', { name: 'Cancel' }).click()
  await expect(importButton).not.toHaveAttribute('aria-disabled', 'true')
})

test('an agent cannot import drafts', async ({ page }) => {
  await setup(page, { kind: 'agent' })
  await expect(page.getByRole('button', { name: 'Import' })).toHaveAttribute('aria-disabled', 'true')
  await page.getByRole('button', { name: 'Import' }).click({ force: true })
  await expect(page.getByRole('dialog', { name: 'Import rules' })).toHaveCount(0)
})
