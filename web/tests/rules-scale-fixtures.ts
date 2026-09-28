// SPDX-License-Identifier: AGPL-3.0-only
// Agent rules at realistic scale (AEON-263): 14 sets and 51 rules, 11 company
// sets and 3 for the Aeon project, 30 of them locked, in the Markdown the real
// doctrine uses. One mock serves the rules API, permissions, projects and members.
import type { Page, Route } from '@playwright/test'
import { mockEffectivePermissions } from './authz-fixtures'

export const SCALE_TENANT = 't1'
export const SCALE_PERSON = '11111111-1111-4111-8111-111111111111'
export const SCALE_AGENT = '22222222-2222-4222-8222-222222222222'
export const AEON_PROJECT = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
export const NUNCID_PROJECT = 'abababab-abab-4bab-8bab-abababababab'
const COMPANY_LAYER = 'c0000000-0000-4000-8000-000000000001'
const PROJECT_LAYER = 'c0000000-0000-4000-8000-000000000002'

type Strength = 'normal' | 'locked'
interface Spec { id: string; text: string; why: string; locked?: boolean; details?: string; roles?: string[]; off?: boolean }
const L = true

const COMPANY: [string, Spec[]][] = [
  ['Secrets', [
    { id: 'no-env-dump', locked: L, text: 'Never run a command whose output **is** the resolved environment, such as `env`, `printenv` or `direnv export`.', why: 'Those commands print every credential in the session at once.', details: 'To confirm a variable exists, test it: `[ -n "$VAR" ] && echo set`. Never echo the value.' },
    { id: 'secret-stop', locked: L, text: 'If a secret appears in any output, **stop**, do not repeat it, and alert the operator.', why: 'A leaked value must be treated as compromised and rotated.' },
    { id: 'agent-secrets', locked: L, text: 'Load agent credentials only with `( set -a; source <file>; cmd; set +a )`; never read the file itself.', why: 'Reading the file puts the secret into the transcript.' },
    { id: 'no-commit-secrets', locked: L, text: 'Never commit passwords, tokens, bcrypt hashes or decrypted `.age` content.', why: 'Git history is permanent and widely copied.' },
  ]],
  ['Git', [
    { id: 'git-destructive', locked: L, text: 'Never run `git reset --hard`, `git clean -f` or `git checkout .` unless the operator asks for it explicitly.', why: 'They destroy uncommitted work without a trace.' },
    { id: 'git-force-main', locked: L, text: 'No `git push --force` to `main` or `master`.', why: 'Rewriting shared history breaks every other clone.' },
    { id: 'git-hooks', locked: L, text: 'Never bypass hooks with `--no-verify`; fix the cause and create a **new** commit.', why: 'Hooks carry the secret scan and the formatting gate.' },
    { id: 'git-amend', locked: L, text: 'Do not `git commit --amend` unless asked: the prior commit may be the work you would destroy.', why: 'An amend silently replaces reviewed work.' },
    { id: 'git-diff-first', text: 'Run `git diff` and `git status` before every commit to scan the file set.', why: 'Stray files and secrets are caught before they land.' },
  ]],
  ['Cross-repo authoring', [
    { id: 'own-repo-only', locked: L, text: 'Author changes **only** in the session’s own repository; elsewhere, file a ticket with the diff in the body.', why: 'Foreign repositories have owners and reviews of their own.' },
    { id: 'release-pins', locked: L, text: 'In a repo holding a deploy pin, edit only the pin, its comment and the documented vendoring step.', why: 'Release pins are the single allowed cross-repo write.' },
    { id: 'third-party-stop', locked: L, text: 'Third-party repositories without a PR path: **stop and ask**, never push.', why: 'Business-owned code is never changed on agent initiative.' },
    { id: 'own-residue', text: 'Clean up only your own residue; other people’s branches become a ticket.', why: 'Deleting someone else’s branch can lose their work.' },
  ]],
  ['Files and operations', [
    { id: 'trash-not-rm', locked: L, text: 'Delete with `trash`, never `rm -rf`.', why: 'A trash can be emptied later; `rm -rf` cannot be undone.' },
    { id: 'no-nixos-on-mac', locked: L, text: 'Never build NixOS configurations on macOS; build remotely over SSH.', why: 'Local builds fail slowly and leave broken store paths.' },
    { id: 'encrypted-files', locked: L, text: 'Touch `.age` and `.env` files only with explicit permission; hand the command to the operator.', why: 'Encrypted files hold production credentials.' },
    { id: 'no-new-md', text: 'Do not create new `.md` files unless asked; durable knowledge goes to a PPM Knowledge entry.', why: 'Scattered notes drift; one knowledge base stays searchable.' },
  ]],
  ['Fleet', [
    { id: 'prod-not-lab', locked: L, text: 'Fleet hosts (`hsb*`, `csb*`) run production only: no test VMs, lab containers or disposable deployments.', why: 'Lab VMs once exhausted a production host’s memory (INSPR-461).' },
    { id: 'labs-local', locked: L, text: 'Labs run on the operator workstation or on CI runners, never on a fleet host.', why: 'Production capacity is sized for production.' },
    { id: 'one-step', locked: L, text: 'For SSH handshakes, key rotation and other interactive procedures, go **one step at a time** and wait for “done”.', why: 'A ten-step dump gets half-followed and leaves hosts in between.' },
  ]],
  ['Tickets and attribution', [
    { id: 'ticket-first', locked: L, text: 'Bind material work to exactly one ticket before editing, and add the worker marker.', why: 'Every change needs an owner and a trail.' },
    { id: 'one-tracker', locked: L, text: 'Use the product’s designated tracker, never two.', why: 'Split tickets split the history.' },
    { id: 'handoffs', text: 'Record handoffs on the ticket without erasing earlier markers.', why: 'The next worker needs to know who did what.' },
    { id: 'no-backlog-picking', text: 'Do not pick backlog items yourself; ask what to tackle next.', why: 'Priorities belong to the owner.' },
  ]],
  ['Review gates', [
    { id: 'cross-family', locked: L, text: 'Deep review goes to a **different model family** than the author.', why: 'Same-family reviewers share blind spots.' },
    { id: 'high-risk-before-merge', locked: L, text: 'Authentication, permissions, migrations, secrets, production deploys and company rules get review **before** merge.', why: 'These are the changes that cannot wait for a batch.' },
    { id: 'reviewer-from-registry', locked: L, text: 'Reviewer models come from `paimos model resolve review-gate`, never from rule text.', why: 'Model names change; the registry is the one source.' },
    { id: 'explicit-ok', text: 'Proceed only when the reviewer returns an explicit `ok`.', why: 'Silence or a partial answer is not an approval.' },
    { id: 'batch-review', text: 'Everything else is reviewed once per release batch.', why: 'Per-change review of routine work costs more than it finds.', roles: ['coordinator', 'reviewer'] },
  ]],
  ['Trust contexts', [
    { id: 'no-cross-context', locked: L, text: 'Never cross personal, INSPR and business contexts with credentials or tickets; **stop and ask**.', why: 'Client data must never meet personal or open-source work.' },
    { id: 'classify-by-output', locked: L, text: 'Classify a repository by who owns its output, never by its GitHub organisation.', why: 'Organisations are historical accidents.' },
    { id: 'person-layer-private', locked: L, text: 'A person’s rules never leave their workspace.', why: 'Preferences can reveal private details.' },
  ]],
  ['Versioning', [
    { id: 'calendar-versions', locked: L, text: 'Releases use INSPR calendar versions `YYMMDDhhmmss.0.0` in UTC.', why: 'One scheme across products makes every artifact traceable.' },
    { id: 'keep-history', text: 'Preserve migration gates and historical artifacts when adopting a new version.', why: 'Old releases must stay reproducible.' },
    { id: 'pin-review', text: 'Adopted projects review their saved presentation pin before each release.', why: 'A stale pin shows the wrong version to users.' },
  ]],
  ['Communication', [
    { id: 'telegraph', text: 'Write telegraph style: dense, low fluff, **TL;DR** at the start and end of long answers.', why: 'The owner reads many reports a day.' },
    { id: 'no-emojis', text: 'No emojis in reports or commit messages.', why: 'They add noise and break some terminals.' },
    { id: 'time-neutral', text: 'Run `date` before any time-of-day greeting, or stay time-neutral.', why: 'The date alone does not tell morning from night.' },
    { id: 'absolute-paths', text: 'Share absolute file paths in the final report.', why: 'Relative paths are ambiguous across worktrees.', off: true },
  ]],
  ['Knowledge', [
    { id: 'kb-first', locked: L, text: 'Architecture, rationale and playbooks go to a PPM Knowledge entry when writes are authorized.', why: 'Knowledge in chat transcripts is lost.' },
    { id: 'readme-local', text: 'README, AGENTS.md, RUNBOOK.md and CHANGELOG.md stay local to the repository.', why: 'These travel with the code they describe.' },
    { id: 'ask-before-kb', text: 'Without write authority, report the intended knowledge entry and ask.', why: 'The owner decides what becomes shared knowledge.' },
  ]],
]
const PROJECT: [string, Spec[]][] = [
  ['Package scope', [
    { id: 'package-scope', locked: L, text: 'Your package is your scope: change only the files your package needs.', why: 'Parallel workers must not collide.' },
    { id: 'shared-files', text: 'Keep changes to shared files such as `api/openapi.yaml` or `go.mod` minimal and **additive**.', why: 'PHAROS and JANUS parse the contract strictly.' },
    { id: 'migration-numbers', text: 'Take the next free migration number and re-check it right before committing.', why: 'Other workers add migrations at the same time.', roles: ['builder'] },
  ]],
  ['Contract first', [
    { id: 'contract-first', locked: L, text: 'Add endpoints to `api/openapi.yaml` in the **same change** as their handler.', why: 'The contract is the single source for every client.' },
    { id: 'sse-live', text: 'Live updates use server-sent events; do not add WebSockets.', why: 'One transport keeps the proxy setup simple.' },
    { id: 'tenant-rls', text: 'Every table carries `tenant_id` and an RLS policy on `current_setting(\'aeon.tenant_id\')`.', why: 'Row-level security is the tenant boundary.' },
  ]],
  ['Interface', [
    { id: 'no-edge-accents', locked: L, text: 'Never mark state with a coloured bar on the **left or top edge** of a row, card or toast.', why: 'It is the classic AI-generated UI tell (Markus, 2026-09-24).' },
    { id: 'svg-icons', text: 'Use SVG icons only, centred in their controls; never text glyphs.', why: 'Glyphs render differently on every platform.' },
    { id: 'design-tokens', text: 'Take colours and spacing from `web/src/styles/tokens.css`.', why: 'Tokens keep light and dark themes consistent.' },
  ]],
]

function rule(spec: Spec, reference: string) {
  return {
    identity: spec.id, text: spec.text, why: spec.why, ...(spec.details ? { details: spec.details } : {}),
    strength: (spec.locked ? 'locked' : 'normal') as Strength, enabled: spec.locked ? true : !spec.off,
    roles: spec.roles ?? [], harnesses: [], source: { reference, identity: spec.locked ? spec.id : undefined, edited_here: false },
  }
}
export type ScaleRule = ReturnType<typeof rule>
const setId = (n: number) => `e0000000-0000-4000-8000-${String(n).padStart(12, '0')}`

/** The draft file an operator imports: company and Aeon project, 14 sets, 51 rules. */
export function scaleImportFile(tenant = SCALE_TENANT) {
  const layers = [
    { scope: { layer: 'company' }, sets: COMPANY.map(([name, specs]) => ({ name, rules: specs.map(spec => rule(spec, 'inspr-doctrine 0.14')) })) },
    { scope: { layer: 'project', project_id: AEON_PROJECT }, sets: PROJECT.map(([name, specs]) => ({ name, rules: specs.map(spec => rule(spec, 'aeon/AGENTS.md')) })) },
  ]
  return { name: 'inspr-rules-2026-09-28.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify({ schema: 'aeon.rules-draft-import.v1', tenant_id: tenant, layers })) }
}
export const SCALE_COUNTS = { sets: COMPANY.length + PROJECT.length, rules: [...COMPANY, ...PROJECT].reduce((n, [, specs]) => n + specs.length, 0), locked: [...COMPANY, ...PROJECT].reduce((n, [, specs]) => n + specs.filter(spec => spec.locked).length, 0) }

export type ScaleState = 'empty' | 'drafts' | 'live'
export interface ScaleOptions {
  state?: ScaleState
  kind?: 'person' | 'agent'
  publish?: boolean
  /** Holds every project-scoped permission answer until released. */
  holdProjectPermissions?: boolean
  batchFailure?: { status: number; code: string; error: string; actual_bytes?: number; max_bytes?: number }
}
export interface ScaleMock {
  calls: { method: string; path: string; body?: unknown }[]
  releasePermissions: () => void
}

interface MockSet { id: string; layer_id: string; scope: Record<string, string>; name: string; revision: number; rules: ScaleRule[]; published_version: string }
interface MockSnapshot { set_id: string; scope: Record<string, string>; name: string; revision: number; version: string; sha256: string; rules: ScaleRule[]; published_at: string; note?: string }

export async function mockRulesScale(page: Page, options: ScaleOptions = {}): Promise<ScaleMock> {
  const state = options.state ?? 'drafts'
  const calls: ScaleMock['calls'] = []
  let release = () => {}
  const held = options.holdProjectPermissions ? new Promise<void>(resolve => { release = resolve }) : null
  const layers: { id: string; scope: Record<string, string> }[] = []
  const sets: MockSet[] = []
  const versions = new Map<string, MockSnapshot[]>()
  const snapshot = (set: MockSet, version: string, rules = set.rules, note?: string): MockSnapshot => ({
    set_id: set.id, scope: set.scope, name: set.name, revision: set.revision, version, sha256: 'ab'.repeat(32), rules: rules.map(item => ({ ...item })),
    published_at: `20${version.slice(0, 2)}-${version.slice(2, 4)}-${version.slice(4, 6)}T${version.slice(6, 8)}:${version.slice(8, 10)}:${version.slice(10, 12)}Z`, ...(note ? { note } : {}),
  })
  if (state !== 'empty') {
    layers.push({ id: COMPANY_LAYER, scope: { layer: 'company' } }, { id: PROJECT_LAYER, scope: { layer: 'project', project_id: AEON_PROJECT } })
    let n = 0
    for (const [name, specs] of COMPANY) sets.push({ id: setId(++n), layer_id: COMPANY_LAYER, scope: { layer: 'company' }, name, revision: 2, rules: specs.map(spec => rule(spec, 'inspr-doctrine 0.14')), published_version: '' })
    for (const [name, specs] of PROJECT) sets.push({ id: setId(++n), layer_id: PROJECT_LAYER, scope: { layer: 'project', project_id: AEON_PROJECT }, name, revision: 2, rules: specs.map(spec => rule(spec, 'aeon/AGENTS.md')), published_version: '' })
    const publishAll = state === 'live' ? sets : sets.slice(0, 3)
    for (const set of publishAll) {
      set.published_version = '260926090000.0.0'
      // Cross-repo authoring was edited after its publication: one rule is new since.
      const live = state === 'drafts' && set.name === 'Cross-repo authoring' ? set.rules.slice(0, 3) : set.rules
      const history = [snapshot(set, '260926090000.0.0', live, 'Adopted INSPR doctrine 0.14.')]
      if (set.name === 'Secrets') history.push(snapshot(set, '260920100000.0.0', live.slice(0, 3)))
      versions.set(set.id, history)
    }
  }
  const permissionsFor = (projectId?: string) => {
    const answer = mockEffectivePermissions('admin', projectId)
    answer.workspace.permissions = [...answer.workspace.permissions, 'rules.read', 'rules.write', ...(options.publish === false ? [] : ['rules.publish'])]
    return answer
  }
  const fulfil = (route: Route, json: unknown, status = 200) => route.fulfill({ status, json })
  await page.route('**/api/**', async route => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const method = request.method()
    const body = ['POST', 'PUT', 'PATCH'].includes(method) ? request.postDataJSON() : undefined
    if (path.startsWith('/api/rules') || path === '/api/me/permissions') calls.push({ method, path: path + url.search, body })
    if (path === '/api/me' && options.kind === 'agent') {
      return fulfil(route, { principal: { id: SCALE_AGENT, name: 'Worker', kind: 'agent' }, tenant: { id: SCALE_TENANT, name: 'INSPR Studio' } })
    }
    if (path === '/api/me/permissions') {
      const project = url.searchParams.get('project_id') ?? undefined
      if (project && held) await held
      return fulfil(route, permissionsFor(project))
    }
    if (path === '/api/projects' && method === 'GET') {
      const project = (id: string, key: string, title: string) => ({ id, key, title, state: 'active', open: 1, in_progress: 0, done: 0, cancelled: 0, total: 1, last_activity: '2026-09-28T10:00:00Z', people: [] })
      return fulfil(route, { items: [project(NUNCID_PROJECT, 'NUN', 'Nuncid'), project(AEON_PROJECT, 'AEON', 'Aeon')] })
    }
    if (path === '/api/members' && method === 'GET') {
      return fulfil(route, {
        people: [{ principal_id: SCALE_PERSON, name: 'Markus Barta', avatar_url: null, has_avatar: false, email: 'markus@barta.com', status: 'active', identity: 'inspr_id', workspace_role: null, project_roles: [], aliases: [], classic_role: null, last_active_at: null, last_owner: true }],
        agents: [{ principal_id: SCALE_AGENT, name: 'Worker', has_avatar: false, workspace_role: null, key_count: 1, last_seen_at: null, service: false }],
        invites: [], imported: [], owner_count: 1,
      })
    }
    if (!path.startsWith('/api/rules')) return route.fallback()
    if (path === '/api/rules/layers' && method === 'GET') return fulfil(route, { layers })
    if (path === '/api/rules/layers' && method === 'POST') {
      const layer = { id: `c1000000-0000-4000-8000-${String(layers.length + 1).padStart(12, '0')}`, scope: body }
      layers.push(layer)
      return fulfil(route, layer)
    }
    if (path === '/api/rules/sets' && method === 'GET') return fulfil(route, { sets: sets.filter(set => set.layer_id === url.searchParams.get('layer_id')) })
    if (path === '/api/rules/sets' && method === 'POST') {
      const layer = layers.find(item => item.id === body.layer_id)
      const created: MockSet = { id: `e1000000-0000-4000-8000-${String(sets.length + 1).padStart(12, '0')}`, layer_id: body.layer_id, scope: layer?.scope ?? { layer: 'company' }, name: body.name, revision: 1, rules: [], published_version: '' }
      sets.push(created)
      return fulfil(route, created)
    }
    const one = /^\/api\/rules\/sets\/([^/]+)(?:\/(draft|publish|restore|versions)(?:\/([^/]+))?)?$/.exec(path)
    const set = one ? sets.find(item => item.id === one[1]) : undefined
    if (one && !set) return fulfil(route, { error: 'rule resource unavailable', code: 'not_found' }, 404)
    if (one && set) {
      const [, , action, version] = one
      if (!action && method === 'GET') return fulfil(route, set)
      if (action === 'draft' && method === 'PUT') {
        if (body.expected_revision !== set.revision) return fulfil(route, { error: 'draft changed', code: 'revision_conflict' }, 409)
        set.revision += 1
        set.name = body.name
        set.rules = body.rules
        return fulfil(route, set)
      }
      if (action === 'versions' && method === 'GET' && version) {
        const found = versions.get(set.id)?.find(item => item.version === decodeURIComponent(version))
        return found ? fulfil(route, found) : fulfil(route, { error: 'unavailable', code: 'not_found' }, 404)
      }
      if (action === 'versions' && method === 'GET') return fulfil(route, { versions: versions.get(set.id) ?? [] })
      if (action === 'publish' && method === 'POST') {
        const snap = snapshot(set, body.version, set.rules, body.note)
        versions.set(set.id, [snap, ...(versions.get(set.id) ?? [])])
        set.published_version = body.version
        return fulfil(route, snap)
      }
      if (action === 'restore' && method === 'POST') {
        const prior = versions.get(set.id)?.find(item => item.version === body.version)
        set.rules = (prior?.rules ?? set.rules).map(item => ({ ...item }))
        set.revision += 1
        const snap = snapshot(set, body.new_version, set.rules, body.note)
        versions.set(set.id, [snap, ...(versions.get(set.id) ?? [])])
        set.published_version = body.new_version
        return fulfil(route, snap)
      }
    }
    if (path === '/api/rules/publish' && method === 'POST') {
      if (options.kind === 'agent') return fulfil(route, { error: 'permission or scoped ownership denied', code: 'forbidden' }, 403)
      if (options.batchFailure) return fulfil(route, options.batchFailure, options.batchFailure.status)
      const items = body.items as { set_id: string; expected_revision: number; version: string }[]
      for (const item of items) {
        const target = sets.find(entry => entry.id === item.set_id)
        if (!target) return fulfil(route, { error: 'unavailable', code: 'not_found' }, 404)
        if (target.revision !== item.expected_revision) return fulfil(route, { error: `draft revision does not match for set ${target.name}`, code: 'revision_conflict' }, 409)
      }
      const version = '260928213000.0.0'
      const out = items.map(item => {
        const target = sets.find(entry => entry.id === item.set_id)!
        const snap = snapshot(target, item.version === 'auto' ? version : item.version, target.rules, body.note)
        versions.set(target.id, [snap, ...(versions.get(target.id) ?? [])])
        target.published_version = snap.version
        return snap
      })
      return fulfil(route, { batch_id: '8a0f0c3e-5d1b-8e2a-9c4f-0b1d2e3f4a5b', versions: out, max_bytes: 6120 })
    }
    if (path === '/api/rules/merged' && method === 'GET') {
      const live = sets.filter(item => item.published_version && (item.scope.layer === 'company' || item.scope.project_id === url.searchParams.get('project_id')))
      const chosen = new Map<string, ScaleRule>()
      for (const item of live) for (const entry of versions.get(item.id)?.[0]?.rules ?? []) if (!chosen.has(entry.identity)) chosen.set(entry.identity, entry)
      const text = `# Aeon session rules\n\n${[...chosen.keys()].sort().filter(key => chosen.get(key)!.enabled).map(key => `- [${key}] ${chosen.get(key)!.text}\n`).join('')}`
      return fulfil(route, {
        context: { tenant_id: SCALE_TENANT, project_id: url.searchParams.get('project_id'), person_id: url.searchParams.get('person_id'), role: url.searchParams.get('role'), harness: url.searchParams.get('harness') },
        versions: live.map(item => ({ set_id: item.id, version: item.published_version, sha256: 'ab'.repeat(32) })),
        version: live.length ? '260926090000.0.0' : 'floor-only', sha256: 'cd'.repeat(32), body: text, byte_size: Buffer.byteLength(text), rules: [], floor: live.length ? 'locked company floor' : '', valid_until: null,
      })
    }
    return fulfil(route, { error: 'unexpected rules call', code: 'not_found' }, 404)
  })
  return { calls, releasePermissions: () => release() }
}
