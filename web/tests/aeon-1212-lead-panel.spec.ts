// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1212 P4 acceptance, adapted from the approved AEON-1196 check.mjs to
// product selectors: zero shift, real options/results, SVG, state badges,
// English, 44px phone targets, keys and no sideways scrolling, both themes.
// Risk: a chat send closes a question; a late decision lands on another person
// or session; a historical gate passes as current; tab/header controls move.
import { test, expect, chromium, type Page } from '@playwright/test'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { copyFile, mkdir, readFile, readdir, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { mockLeadFlow, LEAD_SESSION, LEAD_AGENT, NOW } from './lead-flow-fixtures'
import { me, watchErrors } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import type { Question } from '../src/lib/decisionDeskApi'
import type { ProjectMessage } from '../src/lib/agents'
import type { LeadDecision } from '../src/lib/lead'

const Q = '6e000000-0000-4000-8000-000000000001'
const makeAdmission = (): LeadDecision => ({ event_id: 12, project_id: 'p-pharos', session_id: LEAD_SESSION, recorded_at: '2026-10-06T09:02:00Z', outcome: 'selected', reason_codes: ['gates_ready'], results: [],
  request: { stage: 'admission', outcome: 'selected', reason_codes: ['gates_ready'], attempt: 1 },
  gate_freshness: ['dial', 'harness', 'account_room', 'host_load'].map(kind => ({ kind: kind as 'dial', freshness: 'fresh' })),
})
const makeQuestion = (): Question => ({
  id: Q, project_id: 'p-pharos', revision: 1, state: 'open', input: { request_id: 'ask-one', session_id: LEAD_SESSION,
    question: 'Merge AEON-1182 now, or wait for the 14:00 release train?', meanwhile: 'parked', recommend: 'yes',
    options: [{ id: 'yes', title: 'Yes, merge now', description: 'Merge the reviewed change.', answer: 'Yes' }, { id: 'later', title: 'Not now', description: 'Wait for the release train.', answer: 'Not now' }] },
  suggested_outcome: 'once', suggestion_reason: 'agent_suggestion', askers: [{ id: 'asker', principal_id: 'lead-agent', session_id: LEAD_SESSION, reply_root_id: '', comment_node_id: '', input: { request_id: 'ask-one', question: 'Merge?', options: [], meanwhile: 'parked' } }],
  pending: [], created_at: '2026-10-06T09:39:00Z', updated_at: '2026-10-06T09:39:00Z',
})
async function setup(page: Page, options: { denied?: boolean; question?: boolean; german?: boolean; pinned?: string; last?: string; actions?: boolean; workerMetadata?: { model: string; account: string; computer: string } } = {}) {
  await page.clock.setSystemTime(NOW)
  const world = await mockLeadFlow(page, { lead: 'working', longText: options.german, accountLabels: { claude: 'Claude Max', codex: options.workerMetadata?.account ?? 'ChatGPT Pro' }, hostLabel: options.workerMetadata?.computer })
  const lead = world.state.leads['p-pharos']!
  lead.actions = Object.fromEntries(['start', 'resume', 'pause', 'adopt', 'cancel_adoption', 'remove'].map(action => [action, { available: action === 'pause', blocked_reason: action === 'pause' ? '' : 'ordinary_succession_required' }]))
  if (options.actions === false) delete lead.actions
  if (options.pinned || options.last) world.data.preferences['agents:lead-panel'] = { pinned: options.pinned, last: options.last }
  // The approved lead-chat reference has four ordinary messages before its ask.
  // These remain messages; the independent question below has its own API record.
  const messages: ProjectMessage[] = [
    'Dispatched AEON-1160 · Grouped picker to Codex Sol xhigh on laptop1. Next: AEON-1199 once a slot frees.',
    'Hold AEON-1199 until AEON-1195 lands.',
    'Done. AEON-1199 waits behind AEON-1195; next up is AEON-1205.',
    'AEON-1176 passed review and is in the gate.',
  ].map((body, index) => ({ id: `8e000000-0000-4000-8000-${String(index + 1).padStart(12, '0')}`, sender_principal_id: index === 1 ? me.id : LEAD_AGENT, recipient_principal_id: index === 1 ? LEAD_AGENT : me.id, sender_session_id: index === 1 ? undefined : LEAD_SESSION, recipient_session_id: index === 1 ? LEAD_SESSION : undefined, sender_label: index === 1 ? 'Markus' : 'Claude lead', to: index === 1 ? 'claude:lead' : 'paimos:markus', body, sent_event_id: 100 + index, is_action_request: false, expects_reply: false, delivery_level: 'simple', status: 'accepted', reply_obligation: 'none', created_at: `2026-10-06T09:${[31,33,33,38][index]}:00Z` }))
  const state = { question: makeQuestion(), calls: [] as Record<string, unknown>[], hold: false, fail: false, messages, denied: !!options.denied, admission: [makeAdmission()], historyUnreadable: false, queueUnreadable: false }
  const readMarkers = new Map<string, { last_read_message_id: string | null; last_read_event_id: number }>()
  let release: () => void = () => {}, arrived: () => void = () => {}
  let barrier = Promise.resolve(), started = Promise.resolve()
  function hold() { state.hold = true; barrier = new Promise<void>(resolve => { release = resolve }); started = new Promise<void>(resolve => { arrived = resolve }) }
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions.push('questions.read', 'questions.decide', 'run.create', 'harness.read', 'harness.control', 'inbox.read', 'inbox.write')
    if (state.denied) {
      answer.workspace.permissions = answer.workspace.permissions.filter(p => p !== 'questions.decide')
      if (answer.project) answer.project.permissions = answer.project.permissions.filter(p => p !== 'questions.decide')
    }
    return route.fulfill({ json: answer })
  })
  await page.route('**/api/decision-desk?*', route => route.fulfill({ json: { items: options.question === false || state.question.state !== new URL(route.request().url()).searchParams.get('state') ? [] : [state.question], has_more: false } }))
  await page.route(url => url.pathname === '/api/queue' && url.searchParams.has('project_id'), route => state.queueUnreadable
    ? route.fulfill({ status: 503, json: { error: 'queue store unavailable' } })
    : route.fallback())
  await page.route('**/api/projects/p-pharos/lead-decisions*', route => state.historyUnreadable
    ? route.fulfill({ status: 503, json: { error: 'The admission history is temporarily unavailable.' } })
    : route.fulfill({ json: { items: state.admission.filter(item => item.event_id > Number(new URL(route.request().url()).searchParams.get('after') ?? 0)), next_after: null } }))
  await page.route(url => url.pathname === `/api/questions/${Q}` || url.pathname === `/api/questions/${Q}/decision`, async route => {
    if (route.request().method() === 'GET') return route.fulfill({ json: state.question })
    const body = route.request().postDataJSON() as Record<string, unknown>
    state.calls.push(body); arrived()
    if (state.hold) await barrier
    if (state.fail) { state.fail = false; return route.fulfill({ status: 503, json: { error: 'The record is temporarily unavailable.' } }) }
    if (body.expected_revision !== state.question.revision) return route.fulfill({ status: 409, json: { error: 'Question revision changed.' } })
    state.question = { ...state.question, revision: 2, state: 'answered', answer: { id: 'answer-one', revision: 2, answer: body.option_id === 'yes' ? 'Yes' : 'Not now', option_id: String(body.option_id), outcome: 'once', decided_by: me.id, created_at: NOW, deliver_after: NOW } }
    return route.fulfill({ json: state.question })
  })
  await page.route('**/api/**', route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname
    if (path === '/api/models') return route.fulfill({ json: [
      { id: 'sol', slug: 'sol-medium', model: 'gpt-6-sol', display_name: options.workerMetadata?.model ?? 'Sol', model_version: '6', harness: 'codex', family: 'openai', effort: 'medium', tier: 'standard', enabled: true },
      { id: 'opus', slug: 'opus-high', model: 'Claude Opus 5.5', display_name: 'Opus', model_version: '5.5', harness: 'claude', family: 'anthropic', effort: 'high', tier: 'standard', enabled: true },
    ] })
    // Full-viewport evidence also renders the predecessor's background reads.
    if (['/api/approvals', '/api/agent-accounts', '/api/agent-accounts/capacity', '/api/agent-accounts/capacity/schedule'].includes(path)) return route.fulfill({ json: [] })
    if (path === '/api/projects/p-pharos/message-targets') return route.fulfill({ json: [{ id: 'lead-target', principal_id: LEAD_AGENT, address: 'claude:lead', adapter: 'claude', target_kind: 'agent', maximum_level: 'simple', role: 'coordinator', enabled: true }] })
    if (path === '/api/projects/p-pharos/messages') {
      if (request.method() === 'POST') {
        const body = request.postDataJSON()
        const sent: ProjectMessage = { id: `message-${state.messages.length + 1}`, sender_principal_id: me.id, recipient_principal_id: LEAD_AGENT, recipient_session_id: body.recipient_session_id, to: body.to, body: body.body, sent_event_id: 100 + state.messages.length, is_action_request: false, expects_reply: false, delivery_level: body.delivery_level, status: 'accepted', reply_obligation: 'none', created_at: NOW }
        state.messages.push(sent)
        return route.fulfill({ status: 201, json: sent })
      }
      return route.fulfill({ json: { items: state.messages, next_after: 0 } })
    }
    if (path.endsWith('/read-marker') && path.startsWith('/api/projects/p-pharos/harness-sessions/')) {
      const id = path.split('/').at(-2)!
      if (request.method() === 'PUT') readMarkers.set(id, request.postDataJSON())
      return route.fulfill({ json: { session_id: id, ...(readMarkers.get(id) ?? { last_read_message_id: null, last_read_event_id: 0 }), updated_at: NOW } })
    }
    if (path === '/api/inbox/message-status') return route.fulfill({ json: { items: (url.searchParams.get('ids') ?? '').split(',').filter(Boolean).map(id => ({ message_id: id, status: 'read', delivered_at: NOW, read_at: NOW, deliver_by: null })) } })
    return route.fallback()
  })
  return { ...world, state, hold, release: () => { state.hold = false; release() }, started: () => started }
}
const panel = (page: Page) => page.getByRole('complementary', { name: 'Session details', exact: true })
const question = (page: Page) => panel(page).locator('.session-question')
const tab = (page: Page, name: string) => panel(page).getByRole('tab', { name: new RegExp(`^${name}`) })
const deferred = () => { let resolve!: () => void; const promise = new Promise<void>(r => { resolve = r }); return { promise, resolve } }

// Risk: wide fixed-height worker rows let long metadata cover a neighbour,
// or truncate a missing/old Beat label without a way to read it.
for (const look of ['light', 'dark'] as const) {
  test(`AEON-1212 fix4: ${look} wide worker metadata stays in its row, full values are readable and every Beat label fits`, async ({ page }, info) => {
    await page.setViewportSize({ width: 1800, height: 1000 })
    await page.emulateMedia({ colorScheme: look, reducedMotion: 'reduce' })
    const metadata = {
      model: 'Sol mit einer besonders ausführlichen Modellbezeichnung für umfangreiche Implementierungsaufgaben',
      account: 'Entwicklungskonto mit einer besonders ausführlichen Bezeichnung für gemeinsame Implementierungsaufgaben',
      computer: 'ArbeitsrechnerMitEinerBesondersAusführlichenBezeichnungFürGemeinsameImplementierungsaufgaben',
    }
    const errors = watchErrors(page), world = await setup(page, { question: false, workerMetadata: metadata })
    const worker = world.data.live.find(row => row.role === 'worker')!
    worker.heartbeat_at = null
    world.data.live.push({ ...worker, session_id: 'aaaaaaaa-0000-4000-8000-000000000003', principal_id: 'aaaaaaaa-0000-4000-8000-0000000000a3', name: 'Second worker', heartbeat_at: '2025-12-31T09:59:40Z' })
    await page.goto(`/agents/${LEAD_SESSION}?tab=lead`)
    const rows = panel(page).locator('.worker'), row = rows.first(), neighbour = rows.nth(1)
    await expect(rows).toHaveCount(2)
    await expect(row.locator('.worker-execution')).toBeVisible()
    await expect(row.locator('.worker-beat')).toHaveText('Not reported')
    await expect(neighbour.locator('.worker-beat')).toHaveText('31 Dec 2025')
    const contained = async () => {
      const failures = await rows.evaluateAll(elements => elements.flatMap(element => {
        const bounds = element.getBoundingClientRect(), bad: string[] = []
        for (const child of element.querySelectorAll('.worker-execution, .worker-execution > *, .worker-computer, .worker-beat')) {
          const box = child.getBoundingClientRect()
          if (box.top < bounds.top - .5 || box.bottom > bounds.bottom + .5 || box.left < bounds.left - .5 || box.right > bounds.right + .5) bad.push(`metadata outside row: ${child.className || child.tagName}`)
          if (child.classList.contains('worker-beat') && child.scrollWidth > child.clientWidth + 1) bad.push(`Beat does not fit: ${child.textContent}`)
        }
        if (element.scrollWidth > element.clientWidth + 1) bad.push('row horizontal overflow')
        return bad
      }))
      expect(failures).toEqual([])
    }
    for (const width of [1800, 2560]) {
      await page.setViewportSize({ width, height: 1000 })
      await row.scrollIntoViewIfNeeded()
      await contained()
      const controls = { header: panel(page).locator('.pane-bar'), actions: panel(page).getByRole('group', { name: 'Session actions' }), tabs: panel(page).getByRole('tablist'), row, neighbour }
      await expectStableControls({ controls, scrollAreas: { body: panel(page).locator('.lead-content'), workers: panel(page).locator('.worker-list') }, interactions: [
        { name: 'read each clipped metadata value on hover', run: async () => {
          for (const [selector, value] of [['.worker-execution > span', `${metadata.model} 6 · medium`], ['.worker-execution > small', `Codex · ${metadata.account}`], ['.worker-computer', metadata.computer]] as const) {
            await row.locator(selector).hover()
            await expect(page.locator('.tooltip')).toBeVisible()
            await expect(page.locator('.tooltip')).toHaveText(value)
            await contained()
          }
        } },
        { name: 'read full metadata from the row on keyboard focus', run: async () => {
          await page.keyboard.press('Tab')
          await row.focus()
          await expect(row).toBeFocused()
          await expect(page.locator('.tooltip')).toBeVisible()
          for (const value of Object.values(metadata)) await expect(page.locator('.tooltip')).toContainText(value)
          await contained()
        } },
      ] })
      await row.evaluate(element => (element as HTMLElement).blur())
      await panel(page).locator('#lp-workers').hover()
      await page.screenshot({ path: info.outputPath(`AEON-1212/worker-metadata-${width}-${look}.png`) })
    }
    expect(errors).toEqual([])
  })
}

test('AEON-1212 fix2: an overlapping refresh cannot finish the opening on discarded question evidence', async ({ page }) => {
  const world = await setup(page, { question: false, pinned: 'session' })
  await page.goto('/agents')
  await expect(page.locator(`[data-row="s:${LEAD_SESSION}"]`)).toBeVisible()
  await expect.poll(() => page.evaluate(async () => (await import('/src/stores/agentProjection.ts' /* @vite-ignore */)).useAgentProjection().questions.state)).toBe('ready')
  const started = deferred(), openingAnswer = deferred(), laterAnswer = deferred()
  let reads = 0
  await page.route('**/api/decision-desk?*', async route => {
    const first = ++reads === 1
    if (first) started.resolve()
    await (first ? openingAnswer.promise : laterAnswer.promise)
    await route.fulfill({ json: { items: [world.state.question], has_more: false } })
  })
  const opening = page.evaluate(async () => {
    const projection = (await import('/src/stores/agentProjection.ts' /* @vite-ignore */)).useAgentProjection()
    const opening = projection.refresh(), overlapping = projection.refresh()
    Object.assign(window, { overlappingQuestionRead: overlapping })
    await opening
    return projection.asksOf('aaaaaaaa-0000-4000-8000-000000000001').map(item => item.id)
  })
  try {
    await started.promise
    openingAnswer.resolve()
    expect(await opening).toEqual([Q])
    expect(reads).toBe(1)
    await page.locator(`[data-row="s:${LEAD_SESSION}"] .agent-link`).click()
    laterAnswer.resolve()
    await expect(tab(page, 'Chat')).toHaveAttribute('aria-selected', 'true')
    await expect(panel(page).locator('.why')).toContainText('it asks you something')
  } finally {
    openingAnswer.resolve(); laterAnswer.resolve()
    await opening
    await page.evaluate(() => (window as unknown as { overlappingQuestionRead: Promise<unknown> }).overlappingQuestionRead)
  }
})

test('AEON-1212 fix2: a manual tab choice cancels automatic opening while questions are still being read', async ({ page }) => {
  await setup(page, { pinned: 'lead' })
  const started = deferred(), answer = deferred()
  await page.route('**/api/decision-desk?*', async route => {
    started.resolve()
    await answer.promise
    await route.fallback()
  })
  try {
    await page.goto(`/agents/${LEAD_SESSION}`)
    await started.promise
    await expect(tab(page, 'Session')).toBeVisible()
    await expectStableControls({ controls: { header: panel(page).locator('.pane-bar'), tabs: panel(page).getByRole('tablist'), selected: tab(page, 'Session') }, interactions: [{ name: 'manual Session choice before the opening read finishes', run: async () => {
      await tab(page, 'Session').click()
      answer.resolve()
      await page.evaluate(async () => {
        await (await import('/src/stores/agentProjection.ts' /* @vite-ignore */)).useAgentProjection().refresh()
        await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
      })
      await expect(tab(page, 'Session')).toHaveAttribute('aria-selected', 'true')
      await expect(panel(page).locator('#session-panel-overview')).toBeVisible()
    } }] })
  } finally { answer.resolve() }
})

test('AEON-1212 fix2: unreadable and truncated live worker reads never render a complete empty inventory', async ({ page }) => {
  const world = await setup(page)
  let status = 503
  await page.route('**/api/harness-sessions/live*', route => route.fulfill({ status, json: status === 200
    ? { items: world.data.live.filter(agent => agent.session_id === LEAD_SESSION), at: NOW, fresh_seconds: 120, truncated: true }
    : { error: 'Worker inventory unavailable' } }))
  await page.goto(`/agents/${LEAD_SESSION}?tab=lead`)
  const count = panel(page).locator('.facts div').filter({ has: page.getByText('Workers', { exact: true }) }).locator('dd')
  const unreadable = panel(page).getByText('Its workers can’t be read right now.', { exact: true })
  await expect(unreadable).toBeVisible()
  await expect(count).toHaveText('—')
  const refresh = () => page.evaluate(async () => { await (await import('/src/stores/liveAgents.ts' /* @vite-ignore */)).useLiveAgents().refresh() })
  await expectStableControls({ controls: { header: panel(page).locator('.pane-bar'), tabs: panel(page).getByRole('tablist') }, interactions: [
    { name: 'truncated live read with no worker in its window', run: async () => {
      status = 200; await refresh()
      await expect(count).toHaveText('0+')
      await expect(panel(page)).toContainText('Not every session of this project could be read; more workers may run.')
      await expect(panel(page).getByText('No workers yet.', { exact: true })).toHaveCount(0)
    } },
    { name: 'failed refresh of the retained live window', run: async () => {
      status = 503; await refresh()
      await expect(unreadable).toBeVisible()
      await expect(count).toHaveText('—')
      await expect(panel(page).getByText('No workers yet.', { exact: true })).toHaveCount(0)
    } },
    { name: 'live inventory forbidden', run: async () => {
      status = 403; await refresh()
      await expect(unreadable).toBeVisible()
      await expect(count).toHaveText('—')
    } },
  ] })
})

// Same observable probes as check.mjs, scoped to this package's changed panel.
async function checkPanel(page: Page, phone: boolean) {
  const failures = await panel(page).evaluate((root, touch) => {
    const bad: string[] = []
    const visible = (element: Element) => { const r = element.getBoundingClientRect(), s = getComputedStyle(element); return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none' && !element.closest('[inert]') }
    for (const element of [root, ...root.querySelectorAll('*')].filter(visible)) {
      const s = getComputedStyle(element), left = parseFloat(s.borderLeftWidth), right = parseFloat(s.borderRightWidth), top = parseFloat(s.borderTopWidth), bottom = parseFloat(s.borderBottomWidth)
      if (left >= 2 && left - right >= 1 || top >= 2 && top - bottom >= 1) bad.push(`edge accent: ${element.className}`)
      if (s.fontStyle === 'italic') bad.push('italic text')
      if (element.scrollWidth > element.clientWidth + 1 && ['lead-content','session-chat','thread-scroll','pane-bar','tabs-line'].some(c => element.classList.contains(c))) bad.push(`sideways scroll: ${element.className}`)
      if (touch && element.matches('button,a[href],textarea,input,select') && !element.closest('[aria-hidden="true"]')) {
        const r = element.getBoundingClientRect()
        if (r.width < 43.5 || r.height < 43.5) bad.push(`touch: ${element.getAttribute('aria-label') || element.textContent} ${r.width}x${r.height}`)
      }
    }
    const text = (root as HTMLElement).innerText
    const computer = root.querySelector('.host-fact')
    if (touch && computer && visible(computer)) {
      const name = computer.querySelector('.host-badge')?.getBoundingClientRect()
      const rename = computer.querySelector('.host-pencil')?.getBoundingClientRect()
      if (name && rename && name.right > rename.left + .5) bad.push('computer name and rename touch targets overlap')
    }
    if (/\b(Agenten|Sitzung|Einstellungen|nicht|bitte)\b/.test(text)) bad.push('mixed language')
    if (/[✓✔✕✖×▾▸▲▼◂▶◀★☆●○⚠]/.test(text)) bad.push('text icon glyph')
    if (root.querySelectorAll('.pane-bar .agent-glyph .agent-state-mark').length !== 1) bad.push('avatar state badge')
    return bad
  }, phone)
  expect(failures).toEqual([])
}

// Copy/reply must remain reachable without leaving an empty action row or
// moving the message, its neighbours, the question, or the pinned composer.
async function checkChatSpacing(page: Page, output: string) {
  const thread = panel(page).locator('.thread-scroll')
  const scrollTop = await thread.evaluate(element => element.scrollTop)
  const messages = panel(page).locator('.thread > .msg')
  await expect(messages).toHaveCount(4)
  const first = messages.first(), copy = first.getByRole('button', { name: 'Copy', exact: true })
  await expectStableControls({ controls: {
    header: panel(page).locator('.pane-bar'), tabs: panel(page).getByRole('tablist'),
    composer: panel(page).locator('.composer'), question: question(page),
    message: first, next: messages.nth(1), copy, reply: first.getByRole('button', { name: 'Reply', exact: true }),
  }, scrollAreas: { thread: panel(page).locator('.thread-scroll') }, interactions: [
    { name: 'reveal message actions', run: () => first.hover() },
    { name: 'focus message action', run: () => copy.focus() },
  ] })
  const spacing = await messages.evaluateAll(rows => rows.map((row, index) => {
    const bounds = row.getBoundingClientRect(), previous = rows[index - 1]?.getBoundingClientRect()
    const actions = row.querySelector('.message-actions')?.getBoundingClientRect()
    const body = row.querySelector('.markdown-body, .msg-body')!.getBoundingClientRect()
    return { gap: previous ? bounds.top - previous.bottom : null, timeGroup: row.classList.contains('time-group'),
      actionsInside: !actions || actions.top >= bounds.top && actions.bottom <= bounds.bottom + 0.5,
      actionsBesideText: !actions || actions.left >= body.right }
  }))
  for (const item of spacing) {
    if (item.gap !== null) expect(Math.abs(item.gap - (item.timeGroup ? 20 : 12)), 'compact message/time-group spacing').toBeLessThanOrEqual(0.5)
    expect(item.actionsInside, '44 px message actions stay inside their row').toBe(true)
    expect(item.actionsBesideText, 'message actions cannot cover the text').toBe(true)
  }
  await writeFile(output, `${JSON.stringify(spacing, null, 2)}\n`)
  console.log(`AEON-1212 chat spacing: ${spacing.slice(1).map(item => item.gap).join(', ')} px; action hover/focus shift ≤ 0.5 px`)
  await copy.blur()
  await panel(page).locator('.pane-bar').hover()
  // Hover/focus can intentionally scroll to an older post. Capture the same
  // latest-message viewport as the approved lead-chat reference afterward.
  await thread.evaluate((element, top) => { element.scrollTop = top }, scrollTop)
}

for (const width of [1440, 1024, 1000, 400, 390]) for (const look of ['light','dark'] as const) {
  test(`AEON-1212 check.mjs: ${width} ${look} Lead Session Chat controls stay put and the real answer records in place`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: look, reducedMotion: 'reduce' })
    const errors = watchErrors(page), world = await setup(page)
    await page.goto(`/agents/${LEAD_SESSION}`)
    await expect(tab(page, 'Chat')).toHaveAttribute('aria-selected', 'true')
    await expect(panel(page).locator('.why')).toContainText('it asks you something')
    await expect(question(page)).toContainText('Asks you')
    if (width === 1000) await page.screenshot({ path: info.outputPath(`AEON-1212/1000-sheet-${width}-${look}.png`) })
    const stable = { frame: panel(page), header: panel(page).locator('.pane-bar'), actions: panel(page).getByRole('group', { name: 'Session actions' }), tabs: panel(page).getByRole('tablist'), pin: panel(page).getByRole('button', { name: 'Make this your default tab' }), close: panel(page).getByRole('button', { name: 'Close session details' }) }
    await expectStableControls({ controls: stable, scrollAreas: { panel: panel(page) }, interactions: [
      { name: 'Lead', run: async () => { await tab(page, 'Lead').click(); await expect(panel(page).locator('#lp-checks')).toHaveText('Before every start'); await expect(panel(page).locator('.admission-caption')).toContainText('All passed at the last start'); await expect(panel(page).locator('[data-check]')).toHaveCount(4); await checkPanel(page, width <= 400); await page.screenshot({ path: info.outputPath(`AEON-1212/lead-sheet-${width}-${look}.png`) }) } },
      { name: 'Session', run: async () => { await tab(page, 'Session').click(); await expect(panel(page).locator('#session-panel-overview')).toBeVisible(); await checkPanel(page, width <= 400); await page.screenshot({ path: info.outputPath(`AEON-1212/session-${width}-${look}.png`) }) } },
      { name: 'Chat', run: async () => { await tab(page, 'Chat').click(); await expect(question(page)).toBeVisible(); await checkPanel(page, width <= 400); await checkChatSpacing(page, info.outputPath(`AEON-1212/chat-spacing-${width}-${look}.md`)); await page.screenshot({ path: info.outputPath(`AEON-1212/lead-chat-${width}-${look}.png`) }) } },
    ] })
    world.hold()
    await expectStableControls({ controls: { ...stable, card: question(page), answerArea: question(page).locator('.answer-slot'), composer: panel(page).locator('.composer') }, scrollAreas: { thread: panel(page).locator('.thread-scroll') }, interactions: [
      { name: 'Pending', run: async () => { await expectStableControls({ controls: { options: question(page).locator('.question-options'), pressed: question(page).locator('.question-options button').first(), other: question(page).locator('.question-options button').last() }, interactions: [{ name: 'choose', run: async () => { await question(page).getByRole('button', { name: 'Yes, merge now' }).click(); await world.started(); await expect(question(page).getByRole('button', { name: 'Recording…' })).toBeDisabled() } }] }) } },
      { name: 'Recorded', run: async () => { world.release(); await expect(question(page)).toContainText('Answered: Yes, merge now'); await expect(question(page)).toContainText('by you'); await expect(question(page).getByRole('button')).toHaveCount(0); await page.screenshot({ path: info.outputPath(`AEON-1212/recorded-${width}-${look}.png`) }) } },
    ] })
    expect(world.state.calls).toHaveLength(1)
    expect(world.state.calls[0]).toMatchObject({ expected_revision: 1, option_id: 'yes', outcome: 'once' })
    expect(world.state.calls[0]?.request_id).toMatch(/^[a-f0-9-]{36}$/)
    expect(errors).toEqual([])
  })
}

test('AEON-1212: failed answer retries the same request, a plain message never decides, and a late result stays off another session', async ({ page }) => {
  const world = await setup(page)
  await page.goto(`/agents/${LEAD_SESSION}?tab=chat`)
  await expect(question(page)).toBeVisible()
  world.state.fail = true
  await question(page).getByRole('button', { name: 'Not now', exact: true }).click()
  await expect(question(page)).toContainText('Not recorded. Try again')
  const id = world.state.calls[0]!.request_id
  await question(page).getByRole('button', { name: 'Retry', exact: true }).click()
  await expect(question(page)).toContainText('Answered: Not now')
  expect(world.state.calls[1]!.request_id).toBe(id)
  // Reopen a fresh question and hold its decision behind an explicit barrier.
  world.state.question = makeQuestion()
  await page.reload()
  await expect(question(page).getByRole('button', { name: 'Not now', exact: true })).toBeVisible()
  const composer = panel(page).locator('textarea')
  await composer.fill('This is a message, not a decision.')
  await panel(page).locator('.compose .send').click()
  await expect(composer).toHaveValue('')
  expect(world.state.messages.at(-1)?.body).toBe('This is a message, not a decision.')
  await expect(question(page).getByRole('button', { name: 'Not now', exact: true })).toBeEnabled()
  expect(world.state.calls).toHaveLength(2)
  world.hold()
  await question(page).getByRole('button', { name: 'Not now', exact: true }).click()
  await world.started()
  const worker = world.data.live.find(row => row.role === 'worker')!
  await tab(page, 'Lead').click()
  await panel(page).locator('.worker').filter({ hasText: worker.ticket!.key }).click()
  await expect(tab(page, 'Overview')).toBeVisible()
  world.release()
  await expect.poll(() => world.state.question.state).toBe('answered')
  await expect(question(page)).toHaveCount(0)
})

for (const width of [1440, 1024, 390]) for (const look of ['light', 'dark'] as const) {
  test(`AEON-1212: ${width} ${look} long German question options stay readable and do not move the header or answer controls`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: look, reducedMotion: 'reduce' })
    const world = await setup(page, { german: true })
    world.state.question.input.question = 'Soll die bereits geprüfte Änderung jetzt übernommen oder für die nächste gemeinsame Veröffentlichung zurückgestellt werden?'
    world.state.question.input.options[0]!.title = 'Die bereits geprüfte Änderung jetzt vollständig übernehmen'
    world.state.question.input.options[1]!.title = 'Bis zur nächsten gemeinsamen Veröffentlichung zurückstellen'
    delete world.state.question.input.recommend
    await page.goto(`/agents/${LEAD_SESSION}?tab=chat`)
    await expect(question(page)).toBeVisible()
    await expect(question(page).locator('.primary')).toHaveCount(0)
    const controls = { frame: panel(page), header: panel(page).locator('.pane-bar'), actions: panel(page).getByRole('group', { name: 'Session actions' }), composer: panel(page).locator('.composer'), card: question(page), answerArea: question(page).locator('.answer-slot') }
    await page.screenshot({ path: info.outputPath(`AEON-1212/long-text-chat-${width}-${look}.png`) })
    world.hold()
    await expectStableControls({ controls, scrollAreas: { body: panel(page).locator('.thread-scroll') }, interactions: [
      { name: 'record long option', run: async () => { await expectStableControls({ controls: { options: question(page).locator('.question-options'), pressed: question(page).locator('.question-options button').first() }, interactions: [{ name: 'choose', run: async () => { await question(page).getByRole('button', { name: world.state.question.input.options[0]!.title, exact: true }).click(); await world.started(); await expect(question(page).getByRole('button', { name: 'Recording…', exact: true })).toBeDisabled() } }] }) } },
      { name: 'recorded long answer', run: async () => { world.release(); await expect(question(page)).toContainText(`Answered: ${world.state.question.input.options[0]!.title}`) } },
    ] })
    await page.screenshot({ path: info.outputPath(`AEON-1212/long-text-recorded-${width}-${look}.png`) })
    await tab(page, 'Lead').click()
    await expect(panel(page).locator('.queued-item')).toContainText('Agenten-Dial')
    await page.screenshot({ path: info.outputPath(`AEON-1212/long-text-lead-${width}-${look}.png`) })
  })
}

test('AEON-1212: server action omission and decision denial remain read-only, and more than four real options link to the Desk', async ({ page }) => {
  const world = await setup(page, { denied: true, actions: false })
  await page.goto(`/agents/${LEAD_SESSION}?tab=chat`)
  await expect(question(page)).toContainText('Waits for a project admin')
  await expect(question(page).getByRole('button')).toHaveCount(0)
  await expect(question(page).getByRole('link', { name: 'Open on the Desk' })).toHaveAttribute('href', `/decision-desk?item=q:${Q}`)
  await expect(panel(page).getByRole('button', { name: 'Pause…', exact: true })).toBeDisabled()
  expect(world.state.calls).toEqual([])
  const more = await setup(page)
  more.state.question.input.options.push(...[3,4,5].map(n => ({ id: `choice${n}`, title: `Option ${n}`, description: '', answer: String(n) })))
  await page.reload()
  await expect(question(page).getByRole('button')).toHaveCount(3)
  await expect(question(page).getByRole('link', { name: 'All options on the Desk' })).toBeVisible()
})

test('AEON-1212: losing question-read access clears retained cards and discards the pending decision response', async ({ page }) => {
  const world = await setup(page)
  await page.goto(`/agents/${LEAD_SESSION}?tab=chat`)
  await expect(question(page)).toBeVisible()
  world.hold()
  await question(page).getByRole('button', { name: 'Not now', exact: true }).click()
  await world.started()
  await page.route('**/api/me/permissions*', route => {
    const value = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    value.workspace.permissions = value.workspace.permissions.filter(permission => !permission.startsWith('questions.'))
    if (value.project) value.project.permissions = value.project.permissions.filter(permission => !permission.startsWith('questions.'))
    return route.fulfill({ json: value })
  })
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(question(page)).toHaveCount(0)
  world.release()
  await expect.poll(() => world.state.question.state).toBe('answered')
  await expect(question(page)).toHaveCount(0)
  await expect(panel(page)).not.toContainText('Answered:')
  await tab(page, 'Lead').click()
  await expect(panel(page)).not.toContainText(world.state.question.input.question)
})

test('AEON-1212: pinned and last-used tabs belong to the person; queue links force Lead at Next up and chat keys only type', async ({ page }) => {
  const world = await setup(page, { question: false, pinned: 'session', last: 'chat', german: true })
  await page.goto(`/agents/${LEAD_SESSION}`)
  await expect(tab(page, 'Session')).toHaveAttribute('aria-selected', 'true')
  await expect(panel(page).locator('.why')).toContainText('your default')
  await panel(page).getByRole('button', { name: 'Remove your default tab' }).click()
  await tab(page, 'Chat').click()
  await expect.poll(() => world.data.preferences['agents:lead-panel']).toMatchObject({ pinned: null, last: 'chat' })
  await page.reload()
  await expect(tab(page, 'Chat')).toHaveAttribute('aria-selected', 'true')
  await expect(panel(page).locator('.why')).toContainText('last used')
  const field = panel(page).locator('textarea')
  await field.fill('jksf/')
  await field.press('Control+a')
  await expect(field).toHaveValue('jksf/')
  await field.press('Escape')
  await expect(field).not.toBeFocused()
  await page.goto(`/agents/${LEAD_SESSION}?tab=lead#next-up`)
  await expect(tab(page, 'Lead')).toHaveAttribute('aria-selected', 'true')
  await expect(panel(page).locator('#next-up')).toBeFocused()
  await expect(panel(page).locator('.why')).toContainText('Next up')
})

for (const width of [1440, 1000, 400]) for (const look of ['light', 'dark'] as const) {
  test(`AEON-1212 fix3: ${width} ${look} G3 results and G6 admission evidence stay honest and the owned worker row stays put`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: look, reducedMotion: 'reduce' })
    const errors = watchErrors(page), world = await setup(page)
    const shot = async (state: string, captureWidth = width) => {
      await checkPanel(page, width === 400)
      await page.screenshot({ path: info.outputPath(`AEON-1212/${state}-${captureWidth}-${look}.png`) })
    }
    await page.goto(`/agents/${LEAD_SESSION}?tab=chat`)
    await expect(question(page)).toContainText('Asks you')
    await expect(panel(page).locator('.pt-sub')).toContainText('Opus 5.5 · high')
    const controls = { header: panel(page).locator('.pane-bar'), actions: panel(page).getByRole('group', { name: 'Session actions' }), tabs: panel(page).getByRole('tablist') }
    world.hold()
    try {
      await expectStableControls({ controls: { ...controls, card: question(page), answer: question(page).locator('.answer-slot'), composer: panel(page).locator('.composer') }, scrollAreas: { chat: panel(page).locator('.thread-scroll') }, interactions: [
        { name: 'pending', run: async () => {
          await expectStableControls({ controls: { options: question(page).locator('.question-options'), pressed: question(page).locator('.question-options button').first(), other: question(page).locator('.question-options button').last() }, interactions: [
            { name: 'choose the real server option', run: async () => {
              await question(page).getByRole('button', { name: 'Yes, merge now', exact: true }).click()
              await world.started()
              await expect(question(page).getByRole('button', { name: 'Recording…', exact: true })).toBeDisabled()
            } },
          ] })
          await shot('g3-pending')
        } },
        { name: 'recorded', run: async () => {
          world.release()
          await expect(question(page)).toContainText('Answered: Yes, merge now')
          await expect(question(page)).toContainText('by you')
          await expect(question(page).getByRole('button')).toHaveCount(0)
          await shot('g3-recorded')
        } },
      ] })
    } finally { world.release() }
    world.state.question = makeQuestion()
    await page.reload()
    await expect(question(page).getByRole('button', { name: 'Not now', exact: true })).toBeVisible()
    world.state.fail = true
    await expectStableControls({ controls: { ...controls, card: question(page), answer: question(page).locator('.answer-slot'), composer: panel(page).locator('.composer') }, scrollAreas: { chat: panel(page).locator('.thread-scroll') }, interactions: [
      { name: 'failed answer', run: async () => {
        await question(page).getByRole('button', { name: 'Not now', exact: true }).click()
        await expect(question(page)).toContainText('Not recorded. Try again')
        await expect(question(page).getByRole('button', { name: 'Retry', exact: true })).toBeEnabled()
        await shot('g3-failed')
      } },
      { name: 'decided elsewhere', run: async () => {
        world.state.question = { ...world.state.question, revision: 2, state: 'answered', answer: { id: 'desk-answer', revision: 2, option_id: 'later', answer: 'Not now', outcome: 'once', decided_by: 'another-person', created_at: NOW, deliver_after: NOW } }
        await page.evaluate(async () => { await (await import('/src/stores/agentProjection.ts' /* @vite-ignore */)).useAgentProjection().refresh() })
        await expect(question(page)).toContainText('Answered on the Desk: Not now')
        await expect(question(page).getByRole('button')).toHaveCount(0)
        await shot('g3-decided-elsewhere')
      } },
    ] })
    world.state.question = makeQuestion(); world.state.denied = true
    await page.reload()
    await expect(question(page)).toContainText('Waits for a project admin')
    await expect(question(page).getByRole('button')).toHaveCount(0)
    await expect(question(page).getByRole('link', { name: 'Open on the Desk' })).toHaveAttribute('href', `/decision-desk?item=q:${Q}`)
    expect(world.state.calls).toHaveLength(2)
    await shot('g3-no-permission')

    world.state.denied = false
    await page.goto(`/agents/${LEAD_SESSION}?tab=lead`)
    const caption = panel(page).locator('.admission-caption')
    const checks = panel(page).locator('[data-check]')
    const admissionShot = async (state: string) => {
      await panel(page).locator('#lp-checks').scrollIntoViewIfNeeded()
      await shot(`g6-${state}`)
    }
    await expect(caption).toContainText('All passed at the last start')
    await expect(checks).toHaveCount(4)
    await admissionShot('all-passed')
    const row = panel(page).locator('.worker').first()
    await expect(row.locator('.worker-name')).toHaveText('PHAROS-11 · Connect Hetzner Cloud for managed provisioning')
    await expect(row.locator('.worker-name')).not.toContainText('imac0')
    await expect(row.locator('.worker-execution')).toContainText('Sol 6 · medium')
    await expect(row.locator('.worker-execution')).toContainText('Codex')
    await expect(row.locator('.worker-execution')).toContainText('ChatGPT Pro')
    await expect(row.locator('.worker-computer')).toHaveText('imac0')
    await expect(row.locator('.worker-beat')).not.toHaveText('Not reported')
    await expect(row.locator('.listening-label')).toHaveCount(0)
    await expect(panel(page).locator('.worker-columns [role="columnheader"]')).toHaveText(['Session', 'Time', 'Execution', 'Computer', 'Beat'])
    // The current P4 branch has the predecessor's fixed side panel; Focus and
    // Overview are P5 layouts. Its own rows must also behave in a wide panel.
    if (width === 1440) {
      await page.setViewportSize({ width: 1800, height: 1000 })
      await expect(row.locator('.worker-execution')).toBeVisible()
    }
    await row.scrollIntoViewIfNeeded()
    await expectStableControls({ controls: { ...controls, row }, scrollAreas: { body: panel(page).locator('.lead-content') }, interactions: [
      { name: 'hover the owned worker row', run: async () => { await row.hover(); await expect(row).toBeVisible() } },
    ] })
    await shot('worker-row', width === 1440 ? 1800 : width)
    if (width === 1440) await page.setViewportSize({ width, height: 1000 })
    await expectStableControls({ controls, scrollAreas: { body: panel(page).locator('.lead-content') }, interactions: [
      { name: 'held history', run: async () => {
        world.state.admission = [{ ...makeAdmission(), reason_codes: ['dial_full'] }]
        await page.reload()
        await expect(caption).toContainText('1 held the last start')
        await expect(checks).toHaveCount(4)
        await expect(checks.first()).toHaveText('Dial held it: the dial was full')
        await expect(checks.first()).toHaveAttribute('data-tip', 'dial_full')
        await admissionShot('held')
      } },
      { name: 'held history with an unknown reason', run: async () => {
        world.state.admission = [{ ...makeAdmission(), outcome: 'wait', reason_codes: ['future_check'], request: { ...makeAdmission().request, outcome: 'wait', reason_codes: ['future_check'], gates: [{ kind: 'dial', state: 'full' }] } }]
        await page.reload()
        await expect(caption).toContainText('1 held the last start')
        await expect(checks.first()).toHaveText('Dial held it: a check said no')
        await expect(checks.first()).toHaveAttribute('data-tip', 'future_check')
        await expect(panel(page).locator('.lead-content')).not.toContainText('future_check')
        await checks.first().focus()
        await expect(page.locator('.tooltip')).toHaveText('future_check')
        await checks.first().blur()
        await panel(page).locator('.pane-bar').hover()
        await admissionShot('unknown-reason')
      } },
      { name: 'no start yet', run: async () => {
        world.state.admission = []
        await page.reload()
        await expect(caption).toHaveText('No start yet')
        await expect(checks).toHaveCount(0)
        await expect(panel(page)).toContainText('The checks run when this lead starts its next worker.')
        await admissionShot('no-start-yet')
      } },
      { name: 'unreadable history', run: async () => {
        world.state.historyUnreadable = true
        await page.reload()
        await expect(caption).toHaveText("Couldn't read the last start")
        await expect(checks).toHaveCount(0)
        await expect(panel(page).locator('[aria-labelledby="lp-checks"]').getByRole('button', { name: 'Try again', exact: true })).toBeVisible()
        const failure = panel(page).locator('[data-read-fail="history"]')
        await expect(failure).toContainText('Recent, Gate and Merged are unknown.')
        await expect(panel(page)).not.toContainText('The admission history is temporarily unavailable.')
        await admissionShot('unreadable')
        world.state.historyUnreadable = false
        await failure.getByRole('button', { name: 'Try again', exact: true }).click()
        await expect(failure).toHaveCount(0)
        await expect(caption).toHaveText('No start yet')
      } },
      { name: 'unreadable queue retries without moving its controls', run: async () => {
        world.state.queueUnreadable = true
        await page.reload()
        const failure = panel(page).locator('[data-read-fail="queue"]')
        await expect(failure).toContainText('The queue can’t be read right now, so Next up is unknown.')
        await expect(panel(page)).not.toContainText('queue store unavailable')
        const again = failure.getByRole('button', { name: 'Try again', exact: true })
        await again.scrollIntoViewIfNeeded()
        let release!: () => void, arrived!: () => void
        const held = new Promise<void>(resolve => { release = resolve })
        const started = new Promise<void>(resolve => { arrived = resolve })
        await page.route(url => url.pathname === '/api/queue' && url.searchParams.get('project_id') === 'p-pharos', async route => {
          arrived(); await held; return route.fallback()
        })
        world.state.queueUnreadable = false
        try {
          await expectStableControls({ controls: { ...controls, again }, scrollAreas: { body: panel(page).locator('.lead-content') }, interactions: [
            { name: 'retry the held queue read', run: async () => {
              await again.click(); await started
              await expect(again).toHaveAttribute('aria-busy', 'true')
              await expect(again).toHaveAttribute('aria-disabled', 'true')
              await expect(again).toHaveText('Try again')
            } },
          ] })
        } finally { release() }
        await expect(failure).toHaveCount(0)
        await expect(panel(page).locator('#next-up .wk')).not.toHaveCount(0)
      } },
      { name: 'stale history', run: async () => {
        world.state.historyUnreadable = false
        world.state.admission = [{ ...makeAdmission(), recorded_at: '2026-10-04T09:02:00Z' }]
        await page.reload()
        await expect(caption).toContainText('may be out of date')
        await expect(checks).toHaveCount(4)
        await admissionShot('stale')
      } },
    ] })
    if (width === 1440) {
      await panel(page).getByRole('button', { name: 'Close session details' }).click()
      await page.locator('.sessions .row[data-row^="s:"] .agent-link').last().click()
      await expect(tab(page, 'Overview')).toHaveAttribute('aria-selected', 'true')
      await page.screenshot({ path: info.outputPath(`AEON-1212/pick-last-row-${width}-${look}.png`) })
    }
    expect(errors).toEqual([])
  })
}

test('AEON-1212 approved check.mjs: the round-7 design passes and supplies the reference screenshots', async ({}, info) => {
  test.setTimeout(240_000)
  const fixture = fileURLToPath(new URL('./fixtures/aeon-1212-ag-approved/', import.meta.url))
  const output = info.outputPath('AEON-1212/approved-reference')
  await mkdir(output, { recursive: true })
  const units = await promisify(execFile)(process.execPath, ['--experimental-strip-types', '--test', '--test-reporter=tap', fileURLToPath(new URL('./lead-panel.test.ts', import.meta.url))], { timeout: 30_000, maxBuffer: 1024 * 1024 })
  expect(units.stdout).toMatch(/# pass 3\b/)
  console.log('AEON-1212 panel units: 3 passed')
  await copyFile(resolve(fixture, 'agents-page-approved.html'), resolve(output, 'fragment.html'))
  // No Playwright browser fixture is requested: the original check owns its
  // one headless browser, exits, then the extra reference capture owns one.
  await promisify(execFile)(process.execPath, [resolve(fixture, 'check.mjs')], { cwd: output, timeout: 180_000, maxBuffer: 1024 * 1024 })
  const result = await readFile(resolve(output, 'check.txt'), 'utf8')
  await writeFile(resolve(output, 'check.md'), result)
  expect(result).toMatch(/\nALL PASS \(\d+ checks\)\n$/)
  const shots = resolve(output, 'shots-r7/check')
  for (const name of await readdir(shots)) {
    const parsed = /^(\d+)-(light|dark)-(.+)\.png$/.exec(name)
    if (parsed) await copyFile(resolve(shots, name), resolve(output, `${parsed[3]}-${parsed[1]}-${parsed[2]}.png`))
  }
  const browser = await chromium.launch({ headless: true })
  try {
    for (const width of [1440, 400, 1000]) for (const look of ['light', 'dark']) {
      const reference = await browser.newPage({ viewport: { width, height: 1000 }, deviceScaleFactor: 1 })
      await reference.goto(`${pathToFileURL(resolve(output, 'fragment.html')).href}?state=busy&theme=${look}&width=${width === 400 ? 'phone' : 'desktop'}&open=AEON-LEAD`)
      await expect(reference.locator('[data-tab="chat"][aria-selected="true"]')).toBeVisible()
      await reference.locator('#stage').scrollIntoViewIfNeeded()
      if (width === 1000) await reference.screenshot({ path: resolve(output, `1000-sheet-${width}-${look}.png`) })
      await reference.locator('[data-tab="lead"]').click()
      await expect(reference.locator('#lp-now')).toBeVisible()
      await reference.screenshot({ path: resolve(output, `lead-sheet-${width}-${look}.png`) })
      await reference.close()
    }
  } finally { await browser.close() }
  console.log(`AEON-1212 approved check.mjs: ${result.trim().split('\n').at(-1)}`)
})
