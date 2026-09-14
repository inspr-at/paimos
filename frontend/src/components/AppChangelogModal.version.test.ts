import { afterEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, type App } from 'vue'
import AppChangelogModal from './AppChangelogModal.vue'
vi.mock('@docs/CHANGELOG.md?raw', () => ({
  default:
    '## [260910151030.0.0] — 2026-09-10\nNewer release.\n\n## [260909151030.0.0] — 2026-09-09\nMigration anchor.\n\n## [26.09.09.13.13] — 2026-09-09\nLegacy release.\n',
}))
let app: App | undefined

afterEach(() => {
  app?.unmount()
  app = undefined
  document.body.replaceChildren()
})

it('uses the release migration anchor and preserves changelog navigation controls', async () => {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp({ render: () => h(AppChangelogModal, { open: true }) })
  app.mount(root)
  await nextTick()
  const modern = document.querySelector<HTMLButtonElement>(
    '.cl-row[data-version="260909151030.0.0"]',
  )!
  const legacy = document.querySelector<HTMLButtonElement>(
    '.cl-row[data-version="26.09.09.13.13"]',
  )!
  expect(modern).not.toBeNull()
  expect(legacy).not.toBeNull()
  expect(modern.querySelector('.yy')).not.toBeNull()
  expect(legacy.querySelector('.yy')).toBeNull()
  expect(modern.querySelector('button, [role="button"]')).toBeNull()
  legacy.click()
  await nextTick()
  expect(legacy.getAttribute('aria-selected')).toBe('true')
  expect(document.querySelector('.cl-content h2')!.textContent).toBe('v26.09.09.13.13')
  modern.click()
  await nextTick()
  expect(
    document.querySelector('.cl-content h2 [data-canonical]')!.getAttribute('data-canonical'),
  ).toBe('260909151030.0.0')
})
