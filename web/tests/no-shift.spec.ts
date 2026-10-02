// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { capacityWorld } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'

const session = (n: number) => `5e000000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`
async function setup(page: Page, failDecision = false) {
  const work = fixtures()
  work.preferences['agents.working'] = { cap: 9, view: 'model', model: { codex: 1 } }
  await mockWork(page, work, { admin: true })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  const capacity = capacityWorld()
  data.accounts = capacity.accounts as unknown as typeof data.accounts
  await mockAgents(page, data, { capacity, failDecision })
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions.push('account.read', 'account.manage')
    return route.fulfill({ json: answer })
  })
  return data
}

for (const width of [1440, 1024, 390]) {
  test(`ticket relation popover keeps its frame and selector through result changes at ${width}`, async ({ page }) => {
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
      controls: { dialog, search, selector: dialog.getByRole('radiogroup'), blocks: dialog.getByRole('radio', { name: 'Blocked by', exact: true }) },
      scrollAreas: { dialog },
      interactions: [
        { name: 'switch relation', run: () => dialog.getByRole('radio', { name: 'Blocked by', exact: true }).click() },
        { name: 'one result', run: async () => { await search.fill('PHAROS-14'); await expect(dialog.getByRole('option')).toHaveCount(1) } },
        { name: 'no results', run: async () => { await search.fill('no match anywhere'); await expect(dialog.getByText(/Nothing matches/)).toBeVisible() } },
        { name: 'results return', run: async () => { await search.fill('PHAROS-1'); await expect(dialog.getByRole('option').first()).toContainText('PHAROS-1') } },
      ],
    })
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
      controls: { dialog, cancel, stop: dialog.getByRole('button', { name: 'Stop session' }) },
      scrollAreas: { body: dialog.locator('#confirm-body') },
      interactions: [2, 1].map(n => ({ name: `next/previous session ${n}`, run: async () => { await cancel.click(); await open(n); await expect(cancel).toBeFocused() } })),
    })
  })

  test(`Agents working dial and selectors stay put at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await setup(page)
    await page.goto('/agents')
    const dial = page.getByRole('region', { name: 'Agents working at once' })
    const more = dial.getByRole('button', { name: 'More agents at once' })
    const fewer = dial.getByRole('button', { name: 'Fewer agents at once' })
    const row = dial.locator('[data-key="codex"]')
    await expect(row).toBeVisible()
    await expectStableControls({
      controls: { dial, more, fewer, selector: dial.getByRole('tablist'), row, rowMore: row.getByRole('button', { name: /^More agents/ }), rowFewer: row.getByRole('button', { name: /^Fewer agents/ }) },
      scrollAreas: { rows: dial.locator('.rows') },
      interactions: [
        ...[more, more, fewer, fewer].map((button, i) => ({ name: `total step ${i + 1}`, run: () => button.click() })),
        ...['More', 'Fewer'].map(direction => ({ name: `${direction} on Codex`, run: () => row.getByRole('button', { name: new RegExp(`^${direction} agents`) }).click() })),
      ],
    })
    await expectStableControls({
      controls: { dial, more, fewer, selector: dial.getByRole('tablist') },
      scrollAreas: { rows: dial.locator('.rows') },
      interactions: ['By area', 'By model'].map(name => ({ name, run: async () => { await dial.getByRole('tab', { name }).click(); await expect(dial.getByRole('tab', { name })).toHaveAttribute('aria-selected', 'true') } })),
    })
  })
}

test('stability guard allows nested scrolling but detects movement, resizing and overflow', async ({ page }) => {
  await page.setContent('<div id="scroll" style="height:120px;width:200px;overflow:auto"><div style="height:600px"><button id="control" style="margin-top:60px">Choose</button></div></div>')
  const controls = { choose: page.locator('#control') }, scrollAreas = { body: page.locator('#scroll') }
  await expectStableControls({ controls, scrollAreas, interactions: [{ name: 'scroll', run: () => page.locator('#scroll').evaluate(el => { el.scrollTop = 30 }) }] })
  for (const [name, property, value] of [['movement', 'marginLeft', '2px'], ['resize', 'width', '180px']] as const) {
    await expect(expectStableControls({ controls, interactions: [{ name, run: () => page.locator('#control').evaluate((el, change) => { (el as HTMLElement).style[change.property] = change.value }, { property, value }) }] })).rejects.toThrow(/choose\.(x|width)/)
  }
  await expect(expectStableControls({ controls, scrollAreas, interactions: [{ name: 'overflow', run: () => page.locator('#scroll > div').evaluate(el => { (el as HTMLElement).style.width = '300px' }) }] })).rejects.toThrow(/horizontal overflow/)
})
