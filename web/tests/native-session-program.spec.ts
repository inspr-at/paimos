// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1309: retain the panel authority and stability regressions in balanced ESSENTIAL files.
import { expect, test } from '@playwright/test'
import { LEAD_SESSION } from './lead-flow-fixtures'
import { me } from './work-fixtures'
import { WORKER, binding, setup, refresh } from './native-session-program-fixtures'
import { controlStability } from './control-stability'

test('AEON-1309: fake-host mode changes stay read-only, preserve the menu and reject stale context', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { state, worker, errors } = await setup(page)
  await page.goto(`/agents/${WORKER}`)
  const panel = page.getByRole('complementary', { name: 'Session details' })
  await expect(panel.getByRole('tab', { name: 'Details', exact: true })).toHaveAttribute('aria-selected', 'true')
  await expect(panel.getByRole('tab')).toHaveCount(4)
  await panel.getByRole('tab', { name: 'Events', exact: true }).click()
  await expect(panel.getByRole('button', { name: 'Load earlier' })).toHaveCount(0)
  await panel.getByRole('button', { name: 'Watch live' }).click()
  await expect(panel.getByText('Live events are not available for this session yet.')).toBeVisible()
  const attached = state.calls.find(call => call.path.endsWith('/attach'))!
  expect(attached.body).toMatchObject({ mode: 'view', view: 'structured', consent_id: null, expected_binding: binding })
  expect(state.calls.filter(call => /\/(take|return|input|stop|resize)$/.test(call.path))).toEqual([])

  // A qualified fake host makes the actual runtime transition. The UI cannot
  // pretend it performed a handover for which it has no controller/consent.
  state.native = { ...state.native, state: 'taken', mode: 'terminal', revision: 2, generation: 2,
    current_binding: { ...binding, runtime_epoch: 'runtime-2', binding_revision: 2 }, terminal_viewer_url: null,
    controller: { controller_id: 'controller-2', actor_id: me.id, client_id: 'local-client', epoch: 'lease-2', expires_at: '2027-01-01T00:00:00Z' } }
  worker.heartbeat_at = '2026-10-06T10:00:01Z'
  await refresh(page)
  await panel.getByRole('tab', { name: 'Terminal', exact: true }).click()
  await expect(panel.getByRole('button', { name: 'Hand back', exact: true })).toBeDisabled()
  await expect(panel.getByText('Terminal output needs current watch permission and the owner’s active consent.')).toBeVisible()
  await expect(panel.getByRole('tab')).toHaveCount(4)

  state.native = { ...state.native, state: 'recovering', mode: 'none', controller: null, revision: 3, generation: 3,
    current_binding: { ...binding, runtime_epoch: 'runtime-3', binding_revision: 3 }, reason: 'after_exit_unconfirmed' }
  worker.heartbeat_at = '2026-10-06T10:00:02Z'
  await refresh(page)
  await expect(panel.getByRole('button', { name: 'Take over', exact: true })).toBeDisabled()
  await expect(panel.getByRole('tab', { name: 'Terminal', exact: true })).toHaveAttribute('aria-selected', 'true')

  let arrived!: () => void, release!: () => void
  const started = new Promise<void>(resolve => { arrived = resolve }), barrier = new Promise<void>(resolve => { release = resolve })
  await page.route(`**/api/projects/p-pharos/harness-sessions/${WORKER}/native-context`, async route => {
    const old = structuredClone(state.native); arrived(); await barrier
    await route.fulfill({ json: old }).catch(() => {}) // Navigation deliberately aborts this read.
  })
  worker.heartbeat_at = '2026-10-06T10:00:03Z'
  await refresh(page); await started
  await page.evaluate(async id => { const { router } = await import('/src/router.ts'); await router.push(`/agents/${id}`) }, LEAD_SESSION)
  await expect(panel.getByRole('tab', { name: 'Lead', exact: true })).toBeVisible()
  release()
  await expect(panel.getByRole('tab', { name: 'Terminal', exact: true })).toHaveCount(0)
  expect(state.calls.filter(call => /\/(take|return|input|stop|resize)$/.test(call.path))).toEqual([])
  // The same panel is also mounted by project overlays. Its shortcut owner
  // must take precedence without registering the actions twice.
  await page.evaluate(async () => { const { openLeadPanel } = await import('/src/lib/leadOverlay.ts'); openLeadPanel('p-pharos') })
  const overlay = page.locator('.project-lead-panel')
  await expect(overlay.getByRole('tab', { name: 'Lead', exact: true })).toBeVisible()
  await page.keyboard.press('3')
  await expect(overlay.getByRole('tab', { name: 'Chat', exact: true })).toHaveAttribute('aria-selected', 'true')
  await overlay.getByRole('button', { name: 'Full screen', exact: true }).click()
  await expect(overlay).toHaveAttribute('data-layout', 'full')
  await overlay.evaluate(el => { const holder = document.createElement('div'); holder.dataset.nativeTerminalHolder = 'true'; holder.tabIndex = 0; el.append(holder); holder.focus() })
  await page.keyboard.press('Escape')
  await expect(overlay).toHaveAttribute('data-layout', 'full')
  await overlay.getByRole('button', { name: 'Close session details' }).focus()
  await page.keyboard.press('Escape')
  await expect(overlay).toHaveAttribute('data-layout', 'side')
  await page.keyboard.press('Escape')
  await expect(overlay).toHaveCount(0)
  // AEON-1291 / GUI-35 #39: the server has withheld terminal/export
  // capabilities from this reader. A taken runtime is status, never permission.
  state.native = { ...state.native, owner_person_id: 'another-person', state: 'taken', mode: 'terminal',
    terminal_viewer_url: null, controller: null, capabilities: { ...state.native.capabilities,
      take: false, take_supported: false, terminal_view: false, expert: false } }
  await page.goto(`/agents/${WORKER}`)
  await expect(panel.getByRole('tab', { name: 'Terminal', exact: true })).toHaveCount(0)
  await expect(panel.getByRole('tab', { name: 'Chat', exact: true })).toBeVisible()
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
    await page.setViewportSize({ width, height: 900 })
    await page.evaluate(async choice => { const { setTheme } = await import('/src/lib/theme.ts'); setTheme(choice, false) }, theme)
    const controls = { chat: panel.getByRole('tab', { name: 'Chat', exact: true }),
      details: panel.getByRole('tab', { name: 'Details', exact: true }), menu: panel.getByRole('tablist'),
      more: panel.getByRole('button', { name: 'More session actions' }), close: panel.getByRole('button', { name: 'Close session details' }) }
    const stable = await controlStability(page, controls)
    await stable.check(async () => {
      await controls.chat.click()
      await expect(controls.chat).toHaveAttribute('aria-selected', 'true')
      await expect(panel.getByRole('tab', { name: 'Terminal', exact: true })).toHaveCount(0)
    })
    await stable.check(async () => { await controls.details.click(); await expect(controls.details).toHaveAttribute('aria-selected', 'true') })
    stable.done()
    await page.screenshot({ path: info.outputPath(`AEON-1291-${width}-${theme}-member-status-chat.png`) })
  }
  expect(state.calls.filter(call => /\/(take|return|input|stop|resize)$/.test(call.path))).toEqual([])
  expect(errors).toEqual([])
})
