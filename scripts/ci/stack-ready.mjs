// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

// Startup failures stay inside the bound. Node fetch wraps a refused socket as
// TypeError('fetch failed') with cause.code, sometimes under AggregateError.
export function isConnectionRefused(err) {
  const seen = new Set()
  const pending = [err]
  while (pending.length) {
    const current = pending.pop()
    if (!current || typeof current !== 'object' || seen.has(current)) continue
    seen.add(current)
    if (current.code === 'ECONNREFUSED') return true
    if (typeof current.message === 'string' && current.message.includes('ECONNREFUSED')) return true
    if (current.cause) pending.push(current.cause)
    if (Array.isArray(current.errors)) pending.push(...current.errors)
  }
  return false
}

export function readinessError(kind, url, timeoutMs) {
  const label = kind === 'backend' ? `Go backend at ${url}` : `Web server at ${url}`
  const error = new Error(`${label} did not become ready within ${timeoutMs}ms`)
  error.code = 'AEON_NOT_READY'
  return error
}

export function ciEnabled(env = process.env) {
  return env.CI !== undefined && env.CI !== '' && !['0', 'false'].includes(env.CI)
}

function positive(name, value) {
  if (!Number.isFinite(value) || value <= 0) throw new Error(`${name} must be a positive number of milliseconds`)
  return value
}

export function stackTimeout(env = process.env) {
  if (env.AEON_STACK_READY_TIMEOUT_MS === undefined || env.AEON_STACK_READY_TIMEOUT_MS === '') return 30_000
  return positive('AEON_STACK_READY_TIMEOUT_MS', Number(env.AEON_STACK_READY_TIMEOUT_MS))
}

export function backendMode(env = process.env, fallback = 'optional') {
  const backend = env.AEON_REQUIRE_BACKEND ?? fallback
  if (backend !== 'required' && backend !== 'optional' && backend !== 'off') {
    throw new Error('AEON_REQUIRE_BACKEND must be required, optional, or off')
  }
  return backend
}

// Same derivation as web/playwright.ui.config.ts. Each checkout keeps its own port.
export function uiDevPort(cwd = process.cwd(), env = process.env) {
  const derived = 5200 + (parseInt(createHash('sha256').update(cwd).digest('hex').slice(0, 4), 16) % 700)
  return env.PLAYWRIGHT_PORT ?? String(derived)
}

export function uiStackTarget(env = process.env, cwd = process.cwd()) {
  // CI waits for the backend. A mock-only job must opt out with
  // AEON_REQUIRE_BACKEND=off. Local runs still probe once.
  return {
    webURL: `http://127.0.0.1:${uiDevPort(cwd, env)}`,
    backendURL: new URL('/api/health', env.AEON_API_URL ?? 'http://127.0.0.1:8080').href,
    backend: backendMode(env, ciEnabled(env) ? 'required' : 'optional'),
    timeoutMs: stackTimeout(env),
  }
}

// Approved mock-only jobs fulfill API routes in the browser and do not start
// paimos serve. Each workflow job sets AEON_REQUIRE_BACKEND=off. The full-ui-qa
// shard also runs the performance preview, which has no readiness setup and
// must not start a Go server.
export const mockOnlyCiJobs = [
  ['ci.yml', 'web-shard'],
  ['ci.yml', 'visual-reference-capture'],
  ['nightly-full.yml', 'nightly-web-shard'],
  ['full-ui-qa.yml', 'qa-shard'],
  ['agents-dial-ui.yml', 'agents-dial-ui'],
  ['agents-run-kinds.yml', 'execution-column'],
  ['ci-preflight.yml', 'browser'],
]

// These jobs start paimos serve and set AEON_REQUIRE_BACKEND=required.
export const backendRequiredCiJobs = [
  ['ci.yml', 'e2e-run'],
  ['nightly-full.yml', 'nightly-e2e'],
]

export function e2eStackTarget(env = process.env) {
  const base = env.BASE_URL ?? 'http://127.0.0.1:8080'
  return {
    webURL: base,
    backendURL: new URL('/api/health', base).href,
    backend: 'required',
    timeoutMs: stackTimeout(env),
  }
}

function defaultSchedule(ms, fn) {
  const id = setTimeout(fn, ms)
  return () => clearTimeout(id)
}

function deps(options) {
  return {
    fetch: options.fetch ?? globalThis.fetch,
    sleep: options.sleep ?? (ms => new Promise(resolveSleep => setTimeout(resolveSleep, ms))),
    now: options.now ?? Date.now,
    schedule: options.schedule ?? defaultSchedule,
    intervalMs: positive('readiness interval', options.intervalMs ?? 200),
    timeoutMs: positive('readiness timeout', options.timeoutMs ?? 30_000),
  }
}

// One attempt. The schedule aborts the socket when the shared deadline is
// reached. A 2xx after that abort, or after the clock has passed the
// deadline, is not readiness.
async function probeHTTP(url, { fetch, schedule, now, deadline }) {
  const remaining = deadline - now()
  if (remaining <= 0) return { late: true }
  const controller = new AbortController()
  const cancel = schedule(remaining, () => controller.abort())
  try {
    const res = await fetch(url, { signal: controller.signal })
    if (controller.signal.aborted || now() >= deadline) return { late: true, res }
    return { res }
  } catch (err) {
    if (controller.signal.aborted || now() >= deadline) return { late: true, err }
    throw err
  } finally {
    cancel()
  }
}

export async function waitForHTTP(url, options = {}) {
  const { fetch, sleep, now, schedule, intervalMs, timeoutMs } = deps(options)
  const kind = options.kind === 'backend' ? 'backend' : 'web'
  const deadline = options.deadline ?? now() + timeoutMs
  while (now() < deadline) {
    try {
      const probed = await probeHTTP(url, { fetch, schedule, now, deadline })
      if (probed.late) break
      const res = probed.res
      if (res && res.status >= 200 && res.status < 300) return { url, status: res.status }
    } catch {
      // Refused and other startup failures are retries, not the shard result.
    }
    const remaining = deadline - now()
    if (remaining <= 0) break
    await sleep(Math.min(intervalMs, remaining))
  }
  throw readinessError(kind, url, timeoutMs)
}

export async function waitForStack(options) {
  const backend = options.backend ?? 'optional'
  if (backend !== 'required' && backend !== 'optional' && backend !== 'off') {
    throw new Error('AEON_REQUIRE_BACKEND must be required, optional, or off')
  }
  const { fetch, sleep, now, schedule, intervalMs, timeoutMs } = deps(options)
  const deadline = options.deadline ?? now() + timeoutMs
  const shared = { fetch, sleep, now, schedule, intervalMs, timeoutMs, deadline }
  await waitForHTTP(options.webURL, { ...shared, kind: 'web' })
  if (backend === 'off') return { web: 'up', backend: 'off' }
  if (backend === 'optional') {
    // Local mocked runs have nothing starting on :8080. One refused probe
    // returns. A stall or a late 2xx still ends at the shared deadline.
    try {
      const probed = await probeHTTP(options.backendURL, { fetch, schedule, now, deadline })
      if (probed.late) throw readinessError('backend', options.backendURL, timeoutMs)
      if (probed.res && probed.res.status >= 200 && probed.res.status < 300) return { web: 'up', backend: 'up' }
    } catch (err) {
      if (err && err.code === 'AEON_NOT_READY') throw err
      if (isConnectionRefused(err)) return { web: 'up', backend: 'down' }
    }
    if (now() >= deadline) throw readinessError('backend', options.backendURL, timeoutMs)
    await waitForHTTP(options.backendURL, { ...shared, kind: 'backend' })
    return { web: 'up', backend: 'up' }
  }
  await waitForHTTP(options.backendURL, { ...shared, kind: 'backend' })
  return { web: 'up', backend: 'up' }
}

// Success sticks. A miss does not: a backend that starts later must be proxied.
export function createBackendProxyGate({ check }) {
  let healthy = false
  return async function gate({ connect, respond }) {
    if (!healthy && await check()) healthy = true
    if (!healthy) {
      await respond()
      return 'hold'
    }
    await connect()
    return 'proxy'
  }
}

export async function healthIsReady(url, fetchImpl = globalThis.fetch, options = {}) {
  const now = options.now ?? Date.now
  const schedule = options.schedule ?? defaultSchedule
  const timeoutMs = options.timeoutMs ?? stackTimeout(options.env)
  const deadline = options.deadline ?? now() + timeoutMs
  try {
    const probed = await probeHTTP(url, { fetch: fetchImpl, schedule, now, deadline })
    if (probed.late) return false
    return !!probed.res && probed.res.status >= 200 && probed.res.status < 300
  } catch (err) {
    if (isConnectionRefused(err)) return false
    throw err
  }
}

// Mock-only jobs must not dial. Every other test-mode proxy check is bounded
// and a late 2xx stays down.
export function proxyReadyCheck(env = process.env, url, fetchImpl = globalThis.fetch, options = {}) {
  if (env.AEON_REQUIRE_BACKEND === 'off') return async () => false
  return () => healthIsReady(url, fetchImpl, { env, ...options })
}

export function holdBackendResponse(res) {
  // Vite calls bypass with no response on a WebSocket upgrade. There is
  // nothing to end, and reading writableEnded would throw.
  if (!res || res.writableEnded) return
  res.statusCode = 502
  res.setHeader('Connection', 'close')
  res.setHeader('Content-Type', 'text/plain; charset=utf-8')
  res.end('backend unavailable')
}

// Vite 8.3 treats a string bypass as a rewrite unless the response is already
// ended. Ending here is what stops http-proxy from dialing :8080.
// res is undefined for a WebSocket upgrade; a hold still returns the URL.
export async function backendProxyBypass(gate, req, res) {
  const decision = await gate({
    connect: async () => {},
    respond: async () => holdBackendResponse(res),
  })
  if (decision === 'hold') return req.url ?? '/'
}

export async function main(argv, options = {}) {
  const url = argv[0]
  const stderr = options.stderr ?? (line => console.error(line))
  if (!url) {
    stderr('usage: node scripts/ci/stack-ready.mjs <url>')
    return 2
  }
  try {
    await waitForHTTP(url, {
      kind: 'backend',
      timeoutMs: options.timeoutMs ?? stackTimeout(options.env ?? process.env),
      intervalMs: options.intervalMs,
      fetch: options.fetch,
      sleep: options.sleep,
      now: options.now,
    })
    return 0
  } catch (err) {
    stderr(err instanceof Error ? err.message : String(err))
    return 1
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = await main(process.argv.slice(2))
}
