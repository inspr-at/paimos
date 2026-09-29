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

export interface RulesMockOptions {
  kind?: 'person' | 'agent'; conflict?: boolean; publish?: boolean; draftFailAt?: number; draftFailStatus?: number; holdDraftFailure?: boolean; rejectNextDraft?: boolean; setAbortAt?: number; comparisons?: unknown[]; comparisonStatus?: number
  /** AEON-314: sets and rules carry explanations for people. */
  tldr?: boolean
  /** AEON-314: the caller may manage workspace settings (the budget). */
  settings?: boolean
  /** AEON-314: the workspace budget the server reports. */
  budget?: { max_bytes: number; layer_max_bytes: Record<string, number> }
  /** AEON-315: every named-agent preview is refused as not_key_creator. */
  denyNamedPreview?: boolean
  /** AEON-315: the named agents the members listing returns. */
  agents?: { principal_id: string; name: string; preview?: { allowed: boolean; reason?: string; creator_name?: string } }[]
}

export async function mockRules(page: Page, options: RulesMockOptions = {}): Promise<RulesMock> {
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
    companyTldr: null as null | { en: string; de?: string; basis?: string; check?: boolean },
    projectTldr: null as null | { en: string; de?: string; basis?: string; check?: boolean },
    budget: options.budget ?? { max_bytes: 12000, layer_max_bytes: {} as Record<string, number> },
  }
  if (options.tldr) {
    state.companyTldr = { en: 'Keeps credentials and private data out of every transcript and log.', de: 'Hält Zugangsdaten aus Protokollen heraus.', basis: '0123456789abcdef' }
    state.projectTldr = { en: 'How work in Aeon is scoped and recorded.', basis: '0123456789abcdef', check: true }
    Object.assign(state.companyRules[0]!, { tldr: { en: 'Environment dumps put secrets into logs and chat; agents never print them.', de: 'Keine Umgebungsvariablen ausgeben.', basis: '0123456789abcdef' } })
    Object.assign(state.projectRules[1]!, { tldr: { en: 'A worker changes only the files its package needs.', basis: '0123456789abcdef', check: true } })
  }
  const versions = [{
    set_id: COMPANY_SET, scope: { layer: 'company' }, name: 'Secrets', revision: 2, version: '260920100000.0.0',
    sha256: 'ab'.repeat(32), rules: state.companyRules.map(item => ({ ...item })), published_at: '2026-09-20T10:00:00Z',
    ...(state.companyTldr ? { tldr: { ...state.companyTldr } } : {}),
  }]
  const sets = () => ({
    [COMPANY_SET]: { id: COMPANY_SET, layer_id: COMPANY, scope: { layer: 'company' }, name: state.companyName, revision: state.revision, rules: state.companyRules, published_version: state.published, ...(state.companyTldr ? { tldr: state.companyTldr } : {}) },
    [PROJECT_SET]: { id: PROJECT_SET, layer_id: PROJECT_LAYER, scope: { layer: 'project', project_id: RULE_PROJECT }, name: state.projectName, revision: state.revision, rules: state.projectRules, published_version: state.projectPublished, ...(state.projectTldr ? { tldr: state.projectTldr } : {}) },
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
      if (options.settings === false) answer.workspace.permissions = answer.workspace.permissions.filter(permission => permission !== 'settings.manage')
      answer.workspace.permissions = [...answer.workspace.permissions, ...extra]
      return route.fulfill({ json: answer })
    }
    if (path === '/api/projects' && method === 'GET') {
      return route.fulfill({ json: { items: [{ id: RULE_PROJECT, key: 'AEON', title: 'Aeon', state: 'active', open: 1, in_progress: 0, done: 0, cancelled: 0, total: 1, last_activity: '2026-09-28T10:00:00Z', people: [] }] } })
    }
    if (path === '/api/members' && method === 'GET') {
      return route.fulfill({ json: {
        people: [{ principal_id: RULE_PERSON, name: 'Markus Barta', avatar_url: null, has_avatar: false, email: 'markus@barta.com', status: 'active', identity: 'inspr_id', workspace_role: null, project_roles: [], aliases: [], classic_role: null, last_active_at: null, last_owner: true }],
        agents: (options.agents ?? [{ principal_id: RULE_AGENT, name: 'Worker' }]).map(agent => ({ principal_id: agent.principal_id, name: agent.name, has_avatar: false, workspace_role: null, key_count: 1, last_seen_at: null, service: false, ...(agent.preview ? { preview: agent.preview } : {}) })),
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
    const tldr = /^\/api\/rules\/sets\/([^/]+)\/tldr$/.exec(path)
    if (tldr && method === 'PUT') {
      if (body.expected_revision !== state.revision) return route.fulfill({ status: 409, json: { error: 'revision conflict', code: 'revision_conflict' } })
      const company = tldr[1] === COMPANY_SET
      const rules = (company ? state.companyRules : state.projectRules) as Record<string, unknown>[]
      for (const [identity, value] of Object.entries((body.rules ?? {}) as Record<string, { en: string; de?: string } | null>)) {
        const target = rules.find(item => item.identity === identity)
        if (!target) return route.fulfill({ status: 400, json: { error: 'no rule ' + identity, code: 'unknown_rule' } })
        if (value) target.tldr = { ...value, basis: 'fedcba9876543210' }
        else delete target.tldr
      }
      if ('set' in body) {
        const value = body.set ? { ...body.set, basis: 'fedcba9876543210' } : null
        if (company) state.companyTldr = value
        else state.projectTldr = value
      }
      state.revision += 1
      return route.fulfill({ json: sets()[tldr[1]] })
    }
    if (path === '/api/rules/budget') {
      if (method === 'PUT') {
        if (body.max_bytes < 2000 || body.max_bytes > 12000) return route.fulfill({ status: 400, json: { error: 'the session file budget must be between 2000 and 12000 bytes', code: 'invalid_budget' } })
        state.budget = { max_bytes: body.max_bytes, layer_max_bytes: body.layer_max_bytes ?? {} }
      }
      return route.fulfill({ json: { ...state.budget, default_bytes: 12000, min_bytes: 2000, ceiling_bytes: 12000, min_layer_bytes: 500 } })
    }
    if (path === '/api/rules/explained' && method === 'GET') {
      if (options.denyNamedPreview && url.searchParams.get('agent_id')) {
        return route.fulfill({ status: 403, json: { error: "You didn't create a key for this agent.", code: 'not_key_creator' } })
      }
      const harness = url.searchParams.get('harness') ?? 'claude-code'
      const served: { rule: Record<string, unknown>; set: string; layer: string }[] = []
      const seen = new Set<string>()
      const add = (list: Record<string, unknown>[], set: string, layer: string) => { for (const item of list) if (!seen.has(item.identity as string) && item.enabled) { seen.add(item.identity as string); served.push({ rule: item, set, layer }) } }
      add(state.companyRules as Record<string, unknown>[], COMPANY_SET, 'company')
      add(state.projectRules as Record<string, unknown>[], PROJECT_SET, 'project')
      if (harness === 'cursor') served.push({ rule: rule('cursor-only', 'Use the Cursor rules file.'), set: PROJECT_SET, layer: 'project' })
      served.sort((a, b) => (a.rule.identity as string).localeCompare(b.rule.identity as string))
      const lines = served.map(item => `- [${item.rule.identity}] ${item.rule.text}`)
      const body = `# Aeon session rules\n\n${lines.map(line => line + '\n').join('')}`
      const bytes = (text: string) => new TextEncoder().encode(text).length
      const usage: Record<string, number> = {}
      for (const [i, item] of served.entries()) usage[item.layer] = (usage[item.layer] ?? 0) + bytes(lines[i]! + '\n')
      const setBytes = (id: string) => served.reduce((n, item, i) => item.set === id ? n + bytes(lines[i]! + '\n') : n, 0)
      return route.fulfill({ json: {
        context: { tenant_id: 't1', project_id: url.searchParams.get('project_id'), person_id: url.searchParams.get('person_id'), role: url.searchParams.get('role'), harness },
        version: '260920100000.0.0', sha256: 'ab'.repeat(32), body, byte_size: bytes(body), budget: state.budget, usage,
        sets: [
          { set_id: COMPANY_SET, name: state.companyName, scope: { layer: 'company' }, version: '260920100000.0.0', bytes: setBytes(COMPANY_SET), ...(state.companyTldr ? { tldr: state.companyTldr } : {}) },
          { set_id: PROJECT_SET, name: state.projectName, scope: { layer: 'project', project_id: RULE_PROJECT }, version: '260920100000.0.0', bytes: setBytes(PROJECT_SET), ...(state.projectTldr ? { tldr: state.projectTldr } : {}) },
        ].filter(set => set.bytes > 0),
        rules: served.map((item, i) => ({ identity: item.rule.identity, text: item.rule.text, line: lines[i], set_id: item.set, layer: item.layer, strength: item.rule.strength, bytes: bytes(lines[i]! + '\n'), ...(item.rule.tldr ? { tldr: item.rule.tldr } : {}) })),
      } })
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
    if (path === '/api/rules/doctrine' && method === 'GET') return route.fulfill({ json: { sources: [] } })
    if (path === '/api/rules/merged' && method === 'GET') {
      if (options.denyNamedPreview && url.searchParams.get('agent_id')) {
        return route.fulfill({ status: 403, json: { error: "You didn't create a key for this agent.", code: 'not_key_creator' } })
      }
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
