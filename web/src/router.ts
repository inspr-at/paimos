// SPDX-License-Identifier: AGPL-3.0-only
import { defineComponent } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import { setPageTitle } from './lib/brand'
import { useProjects } from './stores/projects'
import { useSession } from './stores/session'
import { sessionEnded } from './lib/api'
import { can, ensurePermissions, permissionsRevoked } from './lib/authz'
import { toast } from './lib/toast'
import { announceAttachCode, attachCodeFromHash, dropAttachCode, hasAttachFragment, holdAttachCode } from './lib/attachLink'
import { scopeOwner } from './lib/identityScope'
import { expiredSignIn, takeSignInReturn } from './lib/signInReturn'
import ProjectsView from './views/ProjectsView.vue'
import SignInView from './views/SignInView.vue'
import NotFoundView from './views/NotFoundView.vue'
import { DOCK_MEDIA, isKnowledgeType, parseEntryParam } from './lib/knowledge'
import { projectSection } from './components/work/projectNavigation'

// Child records of the project page carry only the address; ProjectView renders
// what they name, so they need a component that draws nothing.
const RouteMarker = defineComponent({ name: 'RouteMarker', render: () => null })

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: ProjectsView, meta: { title: 'Projects' } },
    { path: '/briefing', component: () => import('./views/MorningBriefingView.vue'), meta: { title: 'Morning briefing' } },
    // One record for the project page: its list, the open ticket and its Knowledge
    // tab and entries are children, so moving between them never remounts the page
    // (and its guards stay on the record that is matched throughout).
    {
      path: '/p/:projectKey', component: () => import('./views/ProjectView.vue'), meta: { title: 'Project' },
      children: [
        { path: 'tickets', component: RouteMarker, meta: { projectSection: 'tickets' } },
        { path: 'journey', component: RouteMarker, meta: { title: 'Journey', projectSection: 'journey' } },
        // A docked entry (?entry=<type>/<slug>) on a screen too narrow to dock it opens the entry's own page.
        { path: 'knowledge', component: RouteMarker, meta: { title: 'Knowledge', projectSection: 'knowledge' }, beforeEnter: to => {
          const entry = parseEntryParam(to.query.entry)
          // Narrow screens open a docked link as the entry's page; the graph shows its selection itself.
          if (!entry || to.query.view === 'graph' || to.query.mode === 'graph' || window.matchMedia(DOCK_MEDIA).matches) return true
          const { entry: _entry, ...query } = to.query
          return { path: `/p/${encodeURIComponent(String(to.params.projectKey))}/knowledge/${entry.type}/${encodeURIComponent(entry.slug)}`, query, hash: to.hash, replace: true }
        } },
        // One kind: the tab filtered to it.
        { path: 'knowledge/:knowledgeType', redirect: to => ({ path: `/p/${encodeURIComponent(String(to.params.projectKey))}/knowledge`, query: { ...to.query, ...(isKnowledgeType(to.params.knowledgeType) ? { type: to.params.knowledgeType } : {}) }, hash: to.hash, replace: true }) },
        { path: 'knowledge/:knowledgeType/:slug', component: RouteMarker, meta: { title: 'Knowledge', projectSection: 'knowledge' },
          beforeEnter: to => isKnowledgeType(to.params.knowledgeType) ? true : { path: `/p/${encodeURIComponent(String(to.params.projectKey))}/knowledge`, replace: true } },
        { path: ':ticketKey?', component: RouteMarker },
      ],
    },
    // Knowledge across every project: search runbooks, guidelines, memory and more.
    { path: '/knowledge', component: () => import('./views/KnowledgeView.vue'), meta: { title: 'Knowledge' } },
    // The earlier workspace tree and list are gone; the projects page replaces them.
    { path: '/workspace', redirect: '/' },
    // Earlier journey links lead to the project's Journey section.
    {
      path: '/projects/:projectId/:rest(.*)*', component: NotFoundView, meta: { title: 'Journey' },
      beforeEnter: async to => {
        const projects = useProjects()
        await projects.load()
        const project = projects.byId(String(to.params.projectId))
        const rest = Array.isArray(to.params.rest) ? to.params.rest : []
        const stage = rest[0] === 'journey' && rest[1] ? { stage: rest[1] } : {}
        return project ? { path: `/p/${encodeURIComponent(project.routeKey)}/journey`, query: { ...to.query, ...stage }, hash: to.hash, replace: true } : true
      },
    },
    // Business: Overview · Customers · Quotes · Hours · Rates.
    { path: '/business', component: () => import('./views/business/BusinessHome.vue'), meta: { title: 'Business' } },
    { path: '/business/customers', component: () => import('./views/business/CustomersView.vue'), meta: { title: 'Customers' } },
    { path: '/business/customers/:id', component: () => import('./views/business/CustomerView.vue'), meta: { title: 'Customer' } },
    // One record for the list and the quote docked beside it (?quote=), so opening one never remounts the list.
    { path: '/business/quotes', component: () => import('./views/business/QuotesView.vue'), meta: { title: 'Quotes' } },
    { path: '/business/quotes/:quoteId', component: () => import('./views/business/QuoteEditorView.vue'), props: true, meta: { title: 'Quote editor', fill: true, foldHeader: true },
      beforeEnter: to => /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(String(to.params.quoteId)) ? true : '/business/quotes' },
    { path: '/business/hours', component: () => import('./views/business/HoursView.vue'), meta: { title: 'Hours' } },
    { path: '/business/rates', component: () => import('./views/business/CostUnitsView.vue'), meta: { title: 'Rates' } },
    { path: '/business/costs', redirect: '/business/rates' },
    { path: '/business/cost-units', redirect: '/business/rates' },
    { path: '/business/quotes/:quoteId/:rest(.*)+', redirect: '/business/quotes' },
    { path: '/business/:parked(organisations|crm)/:rest(.*)*', redirect: '/business/customers' },
    { path: '/crm', redirect: '/business/customers' },
    { path: '/decision-desk', component: () => import('./views/DecisionDeskView.vue'), meta: { title: 'Decision Desk', fill: false } },
    { path: '/agents/usage', component: () => import('./views/UsageDashboardView.vue'), meta: { title: 'Usage' } },
    // Before :sessionId, or that param captures the public guide. Anonymous readers stay on this route.
    { path: '/agents/register-agent', component: () => import('./views/RegisterAgentView.vue'), meta: { title: 'Connect your machine', public: true } },
    // One record for the overview and its open session, so opening the panel never remounts the page.
    { path: '/agents/:sessionId?', component: () => import('./views/AgentsView.vue'), meta: { title: 'Agents', fill: false } },
    // Earlier separate pages now live inside Agents.
    // eslint-disable-next-line no-restricted-syntax -- a route of the page, not a request
    { path: '/runs/:runId?', redirect: '/agents' },
    { path: '/approvals', redirect: '/agents' },
    { path: '/phone-approvals/:kind(approval|attach)/:requestId', component: () => import('./views/PhoneApprovalView.vue'), meta: { title: 'Review approval' } },
    { path: '/pacing', redirect: '/agents' },
    // The release history is a sheet over the page (App.vue); its own links open it over Projects.
    { path: '/releases/:version?', component: ProjectsView, meta: { title: 'Releases' } },
    // Settings: Personal for everyone; Workspace, Business and Projects for admins.
    { path: '/settings', redirect: '/settings/personal' },
    { path: '/settings/business/profiles/:profileId?', component: () => import('./views/settings/DocumentProfilesView.vue'), props: true, meta: { title: 'Document profiles', fill: true } },
    { path: '/settings/:section(personal|developer|agent-rules|accounts|workspace|business|projects|portal)', component: () => import('./views/SettingsView.vue'), meta: { title: 'Settings' } },
    // Access: /settings/access/<tab>/<id> (a person, a role, a project).
    { path: '/settings/:section(access)/:tab(people|invites|roles|projects|agents|audit)?/:id?', component: () => import('./views/SettingsView.vue'), meta: { title: 'Access', keepsFocus: true } },
    { path: '/link', component: () => import('./views/LinkAccountView.vue'), meta: { title: 'Link an account' } },
    { path: '/signin', component: SignInView, meta: { title: 'Sign in', bare: true } },
    { path: '/from-classic/:rest(.*)*', component: () => import('./views/FromClassicView.vue'), meta: { title: 'Finding your page' } },
    { path: '/offers/:publicTenant/:token', component: () => import('./public/PublicQuoteView.vue'), props: true, meta: { title: 'Customer quote', bare: true, public: true } },
    { path: '/portal/:tenantSlug/releases', component: () => import('./public/PublicReleasesView.vue'), props: true, meta: { title: 'Releases', bare: true, public: true } },
    { path: '/portal/:tenantSlug/roadmap', component: () => import('./public/PublicRoadmapView.vue'), props: true, meta: { title: "What's coming", bare: true, public: true } },
    { path: '/portal/:tenantSlug', component: () => import('./public/PublicPortalView.vue'), props: true, meta: { title: 'Product portal', bare: true, public: true } },
    // quote-print.html is the separate Vite entry, served directly from webFS.
    { path: '/:pathMatch(.*)*', component: NotFoundView, meta: { title: 'Page not found' } },
  ],
})

sessionEnded.handler = path => {
  const session = useSession()
  const alreadyEnded = session.requiresSignIn
  session.invalidate()
  // The current view may hold a draft or a secret shown only once. Keep it
  // mounted; the next protected navigation is handled by the guard below.
  if (!alreadyEnded && path !== '/me') toast('Your session has ended', { tone: 'error' })
}

// Overlapping checks would race each other and the later failure could clear a
// session the earlier one had just confirmed.
let refreshing: Promise<void> | null = null
function refreshSession() {
  if (!refreshing) refreshing = useSession().refresh().finally(() => { refreshing = null })
  return refreshing
}
// A code held for a session that is gone is dropped, and the return path never carries it.
function signInAgain(fullPath: string) { dropAttachCode(); return expiredSignIn(fullPath) }
router.beforeEach(async (to, from) => {
  // The terminal's attach link carries a one-time code in its fragment. It leaves the
  // address bar before any other rule runs, so no redirect, return path or report can
  // copy it; the page that opens the review picks it up from memory (AEON-440).
  if (hasAttachFragment(to.hash)) {
    const code = attachCodeFromHash(to.hash)
    if (code && to.path === '/agents') holdAttachCode(code, scopeOwner(useSession().identity))
    return { path: to.path, query: to.query, hash: '', replace: true }
  }
  // Canonical section URLs replace bookmarks without adding a history step.
  // Ticket addresses stay /p/KEY/TICKET; ?section= preserves a non-default background,
  // including across reload, expand/collapse and links inside the side panel.
  if (to.params.projectKey) {
    const query = { ...to.query }
    let section = projectSection(to)
    let path = to.path
    if (!to.meta.projectSection) {
      if (query.view === 'journey' || query.view === 'knowledge') {
        section = query.view
        delete query.view
      }
      if (!to.params.ticketKey) {
        path = `/p/${encodeURIComponent(String(to.params.projectKey))}/${section}`
        delete query.section
      } else if (section !== 'tickets') query.section = section
      else delete query.section
    }
    if (section === 'knowledge' && query.mode !== undefined) {
      if (query.view !== 'graph' && query.view !== 'entries' && query.mode === 'graph') query.view = 'graph'
      delete query.mode
    }
    if (path !== to.path || JSON.stringify(query) !== JSON.stringify(to.query)) return { path, query, hash: to.hash, replace: true }
  }
  if (to.meta.public) return true
  const session = useSession()
  // A 401 or sign-out is authoritative for this tab until an explicit sign-in.
  // A later /me response must not silently reauthorize a revoked page.
  if (session.requiresSignIn) return to.path === '/signin' ? true : signInAgain(to.fullPath)
  const wasSignedIn = !!session.identity
  await refreshSession()
  if (session.error) return true // The shell shows a retry screen, never protected content.
  // Losing a session without signing out means it expired; say so on the sign-in page.
  if (!session.identity && to.path !== '/signin') {
    // A classic link arrives before sign-in (AEON-175): keep it for after OIDC.
    if (to.path.startsWith('/from-classic/')) sessionStorage.setItem('aeon.fromClassicReturn', to.fullPath)
    if (to.path.startsWith('/phone-approvals/')) return { path: '/signin', query: { return: to.fullPath } }
    if (wasSignedIn) return signInAgain(to.fullPath)
    dropAttachCode()
    return '/signin'
  }
  if (session.identity && to.path === '/signin') return '/'
  // OIDC returns to / after sign-in. Restore only a same-origin resolver route
  // saved by this tab, or the return path of an expired session.
  if (session.identity && to.path === '/') {
    const pending = sessionStorage.getItem('aeon.fromClassicReturn')
    if (pending) {
      sessionStorage.removeItem('aeon.fromClassicReturn')
      if (/^\/from-classic\/(?!\/)/.test(pending)) return pending
    }
    if (!from.matched.length) {
      const returnPath = takeSignInReturn()
      if (returnPath !== '/') return returnPath
    }
  }
  // ?new=1 opens the new-agent sheet only for a person who may create one.
  // Strip it here, once identity and permissions have settled: a later replace
  // from the page is cancelled by the permission refresh this guard starts.
  // A failed answer is not a denial, so the deep link stays until we know.
  if (to.params.section === 'access' && to.params.tab === 'agents' && to.query.new === '1' && session.identity) {
    const settled = await ensurePermissions()
    const mayCreate = session.identity.principal.kind === 'person' && can('keys.manage') && can('members.read')
    if ((settled === 'known' || permissionsRevoked()) && !mayCreate) {
      const query = { ...to.query }
      delete query.new
      return { path: to.path, query, hash: to.hash, replace: true }
    }
  }
})
// A held attach code is offered once the navigation that cleaned the address bar has settled.
router.afterEach((to, _from, failure) => { if (!failure && to.path === '/agents') announceAttachCode() })
// A new page names the tab; a query change (filters, the release sheet) keeps the page's own title.
router.afterEach((to, from) => { if (to.path !== from.path || !from.matched.length) setPageTitle(String(to.meta.title ?? '')) })
