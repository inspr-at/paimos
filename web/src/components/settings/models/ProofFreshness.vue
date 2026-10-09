<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from '../../AppIcon.vue'
import HoverText from './HoverText.vue'
import { can, onAccessChange } from '../../../lib/authz'
import { createScope, scopeOwner } from '../../../lib/identityScope'
import { useSession } from '../../../stores/session'
import { getBoardEvidence, getEvidenceProfiles } from '../../../lib/modelsBoardApi'
import { localizeColumn, type BoardColumn } from '../../../lib/modelsBoard'
import type { BoardEvidence } from '../../../lib/modelsSettings'
import type { PrefProfile } from '../../../lib/modelPrefs'
const props = defineProps<{ columns: BoardColumn[]; person?: string | null; project?: string; german: boolean }>()
const session = useSession(), owner = computed(() => scopeOwner(session.identity) ? `${session.identity!.tenant.id}/${props.person || session.identity!.principal.id}` : '')
const text = (en: string, de: string) => props.german ? de : en
const folds = ref<string[]>([]), kind = ref(''), evidence = ref<BoardEvidence | null>(null), error = ref(''), loading = ref(false)
const profiles = ref<PrefProfile[]>([]), catalogNote = ref('')
const scope = createScope(() => session.authenticationCurrent() && can('models.read', props.project) ? owner.value : ''), lane = scope.lane()
const storageKey = computed(() => `models-page/${owner.value}/proof`)
function persist() { if (!owner.value) return; try { localStorage.setItem(storageKey.value, JSON.stringify({ folds: folds.value, kind: kind.value })) } catch { /* Storage is optional. */ } }
function toggle(id: string) { folds.value = folds.value.includes(id) ? folds.value.filter(value => value !== id) : [...folds.value, id]; persist() }
function load(cursor?: string) {
  loading.value = true; error.value = ''
  const selected = kind.value, project = props.project
  void lane.run(({ after, signal }) => after(Promise.allSettled([getBoardEvidence(selected, project, cursor, signal), getEvidenceProfiles(signal)]), answers => {
    if (answers[0].status !== 'fulfilled') throw answers[0].reason
    evidence.value = answers[0].value
    profiles.value = answers[1].status === 'fulfilled' ? answers[1].value.profiles : []
    catalogNote.value = answers[1].status !== 'fulfilled' ? text('Model names could not be loaded. Run records remain available.', 'Modellnamen konnten nicht geladen werden. Laufdaten bleiben verfügbar.') : answers[1].value.truncated ? text('Only the first 256 model names are available. Older models may be missing.', 'Nur die ersten 256 Modellnamen sind verfügbar. Ältere Modelle können fehlen.') : ''
  }), { failed: () => { evidence.value = null; error.value = text('Couldn’t load this.', 'Konnte nicht geladen werden.') }, settled: () => { loading.value = false } })
}
watch(owner, () => {
  scope.reset(); folds.value = []; kind.value = ''; evidence.value = null; profiles.value = []; catalogNote.value = ''; error.value = ''; loading.value = false
  try { const saved = JSON.parse(localStorage.getItem(storageKey.value) || '{}'); if (Array.isArray(saved.folds)) folds.value = saved.folds.filter((id: unknown) => ['more', 'proof', 'watch'].includes(String(id))); if (typeof saved.kind === 'string' && saved.kind.length <= 48) kind.value = saved.kind } catch { /* Start closed. */ }
}, { immediate: true, flush: 'sync' })
watch([kind, () => props.project, () => folds.value.includes('more') && folds.value.includes('proof')], ([,,open]) => { lane.cancel(); evidence.value = null; loading.value = false; if (open) load() }, { immediate: true })
const stop = onAccessChange(() => { scope.reset(); evidence.value = null; loading.value = false; if (folds.value.includes('proof') && can('models.read', props.project)) load() })
onBeforeUnmount(() => { scope.dispose(); stop() })
const groups = computed(() => [['run', text('Runs this page drives', 'Läufe, die diese Seite steuert')], ['harness', text('Runs started by the Lead’s dispatcher', 'Läufe, die der Lead-Dispatcher startet')]].map(([source, label]) => ({ source, label, items: evidence.value?.items.filter(item => item.source === source) ?? [] })))
function modelLabel(item: BoardEvidence['items'][number]) {
  if (!item.actual_profile_id) return text('No model recorded', 'Kein Modell erfasst')
  const profile = profiles.value.find(profile => profile.id === item.actual_profile_id)
  return profile ? [profile.display_name || profile.model, profile.model_version, profile.effort].filter(Boolean).join(' · ') : text('Recorded model is unavailable in the catalog', 'Erfasstes Modell ist im Katalog nicht verfügbar')
}
function personLabel(person: string | null) { return person && person === (props.person || session.identity?.principal.id) ? session.identity!.principal.name : person ? text('another person', 'eine andere Person') : text('workspace default', 'Vorgabe des Arbeitsbereichs') }
function chosenBy(item: BoardEvidence['items'][number]) { return item.preference.lock?.why || (item.preference.preference_of?.source === 'person' ? text('Personal board', 'Persönliches Board') : item.preference.preference_of?.source === 'workspace' ? text('Workspace default', 'Vorgabe des Arbeitsbereichs') : text('No preference recorded', 'Keine Präferenz erfasst')) }
</script>
<template>
  <section id="models-proof" class="proof glass-card">
    <button type="button" class="fold" data-proof-fold :aria-expanded="folds.includes('more')" aria-controls="models-proof-body" @click="toggle('more')"><AppIcon name="chevron-right" :size="14" :class="{ open: folds.includes('more') }" /><b>{{ text('Proof and freshness', 'Nachweis und Aktualität') }}</b><HoverText :text="text('Run records · catalog · Model watch', 'Laufdaten · Katalog · Modellbeobachtung')" /></button>
    <div v-if="folds.includes('more')" id="models-proof-body">
      <button type="button" class="fold" :aria-expanded="folds.includes('proof')" aria-controls="models-runs" @click="toggle('proof')"><AppIcon name="chevron-right" :size="14" :class="{ open: folds.includes('proof') }" /><b>{{ text('What actually ran', 'Was tatsächlich lief') }}</b></button>
      <div v-if="folds.includes('proof')" id="models-runs" class="fold-body">
        <div class="proof-tools"><label>{{ text('Kind of work', 'Art der Arbeit') }}<select v-model="kind" data-proof-kind @change="persist"><option value="">{{ text('All', 'Alle') }}</option><option v-for="column in columns.filter(column => !column.column.startsWith('review:') && column.column !== 'concept')" :key="column.column" :value="column.column">{{ localizeColumn(column, german).label }}</option></select></label><button v-if="evidence?.next_cursor" type="button" class="btn sm" :disabled="loading" @click="load(evidence.next_cursor!)">{{ text('Next 50 records', 'Nächste 50 Einträge') }}</button><button v-if="error" type="button" class="btn sm" @click="load()">{{ text('Try again', 'Erneut versuchen') }}</button></div>
        <p v-if="error" role="alert">{{ error }}</p><p v-else-if="loading" role="status">{{ text('Loading run records…', 'Laufdaten werden geladen…') }}</p>
        <template v-else><p v-if="catalogNote" class="small" role="status">{{ catalogNote }}</p><section v-for="group in groups" :key="group.source" class="run-group"><h3>{{ group.label }}</h3><p class="small">{{ group.source === 'run' ? text('PAIMOS run records · for whom, and whose board or which rule chose', 'PAIMOS-Laufdaten · für wen, und wessen Board oder welche Regel wählte') : text('Harness sessions · reported by the launcher · not driven by this page until Engine Wave 2', 'Harness-Sitzungen · vom Starter gemeldet · bis Engine Wave 2 nicht von dieser Seite gesteuert') }}</p><p v-if="!group.items.length" class="empty">{{ group.source === 'run' ? text('No PAIMOS-dispatched runs recorded yet', 'Noch keine von PAIMOS gestarteten Läufe') : text('No harness sessions recorded yet', 'Noch keine Harness-Sitzungen erfasst') }}</p><ol v-else class="runs"><li v-for="item in group.items" :key="item.id"><time>{{ new Date(item.at).toLocaleString(german ? 'de' : 'en') }}</time><span>{{ columns.find(column => column.column === item.kind) ? localizeColumn(columns.find(column => column.column === item.kind)!, german).label : item.kind }} · {{ text('for', 'für') }} {{ personLabel(item.for_person) }}</span><span>{{ modelLabel(item) }}</span><span>{{ chosenBy(item) }}</span><span>{{ item.agreement === 'matches' ? text('Agrees with the recorded choice', 'Stimmt mit der erfassten Auswahl überein') : item.agreement === 'differs' ? text('Differs', 'Weicht ab') : text('Agreement not verified', 'Übereinstimmung nicht geprüft') }}</span></li></ol></section><p v-if="evidence?.next_cursor" class="small">{{ text('More records are available. This page shows up to 50.', 'Weitere Einträge sind verfügbar. Diese Seite zeigt bis zu 50.') }}</p></template>
      </div>
      <button type="button" class="fold" :aria-expanded="folds.includes('watch')" aria-controls="models-watch" @click="toggle('watch')"><AppIcon name="chevron-right" :size="14" :class="{ open: folds.includes('watch') }" /><b>{{ text('Model watch', 'Modellbeobachtung') }}</b></button>
      <div v-if="folds.includes('watch')" id="models-watch" class="fold-body"><p>{{ text('Model watch proposals for ranked boards are not available in this release.', 'Vorschläge der Modellbeobachtung für geordnete Boards sind in diesem Release noch nicht verfügbar.') }}</p><p class="small">{{ text('Later: tweaks learned from 4 weeks of runs.', 'Später: Feinschliff aus 4 Wochen Läufen.') }}</p></div>
      <p class="links small"><RouterLink to="/settings/accounts">{{ text('Accounts and computers', 'Konten und Computer') }}</RouterLink> · <RouterLink to="/settings/policies">{{ text('Policies', 'Richtlinien') }}</RouterLink> · <RouterLink to="/agents">{{ text('Agents header', 'Agenten-Kopf') }}</RouterLink></p>
    </div>
  </section>
</template>
<style scoped>.proof { padding: 12px 20px; min-width: 0; }.fold { display: flex; align-items: center; gap: 10px; min-height: 44px; width: 100%; padding: 8px 0; text-align: left; border: 0; background: transparent; color: var(--ink); }.fold b { font-size: 14px; }.fold > span { margin-left: auto; font-size: 12px; color: var(--ink-3); }.fold svg { flex: none; color: var(--ink-3); }.fold svg.open { transform: rotate(90deg); }.fold:focus-visible { outline: none; box-shadow: var(--focus-ring); }.fold-body { padding: 12px 0 20px; font-size: 13px; line-height: 1.5; }.proof-tools { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; }.proof-tools label { display: flex; align-items: center; gap: 8px; }.proof-tools select { min-height: 44px; max-width: 100%; color: var(--ink); background: var(--surface-raised); border: 1px solid var(--line); border-radius: 8px; }.run-group { margin-top: 20px; }.run-group h3 { font-size: 14px; }.small, .empty { color: var(--ink-3); font-size: 12px; }.empty { padding: 20px 0; }.runs { list-style: none; padding: 0; }.runs li { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 4px 12px; padding: 12px 0; border-bottom: 1px solid var(--line); overflow-wrap: anywhere; }.links { padding: 12px 0 4px; }.links a { color: var(--teal-ink); }@media (max-width: 600px) { .proof { padding-inline: 16px; }.fold > span { display: none; }.proof-tools label { display: grid; width: 100%; }.runs li { grid-template-columns: minmax(0, 1fr); } }</style>
<style scoped src="../../../styles/settingsButtons.css"></style>
