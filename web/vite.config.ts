// SPDX-License-Identifier: AGPL-3.0-only
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'node:path'

// AEON_API_URL points the dev proxy at another local backend (a branch build on its own port).
// The two public files are exact. The catalog page stays on the Vite SPA.
// Preview uses the same proxy so a test build behaves like the dev server for unmocked /api calls.
const api = process.env.AEON_API_URL ?? 'http://127.0.0.1:8080'
const proxy = {
  '/api': api,
  '^/portal/[^/]+/llms\\.txt$': {
    target: api,
    rewrite: (path: string) => path.replace(/^\/portal\/([^/]+)\/llms\.txt$/, '/api/public/portal/$1/llms.txt'),
  },
  '^/portal/[^/]+/catalog\\.json$': {
    target: api,
    rewrite: (path: string) => path.replace(/^\/portal\/([^/]+)\/catalog\.json$/, '/api/public/portal/$1/catalog.json'),
  },
}

export default defineConfig({
  plugins: [vue()],
  // Never inline assets as data: URLs; the server CSP (default-src 'self') blocks them.
  build: { assetsInlineLimit: 0, rollupOptions: { input: { index: resolve(import.meta.dirname, 'index.html'), 'quote-print': resolve(import.meta.dirname, 'quote-print.html') } } },
  server: { proxy },
  preview: { proxy },
})
