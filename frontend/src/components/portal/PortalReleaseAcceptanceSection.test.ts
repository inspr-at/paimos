import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick } from 'vue'

import PortalReleaseAcceptanceSection from './PortalReleaseAcceptanceSection.vue'
import { api } from '@/api/client'
import type { Acceptance } from '@/services/projectReleaseAcceptance'

vi.mock('@/api/client', () => ({
  api: { get: vi.fn(), post: vi.fn() },
  errMsg: (_e: unknown, fallback: string) => fallback,
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 44 } }),
}))

async function settle() {
  for (let i = 0; i < 8; i += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

const acceptance: Acceptance = {
  id: 1,
  release: {
    id: 7, project_id: 9, batch_id: 4, release_ref: 'release_abc', batch_key: 'batch-acc',
    baseline_ref: 'baseline_7',
    content_digest: 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    revision_seal: 'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    artifact_digest: 'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
    artifact_coordinate: 'ghcr:demo:acc', version_scheme: 'inspr-calendar-v1', release_channel: 'stable',
    release_sequence: 1, version: '26.09.08', commit_sha: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    state: 'acceptance_pending', revision: 1, created_at: '2026-09-08T12:00:00Z',
  },
  revision: 1, status: 'pending', operating_mode: 'customer_operated', operating_mode_label: 'customer-operated',
  agreement_ref: 'SOW-9',
  disclosed_gaps: [{ gap_ref: 'gap_backup', statement: 'Backup restore not proven for this target.' }],
  required_party_refs: ['party_customer'], delivery_party_ref: 'party_delivery', operator_party_ref: 'party_customer',
  parties: [
    { party_ref: 'party_customer', kind: 'linked_user', user_id: 44, email: 'c@example.test', display_name: 'Customer', roles: ['operator'] },
  ],
  confirmations: [{
    party_ref: 'party_customer', party_name: 'Customer', decision: 'accept', source: 'platform',
    source_label: 'platform confirmation', actor_user_id: 44, confirmed_at: '2026-09-08T12:01:00Z', acceptance_revision: 1,
  }],
  email_evidence: [],
  preview_subject: '', preview_body: '', preview_revision: 0,
  missing: { confirmations: [], email_coverage: [], preview: true, send: true },
  defaults: { agreement_ref: 'SOW-9' },
  offer_disclaimer: 'Provider-operated is representable here. It is not an offer that Augmentoring will operate the service.',
}

describe('PortalReleaseAcceptanceSection', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  it('shows party names and attestation source instead of storage refs', async () => {
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      if (path.endsWith('/release-records') && !path.includes('/acceptance')) return [acceptance.release]
      return acceptance
    })
    const host = document.createElement('div')
    document.body.appendChild(host)
    const app = createApp(defineComponent({
      setup() {
        return () => h(PortalReleaseAcceptanceSection, { projectId: 9 })
      },
    }))
    app.mount(host)
    await settle()
    expect(host.textContent).toContain('Backup restore not proven for this target.')
    expect(host.textContent).not.toContain('gap_backup:')
    expect(host.querySelector('[data-testid="portal-confirmations"]')?.textContent).toContain('Customer')
    expect(host.querySelector('[data-testid="portal-confirmations"]')?.textContent).toContain('platform confirmation')
    expect(host.textContent).not.toContain('party_customer')
    app.unmount()
  })
})
