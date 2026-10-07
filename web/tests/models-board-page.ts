// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Page } from '@playwright/test'
import { boardCards, boardFixture, boardPerson, boardPin } from './models-board-fixtures'
import type { ModelRule, OrderBody } from '../src/lib/modelsBoard'

export async function mockBoard(page: Page, options: { manage?: boolean; fail?: number; german?: boolean; noRoute?: boolean; fallback?: boolean } = {}) {
  let document = boardFixture(options.german), rules = [structuredClone(boardPin)], ruleRevision = 2
  const writes: { path: string; body: Record<string, unknown>; person?: string; method: string }[] = []
  const reads: string[] = []
  let fail = options.fail, hold: (() => Promise<void>) | undefined
  const original = structuredClone(document)
  const orders = new Map<string, OrderBody>()
  await page.route('**/api/**', async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname.replace(/^\/api/, '')
    if (options.fallback && !path.startsWith('/model-preferences/') && !path.startsWith('/model-rules')) return route.fallback()
    if (path === '/me/permissions') return route.fulfill({ json: { workspace: { id: 'board-tenant', role: 'member', permissions: ['models.read', ...(options.manage === false ? [] : ['model_prefs.manage', 'work_kinds.manage'])] }, project: url.searchParams.get('project_id') ? { id: url.searchParams.get('project_id'), role: 'member', permissions: [] } : null } })
    if (path === '/projects') return route.fulfill({ json: { items: [{ id: 'project-a', title: 'AEON', state: 'active', archived: false }], next_cursor: null } })
    if (request.method() === 'GET') {
      if (path === '/model-rules') return route.fulfill({ json: { revision: ruleRevision, rules } })
      if (path === '/model-preferences/board') {
        reads.push(url.search)
        const view = structuredClone(document), project = url.searchParams.get('project_id'), layer = url.searchParams.get('layer') ?? 'mine'
        view.layer = layer as typeof view.layer
        if (layer === 'default') view.profile = { ...view.profile, scope: 'workspace', person_id: null, template: 'balanced', thinking: 'standard', usage: 'balanced' }
        const applicable = rules.filter(rule => rule.scope === 'workspace' || rule.project_id === project)
        for (const column of view.columns) {
          const cards = [...boardCards, ...document.tray].map(card => column.column.startsWith('review:') ? { ...card, effort: 'xhigh' } : card)
          const bans = column.not.filter(card => card.lock?.kind === 'cross_family')
          const own = orders.get(column.column)
          const rank = own?.rank ?? original.columns.find(value => value.column === column.column)!.list.map(card => card.line)
          const free = rank.filter(line => !applicable.some(rule => rule.column === column.column && rule.line === line) && !bans.some(card => card.line === line) && !column.cant.some(item => item.line === line)).map(line => ({ ...cards.find(card => card.line === line)! }))
          const pins = (lock: string) => applicable.filter(rule => rule.column === column.column && rule.lock === lock).sort((a, b) => Number(b.scope === 'project') - Number(a.scope === 'project') || a.position - b.position).map(rule => ({ ...cards.find(card => card.line === rule.line)!, lock: { kind: 'rule' as const, value: rule.lock, who: rule.set_by, why: rule.why, at: rule.set_at, scope: rule.scope } }))
          column.top = pins('top'); column.bottom = pins('bottom'); column.free = free
          column.list = [...column.top, ...free, ...column.bottom].filter(card => !column.cant.some(item => item.line === card.line))
          column.not = [...bans, ...pins('not'), ...(own?.not ?? []).filter(line => !bans.some(card => card.line === line) && !applicable.some(rule => rule.column === column.column && rule.line === line)).map(line => ({ ...cards.find(card => card.line === line)! }))]
          if (own) column.source = 'own'
          column.hidden = view.profile.hidden_kinds.includes(column.column)
          if (options.noRoute) { column.not = column.list.map(card => ({ ...card, lock: { kind: 'residency' as const, value: 'not' as const, who: null, why: 'No qualifying route', at: null, scope: 'workspace' } })).concat(column.not); column.list = [] }
        }
        if (options.noRoute) view.residency.effective = 'eu'
        return route.fulfill({ json: view })
      }
      return route.fulfill({ json: { items: [] } })
    }
    const body = request.postDataJSON() ?? {}, person = request.headers()['if-prefs-person']
    writes.push({ path, body, person, method: request.method() })
    if (url.searchParams.get('for') === 'me' && person !== boardPerson) return route.fulfill({ status: 428, json: { error: 'person_precondition_required' } })
    if (hold) await hold()
    if (fail) return route.fulfill({ status: fail, json: { error: fail === 409 ? 'stale_revision' : 'looser_than_workspace' } })
    if (path.startsWith('/model-rules/')) {
      const scope = path.split('/')[2] as 'workspace' | 'project', column = decodeURIComponent(path.split('/')[3]!)
      const desired = [...body.top.map((pin: { line: string; why: string }, position: number) => ({ ...pin, lock: 'top', position })), ...body.bottom.map((pin: { line: string; why: string }, position: number) => ({ ...pin, lock: 'bottom', position })), ...Object.entries(body.not).map(([line, why]) => ({ line, why, lock: 'not', position: 0 }))]
      if (scope === 'project' && rules.filter(rule => rule.scope === 'workspace' && rule.column === column).some(rule => !desired.some(pin => pin.line === rule.line && (pin.lock === rule.lock || pin.lock === 'not')))) return route.fulfill({ status: 422, json: { error: 'looser_than_workspace' } })
      rules = rules.filter(rule => rule.scope !== scope || rule.column !== column).concat(desired.filter(pin => scope !== 'project' || !rules.some(rule => rule.scope === 'workspace' && rule.column === column && rule.line === pin.line && rule.lock === pin.lock)).map(pin => ({ ...pin, column, scope, project_id: scope === 'project' ? url.searchParams.get('project_id') : null, set_by: 'Markus', set_at: '2026-10-07T13:00:00Z' }) as ModelRule))
      return route.fulfill({ json: { rules, revision: ++ruleRevision } })
    }
    if (url.searchParams.get('dry_run') === 'true') return route.fulfill({ json: { person_id: boardPerson, revision: document.revision, profile: document.profile, dry_run: true, moved: [{ column: 'backend', before: ['openai:sol'], after: ['openai:astra'] }] } })
    if (path.startsWith('/model-preferences/orders/')) {
      const column = decodeURIComponent(path.split('/')[3]!)
      if (request.method() === 'DELETE') orders.delete(column)
      else orders.set(column, { rank: body.rank, not: body.not })
    } else if (path === '/model-preferences/profile') {
      for (const field of ['residency', 'hidden_kinds', 'dismissed_lines', 'template', 'thinking', 'usage']) if (field in body) Object.assign(document.profile, { [field]: body[field] })
      document.residency.own = document.profile.residency; document.residency.effective = document.profile.residency ?? 'any'
    } else if (path.includes('/tray/')) document.profile.dismissed_lines.push(decodeURIComponent(path.split('/')[3]!))
    document.revision++; document.profile.revision = document.revision
    return route.fulfill({ json: { person_id: boardPerson, revision: document.revision, profile: document.profile, dry_run: false, moved: [] } })
  })
  return { writes, reads, getDocument: () => structuredClone(document), setFail: (status?: number) => { fail = status }, holdNext: () => {
    let release!: () => void; const barrier = new Promise<void>(resolve => { release = resolve }); hold = () => barrier
    return () => { hold = undefined; release() }
  }, addProjectRule: (rule: ModelRule) => { rules.push(rule) }, clearTray: () => { document.tray = [] } }
}
export async function open(page: Page, query = '') { await page.goto(`/tests/models-board-harness.html${query}`); await expect(page.locator('[data-board-ready="true"]')).toBeVisible() }
export const col = (page: Page, column = 'backend') => page.locator(`[data-column="${column}"]`)
export const card = (page: Page, line: string, column = 'backend') => col(page, column).locator(`[data-line="${line}"]`)
export const headerControls = (page: Page) => ({ head: page.locator('.bhead'), show: page.locator('[data-board-show]'), project: page.locator('[data-board-project]'), providers: page.locator('[data-board-providers]'), full: page.locator('[data-models-fullscreen]') })
export async function layer(page: Page, name: string) { await page.locator('[data-board-show]').click(); await page.getByRole('menuitem', { name, exact: true }).click(); await expect(page.locator('[data-board-ready="true"]')).toBeVisible() }
