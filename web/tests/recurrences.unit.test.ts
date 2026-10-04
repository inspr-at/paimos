// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { copyRecurrenceInput, parseCriteria, recurrenceEstimate, renderTitle, templateProblems, triggerWords, type RecurrenceInput } from '../src/lib/recurrences'
const input = (): RecurrenceInput => ({ project_id: 'p', parent_id: 'p', template: { name: 'Sweep', title: 'Sweep {{date}} #{{occurrence}}', description: '', acceptance_criteria: [], estimate_hours: 0, type: 'ticket', priority: 'medium', tags: [] }, trigger: { kind: 'time', rrule: 'FREQ=WEEKLY;BYDAY=MO', time_of_day: '09:00', timezone: 'Europe/Vienna' }, queue_each: false, overlap_policy: 'skip', catch_up_policy: 'one' })
describe('recurrence templates', () => {
  it('uses local dates across UTC midnight and DST, and codenames for releases', () => {
    expect(renderTitle('Sweep {{date}} #{{occurrence}}', 2, '2026-10-02T23:30:00Z', 'Europe/Vienna')).toBe('Sweep 2026-10-03 #2')
    expect(renderTitle('{{release_name}} · {{date}}', 3, '2026-10-25T23:30:00Z', 'Europe/Vienna', { name: 'Sunlit Sonde', version: '261002081219.0.0' })).toBe('Sunlit Sonde · 2026-10-26')
  })
  it('rejects invalid tokens and incomplete queued work', () => {
    const draft = input()
    draft.queue_each = true
    expect(templateProblems(draft, '')).toContain('To queue each one, add an estimate and criteria.')
    draft.template.title = '{{release_name}} {{ date }}'
    expect(templateProblems(draft, '')).toHaveLength(3)
    draft.trigger = { kind: 'event', event: 'release.published' }
    draft.template.title = '{{release_name}}'
    draft.template.estimate_hours = recurrenceEstimate('20 min')!
    draft.template.acceptance_criteria = parseCriteria('- [ ] Check queues\n- [x] Record outcome\n')
    expect(draft.template.acceptance_criteria).toEqual(['Check queues', 'Record outcome'])
    expect(templateProblems(draft, '20 min')).toEqual([])
    expect(recurrenceEstimate('2 h')).toBe(2)
    expect(recurrenceEstimate('201 h')).toBeNull()
  })
  it('copies nested values and describes quarterly last-day schedules', () => {
    const source = input(), copied = copyRecurrenceInput(source)
    copied.template.tags.push('tag'); copied.trigger.timezone = 'UTC'
    expect(source.template.tags).toEqual([])
    expect(source.trigger.timezone).toBe('Europe/Vienna')
    expect(triggerWords({ ...source.trigger, rrule: 'FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=-1' })).toBe('Every 3 months on the last day at 09:00 · Europe/Vienna')
  })
})
