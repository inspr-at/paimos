// SPDX-License-Identifier: AGPL-3.0-only
import { defineConfig, type Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'node:path'

export default defineConfig(({ mode }) => ({
  // The static pre-filter shares dependencies read-only with a disposable tree.
  cacheDir: process.env.VITE_CACHE_DIR,
  plugins: [vue(), testModeModuleConnections(mode)],
  // Never inline assets as data: URLs; the server CSP (default-src 'self') blocks them.
  build: { assetsInlineLimit: 0, rollupOptions: { input: { index: resolve(import.meta.dirname, 'index.html'), 'quote-print': resolve(import.meta.dirname, 'quote-print.html') } } },
  // AEON_API_URL points the dev proxy at another local backend (a branch build on its own port).
  // The two public files are exact. The catalog page stays on the Vite SPA.
  server: {
    proxy: {
      '/api': process.env.AEON_API_URL ?? 'http://127.0.0.1:8080',
      '^/portal/[^/]+/llms\\.txt$': {
        target: process.env.AEON_API_URL ?? 'http://127.0.0.1:8080',
        rewrite: path => path.replace(/^\/portal\/([^/]+)\/llms\.txt$/, '/api/public/portal/$1/llms.txt'),
      },
      '^/portal/[^/]+/catalog\\.json$': {
        target: process.env.AEON_API_URL ?? 'http://127.0.0.1:8080',
        rewrite: path => path.replace(/^\/portal\/([^/]+)\/catalog\.json$/, '/api/public/portal/$1/catalog.json'),
      },
      '^/portal/[^/]+/roadmap\\.json$': {
        target: process.env.AEON_API_URL ?? 'http://127.0.0.1:8080',
        rewrite: path => path.replace(/^\/portal\/([^/]+)\/roadmap\.json$/, '/api/public/portal/$1/roadmap.json'),
      },
    },
  },
}))

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
