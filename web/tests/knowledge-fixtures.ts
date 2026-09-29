// SPDX-License-Identifier: AGPL-3.0-only
// An in-memory /api/knowledge with the server's rules: list and search (title,
// slug, text), resolve by slug following renames, create with unique slugs,
// PATCH and DELETE with If-Unmodified-Since (412 carries the current entry), and
// undo of knowledge events. Install after mockWork: its routes win.
import type { Page, Route } from '@playwright/test'
import { me } from './work-fixtures'

const mira = { id: '22222222-2222-4222-8222-222222222222', name: 'Mira Holm' }
const now = Date.parse('2026-09-23T12:00:00Z')
const ago = (hours: number) => new Date(now - hours * 3_600_000).toISOString()
type Type = 'runbook' | 'guideline' | 'memory' | 'external-system' | 'related-project'
type Status = 'active' | 'proposed' | 'archived'
const PROJECTS: Record<string, { id: string; key: string; title: string; prefix: string }> = {
  'p-pharos': { id: 'p-pharos', key: 'PRJ-17', title: 'Pharos', prefix: 'PHAROS' },
  'p-aeon': { id: 'p-aeon', key: 'PRJ-35', title: 'Aeon', prefix: 'AEON' },
}
export interface MockLink { relation_id: string; type: string; direction: 'out' | 'in'; node: { id: string; key: string; title: string; state: string; kind: string; project_id: string | null; slug?: string; type?: Type } }
export interface MockEntry {
  id: string; key: string; type: Type; slug: string; title: string; body: string; status: Status; project: string
  metadata: Record<string, unknown>; created_at: string; updated_at: string
  updated_by: { id: string; name: string } | null; author: { id: string; name: string } | null; imported: boolean
  links: MockLink[]; deleted?: boolean
}

const DEPLOY = [
  '# Deploy a release to production',
  '',
  'Every Pharos release goes out the same way: build once, pin the image, roll out host by host, and keep the previous tag one command away.',
  '',
  '## Before you start',
  '',
  '- [x] CI is green on the release commit',
  '- [ ] The release notes name every ticket',
  '- [ ] Nobody else is deploying right now',
  '',
  '> Never deploy on a Friday after 15:00 unless a fix cannot wait.',
  '',
  '## Build and pin the image',
  '',
  '```sh',
  'just release-check',
  'just build',
  'docker push ghcr.io/inspr-at/pharos:${VERSION}',
  '```',
  '',
  'Pin the new tag in `hosts/csb1/pharos.nix` and open a pull request. The pin is the only change in it.',
  '',
  '## Roll out',
  '',
  '### Canary on csb1',
  '',
  'Switch csb1 first and watch the beacon health probes for ten minutes. A single red probe stops the rollout.',
  '',
  '### The rest of the fleet',
  '',
  'Switch the remaining hosts one at a time and stop at the first failing probe.',
  '',
  '| Host | Role | Window |',
  '|---|---|---|',
  '| csb1 | canary | any time |',
  '| hsb1 | production | after 18:00 |',
  '| hsb8 | production | after 18:00 |',
  '',
  '## Roll back',
  '',
  'Revert the pin commit and switch the host again. The previous image is still cached on every host, so a rollback takes under a minute. If the host does not come back, follow [the recovery runbook](#after-the-deploy) and page whoever is on call.',
  '',
  '## After the deploy',
  '',
  'Post the version and the tickets it closes in the release thread, then mark the tickets delivered. Update [the status page](https://status.example.com) if the release changed anything customers see.',
].join('\n')

export function knowledgeWorld(options: { empty?: boolean } = {}) {
  const entries: MockEntry[] = []
  const add = (entry: Partial<MockEntry> & Pick<MockEntry, 'id' | 'key' | 'type' | 'slug' | 'title'>) => {
    const full: MockEntry = { body: '', status: 'active', project: 'p-pharos', metadata: {}, created_at: ago(24 * 60), updated_at: ago(30), updated_by: me, author: me, imported: false, links: [], ...entry }
    entries.push(full)
    return full
  }
  if (!options.empty) {
    const rotate = add({ id: 'k-rotate', key: 'PHAROS-41', type: 'runbook', slug: 'rotate-host-keys', title: 'Rotate the fleet host keys', updated_at: ago(26), updated_by: mira, author: mira,
      body: 'Host keys rotate every 90 days, or at once when a laptop with fleet access goes missing.\n\n## Steps\n\n1. Generate the new keys on the bastion.\n2. Push them with `just fleet-keys`.\n3. Remove the old keys from `authorized_keys` after one day.\n\n## Check\n\nLog in to every host with the new key before the old one goes.' })
    add({ id: 'k-deploy', key: 'PHAROS-40', type: 'runbook', slug: 'deploy-release', title: 'Deploy a release to production', body: DEPLOY, updated_at: ago(2), updated_by: mira, author: me,
      metadata: { related_agents: ['camy', 'kite'] },
      links: [
        { relation_id: 'r-k1', type: 'cites', direction: 'in', node: { id: 'n-1', key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning', state: 'in-progress', kind: 'ticket', project_id: 'p-pharos' } },
        { relation_id: 'r-k2', type: 'relates', direction: 'out', node: { id: 'n-5', key: 'PHAROS-15', title: 'Beacon health probes', state: 'done', kind: 'ticket', project_id: 'p-pharos' } },
        { relation_id: 'r-k3', type: 'relates', direction: 'out', node: { id: rotate.id, key: rotate.key, title: rotate.title, state: 'backlog', kind: 'runbook', project_id: 'p-pharos', slug: rotate.slug, type: 'runbook' } },
      ] })
    add({ id: 'k-recover', key: 'PHAROS-39', type: 'runbook', slug: 'recover-host', title: 'Recover a host from backup', status: 'archived', updated_at: ago(24 * 40), imported: true, updated_by: null, author: null,
      body: 'Superseded by the image-based rebuild. Kept for the old hosts that still run the previous layout.' })
    add({ id: 'k-edges', key: 'PHAROS-44', type: 'guideline', slug: 'no-edge-accents', title: 'No coloured edge accents', updated_at: ago(48),
      metadata: { rule: 'Never mark selection or state with a coloured bar on the left or top edge; use a tint, an outline or elevation.' },
      body: 'The coloured edge is the most common tell of generated interfaces. It also fails people who do not see colour well.\n\n## Instead\n\n- A subtle full tint for selection\n- A hairline ring for focus and the open item\n- Type weight for emphasis' })
    add({ id: 'k-words', key: 'PHAROS-45', type: 'guideline', slug: 'plain-words', title: 'Write for people, not for the log', updated_at: ago(24 * 6),
      metadata: { rule: 'Say what happened and what to do next, in words a customer would use.' },
      body: 'Messages name the thing and the next step. No codes, no stack traces, no blame.' })
    add({ id: 'k-token', key: 'PHAROS-46', type: 'memory', slug: 'hetzner-token-expiry', title: 'Hetzner API tokens expire after 90 days', status: 'proposed', updated_at: ago(5), updated_by: { id: 'agent-camy', name: 'camy' }, author: { id: 'agent-camy', name: 'camy' },
      metadata: { confidence: 'high' }, body: 'Provisioning failed with 401 on 12 September: the project token had expired. Rotate it with the host keys, and set a reminder at day 80.' })
    add({ id: 'k-review', key: 'PHAROS-47', type: 'memory', slug: 'ui-review-screenshots', title: 'UI reviews happen on screenshots at five widths', updated_at: ago(24 * 3), metadata: { confidence: 'medium' },
      body: 'Every screen is reviewed at 1920, 1440, 1280, 1024 and 390 pixels, light and dark, before it is called done.' })
    add({ id: 'k-hetzner', key: 'PHAROS-48', type: 'external-system', slug: 'hetzner', title: 'Hetzner Cloud', updated_at: ago(24 * 9),
      metadata: { url: 'https://console.hetzner.cloud', purpose: 'Provisioning, DNS and firewalls', secret_path: '1Password: Studio / Hetzner API' },
      body: 'The fleet runs on Hetzner Cloud in Falkenstein and Helsinki. Every host is created through Pharos, never by hand in the console.' })
    add({ id: 'k-aeon', key: 'PHAROS-49', type: 'related-project', slug: 'aeon', title: 'Aeon', updated_at: ago(24 * 12),
      metadata: { instance_url: 'https://pm.example.com', key: 'AEON', relationship: 'Runs on the Pharos fleet' }, body: 'Aeon is deployed by Pharos to csb1 and hsb1.' })
    add({ id: 'k-aeon-deploy', key: 'AEON-60', type: 'runbook', slug: 'deploy-release', title: 'Ship an Aeon release', project: 'p-aeon', updated_at: ago(8),
      body: 'Tag the release, let CI build the image, then ask Pharos to deploy it. Aeon never deploys itself.' })
    add({ id: 'k-aeon-cutover', key: 'AEON-61', type: 'guideline', slug: 'no-cutover-without-approval', title: 'No cutover without explicit approval', project: 'p-aeon', updated_at: ago(24 * 2),
      metadata: { rule: 'The classic app stays untouched until its owner approves a cutover.' }, body: 'Imports read from the classic app through its API only.' })
  }
  return {
    entries,
    events: [] as { id: number; node_id: string; type: string; before: MockEntry | null; after: MockEntry; undo_of: number | null }[],
    renames: { 'deploy-flow': 'k-deploy' } as Record<string, string>,
    counter: { next: 90, event: 500, clock: 0 },
    learnings: [] as MockLearning[],
    decisions: [] as { event_id: number; item: MockLearning }[],
  }
}
export interface MockLearning {
  id: string; source: 'ticket' | 'comment'; node_id: string; key: string; title: string; text: string
  comment_id?: string; at: string; author: { id: string; name: string } | null; href: string
  // Mock only: the server answers 409 learning_sensitive with these ranges
  // until a person confirms.
  sensitive?: { field: 'text' | 'title'; start: number; end: number }[]
}
export type KnowledgeWorld = ReturnType<typeof knowledgeWorld>
export interface KnowledgeMockOptions {
  // Someone else saves this entry just before this page's next write.
  conflictOn?: string
  readOnly?: boolean
  failList?: boolean
  delayList?: number
}

const words = (q: string) => q.toLowerCase().split(/\s+/).filter(w => w.length >= 2)
// Like the server: a heading leads in with a colon, list items are separated by commas.
function plain(body: string) {
  const lines = body.replace(/```[\s\S]*?```/g, '\n').replace(/^\s*\|?\s*:?-{2,}.*$/gm, '').split('\n')
  const parts: string[] = []
  for (const raw of lines) {
    const heading = /^\s{0,3}#{1,6}\s+/.test(raw), item = /^\s*(?:[-*+]|\d+[.)])\s+/.test(raw)
    let line = raw.replace(/^\s{0,3}#{1,6}\s+/, '').replace(/^\s*(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?/, '').replace(/^\s*>\s?/, '')
      .replace(/\|/g, ' ').replace(/\[([^\]]+)\]\([^)]*\)/g, '$1').replace(/\*\*|`/g, '').replace(/\s+/g, ' ').trim()
    if (!line) continue
    if (!/[.:!?;,]$/.test(line)) line += heading ? ':' : item ? ',' : ''
    parts.push(line)
  }
  return parts.join(' ').replace(/,$/, '')
}
function excerpt(entry: MockEntry, q: string) {
  const heading = /^\s*#{1,6}\s+(.+?)\s*(?:\n|$)/.exec(entry.body)
  let text = plain(heading && heading[1].toLowerCase() === entry.title.toLowerCase() ? entry.body.slice(heading[0].length) : entry.body)
  const first = words(q).map(w => text.toLowerCase().indexOf(w)).find(i => i >= 0) ?? -1
  if (first > 60) text = `…${text.slice(first - 60)}`
  return text.length > 200 ? `${text.slice(0, 200).replace(/\s+\S*$/, '')}…` : text
}
function item(entry: MockEntry, q = '') {
  const project = PROJECTS[entry.project]
  return {
    id: entry.id, key: entry.key, type: entry.type, kind: entry.type.replace('-', '_'), slug: entry.slug, title: entry.title, status: entry.status,
    state: entry.status === 'archived' ? 'cancelled' : entry.status === 'proposed' ? 'proposed' : 'backlog',
    project: { id: project.id, key: project.key, title: project.title }, excerpt: excerpt(entry, q), link_count: entry.links.length,
    created_at: entry.created_at, updated_at: entry.updated_at, updated_by: entry.imported ? null : entry.updated_by, imported: entry.imported,
  }
}
function full(entry: MockEntry, extra: Record<string, unknown> = {}) {
  return { ...item(entry), body: entry.body, metadata: entry.metadata, author: entry.author, links: entry.links, ...extra }
}
function matches(entry: MockEntry, q: string) {
  const hay = `${entry.title} ${entry.slug} ${entry.key} ${entry.body}`.toLowerCase()
  const list = words(q)
  return !q.trim() || (list.length ? list.every(w => hay.includes(w)) : hay.includes(q.trim().toLowerCase()))
}
const json = (route: Route, status: number, body: unknown) => route.fulfill({ status, json: body })

export async function mockKnowledge(page: Page, world: KnowledgeWorld, options: KnowledgeMockOptions = {}) {
  const calls: { path: string; method: string; query: URLSearchParams; body: unknown; headers: Record<string, string> }[] = []
  let conflicted = false
  const tick = () => new Date(now + 60_000 * ++world.counter.clock).toISOString()
  const live = () => world.entries.filter(entry => !entry.deleted)
  const record = (type: string, before: MockEntry | null, after: MockEntry) => {
    const event = { id: ++world.counter.event, node_id: after.id, type, before: before ? structuredClone(before) : null, after: structuredClone(after), undo_of: null as number | null }
    world.events.push(event)
    return event.id
  }
  await page.route('**/api/knowledge**', async route => {
    const request = route.request(), url = new URL(request.url()), method = request.method(), query = url.searchParams
    let body: unknown = null
    try { body = request.postDataJSON() } catch { body = null }
    calls.push({ path: url.pathname, method, query, body, headers: request.headers() })
    const parts = url.pathname.replace(/^\/api\/knowledge\/?/, '').split('/').filter(Boolean)
    if (parts[0] === 'learnings') {
      if (method === 'GET') return json(route, 200, { items: world.learnings.map(({ sensitive: _sensitive, ...item }) => item), truncated: false })
      const id = decodeURIComponent(parts[1] ?? '')
      const found = world.learnings.find(item => item.id === id)
      if (!found) return json(route, 404, { error: 'This learning is no longer open.', code: 'learning_closed' })
      if (options.readOnly) return json(route, 403, { error: 'you can read knowledge but not change it', code: 'forbidden' })
      if (parts[2] === 'dismiss' && method === 'POST') {
        const eventId = ++world.counter.event
        world.decisions.push({ event_id: eventId, item: found })
        world.learnings = world.learnings.filter(item => item.id !== id)
        return json(route, 200, { id, decision: 'dismissed', event_id: eventId })
      }
      if ((parts[2] === 'accept' || parts[2] === 'draft') && method === 'POST' && found.sensitive?.length && (body as { confirm_not_sensitive?: boolean } | null)?.confirm_not_sensitive !== true) {
        return json(route, 409, { error: 'This looks like a credential — remove it, or confirm it is not one.', code: 'learning_sensitive', ranges: found.sensitive })
      }
      if (parts[2] === 'accept' && method === 'POST') {
        const input = body as { knowledge_id?: string }
        const entry = world.entries.find(candidate => candidate.id === input.knowledge_id && !candidate.deleted)
        if (!entry) return json(route, 404, { error: 'knowledge entry not found', code: 'not_found' })
        const expected = request.headers()['if-unmodified-since']
        if (expected && expected !== entry.updated_at) return json(route, 412, { error: 'the entry changed since you opened it', code: 'stale', entry: full(entry) })
        const before = structuredClone(entry)
        const date = new Date().toISOString().slice(0, 10)
        const line = `- ${date}: ${found.text}. Source: [${found.key}](${found.href}).`
        entry.body = `${entry.body.replace(/\s*$/, '')}\n\n${line}\n`
        entry.updated_at = tick()
        entry.updated_by = me
        entry.imported = false
        const eventId = record('knowledge.learning_accepted', before, entry)
        world.decisions.push({ event_id: eventId, item: found })
        world.learnings = world.learnings.filter(item => item.id !== id)
        return json(route, 200, { id, decision: 'accepted', event_id: eventId, knowledge_id: entry.id, heading: 'Changelog', line, entry: full(entry, { event_id: eventId }) })
      }
      if (parts[2] === 'draft' && method === 'POST') {
        const input = body as { layer_id?: string; set_id?: string }
        const eventId = ++world.counter.event
        world.decisions.push({ event_id: eventId, item: found })
        world.learnings = world.learnings.filter(item => item.id !== id)
        const identity = found.source === 'comment' ? `learn.c.${found.node_id}.${found.comment_id}` : `learn.n.${found.node_id}`
        return json(route, 200, { id, decision: 'drafted', event_id: eventId, rule_set_id: input.set_id, rule_layer_id: input.layer_id, rule_identity: identity })
      }
      return json(route, 400, { error: 'invalid request', code: 'invalid_request' })
    }
    if (!parts.length && method === 'GET') {
      if (options.failList) return json(route, 503, { error: 'knowledge is resting', code: 'internal' })
      if (options.delayList) await new Promise(resolve => setTimeout(resolve, options.delayList))
      const q = query.get('q') ?? '', project = query.get('project_id')
      const types = (query.get('type') ?? '').split(',').filter(Boolean), statuses = (query.get('status') ?? '').split(',').filter(Boolean)
      const scope = live().filter(entry => (!project || entry.project === project) && matches(entry, q))
      const counts = { type: {} as Record<string, number>, status: {} as Record<string, number> }
      for (const entry of scope) { counts.type[entry.type] = (counts.type[entry.type] ?? 0) + 1; counts.status[entry.status] = (counts.status[entry.status] ?? 0) + 1 }
      const kept = scope.filter(entry => (!types.length || types.includes(entry.type)) && (!statuses.length || statuses.includes(entry.status)))
      const score = (entry: MockEntry) => (entry.slug === q.trim() ? 100 : 0) + (entry.title.toLowerCase().includes(q.trim().toLowerCase()) ? 40 : 0)
      const sorted = [...kept].sort((a, b) => (q ? score(b) - score(a) : 0) || Date.parse(b.updated_at) - Date.parse(a.updated_at))
      const limit = Number(query.get('limit') ?? 500)
      return json(route, 200, { items: sorted.slice(0, limit).map(entry => item(entry, q)), total: sorted.length, truncated: sorted.length > limit, counts })
    }
    if (parts[0] === 'resolve') {
      const project = query.get('project_id'), type = query.get('type'), slug = query.get('slug') ?? ''
      const found = live().find(entry => entry.project === project && entry.type === type && entry.slug === slug)
      if (found) return json(route, 200, full(found))
      const renamed = world.renames[slug] ? live().find(entry => entry.id === world.renames[slug] && entry.project === project && entry.type === type) : undefined
      if (renamed) return json(route, 200, full(renamed, { renamed_from: slug }))
      return json(route, 404, { error: 'knowledge entry not found', code: 'not_found' })
    }
    if (!parts.length && method === 'POST') {
      if (options.readOnly) return json(route, 403, { error: 'you can read knowledge but not change it', code: 'forbidden' })
      const input = body as { project_id: string; type: Type; slug: string; title: string; body?: string; key_prefix?: string }
      const taken = live().find(entry => entry.project === input.project_id && entry.type === input.type && entry.slug === input.slug)
      if (taken) return json(route, 409, { error: `another ${input.type} in this project already uses the slug ${input.slug}`, code: 'slug_taken', conflict: item(taken) })
      const at = tick()
      const entry: MockEntry = { id: `k-new-${world.counter.next}`, key: `${input.key_prefix ?? 'RUN'}-${world.counter.next++}`, type: input.type, slug: input.slug, title: input.title, body: input.body ?? '', status: 'active',
        project: input.project_id, metadata: {}, created_at: at, updated_at: at, updated_by: me, author: me, imported: false, links: [] }
      world.entries.push(entry)
      return json(route, 201, full(entry, { event_id: record('knowledge.created', null, entry) }))
    }
    const entry = world.entries.find(candidate => candidate.id === decodeURIComponent(parts[0]))
    if (!entry || entry.deleted) return json(route, 404, { error: 'knowledge entry not found', code: 'not_found' })
    if (method === 'GET') return json(route, 200, full(entry))
    if (options.readOnly) return json(route, 403, { error: 'you can read knowledge but not change it', code: 'forbidden' })
    if (options.conflictOn === entry.id && !conflicted) {
      conflicted = true
      entry.body = `${entry.body}\n\nMira added this line while you were editing.`
      entry.title = entry.title.includes('(checked)') ? entry.title : `${entry.title} (checked)`
      entry.updated_at = tick(); entry.updated_by = mira
    }
    const expected = request.headers()['if-unmodified-since']
    if (expected && expected !== entry.updated_at) return json(route, 412, { error: 'the entry changed since you opened it', code: 'stale', entry: full(entry) })
    if (method === 'DELETE') {
      const before = structuredClone(entry)
      entry.deleted = true; entry.updated_at = tick()
      return json(route, 200, { id: entry.id, event_id: record('knowledge.deleted', before, entry) })
    }
    const patch = body as { title?: string; body?: string; status?: Status; slug?: string; metadata?: Record<string, unknown> }
    if (patch.slug && patch.slug !== entry.slug && live().some(other => other.id !== entry.id && other.project === entry.project && other.type === entry.type && other.slug === patch.slug)) {
      return json(route, 409, { error: `another ${entry.type} in this project already uses the slug ${patch.slug}`, code: 'slug_taken', conflict: item(live().find(other => other.slug === patch.slug)!) })
    }
    const before = structuredClone(entry)
    if (patch.slug && patch.slug !== entry.slug) world.renames[entry.slug] = entry.id
    Object.assign(entry, Object.fromEntries(Object.entries(patch).filter(([, value]) => value !== undefined)), { updated_at: tick(), updated_by: me, imported: false })
    return json(route, 200, full(entry, { event_id: record('knowledge.updated', before, entry) }))
  })
  await page.route('**/api/events/*/undo', async route => {
    const id = Number(new URL(route.request().url()).pathname.split('/')[3])
    const decision = world.decisions.find(candidate => candidate.event_id === id)
    if (decision) {
      world.learnings = [decision.item, ...world.learnings.filter(item => item.id !== decision.item.id)]
      world.decisions = world.decisions.filter(candidate => candidate.event_id !== id)
    }
    const event = world.events.find(candidate => candidate.id === id)
    if (!event) {
      if (decision) return json(route, 201, { id: ++world.counter.event })
      return route.fallback()
    }
    if (!event) return route.fallback()
    if (world.events.some(candidate => candidate.undo_of === id)) return json(route, 409, { error: 'conflict' })
    const entry = world.entries.find(candidate => candidate.id === event.node_id)!
    if (entry.updated_at !== event.after.updated_at) return json(route, 409, { error: 'conflict' })
    if (event.type === 'knowledge.created') entry.deleted = true
    else Object.assign(entry, structuredClone(event.before!), { deleted: false })
    entry.updated_at = tick()
    world.events.push({ id: ++world.counter.event, node_id: entry.id, type: event.type, before: event.after, after: structuredClone(entry), undo_of: id })
    return json(route, 201, { id: world.counter.event })
  })
  return calls
}
