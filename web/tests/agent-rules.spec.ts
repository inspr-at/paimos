// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockRules } from './rules-fixtures'

async function setup(page: Page, options: Parameters<typeof mockRules>[1] = {}) {
  await mockWork(page, fixtures())
  await mockSettings(page, settingsData())
  return mockRules(page, options)
}
const rulesCalls = (calls: { method: string; path: string; body?: unknown }[]) => calls.filter(call => call.path.startsWith('/api/rules'))
async function openSet(page: Page, name: string) {
  await page.getByRole('button', { name: new RegExp(`^${name}`) }).click()
}
async function editSet(page: Page, name: string) {
  await page.getByRole('button', { name: `Actions for ${name}` }).click()
  await page.getByRole('menuitem', { name: 'Edit rules' }).click()
  await expect(page.getByRole('textbox', { name: 'Set name' })).toHaveValue(name)
}

test('agent rules load as a read, then save an explicit draft in place', async ({ page }) => {
  const errors = watchErrors(page)
  const rules = await setup(page)
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('navigation', { name: 'Settings sections' }).getByRole('link', { name: /^Agent rules/ })).toHaveAttribute('aria-current', 'page')
  await openSet(page, 'Secrets')
  await expect(page.getByRole('img', { name: 'Locked' })).toBeVisible()
  await openSet(page, 'Scope')
  // The project copy of a company-locked identity says who holds it.
  await expect(page.getByRole('img', { name: 'Locked in company rules' })).toBeVisible()
  expect(rulesCalls(rules.calls).every(call => call.method === 'GET')).toBe(true)

  await editSet(page, 'Secrets')
  await page.getByRole('checkbox', { name: 'Record the source. in Secrets is on' }).uncheck()
  await expect(page.getByRole('checkbox', { name: 'Never print the environment. in Secrets is on' })).toBeDisabled()
  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect(page.getByText('Draft saved')).toBeVisible()
  const put = rules.calls.find(call => call.method === 'PUT' && call.path.endsWith('/draft'))
  expect(put?.body).toMatchObject({ expected_revision: 3, name: 'Secrets' })
  expect((put?.body as { rules: { identity: string; enabled: boolean }[] }).rules.find(rule => rule.identity === 'record-source')?.enabled).toBe(false)
  // Saving a draft is not publishing it.
  expect(rules.calls.some(call => call.path.includes('publish'))).toBe(false)
  await expect(page.getByText('Changed', { exact: true })).toBeVisible()
  expect(errors).toEqual([])
})

test('a revision conflict keeps the unsaved draft', async ({ page }) => {
  const rules = await setup(page, { conflict: true })
  await page.goto('/settings/agent-rules')
  await editSet(page, 'Secrets')
  const box = page.getByRole('checkbox', { name: /Record the source\. in Secrets is (on|off)/ })
  await box.uncheck()
  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect(page.getByRole('alert')).toContainText('Your edits are still here')
  await expect(box).not.toBeChecked()
  await expect(box).toHaveAttribute('aria-label', 'Record the source. in Secrets is off')
  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect(page.getByText('Draft saved')).toBeVisible()
  const puts = rules.calls.filter(call => call.method === 'PUT')
  expect(puts.map(call => (call.body as { expected_revision: number }).expected_revision)).toEqual([3, 4])
})

test('a new rule needs its reason before the draft saves', async ({ page }) => {
  const rules = await setup(page)
  await page.goto('/settings/agent-rules')
  await editSet(page, 'Secrets')
  await page.getByRole('button', { name: 'Add rule' }).click()
  const row = page.getByRole('listitem', { name: 'New rule' })
  await row.getByRole('textbox', { name: 'Rule text' }).fill('Say which tests ran.')
  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect(page.getByRole('alert')).toContainText('reason')
  expect(rules.calls.some(call => call.method === 'PUT')).toBe(false)
  await page.getByRole('listitem', { name: 'Say which tests ran.' }).getByRole('textbox', { name: 'Why' }).fill('Reviewers need to know.')
  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect(page.getByText('Draft saved')).toBeVisible()
  const put = rules.calls.find(call => call.method === 'PUT')
  expect((put?.body as { rules: { text: string; source: { reference: string } }[] }).rules.find(rule => rule.text === 'Say which tests ran.')?.source.reference).toBe('Written by hand')
})

test('publish is one person approval and omits an empty note', async ({ page }) => {
  const rules = await setup(page)
  await page.goto('/settings/agent-rules')
  await page.getByRole('button', { name: 'Review and publish (1 set)' }).click()
  const dialog = page.getByRole('dialog', { name: 'Review and publish' })
  await expect(dialog).toContainText('Scope')
  await dialog.getByRole('textbox', { name: /Note/ }).fill('   ')
  await dialog.getByRole('button', { name: 'Publish 1 set' }).click()
  await expect(page.getByText(/Published 1 set/)).toBeVisible()
  const post = rules.calls.find(call => call.method === 'POST' && call.path === '/api/rules/publish')
  expect(Object.keys(post?.body as object).sort()).toEqual(['items'])
  expect((post?.body as { items: { version: string; expected_revision: number }[] }).items).toEqual([{ set_id: 'ffffffff-ffff-4fff-8fff-ffffffffffff', expected_revision: 3, version: 'auto' }])
  expect(rules.calls.some(call => /\/sets\/[^/]+\/publish$/.test(call.path))).toBe(false)
})

test('a publish note is saved and history restores a version with its own note', async ({ page }) => {
  const rules = await setup(page)
  await page.goto('/settings/agent-rules')
  await editSet(page, 'Secrets')
  await page.getByRole('checkbox', { name: 'Record the source. in Secrets is on' }).uncheck()
  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect(page.getByText('Draft saved')).toBeVisible()
  await page.getByRole('button', { name: 'Review and publish (2 sets)' }).click()
  const dialog = page.getByRole('dialog', { name: 'Review and publish' })
  await expect(dialog.getByText('1 changed')).toBeVisible()
  await dialog.getByRole('textbox', { name: /Note/ }).fill('Floor clarified for AEON-252.')
  await dialog.getByRole('button', { name: 'Publish 2 sets' }).click()
  await expect(page.getByText(/Published 2 sets/)).toBeVisible()
  const post = rules.calls.find(call => call.path === '/api/rules/publish')
  expect((post?.body as { note: string }).note).toBe('Floor clarified for AEON-252.')

  await page.getByRole('button', { name: 'Actions for Secrets' }).click()
  await page.getByRole('menuitem', { name: 'Version history' }).click()
  const history = page.getByRole('dialog', { name: 'Secrets · history' })
  await expect(history).toContainText('Floor clarified for AEON-252.')
  await expect(history.getByText('Live', { exact: true })).toHaveCount(1)
  await history.getByRole('button', { name: 'Restore as new version' }).click()
  const note = history.getByRole('textbox', { name: /Note for the new version/ })
  await expect(note).toHaveValue('')
  await note.fill('Restored after review.')
  await history.getByRole('button', { name: 'Restore', exact: true }).click()
  await expect(page.getByText('Restored as a new version')).toBeVisible()
  const restored = rules.calls.find(call => call.method === 'POST' && call.path.endsWith('/restore'))
  const body = restored?.body as { version: string; new_version: string; note: string; expected_revision: number }
  expect(body.version).toBe('260920100000.0.0')
  expect(body.note).toBe('Restored after review.')
  expect(body.new_version).toMatch(/^[1-9][0-9]{11}\.0\.0$/)
  expect(body.new_version).not.toBe(body.version)
})

test('a member without publish cannot edit or add project, company and role rules', async ({ page }) => {
  await setup(page, { publish: false })
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('heading', { name: 'Secrets' })).toBeVisible()
  await expect(page.getByRole('button', { name: /Review and publish/ })).toHaveCount(0)
  await page.getByRole('button', { name: 'Actions for Scope' }).click()
  const edit = page.getByRole('menuitem', { name: /Edit rules/ })
  await expect(edit).toHaveAttribute('aria-disabled', 'true')
  await expect(edit).toContainText('Editing this project’s rules needs permission to publish rules for that project.')
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: 'Actions for Secrets' }).click()
  await expect(page.getByRole('menuitem', { name: /Edit rules/ })).toContainText('Editing company rules needs the workspace permission to publish rules.')
  await page.keyboard.press('Escape')
  const layers = page.locator('.layers')
  await expect(layers.getByRole('button', { name: /Add set|Add a set for a role|Add rules for/ })).toHaveCount(0)
  // Their own person rules stay theirs to write.
  await expect(layers.getByRole('button', { name: 'Add your first set' })).toBeEnabled()
})

test('an agent cannot publish or import', async ({ page }) => {
  const rules = await setup(page, { kind: 'agent' })
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('heading', { name: 'Secrets' })).toBeVisible()
  await expect(page.getByRole('button', { name: /Review and publish/ })).toHaveCount(0)
  await expect(page.getByText('Agents read rules; a person publishes them.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Import' })).toHaveAttribute('aria-disabled', 'true')
  await page.getByRole('button', { name: 'Import' }).click({ force: true })
  await expect(page.getByRole('dialog', { name: 'Import rules' })).toHaveCount(0)
  expect(rules.calls.some(call => call.method !== 'GET' && call.path.startsWith('/api/rules'))).toBe(false)
})

test('preview explains the session file for this project, you and the builder role', async ({ page }) => {
  const rules = await setup(page)
  await page.goto('/settings/agent-rules')
  await page.getByRole('button', { name: 'Preview' }).click()
  const dialog = page.getByRole('dialog', { name: 'What agents receive' })
  await expect(dialog.getByRole('table', { name: 'Explanation and exact rule' })).toContainText('Never print the environment.')
  await expect(dialog).toContainText('Aeon · Markus Barta · Builder')
  await expect(dialog.getByRole('radio', { name: 'Claude' })).toHaveAttribute('aria-checked', 'true')
  await expect(dialog.getByLabel('Session file', { exact: true })).toHaveCount(0)
  await dialog.getByRole('button', { name: 'Show exact file' }).click()
  await expect(dialog.getByLabel('Session file', { exact: true })).toContainText('- [keep-secrets] Never print the environment.')
  await expect(dialog).toContainText('of 12,000 bytes')
  const explained = rules.calls.find(call => call.path.startsWith('/api/rules/explained'))
  expect(explained?.path).toContain('project_id=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa')
  expect(explained?.path).toContain('harness=claude-code')
  expect(explained?.path).toContain('role=builder')
  await dialog.getByRole('radio', { name: 'Cursor' }).click()
  await expect.poll(() => rules.calls.filter(call => call.path.startsWith('/api/rules/explained')).at(-1)?.path).toContain('harness=cursor')
  await expect(dialog.getByRole('table')).toContainText('Use the Cursor rules file.')
  expect(rules.calls.filter(call => call.path.startsWith('/api/rules/explained')).every(call => call.method === 'GET')).toBe(true)
  expect(rules.calls.some(call => call.path.startsWith('/api/rules/merged'))).toBe(false)
})

test('rule details stay behind the row disclosure', async ({ page }) => {
  await setup(page)
  await page.goto('/settings/agent-rules')
  await openSet(page, 'Secrets')
  await expect(page.getByText('Because the record says so.')).toHaveCount(0)
  await expect(page.getByText('keep-secrets', { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: 'Show details for Never print the environment.' }).click()
  await expect(page.getByText('Because the record says so.')).toBeVisible()
  await expect(page.getByText('More when asked.')).toBeVisible()
  await expect(page.getByText('keep-secrets', { exact: true })).toBeVisible()
})

test('layout holds at 1600 and 390 in light and dark', async ({ page }) => {
  await setup(page)
  await page.goto('/settings/agent-rules')
  await openSet(page, 'Secrets')
  for (const theme of ['light', 'dark'] as const) {
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    for (const width of [1600, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await expect(page.getByRole('heading', { name: 'Agent rules' })).toBeVisible()
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1 || document.body.scrollWidth > document.body.clientWidth + 1)
      expect(overflow, `${theme} ${width}`).toBe(false)
    }
  }
})

test('axe: agent rules', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
  await setup(page)
  await page.goto('/settings/agent-rules')
  await openSet(page, 'Secrets')
  await editSet(page, 'Scope')
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.calendar-version').analyze()
  const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 6).map(n => `  ${n.target.join(' ')} — ${n.html.slice(0, 180)}`).join('\n')}`)
  expect(summary, summary.join('\n')).toEqual([])
})
