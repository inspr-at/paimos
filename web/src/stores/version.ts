// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, type Version } from '../lib/api'
import { setBrand } from '../lib/brand'
import { rememberCodename } from '../lib/codenames'

export const useVersion = defineStore('version', () => {
  const value = ref<Version | null>(null)
  const failed = ref(false)
  let request: Promise<void> | undefined
  function load() {
    return request ??= (async () => {
      try {
        const response = await api('/version')
        if (!response.ok) throw new Error('Version unavailable')
        const body = await response.json()
        if (typeof body.version !== 'string' || typeof body.scheme !== 'string') throw new Error('Invalid version')
        value.value = body
        rememberCodename(body.version, body.codename)
        setBrand(body.brand)
      } catch { failed.value = true }
    })()
  }
  return { value, failed, load }
})
