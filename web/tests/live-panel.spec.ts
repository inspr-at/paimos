// SPDX-License-Identifier: AGPL-3.0-only
// AEON-326 slice 1a: two browsers on the same ticket. B's ticket panel follows
// A's changes through the live event stream: fields patch in place with a
// brief tint and a polite announcement, a change during an edit waits and
// meets the save as a conflict, and a deletion shows the ticket as gone.
import { expect, test, type Browser, type BrowserContext, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors, type Fixtures, type MockNode } from './work-fixtures'

const mira = '22222222-2222-4222-8222-222222222222'
const mod = process.platform === 'darwin' ? 'Meta' : 'Control'
const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })

// The server side of the stream over the shared mock data: every stream
// request first logs what changed since the last look (as node events with
// node_changes, never values), then answers in live mode and closes; the
// browser reconnects after `retry`, as after a real drop. Playwright does not
// show the Last-Event-ID header Chromium adds to a reconnect, so each page's
// last delivered ID stands in for it (the Go stream tests cover the header).
function liveServer(data: Fixtures) {
  const seen = new Map(data.nodes.map(node => [node.id, structuredClone(node)]))
  const log: { id: number; type: string; data: unknown }[] = []
  let newest = 500
  const requests: { page: string; after: string | null; resumed: boolean }[] = []
  const changedFields = (before: MockNode, after: MockNode) => {
    const fields = (['title', 'body', 'state', 'parent_id'] as const).filter(key => before[key] !== after[key]) as string[]
    for (const key of new Set([...Object.keys(before.fields), ...Object.keys(after.fields)])) {
      if (JSON.stringify(before.fields[key]) !== JSON.stringify(after.fields[key])) fields.push(`fields.${key}`)
    }
    return fields
  }
  const record = (type: string, before: MockNode | null, after: MockNode | null) => {
    const node = (after ?? before)!
    const id = ++newest
    const change = {
      id: node.id, project_id: node.project, change: !after ? 'deleted' : before ? 'updated' : 'created',
      fields: before && after ? changedFields(before, after) : after ? [] : ['deleted_at'],
      revision: after?.updated_at ?? new Date(Date.parse(node.updated_at) + 1000).toISOString(),
    }
    log.push({ id, type, data: { id, actor_principal_id: mira, node_id: node.id, type, before: null, after: null, at: new Date().toISOString(), undo_of: null, node_changes: [change] } })
  }
  const scan = () => {
    const now = new Map(data.nodes.map(node => [node.id, node]))
    for (const [id, before] of seen) {
      const after = now.get(id)
      if (!after) { record('node.deleted', before, null); seen.delete(id) }
      else if (JSON.stringify(after) !== JSON.stringify(before)) { record('node.updated', before, after); seen.set(id, structuredClone(after)) }
    }
    for (const [id, node] of now) if (!seen.has(id)) { record('node.created', null, node); seen.set(id, structuredClone(node)) }
  }
  async function install(page: Page, name: string) {
    let lastEventId: number | null = null
    // Registered after mockWork, so it answers first.
    await page.route('**/api/events/stream**', route => {
      const query = new URL(route.request().url()).searchParams
      scan()
      const numeric = query.get('after') !== 'latest' ? Number(query.get('after')) : null
      const resume = lastEventId ?? numeric
      const after = resume ?? newest
      requests.push({ page: name, after: query.get('after'), resumed: resume !== null })
      const lines = ['retry: 150', '', ': connected', '', `id: ${after}`, 'event: stream.ready', `data: ${JSON.stringify({ after, resumed: resume !== null })}`, '']
      lastEventId = after
      for (const event of log.filter(event => event.id > after)) {
        lines.push(`id: ${event.id}`, `event: ${event.type}`, `data: ${JSON.stringify(event.data)}`, '')
        lastEventId = event.id
      }
      return route.fulfill({ status: 200, headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' }, body: `${lines.join('\n')}\n` })
    })
  }
  return { install, requests }
}

async function openBoth(browser: Browser, url: string, viewportB?: { width: number; height: number }) {
  const data = fixtures()
  const live = liveServer(data)
  const contexts: BrowserContext[] = [await browser.newContext(), await browser.newContext(viewportB ? { viewport: viewportB } : {})]
  const [a, b] = await Promise.all(contexts.map(context => context.newPage()))
  for (const [page, name] of [[a, 'a'], [b, 'b']] as const) { await mockWork(page, data); await live.install(page, name) }
  const errors = watchErrors(b)
  await Promise.all([a.goto(url), b.goto(url)])
  // Both listen from here on: their first stream requests have been answered.
  await expect.poll(() => new Set(live.requests.map(r => r.page)).size).toBe(2)
  return { data, live, a, b, errors, close: () => Promise.all(contexts.map(context => context.close())) }
}

test('a change in one browser patches the open ticket in the other in place', async ({ browser }) => {
  const { a, b, live, errors, close } = await openBoth(browser, '/p/PHAROS/PHAROS-12')
  try {
    const wsA = panel(a), wsB = panel(b)
    await expect(wsB.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()

    await wsA.getByRole('heading', { name: 'Add an Oracle Cloud connector' }).click()
    await wsA.getByLabel('Title', { exact: true }).fill('Add an Oracle Cloud Always Free connector')
    await a.keyboard.press('Enter')
    await expect(wsA.getByRole('heading', { name: 'Add an Oracle Cloud Always Free connector' })).toBeVisible()

    // B: the title patches in place, tinted briefly, and is announced politely.
    const title = wsB.getByRole('heading', { name: 'Add an Oracle Cloud Always Free connector' })
    await expect(title).toBeVisible()
    await expect(wsB.locator('.inline-title')).toHaveClass(/live-tint/)
    await expect(wsB.locator('p.sr-only[role="status"]')).toHaveText('PHAROS-12 was updated elsewhere: title.')
    await expect(wsB.locator('.inline-title')).not.toHaveClass(/live-tint/, { timeout: 4000 })
    // The row under the panel is the same ticket, patched in place too.
    await expect(b.locator('#row-n-2')).toContainText('Always Free')

    await a.keyboard.press('p')
    await a.getByRole('menu', { name: 'Priority of PHAROS-12' }).getByRole('menuitemradio', { name: 'High' }).click()
    await expect(wsB.getByRole('button', { name: /Priority: High/ })).toBeVisible()
    await expect(wsB.locator('p.sr-only[role="status"]')).toHaveText('PHAROS-12 was updated elsewhere: priority.')
    // A's own change is not announced to A.
    await expect(wsA.locator('p.sr-only[role="status"]')).toHaveText('')

    // Every connection asked for live mode; reconnects resumed after the last event.
    expect(live.requests.every(r => r.after === 'latest')).toBe(true)
    expect(live.requests.filter(r => r.page === 'b' && r.resumed).length).toBeGreaterThan(0)
    expect(errors).toEqual([])
  } finally { await close() }
})

test('a change during an edit waits, meets the save as a conflict and keeps the draft', async ({ browser }) => {
  const { a, b, data, close } = await openBoth(browser, '/p/PHAROS/PHAROS-12')
  try {
    const wsA = panel(a), wsB = panel(b)
    await expect(wsB.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    await wsB.getByRole('button', { name: 'Edit', exact: true }).click()
    const draftTitle = wsB.locator('#edit-title')
    await draftTitle.fill('Oracle connector, my wording')

    await a.keyboard.press('s')
    await a.getByRole('menu', { name: 'Status of PHAROS-12' }).getByRole('menuitemradio', { name: 'In progress' }).click()
    await expect(wsA.getByRole('button', { name: /Status: In progress/ })).toBeVisible()

    // B is told, and nothing moved under the editor.
    await expect(wsB.getByRole('status').filter({ hasText: 'Changed elsewhere meanwhile' })).toBeVisible()
    await expect(draftTitle).toHaveValue('Oracle connector, my wording')

    // The save still sends the revision B started from: a conflict, not an overwrite.
    await draftTitle.focus()
    await b.keyboard.press(`${mod}+Enter`)
    await expect(b.getByText('PHAROS-12 was changed elsewhere. The newer version is shown; your draft is kept.')).toBeVisible()
    await expect(draftTitle).toHaveValue('Oracle connector, my wording')
    await expect(wsB.getByRole('status').filter({ hasText: 'Changed elsewhere meanwhile' })).toHaveCount(0)
    // Saving again keeps A's status: the draft took the newer value where B did not edit.
    await b.keyboard.press(`${mod}+Enter`)
    await expect(wsB.getByRole('heading', { name: 'Oracle connector, my wording' })).toBeVisible()
    await expect(wsB.getByRole('button', { name: /Status: In progress/ })).toBeVisible()
    const node = data.nodes.find(n => n.id === 'n-2')!
    expect(node.title).toBe('Oracle connector, my wording')
    expect(node.state).not.toBe('backlog')
  } finally { await close() }
})

test('a ticket deleted in one browser reads as gone in the other, on a phone too', async ({ browser }) => {
  const { a, b, errors, close } = await openBoth(browser, '/p/PHAROS/PHAROS-14', { width: 390, height: 844 })
  try {
    const wsB = panel(b)
    await expect(wsB.getByRole('heading', { name: 'Visual acceptance of the version pill' })).toBeVisible()
    await panel(a).getByRole('button', { name: 'More actions' }).click()
    await a.getByRole('menuitem', { name: 'Delete ticket…' }).click()
    await a.getByRole('dialog', { name: 'Delete PHAROS-14?' }).getByRole('button', { name: 'Delete ticket' }).click()
    await expect(a).toHaveURL('/p/PHAROS/tickets')

    await expect(wsB.getByRole('alert')).toContainText('PHAROS-14 is no longer here')
    expect(await b.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    expect(errors).toEqual([])
  } finally { await close() }
})
