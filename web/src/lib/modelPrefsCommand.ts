// SPDX-License-Identifier: AGPL-3.0-only
import { shallowRef } from 'vue'
import type { PrefLevel } from './modelPrefs'
let nextRequest = 0
export interface PrefsContext { requestId?: number; project?: { id: string; title: string }; level?: PrefLevel; kind?: string; why?: boolean; preview?: string }
export const modelPrefsContext = shallowRef<PrefsContext | null>(null)
export function openModelPrefs(context: PrefsContext = {}) { modelPrefsContext.value = { ...context, requestId: ++nextRequest } }
export function closeModelPrefs() { modelPrefsContext.value = null }
