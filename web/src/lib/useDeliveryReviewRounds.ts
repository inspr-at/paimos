// SPDX-License-Identifier: AGPL-3.0-only
import { onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { onAccessRevalidate } from './authz'
import type { WindowDays } from './deliveryNumbers'
import { readReviewRounds, type ReviewRoundNumbers } from './deliveryReviewRounds'
import { useIdentityScope } from './useIdentityScope'
import { usePoller } from './usePolledData'

export function useDeliveryReviewRounds(options: { projectId: () => string; window: () => WindowDays; active: () => boolean }) {
  const scope = useIdentityScope(), reads = scope.lane()
  const data = shallowRef<ReviewRoundNumbers | null>(null)
  const state = ref<'loading' | 'error' | 'ready'>('loading')
  const keyOf = () => `${scope.owner.value}|${options.projectId()}|${options.window()}`
  function load() {
    if (!options.active()) return Promise.resolve()
    const key = keyOf()
    return reads.run(({ after, signal }) => after(readReviewRounds(options.projectId(), options.window(), signal), answer => {
      if (key !== keyOf()) return
      data.value = answer; state.value = 'ready'
    }), { failed: () => { if (key === keyOf()) { data.value = null; state.value = 'error' } } })
  }
  function reset() { reads.cancel(); data.value = null; state.value = 'loading' }
  watch(() => [options.projectId(), scope.owner.value, options.window()], reset, { flush: 'sync' })
  watch(() => [keyOf(), options.active()], () => { if (options.active()) void load(); else reset() }, { immediate: true })
  onBeforeUnmount(onAccessRevalidate({ reset: () => { reset(); if (options.active()) void load() } }))
  const poller = usePoller(load, 60_000, { enabled: options.active, invalidate: () => reads.cancel() })
  onMounted(() => poller.start(false))
  onBeforeUnmount(() => { poller.stop(); reads.cancel() })
  return { data, state, retry: () => { reset(); void load() } }
}
