import { test, expect, type Page } from '@playwright/test'

// PAI-709 (epic PAI-703) — Voice Intake workbench smoke: dev text mode.
// Type a transcript paragraph, edit the spec, pin a project, create the
// issue, and follow the created-issue link. AI is unconfigured in the e2e
// stack by design — this exercises the degraded-mode guarantee that
// capture → spec → create works without a provider.
const TOKEN = process.env.PAIMOS_DEV_LOGIN_TOKEN ?? ''

// page.request shares the browser context's cookie jar and goes through the
// vite proxy (same-origin), so dev-login lands exactly like the SPA's own.
async function devLogin(page: Page, username = 'dev_admin'): Promise<void> {
  const res = await page.request.post('/api/auth/dev-login', {
    data: { username, token: TOKEN },
  })
  expect(res.ok(), `dev-login failed: ${res.status()}`).toBeTruthy()
}

test.beforeAll(() => {
  expect(TOKEN, 'PAIMOS_DEV_LOGIN_TOKEN must be set for E2E').not.toBe('')
})

test('voice intake: transcript → spec → detect project → create issue', async ({ page }) => {
  await devLogin(page)
  await page.goto('/intake')
  await expect(page.getByRole('heading', { name: 'Live Specification' })).toBeVisible()

  // Start talking (dev text mode) and send a transcript chunk.
  await page.getByRole('button', { name: /Start Talking/ }).click()
  const input = page.getByPlaceholder(/Type or paste what you would say/)
  await expect(input).toBeVisible()
  await input.fill('We need a CSV export button on the reporting page so controlling can pull monthly numbers.')
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  // The right-column cards are collapsed by default (PAI-715) — the
  // summary carries the transcript tail; expand for the full text.
  await page.getByRole('heading', { name: 'Transcript' }).click()
  await expect(page.locator('.vi-transcript')).toContainText('CSV export button')

  // Manual spec edit (degraded mode: no AI in the e2e stack).
  await page.getByRole('tab', { name: 'Edit' }).click()
  const editor = page.locator('.vi-spec-editor')
  await editor.fill('# Reporting CSV export\n\n## Summary\nAdd a CSV export to the reporting page.')
  await editor.blur()

  // Checkpoint the state.
  await page.getByRole('button', { name: /Save checkpoint/ }).click()
  await page.getByRole('button', { name: 'Save', exact: true }).click()

  // No pin needed: deterministic project detection (Stage A, works in
  // degraded mode) sets the target from the transcript's lexical matches —
  // the Create button enables once the project_match event lands.

  // Create the issue and follow the post-create panel.
  const createBtn = page.getByRole('button', { name: /Create Issue/ })
  await expect(createBtn).toBeEnabled({ timeout: 10_000 })
  await createBtn.click()
  const created = page.locator('.vi-postcreate h2')
  await expect(created).toContainText('created', { timeout: 15_000 })
  const keyText = await created.textContent()
  const key = keyText?.match(/([A-Z0-9]+-\d+)/)?.[1]
  expect(key, `issue key in "${keyText}"`).toBeTruthy()

  // Jump to the created issue.
  await page.getByRole('button', { name: new RegExp(`Open ${key}`) }).click()
  await expect(page).toHaveURL(/\/issues\//)
  await expect(page.locator('body')).toContainText('Reporting CSV export')
})
