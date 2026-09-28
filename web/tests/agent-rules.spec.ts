// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
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

test('agent rules load as a read, then save an explicit draft', async ({ page }) => {
  const errors = watchErrors(page)
  const rules = await setup(page)
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('navigation', { name: 'Settings sections' }).getByRole('link', { name: /^Agent rules/ })).toHaveAttribute('aria-current', 'page')
  await expect(page.getByRole('checkbox', { name: 'Record the source.' })).toBeVisible()
  await expect(page.getByRole('checkbox', { name: 'Never print the environment., locked and stays on' })).toBeDisabled()
  await expect(page.getByRole('img', { name: /locked by a higher layer and stays on/ })).toBeVisible()
  expect(rulesCalls(rules.calls).every(call => call.method === 'GET')).toBe(true)

  await page.getByRole('checkbox', { name: 'Record the source.' }).uncheck()
  await expect(page.getByText(/unsaved draft/)).toBeVisible()
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page.getByRole('status')).toContainText('Draft saved')
  const put = rules.calls.find(call => call.method === 'PUT' && call.path.endsWith('/draft'))
  expect(put?.body).toMatchObject({ expected_revision: 3, name: 'Secrets' })
  expect((put?.body as { rules: { identity: string; enabled: boolean }[] }).rules.find(rule => rule.identity === 'record-source')?.enabled).toBe(false)
  expect(errors).toEqual([])
})

test('a revision conflict keeps the unsaved draft', async ({ page }) => {
  const rules = await setup(page, { conflict: true })
  await page.goto('/settings/agent-rules')
  const box = page.getByRole('checkbox', { name: 'Record the source.' })
  await box.uncheck()
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('Your draft is still here')
  await expect(box).not.toBeChecked()
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page.getByRole('status')).toContainText('Draft saved')
  const puts = rules.calls.filter(call => call.method === 'PUT')
  expect(puts.map(call => (call.body as { expected_revision: number }).expected_revision)).toEqual([3, 4])
})

test('publish is person-only and omits an empty note', async ({ page }) => {
  const rules = await setup(page)
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('checkbox', { name: 'Record the source.' })).toBeVisible()
  await page.getByRole('button', { name: 'Publish' }).click()
  const dialog = page.getByRole('dialog', { name: 'Publish' })
  const note = dialog.getByRole('textbox', { name: 'Publish note' })
  await expect(note).toBeEnabled()
  await note.fill('   ')
  await expect(dialog).toContainText('Leave this empty to publish without a note')
  await dialog.getByRole('button', { name: 'Publish', exact: true }).click()
  await expect(page.getByRole('status')).toContainText(/Published /)
  const post = rules.calls.find(call => call.method === 'POST' && call.path.endsWith('/publish'))
  expect(Object.keys(post?.body as object).sort()).toEqual(['expected_revision', 'version'])
  expect((post?.body as { version: string }).version).toMatch(/^[1-9][0-9]{11}\.0\.0$/)
})

test('a publish note is saved on that version and a restore note stays separate', async ({ page }) => {
  const rules = await setup(page)
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('checkbox', { name: 'Record the source.' })).toBeVisible()
  await page.getByRole('button', { name: 'Publish' }).click()
  const dialog = page.getByRole('dialog', { name: 'Publish' })
  await dialog.getByRole('textbox', { name: 'Publish note' }).fill('Floor clarified for AEON-252.')
  await dialog.getByRole('button', { name: 'Publish', exact: true }).click()
  await expect(page.getByRole('status')).toContainText(/Published /)
  const post = rules.calls.find(call => call.method === 'POST' && call.path.endsWith('/publish'))
  const version = (post?.body as { version: string; note: string }).version
  expect((post?.body as { note: string }).note).toBe('Floor clarified for AEON-252.')
  const versions = page.getByLabel('Versions of Secrets')
  await versions.focus()
  await versions.selectOption(version)
  await expect(page.getByText('Publish note: Floor clarified for AEON-252.')).toBeVisible()
  await page.getByRole('button', { name: 'Never print the environment.' }).first().click()
  await page.getByRole('button', { name: 'Restore as a new version' }).click()
  const restore = page.getByRole('dialog', { name: 'Restore version' })
  await expect(restore).toContainText(`Note on ${version}: Floor clarified for AEON-252.`)
  const next = restore.getByRole('textbox', { name: 'New publish note' })
  await expect(next).toHaveValue('')
  await next.fill('Restored after review.')
  await expect.poll(() => page.evaluate(() => {
    const part = (n: number) => String(n).padStart(2, '0')
    const now = new Date()
    return `${String(now.getUTCFullYear()).slice(2)}${part(now.getUTCMonth() + 1)}${part(now.getUTCDate())}${part(now.getUTCHours())}${part(now.getUTCMinutes())}${part(now.getUTCSeconds())}.0.0`
  }), { timeout: 2500 }).not.toBe(version)
  await restore.getByRole('button', { name: 'Restore', exact: true }).click()
  await expect(page.getByRole('status')).toContainText(/Restored as /)
  const restored = rules.calls.find(call => call.method === 'POST' && call.path.endsWith('/restore'))
  const body = restored?.body as { version: string; new_version: string; note: string }
  expect(body.version).toBe(version)
  expect(body.note).toBe('Restored after review.')
  expect(body.new_version).not.toBe(version)
  await versions.focus()
  await versions.selectOption(version)
  await expect(page.getByText('Publish note: Floor clarified for AEON-252.')).toBeVisible()
  await expect(page.getByText('Restored after review.')).toHaveCount(0)
  await versions.selectOption('260920100000.0.0')
  await expect(page.getByText('Published version 260920100000.0.0 is read-only.')).toBeVisible()
  await expect(page.getByText(/Publish note:/)).toHaveCount(0)
})

test('a member without publish cannot add or save project and role rules', async ({ page }) => {
  await setup(page, { publish: false })
  await page.goto('/settings/agent-rules')
  const project = page.getByRole('region', { name: 'Project', exact: true })
  const agent = page.getByRole('region', { name: 'Agent', exact: true })
  await expect(project).toContainText('Editing this project’s rules needs permission to publish rules for that project.')
  await expect(project.getByRole('checkbox', { name: 'Your package is your scope.' })).toBeDisabled()
  await expect(page.getByRole('region', { name: 'Company', exact: true })).toContainText('Editing company rules needs the workspace permission to publish rules.')
  await expect(agent).toContainText('Editing role rules needs the workspace permission to publish rules.')
  await expect(agent.getByRole('button', { name: 'Add agent rules' })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Add rule' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Add set' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Save', exact: true })).toBeDisabled()
  await expect(page.getByRole('region', { name: 'Person', exact: true }).getByRole('button', { name: 'Add person rules' })).toBeEnabled()
})

test('an agent cannot publish', async ({ page }) => {
  await setup(page, { kind: 'agent' })
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('checkbox', { name: 'Record the source.' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Publish' })).toBeDisabled()
  await expect(page.getByText('Only a person can publish rules.')).toBeVisible()
})

test('preview shows the merged file and its budget', async ({ page }) => {
  const rules = await setup(page)
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('checkbox', { name: 'Record the source.' })).toBeVisible()
  await page.getByRole('button', { name: 'Preview' }).click()
  const dialog = page.getByRole('dialog', { name: 'Merged rules' })
  await expect(dialog.locator('pre')).toContainText('Never print the environment.')
  await expect(dialog).toContainText('58 bytes of 12000')
  const merged = rules.calls.find(call => call.path.startsWith('/api/rules/merged'))
  expect(merged?.path).toContain('project_id=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa')
  expect(merged?.path).toContain('harness=cursor')
  expect(merged?.method).toBe('GET')
})

test('a published version is read-only and restore publishes a new one', async ({ page }) => {
  const rules = await setup(page)
  await page.goto('/settings/agent-rules')
  await page.getByRole('button', { name: 'Never print the environment.' }).first().click()
  const versions = page.getByLabel('Versions of Secrets')
  await versions.focus()
  await versions.selectOption('260920100000.0.0')
  await expect(page.getByText('Published version 260920100000.0.0 is read-only.')).toBeVisible()
  await expect(page.locator('.detail textarea').first()).toHaveJSProperty('readOnly', true)
  await page.getByRole('button', { name: 'Restore as a new version' }).click()
  await page.getByRole('dialog', { name: 'Restore version' }).getByRole('button', { name: 'Restore', exact: true }).click()
  await expect(page.getByRole('status')).toContainText(/Restored as /)
  const post = rules.calls.find(call => call.method === 'POST' && call.path.endsWith('/restore'))
  const body = post?.body as { version: string; new_version: string; expected_revision: number }
  expect(body.version).toBe('260920100000.0.0')
  expect(body.new_version).toMatch(/^[1-9][0-9]{11}\.0\.0$/)
  expect(body.new_version).not.toBe(body.version)
})

test('reset to template stays unavailable without original wording', async ({ page }) => {
  await setup(page)
  await page.goto('/settings/agent-rules')
  await page.getByRole('button', { name: 'Never print the environment.' }).first().click()
  await expect(page.getByText('Reset needs the original wording.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Reset to template' })).toBeDisabled()
})

test('layout holds at 1600 and 390 in light and dark', async ({ page }) => {
  mkdirSync(new URL('../../.agent-shots/', import.meta.url), { recursive: true })
  await setup(page)
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('checkbox', { name: 'Record the source.' })).toBeVisible()
  await page.getByRole('button', { name: 'Record the source.' }).click()
  for (const theme of ['light', 'dark'] as const) {
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    for (const width of [1600, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      const detail = page.getByRole('button', { name: 'Close rule' })
      if (width === 390 && await detail.isVisible()) await detail.click()
      if (width !== 390 && !await detail.isVisible()) await page.getByRole('button', { name: 'Record the source.' }).click()
      await page.getByRole('region', { name: 'Company' }).scrollIntoViewIfNeeded()
      await expect(page.getByRole('heading', { name: 'Agent rules' })).toBeVisible()
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1 || document.body.scrollWidth > document.body.clientWidth + 1)
      expect(overflow, `${theme} ${width}`).toBe(false)
      await page.screenshot({ path: new URL(`../../.agent-shots/agent-rules-${theme}-${width}.png`, import.meta.url).pathname, fullPage: false })
    }
  }
})

test('axe: agent rules', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
  await setup(page)
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('checkbox', { name: 'Record the source.' })).toBeVisible()
  await page.getByRole('button', { name: 'Record the source.' }).click()
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.calendar-version').analyze()
  const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 6).map(n => `  ${n.target.join(' ')} — ${n.html.slice(0, 180)}`).join('\n')}`)
  expect(summary, summary.join('\n')).toEqual([])
})
