// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { createHash } from 'node:crypto'
import { createServer } from 'node:http'
import { smoke } from '../../scripts/verify-live-smoke.mjs'

// Exercise the production smoke driver against an isolated HTTP fixture. No
// credentials, operator browser, production traffic or datastore is involved.
for (const scenario of ['healthy', 'background write', 'script error', 'wrong bundle']) {
  test(`release smoke: ${scenario}`, async () => {
    let mutations = 0
    const bundle = `
      document.querySelector('#app').innerHTML = '<h1 id="signin-title">Sign in</h1><a class="login-button" href="/api/auth/login">Sign in</a>';
      ${scenario === 'background write' ? "fetch('/api/nodes', { method: 'POST' }).catch(() => {});" : ''}
      ${scenario === 'script error' ? "throw new Error('fixture failure');" : ''}
    `
    const version = '261001130110.0.0'
    const server = createServer((request, response) => {
      if (!['GET', 'HEAD'].includes(request.method ?? '')) { mutations++; response.writeHead(405).end(); return }
      if (request.url === '/api/version') {
        response.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ version, scheme: 'inspr-calver-3' })); return
      }
      if (request.url === '/assets/index-fixture.js') {
        response.writeHead(200, { 'Content-Type': 'text/javascript' }).end(bundle); return
      }
      response.writeHead(200, { 'Content-Type': 'text/html' }).end('<!doctype html><div id="app"></div><script type="module" src="/assets/index-fixture.js"></script>')
    })
    await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve))
    const address = server.address()
    if (!address || typeof address === 'string') throw new Error('fixture address missing')
    const base = `http://127.0.0.1:${address.port}`
    const env = { LIVE_VERSION: version, LIVE_SCHEME: 'inspr-calver-3', LIVE_BUNDLE: '/assets/index-fixture.js',
      LIVE_BUNDLE_SHA256: scenario === 'wrong bundle' ? '0'.repeat(64) : createHash('sha256').update(bundle).digest('hex') }
    try {
      if (scenario === 'healthy') await smoke(env, base)
      else await expect(smoke(env, base)).rejects.toThrow()
      expect(mutations).toBe(0)
    } finally {
      await new Promise<void>(resolve => server.close(() => resolve()))
    }
  })
}
