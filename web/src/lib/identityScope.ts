// SPDX-License-Identifier: AGPL-3.0-only

// The one rule for every async path that belongs to a signed-in person: an answer
// is used only while the person, the workspace and the generation it was asked in
// are still the current ones. A run captures them when it starts and checks them
// again after EVERY await; once any of them moved on, the continuation is dropped
// and nothing of it ever reaches the screen (AEON-440: one person's attach code
// opened for the next person because a continuation only checked before its await).
//
// Components never await a bare promise in an attach path. They run the path as
//
//   await scope.run(async ({ step, signal }) => {
//     const answer = await step(ask(signal))      // resumes only while still current
//     show(answer)                                // so this is never reached when stale
//   }, { failed: error => …, settled: () => … })  // called only while still current
//
// The core has no Vue in it: `owner` names who the scope belongs to ('' = nobody, so
// nothing starts), and `reset()` moves the generation on (a person or workspace
// change, sign-out, a permission loss, unmount), which also aborts every signal.

/** Who an identity is for the scope: the workspace and the person together, '' when nobody is signed in. */
export const scopeOwner = (who: { tenant: { id: string }; principal: { id: string } } | null | undefined): string => who ? `${who.tenant.id}/${who.principal.id}` : ''

export class StaleScopeError extends Error {
  constructor() { super('The person or workspace changed.'); this.name = 'StaleScopeError' }
}

/** Awaits a promise (or value) and throws StaleScopeError instead of resuming when the scope moved on meanwhile. */
export type Step = <V>(pending: PromiseLike<V> | V) => Promise<V>
export interface Run { step: Step; signal: AbortSignal }
export interface Handlers {
  /** The run threw. Called only while the run is still current; without it the error is thrown to the caller. */
  failed?: (error: unknown) => void
  /** The run ended, with or without an error. Called only while the run is still current. */
  settled?: () => void
}
export interface Lane {
  /** Starts a run that replaces the previous one of this lane: the earlier run is aborted and dropped. */
  run<T>(work: (run: Run) => Promise<T>, handlers?: Handlers): Promise<T | undefined>
  /** Aborts and drops the lane's current run, if any. */
  cancel(): void
}
export interface Scope {
  /** A run on its own: it is never replaced, only dropped when the scope moves on. */
  run: Lane['run']
  /** A sequence of runs where only the newest may answer (a poll, a lookup, a decision). */
  lane(): Lane
  /** Moves the generation on: every run in flight is aborted and dropped. */
  reset(): void
  /** Like reset, and nothing starts afterwards. */
  dispose(): void
}

export function createScope(owner: () => string): Scope {
  let generation = 0
  let disposed = false
  const controllers = new Set<AbortController>()

  function execute<T>(slot: { current?: AbortController } | undefined, work: (run: Run) => Promise<T>, handlers: Handlers): Promise<T | undefined> {
    const started = { generation, owner: owner() }
    if (disposed || !started.owner) return Promise.resolve(undefined)
    slot?.current?.abort()
    const controller = new AbortController()
    controllers.add(controller)
    if (slot) slot.current = controller
    const live = () => !disposed && !controller.signal.aborted && generation === started.generation && owner() === started.owner
    const step: Step = async <V>(pending: PromiseLike<V> | V): Promise<V> => {
      let value: V
      try { value = await pending } catch (error) {
        if (!live()) throw new StaleScopeError()
        throw error
      }
      if (!live()) throw new StaleScopeError()
      return value
    }
    return (async () => {
      try {
        const value = await work({ step, signal: controller.signal })
        if (!live()) return undefined
        handlers.settled?.()
        return value
      } catch (error) {
        if (!live()) return undefined
        if (!handlers.failed) throw error
        handlers.failed(error)
        handlers.settled?.()
        return undefined
      } finally {
        controllers.delete(controller)
        if (slot?.current === controller) slot.current = undefined
      }
    })()
  }
  function reset() {
    generation++
    for (const controller of controllers) controller.abort()
    controllers.clear()
  }
  function lane(): Lane {
    const slot: { current?: AbortController } = {}
    return {
      run: (work, handlers = {}) => execute(slot, work, handlers),
      cancel() { slot.current?.abort(); slot.current = undefined },
    }
  }
  return {
    run: (work, handlers = {}) => execute(undefined, work, handlers),
    lane,
    reset,
    dispose() { disposed = true; reset() },
  }
}
