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
  return mockRules(page, options)
}
const writes = (calls: { method: string; path: string }[]) => calls.filter(call => (call.method === 'POST' || call.method === 'PUT') && call.path.startsWith('/api/rules'))

test('a person previews a draft file and nothing is written or published before confirm', async ({ page }) => {
  const errors = watchErrors(page)
  const rules = await setup(page)
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('checkbox', { name: 'Record the source.' })).toBeVisible()
  await page.getByRole('button', { name: 'Import drafts' }).click()
  const dialog = page.getByRole('dialog', { name: 'Import drafts' })
  await expect(dialog.getByText('INSPR Studio')).toBeVisible()
  await expect(dialog.getByText(TENANT, { exact: true })).toBeVisible()
  await expect(dialog.getByRole('button', { name: 'Import drafts' })).toBeDisabled()
  await expect(dialog.getByText('Nothing is published')).toBeVisible()
  const before = writes(rules.calls).length
  await dialog.locator('#draft-import-file').setInputFiles(draftFile(personSets([
    { name: 'Desk', rules: [rule('desk-clear', 'Keep <script>alert(1)</script> as text.')] },
  ])))
  await expect(dialog.getByRole('heading', { name: `Person ${RULE_PERSON}` })).toBeVisible()
  await expect(dialog.getByText('Desk · 1 rule')).toBeVisible()
  await expect(dialog.getByText('Keep <script>alert(1)</script> as text.')).toBeVisible()
  await expect(dialog.locator('script')).toHaveCount(0)
  expect(writes(rules.calls)).toHaveLength(before)
  await dialog.getByRole('button', { name: 'Import drafts' }).click()
  await expect(page.getByText(/Nothing was published/)).toBeVisible()
  await expect(page.getByRole('region', { name: 'Person', exact: true })).toContainText('Keep <script>alert(1)</script> as text.')
  expect(rules.calls.some(call => call.path.endsWith('/publish'))).toBe(false)
  expect(rules.calls.filter(call => call.method === 'POST' && call.path === '/api/rules/sets')).toHaveLength(1)
  const draft = rules.calls.find(call => call.method === 'PUT' && call.path.endsWith('/draft'))
  expect(draft?.body).toMatchObject({ expected_revision: 1, name: 'Desk' })
  expect(errors).toEqual([])
})

test('a file for another workspace is refused before any write', async ({ page }) => {
  const rules = await setup(page)
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('checkbox', { name: 'Record the source.' })).toBeVisible()
  await page.getByRole('button', { name: 'Import drafts' }).click()
  const dialog = page.getByRole('dialog', { name: 'Import drafts' })
  await dialog.locator('#draft-import-file').setInputFiles(draftFile(personSets([
    { name: 'Desk', rules: [rule('desk-clear', 'Keep the desk clear.')] },
  ]), '99999999-9999-4999-8999-999999999999'))
  await expect(dialog.getByRole('alert')).toContainText('different workspace')
  await expect(dialog.getByText('Keep the desk clear.')).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: 'Import drafts' })).toBeDisabled()
  expect(writes(rules.calls)).toHaveLength(0)
  expect(rules.calls.some(call => call.path.endsWith('/publish'))).toBe(false)
})

test('a failed draft stops the import and does not publish', async ({ page }) => {
  const rules = await setup(page, { draftFailAt: 2, draftFailStatus: 422 })
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('checkbox', { name: 'Record the source.' })).toBeVisible()
  await page.getByRole('button', { name: 'Import drafts' }).click()
  const dialog = page.getByRole('dialog', { name: 'Import drafts' })
  await dialog.locator('#draft-import-file').setInputFiles(draftFile(personSets([
    { name: 'Desk', rules: [rule('desk-clear', 'Keep the desk clear.')] },
    { name: 'Hours', rules: [rule('hours-log', 'Log the hours.')] },
    { name: 'Later', rules: [rule('later-note', 'Leave a note.')] },
  ])))
  await dialog.getByRole('button', { name: 'Import drafts' }).click()
  const alert = dialog.getByRole('alert')
  await expect(alert).toContainText('Hours')
  await expect(alert).toContainText('not saved')
  await expect(alert).toContainText('Nothing was rolled back or published')
  await expect(dialog.getByRole('button', { name: 'Import drafts' })).toBeDisabled()
  await dialog.getByRole('button', { name: 'Close import' }).click()
  const person = page.getByRole('region', { name: 'Person', exact: true })
  await expect(person).toContainText('Keep the desk clear.')
  await expect(person.getByLabel('Name of Hours')).toBeVisible()
  await expect(person.getByRole('button', { name: 'Leave a note.' })).toHaveCount(0)
  expect(rules.calls.filter(call => call.method === 'POST' && call.path === '/api/rules/sets')).toHaveLength(2)
  expect(rules.calls.filter(call => call.method === 'PUT' && call.path.includes('/draft'))).toHaveLength(2)
  expect(rules.calls.some(call => call.path.endsWith('/publish'))).toBe(false)
})

test('a lost reply stops the import and tells the person to check the server', async ({ page }) => {
  const rules = await setup(page, { setAbortAt: 2 })
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('checkbox', { name: 'Record the source.' })).toBeVisible()
  await page.getByRole('button', { name: 'Import drafts' }).click()
  const dialog = page.getByRole('dialog', { name: 'Import drafts' })
  await dialog.locator('#draft-import-file').setInputFiles(draftFile(personSets([
    { name: 'Desk', rules: [rule('desk-clear', 'Keep the desk clear.')] },
    { name: 'Hours', rules: [rule('hours-log', 'Log the hours.')] },
  ])))
  await dialog.getByRole('button', { name: 'Import drafts' }).click()
  const alert = dialog.getByRole('alert')
  await expect(alert).toContainText('did not confirm')
  await expect(alert).toContainText('Check the server')
  await expect(alert).toContainText('did not roll')
  await expect(alert).toContainText('reloaded from the server')
  await dialog.getByRole('button', { name: 'Close import' }).click()
  const person = page.getByRole('region', { name: 'Person', exact: true })
  await expect(person).toContainText('Keep the desk clear.')
  await expect(person.getByLabel('Name of Hours')).toHaveCount(0)
  expect(rules.calls.filter(call => call.method === 'POST' && call.path === '/api/rules/sets')).toHaveLength(2)
  expect(rules.calls.filter(call => call.method === 'PUT' && call.path.includes('/draft'))).toHaveLength(1)
  expect(rules.calls.some(call => call.path.endsWith('/publish'))).toBe(false)
})

test('technical details stay collapsed until opened', async ({ page }) => {
  const rules = await setup(page)
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('checkbox', { name: 'Record the source.' })).toBeVisible()
  await page.getByRole('button', { name: 'Import drafts' }).click()
  const dialog = page.getByRole('dialog', { name: 'Import drafts' })
  const details = `{"source":"bundle","meta":"${'x'.repeat(240)}"}`
  await dialog.locator('#draft-import-file').setInputFiles(draftFile(personSets([
    { name: 'Desk', rules: [rule('desk-clear', 'Keep the desk clear.', details)] },
  ])))
  await expect(dialog.getByText('Keep the desk clear.')).toBeVisible()
  await expect(dialog.getByText('Synthetic reason for the import test.')).toBeVisible()
  const folded = dialog.locator('.import-detail')
  await expect(folded).toHaveJSProperty('open', false)
  await expect(folded.locator('p')).toBeHidden()
  await folded.getByText('Details', { exact: true }).click()
  await expect(folded.locator('p')).toBeVisible()
  await expect(folded.locator('p')).toHaveText(details)
  expect(writes(rules.calls)).toHaveLength(0)
})

test('import stays off while unsaved edits remain', async ({ page }) => {
  const rules = await setup(page)
  await page.goto('/settings/agent-rules')
  const box = page.getByRole('checkbox', { name: 'Record the source.' })
  await expect(box).toBeVisible()
  await box.uncheck()
  await expect(page.getByText(/unsaved draft/)).toBeVisible()
  const importButton = page.getByRole('button', { name: 'Import drafts' })
  await expect(importButton).toBeDisabled()
  await expect(importButton).toHaveAttribute('title', 'Save or discard unsaved drafts before importing.')
  await expect(page.getByRole('dialog', { name: 'Import drafts' })).toHaveCount(0)
  await expect(box).not.toBeChecked()
  expect(writes(rules.calls)).toHaveLength(0)
  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(box).toBeChecked()
  await expect(importButton).toBeEnabled()
})

test('a lost save keeps unsaved edits and does not continue', async ({ page }) => {
  const rules = await setup(page, { draftFailAt: 2, draftFailStatus: 503, holdDraftFailure: true })
  await page.goto('/settings/agent-rules')
  const box = page.getByRole('checkbox', { name: 'Record the source.' })
  await expect(box).toBeVisible()
  await page.getByRole('button', { name: 'Import drafts' }).click()
  const dialog = page.getByRole('dialog', { name: 'Import drafts' })
  await dialog.locator('#draft-import-file').setInputFiles(draftFile(personSets([
    { name: 'Desk', rules: [rule('desk-clear', 'Keep the desk clear.')] },
    { name: 'Hours', rules: [rule('hours-log', 'Log the hours.')] },
    { name: 'Later', rules: [rule('later-note', 'Leave a note.')] },
  ])))
  await dialog.getByRole('button', { name: 'Import drafts' }).click()
  await expect(dialog.getByRole('button', { name: 'Importing…' })).toBeVisible()
  await box.evaluate((input: HTMLInputElement) => {
    input.checked = false
    input.dispatchEvent(new Event('change', { bubbles: true }))
  })
  await expect(page.getByText(/unsaved draft/)).toBeVisible()
  rules.releaseDraftFailure()
  const alert = dialog.getByRole('alert')
  await expect(alert).toContainText('did not confirm')
  await expect(alert).toContainText('unknown')
  await expect(alert).toContainText('Check the server')
  await expect(alert).toContainText('Unsaved edits on this page were kept')
  await expect(alert).not.toContainText('not saved')
  await expect(alert).not.toContainText('not created')
  await expect(alert).not.toContainText('reloaded from the server')
  await expect(box).not.toBeChecked()
  await expect(dialog.getByRole('button', { name: 'Import drafts' })).toBeDisabled()
  expect(rules.calls.filter(call => call.method === 'POST' && call.path === '/api/rules/sets')).toHaveLength(2)
  expect(rules.calls.filter(call => call.method === 'PUT' && call.path.includes('/draft'))).toHaveLength(2)
  expect(rules.calls.some(call => call.path.endsWith('/publish'))).toBe(false)
  expect(rules.calls.some(call => JSON.stringify(call.body ?? '').includes('later-note'))).toBe(false)
})

test('an agent cannot import drafts', async ({ page }) => {
  await setup(page, { kind: 'agent' })
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('checkbox', { name: 'Record the source.' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Import drafts' })).toBeDisabled()
  await expect(page.getByRole('dialog', { name: 'Import drafts' })).toHaveCount(0)
})
