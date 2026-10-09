// SPDX-License-Identifier: AGPL-3.0-only
// Words of the Delivery page (AEON-994 draft 5, section 7). English and German,
// impersonal; the page reads the shared app display language.
import type { DeliveryLanguage } from './delivery'

// The approved copy reads "start → end" and "PR opened → merged": the arrow is the word "to" in text, never an icon.
const TO = '→'

export type MetricTextKey = 'pr_ci_wall' | 'queue_run_wall' | 'first_attempt_green' | 'time_to_first_green' | 'pr_open_to_merged'
  | 'queue_runs_per_pr' | 'review_time' | 'merge_rounds_model_share' | 'release_queue_to_live' | 'nightly_green'
export interface MetricText { label: string; definition: string; target: string; line: string; count: string; one: string; many: string }

const EN = {
  title: 'Delivery',
  sub: 'How fast changes move from pull request to live, measured all the time against the Project Arion targets.',
  views: { numbers: 'Numbers', flow: 'Flow' }, viewsLabel: 'Delivery views',
  windowLabel: 'Chart window', days: 'days',
  level: { simple: 'Simple', expert: 'Expert' }, levelLabel: 'Level of detail',
  updated: 'Updated {t}', live: 'live',
  srcApp: ['GitHub App events', ' · live'], srcAppOff: ['GitHub App', ' · not receiving events'],
  srcBack: ['Backfill', ' from the GitHub API · back to {d}'], srcRun: ['Backfill running', ' · complete since {d}'], srcRunNone: ['Backfill running', ' · older days still loading'],
  srcNone: ['No backfill yet', ' · only events from now on'], srcTool: ['Reported by tooling', ' · merge rounds, releases'],
  nodataT: 'No delivery data yet.', nodataB: 'PAIMOS starts counting with the first pull request the GitHub App sees in {repo}. History is backfilled once from the GitHub API.',
  norepoT: 'No repository is linked yet.', norepoB: 'Someone who manages delivery for this project links its GitHub repository; history is then backfilled once from the GitHub API.',
  errT: 'Delivery numbers could not be loaded.', errB: 'Nothing on this page is stale: no old numbers are shown in place of new ones.', retry: 'Retry',
  prefErr: 'The window and level could not be saved. This choice stays on this page.', prefRetry: 'Save again',
  cap: 'Key numbers · last {w} days · change against the {w} days before',
  trends: 'Trends', perDay: 'One point per day', perWeek: 'One point per week', perMonth: 'One point per month', lastDays: 'last {w} days',
  lgP50: { day: 'p50 per day', week: 'p50 per week', month: 'p50 per month' }, lgBand: 'p50 to p90', lgTarget: 'Arion target', lgPartial: 'partial', lgNone: 'no data yet',
  partial: 'Partial', noData: 'No data yet', notLoaded: 'Not loaded', nothing: 'Nothing recorded', loading: 'Loading',
  better: 'better', worse: 'worse', same: 'steady', pts: 'pts',
  tNone: 'No data in the {w} days before', tLoading: 'History still loading: no comparison yet',
  covers: 'covers {k} of {n} {u}', units: { day: 'days', week: 'weeks', month: 'months' },
  infoFor: 'Definition of', why: 'Why partial', source: 'Source', window: 'Window', windowT: 'last {w} days; the change compares with the {w} days before', arion: 'Project Arion',
  chartOf: 'Chart of', keys: 'Arrow keys move through the points.', clip: 'p90 above the scale', upTo: 'up to',
  today: 'Today', thisWeek: 'This week', thisWk: 'this wk', weekOf: 'Week of', soFar: 'so far',
  green: 'Green', red: 'Red', noRun: 'No run', failed: 'failed', firstTry: 'green 1st try', flake: 'flake-only', changes: 'changes', changesShare: 'Share of “changes”, %',
  greenWord: 'green', byModel: 'by a model', mean: 'mean', of: 'of', nightsGreen: 'nights green', nightGreen: 'night green',
  flakesOnly: 'Failed only by flakes', changesVerdicts: '“changes” verdicts', scripted: 'scripted', solvedByModel: 'solved by a model', reported: 'reported',
  release: 'Release', releaseOne: 'release', releaseMany: 'releases', redRow: 'Red {n} in a row', greenRow: 'Green {n} in a row', withoutRun: '{n} nights without a run', withoutRunOne: '1 night without a run',
  targetGreen: 'Target green ≥ {v}', targetFlake: 'Target flake-only ≤ {v}', targetChanges: 'Target ≤ {v}',
  flowSub: 'What is moving right now, what it waits for and when it will be done. Replay any run, or race it against the target.',
  flowNone: 'No flow data recorded yet.', flowNoneB: 'Live, Replay and Compare appear here once delivery steps are recorded.',
  defsTitle: 'How these numbers are counted',
  defs: [
    ['First attempt vs. time to green', 'PR CI run measures one run’s first attempt. Time to first green counts from the first run to the first green one, with every re-run and fix in between.'],
    ['Wall time vs. queue delay', `Wall time is run start ${TO} run end. Waiting for a runner or a merge-queue slot happens before the start and is not in it; PR opened ${TO} merged includes all waiting.`],
    ['p50 and p90', 'p50 is the median: half were faster. p90: nine in ten were faster; it shows the slow tail.'],
    ['Partial and no data yet', 'Partial: the source covers only part of the window (backfill limit, a review status on only some PRs, reports that started recently). No data yet: nothing recorded, never shown as 0.'],
    ['Change vs. the window before', 'The last {w} days against the {w} days before. Within 10 % (3 points for shares) counts as about the same.'],
    ['Sources', 'GitHub App events (live), history backfilled once from the GitHub API, and facts reported by the merge and release tooling.'],
  ] as [string, string][],
  sources: { gh: 'GitHub App + backfill', review: 'aeon/review status', merge: 'Merge tooling', release: 'Release tooling', nightly: '{wf} · GitHub App' },
  metrics: {
    pr_ci_wall: { label: 'PR CI run (wall)', definition: `First attempt of each pull-request CI run: run started ${TO} run completed (wall clock). Runner wait before the start is not included; re-runs count in Time to first green.`, target: 'Target 8 min, then 5', line: `Target 8 ${TO} 5`, count: 'runs', one: 'run', many: 'runs' },
    queue_run_wall: { label: 'Merge-queue run', definition: `First attempt of each merge-queue CI run: started ${TO} completed. Waiting in the queue before the run starts is not included.`, target: 'Target 7 min (3 on reuse)', line: 'Target 7', count: 'queue runs', one: 'run', many: 'runs' },
    first_attempt_green: { label: 'Green on first try', definition: 'Share of first attempts of PR CI runs that ended green; cancelled runs (replaced by a newer push) are left out. Flake-only: the first attempt failed, but a re-run on the same commit passed without a code change.', target: 'Target ≥ 80 %, flakes ≤ 5 %', line: 'Target flake-only ≤ 5 %', count: 'first attempts', one: 'run', many: 'runs' },
    time_to_first_green: { label: 'Time to first green', definition: `Per PR branch: first CI run created ${TO} first green run completed, including re-runs and fix pushes. Not the duration of one run.`, target: 'Target ~12 min', line: 'Target 12', count: 'PR branches', one: 'branch', many: 'branches' },
    pr_open_to_merged: { label: `PR opened ${TO} merged`, definition: `PR opened ${TO} PR merged, wall clock: review, fix rounds, queue waits and re-queues included.`, target: 'Target ~40 min', line: 'Target 40', count: 'merged PRs', one: 'PR', many: 'PRs' },
    queue_runs_per_pr: { label: 'Queue runs per PR', definition: 'Average number of merge-queue runs a PR needs until it merges. 1.0 means every PR merged on its first queue run.', target: 'Target 1.1 runs per PR', line: 'Target 1.1', count: 'merged PRs', one: 'PR', many: 'PRs' },
    review_time: { label: 'Review time', definition: `First review round: review requested (aeon/review pending) ${TO} verdict posted. “Changes” share: verdicts that ask for changes. Only PRs with a review status count, so this number is always partial.`, target: 'Target 8 min, changes ≤ 25 %', line: 'Target 8', count: 'reviews', one: 'review', many: 'reviews' },
    merge_rounds_model_share: { label: 'Merge rounds', definition: 'A merge round is a PR that conflicts with a moved main. Scripted: the merge driver resolved it; model: an agent had to. Reported by the merge tooling; PAIMOS cannot see merge rounds by itself.', target: 'Target ≤ 20 % by a model', line: 'Target ≤ 20 %', count: 'merge rounds', one: 'merge round', many: 'merge rounds' },
    release_queue_to_live: { label: `Release ${TO} live (csb1)`, definition: `Per release: the release PR enters the merge queue ${TO} the version is live on csb1, as reported by the release tooling. PAIMOS cannot observe the host itself.`, target: 'Target ~24 min, later ~12', line: 'Target 24', count: 'releases', one: 'release', many: 'releases' },
    nightly_green: { label: 'Nightly full run', definition: 'One verdict per night: the scheduled full run’s last attempt. Green only on success. A night without a run shows as “no run”, never as red.', target: 'Target: green every night', line: '', count: 'nights', one: 'night', many: 'nights' },
  } satisfies Record<MetricTextKey, MetricText>,
}
export type DeliveryText = typeof EN

const DE: DeliveryText = {
  title: 'Lieferung',
  sub: 'Wie schnell Änderungen vom Pull Request bis live kommen, laufend gemessen an den Zielen von Project Arion.',
  views: { numbers: 'Zahlen', flow: 'Ablauf' }, viewsLabel: 'Ansichten der Lieferung',
  windowLabel: 'Zeitraum der Diagramme', days: 'Tage',
  level: { simple: 'Einfach', expert: 'Experte' }, levelLabel: 'Detailgrad',
  updated: 'Aktualisiert {t}', live: 'live',
  srcApp: ['GitHub-App-Ereignisse', ' · live'], srcAppOff: ['GitHub-App', ' · empfängt keine Ereignisse'],
  srcBack: ['Nachgeladen', ' aus der GitHub-API · bis {d}'], srcRun: ['Nachladen läuft', ' · vollständig seit {d}'], srcRunNone: ['Nachladen läuft', ' · ältere Tage laden noch'],
  srcNone: ['Noch nicht nachgeladen', ' · nur Ereignisse ab jetzt'], srcTool: ['Vom Tooling gemeldet', ' · Merge-Runden, Releases'],
  nodataT: 'Noch keine Lieferdaten.', nodataB: 'PAIMOS zählt ab dem ersten Pull Request, den die GitHub-App in {repo} sieht. Die Historie wird einmal aus der GitHub-API nachgeladen.',
  norepoT: 'Noch kein Repository verknüpft.', norepoB: 'Wer die Lieferung dieses Projekts verwaltet, verknüpft sein GitHub-Repository; die Historie wird dann einmal aus der GitHub-API nachgeladen.',
  errT: 'Die Lieferzahlen konnten nicht geladen werden.', errB: 'Nichts hier ist veraltet: Alte Zahlen werden nicht anstelle neuer gezeigt.', retry: 'Erneut versuchen',
  prefErr: 'Zeitraum und Detailgrad konnten nicht gespeichert werden. Die Auswahl bleibt auf dieser Seite.', prefRetry: 'Erneut speichern',
  cap: 'Kennzahlen · letzte {w} Tage · Änderung gegenüber den {w} Tagen davor',
  trends: 'Verlauf', perDay: 'Ein Punkt pro Tag', perWeek: 'Ein Punkt pro Woche', perMonth: 'Ein Punkt pro Monat', lastDays: 'letzte {w} Tage',
  lgP50: { day: 'p50 pro Tag', week: 'p50 pro Woche', month: 'p50 pro Monat' }, lgBand: 'p50 bis p90', lgTarget: 'Arion-Ziel', lgPartial: 'teilweise', lgNone: 'noch keine Daten',
  partial: 'Teilweise', noData: 'Noch keine Daten', notLoaded: 'Nicht geladen', nothing: 'Nichts erfasst', loading: 'Lädt',
  better: 'besser', worse: 'schlechter', same: 'stabil', pts: 'Pp.',
  tNone: 'Keine Daten in den {w} Tagen davor', tLoading: 'Historie lädt noch: noch kein Vergleich',
  covers: 'deckt {k} von {n} {u} ab', units: { day: 'Tagen', week: 'Wochen', month: 'Monaten' },
  infoFor: 'Definition von', why: 'Warum teilweise', source: 'Quelle', window: 'Zeitraum', windowT: 'letzte {w} Tage; die Änderung vergleicht mit den {w} Tagen davor', arion: 'Project Arion',
  chartOf: 'Diagramm von', keys: 'Pfeiltasten wechseln den Punkt.', clip: 'p90 über der Skala', upTo: 'bis',
  today: 'Heute', thisWeek: 'Diese Woche', thisWk: 'diese W.', weekOf: 'Woche ab', soFar: 'bisher',
  green: 'Grün', red: 'Rot', noRun: 'Kein Lauf', failed: 'gescheitert', firstTry: 'grün beim 1. Versuch', flake: 'nur Flake', changes: 'Änderungen', changesShare: 'Anteil „Änderungen“, %',
  greenWord: 'grün', byModel: 'durch ein Modell', mean: 'Mittel', of: 'von', nightsGreen: 'Nächte grün', nightGreen: 'Nacht grün',
  flakesOnly: 'Nur durch Flakes gescheitert', changesVerdicts: '„Änderungen“', scripted: 'per Skript', solvedByModel: 'durch ein Modell gelöst', reported: 'gemeldet',
  release: 'Release', releaseOne: 'Release', releaseMany: 'Releases', redRow: '{n} Nächte in Folge rot', greenRow: '{n} Nächte in Folge grün', withoutRun: '{n} Nächte ohne Lauf', withoutRunOne: '1 Nacht ohne Lauf',
  targetGreen: 'Ziel grün ≥ {v}', targetFlake: 'Ziel nur Flake ≤ {v}', targetChanges: 'Ziel ≤ {v}',
  flowSub: 'Was gerade läuft, worauf es wartet und wann es fertig ist. Jeden Lauf wiedergeben oder gegen das Ziel antreten lassen.',
  flowNone: 'Noch keine Ablaufdaten aufgezeichnet.', flowNoneB: 'Live, Wiedergabe und Vergleich erscheinen hier, sobald Lieferschritte aufgezeichnet werden.',
  defsTitle: 'So werden die Zahlen gezählt',
  defs: [
    ['Erster Versuch vs. Zeit bis Grün', 'PR-CI-Lauf misst den ersten Versuch eines Laufs. Zeit bis erstes Grün zählt vom ersten Lauf bis zum ersten grünen, mit allen Wiederholungen und Fixes dazwischen.'],
    ['Wanduhrzeit vs. Wartezeit', `Wanduhrzeit ist Start ${TO} Ende eines Laufs. Das Warten auf einen Runner oder einen Platz in der Merge-Queue liegt davor und zählt nicht; „PR geöffnet ${TO} gemergt“ enthält alles Warten.`],
    ['p50 und p90', 'p50 ist der Median: Die Hälfte war schneller. p90: Neun von zehn waren schneller; das zeigt die langsamen Ausreißer.'],
    ['Teilweise und noch keine Daten', 'Teilweise: Die Quelle deckt nur einen Teil des Zeitraums ab (Nachlade-Grenze, Review-Status nur auf manchen PRs, Meldungen erst seit Kurzem). Noch keine Daten: nichts erfasst, nie als 0 gezeigt.'],
    ['Änderung ggü. dem Zeitraum davor', 'Die letzten {w} Tage gegen die {w} Tage davor. Weniger als 10 % (bei Anteilen 3 Pp.) gilt als etwa gleich.'],
    ['Quellen', 'GitHub-App-Ereignisse (live), einmal aus der GitHub-API nachgeladene Historie und Fakten, die Merge- und Release-Tooling melden.'],
  ],
  sources: { gh: 'GitHub-App + Nachladen', review: 'aeon/review-Status', merge: 'Merge-Tooling', release: 'Release-Tooling', nightly: '{wf} · GitHub-App' },
  metrics: {
    pr_ci_wall: { label: 'PR-CI-Lauf (Wall)', definition: `Erster Versuch jedes PR-CI-Laufs: Start ${TO} Ende (Wanduhrzeit). Die Wartezeit auf einen Runner vor dem Start zählt nicht; Wiederholungen zählen bei „Zeit bis erstes Grün“.`, target: 'Ziel 8 min, danach 5', line: `Ziel 8 ${TO} 5`, count: 'Läufe', one: 'Lauf', many: 'Läufe' },
    queue_run_wall: { label: 'Merge-Queue-Lauf', definition: `Erster Versuch jedes Merge-Queue-Laufs: Start ${TO} Ende. Die Wartezeit in der Queue vor dem Start zählt nicht.`, target: 'Ziel 7 min (3 bei Reuse)', line: 'Ziel 7', count: 'Queue-Läufe', one: 'Lauf', many: 'Läufe' },
    first_attempt_green: { label: 'Grün beim 1. Versuch', definition: 'Anteil der ersten Versuche von PR-CI-Läufen, die grün enden; abgebrochene Läufe (durch einen neueren Push ersetzt) zählen nicht. Nur Flake: Der erste Versuch scheiterte, eine Wiederholung auf demselben Commit lief ohne Codeänderung grün.', target: 'Ziel ≥ 80 %, Flakes ≤ 5 %', line: 'Ziel nur Flake ≤ 5 %', count: 'erste Versuche', one: 'Lauf', many: 'Läufe' },
    time_to_first_green: { label: 'Zeit bis erstes Grün', definition: `Pro PR-Branch: erster CI-Lauf angelegt ${TO} erster grüner Lauf fertig, inklusive Wiederholungen und Fix-Pushes. Nicht die Dauer eines Laufs.`, target: 'Ziel ~12 min', line: 'Ziel 12', count: 'PR-Branches', one: 'Branch', many: 'Branches' },
    pr_open_to_merged: { label: `PR geöffnet ${TO} gemergt`, definition: `PR geöffnet ${TO} PR gemergt, Wanduhrzeit: Review, Fix-Runden, Queue-Wartezeit und erneutes Einreihen inklusive.`, target: 'Ziel ~40 min', line: 'Ziel 40', count: 'gemergte PRs', one: 'PR', many: 'PRs' },
    queue_runs_per_pr: { label: 'Queue-Läufe pro PR', definition: 'Durchschnittliche Zahl der Merge-Queue-Läufe, bis ein PR gemergt ist. 1,0 heißt: Jeder PR ging im ersten Queue-Lauf durch.', target: 'Ziel 1,1 Läufe pro PR', line: 'Ziel 1,1', count: 'gemergte PRs', one: 'PR', many: 'PRs' },
    review_time: { label: 'Review-Dauer', definition: `Erste Review-Runde: Review angefordert (aeon/review pending) ${TO} Urteil gesetzt. Anteil „Änderungen“: Urteile, die Änderungen verlangen. Es zählen nur PRs mit Review-Status, daher immer teilweise.`, target: 'Ziel 8 min, Änderungen ≤ 25 %', line: 'Ziel 8', count: 'Reviews', one: 'Review', many: 'Reviews' },
    merge_rounds_model_share: { label: 'Merge-Runden', definition: 'Eine Merge-Runde ist ein PR, der mit einem weitergezogenen main kollidiert. Per Skript: Der Merge-Treiber hat sie gelöst; Modell: Ein Agent musste ran. Gemeldet vom Merge-Tooling; PAIMOS sieht Merge-Runden nicht selbst.', target: 'Ziel ≤ 20 % durch Modell', line: 'Ziel ≤ 20 %', count: 'Merge-Runden', one: 'Merge-Runde', many: 'Merge-Runden' },
    release_queue_to_live: { label: `Release ${TO} live (csb1)`, definition: `Pro Release: Der Release-PR kommt in die Merge-Queue ${TO} die Version läuft live auf csb1, gemeldet vom Release-Tooling. PAIMOS kann den Host nicht selbst beobachten.`, target: 'Ziel ~24 min, später ~12', line: 'Ziel 24', count: 'Releases', one: 'Release', many: 'Releases' },
    nightly_green: { label: 'Nightly-Volllauf', definition: 'Ein Urteil pro Nacht: der letzte Versuch des geplanten Volllaufs. Grün nur bei Erfolg. Eine Nacht ohne Lauf heißt „kein Lauf“, nie rot.', target: 'Ziel: jede Nacht grün', line: '', count: 'Nächte', one: 'Nacht', many: 'Nächte' },
  },
}

export const deliveryText = (lang: DeliveryLanguage): DeliveryText => lang === 'de' ? DE : EN
/** Replaces {name} placeholders. */
export function fill(text: string, values: Record<string, string | number>): string {
  return text.replace(/\{(\w+)\}/g, (whole, name: string) => name in values ? String(values[name]) : whole)
}
