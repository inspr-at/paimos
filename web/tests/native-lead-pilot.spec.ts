// SPDX-License-Identifier: AGPL-3.0-only
// Offline qualification of the existing conversation surface. Server grant,
// native reader, terminal publication and live soak remain I1/coordinator work.
import { expect, test } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { controlStability } from './control-stability'

for (const theme of ['light', 'dark'] as const) for (const width of [390, 1024, 1440]) {
  test(`AEON-1287 LEAD conversation ${theme} ${width}`, async ({ page }, testInfo) => {
    // Risk: accepting an input is shown as consumption, the final lands in a
    // different session, or growing conversation content moves the composer.
    const now = Date.parse('2026-10-13T08:00:00Z')
    await page.clock.install({ time: now })
    await page.addInitScript(() => {
      class Stream extends EventTarget {
        onopen: ((event: Event) => void) | null = null
        onerror: ((event: Event) => void) | null = null
        receive = (event: Event) => this.dispatchEvent(new MessageEvent((event as CustomEvent<string>).detail, { data: '{}' }))
        constructor() {
          super()
          window.addEventListener('test:lead-signal', this.receive)
          queueMicrotask(() => this.onopen?.(new Event('open')))
        }
        close() { window.removeEventListener('test:lead-signal', this.receive) }
      }
      Object.assign(window, { EventSource: Stream })
    })
    const work = fixtures()
    work.preferences.theme = { choice: theme }
    await mockWork(page, work, { admin: true })
    const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
    const lead = data.sessions[0]!
    const messageTemplate = data.messages[1]!
    Object.assign(lead, {
      display_label: 'Native LEAD', run_id: null, role: 'coordinator', activity: 'idle', management_mode: 'managed',
      advertised_capabilities: ['status', 'inbox', 'interrupt', 'stop', 'chat'],
    })
    data.sessions.splice(1); data.runs.splice(0); data.approvals.splice(0); data.messages.splice(0)
    const calls = await mockAgents(page, data)
    let consumed = false
    await page.route('**/api/inbox/message-status?*', route => {
      const ids = new URL(route.request().url()).searchParams.get('ids')?.split(',') ?? []
      return route.fulfill({ json: { items: ids.map(id => ({ message_id: id, status: consumed ? 'read' : 'sent', delivered_at: consumed ? new Date(now).toISOString() : null, read_at: consumed ? new Date(now).toISOString() : null, deliver_by: new Date(now + 240_000).toISOString() })) } })
    })
    await page.setViewportSize({ width, height: 900 })
    await page.goto(`/agents/${lead.id}?tab=messages`)
    const panel = page.getByRole('complementary', { name: 'Session details' })
    const composer = panel.getByRole('textbox', { name: 'Message to Native LEAD' })
    const send = panel.getByRole('button', { name: /^(Send|After this turn)$/, exact: true })
    await expect(composer).toBeVisible()
    const prompt = 'Bitte den vollständigen Übergabebericht und die ausführliche Freigabeprüfung berücksichtigen.'
    await composer.fill(prompt)
    await send.click()
    await expect(panel.getByText(prompt, { exact: true })).toBeVisible()
    expect(data.sent).toHaveLength(1)
    const input = data.sent[0]!
    expect(input.recipient_session_id).toBe(lead.id)
    expect(input.status).toBe('accepted')
    await expect(panel.getByText('Read', { exact: true })).toHaveCount(0)

    const guard = await controlStability(page, { frame: panel, composer, send, chat: panel.getByRole('tab', { name: /Chat/ }) })
    await guard.check(async () => {
      // Only this consumed input receives the explicit final. Retaining a
      // foreign final in the backend makes a wrongly scoped thread read fail.
      consumed = true
      const final = 'Der Übergabebericht ist vollständig. Die Freigabe bleibt bis zur bestätigten Prozessbeendigung gesperrt.'
      data.messages.push({ ...messageTemplate, id: 'lead-final', project: lead.project_id, sender_session_id: lead.id, sender_principal_id: lead.agent_principal_id, recipient_principal_id: me.id, recipient_session_id: undefined, reply_to: input.id, sent_event_id: 1001, body: final, created_at: new Date(now + 1).toISOString() })
      data.messages.push({ ...messageTemplate, id: 'foreign-final', project: lead.project_id, sender_session_id: 'foreign-generation', reply_to: null, body: 'This belongs to another conversation.', sent_event_id: 1002 })
      await page.evaluate(() => window.dispatchEvent(new CustomEvent('test:lead-signal', { detail: 'inbox.compat_sent' })))
      await expect(panel.getByText(final, { exact: true })).toBeVisible()
      await expect(panel.getByText('This belongs to another conversation.', { exact: true })).toHaveCount(0)
      await expect(panel.getByText('Read', { exact: true })).toBeVisible()
    })
    guard.done()
    expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/messages'))).toHaveLength(1)
    expect(calls.some(call => call.method === 'GET' && call.path.endsWith('/messages') && call.query.get('session') === lead.id)).toBe(true)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.screenshot({ path: testInfo.outputPath(`native-lead-${theme}-${width}.png`) })
  })
}
