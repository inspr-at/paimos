// SPDX-License-Identifier: AGPL-3.0-only
// The server side of the Model registry card for UI tests: the profile list, the
// auto-update settings, Check now with its cooldown, line edits, the remove
// preview and retire/restore. Every write is recorded so a test can assert the
// exact calls, and a test can make any one of them fail.
import { expect, type Page } from '@playwright/test'

export interface MockProfile {
  id: string; slug: string; version: string; harness: string; family: string; model: string; effort: string; tier: string; enabled: boolean; created_at: string
  display_name?: string; short_name?: string; model_version?: string; note?: string; source: 'auto' | 'manual'; retire_at: string | null; retired: boolean; effort_level: number | null; provider?: string
}
export interface Write { method: string; path: string; body: unknown }
export interface RegistryWorld {
  profiles: MockProfile[]; permissions: string[]
  /** AEON-1054: Auto-update is the account matrix's New model versions rule and sends this revision. */
  accountUseRevision: number
  settings: { agent_reports_enabled: boolean; auto_add_profiles: boolean; api_enabled: boolean; interval_minutes: number }
  lastRun: string | null; writes: Write[]; reads: string[]
  /** What the next POST /models/refresh answers: success with new lines, a 429 or a 500. `added` counts profiles. `acceptedModels` lists distinct models the server accepted. */
  check: { status: 200 | 429 | 500; retryAfter?: number; newLines?: string[]; added?: number; acceptedModels?: string[]; sources?: { state: string; vendor?: string; account_id?: string; seen?: number }[] }
  failPut: boolean; conflictPut: boolean; failSettings: boolean; failUsage: boolean; failRetireAfter: number | null; failList: boolean
  /** While set, retire answers wait for it: a barrier that holds a removal in flight. */
  gateRetire: Promise<void> | null
  /** While set, the usage answer waits, so a test can change the line before the revision is captured. */
  gateUsage: Promise<void> | null
  /** Registry revision returned with the usage snapshot. */
  usageRevision: string
  usage: { used_by: { column: string; layer: string; person?: string; replacement: { line: string | null; effort: string | null; harness?: string; model?: string } }[]; incomplete: boolean }
}
const LEVELS: Record<string, number | null> = { off: 0, low: 1, medium: 2, high: 3, xhigh: 4, max: 5, default: null }
let counter = 0
export function mockProfile(over: Partial<MockProfile> & Pick<MockProfile, 'harness' | 'model' | 'effort'>): MockProfile {
  counter++
  return {
    id: `00000000-0000-4000-8000-${String(counter).padStart(12, '0')}`, slug: `seed-${counter}`, version: '1', family: 'openai', tier: 'strong', enabled: true, created_at: '2026-09-20T08:00:00Z',
    source: 'auto', retire_at: null, retired: false, effort_level: LEVELS[over.effort] ?? null, ...over,
  }
}
const levels = (base: Omit<Parameters<typeof mockProfile>[0], 'effort'>, efforts: string[]) => efforts.map(effort => mockProfile({ ...base, effort }))

export function registryProfiles(): MockProfile[] {
  return [
    ...levels({ harness: 'codex', model: 'gpt-6.1-sol', version: '2026-10-03-accepted', created_at: '2026-10-03T12:00:00Z', display_name: 'GPT Sol', short_name: 'Sol', model_version: '6.1', note: 'strongest for building' }, ['low', 'medium', 'high', 'xhigh']),
    ...levels({ harness: 'codex', model: 'gpt-6-sol', display_name: 'GPT Sol', short_name: 'Sol', model_version: '6', retired: true }, ['high']),
    ...levels({ harness: 'codex', model: 'gpt-6-astra', display_name: 'GPT Astra', short_name: 'Astra', model_version: '6', note: 'deepest reasoning, slower' }, ['low', 'medium', 'high', 'xhigh']),
    ...levels({ harness: 'codex', model: 'gpt-6-luna', display_name: 'GPT Luna', short_name: 'Luna', model_version: '6', note: 'fast and light', retire_at: '2026-10-31T12:00:00Z' }, ['low', 'medium', 'high', 'xhigh']),
    ...levels({ harness: 'claude', model: 'claude-opus-5-5', family: 'anthropic', display_name: 'Claude Opus', short_name: 'Opus', model_version: '5.5', note: 'best for design and concepts' }, ['high', 'xhigh', 'max']),
    ...levels({ harness: 'claude', model: 'claude-sonnet-5-5', family: 'anthropic', display_name: 'Claude Sonnet', short_name: 'Sonnet', model_version: '5.5', note: 'fast everyday work' }, ['high', 'xhigh', 'max']),
    ...levels({ harness: 'grok', model: 'grok-4.7', family: 'xai', display_name: 'Grok', short_name: 'Grok', model_version: '4.7', note: 'reviews and concepts' }, ['medium', 'high', 'xhigh']),
    ...levels({ harness: 'grok', model: 'grok-4.8-preview', family: 'xai', display_name: 'Grok', short_name: 'Grok', model_version: '4.8 preview', enabled: false }, ['high']),
    ...levels({ harness: 'cursor', model: 'composer-2.5', family: 'cursor', display_name: 'Composer', short_name: 'Composer', model_version: '2.5', note: 'quick edits in the editor' }, ['default']),
    ...levels({ harness: 'pi', model: 'openrouter/qwen/qwen3-coder', family: 'unknown', display_name: 'Qwen3 Coder', short_name: 'Qwen3 Coder', note: 'trial for small scripts (AEON-1003)', source: 'manual', tier: 'standard' }, ['off']),
  ]
}
export function registryWorld(over: Partial<RegistryWorld> = {}): RegistryWorld {
  return {
    profiles: registryProfiles(), permissions: ['models.read', 'models.manage', 'models.refresh', 'account.use.manage'], accountUseRevision: 7,
    settings: { agent_reports_enabled: true, auto_add_profiles: true, api_enabled: true, interval_minutes: 360 }, lastRun: '2026-10-09T04:12:00Z', writes: [], reads: [],
    check: { status: 200 }, failPut: false, conflictPut: false, failSettings: false, failUsage: false, failRetireAfter: null, failList: false, gateRetire: null, gateUsage: null, usageRevision: 'r1',
    usage: { used_by: [], incomplete: false }, ...over,
  }
}

/** Registers the routes; the returned world is the live state a test can change or inspect. */
export async function mockRegistry(page: Page, world: RegistryWorld, options: { permissions?: boolean } = {}) {
  let retired = 0
  const decode = (value: string) => decodeURIComponent(value)
  const sameLine = (profile: MockProfile, harness: string, model: string) => profile.harness === harness && profile.model === model && !profile.retired
  if (options.permissions !== false) await page.route('**/api/me/permissions*', route => route.fulfill({ json: { workspace: { id: 'registry-tenant', role: 'admin', permissions: world.permissions }, project: null } }))
  await page.route('**/api/account-use?*', route => route.fulfill({ json: { rules: { new_accounts: 'ask', new_contexts: 'ask', new_projects: 'default', new_models: world.settings.auto_add_profiles ? 'allow' : 'shipped_only', revision: world.accountUseRevision, enforced_at: null, confirmation_required: false, confirmed_at: null }, accounts: [], contexts: [], cells: [], next_account: null, next_context: null, running_outside: [], running_outside_truncated: false } }))
  await page.route('**/api/work-kinds*', route => route.fulfill({ json: { items: [{ id: 'k1', slug: 'backend', label: 'Backend build', hint: '', position: 1, examples: [], labels: [], ticket_count: 0 }], next_cursor: null } }))
  await page.route(/\/api\/models(\/.*)?(\?.*)?$/, async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname.replace(/^\/api/, ''), method = request.method()
    const body = request.postData() ? request.postDataJSON() : undefined
    if (method !== 'GET') world.writes.push({ method, path: decode(path), body })
    else world.reads.push(decode(path))
    if (path === '/models' && method === 'GET') return world.failList ? route.fulfill({ status: 500, json: { error: 'boom' } }) : route.fulfill({ json: world.profiles })
    if (path === '/models' && method === 'POST') {
      const created = mockProfile({ ...(body as object), source: 'manual', model: body.model, harness: body.harness, effort: body.effort, created_at: new Date().toISOString() })
      world.profiles.push(created)
      return route.fulfill({ status: 201, json: created })
    }
    if (path === '/models/refresh' && method === 'GET') return route.fulfill({ json: { settings: world.settings, last_run_at: world.lastRun, last_result: {}, sources: [], observations: [] } })
    if (path === '/models/refresh' && method === 'POST') {
      if (world.check.status === 429) return route.fulfill({ status: 429, headers: { 'Retry-After': String(world.check.retryAfter ?? 300) }, json: { error: 'model refresh cooldown', retry_after: world.check.retryAfter ?? 300 } })
      if (world.check.status === 500) return route.fulfill({ status: 500, json: { error: 'refresh failed' } })
      world.lastRun = new Date().toISOString()
      return route.fulfill({ json: { new_lines: world.check.newLines ?? [], ...(world.check.added === undefined ? {} : { added: world.check.added }), ...(world.check.acceptedModels ? { accepted_models: world.check.acceptedModels } : {}), ...(world.check.sources ? { sources: world.check.sources } : {}) } })
    }
    if (path === '/models/refresh/settings' && method === 'PUT') {
      if (world.failSettings) return route.fulfill({ status: 500, json: { error: 'settings failed' } })
      const { account_use_revision: revision, ...stored } = body as typeof world.settings & { account_use_revision?: number }
      if (stored.auto_add_profiles !== world.settings.auto_add_profiles && revision !== world.accountUseRevision) return route.fulfill({ status: 409, json: { error: 'account_use_revision_conflict' } })
      if (revision !== undefined) world.accountUseRevision++
      world.settings = stored
      return route.fulfill({ json: world.settings })
    }
    const line = /^\/models\/lines\/([^/]+)\/([^/]+)(\/usage)?$/.exec(path)
    if (line) {
      const harness = decode(line[1]!), model = decode(line[2]!)
      if (line[3]) {
        if (world.gateUsage) await world.gateUsage
        if (world.failUsage) return route.fulfill({ status: 500, json: { error: 'usage failed' } })
        const lineProfiles = world.profiles.filter(profile => sameLine(profile, harness, model))
        return route.fulfill({ json: { ...world.usage, replacement: world.usage.used_by[0]?.replacement ?? { line: null, effort: null }, revision: world.usageRevision, line_profiles: lineProfiles } })
      }
      if (world.failPut) return route.fulfill({ status: 422, json: { error: 'provider route does not match model namespace' } })
      if (world.conflictPut) return route.fulfill({ status: 409, json: { error: 'stale registry revision' } })
      const before = world.profiles.filter(profile => sameLine(profile, harness, model))
      if (!before.length) return route.fulfill({ status: 404, json: { error: 'model line not found' } })
      before.forEach(profile => { profile.retired = true })
      const next = body.model ?? model, made = (body.efforts as string[]).map((effort, index) => mockProfile({
        harness, model: next, effort, family: before[0]!.family, display_name: body.display_name, short_name: body.display_name, note: body.note, source: before[0]!.source, tier: before[0]!.tier, effort_level: LEVELS[effort] ?? Math.min(index, 5),
        model_version: before[0]!.model_version, version: `edit-${world.writes.length}`, created_at: new Date().toISOString(),
      }))
      world.profiles.push(...made)
      return route.fulfill({ json: { profiles: made, revision: 'r2' } })
    }
    const retire = /^\/models\/([^/]+)\/retire$/.exec(path)
    if (retire) {
      const profile = world.profiles.find(candidate => candidate.id === retire[1])
      if (!profile) return route.fulfill({ status: 404, json: { error: 'not found' } })
      if (method === 'POST') {
        if (world.gateRetire) await world.gateRetire
        if (world.failRetireAfter !== null && retired >= world.failRetireAfter) return route.fulfill({ status: 500, json: { error: 'retire failed' } })
        retired++; profile.retired = true; return route.fulfill({ json: { profile_id: profile.id, retired: true } })
      }
      profile.retired = false; return route.fulfill({ json: { profile_id: profile.id, retired: false } })
    }
    return route.fulfill({ status: 404, json: { error: `unmocked ${method} ${path}` } })
  })
  return world
}

export async function openRegistry(page: Page, query = '') {
  await page.goto(`/tests/models-registry-harness.html${query}`)
  await expect(page.locator('[data-model-registry]')).toBeVisible()
}
export const lastWrite = (world: RegistryWorld) => world.writes.at(-1)
export const modifier = 'ControlOrMeta'
