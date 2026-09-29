// SPDX-License-Identifier: AGPL-3.0-only
import { shallowRef } from 'vue'

// The open project publishes its journey pill here. The footer reads it, so the
// pill stays in the shell (it does not scroll with the page) and only exists on
// a project route.
export interface FlowPillContext {
  projectId: string
  active: boolean
  open: () => void
}

export const flowPillContext = shallowRef<FlowPillContext | null>(null)
