// SPDX-License-Identifier: AGPL-3.0-only
// Follows one paired computer for the register page (AEON-434): bounded polling
// with backoff as the fallback, a read on demand when the live stream names the
// pairing. One fixed deadline per followed computer; a reset, a sign-out or a
// different computer retires every read and timer that was outstanding, so a
// late answer or failure never restarts polling. Every later read (poll,
// backoff, rate-limit wait) is armed by schedule() alone, and schedule() refuses
// once the follow is retired or the last view is finished or failed.
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
    this.schedule()
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

  /**
   * The only authority for a read that happens later: no other code arms a
   * timer. It refuses when the follow is retired, a read is in flight (it arms
   * the next one when it lands), the last view is finished or failed, or the
   * polling lifetime is used up. A live wake reads once through poke() and arms
   * nothing by itself. Returns whether a read was scheduled.
   */
  private schedule(wait: { rateLimited?: boolean; retryAfterSeconds?: number | null } = {}): boolean {
    this.clearTimer()
    const following = this.following
    const last = this.last
    if (!following || !last || this.reading || !pairingStillLive(last)) return false
    const plan = planPoll({
      startedAt: following.startedAt, now: Date.now(), intervalSeconds: last.interval_seconds, state: last.state,
      failures: this.failures, rateLimited: wait.rateLimited, retryAfterSeconds: wait.retryAfterSeconds,
    })
    if (plan.action === 'stop') return false
    this.timer = setTimeout(() => {
      this.timer = undefined
      // Asked again at the moment of reading: the view may have settled or the follow been retired.
      if (this.following === following && this.last && pairingStillLive(this.last)) void this.run()
    }, plan.delayMs)
    return true
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
      this.schedule()
    } catch (error) {
      if (!current()) return
      this.reading = false
      this.again = false
      if (error instanceof PairingError && error.code === 'session_reset') return
      if (error instanceof PairingError && error.code === 'rate_limited') {
        this.schedule({ rateLimited: true, retryAfterSeconds: error.retryAfterSeconds })
        return
      }
      this.failures += 1
      // Refused: the lifetime is used up or the view is settled. Nothing retries, so say so.
      if (!this.schedule()) this.onFailure(error)
    }
  }
}
