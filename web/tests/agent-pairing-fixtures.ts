// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test'

const REQUEST = '11111111-1111-4111-8111-111111111111'
const TENANT = '22222222-2222-4222-8222-222222222222'
const COMPUTER = '33333333-3333-4333-8333-333333333333'
const ACCOUNT = '44444444-4444-4444-8444-444444444444'
const ACCOUNT_2 = '55555555-5555-4555-8555-555555555555'
const MODEL = '66666666-6666-4666-8666-666666666666'
const DIGEST = 'ab'.repeat(32)

export const SETUP_COMMAND = `"<verified absolute paimos-agentd path>" pair --url 'https://aeon.example'`
export const NIX_PAIR_COMMAND = "aeon-agentd pair --url 'https://aeon.example'"

export function pairingGuide() {
  return {
    instance_url: 'https://aeon.example',
    default_tenant_slug: 'inspr',
    protocol: 'pairing-v1',
    platforms: ['darwin/arm64', 'linux/amd64'],
    version: '260927181849.0.0',
    platform_qualification: 'candidate; consult the exact release service qualification evidence',
    setup_command: SETUP_COMMAND,
    install_available: false,
    install_targets: [],
    managed_installation: 'Use the owning Nix or Home Manager configuration.',
    managed_setup: {
      command: NIX_PAIR_COMMAND,
      service_option: 'services.aeon.enable',
      module_url: 'https://example.test/instance/module.nix',
      service_note: 'The current Home Manager module needs a paired-service update before this computer can connect.',
      platform_note: 'Service module: macOS only.',
      prerequisite_note: 'Use aeon-agentd on PATH from a reviewed release pin with pair; a service module alone does not ensure this.',
    },
  }
}

function verification(mode: string | null = null) {
  return {
    mode, policy: 'read_only', runs_per_account: 1, max_parallel_runs: 1, max_duration_seconds: 60,
    allowance: 1, unit: 'requests', expires_at: '2026-09-27T20:30:00.000Z',
    task: 'Reply exactly AEON_VERIFIED.',
  }
}

function base(overrides: Record<string, unknown> = {}) {
  return {
    request_id: REQUEST, tenant_id: TENANT, tenant_name: 'INSPR', state: 'pending', request_digest: DIGEST,
    expires_at: '2099-01-01T00:00:00.000Z', computer_name: 'studio', platform: 'darwin', arch: 'arm64',
    workspace_path: '/Users/markus/work', capabilities: ['managed_runs'],
    requested_accounts: [
      { account_key: 'cursor-1', harness: 'cursor', label: 'Cursor work', model_profile_id: MODEL },
      { account_key: 'codex-1', harness: 'codex', label: 'Codex work', model_profile_id: MODEL },
    ],
    verification: verification(),
    verification_capabilities: {
      cursor: { supported: false, policy: 'unavailable', reason: 'Cursor ask mode and an isolated config do not enforce a no-tools policy.' },
      codex: { supported: false, policy: 'unavailable', reason: 'Codex read-only sandboxing does not isolate inherited MCP tools and startup hooks.' },
    },
    verification_helper_version: '260927181849.0.0',
    computer_id: null, computer_state: null, principal_id: null, daemon_id: null,
    local_cleanup: 'pending', local_processes: 'unconfirmed', enrollments: [], revision: 1,
    setup_state: 'not_started', setup_error: '', last_seen_at: null, connectivity: 'unknown', accounting_state: 'settled',
    ...overrides,
  }
}

function enrollment(id: string, key: string, harness: string, label: string) {
  return {
    account_id: id, account_key: key, harness, label, model_profile_id: MODEL, state: 'connected',
    local_cleanup: 'pending', verification_run_id: null, active_run_ids: [], verification_state: 'not_selected',
    verification_error: '', local_processes: 'unconfirmed', accounting_state: 'settled',
  }
}

export interface PairingCall { path: string; method: string; body: unknown }

export async function mockPairing(page: Page, reviewOverrides: Record<string, unknown> = {}) {
  const calls: PairingCall[] = []
  await page.route('**/api/me/permissions*', route => route.fulfill({
    json: { workspace: { role: { id: 'role-admin', key: 'admin', name: 'Admin' }, permissions: ['account.manage', 'account.read', 'authz.read'] }, project: null },
  }))
  await page.route('**/api/agent-pairing/**', async route => {
    const url = new URL(route.request().url())
    const path = url.pathname
    const method = route.request().method()
    let body: unknown = null
    try { body = route.request().postDataJSON() } catch { body = null }
    calls.push({ path, method, body })
    if (path === '/api/agent-pairing/guide') return route.fulfill({ json: pairingGuide() })
    if (path === '/api/agent-pairing/lookup' && method === 'POST') {
      const code = (body as { user_code?: string })?.user_code
      if (code === '111-222-333') return route.fulfill({ json: base({ existing_computer_id: COMPUTER, computer_name: 'studio' }) })
      return route.fulfill({ json: base(reviewOverrides) })
    }
    if (path.endsWith('/approve') && method === 'POST') {
      return route.fulfill({ json: base({
        state: 'approved', computer_id: COMPUTER, computer_state: 'connected', revision: 2, setup_state: 'approved',
        verification: verification((body as { verification: string }).verification),
        enrollments: [enrollment(ACCOUNT_2, 'codex-1', 'codex', 'Codex work')],
      }) })
    }
    if (path.endsWith('/deny') && method === 'POST') return route.fulfill({ json: base({ state: 'denied' }) })
    if (path === '/api/agent-pairing/computers' && method === 'GET') {
      return route.fulfill({ json: { computers: [base({
        state: 'redeemed', computer_id: COMPUTER, computer_state: 'connected', revision: 4, setup_state: 'connected',
        connectivity: 'online', last_seen_at: new Date().toISOString(), verification: verification('connect_only'),
        enrollments: [enrollment(ACCOUNT, 'cursor-1', 'cursor', 'Cursor work'), enrollment(ACCOUNT_2, 'codex-1', 'codex', 'Codex work')],
      })] } })
    }
    if (path === `/api/agent-pairing/computers/${COMPUTER}` && method === 'GET') {
      return route.fulfill({ json: base({
        state: 'redeemed', computer_id: COMPUTER, computer_state: 'connected', revision: 4, setup_state: 'connected',
        connectivity: 'online', verification: verification('connect_only'),
        enrollments: [enrollment(ACCOUNT, 'cursor-1', 'cursor', 'Cursor work'), enrollment(ACCOUNT_2, 'codex-1', 'codex', 'Codex work')],
      }) })
    }
    if (path.endsWith('/disconnect') && method === 'POST') {
      return route.fulfill({ json: base({
        state: 'redeemed', computer_id: COMPUTER, computer_state: 'draining', revision: 5, setup_state: 'connected',
        connectivity: 'online', local_processes: 'unconfirmed', accounting_state: 'unconfirmed',
        enrollments: [enrollment(ACCOUNT, 'cursor-1', 'cursor', 'Cursor work'), enrollment(ACCOUNT_2, 'codex-1', 'codex', 'Codex work')],
      }) })
    }
    return route.fulfill({ status: 404, json: { error: 'not found', code: 'not_found' } })
  })
  return calls
}

export async function mockAnonymousGuide(page: Page) {
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/version') return route.fulfill({ json: { version: '260927181849.0.0', scheme: 'inspr-calendar-v2' } })
    if (path === '/api/agent-pairing/guide') return route.fulfill({ json: pairingGuide() })
    if (path === '/api/me') return route.fulfill({ status: 401, json: {} })
    return route.fulfill({ status: 404, json: { error: 'unmocked' } })
  })
}

// Builders for other fixtures (capacity, agents desk).
export { base as pairingView, enrollment as pairingEnrollment }
