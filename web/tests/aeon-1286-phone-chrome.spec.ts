// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1286: compact phone chrome (essentials only, the rest behind ⋯), approved phone
// design aeon-1438-chat-phone-approved.html. Risks: the chat chrome takes the thread's
// room; the head keeps unexplained or unlabelled controls (pin, pause, stop) and says the
// state twice; the safe area is padded twice and leaves a blank band under the composer;
// the ⋯ menu moves its items when a choice is ticked; a phone scrolls sideways; quiet
// text drops below 4.5:1 in either theme; the ticket list and the agent view spend the
// screen on chrome instead of rows.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import { dirname } from 'node:path'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { mockLeadFlow, LEAD_AGENT, LEAD_SESSION, NOW } from './lead-flow-fixtures'
import { controlStability } from './control-stability'

type Size = { width: number; height: number }
// Phone frames as the approved design draws them: status bar and home indicator are the safe areas.
const PHONES: { size: Size; safe: { top: number; bottom: number } }[] = [
  { size: { width: 390, height: 844 }, safe: { top: 47, bottom: 34 } },
  { size: { width: 320, height: 568 }, safe: { top: 20, bottom: 0 } },
]
const LANDSCAPE: Size = { width: 844, height: 340 }

const panelOf = (page: Page) => page.getByRole('complementary', { name: 'Session details', exact: true })
const tabOf = (page: Page, name: string) => panelOf(page).getByRole('tab', { name: new RegExp(`^${name}`) })
const menuOf = (page: Page) => page.getByRole('menu', { name: 'More session actions' })

async function report(path: string, lines: string[]) {
  await mkdir(dirname(path), { recursive: true })
  await writeFile(path, lines.join('\n') + '\n')
}

// WCAG contrast of an element's text over the layers actually behind it (as AEON-1438 measures it).
async function contrast(locator: Locator) {
  return locator.first().evaluate(el => {
    const canvas = document.createElement('canvas'); canvas.width = canvas.height = 1
    const ctx = canvas.getContext('2d', { willReadFrequently: true })!
    const rgba = (css: string) => { ctx.clearRect(0, 0, 1, 1); ctx.fillStyle = '#000'; ctx.fillStyle = css; ctx.fillRect(0, 0, 1, 1); const [r, g, b, a] = ctx.getImageData(0, 0, 1, 1).data; return [r!, g!, b!, a! / 255] as const }
    const layers: (readonly [number, number, number, number])[] = []
    for (let node: Element | null = el; node; node = node.parentElement) {
      const c = rgba(getComputedStyle(node).backgroundColor)
      if (c[3] > 0) layers.push(c)
      if (c[3] >= 1) break
    }
    let bg: [number, number, number] = [255, 255, 255]
    for (const [r, g, b, a] of layers.reverse()) bg = [r * a + bg[0] * (1 - a), g * a + bg[1] * (1 - a), b * a + bg[2] * (1 - a)]
    const [r, g, b, a] = rgba(getComputedStyle(el).color)
    const fg = [r * a + bg[0] * (1 - a), g * a + bg[1] * (1 - a), b * a + bg[2] * (1 - a)]
    const lum = (c: number[]) => { const [x, y, z] = c.map(v => { const s = v / 255; return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4 }); return 0.2126 * x! + 0.7152 * y! + 0.0722 * z! }
    const [hi, lo] = [lum(fg), lum(bg)].sort((p, q) => q - p)
    return (hi! + 0.05) / (lo! + 0.05)
  })
}

const noSidewaysScroll = (page: Page) => page.evaluate(() => {
  const doc = document.scrollingElement!, panel = document.querySelector('.session-panel')
  return doc.scrollWidth <= doc.clientWidth && (!panel || panel.scrollWidth <= panel.clientWidth)
})
// How often a word is painted in an element: visible text nodes only (no sr-only, nothing hidden).
const paintedCount = (locator: Locator, word: string) => locator.evaluate((root, w) => {
  let count = 0
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const parent = node.parentElement!
    if (parent.closest('.sr-only') || !parent.checkVisibility({ visibilityProperty: true, opacityProperty: true })) continue
    count += (node.textContent ?? '').split(w).length - 1
  }
  return count
}, word)
const box = (locator: Locator) => locator.evaluate(el => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height, bottom: r.bottom, right: r.right } })

// The safe areas of the design's phone frame, so env(safe-area-inset-*) is what an iPhone reports.
async function safeAreas(page: Page, safe: { top: number; bottom: number }) {
  const cdp = await page.context().newCDPSession(page)
  await cdp.send('Emulation.setSafeAreaInsetsOverride' as never, { insets: { top: safe.top, bottom: safe.bottom, left: 0, right: 0 } } as never)
  const read = await page.evaluate(() => {
    const probe = document.createElement('div')
    probe.style.cssText = 'position:fixed;padding-top:env(safe-area-inset-top);padding-bottom:env(safe-area-inset-bottom)'
    document.body.append(probe)
    const style = getComputedStyle(probe), value = { top: parseFloat(style.paddingTop), bottom: parseFloat(style.paddingBottom) }
    probe.remove()
    return value
  })
  expect(read, 'the browser reports the emulated safe areas').toEqual(safe)
}

async function openLeadChat(page: Page, size: Size, look: 'light' | 'dark') {
  await page.setViewportSize(size)
  await page.emulateMedia({ colorScheme: look, reducedMotion: 'reduce' })
  const world = await mockLeadFlow(page, { lead: 'working' })
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    if (['/api/models', '/api/approvals', '/api/agent-accounts', '/api/agent-accounts/capacity', '/api/agent-accounts/capacity/schedule'].includes(path)) return route.fulfill({ json: [] })
    return route.fallback()
  })
  // The lead's thread, so Chat shows its composer (the lead fixture serves no messages).
  const at = (minutes: number) => new Date(Date.parse(NOW) - minutes * 60_000).toISOString()
  const post = (n: number, body: string, mine: boolean, minutes: number) => ({
    id: `3e000000-0000-4000-8000-${String(n).padStart(12, '0')}`, project: 'p-pharos', body, reply_to: null, sent_event_id: 100 + n, created_at: at(minutes),
    is_action_request: false, expects_reply: false, delivery_level: 'simple', status: 'accepted', reply_obligation: 'none', human_resolution_outcome: null,
    ...(mine ? { sender_principal_id: me.id, recipient_principal_id: LEAD_AGENT, to: 'claude:lead', recipient_session_id: LEAD_SESSION, sender_label: me.name }
      : { sender_principal_id: LEAD_AGENT, recipient_principal_id: me.id, to: 'paimos:markus', sender_session_id: LEAD_SESSION, sender_label: 'Claude lead' }),
  })
  const thread = [
    post(1, 'Morning. The nightly is green; PHAROS-11 waits for its review.', false, 50),
    post(2, 'Take the review findings first, then the release check.', true, 44),
    post(3, 'On it. Three findings, all small; a Codex worker has them.', false, 40),
    post(4, 'Merged PHAROS-11. Shall I start the release check now?', false, 12),
  ]
  await page.route(/\/api\/projects\/[^/]+\/(messages|message-targets)(\?|$)/, route => {
    const url = new URL(route.request().url())
    if (route.request().method() !== 'GET') return route.fallback()
    if (url.pathname.endsWith('/message-targets')) return route.fulfill({ json: [] })
    const after = Number(url.searchParams.get('after') ?? 0)
    const items = url.searchParams.get('newest_first') === 'true' ? [...thread].reverse().filter(m => !after || m.sent_event_id < after) : thread.filter(m => m.sent_event_id > after)
    return route.fulfill({ json: { items, next_after: items.at(-1)?.sent_event_id ?? after, preamble: 'Untrusted agent message content follows.' } })
  })
  await page.route('**/api/inbox/message-status?*', route => route.fulfill({ json: { items: [] } }))
  await page.route(/\/api\/chat-threads\/[^/]+\/messages(\?|$)/, route => route.fulfill({ json: { contract: 'chat-v1', items: [], has_more_before: false, has_more_after: false, before_cursor: null, after_cursor: null, snapshot_cursor: 'c', migration_epoch: '0' } }))
  await page.route('**/api/chat-sessions/*/thread', route => route.fulfill({ json: { contract: 'chat-v1', conversation_id: '7c000000-0000-4000-8000-000000001286', role: { contract: 'chat-v1' }, revision: '2', binding_epoch: '1', readiness: { state: 'offline', capabilities: [], reason: 'session_offline' } } }))
  await page.goto(`/agents/${LEAD_SESSION}?tab=chat`)
  await expect(tabOf(page, 'Chat')).toHaveAttribute('aria-selected', 'true')
  await expect(panelOf(page).getByRole('textbox', { name: /^Message to / })).toBeVisible()
  return world
}

for (const { size, safe } of PHONES) for (const look of ['light', 'dark'] as const) {
  test(`AEON-1286 chat chrome ${size.width} ${look}: one head line pair, the menu row, the thread first, no band`, async ({ page }, info) => {
    const errors = watchErrors(page)
    const world = await openLeadChat(page, size, look)
    await safeAreas(page, safe)
    const panel = panelOf(page), head = panel.locator('.panel-head')

    // The head: ← · avatar · name over "state · model" · ⋯ — and nothing else.
    const back = panel.getByRole('button', { name: 'Close session details', exact: true })
    const more = panel.getByRole('button', { name: 'More session actions', exact: true })
    expect(await head.locator('.pane-bar > button, .pane-buttons > button').evaluateAll(buttons => buttons.filter(b => b.checkVisibility()).map(b => b.getAttribute('aria-label')))).toEqual(['Close session details', 'More session actions'])
    await expect(head.getByRole('button', { name: /Interrupt|Pause|Stop|Resume|default|Full screen/ })).toHaveCount(0)
    const [backBox, avatarBox, moreBox, headBox] = await Promise.all([box(back), box(head.locator('.agent-glyph')), box(more), box(head)])
    expect(backBox.x, '← sits at the left edge').toBeLessThanOrEqual(1)
    expect(backBox.right).toBeLessThanOrEqual(avatarBox.x)
    expect(Math.abs(moreBox.right - headBox.right), '⋯ sits at the right edge').toBeLessThanOrEqual(8)
    expect(avatarBox.width, 'the avatar is 40 px').toBeGreaterThanOrEqual(40)
    expect(headBox.y, 'the head starts below the status bar').toBeGreaterThanOrEqual(0)
    expect(await box(head.locator('.pane-bar')).then(b => b.y)).toBeGreaterThanOrEqual(safe.top - .5)
    for (const target of [back, more]) {
      const b = await box(target)
      expect(b.width).toBeGreaterThanOrEqual(44); expect(b.height).toBeGreaterThanOrEqual(44)
    }

    // The state, once: line 2 says it; the avatar is artwork; the heading tells screen readers.
    await expect(head.locator('.pane-state')).toBeVisible()
    await expect(head.locator('.pane-state .state-word')).toHaveText('Working')
    await expect(head.locator('.pane-model')).toContainText('high')
    expect(await paintedCount(head, 'Working'), 'the state word is painted once in the head').toBe(1)
    await expect(head.getByRole('img', { name: 'Working' })).toHaveCount(0)
    await expect(head.getByRole('heading', { level: 2 })).toHaveAccessibleName(/^Claude lead · Pharos · Working/)

    // The flat menu on its own row, no pin beside it; 44 px items; labels while they fit.
    const tabs = ['Lead', 'Details', 'Chat', 'Events']
    for (const name of tabs) {
      const b = await box(tabOf(page, name))
      expect(b.height, `${name} tab height`).toBeGreaterThanOrEqual(44); expect(b.width, `${name} tab width`).toBeGreaterThanOrEqual(44)
    }
    await expect(panel.getByRole('tablist').locator('.pinned, [aria-label="Your default"]')).toHaveCount(0)
    // Labels while they fit, then smaller labels, then icons: one form for all tabs, chosen by the
    // width only, so choosing a tab never moves one (AEON-541).
    const labelled = await panel.getByRole('tab').evaluateAll(items => items.map(item => (item.querySelector('.tab-label') as HTMLElement | null)?.checkVisibility() ?? false))
    expect(new Set(labelled).size, `one form for every tab: ${labelled}`).toBe(1)
    expect((await box(panel.getByRole('tablist'))).right, 'the menu never leaves its row').toBeLessThanOrEqual(size.width - 8 + .5)
    const tabGuard = await controlStability(page, Object.fromEntries(tabs.map(name => [name, tabOf(page, name)])))
    for (const name of ['Lead', 'Events', 'Chat']) await tabGuard.check(async () => { await tabOf(page, name).click(); await expect(tabOf(page, name)).toHaveAttribute('aria-selected', 'true') })
    tabGuard.done()
    expect((await box(panel.getByRole('tablist'))).y, 'the menu has its own row under the head').toBeGreaterThanOrEqual((await box(head.locator('.pane-identity'))).bottom)

    // The thread takes at least 60 % of the screen; the composer ends at the screen's foot and
    // pads the home indicator once — no blank band of a second inset under it.
    const thread = await box(panel.locator('#session-panel-messages .thread-scroll'))
    const composer = await box(panel.locator('#session-panel-messages .composer'))
    const field = await box(panel.getByRole('textbox', { name: /^Message to / }))
    const share = thread.height / size.height
    expect(share, 'thread share of the screen').toBeGreaterThanOrEqual(.6)
    expect(Math.abs(composer.bottom - size.height), 'the composer reaches the foot').toBeLessThanOrEqual(.5)
    expect(Math.round(size.height - field.bottom), 'under the field: the home indicator inset, or 8 px').toBe(Math.max(8, safe.bottom))
    expect(await noSidewaysScroll(page), 'no sideways scroll').toBe(true)

    // Contrast in both themes for every word of the chrome.
    for (const [name, target] of [
      ['name', head.locator('.pane-title .name')], ['title', head.locator('.title-detail')], ['state', head.locator('.pane-state .state-word')],
      ['model', head.locator('.pane-model')], ['selected tab', tabOf(page, 'Chat')], ['tab', tabOf(page, 'Events')],
    ] as const) expect(await contrast(target), `${name} contrast`).toBeGreaterThanOrEqual(4.5)
    await page.screenshot({ path: info.outputPath(`AEON-1286/chat-${size.width}-${look}.png`) })

    // ⋯ opens as a sheet from the bottom: control first, then how the panel opens, then layout.
    await more.click()
    const menu = menuOf(page), sheet = page.locator('.floating.bottom')
    await expect(menu).toBeVisible()
    const sheetBox = await box(sheet)
    expect(sheetBox.x).toBeLessThanOrEqual(.5); expect(Math.abs(sheetBox.width - size.width)).toBeLessThanOrEqual(.5)
    expect(Math.abs(sheetBox.bottom - size.height), 'the sheet sits on the bottom edge').toBeLessThanOrEqual(.5)
    await expect(page.locator('.floating-scrim')).toBeVisible()
    const names = await menu.locator('[role^="menuitem"]').evaluateAll(items => items.filter(i => i.checkVisibility()).map(i => (i.firstElementChild?.nextElementSibling?.firstChild?.textContent ?? i.textContent ?? '').trim()))
    const at = (label: string) => names.findIndex(name => name.startsWith(label))
    for (const label of ['Interrupt', 'Pause…', 'Stop…', 'Open on Chat by default', 'Full screen']) expect(at(label), `${label} in ⋯ (${names.join(' | ')})`).toBeGreaterThanOrEqual(0)
    expect(at('Interrupt')).toBeLessThan(at('Pause…')); expect(at('Pause…')).toBeLessThan(at('Stop…'))
    expect(at('Stop…')).toBeLessThan(at('Open on Chat by default')); expect(at('Open on Chat by default')).toBeLessThan(at('Full screen'))
    expect(await menu.locator('[role="separator"]').count(), 'hairlines between the groups').toBeGreaterThanOrEqual(2)
    for (const item of await menu.locator('[role^="menuitem"]').all()) if (await item.isVisible()) expect((await box(item)).height).toBeGreaterThanOrEqual(44)
    const stop = menu.getByRole('menuitem', { name: /^Stop…/ })
    for (const [name, target] of [
      ['Interrupt', menu.getByRole('menuitem', { name: /^Interrupt/ }).locator('span').first()], ['Stop…', stop.locator('span').first()], ['sub-label', stop.locator('small')],
    ] as const) expect(await contrast(target), `${name} contrast in ⋯`).toBeGreaterThanOrEqual(4.5)

    // The default tab is a labelled choice for the tab on screen; ticking it moves nothing.
    const pin = menu.getByRole('menuitemcheckbox', { name: /^Open on Chat by default/ })
    await expect(pin).toHaveAttribute('aria-checked', 'false')
    await expect(pin).toContainText('Now it opens on the last tab used')
    const guard = await controlStability(page, { pin, interrupt: menu.getByRole('menuitem', { name: /^Interrupt/ }), stop, full: menu.getByRole('menuitem', { name: /^Full screen/ }) })
    await guard.check(async () => {
      await pin.click()
      await expect(pin).toHaveAttribute('aria-checked', 'true')
      await expect(pin).toContainText('Unless something needs you')
    })
    guard.done()
    await expect.poll(() => world.data.preferences['agents:lead-panel']).toMatchObject({ pinned: 'chat' })
    await expect(menu, 'a ticked choice keeps the sheet open').toBeVisible()
    await page.screenshot({ path: info.outputPath(`AEON-1286/menu-${size.width}-${look}.png`) })
    await page.keyboard.press('Escape')
    await expect(menu).toHaveCount(0)
    await expect(more).toBeFocused()
    expect(errors).toEqual([])
  })
}

for (const look of ['light', 'dark'] as const) test(`AEON-1286 short landscape ${LANDSCAPE.width}x${LANDSCAPE.height} ${look}: full-screen sheet, one head line, thread at least 60 %`, async ({ page }, info) => {
  await openLeadChat(page, LANDSCAPE, look)
  const panel = panelOf(page), head = panel.locator('.panel-head')
  const panelBox = await box(panel)
  expect(panelBox.y).toBeLessThanOrEqual(.5); expect(Math.abs(panelBox.bottom - LANDSCAPE.height)).toBeLessThanOrEqual(.5)
  expect(panelBox.x).toBeLessThanOrEqual(.5); expect(Math.abs(panelBox.width - LANDSCAPE.width)).toBeLessThanOrEqual(.5)
  expect((await box(head)).height, 'head and menu share one line').toBeLessThanOrEqual(49)
  expect((await box(panel.getByRole('tablist'))).y).toBeLessThan((await box(head)).bottom)
  await expect(panel.getByRole('button', { name: 'Add a ticket or session' }), 'the one-line phone composer').toBeVisible()
  const thread = await box(panel.locator('#session-panel-messages .thread-scroll'))
  expect(thread.height / LANDSCAPE.height, 'thread share in landscape').toBeGreaterThanOrEqual(.6)
  expect(await noSidewaysScroll(page)).toBe(true)
  await page.screenshot({ path: info.outputPath(`AEON-1286/chat-landscape-${LANDSCAPE.width}-${look}.png`) })
  await report(info.outputPath(`AEON-1286/landscape-${look}.md`), [`thread ${Math.round(thread.height)} px of ${LANDSCAPE.height} = ${(thread.height / LANDSCAPE.height * 100).toFixed(1)} %`])
})

test('AEON-1286 1024 sanity: the docked head and its anchored menu are unchanged in form', async ({ page }, info) => {
  await openLeadChat(page, { width: 1024, height: 768 }, 'light')
  const panel = panelOf(page), head = panel.locator('.panel-head')
  await expect(head.locator('.pane-back')).toBeHidden()
  await expect(head.locator('.pane-state')).toBeHidden()
  await expect(head.locator('.pane-close')).toBeVisible()
  expect((await box(head)).height).toBeLessThanOrEqual(49)
  await expect(panel.getByRole('tablist').locator('.pinned')).toHaveCount(0)
  await panel.getByRole('button', { name: 'More session actions', exact: true }).click()
  await expect(menuOf(page)).toBeVisible()
  await expect(page.locator('.floating.bottom')).toHaveCount(0)
  await expect(page.locator('.floating-scrim')).toHaveCount(0)
  await expect(menuOf(page).getByRole('menuitemcheckbox', { name: /^Open on Chat by default/ })).toBeVisible()
  await page.screenshot({ path: info.outputPath('AEON-1286/chat-1024-light.png') })
})

// Ticket list and agent view: rows first. A page opened on a phone folds the dial's body (its
// saved fold belongs to wider screens), and on short landscape screens the footer lies over
// the page and folds away while reading. Row counts are recorded as evidence for the hand-over.
const fullyVisible = (page: Page, rows: string) => page.evaluate(selector => [...document.querySelectorAll<HTMLElement>(selector)].filter(row => {
  const b = row.getBoundingClientRect(), x = b.left + Math.min(b.width / 2, 120)
  if (b.height === 0 || b.top < 0 || b.bottom > innerHeight) return false
  const top = document.elementFromPoint(x, b.top + 1), bottom = document.elementFromPoint(x, b.bottom - 1)
  return !!top && !!bottom && row.contains(top) && row.contains(bottom)
}).length, rows)
async function listWorld(page: Page, size: Size) {
  const now = Date.parse('2026-10-16T08:00:00Z')
  await page.setViewportSize(size)
  await page.clock.install({ time: now })
  const work = fixtures({ bigProject: 40 })
  await mockWork(page, work, { admin: true })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {} })
  const base = data.sessions[1]!
  for (let n = 0; n < 24; n++) data.sessions.push({ ...base, id: `5e000000-0000-4000-8000-${String(100 + n).padStart(12, '0')}`, display_label: `worker-${n + 1}` } as typeof base)
  await mockAgents(page, data)
  return work
}
// Read down the page: the app's scroller moves by whole steps, as a finger would.
const readDown = (page: Page, by: number) => page.locator('main').evaluate((main, step) => { main.scrollTop += step }, by)

for (const size of [{ width: 390, height: 664 }, { width: 320, height: 568 }]) {
  test(`AEON-1286 agent view ${size.width}x${size.height}: a phone opens on the sessions, the dial folded for the visit`, async ({ page }, info) => {
    const work = await listWorld(page, size)
    // The person's saved fold is open (as on the desktop); the phone still opens folded.
    work.preferences['ui.agents.sections'] = { dial: true }
    await page.goto('/agents')
    const fold = page.locator('.cdial .fs-tog'), rows = page.locator('[data-row^="s:"]')
    await expect(rows.nth(5)).toBeAttached()
    await expect(fold).toHaveAttribute('aria-expanded', 'false')
    await expect(page.locator('.dial-card')).toHaveCount(0)
    const firstRow = await box(rows.first())
    expect(firstRow.y, 'the first session is on the first screen').toBeLessThan(size.height)
    const atTop = await fullyVisible(page, '[data-row^="s:"]')
    await page.screenshot({ path: info.outputPath(`AEON-1286/agents-${size.width}x${size.height}.png`) })
    await readDown(page, 900)
    const reading = await fullyVisible(page, '[data-row^="s:"]')
    await page.screenshot({ path: info.outputPath(`AEON-1286/agents-reading-${size.width}x${size.height}.png`) })
    // Its toggle unfolds the body for this visit; the saved choice stays as it was.
    await readDown(page, -5000)
    await fold.click()
    await expect(page.locator('.dial-card')).toBeVisible()
    expect(work.preferences['ui.agents.sections']).toEqual({ dial: true })
    await report(info.outputPath(`AEON-1286/agents-rows-${size.width}x${size.height}.md`), [`agents ${size.width}x${size.height}: first session row at ${Math.round(firstRow.y)} px; ${atTop} rows fully on screen at the top, ${reading} while reading (baseline on main: first row ~1,940 px, 0 rows)`])
  })
}

for (const look of ['light', 'dark'] as const) test(`AEON-1286 short landscape ${LANDSCAPE.width}x${LANDSCAPE.height} ${look}: the footer folds away while reading the lists`, async ({ page }, info) => {
  await page.emulateMedia({ colorScheme: look, reducedMotion: 'reduce' })
  await listWorld(page, LANDSCAPE)
  const footer = page.locator('.app-footer')
  const lines: string[] = []
  for (const [name, path, rows] of [['tickets', '/p/AEON', 'tr.ticket-row'], ['sessions', '/agents', '[data-row^="s:"]']] as const) {
    await page.goto(path)
    await expect(page.locator(rows).nth(5)).toBeAttached()
    await expect(footer).not.toHaveClass(/hidden/)
    // The footer lies over the page's end; the page keeps its room so the last row clears it.
    expect(await page.locator('main').evaluate(main => parseFloat(getComputedStyle(main).paddingBottom))).toBeGreaterThan(0)
    await readDown(page, 400)
    await expect(footer, `${name}: the footer folds away while reading down`).toHaveClass(/hidden/)
    await expect.poll(() => footer.evaluate(el => el.getBoundingClientRect().top), `${name}: folded below the screen`).toBeGreaterThanOrEqual(LANDSCAPE.height - .5)
    const reading = await fullyVisible(page, rows)
    await page.screenshot({ path: info.outputPath(`AEON-1286/${name}-reading-${LANDSCAPE.width}-${look}.png`) })
    // The footer settles for 260 ms after it moves; past that, reading back up brings it back.
    await page.clock.runFor(300)
    await readDown(page, -40)
    await expect(footer, `${name}: reading back up brings it back`).not.toHaveClass(/hidden/)
    lines.push(`${name} ${LANDSCAPE.width}x${LANDSCAPE.height}: ${reading} rows fully on screen while reading`)
  }
  await report(info.outputPath(`AEON-1286/landscape-rows-${look}.md`), lines)
})
