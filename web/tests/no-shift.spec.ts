// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { capacityWorld } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import type { Shell } from './helpers/no-shift-shells'

const session = (n: number) => `5e000000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`
async function setup(page: Page, failDecision = false) {
  const work = fixtures()
  work.preferences['agents.working'] = { total: 9, limits: { codex: 1 } }
  await mockWork(page, work, { admin: true })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  const capacity = capacityWorld()
  data.accounts = (capacity.accounts as unknown as typeof data.accounts).map(a => ({ ...a, registered_by_principal_id: me.id }))
  await mockAgents(page, data, { capacity, failDecision, workingPreference: () => work.preferences['agents.working'] })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions.push('account.read', 'account.manage')
    return route.fulfill({ json: answer })
  })
  return data
}

for (const width of [1440, 1024, 390]) {
  test(`ticket relation popover keeps its selectors through result changes at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 800 })
    await mockWork(page, fixtures())
    await page.goto('/p/PHAROS/PHAROS-12')
    const ticket = page.getByRole('complementary', { name: 'Ticket details' })
    await expect(ticket.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
    await ticket.focus()
    await page.keyboard.press('r')
    const dialog = page.getByRole('dialog', { name: 'Link PHAROS-12 to another ticket' })
    const search = dialog.getByRole('combobox')
    await expect(dialog).toBeVisible()
    await expectStableControls({
      controls: { search, selector: dialog.getByRole('radiogroup'), blocks: dialog.getByRole('radio', { name: 'Blocked by', exact: true }) },
      scrollAreas: { dialog },
      interactions: [
        { name: 'switch relation', run: () => dialog.getByRole('radio', { name: 'Blocked by', exact: true }).click() },
        { name: 'one result', run: async () => { await search.fill('PHAROS-14'); await expect(dialog.getByRole('option')).toHaveCount(1) } },
        { name: 'no results', run: async () => { await search.fill('no match anywhere'); await expect(dialog.getByText(/Nothing matches/)).toBeVisible() } },
        { name: 'results return', run: async () => { await search.fill('PHAROS-1'); await expect(dialog.getByRole('option').first()).toContainText('PHAROS-1') } },
      ],
    })
    await search.fill('no match anywhere')
    await expect(dialog.getByText(/Nothing matches/)).toBeVisible()
    if (!(await dialog.evaluate(el => el.classList.contains('above')))) {
      await expect.poll(async () => (await dialog.boundingBox())!.height, 'empty downward picker stays short').toBeLessThan(320)
    }
  })

  test(`managed Interrupt and Stop controls stay put at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const data = await setup(page)
    const ownership = { daemon_id: 'fixture', generation: 'a'.repeat(32), process_id: 'b'.repeat(32), root_pid: 1234, group_id: 1234, started_at: new Date().toISOString() }
    Object.assign(data.sessions[0]!, { advertised_capabilities: ['status', 'interrupt', 'stop', 'managed_control_v1'], process_ownership: ownership, process_observed_at: new Date().toISOString() })
    let request: Record<string, unknown>
    await page.route('**/api/projects/*/harness-sessions/*/managed-controls', route => {
      request = route.request().postDataJSON()
      return route.fulfill({ status: 201, json: { id: request.request_id, session_id: session(1), kind: request.kind, state: 'pending', expires_at: new Date(Date.now() + 45000).toISOString() } })
    })
    await page.route('**/api/projects/*/harness-sessions/*/controls/*', route => route.fulfill({ json: { id: request.request_id, session_id: session(1), kind: request.kind, state: 'completed', outcome: 'applied', reason: 'native_interrupt_acknowledged' } }))
    await page.goto(`/agents/${session(1)}`)
    const controls = page.getByRole('region', { name: 'Session controls' })
    const interrupt = controls.getByRole('button', { name: 'Interrupt', exact: true }), stop = controls.getByRole('button', { name: 'Stop', exact: true })
    await expectStableControls({
      controls: { interrupt, stop },
      interactions: [
        { name: 'interrupt receipt', run: async () => { await interrupt.click(); await expect(controls.getByRole('status')).toContainText('Interrupt applied') } },
        { name: 'open and cancel Stop', run: async () => { await stop.click(); await page.getByRole('button', { name: 'Confirm stop' }).hover(); await page.getByRole('button', { name: 'Cancel', exact: true }).click() } },
      ],
    })
    if (width === 390) {
      await stop.click()
      const sheet = page.getByRole('dialog', { name: 'Stop', exact: true })
      await expectStableControls({
        controls: { sheet, close: sheet.getByRole('button', { name: 'Close' }), confirm: sheet.getByRole('button', { name: 'Confirm stop' }), cancel: sheet.getByRole('button', { name: 'Cancel' }) },
        scrollAreas: { body: sheet.locator('.sheet-body') },
        interactions: [{ name: 'hover confirmation', run: () => sheet.getByRole('button', { name: 'Confirm stop' }).hover() }],
      })
    }
  })

  test(`approval choices, typing and failure keep the decision controls put at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await setup(page, true)
    await page.goto('/agents')
    const card = page.getByRole('region', { name: 'Needs you' }).locator('[data-row^="a:"]').first()
    await card.getByRole('button', { name: 'Approve', exact: true }).click()
    const form = card.locator('.decision')
    const submit = form.locator('button[type="submit"]')
    await expectStableControls({
      controls: { card, form, reason: form.locator('textarea'), cancel: form.getByRole('button', { name: 'Cancel' }), submit },
      scrollAreas: { feedback: form.locator('.decision-feedback') },
      interactions: [
        ...['d', 'a'].map(key => ({ name: `switch to ${key === 'd' ? 'deny' : 'approve'}`, run: async () => { await card.focus(); await page.keyboard.press(key); await expect(submit).toContainText(key === 'd' ? 'Deny permission' : 'Approve permission') } })),
        { name: 'type a long reason', run: () => form.locator('textarea').fill('Keep the controls still. '.repeat(100)) },
        { name: 'failed decision feedback', run: async () => { await submit.click(); await expect(form.getByRole('alert')).toBeVisible(); await expect(submit).toBeEnabled() } },
      ],
    })
  })

  test(`Stop confirmation stays the same through the session series at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await setup(page)
    await page.goto('/agents')
    async function open(n: number) {
      await page.locator(`[data-row="s:${session(n)}"]`).getByRole('button', { name: /^Actions for/ }).click()
      await page.getByRole('menuitem', { name: /Stop session/ }).click()
      await expect(page.locator('.confirm[open]')).toBeVisible()
    }
    await open(1)
    const dialog = page.locator('.confirm[open]')
    const cancel = dialog.getByRole('button', { name: 'Cancel' })
    await expectStableControls({
      controls: { ...(width === 390 ? { dialog } : {}), cancel, stop: dialog.getByRole('button', { name: 'Stop session' }) },
      scrollAreas: { body: dialog.locator('#confirm-body') },
      interactions: [2, 1].map(n => ({ name: `next/previous session ${n}`, run: async () => { await cancel.click(); await open(n); await expect(cancel).toBeFocused() } })),
    })
  })

  test(`Agents working dial and selectors stay put at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    await setup(page)
    await page.goto('/agents')
    const dial = page.getByRole('region', { name: 'Agents at once' })
    const more = dial.getByRole('button', { name: 'One agent more at once' })
    const fewer = dial.getByRole('button', { name: 'One agent fewer at once' })
    const row = dial.locator('[data-key="codex"]')
    await expect(row).toBeVisible()
    await expect(more).toHaveCSS('transition-property', 'background')
    const rowCount = await dial.locator('.rows > li').count()
    const rowsHeight = (await dial.locator('.rows').boundingBox())!.height
    expect(rowsHeight, 'rows reserve controls and their explanation slot').toBeLessThanOrEqual(rowCount * (width <= 760 ? 127 : 61))
    await expectStableControls({
      controls: { more, fewer, selector: row.getByRole('radiogroup'), row, rowMore: row.getByRole('button', { name: 'Codex: at most one more' }), rowFewer: row.getByRole('button', { name: 'Codex: at most one fewer' }) },
      scrollAreas: { rows: dial.locator('.rows') },
      interactions: [
        { name: 'hold total stepper', run: async () => { await more.hover(); await page.mouse.down() } },
        { name: 'release total stepper', run: () => page.mouse.up() },
        ...[more, more, fewer, fewer].map((button, i) => ({ name: `total step ${i + 1}`, run: () => button.click() })),
        ...['more', 'fewer'].map(direction => ({ name: `${direction} on Codex`, run: () => row.getByRole('button', { name: `Codex: at most one ${direction}` }).click() })),
      ],
    })
    await expectStableControls({
      controls: { more, fewer, selector: row.getByRole('radiogroup'), row },
      scrollAreas: { rows: dial.locator('.rows') },
      interactions: ['No limit', 'Off', 'At most'].map(name => ({ name, run: async () => { await row.getByRole('radio', { name, exact: true }).click(); await expect(row.getByRole('radio', { name, exact: true })).toHaveAttribute('aria-checked', 'true') } })),
    })
  })
}

for (const width of [1440, 390]) {
  test(`confirmation content grows away from its controls at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 844 })
    await mockWork(page, fixtures())
    await page.goto('/p/PHAROS')
    await expect(page.locator('.confirm')).toHaveCount(1)
    const open = (body: string) => page.evaluate(async body => {
      const modulePath = '/src/lib/confirm.ts'
      const { confirmAction } = await import(/* @vite-ignore */ modulePath)
      void confirmAction({ title: 'Confirm change', body, confirmLabel: 'Accept', danger: true })
    }, body)
    await open('A short explanation.')
    const dialog = page.locator('.confirm[open]')
    await expect(dialog).toBeVisible()
    const initial = await dialog.boundingBox()
    if (width > 720) expect(initial!.height, 'short content has no padded body').toBeLessThan(240)
    await expectStableControls({
      controls: { title: dialog.locator('h2'), actions: dialog.locator('.actions'), accept: dialog.getByRole('button', { name: 'Accept' }), cancel: dialog.getByRole('button', { name: 'Cancel' }) },
      scrollAreas: { body: dialog.locator('#confirm-body') },
      interactions: [
        { name: 'longer explanation', run: () => open('A much longer explanation that wraps. '.repeat(100)) },
        { name: 'scroll explanation', run: () => dialog.locator('#confirm-body').evaluate(el => { el.scrollTop = 100 }) },
        { name: 'short explanation again', run: () => open('A short explanation.') },
      ],
    })
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused()
  })
}

test('saving a pacing option keeps keyboard focus for Escape', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1100 })
  await setup(page)
  let release!: () => void
  const pending = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/agent-accounts/capacity/schedule', async route => {
    if (route.request().method() === 'PUT') await pending
    await route.fallback()
  })
  await page.goto('/agents')
  await page.getByRole('region', { name: 'Accounts and computers' }).getByRole('button', { name: /days · keep/ }).click()
  const pacing = page.getByRole('dialog', { name: 'Pacing' })
  const seven = pacing.getByRole('radio', { name: '7', exact: true })
  await seven.click()
  await expect(seven).toHaveAttribute('aria-disabled', 'true')
  await expect(seven).toBeFocused()
  release()
  await expect(seven).toHaveAttribute('aria-checked', 'true')
  await expect(seven).toHaveAttribute('aria-disabled', 'false')
  await expect(seven).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(pacing).toHaveCount(0)
})

test('phone steer feedback and retry never move the pinned action bar', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const data = await setup(page)
  Object.assign(data.sessions[0]!, { advertised_capabilities: ['status', 'steer', 'stop', 'managed_control_v1'], process_ownership: { daemon_id: 'fixture', generation: 'a'.repeat(32), process_id: 'b'.repeat(32), root_pid: 1234, group_id: 1234, started_at: new Date().toISOString() }, process_observed_at: new Date().toISOString() })
  await page.route('**/api/projects/*/harness-sessions/*/managed-controls', route => route.abort('failed'))
  await page.goto(`/agents/${session(1)}`)
  await page.getByRole('region', { name: 'Session controls' }).getByRole('button', { name: 'Steer', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Steer', exact: true })
  const send = sheet.getByRole('button', { name: 'Send steer' })
  await expectStableControls({
    controls: { sheet, send, cancel: sheet.getByRole('button', { name: 'Cancel' }), footer: sheet.locator('.pinned-actions'), draft: sheet.getByLabel('What should change?') },
    scrollAreas: { body: sheet.locator('.sheet-body') },
    interactions: [
      { name: 'long draft', run: () => sheet.getByLabel('What should change?').fill('Keep the footer still. '.repeat(200)) },
      { name: 'lost response and retry controls', run: async () => { await send.click(); await expect(sheet.getByRole('alert')).toBeVisible(); await expect(sheet.getByRole('button', { name: 'Retry same request' })).toBeVisible() } },
    ],
  })
})

test('stability guard allows scrolling but detects movement, resizing and overflow', async ({ page }) => {
  await page.setContent('<div id="scroll" style="height:120px;width:200px;overflow:auto"><div style="height:600px"><button id="control" style="margin-top:60px">Choose</button></div></div>')
  const controls = { choose: page.locator('#control') }, scrollAreas = { body: page.locator('#scroll') }
  await expectStableControls({ controls, scrollAreas, interactions: [{ name: 'scroll', run: () => page.locator('#scroll').evaluate(el => { el.scrollTop = 30 }) }] })
  for (const [name, property, value] of [['movement', 'marginLeft', '2px'], ['resize', 'width', '180px']] as const) {
    await expect(expectStableControls({ controls, interactions: [{ name, run: () => page.locator('#control').evaluate((el, change) => { (el as HTMLElement).style[change.property] = change.value }, { property, value }) }] })).rejects.toThrow(/choose\.(x|width)/)
  }
  await expect(expectStableControls({ controls, scrollAreas, interactions: [{ name: 'overflow', run: () => page.locator('#scroll > div').evaluate(el => { (el as HTMLElement).style.width = '300px' }) }] })).rejects.toThrow(/horizontal overflow/)
})

test('stability guard rejects empty interactions and collapse during its animation wait', async ({ page }) => {
  await page.setContent('<div id="control" style="width:100px;height:40px">Choose</div>')
  const controls = { choose: page.locator('#control') }
  await expect(expectStableControls({ controls, interactions: [] })).rejects.toThrow(/at least one interaction/)
  await page.locator('#control').evaluate(el => {
    const animation = el.animate([{ opacity: 1 }, { opacity: 0.9 }], { duration: 500 })
    animation.onfinish = () => Object.assign((el as HTMLElement).style, { width: '0px', height: '0px' })
  })
  await expect(expectStableControls({ controls, interactions: [{ name: 'hover', run: () => page.locator('#control').hover() }] })).rejects.toThrow(/sampled control (width|height) must be positive/)
})

async function mountShell(page: Page, kind: Shell, width: number, above = false) {
  await page.setViewportSize({ width, height: 1000 })
  await mockWork(page, fixtures(), { admin: true })
  await page.goto('/p/PHAROS')
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await page.evaluate(async ({ kind, phone, above }) => {
    const path = '/tests/helpers/no-shift-shells.ts'
    const { mountShell } = await import(/* @vite-ignore */ path)
    mountShell(kind, phone, above)
  }, { kind, phone: width === 390, above })
}
const setLongContent = (page: Page, long: boolean) => page.evaluate(async long => {
  const path = '/tests/helpers/no-shift-shells.ts'
  const { setLongContent } = await import(/* @vite-ignore */ path)
  setLongContent(long)
}, long)

for (const width of [1440, 390]) {
  for (const kind of ['access', 'rules'] as const) {
    test(`${kind} short task grows below desktop actions and pins phone actions at ${width}`, async ({ page }) => {
      await mountShell(page, kind, width)
      const dialog = page.getByRole('dialog', { name: 'Short task' })
      const body = dialog.locator(kind === 'access' ? '.sheet-body' : '.body')
      await expect(dialog).toBeVisible()
      if (width === 1440) expect((await dialog.boundingBox())!.height, 'short task fits its content').toBeLessThan(240)
      else expect((await dialog.boundingBox())!.height).toBe(1000)
      await expectStableControls({
        controls: { ...(width === 390 ? { dialog } : {}), title: dialog.getByRole('heading'), accept: dialog.getByRole('button', { name: 'Accept' }), close: dialog.getByRole('button', { name: /^Close/ }) },
        scrollAreas: { body },
        interactions: [
          { name: 'long body', run: async () => { await setLongContent(page, true); await expect(body).toContainText('A longer explanation') } },
          { name: 'scroll body', run: () => body.evaluate(el => { el.scrollTop = 120 }) },
          { name: 'short body again', run: async () => { await setLongContent(page, false); await expect(body).toHaveText('A short explanation.') } },
        ],
      })
      if (width === 390) {
        const accept = (await dialog.getByRole('button', { name: 'Accept' }).boundingBox())!
        expect(accept.y + accept.height).toBeGreaterThan(950)
        if (kind === 'access') await expect(dialog.locator('.sheet-foot')).toHaveCSS('padding-bottom', '14px')
      }
    })
  }

  test(`Keep for you uses a natural vendor disclosure and stable actions at ${width}`, async ({ page }) => {
    await mountShell(page, 'keep', width)
    const dialog = page.getByRole('dialog', { name: 'Keep for you' })
    const vendors = dialog.getByRole('button', { name: 'Per vendor' })
    await expect(vendors).toHaveAttribute('aria-expanded', 'false')
    expect((await dialog.locator('.vendors').boundingBox())!.height, 'closed vendors contain only the disclosure').toBe(30)
    if (width === 1440) expect((await dialog.boundingBox())!.height).toBeLessThan(720)
    else expect((await dialog.boundingBox())!.height).toBe(1000)
    await expectStableControls({
      controls: { ...(width === 390 ? { dialog } : {}), save: dialog.getByRole('button', { name: 'Save', exact: true }), cancel: dialog.getByRole('button', { name: 'Cancel' }), choices: dialog.locator('.modes'), vendors },
      scrollAreas: { body: dialog.locator('.ed-body') },
      interactions: [
        { name: 'open vendors', run: () => vendors.click() },
        { name: 'close vendors', run: () => vendors.click() },
        { name: 'fixed share', run: () => dialog.getByRole('radio', { name: 'A fixed share' }).check() },
        { name: 'nothing', run: () => dialog.getByRole('radio', { name: /Nothing/ }).check() },
      ],
    })
  })

  test(`schedule model changes grow downward with actions in place at ${width}`, async ({ page }) => {
    await mountShell(page, 'night', width)
    const dialog = page.getByRole('dialog', { name: 'Agents outside your hours' })
    if (width === 1440) expect((await dialog.boundingBox())!.height).toBeLessThan(720)
    else expect((await dialog.boundingBox())!.height).toBe(1000)
    await expectStableControls({
      controls: { ...(width === 390 ? { dialog } : {}), save: dialog.getByRole('button', { name: 'Save', exact: true }), cancel: dialog.getByRole('button', { name: 'Cancel' }), models: dialog.locator('.models') },
      scrollAreas: { body: dialog.locator('.ed-body') },
      interactions: ['No shifts', 'Day and night', 'Three shifts', 'Custom blocks', 'No shifts'].map(name => ({ name, run: () => dialog.getByRole('radio', { name: new RegExp(`^${name}`) }).check() })),
    })
  })

  test(`short recovery states keep actions above content at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const data = await setup(page)
    const selected = data.sessions[1]!
    Object.assign(selected, { management_mode: 'unmanaged', advertised_capabilities: ['status'] })
    let release!: () => void
    const pending = new Promise<void>(resolve => { release = resolve })
    await page.route('**/harness-sessions/*/recovery', async route => {
      await pending
      await route.fulfill({ json: { session_id: selected.id, can_archive: false, force_stop_available: false, force_stop_reason: 'Force stop unavailable.', observed_revision: 'a'.repeat(64), host: 'fixture' } })
    })
    await page.goto(`/agents/${selected.id}`)
    await page.getByRole('button', { name: 'Recover', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Recover session' })
    await expect(dialog.getByText('Loading current session…')).toBeVisible()
    if (width === 1440) expect((await dialog.boundingBox())!.height).toBeLessThan(330)
    await expectStableControls({
      controls: { ...(width === 390 ? { dialog } : {}), title: dialog.getByRole('heading'), cancel: dialog.getByRole('button', { name: 'Cancel' }) },
      scrollAreas: { body: dialog.locator('.recovery-body') },
      interactions: [{ name: 'no recovery action', run: async () => { release(); await expect(dialog.getByText('No recovery action is available for this registration.')).toBeVisible() } }],
    })
    if (width === 1440) expect((await dialog.boundingBox())!.height).toBeLessThan(400)
  })

  test(`Done validation keeps its action position without padding at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await mockWork(page, fixtures())
    await page.goto('/p/PHAROS')
    await page.getByRole('button', { name: /Change status of PHAROS-11/ }).click()
    await page.getByRole('menuitemradio', { name: 'Done', exact: true }).click()
    const dialog = page.locator('.gate[open]')
    await expect(dialog).toBeVisible()
    if (width === 1440) expect((await dialog.boundingBox())!.height).toBeLessThan(680)
    await expectStableControls({
      controls: { ...(width === 390 ? { dialog } : {}), actions: dialog.locator('.actions'), submit: dialog.getByRole('button', { name: 'Mark done' }), cancel: dialog.getByRole('button', { name: 'Not now' }) },
      scrollAreas: { body: dialog.locator('.scroll') },
      interactions: [
        { name: 'validation errors', run: async () => { await dialog.getByRole('button', { name: 'Mark done' }).click(); await expect(dialog.getByLabel('Pill · English')).toHaveAttribute('aria-invalid', 'true') } },
        { name: 'resize text field', run: () => dialog.getByLabel('Benefit · English').evaluate(el => { (el as HTMLElement).style.height = '600px' }) },
        { name: 'scroll fields', run: () => dialog.locator('.scroll').evaluate(el => { el.scrollTop = 150 }) },
      ],
    })
  })
}

for (const above of [false, true]) test(`floating search ${above ? 'above' : 'below'} its trigger keeps controls while results change`, async ({ page }) => {
  await mountShell(page, 'floating', 1440, above)
  const dialog = page.getByRole('dialog', { name: 'Fixture picker' })
  await expect(dialog).toBeVisible()
  if (!above) expect((await dialog.boundingBox())!.height).toBeLessThan(160)
  await expectStableControls({
    controls: { ...(above ? { dialog } : {}), search: dialog.getByRole('textbox') },
    scrollAreas: { dialog },
    interactions: [
      { name: 'long results', run: () => setLongContent(page, true) },
      { name: 'short results', run: () => setLongContent(page, false) },
    ],
  })
})

for (const width of [1440, 1024]) {
  for (const constrained of [false, true]) test(`Display grouping keeps sort, density and columns in their slots at ${width}${constrained ? ' with wider text and scrollbar' : ''}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await mockWork(page, fixtures())
    await page.goto('/p/PHAROS')
    // Exercise wider text metrics and a classic scrollbar even on macOS, whose
    // default overlay scrollbar and system font leave more room than Linux CI.
    if (constrained) await page.addStyleTag({ content: '.display-panel { font-family: var(--mono); } .floating { scrollbar-gutter: stable; } .floating::-webkit-scrollbar { width: 15px; height: 15px; }' })
    await page.getByRole('button', { name: /^Display:/ }).click()
    const dialog = page.getByRole('dialog', { name: 'Display options', exact: true })
    const groups = dialog.getByRole('radiogroup', { name: 'Group by' })
    await expectStableControls({
      controls: { groups, ...Object.fromEntries(['None', 'Status', 'Priority'].map(name => [name, groups.getByRole('radio', { name, exact: true })])), expand: dialog.getByRole('button', { name: 'Expand groups' }), collapse: dialog.getByRole('button', { name: 'Collapse groups' }), sort: dialog.locator('.sort-editor'), density: dialog.getByRole('radiogroup', { name: 'Row height' }), columns: dialog.locator('.columns') },
      scrollAreas: { dialog },
      interactions: ['None', 'Status', 'Priority', 'None'].map(name => ({ name, run: async () => { await groups.getByRole('radio', { name, exact: true }).click(); await expect(groups.getByRole('radio', { name, exact: true })).toHaveAttribute('aria-checked', 'true') } })),
    })
    await expect(dialog.getByRole('button', { name: 'Expand groups' })).toBeDisabled()
    for (const button of await dialog.locator('.pair button').all()) {
      expect(await button.evaluate(el => el.scrollWidth - el.clientWidth), 'group action label fits its button').toBeLessThanOrEqual(1)
    }
  })

  test(`invalid date range feedback never moves Apply at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await mockWork(page, fixtures())
    await page.goto('/p/PHAROS')
    await page.getByRole('button', { name: 'Filter by more' }).click()
    await page.getByRole('menuitem', { name: /Date/ }).click()
    const dialog = page.getByRole('dialog', { name: 'Filter by date' })
    const from = dialog.getByLabel('From', { exact: true }), to = dialog.getByLabel('To', { exact: true })
    const apply = dialog.getByRole('button', { name: 'Apply range' })
    await expectStableControls({
      controls: { fields: dialog.getByRole('radiogroup', { name: 'Which date' }), from, to, apply },
      scrollAreas: { dialog },
      interactions: [
        { name: 'valid start', run: () => from.fill('2026-10-02') },
        { name: 'invalid end', run: async () => { await to.fill('2026-10-01'); await expect(dialog.getByRole('alert')).toHaveText('The end is before the start.'); await expect(apply).toBeDisabled() } },
        { name: 'valid end', run: async () => { await to.fill('2026-10-03'); await expect(dialog.getByRole('alert')).toHaveCount(0); await expect(apply).toBeEnabled() } },
      ],
    })
  })
}
