// SPDX-License-Identifier: AGPL-3.0-only
// AEON-326: two browsers over the same mock data, with the live event stream.
import { expect, type Browser, type BrowserContext, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors, type Fixtures, type MockNode, type MockOptions } from './work-fixtures'

const mira = '22222222-2222-4222-8222-222222222222'

// The server side of the stream over the shared mock data: every stream
// request first logs what changed since the last look (as node events with
// node_changes, never values), then answers in live mode and closes; the
// browser reconnects after `retry`, as after a real drop. Playwright does not
// show the Last-Event-ID header Chromium adds to a reconnect, so each page's
// last delivered ID stands in for it (the Go stream tests cover the header).
// actor: whose writes the events name (the signed-in person: their other tab).
export function liveServer(data: Fixtures, actor = mira) {
  const seen = new Map(data.nodes.map(node => [node.id, structuredClone(node)]))
  const log: { id: number; type: string; data: unknown }[] = []
  let newest = 500
  const requests: { page: string; after: string | null; resumed: boolean }[] = []
  // A held page hears nothing new (a slow connection): its stream resumes where it was.
  const held = new Set<string>()
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
    log.push({ id, type, data: { id, actor_principal_id: actor, node_id: node.id, type, before: null, after: null, at: new Date().toISOString(), undo_of: null, node_changes: [change] } })
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
      for (const event of held.has(name) ? [] : log.filter(event => event.id > after)) {
        lines.push(`id: ${event.id}`, `event: ${event.type}`, `data: ${JSON.stringify(event.data)}`, '')
        lastEventId = event.id
      }
      return route.fulfill({ status: 200, headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' }, body: `${lines.join('\n')}\n` })
    })
  }
  return { install, requests, hold: (name: string, on = true) => { if (on) held.add(name); else held.delete(name) } }
}

type Viewport = { width: number; height: number }
// Two signed-in browsers on url. viewportB alone keeps the slice 1a call; an
// options object also sets A's viewport and the fixture options.
export async function openBoth(browser: Browser, url: string, viewport?: Viewport | { a?: Viewport; b?: Viewport; options?: MockOptions; colorScheme?: 'light' | 'dark'; actor?: string }) {
  const settings = viewport && 'width' in viewport ? { b: viewport } : viewport ?? {}
  const viewportB = settings.b, viewportA = 'a' in settings ? settings.a : undefined
  const data = fixtures('options' in settings ? settings.options : {})
  const live = liveServer(data, 'actor' in settings ? settings.actor : undefined)
  const scheme = 'colorScheme' in settings && settings.colorScheme ? { colorScheme: settings.colorScheme } : {}
  const contexts: BrowserContext[] = [await browser.newContext({ ...scheme, ...(viewportA ? { viewport: viewportA } : {}) }), await browser.newContext({ ...scheme, ...(viewportB ? { viewport: viewportB } : {}) })]
  const [a, b] = await Promise.all(contexts.map(context => context.newPage()))
  for (const [page, name] of [[a, 'a'], [b, 'b']] as const) { await mockWork(page, data); await live.install(page, name) }
  const errors = watchErrors(b), errorsA = watchErrors(a)
  await Promise.all([a.goto(url), b.goto(url)])
  // Both listen from here on: their first stream requests have been answered.
  await expect.poll(() => new Set(live.requests.map(r => r.page)).size).toBe(2)
  return { data, live, a, b, errors, errorsA, close: () => Promise.all(contexts.map(context => context.close())) }
}

