// SPDX-License-Identifier: AGPL-3.0-only
// AEON-252: turn a layer, a set, or one rule on or off; duplicate a rule or a set;
// say so when a template reset has no stored original. Drafts only — never a publish.
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockRules } from './rules-fixtures'

const SHOTS = process.env.RULES_SHOTS ?? ''

async function setup(page: Page, options: Parameters<typeof mockRules>[1] = {}) {
  await mockWork(page, fixtures())
  await mockSettings(page, settingsData())
  const rules = await mockRules(page, options)
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('heading', { name: 'Agent rules' })).toBeVisible()
  await expect(page.getByRole('status', { name: 'Loading rules' })).toHaveCount(0)
  return rules
}
async function openSet(page: Page, name: string) {
  await page.getByRole('button', { name: new RegExp(`^${name}(?! \\(copy\\))`) }).click()
}
async function editSet(page: Page, name: string) {
  await page.getByRole('button', { name: `Actions for ${name}` }).click()
  await page.getByRole('menuitem', { name: 'Edit rules' }).click()
  await expect(page.getByRole('textbox', { name: 'Set name' })).toHaveValue(name)
}
const overflows = (page: Page) => page.evaluate(() =>
  document.documentElement.scrollWidth > document.documentElement.clientWidth + 1
  || document.body.scrollWidth > document.body.clientWidth + 1)
const draftRules = (body: unknown) => (body as { rules: { identity: string; text: string; strength: string; enabled: boolean; source: { identity?: string; edited_here?: boolean } }[] }).rules

test('an edited upstream rule says the original wording was not stored', async ({ page }) => {
  const rules = await setup(page)
  await editSet(page, 'Secrets')
  const row = page.getByRole('listitem', { name: 'Never print the environment.' })
  await expect(row.getByRole('button', { name: 'Reset to template' })).toHaveCount(0)
  await row.getByRole('textbox', { name: 'Rule text' }).fill('Never print secrets aloud.')
  const editedRow = page.getByRole('listitem', { name: 'Never print secrets aloud.' })
  await expect(editedRow.getByText('Reset needs the original template wording, which this rule does not store.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Reset to template' })).toHaveCount(0)
  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect(page.getByText('Draft saved')).toBeVisible()
  const put = rules.calls.find(call => call.method === 'PUT')
  const edited = draftRules(put?.body).find(rule => rule.identity === 'keep-secrets')
  expect(edited?.text).toBe('Never print secrets aloud.')
  expect(edited?.source.edited_here).toBe(true)
  expect(edited?.source.identity).toBe('keep-secrets')
  expect(rules.calls.some(call => call.path.includes('publish'))).toBe(false)
  await openSet(page, 'Secrets')
  await expect(page.getByText('Edited here', { exact: true })).toBeVisible()
})

test('someone who cannot write the layer sees no ticks', async ({ page }) => {
  await setup(page, { publish: false })
  await expect(page.getByRole('checkbox', { name: /Turn / })).toHaveCount(0)
  await openSet(page, 'Secrets')
  await openSet(page, 'Scope')
  await expect(page.getByRole('checkbox', { name: 'Record the source. in Secrets is on' })).toHaveCount(0)
  await expect(page.getByRole('checkbox', { name: 'Your package is your scope. in Scope is on' })).toHaveCount(0)
  await page.getByRole('button', { name: 'Actions for Secrets' }).click()
  await expect(page.getByRole('menuitem', { name: /Duplicate set/ })).toHaveAttribute('aria-disabled', 'true')
})

test('an agent can draft a project rule and cannot touch company rules', async ({ page }) => {
  const rules = await setup(page, { kind: 'agent' })
  await expect(page.getByRole('checkbox', { name: 'Turn Company rules on or off' })).toHaveCount(0)
  await expect(page.getByRole('checkbox', { name: 'Turn Secrets rules on or off' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /Review and publish/ })).toHaveCount(0)
  await openSet(page, 'Scope')
  await page.getByRole('checkbox', { name: 'Your package is your scope. in Scope is on' }).uncheck()
  await expect(page.getByText('Draft saved')).toBeVisible()
  expect(rules.calls.some(call => call.path.includes('publish'))).toBe(false)
  expect(rules.calls.some(call => call.method === 'PUT' && call.path.includes('ffffffff-ffff-4fff-8fff-ffffffffffff'))).toBe(true)
})

async function growToContent(page: Page, base: number) {
  let height = base
  for (let pass = 0; pass < 4; pass++) {
    const hidden = await page.evaluate(() => {
      let most = 0
      for (const el of [document.documentElement, ...document.querySelectorAll('body *')]) {
        if (el !== document.documentElement && !/(auto|scroll)/.test(getComputedStyle(el).overflowY)) continue
        if (el.clientHeight === 0) continue
        el.scrollTop = 0
        most = Math.max(most, el.scrollHeight - el.clientHeight)
      }
      return most
    })
    if (hidden < 4 || height >= 6000) break
    height = Math.min(6000, height + hidden)
    await page.setViewportSize({ width: page.viewportSize()!.width, height })
  }
}

test('the ticked list and the reset note hold at 390 and 1600', async ({ page }) => {
  if (SHOTS) mkdirSync(SHOTS, { recursive: true })
  await setup(page)
  await openSet(page, 'Secrets')
  await page.getByRole('checkbox', { name: 'Record the source. in Secrets is on' }).uncheck()
  await expect(page.getByText('Changed', { exact: true })).toBeVisible()
  await expect(page.getByRole('checkbox', { name: 'Turn Secrets rules on or off' })).toHaveAttribute('aria-checked', 'mixed')
  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    for (const width of [1600, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await expect(page.getByRole('heading', { name: 'Agent rules' })).toBeVisible()
      expect(await overflows(page), `read ${theme} ${width}`).toBe(false)
      await growToContent(page, width === 390 ? 844 : 1000)
      expect(await overflows(page), `read grown ${theme} ${width}`).toBe(false)
      const aligned = await page.evaluate(() => {
        const left = [...document.querySelectorAll('.rule .rule-text')].map(el => Math.round(el.getBoundingClientRect().left))
        return left.length ? Math.max(...left) - Math.min(...left) : 0
      })
      expect(aligned, `rule text lines up ${theme} ${width}`).toBeLessThan(2)
      if (theme === 'dark') {
        const paint = await page.evaluate(() => {
          const read = (name: string) => {
            const el = [...document.querySelectorAll('input')].find(input => input.getAttribute('aria-label') === name) as HTMLInputElement | undefined
            if (!el) return null
            return { bg: getComputedStyle(el).backgroundImage, knob: getComputedStyle(el, '::after').transform }
          }
          return { mixed: read('Turn Secrets rules on or off'), on: read('Turn Project rules on or off') }
        })
        expect(paint.mixed?.bg, `mixed track ${width} ${paint.mixed?.bg}`).toContain('50%')
        expect(paint.mixed?.bg, `mixed track ${width}`).not.toBe(paint.on?.bg)
        expect(paint.mixed?.knob, `mixed knob ${width} ${paint.mixed?.knob}`).toContain('7')
        expect(paint.on?.knob, `on knob ${width}`).toContain('14')
      }
      if (SHOTS) await page.screenshot({ path: `${SHOTS}/read-mixed-${width}-${theme}.png`, fullPage: true })
    }
  }
  await editSet(page, 'Secrets')
  await page.getByRole('listitem', { name: 'Never print the environment.' }).getByRole('textbox', { name: 'Rule text' }).fill('Never print secrets aloud.')
  await expect(page.getByText('Reset needs the original template wording, which this rule does not store.')).toBeVisible()
  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
    for (const width of [1600, 390]) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await page.locator('.set.editing').scrollIntoViewIfNeeded()
      expect(await overflows(page), `edit ${theme} ${width}`).toBe(false)
      await growToContent(page, width === 390 ? 844 : 1200)
      expect(await overflows(page), `edit grown ${theme} ${width}`).toBe(false)
      if (SHOTS) await page.screenshot({ path: `${SHOTS}/edit-reset-${width}-${theme}.png`, fullPage: true })
    }
  }
})
