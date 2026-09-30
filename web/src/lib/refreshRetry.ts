// SPDX-License-Identifier: AGPL-3.0-only
// One retry for a refresh that failed (AEON-326): list resync, the list's
// gap read, and the open ticket's gap read. The first try runs at once.
// Further tries wait longer, then stop until the stream resumes, which
// tries again immediately. The wait is kept until one try succeeds.
export const RETRY_MS = [1_000, 3_000, 10_000, 30_000] as const

export class RefreshRetry {
  private failures = 0
  private failed = false
  private running = false
  private again = false
  private generation = 0
  private timer: ReturnType<typeof setTimeout> | undefined
  private readonly attempt: () => Promise<boolean>

  // attempt returns true when the read failed and should be tried again.
  constructor(attempt: () => Promise<boolean>) { this.attempt = attempt }

  // Run now. A run already going remembers to go once more, and does not
  // also arm a wait: the run that is in flight decides that when it lands.
  request() {
    this.clearTimer()
    if (this.running) { this.again = true; return }
    this.start()
  }

  // A resumed stream retries only a read that is still failing.
  resume() {
    if (!this.failed) return
    this.failures = 0
    this.failed = false
    this.request()
  }

  // Drop the wait and ignore a run already in flight. The next request starts clean.
  clear() {
    this.generation++
    this.failed = false
    this.failures = 0
    this.again = false
    this.running = false
    this.clearTimer()
  }

  private clearTimer() {
    clearTimeout(this.timer)
    this.timer = undefined
  }

  private start() {
    const run = this.generation
    if (this.running) { this.again = true; return }
    this.running = true
    void this.attempt().then(failed => this.landed(run, failed), () => this.landed(run, true))
  }

  private landed(run: number, failed: boolean) {
    if (run !== this.generation) return
    this.running = false
    this.failed = failed
    if (!failed) this.failures = 0
    if (this.again) { this.again = false; this.request(); return }
    if (failed && this.failures < RETRY_MS.length) this.arm(RETRY_MS[this.failures++])
  }

  private arm(delay: number) {
    this.clearTimer()
    const run = this.generation
    this.timer = setTimeout(() => {
      this.timer = undefined
      if (run !== this.generation) return
      this.start()
    }, delay)
  }
}
