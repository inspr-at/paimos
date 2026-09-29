// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test'
import { mockEffectivePermissions } from './authz-fixtures'

export const RULE_PROJECT = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
export const RULE_PERSON = '11111111-1111-4111-8111-111111111111'
export const RULE_AGENT = '22222222-2222-4222-8222-222222222222'
const COMPANY = 'cccccccc-cccc-4ccc-8ccc-cccccccccccc'
const PROJECT_LAYER = 'dddddddd-dddd-4ddd-8ddd-dddddddddddd'
const COMPANY_SET = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'
const PROJECT_SET = 'ffffffff-ffff-4fff-8fff-ffffffffffff'

const rule = (identity: string, text: string, strength: 'normal' | 'locked' = 'normal') => ({
  identity, text, why: 'Because the record says so.', details: 'More when asked.', strength, enabled: true,
  roles: ['builder'], harnesses: ['cursor'], source: { reference: 'AEON-252', identity: strength === 'locked' ? identity : '', edited_here: false },
})

export interface RulesMock {
  calls: { method: string; path: string; body?: unknown }[]
  releaseDraftFailure: () => void
}

export async function mockRules(page: Page, options: { kind?: 'person' | 'agent'; conflict?: boolean; publish?: boolean; draftFailAt?: number; draftFailStatus?: number; holdDraftFailure?: boolean; rejectNextDraft?: boolean; setAbortAt?: number; comparisons?: unknown[]; comparisonStatus?: number } = {}): Promise<RulesMock> {
  const calls: RulesMock['calls'] = []
  let releaseDraftFailure = () => {}
  const draftGate = options.holdDraftFailure ? new Promise<void>(resolve => { releaseDraftFailure = () => resolve() }) : null
  let conflicted = false
  let layerPosts = 0
  let setPosts = 0
  let draftPuts = 0
  let rejectDrafts = options.rejectNextDraft ? 1 : 0
  const createdLayers: { id: string; scope: unknown }[] = []
  const createdSets = new Map<string, { id: string; layer_id: string; scope: unknown; name: string; revision: number; rules: unknown[]; published_version: string }>()
  const layerScope = (id: string) => {
    if (id === COMPANY) return { layer: 'company' }
    if (id === PROJECT_LAYER) return { layer: 'project', project_id: RULE_PROJECT }
    return createdLayers.find(layer => layer.id === id)?.scope ?? { layer: 'company' }
  }
  const state = {
    revision: 3,
    companyName: 'Secrets',
    projectName: 'Scope',
    published: '260920100000.0.0',
    projectPublished: '',
    companyRules: [rule('keep-secrets', 'Never print the environment.', 'locked'), rule('record-source', 'Record the source.')],
    projectRules: [rule('keep-secrets', 'Never print the environment.'), rule('package-scope', 'Your package is your scope.')],
  }
  const versions = [{
    set_id: COMPANY_SET, scope: { layer: 'company' }, name: 'Secrets', revision: 2, version: '260920100000.0.0',
    sha256: 'ab'.repeat(32), rules: state.companyRules.map(item => ({ ...item })), published_at: '2026-09-20T10:00:00Z',
  }]
  const sets = () => ({
    [COMPANY_SET]: { id: COMPANY_SET, layer_id: COMPANY, scope: { layer: 'company' }, name: state.companyName, revision: state.revision, rules: state.companyRules, published_version: state.published },
    [PROJECT_SET]: { id: PROJECT_SET, layer_id: PROJECT_LAYER, scope: { layer: 'project', project_id: RULE_PROJECT }, name: state.projectName, revision: state.revision, rules: state.projectRules, published_version: state.projectPublished },
  })
  await page.route('**/api/**', async route => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const method = request.method()
    const body = ['POST', 'PUT', 'PATCH'].includes(method) ? request.postDataJSON() : undefined
    if (path.startsWith('/api/rules') || path === '/api/me/permissions' || path === '/api/projects' || path === '/api/members' || (options.kind === 'agent' && path === '/api/me')) {
      calls.push({ method, path: path + url.search, body })
    }
    if (path === '/api/me' && options.kind === 'agent') {
      return route.fulfill({ json: { principal: { id: RULE_AGENT, name: 'Worker', kind: 'agent' }, tenant: { id: 't1', name: 'INSPR Studio' } } })
    }
    if (path === '/api/me/permissions') {
      const answer = mockEffectivePermissions('admin', url.searchParams.get('project_id') ?? undefined)
      const extra = ['rules.read', 'rules.write', ...(options.publish === false ? [] : ['rules.publish'])]
      answer.workspace.permissions = [...answer.workspace.permissions, ...extra]
      return route.fulfill({ json: answer })
    }
    if (path === '/api/projects' && method === 'GET') {
      return route.fulfill({ json: { items: [{ id: RULE_PROJECT, key: 'AEON', title: 'Aeon', state: 'active', open: 1, in_progress: 0, done: 0, cancelled: 0, total: 1, last_activity: '2026-09-28T10:00:00Z', people: [] }] } })
    }
    if (path === '/api/members' && method === 'GET') {
      return route.fulfill({ json: {
        people: [{ principal_id: RULE_PERSON, name: 'Markus Barta', avatar_url: null, has_avatar: false, email: 'markus@barta.com', status: 'active', identity: 'inspr_id', workspace_role: null, project_roles: [], aliases: [], classic_role: null, last_active_at: null, last_owner: true }],
        agents: [{ principal_id: RULE_AGENT, name: 'Worker', has_avatar: false, workspace_role: null, key_count: 1, last_seen_at: null, service: false }],
        invites: [], imported: [], owner_count: 1,
      } })
    }
    if (path === '/api/rules/layers' && method === 'GET') {
      return route.fulfill({ json: { layers: [
        { id: COMPANY, scope: { layer: 'company' } },
        { id: PROJECT_LAYER, scope: { layer: 'project', project_id: RULE_PROJECT } },
        ...createdLayers,
      ] } })
    }
    if (path === '/api/rules/layers' && method === 'POST') {
      layerPosts += 1
      const layer = { id: `b1b1b1b1-b1b1-41b1-81b1-${layerPosts.toString(16).padStart(12, '0')}`, scope: body }
      createdLayers.push(layer)
      return route.fulfill({ status: 201, json: layer })
    }
    if (path === '/api/rules/sets' && method === 'GET') {
      const layer = url.searchParams.get('layer_id')
      const all = sets()
      const items = [...Object.values(all), ...createdSets.values()].filter(set => set.layer_id === layer)
      return route.fulfill({ json: { sets: items } })
    }
    if (path === '/api/rules/sets' && method === 'POST') {
      setPosts += 1
      if (options.setAbortAt && setPosts === options.setAbortAt) return route.abort('failed')
      const created = { id: `c1c1c1c1-c1c1-41c1-81c1-${setPosts.toString(16).padStart(12, '0')}`, layer_id: body.layer_id as string, scope: layerScope(body.layer_id as string), name: body.name as string, revision: 1, rules: [] as unknown[], published_version: '' }
      createdSets.set(created.id, created)
      return route.fulfill({ status: 201, json: created })
    }
    const setPath = /^\/api\/rules\/sets\/([^/]+)$/.exec(path)
    if (setPath && method === 'GET') return route.fulfill({ json: sets()[setPath[1]] ?? createdSets.get(setPath[1]) ?? { error: 'missing' } })
    const draft = /^\/api\/rules\/sets\/([^/]+)\/draft$/.exec(path)
    if (draft && method === 'PUT') {
      if (rejectDrafts > 0) {
        rejectDrafts -= 1
        if (draftGate) await draftGate
        return route.fulfill({ status: options.draftFailStatus ?? 500, json: { error: 'The draft was not saved.', code: 'unavailable' } })
      }
      const created = createdSets.get(draft[1])
      if (created) {
        draftPuts += 1
        if (options.draftFailAt && draftPuts === options.draftFailAt) {
          if (draftGate) await draftGate
          return route.fulfill({ status: options.draftFailStatus ?? 500, json: { error: 'The draft was not saved.', code: 'unavailable' } })
        }
        created.revision += 1
        created.name = body.name
        created.rules = body.rules
        return route.fulfill({ json: created })
      }
      if (options.conflict && !conflicted) {
        conflicted = true
        state.revision = 4
        return route.fulfill({ status: 409, json: { error: 'revision conflict', code: 'revision_conflict' } })
      }
      state.revision += 1
      if (draft[1] === COMPANY_SET) { state.companyName = body.name; state.companyRules = body.rules }
      if (draft[1] === PROJECT_SET) { state.projectName = body.name; state.projectRules = body.rules }
      return route.fulfill({ json: sets()[draft[1]] })
    }
    const publish = /^\/api\/rules\/sets\/([^/]+)\/publish$/.exec(path)
    if (publish && method === 'POST') {
      state.revision += 1
      const note = typeof body.note === 'string' ? body.note.trim() : ''
      const snapshot = { set_id: publish[1], scope: { layer: 'company' }, name: state.companyName, revision: state.revision, version: body.version, sha256: 'cd'.repeat(32), rules: sets()[publish[1]]?.rules ?? [], published_at: '2026-09-28T11:00:00Z', ...(note ? { note } : {}) }
      versions.unshift(snapshot)
      if (publish[1] === COMPANY_SET) state.published = body.version
      return route.fulfill({ json: snapshot })
    }
    const restore = /^\/api\/rules\/sets\/([^/]+)\/restore$/.exec(path)
    if (restore && method === 'POST') {
      state.revision += 1
      const prior = versions.find(version => version.version === body.version)
      const note = typeof body.note === 'string' ? body.note.trim() : ''
      const snapshot = { set_id: restore[1], scope: { layer: 'company' }, name: prior?.name ?? 'Secrets', revision: state.revision, version: body.new_version, sha256: 'ef'.repeat(32), rules: prior?.rules ?? [], published_at: '2026-09-28T11:05:00Z', ...(note ? { note } : {}) }
      versions.unshift(snapshot)
      if (restore[1] === COMPANY_SET) { state.companyRules = snapshot.rules.map(item => ({ ...item })); state.published = body.new_version }
      return route.fulfill({ json: snapshot })
    }
    const one = /^\/api\/rules\/sets\/([^/]+)\/versions\/([^/]+)$/.exec(path)
    if (one && method === 'GET') {
      const found = one[1] === COMPANY_SET ? versions.find(version => version.version === decodeURIComponent(one[2])) : undefined
      return found ? route.fulfill({ json: found }) : route.fulfill({ status: 404, json: { error: 'unavailable', code: 'not_found' } })
    }
    if (path === '/api/rules/publish' && method === 'POST') {
      if (options.kind === 'agent') return route.fulfill({ status: 403, json: { error: 'permission or scoped ownership denied', code: 'forbidden' } })
      const note = typeof body.note === 'string' ? body.note.trim() : ''
      const out = (body.items as { set_id: string; version: string }[]).map(item => {
        const version = item.version === 'auto' ? '260928213000.0.0' : item.version
        const set = sets()[item.set_id]
        const snapshot = { set_id: item.set_id, scope: set?.scope ?? { layer: 'company' }, name: set?.name ?? '', revision: state.revision, version, sha256: 'cd'.repeat(32), rules: set?.rules ?? [], published_at: '2026-09-28T21:30:00Z', ...(note ? { note } : {}) }
        if (item.set_id === COMPANY_SET) { versions.unshift(snapshot); state.published = version }
        if (item.set_id === PROJECT_SET) state.projectPublished = version
        return snapshot
      })
      return route.fulfill({ json: { batch_id: '8a0f0c3e-5d1b-8e2a-9c4f-0b1d2e3f4a5b', versions: out, max_bytes: 120 } })
    }
    const history = /^\/api\/rules\/sets\/([^/]+)\/versions$/.exec(path)
    if (history && method === 'GET') return route.fulfill({ json: { versions: history[1] === COMPANY_SET ? versions : [] } })
    if (path === '/api/rules/comparisons' && method === 'GET') {
      if (options.comparisonStatus) return route.fulfill({ status: options.comparisonStatus, json: { error: 'unavailable', code: 'not_found' } })
      return route.fulfill({ json: { comparisons: options.comparisons ?? [] } })
    }
    if (path === '/api/rules/merged' && method === 'GET') {
      return route.fulfill({ json: {
        context: { tenant_id: 't1', project_id: url.searchParams.get('project_id'), person_id: url.searchParams.get('person_id'), role: url.searchParams.get('role'), harness: url.searchParams.get('harness') },
        versions: [{ set_id: COMPANY_SET, version: '260920100000.0.0', sha256: 'ab'.repeat(32) }],
        version: '260920100000.0.0', sha256: 'ab'.repeat(32), body: '# Rules\n\n- [keep-secrets] Never print the environment.', byte_size: 58, rules: [], floor: 'locked company floor', valid_until: null,
      } })
    }
    return route.fallback()
  })
  return { calls, releaseDraftFailure }
}
