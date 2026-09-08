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

function mockLoads(acc: Acceptance = acceptanceFixture) {
  vi.mocked(api.get).mockImplementation(async (path: string) => {
    if (path.includes('/release-records/') && path.endsWith('/acceptance')) return acc
    if (path.endsWith('/release-records')) return [acc.release]
    if (path.includes('baseline-batches')) return { batches: [{ id: 4, batch_key: 'batch-acc' }], active_batch: { id: 4, batch_key: 'batch-acc' } }
    if (path.includes('standing-policies')) return []
    if (path === '/users') {
      return [
        { id: 44, username: 'customer', email: 'c@example.test', status: 'active', nickname: 'Customer', first_name: '', last_name: '' },
        { id: 12, username: 'delivery', email: 'd@example.test', status: 'active', nickname: 'Delivery', first_name: '', last_name: '' },
      ]
    }
    return []
  })
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

  it('loads row editors instead of pipe syntax and can save, confirm, preview, and send', async () => {
    mockLoads()
    vi.mocked(api.put).mockResolvedValue({ ...acceptanceFixture, revision: 2 })
    vi.mocked(api.post).mockImplementation(async (path: string) => {
      if (path.endsWith('/confirm')) {
        return { ...acceptanceFixture, confirmations: [{ party_ref: 'party_customer', decision: 'accept', source: 'platform', actor_user_id: 44, confirmed_at: '2026-09-08T12:01:00Z' }] }
      }
      if (path.endsWith('/email/preview')) {
        return { ...acceptanceFixture, preview_subject: 'Accept', preview_body: 'Please accept', preview_revision: 1, missing: { ...acceptanceFixture.missing, preview: false } }
      }
      if (path.endsWith('/authorize-send')) {
        return { ...acceptanceFixture, status: 'pending', mail_recovery: '', preview_revision: 1 }
      }
      return acceptanceFixture
    })
    const { host, app } = await mountSection()
    const text = host.textContent || ''
    expect(text).toContain('provider-operated')
    expect(text).toContain('not an offer that Augmentoring will operate')
    expect(host.querySelector('[data-testid="party-editor"]')).toBeTruthy()
    expect(host.querySelector('[data-testid="gap-editor"]')).toBeTruthy()
    expect(host.querySelector('textarea')?.getAttribute('placeholder') ?? '').not.toContain('|')
    expect(text).not.toContain('ref | kind | user_id')
    expect(host.querySelector('[data-testid="missing-list"]')?.textContent).toContain('Customer')
    expect(host.querySelector('[data-testid="missing-list"]')?.textContent).not.toContain('party_customer')
    const attests = [...host.querySelectorAll<HTMLInputElement>('[data-testid="attest-party"]')]
    expect(attests.length).toBeGreaterThan(0)
    expect(attests.every((box) => !box.checked)).toBe(true)
    expect(host.querySelector('select')).toBeTruthy()

    host.querySelector<HTMLButtonElement>('[data-testid="add-party"]')!.click()
    host.querySelector<HTMLButtonElement>('[data-testid="add-gap"]')!.click()
    await settle()
    expect(host.querySelectorAll('[data-testid="party-editor"] .ra-party').length).toBe(3)
    expect(host.querySelectorAll('[data-testid="gap-editor"] .ra-row').length).toBe(2)

    host.querySelector<HTMLButtonElement>('[data-testid="save-arrangement"]')!.click()
    await settle()
    expect(api.put).toHaveBeenCalled()
    const putBody = vi.mocked(api.put).mock.calls[0][1] as { parties: { user_id?: number; email: string }[]; disclosed_gaps: { statement: string }[] }
    expect(putBody.parties.some((p) => p.user_id === 44)).toBe(true)
    expect(putBody.disclosed_gaps[0].statement).toContain('Backup restore')

    host.querySelector<HTMLButtonElement>('[data-testid="confirm-own"]')!.click()
    await settle()
    expect(api.post).toHaveBeenCalledWith(
      '/projects/9/release-records/7/acceptance/confirm',
      expect.objectContaining({ party_ref: 'party_customer' }),
    )

    const subject = host.querySelector<HTMLInputElement>('input')
    expect(subject).toBeTruthy()
    host.querySelector<HTMLButtonElement>('[data-testid="save-preview"]')!.click()
    await settle()
    expect(vi.mocked(api.post).mock.calls.some((call) => String(call[0]).endsWith('/email/preview'))).toBe(true)

    expect(host.querySelector<HTMLButtonElement>('[data-testid="authorize-send"]')?.disabled).toBe(true)
    host.querySelector<HTMLInputElement>('[data-testid="confirm-send"]')!.click()
    await settle()
    expect(host.querySelector<HTMLButtonElement>('[data-testid="authorize-send"]')?.disabled).toBe(false)
    expect(host.textContent).not.toContain('actor 44')
    app.unmount()
  })

  it('renders mail evidence and keeps send disabled while a message is in flight', async () => {
    mockLoads({
      ...acceptanceFixture,
      mail_in_flight: true,
      mail_recovery: 'Delivery is uncertain. Do not send this message again until it is reconciled. Automatic retry is not used.',
      confirmations: [{
        party_ref: 'party_customer',
        party_name: 'Customer',
        decision: 'accept',
        source: 'platform',
        source_label: 'platform confirmation',
        actor_user_id: 44,
        confirmed_at: '2026-09-08T12:01:00Z',
        acceptance_revision: 1,
      }],
      email_evidence: [{
        message_ref: 'message_1',
        acceptance_revision: 1,
        recipient_party_refs: ['party_customer'],
        recipient_names: ['Customer'],
        state: 'pending',
        display_state: 'sending',
        source: 'platform_send',
        recorded_at: '2026-09-08T12:00:00Z',
        sent_at: null,
        actor_user_id: 1,
        body_sha256: 'abc',
        last_error_class: '',
      }],
    })
    const { host, app } = await mountSection()
    expect(host.querySelector('[data-testid="email-evidence"]')?.textContent).toContain('sending')
    expect(host.querySelector('[data-testid="email-evidence"]')?.textContent).toContain('Customer')
    expect(host.querySelector('[data-testid="confirmation-list"]')?.textContent).toContain('platform confirmation')
    expect(host.querySelector('[data-testid="confirmation-list"]')?.textContent).toContain('revision 1')
    expect(host.textContent).not.toContain('actor 44')
    host.querySelector<HTMLInputElement>('[data-testid="confirm-send"]')!.click()
    await settle()
    expect(host.querySelector<HTMLButtonElement>('[data-testid="authorize-send"]')?.disabled).toBe(true)
    expect(host.querySelector('[data-testid="mail-recovery"]')?.textContent).toContain('Do not send this message again')
    app.unmount()
  })

  it('shows load and save errors without treating failed mail as accepted', async () => {
    mockLoads({
      ...acceptanceFixture,
      mail_recovery: 'Send did not complete before the server accepted the message. After fixing transport, authorize a new send with a new request key.',
      email_evidence: [{
        message_ref: 'message_1',
        acceptance_revision: 1,
        recipient_party_refs: ['party_customer'],
        recipient_names: ['Customer'],
        state: 'failed',
        display_state: 'failed',
        source: 'platform_send',
        recorded_at: '2026-09-08T12:00:00Z',
        sent_at: null,
        actor_user_id: 1,
        body_sha256: 'abc',
        last_error_class: 'smtp_unconfigured',
      }],
    })
    vi.mocked(api.put).mockRejectedValue(new Error('stale'))
    const { host, app } = await mountSection()
    expect(host.querySelector('[data-testid="mail-recovery"]')?.textContent).toContain('authorize a new send')
    host.querySelector<HTMLButtonElement>('[data-testid="save-arrangement"]')!.click()
    await settle()
    expect(host.textContent).toContain('Release acceptance action failed.')
    app.unmount()
  })
})
