/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 */

/**
 * Native public base path injected by the server-owned index bootstrap.
 * Empty means standalone origin-root. Never derived from request headers.
 */
export function publicBasePath(): string {
  if (typeof window === 'undefined') return ''
  const raw = window.__PAIMOS_PUBLIC_BASE_PATH__
  return typeof raw === 'string' ? raw : ''
}

export function isSharedOrigin(): boolean {
  return publicBasePath() !== ''
}

/** Join a same-origin absolute path with the configured prefix. */
export function publicURL(path: string): string {
  if (!path.startsWith('/') || path.startsWith('//')) return path
  const base = publicBasePath()
  if (!base) return path
  if (path === base || path.startsWith(`${base}/`)) return path
  return `${base}${path}`
}

export function apiURL(path = ''): string {
  if (!path) return publicURL('/api')
  return publicURL(path.startsWith('/api') ? path : `/api${path.startsWith('/') ? path : `/${path}`}`)
}

export function routerHistoryBase(): string {
  const base = publicBasePath()
  return base ? `${base}/` : '/'
}

/** Strip the configured prefix so Vue Router and stored redirects stay app-relative. */
export function stripPublicBase(path: string): string {
  if (!path.startsWith('/') || path.startsWith('//')) return path
  const base = publicBasePath()
  if (!base) return path
  if (path === base) return '/'
  if (path.startsWith(`${base}/`)) return path.slice(base.length) || '/'
  return path
}

/** Prefix stored root-absolute same-origin URLs in rendered HTML. */
export function rewriteRootAbsoluteURLs(html: string): string {
  const base = publicBasePath()
  if (!base) return html
  return html.replace(
    /\b(src|href)="(\/(?:api|brand|assets)\/[^"]*)"/g,
    (_m, attr: string, path: string) => `${attr}="${base}${path}"`,
  )
}
