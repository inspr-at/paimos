// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { sessionEnded } from '../src/lib/api.ts'
import { fetchTicketGraph, ticketGraphPath, type TicketGraph } from '../src/lib/ticketGraph.ts'

const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch; sessionEnded.blocked = false })

const project = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
const graph: TicketGraph = {
  truncated: false,
  nodes: [{
    id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', key: 'PHA-1', title: 'Rotate keys', type: 'ticket',
    status: 'new', status_category: 'open', priority: 'high', parent_id: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc',
    release_id: null, updated_at: '2026-09-26T12:00:00Z', link_count: 1,
  }],
  links: [{ source: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', target: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc', kind: 'parent' }],
}

test('the path names the project and adds include_closed only when asked', () => {
  assert.equal(ticketGraphPath(project), `/tickets/graph?project_id=${project}`)
  assert.equal(ticketGraphPath(project, false), `/tickets/graph?project_id=${project}`)
  assert.equal(ticketGraphPath(project, true), `/tickets/graph?project_id=${project}&include_closed=true`)
})

test('fetchTicketGraph returns the body-free graph', async () => {
  sessionEnded.blocked = false
  const urls: string[] = []
  globalThis.fetch = async url => {
    urls.push(String(url))
    return Response.json(graph)
  }
  const got = await fetchTicketGraph(project)
  assert.equal(urls[0], `/api/tickets/graph?project_id=${project}`)
  assert.equal(got.nodes.length, 1)
  assert.equal(got.nodes[0].title, 'Rotate keys')
  assert.equal(got.nodes[0].status_category, 'open')
  assert.equal(got.links[0].kind, 'parent')
  assert.equal(got.truncated, false)
  assert.equal('body' in got.nodes[0], false)
  await fetchTicketGraph(project, true)
  assert.equal(urls[1], `/api/tickets/graph?project_id=${project}&include_closed=true`)
})

test('a failed load rejects', async () => {
  sessionEnded.blocked = false
  globalThis.fetch = async () => new Response('no', { status: 404 })
  await assert.rejects(() => fetchTicketGraph(project), /could not be loaded/)
})
