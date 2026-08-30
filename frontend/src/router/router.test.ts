import { describe, expect, it } from 'vitest'

import router, { buildRoutes } from './index'

describe('router meta', () => {
  // PAI-274 / PAI-361: IssueList table relies on AppLayout's
  // `.main-content--self-scroll` variant to keep the sticky thead +
  // frozen columns working. If this meta drifts on any route that
  // embeds IssueList, the regression returns silently — past fixes
  // were repeatedly papered over without a check, hence this guard.
  // Extend the array below whenever a new route gains an
  // internally-scrolling list (search results, custom views, …).
  const SELF_SCROLL_ROUTES = ['/issues', '/projects/:id']

  it.each(SELF_SCROLL_ROUTES)(
    'sets scrollMode=self on %s so AppLayout flex-bounds .main-content',
    (path) => {
      const r = router.getRoutes().find((r) => r.path === path)
      expect(r, `expected ${path} route to be registered`).toBeDefined()
      expect(r!.meta.scrollMode).toBe('self')
    },
  )

  it('leaves scrollMode unset on default page-scroll views (Settings, Customers, IssueDetail)', () => {
    // IssueDetail uses IssueList in `compact` mode (overflow:hidden, no
    // internal scroll), so its sticky thead inherits .main-content's
    // page-scroll context — opt-in is unnecessary and would clip content.
    for (const path of [
      '/settings',
      '/customers',
      '/projects/:id/issues/:issueId',
      '/issues/:issueId',
    ]) {
      const r = router.getRoutes().find((rt) => rt.path === path)
      expect(r?.meta.scrollMode, `${path} should be page-scroll`).toBeUndefined()
    }
  })

  it('registers direct issue detail deeplinks', () => {
    const r = router.getRoutes().find((rt) => rt.path === '/issues/:issueId')
    expect(r, 'expected /issues/:issueId route to be registered').toBeDefined()
  })
})

// PAI-805: Agent Mode renders in a reduced shell selected from route
// meta. The contract must hold in production (no DEV reference routes)
// and must not leak onto any other route.
describe('router shell contract (PAI-805)', () => {
  it('gives /agent-mode the agent shell and nothing else', () => {
    const r = router.getRoutes().find((rt) => rt.path === '/agent-mode')
    expect(r, 'expected /agent-mode to be registered').toBeDefined()
    expect(r!.meta.shell).toBe('agent')
    expect(r!.meta.public).toBeUndefined()
    expect(r!.meta.portal).toBeUndefined()
  })

  it('leaves every other route on the standard shell (parity)', () => {
    for (const r of router.getRoutes()) {
      if (r.path === '/agent-mode' || r.path === '/dev/agent-mode' || r.path === '/dev/paimos-6') continue
      expect(r.meta.shell, `${r.path} must not opt into a reduced shell`).toBeUndefined()
    }
  })

  it('registers the fixture reference only in DEV builds; production carries no /dev/* routes', () => {
    const prod = buildRoutes(false)
    expect(prod.some((r) => r.path.startsWith('/dev/'))).toBe(false)
    expect(prod.find((r) => r.path === '/agent-mode')?.meta?.shell).toBe('agent')
    const dev = buildRoutes(true)
    const ref = dev.find((r) => r.path === '/dev/agent-mode')
    expect(ref).toBeDefined()
    expect(ref!.meta?.shell).toBe('agent')
  })

  it('keeps the Paimos 6 preview development-only and preserves the exact Dashboard root', () => {
    const prod = buildRoutes(false)
    expect(prod.find((r) => r.path === '/dev/paimos-6')).toBeUndefined()
    expect(prod.find((r) => r.path === '/legacy')).toBeUndefined()

    const dashboard = prod.find((r) => r.path === '/')
    expect(dashboard).toBeDefined()
    expect(dashboard!.redirect).toBeUndefined()
    expect(String(dashboard!.component)).toContain('DashboardView.vue')

    const preview = buildRoutes(true).find((r) => r.path === '/dev/paimos-6')
    expect(preview).toBeDefined()
    expect(preview!.meta?.shell).toBe('v6')
    expect(String(preview!.component)).toContain('Paimos6PreviewView.vue')
  })
})
