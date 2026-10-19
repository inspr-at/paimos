<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useSession } from '../../stores/session'
import { useProfile } from '../../stores/profile'
import { displayLanguage } from '../../lib/displayLanguage'
import { can, onAccessRevalidate } from '../../lib/authz'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { APIError } from '../../lib/api'
import { checkCount, definitionDate, definitionAdminNames, readWorkspaceDefinitionContext, saveWorkspaceDefinition, subscribeDefinitionChanges, type DefinitionChecks, type WorkspaceDefinitionContext } from '../../lib/definitions'
import { toast } from '../../lib/toast'
import AppIcon from '../AppIcon.vue'
import SettingsCard from './SettingsCard.vue'
import DefinitionEditor from './DefinitionEditor.vue'

const session = useSession(), profile = useProfile(), scope = useIdentityScope(), reads = scope.lane()
const context = ref<WorkspaceDefinitionContext | null>(null), busy = ref(false), loading = ref(true), error = ref(''), stale = ref(false), open = ref(false), moreBusy = ref(false)
const editor=ref<InstanceType<typeof DefinitionEditor>>()
const language = computed(() => displayLanguage(profile.profile?.locale, 'definitions'))
const t = (en: string, de: string) => language.value === 'de' ? de : en
const editable = computed(() => !!context.value?.can_manage && session.identity?.principal.kind === 'person' && can('settings.manage') && !stale.value)
const checks = computed(() => context.value?.definition?.checks ?? {})
const admins = computed(() => definitionAdminNames(context.value?.admins, context.value?.admins_more, language.value))
const people = computed(() => context.value?.admins.map(person => ({...person,kind:'person' as const})) ?? [])
const empty = computed(() => !checkCount(checks.value))
function failed(problem: unknown) {
  error.value = problem instanceof APIError && problem.status === 409 ? t('The workspace default changed. Reload before trying again.','Der Workspace-Standard hat sich geändert. Vor einem erneuten Versuch neu laden.') : problem instanceof Error ? problem.message : t('The workspace default could not be read.','Der Workspace-Standard konnte nicht gelesen werden.')
  if (problem instanceof APIError && [403,409].includes(problem.status)) stale.value = true
  if (context.value && !editor.value?.formOpen) {
    const owner=scope.owner.value
    toast(error.value,{tone:'error',key:'definitions/workspace',action:{label:t('Reload','Neu laden'),run:()=>{if(scope.owner.value===owner&&!busy.value)load()}}})
  }
}
function load() {
  if (busy.value) return
  loading.value = true; error.value = ''
  void reads.run(({after,signal}) => after(readWorkspaceDefinitionContext(undefined,signal),saved => {context.value=saved;stale.value=false}),{failed,settled:()=>{loading.value=false}})
}
function save(next: DefinitionChecks, applied: () => void) {
  const captured = context.value
  if (!captured || !editable.value || busy.value) return
  busy.value=true;error.value=''
  void scope.run(async ({after,signal}) => {
    let savedContext: WorkspaceDefinitionContext | undefined
    await after(saveWorkspaceDefinition(next,captured.definition,signal),saved => {
      if (context.value !== captured) return
      context.value={...captured,definition:saved};savedContext=context.value;applied()
    })
    if (!savedContext) return
    loading.value=true
    await after(readWorkspaceDefinitionContext(undefined,signal),saved => {
      if (context.value !== savedContext) return
      context.value=saved;toast(t('Workspace default saved.','Workspace-Standard gespeichert.'),{key:'definitions/workspace'})
    })
  },{failed,settled:()=>{busy.value=false;loading.value=false}})
}
function more() {
  const captured=context.value,cursor=captured?.next_cursor
  if (!captured || !cursor || moreBusy.value || loading.value || busy.value) return
  moreBusy.value=true;error.value=''
  void scope.run(({after,signal}) => after(readWorkspaceDefinitionContext(cursor,signal),saved => {
    if (context.value !== captured) return
    if (saved.definition?.revision !== captured.definition?.revision) {stale.value=true;error.value=t('The default changed. Reload the project list.','Der Standard hat sich geändert. Projektliste neu laden.');return}
    context.value={...saved,projects:[...captured.projects,...saved.projects]}
  }),{failed,settled:()=>{moreBusy.value=false}})
}
let stopLive: (() => void) | undefined
watch(scope.owner,()=>{stopLive?.();scope.reset();context.value=null;busy.value=false;moreBusy.value=false;open.value=false;stale.value=false;if(scope.owner.value){load();stopLive=subscribeDefinitionChanges(()=>{if(!busy.value&&!loading.value&&!editor.value?.formOpen)load()})}},{immediate:true,flush:'sync'})
const stopAccess=onAccessRevalidate({reset:()=>{context.value=null;busy.value=false;stale.value=true;open.value=false},refresh:load})
onBeforeUnmount(()=>{stopLive?.();stopAccess()})
</script>

<template>
  <SettingsCard :title="t('Definitions','Definitionen')" icon="check" anchor="definitions" variant="project" class="workspace-definitions">
    <template #lead>{{ t('The checks a ticket must pass before it enters In progress, QA or Done. Other statuses have none.','Die Prüfungen vor dem Wechsel eines Tickets zu In Arbeit, QA oder Erledigt. Für andere Status gibt es keine.') }}</template>
    <p v-if="context && !editable" class="lock-line"><AppIcon name="lock" :size="14" />{{ t(`Admins change the workspace default${admins ? `: ${admins}` : ''}. These checks are read-only.`,`Administratoren ändern den Workspace-Standard${admins ? `: ${admins}` : ''}. Die Prüfungen sind schreibgeschützt.`) }}</p>
    <div class="origin" :aria-busy="loading || busy"><AppIcon :name="empty ? 'minus' : 'folders'" :size="16" /><div><b>{{ empty ? t('No workspace default','Kein Workspace-Standard') : t('The workspace default','Der Workspace-Standard') }}</b><p>{{ empty ? t('No workspace default. Tickets move freely in every project that has no definition of its own.','Kein Workspace-Standard. Tickets bewegen sich frei in jedem Projekt ohne eigene Definition.') : t(`For every project without a definition of its own. Changed on ${definitionDate(context?.definition?.revision ?? '',language)}.`,`Für jedes Projekt ohne eigene Definition. Geändert am ${definitionDate(context?.definition?.revision ?? '',language)}.`) }}</p></div></div>
    <p v-if="!context && !loading && error" class="error" role="alert">{{ error }} <button type="button" class="link-btn" :disabled="busy" @click="load">{{ t('Reload','Neu laden') }}</button></p>
    <p v-else-if="!context && loading" role="status">{{ t('Reading the definition…','Definition wird gelesen…') }}</p>
    <DefinitionEditor v-if="context" ref="editor" :key="scope.owner.value" :checks="checks" project-key="Workspace" scope="workspace" :people="people" :editable="editable" :busy="busy" :error="error" @change="save" />
    <div v-if="context" class="differ">
      <p><AppIcon name="folder" :size="14" /><button type="button" class="link-btn differ-toggle" :aria-expanded="open" aria-controls="definition-project-differences" :disabled="loading || busy" @click="open=!open">{{ t(`${context.differ_count}\u00a0${context.differ_count === 1 ? 'project differs' : 'projects differ'}`,`${context.differ_count}\u00a0${context.differ_count === 1 ? 'Projekt weicht ab' : 'Projekte weichen ab'}`) }}<AppIcon name="chevron-right" :size="12" :class="{ expanded:open }" /></button><span v-if="context.uses_default.length">· <template v-for="(key, index) in context.uses_default" :key="key"><template v-if="index">, </template><span class="project-key">{{ key }}</span></template> {{ t(`${context.uses_default.length === 1 ? 'uses' : 'use'} this default`,`${context.uses_default.length === 1 ? 'nutzt' : 'nutzen'} diesen Standard`) }}</span></p>
      <ul v-if="open" id="definition-project-differences"><li v-for="project in context.projects" :key="project.project_id"><RouterLink :to="`/p/${encodeURIComponent(project.key)}/settings/definitions`"><b class="project-key">{{ project.key }}</b><span>{{ project.pending_count ? t(`${project.pending_count}\u00a0${project.pending_count === 1 ? 'change waits' : 'changes wait'}`,`${project.pending_count}\u00a0${project.pending_count === 1 ? 'Änderung wartet' : 'Änderungen warten'}`) : t(`Differs in ${project.difference_count}\u00a0${project.difference_count === 1 ? 'place' : 'places'}`,`Weicht an ${project.difference_count}\u00a0${project.difference_count === 1 ? 'Stelle' : 'Stellen'} ab`) }}</span><span class="open">{{ t('Open','Öffnen') }}<AppIcon name="chevron-right" :size="12" /></span></RouterLink></li><li v-if="context.next_cursor"><button type="button" class="link-btn" :disabled="moreBusy" @click="more">{{ t('More projects','Weitere Projekte') }}</button></li></ul>
    </div>
  </SettingsCard>
</template>

<style scoped>
.workspace-definitions { container: project-settings-body / inline-size; }.origin { display:grid;grid-template-columns:28px minmax(0,1fr);gap:12px;padding:14px 2px;border-top:1px solid var(--line);font-size:12.5px;line-height:1.55;color:var(--ink-2); }.origin > svg { justify-self:center;color:var(--ink-3);margin-top:3px; }.origin b {font-size:13.5px;font-weight:600;color:var(--ink)}.origin p {margin-top:3px}.lock-line {display:flex;gap:8px;font-size:12.5px;line-height:1.5;color:var(--ink-3);margin-bottom:12px}.lock-line svg{flex:none;margin-top:2px}.link-btn{display:inline-flex;align-items:center;gap:4px;padding:0;border:0;background:transparent;color:var(--ink-2);font-size:12.5px;min-height:32px;white-space:normal;text-align:start}.link-btn:hover,.differ a:hover{color:var(--teal-ink)}.error{font-size:12.5px;color:var(--danger-ink);line-height:1.5}.differ{margin-top:14px;font-size:12.5px;color:var(--ink-3)}.differ > p{display:flex;flex-wrap:wrap;align-items:center;gap:4px 8px}.differ-toggle{font-weight:500}.expanded{transform:rotate(90deg)}.differ ul{list-style:none;padding:0;margin:4px 0 0;border-top:1px solid var(--line)}.differ li{border-bottom:1px solid var(--line)}.differ a{display:grid;grid-template-columns:auto minmax(0,1fr) auto;align-items:center;gap:12px;min-height:44px;padding:6px 2px;color:var(--ink-2);text-decoration:none}.differ b{font-weight:600}.project-key{white-space:nowrap;overflow-wrap:normal}.open{display:inline-flex;align-items:center;gap:4px;justify-self:end}.differ a:focus-visible,.link-btn:focus-visible{outline:none;box-shadow:var(--focus-ring);border-radius:4px}@media(pointer:coarse),(max-width:600px){.link-btn{min-height:44px}}
</style>
