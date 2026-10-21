// SPDX-License-Identifier: AGPL-3.0-only
import { defineConfig, type Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'node:path'
import { backendProxyBypass, createBackendProxyGate, healthIsReady, waitForHTTP } from '../scripts/ci/stack-ready.mjs'

type HoldResponse = {
  writableEnded?: boolean
  statusCode: number
  setHeader: (name: string, value: string) => void
  end: (body?: string) => void
}

export default defineConfig(({ mode }) => {
  const target = process.env.AEON_API_URL ?? 'http://127.0.0.1:8080'
  // Vite installs the proxy before configureServer hooks, so a later middleware
  // cannot stop http-proxy from dialing :8080. Test mode holds /api until
  // health is 2xx. Dev mode still proxies straight at the configured target.
  const gate = mode === 'test'
    ? createBackendProxyGate({ check: () => healthIsReady(new URL('/api/health', target).href) })
    : null
  const bypass = gate
    ? (req: { url?: string }, res: HoldResponse) => backendProxyBypass(gate, req, res)
    : undefined
  const entry = (rewrite?: (path: string) => string) => {
    if (!bypass && !rewrite) return target
    return { target, ...(rewrite ? { rewrite } : {}), ...(bypass ? { bypass } : {}) }
  }
  return {
    // The static pre-filter shares dependencies read-only with a disposable tree.
    cacheDir: process.env.VITE_CACHE_DIR,
    plugins: [vue(), testModeModuleConnections(mode), testModeBackendReady(mode)],
    // Never inline assets as data: URLs; the server CSP (default-src 'self') blocks them.
    build: { assetsInlineLimit: 0, rollupOptions: { input: { index: resolve(import.meta.dirname, 'index.html'), 'quote-print': resolve(import.meta.dirname, 'quote-print.html') } } },
    // AEON_API_URL points the dev proxy at another local backend (a branch build on its own port).
    // The two public files are exact. The catalog page stays on the Vite SPA.
    server: {
      proxy: {
        '/api': entry(),
        '^/portal/[^/]+/llms\\.txt$': entry(path => path.replace(/^\/portal\/([^/]+)\/llms\.txt$/, '/api/public/portal/$1/llms.txt')),
        '^/portal/[^/]+/catalog\\.json$': entry(path => path.replace(/^\/portal\/([^/]+)\/catalog\.json$/, '/api/public/portal/$1/catalog.json')),
        '^/portal/[^/]+/roadmap\\.json$': entry(path => path.replace(/^\/portal\/([^/]+)\/roadmap\.json$/, '/api/public/portal/$1/roadmap.json')),
      },
    },
  }
})

// Required UI runs fail in this process, before listen, when health never
// arrives. The default shard leaves the variable unset and does not wait out
// the bound: mocked CI does not start the Go server.
function testModeBackendReady(mode: string): Plugin {
  return {
    name: 'aeon-test-mode-backend-ready',
    apply: 'serve',
    async configureServer() {
      if (mode !== 'test' || process.env.AEON_REQUIRE_BACKEND !== 'required') return
      const backendURL = new URL('/api/health', process.env.AEON_API_URL ?? 'http://127.0.0.1:8080').href
      const timeoutMs = process.env.AEON_STACK_READY_TIMEOUT_MS ? Number(process.env.AEON_STACK_READY_TIMEOUT_MS) : 30_000
      try {
        await waitForHTTP(backendURL, { kind: 'backend', timeoutMs })
      } catch (error) {
        console.error(error instanceof Error ? error.message : String(error))
        process.exit(1)
      }
    },
  }
}

// Playwright's page.route installs Fetch interception for every URL. Chromium
// then reads only six HTTP/1.1 bodies at a time, and Node's default
// Keep-Alive: timeout=5 holds those sockets until the idle timer. The rest of
// a module burst finishes about 5s later, which is past the panel wait in
// access.spec. Test mode (the Playwright dev server) closes each response so
// the socket is free as soon as the body is written. The HMR upgrade is left
// alone. Dev mode keeps keep-alive.
function testModeModuleConnections(mode: string): Plugin {
  return {
    name: 'aeon-test-mode-module-connections',
    apply: 'serve',
    configureServer(server) {
      if (mode !== 'test') return
      server.middlewares.use((req, res, next) => {
        if (!req.headers.upgrade) res.setHeader('Connection', 'close')
        next()
      })
    },
  }
}
