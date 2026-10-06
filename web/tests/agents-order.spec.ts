// SPDX-License-Identifier: AGPL-3.0-only
// AEON-468: /agents rows keep their place across live refreshes. Default order is
// state, then start time; a column header orders by that column, remembered per
// viewer, until "Default order" restores it.
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

const sid = (n: number) => `5e000000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`
const storageKey = `aeon.agents.sort.t1.${me.id}`
async function signal(page: Page, name: string) {
  await page.evaluate(name => window.dispatchEvent(new CustomEvent('test:agents-signal', { detail: name })), name)
}
// Session ids of the visible rows, top to bottom.
const rowOrder = (page: Page) => page.locator('.row[data-row]').evaluateAll(rows => rows.map(r => r.getAttribute('data-row')!.slice(2)))

async function setup(page: Page) {
  await mockWork(page, fixtures(), { admin: true })
  await page.addInitScript(() => {
    class Stream extends EventTarget {
      onopen: ((event: Event) => void) | null = null
      onerror: ((event: Event) => void) | null = null
      closed = false
      receive = (event: Event) => this.dispatchEvent(new Event((event as CustomEvent<string>).detail))
      constructor() {
        super()
        window.addEventListener('test:agents-signal', this.receive)
        setTimeout(() => { if (!this.closed) this.onopen?.(new Event('open')) }, 0)
      }
      close() { this.closed = true; window.removeEventListener('test:agents-signal', this.receive) }
    }
    Object.assign(window, { EventSource: Stream })
  })
  const now = Date.now()
  const ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString()
  // Four live sessions: start order (s3, s1, s2, s4) differs from ticket order (s2, s3, s1, s4).
  const tickets = { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }
  const nodes: Record<string, { key: string; title: string }> = {
    'n-1': { key: 'PHAROS-30', title: 'Fleet list' }, 'n-2': { key: 'PHAROS-4', title: 'Restore flow' },
    'n-a1': { key: 'PHAROS-12', title: 'Web shell' }, 'n-5': { key: 'PHAROS-100', title: 'Release notes' },
  }
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-pharos', pai: 'p-pharos' }, tickets, nodes: { 'p-pharos': { key: 'PHAROS', title: 'Pharos' }, ...nodes } })
  const template = data.sessions[0]!
  data.sessions.splice(0, data.sessions.length, ...[
    { n: 1, node: 'n-1', created: 72 }, { n: 2, node: 'n-2', created: 26 }, { n: 3, node: 'n-a1', created: 140 }, { n: 4, node: 'n-5', created: 3 },
  ].map(({ n, node, created }) => ({
    ...template, id: sid(n), role: 'worker', run_id: null, ticket_node_id: node, ticket: { id: node, ...nodes[node]! },
    display_label: `Worker ${n}`, phase: 'working', activity: 'busy', heartbeat_at: ago(0.2), created_at: ago(created),
  })))
  data.approvals.splice(0)
  data.messages.splice(0)
  const calls = await mockAgents(page, data)
  // Every session beats again in a new order: newest beat moves around.
  let round = 0
  async function liveRefresh() {
    round++
    data.sessions.forEach((s, i) => { s.heartbeat_at = new Date(Date.now() - ((round * 3 + i * 5) % 7) * 6_000).toISOString() })
    const before = calls.filter(c => c.path === '/api/harness-sessions').length
    await signal(page, 'harness.bound')
    await expect.poll(() => calls.filter(c => c.path === '/api/harness-sessions').length).toBeGreaterThan(before)
  }
  return { data, liveRefresh }
}

test('default order is start time and survives live refreshes with shuffled heartbeats', async ({ page }) => {
  const { liveRefresh } = await setup(page)
  await page.goto('/agents')
  const expected = [sid(3), sid(1), sid(2), sid(4)]
  await expect.poll(() => rowOrder(page)).toEqual(expected)
  await expect(page.getByRole('columnheader', { name: 'State' })).toHaveAttribute('aria-sort', 'ascending')
  await expect(page.getByRole('button', { name: 'Default order' })).toHaveCount(0)
  for (let i = 0; i < 2; i++) {
    await liveRefresh()
    expect(await rowOrder(page)).toEqual(expected)
  }
})

test('clicking Ticket orders rows by ticket, stays put on refresh, is remembered, and resets', async ({ page }) => {
  const { liveRefresh } = await setup(page)
  await page.goto('/agents')
  await expect.poll(() => rowOrder(page)).toEqual([sid(3), sid(1), sid(2), sid(4)])
  const ticket = page.getByRole('columnheader', { name: 'Ticket' })
  await ticket.getByRole('button').click()
  const byTicket = [sid(2), sid(3), sid(1), sid(4)] // PHAROS-4, -12, -30, -100
  await expect.poll(() => rowOrder(page)).toEqual(byTicket)
  await expect(ticket).toHaveAttribute('aria-sort', 'ascending')
  await expect(page.getByRole('columnheader', { name: 'State' })).not.toHaveAttribute('aria-sort', /.+/)
  await liveRefresh()
  await liveRefresh()
  expect(await rowOrder(page)).toEqual(byTicket)

  // Keyboard: the header is a button; Enter reverses the order.
  await ticket.getByRole('button').focus()
  await page.keyboard.press('Enter')
  await expect.poll(() => rowOrder(page)).toEqual([...byTicket].reverse())
  await expect(ticket).toHaveAttribute('aria-sort', 'descending')

  // Remembered for this viewer across a reload.
  expect(await page.evaluate(key => localStorage.getItem(key), storageKey)).toBe(JSON.stringify({ key: 'ticket', dir: 'desc' }))
  await page.reload()
  await expect.poll(() => rowOrder(page)).toEqual([...byTicket].reverse())

  await page.getByRole('button', { name: 'Default order' }).click()
  await expect.poll(() => rowOrder(page)).toEqual([sid(3), sid(1), sid(2), sid(4)])
  await expect(page.getByRole('columnheader', { name: 'State' })).toHaveAttribute('aria-sort', 'ascending')
  await expect(page.getByRole('button', { name: 'Default order' })).toHaveCount(0)
  expect(await page.evaluate(key => localStorage.getItem(key), storageKey)).toBeNull()
})

test('sorting still works when storage refuses the choice', async ({ page }) => {
  await page.addInitScript(() => {
    for (const name of ['getItem', 'setItem', 'removeItem'] as const) {
      const original = Storage.prototype[name] as (...args: string[]) => unknown
      Storage.prototype[name] = function (this: Storage, ...args: string[]) {
        if (args[0]?.startsWith('aeon.agents.sort.')) throw new DOMException('blocked', 'QuotaExceededError')
        return original.apply(this, args)
      } as never
    }
  })
  await setup(page)
  await page.goto('/agents')
  await expect.poll(() => rowOrder(page)).toEqual([sid(3), sid(1), sid(2), sid(4)])
  await page.getByRole('columnheader', { name: 'Running' }).getByRole('button').click()
  await expect.poll(() => rowOrder(page)).toEqual([sid(4), sid(2), sid(1), sid(3)])
})

test('the Heartbeat sort explains that fresh beats tie, for screen readers too', async ({ page }) => {
  await setup(page)
  await page.goto('/agents')
  await expect.poll(() => rowOrder(page)).toHaveLength(4)
  const help = 'Beats under 3 minutes old count as equal; start time and id then decide their order.'
  const beat = page.getByRole('columnheader', { name: 'Heartbeat' }).getByRole('button')
  await expect(beat).toHaveAccessibleDescription(help)
  await expect(beat).toHaveAttribute('data-tip', new RegExp(help.slice(0, 30)))
})

test('at 390 px the compact sort control orders by Ticket, reverses, and restores the default', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page)
  await page.goto('/agents')
  const start = [sid(3), sid(1), sid(2), sid(4)]
  await expect.poll(() => rowOrder(page)).toEqual(start)
  await expect(page.locator('.thead')).toBeHidden()
  // The Sessions head's sort tool opens the same select and direction (AEON-784).
  const tool = page.locator('.sessions .sort-tool')
  await expect(tool).toHaveAccessibleName(/^Sort sessions: State, ascending/)
  await tool.click()
  const bar = page.getByRole('group', { name: 'Sort sessions' })
  const pick = bar.getByRole('combobox', { name: 'Sort' })
  await expect(pick).toHaveValue('state')
  await expect(bar.getByRole('button', { name: /Order ascending/ })).toBeVisible()

  await pick.selectOption('ticket')
  const byTicket = [sid(2), sid(3), sid(1), sid(4)]
  await expect.poll(() => rowOrder(page)).toEqual(byTicket)
  await expect(pick).toHaveValue('ticket')

  // Keyboard: the direction button is reachable and reverses the order.
  await bar.getByRole('button', { name: /Order ascending/ }).focus()
  await page.keyboard.press('Enter')
  await expect.poll(() => rowOrder(page)).toEqual([...byTicket].reverse())
  await expect(bar.getByRole('button', { name: /Order descending/ })).toBeVisible()

  // Heartbeat has no column on phones; choosing it says how fresh beats compare.
  await pick.selectOption('heartbeat')
  await expect(bar.getByText('Beats under 3 minutes old count as equal', { exact: false })).toBeVisible()
  await expect(pick).toHaveAccessibleDescription(/Beats under 3 minutes old/)

  await page.keyboard.press('Escape')
  await expect(bar).toHaveCount(0)
  await expect(tool).toHaveAccessibleName(/^Sort sessions: Heartbeat, ascending/)
  await page.getByRole('button', { name: 'Default order' }).click()
  await expect.poll(() => rowOrder(page)).toEqual(start)
  await tool.click()
  await expect(pick).toHaveValue('state')
})
