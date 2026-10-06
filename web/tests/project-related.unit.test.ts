// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import { expect, it, vi } from 'vitest'

// Execute the view's actual navigation handlers with controlled network and
// history dependencies, without maintaining a duplicate implementation.
const { descriptor } = parse(readFileSync(new URL('../src/views/ProjectView.vue', import.meta.url), 'utf8'))
const source = ts.createSourceFile('ProjectView.ts', descriptor.scriptSetup!.content, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
const names = ['openRelated', 'follow', 'trailBack']
const functions = source.statements.filter(statement => ts.isFunctionDeclaration(statement) && names.includes(statement.name?.text ?? ''))
expect(functions).toHaveLength(names.length)
const printer = ts.createPrinter()
const { outputText } = ts.transpileModule(functions.map(fn => printer.printNode(ts.EmitHint.Unspecified, fn, source)).join('\n'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022 },
})

function harness(oldKey: string, canonicalKey: string, owner: string) {
  const route = { fullPath: '/p/PHAROS/PHAROS-12?q=Oracle&type=ticket', query: { q: 'Oracle', type: 'ticket' } }
  const origin = route.fullPath
  const target = { id: owner, routeKey: owner === 'p-aeon' ? 'AEON' : 'PHAROS' }
  const router = { push: vi.fn(), go: vi.fn(() => { route.fullPath = origin }) }
  const context = {
    route, router, session: { identity: 'person' }, scopeOwner: (identity: string) => identity,
    routeKey: { value: 'PHAROS' }, projectId: { value: 'p-pharos' }, ticketKey: { value: 'PHAROS-12' },
    panelItem: { value: { key: 'PHAROS-12' } }, trail: { value: [] as string[] }, ticketSectionQuery: () => ({}),
    projects: { byId: vi.fn((id: string) => id === owner ? target : undefined) }, list: { rows: { value: [] } },
    ticketRef: vi.fn((key: string) => key === oldKey ? { key: canonicalKey, projectId: owner } : undefined),
    ticketPath: (key: string) => `/p/PHAROS/${key}`, openKey: vi.fn(), newTab: vi.fn(),
    listNodes: vi.fn(async () => ({ items: [] as { key: string; project: { id: string } }[] })),
  }
  const handlers = new Function('context', `const { ${Object.keys(context).join(', ')} } = context; ${outputText}; return { openRelated, trailBack }`)(context)
  return { context, handlers, target }
}

it.each([
  ['PHAROS-14', 'AEON-2', 'p-aeon'],
  ['AEON-1', 'PHAROS-17', 'p-pharos'],
])('a resolved old key %s follows the canonical record %s and retains Back history', async (oldKey, canonicalKey, owner) => {
  const { context, handlers, target } = harness(oldKey, canonicalKey, owner)
  await handlers.openRelated(oldKey)
  expect(context.router.push).toHaveBeenCalledExactlyOnceWith({
    path: `/p/${target.routeKey}/${canonicalKey}`, query: { q: 'Oracle', type: 'ticket' }, state: { trail: ['PHAROS-12'] },
  })
  expect(context.listNodes).not.toHaveBeenCalled()
  context.trail.value = ['PHAROS-12']
  handlers.trailBack()
  expect(context.router.go).toHaveBeenCalledExactlyOnceWith(-1)
  expect(context.route.fullPath).toBe('/p/PHAROS/PHAROS-12?q=Oracle&type=ticket')
})

it.each(['route', 'person'])('a pending related lookup cannot navigate after the %s changes', async changed => {
  const { context, handlers } = harness('unknown', 'unused', 'p-aeon')
  let release!: (value: { items: { key: string; project: { id: string } }[] }) => void
  context.listNodes.mockImplementation(() => new Promise(resolve => { release = resolve }))
  const opening = handlers.openRelated('AEON-99')
  expect(context.listNodes).toHaveBeenCalledExactlyOnceWith({ q: 'AEON-99', limit: 25 })
  if (changed === 'route') context.route.fullPath = '/p/PHAROS/PHAROS-13'
  else context.session.identity = 'another-person'
  release({ items: [{ key: 'AEON-99', project: { id: 'p-aeon' } }] })
  await opening
  expect(context.router.push).not.toHaveBeenCalled()
  expect(context.openKey).not.toHaveBeenCalled()
})

// Keep the real menu handlers and their generation state together so deferred
// responses exercise the view's cancellation, rather than a copy of it.
const bulkFunctions = ['openBulk', 'closeBulk', 'openRelease']
const bulkStatements = source.statements.filter(statement =>
  (ts.isFunctionDeclaration(statement) && bulkFunctions.includes(statement.name?.text ?? '')) ||
  (ts.isVariableStatement(statement) && statement.declarationList.declarations.some(declaration =>
    ts.isIdentifier(declaration.name) && declaration.name.text === 'bulkMenuGeneration')),
)
const bulkCode = ts.transpileModule(bulkStatements.map(statement => printer.printNode(ts.EmitHint.Unspecified, statement, source)).join('\n'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022 },
}).outputText

it.each(['status', 'priority', 'labels', 'move', 'release', 'direct release', 'close'])(
  'a deferred bulk assignee opening cannot replace a later %s action', async action => {
    let resolvePeople!: () => void
    const pendingPeople = new Promise<void>(resolve => { resolvePeople = resolve })
    const anchor = { isConnected: true, focus: vi.fn() }
    const context = {
      bulkMenu: { value: null as { kind: string; anchor: typeof anchor } | null },
      peopleGeneration: 0, selectedRows: { value: [{ id: 'ticket-1', updated_at: 'revision-1' }] },
      loadProjectPeople: vi.fn(() => pendingPeople),
      list: { requestFacet: vi.fn(), loadEpics: vi.fn() },
      project: { value: { id: 'project-1' } }, releaseIds: { value: [] as string[] },
      liveSelection: () => ['ticket-1'], can: () => true,
    }
    const handlers = new Function('context', `const { ${Object.keys(context).join(', ')} } = context; ${bulkCode}; return { openBulk, closeBulk, openRelease }`)(context)
    const opening = handlers.openBulk('assignee', anchor)
    expect(context.loadProjectPeople).toHaveBeenCalledExactlyOnceWith()
    expect(context.bulkMenu.value).toBeNull()

    if (action === 'close') handlers.closeBulk(false)
    else if (action === 'direct release') handlers.openRelease(anchor, ['ticket-1'])
    else await handlers.openBulk(action, anchor)
    const expected = action === 'close' ? null : { kind: action === 'direct release' ? 'release' : action, anchor }
    expect(context.bulkMenu.value).toEqual(expected)

    resolvePeople()
    await opening
    expect(context.bulkMenu.value).toEqual(expected)

    // Cancellation must still allow a fresh, intentional assignee opening.
    await handlers.openBulk('assignee', anchor)
    expect(context.bulkMenu.value).toEqual({ kind: 'assignee', anchor })
  },
)
