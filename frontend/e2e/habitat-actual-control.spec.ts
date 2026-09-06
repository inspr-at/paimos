import {
  expect,
  test,
  type APIRequestContext,
  type APIResponse,
  type Page,
  type Response,
} from '@playwright/test'
import { createHash } from 'node:crypto'
import { lstat, readFile, stat, writeFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'

// This is an opt-in, destructive proof against one explicitly prepared local
// composition. It has no fixtures and is never eligible for the ordinary E2E
// suite. The operator owns the runtime, query budget, cleanup, and invocation.
const ENABLED = process.env.PAI926_ACTUAL_CONTROL === '1'
const HANDOFF_PATH = process.env.PAI926_ACTUAL_HANDOFF
test.skip(!ENABLED, 'Set PAI926_ACTUAL_CONTROL=1 with a reviewed PAI926_ACTUAL_HANDOFF')
test.setTimeout(8 * 60_000)

type JSONRecord = Record<string, unknown>
type Handoff = {
  schema_version: 1
  evidence_kind: 'pai-926-actual-browser-control'
  url: string
  project: { id: number; key: string }
  ticket: { id: number; key: string }
  agents: {
    coordinator: { name: string }
    worker: { name: string; address: string }
  }
  runtime: { id: string; generation: string }
  profile: { id: string; version: string }
  workspaces: {
    coordinator: { handle: string }
    worker: { handle: string }
  }
  private_human_password_file: string
  private_message_file: string
  private_steer_file: string
  evidence_directory: string
  served_frontend: { source: string; index_sha256: string }
  browser_proof: {
    authorization_id: string
    expires_at: string
    root_display_label: string
    fresh_agent_addresses: true
    max_queries: 3
    max_input_turns: number
    cleanup_owner: string
  }
}

type Receipt = {
  evidence_kind: string
  status: 'RUNNING' | 'PASS' | 'FAIL'
  fixture: false
  authorization_id: string
  base_url: string
  source: Handoff['served_frontend']
  started_at: string
  finished_at?: string
  limits: { max_queries: number; max_input_turns: number }
  owned_session_ids: string[]
  steps: JSONRecord[]
  raw_responses: {
    label: string
    status: number
    path: string
    file: string
    sha256: string
  }[]
  outside_requests_blocked: string[]
  unexpected_mutations_blocked: string[]
  cleanup: JSONRecord
  failure?: { name: string }
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-57][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const sha256 = (bytes: Buffer) => createHash('sha256').update(bytes).digest('hex')
const record = (value: unknown): JSONRecord => value as JSONRecord

function requireHandoff(value: unknown): Handoff {
  const h = value as Handoff
  const local = /^http:\/\/(?:127\.0\.0\.1|localhost):[1-9]\d{1,4}$/.test(h?.url ?? '')
  const expires = Date.parse(h?.browser_proof?.expires_at ?? '')
  expect(h?.schema_version).toBe(1)
  expect(h?.evidence_kind).toBe('pai-926-actual-browser-control')
  expect(local).toBe(true)
  expect(h?.project?.id).toBeGreaterThan(0)
  expect(h?.ticket?.id).toBeGreaterThan(0)
  expect(UUID.test(h?.runtime?.id ?? '')).toBe(true)
  expect(UUID.test(h?.runtime?.generation ?? '')).toBe(true)
  expect(UUID.test(h?.workspaces?.coordinator?.handle ?? '')).toBe(true)
  expect(UUID.test(h?.workspaces?.worker?.handle ?? '')).toBe(true)
  expect(h?.agents?.worker?.address).toBe(`claude:${h?.agents?.worker?.name}`)
  expect(h?.agents?.worker?.name).toMatch(/^browser-proof-[a-z0-9-]+$/)
  expect(h?.agents?.coordinator?.name).toMatch(/^browser-proof-[a-z0-9-]+$/)
  expect(h?.browser_proof?.fresh_agent_addresses).toBe(true)
  expect(h?.browser_proof?.max_queries).toBe(3)
  expect(h?.browser_proof?.max_input_turns).toBeGreaterThanOrEqual(5)
  expect(h?.browser_proof?.max_input_turns).toBeLessThanOrEqual(6)
  expect(h?.browser_proof?.cleanup_owner).toMatch(/^\/root(?:\/[a-z0-9_-]+)?$/)
  expect(expires).toBeGreaterThan(Date.now() + 60_000)
  expect(h?.browser_proof?.authorization_id).toMatch(/^[A-Za-z0-9._:-]{1,128}$/)
  expect(h?.browser_proof?.root_display_label).toMatch(/^Browser proof [A-Za-z0-9._:-]{1,48}$/)
  expect(h?.served_frontend?.index_sha256).toMatch(/^[a-f0-9]{64}$/)
  return h
}

async function requirePrivateFile(path: string) {
  const info = await lstat(path)
  expect(info.isFile()).toBe(true)
  expect(info.isSymbolicLink()).toBe(false)
  expect(info.uid).toBe(process.getuid!())
  expect(info.mode & 0o077).toBe(0)
  return readFile(path, 'utf8')
}

async function requirePrivateDirectory(path: string) {
  const info = await lstat(path)
  expect(info.isDirectory()).toBe(true)
  expect(info.isSymbolicLink()).toBe(false)
  expect(info.uid).toBe(process.getuid!())
  expect(info.mode & 0o077).toBe(0)
}

async function readJSON(response: Response): Promise<JSONRecord> {
  return record(await response.json())
}

test('actual browser controls one fresh owned child and closes every generation', async ({
  browser,
}) => {
  expect(HANDOFF_PATH).toBeTruthy()
  const handoffPath = resolve(HANDOFF_PATH!)
  const handoff = requireHandoff(JSON.parse(await readFile(handoffPath, 'utf8')))
  const evidenceDirectory = resolve(handoff.evidence_directory)
  await requirePrivateDirectory(evidenceDirectory)
  const resultPath = resolve(evidenceDirectory, 'pai-926-actual-browser-control.json')
  expect(dirname(resultPath)).toBe(evidenceDirectory)
  await expect(stat(resultPath)).rejects.toThrow()
  const password = (await requirePrivateFile(handoff.private_human_password_file)).trimEnd()
  const message = (await requirePrivateFile(handoff.private_message_file)).trim()
  const steer = (await requirePrivateFile(handoff.private_steer_file)).trim()
  expect(password.length).toBeGreaterThan(0)
  expect(message.length).toBeGreaterThan(0)
  expect(message.length).toBeLessThanOrEqual(8_000)
  expect(steer.length).toBeGreaterThan(0)
  expect(steer.length).toBeLessThanOrEqual(8_000)

  const receipt: Receipt = {
    evidence_kind: handoff.evidence_kind,
    status: 'RUNNING',
    fixture: false,
    authorization_id: handoff.browser_proof.authorization_id,
    base_url: handoff.url,
    source: handoff.served_frontend,
    started_at: new Date().toISOString(),
    limits: {
      max_queries: handoff.browser_proof.max_queries,
      max_input_turns: handoff.browser_proof.max_input_turns,
    },
    owned_session_ids: [],
    steps: [],
    raw_responses: [],
    outside_requests_blocked: [],
    unexpected_mutations_blocked: [],
    cleanup: { attempted: false },
  }
  const origin = new URL(handoff.url).origin
  const project = handoff.project.id
  const mutationPaths = [
    /^\/api\/orchestrator\/v1\/config$/,
    new RegExp(`^/api/projects/${project}/lifecycle/v1/intents$`),
  ]
  const ownedHarnessMutation = new RegExp(
    `^/api/projects/${project}/harness-sessions/([0-9a-f-]+)/(?:messages/v1|controls/v1/(?:interrupt|stop))$`,
  )
  const context = await browser.newContext({
    baseURL: handoff.url,
    viewport: { width: 1440, height: 1000 },
  })
  const rawIndex = { value: 0 }
  const pages = new Set<Page>()
  let page: Page | null = null
  let failure: unknown = null
  let originalRoot: { key: string; displayLabel: string } | null = null
  let rootBindingChanged = false

  async function saveRaw(label: string, response: Response | APIResponse) {
    const body = await response.body()
    const index = String(++rawIndex.value).padStart(2, '0')
    const safeLabel = label.replace(/[^a-z0-9-]/gi, '-').toLowerCase()
    const rawPath = resolve(evidenceDirectory, `${index}-${safeLabel}.response`)
    await writeFile(rawPath, body, { flag: 'wx', mode: 0o600 })
    receipt.raw_responses.push({
      label,
      status: response.status(),
      path: new URL(response.url()).pathname,
      file: rawPath,
      sha256: sha256(body),
    })
  }

  async function guard(candidate: Page) {
    pages.add(candidate)
    await candidate.route('**/*', async (route) => {
      const request = route.request()
      const url = new URL(request.url())
      if (url.origin !== origin) {
        receipt.outside_requests_blocked.push(url.origin)
        return route.abort()
      }
      if (!['GET', 'HEAD'].includes(request.method())) {
        const owned = ownedHarnessMutation.exec(url.pathname)
        const allowed =
          mutationPaths.some((pattern) => pattern.test(url.pathname)) ||
          (!!owned && receipt.owned_session_ids.includes(owned[1]!))
        if (!allowed) {
          receipt.unexpected_mutations_blocked.push(`${request.method()} ${url.pathname}`)
          return route.abort()
        }
      }
      return route.continue()
    })
  }

  async function getJSON(api: APIRequestContext, path: string): Promise<unknown> {
    expect(new URL(path, origin).origin).toBe(origin)
    const response = await api.get(path, { maxRedirects: 0, timeout: 8_000 })
    expect(response.status()).toBe(200)
    return response.json()
  }

  function sessionRows(value: unknown): JSONRecord[] {
    const rows = Array.isArray(value) ? value : record(value).sessions
    expect(Array.isArray(rows)).toBe(true)
    return rows as JSONRecord[]
  }

  async function orchestration(api: APIRequestContext) {
    return record(
      await getJSON(api, `/api/agent-mode/projects/${project}/orchestration/v1?zoom=10`),
    )
  }

  async function workerRow(api: APIRequestContext, id: string) {
    const fleet = record((await orchestration(api)).fleet)
    const workers = fleet.workers as JSONRecord[]
    return workers.find((row) => row.harness_session_id === id) ?? null
  }

  async function submitIntent(label: string) {
    const review = page!.getByRole('button', { name: `Review ${label} request`, exact: true })
    await expect(review).toBeEnabled()
    await review.click()
    await expect(
      page!.getByRole('button', { name: 'Confirm exact request', exact: true }),
    ).toBeFocused()
    const [created] = await Promise.all([
      page!.waitForResponse(
        (response) =>
          response.request().method() === 'POST' &&
          new URL(response.url()).pathname === `/api/projects/${project}/lifecycle/v1/intents`,
      ),
      page!.getByRole('button', { name: 'Confirm exact request', exact: true }).click(),
    ])
    await saveRaw(`${label}-intent-created`, created)
    expect(created.status()).toBe(201)
    let intent = await readJSON(created)
    let terminalResponse = created
    expect(record(intent.request).operation).toBe(label)
    for (
      let attempt = 0;
      attempt < 90 &&
      !['completed', 'failed', 'expired', 'cancelled'].includes(String(intent.state));
      attempt++
    ) {
      await page!.waitForTimeout(500)
      const [checked] = await Promise.all([
        page!.waitForResponse(
          (response) =>
            response.request().method() === 'GET' &&
            new URL(response.url()).pathname.endsWith(`/lifecycle/v1/intents/${intent.id}`),
        ),
        page!.getByRole('button', { name: 'Refresh intent evidence', exact: true }).click(),
      ])
      expect(checked.status()).toBe(200)
      intent = await readJSON(checked)
      terminalResponse = checked
    }
    await saveRaw(`${label}-intent-terminal`, terminalResponse)
    expect(intent.state).toBe('completed')
    expect(intent.reason).toBe('applied')
    receipt.steps.push({
      step: label,
      intent_id: intent.id,
      state: intent.state,
      result_session_id: intent.result_session_id ?? null,
      new_generation: intent.new_generation ?? null,
    })
    await page!.getByRole('button', { name: 'Finish reviewing this result', exact: true }).click()
    await expect
      .poll(() =>
        page!
          .getByTestId('habitat-lifecycle')
          .evaluate((element) => element.contains(document.activeElement)),
      )
      .toBe(true)
    return intent
  }

  async function selectOwnedWorker(target: Page, id: string) {
    await target.goto(`/?view=workers&project=${project}&zoom=10`, {
      waitUntil: 'domcontentloaded',
    })
    await expect(
      target.getByRole('heading', { name: 'Your workers and their current work.' }),
    ).toBeVisible()
    for (let attempt = 0; attempt < 6; attempt++) {
      const row = target.locator(`[data-worker-id="${id}"]`)
      if (await row.count()) {
        await row.locator('.habitat-worker-select').click()
        await expect(target.getByRole('heading', { name: 'Controls', exact: true })).toBeVisible()
        return
      }
      const expand = target.getByRole('button', { name: /Expand .* descendants/ }).first()
      if (await expand.count()) await expand.click()
      else await target.getByRole('button', { name: 'Refresh', exact: true }).click()
    }
    throw new Error('owned_worker_not_visible')
  }

  async function controlSelected(target: Page, id: string, kind: 'interrupt' | 'stop') {
    const label = kind[0]!.toUpperCase() + kind.slice(1)
    await target.getByRole('button', { name: label, exact: true }).click()
    await expect(target.getByRole('button', { name: `Confirm ${kind}`, exact: true })).toBeFocused()
    const path = `/api/projects/${project}/harness-sessions/${id}/controls/v1/${kind}`
    const [requested] = await Promise.all([
      target.waitForResponse(
        (response) =>
          response.request().method() === 'POST' && new URL(response.url()).pathname === path,
      ),
      target.getByRole('button', { name: `Confirm ${kind}`, exact: true }).click(),
    ])
    await saveRaw(`${kind}-control-created-${id.slice(0, 8)}`, requested)
    expect(requested.status()).toBe(200)
    const created = record((await requested.json()).control)
    let terminal = created
    let terminalResponse = requested
    for (
      let attempt = 0;
      attempt < 80 && !['applied', 'rejected'].includes(String(terminal.state));
      attempt++
    ) {
      await target.waitForTimeout(250)
      const [checked] = await Promise.all([
        target.waitForResponse(
          (response) =>
            response.request().method() === 'GET' &&
            new URL(response.url()).pathname.endsWith(`/controls/${created.id}`),
        ),
        target.getByRole('button', { name: 'Check control outcome', exact: true }).click(),
      ])
      expect(checked.status()).toBe(200)
      terminal = await readJSON(checked)
      terminalResponse = checked
    }
    expect(terminal.state).toBe('applied')
    await saveRaw(`${kind}-control-terminal-${id.slice(0, 8)}`, terminalResponse)
    return { id: created.id, state: terminal.state, reason: terminal.reason }
  }

  async function control(target: Page, id: string, kind: 'interrupt' | 'stop') {
    await selectOwnedWorker(target, id)
    return controlSelected(target, id, kind)
  }

  async function sendMessage(
    target: Page,
    id: string,
    level: 'simple' | 'steer',
    body: string,
  ) {
    const openLabel = level === 'simple' ? 'Message' : 'Steer'
    const submitLabel = level === 'simple' ? 'Send message' : 'Confirm steer'
    await target.getByRole('button', { name: openLabel, exact: true }).click()
    await target.getByLabel('Message draft', { exact: true }).fill(body)
    const path = `/api/projects/${project}/harness-sessions/${id}/messages/v1`
    const [sent] = await Promise.all([
      target.waitForResponse(
        (response) =>
          response.request().method() === 'POST' && new URL(response.url()).pathname === path,
      ),
      target.getByRole('button', { name: submitLabel, exact: true }).click(),
    ])
    await saveRaw(`${level}-message-created`, sent)
    expect(sent.status()).toBe(201)
    const acknowledgement = await readJSON(sent)
    expect(acknowledgement.schema_version).toBe(1)
    expect(acknowledgement.harness_session_id).toBe(id)
    expect(acknowledgement.delivery_level).toBe(level)
    expect(UUID.test(String(acknowledgement.message_id))).toBe(true)
    expect(UUID.test(String(acknowledgement.delivery_id))).toBe(true)
    let delivery: JSONRecord | null = null
    for (let attempt = 0; attempt < 80 && delivery?.state !== 'handed_off'; attempt++) {
      const value = await getJSON(context.request, `/api/projects/${project}/message-deliveries`)
      const rows = Array.isArray(value) ? value : (record(value).deliveries as JSONRecord[])
      delivery = rows.find((row) => row.delivery_id === acknowledgement.delivery_id) ?? null
      if (
        delivery?.last_error_code ||
        ['failed', 'unknown', 'handoff_required'].includes(String(delivery?.state))
      )
        throw new Error(`${level}_message_delivery_failed`)
      if (delivery?.state !== 'handed_off') await target.waitForTimeout(250)
    }
    expect(delivery?.message_id).toBe(acknowledgement.message_id)
    expect(delivery?.state).toBe('handed_off')
    expect(delivery?.effective_level).toBe(level)
    return {
      message_id: acknowledgement.message_id,
      delivery_id: acknowledgement.delivery_id,
      requested_level: level,
      effective_level: delivery!.effective_level,
      state: delivery!.state,
    }
  }

  async function cleanupOwned(api: APIRequestContext) {
    receipt.cleanup = { attempted: true, stopped: [], unresolved: [] }
    let rows: JSONRecord[] = []
    try {
      rows = sessionRows(await getJSON(api, `/api/projects/${project}/harness-sessions`))
    } catch {
      ;(receipt.cleanup.unresolved as unknown[]).push('public_session_inventory_unavailable')
      return
    }
    const started = Date.parse(receipt.started_at) - 5_000
    const names = new Set([handoff.agents.coordinator.name, handoff.agents.worker.name])
    for (const row of rows) {
      if (
        names.has(String(row.agent_name)) &&
        Date.parse(String(row.created_at)) >= started &&
        UUID.test(String(row.id)) &&
        !receipt.owned_session_ids.includes(String(row.id))
      )
        receipt.owned_session_ids.push(String(row.id))
    }
    for (const id of [...receipt.owned_session_ids].reverse()) {
      const current = rows.find((row) => row.id === id)
      if (!current || current.phase === 'stopped') continue
      try {
        await control(page!, id, 'stop')
        ;(receipt.cleanup.stopped as unknown[]).push(id)
      } catch {
        ;(receipt.cleanup.unresolved as unknown[]).push(id)
      }
    }
    rows = sessionRows(await getJSON(api, `/api/projects/${project}/harness-sessions`))
    receipt.cleanup.public_terminal = receipt.owned_session_ids.map((id) => {
      const row = rows.find((candidate) => candidate.id === id)
      return { id, phase: row?.phase ?? null, activity_state: row?.activity_state ?? null }
    })
  }

  async function restoreRootBinding() {
    if (!page || !rootBindingChanged || !originalRoot) return
    await page.goto(`/?view=assign&project=${project}`, { waitUntil: 'domcontentloaded' })
    await page.getByLabel('Canonical agent', { exact: true }).selectOption(originalRoot.key)
    await page.getByLabel('Root display label', { exact: true }).fill(originalRoot.displayLabel)
    await page.getByRole('button', { name: 'Review root binding', exact: true }).click()
    const [restored] = await Promise.all([
      page.waitForResponse(
        (response) =>
          response.request().method() === 'PUT' &&
          new URL(response.url()).pathname === '/api/orchestrator/v1/config',
      ),
      page.getByRole('button', { name: 'Confirm root binding', exact: true }).click(),
    ])
    await saveRaw('root-binding-restored', restored)
    expect(restored.status()).toBe(200)
    const config = await readJSON(restored)
    expect(record(config.orchestrator).key).toBe(originalRoot.key)
    expect(record(config.orchestrator).display_label).toBe(originalRoot.displayLabel)
    rootBindingChanged = false
    receipt.cleanup.root_binding_restored = true
  }

  try {
    await guard((page = await context.newPage()))
    // APIRequestContext is used only for the explicit login and authenticated reads.
    const login = await context.request.post('/api/auth/login', {
      data: { username: 'admin', password },
      maxRedirects: 0,
      timeout: 8_000,
    })
    expect(login.status()).toBe(200)
    const index = await context.request.get('/', { maxRedirects: 0, timeout: 8_000 })
    expect(index.status()).toBe(200)
    expect(sha256(await index.body())).toBe(handoff.served_frontend.index_sha256)
    const before = sessionRows(
      await getJSON(context.request, `/api/projects/${project}/harness-sessions`),
    )
    expect(
      before.some(
        (row) =>
          row.phase !== 'stopped' &&
          [handoff.agents.coordinator.name, handoff.agents.worker.name].includes(
            String(row.agent_name),
          ),
      ),
    ).toBe(false)
    const originalConfig = record(await getJSON(context.request, '/api/orchestrator/v1/config'))
    const originalIdentity = record(originalConfig.orchestrator)
    expect(originalIdentity.project_id).toBe(project)
    expect(originalIdentity.key).toMatch(/^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$/)
    expect(originalIdentity.display_label).toMatch(/^.{1,64}$/)
    originalRoot = {
      key: String(originalIdentity.key),
      displayLabel: String(originalIdentity.display_label),
    }
    expect(originalRoot.key).not.toBe(handoff.agents.coordinator.name)

    await page.goto(`/?view=assign&project=${project}`, { waitUntil: 'domcontentloaded' })
    await page
      .getByLabel('Canonical agent', { exact: true })
      .selectOption(handoff.agents.coordinator.name)
    await page
      .getByLabel('Root display label', { exact: true })
      .fill(handoff.browser_proof.root_display_label)
    await page.getByRole('button', { name: 'Review root binding', exact: true }).click()
    await expect(
      page.getByRole('button', { name: 'Confirm root binding', exact: true }),
    ).toBeFocused()
    const [binding] = await Promise.all([
      page.waitForResponse(
        (response) =>
          response.request().method() === 'PUT' &&
          new URL(response.url()).pathname === '/api/orchestrator/v1/config',
      ),
      page.getByRole('button', { name: 'Confirm root binding', exact: true }).click(),
    ])
    if (binding.status() === 200) rootBindingChanged = true
    await saveRaw('root-binding', binding)
    expect(binding.status()).toBe(200)
    const bound = await readJSON(binding)
    expect(record(bound.orchestrator).key).toBe(handoff.agents.coordinator.name)
    receipt.steps.push({
      step: 'root-binding',
      revision: bound.revision,
      key: record(bound.orchestrator).key,
    })

    await page
      .getByLabel('Profile for a new worker', { exact: true })
      .selectOption(`${handoff.profile.id}@${handoff.profile.version}`)
    await page.getByLabel('Runtime', { exact: true }).selectOption(handoff.runtime.id)
    await page
      .getByLabel('Workspace', { exact: true })
      .selectOption(handoff.workspaces.coordinator.handle)
    await page.getByLabel('Role', { exact: true }).selectOption('coordinator')
    const rootIntent = await submitIntent('start')
    const rootID = String(rootIntent.result_session_id)
    expect(UUID.test(rootID)).toBe(true)
    receipt.owned_session_ids.push(rootID)

    await page
      .getByLabel('Canonical agent', { exact: true })
      .selectOption(handoff.agents.worker.name)
    await page
      .getByLabel('Profile for a new worker', { exact: true })
      .selectOption(`${handoff.profile.id}@${handoff.profile.version}`)
    await expect(page.getByLabel('Runtime', { exact: true })).toBeEnabled()
    await page.getByLabel('Runtime', { exact: true }).selectOption(handoff.runtime.id)
    await page
      .getByLabel('Workspace', { exact: true })
      .selectOption(handoff.workspaces.worker.handle)
    await page.getByLabel('Role', { exact: true }).selectOption('worker')
    const childIntent = await submitIntent('start')
    const childID = String(childIntent.result_session_id)
    expect(UUID.test(childID)).toBe(true)
    expect(childID).not.toBe(rootID)
    receipt.owned_session_ids.push(childID)

    const stalePage = await context.newPage()
    await guard(stalePage)
    // Keep this second tab on its original read. This is an injected browser
    // disconnect, not a mock server response; the main tab still uses the
    // actual server and advances the binding revision below.
    await stalePage.route('**/api/agent-mode/deliveries/events**', (route) => route.abort())
    await selectOwnedWorker(stalePage, childID)
    await stalePage.getByRole('button', { name: 'Stop', exact: true }).click()
    await expect(stalePage.getByRole('button', { name: 'Confirm stop', exact: true })).toBeFocused()
    await stalePage.route(`**/api/agent-mode/projects/${project}/orchestration/v1**`, (route) =>
      route.abort(),
    )

    await page.goto(`/?view=assign&project=${project}&worker=${childID}`, {
      waitUntil: 'domcontentloaded',
    })
    await expect(page.getByLabel('Operation', { exact: true })).toHaveValue('reassign')
    await page.getByLabel('Operation', { exact: true }).selectOption('attach')
    await expect(page.getByLabel('Lifecycle worker', { exact: true })).toHaveValue(childID)
    await page.getByLabel('Ticket', { exact: true }).selectOption(String(handoff.ticket.id))
    await page.getByLabel('Parent worker', { exact: true }).selectOption(rootID)
    const attachIntent = await submitIntent('attach')
    expect(attachIntent.result_session_id).toBe(childID)

    const [staleStop] = await Promise.all([
      stalePage.waitForResponse(
        (response) =>
          response.request().method() === 'POST' &&
          new URL(response.url()).pathname.endsWith(
            `/harness-sessions/${childID}/controls/v1/stop`,
          ),
      ),
      stalePage.getByRole('button', { name: 'Confirm stop', exact: true }).click(),
    ])
    await saveRaw('stale-tab-stop-refusal', staleStop)
    expect(staleStop.status()).toBe(409)
    await expect(
      stalePage.getByText(
        'Worker revision or recipient changed. Refresh and review before trying again.',
      ),
    ).toBeVisible()
    receipt.steps.push({
      step: 'stale-disconnected-tab-refusal',
      status: staleStop.status(),
      mutation_applied: false,
    })
    await stalePage.close()
    pages.delete(stalePage)

    await selectOwnedWorker(page, childID)
    receipt.steps.push({ step: 'message', ...(await sendMessage(page, childID, 'simple', message)) })

    let busyObserved = false
    for (let attempt = 0; attempt < 24 && !busyObserved; attempt++) {
      const current = await workerRow(context.request, childID)
      busyObserved = !!current?.liveness && record(current.liveness).state === 'busy'
      if (!busyObserved) await page.waitForTimeout(250)
    }
    receipt.steps.push({ step: 'steer', ...(await sendMessage(page, childID, 'steer', steer)) })
    const beforeInterrupt = await workerRow(context.request, childID)
    const busyAtInterrupt =
      !!beforeInterrupt?.liveness && record(beforeInterrupt.liveness).state === 'busy'
    const interrupt = await controlSelected(page, childID, 'interrupt')
    receipt.steps.push({
      step: 'interrupt',
      ...interrupt,
      busy_observed_before_steer: busyObserved,
      busy_observed_immediately_before_request: busyAtInterrupt,
      claim: busyAtInterrupt
        ? 'active-turn interruption'
        : 'control acceptance only; no active turn observed',
    })

    const stoppedChild = await control(page, childID, 'stop')
    receipt.steps.push({ step: 'stop-child', ...stoppedChild })
    await selectOwnedWorker(page, childID)
    await expect(page.getByRole('button', { name: 'Stop', exact: true })).toBeDisabled()
    receipt.steps.push({ step: 'stopped-generation-control-denied', mutation_applied: false })

    await page.goto(`/?view=assign&project=${project}&worker=${childID}`, {
      waitUntil: 'domcontentloaded',
    })
    await expect(page.getByLabel('Operation', { exact: true })).toHaveValue('restart')
    const restartIntent = await submitIntent('restart')
    const replacementID = String(restartIntent.result_session_id)
    expect(UUID.test(replacementID)).toBe(true)
    expect(replacementID).not.toBe(childID)
    receipt.owned_session_ids.push(replacementID)

    await page.getByLabel('Operation', { exact: true }).selectOption('repair')
    await page.getByLabel('Repair layer', { exact: true }).selectOption('listeners')
    const repairIntent = await submitIntent('repair')
    expect(repairIntent.result_session_id ?? null).toBeNull()

    receipt.steps.push({
      step: 'stop-replacement',
      ...(await control(page, replacementID, 'stop')),
    })
    receipt.steps.push({ step: 'stop-coordinator', ...(await control(page, rootID, 'stop')) })
    await cleanupOwned(context.request)
    await restoreRootBinding()
    const terminal = record(receipt.cleanup).public_terminal as JSONRecord[]
    expect(terminal).toHaveLength(3)
    expect(terminal.every((row) => row.phase === 'stopped' && row.activity_state === 'dead')).toBe(
      true,
    )
    expect(receipt.outside_requests_blocked).toEqual([])
    expect(receipt.unexpected_mutations_blocked).toEqual([])
    receipt.status = 'PASS'
  } catch (error) {
    failure = error
    receipt.status = 'FAIL'
    receipt.failure = { name: error instanceof Error ? error.name : 'UnknownError' }
    if (page) await cleanupOwned(context.request).catch(() => {})
    await restoreRootBinding().catch(() => {
      const unresolved = record(receipt.cleanup).unresolved as unknown[] | undefined
      unresolved?.push('root_binding_not_restored')
    })
  } finally {
    receipt.finished_at = new Date().toISOString()
    await writeFile(resultPath, `${JSON.stringify(receipt, null, 2)}\n`, {
      flag: 'wx',
      mode: 0o600,
    })
    for (const candidate of pages) await candidate.close().catch(() => {})
    await context.close()
  }
  if (failure) throw failure
})
