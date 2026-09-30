// SPDX-License-Identifier: AGPL-3.0-only
// Follows one paired computer for the register page (AEON-434): bounded polling
// with backoff as the fallback, a read on demand when the live stream names the
// pairing. One fixed deadline per followed computer; a reset, a sign-out or a
// different computer retires every read and timer that was outstanding, so a
// late answer or failure never restarts polling.
import { PairingError, getPairingComputer, pairingReadGeneration, pairingStillLive, planPoll, type PairingView } from './agentPairing'

export interface PairingFollowDeps {
  read?: (computerId: string) => Promise<PairingView>
  /** Changes when signed-in reads are discarded (sign-out, access reset). */
  generation?: () => number
  /** A fresh read of the followed computer. */
  onView: (view: PairingView) => void
  /** The read kept failing until the polling lifetime ended. */
  onFailure: (error: unknown) => void
}

export class PairingFollow {
  private readonly read: (computerId: string) => Promise<PairingView>
  private readonly generation: () => number
  private readonly onView: (view: PairingView) => void
  private readonly onFailure: (error: unknown) => void
  // The followed computer and the moment following began. The polling
  // lifetime is measured from startedAt and from nothing else.
  private following: { computerId: string; startedAt: number } | null = null
  private last: PairingView | null = null
  private failures = 0
  private timer: ReturnType<typeof setTimeout> | undefined
  // Each read takes a turn; only the newest turn of the current follow may answer.
  private turn = 0
  private reading = false
  private again = false

  constructor(deps: PairingFollowDeps) {
    this.read = deps.read ?? (id => getPairingComputer(id))
    this.generation = deps.generation ?? pairingReadGeneration
    this.onView = deps.onView
    this.onFailure = deps.onFailure
  }

  /** The computer being followed, if any. */
  get computerId(): string | null {
    return this.following?.computerId ?? null
  }

  /**
   * Follow the computer this view names. The same computer keeps its deadline;
   * another computer starts its own. A view without a computer follows nothing.
   */
  follow(view: PairingView): void {
    if (!view.computer_id) { this.stop(); return }
    if (this.following?.computerId !== view.computer_id) {
      this.stop()
      this.following = { computerId: view.computer_id, startedAt: Date.now() }
    }
    this.last = view
    this.arm(view)
  }

  /** Read now, because the stream named this pairing. A read in flight is followed by one more, never a second at once. */
  poke(): void {
    if (!this.following) return
    if (this.reading) { this.again = true; return }
    this.clearTimer()
    void this.run()
  }

  /** Reset, sign-out or unmount: nothing outstanding may act any more. */
  stop(): void {
    this.clearTimer()
    this.turn += 1
    this.following = null
    this.last = null
    this.failures = 0
    this.reading = false
    this.again = false
  }

  private clearTimer() {
    if (this.timer) clearTimeout(this.timer)
    this.timer = undefined
  }

  private arm(view: PairingView) {
    this.clearTimer()
    const following = this.following
    // A read in flight arms the next one when it lands.
    if (!following || this.reading || !pairingStillLive(view)) return
    const plan = planPoll({ startedAt: following.startedAt, now: Date.now(), intervalSeconds: view.interval_seconds, state: view.state, failures: this.failures })
    if (plan.action === 'stop') return
    this.timer = setTimeout(() => { this.timer = undefined; void this.run() }, plan.delayMs)
  }

  private async run(): Promise<void> {
    const following = this.following
    if (!following) return
    const { computerId } = following
    const turn = ++this.turn
    const generation = this.generation()
    this.reading = true
    // Current only while this is still the newest read of the same follow.
    const current = () => turn === this.turn && generation === this.generation() && this.following === following
    try {
      const view = await this.read(computerId)
      if (!current()) return
      this.reading = false
      this.failures = 0
      this.last = view
      this.onView(view)
      if (!current()) return
      if (this.again) { this.again = false; void this.run(); return }
      this.arm(view)
    } catch (error) {
      if (!current()) return
      this.reading = false
      this.again = false
      if (error instanceof PairingError && error.code === 'session_reset') return
      const state = this.last?.state
      const interval = this.last?.interval_seconds
      if (error instanceof PairingError && error.code === 'rate_limited') {
        const plan = planPoll({ startedAt: following.startedAt, now: Date.now(), rateLimited: true, retryAfterSeconds: error.retryAfterSeconds, state })
        if (plan.action === 'wait') this.timer = setTimeout(() => { this.timer = undefined; void this.run() }, plan.delayMs)
        return
      }
      this.failures += 1
      const plan = planPoll({ startedAt: following.startedAt, now: Date.now(), intervalSeconds: interval, state, failures: this.failures })
      if (plan.action === 'wait') { this.timer = setTimeout(() => { this.timer = undefined; void this.run() }, plan.delayMs); return }
      this.onFailure(error)
    }
  }
}
