// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { agentData, mockAgents } from './agents-fixtures'
import { mockAllowanceRoutes } from './allowance-window-fixtures'
import { fixtures, me, mockWork } from './work-fixtures'

const world = {
  me: me.id,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}

async function setup(page: Page, options: { manage?: boolean; kind?: 'person' | 'agent'; readOnly?: boolean; mode?: 'ok' | 'overlap' | 'forbidden' | 'uncertain'; hold?: boolean } = {}) {
  await mockWork(page, fixtures(), { readOnly: options.readOnly, admin: !options.readOnly })
  const data = agentData(world)
  await mockAgents(page, data)
  const routes = options.readOnly ? null : await mockAllowanceRoutes(page, data.accounts, options)
  return { data, routes }
}
async function openAccounts(page: Page) {
  await page.goto('/agents')
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  await page.getByText('Accounts and pacing', { exact: true }).first().click()
  return page.getByRole('region', { name: 'Accounts and pacing' })
}
const row = (page: Page, name: string) => page.getByRole('region', { name: 'Accounts and pacing' }).locator('.account').filter({ hasText: name })
async function localRange(page: Page, startMs: number, endMs: number) {
  return page.evaluate(({ startMs, endMs }) => {
    const stamp = (ms: number) => {
      const date = new Date(ms)
      const part = (value: number) => String(value).padStart(2, '0')
      return `${date.getFullYear()}-${part(date.getMonth() + 1)}-${part(date.getDate())}T${part(date.getHours())}:${part(date.getMinutes())}`
    }
    return { start: stamp(startMs), end: stamp(endMs), zone: Intl.DateTimeFormat().resolvedOptions().timeZone }
  }, { startMs, endMs })
}
async function fillWindow(page: Page, name: string, start: string, end: string, allowance = '1') {
  const account = row(page, name)
  await account.getByRole('button', { name: `Add allowance window for ${name}` }).click()
  const form = account.locator('form')
  await expect(form.getByLabel('Starts (your local time)')).toBeFocused()
  await form.getByLabel('Starts (your local time)').fill(start)
  await form.getByLabel('Ends (your local time)').fill(end)
  await form.getByLabel('Allowance', { exact: true }).fill(allowance)
  return form
}

test('a viewer does not get an allowance editor', async ({ page }) => {
  await setup(page, { readOnly: true })
  const accounts = await openAccounts(page)
  await expect(accounts).toContainText('Pi on hsb1')
  await expect(page.getByRole('button', { name: /Add allowance window/ })).toHaveCount(0)
})

test('an agent session does not get an allowance editor', async ({ page }) => {
  await setup(page, { manage: true, kind: 'agent' })
  const accounts = await openAccounts(page)
  await expect(accounts).toContainText('Pi on hsb1')
  await expect(page.getByRole('button', { name: /Add allowance window/ })).toHaveCount(0)
})

test('a person with account.manage saves one window and keeps each draft on its account', async ({ page }) => {
  const { routes } = await setup(page, { manage: true, hold: true })
  const accounts = await openAccounts(page)
  const now = Date.now()
  const times = await localRange(page, now + 3_600_000, now + 7_200_000)
  const form = await fillWindow(page, 'Pi on hsb1', times.start, times.end, '4')
  await expect(form).toContainText('Pi on hsb1')
  await expect(form).toContainText(times.zone)
  await expect(form).toContainText('does not infer the vendor subscription')
  await expect(form.locator('time').first()).toBeVisible()
  const filled = {
    start: await form.getByLabel('Starts (your local time)').inputValue(),
    end: await form.getByLabel('Ends (your local time)').inputValue(),
  }
  const startAt = await form.locator('time').nth(0).getAttribute('datetime')
  const endAt = await form.locator('time').nth(1).getAttribute('datetime')
  const expected = await page.evaluate(({ start, end }) => ({
    starts: new Date(start).toISOString(), ends: new Date(end).toISOString(),
  }), filled)
  expect(startAt).toBe(expected.starts)
  expect(endAt).toBe(expected.ends)
  await form.getByRole('button', { name: 'Save allowance window' }).click()
  await expect.poll(() => routes?.posts.length).toBe(1)
  await form.getByRole('button', { name: 'Cancel' }).click()
  routes?.release()
  await expect(accounts).toContainText('Allowance window saved for Pi on hsb1.')
  await row(page, 'Codex Pro').getByRole('button', { name: 'Add allowance window for Codex Pro' }).click()
  const codex = row(page, 'Codex Pro').locator('form')
  await expect(codex.getByLabel('Allowance', { exact: true })).toHaveValue('')
  await expect(codex).not.toContainText('Pi on hsb1')
  expect(routes?.posts).toHaveLength(1)
  expect(JSON.stringify(routes?.posts)).not.toContain('9ff8f945-16c4-4145-be04-917e406ac4f6')
  expect(JSON.stringify(routes?.posts)).not.toContain('ab5ca556-d22f-47b8-bed4-ba58d0201a06')
  const body = routes?.posts[0]?.body as { allowance: number; starts_at: string; ends_at: string }
  expect(body.allowance).toBe(4)
  expect(body.starts_at).toBe(expected.starts)
  expect(body.ends_at).toBe(expected.ends)
})

test('invalid, ended, overlap, forbidden and uncertain results do not post twice', async ({ page }) => {
  const { routes } = await setup(page, { manage: true })
  await openAccounts(page)
  const now = Date.now()
  const reversed = await localRange(page, now + 7_200_000, now + 3_600_000)
  let form = await fillWindow(page, 'Pi on hsb1', reversed.start, reversed.end)
  await form.getByRole('button', { name: 'Save allowance window' }).click()
  const alert = form.getByRole('alert')
  await expect(alert).toBeFocused()
  await expect(alert).toContainText('positive whole allowance')
  expect(routes?.posts).toHaveLength(0)
  await form.getByRole('button', { name: 'Cancel' }).click()

  const past = await localRange(page, now - 86_400_000, now - 3_600_000)
  form = await fillWindow(page, 'Pi on hsb1', past.start, past.end)
  await form.getByRole('button', { name: 'Save allowance window' }).click()
  await expect(form.getByRole('alert')).toContainText('already ended')
  expect(routes?.posts).toHaveLength(0)
  await form.getByRole('button', { name: 'Cancel' }).click()

  routes?.setMode('overlap')
  const ahead = await localRange(page, now + 3_600_000, now + 7_200_000)
  form = await fillWindow(page, 'Codex Pro', ahead.start, ahead.end)
  await expect(form).toContainText('overlaps an existing requests window')
  await form.getByRole('button', { name: 'Save allowance window' }).click()
  await expect(form.getByRole('alert')).toContainText('allowance windows overlap')
  expect(routes?.posts).toHaveLength(1)
  await form.getByRole('button', { name: 'Cancel' }).click()

  routes?.setMode('forbidden')
  form = await fillWindow(page, 'Pi on hsb1', ahead.start, ahead.end)
  await form.getByRole('button', { name: 'Save allowance window' }).click()
  await expect(form.getByRole('alert')).toContainText('permission denied')
  expect(routes?.posts).toHaveLength(2)
  await form.getByRole('button', { name: 'Cancel' }).click()

  routes?.setMode('uncertain')
  const active = await localRange(page, now - 60_000, now + 7_200_000)
  form = await fillWindow(page, 'Pi on hsb1', active.start, active.end, '2')
  await form.getByRole('button', { name: 'Save allowance window' }).click()
  await expect(form.getByRole('alert')).toContainText('may already be saved')
  await expect(form.getByRole('alert')).not.toContainText('Set allowance again')
  expect(routes?.posts).toHaveLength(3)
  await form.getByRole('button', { name: 'Check again' }).click()
  await expect(form.getByRole('alert')).toContainText('not on the account')
  expect(routes?.posts).toHaveLength(3)
  routes?.setMode('ok')
  await form.getByRole('button', { name: 'Save allowance window' }).click()
  await expect(page.getByRole('region', { name: 'Accounts and pacing' })).toContainText('Allowance window saved for Pi on hsb1.')
  await expect(row(page, 'Pi on hsb1')).toContainText('Unmeasured')
  expect(routes?.posts).toHaveLength(4)
})

test('screenshots at 390 and 1600 in light and dark', async ({ page }) => {
  mkdirSync('../.agent-shots', { recursive: true })
  await setup(page, { manage: true })
  const now = Date.now()
  const times = await localRange(page, now + 3_600_000, now + 7_200_000)
  for (const pass of [1, 2]) {
    for (const colorScheme of ['light', 'dark'] as const) {
      for (const width of [1600, 390] as const) {
        await page.setViewportSize({ width, height: width === 1600 ? 1000 : 844 })
        await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
        await page.goto('/agents')
        await page.getByText('Accounts and pacing', { exact: true }).first().click()
        const form = await fillWindow(page, 'Pi on hsb1', times.start, times.end, '1')
        const save = form.getByRole('button', { name: 'Save allowance window' })
        await save.scrollIntoViewIfNeeded()
        await expect(form).toContainText(times.zone)
        await expect(form.locator('time').first()).toBeVisible()
        await expect(save).toBeVisible()
        const border = await form.evaluate(el => getComputedStyle(el).borderLeftWidth)
        expect(border, `pass ${pass} ${colorScheme} ${width}`).toBe('0px')
        const boxes = await form.evaluate(el => {
          const formBox = el.getBoundingClientRect()
          const card = el.closest('.accounts')!.getBoundingClientRect()
          const button = el.querySelector('button[type="submit"]')!.getBoundingClientRect()
          return { formBottom: formBox.bottom, cardBottom: card.bottom, buttonTop: button.top, buttonBottom: button.bottom }
        })
        expect(boxes.buttonBottom).toBeLessThanOrEqual(boxes.formBottom + 1)
        expect(boxes.buttonBottom).toBeLessThanOrEqual(boxes.cardBottom + 1)
        expect(boxes.buttonBottom - boxes.buttonTop).toBeGreaterThan(20)
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
        await form.screenshot({ path: `../.agent-shots/bk2-allowance-${colorScheme}-${width}.png` })
        const results = await new AxeBuilder({ page }).include('.allowance').analyze()
        expect(results.violations, `pass ${pass} ${colorScheme} ${width}`).toEqual([])
      }
    }
  }
})
