// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page, type Route, type TestInfo } from '@playwright/test'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { expectStableControls } from './helpers/stable'
import { me } from './work-fixtures'
import type { StepupRequest } from '../src/lib/stepup'

function request(id: string, patch: Partial<StepupRequest> = {}): StepupRequest {
  return { id, requested_by: 'agent-1', permission: 'settings.manage', payload: { kind: 'feature', key: 'workspace-summary', project_id: null, enabled: true, expected_revision: 0 },
    before: { key: 'workspace-summary', project_id: null, override: null, revision: 0 }, after: { key: 'workspace-summary', project_id: null, override: true, revision: 1 },
    before_hash: 'b'.repeat(64), after_hash: 'c'.repeat(64), request_digest: id.padEnd(64, 'd').slice(0, 64), created_at: '2026-10-09T15:00:00Z',
    expires_at: new Date(Date.now() + 600_000).toISOString(), state: 'pending', revision: 1, ...patch }
}
const anna = { decided_by: 'person-anna', decided_by_name: 'Anna', decision: 'approve' as const, method: 'oidc_reauth' as const, auth_time: '2026-10-09T11:21:00Z', applied_at: '2026-10-09T11:21:02Z', decided_at: '2026-10-09T11:21:02Z' }

async function mockStepups(page: Page, options: { passkeys?: boolean; theme?: 'light' | 'dark'; rows?: StepupRequest[] } = {}) {
  const rows = options.rows ?? [request('step-1'), request('step-2', { state: 'applied', revision: 2, ...anna })]
  const desk = await mockDecisionDesk(page, { theme: options.theme, projectedSources: () => rows.filter(row => row.state === 'pending').map(row => ({
    id: row.id, kind: 'stepup', project_id: row.project_id ?? undefined, revision: row.revision, title: `Step-up · ${row.permission}`,
    created_at: row.created_at, expires_at: row.expires_at, held: true, href: `/decision-desk?item=s:${row.id}`, source: `/api/stepup-requests/${row.id}`,
  })) })
  const control = { rows, calls: [] as { path: string; body: Record<string, unknown> }[],
    passkeys: options.passkeys ?? true, settle: undefined as undefined | ((row: StepupRequest, path: string) => void), desk,
    // hold delays the next decision call; ended settles the row elsewhere first and answers 409.
    hold: undefined as undefined | Promise<void>, ended: undefined as undefined | ((row: StepupRequest) => void), handled: [] as Promise<void>[] }
  await page.addInitScript(() => {
    const bytes = (n: number) => new Uint8Array([n]).buffer
    const credential = { id: 'cred', rawId: bytes(1), type: 'public-key', authenticatorAttachment: 'platform', getClientExtensionResults: () => ({}),
      response: { clientDataJSON: bytes(2), authenticatorData: bytes(3), signature: bytes(4), userHandle: null } }
    Object.defineProperty(navigator, 'credentials', { configurable: true, value: { get: async () => credential } })
  })
  await page.route('**/api/me/phone-approvals', route => route.fulfill({ json: { available: true, push_available: false, passkeys: control.passkeys ? [{ id: 'passkey-1', created: '2026-10-01T08:00:00Z' }] : [], subscriptions: [] } }))
  await page.route('**/api/stepup-requests**', async route => {
    const call = route.request(), url = new URL(call.url()), path = url.pathname
    const one = control.rows.find(row => path === `/api/stepup-requests/${row.id}`)
    if (call.method() === 'GET' && one) return route.fulfill({ json: one })
    if (call.method() === 'GET') return route.fulfill({ json: { items: control.rows.filter(row => (row.state === 'pending') === (url.searchParams.get('state') === 'pending')), has_more: false } })
    const body = call.postDataJSON(); control.calls.push({ path, body })
    const row = control.rows.find(row => row.id === path.split('/')[3])!
    expect(body).toMatchObject({ request_digest: row.request_digest, revision: row.revision })
    if (control.hold) { const hold = control.hold; control.hold = undefined; control.handled.push(hold.then(() => answer(route, path, url, row)).catch(() => undefined)); return }
    if (control.ended) { control.ended(row); return route.fulfill({ status: 409, json: { error: 'request ended' } }) }
    return answer(route, path, url, row)
  })
  async function answer(route: Route, path: string, url: URL, row: StepupRequest) {
    if (path.endsWith('/options')) {
      if (control.passkeys) return route.fulfill({ json: { method: 'passkey', challenge_id: 'challenge-1', publicKey: { challenge: 'AQID', rpId: '127.0.0.1', userVerification: 'required' } } })
      // The provider round trip ends at the callback, which applies and redirects back.
      Object.assign(row, { state: 'applied', revision: row.revision + 1, decided_by: me.id, decided_by_name: me.name, decision: 'approve', method: 'oidc_reauth', auth_time: new Date().toISOString(), applied_at: new Date().toISOString() })
      return route.fulfill({ json: { method: 'oidc_reauth', authorize_url: `${url.origin}/decision-desk?needs=s:${row.id}` } })
    }
    if (control.settle) control.settle(row, path)
    else Object.assign(row, { state: path.endsWith('/approve') ? 'applied' : 'declined', revision: row.revision + 1, decided_by: me.id, decided_by_name: me.name, decision: path.endsWith('/approve') ? 'approve' : 'decline',
      ...(path.endsWith('/approve') && { method: 'passkey_platform', applied_at: new Date().toISOString() }) })
    return route.fulfill({ json: row })
  }
  return control
}
// The desk page behind the glass memo carries list counts; only the memo is compared.
async function capture(page: Page, testInfo: TestInfo, name: string) {
  const shot = await page.getByTestId('desk-frame').screenshot({ animations: 'disabled', caret: 'hide', style: '.decision-desk > :not(dialog) { visibility: hidden !important; }', path: testInfo.outputPath(`${name}.png`) })
  await testInfo.attach(name, { body: shot, contentType: 'image/png' })
  return shot
}

for (const width of [390, 1440]) {
  test(`step-up Approve uses the passkey with the desk controls still at ${width}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    const off = { payload: { kind: 'feature', key: 'workspace-summary', project_id: null, enabled: false, expected_revision: 0 }, after: { key: 'workspace-summary', project_id: null, override: false, revision: 1 } }
    const control = await mockStepups(page, { rows: [request('step-1'), request('step-3', off)] })
    await page.goto('/decision-desk'); await page.getByTestId('desk-row-s:step-1').click()
    await expect(page.getByTestId('stepup-before')).toContainText('Inherited')
    await expect(page.getByTestId('stepup-after')).toContainText('On')
    await expect(page.getByTestId('stepup-method')).toHaveText('Passkey')
    await expect(page.getByTestId('desk-skip')).toHaveText('Decline')
    await capture(page, testInfo, `stepup-memo-${width}`)
    await expectStableControls({ controls: {
      actions: page.getByTestId('desk-actions').locator('.action-buttons'), primary: page.getByTestId('desk-decide'), secondary: page.getByTestId('desk-skip'), pager: page.getByTestId('desk-pager'), close: page.getByTestId('desk-close'), stamps: page.getByTestId('desk-stamps'),
    }, scrollAreas: { body: page.getByTestId('desk-body') }, interactions: [
      { name: 'previous memo', run: async () => { await page.getByTestId('desk-paper').press('ArrowLeft'); await expect(page.getByTestId('stepup-change')).toHaveCount(0) } },
      { name: 'back to the step-up', run: async () => { await page.getByTestId('desk-paper').press('ArrowRight'); await expect(page.getByTestId('stepup-method')).toBeVisible() } },
      { name: 'approve with the passkey', run: async () => { await page.getByTestId('desk-paper').press('Enter'); await expect.poll(() => control.calls.length).toBe(2); await expect(page.getByRole('heading', { name: 'Turn off workspace-summary for the workspace?' })).toBeVisible(); await expect(page.getByTestId('desk-decide')).toBeFocused() } },
    ] })
    expect(control.calls.map(call => call.path)).toEqual(['/api/stepup-requests/step-1/options', '/api/stepup-requests/step-1/approve'])
    expect(control.calls[1]!.body).toMatchObject({ challenge_id: 'challenge-1', credential: { id: 'cred' } })
  })
}
test('Decline needs no step-up; the Decided view names who and how, and a first decision elsewhere stays theirs', async ({ page }) => {
  const control = await mockStepups(page, { rows: [request('step-1'), request('step-3'), request('step-2', { state: 'applied', revision: 2, ...anna })] })
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-s:step-1').click()
  await page.getByTestId('desk-skip').click()
  await expect.poll(() => control.calls.map(call => call.path)).toEqual(['/api/stepup-requests/step-1/decline'])
  await expect(page.getByTestId('stepup-change')).toBeVisible()
  control.settle = row => Object.assign(row, { state: 'applied', revision: row.revision + 1, ...anna, method: 'passkey' })
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-status')).toHaveText('Decided by Anna first: Approved · Anna · passkey. Nothing for you to do.')
  await expect(page.getByTestId('stepup-answer')).toHaveText('Approved · Anna · passkey')
  await page.getByTestId('desk-close').click()
  await page.getByRole('button', { name: /^Decided / }).click()
  await expect(page.getByTestId('desk-row-s:step-1')).toContainText('Declined · Markus Barta')
  await expect(page.getByTestId('desk-row-s:step-2')).toContainText(/Approved · Anna · fresh sign-in at /)
  await page.getByTestId('desk-row-s:step-2').click()
  await expect(page.getByTestId('stepup-answer')).toHaveText(/Approved · Anna · fresh sign-in at /)
  await expect(page.getByTestId('desk-decide')).toHaveText(/Next/)
})
test('without a passkey Approve leaves for a fresh sign-in and the return opens the decided memo', async ({ page }) => {
  const control = await mockStepups(page, { passkeys: false })
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-s:step-1').click()
  await expect(page.getByTestId('stepup-method')).toHaveText('Sign in again')
  await page.getByTestId('desk-decide').click()
  await page.waitForURL(/needs=s:step-1/)
  await expect(page.getByTestId('stepup-answer')).toHaveText(/Approved · Markus Barta · fresh sign-in at /)
  await expect(page.getByRole('button', { name: /^Decided / })).toHaveAttribute('aria-pressed', 'true')
  expect(control.calls.map(call => call.path)).toEqual(['/api/stepup-requests/step-1/options'])
})
test('closing the memo while Approve waits cancels the step-up, so a late answer starts no sign-in', async ({ page }) => {
  const control = await mockStepups(page, { passkeys: false })
  let release!: () => void
  control.hold = new Promise(resolve => { release = resolve })
  // The fetch's own timeout aborts too; only the memo's close carries AbortError.
  await page.addInitScript(() => {
    const native = window.fetch.bind(window)
    window.fetch = (input, init) => {
      if (String(input).endsWith('/options')) init?.signal?.addEventListener('abort', () => { (window as unknown as { optionsAbort: string }).optionsAbort = (init.signal!.reason as DOMException).name }, { once: true })
      return native(input, init)
    }
  })
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-s:step-1').click()
  const asked = page.waitForRequest(request => request.url().endsWith('/api/stepup-requests/step-1/options'))
  await page.getByTestId('desk-decide').click(); await asked
  await page.getByTestId('desk-close').click()
  await expect.poll(() => page.evaluate(() => (window as unknown as { optionsAbort?: string }).optionsAbort)).toBe('AbortError')
  // The held answer arrives late; the page must stay on the desk.
  release(); await Promise.all(control.handled)
  await expect(page.getByTestId('desk-frame')).toHaveCount(0)
  expect(new URL(page.url()).search).toBe('')
  expect(control.calls.map(call => call.path)).toEqual(['/api/stepup-requests/step-1/options'])
})
test('Approve on a request someone else settled first shows their outcome, not the pending memo', async ({ page }) => {
  const control = await mockStepups(page)
  await page.goto('/decision-desk'); await page.getByTestId('desk-row-s:step-1').click()
  control.ended = row => Object.assign(row, { state: 'applied', revision: row.revision + 1, ...anna, method: 'passkey' })
  await page.getByTestId('desk-decide').click()
  await expect(page.getByTestId('desk-status')).toHaveText('Decided by Anna first: Approved · Anna · passkey. Nothing for you to do.')
  await expect(page.getByTestId('stepup-answer')).toHaveText('Approved · Anna · passkey')
  await expect(page.getByTestId('desk-decide')).toHaveText(/Next/)
  expect(control.calls.map(call => call.path)).toEqual(['/api/stepup-requests/step-1/options'])
})
for (const theme of ['light', 'dark'] as const) {
  test(`the step-up memo uses the desk tokens in ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 1024, height: 900 })
    await mockStepups(page, { theme })
    await page.goto('/decision-desk'); await page.getByTestId('desk-row-s:step-1').click()
    await expect(page.getByTestId('stepup-change')).toBeVisible()
    const styles = await page.getByTestId('desk-frame').evaluate(frame => {
      const probe = (property: string, value: string) => { const el = document.createElement('i'); el.style.setProperty(property, value); frame.appendChild(el); const out = getComputedStyle(el).getPropertyValue(property); el.remove(); return out }
      const css = (selector: string, property: string) => getComputedStyle(frame.querySelector(selector)!).getPropertyValue(property)
      return {
        heading: [css('[data-testid="stepup-before"] h3', 'color'), css('.context-column h3', 'color'), probe('color', 'var(--ink-3)')],
        hairline: [css('[data-testid="stepup-before"] dl > div', 'border-bottom-color'), probe('border-bottom-color', 'var(--line)')],
        changed: [css('[data-testid="stepup-after"] .changed', 'background-color'), probe('background-color', 'var(--row-hover)')],
        primary: [css('[data-testid="desk-decide"]', 'background-color'), probe('background-color', 'var(--primary)')],
        type: [css('[data-testid="stepup-after"] dd', 'font-family'), css('.context-column p', 'font-family')],
      }
    })
    for (const [name, values] of Object.entries(styles)) expect(new Set(values).size, `${name}: ${values.join(' | ')}`).toBe(1)
    for (const width of [1024, 390, 1440]) {
      await page.setViewportSize({ width, height: 900 })
      await expect(page.getByTestId('stepup-change')).toBeVisible()
      await capture(page, testInfo, `stepup-memo-${width}-${theme}`)
    }
  })
}
for (const [width, theme] of [[390, 'light'], [1440, 'light'], [1440, 'dark']] as const) {
  test(`an approval memo is pixel-identical with and without step-up requests at ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    // Decided step-ups only, so the open round and its pager count stay the same.
    const control = await mockStepups(page, { rows: [], theme })
    await page.goto('/decision-desk'); await page.getByTestId('desk-row-a:approval-1').click()
    await expect(page.getByRole('heading', { name: 'Allow nodes.read?' })).toBeVisible()
    const without = await capture(page, testInfo, `approval-memo-${width}-${theme}`)
    control.rows.push(request('step-2', { state: 'applied', revision: 2, ...anna }), request('step-4', { state: 'declined', revision: 2, ...anna, decision: 'decline' }))
    await page.reload(); await page.getByTestId('desk-row-a:approval-1').click()
    await expect(page.getByRole('heading', { name: 'Allow nodes.read?' })).toBeVisible()
    await expect(page.getByRole('button', { name: /^Decided 2/ })).toBeAttached()
    const withStepups = await capture(page, testInfo, `approval-memo-with-stepups-${width}-${theme}`)
    expect(withStepups.equals(without), 'approval memo pixels changed').toBe(true)
  })
}
