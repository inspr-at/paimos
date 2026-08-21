/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public
 * License along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'
import { readFileSync } from 'node:fs'
import { execSync } from 'node:child_process'
import { resolve } from 'node:path'

const frontendDir = fileURLToPath(new URL('.', import.meta.url))
const version = readFileSync(resolve(frontendDir, '../VERSION'), 'utf-8').trim()
let gitHash = ''
try { gitHash = execSync('git rev-parse --short HEAD', { encoding: 'utf-8' }).trim() } catch { /* not in git */ }

export default defineConfig({
  plugins: [vue()],
  define: {
    __APP_VERSION__: JSON.stringify(version),
    __GIT_HASH__: JSON.stringify(gitHash),
  },
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
      '@docs': fileURLToPath(new URL('../docs', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8888',
        changeOrigin: true,
        // changeOrigin rewrites Host but NOT Origin, so cookie-auth
        // mutations from the dev SPA trip the backend's same-origin CSRF
        // guard (Origin localhost:5173 vs Host localhost:8888). Align
        // Origin with the proxied Host — matches the single-host prod
        // deployment the guard is designed for. (PAI-709)
        configure(proxy) {
          proxy.on('proxyReq', (proxyReq, req) => {
            if (req.headers.origin) proxyReq.setHeader('origin', 'http://localhost:8888')
          })
        },
      },
    },
  },
  build: {
    outDir: 'dist',
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes('node_modules')) return
          if (id.includes('/vue/') || id.includes('/pinia/') || id.includes('/vue-router/') || id.includes('/vue-i18n/')) {
            return 'vue-core'
          }
          if (id.includes('/lucide-vue-next/')) return 'icons'
          if (id.includes('/marked/') || id.includes('/dompurify/')) return 'content-rendering'
        },
      },
    },
  },
})
