// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { knowledgeWorld, mockKnowledge } from './knowledge-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'

test.use({ launchOptions: { args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] } })

async function setup(page: Page) {
  const data = fixtures()
  await mockWork(page, data)
  const world = knowledgeWorld()
  await mockKnowledge(page, world)
  const nodes = world.entries.filter(n => n.project === 'p-pharos' && n.status !== 'archived').map(n => ({
    id: n.id, key: n.key, type: n.type, kind: 'knowledge', slug: n.slug, title: n.title, status: n.status, degree: 2, updated_at: n.updated_at,
  }))
  const edges = nodes.slice(1).map((n, i) => ({ source: nodes[i].id, target: n.id, kind: 'mention', label: 'mentions' }))
  await page.route('**/api/knowledge/graph?*', route => route.fulfill({ json: { nodes, edges, truncated: false } }))
  await mockSettings(page, settingsData())
  return data
}

const canvas = (page: Page) => page.locator('.kg-canvas')
const labels = (page: Page) => page.getByRole('combobox', { name: 'Graph labels', exact: true })
const motionOf = (page: Page, name: string) => page.getByRole('radiogroup', { name: 'Graph motion' }).getByRole('radio', { name })

async function openGraph(page: Page) {
  await page.goto('/p/PHAROS/knowledge?view=graph')
  await expect(canvas(page)).toHaveAttribute('data-ready', 'true', { timeout: 30_000 })
}

async function openAppearance(page: Page) {
  // Graph controls mount after the profile fixture arrives, independently of
  // the document load event used by goto().
  const [profile] = await Promise.all([
    page.waitForResponse(response => new URL(response.url()).pathname === '/api/me/profile'),
    page.goto('/settings/personal#appearance'),
  ])
  await profile.finished()
}

test('default orbit is one turn per 120s and Personal can change it', async ({ page }) => {
  test.setTimeout(120_000)
  const data = await setup(page)
  await openGraph(page)
  await expect(canvas(page)).toHaveAttribute('data-orbit-seconds', '120')

  await openAppearance(page)
  const motion = page.getByRole('radiogroup', { name: 'Graph motion' })
  await expect(motion).toBeVisible()
  await expect(motion.getByRole('radio', { name: 'Default' })).toBeChecked()
  await motion.getByRole('radio', { name: 'Default' }).focus()
  await page.keyboard.press('ArrowRight')
  await expect(motion.getByRole('radio', { name: 'Lively' })).toBeChecked()
  await expect.poll(() => data.preferences['graph-motion']).toEqual({ pace: 'lively' })
  await openGraph(page)
  await expect(canvas(page)).toHaveAttribute('data-orbit-seconds', '60', { timeout: 15_000 })

  await openAppearance(page)
  await motionOf(page, 'Slow').click()
  await expect.poll(() => data.preferences['graph-motion']).toEqual({ pace: 'slow' })
  await openGraph(page)
  await expect(canvas(page)).toHaveAttribute('data-orbit-seconds', '180', { timeout: 15_000 })

  await openAppearance(page)
  await motionOf(page, 'Off').click()
  await expect.poll(() => data.preferences['graph-motion']).toEqual({ pace: 'off' })
  await openGraph(page)
  await expect(canvas(page)).toHaveAttribute('data-orbit-seconds', '0', { timeout: 15_000 })
})

test.describe('label fades', () => {
  test.use({ reducedMotion: 'no-preference' })

  test('labels fade with an opacity transition instead of toggling display', async ({ page }) => {
    test.setTimeout(120_000)
    await setup(page)
    await openGraph(page)
    await page.getByRole('button', { name: 'Pause motion' }).click()
    await expect(canvas(page)).toHaveAttribute('data-motion', 'still')
    await expect(canvas(page)).toHaveAttribute('data-labels-ready', 'true', { timeout: 15_000 })
    const probe = canvas(page).locator('.graph-label:not([hidden])').first()
    await expect(probe).toBeVisible()
    const before = await probe.evaluate(el => {
      const style = getComputedStyle(el)
      return { opacity: parseFloat(style.opacity), property: style.transitionProperty, duration: style.transitionDuration, timing: style.transitionTimingFunction }
    })
    expect(before.property).toContain('opacity')
    expect(before.duration).toBe('0.7s')
    expect(['ease-in-out', 'cubic-bezier(0.42, 0, 0.58, 1)']).toContain(before.timing)
    expect(before.opacity).toBeGreaterThan(0.5)

    await probe.evaluate(el => {
      el.setAttribute('data-fade-probe', '1')
      const samples: { opacity: number; display: string; hidden: boolean }[] = []
      const started = performance.now()
      const step = () => {
        const style = getComputedStyle(el)
        samples.push({ opacity: parseFloat(style.opacity), display: style.display, hidden: (el as HTMLElement).hidden })
        if (performance.now() - started < 1600) requestAnimationFrame(step)
        else Object.assign(window, { __fadeSamples: samples })
      }
      requestAnimationFrame(step)
    })
    await labels(page).selectOption('off')
    await page.waitForFunction(() => Array.isArray((window as unknown as { __fadeSamples?: unknown[] }).__fadeSamples))
    const fading = await page.evaluate(() => (window as unknown as { __fadeSamples: { opacity: number; display: string; hidden: boolean }[] }).__fadeSamples)
    expect(fading.some(sample => !sample.hidden && sample.display !== 'none' && sample.opacity > 0.05 && sample.opacity < 0.92)).toBe(true)
    await expect(canvas(page)).toHaveAttribute('data-labels-ready', 'true', { timeout: 15_000 })
    await expect(page.locator('.graph-label:not([hidden])')).toHaveCount(0)

    await page.evaluate(() => {
      const samples: { opacity: number; display: string; hidden: boolean }[] = []
      const started = performance.now()
      const step = () => {
        for (const el of document.querySelectorAll('.graph-label')) {
          const style = getComputedStyle(el)
          samples.push({ opacity: parseFloat(style.opacity), display: style.display, hidden: (el as HTMLElement).hidden })
        }
        if (performance.now() - started < 1600) requestAnimationFrame(step)
        else Object.assign(window, { __appearSamples: samples })
      }
      requestAnimationFrame(step)
    })
    await labels(page).selectOption('all')
    await page.waitForFunction(() => Array.isArray((window as unknown as { __appearSamples?: unknown[] }).__appearSamples))
    const appearing = await page.evaluate(() => (window as unknown as { __appearSamples: { opacity: number; display: string; hidden: boolean }[] }).__appearSamples)
    expect(appearing.some(sample => !sample.hidden && sample.display !== 'none' && sample.opacity > 0.05 && sample.opacity < 0.92)).toBe(true)
    await expect(canvas(page)).toHaveAttribute('data-labels-ready', 'true', { timeout: 15_000 })
  })
})
