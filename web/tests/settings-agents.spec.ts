// SPDX-License-Identifier: AGPL-3.0-only
// Risk: invalid or failed writes must not look saved; feedback must not move controls.
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { controlStability } from './control-stability'

async function setup(page: Page, theme: 'light' | 'dark' = 'light', admin = true) {
  const work = fixtures(); work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin })
  await mockSettings(page, settingsData())
  const values: Record<string, Record<string, unknown>> = {
    'agent-activity': { mode: 'agent_summary' }, 'eta-interval': { interval_minutes: 10 }, 'heartbeat-lost': { heartbeat_lost_minutes: 15 },
  }
  const writes: { path: string; body: Record<string, unknown> }[] = []
  const state = { fail: '', failLoad: '' }
  await page.route('**/api/settings/*', async route => {
    const path = new URL(route.request().url()).pathname.split('/').pop()!
    if (!(path in values)) return route.fallback()
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON()
      writes.push({ path, body })
      if (state.fail === path) return route.fulfill({ status: 503, json: { error: 'unavailable' } })
      const key = Object.keys(values[path])[0]
      if (`expected_${key}` in body && body[`expected_${key}`] !== values[path][key]) return route.fulfill({ status: 409, json: { error: 'changed elsewhere' } })
      values[path] = { [key]: body[key] }
    } else if (state.failLoad === path) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
    return route.fulfill({ json: values[path] })
  })
  return { values, writes, state }
}

test('stored agent settings stay together; bounds, failures, Undo and Models link stay honest', async ({ page }) => {
  const { writes, state, values } = await setup(page)
  state.failLoad = 'heartbeat-lost'
  await page.goto('/settings/agents')
  const card = page.getByRole('region', { name: 'While agents work' })
  const estimate = card.locator('#interval_minutes'), lost = card.locator('#heartbeat_lost_minutes')
  const group = card.getByRole('radiogroup', { name: 'Agent activity' })
  await expect(estimate).toHaveValue('10')
  await expect(lost).toBeDisabled()
  await expect(card.getByRole('alert')).toContainText("Couldn't load this")
  state.failLoad = ''
  await card.getByRole('button', { name: 'Try again', exact: true }).click()
  await expect(lost).toHaveValue('15')
  const guard = await controlStability(page, {
    estimates: estimate, 'silent sessions': lost, options: group,
    off: group.locator('.opt').nth(0), tool: group.locator('.opt').nth(1), summary: group.locator('.opt').nth(2),
  })
  for (const [value, message] of [['0', 'Use 1 to 240'], ['241', 'Use 1 to 240'], ['1.5', 'Use 1 to 240'], ['', 'Use 1 to 240']]) {
    await guard.check(async () => {
      await estimate.fill(value); await estimate.press('Tab')
      await expect(estimate).toHaveValue(value)
      await expect(estimate).toHaveAttribute('aria-invalid', 'true')
      await expect(card.locator('#interval_minutes-feedback')).toContainText(message)
    })
  }
  for (const value of ['4', '1441']) await guard.check(async () => {
    await lost.fill(value); await lost.press('Tab')
    await expect(lost).toHaveValue(value)
    await expect(card.locator('#heartbeat_lost_minutes-feedback')).toContainText('Use 5 to 1440')
  })
  expect(writes).toEqual([])
  for (const value of ['1', '240']) await guard.check(async () => {
    await estimate.fill(value); await estimate.press('Enter')
    await expect(estimate).toBeEnabled(); await expect(estimate).toHaveAttribute('aria-invalid', 'false')
    expect(values['eta-interval']).toEqual({ interval_minutes: Number(value) })
  })
  await page.locator('.toast:not(.toast-leave-active)').filter({ hasText: 'Estimates saved.' }).getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(estimate).toHaveValue('1')
  expect(values['eta-interval']).toEqual({ interval_minutes: 1 })
  for (const value of ['5', '1440']) await guard.check(async () => {
    await lost.fill(value); await lost.press('Tab')
    await expect(lost).toBeEnabled(); await expect(lost).toHaveAttribute('aria-invalid', 'false')
    expect(values['heartbeat-lost']).toEqual({ heartbeat_lost_minutes: Number(value) })
  })
  state.fail = 'eta-interval'
  await guard.check(async () => {
    await estimate.fill('30'); await estimate.press('Tab')
    await expect(card.locator('#interval_minutes-feedback')).toContainText('Could not save')
    await expect(estimate).toHaveValue('30')
    expect(values['eta-interval']).toEqual({ interval_minutes: 1 })
  })
  state.fail = ''
  await estimate.focus(); await estimate.press('Tab')
  await expect(card.getByRole('status').filter({ hasText: /^Saved$/ })).toBeVisible()
  for (const mode of ['tool_activity', 'off', 'agent_summary']) await guard.check(async () => {
    await group.locator(`input[value="${mode}"]`).check()
    await expect(group).toBeEnabled()
    expect(values['agent-activity']).toEqual({ mode })
  })
  // A remote edit makes this toast's reverse write obsolete.
  values['agent-activity'] = { mode: 'off' }
  const before = writes.length
  await page.locator('.toast:not(.toast-leave-active)').filter({ hasText: 'Agent activity saved.' }).getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(card.locator('#mode-feedback')).toContainText('Changed elsewhere')
  expect(writes).toHaveLength(before + 1)
  expect(values['agent-activity']).toEqual({ mode: 'off' })
  guard.done()
  await expect(page.locator('#models').getByRole('link', { name: 'Accounts and computers' })).toHaveAttribute('href', '/settings/accounts')
  await page.locator('#models').getByRole('link', { name: 'Accounts and computers' }).click()
  await expect(page).toHaveURL('/settings/accounts')
  await expect(page.locator('.toast:not(.toast-leave-active)').filter({ hasText: 'Agent activity saved.' })).toHaveCount(0)
})

test('members do not see or fetch workspace agent settings', async ({ page }) => {
  const { writes } = await setup(page, 'light', false)
  const reads: string[] = []
  page.on('request', request => { if (/settings\/(agent-activity|eta-interval|heartbeat-lost)/.test(request.url())) reads.push(request.url()) })
  await page.goto('/settings/agents')
  await expect(page.getByRole('heading', { name: 'Agents settings are for workspace admins' })).toBeVisible()
  expect(reads).toEqual([]); expect(writes).toEqual([])
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`agent card fits ${width}px ${theme}, with stable controls and long German copy`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1100 })
    await setup(page, theme)
    await page.goto('/settings/agents')
    const card = page.locator('#while-agents-work')
    const estimate = card.locator('#interval_minutes'), lost = card.locator('#heartbeat_lost_minutes')
    await expect(estimate).toHaveValue('10'); await expect(lost).toHaveValue('15')
    await card.locator('.flabel small').nth(1).evaluate(el => { el.textContent = 'Wie oft ein arbeitender Agent meldet, wann eine Aufgabe fertig sein und veröffentlicht werden wird.' })
    const group = card.getByRole('radiogroup')
    const guard = await controlStability(page, { estimate, lost, options: group, 'clicked row': group.locator('.opt').nth(0) })
    await guard.check(async () => {
      await estimate.fill('241'); await estimate.press('Tab')
      await expect(estimate).toHaveAttribute('aria-invalid', 'true')
    })
    await guard.check(async () => { await group.getByRole('radio', { name: /^Off/ }).check(); await expect(group).toBeEnabled() })
    guard.done()
    if (width === 390) expect((await estimate.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    await page.screenshot({ path: `test-results/aeon-699-nb/agents-${width}-${theme}.png`, fullPage: true })
  })
}
