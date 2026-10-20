// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Locator, type Page } from '@playwright/test'

// AEON-1559: Hide's form follows the navigation row. Callers sample the control
// that is actually shown, and keep its checked or pressed state.
export async function navigationHide(page: Page): Promise<{ step: number; control: Locator; kind: 'switch' | 'pill' | 'menu' }> {
  const settings = page.locator('#project-view-settings .view-settings')
  const step = Number(await settings.getAttribute('data-navigation-step') ?? '0')
  if (step >= 4) return { step, control: page.locator('.menu-switch input'), kind: 'menu' }
  if (step >= 2) return { step, control: settings.locator('.closed-pill'), kind: 'pill' }
  return { step, control: settings.locator('.closed-switch input'), kind: 'switch' }
}

export async function expectHideOn(page: Page, hidden: boolean) {
  const { control, kind } = await navigationHide(page)
  if (kind === 'pill') await expect(control).toHaveAttribute('aria-pressed', String(hidden))
  else await expect(control).toBeChecked({ checked: hidden })
}
