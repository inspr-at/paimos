// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import {
  backendMode,
  backendProxyBypass,
  createBackendProxyGate,
  e2eStackTarget,
  healthIsReady,
  holdBackendResponse,
  isConnectionRefused,
  main,
  stackTimeout,
  uiDevPort,
  uiStackTarget,
  waitForStack,
} from './stack-ready.mjs'
import { runE2eStackSetup } from './e2e-stack-setup.mjs'
import { runUiStackSetup } from './ui-stack-setup.mjs'

const webURL = 'http://127.0.0.1:5200'
const backendURL = 'http://127.0.0.1:8080/api/health'

function clock() {
  let t = 0
  return { now: () => t, sleep: async ms => { t += ms } }
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
  assert.match(hosted, /import '\.\/ci\/stack-ready\.test\.mjs'/)
  assert.match(readFileSync(new URL('./ui-stack-setup.mjs', import.meta.url), 'utf8'), /verifySupervisor/)
  assert.match(readFileSync(new URL('./e2e-stack-setup.mjs', import.meta.url), 'utf8'), /backend: 'required'|e2eStackTarget/)
})
