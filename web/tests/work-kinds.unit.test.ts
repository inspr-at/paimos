// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { computed, defineComponent, onScopeDispose, reactive, watch } from 'vue'
import { createMemoryHistory, createRouter, type RouteRecordRaw, type Router } from 'vue-router'
import { parse as parseSFC } from '@vue/compiler-sfc'
import { NodeTypes, parse as parseTemplate, type AttributeNode, type DirectiveNode, type ElementNode, type TemplateChildNode } from '@vue/compiler-dom'
import { createScope, scopeOwner } from '../src/lib/identityScope'
import * as kinds from '../src/lib/workKinds'
import { textForKinds } from '../src/lib/workKindsCopy'
import { deferred, flush, setupSource, sourceModule, sourceText } from './record-source'

const kind = (id = 'design', extra: Partial<kinds.WorkKind> = {}): kinds.WorkKind => ({ id, slug: id, label: id, hint: 'A plain sentence.', position: 0, examples: ['An example'], labels: ['ui'], ticket_count: 2, ...extra })
const limits: kinds.SituationLimits = { small_hours: 2, fix_rounds: 3, revision: 7, set_by: null, set_at: null }
const words = { label: 'Design', hint: 'Approved screens.', examples: ['A screen'], labels: ['ui'] }
const stops: (() => void)[] = []
afterEach(() => { for (const stop of stops.splice(0)) stop(); vi.unstubAllGlobals() })
function setup(overrides: Partial<typeof kinds> = {}) {
  const session = reactive({ identity: { tenant: { id: 'workspace' }, principal: { id: 'person-a', kind: 'person' } } })
  const grants = reactive({ read: true, write: true })
  const api = { ...kinds, listWorkKinds: vi.fn(async () => ({ items: [kind(), kind('security', { system: 'security', position: 1 }), kind('other', { system: 'other', position: 2 })], truncated: false })), getSituationLimits: vi.fn(async () => ({ ...limits })), updateKind: vi.fn(async () => kind('design', words)), archiveKind: vi.fn(async () => kind('design', { archived_at: '2026-10-07T13:00:00Z' })), putSituationLimits: vi.fn(async () => ({ ...limits, small_hours: 4, revision: 8 })), ...overrides }
  const toast = vi.fn()
  const component = setupSource('components/settings/KindsOfWorkSection.vue', {}, {
    '../../lib/authz': { can: (permission: string) => permission === 'models.read' ? grants.read : grants.write },
    '../../stores/session': { useSession: () => session }, '../../stores/profile': { useProfile: () => ({ profile: null }) },
    '../../lib/identityScope': { scopeOwner }, '../../lib/useIdentityScope': { useIdentityScope: (requires: () => boolean) => {
      const owner = computed(() => requires() ? scopeOwner(session.identity) : '')
      const scope = createScope(() => owner.value)
      const stop = watch(owner, () => scope.reset(), { flush: 'sync' }); onScopeDispose(() => { stop(); scope.dispose() })
      return { ...scope, owner }
    } }, '../../lib/toast': { toast, dismiss: vi.fn() }, '../../lib/workKindsCopy': { textForKinds }, '../../lib/workKinds': api,
  })
  stops.push(component.stop)
  return { state: component.state, api, session, grants, toast }
}

it('bounds kind wording without silently dropping extra examples or duplicate labels', () => {
  expect(kinds.parseKindWords({ label: ' Design ', hint: ' Approved. ', examples: ' A\nB\nC ', labels: 'ui, ux' })).toEqual({ label: 'Design', hint: 'Approved.', examples: ['A', 'B', 'C'], labels: ['ui', 'ux'] })
  for (const extra of [{ examples: 'a\nb\nc\nd' }, { labels: 'ui,ui' }, { label: 'x'.repeat(41) }, { hint: '' }, { examples: 'x'.repeat(121) }]) expect(kinds.parseKindWords({ label: 'Design', hint: 'Approved.', examples: '', labels: '', ...extra })).toBeNull()
  expect(kinds.validLimit(1.5, 8)).toBe(false); expect(kinds.validLimit(9, 8)).toBe(false)
})
it('pins Everything else last, excludes review and preserves the hidden review row in an order write', () => {
  const items = [kind('other', { system: 'other', position: -1 }), kind('review', { system: 'review' }), kind(), kind('backend', { position: 1 })]
  expect(kinds.activeKinds(items).map(kind => kind.slug)).toEqual(['design', 'backend', 'other'])
  expect(kinds.movedKinds(items, 'design', 1)).toEqual(['backend', 'design', 'other', 'review'])
  expect(kinds.movedKinds(items, 'other', -1)).toBeNull(); expect(kinds.movedKinds(items, 'backend', 1)).toBeNull()
})
it('paginates with explicit bounds and reports truncation instead of a complete list', async () => {
  const api = vi.fn(async (path: string) => {
    const cursor = Number(new URL(path, 'http://localhost').searchParams.get('cursor') ?? 0)
    return Response.json({ items: [kind(`kind-${cursor}`)], next_cursor: String(cursor + 1) })
  })
  const client = sourceModule<typeof kinds>('lib/workKinds.ts', { './api.ts': { api, APIError: Error } })
  const result = await client.listWorkKinds(new AbortController().signal)
  expect(api).toHaveBeenCalledTimes(16); expect(result.truncated).toBe(true); expect(result.items).toHaveLength(16)
  expect(api.mock.calls[1]![0]).toContain('cursor=1')
})
it('drops a pending kind response and its toast when the person changes', async () => {
  const pending = deferred<kinds.WorkKind>(), update = vi.fn(() => pending.promise)
  const { state, session, toast } = setup({ updateKind: update })
  await flush(); state.edit(state.kinds.value[0], {}); const writing = state.save(words)
  expect(update).toHaveBeenCalledOnce()
  session.identity.principal.id = 'person-b'; await flush()
  pending.resolve(kind('design', words)); await writing; await flush()
  expect(state.editor.value).toBeNull(); expect(state.kinds.value[0].label).toBe('design'); expect(toast).not.toHaveBeenCalled()
})
it('failed kind writes keep the editor and saved words; revoked permissions cannot write or archive system kinds', async () => {
  const update = vi.fn(async () => { throw new Error('storage failed') })
  const { state, grants, api, toast } = setup({ updateKind: update })
  await flush(); state.edit(state.kinds.value[0], {}); state.save(words); await flush()
  expect(state.editorError.value).toContain('could not be saved'); expect(state.editor.value).not.toBeNull(); expect(state.kinds.value[0].label).toBe('design'); expect(toast).not.toHaveBeenCalled()
  state.askArchive(state.kinds.value[1], {}); expect(state.confirmation.value).toBeNull()
  grants.write = false; state.save(words); state.askArchive(state.kinds.value[0], {}); await flush()
  expect(update).toHaveBeenCalledOnce(); expect(api.archiveKind).not.toHaveBeenCalled()
})
it('Undo cannot send an inverse write after identity changes during its freshness read', async () => {
  const pending = deferred<{ items: kinds.WorkKind[]; truncated: boolean }>()
  const { state, api, session, toast } = setup()
  await flush(); state.edit(state.kinds.value[0], {}); state.save(words); await flush()
  const undo = toast.mock.calls[0]![1].action.run
  api.listWorkKinds.mockImplementationOnce(() => pending.promise)
  undo(); await flush(); session.identity.principal.id = 'person-b'; await flush()
  pending.resolve({ items: [kind('design', words)], truncated: false }); await flush()
  expect(api.updateKind).toHaveBeenCalledOnce()
  expect(toast).toHaveBeenCalledTimes(1)
})
it('failed limit writes retain the last server values and stale revisions are explained', async () => {
  const { state, toast, api } = setup({ putSituationLimits: vi.fn(async () => { throw Object.assign(new Error('stale_revision'), { status: 409 }) }) })
  await flush(); state.setLimit('small_hours', 4); await flush()
  expect(api.putSituationLimits).toHaveBeenCalledWith({ small_hours: 4, fix_rounds: 3, revision: 7 }, expect.any(AbortSignal))
  expect(state.limits.value).toEqual(limits); expect(state.limitsError.value).toContain('changed elsewhere'); expect(toast).not.toHaveBeenCalled()
})

it('Undo refuses to replace kind words changed elsewhere after the original save', async () => {
  const { state, api, toast } = setup()
  await flush(); state.edit(state.kinds.value[0], {}); state.save(words); await flush()
  api.listWorkKinds.mockResolvedValueOnce({ items: [kind('design', { ...words, hint: 'Changed by another administrator.' })], truncated: false })
  toast.mock.calls[0]![1].action.run(); await flush()
  expect(api.updateKind).toHaveBeenCalledOnce()
  expect(toast.mock.calls.at(-1)![0]).toContain('changed elsewhere')
  expect(toast.mock.calls.at(-1)![1]).toEqual({ tone: 'error' })
})

it('system kind wording is editable while its immutable ticket area is preserved', async () => {
  const update = vi.fn(async () => kind('security', { ...words, system: 'security' }))
  const { state } = setup({ updateKind: update })
  await flush(); state.edit(state.kinds.value[1], {}); state.save(words); await flush()
  expect(update).toHaveBeenCalledWith('security', words, expect.any(AbortSignal))
  expect(state.kinds.value.find((item: kinds.WorkKind) => item.id === 'security')?.slug).toBe('security')
})

function literalTarget(source: string): string {
  const match = /^(['"])([\s\S]*)\1$/.exec(source.trim())
  if (!match) throw new Error(`Navigation target is not a literal: ${source}`)
  return match[2]
}
function navigationTarget(prop: AttributeNode | DirectiveNode, tag: string): string | null {
  if (prop.type === NodeTypes.ATTRIBUTE) {
    if ((prop.name !== 'to' && prop.name !== 'href') || !prop.value) return prop.name === 'to' || prop.name === 'href' ? '' : null
    return prop.value.content
  }
  if (prop.name !== 'bind' || prop.arg?.type !== NodeTypes.SIMPLE_EXPRESSION || (prop.arg.content !== 'to' && prop.arg.content !== 'href')) return null
  if (prop.exp?.type !== NodeTypes.SIMPLE_EXPRESSION) throw new Error(`${tag} binds ${prop.arg.content} dynamically`)
  return literalTarget(prop.exp.content)
}
function navigationTargets(source: string): string[] {
  const parsed = parseSFC(source)
  if (parsed.errors.length || !parsed.descriptor.template) throw new Error('Kinds of work template did not parse')
  const targets: string[] = []
  const visit = (nodes: TemplateChildNode[]) => {
    for (const node of nodes) {
      if (node.type === NodeTypes.ELEMENT) {
        const element: ElementNode = node
        if (element.tag === 'RouterLink' || element.tag === 'router-link' || element.tag === 'a') {
          for (const prop of element.props) {
            const target = navigationTarget(prop, element.tag)
            if (target !== null) targets.push(target)
          }
        }
        visit(element.children)
      } else if (node.type === NodeTypes.IF) for (const branch of node.branches) visit(branch.children)
      else if (node.type === NodeTypes.FOR) visit(node.children)
    }
  }
  visit(parseTemplate(parsed.descriptor.template.content).children)
  return targets
}
function settingsRouter(): Router {
  vi.stubGlobal('sessionStorage', { getItem: () => null, removeItem() {}, setItem() {} })
  const empty = defineComponent({ render: () => null })
  const session = { identity: { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } }, requiresSignIn: false, error: '', refresh: vi.fn(async () => {}) }
  const views = (routes: RouteRecordRaw[]): RouteRecordRaw[] => routes.map(route => ({ ...route, ...(route.component ? { component: empty } : {}), ...(route.children ? { children: views(route.children) } : {}) })) as RouteRecordRaw[]
  return sourceModule<{ router: Router }>('router.ts', {
    'vue-router': { createWebHistory: createMemoryHistory, createRouter: (options: Parameters<typeof createRouter>[0]) => createRouter({ ...options, routes: views(options.routes) }) },
    './lib/brand': { setPageTitle() {} }, './stores/projects': {}, './stores/session': { useSession: () => session }, './stores/workVocabulary': { useWorkVocabulary: () => ({ load: vi.fn(async () => {}) }) },
    './lib/api': { sessionEnded: {} }, './lib/authz': {}, './lib/toast': {},
    './lib/attachLink': { hasAttachFragment: () => false, announceAttachCode() {} }, './lib/identityScope': {}, './lib/signInReturn': {},
    './lib/knowledge': {}, './components/work/projectNavigation': {}, './lib/ticketPeek': {},
    './views/ProjectsView.vue': { default: empty }, './views/SignInView.vue': { default: empty }, './views/NotFoundView.vue': { default: empty },
  }).router
}

it('kinds of work does not link to an unregistered Models board', async () => {
  const router = settingsRouter()
  await router.push('/settings/models')
  expect(router.currentRoute.value.meta.title).toBe('Page not found')
  const source = sourceText('components/settings/KindsOfWorkSection.vue')
  expect(source).toContain('anchor="k-kinds"')
  expect(navigationTargets(source)).toEqual([])
  await router.push('/settings/workspace#models')
  expect(router.currentRoute.value.fullPath).toBe('/settings/agents#models')
  expect(textForKinds(false)('lead')).toContain('Settings › Models')
  expect(textForKinds(true)('lead')).toContain('Einstellungen › Modelle')
})

it('bounds response bytes before JSON decoding and rejects repeated pagination cursors', async () => {
  const api = vi.fn(async () => new Response('x'.repeat(1024 * 1024 + 1)))
  const client = sourceModule<typeof kinds>('lib/workKinds.ts', { './api.ts': { api, APIError: Error } })
  await expect(client.listWorkKinds(new AbortController().signal)).rejects.toThrow('Response is too large')
  api.mockImplementation(async () => Response.json({ items: [kind()], next_cursor: 'same-cursor' }))
  await expect(client.listWorkKinds(new AbortController().signal)).rejects.toThrow('Invalid kinds cursor')
  expect(api).toHaveBeenCalledTimes(3)
})
