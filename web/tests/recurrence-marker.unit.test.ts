// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { recurrenceMarkerLabel, recurrenceSchedule } from '../src/lib/recurrenceMarker'
import type { NodeRecurrence } from '../src/lib/api'

describe('recurring marker copy', () => {
  const marker: NodeRecurrence = { id: 'r', project_id: 'p', project_key: 'PRJ-1', number: 4, retired: false, trigger: { kind: 'time', rrule: 'FREQ=WEEKLY;BYDAY=MO' } }
  it('describes the actual schedule and receipt number in English and German', () => {
    expect(recurrenceMarkerLabel(marker, 'en-GB')).toBe('Recurring · every Monday · #4')
    expect(recurrenceMarkerLabel(marker, 'de-AT')).toBe('Wiederkehrend · jeden Montag · Nr. 4')
    expect(recurrenceSchedule({ kind: 'time', rrule: 'FREQ=WEEKLY;BYDAY=MO,FR' }, 'de')).toBe('jeden Montag, Freitag')
    expect(recurrenceSchedule({ kind: 'time', rrule: 'FREQ=WEEKLY', start_date: '2026-10-04' })).toBe('every Sunday')
    expect(recurrenceMarkerLabel({ ...marker, retired: true }, 'en')).toBe('Recurring · every Monday · #4')
  })
  it('covers daily, quarterly, negative monthly days, events and incomplete legacy triggers honestly', () => {
    expect(recurrenceSchedule({ kind: 'time', rrule: 'FREQ=DAILY' }, 'de')).toBe('jeden Tag')
    expect(recurrenceSchedule({ kind: 'time', rrule: 'FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=-1' })).toBe('every 3 months on the last day')
    expect(recurrenceSchedule({ kind: 'time', rrule: 'FREQ=MONTHLY;BYMONTHDAY=-2' })).toBe('every month on day 2 from the end')
    expect(recurrenceSchedule({ kind: 'time', rrule: 'FREQ=MONTHLY', start_date: '2026-10-03' }, 'de')).toBe('jeden Monat am 3. Tag')
    expect(recurrenceSchedule({ kind: 'event', event: 'release.published' }, 'de')).toBe('nach jeder Veröffentlichung')
    expect(recurrenceSchedule({ kind: 'time' })).toBe('on a schedule')
  })
})
