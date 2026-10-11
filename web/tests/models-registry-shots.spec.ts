// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { manyProfiles, mockRegistry, openRegistry, registryWorld } from './models-registry-fixtures'

// AEON-1163 · the grouped Model registry, in pictures and against the approved design's check (models-policies-approved
// check.mjs): screenshots at 1440, 1024 and 390 px, light and dark, and the check's common probes with folds, the pause
// menu and the remove question open. Kept apart from models-registry.spec.ts so each spec stays within its shard's budget.

const card = (page: Page) => page.locator('[data-model-registry]')
const row = (page: Page, key: string) => page.locator(`[data-reg-row="${key}"]`)
const fold = (page: Page, key: string) => row(page, key).locator('[data-reg-fold]')
const body = (page: Page, key: string) => row(page, key).locator('[data-reg-body]')
const editor = (page: Page) => page.locator('[data-reg-editor]')
const confirm = (page: Page, key: string) => row(page, key).locator('[data-reg-confirm]')
const menu = (page: Page) => page.locator('[data-reg-pause-menu]')
const OPUS = 'anthropic:opus', GROK = 'xai:grok', QWEN = 'unknown:openrouter/qwen/qwen3-coder'
async function open(page: Page, key: string) {
  await fold(page, key).click()
  await expect(fold(page, key)).toHaveAttribute('aria-expanded', 'true')
  await expect(body(page, key)).toBeVisible()
}

test('screenshots of the registry at 1440, 1024 and 390 px, light and dark', async ({ page }, testInfo) => {
  await mockRegistry(page, registryWorld({ profiles: manyProfiles(), usage: { incomplete: false, used_by: [{ column: 'other', layer: 'default', replacement: { line: 'openai:astra', effort: 'xhigh', harness: 'codex', model: 'gpt-6.1-astra' } }] } }))
  await openRegistry(page)
  await expect(row(page, OPUS)).toBeVisible()
  // Tall enough for the whole card: the harness scrolls inside its own frame.
  for (const [width, height] of [[1440, 3200], [1024, 3400], [390, 5200]] as const) {
    for (const scheme of ['light', 'dark'] as const) {
      await page.setViewportSize({ width, height }); await page.emulateMedia({ colorScheme: scheme, reducedMotion: 'reduce' })
      const shot = async (name: string) => card(page).screenshot({ path: testInfo.outputPath(`${name}-${width}-${scheme}.png`) })
      await shot('list')
      await open(page, OPUS); await open(page, GROK)
      await shot('folds')
      await body(page, OPUS).getByRole('button', { name: 'Remove…' }).click()
      await expect(confirm(page, OPUS)).toContainText('takes over')
      await body(page, OPUS).locator('[data-reg-pause]').click()
      await expect(menu(page)).toBeVisible()
      await page.screenshot({ path: testInfo.outputPath(`pause-menu-${width}-${scheme}.png`) })
      await page.keyboard.press('Escape')
      await shot('remove-question')
      await confirm(page, OPUS).getByRole('button', { name: 'Keep it' }).click()
      await fold(page, OPUS).click(); await fold(page, GROK).click()
      await card(page).locator('[data-reg-discovery-fold]').click()
      await card(page).getByRole('button', { name: 'Add model' }).click()
      await editor(page).getByRole('button', { name: /^Add/ }).click()
      await shot('discovery-add-errors')
      await editor(page).getByRole('button', { name: /^Cancel/ }).click()
      await card(page).locator('[data-reg-discovery-fold]').click()
      await expect(card(page).locator('[data-reg-fold][aria-expanded="true"]')).toHaveCount(0)
    }
  }
})

// The approved design's check (models-policies-approved check.mjs) run against the built card: its common probes
// (page errors, horizontal scroll, no dialog element, no coloured edge accents, touch targets, one-line labels not cut)
// with folds, the pause menu and the remove question open, at 1440 and 400 px, light and dark. On touch screens
// base.css widens a button's hit area with ::before, so the larger box counts, as in the AEON-1164 sheet check.
const probes = (page: Page, min: number) => page.evaluate(least => {
  const vis = (e: Element) => { const r = e.getBoundingClientRect(), cs = getComputedStyle(e); return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none' && !e.closest('[hidden], [inert]') }
  const scope = [...document.querySelectorAll('[data-model-registry], .mdl-pop')]
  const all = scope.flatMap(root => [root, ...root.querySelectorAll('*')]).filter(vis)
  const accents = all.flatMap(e => {
    const cs = getComputedStyle(e), out: string[] = [], w = (s: string) => parseFloat(cs.getPropertyValue(`border-${s}-width`)) || 0, c = (s: string) => cs.getPropertyValue(`border-${s}-color`), clear = (v: string) => /rgba\(.*,\s*0\)$|transparent/.test(v)
    for (const [a, b] of [['left', 'right'], ['top', 'bottom'], ['right', 'left'], ['bottom', 'top']]) if (w(a) >= 2 && !clear(c(a)) && (w(b) < w(a) || c(b) !== c(a))) out.push(`${e.className} border-${a}`)
    for (const m of cs.boxShadow.matchAll(/inset\s+(-?[\d.]+)px\s+(-?[\d.]+)px\s+([\d.]+)px|(-?[\d.]+)px\s+(-?[\d.]+)px\s+([\d.]+)px[^,]*inset/g)) {
      const x = +(m[1] ?? m[4]), y = +(m[2] ?? m[5]), blur = +(m[3] ?? m[6]); if ((Math.abs(x) >= 2 || Math.abs(y) >= 2) && blur <= 1) out.push(`${e.className} inset ${x}/${y}`)
    }
    return out
  })
  const targets = all.filter(e => e.matches('button, [role="menuitem"], input:not([type="checkbox"]), select, label.switch, [tabindex="0"]')).map(e => {
    const r = e.getBoundingClientRect(), hit = getComputedStyle(e, '::before'), wide = hit.content !== 'none' && hit.position === 'absolute'
    return { e, w: Math.max(r.width, wide ? parseFloat(hit.width) || 0 : 0), h: Math.max(r.height, wide ? parseFloat(hit.height) || 0 : 0) }
  }).filter(t => t.w < least - 0.5 || t.h < least - 0.5).map(t => `${t.e.tagName.toLowerCase()}.${[...t.e.classList].join('.')}[${(t.e.textContent || t.e.getAttribute('aria-label') || '').trim().slice(0, 18)}] ${Math.round(t.w)}x${Math.round(t.h)}`)
  const clipped = all.filter(e => e.matches('.ln-n, .rt-h, .pg-h, .btn, .mi .t')).filter(e => e.scrollWidth > e.clientWidth + 1).map(e => (e.textContent ?? '').trim().slice(0, 30))
  return { accents, targets, clipped, dialogs: document.querySelectorAll('dialog').length, overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth }
}, min)
async function designCheck(page: Page, width: number, min: number) {
  const errors: string[] = []
  page.on('pageerror', error => errors.push(String(error)))
  await mockRegistry(page, registryWorld({ profiles: manyProfiles(), usage: { incomplete: false, used_by: [{ column: 'other', layer: 'default', replacement: { line: 'openai:astra', effort: 'xhigh', harness: 'codex', model: 'gpt-6.1-astra' } }] } }))
  await openRegistry(page)
  await expect(row(page, OPUS)).toBeVisible()
  for (const scheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: scheme, reducedMotion: 'reduce' })
    const state = async (what: string) => {
      const found = await probes(page, min)
      expect(found.accents, `${width} ${scheme} ${what}: no coloured edge accents`).toEqual([])
      expect(found.targets, `${width} ${scheme} ${what}: targets ≥ ${min} px`).toEqual([])
      expect(found.clipped, `${width} ${scheme} ${what}: one-line labels not cut`).toEqual([])
      expect(found.dialogs, `${width} ${scheme} ${what}: no dialog element`).toBe(0)
      expect(found.overflow, `${width} ${scheme} ${what}: no horizontal scroll`).toBeLessThanOrEqual(1)
    }
    await state('folded')
    await open(page, OPUS); await open(page, GROK); await open(page, QWEN)
    await card(page).locator('[data-reg-discovery-fold]').click()
    await state('folds open')
    // Flow E: many models stay calm, one row per line.
    expect(await page.locator('[data-reg-row]').count()).toBeLessThanOrEqual(20)
    // A menu opens inside the viewport and moves nothing on the card.
    const tops = () => page.locator('[data-model-registry] .ln-row').evaluateAll(els => els.map(el => Math.round(el.getBoundingClientRect().top + (el.closest('.harness-scroll')?.scrollTop ?? 0))).join(','))
    const before = await tops()
    await body(page, OPUS).locator('[data-reg-pause]').click()
    await expect(menu(page)).toBeVisible()
    const box = (await page.locator('.mdl-pop').boundingBox())!, view = page.viewportSize()!
    expect(box.x).toBeGreaterThanOrEqual(-0.5); expect(box.y).toBeGreaterThanOrEqual(-0.5)
    expect(box.x + box.width).toBeLessThanOrEqual(view.width + 0.5); expect(box.y + box.height).toBeLessThanOrEqual(view.height + 0.5)
    expect(await tops(), 'the pause menu moves nothing').toBe(before)
    await state('pause menu')
    await page.keyboard.press('Escape')
    await body(page, OPUS).locator('[data-reg-remove]').click()
    await expect(confirm(page, OPUS)).toContainText('takes over')
    await state('remove question')
    await confirm(page, OPUS).getByRole('button', { name: 'Keep it' }).click()
    await fold(page, OPUS).click(); await fold(page, GROK).click(); await fold(page, QWEN).click()
    await card(page).locator('[data-reg-discovery-fold]').click()
  }
  expect(errors, 'no page errors').toEqual([])
}
test('the design check passes on the built registry at 1440 px, light and dark', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await designCheck(page, 1440, 24)
})
test.describe('on a phone, the design check', () => {
  test.use({ viewport: { width: 400, height: 860 }, hasTouch: true, isMobile: true })
  test('passes on the built registry at 400 px with 44 px targets, light and dark', async ({ page }) => {
    await designCheck(page, 400, 44)
  })
})
