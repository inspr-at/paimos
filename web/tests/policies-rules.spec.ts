// SPDX-License-Identifier: AGPL-3.0-only
// Settings › Policies, rules only (AEON-1165; approved design AEON-1143/1144, check flow G).
// Risks: tabs, a dialog, a ladder editor or a model name come back; opening a risk
// group moves the controls above it; phone targets drop below 44 px; a coloured
// edge accent creeps in; "Rule in Policies" lands anywhere but the review card.
import { expect, test, type Page } from '@playwright/test'
import { mockPolicies } from './policies-fixtures'
import { expectStableControls } from './helpers/stable'

// The approved design's probes (check.mjs), scoped to the Policies section. The
// review card's existing controls stay as they were (AEON-853) and are not re-measured.
async function probe(page: Page, min: number) {
  return page.evaluate(min => {
    const root = document.querySelector('.policies')!
    const vis = (e: Element) => { const r = e.getBoundingClientRect(), cs = getComputedStyle(e); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none' && !e.closest('[hidden], [inert]') }
    const small = [...root.querySelectorAll('button, a[href], input:not([type="checkbox"]), select, [tabindex="0"]')].filter(vis)
      .filter(e => !e.closest('.review-policy') && e.id !== 'review-policy-scope')
      .map(e => ({ e, r: e.getBoundingClientRect() })).filter(t => t.r.width < min - 0.5 || t.r.height < min - 0.5)
      .map(t => `${t.e.tagName.toLowerCase()}[${(t.e.textContent ?? '').trim().slice(0, 24)}] ${Math.round(t.r.width)}x${Math.round(t.r.height)}`)
    const accents = [...root.querySelectorAll('*')].filter(vis).flatMap(e => {
      const cs = getComputedStyle(e), out: string[] = [], w = (s: string) => parseFloat(cs.getPropertyValue(`border-${s}-width`)) || 0, c = (s: string) => cs.getPropertyValue(`border-${s}-color`), clear = (v: string) => /rgba\(.*,\s*0\)$|transparent/.test(v)
      for (const [a, b] of [['left', 'right'], ['top', 'bottom'], ['right', 'left'], ['bottom', 'top']] as const) if (w(a) >= 2 && !clear(c(a)) && (w(b) < w(a) || c(b) !== c(a))) out.push(`${e.className} border-${a}`)
      for (const m of cs.boxShadow.matchAll(/(-?[\d.]+)px\s+(-?[\d.]+)px\s+([\d.]+)px(?:\s+-?[\d.]+px)?\s+inset|inset\s+(-?[\d.]+)px\s+(-?[\d.]+)px\s+([\d.]+)px/g)) {
        const x = +(m[1] ?? m[4]), y = +(m[2] ?? m[5]), blur = +(m[3] ?? m[6]); if ((Math.abs(x) >= 2 || Math.abs(y) >= 2) && blur <= 1) out.push(`${e.className} inset ${x}/${y}`)
      }
      return out
    })
    const clipped = [...root.querySelectorAll('.settings-card h2, .rule-t b, .ask-t b, .rule-go, .rule-s, .btn, .risk-head b')].filter(vis).filter(e => e.scrollWidth > e.clientWidth + 1).map(e => (e.textContent ?? '').trim().slice(0, 30))
    const main = document.querySelector('main')!, edge = main.getBoundingClientRect().right
    // Whatever reaches past the content edge, named, so a failure says what overflowed.
    const wide = [...main.querySelectorAll('*')].filter(e => e.getBoundingClientRect().right > edge + 1 && e.getBoundingClientRect().width > 0).slice(0, 4).map(e => `${e.tagName.toLowerCase()}.${[...e.classList].join('.')} → ${Math.round(e.getBoundingClientRect().right)}`)
    return { small, accents, clipped, overflow: Math.max(main.scrollWidth - main.clientWidth, document.documentElement.scrollWidth - innerWidth), wide }
  }, min)
}
async function open(page: Page, theme: 'light' | 'dark', role: 'admin' | 'member' = 'admin') {
  const errors: string[] = []
  page.on('pageerror', error => errors.push(String(error)))
  const mock = await mockPolicies(page, theme, role)
  await page.goto('/settings/policies')
  await expect(page.locator('.policies .settings-card')).toHaveCount(3)
  await expect(page.locator('#cross-family-review .chosen li').first()).toContainText('OpenAI, xAI, Anthropic, Cursor, Google, local models')
  return { ...mock, errors }
}
// Evidence: the whole section in one picture, without the scroll container cutting it.
async function evidence(page: Page, path: string) {
  const size = page.viewportSize()!
  const height = await page.evaluate(() => Math.ceil(document.querySelector('main')!.scrollHeight + 40))
  await page.setViewportSize({ width: size.width, height: Math.max(size.height, height) })
  await page.mouse.move(0, 0)
  await page.screenshot({ path })
  await page.setViewportSize(size)
}

for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
  test.describe(`Policies at ${width} ${theme}`, () => {
    if (width === 390) test.use({ hasTouch: true, isMobile: true })
    const min = width === 390 ? 44 : 24

    test(`three cards, rules only; a risk group grows below its head at ${width} ${theme}`, async ({ page }, info) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      const state = await open(page, theme)
      const root = page.locator('.policies'), head = (risk: string) => root.locator(`[data-risk="${risk}"]`), desk = root.locator('.card-aside').getByRole('link', { name: 'Decision Desk' })
      await expect(root.locator('.intro')).toHaveText('Policies say who may do what. Which model runs is decided on Models.')
      await expect(page.locator('.body > .who')).toHaveCount(0)
      await expect(root.getByRole('tab')).toHaveCount(0)
      await expect(page.locator('.settings-page dialog')).toHaveCount(0)
      await expect(root.locator('.settings-card h2')).toHaveText(['Cross-family review', 'What agents must ask you for', 'Rules that live elsewhere'])
      await expect(root.locator('.risk-head')).toHaveText([/^High risk · 10 · Approve or deny/, /^Medium risk · 3 · Connect a chat/, /^Low risk · 3 · Read the audit log/])
      await expect(root.locator('.rule')).toHaveCount(12)
      await expect(root.locator('.rule').filter({ hasText: 'Approvals in force' }).getByRole('link', { name: 'Decision Desk' })).toHaveAttribute('href', '/decision-desk')
      await expect(root.locator('.rule').filter({ hasText: 'Merge queue and code owners' }).getByRole('link')).toHaveCount(0)
      expect(await root.textContent()).not.toMatch(/Touch ID|Face ID|GPT-|Opus|Grok 4|Sonnet/)

      await expectStableControls({
        controls: { intro: root.locator('.intro a'), desk, high: head('high'), save: page.locator('#cross-family-review').getByRole('button', { name: /Save rule/ }) },
        scrollAreas: { page: page.locator('main') },
        interactions: [
          { name: 'open High risk', run: async () => { await head('high').click(); await expect(head('high')).toHaveAttribute('aria-expanded', 'true'); await expect(root.locator('#agent-asks-high .ask')).toHaveCount(10); await expect(root.locator('#agent-asks-high .ask').last()).toBeVisible() } },
          { name: 'close High risk', run: async () => { await head('high').click(); await expect(head('high')).toHaveAttribute('aria-expanded', 'false'); await expect(root.locator('#agent-asks-high .ask').first()).toBeHidden() } },
        ],
      })
      await expectStableControls({
        controls: { desk, high: head('high'), medium: head('medium'), low: head('low') },
        scrollAreas: { page: page.locator('main') },
        interactions: [
          { name: 'open Low risk', run: async () => { await head('low').click(); await expect(root.locator('#agent-asks-low .ask').last()).toBeVisible(); await expect(root.locator('#agent-asks-low .ask').first()).toContainText('Asks you') } },
          { name: 'close Low risk', run: async () => { await head('low').click(); await expect(root.locator('#agent-asks-low .ask').first()).toBeHidden() } },
        ],
      })

      await head('high').click(); await expect(root.locator('#agent-asks-high .ask').last()).toBeVisible()
      const found = await probe(page, min)
      expect(found.small, `targets ≥ ${min} px`).toEqual([])
      expect(found.accents, 'no coloured edge accents').toEqual([])
      expect(found.clipped, 'one-line labels not clipped').toEqual([])
      expect(found.overflow, `no horizontal scroll: ${found.wide.join(' | ')}`).toBeLessThanOrEqual(1)
      expect(state.retired, 'no ladder or registry reads').toEqual([])
      expect(state.errors).toEqual([])
      await evidence(page, info.outputPath(`policies-${width}-${theme}.png`))
    })

    test(`a member reads the permission note and links only where they open at ${width} ${theme}`, async ({ page }, info) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      const state = await open(page, theme, 'member')
      const root = page.locator('.policies'), card = page.locator('#cross-family-review'), rule = (title: string) => root.locator('.rule').filter({ hasText: title })
      await expect(card.locator('.permission')).toHaveText('Changing this needs Manage review policy. Agents can read this rule but change it only with an explicit grant.')
      await expect(card.getByRole('button', { name: /Save rule/ })).toHaveCount(0)
      for (const title of ['Who holds which permission', 'Account use', 'Autopilot', 'Access log']) await expect(rule(title).getByRole('link')).toHaveCount(0)
      await expect(rule('Who holds which permission').locator('.rule-go')).toHaveText('Access')
      await expect(rule('Kinds of work').getByRole('link', { name: 'Kinds of work' })).toHaveAttribute('href', '/settings/kinds')
      await expect(rule('Routine guardrails').getByRole('link', { name: 'Routines' })).toHaveAttribute('href', '/settings/routines#rules')
      await expect(root.locator('.card-aside').getByRole('link', { name: 'Decision Desk' })).toHaveAttribute('href', '/decision-desk')
      const found = await probe(page, min)
      expect(found.small, `targets ≥ ${min} px`).toEqual([])
      expect(found.overflow, `no horizontal scroll: ${found.wide.join(' | ')}`).toBeLessThanOrEqual(1)
      expect(state.errors).toEqual([])
      await evidence(page, info.outputPath(`policies-member-${width}-${theme}.png`))
    })
  })
}

test('“Rule in Policies” lands on the cross-family card and the next Tab stays inside it', async ({ page }) => {
  await mockPolicies(page, 'light')
  await page.goto('/settings/developer')
  await page.evaluate(() => import('/src/router.ts').then(({ router }) => router.push('/settings/policies#cross-family-review')))
  const card = page.locator('#cross-family-review')
  await expect(card).toBeFocused()
  await expect(card).toHaveClass(/arrived/)
  await page.keyboard.press('Tab')
  expect(await card.evaluate(element => element !== document.activeElement && element.contains(document.activeElement))).toBe(true)
  // A deep link opened directly lands there too.
  await page.goto('/settings/policies#cross-family-review')
  await expect(card).toBeFocused()
})
