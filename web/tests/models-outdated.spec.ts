// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { controlStability } from './control-stability'
import { mockProfile, mockRegistry, openRegistry, registryProfiles, registryWorld, type MockOutdatedVersion, type MockProfile, type RegistryWorld } from './models-registry-fixtures'

// AEON-1164 · Settings › Models › Model registry: the outdated tray and the review sheet
// (approved design AEON-1143/1144, check.mjs flow D). Each test names the risk it guards.

const MIA = '22222222-2222-4222-8222-222222222222'
const REVISION = 'a'.repeat(64)
const IDENTITY: Record<string, string> = { codex: 'openai', claude: 'anthropic', grok: 'xai', gemini: 'google', cursor: 'cursor' }
const identityOf = (profile: MockProfile) => profile.harness === 'pi' ? profile.model.replace(/^anthropic\//, 'anthropic:').replace(/^openrouter\/qwen\//, 'qwen:') : `${IDENTITY[profile.harness]}:${profile.model}`
const levels = (base: Omit<Parameters<typeof mockProfile>[0], 'effort'>, efforts: string[]) => efforts.map(effort => mockProfile({ ...base, effort }))

/** The design's "Outdated versions" state: auto-update was off, so four old versions are still used five times. */
function outdatedProfiles(): MockProfile[] {
  const profiles = registryProfiles().map(profile => profile.model === 'gpt-6-sol' ? { ...profile, retired: false } : profile)
  profiles.push(
    ...levels({ harness: 'pi', model: 'anthropic/claude-opus-5', family: 'anthropic', display_name: 'Claude Opus', short_name: 'Opus', model_version: '5' }, ['high', 'xhigh']),
    ...levels({ harness: 'grok', model: 'grok-4.5', family: 'xai', display_name: 'Grok', short_name: 'Grok', model_version: '4.5' }, ['high']),
    ...levels({ harness: 'gemini', model: 'gemini-2.5-pro', family: 'google', display_name: 'Gemini Pro', short_name: 'Gemini Pro', model_version: '2.5' }, ['low', 'high']),
    ...levels({ harness: 'gemini', model: 'gemini-3-pro', family: 'google', display_name: 'Gemini Pro', short_name: 'Gemini Pro', model_version: '3', note: 'long context' }, ['low', 'high']),
  )
  return profiles.map(profile => ({ ...profile, identity: identityOf(profile) }))
}
const use = (label: string, who: string, from: string, to: string) => ({ label, who, from, to })
function outdatedVersions(): MockOutdatedVersion[] {
  const version = (over: Partial<MockOutdatedVersion> & Pick<MockOutdatedVersion, 'version' | 'successor' | 'line'>): MockOutdatedVersion => ({ state: 'superseded', date: null, reason: 'Source reports a successor', source: 'agent-report:codex', uses: [], incomplete: false, ...over })
  return [
    version({ version: 'anthropic:claude-opus-5', line: 'anthropic:claude-opus', successor: 'anthropic:claude-opus-5-5', state: 'retiring', date: '2027-01-15T12:00:00Z', source: 'https://api.anthropic.com/v1/models', reason: 'Source reports a retirement date',
      uses: [use('Except for · concept · first', MIA, 'anthropic:claude-opus-5', 'anthropic:claude-opus-5-5'), use('Except for · backend · first', 'Default', 'anthropic:claude-opus-5', 'anthropic:claude-opus-5-5')] }),
    version({ version: 'google:gemini-2.5-pro', line: 'google:gemini-pro', successor: 'google:gemini-3-pro', source: 'aeon_model_line/aeon_model_version_newer', reason: 'Same model line has a newer sourced version',
      uses: [use('fallback · backend · first', 'Default', 'google:gemini-2.5-pro', 'google:gemini-3-pro')] }),
    version({ version: 'openai:gpt-6-sol', line: 'openai:gpt-sol', successor: 'openai:gpt-6.1-sol', uses: [use('Default · other · first', MIA, 'openai:gpt-6-sol', 'openai:gpt-6.1-sol')] }),
    version({ version: 'xai:grok-4.5', line: 'xai:grok', successor: 'xai:grok-4.7', source: 'aeon_model_line/aeon_model_version_newer', reason: 'Same model line has a newer sourced version',
      uses: [use('Default · other · fix', 'Default', 'xai:grok-4.5', 'xai:grok-4.7')] }),
  ]
}
function outdatedWorld(over: Partial<RegistryWorld> = {}, auto = false): RegistryWorld {
  const versions = outdatedVersions()
  if (auto) for (const version of versions) version.uses = []
  return registryWorld({
    profiles: outdatedProfiles(), permissions: ['models.read', 'models.manage', 'models.refresh', 'account.use.manage', 'members.read'],
    settings: { agent_reports_enabled: true, auto_add_profiles: auto, api_enabled: true, interval_minutes: 360 },
    outdated: { versions, revision: REVISION, nextCursor: '', incomplete: false, failRetire: null, undo: null }, ...over,
  })
}

const card = (page: Page) => page.locator('[data-model-registry]')
const tray = (page: Page) => page.locator('[data-reg-tray]')
const review = (page: Page) => page.locator('[data-reg-review]')
const sheet = (page: Page) => page.getByRole('dialog', { name: 'Remove old versions' })
const go = (page: Page) => sheet(page).locator('[data-od-go]')
const toasts = (page: Page) => page.locator('.toast')
async function setup(page: Page, world: RegistryWorld) {
  await page.route('**/api/members', route => route.fulfill({ json: { people: [{ principal_id: MIA, name: 'Mia' }], agents: [], invites: [], imported: [], owner_count: 1 } }))
  await mockRegistry(page, world)
  await openRegistry(page)
  await expect(page.locator('[data-reg-row="gpt-6.1-sol"]')).toBeVisible()
  return world
}
/** Top edges of everything on the card, in page coordinates: an overlay must not change one of them. */
const tops = (page: Page) => page.locator('[data-model-registry] .reg-strip, [data-model-registry] [data-reg-tray], [data-model-registry] .reg-row').evaluateAll(els => els.map(el => Math.round(el.getBoundingClientRect().top + (el.closest('.harness-scroll')?.scrollTop ?? 0))).join(','))
/** The design check's edge-accent probe (check.mjs __accents): a 2 px or wider coloured edge, or an inset bar. */
const accents = (page: Page) => page.locator('[role="dialog"], [role="dialog"] *').evaluateAll(els => els.flatMap(el => {
  const cs = getComputedStyle(el), out: string[] = [], width = (side: string) => parseFloat(cs.getPropertyValue(`border-${side}-width`)) || 0, colour = (side: string) => cs.getPropertyValue(`border-${side}-color`)
  const clear = (value: string) => /rgba\(.*,\s*0\)$|transparent/.test(value)
  for (const [a, b] of [['left', 'right'], ['top', 'bottom'], ['right', 'left'], ['bottom', 'top']]) if (width(a) >= 2 && !clear(colour(a)) && (width(b) < width(a) || colour(b) !== colour(a))) out.push(`${el.className} border-${a}`)
  for (const m of cs.boxShadow.matchAll(/inset\s+(-?[\d.]+)px\s+(-?[\d.]+)px\s+([\d.]+)px|(-?[\d.]+)px\s+(-?[\d.]+)px\s+([\d.]+)px[^,]*inset/g)) {
    const x = +(m[1] ?? m[4]), y = +(m[2] ?? m[5]), blur = +(m[3] ?? m[6])
    if ((Math.abs(x) >= 2 || Math.abs(y) >= 2) && blur <= 1) out.push(`${el.className} inset ${x}/${y}`)
  }
  return out
}))
/**
 * Controls in the sheet whose touch target is under the minimum (check.mjs __targets). A checkbox's target is
 * its whole row; on touch screens base.css widens buttons with a ::before hit area, so the larger box counts.
 */
const small = (page: Page, min: number) => page.locator('[role="dialog"] button, [role="dialog"] label.od-l').evaluateAll((els, least) => els.map(el => {
  const r = el.getBoundingClientRect(), hit = getComputedStyle(el, '::before')
  const w = Math.max(r.width, hit.content !== 'none' && hit.position === 'absolute' ? parseFloat(hit.width) || 0 : 0), h = Math.max(r.height, hit.content !== 'none' && hit.position === 'absolute' ? parseFloat(hit.height) || 0 : 0)
  return { el, w, h, shown: r.width > 0 && r.height > 0 }
}).filter(t => t.shown && (t.w < least - 0.5 || t.h < least - 0.5)).map(t => `${t.el.className}[${(t.el.textContent || t.el.getAttribute('aria-label') || '').trim().slice(0, 18)}] ${Math.round(t.w)}x${Math.round(t.h)}`), min)

test('the tray names the two successors that take over most uses and the count, only for admins and only when something is outdated', async ({ page }) => {
  const world = await setup(page, outdatedWorld())
  // The list comes sorted by identity; Opus 5 has two uses, the others one each, so Gemini is the second name.
  await expect(tray(page).locator('[data-reg-tray-text]')).toHaveText('Claude Opus 5.5 replaces Claude Opus 5 · Gemini Pro 3 replaces Gemini Pro 2.5 · 4 old versions')
  await expect(review(page)).toHaveAttribute('aria-haspopup', 'dialog')
  expect(world.reads.filter(path => path === '/models/outdated')).toHaveLength(1)
  // People who read models but don't manage them get no tray, and the page doesn't ask for the list.
  const reader = await setup(page, outdatedWorld({ permissions: ['models.read'] }))
  await expect(tray(page)).toHaveCount(0)
  expect(reader.reads.filter(path => path === '/models/outdated')).toEqual([])
  // Nothing outdated, or a server without the list: no tray, and the registry still loads.
  await setup(page, outdatedWorld({ outdated: { versions: [], revision: REVISION, nextCursor: '', incomplete: false, failRetire: null, undo: null } }))
  await expect(tray(page)).toHaveCount(0)
  await setup(page, outdatedWorld({ outdated: null }))
  await expect(tray(page)).toHaveCount(0)
  await expect(page.locator('[data-reg-row="gpt-6-sol"]')).toBeVisible()
})

test('the review sheet overlays the page: inside the viewport, nothing on the page moves, focus stays in it and Esc closes it without a write', async ({ page }) => {
  const world = await setup(page, outdatedWorld())
  const before = await tops(page)
  await review(page).click()
  await expect(sheet(page)).toBeVisible()
  expect(await page.locator('dialog').count(), 'no dialog element').toBe(0)
  const box = (await sheet(page).boundingBox())!, view = page.viewportSize()!
  expect(box.x).toBeGreaterThanOrEqual(-0.5); expect(box.y).toBeGreaterThanOrEqual(-0.5)
  expect(box.x + box.width).toBeLessThanOrEqual(view.width + 0.5); expect(box.y + box.height).toBeLessThanOrEqual(view.height + 0.5)
  expect(await tops(page), 'the sheet moves nothing on the page').toBe(before)
  expect(await accents(page)).toEqual([])
  expect(await small(page, 24)).toEqual([])
  // Every old version with why it is old, and every use that moves.
  await expect(sheet(page).locator('.od')).toHaveCount(4)
  await expect(sheet(page).locator('.rf:not(.none)')).toHaveCount(5)
  await expect(sheet(page).locator('[data-od-row="anthropic:claude-opus-5"] .od-n')).toHaveText(/^Claude Opus 5\s*replaced by\s*Claude Opus 5\.5$/)
  await expect(sheet(page).locator('[data-od-row="anthropic:claude-opus-5"] .od-r')).toHaveText('pi · anthropic/claude-opus-5')
  await expect(sheet(page).locator('[data-od-row="anthropic:claude-opus-5"] .od-why')).toHaveText('Retires on 15 Jan 2027From provider')
  await expect(sheet(page).locator('[data-od-row="xai:grok-4.5"] .od-why')).toHaveText('Newer version in the same line')
  await expect(sheet(page).locator('.rf').first()).toContainText('ConceptsMia')
  await expect(sheet(page).locator('.rf').filter({ hasText: 'Fix rounds · Default' })).toContainText('for everyone · situation')
  await expect(sheet(page).locator('.od-note')).toContainText('Auto-update is off, which is why these are still in use.')
  // Focus starts on the first box and never leaves: a full round of Tab and of Shift+Tab stays inside,
  // and the last control in the sheet's order wraps to the first (the shared sheet keeps its actions before the list).
  const first = sheet(page).locator('[data-od]').first(), close = sheet(page).getByRole('button', { name: 'Close Remove old versions' })
  await expect(first).toBeFocused()
  const inside = () => page.evaluate(() => !!document.activeElement?.closest('[role="dialog"]'))
  for (const key of ['Tab', 'Shift+Tab']) for (let i = 0; i < 9; i++) { await page.keyboard.press(key); expect(await inside(), `${key} ${i + 1} stays in the sheet`).toBe(true) }
  await sheet(page).locator('[data-od]').last().focus(); await page.keyboard.press('Tab')
  await expect(close).toBeFocused()
  await page.keyboard.press('Shift+Tab')
  await expect(sheet(page).locator('[data-od]').last()).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(sheet(page)).toHaveCount(0)
  await expect(review(page)).toBeFocused()
  expect(world.writes.filter(write => write.path.startsWith('/models/outdated'))).toEqual([])
  expect(await tops(page)).toBe(before)
})

test('unticking versions changes what moves and the button label while the actions and boxes stay put (±0.5 px)', async ({ page }) => {
  await setup(page, outdatedWorld())
  await review(page).click()
  const boxes = sheet(page).locator('[data-od]')
  const guard = await controlStability(page, { first: boxes.first(), last: boxes.last(), keep: sheet(page).locator('[data-od-keep]'), go: go(page), close: sheet(page).getByRole('button', { name: 'Close Remove old versions' }) })
  await expect(go(page)).toHaveAccessibleName('Remove 4 old versions')
  await guard.check(() => boxes.nth(0).click())
  await expect(go(page)).toHaveAccessibleName('Remove 3 old versions')
  await expect(sheet(page).locator('.rf:not(.none)')).toHaveCount(3)
  await guard.check(() => boxes.nth(1).click())
  await guard.check(() => boxes.nth(2).click())
  await expect(go(page)).toHaveAccessibleName('Remove 1 old version')
  await guard.check(() => boxes.nth(3).click())
  await expect(go(page)).toHaveAccessibleName('Nothing selected')
  await expect(go(page)).toBeDisabled()
  await expect(sheet(page).locator('.rf.none')).toHaveText('Select an old version to see what moves.')
  await guard.check(() => boxes.nth(3).click())
  await expect(sheet(page).locator('.rf:not(.none)')).toHaveCount(1)
  guard.done()
})

test('Remove moves every use and removes the versions in one step; Undo restores the versions and every use', async ({ page }) => {
  const world = await setup(page, outdatedWorld())
  const text = await tray(page).locator('[data-reg-tray-text]').textContent()
  await review(page).click()
  await go(page).click()
  await expect(sheet(page)).toHaveCount(0)
  expect(world.writes.filter(write => write.path.startsWith('/models/outdated'))).toEqual([{ method: 'POST', path: '/models/outdated/retire', body: { versions: ['anthropic:claude-opus-5', 'google:gemini-2.5-pro', 'openai:gpt-6-sol', 'xai:grok-4.5'], revision: REVISION } }])
  await expect(tray(page)).toHaveCount(0)
  await expect(page.locator('[data-reg-row="gpt-6-sol"]')).toHaveCount(0)
  await expect(page.locator('[data-reg-add]')).toBeFocused()
  const done = toasts(page).filter({ hasText: 'Removed 4 old versions · 5 uses moved' })
  await expect(done).toBeVisible()
  await done.getByRole('button', { name: 'Undo' }).click()
  await expect(toasts(page).filter({ hasText: 'Undone' })).toBeVisible()
  expect(world.writes.at(-1)).toEqual({ method: 'POST', path: '/models/outdated/undo', body: { undo_token: `41.${'c'.repeat(64)}` } })
  // The versions and every use are back: the same tray, the same rows, the same five uses in the sheet.
  await expect(tray(page).locator('[data-reg-tray-text]')).toHaveText(text!)
  await expect(page.locator('[data-reg-row="gpt-6-sol"]')).toBeVisible()
  await review(page).click()
  await expect(sheet(page).locator('.rf:not(.none)')).toHaveCount(5)
})

test('a review that went stale is refused without removing anything, says so and shows the list read again', async ({ page }) => {
  const world = await setup(page, outdatedWorld())
  await review(page).click()
  await expect(sheet(page).locator('.od')).toHaveCount(4)
  // Another admin removed Grok 4.5 meanwhile; the revision on screen no longer matches.
  world.outdated!.versions = world.outdated!.versions.filter(version => version.version !== 'xai:grok-4.5')
  world.outdated!.revision = 'f'.repeat(64)
  const reads = world.reads.filter(path => path === '/models/outdated').length
  await go(page).click()
  await expect(toasts(page).filter({ hasText: 'The old versions changed meanwhile. Nothing was removed; the list was refreshed, so review it and remove again.' })).toBeVisible()
  await expect(sheet(page)).toBeVisible()
  await expect(sheet(page).locator('.od')).toHaveCount(3)
  expect(world.reads.filter(path => path === '/models/outdated').length).toBeGreaterThan(reads)
  expect(world.profiles.filter(profile => profile.retired && profile.identity === 'openai:gpt-6-sol')).toEqual([])
  // A refusal for another reason keeps the sheet and the choices too.
  world.outdated!.failRetire = { status: 403, error: 'forbidden' }
  await go(page).click()
  await expect(toasts(page).filter({ hasText: 'You can’t remove old versions or move their uses. Ask a workspace admin.' })).toBeVisible()
  await expect(go(page)).toHaveAccessibleName('Remove 3 old versions')
})

test('with auto-update on the uses already moved, so the tray says nothing uses them and Remove only removes', async ({ page }) => {
  const world = await setup(page, outdatedWorld({}, true))
  await expect(tray(page).locator('[data-reg-tray-text]')).toContainText('4 old versions, nothing uses them')
  await review(page).click()
  await expect(sheet(page).locator('.rf.none')).toHaveText('Nothing uses them.')
  await expect(sheet(page).locator('.od-note')).toContainText('Auto-update is on, so uses already moved by themselves; this only removes the old versions.')
  await go(page).click()
  await expect(toasts(page).filter({ hasText: 'Removed 4 old versions · 0 uses moved' })).toBeVisible()
  expect(world.writes.at(-1)?.path).toBe('/models/outdated/retire')
})

test.describe('on a phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  test('the sheet fills the screen with its actions pinned at the bottom, Keep them left of Remove, and every target is at least 44 px', async ({ page }) => {
    await setup(page, outdatedWorld())
    await review(page).click()
    const box = (await sheet(page).boundingBox())!
    expect(box.width).toBeCloseTo(390, 0); expect(box.height).toBeCloseTo(844, 0)
    const keep = (await sheet(page).locator('[data-od-keep]').boundingBox())!, remove = (await go(page).boundingBox())!
    expect(keep.x + keep.width).toBeLessThanOrEqual(remove.x + 0.5)
    expect(remove.y + remove.height).toBeGreaterThan(844 - 80)
    expect(await small(page, 44)).toEqual([])
    expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
  })
})

test('screenshots of the tray and the sheet at 1440 and 390 px, light and dark', async ({ page }, testInfo) => {
  await setup(page, outdatedWorld())
  for (const [width, height, tall] of [[1440, 1000, 1900], [390, 844, 3600]] as const) {
    for (const scheme of ['light', 'dark'] as const) {
      // The harness scrolls inside its own frame: the whole card needs a tall window, the sheet a real one.
      await page.setViewportSize({ width, height: tall }); await page.emulateMedia({ colorScheme: scheme, reducedMotion: 'reduce' })
      await card(page).screenshot({ path: testInfo.outputPath(`tray-${width}-${scheme}.png`) })
      await page.setViewportSize({ width, height })
      await review(page).click()
      await expect(sheet(page)).toBeVisible()
      await expect(sheet(page).locator('.rf').first()).toContainText('Mia')
      await expect(sheet(page).locator('.rf').nth(1)).toContainText('Backend build')
      await page.screenshot({ path: testInfo.outputPath(`sheet-${width}-${scheme}.png`) })
      await page.keyboard.press('Escape')
      await expect(sheet(page)).toHaveCount(0)
    }
  }
})
