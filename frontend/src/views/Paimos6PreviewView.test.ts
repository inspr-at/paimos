import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, reactive } from 'vue'
import { createPinia, setActivePinia } from 'pinia'

const routerHarness = vi.hoisted(() => ({
  route: { query: {} as Record<string, unknown> },
  replace: vi.fn<(location: { query?: Record<string, unknown> }) => Promise<void>>(),
}))

vi.mock('vue-router', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-router')>(),
  useRoute: () => routerHarness.route,
  useRouter: () => ({ replace: routerHarness.replace }),
}))

import { api } from '@/api/client'
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

function liveProjection(sessions = [managedRow(), unmanagedRow(), paimosRow()], projectId = PROJECT_ID) {
  return {
    schema_version: 1,
    project_id: projectId,
    sessions,
    totals: {
      sessions: sessions.length,
      unread: sessions.reduce((total, row) => total + row.inbox.unread_count, 0),
      attention: sessions.filter((row) => row.attention.required).length,
    },
  }
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

async function mountWithHome(home: unknown | Promise<unknown>) {
  authorizePrincipal()
  vi.spyOn(api, 'get').mockImplementation((path: string) => {
    if (path === '/projects?status=all') return Promise.resolve(projectCatalog()) as never
    if (path === `/projects/${PROJECT_ID}/session-home/v1`) return Promise.resolve(home) as never
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
    if (path === `/projects/${PROJECT_ID}/session-home/v1`) {
      return Promise.resolve(liveProjection([managedRow()])) as never
    }
    if (path === `/projects/${PROJECT_B_ID}/session-home/v1`) return Promise.resolve(projectBHome) as never
    return Promise.reject(new Error(`unexpected GET ${path}`))
  })
  return { auth, mounted: await mountComponent(Paimos6PreviewView) }
}

function renderedStatus(el: HTMLElement) {
  return el.querySelector('.p6-status')?.textContent ?? ''
}

describe('Paimos6PreviewView live session home (PAI-861)', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    routerHarness.route = reactive({ query: {} })
    routerHarness.replace.mockReset().mockImplementation(async ({ query }) => {
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
    expect(mounted.el.textContent).toContain('Loading authorized session home')
    expect(mounted.el.querySelector('.p6-session-card')).toBeNull()

    pending.resolve(liveProjection())
    await flush()
    const text = mounted.el.textContent ?? ''
    expect(api.get).toHaveBeenCalledWith('/projects?status=all', expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(api.get).toHaveBeenCalledWith(
      `/projects/${PROJECT_ID}/session-home/v1`,
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
    expect(attentionCard.classList.contains('is-selected')).toBe(true)
    expect(attentionCard.classList.contains('needs-attention')).toBe(true)
    expect(mounted.el.textContent).toContain('Selected agent target · claude:jan')
    expect(routerHarness.replace).toHaveBeenCalledWith(expect.objectContaining({
      query: expect.objectContaining({ project: String(PROJECT_ID), session: UNMANAGED_ID }),
    }))

    attentionCard.querySelector<HTMLButtonElement>('.p6-card-select')!.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
    )
    await flush()
    expect(mounted.el.querySelector('.p6-session-card.is-selected')).toBeNull()
    expect(mounted.el.textContent).toContain('No selection · preview target Paimos')
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
    }
    await nextTick()

    expect(mounted.el.textContent).not.toContain(PROJECT_A_TITLE)
    expect(mounted.el.textContent).not.toContain(PROJECT_A_ADDRESS)
    expect(renderedStatus(mounted.el)).toContain('Choose a session to target it')
    expect(mounted.el.textContent).toContain('Loading authorized session home')
    expect(routerHarness.route.query.session).toBe(PROJECT_B_PAIMOS_ID)

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
    expect(api.get).toHaveBeenCalledTimes(2)
    await mounted.unmount()
  })

  it('keeps the talk-first door local-only while live rows come from the strict endpoint', async () => {
    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)
    const mounted = await mountWithHome(liveProjection())
    await flush()

    mounted.el.querySelector<HTMLButtonElement>('[aria-label="Open the talk-first door"]')!.click()
    await nextTick()
    const door = mounted.el.querySelector<HTMLElement>('.p6-talk-door')!
    expect(door).not.toBeNull()
    expect(mounted.el.querySelector('main')?.hasAttribute('inert')).toBe(true)
    const mic = door.querySelector<HTMLButtonElement>('.p6-mic')!
    await vi.waitFor(() => expect(document.activeElement).toBe(mic))
    expect(door.textContent!.indexOf('Amy')).toBeLessThan(door.textContent!.indexOf('Human node form'))
    expect(door.textContent).toContain('Tap to toggle · hold to talk, release to stop')
    expect(door.textContent).toContain('does not request microphone access')
    expect(door.textContent).toContain('Preview target · Paimos (no session selected)')

    mic.click()
    await nextTick()
    expect(mic.getAttribute('aria-pressed')).toBe('true')
    expect(mounted.el.querySelector('[role="status"]')?.textContent).toContain('No microphone opened')

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
      if (path === `/projects/${PROJECT_ID}/session-home/v1`) return Promise.reject(new Error('offline'))
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
