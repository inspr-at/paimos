// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { controlStability } from './control-stability'
import type { NativeBinding, NativeSessionView, NativeStreamEvent } from '../src/lib/nativeSessionContract'

const binding: NativeBinding = { tenant_id: 'tenant-1', project_id: 'project-1', owned_session_id: 'owned-a', harness_session_id: 'harness-a',
  runtime_epoch: 'runtime-a', daemon_id: 'daemon-1', daemon_generation: 'daemon-generation-1', binding_revision: 1, run_id: null }
const session: NativeSessionView = {
  owned_session_id: 'owned-a', current_binding: binding, owner_person_id: 'owner-1', role: 'worker', profile: 'native', harness: 'codex', state: 'taken', mode: 'terminal',
  capabilities: { input: true, steer: 'native', interrupt: true, stop: true, resume: true, take: true, expert: true, terminal_view: true, approvals: true, questions: true,
    correlated_receipts: true, quota: 'stream', qualification_digest: 'qualified-fixture', take_supported: true, events_supported: true, interrupt_supported: true, chat_supported: true },
  revision: 1, conversation_ref: 'opaque-conversation', predecessor_harness_session_id: null,
  controller: { controller_id: 'controller-a', actor_id: 'holder-1', client_id: 'holder-tab', epoch: 'lease-a', expires_at: '2027-01-01T00:00:00Z' },
  quota: null, last_receipt: null, reason: null, generation: 1, terminal_viewer_url: null,
}
function event(seq: number, kind: NativeStreamEvent['kind'], payload: unknown, bound = binding, generation = 1): NativeStreamEvent {
  return { binding: bound, seq: String(seq), sequence: String(seq), generation, observed_at: '2026-10-13T08:55:00Z', dropped_frames: '0', source: 'native', kind, payload } as NativeStreamEvent
}
const terminal = (seq: number, text: string, reset = false, epoch = 'terminal-a', cols = 132, rows = 40) =>
  event(seq, 'terminal', { terminal_epoch: epoch, sequence: String(seq), data_base64: Buffer.from(text).toString('base64'), reset, cols, rows })

/** Test-only headless parser double. Query side effects are traps, not stubs
 * that silently accept everything. The actual export filter, cell painter,
 * lifecycle and size observer run unchanged. I2 qualifies @xterm/headless. */
async function setup(page: Page, options: { holder?: boolean; fontsHeld?: boolean; source?: 'web' | 'cli'; theme?: string; full?: boolean } = {}) {
  const response = await page.request.get('/src/components/nativeExpert/NativeTerminal.vue')
  expect(response.ok()).toBe(true)
  const vue = (await response.text()).match(/from\s*["']([^"']*\/vue\.js[^"']*)["']/)?.[1]
  expect(vue).toBeTruthy()
  const actor = options.holder ? 'person-1' : 'holder-1', current = { ...session, controller: { ...session.controller!, actor_id: actor } }
  const initial = { session: current, viewer: { tenantId: 'tenant-1', principalId: 'person-1', kind: 'person', canRead: true, canWatch: true, canReadStructured: true },
    consent: { id: 'consent-a', ownerPersonId: 'owner-1', binding, active: true }, active: true, clientId: 'holder-tab', modelName: 'GPT-6.1 Sol', host: 'Arbeitsrechner-Wien',
    control: { binding, controllerEpoch: 'lease-a', source: options.source ?? 'web', holderName: 'Anna Kogler', since: '22:12', atRisk: false, remainingSeconds: null },
    action: { kind: 'end', label: 'End control…', disabled: false, reason: '', binding, revision: 1, controllerEpoch: 'lease-a' },
    actionLabels: ['Take over', 'Hand back', 'End control…', 'Cancel'], layout: options.full ? 'full' : 'side',
  }
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.route('**/api/preferences/**', route => route.fulfill({ json: { value: {} } }))
  await page.route('**/tests/native-terminal-inline.html', route => route.fulfill({ contentType: 'text/html', body: `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Terminal fixture</title><link rel="stylesheet" href="/src/styles/tokens.css"><link rel="stylesheet" href="/src/styles/base.css"><style>
    body{background:var(--surface);background-image:none;overflow:hidden}body::before{display:none}#app{display:flex;flex-direction:column;height:100dvh;padding-top:5px;box-sizing:border-box;overflow:hidden}.fixture-head{display:flex;align-items:center;height:45px;flex:none;padding:0 12px;border-bottom:1px solid var(--line);box-sizing:border-box}.fixture-title{white-space:nowrap;overflow:hidden;text-overflow:ellipsis;font-size:13px}.fixture-menu{flex:none;margin-left:16px;font-size:13px}.fixture-head #header-control{flex:1;min-width:0}.fixture-head.control-head .fixture-title,.fixture-head.control-head .fixture-menu{display:none}.fixture-frame{display:flex;flex-direction:column;flex:1;min-height:0;min-width:0;overflow:hidden}.fixture-head.phone{padding:0 8px}
  </style></head><body><div id="app"></div><script type="module">
    import {createApp,h,reactive,shallowRef,nextTick,markRaw} from ${JSON.stringify(vue)};
    import NativeTerminal from '/src/components/nativeExpert/NativeTerminal.vue';
    import {SessionEvents,SessionTerminal} from '/src/lib/nativeExpert/views.ts';
    import {createXtermRenderer} from '/src/lib/nativeTerminal/renderer.ts';
    import {TerminalEscapeFilter} from '/src/lib/nativeTerminal/escapeFilter.ts';
    import {HolderSizeObserver} from '/src/lib/nativeTerminal/holderSize.ts';
    import {viewerGeometry} from '/src/lib/nativeTerminal/geometry.ts';
    import {isHolderTerminalTarget} from '/src/lib/nativeTerminal/ports.ts';
    import {setPreferenceOwner} from '/src/lib/preferences.ts';
    const state=reactive(${JSON.stringify(initial)}), streams=[], writes=[], subscriptions=[], outbound=[], parsers=[], pending=[], measurements=[], actions=[], node=shallowRef(null), pane=shallowRef('terminal');
    let held=false, fontsResolve, now=0, nextId=0, jobs=new Map(), controlHost=null;
    const clock={setTimeout(fn,ms){const id=++nextId;jobs.set(id,{fn,at:now+ms});return id},clearTimeout(id){jobs.delete(id)}};
    function advance(ms){now+=ms;for(;;){const ready=[...jobs].filter(([,job])=>job.at<=now);if(!ready.length)break;for(const[id,job]of ready){jobs.delete(id);job.fn()}}}
    if(${JSON.stringify(!!options.fontsHeld)}){const fonts=document.fonts;Object.defineProperty(document,'fonts',{value:{ready:new Promise(resolve=>fontsResolve=resolve),addEventListener:fonts.addEventListener.bind(fonts),removeEventListener:fonts.removeEventListener.bind(fonts)}})}
    document.documentElement.dataset.theme=${JSON.stringify(options.theme ?? 'light')};
    setPreferenceOwner({tenant:{id:state.viewer.tenantId},principal:{id:state.viewer.principalId}});
    Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText(){outbound.push('clipboard');return Promise.resolve()},readText(){outbound.push('clipboard read');return Promise.resolve('')}}});
    window.open=()=>{outbound.push('open');return null};
    class Parser {
      constructor(options){this.options=options;this.text='';this.disposed=false;parsers.push(this);this.buffer={active:{baseY:0,getLine:y=>({getCell:x=>{const chars=(this.text.split('\\n')[y]||'')[x]||' ';return{getChars:()=>chars,getWidth:()=>1,isBold:()=>0,isInverse:()=>0,isFgDefault:()=>true,isFgPalette:()=>false,isFgRGB:()=>false,getFgColor:()=>0,isBgDefault:()=>true,isBgPalette:()=>false,isBgRGB:()=>false,getBgColor:()=>0}}})}}}
      write(data,done){writes.push(data);if(/\\x1b(?:\\]|P|\\[[0-9;?<>]*[nctq]|Z)/.test(data)){outbound.push('device reply')}const finish=()=>{this.text+=data.replace(/\\x1b\\[[0-9;:?]*[A-Za-z]/g,'').replace(/\\r/g,'');done()};if(held)pending.push(finish);else finish()}
      dispose(){this.disposed=true;this.text=''}
      get onData(){subscriptions.push('onData');throw new Error('stdin subscription forbidden')}
      get onBinary(){subscriptions.push('onBinary');throw new Error('reply subscription forbidden')}
      get onResize(){subscriptions.push('onResize');throw new Error('resize subscription forbidden')}
      open(){outbound.push('DOM input handlers')}
    }
    const rendererFactory=markRaw(createXtermRenderer(Parser));
    const transport={stream(request,receive,signal){return new Promise((resolve,reject)=>{streams.push({request,receive,signal,resolve,reject});signal.addEventListener('abort',resolve,{once:true})})}};
    for(const key of ['input','paste','resize','reply'])Object.defineProperty(transport,key,{get(){outbound.push(key);throw new Error(key+' forbidden')}});
    const expertTransport={events(){return Promise.resolve({generation:state.session.generation,items:[],next_before:null,truncated:false})},stream(){return new Promise(()=>{})}};
    function patchPerson(id,kind='person'){state.viewer.principalId=id;state.viewer.kind=kind;setPreferenceOwner({tenant:{id:state.viewer.tenantId},principal:{id}})}
    window.nativeTerminal={state,streams,writes,subscriptions,outbound,parsers,pending,measurements,actions,node,pane,clock,advance,nextTick,TerminalEscapeFilter,HolderSizeObserver,viewerGeometry,isHolderTerminalTarget,
      emit(index,event){streams[index].receive(event)},hold(value){held=value},flush(){while(pending.length)pending.shift()()},fontsReady(){fontsResolve?.()},person:patchPerson,
      measure(){node.value?.measureBox()},restart(){return node.value?.start()},stop(){node.value?.stop()},setHeaderControl(el){controlHost=el;state.headerControl=true},
    };
    createApp({setup(){return()=>h('div',{class:'fixture-frame'},[
      h('header',{class:['fixture-head',innerWidth<=600?'phone':'',state.headerControl?'control-head':'']},[h('span',{class:'fixture-title',title:'Koordinator für die vollständige Freigabeprüfung'},'AEON-1289 · Koordinator für die vollständige Freigabeprüfung'),h('span',{class:'fixture-menu'},'Details · Chat · Events · Terminal'),h('div',{id:'header-control'})]),
      h(pane.value==='events'?SessionEvents:SessionTerminal,{...state,transport:expertTransport,name:'AEON-1289',host:state.host}, {terminal:slot=>h(NativeTerminal,{...state,...slot,modelName:state.modelName,active:state.active,controlTarget:state.headerControl?controlHost:null,transport,rendererFactory,clock,ref:node,onMeasure:m=>measurements.push(m),onAction:a=>actions.push(a)})}),
    ])}}).mount('#app');
  </script></body></html>` }))
  await page.goto('/tests/native-terminal-inline.html')
  await expect.poll(() => page.evaluate(() => (window as any).nativeTerminal?.streams.length)).toBe(1)
  await expect(page.locator('.terminal-box')).toBeVisible()
  return errors
}
const act = (page: Page, source: string) => page.evaluate(async source => { await new Function(`return (async()=>{const f=window.nativeTerminal;${source}})()`)() }, source)
const send = (page: Page, frame: NativeStreamEvent, index = 0) => page.evaluate(({ frame, index }) => (window as any).nativeTerminal.emit(index, frame), { frame, index })
const grid = (page: Page) => page.getByRole('region', { name: 'Terminal output, read-only', exact: true })
async function restart(page: Page, count: number) { await act(page, 'await f.restart();'); await expect.poll(() => page.evaluate(() => (window as any).nativeTerminal.streams.length)).toBe(count) }

test('AEON-1289 untrusted terminal escapes, queries and bounded backpressure have no outbound side effects', async ({ page }) => {
  const errors = await setup(page)
  const requests: string[] = []
  page.on('request', req => { if (/terminal\/(input|resize)|control\/(take|end)|\/take$|\/return$/.test(req.url())) requests.push(req.url()) })
  await send(page, terminal(1, 'safe-prefix\r\n', true))
  await send(page, terminal(2, '\u001b]52;c;Y2xpcGJvYXJk'))
  await send(page, terminal(3, '\u0007\u001b]8;;javascript:alert(1)\u001b\\plain-link\u001b]8;;\u001b\\\r\n'))
  await send(page, terminal(4, '\u001b[6'))
  await send(page, terminal(5, 'n\u001b[?6n\u001b[c\u001b[>c\u001b[18t\u001bP$qm\u001b\\\u001b_Gimage\u001b\\\u001b]0;changed-title\u0007'))
  await send(page, terminal(6, '\u009d52;c;Y2xpcGJvYXJk\u009c\u009b6n\u001b[?1000h\u001b[?2004h\u001b[999999999b'))
  await send(page, terminal(7, '<img src=x onerror=alert(1)>\r\n\u001b[31mred-text\u001b[0m\r\nsafe-suffix'))
  await expect(grid(page)).toContainText('safe-prefix')
  await expect(grid(page)).toContainText('safe-suffix')
  await expect(grid(page)).toContainText('<img src=x onerror=alert(1)>')
  await expect(page.locator('.terminal-grid img, .terminal-grid a, .terminal-grid iframe, .terminal-grid textarea, .terminal-grid input')).toHaveCount(0)
  await expect(page.locator('.terminal-notice')).toContainText('Unsafe terminal controls were omitted')
  expect(await page.title()).toBe('Terminal fixture')
  await grid(page).focus(); await page.keyboard.type('typing-is-not-input'); await page.keyboard.press('Escape'); await page.keyboard.press('Control+v')
  await grid(page).evaluate(el => { const clipboardData = new DataTransfer(); clipboardData.setData('text/plain', 'not-input'); el.dispatchEvent(new ClipboardEvent('paste', { bubbles: true, clipboardData })) })
  expect(await page.evaluate(() => { const f=(window as any).nativeTerminal;return [f.outbound,f.subscriptions,f.measurements] })).toEqual([[], [], []])
  expect(requests).toEqual([])
  const options = await page.evaluate(() => (window as any).nativeTerminal.parsers[0].options)
  expect(options).toMatchObject({ disableStdin: true, scrollback: 0, cursorBlink: false, logLevel: 'off', windowOptions: {} })
  expect((await page.evaluate(() => (window as any).nativeTerminal.writes.join(''))).includes('\u001b[6n')).toBe(false)

  // A held parser makes the queue overlap real arrivals. Bounds reject before
  // a writer callback can release the queue; late completion cannot revive it.
  await act(page, 'f.hold(true);')
  for (let seq = 8; seq < 74; seq++) await send(page, terminal(seq, 'queued-private-output'))
  await expect(page.locator('.terminal-empty')).toContainText('fell behind')
  await expect(grid(page)).toBeHidden()
  await act(page, 'f.flush(); f.hold(false);')
  await expect(page.locator('.terminal-screen')).toBeEmpty()
  await restart(page, 2)
  await send(page, terminal(1, 'fresh-grid', true), 1)
  await send(page, { ...terminal(2, 'oversized'), payload: { ...terminal(2, 'oversized').payload, data_base64: 'A'.repeat(65536) } } as NativeStreamEvent, 1)
  await expect(page.locator('.terminal-empty')).toContainText('bounds')
  await expect(page.locator('.terminal-screen')).toBeEmpty()
  await restart(page, 3)
  await send(page, { ...terminal(1, 'too-wide', true), payload: { ...terminal(1, 'too-wide', true).payload, cols: 321 } } as NativeStreamEvent, 2)
  await expect(page.locator('.terminal-empty')).toContainText('bounds')
  expect(errors).toEqual([])
})

test('AEON-1289 terminal capture and actions stay fenced through identity, epoch, consent and reconnect gaps', async ({ page }) => {
  const errors = await setup(page)
  await send(page, terminal(1, 'private-old-screen', true))
  await expect(grid(page)).toContainText('private-old-screen')
  await act(page, 'f.hold(true);')
  await send(page, terminal(2, 'late-private-output'))
  await act(page, 'f.state.consent.active=false;await f.nextTick();')
  await expect(page.locator('.terminal-screen')).toBeEmpty()
  expect(await page.evaluate(() => (window as any).nativeTerminal.streams[0].signal.aborted)).toBe(true)
  await act(page, 'f.flush();f.hold(false);')
  await send(page, terminal(3, 'callback-after-abort'))
  await expect(page.locator('.terminal-screen')).toBeEmpty()
  await act(page, 'f.state.consent.active=true;await f.nextTick();')
  expect(await page.evaluate(() => (window as any).nativeTerminal.streams.length)).toBe(1)
  await restart(page, 2)
  await send(page, terminal(1, 'epoch-one', true), 1)
  await send(page, terminal(2, 'epoch-two', true, 'terminal-b'), 1)
  await expect(grid(page)).toContainText('epoch-two')
  await expect(grid(page)).not.toContainText('epoch-one')
  await send(page, terminal(3, 'retired-epoch-private', true, 'terminal-a'), 1)
  await expect(grid(page)).not.toContainText('retired-epoch-private')
  await send(page, event(4, 'gap', { reason: 'client_slow', dropped_frames: '2', reload_snapshot: true }), 1)
  await expect(page.locator('.terminal-empty')).toContainText('incomplete')
  await expect(page.locator('.terminal-screen')).toBeEmpty()
  await restart(page, 3)
  await send(page, terminal(1, 'no-reset'), 2)
  await expect(page.locator('.terminal-empty')).toContainText('missing')
  await restart(page, 4)
  await send(page, { ...terminal(1, 'wrong-tenant', true), binding: { ...binding, tenant_id: 'tenant-other' } }, 3)
  await expect(page.locator('.terminal-screen')).toBeEmpty()
  await send(page, terminal(2, 'current-only', true), 3)
  await expect(grid(page)).toContainText('current-only')
  await act(page, 'f.person("person-2");await f.nextTick();')
  await send(page, terminal(3, 'wrong-person'), 3)
  await expect(page.locator('.terminal-screen')).toBeEmpty()
  await restart(page, 5)
  await send(page, terminal(1, 'person-two-screen', true), 4)
  await act(page, 'f.state.session.current_binding.runtime_epoch="runtime-b";f.state.session.current_binding.binding_revision=2;await f.nextTick();')
  await expect(page.locator('.terminal-screen')).toBeEmpty()
  await page.getByRole('button', { name: 'End control…', exact: true }).click()
  expect(await page.evaluate(() => (window as any).nativeTerminal.actions)).toEqual([])
  await act(page, 'f.state.consent.binding={...f.state.session.current_binding};f.state.action.binding={...f.state.session.current_binding};f.state.action.revision=f.state.session.revision;await f.nextTick();')
  await page.getByRole('button', { name: 'End control…', exact: true }).click()
  expect(await page.evaluate(() => (window as any).nativeTerminal.actions[0])).toMatchObject({ ownedSessionId: 'owned-a', revision: 1, controllerEpoch: 'lease-a', binding: { runtime_epoch: 'runtime-b', binding_revision: 2 } })
  await act(page, 'f.state.active=false;await f.nextTick();')
  await expect(page.locator('.terminal-screen')).toBeEmpty()
  await act(page, 'f.state.active=true;f.person("agent-1","agent");await f.nextTick();')
  await expect(page.locator('.native-terminal')).toHaveCount(0)
  expect(await page.evaluate(() => (window as any).nativeTerminal.streams.length)).toBe(5)
  await act(page, 'f.person("person-3");f.state.session.mode="structured";f.state.session.state="running";f.pane.value="events";await f.nextTick();')
  await expect(page.locator('.native-terminal')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Watch live', exact: true })).toBeVisible()
  expect(errors).toEqual([])
})

test('AEON-1289 only the current holder tab measures after fonts with debounced visibility, zoom and lease fences', async ({ page }) => {
  const errors = await setup(page, { holder: true, fontsHeld: true })
  await act(page, 'f.advance(500);await f.nextTick();')
  expect(await page.evaluate(() => (window as any).nativeTerminal.measurements)).toEqual([])
  await act(page, 'f.fontsReady();await f.nextTick();await Promise.resolve();f.measure();f.advance(149);')
  expect(await page.evaluate(() => (window as any).nativeTerminal.measurements)).toEqual([])
  await act(page, 'f.advance(1);')
  expect(await page.evaluate(() => (window as any).nativeTerminal.measurements.length)).toBe(1)
  await act(page, 'f.measure();f.measure();f.advance(150);')
  expect(await page.evaluate(() => (window as any).nativeTerminal.measurements.length)).toBe(1)
  await send(page, terminal(1, 'holder-read-only-output', true))
  await expect(page.getByRole('button', { name: 'Fit to window', exact: true })).toHaveCount(0)
  await grid(page).focus()
  expect(await page.evaluate(() => (window as any).nativeTerminal.isHolderTerminalTarget(document.activeElement))).toBe(true)
  await act(page, 'window.escaped=0;document.addEventListener("keydown",e=>{if(e.key==="Escape")window.escaped++});')
  await page.keyboard.press('Escape')
  expect(await page.evaluate(() => (window as any).escaped)).toBe(0)
  await page.keyboard.press('Control+a')
  expect(await page.evaluate(() => (window as any).nativeTerminal.outbound)).toEqual([])
  await act(page, 'f.state.control.atRisk=true;f.measure();f.advance(150);')
  expect(await page.evaluate(() => (window as any).nativeTerminal.measurements.length)).toBe(1)
  await act(page, 'f.state.control.atRisk=false;await f.nextTick();f.measure();f.advance(150);')
  expect(await page.evaluate(() => (window as any).nativeTerminal.measurements.length)).toBe(2)
  await act(page, 'f.state.clientId="second-holder-tab";await f.nextTick();f.measure();f.advance(150);')
  expect(await page.evaluate(() => (window as any).nativeTerminal.measurements.length)).toBe(2)
  await expect(page.getByRole('button', { name: 'Fit to window', exact: true })).toBeVisible()

  // The same production observer gets explicit samples and a clock, so hidden,
  // pinch zoom, storms, re-entry and physical bounds need no browser sleeps.
  const samples = await page.evaluate(() => {
    const f = (window as any).nativeTerminal, emitted: any[] = [], sample = { key: 'lease-tab-a', visible: true, fontsReady: true, scale: 1, width: 900, height: 600, cell13: 7.8,
      request: { binding: f.state.session.current_binding, generation: 1, revision: 1, controllerEpoch: 'lease-a', clientId: 'tab-a' } }
    const observer = new f.HolderSizeObserver(f.clock, () => sample, (value: unknown) => emitted.push(value))
    observer.changed(); f.advance(149); const before = emitted.length
    sample.width=910; observer.changed(); f.advance(149); const storm=emitted.length;f.advance(1)
    sample.visible=false;observer.changed();f.advance(150);const hidden=emitted.length
    sample.visible=true;sample.scale=2;observer.changed();f.advance(150);const zoom=emitted.length
    sample.scale=1;observer.changed();f.advance(150);const returned=emitted.length
    sample.width=20;sample.height=20;observer.changed();f.advance(150)
    sample.width=10000;sample.height=10000;observer.changed();f.advance(150)
    sample.key='lease-tab-b';observer.changed();sample.key='lease-tab-c';f.advance(150);const stale=emitted.length
    observer.dispose();f.advance(3000)
    return {before,storm,hidden,zoom,returned,stale,grids:emitted.map(value=>[value.cols,value.rows])}
  })
  expect(samples).toMatchObject({ before: 0, storm: 0, hidden: 1, zoom: 1, returned: 2, stale: 4 })
  expect(samples.grids.slice(-2)).toEqual([[40, 12], [320, 100]])
  expect(errors).toEqual([])
})

test('AEON-1289 r5 Fit, grid edges and phone controls stay stable across panel sizes', async ({ browser }, testInfo) => {
  for (const width of [390, 1024, 1440, 1920]) for (const theme of ['light', 'dark']) {
    const context = await browser.newContext({ baseURL: testInfo.project.use.baseURL, viewport: { width, height: 900 }, hasTouch: width === 390, reducedMotion: 'reduce' })
    const page = await context.newPage()
    try {
      const errors = await setup(page, { theme, full: true })
      await send(page, terminal(1, 'Read-only native terminal\r\nChecking the current binding and permissions.\r\n', true))
      const fit = page.getByRole('button', { name: 'Fit to window', exact: true }), action = page.getByRole('button', { name: 'End control…', exact: true })
      await expect(fit).toHaveAttribute('aria-pressed', 'false')
      const guard = await controlStability(page, { fit, action, line: page.locator('.terminal-control'), frame: page.locator('.native-terminal'), box: page.locator('.terminal-box') })
      const checkEdges = async () => {
        const box = (await page.locator('.terminal-box').boundingBox())!, g = (await grid(page).boundingBox())!
        const expected = [g.height < box.height - .5 ? 'top' : '', g.width < box.width - .5 ? 'right' : ''].filter(Boolean).join(' ')
        await expect(grid(page)).toHaveAttribute('data-edge', expected)
        const lines = await grid(page).evaluate(el => ({ top: getComputedStyle(el, '::before').height, right: getComputedStyle(el, '::after').width }))
        if (expected.includes('top')) expect(lines.top).toBe('1px')
        if (expected.includes('right')) expect(lines.right).toBe('1px')
      }
      await checkEdges()
      await page.screenshot({ path: testInfo.outputPath(`AEON-1289/viewer-${theme}-${width}.png`) })
      await guard.check(async () => { await fit.click(); await expect(fit).toHaveAttribute('aria-pressed', 'true') })
      await checkEdges()
      if (width >= 1440) {
        const b = (await page.locator('.terminal-box').boundingBox())!, g = (await grid(page).boundingBox())!, font = Number(await grid(page).getAttribute('data-font'))
        expect(g.width / b.width >= .9 || font === 16).toBe(true)
      }
      const shared = await page.evaluate(() => {
        const b=document.querySelector('.terminal-box')!.getBoundingClientRect(), c=document.querySelector('.terminal-control')!.getBoundingClientRect(), h=document.querySelector('.fixture-head')!.getBoundingClientRect()
        const inset=innerWidth<=600?8:12
        return {left:[b.left,c.left,h.left+inset],right:[b.right,c.right,h.right-inset],gap:b.top-c.bottom,above:b.top,scroll:document.documentElement.scrollHeight-innerHeight}
      })
      expect(Math.max(...shared.left)-Math.min(...shared.left)).toBeLessThanOrEqual(.5)
      expect(Math.max(...shared.right)-Math.min(...shared.right)).toBeLessThanOrEqual(.5)
      expect(shared.gap).toBe(width === 390 ? 0 : 8)
      expect(shared.above).toBeLessThanOrEqual(132)
      expect(shared.scroll).toBeLessThanOrEqual(1)
      await guard.check(async () => { await fit.click(); await expect(fit).toHaveAttribute('aria-pressed', 'false') })
      await guard.check(async () => { await act(page, 'f.state.control.atRisk=true;f.state.control.remainingSeconds=24;await f.nextTick();'); await expect(page.locator('.terminal-control')).toHaveClass(/risk/) })
      guard.done()
      await page.screenshot({ path: testInfo.outputPath(`AEON-1289/lease-risk-${theme}-${width}.png`) })
      await act(page, 'f.state.control.atRisk=false;f.state.actionLabels=["Übernahme der Sitzung bestätigen","Steuerung zurückgeben","Steuerung für diese Sitzung beenden…","Abbrechen"];f.state.action.label="Steuerung zurückgeben";await f.nextTick();')
      const longAction = page.getByRole('button', { name: 'Steuerung zurückgeben', exact: true })
      const labels = await controlStability(page, { action: longAction, box: page.locator('.terminal-box') })
      await labels.check(async () => { await act(page, 'f.state.action.label="Übernahme der Sitzung bestätigen";await f.nextTick();') })
      labels.done()
      await page.screenshot({ path: testInfo.outputPath(`AEON-1289/long-labels-${theme}-${width}.png`) })
      if (width === 390) {
        await act(page, 'f.state.session.controller.actor_id=f.state.viewer.principalId;f.state.action.label="Hand back";f.state.actionLabels=["Take over","Hand back","End control…","Cancel"];await f.nextTick();')
        await expect(fit).toHaveCount(0)
        await expect(page.getByRole('group', { name: 'Terminal keys, read-only here' })).toBeVisible()
        const keys = page.locator('.phone-keys button')
        expect(await keys.count()).toBe(7)
        for (const key of await keys.all()) { await expect(key).toBeDisabled(); expect((await key.boundingBox())!.height).toBeGreaterThanOrEqual(44) }
        await act(page, 'f.setHeaderControl(document.querySelector("#header-control"));await f.nextTick();')
        await expect(page.locator('.fixture-head .terminal-control')).toBeVisible()
        await page.screenshot({ path: testInfo.outputPath(`AEON-1289/holder-phone-${theme}.png`) })
      }
      expect(await page.evaluate(() => (window as any).nativeTerminal.outbound)).toEqual([])
      expect(errors).toEqual([])
    } finally { await context.close() }
  }
})
