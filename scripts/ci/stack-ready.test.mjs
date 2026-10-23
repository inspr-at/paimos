// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import {
  backendMode,
  backendProxyBypass,
  backendRequiredCiJobs,
  createBackendProxyGate,
  e2eStackTarget,
  healthIsReady,
  holdBackendResponse,
  isConnectionRefused,
  main,
  mockOnlyCiJobs,
  proxyReadyCheck,
  stackTimeout,
  uiDevPort,
  uiStackTarget,
  waitForHTTP,
  waitForStack,
} from './stack-ready.mjs'
import { browserPreflight } from '../ci-preflight-browser.mjs'
import { runE2eStackSetup } from './e2e-stack-setup.mjs'
import { runUiStackSetup } from './ui-stack-setup.mjs'

const webURL = 'http://127.0.0.1:5200'
const backendURL = 'http://127.0.0.1:8080/api/health'

function clock() {
  let t = 0
  return { now: () => t, sleep: async ms => { t += ms } }
}

function timerClock() {
  let t = 0
  const timers = []
  const fire = () => {
    for (const timer of timers) {
      if (!timer.fired && timer.at <= t) {
        timer.fired = true
        timer.fn()
      }
    }
  }
  return {
    now: () => t,
    sleep: async ms => { t += ms; fire() },
    schedule: (ms, fn) => {
      const timer = { at: t + ms, fn, fired: false }
      timers.push(timer)
      return () => { timer.fired = true }
    },
  }
}

function refused() {
  const error = new TypeError('fetch failed')
  error.cause = { code: 'ECONNREFUSED', message: 'connect ECONNREFUSED 127.0.0.1:8080' }
  return error
}

function response(status) {
  return { status }
}

test('twenty cold starts are ready before the first spec, which sees no ECONNREFUSED', async () => {
  const wall = Date.now()
  for (let run = 0; run < 20; run++) {
    const time = clock()
    const webAt = 200 * (run + 1)
    const backendAt = webAt + 500
    let spec = false
    let startupRefused = 0
    let specRefused = 0
    const fetch = async url => {
      const up = url === backendURL ? time.now() >= backendAt : time.now() >= webAt
      if (!up) {
        if (spec) specRefused++
        else startupRefused++
        throw refused()
      }
      return response(200)
    }
    let specRan = false
    const result = await waitForStack({
      webURL, backendURL, backend: 'required', timeoutMs: 30_000, intervalMs: 200,
      fetch, sleep: time.sleep, now: time.now,
    })
    spec = true
    const firstSpec = await Promise.all([fetch(webURL), fetch(backendURL)])
    specRan = true
    assert.equal(result.backend, 'up')
    assert.deepEqual(firstSpec.map(item => item.status), [200, 200])
    assert.ok(startupRefused > 0, `run ${run} never saw a closed port`)
    assert.equal(specRefused, 0, `run ${run} refused after ready`)
    assert.equal(specRan, true)
    assert.ok(time.now() >= backendAt)
    assert.ok(time.now() < 30_000)
  }
  assert.ok(Date.now() - wall < 1_000, 'the cold starts must use the injected clock')
})

test('a backend that never accepts fails before the spec and names the bound', async () => {
  const time = clock()
  let specRan = false
  const wall = Date.now()
  await assert.rejects(async () => {
    await waitForStack({
      webURL, backendURL, backend: 'required', timeoutMs: 30_000, intervalMs: 200,
      fetch: async url => {
        if (url === webURL && time.now() >= 400) return response(200)
        throw refused()
      },
      sleep: time.sleep, now: time.now,
    })
    specRan = true
  }, error => {
    assert.equal(error.message, `Go backend at ${backendURL} did not become ready within 30000ms`)
    return true
  })
  assert.equal(specRan, false)
  assert.equal(time.now(), 30_000)
  assert.ok(Date.now() - wall < 1_000)
})

test('a web server that never accepts fails before the backend probe', async () => {
  const time = clock()
  let backendFetches = 0
  let specRan = false
  await assert.rejects(async () => {
    await waitForStack({
      webURL, backendURL, backend: 'required', timeoutMs: 5_000, intervalMs: 200,
      fetch: async url => {
        if (url === backendURL) backendFetches++
        throw refused()
      },
      sleep: time.sleep, now: time.now,
    })
    specRan = true
  }, error => {
    assert.equal(error.message, `Web server at ${webURL} did not become ready within 5000ms`)
    return true
  })
  assert.equal(specRan, false)
  assert.equal(backendFetches, 0)
  assert.equal(time.now(), 5_000)
})

test('optional mode returns on one refused probe and the proxy does not connect', async () => {
  const time = clock()
  let backendFetches = 0
  const result = await waitForStack({
    webURL, backendURL, backend: 'optional', timeoutMs: 30_000, intervalMs: 200,
    fetch: async url => {
      if (url === backendURL) {
        backendFetches++
        throw refused()
      }
      return response(200)
    },
    sleep: async () => { throw new Error('a closed backend must not consume the bound') },
    now: time.now,
  })
  assert.deepEqual(result, { web: 'up', backend: 'down' })
  assert.equal(backendFetches, 1)
  assert.equal(time.now(), 0)

  let connects = 0
  const gate = createBackendProxyGate({
    check: () => healthIsReady(backendURL, async () => { throw refused() }),
  })
  const res = { statusCode: 0, headers: {}, writableEnded: false, setHeader(name, value) { this.headers[name] = value }, end(body) { this.body = body; this.writableEnded = true } }
  const decision = await gate({
    connect: async () => { connects++ },
    respond: async () => holdBackendResponse(res),
  })
  assert.equal(decision, 'hold')
  assert.equal(connects, 0)
  assert.equal(res.statusCode, 502)
  assert.equal(res.writableEnded, true)
  const rewritten = await backendProxyBypass(gate, { url: '/api/nodes/n-a1' }, {
    statusCode: 0, headers: {}, writableEnded: false,
    setHeader(name, value) { this.headers[name] = value },
    end(body) { this.body = body; this.writableEnded = true },
  })
  assert.equal(rewritten, '/api/nodes/n-a1')
})

test('an accepted but unhealthy backend keeps the rest of the optional bound', async () => {
  const time = clock()
  let probes = 0
  const result = await waitForStack({
    webURL, backendURL, backend: 'optional', timeoutMs: 1_000, intervalMs: 200,
    fetch: async url => {
      if (url !== backendURL) return response(200)
      probes++
      return response(time.now() >= 400 ? 200 : 503)
    },
    sleep: time.sleep, now: time.now,
  })
  assert.equal(result.backend, 'up')
  assert.ok(probes > 1)
  assert.equal(time.now(), 400)
})

test('a shared deadline names the backend when only the web server becomes ready', async () => {
  const time = clock()
  await assert.rejects(() => waitForStack({
    webURL, backendURL, backend: 'required', timeoutMs: 1_000, intervalMs: 200,
    fetch: async url => url === webURL ? response(200) : response(503),
    sleep: time.sleep, now: time.now,
  }), error => {
    assert.equal(error.message, `Go backend at ${backendURL} did not become ready within 1000ms`)
    return true
  })
  assert.equal(time.now(), 1_000)
})

test('off mode never dials the backend', async () => {
  const seen = []
  const result = await waitForStack({
    webURL, backendURL, backend: 'off', timeoutMs: 1_000,
    fetch: async url => { seen.push(url); return response(200) },
    sleep: async () => { throw new Error('web was already up') },
    now: () => 0,
  })
  assert.deepEqual(result, { web: 'up', backend: 'off' })
  assert.deepEqual(seen, [webURL])
})

test('a missing proxy response does not dial and does not throw', async () => {
  // Vite calls bypass(req, undefined) for a WebSocket upgrade. A required
  // response makes holdBackendResponse throw before the gate can refuse the dial.
  let checks = 0
  const gate = createBackendProxyGate({
    check: async () => {
      checks++
      return false
    },
  })
  const rewritten = await backendProxyBypass(gate, { url: '/api/events' }, undefined)
  assert.equal(rewritten, '/api/events')
  assert.equal(checks, 1)
  const open = await backendProxyBypass(
    createBackendProxyGate({ check: async () => true }),
    { url: '/api/health' },
    undefined,
  )
  assert.equal(open, undefined)
})

test('proxy connect waits for a 2xx health check and does not cache a miss', async () => {
  let checks = 0
  const gate = createBackendProxyGate({
    check: async () => {
      checks++
      return checks > 1
    },
  })
  let connects = 0
  const down = await gate({ connect: async () => { connects++ }, respond: async () => {} })
  const up = await gate({ connect: async () => { connects++ }, respond: async () => {} })
  const again = await gate({ connect: async () => { connects++ }, respond: async () => {} })
  assert.equal(down, 'hold')
  assert.equal(up, 'proxy')
  assert.equal(again, 'proxy')
  assert.equal(connects, 2)
  assert.equal(checks, 2)
  const res = { statusCode: 0, writableEnded: false, setHeader() {}, end() { this.writableEnded = true } }
  const open = await backendProxyBypass(createBackendProxyGate({ check: async () => true }), { url: '/api/health' }, res)
  assert.equal(open, undefined)
  assert.equal(res.writableEnded, false)
})

test('refused sockets include Node fetch wrappers', () => {
  const aggregate = new TypeError('fetch failed')
  aggregate.cause = new AggregateError([Object.assign(new Error('connect'), { code: 'ECONNREFUSED' })])
  assert.equal(isConnectionRefused(aggregate), true)
  assert.equal(isConnectionRefused(new Error('connect ECONNREFUSED 127.0.0.1:8080')), true)
  assert.equal(isConnectionRefused(new Error('timeout')), false)
  assert.equal(isConnectionRefused(null), false)
})

test('healthIsReady treats refusal as down and a 2xx as up', async () => {
  assert.equal(await healthIsReady(backendURL, async () => { throw refused() }), false)
  assert.equal(await healthIsReady(backendURL, async () => response(200)), true)
  assert.equal(await healthIsReady(backendURL, async () => response(500)), false)
  await assert.rejects(() => healthIsReady(backendURL, async () => { throw new Error('boom') }), /boom/)
})

test('the UI setup probes once by default and the e2e setup requires the backend', async () => {
  let verified = false
  const ui = await runUiStackSetup({
    env: {},
    cwd: '/tmp/aeon-1605-port',
    verify: () => { verified = true },
    fetch: async url => {
      if (String(url).endsWith('/api/health')) throw refused()
      return response(200)
    },
    sleep: async () => { throw new Error('optional refusal must return') },
    now: () => 0,
  })
  assert.equal(verified, true)
  assert.equal(ui.backend, 'down')
  assert.equal(ui.web, 'up')
  assert.equal(uiStackTarget({}, '/tmp/aeon-1605-port').webURL, `http://127.0.0.1:${uiDevPort('/tmp/aeon-1605-port', {})}`)

  const time = clock()
  let fetched = false
  await assert.rejects(() => runE2eStackSetup({
    env: { AEON_REQUIRE_BACKEND: 'off', AEON_STACK_READY_TIMEOUT_MS: '1000', BASE_URL: 'http://127.0.0.1:8080' },
    verify: () => {},
    fetch: async url => {
      fetched = true
      if (String(url).endsWith('/api/health')) throw refused()
      return response(200)
    },
    sleep: time.sleep,
    now: time.now,
  }), /Go backend at http:\/\/127.0.0.1:8080\/api\/health did not become ready within 1000ms/)
  assert.equal(fetched, true)
  assert.equal(time.now(), 1_000)

  let reached = false
  await assert.rejects(() => runUiStackSetup({
    env: { CI: '1' },
    verify: () => { throw new Error('no supervisor') },
    fetch: async () => { reached = true; return response(200) },
  }), /no supervisor/)
  assert.equal(reached, false)
})

test('port derivation matches the UI config and targets stay explicit', () => {
  const cwd = '/tmp/aeon-1605-port'
  const derived = String(5200 + (parseInt(createHash('sha256').update(cwd).digest('hex').slice(0, 4), 16) % 700))
  assert.equal(uiDevPort(cwd, {}), derived)
  assert.equal(uiDevPort(cwd, { PLAYWRIGHT_PORT: '5999' }), '5999')
  assert.equal(backendMode({}, 'optional'), 'optional')
  assert.equal(stackTimeout({}), 30_000)
  assert.equal(stackTimeout({ AEON_STACK_READY_TIMEOUT_MS: '1500' }), 1_500)
  assert.throws(() => backendMode({ AEON_REQUIRE_BACKEND: 'maybe' }), /AEON_REQUIRE_BACKEND/)
  assert.throws(() => stackTimeout({ AEON_STACK_READY_TIMEOUT_MS: '0' }), /positive number/)
  const ui = uiStackTarget({ AEON_API_URL: 'http://127.0.0.1:9090' }, cwd)
  assert.equal(ui.backendURL, 'http://127.0.0.1:9090/api/health')
  assert.equal(ui.backend, 'optional')
  const e2e = e2eStackTarget({ BASE_URL: 'http://127.0.0.1:8080', AEON_REQUIRE_BACKEND: 'off' })
  assert.equal(e2e.webURL, 'http://127.0.0.1:8080')
  assert.equal(e2e.backendURL, 'http://127.0.0.1:8080/api/health')
  assert.equal(e2e.backend, 'required')
})

test('the CLI reports the bound and succeeds once the URL is healthy', async () => {
  const time = clock()
  const lines = []
  const failed = await main(['http://127.0.0.1:8080/api/health'], {
    stderr: line => lines.push(line),
    timeoutMs: 400,
    intervalMs: 200,
    fetch: async () => { throw refused() },
    sleep: time.sleep,
    now: time.now,
  })
  assert.equal(failed, 1)
  assert.deepEqual(lines, ['Go backend at http://127.0.0.1:8080/api/health did not become ready within 400ms'])
  assert.equal(time.now(), 400)
  const ready = clock()
  let calls = 0
  const ok = await main(['http://127.0.0.1:8080/api/health'], {
    stderr: () => { throw new Error('success is silent') },
    timeoutMs: 1_000,
    fetch: async () => {
      calls++
      if (ready.now() < 200) throw refused()
      return response(200)
    },
    sleep: ready.sleep,
    now: ready.now,
  })
  assert.equal(ok, 0)
  assert.ok(calls >= 2)
  const usage = []
  assert.equal(await main([], { stderr: line => usage.push(line) }), 2)
  assert.match(usage[0], /usage: node scripts\/ci\/stack-ready\.mjs/)
})

test('shard configs keep the waiter in front of specs and leave perf on its own server', () => {
  const ui = readFileSync(new URL('../../web/playwright.ui.config.ts', import.meta.url), 'utf8')
  const e2e = readFileSync(new URL('../../web/playwright.config.ts', import.meta.url), 'utf8')
  const perf = readFileSync(new URL('../../web/playwright.perf.config.ts', import.meta.url), 'utf8')
  const vite = readFileSync(new URL('../../web/vite.config.ts', import.meta.url), 'utf8')
  const hosted = readFileSync(new URL('../ci-static.test.mjs', import.meta.url), 'utf8')
  assert.match(ui, /const derived = 5200 \+ \(parseInt\(createHash\('sha256'\)\.update\(process\.cwd\(\)\)\.digest\('hex'\)\.slice\(0, 4\), 16\) % 700\)/)
  assert.match(ui, /const port = process\.env\.PLAYWRIGHT_PORT \?\? String\(derived\)/)
  assert.match(ui, /testMatch: '\*\*\/\*.spec.ts'/)
  assert.match(ui, /testIgnore: '\*\*\/performance.spec.ts'/)
  assert.ok(ui.indexOf('globalSetup: \'../scripts/ci/ui-stack-setup.mjs\'') > ui.indexOf('...browserPolicy'))
  assert.match(e2e, /globalSetup: '\.\.\/scripts\/ci\/e2e-stack-setup\.mjs'/)
  assert.doesNotMatch(perf, /ui-stack-setup|e2e-stack-setup/)
  assert.match(perf, /command: 'npm run build && npm run preview /)
  assert.match(perf, /reuseExistingServer: false/)
  assert.match(vite, /aeon-test-mode-module-connections/)
  assert.match(vite, /aeon-test-mode-backend-ready/)
  assert.match(vite, /backendProxyBypass/)
  assert.match(vite, /AEON_REQUIRE_BACKEND !== 'required'/)
  assert.match(vite, /proxyReadyCheck\(process\.env/)
  assert.match(hosted, /import '\.\/ci\/stack-ready\.test\.mjs'/)
  assert.match(readFileSync(new URL('./ui-stack-setup.mjs', import.meta.url), 'utf8'), /verifySupervisor/)
  assert.match(readFileSync(new URL('./e2e-stack-setup.mjs', import.meta.url), 'utf8'), /backend: 'required'|e2eStackTarget/)
})

// vue-tsc reads stack-ready.d.mts, not the module. A name vite.config.ts
// imports without a declaration fails web typecheck in CI.
test('the declarations cover every name vite.config.ts imports from the module', async () => {
  const vite = readFileSync(new URL('../../web/vite.config.ts', import.meta.url), 'utf8')
  const declared = readFileSync(new URL('./stack-ready.d.mts', import.meta.url), 'utf8')
  const imported = /import \{([^}]+)\} from '\.\.\/scripts\/ci\/stack-ready\.mjs'/.exec(vite)
  assert.ok(imported, 'vite.config.ts imports the readiness module')
  const names = imported[1].split(',').map(name => name.trim()).filter(Boolean)
  assert.ok(names.length > 0)
  const module = await import('./stack-ready.mjs')
  for (const name of names) {
    assert.equal(typeof module[name], 'function', `${name} is a module export`)
    assert.match(declared, new RegExp(`^export function ${name}\\(`, 'm'), `${name} is declared`)
  }
})

test('a stalled probe is cancelled at the deadline and a late 2xx is not readiness', async () => {
  const time = timerClock()
  const wall = Date.now()
  const aborted = { value: false }
  await assert.rejects(() => waitForHTTP(backendURL, {
    kind: 'backend',
    timeoutMs: 1_000,
    intervalMs: 200,
    fetch: async (_url, init = {}) => {
      init.signal?.addEventListener('abort', () => { aborted.value = true })
      await time.sleep(1_000)
      return response(200)
    },
    sleep: time.sleep,
    now: time.now,
    schedule: time.schedule,
  }), error => {
    assert.equal(error.message, `Go backend at ${backendURL} did not become ready within 1000ms`)
    assert.equal(error.code, 'AEON_NOT_READY')
    return true
  })
  assert.equal(time.now(), 1_000)
  assert.equal(aborted.value, true)
  assert.ok(Date.now() - wall < 1_000)
})

test('a 2xx that arrives on the deadline is not readiness and stays inside the injected clock', async () => {
  const time = clock()
  const wall = Date.now()
  await assert.rejects(() => waitForHTTP(backendURL, {
    kind: 'backend',
    timeoutMs: 1_000,
    fetch: async () => {
      await time.sleep(1_000)
      return response(200)
    },
    sleep: time.sleep,
    now: time.now,
  }), /Go backend at http:\/\/127\.0\.0\.1:8080\/api\/health did not become ready within 1000ms/)
  assert.equal(time.now(), 1_000)
  assert.ok(Date.now() - wall < 1_000)
})

test('a slow 2xx inside the deadline is still readiness', async () => {
  const time = clock()
  const wall = Date.now()
  const result = await waitForHTTP(backendURL, {
    kind: 'backend',
    timeoutMs: 1_000,
    fetch: async () => {
      await time.sleep(400)
      return response(200)
    },
    sleep: time.sleep,
    now: time.now,
  })
  assert.equal(result.status, 200)
  assert.equal(time.now(), 400)
  assert.ok(Date.now() - wall < 1_000)
})

test('an optional probe does not report a late 2xx as up', async () => {
  const time = timerClock()
  const aborted = { value: false }
  await assert.rejects(() => waitForStack({
    webURL, backendURL, backend: 'optional', timeoutMs: 1_000, intervalMs: 200,
    fetch: async (url, init = {}) => {
      if (url !== backendURL) return response(200)
      init.signal?.addEventListener('abort', () => { aborted.value = true })
      await time.sleep(1_000)
      return response(200)
    },
    sleep: time.sleep,
    now: time.now,
    schedule: time.schedule,
  }), error => {
    assert.equal(error.message, `Go backend at ${backendURL} did not become ready within 1000ms`)
    assert.equal(error.code, 'AEON_NOT_READY')
    return true
  })
  assert.equal(aborted.value, true)
  assert.equal(time.now(), 1_000)
})

test('healthIsReady and the proxy check reject a late 2xx and keep an in-budget 2xx', async () => {
  const late = timerClock()
  const aborted = { value: false }
  assert.equal(await healthIsReady(backendURL, async (_url, init = {}) => {
    init.signal?.addEventListener('abort', () => { aborted.value = true })
    await late.sleep(500)
    return response(200)
  }, { timeoutMs: 500, now: late.now, schedule: late.schedule }), false)
  assert.equal(aborted.value, true)
  assert.equal(late.now(), 500)

  const budget = clock()
  assert.equal(await healthIsReady(backendURL, async () => {
    await budget.sleep(400)
    return response(200)
  }, { timeoutMs: 500, now: budget.now }), true)
  assert.equal(budget.now(), 400)

  let calls = 0
  const off = proxyReadyCheck({ AEON_REQUIRE_BACKEND: 'off' }, backendURL, async () => {
    calls++
    return response(200)
  })
  assert.equal(await off(), false)
  assert.equal(calls, 0)

  const stalled = timerClock()
  let proxyAborted = false
  const check = proxyReadyCheck({}, backendURL, async (_url, init = {}) => {
    init.signal?.addEventListener('abort', () => { proxyAborted = true })
    await stalled.sleep(500)
    return response(200)
  }, { timeoutMs: 500, now: stalled.now, schedule: stalled.schedule })
  assert.equal(await check(), false)
  assert.equal(proxyAborted, true)
})

test('CI UI setup waits until the backend is ready and a late success is not ready', async () => {
  const time = clock()
  let spec = false
  let specRefused = 0
  const fetch = async url => {
    const backend = String(url).endsWith('/api/health')
    if (backend && time.now() < 600) {
      if (spec) specRefused++
      throw refused()
    }
    return response(200)
  }
  const result = await runUiStackSetup({
    env: { CI: '1', AEON_STACK_READY_TIMEOUT_MS: '5000' },
    cwd: '/tmp/aeon-1605-port',
    verify: () => {},
    fetch,
    sleep: time.sleep,
    now: time.now,
  })
  spec = true
  const firstSpec = await Promise.all([fetch(webURL), fetch(backendURL)])
  assert.equal(result.backend, 'up')
  assert.equal(result.web, 'up')
  assert.deepEqual(firstSpec.map(item => item.status), [200, 200])
  assert.equal(specRefused, 0)
  assert.ok(time.now() >= 600)
  assert.ok(time.now() < 5_000)

  const stalled = timerClock()
  let aborted = false
  await assert.rejects(() => runUiStackSetup({
    env: { CI: '1', AEON_STACK_READY_TIMEOUT_MS: '1000' },
    verify: () => {},
    fetch: async (url, init = {}) => {
      if (!String(url).endsWith('/api/health')) return response(200)
      init.signal?.addEventListener('abort', () => { aborted = true })
      await stalled.sleep(1_000)
      return response(200)
    },
    sleep: stalled.sleep,
    now: stalled.now,
    schedule: stalled.schedule,
  }), error => {
    assert.equal(error.message, 'Go backend at http://127.0.0.1:8080/api/health did not become ready within 1000ms')
    assert.equal(error.code, 'AEON_NOT_READY')
    return true
  })
  assert.equal(aborted, true)
  assert.equal(stalled.now(), 1_000)
})

test('CI UI setup fails inside the bound when the backend never accepts', async () => {
  const time = clock()
  let specRan = false
  await assert.rejects(async () => {
    await runUiStackSetup({
      env: { CI: '1', AEON_STACK_READY_TIMEOUT_MS: '1000' },
      verify: () => {},
      fetch: async url => {
        if (String(url).endsWith('/api/health')) throw refused()
        return response(200)
      },
      sleep: time.sleep,
      now: time.now,
    })
    specRan = true
  }, error => {
    assert.equal(error.message, 'Go backend at http://127.0.0.1:8080/api/health did not become ready within 1000ms')
    assert.equal(error.code, 'AEON_NOT_READY')
    return true
  })
  assert.equal(specRan, false)
  assert.equal(time.now(), 1_000)
})

test('an approved mock-only job does not dial and CI still requires the backend unless it opts out', async () => {
  let health = 0
  let slept = false
  const result = await runUiStackSetup({
    env: { CI: '1', AEON_REQUIRE_BACKEND: 'off' },
    verify: () => {},
    fetch: async url => {
      if (String(url).endsWith('/api/health')) health++
      return response(200)
    },
    sleep: async () => { slept = true },
    now: () => 0,
  })
  assert.deepEqual(result, { web: 'up', backend: 'off' })
  assert.equal(health, 0)
  assert.equal(slept, false)
  assert.equal(uiStackTarget({ CI: '1' }).backend, 'required')
  assert.equal(uiStackTarget({ CI: 'true' }).backend, 'required')
  assert.equal(uiStackTarget({ CI: '1', AEON_REQUIRE_BACKEND: 'off' }).backend, 'off')
  assert.equal(uiStackTarget({ CI: '0' }).backend, 'optional')
  assert.equal(uiStackTarget({ CI: 'false' }).backend, 'optional')
  assert.equal(uiStackTarget({ CI: '' }).backend, 'optional')
  assert.equal(uiStackTarget({}).backend, 'optional')
})

test('e2e setup waits through closed-port probes and the first spec then sees health', async () => {
  const time = clock()
  let spec = false
  let specRefused = 0
  const base = 'http://127.0.0.1:8080'
  const fetch = async url => {
    const backend = String(url).endsWith('/api/health')
    const up = backend ? time.now() >= 800 : time.now() >= 200
    if (!up) {
      if (spec) specRefused++
      throw refused()
    }
    return response(200)
  }
  const result = await runE2eStackSetup({
    env: { CI: '1', BASE_URL: base, AEON_STACK_READY_TIMEOUT_MS: '5000' },
    verify: () => {},
    fetch,
    sleep: time.sleep,
    now: time.now,
  })
  spec = true
  const first = await fetch(`${base}/api/health`)
  assert.equal(result.backend, 'up')
  assert.equal(result.web, 'up')
  assert.equal(first.status, 200)
  assert.equal(specRefused, 0)
  assert.ok(time.now() >= 800)
  assert.ok(time.now() < 5_000)
})

test('backend-dependent CI jobs wait and mock-only jobs opt out', () => {
  const root = new URL('../../.github/workflows/', import.meta.url)
  const hits = []
  let job = ''
  for (const file of readdirSync(root)) {
    if (!file.endsWith('.yml') && !file.endsWith('.yaml')) continue
    job = ''
    for (const line of readFileSync(new URL(file, root), 'utf8').split('\n')) {
      const header = /^  ([a-z0-9-]+):\s*$/.exec(line)
      if (header) job = header[1]
      if (line.includes('AEON_REQUIRE_BACKEND:')) hits.push([file, job, line.trim()])
    }
  }
  const expected = new Map([
    ...mockOnlyCiJobs.map(([file, name]) => [`${file}:${name}`, 'off']),
    ...backendRequiredCiJobs.map(([file, name]) => [`${file}:${name}`, 'required']),
  ])
  assert.deepEqual(hits.map(([file, name]) => `${file}:${name}`).sort(), [...expected.keys()].sort())
  for (const [file, name, line] of hits) {
    assert.match(line, new RegExp(`AEON_REQUIRE_BACKEND: '${expected.get(`${file}:${name}`)}'`))
  }
  function jobBody(file, name) {
    const text = readFileSync(new URL(file, root), 'utf8')
    const start = text.indexOf(`\n  ${name}:\n`)
    assert.ok(start >= 0, `${file} ${name}`)
    const rest = text.slice(start + 1)
    const next = rest.slice(1).search(/\n  [a-z0-9-]+:\n/)
    return next === -1 ? rest : rest.slice(0, next + 1)
  }
  for (const [file, name] of mockOnlyCiJobs) {
    assert.match(jobBody(file, name), /AEON_REQUIRE_BACKEND: 'off'/, `${file} ${name}`)
  }
  for (const [file, name] of backendRequiredCiJobs) {
    const body = jobBody(file, name)
    assert.match(body, /AEON_REQUIRE_BACKEND: 'required'/, `${file} ${name}`)
    assert.match(body, /paimos serve/)
    assert.match(body, /node scripts\/ci\/stack-ready\.mjs/)
    assert.match(body, /cat "\$RUNNER_TEMP\/aeon\.log"/)
    assert.doesNotMatch(body, /curl -sf/)
  }
})

// Job bodies under jobs:, keyed by job id.
function workflowJobs(text) {
  const jobs = new Map()
  let inJobs = false
  let name = ''
  for (const line of text.split('\n')) {
    if (/^[^\s#]/.test(line)) {
      inJobs = line.startsWith('jobs:')
      name = ''
      continue
    }
    if (!inJobs) continue
    const header = /^  ([A-Za-z0-9_-]+):\s*$/.exec(line)
    if (header) {
      name = header[1]
      jobs.set(name, '')
    } else if (name) {
      jobs.set(name, jobs.get(name) + line + '\n')
    }
  }
  return jobs
}

// The job-level env block. GitHub expressions stay as text.
function jobEnv(body) {
  const env = {}
  let inEnv = false
  for (const line of body.split('\n')) {
    if (/^    \S/.test(line)) inEnv = line.trim() === 'env:'
    else if (inEnv) {
      const entry = /^      ([A-Z0-9_]+):\s*(.*)$/.exec(line)
      if (entry) env[entry[1]] = entry[2].replace(/^'(.*)'$/, '$1')
    }
  }
  return env
}

// Commands that start Playwright specs, and so a stack setup. Unit runs and
// the no-browser safety suite do not.
const browserSuite = /npx playwright test|\bnpm(?: --prefix [^\s]+)? test\b|npm run e2e\b|cli\.mjs run web(?![^\n]*--unit)|ci-preflight-browser\.mjs/

test('every CI job that starts browser specs is in the backend inventory', () => {
  const root = new URL('../../.github/workflows/', import.meta.url)
  const launchers = []
  for (const file of readdirSync(root)) {
    if (!file.endsWith('.yml') && !file.endsWith('.yaml')) continue
    for (const [name, body] of workflowJobs(readFileSync(new URL(file, root), 'utf8'))) {
      if (browserSuite.test(body)) launchers.push(`${file}:${name}`)
    }
  }
  const inventory = [...mockOnlyCiJobs, ...backendRequiredCiJobs].map(([file, name]) => `${file}:${name}`)
  assert.ok(launchers.includes('ci-preflight.yml:browser'))
  assert.ok(launchers.includes('ci.yml:visual-reference-capture'))
  assert.deepEqual(launchers.sort(), inventory.sort())
})

test('the preflight controller hands the workflow exemption to UI setup, which never dials', async () => {
  const workflow = readFileSync(new URL('../../.github/workflows/ci-preflight.yml', import.meta.url), 'utf8')
  const env = jobEnv(workflowJobs(workflow).get('browser'))
  // Hosted runners set CI=true. Nothing else in this process decides the mode.
  const keys = ['CI', 'AEON_REQUIRE_BACKEND', 'AEON_STACK_READY_TIMEOUT_MS', ...Object.keys(env)]
  const saved = keys.map(key => [key, process.env[key]])
  const output = mkdtempSync(join(tmpdir(), 'aeon-1605-preflight-'))
  const sha = 'a'.repeat(40)
  let spawned
  try {
    for (const key of keys) delete process.env[key]
    Object.assign(process.env, { CI: 'true' }, env)
    const status = browserPreflight({
      sha, group: 'browser-1', run: 1, attempt: 1, root: '/unused', output,
      environment: 'github-hosted', runnerClass: 'hosted',
    }, (command, args, options) => {
      if (command === 'git') return { status: 0, stdout: `${sha}\n` }
      spawned = { args, env: options.env }
      return { status: 0 }
    })
    assert.equal(status, 0)
  } finally {
    for (const [key, value] of saved) {
      if (value === undefined) delete process.env[key]
      else process.env[key] = value
    }
    rmSync(output, { recursive: true, force: true })
  }
  assert.deepEqual(spawned.args.slice(0, 3), ['scripts/test-tiers/cli.mjs', 'run', 'web'])
  assert.equal(spawned.args.includes('--unit'), false)
  assert.equal(spawned.env.CI, 'true')

  const time = timerClock()
  let health = 0
  const result = await runUiStackSetup({
    env: spawned.env,
    cwd: '/tmp/aeon-1605-port',
    verify: () => {},
    fetch: async url => {
      if (!String(url).endsWith('/api/health')) return response(200)
      health++
      throw refused()
    },
    sleep: time.sleep,
    now: time.now,
    schedule: time.schedule,
  })
  assert.deepEqual(result, { web: 'up', backend: 'off' })
  assert.equal(health, 0)
  assert.equal(time.now(), 0)
})
