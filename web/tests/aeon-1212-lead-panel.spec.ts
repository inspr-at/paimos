// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1212 P4 acceptance, adapted from the approved AEON-1196 check.mjs to
// product selectors: zero shift, real options/results, SVG, state badges,
// English, 44px phone targets, keys and no sideways scrolling, both themes.
// Risk: a chat send closes a question; a late decision lands on another person
// or session; a historical gate passes as current; tab/header controls move.
import { test, expect, type Page } from '@playwright/test'
import { mockLeadFlow, LEAD_SESSION, NOW } from './lead-flow-fixtures'
import { me, watchErrors } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import type { Question } from '../src/lib/decisionDeskApi'

const Q = '6e000000-0000-4000-8000-000000000001'
const makeQuestion = (): Question => ({
  id: Q, project_id: 'p-pharos', revision: 1, state: 'open', input: { request_id: 'ask-one', session_id: LEAD_SESSION,
    question: 'Merge the reviewed change now, or wait for the release train?', meanwhile: 'parked', recommend: 'yes',
    options: [{ id: 'yes', title: 'Yes, merge now', description: 'Merge the reviewed change.', answer: 'Yes' }, { id: 'later', title: 'Not now', description: 'Wait for the release train.', answer: 'Not now' }] },
  suggested_outcome: 'once', suggestion_reason: 'agent_suggestion', askers: [{ id: 'asker', principal_id: 'lead-agent', session_id: LEAD_SESSION, reply_root_id: '', comment_node_id: '', input: { request_id: 'ask-one', question: 'Merge?', options: [], meanwhile: 'parked' } }],
  pending: [], created_at: '2026-10-06T09:39:00Z', updated_at: '2026-10-06T09:39:00Z',
})
async function setup(page: Page, options: { denied?: boolean; question?: boolean; german?: boolean; pinned?: string; last?: string; actions?: boolean } = {}) {
  await page.clock.setSystemTime(NOW)
  const world = await mockLeadFlow(page, { lead: 'working', longText: options.german })
  const lead = world.state.leads['p-pharos']!
  lead.actions = Object.fromEntries(['start', 'resume', 'pause', 'adopt', 'cancel_adoption', 'remove'].map(action => [action, { available: action === 'pause', blocked_reason: action === 'pause' ? '' : 'ordinary_succession_required' }]))
  if (options.actions === false) delete lead.actions
  if (options.pinned || options.last) world.data.preferences['agents:lead-panel'] = { pinned: options.pinned, last: options.last }
  const state = { question: makeQuestion(), calls: [] as Record<string, unknown>[], hold: false, fail: false, messages: [] as string[] }
  let release: () => void = () => {}, arrived: () => void = () => {}
  let barrier = Promise.resolve(), started = Promise.resolve()
  function hold() { state.hold = true; barrier = new Promise<void>(resolve => { release = resolve }); started = new Promise<void>(resolve => { arrived = resolve }) }
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions.push('questions.read', 'questions.decide', 'run.create', 'harness.read', 'harness.control', 'inbox.read', 'inbox.write')
    if (options.denied) {
      answer.workspace.permissions = answer.workspace.permissions.filter(p => p !== 'questions.decide')
      if (answer.project) answer.project.permissions = answer.project.permissions.filter(p => p !== 'questions.decide')
    }
    return route.fulfill({ json: answer })
  })
  await page.route('**/api/decision-desk?*', route => route.fulfill({ json: { items: options.question === false || state.question.state !== new URL(route.request().url()).searchParams.get('state') ? [] : [state.question], has_more: false } }))
  await page.route(`**/api/questions/${Q}*`, async route => {
    if (route.request().method() === 'GET') return route.fulfill({ json: state.question })
    const body = route.request().postDataJSON() as Record<string, unknown>
    state.calls.push(body); arrived()
    if (state.hold) await barrier
    if (state.fail) { state.fail = false; return route.fulfill({ status: 503, json: { error: 'The record is temporarily unavailable.' } }) }
    if (body.expected_revision !== state.question.revision) return route.fulfill({ status: 409, json: { error: 'Question revision changed.' } })
    state.question = { ...state.question, revision: 2, state: 'answered', answer: { id: 'answer-one', revision: 2, answer: body.option_id === 'yes' ? 'Yes' : 'Not now', option_id: String(body.option_id), outcome: 'once', decided_by: me.id, created_at: NOW, deliver_after: NOW } }
    return route.fulfill({ json: state.question })
  })
  return { ...world, state, hold, release: () => { state.hold = false; release() }, started: () => started }
}
const panel = (page: Page) => page.getByRole('complementary', { name: 'Session details', exact: true })
const question = (page: Page) => panel(page).locator('.session-question')
const tab = (page: Page, name: string) => panel(page).getByRole('tab', { name: new RegExp(`^${name}`) })

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
    if (/\b(Agenten|Sitzung|Einstellungen|nicht|bitte)\b/.test(text)) bad.push('mixed language')
    if (/[✓✔✕✖×▾▸▲▼◂▶◀★☆●○⚠]/.test(text)) bad.push('text icon glyph')
    if (root.querySelectorAll('.pane-bar .agent-glyph .agent-state-mark').length !== 1) bad.push('avatar state badge')
    return bad
  }, phone)
  expect(failures).toEqual([])
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
    const stable = { header: panel(page).locator('.pane-bar'), actions: panel(page).getByRole('group', { name: 'Session actions' }), tabs: panel(page).getByRole('tablist'), pin: panel(page).getByRole('button', { name: 'Make this your default tab' }), close: panel(page).getByRole('button', { name: 'Close session details' }) }
    await expectStableControls({ controls: stable, scrollAreas: { panel: panel(page) }, interactions: [
      { name: 'Lead', run: async () => { await tab(page, 'Lead').click(); await expect(panel(page).locator('#lp-checks')).toHaveText('Before every start'); await expect(panel(page).locator('.admission-caption')).toContainText('All passed at the last start'); await expect(panel(page).locator('[data-check]')).toHaveCount(4); await checkPanel(page, width <= 400); await panel(page).screenshot({ path: info.outputPath(`AEON-1212/${width === 1000 ? "1000-sheet" : "lead-sheet"}-${width}-${look}.png`) }) } },
      { name: 'Session', run: async () => { await tab(page, 'Session').click(); await expect(panel(page).locator('#session-panel-overview')).toBeVisible(); await checkPanel(page, width <= 400); await panel(page).screenshot({ path: info.outputPath(`AEON-1212/session-${width}-${look}.png`) }) } },
      { name: 'Chat', run: async () => { await tab(page, 'Chat').click(); await expect(question(page)).toBeVisible(); await checkPanel(page, width <= 400); await panel(page).screenshot({ path: info.outputPath(`AEON-1212/lead-chat-${width}-${look}.png`) }) } },
    ] })
    world.hold()
    await expectStableControls({ controls: { ...stable, card: question(page), choices: question(page).locator('.question-options'), composer: panel(page).locator('.composer') }, scrollAreas: { thread: panel(page).locator('.thread-scroll') }, interactions: [
      { name: 'Pending', run: async () => { await question(page).getByRole('button', { name: 'Yes, merge now' }).click(); await world.started(); await expect(question(page).getByRole('button', { name: 'Recording…' })).toBeDisabled() } },
      { name: 'Recorded', run: async () => { world.release(); await expect(question(page)).toContainText('Answered: Yes, merge now'); await expect(question(page)).toContainText('by you'); await panel(page).screenshot({ path: info.outputPath(`AEON-1212/recorded-${width}-${look}.png`) }) } },
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
    const controls = { header: panel(page).locator('.pane-bar'), actions: panel(page).getByRole('group', { name: 'Session actions' }), composer: panel(page).locator('.composer'), card: question(page), options: question(page).locator('.question-options') }
    await panel(page).screenshot({ path: info.outputPath(`AEON-1212/long-text-chat-${width}-${look}.png`) })
    world.hold()
    await expectStableControls({ controls, scrollAreas: { body: panel(page).locator('.thread-scroll') }, interactions: [
      { name: 'record long option', run: async () => { await question(page).getByRole('button', { name: world.state.question.input.options[0]!.title, exact: true }).click(); await world.started(); await expect(question(page).getByRole('button', { name: 'Recording…', exact: true })).toBeDisabled() } },
      { name: 'recorded long answer', run: async () => { world.release(); await expect(question(page)).toContainText(`Answered: ${world.state.question.input.options[0]!.title}`) } },
    ] })
    await panel(page).screenshot({ path: info.outputPath(`AEON-1212/long-text-recorded-${width}-${look}.png`) })
    await tab(page, 'Lead').click()
    await expect(panel(page).locator('.queued-item')).toContainText('Agenten-Dial')
    await panel(page).screenshot({ path: info.outputPath(`AEON-1212/long-text-lead-${width}-${look}.png`) })
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
