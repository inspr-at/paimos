// SPDX-License-Identifier: AGPL-3.0-only
import { reactive } from 'vue'
import type { BenefitText } from './doneGate'

export interface DoneGateRequest {
  key: string
  title: string
  state: string
  fields: Record<string, unknown>
  index: number
  total: number
}

export const doneGateState = reactive<{
  request: DoneGateRequest | null
  resolve: ((text: BenefitText | null) => void) | null
}>({ request: null, resolve: null })

// A newer ask resolves the previous promise with null without settling, so the
// old caller cannot clear the request that just replaced it.
export function askDoneGate(
  request: Omit<DoneGateRequest, 'index' | 'total'>,
  progress: { index?: number; total?: number } = {},
): Promise<BenefitText | null> {
  const previous = doneGateState.resolve
  doneGateState.resolve = null
  previous?.(null)
  return new Promise(resolve => {
    doneGateState.resolve = resolve
    doneGateState.request = {
      key: request.key,
      title: request.title,
      state: request.state,
      fields: request.fields ?? {},
      index: progress.index ?? 1,
      total: progress.total ?? 1,
    }
  })
}

export function settleDoneGate(text: BenefitText | null) {
  const resolve = doneGateState.resolve
  doneGateState.request = null
  doneGateState.resolve = null
  resolve?.(text)
}
