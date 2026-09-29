// SPDX-License-Identifier: AGPL-3.0-only
// AEON-314: rules readable for humans. Explanations (TL;DR) per set and rule,
// written into the draft on their own, the explanation ↔ exact rule table in
// place of the text wall, and the configurable session-file budget.
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockRules, type RulesMockOptions } from './rules-fixtures'

async function setup(page: Page, options: RulesMockOptions = {}) {
  await mockWork(page, fixtures())
  await mockSettings(page, settingsData())
  const rules = await mockRules(page, { tldr: true, ...options })
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('heading', { name: 'Agent rules' })).toBeVisible()
  return rules
}
const writes = (calls: { method: string; path: string; body?: unknown }[]) => calls.filter(call => call.method !== 'GET' && call.path.startsWith('/api/rules'))

test('sets and rules show their explanation quietly, with a check mark when the text changed', async ({ page }) => {
  await setup(page)
  const secrets = page.getByRole('article', { name: 'Secrets' })
  await expect(secrets).toContainText('Keeps credentials and private data out of every transcript and log.')
  const scope = page.getByRole('article', { name: 'Scope' })
  await expect(scope).toContainText('How work in Aeon is scoped and recorded.')
  await expect(scope.getByText('Check', { exact: true })).toBeVisible()
  await secrets.getByRole('button', { name: /^Secrets/ }).click()
  await expect(secrets.getByRole('listitem').filter({ hasText: 'Never print the environment.' })).toContainText('Environment dumps put secrets into logs and chat')
  await scope.getByRole('button', { name: /^Scope/ }).click()
  await expect(scope.getByRole('listitem').filter({ hasText: 'Your package is your scope.' }).getByText('Check', { exact: true })).toBeVisible()
})

test('a rule explanation is edited in its details and saved as a draft on its own', async ({ page }) => {
  const rules = await setup(page)
  const secrets = page.getByRole('article', { name: 'Secrets' })
  await secrets.getByRole('button', { name: /^Secrets/ }).click()
  await secrets.getByRole('button', { name: 'Show details for Record the source.' }).click()
  const item = secrets.getByRole('listitem').filter({ hasText: 'Record the source.' })
  await expect(item).toContainText('None yet')
  await item.getByRole('button', { name: 'Add' }).click()
  const form = item.locator('form.tldr-edit')
  await form.getByLabel(/^Explanation/).fill('Every rule names the ticket or incident it came from.')
  await form.getByLabel(/^German/).fill('Jede Regel nennt ihre Quelle.')
  await form.getByRole('button', { name: 'Save draft' }).click()
  await expect(form).toHaveCount(0)
  await expect(item).toContainText('Every rule names the ticket or incident it came from.')
  await expect(page.getByText('Draft saved')).toBeVisible()
  const sent = writes(rules.calls)
  expect(sent).toHaveLength(1)
  expect(sent[0]!.method).toBe('PUT')
  expect(sent[0]!.path).toMatch(/\/api\/rules\/sets\/[^/]+\/tldr$/)
  expect(sent[0]!.body).toEqual({ expected_revision: 3, rules: { 'record-source': { en: 'Every rule names the ticket or incident it came from.', de: 'Jede Regel nennt ihre Quelle.' } } })
  // Explanations wait for the normal publish like any other draft change.
  await expect(secrets.getByText('Changed', { exact: true })).toBeVisible()
})

test('an explanation marked for a check is confirmed with “Still fits” or removed', async ({ page }) => {
  const rules = await setup(page)
  const scope = page.getByRole('article', { name: 'Scope' })
  await scope.getByRole('button', { name: /^Scope/ }).click()
  await scope.getByRole('button', { name: 'Show details for Your package is your scope.' }).click()
  const item = scope.getByRole('listitem').filter({ hasText: 'Your package is your scope.' })
  await item.getByRole('button', { name: 'Check', exact: true }).click()
  await expect(item).toContainText('The rule text changed after this was written.')
  await item.getByRole('button', { name: 'Still fits' }).click()
  await expect(item.locator('.tldr .check')).toHaveCount(0)
  expect(writes(rules.calls).at(-1)!.body).toEqual({ expected_revision: 3, rules: { 'package-scope': { en: 'A worker changes only the files its package needs.' } } })
  await item.getByRole('button', { name: 'Edit', exact: true }).click()
  await item.getByRole('button', { name: 'Remove' }).click()
  await expect(item).toContainText('None yet')
  expect(writes(rules.calls).at(-1)!.body).toEqual({ expected_revision: 4, rules: { 'package-scope': null } })
})

test('a set explanation is written from the set menu', async ({ page }) => {
  const rules = await setup(page)
  await page.getByRole('button', { name: 'Actions for Secrets' }).click()
  await page.getByRole('menuitem', { name: 'Edit explanation' }).click()
  const form = page.getByRole('article', { name: 'Secrets' }).locator('form.tldr-edit')
  await form.getByLabel(/^Explanation/).fill('No credential ever reaches a transcript.')
  await form.getByRole('button', { name: 'Save draft' }).click()
  await expect(page.getByRole('article', { name: 'Secrets' })).toContainText('No credential ever reaches a transcript.')
  expect(writes(rules.calls).at(-1)!.body).toEqual({ expected_revision: 3, set: { en: 'No credential ever reaches a transcript.', de: 'Hält Zugangsdaten aus Protokollen heraus.' } })
})

test('people who may not write see explanations without edit controls', async ({ page }) => {
  await setup(page, { kind: 'agent' })
  const secrets = page.getByRole('article', { name: 'Secrets' })
  await secrets.getByRole('button', { name: /^Secrets/ }).click()
  await secrets.getByRole('button', { name: 'Show details for Never print the environment.' }).click()
  const item = secrets.getByRole('listitem').filter({ hasText: 'Never print the environment.' })
  await expect(item.getByRole('term').filter({ hasText: 'Explanation' })).toBeVisible()
  await expect(item.getByRole('button', { name: /^(Edit|Add|Check)$/ })).toHaveCount(0)
  await page.getByRole('button', { name: 'Actions for Secrets' }).click()
  await expect(page.getByRole('menuitem', { name: 'Edit explanation' })).toHaveAttribute('aria-disabled', 'true')
})

test('the preview is an explanation ↔ exact rule table with search, harness filter, layer budget and the exact file', async ({ page }) => {
  await setup(page, { budget: { max_bytes: 12000, layer_max_bytes: { company: 8000 } } })
  await page.getByRole('button', { name: 'Preview' }).click()
  const dialog = page.getByRole('dialog', { name: 'What agents receive' })
  const table = dialog.getByRole('table', { name: 'Explanation and exact rule' })
  await expect(table.getByRole('columnheader')).toHaveText(['Explanation', 'Exact rule'])
  const row = table.getByRole('row').filter({ hasText: 'keep-secrets' })
  await expect(row.getByRole('cell').first()).toContainText('Environment dumps put secrets into logs and chat')
  await expect(row.getByRole('cell').nth(1)).toContainText('Never print the environment.')
  await expect(table.getByRole('row').filter({ hasText: 'record-source' })).toContainText('No explanation yet')
  await expect(table).toContainText('Keeps credentials and private data out of every transcript and log.')
  await expect(dialog).toContainText('without an explanation')
  await expect(dialog.locator('.budget-line')).toContainText(/^Company \d+ B of 8 kB · Project \d+ B · total \d+ B of 12 kB/)
  await dialog.getByRole('searchbox', { name: 'Search rules and explanations' }).fill('package')
  await expect(table.getByRole('row').filter({ hasText: 'keep-secrets' })).toHaveCount(0)
  await expect(table).toContainText('Your package is your scope.')
  await dialog.getByRole('searchbox', { name: 'Search rules and explanations' }).fill('zzz')
  await expect(table).toContainText('No rule matches “zzz”.')
  await dialog.getByRole('searchbox', { name: 'Search rules and explanations' }).fill('')
  await dialog.getByRole('button', { name: 'Show exact file' }).click()
  const file = dialog.getByLabel('Session file', { exact: true })
  await expect(file).toContainText('# Aeon session rules')
  await expect(file).not.toContainText('Environment dumps')
  await dialog.getByRole('button', { name: 'Hide exact file' }).click()
  await expect(file).toHaveCount(0)
})

test('the budget is a workspace setting for people who manage it', async ({ page }) => {
  const rules = await setup(page)
  const section = page.getByRole('region', { name: 'Budget' })
  await expect(section).toContainText('12,000 bytes per session file')
  await section.getByRole('button', { name: 'Change' }).click()
  await section.getByLabel(/^Total/).fill('1000')
  await section.getByRole('button', { name: 'Save budget' }).click()
  await expect(section.getByRole('alert')).toContainText('between 2,000 and 64,000 bytes')
  await section.getByLabel(/^Total/).fill('16000')
  await section.getByLabel(/^Company/).fill('9000')
  await section.getByRole('button', { name: 'Save budget' }).click()
  await expect(section).toContainText('16,000 bytes per session file · Company up to 9,000')
  expect(writes(rules.calls).at(-1)).toMatchObject({ method: 'PUT', path: '/api/rules/budget', body: { max_bytes: 16000, layer_max_bytes: { company: 9000 } } })
})

test('without the settings permission the budget is read only', async ({ page }) => {
  await setup(page, { settings: false })
  const section = page.getByRole('region', { name: 'Budget' })
  await expect(section).toContainText('12,000 bytes per session file')
  await expect(section.getByRole('button', { name: 'Change' })).toHaveCount(0)
})

test('publishing is held when one layer would cross its own cap', async ({ page }) => {
  await setup(page, { budget: { max_bytes: 12000, layer_max_bytes: { project: 40 } } })
  await page.getByRole('button', { name: /Review and publish/ }).click()
  const dialog = page.getByRole('dialog', { name: /publish/i })
  await expect(dialog.getByRole('alert')).toContainText('is over its own cap')
  await expect(dialog.locator('.part-over')).toContainText('Project')
  await expect(dialog.getByRole('button', { name: /^Publish \d+ sets?$/ })).toBeDisabled()
})

test('while editing a set, explanations sit under More and save with the draft', async ({ page }) => {
  const rules = await setup(page)
  await page.getByRole('button', { name: 'Actions for Secrets' }).click()
  await page.getByRole('menuitem', { name: 'Edit rules' }).click()
  const row = page.getByRole('listitem', { name: 'Record the source.' })
  await row.getByText('More').click()
  await row.getByLabel(/^Explanation/).fill('Names where each rule came from.')
  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect(page.getByText('Draft saved')).toBeVisible()
  const draft = writes(rules.calls).find(call => call.path.endsWith('/draft'))
  const sent = (draft!.body as { rules: { identity: string; tldr?: unknown }[] }).rules
  expect(sent.find(item => item.identity === 'record-source')!.tldr).toEqual({ en: 'Names where each rule came from.' })
  expect(sent.find(item => item.identity === 'keep-secrets')!.tldr).toEqual({ en: 'Environment dumps put secrets into logs and chat; agents never print them.', de: 'Keine Umgebungsvariablen ausgeben.', basis: '0123456789abcdef' })
})
