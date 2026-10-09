// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'

import { expectStableControls } from './helpers/stable'
import { expectDialRowsFitContent } from './helpers/dial-layout'
import type { Shell } from './helpers/no-shift-shells'
import { session, setup } from './no-shift-fixtures'

for (const width of [1440, 1024, 390]) {
  test(`Agents header keeps pause actions and Decision Desk navigation at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await setup(page)
    await page.goto('/agents')
    const header = page.locator('.agents-page .page-head')
    const more = header.getByRole('button', { name: 'More agent actions', exact: true })
    const add = header.getByRole('button', { name: 'New: start a lead, attach a session or connect a machine', exact: true })
    await expectStableControls({
      controls: { more, add },
      interactions: [{ name: 'open merged navigation', run: async () => {
        await more.click()
        const menu = page.getByRole('menu')
        // AEON-780/783: "…" keeps five items; Pause all, Resume all and Wind down live in the Wind down popover, History in the Sessions head.
        await expect(menu.getByRole('menuitem')).toHaveText([/^Model preferences/, /^Usage/, /^Decision Desk/, /^Agent keys/, /^Agent settings/])
        await expect(header.getByRole('button', { name: 'Wind down', exact: true })).toBeVisible()
        await expect(page.locator('.sessions .head-tools').getByRole('button', { name: /^Show history/ })).toBeVisible()
        await expect(menu.getByRole('menuitem', { name: 'Decision Desk', exact: true })).toHaveAttribute('href', '/decision-desk')
      } }],
    })
  })

  test(`managed Interrupt menu and Stop now button stay put at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const data = await setup(page)
    const ownership = { daemon_id: 'fixture', generation: 'a'.repeat(32), process_id: 'b'.repeat(32), root_pid: 1234, group_id: 1234, started_at: new Date().toISOString() }
    Object.assign(data.sessions[0]!, { advertised_capabilities: ['status', 'interrupt', 'stop', 'managed_control_v1'], process_ownership: ownership, process_observed_at: new Date().toISOString() })
    let request: Record<string, unknown>
    await page.route('**/api/projects/*/harness-sessions/*/managed-controls', route => {
      request = route.request().postDataJSON()
      return route.fulfill({ status: 201, json: { id: request.request_id, session_id: session(1), kind: request.kind, state: 'pending', expires_at: new Date(Date.now() + 45000).toISOString() } })
    })
    await page.route('**/api/projects/*/harness-sessions/*/controls/*', route => route.fulfill({ json: { id: request.request_id, session_id: session(1), kind: request.kind, state: 'completed', outcome: 'applied', reason: 'native_interrupt_acknowledged' } }))
    await page.goto(`/agents/${session(1)}`)
    const controls = page.getByRole('region', { name: 'Session controls' })
    const more = controls.getByRole('button', { name: 'More session controls', exact: true })
    const interrupt = page.getByRole('menuitem', { name: /^Interrupt this step/ })
    // The panel has a Stop now button; the session list has a menuitem.
    const stop = page.getByRole('button', { name: 'Stop now…', exact: true })
    const dialog = page.getByRole('dialog', { name: /^Stop now / })
    await more.click()
    await expectStableControls({
      controls: { interrupt, more, stop },
      interactions: [
        { name: 'interrupt receipt', run: async () => { await interrupt.click(); await expect(controls.getByRole('status')).toContainText('Interrupt applied'); await more.click() } },
        { name: 'open and cancel Stop now', run: async () => { await more.click(); await stop.click(); await dialog.getByRole('button', { name: /^Stop now/ }).hover(); await dialog.getByRole('button', { name: /^Cancel/ }).click(); await more.click() } },
      ],
    })
    await more.click()
    await stop.click()
    await expectStableControls({
      controls: { ...(width === 390 ? { sheet: dialog } : {}), close: dialog.getByRole('button', { name: 'Close pause dialog' }), confirm: dialog.getByRole('button', { name: /^Stop now/ }), cancel: dialog.getByRole('button', { name: /^Cancel/ }), footer: dialog.locator('.pause-actions') },
      scrollAreas: { body: dialog.locator('.pause-body') },
      interactions: [{ name: 'hover confirmation', run: () => dialog.getByRole('button', { name: /^Stop now/ }).hover() }],
    })
  })

  test(`approval choices, typing and failure keep the decision controls put at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await setup(page, true)
    await page.goto('/agents')
    const card = page.getByRole('region', { name: 'Needs you' }).locator('[data-row^="a:"]').first()
    await card.getByRole('button', { name: 'Approve', exact: true }).click()
    const form = card.locator('.decision')
    const submit = form.locator('button[type="submit"]')
    await expectStableControls({
      controls: { card, form, reason: form.locator('textarea'), cancel: form.getByRole('button', { name: 'Cancel' }), submit },
      scrollAreas: { feedback: form.locator('.decision-feedback') },
      interactions: [
        ...['d', 'a'].map(key => ({ name: `switch to ${key === 'd' ? 'deny' : 'approve'}`, run: async () => { await card.focus(); await page.keyboard.press(key); await expect(submit).toContainText(key === 'd' ? 'Deny permission' : 'Approve permission') } })),
        { name: 'type a long reason', run: () => form.locator('textarea').fill('Keep the controls still. '.repeat(100)) },
        { name: 'failed decision feedback', run: async () => { await submit.click(); await expect(form.getByRole('alert')).toBeVisible(); await expect(submit).toBeEnabled() } },
      ],
    })
  })

  test(`Stop now menu and Pause dialog stay put through the session series at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const data = await setup(page)
    data.sessions[0]!.display_label = 'worker-one'
    // Managed names allow 128 characters: cover wrapping words and a single token.
    data.sessions[1]!.display_label = 'worker '.repeat(18) + 'xx'
    data.sessions[2]!.display_label = 'x'.repeat(128)
    await page.goto('/agents')
    async function open(n: number) {
      const row = page.locator(`[data-row="s:${session(n)}"]`)
      const name = data.sessions[n - 1]!.display_label
      const actions = row.getByRole('button', { name: `Actions for ${name}`, exact: true })
      await actions.click()
      const stopItem = page.getByRole('menuitem', { name: 'Stop now…', exact: true })
      await expectStableControls({
        controls: { actions, stopItem },
        interactions: [{ name: 'hover Stop now menuitem', run: () => stopItem.hover() }],
      })
      await stopItem.click()
      await expect(page.getByRole('dialog', { name: `Stop now ${name}`, exact: true })).toBeVisible()
      await expect(page.getByRole('heading', { name: `Stop now ${name}`, exact: true })).toHaveText(`Stop now ${name}`)
    }
    await open(1)
    const dialog = page.getByRole('dialog', { name: /^Stop now / })
    const cancel = dialog.getByRole('button', { name: /^Cancel/ })
    const stop = dialog.getByRole('button', { name: /^Stop now/ })
    await expectStableControls({
      controls: { ...(width === 390 ? { dialog } : {}), close: dialog.getByRole('button', { name: 'Close pause dialog' }), actions: dialog.locator('.pause-actions'), cancel, stop, heading: dialog.getByRole('heading') },
      scrollAreas: { body: dialog.locator('.pause-body'), head: dialog.locator('.pause-head') },
      interactions: [2, 3, 1].map(n => ({ name: `next/previous session ${n}`, run: async () => { await cancel.click(); await open(n); await expect(stop).toBeFocused() } })),
    })
  })

  test(`Agents working dial and selectors stay put at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    await setup(page)
    await page.goto('/agents')
    const dial = page.getByRole('region', { name: 'Agents at once' })
    const more = dial.getByRole('button', { name: 'One agent more at once' })
    const fewer = dial.getByRole('button', { name: 'One agent fewer at once' })
    const row = dial.locator('[data-key="codex"]')
    // Destination labels change with the current value; the stepper controls persist.
    const rowFewer = row.locator('.lim-step .pm').nth(0), rowMore = row.locator('.lim-step .pm').nth(1)
    await expect(row).toBeVisible()
    await page.evaluate(() => document.fonts.ready)
    // The merged dial uses a separate explanation row and a container breakpoint.
    // Verify uniform selector rows; the interaction guard below proves stability.
    const rowHeights = await dial.locator('.rows > li').evaluateAll(items => items.map(item => {
      const style = getComputedStyle(item)
      // The first row has no separator; compare the actual content slots.
      return item.getBoundingClientRect().height - parseFloat(style.borderTopWidth) - parseFloat(style.borderBottomWidth)
    }))
    expect(rowHeights.length).toBeGreaterThan(0)
    for (const height of rowHeights) {
      expect(height).toBeGreaterThan(0)
      expect(Math.abs(height - rowHeights[0]!)).toBeLessThanOrEqual(.5)
    }
    await expectDialRowsFitContent(dial)
    await expectStableControls({
      controls: { more, fewer, selector: row.getByRole('radiogroup'), row, rowMore, rowFewer },
      scrollAreas: { rows: dial.locator('.rows') },
      interactions: [
        // Theme feedback may animate several properties. Judge the control's
        // geometry through focus, hover and press instead of its CSS spelling.
        { name: 'focus total stepper', run: async () => { await more.focus(); await expect(more).toBeFocused() } },
        { name: 'hover total stepper', run: () => more.hover() },
        // A held + repeats (AEON-781); it may reach 30 and disable itself, so the steps below start downward.
        { name: 'hold total stepper', run: () => page.mouse.down() },
        { name: 'release total stepper', run: () => page.mouse.up() },
        ...[fewer, fewer, more, more].map((button, i) => ({ name: `total step ${i + 1}`, run: () => button.click() })),
        ...[rowMore, rowFewer].map((button, i) => ({ name: `${i ? 'fewer' : 'more'} on Codex`, run: async () => { await expect(button).toHaveAccessibleName(/^Codex:/); await button.click() } })),
      ],
    })
    await expectDialRowsFitContent(dial)
  })

  test(`Agents working harness modes stay put at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    await setup(page)
    await page.goto('/agents')
    const dial = page.getByRole('region', { name: 'Agents at once' })
    const more = dial.getByRole('button', { name: 'One agent more at once' })
    const fewer = dial.getByRole('button', { name: 'One agent fewer at once' })
    const row = dial.locator('[data-key="codex"]')
    await expect(row).toBeVisible()
    await expectStableControls({
      controls: { more, fewer, selector: row.getByRole('radiogroup'), row },
      scrollAreas: { rows: dial.locator('.rows') },
      interactions: ['No limit', 'Off', 'At most'].map(name => ({ name, run: async () => { await row.getByRole('radio', { name, exact: true }).click(); await expect(row.getByRole('radio', { name, exact: true })).toHaveAttribute('aria-checked', 'true') } })),
    })
  })

  // AEON-784: folding a parent, walking the tree with ← and →, toggling History
  // and folding the Sessions section never move a control. A parent's line stays
  // when it folds, so its row keeps its height and actions.
  test(`Sessions tree and section folds keep their controls put at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    const data = await setup(page)
    Object.assign(data.sessions[1]!, { parent_harness_session_id: data.sessions[0]!.id })
    await page.goto('/agents')
    const sessions = page.getByRole('region', { name: 'Sessions', exact: true })
    const lead = sessions.locator(`[data-row="s:${data.sessions[0]!.id}"]`)
    const worker = sessions.locator(`[data-row="s:${data.sessions[1]!.id}"]`)
    const fold = lead.locator('.tree-fold')
    // Its name says Fold or Unfold; the same button is measured either way.
    const sectionFold = sessions.locator('.fs-head > .fs-tog')
    const history = sessions.getByRole('button', { name: 'Show history: every ended or removed session' })
    await expect(worker).toBeVisible()
    await expectStableControls({
      controls: { fold, more: lead.getByRole('button', { name: /^Actions for / }), glyph: lead.locator('.bot'), history, sectionFold },
      interactions: [
        { name: 'hover fold', run: () => fold.hover() },
        { name: 'fold the lead', run: async () => { await fold.click(); await expect(worker).toHaveCount(0); await expect(lead.locator('.kid-count')).toHaveText('1 sub-agent') } },
        { name: 'unfold the lead', run: async () => { await fold.click(); await expect(worker).toBeVisible() } },
        { name: '← folds', run: async () => { await lead.focus(); await page.keyboard.press('ArrowLeft'); await expect(fold).toHaveAttribute('aria-expanded', 'false') } },
        { name: '→ unfolds', run: async () => { await page.keyboard.press('ArrowRight'); await expect(worker).toBeVisible() } },
      ],
    })
    // The title's words change with History; its fold chevron stays put.
    const title = sessions.locator('.fs-title')
    await expectStableControls({
      controls: { sectionFold },
      interactions: [
        { name: 'history on', run: async () => { await history.click(); await expect(title).toContainText('History') } },
        { name: 'history off', run: async () => { await sessions.getByRole('button', { name: 'Back to sessions' }).click(); await expect(title).toContainText('Sessions') } },
        { name: 'fold the section', run: async () => { await sectionFold.click(); await expect(sessions.locator('.fs-sum')).toContainText('live on') } },
        { name: 'unfold the section', run: async () => { await sessions.getByRole('button', { name: 'Sessions: unfold' }).click(); await expect(lead).toBeVisible() } },
      ],
    })
  })
}

test('saving a pacing option keeps keyboard focus on it', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page)
  let release!: () => void
  const pending = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/agent-accounts/capacity/schedule', async route => {
    if (route.request().method() === 'PUT') await pending
    await route.fallback()
  })
  // AEON-782: the pacing settings live in Settings › Accounts and computers › Capacity and load.
  await page.goto('/settings/accounts#capacity-and-load')
  const pacing = page.locator('#capacity-and-load')
  const seven = pacing.getByRole('radio', { name: '7', exact: true })
  await seven.click()
  await expect(seven).toHaveAttribute('aria-disabled', 'true')
  await expect(seven).toBeFocused()
  release()
  await expect(seven).toHaveAttribute('aria-checked', 'true')
  await expect(seven).toHaveAttribute('aria-disabled', 'false')
  await expect(seven).toBeFocused()
})

test('phone steer feedback and retry never move the pinned action bar', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const data = await setup(page)
  Object.assign(data.sessions[0]!, { advertised_capabilities: ['status', 'steer', 'stop', 'managed_control_v1'], process_ownership: { daemon_id: 'fixture', generation: 'a'.repeat(32), process_id: 'b'.repeat(32), root_pid: 1234, group_id: 1234, started_at: new Date().toISOString() }, process_observed_at: new Date().toISOString() })
  await page.route('**/api/projects/*/harness-sessions/*/managed-controls', route => route.abort('failed'))
  await page.goto(`/agents/${session(1)}`)
  await page.getByRole('region', { name: 'Session controls' }).getByRole('button', { name: 'Steer', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Steer', exact: true })
  const send = sheet.getByRole('button', { name: 'Send steer' })
  await expectStableControls({
    controls: { sheet, send, cancel: sheet.getByRole('button', { name: 'Cancel' }), footer: sheet.locator('.pinned-actions'), draft: sheet.getByLabel('What should change?') },
    scrollAreas: { body: sheet.locator('.sheet-body') },
    interactions: [
      { name: 'long draft', run: () => sheet.getByLabel('What should change?').fill('Keep the footer still. '.repeat(200)) },
      { name: 'lost response and retry controls', run: async () => { await send.click(); await expect(sheet.getByRole('alert')).toBeVisible(); await expect(sheet.getByRole('button', { name: 'Retry same request' })).toBeVisible() } },
    ],
  })
})
