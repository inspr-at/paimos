// SPDX-License-Identifier: AGPL-3.0-only
import { displayLanguage } from './displayLanguage.ts'
import type { NodeRecurrence } from './api'
import { ruleParts, type RecurrenceTrigger } from './recurrences'

const days = ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday']
const daysDE = ['Montag', 'Dienstag', 'Mittwoch', 'Donnerstag', 'Freitag', 'Samstag', 'Sonntag']
const codes = ['MO', 'TU', 'WE', 'TH', 'FR', 'SA', 'SU']
export const recurringWord = (locale: string) => displayLanguage(locale) === 'de' ? 'Wiederkehrend' : 'Recurring'

export function recurrenceSchedule(trigger: RecurrenceTrigger, locale = 'en'): string {
  const de = displayLanguage(locale) === 'de'
  if (trigger.kind === 'event') return de ? 'nach jeder Veröffentlichung' : 'after every release'
  const rule = ruleParts(trigger.rrule)
  if (rule.FREQ === 'DAILY') return de ? 'jeden Tag' : 'every day'
  if (rule.FREQ === 'WEEKLY') {
    let selected = codes.filter(code => rule.BYDAY?.split(',').includes(code))
    if (!selected.length && trigger.start_date) {
      const weekday = new Date(`${trigger.start_date}T12:00:00Z`).getUTCDay()
      selected = [codes[(weekday + 6) % 7]].filter((day): day is string => !!day)
    }
    const names = selected.map(code => (de ? daysDE : days)[codes.indexOf(code)])
    return names.length ? `${de ? 'jeden' : 'every'} ${names.join(', ')}` : de ? 'jede Woche' : 'every week'
  }
  if (rule.FREQ === 'MONTHLY') {
    const interval = Number(rule.INTERVAL || 1)
    const month = interval === 1 ? de ? 'jeden Monat' : 'every month' : de ? `alle ${interval} Monate` : `every ${interval} months`
    const day = Number(rule.BYMONTHDAY || trigger.start_date?.slice(-2))
    if (!Number.isInteger(day) || !day) return month
    if (day === -1) return `${month} ${de ? 'am letzten Tag' : 'on the last day'}`
    if (day < 0) return `${month} ${de ? `am ${-day}. Tag von hinten` : `on day ${-day} from the end`}`
    return `${month} ${de ? `am ${day}. Tag` : `on day ${day}`}`
  }
  return de ? 'nach Zeitplan' : 'on a schedule'
}

export function recurrenceMarkerLabel(item: NodeRecurrence, locale = 'en'): string {
  return `${recurringWord(locale)} · ${recurrenceSchedule(item.trigger, locale)} · ${displayLanguage(locale) === 'de' ? `Nr. ${item.number}` : `#${item.number}`}`
}
