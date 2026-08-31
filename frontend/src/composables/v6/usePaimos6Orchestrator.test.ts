import { effectScope, nextTick, ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'

import {
  usePaimos6Orchestrator,
  type Paimos6OrchestratorLoader,
} from './usePaimos6Orchestrator'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (cause: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

const configured = {
  revision: 3,
  displayLabel: 'Amy',
  updatedAt: '2026-08-31T12:00:00.000Z',
}
const unset = { revision: 4, displayLabel: null, updatedAt: null }

describe('usePaimos6Orchestrator authority fencing (PAI-865)', () => {
  it('synchronously clears a stale label across authority and project transitions', async () => {
    const first = deferred<typeof configured>()
    const second = deferred<typeof unset>()
    const third = deferred<typeof unset>()
    const loader = vi.fn<Paimos6OrchestratorLoader>()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
      .mockReturnValueOnce(third.promise)
    const principalId = ref<number | null>(1)
    const authorityKey = ref('ppm:user-1:permission-1')
    const projectKey = ref<string | null>('PAI')
    const scope = effectScope()
    const subject = scope.run(() => usePaimos6Orchestrator({
      principalId,
      authorityKey,
      projectKey,
      loader,
    }))!

    first.resolve(configured)
    await nextTick()
    expect(subject.identityLabel.value).toBe('Amy')

    projectKey.value = 'NEXT'
    expect(subject.identityLabel.value).toBe('Paimos')
    expect(subject.statusText.value).toBe('orchestrator loading')
    expect(loader.mock.calls[0]?.[0]?.aborted).toBe(true)

    authorityKey.value = 'pma:user-1:permission-2'
    second.resolve(unset)
    await nextTick()
    expect(subject.identityLabel.value).toBe('Paimos')
    expect(subject.statusText.value).toBe('orchestrator loading')

    third.resolve(unset)
    await nextTick()
    expect(subject.identityLabel.value).toBe('Paimos')
    expect(subject.statusText.value).toBe('orchestrator not configured')
    expect(subject.projection.value?.revision).toBe(4)
    scope.stop()
  })

  it('never inherits configured identity in a fresh unset scope and fails neutral', async () => {
    const ppmScope = effectScope()
    const ppm = ppmScope.run(() => usePaimos6Orchestrator({
      principalId: ref<number | null>(1),
      authorityKey: ref('ppm:configured-authority'),
      projectKey: ref<string | null>('PAI'),
      loader: vi.fn<Paimos6OrchestratorLoader>().mockResolvedValue(configured),
    }))!
    await nextTick()
    expect(ppm.identityLabel.value).toBe('Amy')
    ppmScope.stop()

    const principalId = ref<number | null>(1)
    const authorityKey = ref('pma:fresh-authority')
    const projectKey = ref<string | null>(null)
    const unsetLoader = vi.fn<Paimos6OrchestratorLoader>().mockResolvedValue(unset)
    const scope = effectScope()
    const subject = scope.run(() => usePaimos6Orchestrator({
      principalId,
      authorityKey,
      projectKey,
      loader: unsetLoader,
    }))!

    expect(subject.identityLabel.value).toBe('Paimos')
    await nextTick()
    expect(subject.identityLabel.value).toBe('Paimos')
    expect(subject.statusText.value).toBe('orchestrator not configured')

    const unavailable = deferred<typeof unset>()
    unsetLoader.mockReturnValueOnce(unavailable.promise)
    subject.reload()
    expect(subject.identityLabel.value).toBe('Paimos')
    unavailable.reject(new Error('offline'))
    await vi.waitFor(() => expect(subject.statusText.value).toBe('orchestrator unavailable'))
    expect(subject.identityLabel.value).toBe('Paimos')

    principalId.value = null
    expect(subject.identityLabel.value).toBe('Paimos')
    expect(subject.statusText.value).toBe('orchestrator unavailable')
    scope.stop()
  })
})
