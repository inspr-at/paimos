// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1187 risks: a retired file still has a production consumer, a glob-loaded
// indicator gets mistaken for dead UI, or contextual links lose their real owner.
import { existsSync, globSync, readFileSync, statSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import { afterEach, expect, it, vi } from 'vitest'
import { ref, type App } from 'vue'
import { createMemoryHistory, createRouter, type Router, type RouteRecordRaw } from 'vue-router'
import { sourceModule } from './record-source'
import { mountView, textOf } from './webcore-view-harness'
import * as projectSettings from '../src/lib/projectSettings'

const root = fileURLToPath(new URL('../src/', import.meta.url))
const removed = [
  'components/agents/CapacityCard.vue', 'components/agents/ApprovalQueue.vue',
  'components/ExpandableText.vue', 'components/agents/approvalSettle.ts',
  'components/agents/LiveNow.vue', 'components/agents/LiveDot.vue', 'components/HelloWorld.vue',
  'views/business/CRMView.vue', 'components/business/OrgPanel.vue',
  'components/business/QuoteStatus.vue', 'components/business/QuoteLines.vue',
  'views/business/QuoteDocumentView.vue', 'views/business/BusinessArriving.vue',
  'lib/quoteDraft.ts', 'components/business/index.ts',
  'components/quotes/collaboration/QuoteCollaborationBar.vue',
  'components/rules/DoctrineInbox.vue', 'components/work/ListFreshness.vue',
  'lib/useModelPrefsEditor.ts', 'lib/modelPrefsApi.ts',
]

// Reproduce the AEON-1138 inventory: trace both app/print roots through static,
// dynamic and barrel imports, including LiveBot's import.meta.glob. Tests are
// deliberately outside this graph. Type-only dependencies and assets are kept.
function productionGraph() {
  const reached = new Set<string>(), pending = ['main.ts', 'print/main.ts'].map(path => resolve(root, path))
  const enqueue = (owner: string, specifier: string) => {
    if (!specifier.startsWith('.')) return
    const base = resolve(dirname(owner), specifier.split('?')[0]!)
    const target = [base, ...['.ts', '.js', '.mjs', '.vue', '/index.ts', '/index.js'].map(suffix => base + suffix)]
      .find(path => existsSync(path) && statSync(path).isFile())
    expect(target, `${owner} imports missing ${specifier}`).toBeDefined()
    pending.push(target!)
  }
  while (pending.length) {
    const path = pending.pop()!
    if (reached.has(path)) continue
    reached.add(path)
    if (!/\.(?:vue|ts|js|mjs)$/.test(path)) continue
    let source = readFileSync(path, 'utf8')
    if (path.endsWith('.vue')) {
      const { descriptor } = parse(source)
      source = [descriptor.script?.content, descriptor.scriptSetup?.content].filter(Boolean).join('\n')
    }
    const ast = ts.createSourceFile(path, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
    const visit = (node: ts.Node) => {
      if ((ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) && node.moduleSpecifier && ts.isStringLiteral(node.moduleSpecifier)) enqueue(path, node.moduleSpecifier.text)
      if (ts.isCallExpression(node) && node.arguments[0] && ts.isStringLiteral(node.arguments[0])) {
        if (node.expression.kind === ts.SyntaxKind.ImportKeyword) enqueue(path, node.arguments[0].text)
        if (node.expression.getText(ast) === 'import.meta.glob') {
          const matches = globSync(node.arguments[0].text, { cwd: dirname(path) })
          expect(matches.length, `${path}: production glob is not empty`).toBeGreaterThan(0)
          pending.push(...matches.map(match => resolve(dirname(path), match)))
        }
      }
      ts.forEachChild(node, visit)
    }
    visit(ast)
  }
  return reached
}

it('both production roots have no imports of the named retired UI and retain live glob and collaboration dependencies', () => {
  const graph = productionGraph()
  for (const path of removed) {
    expect(graph.has(resolve(root, path)), path).toBe(false)
    expect(existsSync(resolve(root, path)), `${path} was retired`).toBe(false)
  }
  const indicators = globSync('components/indicators/*.vue', { cwd: root })
  expect(indicators.length).toBeGreaterThan(0)
  for (const path of [...indicators, 'lib/quotePresence.ts', 'lib/quoteSession.ts', 'components/rules/DoctrineProposalDialog.vue']) expect(graph.has(resolve(root, path)), path).toBe(true)
  expect(existsSync(resolve(root, 'lib/ledger.check.ts'))).toBe(true)
})

const apps: App[] = []
afterEach(() => { for (const app of apps.splice(0)) app.unmount(); vi.unstubAllGlobals() })

it('approval evidence opens the canonical Decision Desk route', () => {
  const view = mountView('../src/components/agents/SessionStateEvidence.vue', {
    '../../lib/agentAppearance': { useAgentAppearance: () => ({ choice: ref({}) }) },
    '../../lib/agentState': {}, '../../lib/work': {},
    '../../lib/agentSignals': { heartbeatEvidence: () => ({}) },
  }, { view: { session: {}, status: { state: 'working', reasons: [{ code: 'approval', detail: 'Permission needed', next: 'Review the request' }] } }, now: 0 })
  apps.push(view.app)
  const link = view.find(el => el.tag === 'a' && textOf(el).includes('Permission requests'))
  expect(link).toBeDefined()
  expect(link.props.to).toBe('/decision-desk')
  expect(routeRecords().resolve(String(link.props.to)).meta.title).toBe('Decision Desk')
})

it('Check computers opens the real Computers settings section without the retired anchor', async () => {
  const router = routeRecords(), push = vi.fn((path: string) => router.push(path))
  const view = mountView('../src/components/lead/LeadBand.vue', {
    '../../lib/projectSettings': projectSettings,
    'vue-router': { useRouter: () => ({ push }) },
    '../../lib/authz': { can: () => true },
    '../../lib/lead': { LEAD_WORDS: { l: 'lead' }, canLaunchLead: () => true },
    '../../lib/leadCardFold': {
      useLeadCardFold: () => ({ ready: ref(true), collapsed: ref(false), moving: ref(false) }),
      useLeadPanelCollapse: () => ({ ready: ref(true), collapsed: ref(false), moving: ref(false), toggle() {}, settle() {} }),
    },
    '../../lib/leadOverlay': {}, '../../lib/toast': {},
    '../../lib/usePolledData': { usePoller: () => ({ start() {}, stop() {} }) },
    '../../lib/useLeadSummary': { useLeadSummary: () => ({
      lead: ref({ state: 'waiting_for_room', session_id: 'session' }),
      band: ref({ state: 'waiting_for_room', action: 'computers', actionLabel: 'Check computers', title: 'Project lead' }),
      leadSession: ref(null), workers: ref([]), stations: ref([]), questions: ref([]), questionsPartial: ref(false),
    }) },
    '../../stores/projectLeads': { useProjectLeads: () => ({ views: {}, busy: {}, load() {} }) },
    '../../stores/session': { useSession: () => ({ identity: { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } } }) },
    '../../stores/workQueue': { useWorkQueue: () => ({ load() {} }) },
  }, { projectId: 'project', projectKey: 'AEON', routeKey: 'AEON' })
  apps.push(view.app)
  const action = view.find(el => el.props['data-act'] === 'main')
  expect(textOf(action)).toContain('Check computers')
  expect(action.props['data-tip']).toBe('Computers in Settings')
  await (action.props.onClick as (event: unknown) => void)({ currentTarget: action })
  expect(push).toHaveBeenCalledExactlyOnceWith('/settings/computers')
  await push.mock.results[0]!.value
  expect(router.currentRoute.value.params.section).toBe('computers')
  expect(router.currentRoute.value.meta.title).toBe('Settings')
  expect(router.currentRoute.value.hash).toBe('')
})

function routeRecords(): Router {
  const empty = { render: () => null }
  const views = (routes: RouteRecordRaw[]): RouteRecordRaw[] => routes.map(route => ({ ...route, ...(route.component ? { component: empty } : {}), ...(route.children ? { children: views(route.children) } : {}) })) as RouteRecordRaw[]
  return sourceModule<{ router: Router }>('router.ts', {
    './lib/projectSettings': sourceModule('lib/projectSettings.ts', {}),
    'vue-router': { createWebHistory: createMemoryHistory, createRouter: (options: Parameters<typeof createRouter>[0]) => createRouter({ ...options, routes: views(options.routes) }) },
    './lib/brand': { setPageTitle() {} }, './stores/projects': {},
    './stores/session': { useSession: () => ({ identity: { principal: { kind: 'person' } }, refresh: async () => {} }) },
    './stores/workVocabulary': { useWorkVocabulary: () => ({ load: async () => {} }) },
    './lib/api': { sessionEnded: {} }, './lib/authz': {}, './lib/toast': {},
    './lib/attachLink': { hasAttachFragment: () => false, announceAttachCode() {} }, './lib/identityScope': {}, './lib/signInReturn': { takeSignInReturn: () => '/' },
    './lib/knowledge': {}, './components/work/projectNavigation': {}, './lib/ticketPeek': {},
    './views/ProjectsView.vue': { default: empty }, './views/SignInView.vue': { default: empty }, './views/NotFoundView.vue': { default: empty },
  }).router
}

it('retired Business and compatibility URLs still resolve to their canonical owners, and external account links remain', async () => {
  vi.stubGlobal('sessionStorage', { getItem: () => null, removeItem() {} })
  const router = routeRecords()
  for (const [old, current] of [
    ['/crm', '/business/customers'],
    ['/business/organisations/old-customer', '/business/customers'],
    ['/business/crm/old-customer', '/business/customers'],
    ['/business/quotes/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/document', '/business/quotes'],
    ['/business/costs', '/business/rates'], ['/business/cost-units', '/business/rates'],
    ['/workspace', '/'], ['/briefing', '/agents'], ['/runs/old-run', '/agents'],
    ['/approvals?item=a:old-request', '/decision-desk?item=a:old-request'], ['/pacing', '/agents'],
    ['/settings/models/board', '/settings/models'],
    ['/settings/projects?source=old#work-vocabulary', '/settings/vocabulary?source=old#work-vocabulary'],
    ['/settings/accounts?computer=old-computer', '/settings/computers?computer=old-computer'],
    ['/settings/accounts?verify_account=old-account', '/settings/computers?verify_account=old-account'],
    ['/settings/accounts#capacity-and-load', '/settings/capacity#when'],
  ]) {
    await router.push('/settings/personal')
    await router.push(old!)
    expect(router.currentRoute.value.fullPath, old).toBe(current)
    expect(router.currentRoute.value.meta.title, old).not.toBe('Not found')
  }
  expect(router.resolve('/link').meta.title).toBe('Link an account')
})
