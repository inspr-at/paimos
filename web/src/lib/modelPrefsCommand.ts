// SPDX-License-Identifier: AGPL-3.0-only
import { router } from '../router'
import { modelsSettingsLink } from './modelsSettings'
import type { PrefLevel } from './modelPrefs'
export interface PrefsContext { project?: { id: string; title: string }; level?: PrefLevel; kind?: string; why?: boolean; ticket?: string; preview?: string }
// These entry points now navigate to an ordinary page. No dialog state survives
// independently of the route, and live Agents updates cannot close its editor.
export function openModelPrefs(context: PrefsContext = {}) { return router.push(modelsSettingsLink(context)) }
