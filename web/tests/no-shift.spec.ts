// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'

import { expectStableControls } from './helpers/stable'
import { expectDialRowsFitContent } from './helpers/dial-layout'
import type { Shell } from './helpers/no-shift-shells'
import { mockDecisionDesk, sampleQuestion } from './decision-desk-fixtures'

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

}

for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`Decision Desk Agents panel keeps review and history controls still ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    const world = await mockDecisionDesk(page, { theme, long: true })
    world.questions[0]!.input.question = 'Welche Berechtigungen und Nachweise werden vor einer mandantenbezogenen Änderung benötigt?'
    await page.goto('/agents')
    const panel = page.getByRole('region', { name: 'Decision Desk', exact: true })
    const count = panel.getByLabel('Open decisions')
    await expect(count).toHaveText('5')
    await expect(page.getByRole('region', { name: 'Needs you', exact: true })).toHaveCount(0)
    const refresh = () => page.evaluate(async () => {
      const path = '/src/stores/decisionDesk.ts'
      const { useDecisionDesk } = await import(/* @vite-ignore */ path)
      await useDecisionDesk().refresh()
    })
    await expectStableControls({
      controls: { review: page.getByTestId('agents-desk-review'), history: page.getByTestId('agents-desk-history'), count },
      interactions: [
        { name: 'new question', run: async () => { world.questions.push(sampleQuestion('new-source')); await refresh(); await expect(count).toHaveText('6') } },
        { name: 'failed source read', run: async () => { world.failProjection = true; await refresh(); await expect(count).toHaveText('?'); await expect(panel.getByRole('button', { name: 'Retry' })).toBeVisible() } },
        { name: 'retry preserves the source count', run: async () => { world.failProjection = false; await panel.getByRole('button', { name: 'Retry' }).click(); await expect(count).toHaveText('6') } },
      ],
    })
    await page.screenshot({ path: testInfo.outputPath(`agents-desk-${width}-${theme}.png`), fullPage: true })
    await page.getByTestId('agents-desk-history').click()
    await expect(page.getByRole('button', { name: /^Decided / })).toHaveAttribute('aria-pressed', 'true')
  })

  test(`Decision Desk native choices, reason and failure stay still ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    const world = await mockDecisionDesk(page, { theme, long: true }); world.denyWrite = true
    await page.goto('/agents?needs=a:approval-1')
    const submit = page.getByTestId('desk-decide'), reason = page.getByRole('textbox', { name: 'Reason', exact: true })
    await page.getByTestId('choice-0').click()
    await expect(submit).toBeEnabled()
    await expectStableControls({
      controls: { choices: page.getByTestId('desk-choices'), approve: page.getByTestId('choice-0'), deny: page.getByTestId('choice-1'), close: page.getByTestId('desk-close'), submit, pager: page.getByTestId('desk-pager') },
      scrollAreas: { body: page.getByTestId('desk-body') },
      interactions: [
        ...[1, 0].map(index => ({ name: `native option ${index}`, run: async () => { await page.getByTestId(`choice-${index}`).click(); await expect(page.getByTestId(`choice-${index}`)).toHaveAttribute('aria-checked', 'true') } })),
        { name: 'long German reason', run: async () => { await reason.fill('Die vorhandenen Berechtigungen und die Mandantentrennung müssen erhalten bleiben. '.repeat(12)); await reason.press('Enter') } },
        { name: 'failed decision', run: async () => { await submit.click(); await expect(page.getByTestId('desk-status')).toContainText('Approval expired'); await expect(submit).toBeEnabled(); expect(world.approval.decision).toBeNull() } },
      ],
    })
    expect(world.calls.map(call => call.path)).toEqual(['/api/approvals/approval-1/decision'])
    await page.screenshot({ path: testInfo.outputPath(`memo-${width}-${theme}.png`), fullPage: true })
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

test('dial content budget rejects fixed whitespace, clipped controls and overflow', async ({ page }) => {
  await page.setContent('<section id="dial" style="width:900px"><ul class="rows" style="width:500px;padding:0;margin:0;list-style:none"><li data-key="codex" style="display:grid;grid-template-columns:1fr 1fr;align-items:center;min-height:76px;box-sizing:border-box;padding:9px 6px"><span>Codex</span><div style="height:56px"><button class="value-slot">1</button><p style="margin:0">No own limit</p></div></li></ul></section>')
  const dial = page.locator('#dial'), row = dial.locator('li')
  await expectDialRowsFitContent(dial)
  await row.evaluate(el => { (el as HTMLElement).style.height = '180px' })
  await expect(expectDialRowsFitContent(dial)).rejects.toThrow(/row fits its content and approved minimum/)
  await row.evaluate(el => { Object.assign((el as HTMLElement).style, { minHeight: '0', height: '20px' }) })
  await expect(expectDialRowsFitContent(dial)).rejects.toThrow(/content stays inside row (top|bottom)/)
  await row.evaluate(el => { Object.assign((el as HTMLElement).style, { minHeight: '76px', height: 'auto' }); (el.lastElementChild as HTMLElement).style.width = '600px' })
  await expect(expectDialRowsFitContent(dial)).rejects.toThrow(/content stays inside row right/)
})
