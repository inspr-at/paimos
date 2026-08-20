import { existsSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { createApp, h, nextTick } from 'vue'

import { DEFAULT_BRAND_LOGO } from '@/composables/brandingAssets'
import BrandLogo from './BrandLogo.vue'

describe('BrandLogo', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('uses the canonical fallback path and the public asset exists', () => {
    expect(DEFAULT_BRAND_LOGO).toBe('/logo.svg')
    expect(existsSync(resolve(process.cwd(), `public${DEFAULT_BRAND_LOGO}`))).toBe(true)
  })

  it('falls back once on image error and never loops if the fallback also fails', async () => {
    document.body.innerHTML = '<div id="root"></div>'
    const app = createApp({ render: () => h(BrandLogo, { src: '/configured-but-missing.svg', alt: 'Acme' }) })
    app.mount('#root')

    const configured = document.querySelector<HTMLImageElement>('img')!
    expect(configured.getAttribute('src')).toBe('/configured-but-missing.svg')
    configured.dispatchEvent(new Event('error'))
    await nextTick()

    const fallback = document.querySelector<HTMLImageElement>('img')!
    expect(fallback.getAttribute('src')).toBe('/logo.svg')
    expect(fallback.dataset.logoFallback).toBe('true')
    fallback.dispatchEvent(new Event('error'))
    await nextTick()
    expect(document.querySelector('img')).toBeNull()
    app.unmount()
  })
})
