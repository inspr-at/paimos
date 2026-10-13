// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { controlStability } from './control-stability'
import type { NativeBinding, NativeEventPage, NativeSessionView, NativeStreamEvent } from '../src/lib/nativeSessionContract'

const binding: NativeBinding = { tenant_id: 'tenant-1', project_id: 'project-1', owned_session_id: 'owned-a', harness_session_id: 'harness-a', runtime_epoch: 'runtime-a', daemon_id: 'daemon-1', daemon_generation: 'daemon-generation-1', binding_revision: 1, run_id: null }
const session: NativeSessionView = {
  owned_session_id: binding.owned_session_id, current_binding: binding, owner_person_id: 'owner-1', role: 'coordinator', profile: 'native', harness: 'codex',
  state: 'running', mode: 'structured', capabilities: { input: true, steer: 'native', interrupt: true, stop: true, resume: true, take: false, expert: true, terminal_view: true, approvals: true, questions: true, correlated_receipts: true, quota: 'stream', qualification_digest: 'qualified-test', take_supported: false, events_supported: true, interrupt_supported: true, chat_supported: true },
  revision: 1, conversation_ref: 'conversation-opaque', predecessor_harness_session_id: null, controller: null, quota: null, last_receipt: null, reason: null, generation: 1, terminal_viewer_url: 'https://terminal.example.test/session-a',
}
const frame = (seq: number | string, kind: NativeStreamEvent['kind'] = 'expert', payload?: unknown, bound = binding, generation = 1) => ({ binding: bound, sequence: String(seq), observed_at: '2026-10-13T02:03:04Z', dropped_frames: '0', generation, seq: String(seq), source: 'native', kind,
  payload: payload ?? { provider: 'claude', event_name: 'tool_progress', native_thread_ref: 'opaque-thread', native_turn_ref: 'opaque-turn', frame_json: '{"type":"tool_progress","tool_name":"Read","duration_ms":100}', redacted_paths: ['session_id'], omitted: false, original_bytes: 140 },
}) as NativeStreamEvent
const replay = (items: NativeStreamEvent[] = [], next_before: string | null = null, truncated = false, generation = 1): NativeEventPage => ({ generation, items, next_before, truncated })

/** An inline Vite fixture keeps every test-only byte in the sole assigned spec.
 * The component and Vue import share Vite's resolved module (no duplicate Vue runtime).
 * All replay/stream completions are explicit barriers, including callbacks after abort.
 */
async function setup(page: Page, options: { terminal?: boolean; take?: boolean; agent?: boolean; theme?: string; page?: NativeEventPage; hold?: boolean } = {}) {
  const response = await page.request.get('/src/components/nativeExpert/SessionExpert.vue')
  expect(response.ok()).toBe(true)
  const source = await response.text(), vue = source.match(/from\s*["']([^"']*\/vue\.js[^"']*)["']/)?.[1]
  expect(vue, 'the real Vite Vue module').toBeTruthy()
  const view = { ...session, mode: options.terminal ? 'terminal' : 'structured', capabilities: { ...session.capabilities, take_supported: options.take ?? false } }
  const state = { session: view, viewer: { tenantId: binding.tenant_id, principalId: options.agent ? 'agent-1' : 'person-1', kind: options.agent ? 'agent' : 'person', canRead: true, canWatch: !options.agent, canReadStructured: !!options.agent }, consent: { id: 'consent-a', ownerPersonId: session.owner_person_id, binding, active: true }, active: true,
    name: 'Koordinator für die vollständige Freigabeprüfung', host: 'Arbeitsrechner-Wien', modelProfile: { harness: 'codex', model: 'gpt-6.1-sol', display_name: 'GPT Sol', model_version: '6.1' },
  }
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.route('**/tests/native-expert-inline.html', route => route.fulfill({ contentType: 'text/html', body: `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/src/styles/tokens.css"><link rel="stylesheet" href="/src/styles/base.css"><style>body { background:var(--surface); background-image:none; } body::before { display:none; } #app { display:flex; flex-direction:column; width:min(100%,980px); margin:auto; } .fixture-head { flex:none; padding:12px 24px; border-bottom:1px solid var(--line); } .fixture-head h1 { font:600 16px var(--font); } .fixture-head p { font-size:12px; } .terminal-fixture { padding:24px; } .terminal-fixture pre { white-space:pre-wrap; }</style></head><body><div id="app"></div><script type="module">
    import { createApp, h, reactive, nextTick } from ${JSON.stringify(vue)};
    import SessionExpert from '/src/components/nativeExpert/SessionExpert.vue';
    import { liveTerminalLink } from '/src/lib/nativeExpert/ports.ts';
    import { setPreferenceOwner } from '/src/lib/preferences.ts';
    const state = reactive(${JSON.stringify(state)}), reads = [], streams = [];
    let held = ${JSON.stringify(!!options.hold)}, pageResult = ${JSON.stringify(options.page ?? replay())};
    document.documentElement.dataset.theme = ${JSON.stringify(options.theme ?? 'light')};
    setPreferenceOwner({tenant:{id:state.viewer.tenantId},principal:{id:state.viewer.principalId}});
    const transport = {
      events(request,before,limit,signal) { return new Promise((resolve,reject) => { reads.push({request,before,limit,signal,resolve,reject}); if (!held) resolve(pageResult); }); },
      stream(request,receive,signal) { return new Promise((resolve,reject) => { streams.push({request,receive,signal,resolve,reject}); signal.addEventListener('abort',() => resolve(),{once:true}); }); }
    };
    window.nativeExpert = { state, reads, streams, transport, nextTick, setHeld(value) { held=value; }, page(value) { pageResult=value; }, emit(index,event) { streams[index].receive(event); }, resolve(index,page) { reads[index].resolve(page); }, setPerson(id,kind='person') { state.viewer.principalId=id; state.viewer.kind=kind; setPreferenceOwner({tenant:{id:state.viewer.tenantId},principal:{id}}); } };
    createApp({setup() { return () => h('main',{id:'app-frame',style:'display:flex;flex-direction:column;min-height:0;flex:1'},[
      h('header',{class:'fixture-head'},[h('h1','Session · Expert'),h('p','GPT-6.1 Sol')]),
      h(SessionExpert,{...state,transport}, {terminal: () => h('div',{class:'terminal-fixture','data-testid':'pty-slot'},[h('p','Read-only · the person at the terminal has control'),h('pre','Native TUI output')])}),
    ]); } }).mount('#app');
    window.nativeExpert.menuLink = () => liveTerminalLink(state.session,state.viewer);
  </script></body></html>` }))
  await page.route('**/api/preferences/**', route => route.fulfill({ json: { value: { hidden: [] } } }))
  await page.goto('/tests/native-expert-inline.html')
  await expect(page.getByRole('region', { name: 'Expert events · GPT-6.1 Sol' })).toBeVisible()
  return errors
}
const act = (page: Page, code: string) => page.evaluate(async code => { await new Function(`return (async () => { const f = window.nativeExpert; ${code} })()`)() }, code)
const send = (page: Page, event: NativeStreamEvent, index = 0) => page.evaluate(({ event, index }) => (window as any).nativeExpert.emit(index, event), { event, index })
const start = async (page: Page, expected = 1) => {
  await page.getByRole('button', { name: 'Watch live', exact: true }).click()
  await expect.poll(() => page.evaluate(() => (window as any).nativeExpert.streams.length)).toBe(expected)
}
const switchTo = (page: Page, segment: string) => page.getByRole('radio', { name: segment, exact: true }).click()

test('AEON-1270 raw diagnostics stay bound to session, person, consent and tab through late replay and stream callbacks', async ({ page }) => {
  const errors = await setup(page)
  expect(await page.evaluate(() => (window as any).nativeExpert.reads.length)).toBe(0)
  await start(page)
  await send(page, frame(1))
  const row = page.locator('[data-event-row]').first()
  const guard = await controlStability(page, { events: page.getByRole('radio', { name: 'Events', exact: true }), terminal: page.getByRole('radio', { name: 'Terminal', exact: true }), follow: page.getByRole('switch', { name: 'Follow live' }), row })
  await guard.check(async () => { await row.click(); await expect(page.getByLabel('Native envelope')).toContainText('tool_progress') })
  await expect(page.locator('.correlations')).toContainText('opaque-turn')
  await expect(page.locator('.redactions')).toContainText('session_id')
  guard.done()
  await act(page, 'f.state.consent.active = false; await f.nextTick();')
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  expect(await page.evaluate(() => (window as any).nativeExpert.streams[0].signal.aborted)).toBe(true)
  await send(page, frame(2, 'assistant.final', { message_id: 'old-message', input_id: null, inferred: false, text: 'late-private-frame' }))
  await expect(page.getByText('late-private-frame', { exact: false })).toHaveCount(0)
  await act(page, 'f.state.consent.active = true; f.setHeld(true); await f.nextTick();')
  await page.getByRole('button', { name: 'Watch live', exact: true }).click()
  await expect.poll(() => page.evaluate(() => (window as any).nativeExpert.reads.length)).toBe(2)
  const nextBinding = { ...binding, owned_session_id: 'owned-b', harness_session_id: 'harness-b', runtime_epoch: 'runtime-b' }
  await page.evaluate(bound => { const f = (window as any).nativeExpert; f.state.session = { ...f.state.session, owned_session_id: bound.owned_session_id, current_binding: bound }; f.state.consent = { ...f.state.consent, id: 'consent-b', binding: bound } }, nextBinding)
  await page.evaluate(page => (window as any).nativeExpert.resolve(1, page), replay([frame(1, 'assistant.final', { message_id: 'late-replay', text: 'late-replay-private', input_id: null, inferred: false })]))
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  expect(await page.evaluate(() => (window as any).nativeExpert.streams.length)).toBe(1)
  await act(page, 'f.setHeld(false); f.page({generation:1,items:[],next_before:null,truncated:false});')
  await start(page, 2)
  await send(page, frame(1, 'assistant.delta', { message_id: 'b-message', text: 'Current session B' }, nextBinding), 1)
  await expect(page.locator('[data-event-row]')).toHaveCount(1)
  await act(page, 'f.setPerson("person-2"); await f.nextTick();')
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  await send(page, frame(2, 'assistant.delta', { message_id: 'b-message', text: 'old-person-private' }, nextBinding), 1)
  await expect(page.getByText('old-person-private', { exact: false })).toHaveCount(0)
  expect(await page.evaluate(() => (window as any).nativeExpert.streams.length)).toBe(2)
  await start(page, 3)
  await act(page, 'f.state.active = false; await f.nextTick();')
  await send(page, frame(3, 'assistant.delta', { message_id: 'hidden', text: 'hidden-private' }, nextBinding), 2)
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  await act(page, 'f.state.active = true; await f.nextTick();')
  expect(await page.evaluate(() => (window as any).nativeExpert.streams.length)).toBe(3)
  await start(page, 4)
  await act(page, 'Object.defineProperty(document,"hidden",{configurable:true,get:()=>true}); document.dispatchEvent(new Event("visibilitychange")); await f.nextTick();')
  expect(await page.evaluate(() => (window as any).nativeExpert.streams[3].signal.aborted)).toBe(true)
  await send(page, frame(4, 'assistant.delta', { message_id: 'background', text: 'background-private' }, nextBinding), 3)
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  await act(page, 'Object.defineProperty(document,"hidden",{configurable:true,get:()=>false}); document.dispatchEvent(new Event("visibilitychange")); await f.nextTick();')
  expect(await page.evaluate(() => (window as any).nativeExpert.streams.length)).toBe(4)
  expect(errors).toEqual([])
})

test('AEON-1270 coalescing keeps native envelopes, inferred correlations, receipts and omitted bodies honest', async ({ page }) => {
  const errors = await setup(page)
  await start(page)
  await send(page, frame(1, 'assistant.delta', { message_id: 'message-1', text: 'First ' }))
  await send(page, frame(2, 'assistant.delta', { message_id: 'message-1', text: 'delta' }))
  await expect(page.locator('[data-event-row]')).toHaveCount(1)
  await send(page, frame(3, 'assistant.final', { message_id: 'message-1', input_id: 'input-1', inferred: true, text: 'Explicit final' }))
  await send(page, frame(4, 'tool.start', { call_id: 'call-1', name: 'Read', args_summary: 'Approved summary' }))
  await send(page, frame(5, 'tool.result', { call_id: 'call-1', result_summary: 'Finished summary', duration_ms: 120 }))
  await expect(page.locator('[data-event-row]')).toHaveCount(2)
  await page.locator('[data-event-row]').first().click()
  await expect(page.getByLabel('C0 event envelope')).toContainText('assistant.delta')
  await expect(page.getByLabel('C0 event envelope')).toContainText('assistant.final')
  await expect(page.locator('.event-detail')).toContainText('Reply correlation inferred')
  await expect(page.locator('.event-detail')).toContainText('input-1')
  await send(page, frame(6, 'receipt', { request_id: 'request-1', operation_id: 'operation-1', binding, operation: 'input', state: 'uncertain', input_id: 'input-1', turn_ref: 'turn-1', source_sequence: '5', updated_at: '2026-10-13T02:03:04Z', reason: 'input_outcome_unconfirmed' }))
  await expect(page.locator('[data-event-row]').filter({ hasText: 'uncertain' })).toHaveCount(1)
  const omitted = frame(7)
  Object.assign(omitted.payload, { event_name: 'unknown_native_frame', omitted: true, frame_json: '', original_bytes: 900 })
  await send(page, omitted)
  await page.locator('[data-event-row]').filter({ hasText: 'unknown_native_frame' }).click()
  await expect(page.getByText('Native body omitted · 900 original bytes.', { exact: true })).toBeVisible()
  const unsafe = frame(8)
  Object.assign(unsafe.payload, { frame_json: '{"thinking":"synthetic-private-canary"}' })
  await send(page, unsafe)
  await expect(page.getByText('A frame was omitted because its view, shape or size was not permitted.')).toBeVisible()
  await expect(page.getByText('synthetic-private-canary', { exact: false })).toHaveCount(0)
  expect(errors).toEqual([])
})

test('AEON-1270 bounded replay, cursor failures and gaps never masquerade as complete history', async ({ page }) => {
  await setup(page, { page: replay([frame(20), frame(19), frame(18)], '18', true) })
  await start(page)
  await expect(page.getByText('Earlier live frames are unavailable. This replay is incomplete.')).toBeVisible()
  expect(await page.evaluate(() => (window as any).nativeExpert.streams[0].request.lastEventId)).toBe('1:20')
  await send(page, frame(23))
  await expect(page.getByText('2 frames are missing from this live view.')).toBeVisible()
  const many = Array.from({ length: 501 }, (_, index) => frame(index + 24))
  await page.evaluate(events => { const f = (window as any).nativeExpert; for (const event of events) f.emit(0, event) }, many)
  await expect(page.locator('[data-event-row]')).toHaveCount(500)
  await expect(page.locator('.notice').first()).toContainText('left the local buffer')
  const oldest = page.locator('[data-event-row]').first()
  await oldest.scrollIntoViewIfNeeded()
  await expect(page.getByRole('switch', { name: 'Follow live' })).not.toBeChecked()
  const y = (await oldest.boundingBox())!.y
  await act(page, 'f.page({generation:1,items:[],next_before:"1",truncated:false});')
  await page.getByRole('button', { name: 'Load earlier', exact: true }).click()
  await expect(page.locator('.notice').last()).toContainText('earlier page was rejected')
  expect(Math.abs((await oldest.boundingBox())!.y - y)).toBeLessThanOrEqual(.5)
  await page.evaluate(page => (window as any).nativeExpert.page(page), replay([frame(17)], '17'))
  await page.getByRole('button', { name: 'Load earlier', exact: true }).click()
  // A full buffer cannot sacrifice its live end to retain a requested older page.
  await expect(page.locator('[data-event-row="event:1:524"]')).toHaveCount(1)
  await expect(page.locator('[data-event-row="event:1:17"]')).toHaveCount(0)
  await expect(page.locator('[data-event-row]')).toHaveCount(500)
  await send(page, frame(525, 'gap', { reason: 'cursor_expired', dropped_frames: '12', reload_snapshot: true }))
  await expect(page.locator('.notice').last()).toContainText('cursor_expired · 12 frames unavailable')
  expect(await page.evaluate(() => (window as any).nativeExpert.streams[0].signal.aborted)).toBe(true)
  await expect(page.getByRole('button', { name: 'Watch live', exact: true })).toBeVisible()
})

test('AEON-1270 earlier pages keep the read row still; stale revisions and 512 KiB overflow stay explicit', async ({ page }) => {
  await setup(page, { page: replay(Array.from({ length: 25 }, (_, index) => frame(50 - index)), '26') })
  await start(page)
  const scroll = page.getByLabel('Events, oldest first')
  await scroll.evaluate(el => { el.scrollTop = 100 })
  await expect(page.getByRole('switch', { name: 'Follow live' })).not.toBeChecked()
  const read = page.locator('[data-event-row]').nth(5), position = (await read.boundingBox())!.y
  await page.evaluate(page => (window as any).nativeExpert.page(page), replay([frame(25), frame(24)], null))
  await page.getByRole('button', { name: 'Load earlier', exact: true }).click()
  await expect(page.locator('[data-event-row]')).toHaveCount(27)
  // Locate the original row by its stable event ID after prepend.
  const original = page.locator('[data-event-row="event:1:31"]')
  expect(Math.abs((await original.boundingBox())!.y - position)).toBeLessThanOrEqual(.5)
  const large = Array.from({ length: 12 }, (_, index) => frame(51 + index, 'assistant.final', { message_id: `large-${index}`, input_id: null, inferred: false, text: 'x'.repeat(50_000) }))
  await page.evaluate(events => { const f = (window as any).nativeExpert; for (const event of events) f.emit(0, event) }, large)
  await expect(page.locator('.notice').first()).toContainText('left the local buffer')
  expect(await page.locator('[data-event-row]').count()).toBeLessThan(12)
  await page.getByRole('button', { name: 'Stop viewing', exact: true }).click()
  await act(page, 'f.setHeld(true);')
  await page.getByRole('button', { name: 'Watch live', exact: true }).click()
  await act(page, 'f.state.session.revision++; f.resolve(2,{generation:1,items:[],next_before:null,truncated:false}); await f.nextTick();')
  await expect(page.getByText('The session changed while events were loading. Watch live to read the current view.')).toBeVisible()
  expect(await page.evaluate(() => (window as any).nativeExpert.streams.length)).toBe(1)
})

test('AEON-1270 agents get structured Events only and revocation clears all diagnostic content', async ({ page }) => {
  await setup(page, { agent: true })
  await expect(page.getByRole('radiogroup', { name: 'Expert view', exact: true })).toHaveCount(0)
  expect(await page.evaluate(() => (window as any).nativeExpert.menuLink())).toBeNull()
  await start(page)
  expect(await page.evaluate(() => (window as any).nativeExpert.streams[0].request.view)).toBe('structured')
  await send(page, frame(1))
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  await send(page, frame(2, 'terminal', { terminal_epoch: 'runtime-a', sequence: '2', data_base64: 'aGVsbG8=', rows: 24, cols: 80, reset: false }))
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  await send(page, frame(3, 'assistant.final', { message_id: 'safe', input_id: null, inferred: false, text: 'Approved final' }))
  await expect(page.locator('[data-event-row]')).toHaveCount(1)
  await send(page, frame(4, 'end', { reason: 'revoked', reload_snapshot: false }))
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Watch live', exact: true })).toBeDisabled()
  await expect(page.getByText('Viewing ended because access or the owner’s consent was revoked.')).toBeVisible()
})

test('AEON-1270 terminal segment is fixed at opening; PTY plus viewer has one terminal and a head-menu descriptor', async ({ page }) => {
  await setup(page, { terminal: true, take: true })
  await switchTo(page, 'Terminal')
  await expect(page.getByTestId('pty-slot')).toBeVisible()
  await expect(page.getByRole('link', { name: 'Open live terminal', exact: true })).toHaveCount(0)
  expect(await page.evaluate(() => (window as any).nativeExpert.menuLink())).toEqual({ href: session.terminal_viewer_url, label: 'Open live terminal', target: '_blank', rel: 'noopener noreferrer' })
  await act(page, 'f.state.viewer.canWatch = false; await f.nextTick();')
  await expect(page.getByTestId('pty-slot')).toHaveCount(0)
  await expect(page.getByText('Live terminal output requires current watch permission and the owner’s active consent.')).toBeVisible()
  await act(page, 'f.setPerson("agent-2","agent"); f.state.viewer.canReadStructured = true; await f.nextTick();')
  await expect(page.getByRole('radiogroup', { name: 'Expert view', exact: true })).toHaveCount(0)
  await expect(page.getByTestId('pty-slot')).toHaveCount(0)
  expect(await page.evaluate(() => (window as any).nativeExpert.menuLink())).toBeNull()
  await page.reload()
  await act(page, 'f.state.session.terminal_viewer_url = null; f.state.session.mode="structured"; f.state.session.capabilities.take_supported=false; f.state.session.owned_session_id="new-panel"; f.state.session.current_binding.owned_session_id="new-panel"; await f.nextTick();')
  await expect(page.getByRole('radio', { name: 'Terminal', exact: true })).toHaveCount(0)
  await act(page, 'f.state.session.terminal_viewer_url="https://terminal.example.test/late"; f.state.session.capabilities.take_supported=true; await f.nextTick();')
  await expect(page.getByRole('radio', { name: 'Terminal', exact: true })).toHaveCount(0)
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`AEON-1270 r2 A1 controls, long German content and live-terminal availability stay stable ${theme} ${width}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    const errors = await setup(page, { theme })
    const events = page.getByRole('radio', { name: 'Events', exact: true }), terminal = page.getByRole('radio', { name: 'Terminal', exact: true })
    await expect(events).toHaveAttribute('aria-checked', 'true')
    const guard = await controlStability(page, { events, terminal, toolbar: page.getByRole('toolbar', { name: 'Expert view tools' }), frame: page.getByRole('region', { name: 'Expert events · GPT-6.1 Sol' }) })
    await guard.check(() => switchTo(page, 'Terminal'))
    const link = page.getByRole('link', { name: 'Open live terminal', exact: true }), hint = page.locator('.terminal-hint')
    await expect(page.locator('.terminal-description')).toHaveText('Koordinator für die vollständige Freigabeprüfung runs in Zellij on Arbeitsrechner-Wien.')
    await expect(link).toHaveAttribute('target', '_blank')
    await expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    await expect(link.locator('svg')).toHaveCount(2)
    if (width === 390) expect((await link.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    await expect(page.getByRole('switch', { name: 'Follow live' })).toHaveCount(0)
    await expect(page.locator('iframe')).toHaveCount(0)
    const viewerGuard = await controlStability(page, { link, hint, events, terminal })
    await page.screenshot({ path: testInfo.outputPath(`AEON-1270/zellij-${theme}-${width}.png`) })
    await viewerGuard.check(async () => { await act(page, 'f.state.session.terminal_viewer_url=null; await f.nextTick();'); await expect(link).toBeDisabled() })
    await expect(hint).toContainText('The live terminal is no longer available.')
    viewerGuard.done()
    await guard.check(() => switchTo(page, 'Events'))
    await start(page)
    await send(page, frame(1, 'assistant.final', { message_id: 'long-german', input_id: 'input-1', inferred: true, text: 'Die vollständige Freigabeprüfung mit ausführlichem Übergabebericht und unveränderten Eigentumsgrenzen ist abgeschlossen.' }))
    const follow = page.getByRole('switch', { name: 'Follow live' }), row = page.locator('[data-event-row]').first()
    const rowGuard = await controlStability(page, { row, follow, events, terminal })
    await rowGuard.check(() => row.click())
    await expect(page.getByLabel('C0 event envelope')).toContainText('long-german')
    await page.screenshot({ path: testInfo.outputPath(`AEON-1270/events-${theme}-${width}.png`) })
    rowGuard.done()
    if (width === 390) {
      await page.getByRole('button', { name: 'Event types', exact: true }).click()
      const text = page.getByRole('checkbox', { name: 'Text', exact: true }), tools = page.getByRole('checkbox', { name: 'Tools', exact: true })
      const menuGuard = await controlStability(page, { text, tools, group: page.getByRole('dialog', { name: 'Event types', exact: true }) })
      await menuGuard.check(() => tools.uncheck())
      await menuGuard.check(() => text.uncheck())
      menuGuard.done()
      await page.keyboard.press('Escape')
    } else {
      const text = page.getByRole('button', { name: 'Text', exact: true }), tools = page.getByRole('button', { name: 'Tools', exact: true })
      const typesGuard = await controlStability(page, { text, tools, follow, events, terminal })
      await typesGuard.check(() => text.click())
      await typesGuard.check(() => tools.click())
      typesGuard.done()
    }
    guard.done()
    expect(errors).toEqual([])
  })
}
