// SPDX-License-Identifier: AGPL-3.0-only
// Words of the lifeline strip on each ticket (design r4b §3.3, §5.2, §6.2; AEON-1359). English and German,
// impersonal. Placeholders in braces are filled with the ticket's own times, never the design's examples.
import type { DeliveryLanguage } from './delivery'
import type { GateId } from './workFigures'

const TO = '→'

const EN = {
  caption: 'Time in each status',
  live: `Created ${TO} live`, soFar: 'so far', fromCreated: 'from created to {s}',
  again: '{s} again', usually: ', usually {v}',
  partly: 'Partly assumed',
  range: `{a} ${TO} {b}`, now: 'now',
  still: 'Still {s}: nobody has moved it on yet', stillWords: { in_progress: 'in progress', qa: 'in QA' } as Record<string, string>,
  moved: '{who} moved it to {s}', withRelease: ' with release {r}', dayAfterLive: ', a day after it went live',
  kinds: { agent: ' (agent)', person: ' (person)', unknown: '' },
  assumed: 'Not recorded: assumed from the ticket’s status at the time.',
  usual: 'Usually {p50} for {step}; 1 in 10 over {p90}.', usualNoTail: 'Usually {p50} for {step}.',
  slower: 'Slower than usual: usually {p50} for {step}; 1 in 10 over {p90}.', slowerNoTail: 'Slower than usual: usually {p50} for {step}.',
  attn: 'Slower than 9 in 10 tickets: usually {p50} for {step}.',
  blocked: 'Blocked has no typical time.',
  gates: { open: 'open', in_progress: 'doing', done: 'done', closed: 'closed' } satisfies Record<GateId, string>,
  entered: 'Entered {g}', enteredAgain: 'Entered {g} again', by: 'by {who}', created: 'created', current: 'current status',
  nowSince: '{s} now · since {when}',
  stripLabel: 'Time in each status. Arrow keys move through the steps.', gatesLabel: 'Gates crossed and the current status',
  failed: 'Time in each status could not be loaded.', retry: 'Retry',
  forbidden: 'Only people who may read ticket history see the time in each status.',
  steps: { created_to_live: `Created ${TO} live`, created_to_done: `Created ${TO} done`, new_to_picked_up: `New ${TO} picked up`, in_progress: 'In progress', qa_to_merged: `QA ${TO} merged`, done_to_live: `Done ${TO} live`, live_to_accepted: `Live ${TO} accepted` },
  status: null as Record<string, string> | null,
}
export type WorkFiguresText = typeof EN

const DE: WorkFiguresText = {
  caption: 'Zeit in jedem Status',
  live: `Angelegt ${TO} live`, soFar: 'bisher', fromCreated: 'vom Anlegen bis {s}',
  again: '{s} erneut', usually: ', üblich {v}',
  partly: 'Teilweise angenommen',
  range: `{a} ${TO} {b}`, now: 'jetzt',
  still: 'Noch {s}: Niemand hat es weiterbewegt', stillWords: { in_progress: 'in Arbeit', qa: 'in QA' },
  moved: '{who} hat es nach {s} bewegt', withRelease: ' mit Release {r}', dayAfterLive: ', einen Tag nach dem Livegang',
  kinds: { agent: ' (Agent)', person: ' (Person)', unknown: '' },
  assumed: 'Nicht erfasst: aus dem damaligen Status des Tickets angenommen.',
  usual: 'Üblich {p50} für {step}; 1 von 10 über {p90}.', usualNoTail: 'Üblich {p50} für {step}.',
  slower: 'Langsamer als üblich: üblich {p50} für {step}; 1 von 10 über {p90}.', slowerNoTail: 'Langsamer als üblich: üblich {p50} für {step}.',
  attn: 'Langsamer als 9 von 10 Tickets: üblich {p50} für {step}.',
  blocked: 'Blockiert hat keine typische Zeit.',
  gates: { open: 'offen', in_progress: 'in Arbeit', done: 'erledigt', closed: 'geschlossen' },
  entered: 'Wurde {g}', enteredAgain: 'Wurde erneut {g}', by: 'von {who}', created: 'angelegt', current: 'aktueller Status',
  nowSince: '{s} jetzt · seit {when}',
  stripLabel: 'Zeit in jedem Status. Die Pfeiltasten gehen durch die Schritte.', gatesLabel: 'Überschrittene Schwellen und der aktuelle Status',
  failed: 'Die Zeit in jedem Status konnte nicht geladen werden.', retry: 'Erneut versuchen',
  forbidden: 'Nur wer die Ticket-Historie lesen darf, sieht die Zeit in jedem Status.',
  steps: { created_to_live: `Angelegt ${TO} live`, created_to_done: `Angelegt ${TO} erledigt`, new_to_picked_up: `Neu ${TO} aufgenommen`, in_progress: 'In Arbeit', qa_to_merged: `QA ${TO} gemergt`, done_to_live: `Erledigt ${TO} live`, live_to_accepted: `Live ${TO} abgenommen` },
  status: { new: 'Neu', backlog: 'Backlog', open: 'Offen', blocked: 'Blockiert', in_progress: 'In Arbeit', qa: 'QA', done: 'Erledigt', delivered: 'Ausgeliefert', accepted: 'Abgenommen', cancelled: 'Abgebrochen', archived: 'Archiviert' },
}

export const workFiguresText = (lang: DeliveryLanguage): WorkFiguresText => lang === 'de' ? DE : EN
/** Fills {name} placeholders; a missing value leaves the placeholder visible, so a wrong key shows in tests. */
export const put = (template: string, values: Record<string, string | number>) => template.replace(/\{(\w+)\}/g, (whole, key: string) => key in values ? String(values[key]) : whole)
