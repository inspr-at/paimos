// SPDX-License-Identifier: AGPL-3.0-only
// AEON-266: a screenshot audit set of the screens built or changed on 2026-09-27/28,
// for review by eye. It captures only; it asserts nothing about the pixels.
//
//   VISUAL_AUDIT=1 VISUAL_AUDIT_DIR=<dir> npx playwright test -c playwright.ui.config.ts tests/visual-audit.spec.ts --workers=2
//
// Every state is captured full-page at 1600x1000 and 390x844, light and dark, as
// <screen>__<state>__<width>__<theme>.png. Console errors, horizontal overflow and
// steps that could not be reached are written next to the shots in notes/*.json.
// VISUAL_AUDIT_FILTER=<text> limits the run to screens or states containing it.
import { mkdirSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, liveAgent, me, mockWork, type Fixtures } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'
import { mockStartAgent } from './start-agent-fixtures'
import { mockAnonymousGuide, mockPairing } from './agent-pairing-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { RULE_PERSON, RULE_PROJECT, mockRules } from './rules-fixtures'
import { journeyWorld, mockJourney, PROJECT, retryJourneyWorld, type JourneyWorld } from './journey-fixtures'
import { mockReleases, releaseHistory } from './releases-fixtures'
import { mockTicketGraph, ticketGraphWorld } from './ticket-graph-fixtures'
import { mockIndicator } from './agent-indicator-fixtures'
import type { UsageDashboard, UsageGroup } from '../src/lib/usageFormat.ts'

test.skip(process.env.VISUAL_AUDIT !== '1', 'The visual audit runs only with VISUAL_AUDIT=1.')
// The header glimpse draws with WebGL; software GL keeps it drawable headless.
test.use({ launchOptions: { args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] } })
test.describe.configure({ mode: 'parallel' })

const OUT = resolve(process.env.VISUAL_AUDIT_DIR ?? 'test-results/visual-audit')
const WIDTHS = [1600, 390] as const
const THEMES = ['light', 'dark'] as const
type Theme = typeof THEMES[number]

interface Shot {
  screen: string
  state: string
  setup: (page: Page) => Promise<void>
  act: (page: Page, width: number) => Promise<void>
  // The header glimpse animates only without reduced motion.
  motion?: boolean
}
interface Note { file: string; problems: string[] }

const visible = async (page: Page, selector: string) => { await expect(page.locator(selector).first()).toBeVisible() }
const heading = async (page: Page, name: string | RegExp, level?: number) => { await expect(page.getByRole('heading', { name, level }).first()).toBeVisible() }

// ---------------------------------------------------------------- permissions
// Admin plus the account, run and rules permissions the newer surfaces check.
async function grantAll(page: Page) {
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions = [...answer.workspace.permissions, 'account.manage', 'account.read', 'run.create', 'run.read', 'models.read', 'work_orders.read', 'rules.read', 'rules.write', 'rules.publish']
    return route.fulfill({ json: answer })
  })
}

// ---------------------------------------------------------------- agents
const AGENT_NOW = Date.parse('2026-09-28T09:30:00Z')
const agoAt = (minutes: number) => new Date(AGENT_NOW - minutes * 60_000).toISOString()
const WORLD: AgentWorld = {
  me: me.id,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}
const sessionId = (n: number) => `5e000000-0000-4000-8000-${String(n).padStart(12, '0')}`
const LEAD = sessionId(1)
const WORKER = sessionId(102)
type AgentVariant = 'busy' | 'quiet' | 'empty' | 'recovery'

function agentsWorld(variant: AgentVariant) {
  const data = agentData({ ...WORLD, now: AGENT_NOW, empty: variant === 'empty' })
  if (variant === 'empty') return data
  const meta: Record<number, Record<string, unknown>> = {
    1: { display_label: 'Release lead · September train', model: 'claude-fable-high', reasoning_effort: 'high', account_label: 'Claude Max', harness_version: '2.4.1', brief: 'AEON-266', worktree: '/Users/markus/Code/aeon-worktrees/release-lead', branch: 'work/release-lead', commits: [{ sha: '5494eb2', subject: 'Merge the private rules bootstrap' }, { sha: '2b06848', subject: 'Bind rules bootstrap receive to the selected CLI' }] },
    2: { display_label: 'oracle-connector', model: 'gpt-6-sol', reasoning_effort: 'xhigh', account_label: 'Codex Pro', harness_version: '1.2.3', brief: 'PHAROS-12', worktree: '/Users/markus/Code/pharos-worktrees/oracle-connector', branch: 'work/oracle-connector', commits: [{ sha: 'abc1234', subject: 'Store the Oracle tenancy handle' }] },
    3: { display_label: 'foundation scout', model: 'anthropic/claude-sonnet-5', reasoning_effort: 'high', account_label: 'Pi on hsb1' },
    4: { display_label: 'release-lock', model: 'composer-2.5-fast', reasoning_effort: 'medium', account_label: 'Cursor Business' },
    6: { display_label: 'approvals-cleanup', model: 'claude-fable-xhigh', reasoning_effort: 'xhigh', account_label: 'Claude Max' },
  }
  data.sessions.forEach((s, index) => Object.assign(s, meta[index + 1] ?? {}))
  const lead = data.sessions[0]!
  const child = (n: number, fields: Record<string, unknown>) => ({
    ...lead, id: sessionId(100 + n), parent_harness_session_id: lead.id, role: 'worker', run_id: null, work_order_id: null, brief: undefined, commits: [], ticket_node_id: null, ticket: null, model: undefined, reasoning_effort: undefined, account_label: undefined, worktree: undefined, branch: undefined,
    agent_principal_id: `a0000000-0000-4000-8000-0000000001${String(n).padStart(2, '0')}`, ...fields,
  }) as unknown as typeof data.sessions[number]
  const ticket = (id: string) => ({ ticket_node_id: id, ticket: { id, ...WORLD.nodes![id] } })
  data.sessions.push(
    child(1, { display_label: 'ui-audit-capture', agent: { id: 'x', name: 'aeon-coordinator' }, harness: 'claude', model: 'claude-fable-high', reasoning_effort: 'high', account_label: 'Claude Max', phase: 'working', activity: 'busy', heartbeat_at: agoAt(0.3), created_at: agoAt(38), ...ticket('n-a1') }),
    child(2, { display_label: 'permission-checks-for-cross-tenant-approvals-and-agent-keys-with-a-very-long-worker-label', agent: { id: 'x', name: 'aeon-coordinator' }, harness: 'codex', model: 'gpt-6-sol', reasoning_effort: 'xhigh', account_label: 'Codex Pro', phase: 'working', activity: 'busy', heartbeat_at: agoAt(0.5), created_at: agoAt(31), ...ticket('n-2') }),
    child(3, { display_label: 'Starting worker', agent: { id: 'x', name: 'aeon-coordinator' }, harness: 'grok', model: 'grok-4.7', reasoning_effort: 'xhigh', account_label: 'SuperGrok', phase: 'starting', activity: 'unknown', heartbeat_at: agoAt(0.1), created_at: agoAt(1) }),
    child(4, { display_label: 'tree-connectors', agent: { id: 'x', name: 'aeon-coordinator' }, harness: 'cursor', model: 'composer-2.5-fast', account_label: 'Cursor Business', phase: 'yielded', activity: 'idle', heartbeat_at: agoAt(2), created_at: agoAt(55), ...ticket('n-1') }),
    child(5, { display_label: 'metadata-history', agent: { id: 'x', name: 'aeon-coordinator' }, harness: 'claude', model: 'claude-fable-high', account_label: 'Claude Max', ...ticket('n-a1'), phase: 'stopped', activity: 'idle', heartbeat_at: agoAt(20), stopped_at: agoAt(19), stop_reason: 'completed', created_at: agoAt(70) }),
    child(6, { display_label: 'session-recovery', agent: { id: 'x', name: 'aeon-coordinator' }, harness: 'codex', model: 'gpt-6-sol', account_label: 'Codex Pro', phase: 'stopped', activity: 'idle', heartbeat_at: agoAt(44), stopped_at: agoAt(43), stop_reason: 'operator_stop', created_at: agoAt(90) }),
    child(7, { display_label: 'usage-dashboard', agent: { id: 'x', name: 'aeon-coordinator' }, harness: 'pi', model: 'anthropic/claude-sonnet-5', account_label: 'Pi on hsb1', phase: 'stopped', activity: 'idle', heartbeat_at: agoAt(61), stopped_at: agoAt(60), stop_reason: 'lease_expired', created_at: agoAt(120) }),
  )
  const keep = variant === 'quiet' ? [4, 5, 6] : [1, 2, 4, 5, 6]
  data.approvals.splice(0, data.approvals.length, ...data.approvals.filter(a => keep.includes(Number(a.id.slice(-2)))))
  if (variant === 'quiet') data.messages.splice(0, data.messages.length, ...data.messages.filter(m => !m.is_action_request && !m.expects_reply))
  if (variant === 'recovery') Object.assign(data.sessions[1]!, { management_mode: 'unmanaged', host: 'workstation-offline', display_label: 'ops', advertised_capabilities: ['status'] })
  return data
}

async function agentsSetup(page: Page, variant: AgentVariant) {
  await page.clock.setSystemTime(AGENT_NOW)
  await mockWork(page, fixtures(), { admin: true })
  await mockPairing(page)
  const data = agentsWorld(variant)
  await mockAgents(page, data)
  await grantAll(page)
  // Metadata history is only on the detail endpoint (SN1).
  await page.route(`**/api/projects/p-pharos/harness-sessions/${LEAD}`, route => route.fulfill({ json: { ...data.sessions[0], metadata_history: [
    { field: 'model', previous_value: 'claude-opus-high', value: 'claude-fable-high', at: agoAt(50) },
    { field: 'reasoning_effort', previous_value: 'medium', value: 'high', at: agoAt(30) },
    { field: 'display_label', previous_value: 'camy', value: 'Release lead · September train', at: agoAt(12) },
  ] } }))
  if (variant === 'recovery') {
    const selected = data.sessions[1]!
    await page.route('**/harness-sessions/*/recovery', route => route.fulfill({ json: {
      session_id: selected.id, host: selected.host, display_label: 'ops', observed_revision: 'a'.repeat(64), confirmation: `archive ${selected.id} on ${selected.host}`,
      process_state: 'unknown', process_scope: 'No process will be signalled. This action archives this registration only; other sessions and child processes are unaffected.',
      can_archive: true, force_stop_available: false, force_stop_reason: 'Force stop requires a live daemon with verified ownership of this exact process generation.',
    } }))
  }
}
async function openAgents(page: Page, path = '/agents') {
  await page.goto(path)
  await heading(page, 'Agents', 1)
  await visible(page, '.agents-page .row')
}
const recoverDialog = (page: Page) => page.getByRole('dialog', { name: 'Recover session' })

// ---------------------------------------------------------------- start agent
const startDialog = (page: Page) => page.getByRole('dialog', { name: 'Start agent', exact: true })
async function startReady(page: Page) {
  await page.goto('/agents')
  await page.locator('button.start-agent').click()
  await expect(startDialog(page)).toBeVisible()
  await startDialog(page).getByRole('searchbox').fill('PHAROS-11')
  await startDialog(page).getByRole('button', { name: /PHAROS-11 Connect Hetzner/ }).click()
  await expect(startDialog(page).getByLabel('Host', { exact: true })).toHaveValue('workstation')
  await expect(startDialog(page).getByText('Account available', { exact: true })).toBeVisible()
}

// ---------------------------------------------------------------- usage
function usageGroup(label: string, sessions: number, cost: string | null, input: number, output: number, extra: Partial<UsageGroup> = {}): UsageGroup {
  const known = cost ? sessions : 0
  return {
    label, key: label, sessions, usage_rows: sessions + 1, unreported_sessions: cost ? 0 : sessions,
    input_tokens: cost ? String(input) : null, input_known_rows: known, input_unknown_rows: cost ? 0 : sessions,
    output_tokens: cost ? String(output) : null, output_known_rows: known, output_unknown_rows: cost ? 0 : sessions,
    cached_input_tokens: cost ? String(Math.round(input * 0.6)) : null, cached_input_known_rows: known, cached_input_unknown_rows: cost ? 0 : sessions,
    tokens_state: cost ? 'known' : 'unknown', estimated_cost_usd: cost, cost_known_rows: known, cost_unknown_rows: cost ? 0 : sessions,
    cost_state: cost ? 'known' : 'unknown', provisional_rows: 0, provisional_sessions: 0, ...extra,
  }
}
function usageDashboard(): UsageDashboard {
  const trend = Array.from({ length: 30 }, (_, i) => {
    const day = new Date(Date.parse('2026-08-29T00:00:00Z') + i * 86_400_000).toISOString().slice(0, 10)
    const sessions = [3, 5, 2, 0, 7, 11, 4, 6, 9, 14, 8, 3, 1, 0, 5, 12, 16, 9, 7, 4, 10, 13, 18, 22, 15, 9, 6, 11, 19, 24][i]!
    return { day, group: usageGroup('', sessions, sessions ? (sessions * 7.35).toFixed(12) : null, sessions * 180_000, sessions * 21_000, i % 9 === 4 ? { cost_state: 'partial', unreported_sessions: 1 } : {}) }
  })
  return {
    from: '2026-08-29T00:00:00Z', to: '2026-09-28T00:00:00Z', generated_at: '2026-09-28T09:30:00Z',
    attribution: 'lifetime_for_sessions_started_in_range', trend_basis: 'session_started_utc_day', list_price_currency: 'USD', truncated: false,
    totals: usageGroup('All visible sessions', 272, '1998.420000000000', 48_960_000, 5_712_000, { unreported_sessions: 9, cost_state: 'partial', tokens_state: 'partial', cost_unknown_rows: 9, provisional_rows: 3, provisional_sessions: 2 }),
    by_project: [
      usageGroup('Aeon', 141, '1204.110000000000', 25_400_000, 3_010_000, { key: 'AEON', id: 'p-aeon' }),
      usageGroup('Pharos', 72, '512.900000000000', 12_900_000, 1_480_000, { key: 'PHAROS', id: 'p-pharos' }),
      usageGroup('Studio infrastructure', 41, '266.300000000000', 8_100_000, 910_000, { key: 'PRJ-26', id: 'p-frozen' }),
      usageGroup('Unreported', 9, null, 0, 0, { key: '' }),
    ],
    by_model: [
      usageGroup('claude-fable-high', 88, '903.200000000000', 19_000_000, 2_200_000),
      usageGroup('gpt-6-sol', 71, '611.400000000000', 14_400_000, 1_700_000),
      usageGroup('composer-2.5-fast', 44, '188.020000000000', 7_100_000, 840_000),
      usageGroup('anthropic/claude-sonnet-5', 38, '240.600000000000', 6_300_000, 720_000),
      usageGroup('grok-4.7', 22, '55.200000000000', 2_160_000, 252_000),
      usageGroup('Unreported', 9, null, 0, 0),
    ],
    by_subscription: [
      usageGroup('Claude Max', 102, '1011.000000000000', 21_000_000, 2_500_000, { billing_mode: 'subscription' }),
      usageGroup('Codex Pro', 71, '611.400000000000', 14_400_000, 1_700_000, { billing_mode: 'subscription' }),
      usageGroup('Cursor Business', 44, '188.020000000000', 7_100_000, 840_000, { billing_mode: 'mixed' }),
      usageGroup('OpenRouter API', 46, '188.000000000000', 6_460_000, 672_000, { billing_mode: 'api' }),
      usageGroup('Unreported', 9, null, 0, 0, { billing_mode: 'unreported' }),
    ],
    trend,
    tickets: [
      ['AEON-266', 'Visual audit capture of the September screens', 'p-aeon', 'AEON', 12, '96.400000000000'],
      ['AEON-253', 'Bind rules bootstrap receive to the selected CLI', 'p-aeon', 'AEON', 9, '81.020000000000'],
      ['AEON-252', 'Reserve stable95 person-operated rules draft import', 'p-aeon', 'AEON', 17, '140.550000000000'],
      ['PHAROS-11', 'Connect Hetzner Cloud for managed provisioning', 'p-pharos', 'PHAROS', 21, '162.000000000000'],
      ['PHAROS-12', 'Add an Oracle Cloud connector', 'p-pharos', 'PHAROS', 8, null],
      ['AEON-227', 'Add existing tickets from Plan and add a selection to a new release', 'p-aeon', 'AEON', 6, '44.800000000000'],
      ['AEON-213', 'Session metadata: model, effort, account and work context', 'p-aeon', 'AEON', 11, '73.210000000000'],
      ['PRJ-26-4', 'Rotate the hsb1 backup keys', 'p-frozen', 'PRJ-26', 3, '12.900000000000'],
    ].map(([key, title, project, projectKey, sessions, cost]) => ({ ...usageGroup(String(title), Number(sessions), cost as string | null, Number(sessions) * 190_000, Number(sessions) * 22_000, { key: String(key), id: `n-${key}` }), project_id: String(project), project_key: String(projectKey) })),
    tickets_cost_unknown: 3,
    allowance: { state: 'visible', windows: [
      { account_id: 'a-1', label: 'Claude Max', harness: 'claude', account_state: 'available', window_id: 'w-1', unit: 'tokens', allowance: 5_000_000, used: 3_600_000, reserved: 120_000, pace_model: 'steady', burst_ratio: '0.0500', starts_at: '2026-09-28T07:00:00Z', ends_at: '2026-09-28T12:00:00Z', provisional: false, pace_cap: 3_300_000, headroom: 1_280_000, hard_remaining: 1_280_000 },
      { account_id: 'a-2', label: 'Codex Pro', harness: 'codex', account_state: 'available', window_id: 'w-2', unit: 'requests', allowance: 1500, used: 450, reserved: 10, pace_model: 'steady', burst_ratio: '0.1000', starts_at: '2026-09-27T19:30:00Z', ends_at: '2026-09-28T19:30:00Z', provisional: false, pace_cap: 900, headroom: 1040, hard_remaining: 1040 },
      { account_id: 'a-3', label: 'Cursor Business', harness: 'cursor', account_state: 'available', window_id: 'w-3', unit: 'cost_micros', allowance: 200_000_000, used: 96_000_000, reserved: 0, pace_model: 'frontload', burst_ratio: '0.1000', starts_at: '2026-09-13T00:00:00Z', ends_at: '2026-10-13T00:00:00Z', provisional: false, pace_cap: 120_000_000, headroom: 104_000_000, hard_remaining: 104_000_000 },
      { account_id: 'a-4', label: 'SuperGrok', harness: 'grok', account_state: 'unavailable', window_id: 'w-4', unit: 'tokens', allowance: 1_000_000, used: null, reserved: 0, pace_model: 'unrestricted', burst_ratio: '0.1000', starts_at: '2026-09-28T06:00:00Z', ends_at: '2026-09-28T10:00:00Z', provisional: true, pace_cap: 1_000_000, headroom: null, hard_remaining: null },
    ] },
  }
}

// ---------------------------------------------------------------- rules
const LAYER = { company: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc', project: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd', person: 'd1d1d1d1-d1d1-41d1-81d1-d1d1d1d1d1d1', agent: 'd2d2d2d2-d2d2-42d2-82d2-d2d2d2d2d2d2' }
const SCOPE = { company: { layer: 'company' }, project: { layer: 'project', project_id: RULE_PROJECT }, person: { layer: 'person', owner_id: RULE_PERSON }, agent: { layer: 'agent', role: 'builder' } }
type RuleSpec = [identity: string, text: string, why: string, extra?: Record<string, unknown>]
const rule = ([identity, text, why, extra = {}]: RuleSpec) => ({
  identity, text, why, details: '', strength: 'normal', enabled: true, roles: [], harnesses: [],
  source: { reference: 'AEON-252', identity: '', edited_here: false }, ...extra,
})
const locked = { strength: 'locked' }
const RULE_SETS: [layer: keyof typeof LAYER, name: string, published: string, rules: RuleSpec[]][] = [
  ['company', 'Secrets and environment', '260926093000.0.0', [
    ['never-print-env', 'Never run a command whose output **is** the resolved environment, such as `env`, `printenv` or `direnv export`.', 'A printed environment leaks every secret it holds into the transcript.', locked],
    ['stop-on-secret', 'If a secret appears in any output, **stop**, do not repeat it, and alert the operator.', 'Treat it as compromised and rotate before continuing.', locked],
    ['source-agent-secrets', 'Load agent credentials only with `( set -a; source <file>; cmd; set +a )`; never read the file itself.', 'Reading the file puts the value into the transcript.'],
    ['no-commit-secrets', 'Never commit passwords, tokens, bcrypt hashes or decrypted `.age` content.', 'History keeps them forever.', locked],
  ]],
  ['company', 'Git discipline', '', [
    ['no-hard-reset', 'Never run `git reset --hard`, `git clean -f` or `git checkout .` unless the operator asks for it explicitly.', 'These destroy uncommitted work without a trace.', locked],
    ['no-force-main', 'No `git push --force` to `main` or `master`.', 'Other workers build on main.'],
    ['no-hook-bypass', 'Never bypass hooks with `--no-verify`; fix the cause and create a **new** commit.', 'Hooks carry the release checks.'],
    ['no-amend', 'Do not `git commit --amend` unless asked: the prior commit may be the work you would destroy.', 'A failed hook means the commit did not happen.'],
    ['diff-before-commit', 'Run `git diff` and `git status` before every commit to scan the file set.', 'Catches stray files and secrets.'],
  ]],
  ['company', 'Cross-repo authoring', '', [
    ['own-repo-only', 'Author changes **only** in the session’s own repository; elsewhere, file a ticket with the diff in the body.', 'Reading foreign repos is free; writing is governed.'],
    ['release-pin-carveout', 'In a repo holding a deploy pin, edit only the pin, its comment and the documented vendoring step.', 'The narrow release carve-out.', { details: 'Use the review path where one exists; never push directly to main, even where main is unprotected.' }],
    ['third-party-stop', 'Third-party repositories without a PR path: **stop and ask**, never push.', 'Business-owned repos are stricter.'],
    ['own-residue', 'Clean up only your own residue; other people’s branches become a ticket.', 'Someone else may still need them.'],
  ]],
  ['company', 'Files and operations', '', [
    ['trash-not-rm', 'Delete with `trash`, never `rm -rf`.', 'Mistakes stay recoverable.'],
    ['no-nixos-on-mac', 'Never build NixOS configurations on macOS; build remotely over SSH.', 'Cross builds break in confusing ways.'],
    ['production-not-lab', 'Fleet hosts (`hsb*`, `csb*`) run production only: no test VMs, lab containers or disposable deployments.', 'Lab VMs exhausted hsb1 memory on 2026-09-16 (INSPR-461).', locked],
    ['no-new-md', 'Do not create new `.md` files unless asked; durable knowledge goes to a PPM Knowledge entry.', 'Docs sprawl otherwise.', { enabled: false }],
  ]],
  ['project', 'Package scope', '260927120000.0.0', [
    ['package-is-scope', 'Your package is your scope: change only the files your package needs.', 'Parallel workers merge cleanly.'],
    ['shared-files-additive', 'Keep changes to shared files such as `api/openapi.yaml` or `go.mod` minimal and **additive**.', 'Several packages touch them at once.'],
    ['migration-range', 'Take the next free migration number inside your package’s range, e.g. `0020`–`0039` for P0.3.', 'Numbers must not collide across packages.'],
  ]],
  ['project', 'Contract first', '', [
    ['openapi-first', 'Add endpoints to `api/openapi.yaml` in the **same change** as their handler.', 'The contract is the single source.'],
    ['sse-live', 'Live updates use server-sent events; do not add WebSockets.', 'ADR-002.'],
    ['tenant-rls', 'Every table carries `tenant_id` and an RLS policy on `current_setting(\'aeon.tenant_id\')`.', 'Isolation is enforced by Postgres.', locked],
  ]],
  ['project', 'Tests and progress', '', [
    ['tests-are-done', 'Tests are part of done: `just test` and `just web-check` must pass.', 'The coordinator merges only green work.'],
    ['progress-file', 'Overwrite `.agent-status.json` after every meaningful step with package, worker, pct and a note.', 'The coordinator watches it.'],
    ['no-cross-review', 'Do not run cross-family review gates per package; QA is consolidated per release.', 'Markus, 2026-09-23.', { roles: ['builder'] }],
  ]],
  ['project', 'Interface conventions', '', [
    ['no-edge-accents', 'Never mark state with a coloured bar on the **left or top edge** of a row, card or toast.', 'The classic AI-generated UI tell.', locked],
    ['svg-icons', 'Use SVG icons only, centred in their controls; never text glyphs.', 'Glyphs render inconsistently.'],
    ['tokens', 'Take colours and spacing from `web/src/styles/tokens.css`.', 'Themes follow the tokens.'],
    ['classic-retired', 'Classic Paimos is retired: never build on it; old links resolve through `/from-classic`.', 'Cutover done 2026-09-26 (AEON-43).'],
  ]],
  ['person', 'Communication style', '', [
    ['telegraph', 'Write telegraph style: dense, low fluff, **TL;DR** at the start and end of long answers.', 'Markus reads a lot of reports.'],
    ['no-emoji', 'No emojis in reports or commit messages.', 'Plain text travels better.'],
    ['time-neutral', 'Run `date` before any time-of-day greeting, or stay time-neutral.', 'The date alone says nothing about morning or night.'],
  ]],
  ['person', 'Pacing', '', [
    ['one-step', 'For interactive procedures (agenix, SSH handshakes, rotations) go **one step at a time** and wait for “done”.', 'Ten-step playbooks get lost.'],
    ['ask-backlog', 'Do not pick backlog items yourself; ask what to tackle next.', 'Priorities change daily.'],
    ['ship-next-format', 'Propose the next delivery item as one sentence, one reason, then ask only for `go`.', 'A bare go is the approval.', { harnesses: ['claude-code', 'codex'] }],
  ]],
  ['person', 'Ticket hygiene', '', [
    ['ticket-first', 'Bind material work to exactly one ticket before editing, and add the worker marker.', 'Work without a marker is invisible.'],
    ['one-tracker', 'Use the product’s designated tracker, never two.', 'Split history is lost history.'],
    ['handoff-history', 'Record handoffs on the ticket without erasing earlier markers.', 'Attribution stays auditable.'],
  ]],
  ['agent', 'Builder loop', '', [
    ['read-agents-md', 'Read the repository’s `AGENTS.md` fully before the first edit.', 'It overrides defaults.'],
    ['worktree-only', 'Work only in the assigned worktree; never touch other worktrees.', 'Parallel workers share the disk.'],
    ['no-push', 'Commit on your branch; do **not** push. The coordinator merges.', 'One integration point.'],
  ]],
  ['agent', 'Commits', '', [
    ['commit-format', 'Commit messages read `P0.x: what changed` plus the ticket key, e.g. `AEON-266`.', 'Release notes are generated from them.'],
    ['no-build-output', 'Never commit generated build output or screenshots.', 'They bloat history.'],
    ['spdx', 'New source files start with the `SPDX-License-Identifier: AGPL-3.0-only` header.', 'Licence compliance.'],
  ]],
  ['agent', 'Reporting', '', [
    ['final-note', 'When done, set `pct` to 100 and write a one-paragraph summary in `note`.', 'The coordinator reads it first.'],
    ['absolute-paths', 'Share absolute file paths in the final report.', 'Relative paths are ambiguous across worktrees.'],
    ['long-report-rule', 'In the final report list **what** was produced, **where** it lives, what could not be reached and **why**, and any glaring problem noticed on the way, such as console errors, overflow at `390` wide, or a dialog that does not fit, without fixing it yourself.', 'Capture tasks must stay capture tasks; fixes are separate tickets with their own review.', { details: 'Group the list by screen. Keep it short enough to read in one pass.' }],
  ]],
]

function ruleWorld(empty: boolean) {
  const sets = empty ? [] : RULE_SETS.map(([layer, name, published, rules], i) => ({
    id: `e0000000-0000-4000-8000-${String(i + 1).padStart(12, '0')}`, layer_id: LAYER[layer], scope: SCOPE[layer], name, revision: 3, rules: rules.map(rule), published_version: published,
  }))
  const layers = empty ? [] : (Object.keys(LAYER) as (keyof typeof LAYER)[]).map(key => ({ id: LAYER[key], scope: SCOPE[key] }))
  return { sets, layers }
}
async function rulesSetup(page: Page, empty: boolean) {
  await mockWork(page, fixtures())
  await mockSettings(page, settingsData())
  await mockRules(page)
  const world = ruleWorld(empty)
  // Registered last, so these reads win over the small shared rules mock.
  await page.route('**/api/rules/**', route => {
    const url = new URL(route.request().url()), path = url.pathname
    if (route.request().method() !== 'GET') return route.fallback()
    if (path === '/api/rules/layers') return route.fulfill({ json: { layers: world.layers } })
    if (path === '/api/rules/sets') return route.fulfill({ json: { sets: world.sets.filter(set => set.layer_id === url.searchParams.get('layer_id')) } })
    const one = /^\/api\/rules\/sets\/([^/]+)(\/versions)?$/.exec(path)
    const set = one && world.sets.find(item => item.id === one[1])
    if (one && set && one[2]) return route.fulfill({ json: { versions: set.published_version ? [{ set_id: set.id, scope: set.scope, name: set.name, revision: 2, version: set.published_version, sha256: 'ab'.repeat(32), rules: set.rules, published_at: '2026-09-27T12:00:00Z' }] : [] } })
    if (one && set) return route.fulfill({ json: set })
    return route.fallback()
  })
}
async function openRules(page: Page, empty: boolean) {
  await page.goto('/settings/agent-rules')
  await heading(page, 'Agent rules')
  if (!empty) await expect(page.getByRole('checkbox', { name: /Take colours and spacing/ }).first()).toBeAttached()
  else await expect(page.getByRole('region', { name: 'Company', exact: true })).toBeVisible()
}
function importFile() {
  const r = (identity: string, text: string, why: string, details = '') => ({ ...rule([identity, text, why]), ...(details ? { details } : {}) })
  const layers = [
    { scope: SCOPE.company, sets: [
      { name: 'Incident response', rules: [
        r('declare-early', 'Declare an incident as soon as production behaves unexpectedly; do not wait for certainty.', 'Early declaration shortens outages.'),
        r('leak-protocol', 'On a suspected leak follow the `/incident` protocol: stop, contain, rotate, then write up.', 'Order matters when secrets are involved.', 'Rotation happens before any write-up is shared. The write-up names the rotated credentials by reference, never by value.'),
        r('one-commander', 'One person commands the incident; everyone else reports to them.', 'Parallel commanders contradict each other.'),
        r('timeline', 'Keep a UTC timeline of every action taken during the incident.', 'The review depends on it.'),
        r('no-blame', 'Reviews name causes and fixes, **never** people.', 'Blame hides the next cause.'),
        r('customer-note', 'Tell affected customers what happened and what changed within 48 hours.', 'Trust follows candour.'),
      ] },
      { name: 'Deploy safety', rules: [
        r('backup-before-deploy', 'Record fresh backup evidence before every production deployment.', 'A deploy without a backup cannot be rolled back.'),
        r('staging-first', 'Deploy to staging first and wait for the verification probe to pass.', 'Staging catches configuration drift.'),
        r('rollback-target', 'Name the rollback target in the release notes **before** deploying.', 'Nobody searches for it calmly during an outage.'),
        r('no-friday', 'No production deploys after 16:00 local time on Fridays.', 'Weekends have thin cover.'),
        r('pin-review', 'Deploy pins change only through a reviewed pull request.', 'Pins decide what runs.'),
      ] },
    ] },
    { scope: SCOPE.project, sets: [
      { name: 'Web accessibility', rules: [
        r('axe-clean', 'New screens pass `axe` WCAG 2.1 AA with no serious or critical findings.', 'Accessibility regressions are cheap to prevent.'),
        r('focus-visible', 'Every interactive control shows a visible focus ring.', 'Keyboard users need to see where they are.'),
        r('phone-390', 'Every screen works at **390** px wide with no horizontal scroll.', 'Markus reviews on a phone.'),
        r('names-for-icons', 'Icon-only buttons carry an accessible name.', 'Screen readers announce nothing otherwise.'),
        r('reduced-motion', 'Respect `prefers-reduced-motion`; loops stop when it is set.', 'Motion can make people ill.'),
        r('contrast-both-themes', 'Check contrast in light **and** dark themes.', 'Dark themes hide low-contrast text.'),
      ] },
    ] },
    { scope: SCOPE.person, sets: [
      { name: 'Review habits', rules: [
        r('screenshots-first', 'Attach before and after screenshots to every UI change.', 'Pixels settle arguments quickly.'),
        r('small-prs', 'Keep pull requests under 400 changed lines where possible.', 'Large diffs get skimmed.'),
        r('explain-why', 'Every PR description says **why**, not only what.', 'Reviewers judge intent.'),
        r('link-ticket', 'Link the ticket key in the PR title.', 'Traceability.'),
      ] },
    ] },
    { scope: { layer: 'agent', role: 'reviewer' }, sets: [
      { name: 'Review gate', rules: [
        r('read-only', 'A review gate runs read-only; it never edits files or pushes.', 'The reviewer must not become an author.'),
        r('explicit-ok', 'Answer with an explicit `ok` or a list of blocking findings.', 'Anything else leaves the gate closed.'),
        r('cross-family', 'Review work authored by another model family only.', 'Same-family review shares blind spots.'),
        r('evidence', 'Cite file and line for every blocking finding.', 'Unverifiable findings stall the loop.'),
        r('no-style-nits', 'Do not block on style that the linter accepts.', 'Nits slow delivery without improving it.'),
      ] },
    ] },
  ]
  return { name: 'september-drafts.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify({ schema: 'aeon.rules-draft-import.v1', tenant_id: 't1', layers })) }
}

// ---------------------------------------------------------------- journey and releases
function renewalWorld() {
  const world = journeyWorld('deploy')
  const template = world.approvals[0]!
  const future = new Date(Date.now() + 3_600_000).toISOString()
  const past = new Date(Date.now() - 60_000).toISOString()
  world.approvals = []
  for (const gate of ['candidate', 'deploy'] as const) {
    world.approvals.push({ ...template, id: `old-${gate}`, scope: `journey.${gate}`, decision: 'approved', decided_by_principal_id: me.id, expires_at: past })
    world.approvals.push({ ...template, id: `fresh-${gate}`, scope: `journey.${gate}`, decision: null, decided_by_principal_id: null, expires_at: future })
    Object.assign(world.journey.stages.find(s => s.key === (gate === 'candidate' ? 'build' : 'deploy'))!, {
      gate_approval_id: `old-${gate}`, gate_live: false, gate_offer_id: `fresh-${gate}`, gate_offer_state: 'pending', gate_offer_expires_at: future,
    })
  }
  world.journey.next_action = { key: 'approve_candidate', renewal_action: 'renew_candidate', label: 'Renew candidate approval', stage: 'deploy', available: false, reason: 'The standing candidate gate is no longer live. A fresh approval is required.', approval_request_id: 'fresh-candidate' }
  world.journey.launch_readiness = { can_admit: false, reason: 'Candidate gate is not approved.' }
  world.walkers['r-2']!.state = 'deploying'
  return world
}
async function journeySetup(page: Page, world: JourneyWorld, data: Fixtures = fixtures()) {
  await mockWork(page, data)
  await mockJourney(page, world)
}
async function openJourney(page: Page) {
  await page.goto('/p/PHAROS?view=journey')
  await expect(page.getByRole('navigation', { name: 'Project journey' })).toBeVisible()
}
const decision = (page: Page, name: string) => page.getByRole('region', { name: `Decision: ${name}` })

const PICKER_OPTIONS = [
  ['n-21', 'PHAROS-21', 'Publish the status page', 'backlog', 'ticket', 'n-epic', null, null, 'addable'],
  ['n-22', 'PHAROS-22', 'Page the on-call rotation when a beacon misses three heartbeats in a row', 'new', 'ticket', 'n-epic', null, null, 'addable'],
  ['n-23', 'PHAROS-23', 'Write the deploy runbook', 'backlog', 'ticket', null, null, null, 'addable'],
  ['n-26', 'PHAROS-26', 'Show the provider price before approval', 'backlog', 'ticket', 'n-epic', null, null, 'addable'],
  ['n-27', 'PHAROS-27', 'Retry a refused Hetzner provisioning with fresh backup evidence', 'backlog', 'ticket', 'n-epic', null, null, 'addable'],
  ['n-28', 'PHAROS-28', 'Group hosts by region in the fleet list', 'new', 'ticket', 'n-epic-2', null, null, 'addable'],
  ['n-29', 'PHAROS-29', 'Expire temporary Janus permits after their window', 'backlog', 'ticket', 'n-epic-2', null, null, 'addable'],
  ['n-30', 'PHAROS-30', 'Explain denied access requests in the permit dialog', 'new', 'ticket', 'n-epic-2', null, null, 'addable'],
  ['n-19', 'PHAROS-19', 'Rotate the on-call roster', 'backlog', 'task', 'n-epic', null, null, 'unsupported'],
  ['n-5', 'PHAROS-15', 'Beacon health probes', 'done', 'ticket', 'n-epic', 'r-1', '260901120000.0.0', 'released'],
  ['n-6', 'PHAROS-16', 'Retire the old dashboard', 'cancelled', 'ticket', null, null, null, 'closed'],
  ['n-24', 'PHAROS-24', 'Move the billing epic', 'backlog', 'ticket', 'n-epic-2', 'r-9', 'Release 9', 'other_release'],
  ['n-25', 'PHAROS-25', 'Keep the building release', 'in_progress', 'ticket', null, 'r-4', 'Release 4', 'active_release'],
].map(([ticket_node_id, key, title, status, type, feature_node_id, release_node_id, release_title, availability]) => ({ ticket_node_id, key, title, status, type, feature_node_id, release_node_id, release_title, availability }))
async function mockMembership(page: Page, world: JourneyWorld) {
  await page.route('**/api/projects/*/release-memberships*', route => {
    const ids = new URL(route.request().url()).searchParams.getAll('ticket_node_id')
    return route.fulfill({ json: { tickets: ids.map(id => ({ ticket_node_id: id, release_node_id: null, release_title: null, release_state: null })) } })
  })
  await page.route('**/api/projects/*/releases/*/ticket-options*', route => {
    const release = /releases\/([^/]+)\/ticket-options/.exec(route.request().url())![1]!
    return route.fulfill({ json: { expected_revision: world.walkers[release]?.revision ?? 1, tickets: PICKER_OPTIONS } })
  })
}

// ---------------------------------------------------------------- project view
const AT = Date.parse('2026-09-23T12:00:00Z')
const liveTicket = (id: string, key: string, title: string, project_id = 'p-pharos') => ({ id, key, title, project_id })
const SHARED = 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'
function workersOn(data: Fixtures, tickets: { id: string; key: string; title: string }[]) {
  const [a, b, c, d, e] = tickets
  const named = (label: string, fields: Parameters<typeof liveAgent>[0]) => liveAgent({ principal_id: SHARED, name: 'aeon-coordinator', display_label: label, ...fields })
  data.live.push(
    named('fault', { project_id: 'p-pharos', session_id: 's-fault', has_problem: true, ticket: liveTicket(a!.id, a!.key, a!.title) }),
    named('hausv', { project_id: 'p-pharos', session_id: 's-hausv', harness: 'codex', ticket: liveTicket(b!.id, b!.key, b!.title) }),
    named('wren', { project_id: 'p-pharos', session_id: 's-wren', needs_attention: true, ticket: liveTicket(c!.id, c!.key, c!.title) }),
    liveAgent({ project_id: 'p-pharos', name: undefined, harness: 'grok', ticket: liveTicket(c!.id, c!.key, c!.title) }),
    named('quiet', { project_id: 'p-pharos', session_id: 's-quiet', harness: 'pi', activity: 'idle', heartbeat_at: new Date(AT - 10 * 60_000).toISOString(), ticket: liveTicket(d!.id, d!.key, d!.title) }),
    named('permission-checks-for-cross-tenant-approvals', { project_id: 'p-pharos', session_id: 's-long', harness: 'cursor', ticket: liveTicket(e!.id, e!.key, e!.title) }),
  )
  return data
}
const BENEFITS = { pill_en: 'Hosts in minutes', pill_de: 'Hosts in Minuten', benefit_en: 'You approve the price once and Pharos provisions the Hetzner host for you, then cleans up everything it created.', benefit_de: 'Du bestätigst den Preis einmal, Pharos stellt den Hetzner-Host bereit und räumt danach alles auf.' }
function ticketData() {
  const data = fixtures()
  Object.assign(data.nodes.find(n => n.key === 'PHAROS-11')!.fields, BENEFITS)
  return workersOn(data, ['PHAROS-10', 'PHAROS-11', 'PHAROS-12', 'PHAROS-13', 'PHAROS-14'].map(key => data.nodes.find(n => n.key === key)!))
}
async function projectAgents(page: Page) {
  await mockAgents(page, agentData({ ...WORLD, now: AT }))
}
// One story across the ticket list, the panel chip and Agent work: the live
// worker on PHAROS-11 is the agents world's camy session, the one on PHAROS-12 is
// nova, and the agent-work report lists the same sessions.
function ticketDataWithAgents() {
  const data = ticketData()
  const bind = (key: string, n: number, label: string, harness: 'claude' | 'codex') => {
    const worker = data.live.find(agent => agent.ticket?.key === key && agent.display_label)
    if (worker) Object.assign(worker, { display_label: label, harness, session_id: `5e000000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`, principal_id: `a0000000-0000-4000-8000-00000000000${n}` })
  }
  bind('PHAROS-11', 1, 'camy', 'claude')
  bind('PHAROS-12', 2, 'nova', 'codex')
  return data
}
async function ticketAgentWork(page: Page) {
  const session = (n: number, key: string, harness: string, label: string, model: string, minutes: number, input: string, output: string, cost: string) => ({
    id: `5e000000-0000-4000-8000-0000000000${String(n).padStart(2, '0')}`, ticket_node_id: `n-${n}`, ticket_key: key, ticket_title: '', harness, label,
    model, model_state: 'known', effort: 'high', effort_state: 'known', phase: 'working', started_at: new Date(AT - minutes * 60_000).toISOString(), ended_at: null,
    duration_seconds: minutes * 60, duration_state: 'ongoing', usage_reported: true, models_truncated: false,
    models: [{ model, input_tokens: input, output_tokens: output, cached_input_tokens: null, tokens_state: 'known', cached_state: 'unknown', estimated_cost_usd: cost, cost_state: 'estimated', provisional: true, price_version: '3', billing_mode: 'subscription', subscription_label: null }],
    input_tokens: input, output_tokens: output, cached_input_tokens: null, tokens_state: 'known', cached_state: 'unknown', estimated_cost_usd: cost, cost_state: 'provisional', unknown_token_models: 0, unknown_cost_models: 0,
  })
  const reports: Record<string, ReturnType<typeof session>> = {
    'n-1': session(1, 'PHAROS-11', 'claude', 'camy', 'claude-fable-high', 72, '184300', '22140', '3.840000000000'),
    'n-2': session(2, 'PHAROS-12', 'codex', 'nova', 'codex-astra-xhigh', 26, '41200', '5900', '0.610000000000'),
  }
  for (const [node, row] of Object.entries(reports)) {
    await page.route(`**/api/nodes/${node}/agent-work`, route => route.fulfill({ json: {
      node_id: node, kind: 'ticket', currency: 'USD', usage_available: true, includes_descendants: false, scope_truncated: false, list_truncated: false, sessions: [row],
      totals: { session_count: 1, input_tokens: row.input_tokens, output_tokens: row.output_tokens, cached_input_tokens: null, tokens_state: 'known', cached_state: 'unknown', estimated_cost_usd: row.estimated_cost_usd, cost_state: 'provisional', currency: 'USD', duration_seconds: row.duration_seconds, duration_state: 'ongoing', unknown_token_sessions: 0, unknown_cost_sessions: 0, unknown_token_models: 0, unknown_cost_models: 0 },
    } }))
  }
}
const grid = (page: Page) => page.getByRole('grid', { name: 'Tickets' })
const ticketRow = (page: Page, key: string) => grid(page).locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
const ticketPanel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })

// ---------------------------------------------------------------- the shots
const shots: Shot[] = [
  // 1. Agents overview
  { screen: 'agents', state: 'overview', setup: page => agentsSetup(page, 'busy'), act: page => openAgents(page) },
  { screen: 'agents', state: 'overview-expanded', setup: page => agentsSetup(page, 'busy'), act: async page => {
    await openAgents(page)
    await page.getByRole('button', { name: /^Stopped/ }).first().click()
    const history = page.locator(`[data-row="s:${LEAD}"] .history-toggle`)
    if (await history.isEnabled()) await history.click()
    await page.getByText('Accounts and pacing', { exact: true }).first().click()
    await expect(page.getByRole('region', { name: 'Accounts and pacing' })).toBeVisible()
  } },
  { screen: 'agents', state: 'needs-you-empty', setup: page => agentsSetup(page, 'quiet'), act: page => openAgents(page) },
  { screen: 'agents', state: 'empty', setup: page => agentsSetup(page, 'empty'), act: async page => { await page.goto('/agents'); await heading(page, 'Agents', 1); await page.waitForTimeout(600) } },
  // 2. Session panel
  { screen: 'session-panel', state: 'lead', setup: page => agentsSetup(page, 'busy'), act: async page => {
    await openAgents(page, `/agents/${LEAD}`)
    await expect(page.getByRole('complementary', { name: 'Session details' })).toBeVisible()
  } },
  { screen: 'session-panel', state: 'worker-long-label', setup: page => agentsSetup(page, 'busy'), act: async page => {
    await openAgents(page, `/agents/${WORKER}`)
    await expect(page.getByRole('complementary', { name: 'Session details' })).toBeVisible()
  } },
  // 3. Session recovery
  { screen: 'session-recovery', state: 'open', setup: page => agentsSetup(page, 'recovery'), act: async page => {
    await openAgents(page, `/agents/${sessionId(2)}`)
    await page.getByRole('button', { name: 'Recover', exact: true }).click()
    await expect(recoverDialog(page).getByRole('button', { name: 'Archive session' })).toBeVisible()
  } },
  { screen: 'session-recovery', state: 'filled', setup: page => agentsSetup(page, 'recovery'), act: async page => {
    await openAgents(page, `/agents/${sessionId(2)}`)
    await page.getByRole('button', { name: 'Recover', exact: true }).click()
    await recoverDialog(page).getByLabel('Reason for recovery').fill('Heartbeat loop has exited after the workstation went offline; close the stale record')
    await recoverDialog(page).getByLabel('Type the exact confirmation').fill(`archive ${sessionId(2)} on workstation-offline`)
    await expect(recoverDialog(page).getByRole('button', { name: 'Archive session' })).toBeEnabled()
  } },
  // 4. Start agent and the account cascade (ACU1)
  { screen: 'start-agent', state: 'ready', setup: async page => { await mockStartAgent(page, { catalog: 'two-hosts' }) }, act: startReady },
  { screen: 'start-agent', state: 'other-host', setup: async page => { await mockStartAgent(page, { catalog: 'two-hosts' }) }, act: async page => {
    await startReady(page)
    await startDialog(page).getByLabel('Host', { exact: true }).selectOption({ label: 'Laptop' })
    await expect(startDialog(page).getByLabel('Harness', { exact: true }).locator('option:checked')).toHaveText('Claude')
  } },
  { screen: 'start-agent', state: 'draining-account', setup: async page => { await mockStartAgent(page, { unavailable: true }) }, act: async page => {
    await page.goto('/agents')
    await page.locator('button.start-agent').click()
    await startDialog(page).getByRole('searchbox').fill('PHAROS-11')
    await startDialog(page).getByRole('button', { name: /PHAROS-11 Connect Hetzner/ }).click()
    await expect(startDialog(page).getByText('Account is draining', { exact: true })).toBeVisible()
  } },
  { screen: 'start-agent', state: 'accounts-card', setup: async page => { await mockStartAgent(page, { catalog: 'two-hosts' }) }, act: async page => {
    await page.goto('/agents')
    await page.getByText('Accounts and pacing', { exact: true }).first().click()
    await expect(page.getByRole('region', { name: 'Accounts and pacing' })).toContainText('5-hour')
  } },
  // 5. Connected computers and pairing
  { screen: 'pairing', state: 'public-guide', setup: mockAnonymousGuide, act: async page => {
    await page.goto('/agents/register-agent')
    await heading(page, 'Connect a computer')
    await page.getByText('Manual and agent setup').click()
  } },
  { screen: 'pairing', state: 'connected-computers', setup: pairingSetup, act: async page => {
    await page.goto('/agents/register-agent')
    await heading(page, 'Connected computers')
    await page.waitForTimeout(300)
  } },
  { screen: 'pairing', state: 'review', setup: pairingSetup, act: pairingReview },
  { screen: 'pairing', state: 'ongoing-limits', setup: pairingSetup, act: async page => {
    await pairingReview(page)
    await page.getByRole('checkbox', { name: 'Connect Cursor' }).uncheck()
    await page.getByRole('radio', { name: /Set ongoing limits/ }).check()
    await expect(page.getByRole('spinbutton', { name: 'Requests', exact: true })).toBeVisible()
  } },
  { screen: 'pairing', state: 'setting-up', setup: pairingSetup, act: async page => {
    await pairingReview(page)
    await page.getByRole('region', { name: 'Pairing review' }).getByRole('button', { name: 'Connect only', exact: true }).click()
    await page.getByRole('button', { name: 'Connect computer', exact: true }).click()
    await heading(page, 'Setting up')
  } },
  { screen: 'pairing', state: 'disconnect-confirm', setup: pairingSetup, act: async page => {
    await page.goto('/agents/register-agent')
    await heading(page, 'Connected computers')
    await page.getByRole('button', { name: 'Disconnect' }).first().click()
    await expect(page.getByRole('button', { name: 'Finish runs and disconnect' })).toBeVisible()
  } },
  // 6. Usage dashboard
  { screen: 'usage', state: 'dashboard', setup: async page => {
    await mockWork(page, fixtures())
    await page.route('**/api/usage/dashboard**', route => route.fulfill({ json: usageDashboard() }))
  }, act: async page => { await page.goto('/agents/usage'); await heading(page, 'Usage', 1); await visible(page, '.summary-card') } },
  // 7. Agent rules editor
  { screen: 'rules', state: 'empty', setup: page => rulesSetup(page, true), act: page => openRules(page, true) },
  { screen: 'rules', state: 'populated', setup: page => rulesSetup(page, false), act: page => openRules(page, false) },
  { screen: 'rules', state: 'rule-editing', setup: page => rulesSetup(page, false), act: async page => {
    await openRules(page, false)
    await page.getByRole('button', { name: /Use SVG icons only/ }).first().click()
    const text = page.locator('.detail textarea').first()
    await expect(text).toBeVisible()
    await text.fill('Use SVG icons only, centred in their controls; never text glyphs such as `×` or `›`, and give icon-only buttons an **accessible name**.')
    await text.press('Tab')
    await expect(page.getByText(/unsaved draft/)).toBeVisible()
  } },
  { screen: 'rules', state: 'publish-confirm', setup: page => rulesSetup(page, false), act: async page => {
    await openRules(page, false)
    await page.getByRole('button', { name: 'Publish' }).click()
    const dialog = page.getByRole('dialog', { name: 'Publish' })
    await dialog.getByRole('textbox', { name: 'Publish note' }).fill('September interface conventions and the builder reporting rule (AEON-266).')
  } },
  { screen: 'rules', state: 'import-preview', setup: page => rulesSetup(page, false), act: async page => {
    await openRules(page, false)
    await page.getByRole('button', { name: 'Import drafts' }).click()
    const dialog = page.getByRole('dialog', { name: 'Import drafts' })
    await dialog.locator('#draft-import-file').setInputFiles(importFile())
    await expect(dialog.getByText('Incident response · 6 rules')).toBeVisible()
  } },
  // 8. Journey gates
  { screen: 'journey', state: 'plan', setup: page => journeySetup(page, journeyWorld('plan')), act: openJourney },
  { screen: 'journey', state: 'deploy-refused-pending-gate', setup: page => journeySetup(page, journeyWorld('deploy')), act: openJourney },
  { screen: 'journey', state: 'standing-gate-renewal', setup: page => journeySetup(page, renewalWorld()), act: async page => {
    await openJourney(page)
    await expect(page.getByText('Applied by you · no longer live').first()).toBeVisible()
  } },
  { screen: 'journey', state: 'retry-standing-plus-pending', setup: page => journeySetup(page, retryJourneyWorld('pending')), act: async page => {
    await openJourney(page)
    await expect(decision(page, 'The host did not apply it')).toContainText('Applied by you')
  } },
  { screen: 'journey', state: 'retry-expired-gate', setup: page => journeySetup(page, retryJourneyWorld('expired')), act: async page => {
    await openJourney(page)
    await expect(decision(page, 'The host did not apply it')).toContainText('Approval expired at')
  } },
  { screen: 'journey', state: 'deploy-approval-dialog', setup: page => journeySetup(page, retryJourneyWorld('pending')), act: async page => {
    await openJourney(page)
    await decision(page, 'The host did not apply it').getByRole('button', { name: 'Approve and retry deployment' }).click()
    await expect(page.getByRole('dialog', { name: 'Retry deployment?' })).toBeVisible()
  } },
  // 9. Release creation and the existing ticket picker
  { screen: 'journey', state: 'existing-ticket-picker', setup: async page => {
    const world = journeyWorld('plan')
    await journeySetup(page, world)
    await mockMembership(page, world)
  }, act: async page => {
    await page.goto('/p/PHAROS/journey')
    await page.getByRole('button', { name: 'Add existing' }).click()
    const dialog = page.getByRole('dialog', { name: 'Add existing tickets' })
    await expect(dialog.getByText('Publish the status page')).toBeVisible()
    await dialog.getByRole('checkbox', { name: 'Select PHAROS-22' }).check()
    await dialog.getByRole('checkbox', { name: 'Select PHAROS-26' }).check()
  } },
  { screen: 'releases', state: 'add-to-release-picker', setup: async page => {
    await page.clock.setSystemTime(AT)
    const world = journeyWorld('live')
    await journeySetup(page, world)
    await mockMembership(page, world)
  }, act: async (page, width) => {
    await page.goto('/p/PHAROS')
    for (const key of ['PHAROS-12', 'PHAROS-14']) {
      // Phones hide the row checkboxes; try x on a focused row (no selection may be offered there).
      if (width < 720) { await ticketRow(page, key).focus(); await page.keyboard.press('x'); continue }
      await ticketRow(page, key).hover()
      await ticketRow(page, key).getByRole('checkbox', { name: `Select ${key}` }).check()
    }
    await page.getByRole('toolbar', { name: /selected ticket/ }).getByRole('button', { name: 'Add to release' }).click()
    await expect(page.getByRole('dialog', { name: 'Release for 2 tickets' })).toBeVisible()
  } },
  { screen: 'releases', state: 'history-sheet', setup: async page => {
    await mockWork(page, fixtures())
    await mockReleases(page, releaseHistory())
  }, act: async page => {
    await page.goto(`/releases/${releaseHistory().current}`)
    await expect(page.getByRole('dialog', { name: 'PAIMOS AEON releases' })).toBeVisible()
  } },
  // 10. Project view, header glimpse, ticket workspace
  { screen: 'project', state: 'tickets-glimpse-workers', motion: true, setup: async page => {
    await page.clock.setSystemTime(AT)
    const world = ticketGraphWorld()
    workersOn(world.work, ['PHAROS-100', 'PHAROS-101', 'PHAROS-103', 'PHAROS-113', 'PHAROS-125'].map(key => world.work.nodes.find(n => n.key === key)!))
    Object.assign(world.work.nodes.find(n => n.key === 'PHAROS-101')!.fields, BENEFITS)
    await mockTicketGraph(page, world)
  }, act: async (page, width) => {
    await page.goto('/p/PHAROS/tickets')
    await visible(page, 'tr.ticket-row:not(.ghost), .ticket-row')
    if (width >= 1280) await expect(page.locator('.header-glimpse-canvas')).toHaveAttribute('data-ready', 'true', { timeout: 20_000 })
  } },
  { screen: 'project', state: 'tickets-workers', setup: async page => {
    await page.clock.setSystemTime(AT)
    await mockWork(page, ticketData())
  }, act: async page => { await page.goto('/p/PHAROS'); await visible(page, 'tr.ticket-row:not(.ghost), .ticket-row') } },
  { screen: 'project', state: 'worker-disclosure', setup: async page => {
    await page.clock.setSystemTime(AT)
    await mockWork(page, ticketData(), { liveTruncated: true })
  }, act: async page => {
    await page.goto('/p/PHAROS')
    await ticketRow(page, 'PHAROS-12').getByRole('button', { name: /more worker/ }).click()
    await expect(page.getByRole('dialog', { name: 'Workers on PHAROS-12' })).toBeVisible()
  } },
  { screen: 'ticket', state: 'workspace-with-benefits', setup: async page => {
    await page.clock.setSystemTime(AT)
    await mockWork(page, ticketDataWithAgents())
    await projectAgents(page)
    await ticketAgentWork(page)
  }, act: async page => { await page.goto('/p/PHAROS/PHAROS-11'); await expect(ticketPanel(page)).toBeVisible(); await page.waitForTimeout(400) } },
  { screen: 'ticket', state: 'benefits-missing', setup: async page => {
    await page.clock.setSystemTime(AT)
    await mockWork(page, ticketDataWithAgents())
    await projectAgents(page)
    await ticketAgentWork(page)
  }, act: async page => {
    await page.goto('/p/PHAROS/PHAROS-12')
    await expect(ticketPanel(page).getByRole('region', { name: 'User benefit' })).toBeVisible()
  } },
  { screen: 'ticket', state: 'benefits-edit', setup: async page => {
    await page.clock.setSystemTime(AT)
    await mockWork(page, ticketDataWithAgents())
    await projectAgents(page)
    await ticketAgentWork(page)
  }, act: async page => {
    await page.goto('/p/PHAROS/PHAROS-12')
    await ticketPanel(page).getByRole('button', { name: 'Edit', exact: true }).click()
    await ticketPanel(page).getByLabel('Pill · English').fill('Oracle hosts')
  } },
  // 11. Settings: agent indicator
  { screen: 'settings', state: 'agent-indicator', setup: async page => { await mockIndicator(page) }, act: async page => {
    await page.goto('/settings/personal#agents')
    await expect(page.getByRole('radiogroup', { name: 'Agent indicator' })).toBeVisible()
  } },
  // 12. Projects overview with live agents
  { screen: 'projects', state: 'cards-live', setup: async page => { await mockIndicator(page) }, act: async page => { await page.goto('/'); await visible(page, '.card .live-chip') } },
  { screen: 'projects', state: 'list-live', setup: async page => {
    const { data } = await mockIndicator(page)
    data.preferences.projects = { view: 'list' }
  }, act: async page => { await page.goto('/'); await visible(page, '.project-item .live-chip') } },
  { screen: 'projects', state: 'live-agents-dialog', setup: async page => { await mockIndicator(page) }, act: async page => {
    await page.goto('/')
    await page.locator('.card .live-chip').first().click()
    await expect(page.getByRole('dialog', { name: /^Active agents on/ })).toBeVisible()
  } },
]

async function pairingSetup(page: Page) {
  await page.clock.setSystemTime(new Date('2026-09-27T20:00:00.000Z'))
  await mockWork(page, fixtures())
  await mockPairing(page)
}
async function pairingReview(page: Page) {
  await page.goto('/agents/register-agent')
  await heading(page, 'Connect a computer')
  await page.getByLabel('Pairing code').fill('123-456-789')
  await page.getByRole('button', { name: 'Look up code' }).click()
  await expect(page.getByRole('region', { name: 'Pairing review' }).getByRole('heading', { name: 'Review this computer' })).toBeVisible()
}

// The app scrolls inside its own containers, so a full-page shot alone shows one
// screen. Grow the viewport by the largest hidden scroll height (a few passes, as
// layouts reflow), scrolled to the top, so the shot holds the whole content.
async function growToContent(page: Page, base: number) {
  let height = base
  for (let pass = 0; pass < 4; pass++) {
    const hidden = await page.evaluate(() => {
      let most = 0
      for (const el of [document.documentElement, ...document.querySelectorAll('body *')]) {
        const style = getComputedStyle(el)
        if (el !== document.documentElement && !/(auto|scroll)/.test(style.overflowY)) continue
        if (el.clientHeight === 0) continue
        el.scrollTop = 0
        most = Math.max(most, el.scrollHeight - el.clientHeight)
      }
      return most
    }).catch(() => 0)
    if (hidden < 4 || height >= 9000) break
    height = Math.min(9000, height + hidden)
    await page.setViewportSize({ width: page.viewportSize()!.width, height })
    await page.waitForTimeout(250)
  }
}

const openDialog = (page: Page) => page.evaluate(() => [...document.querySelectorAll('[role="dialog"], dialog[open]')].some(el => {
  const box = el.getBoundingClientRect()
  return box.width > 0 && box.height > 0 && getComputedStyle(el).visibility !== 'hidden'
})).catch(() => false)
const scrollDialogToEnd = (page: Page) => page.evaluate(() => {
  let moved = false
  for (const dialog of document.querySelectorAll('[role="dialog"], dialog[open]')) {
    for (const el of [dialog, ...dialog.querySelectorAll('*')]) {
      if (!/(auto|scroll)/.test(getComputedStyle(el).overflowY) || el.scrollHeight - el.clientHeight < 4) continue
      el.scrollTop = el.scrollHeight
      moved = true
    }
  }
  return moved
}).catch(() => false)

const filter = process.env.VISUAL_AUDIT_FILTER ?? ''
for (const shot of shots.filter(s => !filter || `${s.screen}__${s.state}`.includes(filter))) {
  test(`${shot.screen} ${shot.state}`, async ({ browser }) => {
    test.setTimeout(300_000)
    mkdirSync(join(OUT, 'notes'), { recursive: true })
    const notes: Note[] = []
    for (const theme of THEMES) for (const width of WIDTHS) {
      const context = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 1000 }, colorScheme: theme, reducedMotion: shot.motion ? 'no-preference' : 'reduce' })
      const page = await context.newPage()
      page.setDefaultTimeout(10_000)
      const problems: string[] = []
      page.on('pageerror', error => problems.push(`pageerror: ${error.message.slice(0, 300)}`))
      page.on('console', message => { if (message.type() === 'error' && !/^Failed to load resource/.test(message.text())) problems.push(`console: ${message.text().slice(0, 300)}`) })
      // Unmocked or refused API reads, by path, instead of the browser's anonymous line.
      page.on('response', response => { if (response.status() >= 400) problems.push(`http ${response.status()} ${response.request().method()} ${new URL(response.url()).pathname}`) })
      let failed = false
      try {
        await shot.setup(page)
        await shot.act(page, width)
      } catch (error) {
        failed = true
        problems.push(`not reached: ${String(error).split('\n').slice(0, 3).join(' ').slice(0, 400)}`)
      }
      await page.evaluate(() => document.fonts.ready).catch(() => {})
      await page.waitForTimeout(350)
      const overflow = await page.evaluate(() => {
        const root = document.documentElement
        return root.scrollWidth > root.clientWidth + 1 ? `${root.scrollWidth}px wide in a ${root.clientWidth}px viewport` : ''
      }).catch(() => '')
      if (overflow) problems.push(`horizontal overflow: ${overflow}`)
      const name = (suffix = '') => `${shot.screen}__${shot.state}${suffix}${failed ? '__not-reached' : ''}__${width}__${theme as Theme}.png`
      const file = name()
      const snap = async (path: string) => {
        const options = { path: join(OUT, path), fullPage: true, animations: shot.motion ? 'allow' as const : 'disabled' as const }
        // A very tall capture can fail in Chromium; retry once at a smaller height.
        await page.screenshot(options).catch(async () => {
          const size = page.viewportSize()!
          await page.setViewportSize({ width: size.width, height: Math.min(size.height, 6000) })
          await page.waitForTimeout(250)
          await page.screenshot(options).catch(error => problems.push(`screenshot failed: ${String(error).split('\n')[0]!.slice(0, 200)}`))
        })
      }
      // An open dialog is shot as seen, at the real viewport height; when its own
      // content scrolls, a second shot shows it scrolled to the end.
      if (await openDialog(page)) {
        await snap(file)
        if (await scrollDialogToEnd(page)) await snap(name('-scrolled'))
      } else {
        await growToContent(page, width === 390 ? 844 : 1000)
        await snap(file)
      }
      notes.push({ file, problems: [...new Set(problems)] })
      await context.close()
    }
    writeFileSync(join(OUT, 'notes', `${shot.screen}__${shot.state}.json`), `${JSON.stringify(notes, null, 2)}\n`)
  })
}
