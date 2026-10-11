// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1188: the list's display settings have one home, the Display menu. Risks:
// the Filters sheet (or another surface) mounts the display editors again, so a
// setting can be changed in two places; the sheet loses its way to Display; the
// link shows where the Display menu has nothing to offer (Graph without a header).
import { globSync, readFileSync } from 'node:fs'
import { dirname, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import { expect, it, vi } from 'vitest'
import { createSSRApp, ref } from 'vue'
import { renderToString } from '@vue/server-renderer'

vi.mock('../src/lib/prefs', () => ({ useModelDisplay: () => ({ modelDisplay: ref({ effortMeter: true, modelNames: 'full', modelVersion: 'show' }), set: vi.fn() }) }))
vi.mock('../src/lib/useProjectHeader', () => ({ useProjectHeader: () => ({ roomyHeader: ref('compact'), setHeaderDensity: vi.fn() }) }))
import { filtersFromQuery } from '../src/lib/ticketList'
import FilterSheet from '../src/components/work/FilterSheet.vue'
import DisplayPanel from '../src/components/work/DisplayPanel.vue'

const root = fileURLToPath(new URL('../src/', import.meta.url))

const EDITORS = ['DisplayMenu', 'DisplayPanel', 'HeaderRoomyChoice', 'SortEditor', 'ColumnPicker']

// Relative imports (static and dynamic) of every source file, resolved under src/.
// A file that never names an editor cannot import it, so only those are parsed.
function importGraph() {
  const graph = new Map<string, Set<string>>()
  for (const file of globSync('**/*.{vue,ts}', { cwd: root })) {
    let source = readFileSync(resolve(root, file), 'utf8')
    if (!EDITORS.some(name => source.includes(name))) continue
    if (file.endsWith('.vue')) {
      const { descriptor } = parse(source)
      source = [descriptor.script?.content, descriptor.scriptSetup?.content].filter(Boolean).join('\n')
    }
    const ast = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
    const targets = new Set<string>()
    const add = (specifier: string) => { if (specifier.startsWith('.')) targets.add(relative(root, resolve(root, dirname(file), specifier))) }
    const visit = (node: ts.Node) => {
      if (ts.isImportDeclaration(node) && ts.isStringLiteral(node.moduleSpecifier)) add(node.moduleSpecifier.text)
      if (ts.isCallExpression(node) && node.expression.kind === ts.SyntaxKind.ImportKeyword && node.arguments[0] && ts.isStringLiteral(node.arguments[0])) add(node.arguments[0].text)
      ts.forEachChild(node, visit)
    }
    visit(ast)
    graph.set(file, targets)
  }
  return graph
}

it('only the Display menu mounts the display editors', () => {
  const graph = importGraph()
  const importers = (target: string) => [...graph].filter(([, targets]) => targets.has(target)).map(([file]) => file).sort()
  expect(importers('components/work/DisplayMenu.vue')).toEqual(['components/work/ListToolbar.vue'])
  expect(importers('components/work/DisplayPanel.vue')).toEqual(['components/work/ListToolbar.vue'])
  expect(importers('components/work/HeaderRoomyChoice.vue')).toEqual(['components/work/DisplayPanel.vue', 'components/work/ListToolbar.vue'])
  expect(importers('components/work/SortEditor.vue')).toEqual(['components/work/DisplayPanel.vue'])
  // The Projects overview picks the columns of its own project list, not of a ticket list.
  expect(importers('components/work/ColumnPicker.vue')).toEqual(['components/work/DisplayPanel.vue', 'views/ProjectsView.vue'])

  // In the toolbar, each editor sits inside <DisplayMenu>, whichever button opened it.
  type Node = { type: number; tag?: string; children?: Node[] }
  const { descriptor } = parse(readFileSync(resolve(root, 'components/work/ListToolbar.vue'), 'utf8'))
  const placed: string[][] = []
  const walk = (node: Node, trail: string[]) => {
    const here = node.type === 1 && node.tag ? [...trail, node.tag] : trail
    if (node.tag === 'DisplayPanel' || node.tag === 'HeaderRoomyChoice') placed.push(here)
    for (const child of node.children ?? []) walk(child, here)
  }
  walk(descriptor.template!.ast as unknown as Node, [])
  expect(placed.map(trail => trail.at(-1))).toEqual(['DisplayPanel', 'HeaderRoomyChoice'])
  for (const trail of placed) expect(trail, trail.join(' > ')).toContain('DisplayMenu')
})

it('the Filters sheet filters and links to the Display menu instead of holding display settings', async () => {
  const filters = filtersFromQuery({})
  const sheet = (props: Record<string, unknown>) => renderToString(createSSRApp(FilterSheet, { filters, options: () => [], total: 3, ...props }))
  // What the Display menu holds, so the sheet's absence below is not vacuous.
  const settings = ['Group by', 'Row height', 'Add sort key', 'Project header density', 'aria-label="Columns"', 'Effort meter']
  const panel = await renderToString(createSSRApp(DisplayPanel, { filters, view: 'list', density: 'comfortable', projectHeader: true, sheet: true, columns: { order: ['key', 'title', 'status'], visible: ['key', 'title', 'status'], customised: false } }))
  for (const setting of settings) expect(panel, setting).toContain(setting)

  for (const view of ['list', 'outline', 'graph'] as const) {
    const html = await sheet({ view, projectHeader: true })
    expect(html, view).toContain('Display settings…')
    for (const setting of settings) expect(html, `${view}: ${setting}`).not.toContain(setting)
    // Filtering stays: Hide and its choices, the facets once loaded, the result button.
    expect(html).toContain('What Hide hides')
    expect(html).toContain('Show 3 tickets')
  }
  // Graph has no list display options; without the project header its Display menu offers nothing.
  expect(await sheet({ view: 'graph', projectHeader: false })).not.toContain('Display settings…')
  expect(await sheet({ view: 'list', projectHeader: false })).toContain('Display settings…')
})
