// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { controlStability } from './control-stability'
import type { NativeBinding, NativeEventPage, NativeQuota, NativeSessionView, NativeStreamEvent } from '../src/lib/nativeSessionContract'

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
async function setup(page: Page, options: { panel?: boolean; terminal?: boolean; take?: boolean; agent?: boolean; theme?: string; page?: NativeEventPage; hold?: boolean; session?: NativeSessionView } = {}) {
  const response = await page.request.get('/src/components/nativeExpert/SessionExpert.vue')
  expect(response.ok()).toBe(true)
  const source = await response.text(), vue = source.match(/from\s*["']([^"']*\/vue\.js[^"']*)["']/)?.[1]
  expect(vue, 'the real Vite Vue module').toBeTruthy()
  const base = options.session ?? session
  const view = { ...base, mode: options.terminal ? 'terminal' : 'structured', capabilities: { ...base.capabilities, take_supported: options.take ?? false } }
  const state = { session: view, viewer: { tenantId: binding.tenant_id, principalId: options.agent ? 'agent-1' : 'person-1', kind: options.agent ? 'agent' : 'person', canRead: true, canWatch: !options.agent, canReadStructured: !!options.agent }, consent: { id: 'consent-a', ownerPersonId: base.owner_person_id, binding: base.current_binding, active: true }, active: true,
    name: 'Koordinator für die vollständige Freigabeprüfung', host: 'Arbeitsrechner-Wien', modelProfile: { harness: 'codex', model: 'gpt-6.1-sol', display_name: 'GPT Sol', model_version: '6.1' },
  }
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.route('**/tests/native-expert-inline.html', route => route.fulfill({ contentType: 'text/html', body: `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/src/styles/tokens.css"><link rel="stylesheet" href="/src/styles/base.css"><style>body { background:var(--surface); background-image:none; } body::before { display:none; } #app { display:flex; flex-direction:column; width:min(100%,980px); margin:auto; } .fixture-head { flex:none; padding:12px 24px; border-bottom:1px solid var(--line); } .fixture-head h1 { font:600 16px var(--font); } .fixture-head p { font-size:12px; } .terminal-fixture { padding:24px; } .terminal-fixture pre { white-space:pre-wrap; }</style></head><body><div id="app"></div><script type="module">
    import { createApp, h, reactive, shallowRef, nextTick } from ${JSON.stringify(vue)};
    import SessionExpert from '/src/components/nativeExpert/SessionExpert.vue';
    import { liveTerminalLink } from '/src/lib/nativeExpert/ports.ts';
    import { setPreferenceOwner } from '/src/lib/preferences.ts';
    const state = reactive(${JSON.stringify(state)}), reads = [], streams = [], reviews = [], mountedView = shallowRef(SessionExpert);
    let held = ${JSON.stringify(!!options.hold)}, pageResult = ${JSON.stringify(options.page ?? replay())};
    document.documentElement.dataset.theme = ${JSON.stringify(options.theme ?? 'light')};
    setPreferenceOwner({tenant:{id:state.viewer.tenantId},principal:{id:state.viewer.principalId}});
    const transport = {
      events(request,before,limit,signal) { return new Promise((resolve,reject) => { reads.push({request,before,limit,signal,resolve,reject}); if (!held) resolve(pageResult); }); },
      stream(request,receive,signal) { return new Promise((resolve,reject) => { streams.push({request,receive,signal,resolve,reject}); signal.addEventListener('abort',() => resolve(),{once:true}); }); }
    };
    window.nativeExpert = { state, reads, streams, reviews, transport, nextTick, async mountPane(pane) { const views = await import('/src/lib/nativeExpert/views.ts'); mountedView.value = pane === 'events' ? views.SessionEvents : views.SessionTerminal; }, setHeld(value) { held=value; }, page(value) { pageResult=value; }, emit(index,event) { streams[index].receive(event); }, resolve(index,page) { reads[index].resolve(page); }, setPerson(id,kind='person') { state.viewer.principalId=id; state.viewer.kind=kind; setPreferenceOwner({tenant:{id:state.viewer.tenantId},principal:{id}}); } };
    createApp({setup() { return () => h('main',{id:'app-frame',style:'display:flex;flex-direction:column;min-height:0;flex:1'},[
      h('header',{class:'fixture-head'},[h('h1','Session'),h('p','GPT-6.1 Sol')]),
      h(mountedView.value,{...state,transport,...${JSON.stringify(options.panel ? {} : { standalone: true })},onReview: approval => reviews.push(approval)}, {terminal: slotProps => { window.nativeExpert.terminalProps = slotProps; return h('div',{class:'terminal-fixture','data-testid':'pty-slot'},[h('p','Read-only · the person at the terminal has control'),h('p',{'data-testid':'pty-model'},slotProps['model-name']),h('pre','Native TUI output')]); }}),
    ]); } }).mount('#app');
    window.nativeExpert.menuLink = () => liveTerminalLink(state.session,state.viewer);
  </script></body></html>` }))
  await page.route('**/api/preferences/**', route => route.fulfill({ json: { value: { hidden: [] } } }))
  await page.goto('/tests/native-expert-inline.html')
  await expect(page.locator('.native-expert')).toBeVisible()
  return errors
}
const act = (page: Page, code: string) => page.evaluate(async code => { await new Function(`return (async () => { const f = window.nativeExpert; ${code} })()`)() }, code)
const send = (page: Page, event: NativeStreamEvent, index = 0) => page.evaluate(({ event, index }) => (window as any).nativeExpert.emit(index, event), { event, index })
const start = async (page: Page, expected = 1) => {
  await page.getByRole('button', { name: 'Watch live', exact: true }).click()
  await expect.poll(() => page.evaluate(() => (window as any).nativeExpert.streams.length)).toBe(expected)
}
const switchTo = (page: Page, segment: string) => page.getByRole('radio', { name: segment, exact: true }).click()

// Risk: Vue camelizes slot attributes, but a terminal consumer also needs the
// documented model-name key, with the current label and exact authority props.
async function terminalModel(page: Page, name: string) {
  await expect(page.getByTestId('pty-model')).toHaveText(name)
  expect(await page.evaluate(() => {
    const f = (window as any).nativeExpert, props = f.terminalProps
    return { names: [props['model-name'], props.modelName], current: [props.session === f.state.session, props.viewer === f.state.viewer, props.consent === f.state.consent] }
  })).toEqual({ names: [name, name], current: [true, true, true] })
}

// Risk: A3 removes the old name from accessible labels as well as visible copy.
// Inspect before native envelopes render: their C0 field names remain unchanged.
async function sessionCopy(page: Page) {
  const copy = await page.locator('.native-expert').evaluate(el => [
    el.getAttribute('aria-label'), (el as HTMLElement).innerText,
    ...Array.from(el.querySelectorAll('[aria-label], [title]')).flatMap(node => [node.getAttribute('aria-label'), node.getAttribute('title')]),
  ].join('\n'))
  expect(copy, 'session UI copy and accessible names follow A3').not.toMatch(/\bexpert\b/i)
}

// C0 fixtures only: a new generation has its own runtime binding. The product
// receives these through exactly the same bounded replay/stream ports as real data.
const mixedBinding = { ...binding, harness_session_id: 'harness-mixed', runtime_epoch: 'runtime-mixed', binding_revision: 2 }
const mixedSession = { ...session, current_binding: mixedBinding, generation: 2 }
const mixedFrame = (seq: number, kind: NativeStreamEvent['kind'], payload: unknown) => frame(seq, kind, payload, mixedBinding, 2)
const textFrame = (seq: number, text: string) => mixedFrame(seq, 'assistant.final', { message_id: `mixed-${seq}`, input_id: null, inferred: false, text })
const stateFrame = (seq: number, state: string) => mixedFrame(seq, 'structured', { kind: 'turn_state', text: null, reference: null, state })
function receiptFrame(seq: number, operation: 'start' | 'resume' | 'interrupt') {
  return mixedFrame(seq, 'receipt', { request_id: `request-${seq}`, operation_id: `operation-${seq}`, binding: mixedBinding, operation, state: 'applied', input_id: null, turn_ref: null, source_sequence: String(seq), updated_at: '2026-10-13T02:03:04Z', reason: null })
}
function approvalFrame(seq: number, kind: 'command' | 'question') {
  return mixedFrame(seq, 'approval', { request_id: `ask-${seq}`, binding: mixedBinding, turn_ref: 'turn-mixed', native_request_ref: `native-${seq}`, kind, effect_digest: 'fixture-effect', summary: kind === 'question' ? 'Which verification should run next?' : 'Freigabe für die vollständige Prüfung des Übergabeberichts und der Eigentumsgrenzen', allowed_decisions: kind === 'question' ? ['answer'] : ['allow_once', 'deny'], expires_at: '2026-10-13T02:33:04Z', desk_item_id: kind === 'command' ? 'desk-mixed' : null, step_up_required: false, approval_id: `approval-${seq}`, expires_in_ms: 1800000 })
}
const mixedEvents = [
  receiptFrame(10, 'resume'), stateFrame(11, 'running'),
  textFrame(12, 'Checking the owned session, its current permissions, and the complete event stream before the next step.'),
  mixedFrame(13, 'tool.start', { call_id: 'mixed-read', name: 'Read', args_summary: 'Read the verification report' }),
  mixedFrame(14, 'tool.result', { call_id: 'mixed-read', result_summary: 'Report checked', duration_ms: 400 }),
  mixedFrame(15, 'quota', { account_id: 'account-mixed', profile_binding_revision: 2, source: 'codex_stream', scope: 'context', state: 'known', used_percent: 23, observed_at: '2026-10-13T02:03:04Z', fresh_until: '2026-10-13T02:13:04Z', resets_at: null }),
  stateFrame(16, 'idle'), textFrame(17, 'The report preserves the current owner and permission boundaries.'),
  mixedFrame(18, 'tool.start', { call_id: 'mixed-bash', name: 'Bash', args_summary: 'Run the targeted verification' }),
  approvalFrame(19, 'command'), mixedFrame(20, 'input.delivered', { input_id: 'input-mixed', from: { id: 'person-1', kind: 'person' }, level: 'next_step', text: 'Keep the current ownership boundaries.' }),
  stateFrame(21, 'requires_action'), textFrame(22, 'Die vollständige Freigabeprüfung mit ausführlichem Übergabebericht und unveränderten Eigentumsgrenzen ist abgeschlossen.'),
  receiptFrame(23, 'start'), approvalFrame(24, 'question'), receiptFrame(25, 'interrupt'),
]

// Risk: account throttling and stale readings must not masquerade as context
// exhaustion; a known context quota keeps Tick's r2 metadata wording.
const quotaCases: { scope: string; state: NativeQuota['state']; percent: number | null; text: string; measure: string }[] = [
  { scope: 'context', state: 'known', percent: 23, text: 'Context usage', measure: '23 % of context' },
  { scope: 'account', state: 'known', percent: 62, text: 'Account usage', measure: '62 %' },
  { scope: 'account', state: 'limited', percent: 100, text: 'Account usage · limited', measure: '100 %' },
  { scope: 'context', state: 'limited', percent: 100, text: 'Context usage · limited', measure: '100 % of context' },
  { scope: 'account', state: 'stale', percent: 62, text: 'Account usage · stale', measure: '62 %' },
  { scope: 'context', state: 'stale', percent: 23, text: 'Context usage · stale', measure: '23 % of context' },
  { scope: 'account', state: 'unknown', percent: null, text: 'Account usage · unknown', measure: '' },
  { scope: 'context', state: 'unsupported', percent: null, text: 'Context usage · unsupported', measure: '' },
]
const quotaEvents = quotaCases.map(({ scope, state, percent }, i) => frame(i + 1, 'quota', {
  account_id: 'account-quota', profile_binding_revision: 1, source: 'codex_stream', scope, state, used_percent: percent,
  observed_at: '2026-10-13T02:03:04Z', fresh_until: '2026-10-13T02:13:04Z', resets_at: null,
}))

async function quotaEvidence(page: Page, theme: string, width: number, shot: (name: string) => Promise<unknown>) {
  await page.setViewportSize({ width, height: 900 })
  const errors = await setup(page, { theme, page: replay([...quotaEvents].reverse()) })
  await start(page)
  await expect(page.locator('[data-event-row]')).toHaveCount(quotaCases.length)
  for (const [i, quota] of quotaCases.entries()) {
    const row = page.locator(`[data-event-row="event:1:${i + 1}"]`)
    await expect(row.locator('strong')).toHaveText('Usage')
    await expect(row.locator('.event-summary > span')).toHaveText(quota.text)
    await expect(row.locator('.event-measure')).toHaveText(quota.measure)
    if (width > 560 && quota.measure) expect(await row.locator('.event-measure').evaluate(el => el.scrollWidth <= el.clientWidth), 'quota metadata is never truncated').toBe(true)
  }
  await shot('usage')
  const row = page.locator('[data-event-row="event:1:3"]')
  const guard = await controlStability(page, { row, follow: page.getByRole('switch', { name: 'Follow live' }), watch: page.getByRole('button', { name: 'Stop watching', exact: true }), events: page.getByRole('radio', { name: 'Events', exact: true }) })
  await guard.check(() => row.click())
  await expect(page.locator('.event-detail .event-text')).toHaveText('Account usage · limited')
  await expect(page.locator('.event-detail > .faint')).toHaveText('100 %')
  const envelope = page.getByLabel('C0 event envelope')
  await expect(envelope).toContainText('"scope": "account"')
  await expect(envelope).toContainText('"state": "limited"')
  await expect(envelope).toContainText('"used_percent": 100')
  await shot('usage-detail')
  await guard.check(() => row.click())
  guard.done()
  expect(errors).toEqual([])
}

async function mixedEvidence(page: Page, theme: string, width: number, shot: (name: string) => Promise<unknown>) {
  const errors = await setup(page, { theme, session: mixedSession, page: replay([...mixedEvents].reverse(), '10', false, 2) })
  const watch = page.getByRole('button', { name: 'Watch live', exact: true }), earlier = page.getByRole('button', { name: 'Load earlier', exact: true })
  const watchAction = page.locator('.replay-controls > button:last-child')
  const events = page.getByRole('radio', { name: 'Events', exact: true }), terminal = page.getByRole('radio', { name: 'Terminal', exact: true })
  const follow = page.getByRole('switch', { name: 'Follow live' }), scroll = page.getByLabel('Events, oldest first'), tail = page.locator('.event-tail')
  await expect(tail).toHaveText("Watch live to see this session's events as they happen.")
  await shot('pre-watch')
  const consentGuard = await controlStability(page, { watch: watchAction, earlier, events, terminal, follow })
  await consentGuard.check(async () => { await act(page, 'f.state.consent.active=false; await f.nextTick();'); await expect(watch).toBeDisabled() })
  await expect(tail).toHaveText("Live events need watch rights and the owner's consent.")
  await shot('lock')
  await consentGuard.check(async () => { await act(page, 'f.state.consent.active=true; await f.nextTick();'); await expect(watch).toBeEnabled() })
  await consentGuard.check(() => start(page))
  consentGuard.done()
  await expect(page.locator('[data-event-row]')).toHaveCount(15)
  await expect(page.locator('[data-event-row] strong')).toHaveText(['Resumed · generation 2', 'Running', 'Codex', 'Read', 'Usage', 'Idle', 'Codex', 'Bash', 'Approval', 'Delivered', 'Needs you', 'Codex', 'Started', 'Question', 'Interrupted'])
  await expect(page.locator('[data-event-row="call:2:mixed-read"] .event-measure')).toHaveText('400 ms')
  await expect(page.locator('[data-event-row="event:2:15"] .event-measure')).toHaveText('23 % of context')
  if (width > 560) {
    for (const meta of await page.locator('.event-measure').all()) {
      expect(await meta.evaluate(el => el.scrollWidth <= el.clientWidth), 'row metadata is never truncated').toBe(true)
    }
  }
  await expect(page.getByRole('button', { name: 'Review', exact: true })).toBeVisible()
  await expect(page.locator('[data-event-row="event:2:19"]').locator('..')).toHaveCSS('box-shadow', /inset/)
  await expect(page.getByRole('button', { name: /^Jump to live/ })).toHaveCount(0)
  await shot('mixed')
  await page.setViewportSize({ width, height: 600 })
  // Wait for the resized scroll area, then establish the live-end baseline
  // through the real scroll handler before testing the upward scroll.
  await expect.poll(() => scroll.evaluate(el => el.getBoundingClientRect().bottom <= innerHeight && el.scrollHeight > el.clientHeight)).toBe(true)
  await scroll.evaluate(el => { el.scrollTop = el.scrollHeight; el.dispatchEvent(new Event('scroll')) })
  await expect(follow).toBeChecked()
  await expect(tail).toBeInViewport()
  await scroll.evaluate(el => { el.scrollTop = 0; el.dispatchEvent(new Event('scroll')) })
  await expect(follow).not.toBeChecked()
  await expect(tail).not.toBeInViewport()
  await expect(page.getByRole('button', { name: /^Jump to live/ })).toHaveCount(0)
  const read = page.locator('[data-event-row="event:2:10"]'), stopWatching = page.getByRole('button', { name: 'Stop watching', exact: true })
  const readGuard = await controlStability(page, { read, earlier, stopWatching, events, terminal, follow })
  await readGuard.check(() => page.evaluate(items => { const f = (window as any).nativeExpert; for (const event of items) f.emit(0, event) }, [textFrame(26, 'The current checks passed.'), textFrame(27, 'The stream stays bound to this generation.'), textFrame(28, 'The next action is ready for review.')]))
  const jump = page.getByRole('button', { name: 'Jump to live · 3 new', exact: true })
  await expect(jump).toBeVisible()
  await expect(tail).not.toBeInViewport()
  await shot('jump')
  readGuard.done()
  const position = (await read.boundingBox())!.y
  await page.evaluate(result => (window as any).nativeExpert.page(result), replay([stateFrame(9, 'idle'), textFrame(8, 'Earlier verification completed.')], '8', false, 2))
  const replayGuard = await controlStability(page, { earlier, stopWatching, events, terminal, follow })
  await replayGuard.check(() => earlier.click())
  await expect(page.locator('[data-event-row]')).toHaveCount(20)
  expect(Math.abs((await read.boundingBox())!.y - position), 'replay preserves the row being read').toBeLessThanOrEqual(.5)
  await expect(jump).toBeVisible() // The two replay events are not new live arrivals.
  await shot('earlier')
  replayGuard.done()
  await send(page, mixedFrame(29, 'gap', { reason: 'cursor_expired', dropped_frames: '3', reload_snapshot: false }))
  await scroll.evaluate(el => { el.scrollTop = 0 })
  await expect(page.locator('.notice')).toHaveText('3 events are missing from this live view.')
  await expect(page.locator('.notice')).toBeVisible()
  await expect(page.locator('.notice')).not.toContainText('cursor_expired')
  await shot('gap')
  await page.getByRole('button', { name: 'Jump to live · 4 new', exact: true }).click()
  await expect(tail).toBeInViewport()
  await expect(follow).toBeChecked()
  await expect(page.getByRole('button', { name: /^Jump to live/ })).toHaveCount(0)
  const stopGuard = await controlStability(page, { earlier, watch: watchAction, events, terminal, follow })
  await stopGuard.check(() => stopWatching.click())
  await expect(tail).toHaveText('Stopped watching. Watch live to continue.')
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  stopGuard.done()
  expect(errors).toEqual([])
}

async function panelEvidence(page: Page, theme: string, width: number, shot: (name: string) => Promise<unknown>) {
  const errors = await setup(page, { panel: true, theme, session: mixedSession, page: replay([...mixedEvents].reverse(), '10', false, 2) })
  await act(page, 'await f.mountPane("events"); await f.nextTick();')
  await expect(page.getByRole('radio')).toHaveCount(0)
  await sessionCopy(page)
  await start(page)
  await expect(page.locator('[data-event-row]')).toHaveCount(15)
  await expect(page.locator('[data-event-row="event:2:15"] .event-measure')).toHaveText('23 % of context')
  const row = page.locator('[data-event-row="message:2:mixed-12"]')
  const guard = await controlStability(page, { row, follow: page.getByRole('switch', { name: 'Follow live' }), watch: page.getByRole('button', { name: 'Stop watching', exact: true }), earlier: page.getByRole('button', { name: 'Load earlier', exact: true }) })
  await guard.check(() => row.click())
  await expect(page.getByLabel('C0 event envelope')).toContainText('mixed-12')
  await shot('panel-events')
  await guard.check(() => row.click())
  guard.done()
  if (width !== 390) await page.locator('#app').evaluate(el => { el.style.width = 'min(100%, 720px)' })
  const trigger = page.getByRole('button', { name: 'Event types', exact: true })
  await trigger.click()
  const group = page.getByRole('dialog', { name: 'Event types', exact: true }), done = group.getByRole('button', { name: 'Done', exact: true })
  const text = group.getByRole('checkbox', { name: 'Text', exact: true }), tools = group.getByRole('checkbox', { name: 'Tools', exact: true })
  const menuGuard = await controlStability(page, { text, tools, done, group })
  await menuGuard.check(() => tools.uncheck())
  await shot('panel-types')
  await menuGuard.check(() => tools.check())
  menuGuard.done()
  await done.click()
  await expect(trigger).toBeFocused()
  if (width !== 390) await page.locator('#app').evaluate(el => { el.style.width = '' })
  await act(page, 'await f.mountPane("terminal"); await f.nextTick();')
  await expect(page.getByRole('radio')).toHaveCount(0)
  await expect(page.getByRole('toolbar')).toHaveCount(0)
  await sessionCopy(page)
  const link = page.getByRole('link', { name: 'Open live terminal', exact: true }), hint = page.locator('.terminal-hint')
  await expect(link).toBeVisible()
  await expect(link).toHaveAttribute('target', '_blank')
  await expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  if (width === 390) expect((await link.boundingBox())!.height).toBeGreaterThanOrEqual(44)
  const terminalGuard = await controlStability(page, { link, hint })
  await shot('panel-terminal')
  await terminalGuard.check(async () => { await act(page, 'f.state.session.terminal_viewer_url=null; await f.nextTick();'); await expect(link).toBeDisabled() })
  terminalGuard.done()
  expect(errors).toEqual([])
}

// Risk: capability withdrawal must end either authorized stream view, including
// retained diagnostics and callbacks the transport delivers after abort.
async function withdrawEvents(page: Page) {
  const captures = await page.evaluate(() => { const f = (window as any).nativeExpert; return { reads: f.reads.length, streams: f.streams.length } })
  expect(await page.locator('[data-event-row]').count()).toBeGreaterThan(0)
  expect(await page.evaluate(() => (window as any).nativeExpert.streams.at(-1).signal.aborted)).toBe(false)
  await act(page, 'f.state.session.capabilities.events_supported = false; await f.nextTick();')
  expect(await page.evaluate(() => (window as any).nativeExpert.streams.at(-1).signal.aborted)).toBe(true)
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  await expect(page.locator('.event-detail')).toHaveCount(0)
  const restart = page.getByRole('button', { name: 'Watch live', exact: true })
  await expect(restart).toBeDisabled()
  await expect(page.getByText('Live events are not available for this session.', { exact: true })).toBeVisible()
  const current = await page.evaluate(() => { const s = (window as any).nativeExpert.state.session; return { binding: s.current_binding, generation: s.generation } })
  await send(page, frame(900, 'assistant.final', { message_id: 'withdrawn', input_id: null, inferred: false, text: 'withdrawn-capability-late-frame' }, current.binding, current.generation), captures.streams - 1)
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  await expect(page.getByText('withdrawn-capability-late-frame', { exact: false })).toHaveCount(0)
  await restart.dispatchEvent('click')
  expect(await page.evaluate(() => { const f = (window as any).nativeExpert; return { reads: f.reads.length, streams: f.streams.length } })).toEqual(captures)
  await act(page, 'f.state.session.capabilities.events_supported = true; await f.nextTick();')
  await expect(restart).toBeEnabled()
  // Restoring the capability requires another explicit Watch live action.
  expect(await page.evaluate(() => { const f = (window as any).nativeExpert; return { reads: f.reads.length, streams: f.streams.length } })).toEqual(captures)
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
}

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
  // Consent withdrawal must begin with retained, expanded diagnostics and a live
  // signal. A previous capability withdrawal would make these checks vacuous.
  await expect(page.locator('[data-event-row]')).toHaveCount(1)
  await expect(page.locator('.event-detail')).toHaveCount(1)
  expect(await page.evaluate(() => (window as any).nativeExpert.streams[0].signal.aborted)).toBe(false)
  await act(page, 'f.state.consent.active = false; await f.nextTick();')
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  await expect(page.locator('.event-detail')).toHaveCount(0)
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
  await expect(page.locator('.event-tail')).toHaveText('Stopped watching while Events is hidden.')
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
  await start(page, 5)
  await send(page, frame(5, 'assistant.final', { message_id: 'current', input_id: null, inferred: false, text: 'Current diagnostics before capability withdrawal' }, nextBinding), 4)
  await page.locator('[data-event-row]').click()
  await expect(page.locator('.event-detail')).toHaveCount(1)
  await withdrawEvents(page)
  expect(errors).toEqual([])
})

test('AEON-1270 coalescing keeps native envelopes, inferred correlations, receipts and omitted bodies honest', async ({ page }) => {
  const errors = await setup(page)
  await sessionCopy(page)
  const quotas = await page.evaluate(async items => {
    const { eventRows } = await import('/src/lib/nativeExpert/rows.ts')
    return eventRows(items, 'codex').map(row => [row.type, row.name, row.text, row.measure, row.wireName])
  }, quotaEvents)
  for (const [i, quota] of quotaCases.entries()) {
    expect.soft(quotas[i], `${quota.scope} quota in ${quota.state} state`).toEqual(['usage', 'Usage', quota.text, quota.measure, 'quota'])
  }
  // Collect label and metadata regressions together, so the baseline diagnostic
  // reports both even if its unrelated pre-watch copy fails later in this case.
  const projected = await page.evaluate(async items => {
    const { eventRows } = await import('/src/lib/nativeExpert/rows.ts')
    return eventRows(items, 'codex').map(row => [row.name, row.measure, row.wireName])
  }, mixedEvents)
  expect.soft(projected).toEqual([
    ['Resumed · generation 2', '', 'receipt'], ['Running', '', 'structured'], ['Codex', '104 chars', 'assistant.final'],
    ['Read', '400 ms', 'tool.result'], ['Usage', '23 % of context', 'quota'], ['Idle', '', 'structured'],
    ['Codex', '65 chars', 'assistant.final'], ['Bash', 'Running', 'tool.start'], ['Approval', '', 'approval'],
    ['Delivered', 'Next step', 'input.delivered'], ['Needs you', '', 'structured'], ['Codex', '120 chars', 'assistant.final'],
    ['Started', '', 'receipt'], ['Question', '', 'approval'], ['Interrupted', '', 'receipt'],
  ])
  await expect(page.getByText("Watch live to see this session's events as they happen.", { exact: true })).toBeVisible()
  await start(page)
  await expect(page.getByRole('button', { name: 'Stop watching', exact: true })).toBeVisible()
  await send(page, frame(1, 'assistant.delta', { message_id: 'message-1', text: 'First ' }))
  await expect(page.locator('.retained-value')).toHaveText('Showing the last event')
  await send(page, frame(2, 'assistant.delta', { message_id: 'message-1', text: 'delta' }))
  await expect(page.locator('[data-event-row]')).toHaveCount(1)
  await send(page, frame(3, 'assistant.final', { message_id: 'message-1', input_id: 'input-1', inferred: true, text: 'Explicit final' }))
  await send(page, frame(4, 'tool.start', { call_id: 'call-1', name: 'Read', args_summary: 'Approved summary' }))
  await send(page, frame(5, 'tool.result', { call_id: 'call-1', result_summary: 'Finished summary', duration_ms: 400 }))
  await expect(page.locator('[data-event-row]')).toHaveCount(2)
  const textRow = page.locator('[data-event-row="message:1:message-1"]'), toolRow = page.locator('[data-event-row="call:1:call-1"]')
  await expect(textRow.locator('strong')).toHaveText('Codex')
  await expect(textRow.locator('.event-measure')).toHaveText('14 chars')
  await expect(toolRow.locator('strong')).toHaveText('Read')
  await expect(toolRow.locator('.event-measure')).toHaveText('400 ms')
  await expect(page.locator('.retained-value')).toHaveText('Showing the last 5 events')
  expect(await page.locator('.event-measure').allTextContents()).not.toContain('Reply correlation inferred')
  await page.locator('[data-event-row]').first().click()
  await expect(page.locator('.raw-head')).toContainText('assistant.final · seq 3')
  await expect(page.getByLabel('C0 event envelope')).toContainText('assistant.delta')
  await expect(page.getByLabel('C0 event envelope')).toContainText('assistant.final')
  await expect(page.locator('.event-detail')).toContainText('Reply correlation inferred')
  await expect(page.locator('.event-detail')).toContainText('input-1')
  await toolRow.click()
  await expect(toolRow.locator('..').locator('..').locator('.raw-head')).toContainText('tool.result · seq 5')
  await send(page, frame(6, 'receipt', { request_id: 'request-1', operation_id: 'operation-1', binding, operation: 'input', state: 'uncertain', input_id: 'input-1', turn_ref: 'turn-1', source_sequence: '5', updated_at: '2026-10-13T02:03:04Z', reason: 'input_outcome_unconfirmed' }))
  await expect(page.locator('[data-event-row]').filter({ hasText: 'uncertain' })).toHaveCount(1)
  const omitted = frame(7)
  Object.assign(omitted.payload, { event_name: 'unknown_native_frame', omitted: true, frame_json: '', original_bytes: 900 })
  await send(page, omitted)
  await page.locator('[data-event-row="event:1:7"]').click()
  await expect(page.locator('[data-event-row="event:1:7"] strong')).toHaveText('Native event')
  await expect(page.locator('.raw-head').last()).toContainText('unknown_native_frame · seq 7')
  await expect(page.getByText('Native body omitted · 900 original bytes.', { exact: true })).toBeVisible()
  const unsafe = frame(8)
  Object.assign(unsafe.payload, { frame_json: '{"thinking":"synthetic-private-canary"}' })
  await send(page, unsafe)
  await expect(page.getByText('An event was omitted because its view, shape or size was not permitted.')).toBeVisible()
  await expect(page.getByText('synthetic-private-canary', { exact: false })).toHaveCount(0)
  expect(errors).toEqual([])
})

test('AEON-1270 bounded replay, cursor failures and gaps never masquerade as complete history', async ({ page }) => {
  await setup(page, { page: replay([frame(20), frame(19), frame(18)], '18', true) })
  await start(page)
  await expect(page.getByText('Earlier live events are unavailable. This replay is incomplete.')).toBeVisible()
  expect(await page.evaluate(() => (window as any).nativeExpert.streams[0].request.lastEventId)).toBe('1:20')
  await send(page, frame(23))
  await expect(page.getByText('2 events are missing from this live view.')).toBeVisible()
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
  await expect(page.locator('.notice').last()).toHaveText('12 events are missing from this live view.')
  await expect(page.locator('.notice').last()).not.toContainText('cursor_expired')
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
  await page.getByRole('button', { name: 'Stop watching', exact: true }).click()
  await expect(page.getByText('Stopped watching. Watch live to continue.', { exact: true })).toBeVisible()
  await act(page, 'f.setHeld(true);')
  await page.getByRole('button', { name: 'Watch live', exact: true }).click()
  await act(page, 'f.state.session.revision++; f.resolve(2,{generation:1,items:[],next_before:null,truncated:false}); await f.nextTick();')
  await expect(page.getByText('The session changed while events were loading. Watch live to read the current view.')).toBeVisible()
  expect(await page.evaluate(() => (window as any).nativeExpert.streams.length)).toBe(1)
})

test('AEON-1270 agents get structured Events only and revocation clears all diagnostic content', async ({ page }) => {
  await setup(page, { agent: true })
  await expect(page.getByRole('radiogroup')).toHaveCount(0)
  expect(await page.evaluate(() => (window as any).nativeExpert.menuLink())).toBeNull()
  await start(page)
  expect(await page.evaluate(() => (window as any).nativeExpert.streams[0].request.view)).toBe('structured')
  await send(page, frame(1))
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  await send(page, frame(2, 'terminal', { terminal_epoch: 'runtime-a', sequence: '2', data_base64: 'aGVsbG8=', rows: 24, cols: 80, reset: false }))
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  await send(page, frame(3, 'assistant.final', { message_id: 'safe', input_id: null, inferred: false, text: 'Approved final' }))
  await expect(page.locator('[data-event-row]')).toHaveCount(1)
  await withdrawEvents(page)
  await start(page, 2)
  await send(page, frame(3, 'assistant.final', { message_id: 'safe', input_id: null, inferred: false, text: 'Approved final' }), 1)
  await expect(page.locator('[data-event-row]')).toHaveCount(1)
  await send(page, frame(4, 'end', { reason: 'revoked', reload_snapshot: false }), 1)
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Watch live', exact: true })).toBeDisabled()
  await expect(page.getByText('Viewing ended because access or the owner’s consent was revoked.')).toBeVisible()
})

test('AEON-1270 terminal segment is fixed at opening; PTY plus viewer has one terminal and a head-menu descriptor', async ({ page }) => {
  // Risk: the panel menu must be the only selector; its independently mounted
  // views still fence retained frames, Review and PTY slots on current authority.
  const errors = await setup(page, { panel: true })
  await expect(page.locator('.expert-segments')).toHaveCount(0)
  await expect(page.getByRole('radio')).toHaveCount(0)
  await act(page, 'await f.mountPane("events"); await f.nextTick();')
  await expect(page.getByRole('region', { name: 'Session events · GPT-6.1 Sol', exact: true })).toBeVisible()
  await expect(page.getByRole('radio')).toHaveCount(0)
  await sessionCopy(page)
  await start(page)
  const approval = { ...approvalFrame(24, 'command').payload, binding }
  await send(page, frame(1, 'approval', approval))
  await page.getByRole('button', { name: 'Review', exact: true }).click()
  expect(await page.evaluate(() => (window as any).nativeExpert.reviews)).toEqual([approval])
  await act(page, 'await f.mountPane("terminal"); await f.nextTick();')
  expect(await page.evaluate(() => (window as any).nativeExpert.streams[0].signal.aborted)).toBe(true)
  await send(page, frame(2, 'assistant.final', { message_id: 'unmounted', input_id: null, inferred: false, text: 'unmounted-private-frame' }))
  await expect(page.locator('[data-event-row]')).toHaveCount(0)
  await expect(page.getByText('unmounted-private-frame', { exact: false })).toHaveCount(0)
  await expect(page.getByRole('radio')).toHaveCount(0)
  await expect(page.getByRole('toolbar')).toHaveCount(0)
  await expect(page.getByRole('switch', { name: 'Follow live' })).toHaveCount(0)
  const link = page.getByRole('link', { name: 'Open live terminal', exact: true })
  await expect(link).toBeVisible()
  await sessionCopy(page)
  const guard = await controlStability(page, { link, hint: page.locator('.terminal-hint') })
  await guard.check(async () => { await act(page, 'f.state.session.terminal_viewer_url=null; await f.nextTick();'); await expect(link).toBeDisabled() })
  guard.done()
  await act(page, 'f.state.session.mode="terminal"; await f.nextTick();')
  await expect(page.getByTestId('pty-slot')).toBeVisible()
  expect(await page.evaluate(() => { const props = (window as any).nativeExpert.terminalProps; return [props.session.owned_session_id, props.viewer.principalId, props.consent.id, props['model-name']] })).toEqual(['owned-a', 'person-1', 'consent-a', 'GPT-6.1 Sol'])
  await terminalModel(page, 'GPT-6.1 Sol')
  await act(page, 'f.state.modelProfile.model="gpt-6.1-astra"; await f.nextTick();')
  await terminalModel(page, 'GPT-6.1 Astra')
  await act(page, 'f.state.modelProfile=null; await f.nextTick();')
  await terminalModel(page, '')
  await act(page, 'f.state.modelProfile={harness:"codex",model:"gpt-6.1-sol",display_name:"GPT Sol",model_version:"6.1"}; await f.nextTick();')
  await terminalModel(page, 'GPT-6.1 Sol')
  await act(page, 'f.state.consent.active=false; await f.nextTick();')
  await expect(page.getByTestId('pty-slot')).toHaveCount(0)
  await act(page, 'f.setPerson("agent-2","agent"); f.state.viewer.canReadStructured=true; await f.nextTick();')
  await expect(page.getByTestId('pty-slot')).toHaveCount(0)
  await expect(link).toHaveCount(0)
  await expect(page.getByRole('region', { name: 'Session terminal · GPT-6.1 Sol', exact: true })).toHaveCount(0)
  await act(page, 'await f.mountPane("events"); await f.nextTick();')
  await expect(page.getByRole('button', { name: 'Watch live', exact: true })).toBeVisible()
  expect(await page.evaluate(() => (window as any).nativeExpert.streams.length)).toBe(1)
  expect(errors).toEqual([])

  await setup(page, { terminal: true, take: true })
  await switchTo(page, 'Terminal')
  await expect(page.getByTestId('pty-slot')).toBeVisible()
  await terminalModel(page, 'GPT-6.1 Sol')
  await expect(page.getByRole('link', { name: 'Open live terminal', exact: true })).toHaveCount(0)
  expect(await page.evaluate(() => (window as any).nativeExpert.menuLink())).toEqual({ href: session.terminal_viewer_url, label: 'Open live terminal', target: '_blank', rel: 'noopener noreferrer' })
  await act(page, 'f.state.viewer.canWatch = false; await f.nextTick();')
  await expect(page.getByTestId('pty-slot')).toHaveCount(0)
  await expect(page.getByText('Live terminal output requires current watch permission and the owner’s active consent.')).toBeVisible()
  await act(page, 'f.setPerson("agent-2","agent"); f.state.viewer.canReadStructured = true; await f.nextTick();')
  await expect(page.getByRole('radiogroup')).toHaveCount(0)
  await expect(page.getByTestId('pty-slot')).toHaveCount(0)
  expect(await page.evaluate(() => (window as any).nativeExpert.menuLink())).toBeNull()
  await page.reload()
  await act(page, 'f.state.session.terminal_viewer_url = null; f.state.session.mode="structured"; f.state.session.capabilities.take_supported=false; f.state.session.owned_session_id="new-panel"; f.state.session.current_binding.owned_session_id="new-panel"; await f.nextTick();')
  await expect(page.getByRole('radio', { name: 'Terminal', exact: true })).toHaveCount(0)
  await act(page, 'f.state.session.terminal_viewer_url="https://terminal.example.test/late"; f.state.session.capabilities.take_supported=true; await f.nextTick();')
  await expect(page.getByRole('radio', { name: 'Terminal', exact: true })).toHaveCount(0)
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`AEON-1270 r2 A1 controls, long German content and live-terminal availability stay stable ${theme} ${width}`, async ({ browser }, testInfo) => {
    const context = await browser.newContext({ baseURL: testInfo.project.use.baseURL, viewport: { width, height: 900 }, hasTouch: width === 390, reducedMotion: 'reduce' })
    const page = await context.newPage()
    try {
      const errors = await setup(page, { theme })
      const events = page.getByRole('radio', { name: 'Events', exact: true }), terminal = page.getByRole('radio', { name: 'Terminal', exact: true })
      await expect(events).toHaveAttribute('aria-checked', 'true')
      const guard = await controlStability(page, { events, terminal, toolbar: page.locator('.expert-bar'), frame: page.locator('.native-expert') })
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
      // Turning Follow off does not imply the live end is out of view. Even an
      // arrival must leave Jump hidden while the live tail remains on-screen.
      await follow.uncheck()
      await send(page, frame(2, 'assistant.final', { message_id: 'visible-arrival', input_id: null, inferred: false, text: 'A new event still fits in the visible stream.' }))
      await expect(page.locator('.event-tail')).toBeInViewport()
      // A soft assertion still fails the case, while letting the negative
      // control independently reach the compact-popover regression below.
      await expect.soft(page.getByRole('button', { name: /^Jump to live/ })).toHaveCount(0)
      const rowGuard = await controlStability(page, { row, follow, events, terminal })
      await rowGuard.check(() => row.click())
      await expect(page.getByLabel('C0 event envelope')).toContainText('long-german')
      await page.screenshot({ path: testInfo.outputPath(`AEON-1270/events-${theme}-${width}.png`) })
      rowGuard.done()
      if (width === 390) {
        const trigger = page.getByRole('button', { name: 'Event types', exact: true })
        await trigger.tap()
        const text = page.getByRole('checkbox', { name: 'Text', exact: true }), tools = page.getByRole('checkbox', { name: 'Tools', exact: true })
        const group = page.getByRole('dialog', { name: 'Event types', exact: true }), done = group.getByRole('button', { name: 'Done', exact: true })
        await expect(done).toBeVisible()
        const target = (await done.boundingBox())!
        expect(target.height).toBeGreaterThanOrEqual(44)
        expect(target.width).toBeGreaterThanOrEqual(44)
        const menuGuard = await controlStability(page, { text, tools, done, group })
        await menuGuard.check(async () => { await tools.tap(); await expect(tools).not.toBeChecked() })
        await menuGuard.check(async () => { await text.tap(); await expect(text).not.toBeChecked() })
        menuGuard.done()
        await page.screenshot({ path: testInfo.outputPath(`AEON-1270/types-${theme}-${width}.png`) })
        // Prove the dismissal action remains reachable when the list must scroll.
        await page.setViewportSize({ width, height: 320 })
        await expect(group).toHaveCSS('height', '320px')
        const options = group.locator('.types-options')
        expect(await options.evaluate(el => el.scrollHeight > el.clientHeight)).toBe(true)
        const compactGuard = await controlStability(page, { text, tools, done, group })
        await compactGuard.check(() => options.evaluate(el => { el.scrollTop = el.scrollHeight }))
        expect(await options.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
        compactGuard.done()
        await expect(done).toBeInViewport()
        await done.tap()
        await expect(group).toHaveCount(0)
        await expect(trigger).toBeFocused()
        await expect(trigger).toHaveAttribute('aria-expanded', 'false')
        await page.setViewportSize({ width, height: 900 })
        await trigger.tap()
        await expect(group).toBeVisible()
        await page.keyboard.press('Escape')
        await expect(group).toHaveCount(0)
        await expect(trigger).toBeFocused()
      } else {
        const text = page.getByRole('button', { name: 'Text', exact: true }), tools = page.getByRole('button', { name: 'Tools', exact: true })
        const typesGuard = await controlStability(page, { text, tools, follow, events, terminal })
        await typesGuard.check(() => text.click())
        await typesGuard.check(() => tools.click())
        typesGuard.done()
        // The shared session panel can be narrow on a wide viewport as well.
        await page.locator('#app').evaluate(el => { el.style.width = 'min(100%, 720px)' })
        const trigger = page.getByRole('button', { name: 'Event types', exact: true })
        await trigger.click()
        const group = page.getByRole('dialog', { name: 'Event types', exact: true }), done = group.getByRole('button', { name: 'Done', exact: true })
        await expect(done).toBeVisible()
        const popover = (await group.boundingBox())!, anchor = (await trigger.boundingBox())!
        expect(popover.width).toBe(240)
        expect(Math.abs(popover.x - anchor.x)).toBeLessThanOrEqual(1)
        expect(Math.abs(popover.y - (anchor.y + anchor.height + 6))).toBeLessThanOrEqual(1)
        expect(popover.height).toBeLessThan(420)
        expect((await done.boundingBox())!.height).toBeGreaterThanOrEqual(44)
        const textOption = group.getByRole('checkbox', { name: 'Text', exact: true }), toolOption = group.getByRole('checkbox', { name: 'Tools', exact: true })
        const menuGuard = await controlStability(page, { text: textOption, tools: toolOption, done, trigger })
        await menuGuard.check(() => toolOption.check())
        menuGuard.done()
        await page.screenshot({ path: testInfo.outputPath(`AEON-1270/types-${theme}-${width}.png`) })
        await done.click()
        await expect(group).toHaveCount(0)
        await expect(trigger).toBeFocused()
      }
      guard.done()
      // Capture the complete mixed list first, then reduce the height for the
      // scrolling checks without inventing extra rows to force overflow.
      await mixedEvidence(page, theme, width, name => page.screenshot({ path: testInfo.outputPath(`AEON-1270/${name}-${theme}-${width}.png`) }))
      await quotaEvidence(page, theme, width, name => page.screenshot({ path: testInfo.outputPath(`AEON-1270-fix5/${name}-${theme}-${width}.png`) }))
      await panelEvidence(page, theme, width, name => page.screenshot({ path: testInfo.outputPath(`AEON-1270-fix6/${name}-${theme}-${width}.png`) }))
      expect(errors).toEqual([])
    } finally { await context.close() }
  })
}
