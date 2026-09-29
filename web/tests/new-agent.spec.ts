// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mockPairing } from './agent-pairing-fixtures'
import { mkdir } from 'node:fs/promises'
import { fixtures, mockWork } from './work-fixtures'
import { accessWorld, mockAccess, ME } from './access-fixtures'

async function shot(page: Page, name: string) {
  const dir = process.env.NEW_AGENT_SHOTS
  if (!dir) return
  await mkdir(dir, { recursive: true })
  await page.screenshot({ path: `${dir}/${name}.png`, fullPage: true })
}
for (const width of [390, 1600]) for (const colorScheme of ['light', 'dark'] as const) {
  test(`new agent ${width} ${colorScheme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme })
    await mockWork(page, fixtures())
    const world = accessWorld()
    world.agents = []; world.keys = []
    await mockAccess(page, world)
    await page.goto('/settings/access/agents')
    await expect(page.locator('.agents-tab')).toBeVisible()
    await expect(page.getByRole('link', { name: 'Connect a computer (the agent daemon, no key to handle)' })).toHaveAttribute('href', '/agents/register-agent')
    await expect(page.locator('.agents-tab')).toContainText('New agent (a key for a CLI or script)')
    await shot(page, `${width}-${colorScheme}-empty`)
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
    await page.getByRole('button', { name: 'New agent', exact: true }).click()
    const form = page.getByRole('dialog', { name: 'New agent', exact: true })
    await expect(form.getByLabel('Name', { exact: true })).toBeFocused()
    await form.getByLabel('Name', { exact: true }).fill('release-helper')
    await form.getByLabel('Description').fill('Prepares release reports')
    await shot(page, `${width}-${colorScheme}-form`)
    await form.getByRole('button', { name: 'Create agent', exact: true }).click()
    const keySheet = page.getByRole('dialog', { name: 'Create first key for release-helper' })
    await expect(keySheet).toBeVisible()
    expect(world.agents).toHaveLength(1)
    expect(world.keys).toHaveLength(0)
    await expect(keySheet.getByRole('checkbox', { name: /nodes\.write/ })).toBeDisabled()
    await keySheet.getByRole('checkbox', { name: /nodes\.read/ }).check()
    await keySheet.getByRole('button', { name: 'Create first key', exact: true }).click()
    const ready = page.getByRole('dialog', { name: 'Key ready', exact: true })
    await expect(ready).toBeVisible()
    await ready.getByRole('button', { name: 'Copy key', exact: true }).click()
    await expect(ready.getByRole('button', { name: 'Copied', exact: true })).toBeVisible()
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(await ready.getByLabel('New agent key').inputValue())
    const origin = new URL(page.url()).origin
    await expect(ready.getByLabel('CLI login command')).toHaveValue(`paimos --instance 127.0.0.1 auth login --name 127.0.0.1 --url '${origin}'`)
    await ready.getByRole('button', { name: 'Copy command', exact: true }).click()
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(await ready.getByLabel('CLI login command').inputValue())
    expect(new URL(page.url()).search).toBe('')
    await shot(page, `${width}-${colorScheme}-ready`)
    const violations = (await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()).violations.filter(v => v.impact === 'serious' || v.impact === 'critical')
    expect(violations.map(v => v.id)).toEqual([])
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    await ready.getByRole('button', { name: 'Done', exact: true }).click()
    await expect(page.getByLabel('New agent key')).toHaveCount(0)
    await expect(page.getByRole('list', { name: 'Agents' })).toContainText('Prepares release reports')
    expect(world.keys).toHaveLength(1)
    expect(world.events.some(e => e.type === 'principal.agent_created')).toBe(true)
    await shot(page, `${width}-${colorScheme}-created`)
  })
}


test('selected projects, clipboard rejection and duplicate-name recovery', async ({ page }) => {
  await mockWork(page, fixtures())
  const world = accessWorld()
  await mockAccess(page, world)
  await page.addInitScript(() => Object.defineProperty(navigator, 'clipboard', { value: { writeText: async () => { throw new Error('denied') } } }))
  await page.goto('/settings/access/agents?new=1')
  const form = page.getByRole('dialog', { name: 'New agent', exact: true })
  await form.getByLabel('Name', { exact: true }).fill('aeon-coordinator')
  await form.getByRole('button', { name: 'Create agent', exact: true }).click()
  await expect(form.getByRole('alert')).toContainText('already in use')
  await form.getByLabel('Name', { exact: true }).fill('project-helper')
  await form.getByLabel('Project access', { exact: true }).selectOption('projects')
  await form.getByRole('button', { name: 'Create agent', exact: true }).click()
  await expect(form).toContainText('Choose at least one project')
  await expect(form.locator('fieldset.projects')).toHaveAttribute('aria-describedby', 'agent-projects-error')
  await form.getByRole('checkbox', { name: 'Pharos', exact: true }).check()
  await form.getByRole('button', { name: 'Create agent', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Create first key for project-helper' })
  await sheet.getByRole('checkbox', { name: /nodes\.read/ }).check()
  await sheet.getByRole('button', { name: 'Create first key', exact: true }).click()
  const ready = page.getByRole('dialog', { name: 'Key ready', exact: true })
  await ready.getByRole('button', { name: 'Copy key', exact: true }).click()
  await expect(ready.getByRole('status')).toContainText('key is selected')
  expect(await ready.getByLabel('New agent key').evaluate((input: HTMLInputElement) => input.selectionStart === 0 && input.selectionEnd === input.value.length && document.activeElement === input)).toBe(true)
  await ready.getByRole('button', { name: 'Copy command', exact: true }).click()
  expect(await ready.getByLabel('CLI login command').evaluate((input: HTMLTextAreaElement) => input.selectionStart === 0 && input.selectionEnd === input.value.length)).toBe(true)
  const created = world.agents.find(a => a.name === 'project-helper')!
  expect(created.workspace_role).toBeNull()
  expect(world.bindings.filter(b => b.principal_id === created.principal_id)).toEqual([{ principal_id: created.principal_id, project_id: 'p-pharos', role_id: 'role-viewer' }])
  await ready.getByRole('button', { name: 'Done', exact: true }).click()
  await expect(page).toHaveURL('/settings/access/agents')
  await page.reload()
  await expect(page.getByLabel('New agent key')).toHaveCount(0)
})

for (const who of ['member', 'guest', 'agent'] as const) test(`${who} has no new-agent action`, async ({ page }) => {
  await mockWork(page, fixtures())
  await mockAccess(page, accessWorld({ role: who === 'agent' ? 'owner' : who }))
  if (who === 'agent') await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: ME, name: 'Agent', kind: 'agent' }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
  await page.goto('/settings/access/agents?new=1')
  if (who === 'guest') await expect(page.getByRole('heading', { name: 'Access is for people who manage the workspace' })).toBeVisible()
  else await expect(page.locator('.access-card')).toBeVisible()
  await expect(page.getByRole('button', { name: 'New agent', exact: true })).toHaveCount(0)
  await expect(page.getByRole('dialog', { name: 'New agent', exact: true })).toHaveCount(0)
  await expect(page).toHaveURL(/\/settings\/access\/agents$/)
})

test('computer pairing offers the CLI path directly', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockPairing(page)
  await mockAccess(page, accessWorld())
  await page.goto('/agents/register-agent')
  await expect(page.locator('.intro')).toContainText('Pair a computer with agentd; no API key needed.')
  await page.getByRole('link', { name: 'create an agent and key', exact: true }).click()
  await expect(page.getByRole('dialog', { name: 'New agent', exact: true })).toBeVisible()
})
