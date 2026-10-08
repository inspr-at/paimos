<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '../AppIcon.vue'
import SettingsDockedPanel from './SettingsDockedPanel.vue'
import SettingsPopover, { type SettingsMenuItem } from './SettingsPopover.vue'
import ModelBoard from './models/ModelBoard.vue'
import TemplatePicker from './models/TemplatePicker.vue'
import KnobRow from './models/KnobRow.vue'
import NextSentence from './models/NextSentence.vue'
import WhyPanel from './models/WhyPanel.vue'
import ProofFreshness from './models/ProofFreshness.vue'
import { useSession } from '../../stores/session'
import { useProjects } from '../../stores/projects'
import { can } from '../../lib/authz'
import { createScope, scopeOwner } from '../../lib/identityScope'
import { useModelsBoard } from '../../lib/useModelsBoard'
import { getBoard, getBoardCoverage, resolveBoardModel } from '../../lib/modelsBoardApi'
import { lineName, localizeColumn, type BoardContext, type BoardMode, type ModelBoardDocument } from '../../lib/modelsBoard'
import type { BoardCoverage, ModelResolution } from '../../lib/modelsSettings'
const route = useRoute(), router = useRouter(), session = useSession(), projects = useProjects()
const german = computed(() => route.query.lang === 'de' || document.documentElement.lang.startsWith('de'))
const text = (en: string, de: string) => german.value ? de : en
const owner = computed(() => scopeOwner(session.identity))
function urlContext(): BoardContext { return { layer: route.query.layer === 'default' || route.query.layer === 'rules' ? route.query.layer : 'mine', situation: route.query.situation === 'fix' || route.query.situation === 'stuck' ? route.query.situation : 'first', ...(typeof route.query.project_id === 'string' ? { project: route.query.project_id } : {}) } }
const context = ref<BoardContext>(urlContext())
watch(() => [route.query.layer, route.query.project_id, route.query.situation], () => { context.value = urlContext() })
const editor = useModelsBoard(context, german), { document: board, loading, busy, error, editable } = editor
// Linking can change the canonical person while the login principal stays the
// same. Browser-only choices belong to the canonical person when known.
const canonicalPerson = ref<string | null>(null)
watch(owner, () => { canonicalPerson.value = null }, { flush: 'sync' })
watch(board, value => { if (value?.layer === 'mine' && value.person_id) canonicalPerson.value = value.person_id }, { immediate: true, flush: 'sync' })
const stateOwner = computed(() => session.identity ? `${session.identity.tenant.id}/${canonicalPerson.value || session.identity.principal.id}` : '')
const workspace = ref<ModelBoardDocument | null>(null), coverage = ref<BoardCoverage | null>(null), coverageError = ref('')
const managedCoverage = computed(() => coverage.value?.consumers.filter(consumer => consumer.reads_board && consumer.consumer !== 'lead_harness') ?? [])
const coverageLabels = computed(() => managedCoverage.value.map(consumer => ({ managed_build: text('Builds', 'Builds'), managed_review: text('Reviews', 'Prüfungen'), queue: text('Queued work', 'Arbeit aus der Warteschlange') }[consumer.consumer] || text('Other managed runs', 'Andere verwaltete Läufe'))).join(' · '))
const mode = ref<BoardMode>('auto')
const isMode = (value: unknown): value is BoardMode => value === 'auto' || value === 'simple' || value === 'expert'
// A copied URL can choose a view. A URL left by another person in this tab
// must not replace the new person's browser preference when Settings remounts.
const urlModeOwner = () => typeof window === 'undefined' ? undefined : window.history?.state?.modelsModeOwner
let requestedMode = isMode(route.query.mode) && (!urlModeOwner() || urlModeOwner() === owner.value) ? route.query.mode : undefined, restoredPerson = false
const modeKey = computed(() => `models-page/${stateOwner.value}/mode`)
watch(stateOwner, () => { mode.value = 'auto'; try { const stored = localStorage.getItem(modeKey.value); if (isMode(stored)) mode.value = stored } catch { /* Optional browser preference. */ }; if (!restoredPerson && requestedMode) mode.value = requestedMode; if (canonicalPerson.value) restoredPerson = true; if (owner.value) setMode(mode.value, false) }, { immediate: true, flush: 'sync' })
watch(owner, (value, previous) => { if (previous && value !== previous) requestedMode = undefined }, { flush: 'sync' })
watch(() => route.query.mode, value => { if (isMode(value) && (!urlModeOwner() || urlModeOwner() === owner.value)) { mode.value = value; if (owner.value) try { localStorage.setItem(modeKey.value, value) } catch { /* Optional browser preference. */ } } })
// A replace is a navigation: it refreshes access, reloads the board and closes a Why
// deep link that just opened, and it must keep the hash. The URL is rewritten only when
// a person chooses a view or it names another one; a matching URL just gets its owner.
function syncModeUrl(value: BoardMode, chosen: boolean) {
  const named = route.query.mode
  if (!owner.value || (named === undefined && !chosen)) return
  if (named !== value) { void router.replace({ query: { ...route.query, mode: value }, hash: route.hash, state: { modelsModeOwner: owner.value } }); return }
  if (urlModeOwner() !== owner.value && typeof window !== 'undefined') window.history.replaceState({ ...window.history.state, modelsModeOwner: owner.value }, '')
}
function setMode(value: BoardMode, chosen = true) { mode.value = value; if (!owner.value) return; try { localStorage.setItem(modeKey.value, value) } catch { /* Optional browser preference. */ }; syncModeUrl(value, chosen) }
const columns = computed(() => board.value?.columns.map(column => localizeColumn(column, german.value)) ?? [])
const orderNames = (lines: string[]) => lines.map(line => lineName({ line, version: '' }))
const inherited = computed(() => context.value.layer === 'mine' && (!board.value?.profile.template || board.value.profile.scope === 'workspace'))
const template = computed(() => board.value?.profile.template ?? workspace.value?.profile.template ?? 'balanced')
const thinking = computed(() => board.value?.profile.thinking ?? workspace.value?.profile.thinking ?? 'standard')
const usage = computed(() => board.value?.profile.usage ?? workspace.value?.profile.usage ?? 'balanced')
const own = computed(() => columns.value.filter(column => column.source === 'own').map(column => column.label))
const profileEditable = computed(() => editable.value && !!board.value && context.value.layer !== 'rules')
const projectOptions = computed(() => projects.projects.filter(project => !project.archived).map(project => ({ id: project.id, name: project.title })))
const projectLabel = computed(() => projectOptions.value.find(project => project.id === context.value.project)?.name || context.value.project || text('any project', 'irgendeinem Projekt'))
const nextKind = computed(() => typeof route.query.kind === 'string' && columns.value.some(column => column.column === route.query.kind) ? route.query.kind : 'other')
const nextColumn = computed(() => columns.value.find(column => column.column === nextKind.value))
const resolution = ref<ModelResolution | null>(null), reviewer = ref<ModelResolution | null>(null), resolutionError = ref(''), resolving = ref(false)
const person = computed(() => session.identity?.principal.name || text('workspace default', 'Vorgabe des Arbeitsbereichs'))
const scope = createScope(() => session.authenticationCurrent() && can('models.read', context.value.project) ? owner.value : ''), previewLane = scope.lane(), factsLane = scope.lane()
function loadPreview() {
  resolution.value = null; reviewer.value = null; resolutionError.value = ''; resolving.value = !!board.value
  if (!board.value) return
  // Resolve the actual person's next run, including when inspecting rules or the
  // default. The API has no parameter that selects a hypothetical default owner.
  const query: Record<string, string> = { role: 'build', area: nextKind.value, situation: 'first', ...(context.value.project ? { project_id: context.value.project } : {}) }
  if (typeof route.query.ticket === 'string') query.ticket = route.query.ticket
  void previewLane.run(async ({ after, signal }) => {
    const family = await after(resolveBoardModel(query, signal), result => { resolution.value = result; return result.profile?.family })
    if (family) await after(resolveBoardModel({ ...query, role: 'review-gate', author_family: family }, signal), result => { reviewer.value = result })
  }, { failed: () => { resolutionError.value = text('The next model could not be checked. Try reloading.', 'Das nächste Modell konnte nicht geprüft werden. Erneut laden.') }, settled: () => { resolving.value = false } })
}
watch(() => [editor.actionKey.value, board.value?.revision, nextKind.value, route.query.ticket], () => {
  previewLane.cancel(); loadPreview()
})
watch(editor.actionKey, () => {
  scope.reset(); workspace.value = null; coverage.value = null; coverageError.value = ''
  if (!owner.value || !can('models.read', context.value.project)) return
  const target = { ...context.value, layer: 'default' as const }
  void factsLane.run(async ({ after, signal }) => {
    await after(Promise.allSettled([getBoard(target, signal), getBoardCoverage(signal)]), answers => {
      if (answers[0].status === 'fulfilled') workspace.value = answers[0].value
      if (answers[1].status === 'fulfilled') coverage.value = answers[1].value
      else coverageError.value = text('Coverage could not be loaded.', 'Die Geltung konnte nicht geladen werden.')
    })
  }, { failed: () => { coverageError.value = text('Coverage could not be loaded.', 'Die Geltung konnte nicht geladen werden.') } })
}, { immediate: true, flush: 'sync' })
const anchor = ref<HTMLElement | null>(null), templateOpen = ref(false), templatePreview = ref<Awaited<ReturnType<typeof editor.previewTemplate>>>(null)
async function chooseTemplate(value: 'best' | 'balanced' | 'save', event: MouseEvent) {
  const trigger = event.currentTarget as HTMLElement
  templateOpen.value = false
  const preview = await editor.previewTemplate(value)
  if (preview && trigger.isConnected && preview.key === editor.actionKey.value) { anchor.value = trigger; templatePreview.value = preview; templateOpen.value = true }
}
async function applyTemplate() { if (templatePreview.value && await editor.applyTemplate(templatePreview.value)) templateOpen.value = false }
const menuOpen = ref(false), menu = ref<'kind' | 'project'>('kind')
const menuItems = computed<SettingsMenuItem[]>(() => menu.value === 'kind' ? columns.value.filter(column => !column.column.startsWith('review:') && column.column !== 'concept').map(column => ({ id: column.column, label: column.label, detail: column.sentence })) : [{ id: '', label: text('Any project', 'Jedes Projekt') }, ...projectOptions.value.map(project => ({ id: project.id, label: project.name }))])
function openMenu(type: 'kind' | 'project', event: MouseEvent) { menu.value = type; anchor.value = event.currentTarget as HTMLElement; menuOpen.value = true }
async function select(id: string) { menuOpen.value = false; await router.replace({ query: { ...route.query, ...(menu.value === 'kind' ? { kind: id, ticket: undefined } : { project_id: id || undefined, ticket: undefined }) } }) }
const whyOpen = ref(false), whyOpener = ref<HTMLElement | null>(null)
function showWhy(event: MouseEvent) { whyOpener.value = event.currentTarget as HTMLElement; whyOpen.value = true }
watch(editor.actionKey, () => { templateOpen.value = false; menuOpen.value = false; whyOpen.value = false; templatePreview.value = null }, { flush: 'sync' })
let deepWhyShown = false
watch(board, value => { if (value && route.query.why === '1' && !deepWhyShown) { deepWhyShown = true; whyOpen.value = true } })
onMounted(() => { void projects.load(); if (route.path === '/settings/models') setMode(mode.value, false) })
onBeforeUnmount(() => scope.dispose())
</script>
<template>
  <SettingsDockedPanel v-model:open="whyOpen" :title="text('Why this model?', 'Warum dieses Modell?')" :opener="whyOpener" :context-key="editor.actionKey.value" layout-frame-selector=".layout">
    <section class="models-section" data-models-section :aria-busy="loading || busy">
      <div class="toolbar"><div class="mode-seg" role="group" :aria-label="text('How much to show', 'Wie viel zeigen')" data-mode-group><button v-for="[value, en, de] in [['auto', 'Auto', 'Auto'], ['simple', 'Simple', 'Einfach'], ['expert', 'Expert', 'Experte']]" :key="value" type="button" :data-models-mode="value" :aria-pressed="mode === value" @click="setMode(value as BoardMode)">{{ text(en!, de!) }}</button></div></div>
      <section id="models" class="models-card glass-card" :data-models-ready="!!board" :aria-label="text('Models', 'Modelle')">
        <div class="profile-controls" :class="{ compact: mode !== 'auto' }"><TemplatePicker :value="template" :own="own" :inherited="inherited" :compact="mode !== 'auto'" :editable="profileEditable" :busy="busy" :german="german" @choose="chooseTemplate" /><KnobRow :thinking="thinking" :usage="usage" :compact="mode !== 'auto'" :editable="profileEditable" :busy="busy" :german="german" @thinking="editor.profile({ thinking: $event })" @usage="editor.profile({ usage: $event })" /></div>
        <div class="feedback" aria-live="polite"><p v-if="error" role="alert">{{ error }}</p></div>
        <NextSentence :kind="nextColumn?.label || text('Everything else', 'Alles andere')" :kind-definition="nextColumn?.sentence || ''" :kind-labels="columns.filter(column => !column.column.startsWith('review:') && column.column !== 'concept').map(column => column.label)" :project="projectLabel" :project-labels="[text('Any project', 'Jedes Projekt'), ...projectOptions.map(project => project.name)]" :person="person" :resolution="resolution" :reviewer="reviewer" :loading="resolving" :error="resolutionError" :german="german" @kind="openMenu('kind', $event)" @project="openMenu('project', $event)" @why="showWhy" />
        <div v-if="!board" class="load-state"><p role="status">{{ loading ? text('Loading the model board…', 'Modellboard wird geladen…') : text('The model board is unavailable.', 'Das Modellboard ist nicht verfügbar.') }}</p><button v-if="!loading" type="button" class="btn" @click="editor.load()">{{ text('Try again', 'Erneut versuchen') }}</button></div>
        <ModelBoard v-if="mode !== 'auto'" :expert="mode === 'expert'" :german="german" :context="context" :controller="editor" @context="context = $event" />
        <p v-if="board?.needs_you.length" class="needs" role="status"><AppIcon name="info" :size="14" />{{ text('The migrated kinds of work need a check of their definitions.', 'Die übernommenen Arten von Arbeit brauchen eine Prüfung ihrer Definitionen.') }} <RouterLink to="/settings/kinds">{{ text('Kinds of work', 'Arten von Arbeit') }}</RouterLink></p>
      </section>
      <section class="where" :aria-label="text('Where this applies', 'Wo das gilt')"><p v-if="coverageError" role="status">{{ coverageError }}</p><template v-else-if="coverage"><p class="coverage-row"><AppIcon :name="managedCoverage.length ? 'check' : 'info'" :size="14" /><b>{{ managedCoverage.length ? text('Runs PAIMOS starts follow this page', 'Läufe, die PAIMOS startet, folgen dieser Seite') : text('No managed runs report reading this page yet', 'Noch keine verwalteten Läufe melden, diese Seite zu lesen') }}</b><span>{{ coverageLabels }}</span></p><p class="coverage-row"><AppIcon :name="coverage.consumers.find(consumer => consumer.consumer === 'lead_harness')?.reads_board ? 'check' : 'info'" :size="14" /><b>{{ coverage.consumers.find(consumer => consumer.consumer === 'lead_harness')?.reads_board ? text('The Lead’s dispatcher too, since Engine Wave 2', 'Auch der Lead-Dispatcher, seit Engine Wave 2') : text('The Lead’s dispatcher does not, until Engine Wave 2', 'Der Lead-Dispatcher nicht, bis Engine Wave 2') }}</b></p></template><p v-else>{{ text('Checking where this applies…', 'Die Geltung wird geprüft…') }}</p></section>
      <p class="usage-link">{{ text('Usage per account, owners and floors:', 'Nutzung je Konto, Besitzer und Untergrenzen:') }} <RouterLink to="/settings/accounts">{{ text('Accounts and computers', 'Konten und Computer') }}</RouterLink></p>
      <ProofFreshness :columns="board?.columns || []" :person="canonicalPerson" :project="context.project" :german="german" />
    </section>
    <template #panel><WhyPanel :resolution="resolution" :reviewer="reviewer" :person="person" :error="resolutionError" :loading="resolving" :german="german" /></template>
  </SettingsDockedPanel>
  <SettingsPopover v-model:open="menuOpen" :anchor="anchor" :label="menu === 'kind' ? text('Kind of work', 'Art der Arbeit') : text('Project', 'Projekt')" :items="menuItems" :context-key="editor.actionKey.value" @select="select" />
  <SettingsPopover v-model:open="templateOpen" :anchor="anchor" :label="text('Apply template', 'Vorlage anwenden')" mode="form" :context-key="editor.actionKey.value" :hint="text('Columns you ordered yourself stay.', 'Selbst geordnete Spalten bleiben.')" :save-label="text('Apply', 'Anwenden')" :error="error" :busy="busy" @submit="applyTemplate"><p class="order-then"><span>{{ text('Now', 'Jetzt') }}</span><span class="sr-only">{{ text(' to ', ', ') }}</span><AppIcon name="arrow" :size="12" /><span>{{ text('After', 'Danach') }}</span></p><dl class="template-diff"><template v-for="change in templatePreview?.moved" :key="change.column"><dt>{{ columns.find(column => column.column === change.column)?.label || change.column }}</dt><dd><span class="order-chain"><template v-for="(name, index) in orderNames(change.before)" :key="`${change.column}-before-${index}`"><AppIcon v-if="index" name="arrow" :size="11" /><span v-if="index" class="sr-only">{{ text(' to ', ', ') }}</span>{{ name }}</template></span><br /><span class="order-chain"><template v-for="(name, index) in orderNames(change.after)" :key="`${change.column}-after-${index}`"><AppIcon v-if="index" name="arrow" :size="11" /><span v-if="index" class="sr-only">{{ text(' to ', ', ') }}</span>{{ name }}</template></span></dd></template></dl><p v-if="!templatePreview?.moved.length">{{ text('No column changes.', 'Keine Spaltenänderungen.') }}</p></SettingsPopover>
</template>
<style scoped>.usage-link { font-size: 13px; color: var(--ink-2); }.usage-link a { color: var(--teal-ink); }.models-section { display: grid; gap: 14px; min-width: 0; }.toolbar { display: flex; justify-content: space-between; gap: 16px; }.mode-seg { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); border: 1px solid var(--line); border-radius: 9px; overflow: hidden; }.mode-seg button { min-inline-size: 5em; min-height: 36px; padding: 6px 14px; background: transparent; border: 0; color: var(--ink-2); font-size: 13px; }.mode-seg button[aria-pressed="true"] { background: var(--row-selected); color: var(--ink); font-weight: 650; }.mode-seg button:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }.models-card { min-width: 0; padding: 20px; }.profile-controls.compact { display: flex; flex-wrap: wrap; gap: 10px 22px; align-items: flex-start; }.profile-controls.compact > :first-child { flex: 1 1 380px; }.profile-controls.compact > :last-child { flex: 1 1 520px; }.feedback { position: relative; height: 0; z-index: 4; }.feedback p { position: absolute; top: 4px; left: 0; padding: 8px 12px; background: var(--surface-raised); box-shadow: var(--shadow-pop); border-radius: 8px; border: 1px solid var(--line); color: var(--danger); font-size: 12px; max-width: 100%; }.where { display: grid; gap: 6px; padding: 12px 16px; border-radius: var(--radius); box-shadow: inset 0 0 0 1px var(--line); background: var(--surface-raised-2); font-size: 13px; color: var(--ink-2); }.coverage-row { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; }.coverage-row svg { flex: none; color: var(--ink-3); }.coverage-row b { font-weight: 600; color: var(--ink); }.coverage-row span { font-size: 12px; }.needs { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; padding-top: 12px; font-size: 12px; color: var(--ink-2); }.needs a { color: var(--teal-ink); }.load-state { padding: 20px 0; color: var(--ink-3); font-size: 13px; }.order-then, .order-chain { display: inline-flex; flex-wrap: wrap; align-items: center; gap: 4px; }.template-diff { font-size: 12px; line-height: 1.5; }.template-diff dt { margin-top: 8px; font-weight: 600; }.template-diff dd { margin: 4px 0; overflow-wrap: anywhere; color: var(--ink-2); }@media (max-width: 600px) { .models-card { padding: 16px; }.mode-seg button { min-height: 44px; } }</style>
<style scoped src="../../styles/settingsButtons.css"></style>
