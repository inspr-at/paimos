// SPDX-License-Identifier: AGPL-3.0-only
// AEON-252: turn a layer, a set, or one rule on or off; duplicate a rule or a set;
// say so when a template reset has no stored original. Drafts only — never a publish.
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockRules } from './rules-fixtures'

const SHOTS = '/private/tmp/claude-501/-Users-markus-Code-aeon/a4527da9-f872-45f5-a2f2-48dde0ce2ce5/scratchpad/shots/aeon-252'

async function setup(page: Page, options: Parameters<typeof mockRules>[1] = {}) {
  await mockWork(page, fixtures())
  await mockSettings(page, settingsData())
  const rules = await mockRules(page, options)
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('heading', { name: 'Agent rules' })).toBeVisible()
  await expect(page.getByRole('status', { name: 'Loading rules' })).toHaveCount(0)
  return rules
}
const rulesCalls = (calls: { method: string; path: string; body?: unknown }[]) => calls.filter(call => call.path.startsWith('/api/rules'))
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

test('a rule tick saves a draft and does not publish', async ({ page }) => {
  const rules = await setup(page)
  await openSet(page, 'Secrets')
  await page.getByRole('checkbox', { name: 'Record the source. is on' }).uncheck()
  await expect(page.getByText('Draft saved')).toBeVisible()
  await expect(page.getByText('Changed', { exact: true })).toBeVisible()
  await expect(page.getByText('Off', { exact: true })).toBeVisible()
  const put = rules.calls.find(call => call.method === 'PUT' && call.path.endsWith('/draft'))
  const record = draftRules(put?.body).find(rule => rule.identity === 'record-source')
  const locked = draftRules(put?.body).find(rule => rule.identity === 'keep-secrets')
  expect(record?.enabled).toBe(false)
  expect(record?.source.edited_here).toBe(false)
  expect(locked?.enabled).toBe(true)
  expect(locked?.strength).toBe('locked')
  expect(rulesCalls(rules.calls).some(call => call.path.includes('publish'))).toBe(false)
})

test('a set tick turns movable rules off and leaves locked rules on', async ({ page }) => {
  const rules = await setup(page)
  await openSet(page, 'Secrets')
  const tick = page.getByRole('checkbox', { name: 'Turn Secrets rules on or off' })
  await expect(tick).toBeChecked()
  await tick.click()
  await expect(page.getByText('Draft saved')).toBeVisible()
  await expect(tick).toHaveAttribute('aria-checked', 'mixed')
  await expect(page.getByRole('checkbox', { name: 'Record the source. is on' })).not.toBeChecked()
  await expect(page.getByRole('img', { name: 'Locked' })).toBeVisible()
  const put = rules.calls.find(call => call.method === 'PUT' && call.path.endsWith('/draft'))
  expect(draftRules(put?.body).find(rule => rule.identity === 'record-source')?.enabled).toBe(false)
  expect(draftRules(put?.body).find(rule => rule.identity === 'keep-secrets')?.enabled).toBe(true)
  expect(rules.calls.some(call => call.path.includes('publish'))).toBe(false)
})

test('a layer tick saves the writable sets underneath it', async ({ page }) => {
  const rules = await setup(page)
  await page.getByRole('checkbox', { name: 'Turn Project rules on or off' }).click()
  await expect(page.getByText('Draft saved')).toBeVisible()
  await openSet(page, 'Scope')
  await expect(page.getByRole('checkbox', { name: 'Your package is your scope. is on' })).not.toBeChecked()
  await expect(page.getByRole('img', { name: 'Locked in company rules' })).toBeVisible()
  await expect(page.getByRole('checkbox', { name: 'Turn Scope rules on or off' })).toHaveAttribute('aria-checked', 'mixed')
  const put = rules.calls.find(call => call.method === 'PUT' && call.path.includes('ffffffff-ffff-4fff-8fff-ffffffffffff'))
  expect(draftRules(put?.body).find(rule => rule.identity === 'package-scope')?.enabled).toBe(false)
  expect(draftRules(put?.body).find(rule => rule.identity === 'keep-secrets')?.enabled).toBe(true)
  expect(rules.calls.some(call => call.path.includes('publish'))).toBe(false)
})

test('a layer tick of two sets says how many drafts were saved, and stops when one fails', async ({ page }) => {
  const rules = await setup(page, { draftFailAt: 2 })
  await page.getByRole('button', { name: 'Actions for Secrets' }).click()
  await page.getByRole('menuitem', { name: 'Duplicate set' }).click()
  await expect(page.getByText('Duplicated as Secrets (copy). Nothing is live until you publish.')).toBeVisible()
  await page.getByRole('checkbox', { name: 'Turn Company rules on or off' }).click()
  await expect(page.getByText('The draft was not saved.')).toBeVisible()
  await expect(page.getByText(/Saved \d+ drafts/)).toHaveCount(0)
  const puts = rules.calls.filter(call => call.method === 'PUT' && call.path.endsWith('/draft'))
  expect(puts).toHaveLength(3)
  const secrets = puts.find(call => call.path.includes('eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'))
  expect(draftRules(secrets?.body).find(rule => rule.identity === 'record-source')?.enabled).toBe(false)
  expect(rules.calls.some(call => call.path.includes('publish'))).toBe(false)
})

test('editing one set blocks ticks on the others until it is saved or cancelled', async ({ page }) => {
  const rules = await setup(page)
  await editSet(page, 'Secrets')
  await page.getByRole('textbox', { name: 'Set name' }).fill('Secrets plus')
  await expect(page.getByRole('checkbox', { name: 'Turn Project rules on or off' })).toBeDisabled()
  await expect(page.getByRole('checkbox', { name: 'Turn Scope rules on or off' })).toBeDisabled()
  const own = page.getByRole('checkbox', { name: 'Turn Secrets rules on or off' })
  await expect(own).toBeEnabled()
  await own.click()
  await expect(page.getByRole('checkbox', { name: 'Record the source. is on' })).not.toBeChecked()
  expect(rules.calls.some(call => call.method === 'PUT')).toBe(false)
  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect(page.getByText('Draft saved')).toBeVisible()
  const put = rules.calls.find(call => call.method === 'PUT')
  expect((put?.body as { name: string }).name).toBe('Secrets plus')
  expect(draftRules(put?.body).find(rule => rule.identity === 'record-source')?.enabled).toBe(false)
})

test('duplicating a rule stays in the draft until save', async ({ page }) => {
  const rules = await setup(page)
  await editSet(page, 'Secrets')
  const row = page.getByRole('listitem', { name: 'Never print the environment.' })
  await row.locator('summary').click()
  await row.getByRole('button', { name: 'Duplicate rule' }).click()
  const copy = page.getByRole('listitem', { name: 'Never print the environment. (copy)' })
  await expect(copy).toBeVisible()
  await expect(copy.getByRole('checkbox', { name: 'Never print the environment. (copy) is on' })).toBeEnabled()
  expect(rules.calls.some(call => call.method === 'PUT')).toBe(false)
  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect(page.getByText('Draft saved')).toBeVisible()
  const put = rules.calls.find(call => call.method === 'PUT')
  const copied = draftRules(put?.body).find(rule => rule.text === 'Never print the environment. (copy)')
  expect(copied?.strength).toBe('normal')
  expect(copied?.identity).not.toBe('keep-secrets')
  expect(copied?.source.identity).toBeUndefined()
  expect(copied?.source.edited_here).toBe(false)
  expect(rules.calls.some(call => call.path.includes('publish'))).toBe(false)
})

test('duplicating a set writes a new draft and leaves the original live', async ({ page }) => {
  const rules = await setup(page)
  await page.getByRole('button', { name: 'Actions for Secrets' }).click()
  await page.getByRole('menuitem', { name: 'Duplicate set' }).click()
  await expect(page.getByText('Duplicated as Secrets (copy). Nothing is live until you publish.')).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Secrets (copy)', exact: true })).toBeVisible()
  const post = rules.calls.find(call => call.method === 'POST' && call.path === '/api/rules/sets')
  expect(post?.body).toMatchObject({ name: 'Secrets (copy)' })
  const put = rules.calls.find(call => call.method === 'PUT' && call.path.endsWith('/draft'))
  expect((put?.body as { name: string }).name).toBe('Secrets (copy)')
  const copies = draftRules(put?.body)
  expect(copies).toHaveLength(2)
  for (const rule of copies) {
    expect(rule.strength).toBe('normal')
    expect(rule.text).toContain(' (copy)')
    expect(rule.identity).not.toBe('keep-secrets')
    expect(rule.identity).not.toBe('record-source')
    expect(rule.source.identity).toBeUndefined()
  }
  expect(rules.calls.some(call => call.path.includes('publish'))).toBe(false)
  await expect(page.getByRole('heading', { name: 'Secrets', exact: true })).toBeVisible()
})

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
  await expect(page.getByRole('checkbox', { name: 'Record the source. is on' })).toHaveCount(0)
  await expect(page.getByRole('checkbox', { name: 'Your package is your scope. is on' })).toHaveCount(0)
  await page.getByRole('button', { name: 'Actions for Secrets' }).click()
  await expect(page.getByRole('menuitem', { name: /Duplicate set/ })).toHaveAttribute('aria-disabled', 'true')
})

test('an agent can draft a project rule and cannot touch company rules', async ({ page }) => {
  const rules = await setup(page, { kind: 'agent' })
  await expect(page.getByRole('checkbox', { name: 'Turn Company rules on or off' })).toHaveCount(0)
  await expect(page.getByRole('checkbox', { name: 'Turn Secrets rules on or off' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /Review and publish/ })).toHaveCount(0)
  await openSet(page, 'Scope')
  await page.getByRole('checkbox', { name: 'Your package is your scope. is on' }).uncheck()
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
  mkdirSync(SHOTS, { recursive: true })
  await setup(page)
  await openSet(page, 'Secrets')
  await page.getByRole('checkbox', { name: 'Record the source. is on' }).uncheck()
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
      await page.screenshot({ path: `${SHOTS}/read-mixed-${width}-${theme}.png`, fullPage: true })
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
      await page.screenshot({ path: `${SHOTS}/edit-reset-${width}-${theme}.png`, fullPage: true })
    }
  }
})
