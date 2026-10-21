// SPDX-License-Identifier: AGPL-3.0-only
// Risk: mounted copies multiply reads, event storms overwrite newer data, or
// hidden polling and late responses leak work across screen/access changes.
import { expect, test, type Page } from '@playwright/test'

async function harness(page: Page, components = false) {
  const store = await (await page.request.get('/src/stores/workQueue.ts')).text()
  const pinia = store.match(/from\s*["']([^"']*\/pinia\.js[^"']*)["']/)?.[1]
  const polled = await (await page.request.get('/src/lib/usePolledData.ts')).text()
  const vue = polled.match(/from\s*["']([^"']*\/vue\.js[^"']*)["']/)?.[1]
  expect(pinia).toBeTruthy(); expect(vue).toBeTruthy()
  await page.route('**/tests/idle-requests-inline.html', route => route.fulfill({ contentType: 'text/html', body: `<!doctype html><html><body><div id="app"></div><script type="module">
    import { createPinia, setActivePinia } from ${JSON.stringify(pinia)};
    import { createApp, h, reactive, watch } from ${JSON.stringify(vue)};
    import { useWorkQueue } from '/src/stores/workQueue.ts';
    import { useProjectLeads } from '/src/stores/projectLeads.ts';
    import { subscribeAgents } from '/src/lib/agents.ts';
    import { resetPositions } from '/src/lib/position.ts';
    const streams = [];
    class Source extends EventTarget {
      onopen = null; onerror = null; closed = false;
      constructor(url) { super(); this.url = url; streams.push(this); }
      close() { this.closed = true; }
    }
    window.EventSource = Source;
    const pinia = createPinia(); setActivePinia(pinia);
    const queue = useWorkQueue(), leads = useProjectLeads(), stops = [], seen = [];
    watch(() => queue.snapshots.p1?.capacity.total, value => seen.push(value), {flush:'sync'});
    const state = reactive({nodeId:'ticket', projectId:'p1'});
    window.idleRequests = {
      queue, leads, streams, stops, seen, state, reset:resetPositions,
      mount() { stops.push(queue.watch('p1'), queue.watch('p1'), leads.watch('p1'), leads.watch('p1'), subscribeAgents(() => {})); },
      stop() { for (const stop of stops.splice(0)) stop(); },
      signal(type, node = 'p1') { for (const stream of streams) if (!stream.closed) stream.dispatchEvent(new MessageEvent(type, {data:JSON.stringify({node_id:node})})); },
      visible(value) { Object.defineProperty(document,'visibilityState',{configurable:true,value:value?'visible':'hidden'}); Object.defineProperty(document,'hidden',{configurable:true,value:!value}); document.dispatchEvent(new Event('visibilitychange')); },
    };
    window.idleRequests.start = async () => { ${components ? `
      const {default: TicketDelivery} = await import('/src/components/work/TicketDelivery.vue');
      const {default: TicketReviews} = await import('/src/components/work/TicketReviews.vue');
      const {useSession} = await import('/src/stores/session.ts');
      useSession().identity = {tenant:{id:'tenant',name:'Test'},principal:{id:'person',name:'Person',kind:'person'}};
      const app = createApp({setup:() => () => h('main',[h(TicketDelivery,state),h(TicketReviews,state)])});
      app.use(pinia); app.mount('#app'); window.idleRequests.unmount = () => app.unmount();
      const {QuotePresence} = await import('/src/lib/quotePresence.ts');
      const presence = new QuotePresence('quote','person',() => {},() => {});
      await presence.start(1); window.idleRequests.presence = presence;
    ` : 'window.idleRequests.mount();'} };
    window.idleRequests.ready = true;
  </script></body></html>` }))
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto('/tests/idle-requests-inline.html')
  await expect.poll(() => page.evaluate(() => !!(window as any).idleRequests?.ready)).toBe(true)
  await page.evaluate(() => (window as any).idleRequests.start())
  return errors
}
// The live transport keeps hearing pings while the injected browser clock
// advances. No elapsed-time assertions or sleeping establish these interleavings.
async function advance(page: Page, milliseconds: number) {
  while (milliseconds > 0) {
    await page.evaluate(() => (window as any).idleRequests.signal('stream.ping'))
    const step = Math.min(milliseconds, 15_000)
    await page.clock.runFor(step)
    milliseconds -= step
  }
}

test('AEON-1117 shares mounted reads and event transport, fences stale answers and recovers missed events and access', async ({ page }) => {
  await page.clock.install({ time: Date.now() })
  const counts = { queue: 0, lead: 0 }
  let revision = 1, denied = false, hold = false
  let release: (() => Promise<void>) | undefined
  await page.route('**/api/**', async route => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/queue') {
      counts.queue++
      const json = { items: [], manual_order: false, capacity: { work_hours: 0, parallel_runs: revision, queued_hours: 0, warning: false } }
      if (hold) { hold = false; release = () => route.fulfill({ json }); return }
      return route.fulfill(denied ? { status: 403, json: { error: 'Queue access revoked' } } : { json })
    }
    if (url.pathname.endsWith('/lead')) {
      counts.lead++
      return route.fulfill(denied ? { status: 403, json: { error: 'Lead access revoked' } } : { json: { state: 'none', revision, generation: 0, project_id: 'p1', session_id: null } })
    }
    return route.fulfill({ json: {} })
  })
  const errors = await harness(page)
  await expect.poll(() => counts).toEqual({ queue: 1, lead: 1 })
  expect(await page.evaluate(() => (window as any).idleRequests.streams.filter((s: any) => !s.closed).length)).toBe(1)
  // Unrelated projects and heartbeats do not read the lead or queue.
  await page.evaluate(() => { const h = (window as any).idleRequests; for (let i = 0; i < 20; i++) { h.signal('harness.heartbeat'); h.signal('lead.start_requested','other-project') } })
  await advance(page, 20_000)
  expect(counts).toEqual({ queue: 1, lead: 1 })
  revision = 2
  await page.evaluate(() => { const h = (window as any).idleRequests; for (let i = 0; i < 20; i++) { h.signal('queue.added'); h.signal('lead.start_requested') } })
  await advance(page, 400)
  await expect.poll(() => counts).toEqual({ queue: 2, lead: 2 })
  await expect.poll(() => page.evaluate(() => (window as any).idleRequests.queue.snapshots.p1?.capacity.total)).toBe(2)
  // Hold a pre-event response; a burst while it is held retains one catch-up.
  revision = 999; hold = true
  await page.evaluate(() => (window as any).idleRequests.signal('queue.moved'))
  await advance(page, 400)
  await expect.poll(() => !!release).toBe(true)
  revision = 3
  await page.evaluate(() => { const h = (window as any).idleRequests; for (let i = 0; i < 20; i++) h.signal('queue.moved') })
  await advance(page, 400)
  expect(counts.queue).toBe(3)
  await release!(); release = undefined
  await expect.poll(() => page.evaluate(() => (window as any).idleRequests.queue.snapshots.p1?.capacity.total)).toBe(3)
  expect(counts.queue).toBe(4)
  expect(await page.evaluate(() => (window as any).idleRequests.seen)).not.toContain(999)
  await page.evaluate(() => (window as any).idleRequests.visible(false))
  const hidden = { ...counts }
  await page.evaluate(() => { const h = (window as any).idleRequests; h.signal('queue.added'); h.signal('lead.start_requested') })
  await advance(page, 120_000)
  expect(counts).toEqual(hidden)
  revision = 4
  await page.evaluate(() => (window as any).idleRequests.visible(true))
  await expect.poll(() => page.evaluate(() => (window as any).idleRequests.queue.snapshots.p1?.capacity.total)).toBe(4)
  await advance(page, 400)
  // A revoked read removes previously authorized data, and a later periodic
  // read repairs a missed hint without adopting the hint's payload.
  denied = true
  await page.evaluate(() => (window as any).idleRequests.signal('binding.removed'))
  await advance(page, 400)
  await expect.poll(() => page.evaluate(() => ({ queue: (window as any).idleRequests.queue.snapshots.p1 ?? null, forbidden: (window as any).idleRequests.leads.views.p1?.forbidden }))).toEqual({ queue: null, forbidden: true })
  denied = false; revision = 5
  await advance(page, 120_000)
  await expect.poll(() => page.evaluate(() => (window as any).idleRequests.queue.snapshots.p1?.capacity.total)).toBe(5)
  // Switching screens stops the old key; an old response cannot repopulate it
  // after the person/workspace reset, or change the new screen's snapshot.
  hold = true; revision = 999
  await page.evaluate(() => (window as any).idleRequests.signal('queue.moved'))
  await advance(page, 400)
  await expect.poll(() => !!release).toBe(true)
  revision = 6
  await page.evaluate(() => { const h = (window as any).idleRequests; h.stop(); h.reset(); h.stops.push(h.queue.watch('p2')) })
  await expect.poll(() => page.evaluate(() => (window as any).idleRequests.queue.snapshots.p2?.capacity.total)).toBe(6)
  await release!()
  await page.evaluate(() => Promise.resolve())
  expect(await page.evaluate(() => (window as any).idleRequests.queue.snapshots.p1 ?? null)).toBeNull()
  await page.evaluate(() => (window as any).idleRequests.stop())
  const stopped = { ...counts }
  await advance(page, 120_000)
  expect(counts).toEqual(stopped)
  expect(await page.evaluate(() => (window as any).idleRequests.streams.filter((s: any) => !s.closed).length)).toBe(0)
  expect(errors).toEqual([])
})

test('AEON-1117 raw ticket readers and quote heartbeats stop while hidden and resume with current screen identity', async ({ page }) => {
  await page.clock.install({ time: Date.now() })
  const reads: string[] = []
  const presence: string[] = []
  await page.route('**/api/**', route => {
    const url = new URL(route.request().url()), method = route.request().method()
    if (url.pathname === '/api/me/permissions') return route.fulfill({ json: { workspace: { role: null, permissions: ['delivery.read', 'delivery_queue.read', 'work_orders.read'] }, project: { id: url.searchParams.get('project_id'), role: null, permissions: [] } } })
    if (/\/delivery$|\/delivery-queue$|\/reviews$/.test(url.pathname)) {
      reads.push(url.pathname + url.search)
      if (url.pathname.endsWith('/reviews')) return route.fulfill({ json: [] })
      if (url.pathname.endsWith('/delivery-queue')) return route.fulfill({ json: { items: [], settings: { mode: 'off' }, next_cursor: null } })
      return route.fulfill({ json: { items: [], next_cursor: null, available: true } })
    }
    if (url.pathname.includes('/presence')) {
      presence.push(method)
      const snapshot = { sessions: [], draft_revision: 1, quote_revision: 1, state: 'draft' }
      return route.fulfill({ json: method === 'POST' ? { session_id: 'presence', snapshot } : snapshot })
    }
    return route.fulfill({ json: {} })
  })
  const errors = await harness(page, true)
  await expect.poll(() => reads.length).toBe(3)
  await page.evaluate(() => (window as any).idleRequests.visible(false))
  await expect.poll(() => presence.filter(method => method === 'PATCH').length).toBe(1)
  const hidden = { reads: reads.length, presence: presence.length }
  await advance(page, 120_000)
  expect({ reads: reads.length, presence: presence.length }).toEqual(hidden)
  await page.evaluate(() => { const h = (window as any).idleRequests; h.state.nodeId = 'next-ticket'; h.visible(true) })
  await expect.poll(() => reads.length).toBe(6)
  expect(reads.slice(3).every(path => path.includes('next-ticket'))).toBe(true)
  await page.evaluate(() => { const h = (window as any).idleRequests; h.unmount(); h.presence.stop() })
  expect(errors).toEqual([])
})
