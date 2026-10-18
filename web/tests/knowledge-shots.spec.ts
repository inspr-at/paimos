// SPDX-License-Identifier: AGPL-3.0-only
// Knowledge screenshots for the design review (AEON-126): every state at 1920,
// 1440, 1280, 1024 and 390 pixels, light and dark, on mocked data, plus one
// contact sheet per state. Skipped unless KNOWLEDGE_SHOTS names the round:
//   KNOWLEDGE_SHOTS=u21-r1 npx playwright test -c playwright.ui.config.ts tests/knowledge-shots.spec.ts
// Shots land in ../../design-ref/shots/<round>/ (outside the repository).
import { test, expect, type Page } from '@playwright/test'
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fixtures, mockWork } from './work-fixtures'
import { knowledgeWorld, mockKnowledge, type KnowledgeMockOptions } from './knowledge-fixtures'

const round = process.env.KNOWLEDGE_SHOTS ?? ''
const dir = resolve(process.cwd(), '../../design-ref/shots', round || 'none')
const widths = (process.env.SHOT_WIDTHS ?? '1920,1440,1280,1024,390').split(',').map(Number)
const themes = (process.env.SHOT_THEMES ?? 'light,dark').split(',') as ('light' | 'dark')[]
const only = process.env.SHOT_FILTER ?? ''
const DOCK_WIDTHS = (process.env.DOCK_WIDTHS ?? '1920,1440,1200,1199,390').split(',').map(Number)

interface State { name: string; path: string; empty?: boolean; options?: KnowledgeMockOptions; act?: (page: Page, width: number) => Promise<void>; full?: boolean; widths?: number[] }
const entry = '/p/PHAROS/knowledge/runbook/deploy-release'
const states: State[] = [
  { name: '02-list-search', path: '/p/PHAROS/knowledge?q=deploy+probe', act: async page => { await expect(page.locator('.k-row mark').first()).toBeVisible() } },
  { name: '04-list-empty', path: '/p/PHAROS/knowledge', empty: true, act: async page => { await expect(page.getByRole('heading', { name: /No knowledge in Pharos yet/ })).toBeVisible() } },
  { name: '05-list-nomatch', path: '/p/PHAROS/knowledge?q=kubernetes', act: async page => { await expect(page.getByRole('heading', { name: /Nothing matches/ })).toBeVisible() } },
  { name: '07-entry-anchor', path: `${entry}#roll-back`, act: async page => { await expect(page.locator('#h-roll-back')).toBeVisible(); await page.waitForTimeout(300) } },
  { name: '09-external', path: '/p/PHAROS/knowledge/external-system/hetzner', act: async page => { await expect(page.getByText('console.hetzner.cloud')).toBeVisible() } },
  { name: '10-proposed', path: '/p/PHAROS/knowledge/memory/hetzner-token-expiry', act: async page => { await expect(page.locator('.e-note.proposed')).toBeVisible() } },
  { name: '11-edit', path: entry, act: async page => { await expect(page.locator('.e-body')).toBeVisible(); await page.keyboard.press('e'); await expect(page.getByRole('form', { name: /Edit deploy-release/ })).toBeVisible() }, full: true },
  { name: '12-edit-rename', path: entry, act: async page => {
    await expect(page.locator('.e-body')).toBeVisible(); await page.keyboard.press('e')
    await page.locator('#e-slug').fill('ship-a-release'); await expect(page.locator('.e-rename')).toBeVisible()
  } },
  { name: '13-conflict', path: entry, options: { conflictOn: 'k-deploy' }, act: async page => {
    await expect(page.locator('.e-body')).toBeVisible(); await page.keyboard.press('e')
    await page.getByRole('textbox', { name: 'Text, Markdown' }).fill('Every release goes out the same way.\n\n## Roll back\n\nRevert the pin.')
    await page.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(page.locator('.e-conflict')).toBeVisible()
    await page.getByRole('button', { name: 'Compare the text' }).click()
  } },
  { name: '16-cross-search', path: '/knowledge?q=deploy', act: async page => { await expect(page.locator('.kp-row mark').first()).toBeVisible() } },
  { name: '17-palette', path: '/p/PHAROS', act: async page => {
    await expect(page.locator('tr.ticket-row:not(.ghost)').first()).toBeVisible()
    await page.keyboard.press('Control+k'); await page.keyboard.type('deploy')
    await expect(page.getByRole('dialog', { name: 'Search and commands' }).getByRole('group', { name: 'Knowledge' })).toBeVisible()
  } },
  { name: '18-missing', path: '/p/PHAROS/knowledge/runbook/nope', act: async page => { await expect(page.getByRole('heading', { name: /No runbook called/ })).toBeVisible() } },
  { name: '19-renamed', path: '/p/PHAROS/knowledge/runbook/deploy-flow', act: async page => { await expect(page).toHaveURL(/deploy-release/); await expect(page.locator('.e-body')).toBeVisible() } },
  { name: '22-dock-edit', path: `/p/PHAROS/knowledge?entry=guideline/no-edge-accents`, widths: DOCK_WIDTHS, act: async page => { await expect(page.locator('.e-body')).toBeVisible(); await page.keyboard.press('e'); await expect(page.getByRole('form', { name: /Edit no-edge-accents/ })).toBeVisible() } },
  { name: '23-dock-missing', path: `/p/PHAROS/knowledge?entry=runbook/nope`, widths: DOCK_WIDTHS, act: async (page, width) => { if (width >= 1200) await expect(page.getByRole('heading', { name: /No runbook called/ })).toBeVisible() } },
]

test.skip(!round, 'set KNOWLEDGE_SHOTS to the round name to capture')
test.describe.configure({ mode: 'parallel' })

for (const state of states.filter(s => !only || s.name.includes(only))) {
  test(`knowledge shots ${state.name}`, async ({ browser }) => {
    test.setTimeout(240_000)
    mkdirSync(dir, { recursive: true })
    const stateWidths = state.widths ?? widths
    for (const theme of themes) for (const width of stateWidths) {
      const context = await browser.newContext({ viewport: { width, height: width <= 400 ? 844 : 900 }, colorScheme: theme, reducedMotion: 'reduce', deviceScaleFactor: 1 })
      const page = await context.newPage()
      await mockWork(page, fixtures())
      await mockKnowledge(page, knowledgeWorld({ empty: state.empty }), state.options)
      await page.goto(state.path)
      await state.act?.(page, width)
      await page.waitForTimeout(250)
      await page.screenshot({ path: resolve(dir, `${state.name}-${width}-${theme}.png`) })
      if (state.full) await page.evaluate(() => { for (const el of [document.getElementById('main'), document.querySelector('.entry-page.dock .e-scroll')]) if (el) el.scrollTop = el.scrollHeight })
      if (state.full) { await page.waitForTimeout(150); await page.screenshot({ path: resolve(dir, `${state.name}-${width}-${theme}-end.png`) }) }
      await context.close()
    }
    // A contact sheet: every width and theme of this state on one page.
    const cells = themes.flatMap(theme => stateWidths.map(width => `<figure><img src="${state.name}-${width}-${theme}.png"><figcaption>${width} ${theme}</figcaption></figure>`)).join('')
    const html = `<!doctype html><meta charset="utf-8"><style>body{margin:0;padding:16px;background:#888;font:12px system-ui;display:grid;grid-template-columns:repeat(${stateWidths.length},auto);gap:12px;align-items:start}figure{margin:0}img{display:block;height:420px;width:auto;box-shadow:0 2px 8px #0006}figcaption{color:#fff;padding:4px 0}</style>${cells}`
    const sheet = resolve(dir, `sheet-${state.name}.html`)
    writeFileSync(sheet, html)
    const context = await browser.newContext({ viewport: { width: 2400, height: 1000 } })
    const page = await context.newPage()
    await page.goto(`file://${sheet}`)
    await page.waitForTimeout(200)
    await page.screenshot({ path: resolve(dir, `sheet-${state.name}.png`), fullPage: true })
    await context.close()
  })
}
