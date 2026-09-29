// SPDX-License-Identifier: AGPL-3.0-only
import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'

// Started by TestSessionChatBrowser with an isolated DB, loopback API and CLI.
// The remaining app shell is mocked; every chat request uses that real API.
test.skip(!process.env.AEON_CHAT_CLI, 'run AEON_SESSION_CHAT_BROWSER=1 go test ./internal/cli -run ^TestSessionChatBrowser$')
for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`person asks and CLI answers in the same thread: ${theme} ${width}`, async ({ page, playwright }) => {
    test.setTimeout(60_000)
    await page.clock.install({ time: new Date() })
    const project = process.env.AEON_CHAT_PROJECT!
    const agent = process.env.AEON_CHAT_AGENT!
    const backend = process.env.AEON_URL!
    const personAPI = await playwright.request.newContext({ baseURL: backend })
    try {
      const login = await personAPI.post('/api/auth/dev-login', { data: { email: 'admin@example.com', tenant: 'aeon' } })
      expect(login.status()).toBe(200)
      const identity = await login.json()
      const person = identity.principal.id as string
      const vendor = `session-chat-${theme}-${width}-native-reference`
      const registered = await personAPI.post(`/api/projects/${project}/harness-sessions`, { data: {
        agent_principal_id: agent, harness: 'claude', host: 'local-test', management_mode: 'unmanaged', role: 'coordinator',
        display_label: 'Release lead', harness_session_ref: vendor, worker_lease: `session-chat-${theme}-${width}-synthetic-lease`, advertised_capabilities: ['inbox'],
      } })
      expect(registered.status()).toBe(201)
      const session = await registered.json()
      const work = fixtures(); work.preferences.theme = { choice: theme }
      await mockWork(page, work, { admin: true })
      const data = agentData({ me: person, projects: { pharos: project, aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
      const worker = data.sessions[0]!
      Object.assign(worker, { ...session, run_id: null, agent: { id: agent, name: 'Release lead' }, attention_reasons: [{ scope: 'shared', kind: 'reply', actor: 'agent', count: 10, blocking: false }] })
      data.sessions.splice(1); data.runs.splice(0); data.approvals.splice(0); data.messages.splice(0)
      data.targets.splice(0)
      await mockAgents(page, data)
      await page.route('**/api/me', route => route.fulfill({ json: identity }))
      await page.route('**/api/**', async route => {
        const url = new URL(route.request().url())
        if (url.pathname.endsWith('/message-targets')) return route.fulfill({ json: [{ principal_id: agent, address: agent, enabled: true, role: 'primary' }] })
        if (!url.pathname.includes('/messages') && !url.pathname.endsWith('/read-marker') && url.pathname !== '/api/inbox/message-status') return route.fallback()
        const response = await personAPI.fetch(url.pathname + url.search, { method: route.request().method(), data: route.request().postData() ?? undefined, headers: { 'Content-Type': 'application/json' } })
        await route.fulfill({ response })
      })
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await page.goto(`/agents/${session.id}?tab=messages`)
      const panel = page.getByRole('complementary', { name: 'Session details' })
      const input = panel.getByRole('textbox', { name: 'Message to Release lead' })
      await input.fill('Are the release checks ready?')
      await panel.getByRole('button', { name: 'Send', exact: true }).click()
      await expect(panel.getByText('Are the release checks ready?', { exact: true })).toBeVisible()
      const questions = await personAPI.get(`/api/projects/${project}/messages?session=${session.id}`)
      const question = (await questions.json()).items[0]
      expect(question.recipient_session_id).toBe(session.id)
      const sessionFile = resolve(process.env.AEON_CHAT_DIR!, `${theme}-${width}.id`)
      writeFileSync(sessionFile, session.id + '\n', { mode: 0o600 })
      const cliEnv = { ...process.env, AEON_SESSION_ID: '', AEON_SESSION_FILE: theme === 'light' ? sessionFile : '', AEON_SESSION_STATE_DIR: '', CLAUDE_CODE_SESSION_ID: vendor, CODEX_SESSION_ID: '', CODEX_THREAD_ID: '' }
      const tell = (body: string, extra: string[] = []) => {
        // No session flag, and the first answer has no reply flag either.
        execFileSync(process.env.AEON_CHAT_CLI!, ['tell', person, '--project', 'AEON', '-m', body, ...extra], { env: cliEnv, stdio: 'pipe' })
      }
      tell('Yes. The release checks are ready.')
      tell('The API and browser checks passed.', ['--reply-to', question.id])
      await page.clock.runFor(21_000)
      const thread = panel.getByRole('list', { name: 'Messages', exact: true })
      await expect(thread.locator('.msg-body')).toHaveText(['Are the release checks ready?', 'Yes. The release checks are ready.', 'The API and browser checks passed.'])
      await expect(thread.locator('.msg').first().getByText('Answered', { exact: true })).toBeVisible()
      await expect(thread.locator('.msg').last()).toBeInViewport()
      await expect(input).toBeInViewport()
      await expect(panel.getByText(/Shared inbox:|Other sessions of/)).toHaveCount(0)
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
      const actual = await personAPI.get(`/api/projects/${project}/messages?session=${session.id}`)
      const items = (await actual.json()).items
      expect(items.slice(1).every((m: { sender_session_id: string }) => m.sender_session_id === session.id)).toBe(true)
      // The real read-marker handler accepts the newest reply.
      await page.clock.runFor(1_700)
      await expect.poll(async () => (await (await personAPI.get(`/api/projects/${project}/harness-sessions/${session.id}/read-marker`)).json()).last_read_message_id).toBe(items.at(-1).id)
      const shots = process.env.SESSION_MESSAGES_SHOTS
      if (shots) { mkdirSync(shots, { recursive: true }); await page.screenshot({ path: resolve(shots, `conversation-${theme}-${width}.png`) }) }
    } finally { await personAPI.dispose() }
  })
}
