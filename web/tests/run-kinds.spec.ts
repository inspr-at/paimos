// SPDX-License-Identifier: AGPL-3.0-only
// Capture the actual /agents page/component on CI with deterministic API fixtures.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Locator } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { expectStableControls } from './helpers/stable'

const now = Date.parse('2026-10-01T14:00:00Z')
const before = process.env.AEON_501_CAPTURE === 'before'

async function availableHostBadgeWidth(hostCell: Locator) {
  return hostCell.evaluate(el => {
    const cell = getComputedStyle(el)
    const control = getComputedStyle(el.querySelector('.host-control')!)
    return el.clientWidth - parseFloat(cell.paddingLeft) - parseFloat(cell.paddingRight)
      - parseFloat(control.paddingLeft) - parseFloat(control.paddingRight)
  })
}

function expectHostBadgeFits(width: number, available: number, message: string) {
  expect(width, message).toBeLessThanOrEqual(available + 0.5)
}

async function modelGlyphSize(model: Locator, characters = 1) {
  return model.evaluate((el, characters) => {
    const text = el.firstChild!
    const range = document.createRange()
    range.setStart(text, 0)
    range.setEnd(text, Math.min(characters, text.textContent!.length))
    const glyph = range.getBoundingClientRect(), box = el.getBoundingClientRect()
    const canvas = document.createElement('canvas')
    const context = canvas.getContext('2d')!
    context.font = getComputedStyle(el).font
    const ellipsis = el.scrollWidth > el.clientWidth ? context.measureText('…').width : 0
    return { width: glyph.width, required: glyph.width + ellipsis, available: box.right - glyph.left, slot: el.clientWidth }
  }, characters)
}

function expectModelGlyphFits(glyph: Awaited<ReturnType<typeof modelGlyphSize>>, message: string, soft = false) {
  const check = soft ? expect.soft : expect
  check(glyph.width, 'model fixture has a measurable first character').toBeGreaterThan(0)
  check(glyph.available, message).toBeGreaterThanOrEqual(glyph.required)
}

// These exercise the same guards as the real table below. The legacy checks
// accept each deliberately broken layout, so they cannot prove containment or
// visible model text. No production style is changed by these isolated fixtures.
test('host capacity guard rejects a spill that the old 160px ceiling accepts', async ({ page }) => {
  await page.setContent(`<style>
    .c-host { width: 132px; box-sizing: border-box; padding: 0 8px; }
    .host-control { display: inline-flex; padding-right: 22px; }
    .host-badge { display: inline-block; flex: none; width: 94px; }
  </style><div class="c-host"><span class="host-control"><span class="host-badge">mbp2606</span></span></div>`)
  const cell = page.locator('.c-host')
  const badge = page.locator('.host-badge')
  const available = await availableHostBadgeWidth(cell)
  expect(available).toBe(94)
  expectHostBadgeFits((await badge.boundingBox())!.width, available, 'valid badge fits')
  await badge.evaluate(el => (el as HTMLElement).style.width = '140px')
  const overflow = (await badge.boundingBox())!
  const cellBox = (await cell.boundingBox())!
  expect(overflow.x + overflow.width).toBeGreaterThan(cellBox.x + cellBox.width)
  expect(overflow.width, 'legacy 160px ceiling wrongly accepts the spill').toBeLessThanOrEqual(160)
  expect(() => expectHostBadgeFits(overflow.width, available, 'badge spills past actual capacity'))
    .toThrow(/badge spills past actual capacity/)
})

test('model glyph guard rejects a separator-only slot that the old positive-width check accepts', async ({ page }) => {
  await page.setContent(`<style>
    .exec-model { display: inline-block; width: 200px; font: 12px monospace; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .exec-model::before { content: '·'; margin: 0 .4em; }
  </style><span class="exec-model" title="gpt-fixture">gpt-fixture</span>`)
  const model = page.locator('.exec-model')
  await expect(model).toHaveText('gpt-fixture')
  expectModelGlyphFits(await modelGlyphSize(model), 'valid model character fits')
  await model.evaluate(el => (el as HTMLElement).style.width = '8px')
  const clipped = await modelGlyphSize(model)
  expect(await model.evaluate(el => getComputedStyle(el, '::before').content)).toBe('"·"')
  expect(clipped.slot, 'legacy positive-width check wrongly accepts the separator-only slot').toBeGreaterThan(0)
  expect(clipped.width).toBeGreaterThan(0)
  expect(clipped.available, 'no part of the real first character fits').toBeLessThanOrEqual(0)
  expect(() => expectModelGlyphFits(clipped, 'model character cannot fit after the separator'))
    .toThrow(/model character cannot fit after the separator/)
})

for (const theme of ['light', 'dark'] as const) test(`ended and lost-contact phone actions stay clear of Host in ${theme}`, async ({ page }) => {
  await page.clock.install({ time: now })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.emulateMedia({ colorScheme: theme })
  const work = fixtures()
  work.preferences.theme = { choice: 'system' }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  const ended = { ...data.sessions[6]!, display_label: 'Ended', brief: 'Ship', host: 'mbp2606', harness: 'terminal', command: 'ffmpeg', run_id: null, ticket_node_id: null, ticket: null }
  const silent = { ...data.sessions[4]!, display_label: 'Silent', brief: 'Ship', host: 'mbp2606', harness: 'terminal', command: 'ffmpeg', run_id: null, ticket_node_id: null, ticket: null, heartbeat_at: new Date(now - 20 * 60_000).toISOString() }
  data.sessions.splice(0, data.sessions.length, ended, silent)
  data.runs.splice(0); data.approvals.splice(0); data.messages.splice(0); data.targets.splice(0)
  await mockAgents(page, data)
  await page.route('**/api/me/host-labels', route => route.fulfill({ json: [] }))
  // Keep both records present while proving the Remove button receives clicks.
  // The real error response lets the row's disabled/busy state settle naturally.
  const removed: string[] = []
  await page.route('**/harness-sessions/*/remove', async route => {
    removed.push(new URL(route.request().url()).pathname.split('/').at(-2)!)
    await route.fulfill({ status: 503, json: { error: 'Removal unavailable in layout fixture' } })
  })
  await page.goto('/agents')
  const accounts = page.getByRole('button', { name: 'Accounts and computers', exact: true })
  if (await accounts.getAttribute('aria-expanded') === 'true') await accounts.click()
  await page.getByRole('button', { name: /^Ended/ }).click()
  await expect(page.locator(`[data-row="s:${ended.id}"]`)).toHaveAttribute('data-state', 'stopped')
  await expect(page.locator(`[data-row="s:${silent.id}"]`)).toHaveAttribute('data-state', 'unresponsive')
  const dialog = page.getByRole('dialog', { name: 'Your name for this computer' })
  for (const width of [390, 320, 1024, 1440]) {
    await page.setViewportSize({ width, height: width <= 390 ? 844 : 1000 })
    for (const session of [ended, silent]) {
      const row = page.locator(`[data-row="s:${session.id}"]`)
      const host = row.locator('.host-badge'), pencil = row.locator('.host-pencil')
      const remove = row.getByRole('button', { name: `Remove ${session.display_label}`, exact: true })
      const menu = row.getByRole('button', { name: `Actions for ${session.display_label}`, exact: true })
      await row.scrollIntoViewIfNeeded()
      await expect(row.locator('.result')).toHaveText('Ship')
      await expect(remove).toBeVisible()
      await expect(menu).toBeVisible()
      for (const action of [remove, menu]) {
        const actionBox = (await action.boundingBox())!
        expect(actionBox.width).toBeGreaterThanOrEqual(width <= 390 ? 44 : 1)
        expect(actionBox.height).toBeGreaterThanOrEqual(width <= 390 ? 44 : 1)
        for (const control of [host, pencil]) {
          const controlBox = (await control.boundingBox())!
          const overlapWidth = Math.min(actionBox.x + actionBox.width, controlBox.x + controlBox.width) - Math.max(actionBox.x, controlBox.x)
          const overlapHeight = Math.min(actionBox.y + actionBox.height, controlBox.y + controlBox.height) - Math.max(actionBox.y, controlBox.y)
          expect(Math.min(overlapWidth, overlapHeight), `${width}px ${session.display_label}: ${await action.getAttribute('aria-label')} stays clear of Host and rename`).toBeLessThanOrEqual(0)
        }
      }
      expectModelGlyphFits(await modelGlyphSize(row.locator('.exec-model'), 6), `${width}px ${session.display_label}: complete ffmpeg remains visible`)
      if (width <= 390) {
        const stateBox = (await row.locator('.agent-state-label').boundingBox())!
        const ticketBox = (await row.locator('.c-ticket').boundingBox())!
        expect(stateBox.x + stateBox.width, `${width}px ${session.display_label}: state stays clear of project`).toBeLessThanOrEqual(ticketBox.x + .5)
      }
      if (width <= 390) await expectStableControls({ controls: { row, host, pencil, remove, menu }, scrollAreas: { row }, interactions: [
        { name: 'open Host from badge', run: async () => {
          await host.click()
          await expect(dialog.getByRole('textbox')).toBeEnabled()
        } },
        { name: 'cancel Host draft', run: async () => {
          await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
          await expect(dialog).toHaveCount(0)
        } },
        { name: 'open Host from rename control', run: async () => {
          await pencil.click()
          await expect(dialog.getByRole('textbox')).toBeEnabled()
        } },
        { name: 'cancel rename', run: async () => {
          await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
          await expect(dialog).toHaveCount(0)
        } },
        { name: 'open row Actions', run: async () => {
          await menu.click()
          await expect(menu).toHaveAttribute('aria-expanded', 'true')
          await expect(page.getByRole('menu', { name: `Actions for ${session.display_label}` })).toBeVisible()
        } },
        { name: 'close row Actions', run: async () => {
          await page.keyboard.press('Escape')
          await expect(menu).toHaveAttribute('aria-expanded', 'false')
        } },
        { name: 'click Remove and report its failure', run: async () => {
          const count = removed.length
          await remove.click()
          await expect.poll(() => removed.length).toBe(count + 1)
          expect(removed.at(-1)).toBe(session.id)
          await expect(remove).toBeEnabled()
          await expect(page.getByText('Removal unavailable in layout fixture', { exact: true }).last()).toBeVisible()
        } },
      ] })
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
    const shots = process.env.AEON_670_FIX2_SHOTS
    if (shots) {
      mkdirSync(shots, { recursive: true })
      await page.locator('.sessions').screenshot({ path: join(shots, `paired-actions-${width}-${theme}.png`), animations: 'disabled' })
    }
  }
})

test('execution kinds and person-specific host names on the real agents table', async ({ page }, info) => {
  await page.clock.install({ time: now })
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.emulateMedia({ colorScheme: 'light' })
  const work = fixtures()
  work.preferences.theme = { choice: 'system' }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: { 'n-1': { key: 'PHAROS-42', title: 'Ship the execution kinds' } } })
  const lead = data.sessions[0]!
  Object.assign(lead, { display_label: 'Release coordinator', model: 'claude-fixture', role: 'coordinator', harness: 'claude', run_id: null, host: 'mbp2607', progress_pct: 50, eta_live_at: new Date(now + 20 * 60_000).toISOString(), heartbeat_at: new Date(now - 5_000).toISOString() })
  const ai = { ...data.sessions[1]!, run_id: null, ticket_node_id: lead.ticket_node_id, ticket: lead.ticket, parent_harness_session_id: lead.id, display_label: 'AI worker', harness: 'codex', model: 'gpt-fixture', reasoning_effort: 'high', host: 'mbp2606', phase: 'working', activity: 'busy', progress_pct: 20, eta_ready_at: new Date(now + 10 * 60_000).toISOString() }
  const xai = { ...ai, id: '52000000-0000-4000-8000-000000000001', display_label: 'xAI worker', harness: 'grok', model: 'grok-fixture' }
  const media = { ...ai, id: '52000000-0000-4000-8000-000000000002', display_label: 'Media worker', harness: 'media', model: null, reasoning_effort: null, generator: 'higgsfield/kling3_0', progress_pct: 40 }
  const terminal = { ...ai, id: '52000000-0000-4000-8000-000000000003', display_label: 'Terminal worker', harness: 'terminal', model: null, reasoning_effort: null, command: 'ffmpeg', progress_pct: 90 }
  data.sessions.splice(0, data.sessions.length, lead, ai, xai, media, terminal)
  data.runs.splice(0); data.approvals.splice(0); data.messages.splice(0); data.targets.splice(0)
  await mockAgents(page, data)
  const names = new Map<string, string>()
  await page.route('**/api/me/host-labels', async route => {
    if (route.request().method() === 'PUT') {
      const { host, label } = route.request().postDataJSON()
      if (label === null) names.delete(host); else names.set(host, label)
      await route.fulfill({ json: { host, label: label ?? host } })
    } else await route.fulfill({ json: [...names].map(([host, label]) => ({ host, label })) })
  })
  await page.goto('/agents')
  const accounts = page.getByRole('button', { name: 'Accounts and computers', exact: true })
  if (await accounts.getAttribute('aria-expanded') === 'true') await accounts.click()
  await expect(page.locator(`[data-row="s:${ai.id}"]`)).toBeVisible()
  const table = page.getByRole('treegrid', { name: 'Agent sessions' })
  if (!before) {
    await expect(table.getByRole('columnheader').filter({ hasText: /^Name/ })).toBeVisible()
    await expect(table.getByRole('columnheader').filter({ hasText: /^Host/ })).toBeVisible()
    await expect(page.locator(`[data-row="s:${media.id}"] .c-exec`)).toContainText('higgsfield/kling3_0')
    await expect(page.locator(`[data-row="s:${terminal.id}"] .c-exec`)).toContainText('ffmpeg')
    await expect(page.locator('[data-run-kind="media"]')).toHaveCount(1)
    await expect(page.locator('[data-run-kind="terminal"]')).toHaveCount(1)
    const slots = await page.locator('.exec-icon').evaluateAll(items => items.map(item => {
      const rect = item.getBoundingClientRect()
      return { width: rect.width, height: rect.height, border: getComputedStyle(item).borderWidth }
    }))
    for (const slot of slots) { expect(slot.width).toBe(slot.height); expect(slot.border).toBe('0px') }
    const labelLefts = await page.locator('.exec-copy').evaluateAll(items => items.map(item => item.getBoundingClientRect().left))
    expect(new Set(labelLefts).size).toBe(1)
    const workerHost = page.locator(`[data-row="s:${ai.id}"] .host-badge`)
    const hostCell = page.locator(`[data-row="s:${ai.id}"] .c-host`)
    // The working-plan card can put this row below the viewport. Complete
    // Playwright's action scroll before measuring hover-induced movement.
    await workerHost.scrollIntoViewIfNeeded()
    const original = await workerHost.boundingBox()
    const availableBadgeWidth = await availableHostBadgeWidth(hostCell)
    expect(availableBadgeWidth, 'host cell leaves room for a badge beside the pencil').toBeGreaterThan(0)
    const pencil = page.locator(`[data-row="s:${ai.id}"] .host-pencil`)
    const row = page.locator(`[data-row="s:${ai.id}"]`)
    const dialog = page.getByRole('dialog', { name: 'Your name for this computer' })
    await expectStableControls({ controls: { host: workerHost, pencil, row }, interactions: [
      { name: 'hover host', run: async () => {
        await workerHost.hover()
        await expect(pencil).toHaveCSS('opacity', '1')
        expect((await pencil.boundingBox())!.x).toBeGreaterThanOrEqual(original!.x + original!.width)
        const pencilBox = await pencil.boundingBox()
        const cellBox = await hostCell.boundingBox()
        expect(pencilBox!.x + pencilBox!.width).toBeLessThanOrEqual(cellBox!.x + cellBox!.width + 0.5)
      } },
      { name: 'open and type a proposed label', run: async () => {
        await workerHost.click()
        await expect(dialog.getByRole('textbox')).toBeEnabled()
        await dialog.getByRole('textbox').fill('Cancelled name')
      } },
      { name: 'cancel proposed label', run: async () => {
        await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
        await expect(dialog).toHaveCount(0)
      } },
    ] })
    await expect(workerHost).toContainText('mbp2606')
    expect(names.size).toBe(0)
    await workerHost.click()
    await expect(dialog.getByRole('textbox')).toBeEnabled()
    await dialog.getByRole('textbox').fill("David's MacBook")
    expect(await workerHost.boundingBox()).toEqual(original)
    await dialog.getByRole('button', { name: 'Save', exact: true }).click()
    for (const child of [ai, xai, media, terminal]) await expect(page.locator(`[data-row="s:${child.id}"] .host-badge`)).toContainText("David's MacBook")
    await expect(page.locator(`[data-row="s:${lead.id}"] .host-badge`)).toContainText('mbp2607')
    const renamed = await workerHost.boundingBox()
    expect(renamed!.width).toBeGreaterThan(original!.width)
    expectHostBadgeFits(renamed!.width, availableBadgeWidth, 'renamed badge fits the cell beside the pencil')
    expect(await workerHost.evaluate(el => getComputedStyle(el).width)).not.toBe('104px')
    await workerHost.click()
    await expect(dialog.getByRole('button', { name: 'Use registered name', exact: true })).toBeEnabled()
    await dialog.getByRole('button', { name: 'Use registered name', exact: true }).click()
    await expect(workerHost).toContainText('mbp2606')
    expect((await workerHost.boundingBox())?.width).toBe(original?.width)
    // Long labels use the available width up to the cap, visibly ellipsised.
    const longLabel = 'Gemeinsame Entwicklungsstation für teamübergreifende Qualitätssicherung und Agenturkoordination'
    await workerHost.click()
    await expect(dialog.getByRole('textbox')).toBeEnabled()
    await dialog.getByRole('textbox').fill(longLabel)
    await dialog.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(dialog).toHaveCount(0)
    await expect(workerHost.locator('.host-name')).toHaveText(longLabel)
    await expect(workerHost).toHaveAttribute('title', `${longLabel} · mbp2606`)
    const longBadge = await workerHost.boundingBox()
    expectHostBadgeFits(longBadge!.width, availableBadgeWidth, 'long badge fits the cell beside the pencil')
    expect(longBadge!.width).toBeGreaterThan(original!.width)
    const longHostCell = await hostCell.boundingBox()
    expect(longBadge!.x + longBadge!.width).toBeLessThanOrEqual(longHostCell!.x + longHostCell!.width + 0.5)
    expect(await workerHost.locator('.host-name').evaluate(el => el.scrollWidth > el.clientWidth && getComputedStyle(el).textOverflow === 'ellipsis')).toBe(true)

    const shots = process.env.AEON_657_SHOTS
    if (shots) mkdirSync(shots, { recursive: true })
    for (const theme of ['light', 'dark'] as const) for (const width of [390, 768, 1024, 1440]) {
      await test.step(`${width}px ${theme}: host and execution layout`, async () => {
        await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
        await page.emulateMedia({ colorScheme: theme })
        await page.mouse.move(0, 0)
        await workerHost.scrollIntoViewIfNeeded()
        await expect(workerHost).toBeVisible()
        const responsiveBadge = await workerHost.boundingBox()
        const responsiveCell = await page.locator(`[data-row="s:${ai.id}"] .c-host`).boundingBox()
        expect.soft(responsiveBadge!.x + responsiveBadge!.width).toBeLessThanOrEqual(responsiveCell!.x + responsiveCell!.width + 0.5)
        const responsivePencil = await page.locator(`[data-row="s:${ai.id}"] .host-pencil`).boundingBox()
        expect.soft(responsivePencil!.x + responsivePencil!.width).toBeLessThanOrEqual(responsiveCell!.x + responsiveCell!.width + 0.5)
        const leadName = page.locator(`[data-row="s:${lead.id}"] .host-name`)
        await expect(leadName).toHaveText('mbp2607')
        const leadNameSize = await leadName.evaluate(el => ({ scroll: el.scrollWidth, client: el.clientWidth }))
        expect.soft(leadNameSize.scroll, `${width}px: short host name stays fully visible`).toBeLessThanOrEqual(leadNameSize.client)
        if (width <= 768) {
          expect.soft(responsiveBadge!.width, 'compact cap follows the sessions container').toBeLessThanOrEqual(104)
          for (const child of [ai, xai, media, terminal]) {
            const row = page.locator(`[data-row="s:${child.id}"]`)
            const copy = row.locator('.exec-copy')
            const copySize = await copy.evaluate(el => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
            expect.soft(copySize.scrollWidth, `${child.display_label}: execution copy does not overflow`).toBeLessThanOrEqual(copySize.clientWidth)
            const harnessBox = await row.locator('.exec-harness').boundingBox()
            const badgeBox = await row.locator('.host-badge').boundingBox()
            expect.soft(harnessBox!.x + harnessBox!.width, `${child.display_label}: execution stays before host`).toBeLessThanOrEqual(badgeBox!.x + 0.5)
          }
        }
        // Real execution text must fit beside Host even on phones: the
        // generated separator alone must never satisfy this guard.
        for (const child of [ai, xai, media, terminal]) {
          const model = page.locator(`[data-row="s:${child.id}"] .exec-model`)
          await expect(model).toContainText(child.model || ('generator' in child ? child.generator : 'command' in child ? child.command : ''))
          const characters = child === terminal ? terminal.command.length : width === 390 ? 3 : 1
          const glyph = await modelGlyphSize(model, characters)
          expectModelGlyphFits(glyph, `${width}px ${child.display_label}: ${characters} execution characters fit after the separator and before any ellipsis`, true)
        }
        if (width === 390) {
          // Risk: platform font metrics squeeze a short command beside Host.
          // Reserve a line wide enough for the command and half the harness;
          // the command must remain whole when the harness needs truncation.
          const terminalCopy = page.locator(`[data-row="s:${terminal.id}"] .exec-copy`)
          await terminalCopy.evaluate(el => {
            const model = el.querySelector('.exec-model')!, harness = el.querySelector('.exec-harness')!
            const width = model.getBoundingClientRect().width + harness.getBoundingClientRect().width / 2
            ;(el as HTMLElement).style.maxWidth = `${width}px`
          })
          const model = terminalCopy.locator('.exec-model')
          expectModelGlyphFits(await modelGlyphSize(model, terminal.command.length), 'the complete terminal command survives narrower font-dependent capacity')
          await terminalCopy.evaluate(el => { (el as HTMLElement).style.removeProperty('max-width') })
        }
        if (width === 390) {
          await expectStableControls({ controls: { host: workerHost, pencil, row, menu: row.locator('.more') }, scrollAreas: { row }, interactions: [
            { name: 'hover compact host', run: async () => {
              await workerHost.hover()
              await expect(pencil).toHaveCSS('opacity', '1')
            } },
            { name: 'edit compact host draft', run: async () => {
              await pencil.click()
              await expect(dialog.getByRole('textbox')).toBeEnabled()
              await dialog.getByRole('textbox').fill('Draft only')
              await expect(workerHost.locator('.host-name')).toHaveText(longLabel)
            } },
            { name: 'cancel compact host draft', run: async () => {
              await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
              await expect(dialog).toHaveCount(0)
              await expect(workerHost.locator('.host-name')).toHaveText(longLabel)
            } },
          ] })
          await workerHost.click()
          await expect(dialog.getByRole('textbox')).toBeEnabled()
          await dialog.getByRole('textbox').fill('mba')
          await dialog.getByRole('button', { name: 'Save', exact: true }).click()
          await expect(dialog).toHaveCount(0)
          await expect(workerHost.locator('.host-name')).toHaveText('mba')
          expect((await workerHost.boundingBox())!.width).toBeLessThan(responsiveBadge!.width)
          const shortHostModel = await modelGlyphSize(row.locator('.exec-model'), 3)
          expectModelGlyphFits(shortHostModel, 'short host leaves real model text visible')
          await workerHost.click()
          await expect(dialog.getByRole('textbox')).toBeEnabled()
          await dialog.getByRole('textbox').fill(longLabel)
          await dialog.getByRole('button', { name: 'Save', exact: true }).click()
          await expect(dialog).toHaveCount(0)
          await expect(workerHost.locator('.host-name')).toHaveText(longLabel)
        }
        expect.soft(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1)
        await page.locator('.sessions').screenshot({ path: info.outputPath(`terminal-command-${width}-${theme}.png`), animations: 'disabled' })
        if (shots) await page.locator('.sessions').screenshot({ path: join(shots, `${width}-${theme}.png`), animations: 'disabled' })
      })
    }
    // Responsive checks must not alter the viewport/theme of the base comparison.
    await page.setViewportSize({ width: 1600, height: 1000 })
    await page.emulateMedia({ colorScheme: 'light' })

    await workerHost.click()
    await expect(dialog.getByRole('button', { name: 'Use registered name', exact: true })).toBeEnabled()
    await dialog.getByRole('button', { name: 'Use registered name', exact: true }).click()
    await expect(dialog).toHaveCount(0)
    await expect(workerHost.locator('.host-name')).toHaveText('mbp2606')
  }
  expect(page.viewportSize(), 'base and candidate captures use the same viewport').toEqual({ width: 1600, height: 1000 })
  expect(await page.evaluate(() => matchMedia('(prefers-color-scheme: light)').matches), 'base and candidate captures use the light theme').toBe(true)
  // Focus without scrolling, then align the page's scrolling body in both captures.
  await page.locator(`[data-row="s:${ai.id}"]`).evaluate(el => (el as HTMLElement).focus({ preventScroll: true }))
  await page.mouse.move(0, 0)
  await page.locator('main').evaluate(el => el.scrollTo({ top: el.scrollHeight, behavior: 'instant' }))
  const root = process.env.AEON_501_SHOTS
  if (root) {
    mkdirSync(root, { recursive: true })
    const layout = await page.locator('[data-row^="s:"]').evaluateAll(items => items.map(item => {
      const row = item.getBoundingClientRect()
      const state = item.querySelector('.c-state')!.getBoundingClientRect()
      return { id: item.getAttribute('data-row'), height: row.height, top: row.top, stateLeft: state.left }
    }))
    const path = join(root, 'before-layout.json')
    if (before) writeFileSync(path, JSON.stringify(layout))
    else {
      const baseline = JSON.parse(readFileSync(path, 'utf8')) as typeof layout
      for (const row of layout) {
        const original = baseline.find(item => item.id === row.id)!
        expect(row.height, `${row.id} retains the base row height`).toBeCloseTo(original.height, 1)
        expect(row.top, `${row.id} retains the base row position in the capture`).toBeCloseTo(original.top, 1)
        expect(row.stateLeft, `${row.id} retains the base state position`).toBe(original.stateLeft)
      }
    }
  }
  await page.screenshot({ path: root ? join(root, `${before ? 'before' : 'after'}.png`) : info.outputPath(`${before ? 'before' : 'after'}.png`), fullPage: true, animations: 'disabled' })
  await page.locator('.sessions').screenshot({ path: root ? join(root, `${before ? 'before' : 'after'}-sessions.png`) : info.outputPath(`${before ? 'before' : 'after'}-sessions.png`), animations: 'disabled' })
})
