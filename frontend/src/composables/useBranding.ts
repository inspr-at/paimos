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

/**
 * useBranding — reactive branding config loaded from the backend.
 *
 * Fetches /api/branding (or a user-selected file via localStorage)
 * and exposes the config as reactive refs. Applies CSS custom properties
 * and updates document title/favicon on load.
 *
 * Singleton: all consumers share the same reactive state.
 */

import { ref, readonly, computed } from 'vue'
import {
  LS_BRANDING_FILE,
  LS_TYPE_COLOR_EPIC,
  LS_TYPE_COLOR_TICKET,
  LS_TYPE_COLOR_TASK,
  LS_TABLE_ROW_BORDER_COLOR,
  LS_TABLE_ROW_STRIPE_COLOR,
  LS_ACCRUALS_ACCENT,
  LS_SIDEBAR_BG_COLOR,
  LS_SIDEBAR_PATTERN_COLOR,
} from '@/constants/storage'
import { applyTypeColorsToDOM } from './useTypeColors'
import { applyTableAppearanceToDOM } from './useTableAppearance'
import {
  isBackgroundPattern,
  type BackgroundPattern,
} from './backgroundPattern'

export interface BrandingConfig {
  name: string
  company: string
  product: string
  tagline: string
  website: string
  logo: string
  favicon: string
  backgroundPattern: BackgroundPattern
  colors: {
    primary: string
    primaryDark: string
    primaryLight: string
    primaryPale: string
    accent: string
    sidebarBg: string
    sidebarText: string
    loginBg: string
    loginPattern: string
    typeEpic: string
    typeTicket: string
    typeTask: string
    tableRowBorder: string
    tableRowAlt: string
    accrualsAccent?: string
  }
  pageTitle: string
  /** Legal-identity lines printed as the "Auftragnehmer" box on report PDFs (PAI-686). */
  contractor?: string[]
}

const LS_KEY = LS_BRANDING_FILE

// PAI-737: defaults are deliberately achromatic. Operators can still replace
// every color through branding.json; the fallback itself carries no legacy
// product identity.
const defaults: BrandingConfig = {
  name: 'PAIMOS',
  company: 'PAIMOS',
  product: 'PAIMOS',
  tagline: 'Project Management Online',
  website: 'https://paimos.com',
  logo: '/logo.png',
  favicon: '/favicon.png',
  backgroundPattern: 'triangle',
  colors: {
    primary: '#52525b',
    primaryDark: '#3f3f46',
    primaryLight: '#a1a1aa',
    primaryPale: '#f4f4f5',
    accent: '#16a34a',
    sidebarBg: '#18181b',
    sidebarText: '#e4e4e7',
    loginBg: '#18181b',
    loginPattern: '#27272a',
    typeEpic: '#5e35b1',
    typeTicket: '#3f3f46',
    typeTask: '#2e7d32',
    tableRowBorder: '#e8eaed',
    tableRowAlt: '#f8f9fa',
    accrualsAccent: '#006497',
  },
  pageTitle: 'PAIMOS',
  contractor: [],
}

const branding = ref<BrandingConfig>({ ...defaults })
const loaded = ref(false)

// Mix #rrggbb with white at given alpha → rgba() string
function hexWithAlpha(hex: string, alpha: number): string {
  const m = /^#([0-9a-f]{6})$/i.exec(hex)
  if (!m) return hex
  const n = parseInt(m[1], 16)
  return `rgba(${(n>>16)&255},${(n>>8)&255},${n&255},${alpha})`
}
// Darken/lighten a hex color by `pct` percent (-100..100)
function shadeHex(hex: string, pct: number): string {
  const m = /^#([0-9a-f]{6})$/i.exec(hex)
  if (!m) return hex
  const n = parseInt(m[1], 16)
  const r = (n>>16)&255, g = (n>>8)&255, b = n&255
  const f = (c: number) => Math.max(0, Math.min(255, Math.round(c + (pct/100) * (pct < 0 ? c : 255 - c))))
  return '#' + [f(r), f(g), f(b)].map(c => c.toString(16).padStart(2,'0')).join('')
}

function applyToDOM(cfg: BrandingConfig) {
  const root = document.documentElement.style
  root.setProperty('--brand-blue', cfg.colors.primary)
  root.setProperty('--brand-blue-dark', cfg.colors.primaryDark)
  root.setProperty('--brand-blue-light', cfg.colors.primaryLight)
  root.setProperty('--brand-blue-pale', cfg.colors.primaryPale)
  root.setProperty('--brand-green', cfg.colors.accent)
  root.setProperty('--sidebar-text', cfg.colors.sidebarText)
  // Type colors and table-row colors are owned by their respective composables
  // (useTypeColors, useTableAppearance). useBranding only supplies the defaults
  // they fall back to when no user override is set.
  applyTypeColorsToDOM(cfg.colors)
  applyTableAppearanceToDOM(cfg.colors)
  // Accruals accent: localStorage override → branding default → fallback
  const accAccent = localStorage.getItem(LS_ACCRUALS_ACCENT) || cfg.colors.accrualsAccent || '#006497'
  root.setProperty('--accruals-accent', accAccent)
  root.setProperty('--accruals-accent-soft', hexWithAlpha(accAccent, 0.10))
  root.setProperty('--accruals-accent-dark', shadeHex(accAccent, -25))

  document.title = cfg.pageTitle

  // Update favicon
  const faviconEl = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (faviconEl) faviconEl.href = cfg.favicon
  const touchEl = document.querySelector<HTMLLinkElement>('link[rel="apple-touch-icon"]')
  if (touchEl) touchEl.href = cfg.logo
}

let initPromise: Promise<void> | null = null

// Shared fetch+apply used by both init (first paint) and refresh (after
// admin edits in the Branding settings tab). Keeps the merge rules for
// `defaults` in one place.
async function fetchAndApply(): Promise<void> {
  try {
    const file = localStorage.getItem(LS_KEY) || ''
    const url = file ? `/api/branding?file=${encodeURIComponent(file)}` : '/api/branding'
    const resp = await fetch(url, { cache: 'no-store' })
    if (resp.ok) {
      const data = await resp.json()
      const merged = { ...defaults, ...data, colors: { ...defaults.colors, ...data.colors } }
      merged.backgroundPattern = isBackgroundPattern(data.backgroundPattern)
        ? data.backgroundPattern
        : defaults.backgroundPattern
      // PAI-736: `name` and `product` are one identity that the server
      // derives from a single BRAND_PRODUCT_NAME. A hand-written
      // branding.json often sets only one of them, and the shallow
      // merge above would then fill the other from `defaults` — so a
      // PMA instance specifying just `product` still rendered "PAIMOS"
      // wherever the other field is read (sidebar brand, login title).
      // Reconcile the pair to whatever the document actually declared.
      const declaredName = typeof data.name === 'string' ? data.name.trim() : ''
      const declaredProduct = typeof data.product === 'string' ? data.product.trim() : ''
      if (declaredName && !declaredProduct) merged.product = declaredName
      if (declaredProduct && !declaredName) merged.name = declaredProduct
      branding.value = merged
    }
  } catch { /* use defaults */ }
  applyToDOM(branding.value)
  loaded.value = true
}

async function init() {
  if (initPromise) return initPromise
  initPromise = fetchAndApply()
  return initPromise
}

// refresh: re-fetch the branding document and re-apply to the DOM. Used by
// the admin Branding tab after a successful PUT so edits show up live
// without a page reload. Intentionally bypasses the init singleton so it
// always re-reads from the server.
async function refresh(): Promise<void> {
  await fetchAndApply()
}

// PAI-736: what this instance calls itself, for chrome that needs a
// short label (the sidebar brand). The sidebar used to hardcode
// "Project Management", so every white-labelled instance advertised the
// wrong name on every page. `name` is the branding document's own
// identity and the field operators set; `product` covers documents
// carrying only BRAND_PRODUCT_NAME. The server derives both from
// BRAND_PRODUCT_NAME, so the order matters only for partially-specified
// branding.json files, where a stale default must never win.
const brandName = computed(
  () => branding.value.name?.trim() || branding.value.product?.trim() || defaults.name,
)

export function useBranding() {
  return {
    branding: readonly(branding),
    brandName,
    loaded: readonly(loaded),
    init,
    refresh,
    /** Set a branding file preference and reload the page */
    switchBranding(file: string | null) {
      if (file) {
        localStorage.setItem(LS_KEY, file)
      } else {
        localStorage.removeItem(LS_KEY)
      }
      // Clear sidebar and type color overrides so the new brand's defaults apply
      localStorage.removeItem(LS_SIDEBAR_BG_COLOR)
      localStorage.removeItem(LS_SIDEBAR_PATTERN_COLOR)
      localStorage.removeItem(LS_TYPE_COLOR_EPIC)
      localStorage.removeItem(LS_TYPE_COLOR_TICKET)
      localStorage.removeItem(LS_TYPE_COLOR_TASK)
      localStorage.removeItem(LS_TABLE_ROW_BORDER_COLOR)
      localStorage.removeItem(LS_TABLE_ROW_STRIPE_COLOR)
      localStorage.removeItem(LS_ACCRUALS_ACCENT)
      window.location.reload()
    },
    /** Currently selected branding file (from localStorage) */
    selectedFile(): string {
      return localStorage.getItem(LS_KEY) || 'branding.json'
    },
  }
}
