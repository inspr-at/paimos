import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick } from 'vue'

import ProjectReleaseAcceptanceSection from './ProjectReleaseAcceptanceSection.vue'
import { api } from '@/api/client'
import type { Acceptance } from '@/services/projectReleaseAcceptance'

vi.mock('vue-router', () => ({
  RouterLink: { props: ['to'], template: '<a><slot /></a>' },
}))

vi.mock('@/api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn() },
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

const acceptanceFixture: Acceptance = {
  id: 1,
  release: {
    id: 7,
    project_id: 9,
    batch_id: 4,
    release_ref: 'release_abc',
    batch_key: 'batch-acc',
    baseline_ref: 'baseline_7',
    content_digest: 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    revision_seal: 'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    artifact_digest: 'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
    artifact_coordinate: 'ghcr:demo:acc',
    version_scheme: 'inspr-calendar-v1',
    release_channel: 'stable',
    release_sequence: 1,
    version: '26.09.08',
    commit_sha: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    state: 'acceptance_pending',
    revision: 1,
    created_at: '2026-09-08T12:00:00Z',
  },
  revision: 1,
  status: 'pending',
  operating_mode: 'agency_operated',
  operating_mode_label: 'provider-operated',
  agreement_ref: 'SOW-9',
  disclosed_gaps: [{ gap_ref: 'gap_backup', statement: 'Backup restore not proven for this target.' }],
  required_party_refs: ['party_customer', 'party_delivery'],
  delivery_party_ref: 'party_delivery',
  operator_party_ref: 'party_customer',
  parties: [
    { party_ref: 'party_customer', kind: 'linked_user', user_id: 44, email: 'c@example.test', display_name: 'Customer', roles: ['operator'] },
    { party_ref: 'party_delivery', kind: 'linked_user', user_id: 12, email: 'd@example.test', display_name: 'Delivery', roles: ['delivery_party'] },
  ],
  confirmations: [],
  email_evidence: [],
  preview_subject: '',
  preview_body: '',
  preview_revision: 0,
  missing: {
    confirmations: ['party_customer', 'party_delivery'],
    email_coverage: ['party_customer', 'party_delivery'],
    preview: true,
    send: true,
  },
  defaults: { agreement_ref: 'SOW-9' },
  offer_disclaimer: 'Provider-operated is representable here. It is not an offer that Augmentoring will operate the service.',
}

async function mountSection() {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(defineComponent({
    setup() {
      return () => h(ProjectReleaseAcceptanceSection, { projectId: 9, canWrite: true })
    },
  }))
  app.mount(host)
  await settle()
  return { host, app }
}

describe('ProjectReleaseAcceptanceSection', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  it('shows provider-operated label, disclaimer, missing steps, and own-party confirm', async () => {
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      if (path.includes('/release-records/') && path.endsWith('/acceptance')) return acceptanceFixture
      if (path.endsWith('/release-records')) return [acceptanceFixture.release]
      if (path.includes('baseline-batches')) return { batches: [{ id: 4, batch_key: 'batch-acc' }], active_batch: { id: 4, batch_key: 'batch-acc' } }
      if (path.includes('standing-policies')) return []
      return []
    })
    const { host, app } = await mountSection()
    const text = host.textContent || ''
    expect(text).toContain('provider-operated')
    expect(text).toContain('not an offer that Augmentoring will operate')
    expect(text).toContain('Confirmation: party_customer')
    expect(text).toContain('Sent email covering')
    expect(text).toContain('Confirm as Customer')
    expect(text).toContain('No agency consent is inferred')
    expect(host.querySelector('[data-testid="release-acceptance"]')).toBeTruthy()
    app.unmount()
  })
})
