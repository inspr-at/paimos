// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { expectStableControls } from './helpers/stable'
import type { KeyTrimProposal } from '../src/lib/keyTrim'

async function mockTrims(page: Page, long = false, paginated = false) {
  await mockDecisionDesk(page)
  const now = Date.now()
  const proposal = (id: string, scopes: number): KeyTrimProposal => ({ id, key_id: `key-${id}`, key_name: `worker-${id}`, owner_id: `agent-${id}`, owner_name: 'Worker owner', previous_scopes: ['nodes.read', ...Array.from({ length: scopes }, (_, i) => `unused.scope${i}`)], snapshot_digest: 'a'.repeat(64), candidate_scopes: ['nodes.read'], candidate_digest: 'b'.repeat(64), evidence: { summary: 'Reviewed ticket workload and retained its read dependency.', observed_at: new Date(now - 86400_000).toISOString(), risks: Array.from({ length: scopes }, (_, i) => ({ scope: `unused.scope${i}`, evidence: 'No recorded use; earlier use remains unknown.', risk_if_dropped: 'Creating and editing tickets can fail without this permission.' })) }, usage: [], created_by: 'coordinator', created_at: new Date(now).toISOString(), expires_at: new Date(now + 3600_000).toISOString(), state: 'pending', revision: 1, applied_at: null, restore_until: null })
  const rows = [proposal('trim-1', long ? 25 : 1), proposal('trim-2', 1)]
  if (paginated) {
    rows[1]!.state = 'applied'; rows[1]!.revision = 2; rows[1]!.restore_until = new Date(now + 86400_000).toISOString()
    for (let i = 99; i >= 0; i--) {
      rows.unshift({ ...proposal(`history-${i}`, 1), state: 'declined', revision: 2 }, proposal(`pending-${i}`, 1))
    }
  }
  const control = { rows, calls: [] as { path: string; body: Record<string, unknown> }[], deny: false }
  await page.route('**/api/key-trim-proposals**', async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname
    if (request.method() === 'GET') {
      const available = rows.filter(row => (row.state === 'pending') === (url.searchParams.get('state') === 'pending'))
      const cursor = url.searchParams.get('cursor'), start = cursor ? available.findIndex(row => row.id === cursor) + 1 : 0
      const items = available.slice(start, start + 100), has_more = available.length > start + items.length
      return route.fulfill({ json: { items, has_more, ...(has_more && { next_cursor: items.at(-1)!.id }) } })
    }
    const body = request.postDataJSON(); control.calls.push({ path, body })
    if (control.deny) return route.fulfill({ status: 409, json: { error: 'A dropped scope was used recently. Prepare a new proposal.' } })
    const row = rows.find(row => row.id === path.split('/')[3])!
    row.revision++; row.state = path.endsWith('/restore') ? 'restored' : body.decision === 'approve' ? 'applied' : 'declined'
    if (row.state === 'applied') { row.applied_at = new Date().toISOString(); row.restore_until = new Date(Date.now() + 30 * 86400_000).toISOString() }
    return route.fulfill({ json: row })
  })
  return control
}
for (const width of [390, 1440]) {
  test(`one-click trim and restore keeps controls stable at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const control = await mockTrims(page, true)
    await page.goto('/decision-desk'); await page.getByTestId('desk-row-k:trim-1').click()
    await expect(page.getByTestId('desk-decide')).toHaveText(/Approve/)
    await expect(page.getByTestId('desk-decide')).toBeEnabled()
    await expect(page.getByTestId('key-trim-evidence')).toContainText('Risk if dropped')
    await expectStableControls({ controls: {
      actions: page.getByTestId('desk-actions').locator('.action-buttons'), primary: page.getByTestId('desk-decide'), secondary: page.getByTestId('desk-skip'), pager: page.getByTestId('desk-pager'), close: page.getByTestId('desk-close'), stamps: page.getByTestId('desk-stamps'), frame: page.getByTestId('desk-frame'),
    }, scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [
      { name: 'approve in one click', run: async () => { await page.getByTestId('desk-decide').click(); await expect.poll(() => control.calls.length).toBe(1); await expect(page.getByRole('heading', { name: 'Trim worker-trim-2?' })).toBeVisible(); await expect(page.getByTestId('desk-decide')).toBeFocused() } },
      { name: 'return to applied memo', run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByTestId('desk-decide')).toHaveText(/Restore/) } },
      { name: 'restore in one click', run: async () => { await page.getByTestId('desk-decide').click(); await expect.poll(() => control.calls.length).toBe(2); await expect(page.getByRole('heading', { name: 'Trim worker-trim-2?' })).toBeVisible() } },
      { name: 'return to restore receipt', run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByTestId('desk-body')).toContainText('Previous scopes restored') } },
    ] })
    expect(control.calls.map(call => call.path)).toEqual(['/api/key-trim-proposals/trim-1/decision', '/api/key-trim-proposals/trim-1/restore'])
    expect(control.calls.map(call => call.body.expected_digest)).toEqual(['a'.repeat(64), 'b'.repeat(64)])
  })
}
test('decline closes in one click and refusal keeps the memo open with an honest error', async ({ page }) => {
  const control = await mockTrims(page)
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-k:trim-1').click()
  control.deny = true
  await page.getByTestId('desk-decide').click(); await expect(page.getByTestId('desk-status')).toContainText('used recently')
  await expect(page.getByRole('heading', { name: 'Trim worker-trim-1?' })).toBeVisible(); expect(control.rows[0]!.state).toBe('pending')
  control.deny = false
  await page.getByTestId('desk-skip').click(); await expect.poll(() => control.calls.length).toBe(2)
  expect(control.calls[1]!.body.decision).toBe('decline'); expect(control.rows[0]!.state).toBe('declined')
})
test('a blocked proposal can still be declined without approving its scopes', async ({ page }) => {
  const control = await mockTrims(page)
  control.rows[0]!.blocked_reason = 'A dropped scope was used in the last 24 hours.'
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-k:trim-1').click()
  await expect(page.getByTestId('desk-decide')).toBeDisabled()
  await expect(page.getByTestId('desk-skip')).toBeEnabled()
  await page.getByTestId('desk-skip').click()
  await expect.poll(() => control.calls.length).toBe(1)
  expect(control.calls[0]!.body.decision).toBe('decline')
  await expect(page.getByRole('heading', { name: 'Trim worker-trim-2?' })).toBeVisible()
})
test('an unconfirmed approval retry uses the same idempotency request', async ({ page }) => {
  const control = await mockTrims(page)
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-k:trim-1').click()
  control.deny = true
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-status')).toContainText('used recently')
  control.deny = false
  await page.getByTestId('desk-decide').click()
  await expect.poll(() => control.calls.length).toBe(2)
  expect(control.calls[1]!.body.request_id).toBe(control.calls[0]!.body.request_id)
  await expect(page.getByRole('heading', { name: 'Trim worker-trim-2?' })).toBeVisible()
})
for (const width of [390, 1440]) {
  test(`approval and restore beyond the first page remain accessible at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const control = await mockTrims(page, false, true)
    await page.goto('/decision-desk')
    const next = page.getByRole('button', { name: 'Next key trims', exact: true })
    const first = page.getByRole('button', { name: 'First key trims', exact: true })
    await expect(page.getByTestId('desk-row-k:trim-1')).toHaveCount(0)
    await expectStableControls({ controls: { next, first }, interactions: [
      { name: 'next open page', run: async () => { await next.click(); await expect(page.getByTestId('desk-row-k:trim-1')).toBeVisible(); await expect(next).toBeDisabled() } },
      { name: 'return to first open page', run: async () => { await first.click(); await expect(page.getByTestId('desk-row-k:pending-0')).toBeVisible(); await expect(first).toBeDisabled() } },
      { name: 'reopen next open page', run: async () => { await next.click(); await expect(page.getByTestId('desk-row-k:trim-1')).toBeVisible() } },
    ] })
    await page.getByTestId('desk-row-k:trim-1').click(); await page.getByTestId('desk-decide').click()
    await expect.poll(() => control.calls.length).toBe(1)
    await page.getByTestId('desk-close').click()
    await page.getByRole('button', { name: /^Decided / }).click()
    await expect(page.getByTestId('desk-row-k:trim-2')).toHaveCount(0)
    await next.click(); await expect(page.getByTestId('desk-row-k:trim-2')).toBeVisible()
    await page.getByTestId('desk-row-k:trim-2').click(); await expect(page.getByTestId('desk-decide')).toHaveText(/Restore/)
    await page.getByTestId('desk-decide').click(); await expect.poll(() => control.calls.length).toBe(2)
    expect(control.calls.map(call => call.path)).toEqual(['/api/key-trim-proposals/trim-1/decision', '/api/key-trim-proposals/trim-2/restore'])
    expect(control.rows.find(row => row.id === 'trim-1')!.state).toBe('applied')
    expect(control.rows.find(row => row.id === 'trim-2')!.state).toBe('restored')
  })
}
