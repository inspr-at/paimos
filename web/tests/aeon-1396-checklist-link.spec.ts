// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1396 risk: the checklist's "Move to … anyway…" link renders as a browser button with its
// square default outline, while focused or while its reason form is open, instead of the app's
// focus ring; or its open state moves the link. Fails on 29a066143 (no link styles in the panel).
import { expect, test, type Locator } from '@playwright/test'
import type { EntryChecks } from '../src/lib/entryChecks'
import { fixtures, mockWork } from './work-fixtures'
import { controlStability } from './control-stability'

// The computed look of the link, beside the app ring as the current theme computes it.
function look(link: Locator) {
  return link.evaluate(el => {
    const probe = document.createElement('span'); probe.style.boxShadow = 'var(--focus-ring)'; document.body.append(probe)
    const ring = getComputedStyle(probe).boxShadow; probe.remove()
    const ink = document.createElement('span'); ink.style.color = 'var(--ink)'; document.body.append(ink)
    const inkColor = getComputedStyle(ink).color; ink.remove()
    const css = getComputedStyle(el)
    return { ring, inkColor, outline: css.outlineStyle, outlineWidth: css.outlineWidth, shadow: css.boxShadow, border: css.borderTopStyle, background: css.backgroundColor, color: css.color, weight: css.fontWeight, focused: document.activeElement === el, visible: el.matches(':focus-visible') }
  })
}

test('AEON-1396 the Move anyway link shows the app ring on keyboard focus and no outline with its form open', async ({ page }, testInfo) => {
  const data = fixtures(), node = data.nodes.find(n => n.id === 'n-4')!
  node.state = 'open'; node.fields.acceptance_criteria = '- [ ] The work satisfies the approved criteria'
  await mockWork(page, data)
  const snapshot = (): EntryChecks => ({ node_id: node.id, revision: node.updated_at,
    definition: { id: 'definition', revision: node.created_at, source: 'project', checks: {} },
    blocks: [{ to_status: 'in_progress', passed: 1, total: 2, checks: [
      { id: 'estimate_set', label: 'Estimate set', applicable: true, passed: false, reason: 'Required before this status', can_tick: false },
      { id: 'acceptance_present', label: 'Acceptance criteria written', applicable: true, passed: true, reason: '', can_tick: false },
    ] }], entries: [], latest_refusal: null })
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (/\/entry-checks$/.test(path)) return route.fulfill({ json: path.includes(node.id) ? snapshot() : { ...snapshot(), node_id: path.split('/')[3], definition: null } })
    if (path === `/api/queue/${node.id}/readiness`) return route.fulfill({ json: { queueable: true, ready: false, missing: ['estimate'], suggested_estimate_hours: 3, security_review_required: false } })
    return route.fallback()
  })
  for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
    await page.setViewportSize({ width, height: 1000 }); data.preferences.theme = { choice: theme }
    await page.goto('/p/PHAROS?sort=key&closed=1')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await page.locator('tr.ticket-row').filter({ hasText: 'PHAROS-14' }).locator('.title-text').click()
    const workspace = page.getByRole('complementary', { name: 'Ticket details' })
    await workspace.getByRole('button', { name: 'Ready 1/2' }).click()
    const panel = page.getByRole('dialog', { name: 'PHAROS-14: Ready checks' })
    const link = panel.getByRole('button', { name: 'Move to In progress anyway…' }), reason = panel.getByRole('textbox', { name: 'Why move it now?' })
    await expect(link).toBeVisible()
    const closed = await look(link)
    expect(closed, 'a link, not a browser button').toMatchObject({ border: 'none', background: 'rgba(0, 0, 0, 0)', weight: '600' })

    // A mouse click opens the reason form: the pressed ink at the same weight, no outline, no ring.
    const guard = await controlStability(page, { link })
    await guard.check(async () => { await link.click(); await expect(reason).toBeFocused() })
    guard.done()
    await expect(link).toHaveAttribute('aria-expanded', 'true')
    const open = await look(link)
    expect(open).toMatchObject({ outline: 'none', shadow: 'none', border: 'none', color: open.inkColor, weight: '600' })
    await page.screenshot({ path: testInfo.outputPath(`form-open-${width}-${theme}.png`) })

    // Keyboard focus, with the form still open: exactly the app ring, never the square outline.
    await reason.press('Escape')
    for (let step = 0; step < 20 && !(await link.evaluate(el => document.activeElement === el)); step++) await page.keyboard.press('Shift+Tab')
    const focused = await look(link)
    expect(focused).toMatchObject({ focused: true, visible: true, outline: 'none', shadow: focused.ring })
    expect(focused.ring).not.toBe('none')
    await page.screenshot({ path: testInfo.outputPath(`focus-ring-${width}-${theme}.png`) })
  }
})
