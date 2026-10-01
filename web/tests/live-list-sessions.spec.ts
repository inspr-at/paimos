// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { fixtures, liveAgent, mockWork, watchErrors } from './work-fixtures'

test('ticket workers and ETA follow stop, registration, heartbeat and reconnect without reload', async ({ page }) => {
  const at = Date.parse('2026-09-23T12:00:00Z')
  await page.clock.setSystemTime(at)
  await page.setViewportSize({ width: 1800, height: 1000 })
  // Isolated streams let the test deliver lifecycle hints independently of HTTP
  // snapshots and simulate a dropped connection without waiting for a poll.
  await page.addInitScript(() => {
    const streams: Stream[] = []
    class Stream extends EventTarget {
      readyState = 1
      onopen: (() => void) | null = null
      onerror: (() => void) | null = null
      close() { this.readyState = 2 }
      constructor(public url: string) {
        super(); streams.push(this)
        queueMicrotask(() => {
          this.onopen?.()
          this.dispatchEvent(new MessageEvent('stream.ready', { data: JSON.stringify({ after: 40, resumed: false }), lastEventId: '40' }))
        })
      }
    }
    Object.assign(window, { EventSource: Stream, aeon514Streams: streams })
  })
  const data = fixtures()
  data.preferences['list:p-pharos'] = { visible: ['key', 'title', 'status', 'priority', 'assignee', 'progress', 'eta'] }
  const ticket = data.nodes.find(node => node.id === 'n-4')!
  const worker = liveAgent({ project_id: ticket.project, session_id: 's-worker', name: 'Builder', ticket: { id: ticket.id, key: ticket.key, title: ticket.title, project_id: ticket.project } })
  data.live.push(worker)
  ticket.eta = { has_working_session: true, eta_ready_at: new Date(at - 300_000).toISOString(), progress_pct: 60 }
  const revision = ticket.updated_at
  await mockWork(page, data)
  const errors = watchErrors(page)
  await page.goto('/p/PHAROS/tickets')
  const row = page.locator('#row-n-4')
  await expect(row.locator('.ticket-workers')).toContainText('Builder')
  await expect(row.locator('.eta-cell')).toHaveClass(/overdue/)

  async function hint(kind: string, id: number, reconnect = false) {
    await page.evaluate(({ kind, id, reconnect }) => {
      const streams = (window as unknown as { aeon514Streams: { readyState: number; onerror: (() => void) | null; onopen: (() => void) | null; dispatchEvent(event: Event): void }[] }).aeon514Streams
      for (const stream of streams.filter(stream => stream.readyState !== 2)) {
        if (reconnect) {
          stream.readyState = 0; stream.onerror?.(); stream.readyState = 1; stream.onopen?.()
          stream.dispatchEvent(new MessageEvent('stream.ready', { data: JSON.stringify({ after: id, resumed: false }), lastEventId: String(id) }))
        } else {
          stream.dispatchEvent(new MessageEvent(kind, { data: JSON.stringify({ id, type: kind, after: { ticket_node_id: 'n-4', project_id: 'p-pharos' } }), lastEventId: String(id) }))
        }
      }
    }, { kind, id, reconnect })
  }

  worker.phase = 'stopped'; worker.stopped_at = new Date(at).toISOString(); worker.finished = true
  ticket.eta = { finished: true, progress_pct: 100 }
  await hint('harness.stopped', 41)
  await expect(row.locator('.ticket-workers')).toHaveCount(0)
  await expect(row.locator('.eta-cell')).toContainText('Done')
  await expect(row.locator('.eta-cell')).not.toHaveClass(/overdue/)

  data.live.push(liveAgent({ ...worker, session_id: 's-new', name: 'New builder', phase: 'working', stopped_at: null, finished: false }))
  ticket.eta = { has_working_session: true, progress_pct: 10 }
  await hint('harness.registered', 42)
  await expect(row.locator('.ticket-workers')).toContainText('New builder')
  ticket.eta = { has_working_session: true, eta_ready_at: new Date(at + 600_000).toISOString(), progress_pct: 40 }
  await hint('harness.heartbeat', 43)
  await expect(row.locator('.eta-cell')).toContainText('~10 min')
  await expect(row.locator('.progress-read')).toHaveAttribute('aria-label', '40% done')

  // The stop occurred while the connection was away: no stop hint is delivered.
  data.live.splice(0)
  ticket.eta = { finished: false }
  await hint('', 50, true)
  await expect(row.locator('.ticket-workers')).toHaveCount(0)
  await expect(row.locator('.eta-cell')).toHaveCount(0)
  expect(ticket.updated_at).toBe(revision)
  expect(errors).toEqual([])
})
