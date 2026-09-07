import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick } from 'vue'

import ait7Workflow from '../../../../backend/contracts/fixtures/baseline-batch/workflow-ait7-import.json'
import ProjectBaselineBatchSection from './ProjectBaselineBatchSection.vue'
import { api } from '@/api/client'
import type { Batch, Draft, Readiness, Workflow } from '@/services/projectBaselineBatch'

vi.mock('vue-router', () => ({
  RouterLink: { props: ['to'], template: '<a><slot /></a>' },
}))

// Only the HTTP client is mocked: the component and the real service module,
// including its routes and payloads, are the code under test.
vi.mock('@/api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn() },
  errMsg: (_e: unknown, fallback: string) => fallback,
}))

async function settle() {
  for (let i = 0; i < 8; i += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

const baseline = {
  baseline_ref: 'baseline:v1',
  revision: 1,
  content_digest: 'sha256:16ae0941380964ae9267b0d9bad1df788bbdd71dc83e6e46856a284716c01eea',
  revision_seal: 'sha256:07c6c40c326be33348be4f8d4f6575a11b065980f61728bb5422ebe830b45830',
  imported_claimed_approved_by: 'party:forged',
  imported_claimed_approved_at: '2026-09-07T11:00:00.000Z',
  authenticity: 'untrusted_imported_claim',
  stream_ref: 'stream:export',
}

function draftFixture(overrides: Partial<Draft> = {}): Draft {
  return {
    id: 1,
    project_id: 9,
    revision: 2,
    status: 'reviewing',
    baseline,
    requirements: [{ requirement_ref: 'req.login', statement: 'Users sign in with email' }],
    selected: { requirement_refs: ['req.login'], constraint_refs: [] },
    unresolved: [{ kind: 'pending_proposal', ref: 'prop.sso', summary: 'SSO scope undecided' }],
    execution_mode: 'manual',
    worker: {},
    review_id: 7,
    review_valid: true,
    impact: {
      requirement_count: 1,
      acceptance_criteria_count: 2,
      constraint_count: 0,
      unresolved_count: 1,
      forecast: {
        subject: 'overall', percent: 0, eta_seconds: 1800, kind: 'educated_guess',
        basis: '1 selected requirements', as_of: '2026-09-07T12:00:00Z', label: 'guessed',
        observed: false, fresh: false,
      },
    },
    ...overrides,
  }
}

function batchFixture(overrides: Partial<Batch> = {}): Batch {
  return {
    id: 4,
    batch_key: 'batch-1',
    status: 'active',
    workflow_state: 'active',
    control_state: 'started',
    execution_mode: 'manual',
    issue_id: 88,
    forecasts: [
      {
        subject: 'overall', percent: 0, eta_seconds: 1800, kind: 'educated_guess',
        basis: 'requirement count', as_of: '2026-09-07T12:00:00Z', label: 'guessed', observed: false, fresh: false,
      },
      {
        subject: 'overall', percent: 10, eta_seconds: null, kind: 'measured',
        basis: 'satisfied canonical delivery stage weight', as_of: '2026-09-07T12:05:00Z', label: 'observed',
        observed: false, fresh: false,
        educated_eta_seconds: 1620, educated_eta_basis: 'requirement count; no progress observed yet',
        educated_eta_as_of: '2026-09-07T12:00:00Z', educated_eta_label: 'guessed',
      },
    ],
    progress: {
      stages: [{
        stage_key: 'implementation', applicability: 'required', weight: 45, state: 'pending', phase: '',
        activity: '', needs_input: false, performed: false, policy_satisfied: false, stale: false, never_signaled: true,
      }],
      evidence_fresh: false,
      evidence_observed: false,
    },
    controls: [
      { action: 'pause', available: true, effect: 'harness_control' },
      { action: 'resume', available: false, reason: 'owned_session_resume_unavailable' },
      { action: 'cancel', available: true, effect: 'harness_control' },
    ],
    baseline,
    scope: { requirement_refs: ['req.login'] },
    started_at: '2026-09-07T12:00:00Z',
    ...overrides,
  }
}

function workflowFixture(overrides: Partial<Workflow> = {}): Workflow {
  return {
    project_id: 9,
    inspr_stream_enabled: true,
    inspr_gating: true,
    legacy_unaffected: false,
    draft: draftFixture(),
    active_batch: batchFixture(),
    batches: [batchFixture(), batchFixture({ id: 3, status: 'completed', workflow_state: 'completed' })],
    choices: { execution_modes: ['manual', 'assisted', 'automatic'], runtimes: [], note: '' },
    ...overrides,
  }
}

function mount() {
  const el = document.createElement('div')
  document.body.appendChild(el)
  const Host = defineComponent({
    render() {
      return h(ProjectBaselineBatchSection, { projectId: 9, canWrite: true })
    },
  })
  const app = createApp(Host)
  app.mount(el)
  return { el, app }
}

describe('ProjectBaselineBatchSection', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  it('keeps a project without the opt-in inert and offers exactly one explicit opt-in', async () => {
    vi.mocked(api.get).mockResolvedValue(workflowFixture({
      inspr_stream_enabled: false, inspr_gating: false, legacy_unaffected: true,
      draft: null, active_batch: null, batches: [],
    }))
    vi.mocked(api.post).mockResolvedValue(workflowFixture())
    const { el, app } = mount()
    await settle()
    expect(el.querySelector('[data-testid="baseline-batch"]')).toBeNull()
    const optIn = el.querySelector('[data-testid="baseline-batch-optin"] button') as HTMLButtonElement
    expect(optIn).not.toBeNull()
    optIn.click()
    await settle()
    expect(vi.mocked(api.post).mock.calls[0][0]).toBe('/projects/9/baseline-batches/opt-in')
    expect(vi.mocked(api.post).mock.calls[0][1]).toEqual({ enabled: true })
    expect(el.querySelector('[data-testid="baseline-batch"]')).not.toBeNull()
    app.unmount()
  })

  it('labels every forecast, keeps freshness separate and renders only controls with an effect', async () => {
    vi.mocked(api.get).mockResolvedValue(workflowFixture())
    const { el, app } = mount()
    await settle()
    expect(el.textContent).toContain('untrusted')
    const forecasts = el.querySelector('[data-testid="batch-forecasts"]')!.textContent ?? ''
    expect(forecasts).toContain('guessed')
    expect(forecasts).toContain('observed')
    expect(forecasts).not.toContain('ETA unknown')
    expect(forecasts).toContain('guessed fallback')
    expect(forecasts).toContain('ETA 27 min')
    expect(el.querySelector('[data-testid="batch-freshness"]')!.textContent).toContain('No progress observed yet')
    expect(el.querySelector('[data-testid="draft-impact"]')!.textContent).toContain('1 unresolved')
    expect(el.querySelector('[data-control="pause"]')).not.toBeNull()
    expect(el.querySelector('[data-control="cancel"]')).not.toBeNull()
    // An action with no owned effect is never rendered as a button.
    expect(el.querySelector('[data-control="resume"]')).toBeNull()
    expect(el.textContent).toContain('resume unavailable: owned_session_resume_unavailable')
    expect(el.querySelector('[data-testid="batch-history"]')).not.toBeNull()
    app.unmount()
  })

  it('renders worker and completed educated ETA fallbacks without unknown placeholders', async () => {
    vi.mocked(api.get).mockResolvedValue(workflowFixture({
      active_batch: batchFixture({
        status: 'completed',
        workflow_state: 'completed',
        forecasts: [
          {
            subject: 'overall', percent: 100, eta_seconds: null, kind: 'measured',
            basis: 'satisfied canonical delivery stage weight', as_of: '2026-09-07T13:00:00Z', label: 'observed',
            observed: true, fresh: true,
            educated_eta_seconds: 0, educated_eta_basis: 'all required delivery stages satisfied',
            educated_eta_as_of: '2026-09-07T13:00:00Z', educated_eta_label: 'guessed',
          },
          {
            subject: 'implementation', percent: 55, eta_seconds: null, kind: 'worker_estimate',
            basis: 'worker report', as_of: '2026-09-07T12:30:00Z', label: 'worker estimate',
            observed: true, fresh: false,
            educated_eta_seconds: 810, educated_eta_basis: 'planning; progress evidence stale',
            educated_eta_as_of: '2026-09-07T12:00:00Z', educated_eta_label: 'guessed',
          },
        ],
      }),
    }))
    const { el, app } = mount()
    await settle()
    const text = el.querySelector('[data-testid="batch-forecasts"]')!.textContent ?? ''
    expect(text).not.toContain('ETA unknown')
    expect(text).toContain('ETA 0 min (guessed fallback)')
    expect(text).toContain('ETA 14 min (guessed fallback)')
    app.unmount()
  })

  it('blocks an agent-mode start on the server-reported readiness reason and can request an owned probe', async () => {
    const readiness: Readiness = {
      status: 'needs_setup',
      basis: 'owned_daemon_probe',
      contract_version: 'inspr.readiness.v1',
      blocking_reason: 'readiness_needs_setup',
      next_action: 'activate_home_manager',
      probe_state: 'completed',
      named_account_proof: false,
      model_profile_proof: false,
      workspace_proof: false,
      client_ready_ignored: true,
      checks: [{ id: 'activated_generation', status: 'fail', reason: 'generation_digest_mismatch' }],
    }
    vi.mocked(api.get).mockResolvedValue(workflowFixture({
      draft: draftFixture({ execution_mode: 'automatic' }),
      active_batch: null,
      batches: [],
      readiness,
    }))
    vi.mocked(api.post).mockResolvedValue(readiness)
    vi.mocked(api.patch).mockResolvedValue(draftFixture({ execution_mode: 'automatic' }))
    const { el, app } = mount()
    await settle()
    const confirm = el.querySelector('[data-testid="confirm-start"]') as HTMLInputElement
    confirm.checked = true
    confirm.dispatchEvent(new Event('change'))
    await settle()
    const start = el.querySelector('[data-testid="start-batch"]') as HTMLButtonElement
    expect(start.disabled).toBe(true)
    expect(el.querySelector('[data-testid="readiness-blocking-reason"]')!.textContent).toContain('readiness_needs_setup')
    expect(el.textContent).toContain('activate_home_manager')
    expect(el.textContent).toContain('generation_digest_mismatch')

    ;(el.querySelector('[data-testid="check-readiness"]') as HTMLButtonElement).click()
    await settle()
    const posted = vi.mocked(api.post).mock.calls.map((call) => call[0])
    expect(posted).toContain('/projects/9/baseline-batches/1/readiness')
    expect(vi.mocked(api.patch).mock.calls[0][0]).toBe('/projects/9/baseline-batches/1')
    app.unmount()
  })

  it('clears the confirmation when the reviewed mode changes', async () => {
    vi.mocked(api.get).mockResolvedValue(workflowFixture({ active_batch: null, batches: [] }))
    const { el, app } = mount()
    await settle()
    const confirm = el.querySelector('[data-testid="confirm-start"]') as HTMLInputElement
    confirm.checked = true
    confirm.dispatchEvent(new Event('change'))
    await settle()
    expect((el.querySelector('[data-testid="start-batch"]') as HTMLButtonElement).disabled).toBe(false)
    const assisted = el.querySelectorAll('.bb-mode input')[1] as HTMLInputElement
    assisted.checked = true
    assisted.dispatchEvent(new Event('change'))
    await settle()
    expect(confirm.checked).toBe(false)
    expect(confirm.disabled).toBe(true)
    expect((el.querySelector('[data-testid="start-batch"]') as HTMLButtonElement).disabled).toBe(true)
    confirm.checked = true
    confirm.dispatchEvent(new Event('change'))
    await settle()
    expect(confirm.checked).toBe(false)
    expect((el.querySelector('[data-testid="start-batch"]') as HTMLButtonElement).disabled).toBe(true)
    expect(startCalls()).toHaveLength(0)
    app.unmount()
  })

  it('renders the Go-serialized AIT-7 workflow fixture without crashing on empty unresolved', async () => {
    vi.mocked(api.get).mockResolvedValue(ait7Workflow as Workflow)
    const { el, app } = mount()
    await settle()
    expect(el.querySelector('[data-testid="baseline-batch"]')).not.toBeNull()
    expect(el.textContent).toContain('req.unicode')
    expect(el.textContent).toContain('Straße café')
    expect(el.querySelector('[data-testid="draft-impact"]')!.textContent).toContain('0 unresolved')
    expect(el.textContent).toContain('party:approver')
    expect(el.textContent).toContain('untrusted')
    expect(el.textContent).toContain('Bind review')
    expect(el.textContent).toContain('Manual')
    app.unmount()
  })

  it('tolerates nullable unresolved lists from older API responses', async () => {
    const legacy = workflowFixture({
      active_batch: null,
      batches: [],
      draft: draftFixture({ unresolved: undefined as unknown as [] }),
    })
    vi.mocked(api.get).mockResolvedValue(legacy)
    const { el, app } = mount()
    await settle()
    expect(el.querySelector('[data-testid="baseline-batch"]')).not.toBeNull()
    expect(el.querySelector('[data-testid="draft-impact"]')!.textContent).toContain('1 unresolved')
    app.unmount()
  })

  it('does not invent zero-minute ETAs when both forecast ETAs are absent', async () => {
    vi.mocked(api.get).mockResolvedValue(workflowFixture({
      active_batch: batchFixture({
        forecasts: [{
          subject: 'overall', percent: 0, eta_seconds: null, kind: 'educated_guess',
          basis: 'planning only', as_of: '2026-09-07T12:00:00Z', label: 'guessed', observed: false, fresh: false,
        }],
      }),
    }))
    const { el, app } = mount()
    await settle()
    const text = el.querySelector('[data-testid="batch-forecasts"]')!.textContent ?? ''
    expect(text).toMatch(/ETA \d+ min \(estimated\)/)
    expect(text).toContain('ETA 30 min (estimated)')
    expect(text).toContain('no progress observed yet')
    expect(text).not.toContain('ETA planning only')
    expect(text).not.toContain('ETA unknown')
    expect(text).not.toContain('ETA 0 min')
    app.unmount()
  })

  it('keeps a hydrated bound review startable and is not dirtied by runtime defaults', async () => {
    vi.mocked(api.get).mockResolvedValue(ait7Workflow as Workflow)
    const { el, app } = mount()
    await settle()
    expect(el.querySelector('[data-testid="baseline-state"]')!.textContent).toBe('review')
    const confirm = el.querySelector('[data-testid="confirm-start"]') as HTMLInputElement
    expect(confirm.disabled).toBe(false)
    confirm.checked = true
    confirm.dispatchEvent(new Event('change'))
    await settle()
    expect(confirm.checked).toBe(true)
    expect((el.querySelector('[data-testid="start-batch"]') as HTMLButtonElement).disabled).toBe(false)
    app.unmount()
  })

  it('refuses start after bind, local scope edit, and confirmation recheck until a new review binds the current scope', async () => {
    const current = openAit7Draft()
    vi.mocked(api.get).mockImplementation(async () => structuredClone(current) as Workflow)
    vi.mocked(api.post).mockImplementation(async (url: string, body?: unknown) => {
      if (String(url).endsWith('/review')) {
        const req = body as { execution_mode?: string; selected_requirement_refs?: string[] }
        current.draft = bindDraft(current, req, current.draft?.review_valid ? 22 : 21)
        return current.draft
      }
      if (String(url).includes('/start')) {
        return batchFixture()
      }
      throw new Error(`unexpected post ${url}`)
    })
    const { el, app } = mount()
    await settle()

    ;(el.querySelector('[data-testid="bind-review"]') as HTMLButtonElement).click()
    await settle()
    expect(el.querySelector('[data-testid="baseline-state"]')!.textContent).toBe('review')

    toggleReq(el, 'req.unicode')
    toggleReq(el, 'req.login')
    await settle()
    expect(el.querySelector('[data-testid="baseline-state"]')!.textContent).toBe('needs-review')
    const confirm = el.querySelector('[data-testid="confirm-start"]') as HTMLInputElement
    expect(confirm.disabled).toBe(true)
    confirm.checked = true
    confirm.dispatchEvent(new Event('change'))
    await settle()
    expect(confirm.checked).toBe(false)
    const start = el.querySelector('[data-testid="start-batch"]') as HTMLButtonElement
    expect(start.disabled).toBe(true)
    start.click()
    await settle()
    expect(startCalls()).toHaveLength(0)

    ;(el.querySelector('[data-testid="bind-review"]') as HTMLButtonElement).click()
    await settle()
    expect(el.querySelector('[data-testid="baseline-state"]')!.textContent).toBe('review')
    expect(confirm.disabled).toBe(false)
    confirm.checked = true
    confirm.dispatchEvent(new Event('change'))
    await settle()
    expect(confirm.checked).toBe(true)
    expect(start.disabled).toBe(false)
    start.click()
    await settle()
    expect(startCalls()).toHaveLength(1)
    expect(startCalls()[0][1]).toMatchObject({
      selected_requirement_refs: ['req.login'],
      execution_mode: 'manual',
      confirm: true,
      review_id: 22,
    })
    app.unmount()
  })

  it('requires re-review after mode, account or profile change and does not call start', async () => {
    vi.mocked(api.get).mockResolvedValue(assistedWorkflow())
    const { el, app } = mount()
    await settle()
    expect(el.querySelector('[data-testid="baseline-state"]')!.textContent).toBe('review')
    const confirm = el.querySelector('[data-testid="confirm-start"]') as HTMLInputElement
    confirm.checked = true
    confirm.dispatchEvent(new Event('change'))
    await settle()
    expect((el.querySelector('[data-testid="start-batch"]') as HTMLButtonElement).disabled).toBe(false)

    const automatic = [...el.querySelectorAll('.bb-mode input')].find((n) => (n as HTMLInputElement).value === 'automatic') as HTMLInputElement
    automatic.checked = true
    automatic.dispatchEvent(new Event('change'))
    await settle()
    expect(el.querySelector('[data-testid="baseline-state"]')!.textContent).toBe('needs-review')
    confirm.checked = true
    confirm.dispatchEvent(new Event('change'))
    await settle()
    expect(confirm.checked).toBe(false)
    ;(el.querySelector('[data-testid="start-batch"]') as HTMLButtonElement).click()
    await settle()
    expect(startCalls()).toHaveLength(0)
    app.unmount()

    document.body.innerHTML = ''
    vi.mocked(api.get).mockResolvedValue(assistedWorkflow())
    const second = mount()
    await settle()
    const account = second.el.querySelector('[data-testid="baseline-account"]') as HTMLSelectElement
    account.value = 'acct-b'
    account.dispatchEvent(new Event('change'))
    await settle()
    expect(second.el.querySelector('[data-testid="baseline-state"]')!.textContent).toBe('needs-review')
    const confirm2 = second.el.querySelector('[data-testid="confirm-start"]') as HTMLInputElement
    confirm2.checked = true
    confirm2.dispatchEvent(new Event('change'))
    await settle()
    expect((second.el.querySelector('[data-testid="start-batch"]') as HTMLButtonElement).disabled).toBe(true)
    ;(second.el.querySelector('[data-testid="start-batch"]') as HTMLButtonElement).click()
    await settle()
    expect(startCalls()).toHaveLength(0)

    const profile = second.el.querySelector('[data-testid="baseline-profile"]') as HTMLSelectElement
    profile.value = 'prof-b@2'
    profile.dispatchEvent(new Event('change'))
    await settle()
    expect(second.el.querySelector('[data-testid="baseline-state"]')!.textContent).toBe('needs-review')
    second.app.unmount()
  })

  it('starts the reviewed two-requirement scope after uncheck then recheck of one ref', async () => {
    const current = twoReqDraft()
    vi.mocked(api.get).mockImplementation(async () => structuredClone(current) as Workflow)
    vi.mocked(api.post).mockImplementation(async (url: string, body?: unknown) => {
      if (String(url).endsWith('/review')) {
        const req = body as { execution_mode?: string; selected_requirement_refs?: string[] }
        current.draft = bindDraft(current, req, 31)
        return current.draft
      }
      if (String(url).includes('/start')) {
        return batchFixture({ scope: { requirement_refs: ['req.a', 'req.b'] } })
      }
      throw new Error(`unexpected post ${url}`)
    })
    const { el, app } = mount()
    await settle()

    ;(el.querySelector('[data-testid="bind-review"]') as HTMLButtonElement).click()
    await settle()
    expect(el.querySelector('[data-testid="baseline-state"]')!.textContent).toBe('review')
    expect(reviewCalls()[0][1]).toMatchObject({ selected_requirement_refs: ['req.a', 'req.b'] })

    toggleReq(el, 'req.a')
    await settle()
    expect(el.querySelector('[data-testid="baseline-state"]')!.textContent).toBe('needs-review')
    const confirm = el.querySelector('[data-testid="confirm-start"]') as HTMLInputElement
    expect(confirm.disabled).toBe(true)

    toggleReq(el, 'req.a')
    await settle()
    expect(el.querySelector('[data-testid="baseline-state"]')!.textContent).toBe('review')
    expect(confirm.disabled).toBe(false)
    confirm.checked = true
    confirm.dispatchEvent(new Event('change'))
    await settle()
    expect(confirm.checked).toBe(true)
    const start = el.querySelector('[data-testid="start-batch"]') as HTMLButtonElement
    expect(start.disabled).toBe(false)
    start.click()
    await settle()
    expect(startCalls()).toHaveLength(1)
    expect(startCalls()[0][1]).toMatchObject({
      selected_requirement_refs: ['req.a', 'req.b'],
      execution_mode: 'manual',
      confirm: true,
      review_id: 31,
    })
    app.unmount()
  })

  it('does not treat CSV-colliding requirement memberships as the reviewed set', async () => {
    const current = fourCommaReqDraft()
    vi.mocked(api.get).mockImplementation(async () => structuredClone(current) as Workflow)
    vi.mocked(api.post).mockImplementation(async (url: string, body?: unknown) => {
      if (String(url).endsWith('/review')) {
        const req = body as { execution_mode?: string; selected_requirement_refs?: string[] }
        current.draft = bindDraft(current, req, 41)
        return current.draft
      }
      if (String(url).includes('/start')) {
        return batchFixture({ scope: { requirement_refs: ['req.a,req.b', 'req.c'] } })
      }
      throw new Error(`unexpected post ${url}`)
    })
    const { el, app } = mount()
    await settle()

    ;(el.querySelector('[data-testid="bind-review"]') as HTMLButtonElement).click()
    await settle()
    expect(el.querySelector('[data-testid="baseline-state"]')!.textContent).toBe('review')
    expect(reviewCalls()[0][1]).toMatchObject({ selected_requirement_refs: ['req.a,req.b', 'req.c'] })
    expect(Array.isArray((reviewCalls()[0][1] as { selected_requirement_refs: string[] }).selected_requirement_refs)).toBe(true)

    toggleReq(el, 'req.a,req.b')
    toggleReq(el, 'req.c')
    toggleReq(el, 'req.a')
    toggleReq(el, 'req.b,req.c')
    await settle()
    expect(el.querySelector('[data-testid="baseline-state"]')!.textContent).toBe('needs-review')
    const confirm = el.querySelector('[data-testid="confirm-start"]') as HTMLInputElement
    expect(confirm.disabled).toBe(true)
    confirm.checked = true
    confirm.dispatchEvent(new Event('change'))
    await settle()
    expect(confirm.checked).toBe(false)
    const start = el.querySelector('[data-testid="start-batch"]') as HTMLButtonElement
    expect(start.disabled).toBe(true)
    start.click()
    await settle()
    expect(startCalls()).toHaveLength(0)
    app.unmount()
  })

  it('does not restore the previous review after a failed re-review of edited scope', async () => {
    vi.mocked(api.get).mockResolvedValue(workflowFixture({ active_batch: null, batches: [] }))
    vi.mocked(api.post).mockRejectedValue(new Error('review refused'))
    const { el, app } = mount()
    await settle()
    toggleReq(el, 'req.login')
    await settle()
    expect(el.querySelector('[data-testid="baseline-state"]')!.textContent).toBe('needs-review')
    ;(el.querySelector('[data-testid="bind-review"]') as HTMLButtonElement).click()
    await settle()
    expect(el.textContent).toContain('Review could not be bound.')
    expect(el.querySelector('[data-testid="baseline-state"]')!.textContent).toBe('needs-review')
    const confirm = el.querySelector('[data-testid="confirm-start"]') as HTMLInputElement
    expect(confirm.disabled).toBe(true)
    confirm.checked = true
    confirm.dispatchEvent(new Event('change'))
    await settle()
    expect(confirm.checked).toBe(false)
    const start = el.querySelector('[data-testid="start-batch"]') as HTMLButtonElement
    expect(start.disabled).toBe(true)
    start.click()
    await settle()
    expect(startCalls()).toHaveLength(0)
    const login = [...el.querySelectorAll('.bb-reqs input')][0] as HTMLInputElement
    expect(login.checked).toBe(false)
    app.unmount()
  })

  it('shows setup-required and never renders an unconditional advance control', async () => {
    vi.mocked(api.get).mockResolvedValue(workflowFixture({
      active_batch: batchFixture({
        progress: {
          stages: [{
            stage_key: 'deployment', applicability: 'required', weight: 15, state: 'pending', phase: '',
            activity: '', needs_input: false, performed: false, policy_satisfied: false, stale: false, never_signaled: true,
          }],
          evidence_fresh: true,
          evidence_observed: true,
          setup_required: 'handoff_secret_mint',
          next_action: 'mint_handoff_secret',
          handoff: { stage_key: 'deployment', handoff_id: '01HTESTHANDOFF000000000000', state: 'offered', credential_epoch: 0, mint_required: true },
        },
      }),
    }))
    const { el, app } = mount()
    await settle()
    expect(el.querySelector('[data-testid="batch-setup-required"]')!.textContent).toContain('handoff_secret_mint')
    expect(el.querySelector('[data-testid="batch-next-action"]')!.textContent).toContain('mint_handoff_secret')
    expect(el.querySelector('[data-testid="batch-handoff"]')!.textContent).toContain('01HTESTHANDOFF000000000000')
    expect(el.textContent).toContain('secret-file')
    expect(el.textContent).not.toContain('Advance')
    expect(el.querySelector('[data-testid="reconcile-handoff"]')).toBeNull()
    expect(vi.mocked(api.post).mock.calls.filter((call) => String(call[0]).includes('/reconcile'))).toHaveLength(0)
    app.unmount()
  })

  it('reconciles automatic authorized handoffs on load and assisted only on the bound control', async () => {
    const automatic = batchFixture({
      execution_mode: 'automatic',
      progress: {
        stages: [],
        evidence_fresh: true,
        evidence_observed: true,
        next_action: 'authorize_pharos_handoff',
      },
    })
    const after = batchFixture({
      execution_mode: 'automatic',
      progress: {
        stages: [],
        evidence_fresh: true,
        evidence_observed: true,
        setup_required: 'handoff_secret_mint',
        next_action: 'mint_handoff_secret',
        handoff: { stage_key: 'deployment', handoff_id: '01HAUTO0000000000000000000', state: 'offered', credential_epoch: 0, mint_required: true },
      },
    })
    vi.mocked(api.get).mockResolvedValue(workflowFixture({ active_batch: automatic, draft: null }))
    vi.mocked(api.post).mockResolvedValue(after)
    const first = mount()
    await settle()
    expect(vi.mocked(api.post).mock.calls[0][0]).toBe('/projects/9/baseline-batches/batches/4/reconcile')
    expect(first.el.querySelector('[data-testid="batch-setup-required"]')!.textContent).toContain('handoff_secret_mint')
    first.app.unmount()

    vi.mocked(api.post).mockClear()
    vi.mocked(api.get).mockResolvedValue(workflowFixture({
      draft: null,
      active_batch: batchFixture({
        execution_mode: 'assisted',
        progress: {
          stages: [],
          evidence_fresh: true,
          evidence_observed: true,
          next_action: 'authorize_pharos_handoff',
        },
      }),
    }))
    const second = mount()
    await settle()
    expect(vi.mocked(api.post).mock.calls.filter((call) => String(call[0]).includes('/reconcile'))).toHaveLength(0)
    const button = second.el.querySelector('[data-testid="reconcile-handoff"]') as HTMLButtonElement
    expect(button).not.toBeNull()
    expect(button.textContent).toContain('Authorize next Pharos handoff')
    button.click()
    await settle()
    expect(vi.mocked(api.post).mock.calls[0][0]).toBe('/projects/9/baseline-batches/batches/4/reconcile')
    second.app.unmount()
  })
})

function startCalls() {
  return vi.mocked(api.post).mock.calls.filter((call) => String(call[0]).includes('/start'))
}

function reviewCalls() {
  return vi.mocked(api.post).mock.calls.filter((call) => String(call[0]).endsWith('/review'))
}

function twoReqDraft(): Workflow {
  return workflowFixture({
    active_batch: null,
    batches: [],
    draft: draftFixture({
      status: 'open',
      review_id: null,
      review_valid: false,
      requirements: [
        { requirement_ref: 'req.a', statement: 'First requirement' },
        { requirement_ref: 'req.b', statement: 'Second requirement' },
      ],
      selected: { requirement_refs: ['req.a', 'req.b'], constraint_refs: [] },
    }),
  })
}

function fourCommaReqDraft(): Workflow {
  return workflowFixture({
    active_batch: null,
    batches: [],
    draft: draftFixture({
      status: 'open',
      review_id: null,
      review_valid: false,
      requirements: [
        { requirement_ref: 'req.a,req.b', statement: 'Comma pair left' },
        { requirement_ref: 'req.c', statement: 'Single right' },
        { requirement_ref: 'req.a', statement: 'Single left' },
        { requirement_ref: 'req.b,req.c', statement: 'Comma pair right' },
      ],
      selected: { requirement_refs: ['req.a,req.b', 'req.c'], constraint_refs: [] },
    }),
  })
}

function toggleReq(el: HTMLElement, ref: string) {
  const item = [...el.querySelectorAll('.bb-reqs label')].find((n) => {
    const text = n.querySelector('span')?.textContent ?? ''
    return text.startsWith(`${ref} — `)
  })
  const input = item?.querySelector('input') as HTMLInputElement
  input.dispatchEvent(new Event('change'))
}

function openAit7Draft(): Workflow {
  const wf = structuredClone(ait7Workflow) as Workflow
  wf.draft = {
    ...wf.draft!,
    requirements: [
      ...wf.draft!.requirements,
      { requirement_ref: 'req.login', statement: 'Users sign in with email' },
    ],
    review_id: null,
    review_valid: false,
    status: 'open',
  }
  wf.active_batch = null
  wf.batches = []
  return wf
}

function bindDraft(wf: Workflow, body: { execution_mode?: string; selected_requirement_refs?: string[] }, reviewId: number): Draft {
  return {
    ...wf.draft!,
    status: 'reviewing',
    review_id: reviewId,
    review_valid: true,
    execution_mode: body.execution_mode ?? wf.draft!.execution_mode,
    selected: {
      requirement_refs: body.selected_requirement_refs ?? wf.draft!.selected.requirement_refs,
      constraint_refs: wf.draft!.selected.constraint_refs,
    },
    worker: (body.execution_mode ?? wf.draft!.execution_mode) === 'manual' ? {} : wf.draft!.worker,
  }
}

function assistedWorkflow(): Workflow {
  return workflowFixture({
    active_batch: null,
    batches: [],
    readiness: {
      status: 'ready',
      basis: 'owned_daemon_probe',
      contract_version: 'inspr.readiness.v1',
      named_account_proof: true,
      model_profile_proof: true,
      workspace_proof: true,
      client_ready_ignored: true,
      checks: [],
    },
    draft: draftFixture({
      execution_mode: 'assisted',
      worker: {
        worker_name: 'builder',
        runtime_id: 'rt-1',
        runtime_generation: 'gen-1',
        account_label: 'operator',
        account_key: 'acct-a',
        profile_id: 'prof-a',
        profile_version: '1',
        workspace_handle: 'ws-a',
      },
    }),
    choices: {
      execution_modes: ['manual', 'assisted', 'automatic'],
      runtimes: [{
        runtime_id: 'rt-1',
        runtime_generation: 'gen-1',
        account_label: 'operator',
        accounts: [
          { key: 'acct-a', label: 'Account A' },
          { key: 'acct-b', label: 'Account B' },
        ],
        profiles: [
          { id: 'prof-a', version: '1' },
          { id: 'prof-b', version: '2' },
        ],
        workspaces: [
          { handle: 'ws-a', identity: 'workspace-a', label: 'Workspace A' },
          { handle: 'ws-b', identity: 'workspace-b', label: 'Workspace B' },
        ],
      }],
      note: '',
    },
  })
}
