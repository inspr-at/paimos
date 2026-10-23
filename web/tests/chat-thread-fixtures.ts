// SPDX-License-Identifier: AGPL-3.0-only
// The AEON-1438/1687 conversation shared by interaction and P02 reference tests.
import type { Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

export const CHAT_THREAD_NOW = Date.parse('2026-10-16T06:04:00Z')
const conversation = '7c000000-0000-4000-8000-000000001438'
export const answered = '55adee55-145a-4c2e-9b1f-0c7d2e1a9f30'
export const waiting = '66bdff66-256b-4d3f-8c2a-1d8e3f2b0a41'
export const longWord = 'sha256:4f9c2a7be1d04c55a3a6c7f1d2e9b8a7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e0f9a8b7c6d5e4f3a2b1'

export async function mockChatThread(page: Page, options: { theme?: 'light' | 'dark'; session?: Record<string, unknown>; today?: boolean; lineProbe?: boolean; now?: number; clock?: boolean; reference?: boolean } = {}) {
  const now = options.now ?? CHAT_THREAD_NOW
  // A scripted EventSource: the agents feed opens quietly; the chat-live stream takes frames from the test.
  await page.addInitScript(() => {
    class Stream extends EventTarget {
      onopen: ((event: Event) => void) | null = null
      onerror: ((event: Event) => void) | null = null
      readyState = 1
      constructor(url: string) {
        super()
        queueMicrotask(() => this.onopen?.(new Event('open')))
        if (url.includes('/live')) {
          ((window as unknown as { __live?: Stream[] }).__live ??= []).push(this)
          queueMicrotask(() => this.dispatchEvent(new MessageEvent('ready', { data: '{"contract":"chat-live-v1"}' })))
        }
      }
      close() { this.readyState = 2 }
    }
    Object.assign(window, { EventSource: Stream })
  })
  if (options.clock !== false) await page.clock.install({ time: now })
  const work = fixtures(); work.preferences.theme = { choice: options.theme ?? 'light' }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {} })
  const worker = data.sessions[0]!
  Object.assign(worker, { display_label: 'AEON-LEAD', run_id: null, activity: 'idle', harness: 'claude', management_mode: 'managed', advertised_capabilities: ['inbox', 'status', 'steer', 'interrupt', 'stop'] }, options.session ?? {})
  data.sessions.splice(1); data.runs.splice(0); data.approvals.splice(0)
  const template = data.messages[1]!
  let event = 300
  const at = (minutesAgo: number) => new Date(now - minutesAgo * 60_000).toISOString()
  const agentPost = (body: string, minutesAgo: number, fields: Record<string, unknown> = {}) => ({ ...template, id: `3e000000-0000-4000-8000-${String(++event).padStart(12, '0')}`, sent_event_id: event, body, created_at: at(minutesAgo), sender_principal_id: worker.agent_principal_id, recipient_principal_id: me.id, to: 'paimos:markus', sender_session_id: worker.id, sender_label: 'AEON-LEAD', ...fields }) as typeof template
  const myPost = (body: string, minutesAgo: number, fields: Record<string, unknown> = {}) => ({ ...template, id: `3e000000-0000-4000-8000-${String(++event).padStart(12, '0')}`, sent_event_id: event, body, created_at: at(minutesAgo), sender_principal_id: me.id, recipient_principal_id: worker.agent_principal_id, to: 'claude:camy', recipient_session_id: worker.id, sender_session_id: undefined, sender_label: 'Markus', ...fields }) as typeof template
  const yesterday = options.today ? [] : Array.from({ length: 12 }, (_, i) => i % 3 === 2
    ? myPost(`Yesterday's note ${i + 1}: keep the release gate as it is.`, 60 * 20 - i * 6)
    : agentPost(`Yesterday's report ${i + 1}: the nightly run is green and the queue is empty.`, 60 * 20 - i * 6))
  const deskRoot = (question: string, minutesAgo: number) => agentPost(`Decision Desk question ${question}; read through ask status`, minutesAgo, { is_action_request: true, expects_reply: true, human_resolution_outcome: 'resolved' })
  const root = deskRoot(answered, 20)
  const messages = [
    ...yesterday,
    agentPost('Morning, Markus. Overnight on `main`:\n\n- Merged 4 PRs; the nightly gate is green since 06:10.\n- AEON-1182 passed review and waits for its merge.\n\nThe merge question for AEON-1182 is on the Desk.', 23),
    root,
    myPost(JSON.stringify({ type: 'question.answer', question_id: answered, answer_id: 'a1', revision: 2, answer: 'merge now', replaces: '', asker_id: 'k1' }), 20, { reply_to: root.id }),
    agentPost('Merged AEON-1182. Shall I start the release check for 135 now, or wait until the CI fixes are in?', 6, { is_action_request: true, expects_reply: true, human_resolution_outcome: 'resolved' }),
    deskRoot(waiting, 5),
    agentPost(`The image digest is ${longWord} and matches the staging build.`, 4),
    myPost('Start it now. Keep the CI fixes in their own PR.', 2),
    // AEON-1286: two wrapped paragraphs, one with an inline code chip on its first line.
    ...(options.lineProbe ? [
      agentPost('Without a chip, the nightly gate stayed green through the morning and the queue held only the release notes already written for the train.', 1),
      agentPost('With a chip, `origin/main` stayed green through the morning and the queue held only the release notes already written for the train.', 0),
    ] : []),
  ]
  // The reference uses these same Desk deliveries, answer and latest reply.
  // This short subset keeps the day, both cards and the sent message visible
  // together in the phone's reading area above its unchanged composer.
  if (options.reference) {
    const last = messages.at(-1)!.id
    messages.splice(0, messages.length, ...messages.filter(message => message.id === last || message.reply_to === root.id || message.body.startsWith('Decision Desk question ')))
  }
  data.messages.splice(0, data.messages.length, ...messages)
  const calls = await mockAgents(page, data)
  // A person who may decide Desk questions: an open one waits for them.
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions.push('questions.read', 'questions.decide')
    return route.fulfill({ json: answer })
  })
  const ownIds = messages.filter(m => m.sender_principal_id === me.id).map(m => m.id)
  const latest = ownIds.at(-1)!
  await page.route('**/api/inbox/message-status?*', route => {
    const ids = new URL(route.request().url()).searchParams.get('ids')!.split(',')
    return route.fulfill({ json: { items: ids.filter(id => ownIds.includes(id) || data.sent.some(m => m.id === id)).map(id => ({ message_id: id, status: id === latest ? 'read' : 'delivered', delivered_at: at(1), read_at: id === latest ? at(1) : null, deliver_by: at(-4) })) } })
  })
  const question = (id: string, text: string, state: 'open' | 'answered') => ({
    id, project_id: worker.project_id, revision: state === 'answered' ? 2 : 1, state, created_at: at(25), updated_at: at(20), suggested_outcome: 'once', suggestion_reason: 'agent_suggestion', pending: [],
    input: { request_id: `r-${id}`, question: text, options: [{ id: 'yes', title: 'Yes, merge now', answer: 'merge now' }, { id: 'wait', title: 'Wait for the 14:00 train', answer: 'wait' }], meanwhile: 'carries_on' },
    askers: [{ id: 'k1', principal_id: worker.agent_principal_id, session_id: worker.id, reply_root_id: 'root', comment_node_id: 'c1', input: { request_id: `r-${id}`, question: text, options: [], meanwhile: 'carries_on' } }],
    ...(state === 'answered' ? { answer: { id: 'a1', revision: 2, answer: 'merge now', option_id: 'yes', outcome: 'once', decided_by: me.id, created_at: at(20), deliver_after: at(20) } } : {}),
  })
  await page.route('**/api/questions/*', route => {
    const id = decodeURIComponent(new URL(route.request().url()).pathname.split('/').at(-1)!)
    if (id === answered) return route.fulfill({ json: question(id, 'Merge AEON-1182 now, or wait for the 14:00 release train?', 'answered') })
    if (id === waiting) return route.fulfill({ json: question(id, 'Ship the 135 notes as they are, or wait for the copy gate?', 'open') })
    return route.fulfill({ status: 404, json: { error: 'not found' } })
  })
  await page.route(/\/api\/chat-threads\/[^/]+\/messages(\?|$)/, route => route.fulfill({ json: { contract: 'chat-v1', items: [], has_more_before: false, has_more_after: false, before_cursor: null, after_cursor: null, snapshot_cursor: 'c', migration_epoch: '0' } }))
  await page.route('**/api/projects/*/harness-sessions/*/native-context', route => route.fulfill({ status: 503, json: { reason: 'feature_disabled', request_id: null, retryable: false } }))
  await page.route('**/api/chat-sessions/*/thread', route => route.fulfill({ json: { contract: 'chat-v1', conversation_id: conversation, role: { contract: 'chat-v1' }, revision: '2', binding_epoch: '1', readiness: { state: 'offline', capabilities: [], reason: 'session_offline' } } }))
  return { worker, data, calls, messages, latest }
}
