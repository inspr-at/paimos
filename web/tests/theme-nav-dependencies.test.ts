// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'
import { openModelPrefs } from '../src/lib/modelPrefsCommand.ts'

// Follow static value imports and re-exports, including the public facade.
// Type-only edges disappear; the editor's lazy registration runs after initialization.
function valueImports(file: URL): string[] {
  const source = ts.createSourceFile(file.pathname, readFileSync(file, 'utf8'), ts.ScriptTarget.Latest, false, ts.ScriptKind.TS)
  return source.statements.flatMap(node => {
    if (!ts.isImportDeclaration(node) && !ts.isExportDeclaration(node)) return []
    if (!node.moduleSpecifier || !ts.isStringLiteral(node.moduleSpecifier)) return []
    if (ts.isImportDeclaration(node)) {
      const clause = node.importClause
      if (clause?.isTypeOnly) return []
      if (clause && !clause.name && clause.namedBindings && ts.isNamedImports(clause.namedBindings) &&
        clause.namedBindings.elements.length && clause.namedBindings.elements.every(item => item.isTypeOnly)) return []
    } else if (node.isTypeOnly || node.exportClause && ts.isNamedExports(node.exportClause) &&
      node.exportClause.elements.length && node.exportClause.elements.every(item => item.isTypeOnly)) return []
    return [node.moduleSpecifier.text]
  })
}

function valueGraph(entry: string): Set<string> {
  const visited = new Set<string>(), pending: string[] = []
  function visit(file: URL) {
    assert.ok(!pending.includes(file.href), `Value-import cycle: ${[...pending, file.href].map(path => new URL(path).pathname.split('/').at(-1)).join(' → ')}`)
    if (visited.has(file.href)) return
    pending.push(file.href)
    for (const path of valueImports(file).filter(path => path.startsWith('.'))) {
      const dependency = new URL(path, file)
      if (!existsSync(dependency)) dependency.pathname += '.ts'
      visit(dependency)
    }
    pending.pop()
    visited.add(file.href)
  }
  visit(new URL(entry, import.meta.url))
  return visited
}

test('theme modules have an acyclic static value-import graph', () => {
  for (const module of ['agentTheme', 'themeRuntime']) valueGraph(`../src/lib/${module}.ts`)
})

test('model preferences command has no transitive dependency on the global router', () => {
  const graph = valueGraph('../src/lib/modelPrefsCommand.ts')
  assert.ok(![...graph].some(file => new URL(file).pathname.endsWith('/router.ts')), 'The caller must supply navigation')
})

test('model preferences navigation uses the supplied callback and preserves context', async () => {
  const links: string[] = [], completion = Promise.resolve('navigated')
  const navigate = (href: string) => { links.push(href); return completion }
  assert.equal(openModelPrefs(navigate), completion)
  assert.equal(links[0], '/settings/models?layer=mine')
  assert.equal(openModelPrefs(navigate, {
    project: { id: 'project & a', title: 'Project A' }, level: 'project', kind: 'backend', ticket: 'AEON-1420', why: true, preview: 'Sol',
  }), completion)
  const link = new URL(links[1]!, 'http://example.invalid')
  assert.equal(link.pathname, '/settings/models')
  assert.deepEqual(Object.fromEntries(link.searchParams), { project_id: 'project & a', layer: 'rules', kind: 'backend', ticket: 'AEON-1420', why: '1' })
  const failure = new Error('navigation failed')
  await assert.rejects(openModelPrefs(() => Promise.reject(failure)), error => error === failure)
})
