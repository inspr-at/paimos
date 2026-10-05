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
  const reads: string[] = []
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
    } else {
      reads.push(path)
      if (state.failLoad === path) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
    }
    return route.fulfill({ json: values[path] })
  })
  return { values, writes, reads, state }
}

// Risk: another admin's intervening write must survive a stale save, and Undo
// must restore the value accepted by the conditional save, not an older read.
for (const item of [
  { path: 'agent-activity', key: 'mode', label: 'Agent activity', initial: 'agent_summary', remote: 'tool_activity', next: 'off' },
  { path: 'eta-interval', key: 'interval_minutes', label: 'Estimates', initial: 10, remote: 20, next: 30 },
  { path: 'heartbeat-lost', key: 'heartbeat_lost_minutes', label: 'Silent sessions', initial: 15, remote: 20, next: 30 },
]) {
  test(`stale ${item.label} saves require a reload before saving and Undo`, async ({ page }) => {
    const { values, writes, reads } = await setup(page)
    await page.goto('/settings/agents')
    const card = page.locator('#while-agents-work')
    const group = card.getByRole('radiogroup')
    const estimate = card.locator('#interval_minutes'), lost = card.locator('#heartbeat_lost_minutes')
    await expect(estimate).toHaveValue('10'); await expect(lost).toHaveValue('15')
    await expect(group.locator('input[value="agent_summary"]')).toBeChecked()
    const feedback = card.locator(item.key === 'mode' ? '#mode-feedback' : `#${item.key}-feedback`)
    const guard = await controlStability(page, { estimate, lost, options: group, 'clicked row': group.locator('.opt').first() })
    const edit = async () => {
      if (item.key === 'mode') await group.locator(`input[value="${item.next}"]`).check()
      else { await card.locator(`#${item.key}`).fill(String(item.next)); await card.locator(`#${item.key}`).press('Tab') }
    }
    values[item.path] = { [item.key]: item.remote }
    await guard.check(async () => { await edit(); await expect(feedback).toContainText('Changed elsewhere') })
    expect(values[item.path]).toEqual({ [item.key]: item.remote })
    expect(writes).toEqual([{ path: item.path, body: { [item.key]: item.next, [`expected_${item.key}`]: item.initial } }])
    await expect(page.locator('.toast').filter({ hasText: `${item.label} saved.` })).toHaveCount(0)
    const readsBefore = reads.length
    await guard.check(async () => {
      await card.getByRole('button', { name: 'Reload', exact: true }).click()
      await expect(feedback).toBeEmpty()
      if (item.key === 'mode') await expect(group.locator(`input[value="${item.remote}"]`)).toBeChecked()
      else await expect(card.locator(`#${item.key}`)).toHaveValue(String(item.remote))
    })
    expect(reads.slice(readsBefore)).toEqual([item.path])
    await guard.check(async () => {
      await edit()
      await expect(page.locator('.toast:not(.toast-leave-active)').filter({ hasText: `${item.label} saved.` })).toBeVisible()
    })
    expect(values[item.path]).toEqual({ [item.key]: item.next })
    await guard.check(async () => {
      await page.locator('.toast:not(.toast-leave-active)').filter({ hasText: `${item.label} saved.` }).getByRole('button', { name: 'Undo', exact: true }).click()
      await expect(page.locator('.toast:not(.toast-leave-active)').filter({ hasText: `${item.label} restored.` })).toBeVisible()
    })
    expect(values[item.path]).toEqual({ [item.key]: item.remote })
    expect(writes).toEqual([
      { path: item.path, body: { [item.key]: item.next, [`expected_${item.key}`]: item.initial } },
      { path: item.path, body: { [item.key]: item.next, [`expected_${item.key}`]: item.remote } },
      { path: item.path, body: { [item.key]: item.remote, [`expected_${item.key}`]: item.next } },
    ])
    guard.done()
  })
}

// Risk: a numeric Undo conflict must remain visible through blur/edit and failed
// reloads; only a successful authoritative read may unblock the next save.
for (const item of [
  { path: 'eta-interval', key: 'interval_minutes', label: 'Estimates', initial: 10 },
  { path: 'heartbeat-lost', key: 'heartbeat_lost_minutes', label: 'Silent sessions', initial: 15 },
]) {
  test(`numeric Undo conflict for ${item.label} persists until an authoritative reload`, async ({ page }) => {
    const { values, writes, reads, state } = await setup(page)
    await page.goto('/settings/agents')
    const card = page.locator('#while-agents-work'), input = card.locator(`#${item.key}`)
    const feedback = card.locator(`#${item.key}-feedback`)
    await expect(card.locator('#interval_minutes')).toHaveValue('10')
    await expect(card.locator('#heartbeat_lost_minutes')).toHaveValue('15')
    const guard = await controlStability(page, {
      estimate: card.locator('#interval_minutes'), lost: card.locator('#heartbeat_lost_minutes'), options: card.getByRole('radiogroup'),
    })
    await input.fill('20'); await input.press('Tab')
    const saved = page.locator('.toast:not(.toast-leave-active)').filter({ hasText: `${item.label} saved.` })
    await expect(saved).toBeVisible()
    values[item.path] = { [item.key]: 30 }
    await guard.check(async () => {
      await saved.getByRole('button', { name: 'Undo', exact: true }).click()
      await expect(feedback).toContainText('Changed elsewhere')
      await expect(input).toHaveValue('20')
    })
    expect(writes[1]).toEqual({ path: item.path, body: { [item.key]: item.initial, [`expected_${item.key}`]: 20 } })
    const before = { reads: reads.length, writes: writes.length }
    await guard.check(async () => {
      await input.focus(); await input.press('Tab')
      await expect(feedback).toContainText('Changed elsewhere')
      await expect(input).toHaveAttribute('aria-invalid', 'true')
    })
    const reload = card.getByRole('button', { name: 'Reload', exact: true })
    await expect(reload).toBeVisible()
    await guard.check(async () => {
      await input.fill('40'); await input.press('Tab')
      await expect(feedback).toContainText('Changed elsewhere')
    })
    expect(reads).toHaveLength(before.reads); expect(writes).toHaveLength(before.writes)
    expect(values[item.path]).toEqual({ [item.key]: 30 })
    state.failLoad = item.path
    let releaseRead!: () => void, readStarted!: () => void
    const pendingRead = new Promise<void>(resolve => { releaseRead = resolve })
    const startedRead = new Promise<void>(resolve => { readStarted = resolve })
    await page.route(`**/api/settings/${item.path}`, async route => {
      if (route.request().method() === 'GET') { readStarted(); await pendingRead }
      await route.fallback()
    })
    const reloadGuard = await controlStability(page, { reload })
    await guard.check(async () => {
      try {
        await reloadGuard.check(async () => {
          await reload.click(); await startedRead
          await expect(reload).toBeDisabled()
          await expect(feedback).toContainText('Changed elsewhere')
        })
        reloadGuard.done()
      } finally { releaseRead() }
      await expect(card.locator('#mode-feedback')).toContainText("Couldn't load this")
      await expect(feedback).toContainText('Changed elsewhere')
      await expect(input).toHaveValue('40'); await expect(input).toBeDisabled()
    })
    expect(reads.slice(before.reads)).toEqual([item.path])
    state.failLoad = ''
    await guard.check(async () => {
      await card.getByRole('button', { name: 'Try again', exact: true }).click()
      await expect(input).toHaveValue('30'); await expect(input).toBeEnabled()
      await expect(input).toHaveAttribute('aria-invalid', 'false'); await expect(feedback).toBeEmpty()
    })
    expect(reads.slice(before.reads)).toEqual([item.path, item.path])
    await expect(reload).toHaveCount(0)
    await guard.check(async () => {
      await input.fill('40'); await input.press('Tab')
      await expect(page.locator('.toast:not(.toast-leave-active)').filter({ hasText: `${item.label} saved.` })).toBeVisible()
    })
    expect(writes[2]).toEqual({ path: item.path, body: { [item.key]: 40, [`expected_${item.key}`]: 30 } })
    expect(values[item.path]).toEqual({ [item.key]: 40 })
    guard.done()
  })
}

test('a numeric conflict suppresses shared saved feedback from another field', async ({ page }) => {
  // Keep the other field's Saved flag alive: expiry must not satisfy this test.
  const at = new Date('2026-10-05T12:00:00Z')
  await page.clock.install({ time: at })
  await page.clock.pauseAt(new Date(at.getTime() + 1000))
  const { values, writes } = await setup(page)
  await page.goto('/settings/agents')
  const card = page.locator('#while-agents-work'), estimate = card.locator('#interval_minutes')
  await expect(estimate).toHaveValue('10')
  await expect(card.locator('#heartbeat_lost_minutes')).toHaveValue('15')
  const group = card.getByRole('radiogroup')
  await expect(group.locator('input[value="agent_summary"]')).toBeChecked()
  await estimate.fill('20'); await estimate.press('Tab')
  const saved = page.locator('.toast:not(.toast-leave-active)').filter({ hasText: 'Estimates saved.' })
  await expect(saved).toBeVisible()
  values['eta-interval'] = { interval_minutes: 30 }
  await saved.getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(card.locator('#interval_minutes-feedback')).toContainText('Changed elsewhere')
  await group.getByRole('radio', { name: /^Off/ }).check()
  await expect(page.locator('.toast:not(.toast-leave-active)').filter({ hasText: 'Agent activity saved.' })).toBeVisible()
  await expect(card.locator('#interval_minutes-feedback')).toContainText('Changed elsewhere')
  await expect(card.getByRole('status').filter({ hasText: /^Saved$/ })).toHaveCount(0)
  expect(writes.map(item => item.path)).toEqual(['eta-interval', 'eta-interval', 'agent-activity'])
  expect(values['eta-interval']).toEqual({ interval_minutes: 30 })
})

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
  state.fail = 'agent-activity'
  await group.getByRole('radio', { name: /^Off/ }).check()
  await expect(card.locator('#mode-feedback')).toContainText('Could not save')
  await expect(card.locator('#mode-feedback')).toContainText("Couldn't load this")
  expect(values['agent-activity']).toEqual({ mode: 'agent_summary' })
  state.fail = ''
  state.failLoad = ''
  await card.getByRole('button', { name: 'Try again', exact: true }).click()
  await expect(lost).toHaveValue('15')
  await card.getByRole('button', { name: 'Reload', exact: true }).click()
  await expect(group.getByRole('radio', { name: /^Agent summary/ })).toBeChecked()
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
  expect(writes).toEqual([{ path: 'agent-activity', body: { mode: 'off', expected_mode: 'agent_summary' } }])
  const mac = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent))
  const submit = mac ? 'Meta+Enter' : 'Control+Enter'
  await estimate.fill('1')
  await estimate.press('Enter')
  await estimate.press(mac ? 'Control+Enter' : 'Meta+Enter')
  expect(writes).toEqual([{ path: 'agent-activity', body: { mode: 'off', expected_mode: 'agent_summary' } }])
  await expect(estimate).toBeFocused()
  await expect(estimate).toHaveAttribute('aria-keyshortcuts', submit)
  for (const value of ['1', '240']) await guard.check(async () => {
    await estimate.fill(value); await estimate.press(submit)
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
    const { values } = await setup(page, theme)
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
    values['eta-interval'] = { interval_minutes: 20 }
    await guard.check(async () => {
      await estimate.fill('30'); await estimate.press('Tab')
      await expect(card.locator('#interval_minutes-feedback')).toContainText('Changed elsewhere')
      await expect(card.getByRole('button', { name: 'Reload', exact: true })).toBeVisible()
    })
    guard.done()
    if (width === 390) expect((await estimate.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    await page.screenshot({ path: `test-results/aeon-699-nb/agents-${width}-${theme}.png`, fullPage: true })
  })
}
