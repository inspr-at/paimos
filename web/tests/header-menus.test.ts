// SPDX-License-Identifier: AGPL-3.0-only
// AEON-312: the gear menu's system status and the feedback message it sends.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { HOOK_COMMANDS, feedbackBody, hookDocsUrl, summarizeStatus } from '../src/lib/headerMenus.ts'
import { nextTick, reactive, ref } from 'vue'
import { renderSource, renderNodes, renderText } from './render-source.ts'
import * as menuWords from '../src/lib/headerMenus.ts'

test('system status: words for every state, checking until the probe answers', () => {
  assert.deepEqual(summarizeStatus(null), { state: 'checking', label: 'Checking…', detail: '' })
  assert.equal(summarizeStatus({ health: { status: 'ok', db: 'ok' }, ready: true }).state, 'healthy')
  assert.equal(summarizeStatus({ health: { status: 'ok', db: 'ok' }, ready: true }).label, 'Operational')
  assert.deepEqual(summarizeStatus({ health: null, ready: null }), { state: 'down', label: 'Unreachable', detail: 'The server did not answer.' })
  assert.deepEqual(summarizeStatus({ health: { status: 'ok', db: 'down' }, ready: false }), { state: 'degraded', label: 'Degraded', detail: 'The database is not answering.' })
  assert.deepEqual(summarizeStatus({ health: { status: 'ok', db: 'ok' }, ready: false }), { state: 'degraded', label: 'Degraded', detail: 'The server is not ready for requests.' })
  // Readiness must be confirmed: an unreadable answer is never Operational.
  assert.deepEqual(summarizeStatus({ health: { status: 'ok', db: 'ok' }, ready: null }), { state: 'degraded', label: 'Unavailable', detail: 'Readiness could not be checked.' })
  assert.equal(summarizeStatus({ health: { status: 'ok' }, ready: true }).state, 'degraded')
})

test('feedback says where it was written, then the words', () => {
  assert.equal(feedbackBody('  Lovely.  ', { path: '/p/AEON', version: '260929120000.0.0' }), 'Feedback from the app (on /p/AEON, version 260929120000.0.0)\n\nLovely.')
  assert.equal(feedbackBody('Hi', { path: '', version: '' }), 'Feedback from the app\n\nHi')
  assert.equal(feedbackBody('Hi', { path: '/', version: '' }), 'Feedback from the app (on /)\n\nHi')
})

test('inbox hooks: one user-scope command per harness and the guide section in this repository', () => {
  assert.deepEqual(HOOK_COMMANDS.map(hook => hook.command), ['aeon hook install --harness claude --scope user', 'aeon hook install --harness codex --scope user'])
  assert.equal(hookDocsUrl('inspr-at/aeon'), 'https://github.com/inspr-at/aeon/blob/main/docs/AGENT_INTEGRATION.md#operator-installed-turn-boundary-hooks-aeon-281')
  assert.equal(hookDocsUrl(''), 'https://github.com/inspr-at/paimos/blob/main/docs/AGENT_INTEGRATION.md#operator-installed-turn-boundary-hooks-aeon-281')
  assert.match(hookDocsUrl('evil/x"><script>'), /^https:\/\/github\.com\/inspr-at\/paimos\//)
})

test('P02 rendered app menus react to current authority, person kind and workspace membership', async () => {
  const grants = reactive(new Set(['settings.manage', 'keys.manage', 'inbox.send']))
  const role = ref<string | null>('admin')
  const session = reactive({ identity: { principal: { id: 'alice', kind: 'person' }, tenant: { id: 'tenant' } } as { principal: { id: string; kind: string }; tenant: { id: string } } | null })
  const routes: string[] = []
  const view = renderSource('components/header/AppMenu.vue', {}, {
    'vue-router': { useRoute: () => ({ fullPath: '/' }), useRouter: () => ({ push: (path: string) => routes.push(path) }) },
    '../../lib/api': { api: async () => new Response('{}') },
    '../../lib/codenames': { codenameOf: () => '', releaseAria: () => '', rememberCodename() {} },
    '../../lib/authz': { can: (permission: string) => grants.has(permission), myWorkspaceRole: () => role.value },
    '../../lib/commands': { command: ref(null), consume() {}, run() {} },
    '../../lib/feedback': { feedbackRecipient: async () => null, sendFeedback: async () => {} },
    '../../lib/headerMenus': menuWords,
    '../../lib/releases': { displayHeadline: () => '', hasUsableNotes: () => false, localizedNote: () => ({}) },
    '../../lib/work': { relativeTime: () => '', absoluteTime: () => '' },
    '../../stores/releases': { useReleases: () => ({ newCount: 0, history: null, current: '', load() {} }) },
    '../../stores/session': { useSession: () => session },
    '../../stores/business': { useBusiness: () => ({ placeOpen: false }) },
    '../../stores/version': { useVersion: () => ref(null) },
    '../../lib/doctrineInbox': { doctrineInbox: { pending: 0 } },
  })
  const labels = () => renderNodes(view.root, node => node.props.role === 'menuitem').map(renderText)
  try {
    assert.ok(labels().includes('Workspace settings')); assert.ok(labels().includes('Agent keys')); assert.ok(labels().includes('Connect your machine'))
    const settings = renderNodes(view.root, node => node.tag === 'button' && renderText(node) === 'Workspace settings')[0]!
    await (settings.props.onClick as Function)(); assert.deepEqual(routes, ['/settings/workspace'])
    grants.delete('settings.manage'); grants.delete('keys.manage'); await nextTick()
    assert.ok(!labels().includes('Workspace settings')); assert.ok(!labels().includes('Agent keys')); assert.ok(labels().includes('Connect your machine'))
    role.value = null; await nextTick()
    assert.ok(!labels().includes('Connect your machine'))
    role.value = 'member'; session.identity!.principal.kind = 'agent'; await nextTick()
    assert.ok(!labels().includes('Connect your machine'))
    session.identity = null; await nextTick()
    assert.deepEqual(labels(), [])
  } finally { view.stop() }
})

test('P02 rendered account menus bind the current identity and preserve it when sign-out fails', async () => {
  const identity = (id: string) => ({ principal: { id, kind: 'person', name: id, email: `${id}@example.invalid` }, tenant: { id: 'tenant', name: 'Workspace' } })
  const session = reactive({ identity: identity('alice') as ReturnType<typeof identity> | null, signOut: async () => { throw new Error('offline') } })
  const routes: string[] = [], choice = ref('system'), brand = ref({ wordmark: 'NOVA DAWN' })
  const view = renderSource('components/header/AccountMenu.vue', {}, {
    'vue-router': { useRouter: () => ({ push: (path: string) => routes.push(path), replace: (path: string) => routes.push(path) }) },
    '../../lib/api': { accountName: (value: ReturnType<typeof identity>) => value.principal.name, accountEmail: (value: ReturnType<typeof identity>) => value.principal.email },
    '../../lib/brand': { brand }, '../../lib/chrome': { compactChrome: () => ref(true), narrowestPhone: () => ref(false) },
    '../../lib/codenames': { codenameOf: () => '', releaseAria: () => '' }, '../../lib/commands': { run() {} },
    '../../lib/theme': { dark: ref(false), themeChoice: choice, setTheme: (next: string) => { choice.value = next }, toggleTheme() {} },
    '../../stores/releases': { useReleases: () => ({ newCount: 0, current: '' }) },
    '../../stores/session': { useSession: () => session }, '../../stores/version': { useVersion: () => ref(null) },
  })
  const signOut = () => renderNodes(view.root, node => node.tag === 'button' && renderText(node) === 'Sign out')[0]!
  try {
    assert.match(renderText(view.root), /alice@example.invalid/); assert.match(renderText(view.root), /NOVA DAWN/)
    session.identity = identity('bob'); await nextTick()
    assert.doesNotMatch(renderText(view.root), /alice/); assert.match(renderText(view.root), /bob@example.invalid/)
    await (signOut().props.onClick as Function)(); await nextTick()
    assert.match(renderText(view.root), /Sign out didn’t complete/); assert.equal(session.identity!.principal.id, 'bob'); assert.deepEqual(routes, [])
    session.signOut = async () => { session.identity = null }
    await (signOut().props.onClick as Function)(); await nextTick()
    assert.deepEqual(routes, ['/signin']); assert.equal(renderText(view.root), '')
  } finally { view.stop() }
})
