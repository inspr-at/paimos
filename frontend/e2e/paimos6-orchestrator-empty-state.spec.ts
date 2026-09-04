import { expect, test, type Page } from '@playwright/test'
import { mkdir, readFile } from 'node:fs/promises'
import { extname, resolve } from 'node:path'

const SELF_HOST = process.env.PAI893_SELF_HOST_DIST === '1'
const APP_ORIGIN = 'http://pai893.local'
const DIST_DIR = resolve(process.cwd(), 'dist')
const SHOT_DIR = process.env.PAI893_SHOT_DIR ?? '/tmp/pai893-shots'
const PROJECT_ID = 42

test.skip(!SELF_HOST, 'set PAI893_SELF_HOST_DIST=1 to exercise the exact production bundle')
test.beforeAll(async () => {
  if (SELF_HOST) await mkdir(SHOT_DIR, { recursive: true })
})

const emptyHome = {
  schema_version: 2,
  project_id: PROJECT_ID,
  zoom: '10',
  band: 'overview',
  sample_limit: 10,
  sample_truncated: false,
  sessions: [],
  selected_session: null,
  totals: {
    sessions: 0,
    unread: 0,
    attention_sessions: 0,
    exception_messages: 0,
    action_requests: 0,
    exception_targets: 0,
    sampled_exception_targets: 0,
  },
}

const emptyKnowledge = {
  schema_version: 1,
  project_id: PROJECT_ID,
  short_body_limit_bytes: 1200,
  compact_product_session_id: null,
  entries: [],
  legacy: [],
  proposals: [],
}

async function installFixture(page: Page) {
  let agents: Array<{ project_id: number; name: string }> = []
  let orchestrator: { display_label: string } | null = null

  await page.addInitScript(() => {
    class QuietEventSource extends EventTarget {
      static readonly CONNECTING = 0
      static readonly OPEN = 1
      static readonly CLOSED = 2
      readonly url: string
      readonly withCredentials = false
      readyState = QuietEventSource.OPEN

      constructor(url: string | URL) {
        super()
        this.url = String(url)
      }

      close() {
        this.readyState = QuietEventSource.CLOSED
      }
    }
    Object.defineProperty(window, 'EventSource', { configurable: true, value: QuietEventSource })
  })

  await page.route(`${APP_ORIGIN}/**`, async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname
    const fulfill = (json: unknown, status = 200) =>
      route.fulfill({ status, json, headers: { 'X-Permissions-Epoch': '1' } })

    if (path === '/api/auth/me') {
      return fulfill({
        user: {
          id: 7,
          username: 'mba',
          role: 'super_admin',
          status: 'active',
          nickname: 'Markus',
          first_name: 'Markus',
          last_name: 'Barta',
          email: 'm@example.com',
          avatar_path: '',
          locale: 'en',
          timezone: 'auto',
          is_super_admin: true,
        },
        access: { all_projects: true, levels: {} },
        suppress_security_nags: true,
      })
    }
    if (path === '/api/branding') {
      return fulfill({ name: 'PAIMOS', company: 'PAIMOS', product: 'PAIMOS', logo: '/logo.svg' })
    }
    if (path === '/api/instance') {
      return fulfill({ label: 'STAGING', attachments_enabled: true, live_updates_enabled: false })
    }
    if (path === '/api/projects') {
      return fulfill([{ id: PROJECT_ID, key: 'PAI', name: 'Paimos' }])
    }
    if (path === '/api/orchestrator/v1') {
      return fulfill(
        orchestrator === null
          ? { schema_version: 1, revision: 0, orchestrator: null, updated_at: null }
          : {
              schema_version: 1,
              revision: 1,
              orchestrator,
              updated_at: '2026-09-04T12:00:00Z',
            },
      )
    }
    if (path === '/api/health') {
      return fulfill({
        agent_bus_identity_enforced: true,
        agent_bus_instance: 'ppm',
        deployment_instance: 'ppm',
      })
    }
    if (path === `/api/projects/${PROJECT_ID}/agents`) return fulfill(agents)
    if (path === `/api/projects/${PROJECT_ID}/session-home/zoom/v1`) return fulfill(emptyHome)
    if (path === `/api/projects/${PROJECT_ID}/structured-knowledge/v1`) {
      return fulfill(emptyKnowledge)
    }
    if (path.startsWith('/api/')) return fulfill([])

    const relative =
      path.startsWith('/assets/') || path === '/logo.svg' || path === '/favicon.svg'
        ? path.slice(1)
        : 'index.html'
    const file = resolve(DIST_DIR, relative)
    const types: Record<string, string> = {
      '.html': 'text/html',
      '.js': 'text/javascript',
      '.css': 'text/css',
      '.svg': 'image/svg+xml',
      '.woff': 'font/woff',
      '.woff2': 'font/woff2',
    }
    return route.fulfill({
      body: await readFile(file),
      contentType: types[extname(file)] ?? 'application/octet-stream',
    })
  })

  return {
    setAgents(next: Array<{ project_id: number; name: string }>) {
      agents = next
    },
    configureOrchestrator(displayLabel: string) {
      orchestrator = { display_label: displayLabel }
    },
  }
}

test('zero-agent setup stays actionable, contextual, keyboard reachable, and mobile-safe', async ({
  page,
}) => {
  const fixture = await installFixture(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`${APP_ORIGIN}/?project=${PROJECT_ID}`, { waitUntil: 'networkidle' })

  const editor = page.getByRole('link', { name: 'Open agent editor' })
  await expect(editor).toHaveAttribute('href', `/projects/${PROJECT_ID}?tab=agents`)
  await expect(editor).toHaveAttribute('target', '_blank')
  await expect(editor).toHaveAttribute('rel', 'noopener')
  await editor.focus()
  await expect(editor).toBeFocused()
  await page.screenshot({ path: `${SHOT_DIR}/desktop-zero-agent.png`, fullPage: true })

  page.once('popup', (popup) => void popup.close())
  await editor.click()
  await expect(page.getByText(/catalog is now treated as stale/i)).toBeVisible()

  fixture.setAgents([{ project_id: PROJECT_ID, name: 'amy' }])
  await page.getByRole('button', { name: 'Refresh agent choices' }).click()
  await expect(page.getByRole('option', { name: 'amy' })).toBeAttached()
  await expect(page.getByText(/catalog is now treated as stale/i)).toHaveCount(0)

  fixture.setAgents([])
  await page.reload({ waitUntil: 'networkidle' })
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByRole('link', { name: 'Open agent editor' })).toBeVisible()
  const geometry = await page.evaluate(() => {
    const binding = document
      .querySelector<HTMLElement>('.p6-empty-binding')!
      .getBoundingClientRect()
    const actions = [
      ...document.querySelectorAll<HTMLElement>('.p6-empty-binding .p6-setup-actions > *'),
    ]
    return {
      horizontalOverflow:
        document.documentElement.scrollWidth > document.documentElement.clientWidth,
      actionsWithinBinding: actions.every((action) => {
        const rect = action.getBoundingClientRect()
        return rect.left >= binding.left && rect.right <= binding.right
      }),
    }
  })
  expect(geometry).toEqual({ horizontalOverflow: false, actionsWithinBinding: true })
  await page.screenshot({ path: `${SHOT_DIR}/phone-zero-agent.png`, fullPage: true })
})

test('configured empty state reaches the existing talk panel without sending', async ({ page }) => {
  const fixture = await installFixture(page)
  fixture.configureOrchestrator('Amy / Primary')
  await page.goto(`${APP_ORIGIN}/?project=${PROJECT_ID}`, { waitUntil: 'networkidle' })

  await page.getByRole('button', { name: 'Talk to orchestrator' }).click()
  await expect(page.locator('.p6-talk-door')).toBeVisible()
  await expect(page.locator('.p6-talk-door')).toContainText('Amy / Primary')
  await expect(page.locator('.p6-status')).toContainText('Nothing was sent')
  await page.screenshot({ path: `${SHOT_DIR}/desktop-configured-talk.png`, fullPage: true })
})
