// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { tierEvidenceRefresh } from '../src/lib/tierEvidenceLive'
afterEach(() => vi.useRealTimers())
it('ignores other sessions and throttles matching telemetry with one trailing read', () => {
  vi.useFakeTimers(); vi.setSystemTime(0)
  const session = { id: 'selected', run_id: 'run' }, load = vi.fn()
  const refresh = tierEvidenceRefresh(() => session, load, () => true)
  refresh.notify('run.telemetry', { run_id: 'other' })
  refresh.notify('harness.usage_reported', { session_id: 'other' })
  refresh.notify('run.telemetry')
  expect(load).not.toHaveBeenCalled()
  refresh.notify('run.telemetry', { run_id: 'run' })
  expect(load).toHaveBeenCalledExactlyOnceWith(session)
  for (let i = 0; i < 12; i++) {
    vi.advanceTimersByTime(400)
    refresh.notify('harness.usage_reported', { session_id: 'selected' })
  }
  expect(load).toHaveBeenCalledTimes(1)
  vi.advanceTimersByTime(199); expect(load).toHaveBeenCalledTimes(1)
  vi.advanceTimersByTime(1); expect(load).toHaveBeenCalledTimes(2)
  vi.advanceTimersByTime(5000); expect(load).toHaveBeenCalledTimes(2)
  refresh.stop()
})
it('drops queued reads on record/run changes, hidden tabs and disposal', () => {
  vi.useFakeTimers(); vi.setSystemTime(0)
  let session = { id: 'selected', run_id: 'run' }, visible = true
  const load = vi.fn(), refresh = tierEvidenceRefresh(() => session, load, () => visible)
  refresh.notify('run.telemetry', { run_id: 'run' })
  refresh.notify('run.telemetry', { run_id: 'run' })
  session = { id: 'other', run_id: 'other-run' }
  vi.advanceTimersByTime(5000); expect(load).toHaveBeenCalledTimes(1)
  refresh.notify('run.telemetry', { run_id: 'other-run' })
  refresh.notify('run.telemetry', { run_id: 'other-run' })
  session = { id: 'other', run_id: 'replacement-run' }
  vi.advanceTimersByTime(5000); expect(load).toHaveBeenCalledTimes(2)
  refresh.notify('run.telemetry', { run_id: 'replacement-run' })
  refresh.notify('run.telemetry', { run_id: 'replacement-run' })
  visible = false
  vi.advanceTimersByTime(5000); expect(load).toHaveBeenCalledTimes(3)
  refresh.notify(); expect(load).toHaveBeenCalledTimes(3)
  visible = true
  refresh.notify(); expect(load).toHaveBeenCalledTimes(4)
  refresh.notify('run.telemetry', { run_id: 'replacement-run' }); refresh.stop()
  vi.advanceTimersByTime(5000); expect(load).toHaveBeenCalledTimes(4)
})
it('decisions on the selected session bypass evidence cooldown', () => {
  vi.useFakeTimers(); vi.setSystemTime(0)
  const session = { id: 'selected', run_id: 'run' }, load = vi.fn()
  const refresh = tierEvidenceRefresh(() => session, load, () => true)
  refresh.notify('run.telemetry', { run_id: 'run' })
  refresh.notify('run.telemetry', { run_id: 'run' })
  refresh.notify('harness.tier_changed', { session_id: 'other' }); expect(load).toHaveBeenCalledTimes(1)
  refresh.notify('harness.tier_changed', { session_id: 'selected' }); expect(load).toHaveBeenCalledTimes(2)
  vi.advanceTimersByTime(5000); expect(load).toHaveBeenCalledTimes(2)
  refresh.stop()
})
