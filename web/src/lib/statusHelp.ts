// SPDX-License-Identifier: AGPL-3.0-only
import { shallowRef } from 'vue'

// One app-level sheet outlives the menu which opened it. Its opener is the
// original status control, so closing restores focus after the menu unmounts.
export const statusHelpRequest = shallowRef<{ projectId?: string; opener: HTMLElement | null } | null>(null)
export function openStatusHelp(projectId: string | undefined, opener: HTMLElement | null) { statusHelpRequest.value = { projectId, opener } }
