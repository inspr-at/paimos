// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { controlStability } from './control-stability'
import type { NativeApproval, NativeBinding, NativeSessionView } from '../src/lib/nativeSessionContract'

const binding: NativeBinding = { tenant_id: '10000000-0000-4000-8000-000000000001', project_id: '10000000-0000-4000-8000-000000000002', owned_session_id: '10000000-0000-4000-8000-000000000003', harness_session_id: '10000000-0000-4000-8000-000000000004', runtime_epoch: '10000000-0000-4000-8000-000000000005', daemon_id: 'fixture-daemon', daemon_generation: 'generation-1', binding_revision: 2, run_id: null }
const session: NativeSessionView = { owned_session_id: binding.owned_session_id, current_binding: binding, owner_person_id: 'owner', role: 'coordinator', profile: 'native', harness: 'claude', state: 'requires_action', mode: 'structured', revision: 3, generation: 1, conversation_ref: 'opaque', predecessor_harness_session_id: null, controller: null, quota: null, last_receipt: null, reason: null, terminal_viewer_url: null,
  capabilities: { input: true, steer: 'unsupported', interrupt: true, stop: true, resume: false, take: false, expert: true, terminal_view: false, approvals: true, questions: true, correlated_receipts: true, quota: 'unsupported', qualification_digest: 'a'.repeat(64), take_supported: false, events_supported: true, interrupt_supported: true, chat_supported: true } }
const request = (n: number, summary: string): NativeApproval => ({ approval_id: `20000000-0000-4000-8000-${String(n).padStart(12, '0')}`, request_id: `30000000-0000-4000-8000-${String(n).padStart(12, '0')}`, binding, turn_ref: `turn-${n}`, native_request_ref: `opaque-${n}`, kind: n === 2 ? 'question' : 'tool', effect_digest: 'a'.repeat(64), summary, allowed_decisions: n === 2 ? ['answer', 'deny'] : ['allow_once', 'deny'], expires_at: '2026-10-13T10:00:00Z', expires_in_ms: 300000, desk_item_id: `40000000-0000-4000-8000-${String(n).padStart(12, '0')}`, step_up_required: n === 2 })

// Every test-only byte stays in this assigned spec. The real Vue component is
// loaded through Vite; deferred completions are explicit, including after abort.
async function setup(page: Page, theme = 'light') {
  const component = await page.request.get('/src/components/nativeExpert/NativeRequests.vue')
  expect(component.ok()).toBe(true)
  const vue = (await component.text()).match(/from\s*["']([^"']*\/vue\.js[^"']*)["']/)?.[1]
  expect(vue).toBeTruthy()
  const state = { session, viewer: { tenantId: binding.tenant_id, principalId: 'person-a', kind: 'person', canRead: true, canDecide: true }, requests: [request(1, 'Die freigegebene Datei einmal lesen.'), request(2, 'Welche geprüfte Änderung soll verwendet werden?\n' + 'Nur die angegebene Wirkung für diese Sitzung bestätigen. '.repeat(24))], active: true,
    reviewLabel: 'Anfrage in der Entscheidungsübersicht prüfen' }
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.route('**/tests/native-requests-inline.html', route => route.fulfill({ contentType: 'text/html', body: `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/src/styles/tokens.css"><link rel="stylesheet" href="/src/styles/base.css"><style>body{background:var(--surface);background-image:none;}body::before{display:none;}#app{display:flex;flex-direction:column;max-width:64rem;margin:auto;}@media(max-width:600px){#app{height:100dvh;}}</style></head><body><div id="app"></div><script type="module">
    import { createApp,h,reactive,nextTick } from ${JSON.stringify(vue)};
    import NativeRequests from '/src/components/nativeExpert/NativeRequests.vue';
    const state=reactive(${JSON.stringify(state)}), calls=[], reviewed=[];
    state.requestStartedAt=performance.now();
    document.documentElement.dataset.theme=${JSON.stringify(theme)};
    const review=(request,signal)=>new Promise((resolve,reject)=>calls.push({request,signal,resolve,reject}));
    window.nativeRequests={state,calls,reviewed,nextTick,resolve(index,wrong=false){const c=calls[index]; c.resolve({binding:c.request.binding,approvalId:wrong?'wrong':c.request.approvalId,requestId:c.request.requestId,deskItemId:c.request.deskItemId});}};
    createApp({setup(){return()=>h(NativeRequests,{...state,review,onReviewed:a=>reviewed.push(a)});}}).mount('#app');
  </script></body></html>` }))
  await page.goto('/tests/native-requests-inline.html')
  await expect(page.getByRole('region', { name: 'Session requests' })).toBeVisible()
  return errors
}
const act = (page: Page, code: string) => page.evaluate(async code => { await new Function(`return (async()=>{const f=window.nativeRequests;${code}})()`)() }, code)
const reviewButton = (page: Page) => page.getByRole('button', { name: 'Anfrage in der Entscheidungsübersicht prüfen' })

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  // Risk: changing request content or feedback moves the actions in a series.
  test(`AEON-1304 request series remains stable ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 820 })
    const errors = await setup(page, theme)
    const controls = { previous: page.getByRole('button', { name: 'Previous request' }), next: page.getByRole('button', { name: 'Next request' }), review: reviewButton(page), navigation: page.getByRole('group', { name: 'Request series' }) }
    const stable = await controlStability(page, width === 390 ? { ...controls, frame: page.getByRole('region', { name: 'Session requests' }) } : controls)
    await stable.check(() => controls.next.click())
    await expect(page.getByRole('heading', { name: 'Question' })).toBeVisible()
    await stable.check(() => reviewButton(page).click())
    expect(await page.evaluate(() => (window as any).nativeRequests.calls[0].request)).toMatchObject({ principalId: 'person-a', binding, approvalId: request(2, '').approval_id, requestId: request(2, '').request_id, revision: 3, effectDigest: 'a'.repeat(64) })
    await stable.check(() => act(page, 'f.resolve(0); await f.nextTick();'))
    await expect(page.getByRole('status')).toHaveText('Request opened in Decision Desk.')
    await stable.check(() => controls.previous.click())
    await expect(page.getByRole('heading', { name: 'Permission request' })).toBeVisible()
    stable.done()
    expect(errors).toEqual([])
    await page.screenshot({ path: testInfo.outputPath(`AEON-1304-requests-${width}-${theme}.png`), fullPage: true })
  })
}

// Risk: a late desk opener can display success for the newly selected person,
// callback or runtime. Adapters cannot approve through an agent's message.
test('AEON-1304 discards stale identities and rejects wrong request completions', async ({ page }) => {
  const errors = await setup(page)
  await reviewButton(page).click()
  await act(page, `f.state.viewer.principalId='person-b'; await f.nextTick(); f.resolve(0); await f.nextTick();`)
  await expect(page.getByRole('status')).not.toHaveText('Request opened in Decision Desk.')
  expect(await page.evaluate(() => (window as any).nativeRequests.reviewed.length)).toBe(0)
  expect(await page.evaluate(() => (window as any).nativeRequests.calls[0].signal.aborted)).toBe(true)
  await reviewButton(page).click()
  await act(page, 'f.resolve(1,true); await f.nextTick();')
  await expect(page.getByRole('status')).toHaveText('The request changed. Reopen the current request.')
  await reviewButton(page).click()
  await act(page, `f.state.session.current_binding={...f.state.session.current_binding,runtime_epoch:'replacement'}; await f.nextTick(); f.resolve(2); await f.nextTick();`)
  await expect(page.getByRole('status')).toHaveText('No pending requests for this runtime.')
  expect(await page.evaluate(() => (window as any).nativeRequests.reviewed.length)).toBe(0)
  await act(page, `f.state.session.current_binding=${JSON.stringify(binding)}; f.state.viewer.kind='agent'; await f.nextTick();`)
  await expect(reviewButton(page)).toHaveAttribute('aria-disabled', 'true')
  await reviewButton(page).click({ force: true })
  expect(await page.evaluate(() => (window as any).nativeRequests.calls.length)).toBe(3)
  await expect(page.getByRole('status')).toHaveText('A signed-in person must decide this request.')
  await act(page, `f.state.viewer.kind='person'; f.state.requestStartedAt=performance.now()-300001; await f.nextTick();`)
  await reviewButton(page).click({ force: true })
  await expect(reviewButton(page)).toHaveAttribute('aria-disabled', 'true')
  expect(await page.evaluate(() => (window as any).nativeRequests.calls.length)).toBe(3)
  expect(errors).toEqual([])
})
