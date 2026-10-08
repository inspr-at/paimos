// SPDX-License-Identifier: AGPL-3.0-only
// AEON-280: whether a session is listening, and Sent → Delivered → Read or a loud
// "Not delivered" on the viewer's own posts, updated live from delivery events.
// Screenshots: DELIVERY_SHOTS=<dir> npx playwright test -c playwright.ui.config.ts delivery-guarantee.spec.ts
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

const now = Date.parse('2026-09-29T06:00:00Z')
const iso = (minutesAgo: number) => new Date(now - minutesAgo * 60_000).toISOString()
const row = (page: Page, id: string) => page.locator(`[data-row="s:${id}"]`)
const panelOf = (page: Page) => page.getByRole('complementary', { name: 'Session details' })
async function signal(page: Page, name: string) {
  await page.evaluate(name => window.dispatchEvent(new CustomEvent('test:agents-signal', { detail: name })), name)
}
function shot(page: Page, name: string) {
  const dir = process.env.DELIVERY_SHOTS
  if (!dir) return
  mkdirSync(dir, { recursive: true })
  return page.screenshot({ path: resolve(dir, `${name}.png`) })
}

type Status = 'sent' | 'delivered' | 'read' | 'not_delivered'
async function setup(page: Page, theme: 'light' | 'dark' = 'light') {
  await page.clock.install({ time: now })
  const work = fixtures(); work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  await page.addInitScript(() => {
    class Stream extends EventTarget {
      onopen: ((event: Event) => void) | null = null
      onerror: ((event: Event) => void) | null = null
      closed = false
      receive = (event: Event) => { this.dispatchEvent(new Event((event as CustomEvent<string>).detail)) }
      constructor() {
        super()
        window.addEventListener('test:agents-signal', this.receive)
        setTimeout(() => { if (!this.closed) this.onopen?.(new Event('open')) }, 0)
      }
      close() { this.closed = true; window.removeEventListener('test:agents-signal', this.receive) }
    }
    Object.assign(window, { EventSource: Stream })
  })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {} })
  data.sessions.splice(3); data.runs.splice(0); data.approvals.splice(0)
  const [listening, quiet, never] = data.sessions as Record<string, unknown>[]
  Object.assign(listening!, { display_label: 'release-lead', run_id: null, inbox_seen_at: new Date(now - 20_000).toISOString(), inbox_seen_via: 'stream' })
  Object.assign(quiet!, { display_label: 'fleet-worker', run_id: null, inbox_seen_at: iso(12), inbox_seen_via: 'hook' })
  Object.assign(never!, { display_label: 'scout', run_id: null })
  const worker = listening as { id: string; agent_principal_id: string }
  const template = data.messages[1]!
  const bodies = ['Take the release checks next.', 'On it, running the chat suite first.', 'Ship the counts behind the gate.', 'Please rebase before you push.', 'Stop after the migration lands.']
  const statuses: Record<string, Status> = {}
  const plan: Status[] = ['read', 'delivered', 'not_delivered', 'sent']
  let mine = 0
  const messages = bodies.map((body, i) => {
    const own = i !== 1
    const id = `3e000000-0000-4000-8000-${String(100 + i).padStart(12, '0')}`
    if (own) statuses[id] = plan[mine++]!
    return {
      ...template, id, sent_event_id: 200 + i, body, created_at: iso(10 - i * 2),
      ...(own
        ? { sender_principal_id: me.id, recipient_principal_id: worker.agent_principal_id, to: 'claude:camy', recipient_session_id: worker.id, sender_session_id: undefined, sender_label: 'Markus' }
        : { sender_principal_id: worker.agent_principal_id, recipient_principal_id: me.id, to: 'paimos:markus', sender_session_id: worker.id, sender_label: 'release-lead' }),
    } as typeof template
  })
  data.messages.splice(0, data.messages.length, ...messages)
  await page.addInitScript(() => localStorage.setItem('aeon.session-tab', 'messages'))
  await mockAgents(page, data)
  const asked: string[][] = []
  await page.route('**/api/inbox/message-status?*', route => {
    const ids = new URL(route.request().url()).searchParams.get('ids')!.split(',')
    asked.push(ids)
    const items = ids.filter(id => statuses[id]).map(id => {
      const status = statuses[id]!
      return {
        message_id: id, status, ...(status === 'not_delivered' ? { reason: 'session_ended' } : {}),
        delivered_at: status === 'delivered' || status === 'read' ? iso(3) : null, read_at: status === 'read' ? iso(2) : null, deliver_by: new Date(now + 180_000).toISOString(),
      }
    })
    return route.fulfill({ json: { items } })
  })
  return { data, worker, quiet: quiet as { id: string }, never: never as { id: string }, statuses, messages, asked }
}

for (const theme of ['light', 'dark'] as const) {
  for (const width of [1600, 390]) {
    test(`${theme} ${width}: listening status in the list and the panel`, async ({ page }) => {
      const { worker, quiet, never } = await setup(page, theme)
      await page.setViewportSize({ width, height: width > 600 ? 1000 : 844 })
      await page.goto('/agents')
      await expect(row(page, worker.id).locator('[data-listening="yes"]:visible')).toContainText('Listening')
      await expect(row(page, quiet.id).locator('[data-listening="no"]:visible')).toContainText('Not listening')
      await expect(row(page, quiet.id).locator('[data-listening="no"]:visible')).toHaveAttribute('data-tip', /^Last pulled its inbox 12m ago through a turn hook\./)
      await expect(row(page, never.id).locator('[data-listening="no"]:visible')).toHaveAttribute('data-tip', /Has not pulled its inbox yet/)
      expect(await page.evaluate(() => document.scrollingElement!.scrollWidth <= document.scrollingElement!.clientWidth)).toBe(true)
      await shot(page, `list-${width}-${theme}`)
      await page.goto(`/agents/${quiet.id}`)
      await panelOf(page).getByRole('tab', { name: 'Overview' }).click()
      await expect(panelOf(page).locator('.now-listen')).toContainText('Not listening · last pulled 12m ago')
      // No coloured edge accent marks the state (AGENTS.md rule 11).
      const edges = await panelOf(page).locator('.now-listen .listening').evaluate(el => { const s = getComputedStyle(el); return [s.borderLeftWidth, s.borderTopWidth] })
      expect(edges).toEqual(['0px', '0px'])
      await shot(page, `panel-${width}-${theme}`)
    })

    test(`${theme} ${width}: own posts read Sent, Delivered, Read or Not delivered, live`, async ({ page }) => {
      const { worker, statuses, messages, asked } = await setup(page, theme)
      await page.setViewportSize({ width, height: width > 600 ? 1000 : 844 })
      await page.goto(`/agents/${worker.id}`)
      const panel = panelOf(page)
      const mine = panel.locator('.msg.mine')
      await expect(mine).toHaveCount(4)
      await expect(mine.nth(0).locator('.delivery')).toHaveText('Read')
      await expect(mine.nth(0).locator('.delivery')).toHaveAttribute('data-tip', /^Read by the session · /)
      await expect(mine.nth(1).locator('.delivery')).toHaveText('Delivered')
      await expect(mine.nth(2).locator('.undelivered > span')).toHaveText('Not delivered · the session ended')
      await expect(mine.nth(2).getByRole('button', { name: 'Retry', exact: true })).toBeVisible()
      await expect(mine.nth(2).locator('.delivery')).toHaveCount(0)
      await expect(mine.nth(3).locator('.delivery')).toHaveText('Sending')
      // Only the viewer's own posts are asked for.
      const own = new Set(messages.filter(m => m.sender_principal_id === me.id).map(m => m.id))
      expect(asked.flat().every(id => own.has(id))).toBe(true)
      await shot(page, `chat-${width}-${theme}`)
      // A delivery event re-reads only unfinished posts, and the tick follows.
      const last = messages.at(-1)!.id
      statuses[last] = 'delivered'
      const before = asked.length
      await signal(page, 'inbox.message_fetched')
      await expect(mine.nth(3).locator('.delivery')).toHaveText('Delivered')
      const reread = asked.slice(before).flat()
      expect(reread).toContain(last)
      expect(reread).not.toContain(messages[0]!.id) // Read is final.
      statuses[last] = 'read'
      await signal(page, 'inbox.receipt_handed_off')
      await expect(mine.nth(3).locator('.delivery.read')).toHaveText('Read')
      expect(await page.evaluate(() => document.scrollingElement!.scrollWidth <= document.scrollingElement!.clientWidth)).toBe(true)
    })
  }
}
