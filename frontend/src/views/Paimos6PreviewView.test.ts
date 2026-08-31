import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, reactive } from 'vue'
import { createPinia, setActivePinia } from 'pinia'

const routerHarness = vi.hoisted(() => ({
  route: { query: {} as Record<string, unknown> },
  replace: vi.fn<(location: { query?: Record<string, unknown> }) => Promise<void>>(),
  push: vi.fn<(location: { query?: Record<string, unknown> }) => Promise<void>>(),
}))

vi.mock('vue-router', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-router')>(),
  useRoute: () => routerHarness.route,
  useRouter: () => ({ replace: routerHarness.replace, push: routerHarness.push }),
}))

import { api, ApiError } from '@/api/client'
import { mountComponent } from '@/components/ai/testMount'
import { useAuthStore, type User } from '@/stores/auth'
import Paimos6PreviewView from './Paimos6PreviewView.vue'

const PROJECT_ID = 42
const PROJECT_B_ID = 99
const MANAGED_ID = '17e5d8f7-0b11-4bee-a8a4-a11406de865a'
const UNMANAGED_ID = '27e5d8f7-0b11-4bee-a8a4-a11406de865a'
const PAIMOS_ID = '37e5d8f7-0b11-4bee-a8a4-a11406de865a'
const PROJECT_B_PAIMOS_ID = '47e5d8f7-0b11-4bee-a8a4-a11406de865a'
const PROJECT_A_TITLE = 'Shape the live six home'
const PROJECT_A_ADDRESS = 'codex:amy'

function projectCatalog() {
  return [{ id: PROJECT_ID, key: 'PAI', name: 'Paimos' }]
}

const unsetOrchestrator = {
  schema_version: 1,
  revision: 0,
  orchestrator: null,
  updated_at: null,
}

function configuredOrchestrator(displayLabel = 'aMY / Primary') {
  return {
    schema_version: 1,
    revision: 3,
    orchestrator: { display_label: displayLabel },
    updated_at: '2026-08-31T12:00:00.000Z',
  }
}

function transitionProjectCatalog() {
  return [
    { id: PROJECT_ID, key: 'PAI-A', name: 'Project A' },
    { id: PROJECT_B_ID, key: 'PAI-B', name: 'Project B' },
  ]
}

function managedRow() {
  return {
    product_session_id: MANAGED_ID,
    title: PROJECT_A_TITLE,
    summary: 'Checking the strict session-home seam.',
    revision: 3,
    updated_at: '2026-08-30T12:02:00Z',
    target: { kind: 'project_agent', project_agent_id: 7, agent_name: 'amy', address: PROJECT_A_ADDRESS },
    status: { phase: 'working', reason: 'active' },
    harness: {
      harness: 'codex',
      management_mode: 'managed',
      advertised_capabilities: { inbox: true, status: true, steer: true, interrupt: true, stop: true },
    },
    controls: { steer: 'direct', interrupt: true, stop: true },
    node: { node_id: 854, node_key: 'PAI-854', label: 'PAI-854 · Paimos 6.0 cut' },
    inbox: { unread_count: 1, latest_unread_at: '2026-08-30T11:58:00Z' },
    attention: { required: false, exception_count: 0, action_request_count: 0, reason: null },
  }
}

function unmanagedRow() {
  return {
    product_session_id: UNMANAGED_ID,
    title: 'Review the same node independently',
    summary: 'An unmanaged second product session on the same node.',
    revision: 2,
    updated_at: '2026-08-30T12:01:00Z',
    target: { kind: 'project_agent', project_agent_id: 8, agent_name: 'jan', address: 'claude:jan' },
    status: { phase: 'yielded', reason: 'active' },
    harness: {
      harness: 'claude',
      management_mode: 'unmanaged',
      advertised_capabilities: { inbox: true, status: true, steer: false, interrupt: false, stop: false },
    },
    controls: { steer: 'paimos_nudge', interrupt: false, stop: false },
    node: { node_id: 854, node_key: 'PAI-854', label: 'PAI-854 · Paimos 6.0 cut' },
    inbox: { unread_count: 0, latest_unread_at: null },
    attention: { required: true, exception_count: 2, action_request_count: 1, reason: 'action_request' },
  }
}

function paimosRow() {
  return {
    product_session_id: PAIMOS_ID,
    title: 'Loose planning session',
    summary: '',
    revision: 1,
    updated_at: '2026-08-30T12:00:00Z',
    target: { kind: 'paimos', project_agent_id: null, agent_name: null, address: 'paimos' },
    status: { phase: 'paimos', reason: 'paimos_target' },
    harness: null,
    controls: { steer: 'paimos_nudge', interrupt: false, stop: false },
    node: null,
    inbox: { unread_count: 0, latest_unread_at: null },
    attention: { required: false, exception_count: 0, action_request_count: 0, reason: null },
  }
}

function projectBPaimosRow() {
  return {
    ...paimosRow(),
    product_session_id: PROJECT_B_PAIMOS_ID,
    title: 'Project B Paimos session',
  }
}

function zoomBand(zoom: string) {
  return zoom.length >= 4 ? 'far' : zoom.length >= 3 ? 'aggregate' : zoom.length >= 2 ? 'overview' : 'detail'
}

function zoomLimit(zoom: string) {
  if (zoom.length >= 3) return 100
  return zoom.length === 1 ? zoom.charCodeAt(0) - 48 : (zoom.charCodeAt(0) - 48) * 10 + zoom.charCodeAt(1) - 48
}

function liveProjection(
  sessions = [managedRow(), unmanagedRow(), paimosRow()],
  projectId = PROJECT_ID,
  zoom = '10',
  selectedSession: ReturnType<typeof managedRow> | ReturnType<typeof unmanagedRow> | ReturnType<typeof paimosRow> | null = null,
  totalSessions = sessions.length,
) {
  const visible = selectedSession && !sessions.some((row) => row.product_session_id === selectedSession.product_session_id)
    ? [...sessions, selectedSession]
    : sessions
  const targetFacts = new Map<number, (typeof visible)[number]>()
  for (const row of visible) {
    if (row.target.kind === 'project_agent') targetFacts.set(row.target.project_agent_id!, row)
  }
  const exceptionalTargets = [...targetFacts.values()].filter((row) => row.attention.required)
  const sampledExceptionalTargets = new Set(sessions
    .filter((row) => row.target.kind === 'project_agent' && row.attention.required)
    .map((row) => row.target.project_agent_id))
  return {
    schema_version: 1,
    project_id: projectId,
    zoom,
    band: zoomBand(zoom),
    sample_limit: zoomLimit(zoom),
    sample_truncated: totalSessions > sessions.length,
    sessions,
    selected_session: selectedSession,
    totals: {
      sessions: totalSessions,
      unread: [...targetFacts.values()].reduce((total, row) => total + row.inbox.unread_count, 0),
      attention_sessions: visible.filter((row) => row.attention.required).length,
      exception_messages: exceptionalTargets.reduce((total, row) => total + row.attention.exception_count, 0),
      action_requests: exceptionalTargets.reduce((total, row) => total + row.attention.action_request_count, 0),
      exception_targets: exceptionalTargets.length,
      sampled_exception_targets: sampledExceptionalTargets.size,
    },
  }
}

async function projectionForPath(home: unknown | Promise<unknown>, path: string) {
  const value = await Promise.resolve(home) as ReturnType<typeof liveProjection>
  const query = new URLSearchParams(path.split('?')[1] ?? '')
  const requestedZoom = query.get('zoom') ?? '10'
  const selectedId = query.get('selected_session_id')
  const selected = selectedId === null
    ? null
    : value.sessions.find((row) => row.product_session_id === selectedId)
      ?? (value.selected_session?.product_session_id === selectedId ? value.selected_session : null)
  if (selectedId !== null && selected === null) throw new ApiError(404, 'not found')
  return liveProjection(value.sessions, value.project_id, requestedZoom, selected, value.totals.sessions)
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (cause: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

async function flush() {
  for (let index = 0; index < 8; index += 1) {
    await Promise.resolve()
    await nextTick()
  }
}

function authorizePrincipal(projectIds = [PROJECT_ID], userId = 7) {
  const pinia = createPinia()
  setActivePinia(pinia)
  const auth = useAuthStore()
  auth.user = {
    id: userId,
    username: 'amy',
    role: 'member',
    status: 'active',
  } as User
  const levels: Record<string, 'viewer'> = {}
  for (const projectId of projectIds) levels[String(projectId)] = 'viewer'
  auth.hydrateAccess({ all_projects: false, levels })
  auth.checked = true
  return auth
}

async function mountWithHome(home: unknown | Promise<unknown>, orchestrator: unknown = unsetOrchestrator) {
  authorizePrincipal()
  vi.spyOn(api, 'get').mockImplementation((path: string) => {
    if (path === '/projects?status=all') return Promise.resolve(projectCatalog()) as never
    if (path === '/orchestrator/v1') return Promise.resolve(orchestrator) as never
    if (path.startsWith(`/projects/${PROJECT_ID}/session-home/zoom/v1?`)) return projectionForPath(home, path) as never
    return Promise.reject(new Error(`unexpected GET ${path}`))
  })
  return mountComponent(Paimos6PreviewView)
}

async function mountTransitionHome(projectBHome: unknown | Promise<unknown>) {
  const auth = authorizePrincipal([PROJECT_ID, PROJECT_B_ID])
  vi.spyOn(api, 'get').mockImplementation((path: string) => {
    if (path === '/projects?status=all') {
      const authorized = transitionProjectCatalog().filter((project) => auth.accessibleProjects.has(project.id))
      return Promise.resolve(authorized) as never
    }
    if (path === '/orchestrator/v1') return Promise.resolve(unsetOrchestrator) as never
    if (path.startsWith(`/projects/${PROJECT_ID}/session-home/zoom/v1?`)) {
      return projectionForPath(liveProjection([managedRow()]), path) as never
    }
    if (path.startsWith(`/projects/${PROJECT_B_ID}/session-home/zoom/v1?`)) return projectionForPath(projectBHome, path) as never
    return Promise.reject(new Error(`unexpected GET ${path}`))
  })
  return { auth, mounted: await mountComponent(Paimos6PreviewView) }
}

function renderedStatus(el: HTMLElement) {
  return el.querySelector('.p6-status')?.textContent ?? ''
}

describe('Paimos6PreviewView semantic-zoom session home (PAI-864)', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    routerHarness.route = reactive({ query: {} })
    routerHarness.replace.mockReset().mockImplementation(async ({ query }) => {
      routerHarness.route.query = { ...query }
    })
    routerHarness.push.mockReset().mockImplementation(async ({ query }) => {
      routerHarness.route.query = { ...query }
    })
    sessionStorage.clear()
  })

  afterEach(() => {
    document.body.innerHTML = ''
    setActivePinia(undefined)
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('shows loading, then renders authorized loose/many-per-node rows with nullable selection', async () => {
    const pending = deferred<ReturnType<typeof liveProjection>>()
    const mounted = await mountWithHome(pending.promise)
    await flush()
    expect(mounted.el.textContent).toContain('Loading authorized semantic-zoom projection')
    expect(mounted.el.querySelector('.p6-session-card')).toBeNull()

    pending.resolve(liveProjection())
    await flush()
    const text = mounted.el.textContent ?? ''
    expect(api.get).toHaveBeenCalledWith('/projects?status=all', expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(api.get).toHaveBeenCalledWith(
      `/projects/${PROJECT_ID}/session-home/zoom/v1?zoom=10`,
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
    expect(mounted.el.querySelectorAll('.p6-session-card')).toHaveLength(3)
    expect(text.match(/PAI-854 · Paimos 6.0 cut/g)).toHaveLength(2)
    expect(text).toContain('Loose session · no node attached')
    expect(text).toContain('Read-only responsive web preview')
    expect(text).toContain('mobile web—not a native client')
    expect(mounted.el.querySelector('.p6-session-card.is-selected')).toBeNull()
    expect(text).toContain('No selection · preview target Paimos')

    const attentionCard = mounted.el.querySelector<HTMLElement>('.p6-session-card.needs-attention')!
    attentionCard.querySelector<HTMLButtonElement>('.p6-card-select')!.click()
    await flush()
    const selectedAttentionCard = mounted.el.querySelector<HTMLElement>('.p6-session-card.needs-attention')!
    expect(selectedAttentionCard.classList.contains('is-selected')).toBe(true)
    expect(selectedAttentionCard.classList.contains('needs-attention')).toBe(true)
    expect(mounted.el.textContent).toContain('Selected agent target · claude:jan')
    expect(routerHarness.replace).toHaveBeenCalledWith(expect.objectContaining({
      query: expect.objectContaining({ project: String(PROJECT_ID), session: UNMANAGED_ID }),
    }))

    selectedAttentionCard.querySelector<HTMLButtonElement>('.p6-card-select')!.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
    )
    await flush()
    expect(mounted.el.querySelector('.p6-session-card.is-selected')).toBeNull()
    expect(mounted.el.textContent).toContain('No selection · preview target Paimos')
    await mounted.unmount()
  })

  it('canonicalizes an invalid zoom once and round-trips far-out lexical input through history', async () => {
    routerHarness.route.query = { project: String(PROJECT_ID), zoom: '1e3' }
    const mounted = await mountWithHome(liveProjection())
    await flush()
    expect(routerHarness.route.query.zoom).toBe('10')
    expect(routerHarness.push).not.toHaveBeenCalled()
    const canonicalReplacements = routerHarness.replace.mock.calls.filter(([location]) => (
      location.query?.zoom === '10' && location.query?.project === String(PROJECT_ID)
    ))
    expect(canonicalReplacements).toHaveLength(1)

    const input = mounted.el.querySelector<HTMLInputElement>('.p6-zoom input')!
    const far = '1234567890123456789012345678901234567890'
    input.value = far
    input.dispatchEvent(new Event('input', { bubbles: true }))
    input.dispatchEvent(new Event('change', { bubbles: true }))
    await flush()
    expect(routerHarness.push).toHaveBeenCalledWith({
      query: expect.objectContaining({ project: String(PROJECT_ID), zoom: far }),
    })
    expect(routerHarness.route.query.zoom).toBe(far)
    expect(api.get).toHaveBeenCalledWith(
      `/projects/${PROJECT_ID}/session-home/zoom/v1?zoom=${far}`,
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
    expect(mounted.el.textContent).toContain('Exception-first projection · far')
    await mounted.unmount()
  })

  it('renders a selected row outside the sample and keeps it visible across zoom reorder', async () => {
    routerHarness.route.query = {
      project: String(PROJECT_ID),
      session: PAIMOS_ID,
      zoom: '1',
    }
    const home = liveProjection([managedRow()], PROJECT_ID, '1', paimosRow(), 2)
    const mounted = await mountWithHome(home)
    await flush()
    expect(mounted.el.querySelector('.p6-pinned')?.textContent).toContain('Loose planning session')
    expect(mounted.el.querySelectorAll('.p6-session-card')).toHaveLength(2)
    expect(mounted.el.textContent).toContain('Selected agent target · Paimos')

    routerHarness.route.query = { ...routerHarness.route.query, zoom: '25' }
    await flush()
    expect(mounted.el.querySelector('.p6-pinned')?.textContent).toContain('Loose planning session')
    expect(mounted.el.textContent).toContain('Selected agent target · Paimos')
    expect(routerHarness.route.query.session).toBe(PAIMOS_ID)
    await mounted.unmount()
  })

  it('keeps the source rail static, disabled, and non-networked', async () => {
    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)
    const mounted = await mountWithHome(liveProjection())
    await flush()
    const rail = mounted.el.querySelector<HTMLElement>('.p6-source-rail')!
    expect(rail.textContent).toContain('Paimos · active source')
    expect(rail.querySelectorAll('[aria-disabled="true"]')).toHaveLength(2)
    expect(rail.querySelector('button, a')).toBeNull()
    expect(rail.textContent).not.toContain('Coming Soon')
    expect(fetchSpy).not.toHaveBeenCalled()
    await mounted.unmount()
  })

  it('fences A metadata while an atomic history transition waits for B and selects only B', async () => {
    const pendingB = deferred<ReturnType<typeof liveProjection>>()
    routerHarness.route.query = { project: String(PROJECT_ID) }
    const { mounted } = await mountTransitionHome(pendingB.promise)
    await flush()

    mounted.el.querySelector<HTMLButtonElement>('.p6-card-select')!.click()
    await flush()
    expect(renderedStatus(mounted.el)).toContain(PROJECT_A_TITLE)
    expect(renderedStatus(mounted.el)).toContain(PROJECT_A_ADDRESS)

    routerHarness.route.query = {
      project: String(PROJECT_B_ID),
      session: PROJECT_B_PAIMOS_ID,
      zoom: '1000',
    }
    await nextTick()

    expect(mounted.el.textContent).not.toContain(PROJECT_A_TITLE)
    expect(mounted.el.textContent).not.toContain(PROJECT_A_ADDRESS)
    expect(renderedStatus(mounted.el)).toContain('Choose a session to target it')
    expect(mounted.el.textContent).toContain('Loading authorized semantic-zoom projection')
    expect(routerHarness.route.query.session).toBe(PROJECT_B_PAIMOS_ID)
    expect(routerHarness.route.query.zoom).toBe('1000')

    pendingB.resolve(liveProjection([projectBPaimosRow()], PROJECT_B_ID))
    await flush()
    const selected = mounted.el.querySelector<HTMLElement>('.p6-session-card.is-selected')
    expect(selected?.textContent).toContain('Project B Paimos session')
    expect(mounted.el.textContent).toContain('Selected agent target · Paimos')
    expect(mounted.el.textContent).not.toContain(PROJECT_A_TITLE)
    expect(mounted.el.textContent).not.toContain(PROJECT_A_ADDRESS)
    expect(routerHarness.route.query.session).toBe(PROJECT_B_PAIMOS_ID)
    await mounted.unmount()
  })

  it('fences A metadata on picker navigation before rendering B with no inherited selection', async () => {
    routerHarness.route.query = { project: String(PROJECT_ID) }
    const { mounted } = await mountTransitionHome(liveProjection([projectBPaimosRow()], PROJECT_B_ID))
    await flush()

    mounted.el.querySelector<HTMLButtonElement>('.p6-card-select')!.click()
    await flush()
    expect(renderedStatus(mounted.el)).toContain(PROJECT_A_ADDRESS)
    mounted.el.querySelector<HTMLButtonElement>('.p6-card-actions button')!.click()
    await flush()
    expect(renderedStatus(mounted.el)).toContain(PROJECT_A_TITLE)
    expect(renderedStatus(mounted.el)).toContain('No request was sent')

    const picker = mounted.el.querySelector<HTMLSelectElement>('.p6-project-picker select')!
    picker.value = String(PROJECT_B_ID)
    picker.dispatchEvent(new Event('change', { bubbles: true }))
    await flush()

    expect(mounted.el.textContent).toContain('Project B Paimos session')
    expect(mounted.el.textContent).toContain('No selection · preview target Paimos')
    expect(mounted.el.textContent).not.toContain(PROJECT_A_TITLE)
    expect(mounted.el.textContent).not.toContain(PROJECT_A_ADDRESS)
    expect(renderedStatus(mounted.el)).toContain('Choose a session to target it')
    expect(routerHarness.route.query).toEqual({ project: String(PROJECT_B_ID), session: undefined })
    await mounted.unmount()
  })

  it('fences copied status on same-view permission and principal authority resets', async () => {
    routerHarness.route.query = { project: String(PROJECT_ID) }
    const { auth, mounted } = await mountTransitionHome(liveProjection([projectBPaimosRow()], PROJECT_B_ID))
    await flush()

    mounted.el.querySelector<HTMLButtonElement>('.p6-card-select')!.click()
    await flush()
    expect(renderedStatus(mounted.el)).toContain(PROJECT_A_TITLE)
    expect(renderedStatus(mounted.el)).toContain(PROJECT_A_ADDRESS)
    mounted.el.querySelector<HTMLButtonElement>('[aria-label="Open the talk-first door"]')!.click()
    await nextTick()
    expect(mounted.el.querySelector('.p6-talk-door')?.textContent).toContain(PROJECT_A_ADDRESS)

    auth.hydrateAccess({ all_projects: false, levels: { [PROJECT_B_ID]: 'viewer' } })
    await flush()
    expect(mounted.el.querySelector('.p6-talk-door')).toBeNull()
    expect(mounted.el.querySelector('main')?.hasAttribute('inert')).toBe(false)
    expect(mounted.el.textContent).toContain('Project B Paimos session')
    expect(mounted.el.textContent).not.toContain(PROJECT_A_TITLE)
    expect(mounted.el.textContent).not.toContain(PROJECT_A_ADDRESS)
    expect(renderedStatus(mounted.el)).toContain('Choose a session to target it')

    mounted.el.querySelector<HTMLButtonElement>('.p6-card-select')!.click()
    await flush()
    expect(renderedStatus(mounted.el)).toContain('Project B Paimos session')
    auth.user = { ...auth.user!, id: 8, username: 'next-principal' }
    await flush()
    expect(renderedStatus(mounted.el)).not.toContain('Project B Paimos session')
    expect(renderedStatus(mounted.el)).toContain('Choose a session to target it')
    expect(mounted.el.textContent).not.toContain(PROJECT_A_TITLE)
    expect(mounted.el.textContent).not.toContain(PROJECT_A_ADDRESS)
    await mounted.unmount()
  })

  it('renders direct controls only when advertised and keeps unmanaged controls honest', async () => {
    const mounted = await mountWithHome(liveProjection())
    await flush()
    const cards = [...mounted.el.querySelectorAll<HTMLElement>('.p6-session-card')]
    const managed = cards.find((card) => card.textContent?.includes('Managed session'))!
    const unmanaged = cards.find((card) => card.textContent?.includes('Unmanaged CLI'))!

    expect(managed.querySelector('.p6-card-actions')?.textContent).toContain('Steer · preview')
    expect(managed.querySelector('.p6-card-actions')?.textContent).toContain('Interrupt · preview')
    expect(managed.querySelector('.p6-card-actions')?.textContent).toContain('Stop · preview')
    const unmanagedActions = unmanaged.querySelector('.p6-card-actions')?.textContent ?? ''
    expect(unmanagedActions).toContain('Ask Paimos to steer · preview')
    expect(unmanagedActions).not.toContain('Interrupt')
    expect(unmanagedActions).not.toContain('Stop')
    expect(unmanaged.textContent).toContain('Paimos does not own this process')

    unmanaged.querySelector<HTMLButtonElement>('.p6-paimos-nudge')!.click()
    await flush()
    expect(mounted.el.querySelector('[role="status"]')?.textContent).toContain('has no mutation endpoint yet')
    expect(mounted.el.querySelector('[role="status"]')?.textContent).toContain('No request was sent')
    // Projects and session home load once; the instance orchestrator reloads
    // when the selected project's authority boundary becomes concrete.
    expect(api.get).toHaveBeenCalledTimes(4)
    await mounted.unmount()
  })

  it('keeps unsupported microphone capture honest while rows stay on the strict endpoint', async () => {
    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)
    const mounted = await mountWithHome(liveProjection(), configuredOrchestrator())
    await flush()

    mounted.el.querySelector<HTMLButtonElement>('[aria-label="Open the talk-first door"]')!.click()
    await nextTick()
    const door = mounted.el.querySelector<HTMLElement>('.p6-talk-door')!
    expect(door).not.toBeNull()
    expect(mounted.el.querySelector('main')?.hasAttribute('inert')).toBe(true)
    const mic = door.querySelector<HTMLButtonElement>('.p6-mic')!
    await vi.waitFor(() => expect(document.activeElement).toBe(mic))
    expect(door.textContent!.indexOf('aMY / Primary')).toBeLessThan(door.textContent!.indexOf('Human node form'))
    expect(door.textContent).toContain('What should aMY / Primary do?')
    expect(door.textContent).toContain('orchestrator configured')
    expect(door.textContent).toContain('Tap to toggle · hold to talk, release to stop')
    expect(door.textContent).toContain('Raw audio stays ephemeral')
    expect(door.textContent).toContain('Preview target · aMY / Primary (no session selected)')

    mic.click()
    await nextTick()
    expect(mic.getAttribute('aria-pressed')).toBe('false')
    expect(door.querySelector('.p6-door-status')?.textContent).toContain('Microphone capture is unavailable')

    const details = door.querySelector<HTMLDetailsElement>('.p6-node-door')!
    const summary = details.querySelector<HTMLElement>('summary')!
    const close = door.querySelector<HTMLButtonElement>('.p6-close')!
    summary.focus()
    summary.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(close)
    close.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(summary)

    details.open = true
    details.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await nextTick()
    expect(door.querySelector('.p6-door-status')?.textContent).toContain('Name the node')

    const input = details.querySelector<HTMLInputElement>('#p6-node-title')!
    input.value = 'Local planning node'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    details.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await nextTick()
    expect(door.querySelector('.p6-door-status')?.textContent).toContain('No mutation endpoint was called')
    expect(fetchSpy).not.toHaveBeenCalled()

    door.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await nextTick()
    const reopenedTrigger = mounted.el.querySelector<HTMLButtonElement>('[aria-label="Open the talk-first door"]')!
    await vi.waitFor(() => expect(document.activeElement).toBe(reopenedTrigger))
    expect(mounted.el.querySelector('main')?.hasAttribute('inert')).toBe(false)
    await mounted.unmount()
  })

  it('renders explicit empty and unavailable states without inventing or retaining rows', async () => {
    const empty = await mountWithHome(liveProjection([]))
    await flush()
    expect(empty.el.textContent).toContain('PAI has no product sessions yet')
    expect(empty.el.querySelector('.p6-session-card')).toBeNull()
    await empty.unmount()

    vi.restoreAllMocks()
    authorizePrincipal()
    vi.spyOn(api, 'get').mockImplementation((path: string) => {
      if (path === '/projects?status=all') return Promise.resolve(projectCatalog()) as never
      if (path === '/orchestrator/v1') return Promise.resolve(unsetOrchestrator) as never
      if (path.startsWith(`/projects/${PROJECT_ID}/session-home/zoom/v1?`)) return Promise.reject(new Error('offline'))
      return Promise.reject(new Error(`unexpected GET ${path}`))
    })
    const unavailable = await mountComponent(Paimos6PreviewView)
    await flush()
    expect(unavailable.el.textContent).toContain('Session home unavailable')
    expect(unavailable.el.textContent).toContain('Previously authorized rows have been cleared')
    expect(unavailable.el.querySelector('.p6-session-card')).toBeNull()
    await unavailable.unmount()
  })
})
