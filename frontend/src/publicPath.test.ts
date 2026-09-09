/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 */

import { afterEach, describe, expect, it } from 'vitest'
import {
  apiURL,
  isSharedOrigin,
  publicBasePath,
  publicURL,
  rewriteRootAbsoluteURLs,
  routerHistoryBase,
  stripPublicBase,
} from './publicPath'

afterEach(() => {
  window.__PAIMOS_PUBLIC_BASE_PATH__ = ''
})

describe('publicPath', () => {
  it('defaults to standalone root', () => {
    delete window.__PAIMOS_PUBLIC_BASE_PATH__
    expect(publicBasePath()).toBe('')
    expect(isSharedOrigin()).toBe(false)
    expect(publicURL('/api/health')).toBe('/api/health')
    expect(apiURL('/auth/oidc/login')).toBe('/api/auth/oidc/login')
    expect(routerHistoryBase()).toBe('/')
    expect(stripPublicBase('/projects/6')).toBe('/projects/6')
  })

  it('joins the configured native prefix exactly once', () => {
    window.__PAIMOS_PUBLIC_BASE_PATH__ = '/paimos'
    expect(publicURL('/api/health')).toBe('/paimos/api/health')
    expect(publicURL('/paimos/api/health')).toBe('/paimos/api/health')
    expect(publicURL('/brand/logo.svg')).toBe('/paimos/brand/logo.svg')
    expect(publicURL('//evil.example')).toBe('//evil.example')
    expect(apiURL('/intake/sessions/1/stream')).toBe('/paimos/api/intake/sessions/1/stream')
    expect(routerHistoryBase()).toBe('/paimos/')
    expect(stripPublicBase('/paimos/projects/6?tab=overview')).toBe('/projects/6?tab=overview')
    expect(stripPublicBase('/paimos')).toBe('/')
  })

  it('rewrites stored markdown attachment URLs only when prefixed', () => {
    const html = '<img src="/api/attachments/42" alt="x">'
    expect(rewriteRootAbsoluteURLs(html)).toBe(html)
    window.__PAIMOS_PUBLIC_BASE_PATH__ = '/paimos'
    expect(rewriteRootAbsoluteURLs(html)).toBe('<img src="/paimos/api/attachments/42" alt="x">')
  })
})
