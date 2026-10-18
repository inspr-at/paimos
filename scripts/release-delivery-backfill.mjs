// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1481: read-only inventory. Deliberately has no apply/write mode.
import { execFileSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const day = 86400000
const normal = state => String(state).trim().toLowerCase().replace(/[\s-]+/g, '_')
const keys = text => [...new Set(text.match(/\b[A-Z][A-Z0-9]{1,9}-[1-9]\d{0,6}\b/g) ?? [])]
const validVersion = version => /^\d{12}\.0\.0$/.test(version)

export function latestMerges(log) {
  const result = new Map()
  for (const record of log.split('\x1e')) {
    const [sha, subject, body] = record.trim().split('\x1f')
    if (!sha || body === undefined) continue
    const title = body.trim().split('\n')[0]
    for (const key of keys(`${subject.toUpperCase()}\n${title}`)) if (!result.has(key)) result.set(key, sha)
  }
  return result
}

export function planBackfill({ nodes, merges, membership, live, now, settings }) {
  const rows = []
  for (const node of nodes) {
    if (!node.is_leaf || normal(node.state) !== 'done') continue
    const sha = merges.get(node.key), release = membership.get(sha)
    const proof = release && live.filter(p => p.version >= release.version).sort((a, b) => Date.parse(a.since) - Date.parse(b.since))[0]
    let action = 'unchanged', reason = ''
    if (!sha) reason = 'No merge key on the current main first-parent history'
    else if (!release || !proof) reason = 'Newest merge is outside published + confirmed-live ancestry'
    else if (node.human_check?.trim()) reason = `Human check: ${node.human_check.trim()}; OPS would record a skip comment`
    else if (node.fields?.no_release_needed === true) reason = 'no_release_needed: separate existing acceptance policy'
    else if (!settings.enabled || !settings.rules.publish.enabled || settings.server_mode === 'off') reason = 'Publication policy disabled'
    else if (settings.server_mode === 'suggest' || Date.parse(settings.suggest_until) > Date.parse(now)) reason = 'Publication policy is in Suggest mode'
    else {
      action = 'Delivered'
      if (settings.rules.accept.enabled && Date.parse(now) >= Date.parse(proof.since) + settings.rules.accept.days * day && !node.fields?.delivery_objection) action = 'Delivered → Accepted (objection recheck required)'
      reason = `Live no later than ${proof.since}`
      if (node.fields?.delivery_objection) reason += '; acceptance blocked by delivery_objection'
    }
    rows.push({ key: node.key, id: node.id, revision: node.updated_at, sha, release, action, reason })
  }
  return rows.sort((a, b) => Number(a.key.split('-').at(-1)) - Number(b.key.split('-').at(-1)))
}

const opsReadback = "### CLI and SQL readback for OPS\n\nRegenerate with existing authentication (the command has no apply mode):\n\n```sh\ngit fetch origin main --tags\nnode scripts/release-delivery-backfill.mjs \\\n  --project 23e3f45b-27e4-43c7-985b-bb0c66179fb3 \\\n  --live 261016060843.0.0=2026-10-16T07:57:00Z\n```\n\nWith OPS's established tenant/project visibility, this parameterized read-only check must retain exactly one row from the approved inventory before a write is considered. Recheck the newest immutable merge using the union in `gitMemberTx`; this readback alone authorizes no change.\n\n```sql\nBEGIN READ ONLY;\nSELECT set_config('aeon.tenant_id', :'tenant_id', true);\nSELECT n.id, n.key, n.updated_at, n.human_check\nFROM nodes n\nWHERE n.tenant_id = :'tenant_id'::uuid\n  AND n.project_id = :'project_id'::uuid\n  AND n.id = :'node_id'::uuid\n  AND n.updated_at = :'captured_revision'::timestamptz\n  AND n.deleted_at IS NULL\n  AND regexp_replace(lower(btrim(n.state)), '[[:space:]-]+', '_', 'g') = 'done'\n  AND NOT EXISTS (\n    SELECT 1 FROM nodes child\n    JOIN node_kinds k ON k.tenant_id = child.tenant_id AND k.id = child.kind_id\n    WHERE child.tenant_id = n.tenant_id AND child.parent_id = n.id\n      AND child.deleted_at IS NULL AND k.slug = 'work'\n  );\nROLLBACK;\n```\n"

function render(rows, { now, history, main, live, project, repository, settings }) {
  const delivered = rows.filter(r => r.action === 'Delivered').length
  const accepted = rows.filter(r => r.action.startsWith('Delivered →')).length
  const human = rows.filter(r => r.reason.startsWith('Human check:')).length
  const text = ['# AEON-1481 — release delivery backfill (DRY RUN)', '',
    `Read at ${now}. Repository ${repository}; project ${project}; main ${main}.`, '',
    '**No production writes were performed. This command has no apply mode.**', '',
    `Running release: ${history.current}; server live since ${history.live_since}.`,
    `Policy: publish ${settings.rules.publish.enabled ? 'on' : 'off'}; accept ${settings.rules.accept.enabled ? 'on' : 'off'} after ${settings.rules.accept.days} day(s).`, '',
    `Counts: ${rows.length} current Done leaves inspected; ${delivered + accepted} would become Delivered; ${accepted} of those are old enough for Accepted after the objection recheck; ${human} human checks stay Done; ${rows.length - delivered - accepted - human} other unchanged.`, '',
    'Confirmed-live evidence (older ancestry inherits the conservative live bound of the confirming release):', '',
    ...live.map(p => `- ${p.version}: ${p.since} (${p.source})`), '',
    'Membership uses all commits between consecutive actually published tags, including merge commits. The newest merge is selected from current GitHub main, using the PR title/branch or squash subject. Ticket release labels and editable merge fields do not establish ancestry. Unknown keys, post-tag follow-ups and missing live evidence remain unchanged. A failed or oversized read aborts the report.', '',
    'Accepted is a conditional plan: OPS must recheck person comments, acceptance Undo and delivery_objection against the historical delivery episode in the final transaction. This inventory does not claim that the comment/Undo check has already passed. It does not backdate an event or silently accept a human check.', '',
    '## OPS execution plan — only after LEAD approval', '',
    '1. Regenerate this report immediately before the approved run. Preserve this inventory and the before-state; compare node id, revision, newest verified merge and repository with the current database under the tenant → pairing → tree fence. Discard a changed row and replan. Recheck nodes.write using an existing responsible person in the final transaction; retain System as the policy actor.',
    '2. For each proposed row, use the existing status-autopilot publication apply path and a durable anchor `git/<repository>/<release tag>`. Restrict to current Done work leaves; exclude no_release_needed and human checks. Record the release name/version/tag, merge SHA and live-proof time in the event reason and comment. Process at most 50 candidates per transaction; lock all node/principal rows before the event counter. Receipts must survive retries and Undo.',
    '3. Human-check rows stay Done. Record a skipped publication event and comment naming the required person step through the existing publicationDecision/apply path; do not clear the check or impersonate a person.',
    '4. For rows old enough for acceptance, preserve the historical live-proof instant as separately reviewed delivery evidence. Do not rewrite old events. Run the existing accept evaluator with accept.days from the current workspace policy and the historical delivery anchor, rechecking all person objections/comments and Undo since that instant. Append Delivered and Accepted events in order, using current event timestamps. If historical acceptance evidence is uncertain, leave Delivered and let the ordinary one-day rule start at the new delivery event.',
    '5. Commit only the approved bounded batch. On failure roll back that batch and report the error. Verify per-row events and counts; a retry uses the same receipt anchors. Rollback uses existing event Undo in reverse order. Never directly UPDATE states or receipt/event history.', '',
    'The deployed automatic path processes only its running tagged release. This document authorizes no historic write; an OPS implementation/run and LEAD approval remain required.', '',
    opsReadback, '',
    '## Inventory', '',
    '| Ticket | Node id / captured revision | Newest merge | First published release | Planned result / reason |',
    '| --- | --- | --- | --- | --- |',
    ...rows.map(r => `| ${r.key} | ${r.id} / ${r.revision} | ${r.sha ?? 'none'} | ${r.release ? `${r.release.name} (${r.release.version})` : 'none'} | ${r.action}: ${r.reason.replaceAll('|', '\\|').replaceAll('\n', ' ')} |`), '']
  return text.join('\n')
}

export function main(args) {
  const options = { repo: '.', repository: 'inspr-at/paimos', instance: 'ppm', aeon: '/Users/markus/.local/share/aeon-cli/aeon', out: 'docs/ops/aeon-1481-backfill.md', live: [] }
  for (let i = 0; i < args.length; i++) {
    const flag = args[i], value = args[++i]
    if (!['--repo', '--repository', '--instance', '--aeon', '--out', '--project', '--live'].includes(flag) || !value || value.startsWith('--')) throw new Error(`Unknown/missing option ${flag}; read-only command, no apply mode`)
    if (flag === '--live') options.live.push(value)
    else options[flag.slice(2)] = value
  }
  if (!/^[\da-f-]{36}$/i.test(options.project ?? '') || !/^[\w.-]+\/[\w.-]+$/.test(options.repository) || options.live.length > 50) throw new Error('--project UUID and valid repository are required; at most 50 live proofs')
  const deadline = Date.now() + 5 * 60000
  const run = (program, argv) => {
    if (Date.now() >= deadline) throw new Error('Inventory deadline exceeded; report not written')
    return execFileSync(program, argv, { encoding: 'utf8', maxBuffer: 32 << 20, timeout: Math.min(60000, deadline - Date.now()), stdio: ['ignore', 'pipe', 'pipe'] }).trim()
  }
  const api = path => JSON.parse(run(options.aeon, ['--instance', options.instance, 'curl', path])) // GET only
  const gh = path => JSON.parse(run('gh', ['api', path]))
  const git = (...argv) => run('git', ['-C', options.repo, ...argv])
  const history = api('/api/releases')
  if (!validVersion(history.current)) throw new Error('No current published calendar release')
  const now = new Date().toISOString()
  const settings = api('/api/settings/status-autopilot')
  const override = api(`/api/projects/${options.project}/status-autopilot`)
  settings.enabled = override.effective_enabled
  const main = gh(`repos/${options.repository}/git/ref/heads/main`).object.sha
  if (!/^[a-f\d]{40}$/.test(main)) throw new Error('Invalid main SHA')
  git('cat-file', '-e', `${main}^{commit}`) // refuse stale local history; operator fetches first
  const published = JSON.parse(run('gh', ['api', '--paginate', '--slurp', `repos/${options.repository}/releases?per_page=100`])).flat().filter(r => !r.draft && r.published_at && validVersion(r.tag_name?.slice(1))).sort((a, b) => a.tag_name.localeCompare(b.tag_name))
  if (published.length > 1000) throw new Error('Published-release inventory exceeds 1000')
  const membership = new Map(), releases = new Map()
  let previous
  for (const r of published) {
    const version = r.tag_name.slice(1)
    if (version > history.current) continue
    if (previous) git('merge-base', '--is-ancestor', previous, r.tag_name)
    const commits = git('rev-list', '--max-count=20001', previous ? `${previous}..${r.tag_name}` : r.tag_name).split('\n').filter(Boolean)
    if (commits.length > 20000) throw new Error('Ancestry limit exceeded; report not written')
    const historic = history.releases.find(h => h.version === version)
    const release = { version, tag: r.tag_name, name: historic?.codename ?? r.name ?? version, published: r.published_at }
    releases.set(version, release)
    for (const sha of commits) membership.set(sha, release)
    previous = r.tag_name
  }
  const live = [{ version: history.current, since: history.live_since, source: 'authenticated /api/releases current + live_since, GitHub publication verified' }]
  for (const proof of options.live) {
    const [version, since] = proof.split('=')
    live.push({ version, since, source: 'explicit OPS/LEAD live evidence supplied with --live' })
  }
  for (const p of live) {
    if (!releases.has(p.version) || !Number.isFinite(Date.parse(p.since)) || Date.parse(p.since) > Date.parse(now) || Date.parse(p.since) < Date.parse(releases.get(p.version).published)) throw new Error('Invalid/unpublished live proof')
    git('merge-base', '--is-ancestor', `v${p.version}`, `v${history.current}`)
  }
  const log = git('log', '--first-parent', '--max-count=20001', '--format=%H%x1f%s%x1f%b%x1e', main)
  if (log.split('\x1e').length > 20001) throw new Error('Main history limit exceeded')
  const merges = latestMerges(log)
  const nodes = [], seen = new Set()
  let cursor
  for (let page = 0; ; page++) {
    if (page >= 100 || nodes.length > 20000) throw new Error('Node inventory limit exceeded')
    const query = new URLSearchParams({ within: options.project, kind: 'work', shape: 'leaf', work_state: 'done', limit: '200', ...(cursor ? { cursor } : {}) })
    const result = api(`/api/nodes?${query}`)
    if (!Array.isArray(result.items)) throw new Error('Invalid node page')
    nodes.push(...result.items)
    cursor = result.next_cursor
    if (!cursor) break
    if (seen.has(cursor)) throw new Error('Repeated cursor; report not written')
    seen.add(cursor)
  }
  const rows = planBackfill({ nodes, merges, membership, live, now, settings })
  mkdirSync(dirname(options.out), { recursive: true })
  writeFileSync(options.out, render(rows, { now, history, main, live, project: options.project, repository: options.repository, settings }))
  process.stdout.write(`DRY RUN: ${rows.length} Done leaves; ${rows.filter(r => r.action.startsWith('Delivered')).length} delivery candidates; report ${options.out}\n`)
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { main(process.argv.slice(2)) } catch (error) { console.error(`Backfill aborted without production writes: ${error.message}`); process.exitCode = 1 }
}
