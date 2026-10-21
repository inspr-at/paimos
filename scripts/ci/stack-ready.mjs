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
  return new Error(`${label} did not become ready within ${timeoutMs}ms`)
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
  return {
    webURL: `http://127.0.0.1:${uiDevPort(cwd, env)}`,
    backendURL: new URL('/api/health', env.AEON_API_URL ?? 'http://127.0.0.1:8080').href,
    backend: backendMode(env, 'optional'),
    timeoutMs: stackTimeout(env),
  }
}

export function e2eStackTarget(env = process.env) {
  const base = env.BASE_URL ?? 'http://127.0.0.1:8080'
  return {
    webURL: base,
    backendURL: new URL('/api/health', base).href,
    backend: 'required',
    timeoutMs: stackTimeout(env),
  }
}

function deps(options) {
  return {
    fetch: options.fetch ?? globalThis.fetch,
    sleep: options.sleep ?? (ms => new Promise(resolveSleep => setTimeout(resolveSleep, ms))),
    now: options.now ?? Date.now,
    intervalMs: positive('readiness interval', options.intervalMs ?? 200),
    timeoutMs: positive('readiness timeout', options.timeoutMs ?? 30_000),
  }
}

export async function waitForHTTP(url, options = {}) {
  const { fetch, sleep, now, intervalMs, timeoutMs } = deps(options)
  const kind = options.kind === 'backend' ? 'backend' : 'web'
  const deadline = options.deadline ?? now() + timeoutMs
  while (now() < deadline) {
    try {
      const res = await fetch(url)
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
  const { fetch, sleep, now, intervalMs, timeoutMs } = deps(options)
  const deadline = options.deadline ?? now() + timeoutMs
  const shared = { fetch, sleep, now, intervalMs, timeoutMs, deadline }
  await waitForHTTP(options.webURL, { ...shared, kind: 'web' })
  if (backend === 'off') return { web: 'up', backend: 'off' }
  if (backend === 'optional') {
    // Nothing is starting on a mocked shard. One refused probe returns; a full
    // bound per playwright-safe chunk would burn the shard on a closed port.
    try {
      const res = await fetch(options.backendURL)
      if (res && res.status >= 200 && res.status < 300) return { web: 'up', backend: 'up' }
    } catch (err) {
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

export async function healthIsReady(url, fetchImpl = globalThis.fetch) {
  try {
    const res = await fetchImpl(url)
    return !!res && res.status >= 200 && res.status < 300
  } catch (err) {
    if (isConnectionRefused(err)) return false
    throw err
  }
}

export function holdBackendResponse(res) {
  if (res.writableEnded) return
  res.statusCode = 502
  res.setHeader('Connection', 'close')
  res.setHeader('Content-Type', 'text/plain; charset=utf-8')
  res.end('backend unavailable')
}

// Vite 8.3 treats a string bypass as a rewrite unless the response is already
// ended. Ending here is what stops http-proxy from dialing :8080.
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
