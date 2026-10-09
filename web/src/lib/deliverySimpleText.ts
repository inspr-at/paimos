// SPDX-License-Identifier: AGPL-3.0-only
// Plain words of the Delivery page's Simple level (AEON-994 draft 5, section 7 and
// the approved mock). English and German, impersonal. Placeholders in braces are
// filled with the selected window's own numbers, never with the mock's.
import type { DeliveryLanguage } from './delivery'
import type { MetricTextKey } from './deliveryNumbersText'

const TO = '→'

export interface SimpleMetricText {
  name: string; say: string; sayAlt?: string; wants: string; learn: string; learnPart?: string; src: string; part?: string
}

const EN = {
  learn: 'Learn', lower: 'lower is better', higher: 'higher is better', better: 'better',
  on: 'On target', close: 'Close', far: 'Far off', none: 'No target yet', nodataV: 'No data yet', notLoaded: 'Not loaded',
  target: 'target', theTarget: 'the target', wanted: 'wanted', about: 'about',
  tBetter: 'Better than the {w} days before', tWorse: 'Worse than the {w} days before', tSame: 'Steady vs. the {w} days before',
  tNone: 'No data in the {w} days before', tLoading: 'History still loading: no comparison yet', tNoData: 'Nothing to compare yet',
  partial: 'Partial', noDataYet: 'no data yet', eachNight: 'one square per night', learnHead: 'Learn', expertName: 'In the Expert view', arion: 'Project Arion',
  units: { min: 'min', h: 'h', times: 'times', byScript: 'by script', nightsGreen: 'nights green', nightGreen: 'night green', of: 'of' },
  // Summary card
  sumNone: 'None of the {t} numbers with a target is on target yet.', sumSome: '{k} of {t} numbers are on target.',
  closest: 'Closest', biggest: 'Biggest gap',
  week: 'Last {w} days vs. the {w} before: {b} better · {x} worse · {s} steady · {n} without a comparison',
  charts: 'Small charts: one point per {u}', chartsHatch: ' · hatched = no data yet (history starts {d})', per: { day: 'day', week: 'week', month: 'month' },
  legend: 'Legend', nodataT: 'No numbers yet.', nodataB: 'They appear with the first pull request. The targets below already apply.',
  onTarget: '{k} of {t} on target',
  nightsNote: { day: 'Last {n} nights · empty = no run', week: 'Last {n} weeks · empty = no run', month: 'Last {n} months · empty = no run' },
  redSince: 'red every night since {d}', redRan: 'red every night it ran', greenOf: '{k} of {n} nights green',
  sections: [
    { title: 'Making a change ready', sub: 'Checks, re-runs and review, before a change can merge.' },
    { title: 'Getting it merged', sub: 'From opening the PR to merged, through the merge queue.' },
    { title: 'Shipping it', sub: 'From release to live, and the full test run each night.' },
  ],
  defsTitle: 'The technical words, explained simply',
  defs: [
    ['Median (p50)', 'The middle value: half were faster, half slower. A few extreme cases do not distort it.'],
    ['Slow tail (p90)', 'Nine in ten were faster. It shows how bad the bad cases are.'],
    ['First try vs. until green', '“PR checks” measures one run. “Until checks are green” counts every re-run and fix as well.'],
    ['Run time vs. waiting', `Run time (wall) is start ${TO} end of one run. Waiting for a runner or in the queue is not in it; “From PR opened to merged” includes all waiting.`],
    ['Flaky test', 'A test that sometimes fails without a real bug. A re-run without any code change turns green.'],
    ['Partial and no data yet', 'Partial: the source sees only part of the picture (e.g. reported only recently). No data yet: nothing recorded, never shown as 0.'],
  ] as [string, string][],
  metrics: {
    pr_ci_wall: { name: 'How long the PR checks take', say: 'Every change waits about {v} for its checks.', wants: 'Arion wants 8 min, later 5.',
      learn: 'p50 (median) {p50}: half of the runs were faster than this. p90 {p90}: nine in ten were faster; it shows the slow tail. Wall time: start to end of one run; waiting for a free runner before the start is not counted.', src: 'Measured from GitHub' },
    queue_run_wall: { name: 'One merge-queue run', say: 'One pass through the merge queue takes about {v}.', wants: 'Arion wants 7 min, 3 when earlier results can be reused.',
      learn: 'Merge-queue run duration (wall), p50 {p50} · p90 {p90}. One run of the full checks in the merge queue; the wait in the queue before it starts is not counted.', src: 'Measured from GitHub' },
    first_attempt_green: { name: 'Checks green on the first try', say: '{v} of changes pass on the first try; {v2} fail only because of a flaky test.', sayAlt: '{v} of changes pass on the first try.',
      wants: 'Arion wants at least 80 %, and flaky failures at 5 % or less.',
      learn: 'First-attempt green rate: {v} of {n} first attempts ended green. Flake-only failure: the first attempt failed, but a re-run of the same commit passed without any code change ({v2}). Arion targets: green on the first try ≥ {t} (D4″), flake-only failures ≤ {t2}.', src: 'Measured from GitHub' },
    time_to_first_green: { name: 'Time until checks are green', say: 'Until its checks are green, a change needs about {v}, re-runs included.', wants: 'Arion wants about 12 min.',
      learn: 'Time to first green, p50 {p50} · p90 {p90}. Counted per PR branch from the first CI run to the first green one, with every re-run and fix in between. Not the same as one run of the checks.', src: 'Measured from GitHub' },
    pr_open_to_merged: { name: 'From PR opened to merged', say: 'From opening a PR to merging it takes about {v}.', wants: 'Arion wants about 40 min.',
      learn: `PR opened ${TO} merged, p50 {p50} · p90 {p90}. Clock time, including review, fix rounds, waiting in the queue and being sent back.`, src: 'Measured from GitHub' },
    queue_runs_per_pr: { name: 'Queue tries per change', say: 'On average a change goes through the queue {v} times before it lands.', wants: 'Arion wants about 1.1: almost every change in one go.',
      learn: 'Queue runs per PR, mean {v} · p90 {p90}. Every extra run means the change was sent back, usually by a flaky test or a conflict.', src: 'Measured from GitHub' },
    review_time: { name: 'How long a review takes', say: 'A review takes about {v}, and {k} of {n} reviews ask for changes.', sayAlt: 'A review takes about {v}.', wants: 'Arion wants 8 min, and changes in at most 1 of 4.',
      learn: `Review time (first round), p50 {p50} · p90 {p90}: review requested ${TO} verdict posted. Share of “changes” verdicts: {k} of {n}. Partial: only PRs with an aeon/review status are counted.`,
      src: 'From review statuses on GitHub', part: 'only reviews with a status count' },
    merge_rounds_model_share: { name: 'Conflicts solved by script', say: 'When a change collides with a newer main, a script solved it {k} times out of {n}; an agent did the rest.', wants: 'Arion wants 8 of 10 by script.',
      learn: 'Merge rounds, model share {m} ({km} of {n}). A merge round is a PR that conflicts with a moved main; scripted means the merge driver resolved it.', learnPart: ' Partial: the merge tooling reports rounds since {d}.',
      src: 'Reported by the merge tooling', part: 'reports since {d}' },
    release_queue_to_live: { name: 'From release to live', say: 'Release {r} took about {v} from the queue to live on csb1.', sayAlt: 'A release takes about {v} from the queue to live on csb1 ({n} releases).', wants: 'Arion wants about 24 min.',
      learn: `Release, merge queue ${TO} live on csb1, p50 {p50} over {n}.`, learnPart: ' Partial: the release tooling reports since {d}.',
      src: 'Reported by the release tooling', part: 'reported since {d}' },
    nightly_green: { name: 'Full test run each night', say: 'The full test run failed on all {n} nights it ran, since it started on {d}.', sayAlt: 'The full test run was green on {k} of {n} nights it ran.', wants: 'Arion wants it green every night.',
      learn: 'Nightly full run, {k} of {n} nights green. One verdict per night from the scheduled full run; a night without a run is shown as “no run”, never as red.', src: 'Measured from GitHub' },
  } satisfies Record<MetricTextKey, SimpleMetricText> as Record<MetricTextKey, SimpleMetricText>,
  releaseOne: '1 release', releaseMany: '{n} releases',
}
export type SimpleText = typeof EN

const DE: SimpleText = {
  learn: 'Lernen', lower: 'niedriger ist besser', higher: 'höher ist besser', better: 'besser',
  on: 'Im Ziel', close: 'Knapp dran', far: 'Weit weg', none: 'Noch kein Ziel', nodataV: 'Noch keine Daten', notLoaded: 'Nicht geladen',
  target: 'Ziel', theTarget: 'das Ziel', wanted: 'gewollt', about: 'etwa',
  tBetter: 'Besser als die {w} Tage davor', tWorse: 'Schlechter als die {w} Tage davor', tSame: 'Stabil ggü. den {w} Tagen davor',
  tNone: 'Keine Daten in den {w} Tagen davor', tLoading: 'Historie lädt noch: noch kein Vergleich', tNoData: 'Noch nichts zu vergleichen',
  partial: 'Teilweise', noDataYet: 'noch keine Daten', eachNight: 'ein Feld pro Nacht', learnHead: 'Lernen', expertName: 'In der Expertenansicht', arion: 'Project Arion',
  units: { min: 'min', h: 'h', times: '-mal', byScript: 'per Skript', nightsGreen: 'Nächten grün', nightGreen: 'Nacht grün', of: 'von' },
  sumNone: 'Noch keine der {t} Zahlen mit Ziel ist im Ziel.', sumSome: '{k} von {t} Zahlen sind im Ziel.',
  closest: 'Am nächsten dran', biggest: 'Größte Lücke',
  week: 'Letzte {w} Tage gegen die {w} davor: {b} besser · {x} schlechter · {s} stabil · {n} ohne Vergleich',
  charts: 'Kleine Diagramme: ein Punkt pro {u}', chartsHatch: ' · schraffiert = noch keine Daten (die Historie beginnt am {d})', per: { day: 'Tag', week: 'Woche', month: 'Monat' },
  legend: 'Legende', nodataT: 'Noch keine Zahlen.', nodataB: 'Sie erscheinen mit dem ersten Pull Request. Die Ziele unten gelten schon.',
  onTarget: '{k} von {t} im Ziel',
  nightsNote: { day: 'Letzte {n} Nächte · leer = kein Lauf', week: 'Letzte {n} Wochen · leer = kein Lauf', month: 'Letzte {n} Monate · leer = kein Lauf' },
  redSince: 'seit {d} jede Nacht rot', redRan: 'jede Nacht mit Lauf rot', greenOf: '{k} von {n} Nächten grün',
  sections: [
    { title: 'Eine Änderung fertig machen', sub: 'Checks, Wiederholungen und Review, bevor eine Änderung mergen kann.' },
    { title: 'Sie in main bringen', sub: 'Vom Öffnen des PR bis zum Merge, durch die Merge-Queue.' },
    { title: 'Ausliefern', sub: 'Vom Release bis live, und der volle Testlauf jede Nacht.' },
  ],
  defsTitle: 'Die Fachwörter, einfach erklärt',
  defs: [
    ['Median (p50)', 'Der mittlere Wert: Die Hälfte war schneller, die Hälfte langsamer. Ausreißer verzerren ihn nicht.'],
    ['Langsame Ausreißer (p90)', 'Neun von zehn waren schneller. Zeigt, wie schlimm die schlechten Fälle sind.'],
    ['Erster Versuch vs. bis grün', '„PR-Checks“ misst einen Lauf. „Bis die Checks grün sind“ zählt alle Wiederholungen und Fixes mit.'],
    ['Laufzeit vs. Wartezeit', `Laufzeit (Wall) ist Start ${TO} Ende eines Laufs. Warten auf einen Runner oder in der Queue zählt dort nicht; „Vom PR bis zum Merge“ enthält alles Warten.`],
    ['Wackeliger Test (Flake)', 'Ein Test, der manchmal ohne echten Fehler scheitert. Eine Wiederholung ohne Codeänderung wird grün.'],
    ['Teilweise und noch keine Daten', 'Teilweise: Die Quelle sieht nur einen Teil (z. B. erst seit Kurzem gemeldet). Noch keine Daten: nichts erfasst, nie als 0 gezeigt.'],
  ],
  metrics: {
    pr_ci_wall: { name: 'Wie lange die PR-Checks dauern', say: 'Jede Änderung wartet etwa {v} auf ihre Checks.', wants: 'Arion will 8 min, später 5.',
      learn: 'p50 (Median) {p50}: Die Hälfte der Läufe war schneller. p90 {p90}: Neun von zehn waren schneller; das zeigt die langsamen Ausreißer. Wall-Zeit: Start bis Ende eines Laufs; das Warten auf einen freien Runner davor zählt nicht.', src: 'Gemessen über GitHub' },
    queue_run_wall: { name: 'Ein Merge-Queue-Durchlauf', say: 'Ein Durchlauf der Merge-Queue dauert etwa {v}.', wants: 'Arion will 7 min, 3 wenn frühere Ergebnisse wiederverwendbar sind.',
      learn: 'Merge-Queue-Laufdauer (Wall), p50 {p50} · p90 {p90}. Ein Lauf der vollen Checks in der Merge-Queue; die Wartezeit davor zählt nicht.', src: 'Gemessen über GitHub' },
    first_attempt_green: { name: 'Checks beim ersten Versuch grün', say: '{v} der Änderungen sind beim ersten Versuch grün; {v2} scheitern nur an einem wackeligen Test.', sayAlt: '{v} der Änderungen sind beim ersten Versuch grün.',
      wants: 'Arion will mindestens 80 % und höchstens 5 % Fehlschläge durch wackelige Tests.',
      learn: 'Grün-Quote beim ersten Versuch: {v} von {n} ersten Versuchen endeten grün. Nur-Flake-Fehlschlag: Der erste Versuch scheiterte, eine Wiederholung desselben Commits lief ohne Codeänderung grün ({v2}). Arion-Ziele: grün beim ersten Versuch ≥ {t} (D4″), Nur-Flake-Fehlschläge ≤ {t2}.', src: 'Gemessen über GitHub' },
    time_to_first_green: { name: 'Zeit, bis die Checks grün sind', say: 'Bis ihre Checks grün sind, braucht eine Änderung etwa {v}, Wiederholungen inklusive.', wants: 'Arion will etwa 12 min.',
      learn: 'Zeit bis zum ersten Grün, p50 {p50} · p90 {p90}. Pro PR-Branch vom ersten CI-Lauf bis zum ersten grünen, mit allen Wiederholungen und Fixes dazwischen. Nicht dasselbe wie ein einzelner Lauf.', src: 'Gemessen über GitHub' },
    pr_open_to_merged: { name: 'Vom PR bis zum Merge', say: 'Vom Öffnen eines PR bis zum Merge vergehen etwa {v}.', wants: 'Arion will etwa 40 min.',
      learn: `PR geöffnet ${TO} gemergt, p50 {p50} · p90 {p90}. Uhrzeit inklusive Review, Fix-Runden, Warten in der Queue und Zurückschicken.`, src: 'Gemessen über GitHub' },
    queue_runs_per_pr: { name: 'Queue-Versuche pro Änderung', say: 'Im Schnitt geht eine Änderung {v}-mal durch die Queue, bevor sie landet.', wants: 'Arion will etwa 1,1: fast jede Änderung im ersten Anlauf.',
      learn: 'Queue-Läufe pro PR, Mittel {v} · p90 {p90}. Jeder zusätzliche Lauf heißt: Die Änderung kam zurück, meist wegen eines Flakes oder eines Konflikts.', src: 'Gemessen über GitHub' },
    review_time: { name: 'Wie lange ein Review dauert', say: 'Ein Review dauert etwa {v}, und {k} von {n} verlangen Änderungen.', sayAlt: 'Ein Review dauert etwa {v}.', wants: 'Arion will 8 min und Änderungen in höchstens 1 von 4.',
      learn: `Review-Dauer (erste Runde), p50 {p50} · p90 {p90}: Review angefordert ${TO} Urteil gesetzt. Anteil „Änderungen“: {k} von {n}. Teilweise: Es zählen nur PRs mit aeon/review-Status.`,
      src: 'Aus Review-Status auf GitHub', part: 'nur Reviews mit Status zählen' },
    merge_rounds_model_share: { name: 'Konflikte per Skript gelöst', say: 'Kollidiert eine Änderung mit einem neueren main, löste ein Skript das {k}-mal von {n}; den Rest machte ein Agent.', wants: 'Arion will 8 von 10 per Skript.',
      learn: 'Merge-Runden, Modell-Anteil {m} ({km} von {n}). Eine Merge-Runde ist ein PR, der mit einem weitergezogenen main kollidiert; per Skript heißt: Der Merge-Treiber hat sie gelöst.', learnPart: ' Teilweise: Meldungen seit {d}.',
      src: 'Vom Merge-Tooling gemeldet', part: 'Meldungen seit {d}' },
    release_queue_to_live: { name: 'Vom Release bis live', say: 'Release {r} brauchte etwa {v} von der Queue bis live auf csb1.', sayAlt: 'Ein Release braucht etwa {v} von der Queue bis live auf csb1 ({n} Releases).', wants: 'Arion will etwa 24 min.',
      learn: `Release, Merge-Queue ${TO} live auf csb1, p50 {p50} über {n}.`, learnPart: ' Teilweise: Das Release-Tooling meldet seit {d}.',
      src: 'Vom Release-Tooling gemeldet', part: 'gemeldet seit {d}' },
    nightly_green: { name: 'Voller Testlauf jede Nacht', say: 'Der volle Testlauf scheiterte in allen {n} Nächten, seit er am {d} startete.', sayAlt: 'Der volle Testlauf war in {k} von {n} Nächten mit Lauf grün.', wants: 'Arion will ihn jede Nacht grün.',
      learn: 'Nightly-Volllauf, {k} von {n} Nächten grün. Ein Urteil pro Nacht aus dem geplanten Volllauf; eine Nacht ohne Lauf heißt „kein Lauf“, nie rot.', src: 'Gemessen über GitHub' },
  },
  releaseOne: '1 Release', releaseMany: '{n} Releases',
}

export const simpleText = (lang: DeliveryLanguage): SimpleText => lang === 'de' ? DE : EN
