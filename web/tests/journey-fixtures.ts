// SPDX-License-Identifier: AGPL-3.0-only
// An in-memory journey backend over the work fixtures (project PHAROS): the
// projection with its stages and next action, gate approvals, intake, requirements,
// releases (R1 nodes of the release kind), the release walker and plan write, stage
// handoffs and the stage plugins. It applies the server's revision and gate rules
// the face depends on, so specs can assert on what is sent.
import type { Page } from '@playwright/test'
import { me, type Call } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

const now = Date.parse('2026-09-23T12:00:00Z')
const ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString()
const ahead = (minutes: number) => new Date(Date.now() + minutes * 60_000).toISOString()
const STAGES = ['inspire', 'shape', 'requirements', 'plan', 'build', 'deploy', 'access', 'live'] as const
type Stage = typeof STAGES[number]
export const AGENT = 'a0000000-0000-4000-8000-000000000001'
export const PROJECT = 'p-pharos'

export type JourneyStart = 'inspire' | 'shape' | 'requirements' | 'open' | 'plan' | 'build' | 'mark' | 'deploy' | 'live'
export interface JourneyWorld {
  journey: {
    project_node_id: string; profile: string; revision: number; stage: Stage; next_action: { key: string; renewal_action?: 'renew_candidate' | 'renew_deploy'; label: string; stage: Stage; available: boolean; reason?: string; approval_request_id: string | null }
    requirements_revision: number; current_release_id: string | null; stages: { key: Stage; state: string; gate_approval_id: string | null; gate_live?: boolean; handoff_id: string | null; gate_offer_id?: string; gate_offer_state?: string; gate_offer_expires_at?: string }[]
    requirements_digest_sha256: string; requirements_approval_scope: string
    launch_readiness: { can_admit: boolean; reason: string }
    stage_source?: 'journey' | 'derived'
    imported?: boolean
  }
  approvals: { id: string; agent_principal_id: string; scope: string; resource_kind: 'node'; resource_id: string; run_id: null; rationale: string; expires_at: string; proposed_at: string; decision: 'approved' | 'denied' | null; decided_by_principal_id: string | null; risk: 'low' | 'medium' | 'high' }[]
  intake: { sources: unknown[]; turns: unknown[]; drafts: { id: string; kind: string; title: string; body: string; base_event_id: number; citations: unknown[]; ticket_suggestions: unknown[]; status: string; proposed_at: string; accepted_at: string | null; requirement_kind?: string }[] }
  requirements: { node_id: string; project_node_id: string; kind: string; revision: number; status: string; title: string; feature_node_id: string | null; generated_ticket_ids: string[] }[]
  releases: { id: string; key: string; kind_id: string; title: string; body: string; fields: Record<string, unknown>; state: string; parent_id: string; position: string; created_at: string; updated_at: string; deleted_at: null }[]
  walkers: Record<string, { release_node_id: string; project_node_id: string; state: string; revision: number; features: { feature_node_id: string; epic_key: string; title: string; selection: string; included_count: number; open_count: number }[]; tickets: { ticket_node_id: string; key: string; title: string; feature_node_id: string | null; included: boolean; position: number; estimated_hours: number | null; screen_node_ids: string[] }[] }>
  handoffs: Record<string, unknown>
  // U24: what an imported project brought besides its tickets.
  projectBody?: string
  knowledge?: { id: string; key: string; type: string; kind: string; slug: string; title: string; status: string; state: string; project: { id: string; key: string; title: string }; excerpt: string; link_count: number; created_at: string; updated_at: string; updated_by: null; imported: boolean }[]
}

const LABEL: Record<string, string> = {
  continue_intake: 'Continue intake', confirm_brief: 'Confirm brief', decide: 'Decide', reopen: 'Reopen', approve_requirements: 'Approve requirements',
  open_first_release: 'Open release 1', mark_candidate: 'Mark candidate',
  start_build: 'Start build', wait_for_build: 'Building', approve_candidate: 'Approve candidate', approve_deploy: 'Approve deployment',
  retry_deploy: 'Retry deployment', approve_permit: 'Approve permit', plan_next_release: 'Plan release 3',
}
function rail(stage: Stage, skipped: Stage[] = []) {
  const at = STAGES.indexOf(stage)
  return STAGES.map((key, i) => ({ key, state: skipped.includes(key) ? 'skipped' : i < at ? 'done' : i === at ? 'current' : 'later', gate_approval_id: null, handoff_id: null }))
}
const approval = (id: string, scope: string, resource: string, rationale: string, extra: Partial<JourneyWorld['approvals'][number]> = {}): JourneyWorld['approvals'][number] => ({
  id, agent_principal_id: AGENT, scope, resource_kind: 'node', resource_id: resource, run_id: null, rationale, expires_at: ahead(120), proposed_at: ago(12), decision: null, decided_by_principal_id: null, risk: 'medium', ...extra,
})

export interface WorldOptions {
  noGate?: boolean
  // B10: a stage derived from an imported project's history, and launch readiness.
  derived?: boolean
  readiness?: JourneyWorld['journey']['launch_readiness']
  noIntake?: boolean
}
export function journeyWorld(start: JourneyStart = 'plan', options: WorldOptions = {}): JourneyWorld {
  const release = (id: string, key: string, title: string, state: string, minutes: number) => ({ id, key, kind_id: 'k-release', title, body: '', fields: {}, state, parent_id: PROJECT, position: '0', created_at: ago(minutes), updated_at: ago(minutes), deleted_at: null })
  const releases = [release('r-1', 'PHAROS-30', '260901120000.0.0', 'done', 60 * 24 * 22), release('r-2', 'PHAROS-31', 'Release 2', 'backlog', 60 * 24 * 2)]
  const walkers: JourneyWorld['walkers'] = {
    'r-1': { release_node_id: 'r-1', project_node_id: PROJECT, state: 'released', revision: 4, features: [{ feature_node_id: 'n-epic', epic_key: 'PHAROS-10', title: 'Guarded multi-cloud provisioning', selection: 'all', included_count: 1, open_count: 1 }],
      tickets: [{ ticket_node_id: 'n-5', key: 'PHAROS-15', title: 'Beacon health probes', feature_node_id: 'n-epic', included: true, position: 0, estimated_hours: 5, screen_node_ids: [] }] },
    'r-2': { release_node_id: 'r-2', project_node_id: PROJECT, state: 'planning', revision: 7,
      features: [
        { feature_node_id: 'n-epic', epic_key: 'PHAROS-10', title: 'Guarded multi-cloud provisioning', selection: 'some', included_count: 2, open_count: 3 },
        { feature_node_id: 'n-epic-2', epic_key: 'PHAROS-20', title: 'Host access with Janus', selection: 'empty', included_count: 0, open_count: 0 },
      ],
      tickets: [
        { ticket_node_id: 'n-1', key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning', feature_node_id: 'n-epic', included: true, position: 0, estimated_hours: 8, screen_node_ids: [] },
        { ticket_node_id: 'n-2', key: 'PHAROS-12', title: 'Add an Oracle Cloud connector', feature_node_id: 'n-epic', included: false, position: 1, estimated_hours: 6, screen_node_ids: [] },
        { ticket_node_id: 'n-3', key: 'PHAROS-13', title: 'Run the disposable Hetzner end-to-end check', feature_node_id: 'n-epic', included: true, position: 2, estimated_hours: 3, screen_node_ids: [] },
        { ticket_node_id: 'n-4', key: 'PHAROS-14', title: 'Visual acceptance of the version pill', feature_node_id: null, included: false, position: 3, estimated_hours: null, screen_node_ids: [] },
      ] },
  }
  const world: JourneyWorld = {
    journey: {
      project_node_id: PROJECT, profile: 'professional', revision: 12, stage: 'plan', next_action: { key: 'start_build', label: 'Start build', stage: 'plan', available: false, reason: 'Build start needs an approved gate.', approval_request_id: 'ap-build' },
      requirements_revision: 3, current_release_id: 'r-2', stages: rail('plan'),
      requirements_digest_sha256: '9f2c1'.padEnd(64, '0'), requirements_approval_scope: `journey.requirements.r12.d${'9f2c1'.padEnd(64, '0')}`,
      launch_readiness: { can_admit: false, reason: 'The release is not ready for deployment.' },
    },
    approvals: options.noGate ? [] : [approval('ap-build', 'journey.build', 'r-2', 'Release 2 is planned and estimated; ready to build.')],
    intake: {
      sources: [
        { id: 's-conv', project_node_id: PROJECT, kind: 'conversation', label: 'Voice conversation with Markus', content_sha256: 'a'.repeat(64), created_at: ago(60 * 30) },
        { id: 's-file', project_node_id: PROJECT, kind: 'file', label: 'Provider price list.pdf', file_id: 'f-1', content_sha256: 'b'.repeat(64), created_at: ago(60 * 29) },
        { id: 's-url', project_node_id: PROJECT, kind: 'url', label: 'Hetzner Cloud API', locator: 'https://docs.hetzner.cloud', content_sha256: 'c'.repeat(64), created_at: ago(60 * 28) },
      ],
      turns: [
        { id: 't-1', source_id: 's-conv', ordinal: 1, speaker: 'person', speaker_principal_id: me.id, body: 'Pharos should provision hosts on Hetzner and Oracle, but only after I approve the cost.', idempotency_key: 'k1', created_at: ago(60 * 30) },
        { id: 't-2', source_id: 's-conv', ordinal: 2, speaker: 'agent', speaker_principal_id: AGENT, body: 'So each new host is a guarded step: a price, your approval, then the provider call.', idempotency_key: 'k2', created_at: ago(60 * 30 - 1) },
        { id: 't-3', source_id: 's-conv', ordinal: 3, speaker: 'person', speaker_principal_id: me.id, body: 'Yes, and cleanup must leave nothing behind.', idempotency_key: 'k3', created_at: ago(60 * 30 - 2) },
      ],
      drafts: [
        { id: 'd-brief', kind: 'brief', title: 'Guarded multi-cloud provisioning', body: 'Provision hosts on **Hetzner** and **Oracle** after an approved price; cleanup leaves nothing behind.', base_event_id: 42, citations: [{ source_id: 's-conv', turn_id: 't-1', locator: '00:31' }, { source_id: 's-file', locator: 'p.2' }], ticket_suggestions: [], status: 'proposed', proposed_at: ago(60 * 20), accepted_at: null },
      ],
    },
    requirements: [
      { node_id: 'q-1', project_node_id: PROJECT, kind: 'functional', revision: 3, status: 'agreed', title: 'A new host is provisioned only after its price is approved.', feature_node_id: 'n-epic', generated_ticket_ids: ['n-1', 'n-2', 'n-3'] },
      { node_id: 'q-2', project_node_id: PROJECT, kind: 'functional', revision: 3, status: 'agreed', title: 'People reach a host through Janus with a bounded permit.', feature_node_id: 'n-epic-2', generated_ticket_ids: [] },
      { node_id: 'q-3', project_node_id: PROJECT, kind: 'nonfunctional', revision: 3, status: 'agreed', title: 'Cleanup leaves no provider resources behind.', feature_node_id: null, generated_ticket_ids: [] },
    ],
    releases, walkers,
    handoffs: {},
  }
  const set = (stage: Stage, key: string, extra: Partial<JourneyWorld['journey']['next_action']> = {}) => {
    world.journey.stage = stage
    world.journey.stages = rail(stage)
    world.journey.next_action = { key, label: LABEL[key], stage, available: true, approval_request_id: null, ...extra }
  }
  if (start === 'inspire') { set('inspire', 'continue_intake'); world.journey.current_release_id = null; world.requirements = [] }
  if (start === 'shape') { set('shape', 'decide', { available: false, reason: 'Shape needs an approved gate before go, reduce scope, park or drop.', approval_request_id: options.noGate ? null : 'ap-shape' }); world.journey.current_release_id = null; world.intake.drafts[0].status = 'accepted'; world.intake.drafts[0].accepted_at = ago(60 * 19); world.approvals = options.noGate ? [] : [approval('ap-shape', 'journey.shape', PROJECT, 'The brief is confirmed; the estimate fits the cap.')] }
  if (start === 'requirements') { set('requirements', 'approve_requirements', { available: false, reason: 'Requirements need an approved gate.', approval_request_id: options.noGate ? null : 'ap-req' }); world.requirements = world.requirements.map(r => ({ ...r, status: 'draft', revision: 0, generated_ticket_ids: [] })); world.approvals = options.noGate ? [] : [approval('ap-req-old', 'journey.requirements.r11.dold', PROJECT, 'An earlier revision.'), approval('ap-req', world.journey.requirements_approval_scope, PROJECT, 'Three requirements are drafted with their sources.')] }
  if (start === 'open') { set('plan', 'open_first_release'); world.journey.current_release_id = null; world.releases = []; world.approvals = [] }
  if (start === 'mark') {
    set('build', 'mark_candidate', { available: false, reason: 'Marking a candidate needs an approved build gate.', approval_request_id: options.noGate ? null : 'ap-mark' })
    world.walkers['r-2'] = { ...world.walkers['r-2'], state: 'building', tickets: [{ ticket_node_id: 'n-5', key: 'PHAROS-15', title: 'Beacon health probes', feature_node_id: 'n-epic', included: true, position: 0, estimated_hours: 5, screen_node_ids: [] }] }
    world.approvals = options.noGate ? [] : [approval('ap-mark', 'journey.build', 'r-2', 'Every ticket of release 2 is done; mark it as the candidate.')]
  }
  if (start === 'build') { set('build', 'wait_for_build', { available: false, reason: 'The build is still in progress.' }); world.walkers['r-2'].state = 'building' }
  if (start === 'deploy') {
    set('deploy', 'retry_deploy', { available: false, reason: 'Deployment retry needs an approved gate.', approval_request_id: options.noGate ? null : 'ap-deploy' })
    world.journey.launch_readiness = { can_admit: false, reason: 'Pharos launch checks are unavailable.' }
    world.journey.stages = world.journey.stages.map(s => s.key === 'deploy' ? { ...s, state: 'blocked', handoff_id: 'h-1' } : s)
    world.walkers['r-2'].state = 'refused'
    world.handoffs['h-1'] = { id: 'h-1', project_node_id: PROJECT, release_node_id: 'r-2', stage: 'deploy', operation: 'deploy', plugin_id: 'pharos', attempt: 1, authority_epoch: 1, state: 'failed', expires_at: ago(10), result: { outcome: 'failed', blocker_code: 'policy_refused', completed_at: ago(20) } }
    world.approvals = options.noGate ? [] : [approval('ap-deploy', 'journey.deploy', 'r-2', 'Fresh backup evidence is recorded; retry the deployment.', { risk: 'high' })]
  }
  if (start === 'live') { set('live', 'plan_next_release'); world.walkers['r-2'].state = 'released'; world.walkers['r-2'].tickets = world.walkers['r-2'].tickets.filter(t => t.included); world.approvals = [] }
  if (start === 'build' && !options.noGate) world.approvals = []
  if (options.derived) {
    world.journey.stage_source = 'derived'
    world.journey.imported = true
    world.intake = { sources: [], turns: [], drafts: [] }
    world.requirements = []
  }
  if (options.noIntake) world.intake = { sources: [], turns: [], drafts: [] }
  if (options.readiness !== undefined) world.journey.launch_readiness = options.readiness
  const offered = world.approvals.find(a => a.id === world.journey.next_action.approval_request_id)
  if (offered) {
    const key = offered.scope === 'journey.build' ? 'plan' : world.journey.stage
    Object.assign(world.journey.stages.find(s => s.key === key)!, { gate_offer_id: offered.id, gate_offer_state: 'pending', gate_offer_expires_at: offered.expires_at })
  }
  return world
}

// journey/1.1 keeps consumed evidence separate from the fresh retry offer.
export function retryJourneyWorld(state: 'pending' | 'approved' | 'expired' | 'revoked' | 'missing' = 'pending') {
  const world = journeyWorld('deploy')
  const standing = approval('ap-standing-consumed', 'journey.deploy', 'r-2', 'The applied deployment gate.', { decision: 'approved', decided_by_principal_id: me.id })
  const retry = approval('ap-fresh-retry', 'journey.deploy', 'r-2', 'A fresh human retry after the host refused deployment.', { decision: state === 'pending' ? null : 'approved', decided_by_principal_id: state === 'pending' ? null : me.id, risk: 'high' })
  world.approvals = state === 'missing' ? [standing] : [retry, standing]
  Object.assign(world.journey.stages.find(s => s.key === 'deploy')!, {
    gate_approval_id: standing.id, gate_live: true,
    gate_offer_id: retry.id, gate_offer_state: state === 'pending' ? 'pending' : state === 'expired' || state === 'revoked' ? state : 'approved_live',
    gate_offer_expires_at: ahead(state === 'expired' ? -1 : 30),
  })
  Object.assign(world.journey.next_action, { approval_request_id: retry.id, available: state === 'approved' || state === 'missing', reason: state === 'revoked' ? 'Gate approval was revoked. The agent asks again for a fresh one.' : state === 'expired' ? 'Gate approval expired. The agent asks again for a fresh one.' : state === 'pending' ? 'Deployment retry needs an approved gate.' : '' })
  return world
}

const PLUGINS = [
  { id: 'pharos', version: '1', owner: 'pharos', installation: { enabled: false }, integrations: [{ id: 'pharos_deploy', permission: 'stage.deploy' }], workflow_steps: [{ key: 'deploy', gates: ['artifact_identity', 'backup_ready', 'readiness', 'launch_admission', 'person_decision', 'prerequisite_seal'] }, { key: 'verify', gates: ['artifact_identity', 'readiness'] }] },
  { id: 'janus', version: '1', owner: 'janus', installation: { enabled: false }, integrations: [{ id: 'janus_apply', permission: 'stage.access_apply' }], workflow_steps: [{ key: 'prepare', gates: ['observed_state'] }, { key: 'apply', gates: ['deployment_succeeded', 'bounded_permit', 'person_decision'] }] },
]

// Registered after mockWork, so its routes win; anything else falls through.
export async function mockJourney(page: Page, world: JourneyWorld, options: { failPlan?: boolean; kind?: 'person' | 'agent'; noTicketRoute?: boolean; flowControls?: boolean } = {}) {
  const calls: Call[] = []
  const bump = () => { world.journey.revision++ }
  await page.route('**/api/**', async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method(), query = url.searchParams
    let body: Record<string, unknown> = {}
    try { body = request.postDataJSON() ?? {} } catch { body = {} }
    const record = () => calls.push({ path, method, query, body, headers: request.headers() })
    // Existing journey specs explicitly exercise the opt-in experience. Default
    // and persistence specs pass false to use mockWork's per-person preferences.
    if (path === '/api/preferences/developer-ui' && options.flowControls !== false && method === 'GET') return route.fulfill({ json: { key: 'developer-ui', value: { show_flow_controls: true } } })
    if (path === '/api/me') return route.fulfill({ json: { principal: { id: me.id, name: me.name, kind: options.kind ?? 'person', roles: ['member'] }, tenant: { id: 't1', name: 'INSPR Studio' } } })
    if (path === '/api/me/permissions') return route.fulfill({ json: mockEffectivePermissions('admin', query.get('project_id') ?? undefined) })
    if (path === '/api/kinds') return route.fulfill({ json: { items: ['epic', 'ticket', 'task', 'project', 'release'].map(slug => ({ id: `k-${slug}`, slug, label: slug[0].toUpperCase() + slug.slice(1), short_prefix: slug.slice(0, 3).toUpperCase(), icon: slug, allowed_child_kinds: null, field_schema: {} })) } })
    if (path === '/api/plugins') return route.fulfill({ json: PLUGINS })
    // U24: the project node itself (its description stands in for an imported project's brief).
    if (path === `/api/nodes/${PROJECT}` && method === 'GET') return route.fulfill({ json: { id: PROJECT, key: 'PRJ-17', kind_id: 'k-project', title: 'Pharos', body: world.projectBody ?? '', fields: { classic: { key: 'PHAROS' } }, state: 'active', parent_id: null, position: '0', created_at: ago(60 * 24 * 90), updated_at: ago(60), deleted_at: null } })
    if (path === '/api/knowledge' && world.knowledge) return route.fulfill({ json: { items: world.knowledge, total: world.knowledge.length, truncated: false, counts: { type: {}, status: {} } } })
    // The agent asking for the gates: one Claude session, so it has a name.
    if (path === '/api/harness-sessions') return route.fulfill({ json: { items: [{
      id: '5e000000-0000-4000-8000-000000000001', project_id: PROJECT, agent_principal_id: AGENT, run_id: null, ticket_node_id: 'n-1', harness: 'claude', host: 'camy',
      parent_harness_session_id: null, work_order_id: null, management_mode: 'managed', role: 'coordinator', advertised_capabilities: ['inbox', 'status'], activity_sequence: 1, revision: 1, finished: false,
      work_shape: 'ship', phase: 'working', activity: 'busy', heartbeat_at: ago(0.2), created_at: ago(60), stopped_at: null, stop_reason: null,
      project: { id: PROJECT, key: 'PRJ-17', title: 'Pharos' }, ticket: { id: 'n-1', key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' },
    }], next_cursor: null } })
    if (path === '/api/nodes' && query.get('kind_id') === 'k-release') return route.fulfill({ json: { items: world.releases, next_cursor: null } })
    if (path === '/api/approvals') return route.fulfill({ json: world.approvals })
    const decision = /^\/api\/approvals\/([^/]+)\/decision$/.exec(path)
    if (decision && method === 'POST') {
      record()
      const found = world.approvals.find(a => a.id === decision[1])
      if (!found) return route.fulfill({ status: 404, json: { error: 'approval not found' } })
      found.decision = body.decision as 'approved' | 'denied'; found.decided_by_principal_id = me.id
      const stage = world.journey.stages.find(s => s.gate_offer_id === found.id)
      if (stage) stage.gate_offer_state = found.decision === 'approved' ? 'approved_live' : 'rejected'
      // An approved gate makes the offered action available.
      if (found.decision === 'approved' && world.journey.next_action.approval_request_id === found.id) { world.journey.next_action.available = true; delete world.journey.next_action.reason }
      return route.fulfill({ json: found })
    }
    const project = /^\/api\/projects\/([^/]+)\/(journey(?:\/profile|\/actions)?|requirements(?:\/agree)?|intake(?:\/drafts\/([^/]+)\/accept)?|releases\/([^/]+)\/(walker|plan|tickets))$/.exec(path)
    if (!project) {
      const handoff = /^\/api\/stage-handoffs\/([^/]+)$/.exec(path)
      if (handoff) return world.handoffs[handoff[1]] ? route.fulfill({ json: world.handoffs[handoff[1]] }) : route.fulfill({ status: 404, json: { error: 'handoff not found' } })
      return route.fallback()
    }
    const [, projectId, what, draftId, releaseId, releaseWhat] = project
    if (projectId !== PROJECT) return route.fulfill({ status: 404, json: { error: 'project not found' } })
    if (method !== 'GET') record()
    if (what === 'journey') return route.fulfill({ json: world.journey })
    if (what === 'journey/profile') {
      if (body.expected_revision !== world.journey.revision) return route.fulfill({ status: 409, json: { error: 'journey revision is stale' } })
      world.journey.profile = body.profile as string; bump()
      return route.fulfill({ json: world.journey })
    }
    if (what === 'journey/actions') {
      if (body.expected_revision !== world.journey.revision) return route.fulfill({ status: 409, json: { error: 'journey revision is stale' } })
      const next = world.journey.next_action
      if (body.action !== (next.renewal_action ?? next.key) && !(next.key === 'decide' && ['go', 'reduce_scope', 'park', 'drop'].includes(String(body.action))) && body.action !== 'reject_candidate') return route.fulfill({ status: 409, json: { error: 'that action is not available' } })
      const gate = world.approvals.find(a => a.id === body.approval_request_id)
      if (world.journey.stages.some(s => s.gate_approval_id && s.gate_approval_id === body.approval_request_id)) return route.fulfill({ status: 409, json: { error: 'approval already consumed' } })
      if (!['confirm_brief', 'plan_next_release', 'open_first_release'].includes(next.key) && (!gate || gate.decision !== 'approved')) return route.fulfill({ status: 403, json: { error: 'approval does not grant this action' } })
      bump()
      if (body.action === 'start_build') {
        world.walkers['r-2'].state = 'building'
        world.journey.stage = 'build'; world.journey.stages = rail('build')
        world.journey.next_action = { key: 'wait_for_build', label: 'Building', stage: 'build', available: false, reason: 'The build is still in progress.', approval_request_id: null }
      } else if (body.action === 'renew_candidate') {
        const stage = world.journey.stages.find(s => s.key === 'build')!
        stage.gate_approval_id = gate!.id; stage.gate_live = true
        delete stage.gate_offer_id; delete stage.gate_offer_state; delete stage.gate_offer_expires_at
        world.journey.next_action = { key: 'approve_deploy', renewal_action: 'renew_deploy', label: 'Renew deployment approval', stage: 'deploy', available: false, reason: 'The standing deployment gate is no longer live. A fresh approval is required.', approval_request_id: 'fresh-deploy' }
      } else if (body.action === 'retry_deploy' || body.action === 'renew_deploy') {
        world.walkers['r-2'].state = 'deploying'
        const stage = world.journey.stages.find(s => s.key === 'deploy')!
        stage.gate_approval_id = gate!.id; stage.gate_live = true
        delete stage.gate_offer_id; delete stage.gate_offer_state; delete stage.gate_offer_expires_at
        world.journey.next_action = { key: 'approve_deploy', label: 'Await deployment evidence', stage: 'deploy', available: false, reason: 'Deployment evidence is not terminal.', approval_request_id: null }
      } else if (body.action === 'mark_candidate') {
        world.walkers['r-2'].state = 'candidate'
        world.journey.next_action = { key: 'approve_candidate', label: 'Approve candidate', stage: 'build', available: false, reason: 'Candidate review needs an approved gate.', approval_request_id: null }
      } else if (body.action === 'open_first_release') {
        world.releases = [{ id: 'r-new', key: 'PHAROS-40', kind_id: 'k-release', title: 'Release 1', body: '', fields: {}, state: 'backlog', parent_id: PROJECT, position: '1', created_at: new Date(now).toISOString(), updated_at: new Date(now).toISOString(), deleted_at: null }]
        world.walkers['r-new'] = { release_node_id: 'r-new', project_node_id: PROJECT, state: 'planning', revision: 1, features: [], tickets: [{ ticket_node_id: 'n-4', key: 'PHAROS-14', title: 'Visual acceptance of the version pill', feature_node_id: null, included: false, position: 0, estimated_hours: null, screen_node_ids: [] }] }
        world.journey.current_release_id = 'r-new'
        world.journey.next_action = { key: 'start_build', label: 'Start build', stage: 'plan', available: false, reason: 'Select at least one ticket for this release.', approval_request_id: null }
      } else if (body.action === 'go') {
        world.journey.stage = 'requirements'; world.journey.stages = rail('requirements')
        world.journey.next_action = { key: 'approve_requirements', label: 'Approve requirements', stage: 'requirements', available: false, reason: 'Requirements need an approved gate.', approval_request_id: null }
      } else if (body.action === 'confirm_brief') {
        world.journey.stage = 'shape'; world.journey.stages = rail('shape')
        world.journey.next_action = { key: 'decide', label: 'Decide', stage: 'shape', available: false, reason: 'Shape needs an approved gate before go, reduce scope, park or drop.', approval_request_id: null }
      }
      return route.fulfill({ json: world.journey })
    }
    if (what === 'requirements') return route.fulfill({ json: world.requirements })
    if (what === 'requirements/agree') {
      if (body.expected_revision !== world.journey.revision) return route.fulfill({ status: 409, json: { error: 'journey revision is stale' } })
      const gate = world.approvals.find(a => a.id === body.approval_request_id)
      if (!gate || gate.decision !== 'approved' || gate.scope !== world.journey.requirements_approval_scope) return route.fulfill({ status: 403, json: { error: 'live person approval for the current requirements digest and revision required' } })
      world.requirements = world.requirements.map(r => ({ ...r, status: 'agreed', revision: 3 })); bump()
      world.journey.stage = 'plan'; world.journey.stages = rail('plan'); world.journey.next_action = { key: 'start_build', label: 'Start build', stage: 'plan', available: false, reason: 'Build start needs an approved gate.', approval_request_id: null }
      return route.fulfill({ json: world.requirements })
    }
    if (what === 'intake') return route.fulfill({ json: world.intake })
    if (draftId) {
      const draft = world.intake.drafts.find(d => d.id === draftId)
      if (!draft || body.expected_base_event_id !== draft.base_event_id) return route.fulfill({ status: 409, json: { error: 'target changed' } })
      draft.status = 'accepted'; draft.accepted_at = new Date(now).toISOString()
      if (draft.kind === 'brief' && world.journey.next_action.key === 'continue_intake') world.journey.next_action = { key: 'confirm_brief', label: 'Confirm brief', stage: 'inspire', available: true, approval_request_id: null }
      return route.fulfill({ json: draft })
    }
    const walker = releaseId ? world.walkers[releaseId] : undefined
    if (!walker) return route.fulfill({ status: 404, json: { error: 'project or release not found' } })
    if (releaseWhat === 'walker') return route.fulfill({ json: walker })
    if (releaseWhat === 'tickets') {
      if (options.noTicketRoute) return route.fulfill({ status: 404, json: { error: 'not found' } })
      if (body.expected_revision !== walker.revision) return route.fulfill({ status: 409, json: { error: 'release revision changed' } })
      const n = walker.tickets.length + 1
      walker.tickets = [...walker.tickets, { ticket_node_id: `n-added-${n}`, key: `PHAROS-${39 + n}`, title: String(body.title), feature_node_id: (body.feature_node_id as string | null) ?? null, included: body.included === true, position: walker.tickets.length, estimated_hours: null, screen_node_ids: [] }]
      walker.revision++; bump()
      return route.fulfill({ status: 201, json: walker })
    }
    if (options.failPlan) return route.fulfill({ status: 409, json: { error: 'release revision changed' } })
    if (releaseId !== world.journey.current_release_id || walker.state !== 'planning') return route.fulfill({ status: 409, json: { error: 'only the current planning release can change' } })
    if (body.expected_revision !== walker.revision) return route.fulfill({ status: 409, json: { error: 'release revision changed' } })
    const order = body.ordered_ticket_ids as string[], included = new Set(body.included_ticket_ids as string[])
    if (order.length !== walker.tickets.length || new Set(order).size !== order.length) return route.fulfill({ status: 409, json: { error: 'order must contain every current release and backlog ticket exactly once' } })
    walker.tickets = order.map((id, position) => ({ ...walker.tickets.find(t => t.ticket_node_id === id)!, position, included: included.has(id) }))
    walker.revision++; bump()
    return route.fulfill({ json: walker })
  })
  return calls
}
