// SPDX-License-Identifier: AGPL-3.0-only
import { holderGrid } from './geometry'
import type { HolderMeasurement, TerminalClock } from './ports'

export interface HolderSample {
  key: string
  visible: boolean
  fontsReady: boolean
  scale: number
  width: number
  height: number
  cell13: number
  request: Omit<HolderMeasurement, 'cols' | 'rows'> | null
}

/** Emits measurements, never sends a resize. I2/P2c recheck the controller and
 * current binding before deciding whether a local holder may resize its PTY. */
export class HolderSizeObserver {
  private timer: unknown
  private scheduled = false
  private last = ''
  private disposed = false
  private readonly clock: TerminalClock
  private readonly sample: () => HolderSample
  private readonly emit: (value: HolderMeasurement) => void
  constructor(clock: TerminalClock, sample: () => HolderSample, emit: (value: HolderMeasurement) => void) {
    this.clock = clock; this.sample = sample; this.emit = emit
  }
  changed() {
    if (this.disposed) return
    if (this.scheduled) this.clock.clearTimeout(this.timer)
    this.scheduled = false
    const sample = this.sample()
    if (!sample.key || !sample.visible || !sample.fontsReady || sample.scale !== 1 || !sample.request) { this.last = ''; return }
    const captured = sample.key
    this.scheduled = true
    this.timer = this.clock.setTimeout(() => {
      this.scheduled = false
      if (this.disposed) return
      const now = this.sample()
      if (now.key !== captured || !now.visible || !now.fontsReady || now.scale !== 1 || !now.request ||
          now.width <= 0 || now.height <= 0 || now.cell13 <= 0) return
      const grid = holderGrid(now.width, now.height, now.cell13), key = `${captured}/${grid.cols}/${grid.rows}`
      if (key === this.last) return
      this.last = key; this.emit({ ...now.request, ...grid })
    }, 150)
  }
  dispose() { this.disposed = true; if (this.scheduled) this.clock.clearTimeout(this.timer); this.scheduled = false; this.last = '' }
}
