// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { fixtures, mockWork } from './work-fixtures'
import { expectStableControls } from './helpers/stable'
import type { WorkAction, WorkPreview } from '../src/lib/workLifecycle'

async function openWork(page: Page, theme: 'light' | 'dark', parent = false) {
  const data = fixtures()
  const target = data.nodes.find(n => n.id === 'n-1')!
  target.kind_slug = 'work'; target.state = 'open'
  target.title = 'Langfristige Bereitstellung der vollständig dokumentierten Arbeitsaufträge mit sicherer Übergabe'
  target.estimate = { hours: 4, is_parent: parent }
  await mockWork(page, data)
  await page.route('**/api/preferences/theme', route => route.fulfill({ json: { choice: theme } }))
  const requests: Record<string, unknown>[] = []
  let pending: WorkAction | null = null
  let stopped = false
  let failContinue = false
  const preview: WorkPreview = { is_leaf: !parent, busy: true, open_leaves: parent ? 3 : 1, updated_at: target.updated_at, scope_revision: 'a'.repeat(64), pending }
  await page.route('**/api/nodes/*/work-lifecycle**', async route => {
    const req = route.request(), suffix = new URL(req.url()).pathname.replace(/^.*\/work-lifecycle/, '')
    if (req.method() === 'GET') return route.fulfill({ json: { ...preview, pending: pending?.state === 'waiting' ? pending : null } })
    if (req.method() === 'DELETE') { const old = pending; pending = null; return route.fulfill({ json: { ...old, state: 'abandoned' } }) }
    if (suffix.endsWith('/continue')) {
      if (failContinue) return route.fulfill({ status: 403, json: { error: 'Permission was revoked; the saved request is still waiting.' } })
      if (stopped && pending) pending = { ...pending, state: 'completed', waiting_count: 0, result: pending.kind === 'split' ? ['child-1', 'child-2'] : ['leaf-1', 'leaf-2', 'leaf-3'] }
      return route.fulfill({ json: pending })
    }
    const body = req.postDataJSON() as Record<string, unknown>; requests.push(body)
    pending = { id: String(body.request_id), node_id: target.id, kind: body.kind as 'split' | 'cancel', state: 'waiting', target_count: preview.open_leaves, waiting_count: preview.open_leaves, result: [] }
    return route.fulfill({ json: pending })
  })
  await page.goto('/p/PHAROS/PHAROS-11')
  await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
  await page.getByRole('button', { name: 'Work actions', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Work actions', exact: true })
  await expect(sheet.getByText(parent ? '3 open leaves will be cancelled.' : 'This leaf is busy.', { exact: false })).toBeVisible()
  return { sheet, requests, stop: () => { stopped = true }, revoke: () => { failContinue = true } }
}
for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`work actions stay still ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const world = await openWork(page, theme)
    const { sheet } = world
    const split = sheet.getByRole('radio', { name: 'Split into children', exact: true })
    const cancel = sheet.getByRole('radio', { name: 'Cancel with its open children', exact: true })
    const submit = sheet.locator('.actions .primary')
    const controls = { split, cancel, choices: sheet.locator('.choices'), submit, close: sheet.getByRole('button', { name: /^Close/ }), check: sheet.getByRole('button', { name: 'Check handover' }), abandon: sheet.getByRole('button', { name: 'Abandon request' }) }
    const dir = 'test-results/aeon-652-wn'
    await mkdir(dir, { recursive: true })
    const initialBox = (await sheet.boundingBox())!
    const initialHeight = initialBox.height
    if (width === 390) { expect(Math.abs(initialBox.width - width)).toBeLessThanOrEqual(.5); expect(Math.abs(initialBox.x)).toBeLessThanOrEqual(.5) }
    await expectStableControls({ controls, scrollAreas: { sheet, body: sheet.locator('.body') }, interactions: [
      { name: 'cancel option', run: async () => { await cancel.click(); await expect(cancel).toHaveAttribute('aria-checked', 'true') } },
      { name: 'split option', run: async () => { await split.click(); await expect(split).toHaveAttribute('aria-checked', 'true') } },
      { name: 'long German child draft', run: async () => { await sheet.getByRole('textbox').fill('Vollständige Überprüfung der Arbeitsaufträge und ihrer nachvollziehbaren Übergabe\nEinrichtung der langfristigen Zuständigkeiten mit ausführlicher Dokumentation'); await expect(submit).toBeEnabled() } },
    ] })
    if (width === 390) expect(Math.abs((await sheet.boundingBox())!.height - initialHeight)).toBeLessThanOrEqual(.5)
    await page.screenshot({ path: `${dir}/split-${width}-${theme}.png` })
    await expectStableControls({ controls, scrollAreas: { sheet, body: sheet.locator('.body') }, interactions: [
      { name: 'request split', run: async () => { await submit.click(); await expect(sheet.getByText(/Waiting for 1 leaf to stop gracefully/)).toBeVisible() } },
      { name: 'confirmed handover', run: async () => { world.stop(); await expect(controls.check).toBeEnabled(); await controls.check.click(); await expect(sheet.getByText('2 children created.')).toBeVisible() } },
    ] })
    expect(world.requests).toHaveLength(1)
    expect(world.requests[0]?.children).toHaveLength(2)
    await controls.close.click()
    await page.reload()
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    // Reopen with a completed action: the saved intent cannot run twice.
    await page.getByRole('button', { name: 'Work actions', exact: true }).click()
    await expect(sheet).toBeVisible()
    await expect(sheet.getByText(/This leaf is busy/)).toBeVisible()
    await controls.close.click()
    // A fresh page/context below uses a new visible leaf preview.
    await page.route('**/api/nodes/*/work-lifecycle', route => route.fulfill({ json: { is_leaf: true, busy: true, open_leaves: 1, updated_at: '2026-09-23T12:00:00Z', scope_revision: 'a'.repeat(64), pending: null } }))
    await page.getByRole('button', { name: 'Work actions', exact: true }).click()
    await expect(sheet.getByText(/This leaf is busy/)).toBeVisible()
    await cancel.click()
    await expect(sheet.getByText(/1 open leaf will be cancelled/)).toBeVisible()
    await page.screenshot({ path: `${dir}/cancel-${width}-${theme}.png` })
  })
}

test('cancel shows parent leaf count, honest permission failure, and saved intent', async ({ page }) => {
  const { sheet, requests, revoke } = await openWork(page, 'light', true)
  await expect(sheet.getByRole('radio', { name: 'Split into children' })).toBeDisabled()
  await sheet.locator('.actions .primary').click()
  await expect(sheet.getByText(/Waiting for 3 leaves to stop gracefully/)).toBeVisible()
  revoke()
  await expect(sheet.getByRole('button', { name: 'Check handover' })).toBeEnabled()
  await sheet.getByRole('button', { name: 'Check handover' }).click()
  await expect(sheet.getByRole('status')).toContainText('Permission was revoked')
  expect(requests).toHaveLength(1)
  await sheet.getByRole('button', { name: /^Close/ }).click()
  await page.getByRole('button', { name: 'Work actions', exact: true }).click()
  await expect(sheet.getByText(/Waiting for 3 leaves/)).toBeVisible()
})

test('keyboard preserves native modifiers and Escape leaves the field before closing', async ({ page }) => {
  const { sheet, requests } = await openWork(page, 'light')
  const field = sheet.getByRole('textbox')
  await field.fill('First\nSecond'); await field.focus()
  await field.press('ControlOrMeta+a')
  expect(requests).toHaveLength(0)
  await field.press('Escape')
  await expect(sheet).toBeVisible(); await expect(field).not.toBeFocused()
  await sheet.press('Escape'); await expect(sheet).toBeHidden()
})
