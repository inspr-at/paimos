<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId } from 'vue'
import AppIcon from '../../AppIcon.vue'
import ModelCard from './ModelCard.vue'
import SettingsPopover, { type SettingsMenuItem } from '../SettingsPopover.vue'
import { lineName, localizeColumn, type ModelBoardDocument } from '../../../lib/modelsBoard'
import { firstChoiceChanges, moveSetupCard, setupCards, setupProviderAllowed, type SetupAnswers, type SetupPreview } from '../../../lib/modelsSetup'
const props = defineProps<{ source: ModelBoardDocument; workspace: ModelBoardDocument; step: number; preview: SetupPreview | null; german: boolean; busy: boolean }>()
const answers = defineModel<SetupAnswers>({ required: true })
const text = (en: string, de: string) => props.german ? de : en
const id = useId(), rankElement = ref<HTMLElement>()
const title = computed(() => [text('Speed or quality?', 'Tempo oder Qualität?'), text('Max out or keep a reserve?', 'Ausschöpfen oder Reserve behalten?'), text('Which models do you trust for design, build and review?', 'Welchen Modellen vertrauen Sie für Design, Bauen und Prüfung?'), text('How much thinking?', 'Wie viel Denken?'), text('Which providers are allowed for your work?', 'Welche Anbieter sind für Ihre Arbeit erlaubt?')][props.step])
const options = computed(() => {
  if (props.step === 0) return [
    { value: 'best', label: text('Quality first', 'Qualität zuerst'), detail: text('The strongest models first, Astra and Opus included. Costs more.', 'Die stärksten Modelle zuerst, auch Astra und Opus. Kostet mehr.') },
    { value: 'balanced', label: text('A balance', 'Ausgewogen'), detail: text('Strong everyday models first; Astra and Opus further down, except in design and concepts.', 'Starke Alltagsmodelle zuerst; Astra und Opus weiter unten, außer bei Design und Konzepten.') },
    { value: 'save', label: text('Speed and cost first', 'Tempo und Kosten zuerst'), detail: text('Cheaper models first; the strongest at the bottom.', 'Günstigere Modelle zuerst; die stärksten ganz unten.') },
  ]
  if (props.step === 1) return [
    { value: 'maxout', label: text('Max out my accounts', 'Meine Konten ausschöpfen'), detail: text('Each window down to its floor before it resets.', 'Jedes Fenster bis zur Untergrenze vor dem Reset.') },
    { value: 'balanced', label: text('Even pace, some reserve', 'Gleichmäßig, etwas Reserve'), detail: text('Keep for you learns your own use.', '„Für Sie behalten“ lernt Ihre eigene Nutzung.') },
    { value: 'careful', label: text('Keep a good reserve for me', 'Eine gute Reserve für mich'), detail: text('About 10% of a 7-day window a day; 30% stays for you.', 'Etwa 10 % eines 7-Tage-Fensters pro Tag; 30 % bleiben für Sie.') },
  ]
  if (props.step === 3) return [
    { value: 'lean', label: text('Lean', 'Knapp'), detail: 'Codex medium · Claude medium · Grok xhigh' }, { value: 'standard', label: 'Standard', detail: 'Codex high · Claude high · Grok xhigh' },
    { value: 'deep', label: text('Deep', 'Gründlich'), detail: 'Codex xhigh · Claude xhigh · Grok xhigh' }, { value: 'max', label: text('Max', 'Maximal'), detail: 'Codex xhigh · Claude max · Grok xhigh' },
  ]
  return [
    { value: '', label: text('Whatever the workspace allows', 'Was der Arbeitsbereich erlaubt'), detail: text('Codex, Claude and Grok.', 'Codex, Claude und Grok.') },
    { value: 'eu', label: text('EU-hosted only', 'Nur EU-gehostet'), detail: text('Only qualifying EU-hosted routes can run.', 'Nur passende EU-gehostete Routen dürfen laufen.') },
    { value: 'local', label: text('Local only', 'Nur lokal'), detail: text('Only qualifying local routes can run.', 'Nur passende lokale Routen dürfen laufen.') },
  ]
})
const field = computed(() => props.step === 0 ? 'template' : props.step === 1 ? 'usage' : props.step === 3 ? 'thinking' : 'residency')
function choose(value: string) {
  if (props.busy) return
  const provider = value === '' ? null : value as SetupAnswers['residency']
  if (field.value === 'residency' && !setupProviderAllowed(provider, props.workspace)) return
  answers.value = { ...answers.value, [field.value]: field.value === 'residency' ? provider : value }
}
const cards = computed(() => { const byLine = new Map(setupCards(props.source).map(card => [card.line, card])); return answers.value.rank.flatMap(line => byLine.has(line) ? [byLine.get(line)!] : []) })
const names = (line?: string) => line ? lineName(setupCards(props.source).find(card => card.line === line) ?? { line, version: '' }) : text('Work waits', 'Arbeit wartet')
const rows = computed(() => {
  const label = (kind: 'template' | 'usage' | 'thinking' | 'residency', value: string | null) => {
    const words: Record<string, string> = { best: text('Best quality', 'Beste Qualität'), balanced: kind === 'template' ? text('Balanced', 'Ausgewogen') : text('Balanced', 'Ausgewogen'), save: text('Save tokens', 'Tokens sparen'), careful: text('Careful', 'Vorsichtig'), maxout: text('Max out', 'Ausschöpfen'), lean: text('Lean', 'Knapp'), standard: 'Standard', deep: text('Deep', 'Gründlich'), max: text('Max', 'Maximal'), eu: text('EU-hosted only', 'Nur EU-gehostet'), local: text('Local only', 'Nur lokal'), any: text('Whatever the workspace allows', 'Was der Arbeitsbereich erlaubt') }
    return words[value ?? (kind === 'residency' ? 'any' : kind === 'thinking' ? 'standard' : 'balanced')]
  }
  const current = props.source.profile, defaults = props.workspace.profile
  return [
    [text('Template', 'Vorlage'), label('template', current.template ?? defaults.template), label('template', answers.value.template)],
    [text('Usage', 'Nutzung'), label('usage', current.usage ?? defaults.usage), label('usage', answers.value.usage)],
    [text('Thinking', 'Denken'), label('thinking', current.thinking ?? defaults.thinking), label('thinking', answers.value.thinking)],
    [text('Providers', 'Anbieter'), label('residency', props.source.residency.effective), label('residency', answers.value.residency ?? props.workspace.residency.effective)],
    [text('Everything else', 'Alles andere'), names(props.source.columns.find(column => column.column === 'other')?.list[0]?.line), names(props.preview?.moved.find(change => change.column === 'other')?.after[0] ?? (props.preview?.moved.some(change => change.column === 'other') ? undefined : props.source.columns.find(column => column.column === 'other')?.list[0]?.line))],
  ]
})
const changes = computed(() => firstChoiceChanges(props.preview?.moved ?? []).filter(change => change.column !== 'other'))
const columnName = (column: string) => { const found = props.source.columns.find(item => item.column === column); return found ? localizeColumn(found, props.german).label : column }
const announcement = ref(''), dragging = ref<string | null>(null), over = ref<string | null>(null)
async function move(line: string, index: number) {
  answers.value = { ...answers.value, rank: moveSetupCard(answers.value.rank, line, index) }
  announcement.value = text(`${names(line)}, number ${answers.value.rank.indexOf(line) + 1} of ${answers.value.rank.length}`, `${names(line)}, Nummer ${answers.value.rank.indexOf(line) + 1} von ${answers.value.rank.length}`)
  await nextTick(); rankElement.value?.querySelector<HTMLElement>(`[data-line="${line}"]`)?.focus({ preventScroll: true })
}
function keys(line: string, event: KeyboardEvent) {
  if (props.busy || event.ctrlKey || event.metaKey || event.shiftKey || !['ArrowUp', 'ArrowDown'].includes(event.key)) return
  const index = answers.value.rank.indexOf(line), next = Math.max(0, Math.min(answers.value.rank.length - 1, index + (event.key === 'ArrowUp' ? -1 : 1)))
  event.preventDefault()
  if (event.altKey) void move(line, next)
  else rankElement.value?.querySelectorAll<HTMLElement>('.mc')[next]?.focus({ preventScroll: true })
}
const moveOpen = ref(false), moveAnchor = ref<HTMLElement | null>(null), moveLine = ref('')
const moveContext = computed(() => JSON.stringify([props.source.person_id, props.source.revision, props.step, props.busy, answers.value.rank]))
const moveItems = computed<SettingsMenuItem[]>(() => [
  { id: 'first', label: text('Move to the top', 'Ganz nach oben'), icon: 'to-top' }, { id: 'up', label: text('Move up', 'Nach oben'), icon: 'arrow-up' },
  { id: 'down', label: text('Move down', 'Nach unten'), icon: 'arrow-down' }, { id: 'last', label: text('Move to the bottom', 'Ganz nach unten'), icon: 'arrow-down' },
])
let suppressClick = false, clickTimer: ReturnType<typeof setTimeout> | undefined
function openMove(line: string, event: MouseEvent) {
  if (suppressClick || props.busy || props.step !== 2) return
  moveLine.value = line; moveAnchor.value = event.currentTarget as HTMLElement; moveOpen.value = true
}
function chooseMove(action: string, captured: string | undefined) {
  if (captured !== moveContext.value || props.busy || props.step !== 2) return
  const index = answers.value.rank.indexOf(moveLine.value)
  if (index < 0) return
  const target = action === 'first' ? 0 : action === 'last' ? answers.value.rank.length - 1 : index + (action === 'up' ? -1 : 1)
  moveOpen.value = false; void move(moveLine.value, target)
}
let dragStart: { line: string; x: number; y: number; pointer: number } | null = null
function press(line: string, event: PointerEvent) {
  if (props.busy || event.button !== 0) return
  dragStart = { line, x: event.clientX, y: event.clientY, pointer: event.pointerId }
  window.addEventListener('pointermove', pointerMove); window.addEventListener('pointerup', release); window.addEventListener('pointercancel', cancel)
}
function pointerMove(event: PointerEvent) {
  if (!dragStart || event.pointerId !== dragStart.pointer || Math.hypot(event.clientX - dragStart.x, event.clientY - dragStart.y) < 5) return
  dragging.value = dragStart.line
  const row = document.elementFromPoint(event.clientX, event.clientY)?.closest<HTMLElement>('[data-setup-line]')
  over.value = row && rankElement.value?.contains(row) ? row.dataset.setupLine ?? null : null
}
function cancel() { dragStart = null; dragging.value = null; over.value = null; window.removeEventListener('pointermove', pointerMove); window.removeEventListener('pointerup', release); window.removeEventListener('pointercancel', cancel) }
function release() { const line = dragging.value, target = over.value; cancel(); if (line) { suppressClick = true; clickTimer = setTimeout(() => { suppressClick = false }, 0) }; if (line && target) void move(line, answers.value.rank.indexOf(target)) }
onBeforeUnmount(() => { cancel(); if (clickTimer) clearTimeout(clickTimer) })
</script>
<template>
  <div data-setup-body>
    <template v-if="step < 5">
      <ol class="steps" aria-hidden="true"><li v-for="index in 5" :key="index" :class="{ reached: index <= step + 1 }" /></ol>
      <fieldset class="question"><legend>{{ title }}</legend>
        <p v-if="step === 2" class="note" :id="`${id}-rank-help`">{{ text('Drag them into order, or use Alt+↑/↓. The top runs first; the bottom only if nothing above can run. This fills your Everything else column; UI design keeps the workspace pin (Opus first), and reviews always go to another family.', 'In Reihenfolge ziehen oder Alt+↑/↓. Oben läuft zuerst; unten nur, wenn nichts darüber laufen kann. Das füllt Ihre Spalte Alles andere; UI-Design behält die Anheftung (Opus zuerst), und Prüfungen gehen immer an eine andere Familie.') }}</p>
        <p v-if="step === 4" class="note">{{ text('You can only narrow what the workspace allows.', 'Sie können nur einschränken, was der Arbeitsbereich erlaubt.') }}</p>
        <ol v-if="step === 2" ref="rankElement" class="rank" data-setup-rank :aria-describedby="`${id}-rank-help`"><li v-for="(card, index) in cards" :key="card.line" :data-setup-line="card.line" :class="{ over: over === card.line }"><ModelCard :card="card" :number="index + 1" :movable="!busy" :placeholder="dragging === card.line" :german="german" @open="openMove(card.line, $event)" @keys="keys(card.line, $event)" @press="press(card.line, $event)" /></li></ol>
        <div v-else class="options" data-setup-options><label v-for="option in options" :key="option.value" class="option"><input type="radio" :name="`${id}-${field}`" :value="option.value" :checked="(answers[field] ?? '') === option.value" :disabled="busy || (step === 4 && !setupProviderAllowed(option.value === '' ? null : option.value as SetupAnswers['residency'], workspace))" @change="choose(option.value)" /><span><strong>{{ option.label }}</strong><small>{{ option.detail }}</small></span></label></div>
      </fieldset>
    </template>
    <template v-else-if="preview">
      <table class="diff" data-setup-diff><thead><tr><th scope="col"><span class="sr-only">{{ text('Setting', 'Einstellung') }}</span></th><th scope="col">{{ text('Now', 'Jetzt') }}</th><th scope="col">{{ text('After', 'Danach') }}</th></tr></thead><tbody><tr v-for="[label, before, after] in rows" :key="label" :class="{ changed: before !== after }"><th scope="row">{{ label }}</th><td>{{ before }}</td><td>{{ after }}</td></tr></tbody></table>
      <h3>{{ text('New first choices', 'Neue erste Wahl') }}</h3><ul v-if="changes.length" class="changes"><li v-for="change in changes" :key="change.column"><AppIcon name="arrow" :size="13" /><span>{{ columnName(change.column) }}: {{ names(change.before[0]) }} <AppIcon name="arrow" :size="12" /> {{ names(change.after[0]) }}</span></li></ul><p v-else class="note">{{ text('No other column changes its first choice.', 'Keine andere Spalte ändert ihre erste Wahl.') }}</p>
      <p class="set-note"><AppIcon name="info" :size="14" /><span>{{ text('Columns you ordered yourself stay as they are. Rules still bound everything.', 'Spalten, die Sie selbst geordnet haben, bleiben. Regeln begrenzen weiterhin alles.') }}</span></p>
    </template>
    <p v-else class="note" role="status">{{ text('Checking the changes…', 'Änderungen werden geprüft…') }}</p>
    <SettingsPopover v-model:open="moveOpen" :anchor="moveAnchor" :label="text('Move to…', 'Verschieben nach…')" :items="moveItems" :context-key="moveContext" @select="chooseMove" />
    <span class="sr-only" aria-live="polite">{{ announcement }}</span>
  </div>
</template>
<style scoped>
.steps { display: flex; gap: 6px; margin: 0 0 14px; padding: 0; list-style: none; }.steps li { flex: 1; height: 4px; border-radius: 4px; background: var(--track); }.steps li.reached { background: var(--teal); }
.question { min-width: 0; margin: 0; padding: 0; border: 0; }.question legend { padding: 0; margin-bottom: 10px; font-size: 16px; font-weight: 600; line-height: 1.35; }.note { font-size: 12.5px; line-height: 1.5; color: var(--ink-2); margin-bottom: 10px; }
.options { display: grid; gap: 6px; }.option { display: grid; grid-template-columns: 20px minmax(0, 1fr); gap: 10px; align-items: start; padding: 8px 10px; border-radius: 10px; height: 7em; box-shadow: inset 0 0 0 1px var(--line); cursor: pointer; font-size: 13px; }.option:has(input:checked) { background: var(--row-selected); }.option:hover { background: var(--row-hover); }.option:has(input:disabled) { opacity: .6; cursor: default; }.option input { margin: 3px 0 0; accent-color: var(--teal); width: 16px; height: 16px; }.option strong { display: block; font-size: 13px; font-weight: 600; }.option small { display: block; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.rank { display: grid; gap: 8px; list-style: none; margin: 0; padding: 0; }.rank li { height: 48px; border-radius: 10px; }.rank li.over { outline: 1px solid var(--teal); }.rank :deep(.mc) { touch-action: none; }
.diff { width: 100%; border-collapse: collapse; font-size: 12.5px; table-layout: fixed; }.diff th, .diff td { text-align: left; vertical-align: top; padding: 8px 6px; border-bottom: 1px solid var(--line); overflow-wrap: anywhere; }.diff th { font-weight: 600; }.diff td { color: var(--ink-2); }.diff .changed td:last-child { font-weight: 650; color: var(--ink); }h3 { font-size: 13px; margin: 18px 0 10px; }.changes { list-style: none; padding: 0; display: grid; gap: 8px; font-size: 12.5px; }.changes li, .set-note { display: flex; align-items: flex-start; gap: 8px; }.changes svg, .set-note svg { flex: none; margin-top: 2px; }.changes span svg { display: inline; vertical-align: middle; }.set-note { margin-top: 16px; color: var(--ink-2); font-size: 12.5px; line-height: 1.5; }
</style>
