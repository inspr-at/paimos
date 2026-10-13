<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { Project } from '../../stores/projects'
import { useSession } from '../../stores/session'
import { can, onAccessRevalidate } from '../../lib/authz'
import { createScope, scopeOwner } from '../../lib/identityScope'
import { readDeliveryMetrics, type MetricSource } from '../../lib/deliveryNumbers'
import { HISTORY_DAYS, checkRepositoryHistory, linkProjectRepository, readRepositoryHistory, repositoryState, workflowFile, type HistoryPosition } from '../../lib/projectDeliverySettings'
import { useProjectSettingsOverlay } from '../../lib/projectSettings'
import { claimSettingsPopover } from '../../lib/settingsOverlays'
import { usePoller } from '../../lib/usePolledData'
import { toast } from '../../lib/toast'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import SettingsCard from './SettingsCard.vue'
import ProjectSettingsExplainer from './ProjectSettingsExplainer.vue'

const props = defineProps<{ project: Project }>()
const session = useSession(), route = useRoute(), router = useRouter()
const allowed = computed(() => can('delivery.read', props.project.id)), manage = computed(() => can('delivery.manage', props.project.id))
const scope = createScope(() => scopeOwner(session.identity) ? `${scopeOwner(session.identity)}/${props.project.id}` : ''), reader = scope.lane()
const source = shallowRef<MetricSource | null>(null), position = shallowRef<HistoryPosition | null>(null)
const loading = ref(true), error = ref(''), busy = ref(false), feedback = ref(''), formOpen = ref(false)
const form = ref<HTMLFormElement>(), linkButton = ref<HTMLButtonElement>(), moreButton = ref<HTMLButtonElement>(), menuContent = ref<HTMLElement>()
const menuOpen = ref(false), repository = ref(''), ci = ref('ci.yml'), nightly = ref('nightly-full.yml'), days = ref(30)
const register = useProjectSettingsOverlay()
let release: (() => void) | undefined, unregister: (() => void) | undefined
const observedAt = ref(Date.now())
const state = computed(() => loading.value ? 'loading' : error.value ? 'error' : repositoryState(source.value, observedAt.value))
const repoOwner = computed(() => source.value?.repository.split('/')[0] ?? '')
const repoName = computed(() => source.value?.repository.split('/')[1] ?? '')
const appURL = computed(() => `https://github.com/organizations/${encodeURIComponent(repoOwner.value)}/settings/installations`)
const checked = computed(() => source.value?.last_read_error?.at ? `Checked ${date(source.value.last_read_error.at, true)}` : '')
const staleDays = computed(() => source.value?.last_event_at ? Math.floor((observedAt.value - Date.parse(source.value.last_event_at)) / 86_400_000) : 0)
const historyDays = computed(() => source.value?.backfill_since ? Math.max(1, Math.floor((observedAt.value - Date.parse(source.value.backfill_since)) / 86_400_000)) : days.value)
const progressDay = computed(() => position.value?.day ? Math.max(0, Math.min(historyDays.value, Math.floor((Date.parse(position.value.day) - Date.parse(position.value.since)) / 86_400_000))) : null)
const phases = [['runs', 'CI runs'], ['pulls', 'Pull requests'], ['jobs', 'Job results']] as const
function date(value: string, clock = false) { return new Date(value).toLocaleString('en-GB', { day: 'numeric', month: 'short', ...(clock ? { hour: '2-digit', minute: '2-digit' } : {}) }) }
function load(initial = false) {
  if (!allowed.value || busy.value) return Promise.resolve()
  if (initial) loading.value = true
  const id = props.project.id
  return reader.run(({ after, signal }) => after(readDeliveryMetrics(id, signal), value => {
    const firstLinkedRead = !source.value && !!value.source
    source.value = value.source; observedAt.value = Date.parse(value.generated_at) || Date.now(); error.value = ''
    if (firstLinkedRead && !formOpen.value) days.value = HISTORY_DAYS.reduce<number>((nearest, option) => Math.abs(option - historyDays.value) < Math.abs(nearest - historyDays.value) ? option : nearest, 30)
    if (value.source?.backfill === 'done') position.value = null
    if (route.query.link === '1' && !value.source && manage.value && !formOpen.value) openForm()
  }), { failed: () => { error.value = 'The repository link couldn’t load.' }, settled: () => { loading.value = false } })
}
function closeMenu(restore = true) {
  menuOpen.value = false; release?.(); release = undefined; unregister?.(); unregister = undefined
  if (restore) moreButton.value?.focus({ preventScroll: true })
}
async function openMenu() {
  if (menuOpen.value) { closeMenu(); return }
  release = claimSettingsPopover(() => closeMenu(false)); menuOpen.value = true
  unregister = register({ element: () => menuContent.value, close: closeMenu })
  await nextTick(); menuContent.value?.querySelector<HTMLButtonElement>('button')?.focus({ preventScroll: true })
}
async function openForm() {
  if (!manage.value || busy.value) return
  closeMenu(false); feedback.value = ''; formOpen.value = true
  repository.value = source.value?.repository ?? ''; ci.value = workflowFile(source.value?.ci_workflow ?? 'ci.yml'); nightly.value = workflowFile(source.value?.nightly_workflow ?? 'nightly-full.yml')
  days.value = HISTORY_DAYS.includes(historyDays.value as 7 | 30 | 60 | 90) ? historyDays.value : 30
  await nextTick(); form.value?.querySelector<HTMLInputElement>('input')?.focus({ preventScroll: true })
}
function cancelForm() { if (busy.value) return; formOpen.value = false; feedback.value = ''; void nextTick(() => linkButton.value?.focus({ preventScroll: true })) }
function startHistory(action: 'continue' | 'restart' | 'check' = 'continue') {
  if (!manage.value || !source.value || busy.value) return Promise.resolve()
  const id = props.project.id, selectedDays = days.value
  closeMenu(false); reader.cancel(); busy.value = true; feedback.value = ''
  return scope.run(({ after, signal }) => after(action === 'check' ? checkRepositoryHistory(id, signal) : readRepositoryHistory(id, selectedDays, action === 'restart', signal), value => {
    position.value = value
    if (value.truncated) toast('GitHub returned only part of the history. Delivery keeps those windows partial.')
  }), { failed: cause => { toast(cause instanceof Error ? cause.message : 'History could not be read. The repository stays linked.', { tone: source.value?.last_read_error ? 'info' : 'error', key: `repository-history/${id}` }) }, settled: () => { busy.value = false; void load() } })
}
function submit() {
  if (!manage.value || busy.value) return
  const repo = repository.value.trim(), ciFile = ci.value.trim(), nightlyFile = nightly.value.trim()
  if (!/^[A-Za-z0-9][A-Za-z0-9-]{0,38}\/[A-Za-z0-9_.-]{1,100}$/.test(repo) || !/^[A-Za-z0-9._-]{1,100}\.ya?ml$/.test(ciFile) || !/^[A-Za-z0-9._-]{1,100}\.ya?ml$/.test(nightlyFile)) {
    feedback.value = 'Enter owner/name and workflow filenames ending in .yml or .yaml.'; return
  }
  const id = props.project.id, input = { repository: repo, ci_workflow: `.github/workflows/${ciFile}`, nightly_workflow: `.github/workflows/${nightlyFile}`, days: days.value }
  reader.cancel(); busy.value = true; feedback.value = ''
  void scope.run(({ after, signal }) => after(linkProjectRepository(id, input, signal), value => {
    source.value = value; position.value = null; formOpen.value = false; error.value = ''; loading.value = false
    if (route.query.link) { const { link: _link, ...query } = route.query; void router.replace({ path: route.path, query, hash: route.hash }) }
  }), { failed: cause => { feedback.value = cause instanceof Error ? cause.message : 'The repository could not be linked. Nothing changed.' }, settled: () => { busy.value = false; if (!formOpen.value) void startHistory() } })
}
function reset() { scope.reset(); closeMenu(false); source.value = null; position.value = null; loading.value = true; error.value = ''; feedback.value = ''; busy.value = false; formOpen.value = false; repository.value = ''; ci.value = 'ci.yml'; nightly.value = 'nightly-full.yml'; days.value = 30 }
watch(() => [props.project.id, scopeOwner(session.identity), allowed.value], () => { reset(); void load(true) }, { immediate: true, flush: 'sync' })
watch(manage, value => { if (!value) { cancelForm(); closeMenu(false) } })
const poller = usePoller(() => load(), 15_000), stopAccess = onAccessRevalidate({ reset, refresh: () => { void load() } })
onMounted(() => poller.start())
onBeforeUnmount(() => { poller.stop(); stopAccess(); scope.dispose(); closeMenu(false) })
</script>

<template>
  <SettingsCard title="Repository" icon="link" anchor="repository" variant="project">
    <template #lead>The GitHub repository this project’s pull requests, CI runs and merges come from. Delivery counts from it.</template>
    <template #aside><ProjectSettingsExplainer id="events" :project-key="project.routeKey" :repository="source?.repository" /></template>
    <p v-if="!manage" class="lock-line"><AppIcon name="lock" :size="13" /><span>People who manage delivery for {{ project.routeKey }} link and change its repository.</span></p>
    <form v-if="formOpen" id="link-form" ref="form" class="link-form" data-project-field aria-label="Link a repository" novalidate @submit.prevent="submit">
      <p v-if="source" class="rule"><AppIcon name="info" :size="15" /><span>Changing the repository or a workflow reads history again from the start.</span></p>
      <div class="lf-row"><label class="fl" for="lf-repo"><b>Repository</b><small id="lf-repo-h">owner/name, as in its GitHub address</small></label><span class="pre-field"><span class="pre" aria-hidden="true">github.com/</span><input id="lf-repo" v-model="repository" class="field" maxlength="140" placeholder="owner/name" autocomplete="off" spellcheck="false" aria-describedby="lf-repo-h" :disabled="busy" /></span></div>
      <div class="lf-row"><span class="fl"><b>Workflows</b><small>Files in <span class="mono">.github/workflows/</span>, prefilled with the usual names. Change them only if this project’s differ.</small></span><div class="lf-two"><label class="lf-w"><span>CI</span><input id="lf-ci" v-model="ci" class="field" maxlength="105" spellcheck="false" aria-label="CI workflow file in .github/workflows/" :disabled="busy" /></label><label class="lf-w"><span>Nightly</span><input id="lf-nightly" v-model="nightly" class="field" maxlength="105" spellcheck="false" aria-label="Nightly workflow file in .github/workflows/" :disabled="busy" /></label></div></div>
      <div class="lf-row"><label class="fl" for="lf-days"><b>History</b><small>Read once on the server; after that PAIMOS follows along live.</small></label><select id="lf-days" v-model.number="days" class="field" :disabled="busy"><option v-for="n in HISTORY_DAYS" :key="n" :value="n">The last {{ n }} days</option></select></div>
      <div class="lf-acts"><button type="button" class="btn" :disabled="busy" @click="cancelForm">Cancel</button><button type="submit" class="btn primary" :disabled="busy"><AppIcon name="link" :size="14" />{{ source ? 'Save and read again' : 'Link repository' }}</button></div>
    </form>
    <div v-else class="repo-slot" :data-state="state" :aria-busy="busy">
      <div v-if="state === 'loading'" class="slot-sk" aria-busy="true" aria-label="Loading the repository"><div class="sk-row"><span class="sk" /><span class="sk" /></div><span class="sk ds-sk" /></div>
      <div v-else-if="state === 'error'" class="empty" role="alert"><span class="empty-ic"><AppIcon name="alert" :size="20" /></span><div><b>The repository link couldn’t load.</b><p>Nothing changed: delivery keeps its last state and the numbers stay as they were.</p></div><button type="button" class="btn" @click="load(true)"><AppIcon name="refresh" :size="14" />Try again</button></div>
      <div v-else-if="state === 'none'" class="empty"><span class="empty-ic"><AppIcon name="repo" :size="20" /></span><div><b>No repository linked yet</b><p>Link the project’s GitHub repository and Delivery starts counting. The server reads the last 30 days once, then follows along live.</p></div><button v-if="manage" ref="linkButton" type="button" class="btn primary" data-act="link" @click="openForm"><AppIcon name="link" :size="14" />Link repository</button></div>
      <template v-else-if="source">
        <div class="repo-id"><span class="repo-ic"><AppIcon name="repo" :size="17" /></span><span class="who"><b>{{ source.repository }}</b><small><AppIcon name="workflow" :size="12" /><span>CI <span class="mono">{{ workflowFile(source.ci_workflow) }}</span> · nightly <span class="mono">{{ workflowFile(source.nightly_workflow) }}</span></span></small></span><span v-if="manage" class="repo-acts"><button ref="linkButton" type="button" class="btn sm" data-act="change" :disabled="busy" @click="openForm"><AppIcon name="edit" :size="13" />Change…</button><button ref="moreButton" type="button" class="icon-btn sm" data-act="repo-more" aria-haspopup="menu" :aria-expanded="menuOpen" aria-label="More for the repository" :disabled="busy" @click="openMenu"><AppIcon name="more" :size="15" /></button></span></div>
        <div class="dstat" :class="{ 'has-source': !!source.history_source, wait: ['noactions', 'appoff', 'unavailable'].includes(state), ok: state === 'done', run: state === 'reading' }" role="status">
          <span class="ds-ic"><AppIcon :name="state === 'noactions' ? 'key' : state === 'appoff' ? 'signal-off' : state === 'unavailable' ? 'alert' : state === 'reading' ? 'history' : state === 'done' ? 'check' : 'clock'" :size="16" /></span>
          <div class="ds-t">
            <p class="ds-h"><b>Linked.</b> <template v-if="state === 'noactions'">History can’t be read: the GitHub App lacks Actions read access.</template><template v-else-if="state === 'appoff'">The GitHub App doesn’t receive events for {{ source.repository }}.</template><template v-else-if="state === 'stale'">No events from GitHub for {{ staleDays }} days.</template><template v-else-if="state === 'unavailable'">History can’t be read: GitHub is unavailable.</template><template v-else-if="state === 'done'">History read back to {{ source.backfill_since ? date(source.backfill_since) : 'the recorded start' }}; live since then.</template><template v-else>Reading history<template v-if="progressDay !== null"> · day {{ progressDay }} of {{ historyDays }}</template></template></p>
            <div class="ds-content" role="region" aria-label="Repository history details" tabindex="0">
            <template v-if="state === 'reading'">
              <div v-if="progressDay !== null" class="prog" role="progressbar" aria-label="History read" aria-valuemin="0" :aria-valuemax="historyDays" :aria-valuenow="progressDay"><span :style="{ width: `${progressDay / historyDays * 100}%` }" /></div>
              <p v-if="position?.phase && position.phase !== 'done'" class="phases"><span v-for="[id, label] in phases" :key="id" :class="{ now: position.phase === id }"><AppIcon name="circle-dot" :size="12" />{{ label }}</span></p>
              <p class="ds-b"><template v-if="position">{{ position.runs }} runs and {{ position.pulls }} pull requests in this step. </template>Reading continues on the server; you can leave this page.</p>
            </template>
            <ul v-else-if="state === 'done'" class="facts"><li><AppIcon name="pulse" :size="13" /><span><b>GitHub App events</b> · live</span></li><li><AppIcon name="history" :size="13" /><span><b>History</b> · back to {{ source.backfill_since ? date(source.backfill_since) : 'the recorded start' }}</span></li><li><AppIcon name="merge" :size="13" /><span><b>Reported by tooling</b> · merge rounds, releases</span></li></ul>
            <div v-else-if="state === 'noactions' || state === 'appoff'" class="fix"><p class="fix-r"><span class="fix-l">Who can fix it</span><span>An owner of <b>{{ repoOwner }}</b> on GitHub.</span></p><p class="fix-r"><span class="fix-l">How</span><span>On GitHub, <b>{{ repoOwner }}</b> <AppIcon name="chevron-right" :size="12" /> Settings <AppIcon name="chevron-right" :size="12" /> GitHub Apps <AppIcon name="chevron-right" :size="12" /> this workspace’s App <AppIcon name="chevron-right" :size="12" /> Configure, then <template v-if="state === 'noactions'">accept <b>Actions: Read</b>.</template><template v-else>add <b>{{ repoName }}</b> under Repository access.</template> <ProjectSettingsExplainer :id="state === 'noactions' ? 'actions' : 'events'" :project-key="project.routeKey" :repository="source.repository" /></span></p></div>
            <p v-else-if="state === 'stale'" class="ds-b">The last one arrived {{ source.last_event_at ? date(source.last_event_at) : 'at an unknown time' }}. That’s expected for a quiet repository<template v-if="project.frozen">, and {{ project.routeKey }} is frozen</template>. If people still push to {{ source.repository }}, the App may have lost access.</p>
            <p v-else class="ds-b">The last stored history is kept. <template v-if="source.retry_at">The server retries {{ date(source.retry_at, true) }}.</template><template v-else>The server will try again.</template></p>
            <p v-if="source.history_source" class="ds-source" :data-history-source="source.history_source">History source: <template v-if="source.history_source === 'actions'">GitHub Actions</template><template v-else>GitHub checks · partial history; attempt and runner wait metrics unavailable.</template></p>
            </div>
            <div class="ds-acts">
              <a v-if="state === 'noactions' || state === 'appoff'" class="btn sm" :href="state === 'noactions' ? appURL : 'https://github.com/settings/installations'" target="_blank" rel="noopener noreferrer"><AppIcon name="external" :size="13" /><span class="wide">{{ state === 'noactions' ? `Open ${repoOwner}’s GitHub Apps` : 'Open the App’s repository access' }}</span><span class="narrow">{{ state === 'noactions' ? 'GitHub Apps' : 'Repository access' }}</span></a>
              <button v-if="manage && ['noactions', 'appoff', 'stale', 'unavailable'].includes(state)" type="button" class="btn sm" data-act="check" :disabled="busy" @click="startHistory('check')"><AppIcon name="refresh" :size="13" />Check again</button>
              <span v-if="checked" class="checked">{{ checked }}</span>
              <RouterLink v-if="state === 'done'" class="link-btn quiet" :to="`/p/${encodeURIComponent(project.routeKey)}/delivery`">See the numbers on Delivery<AppIcon name="chevron-right" :size="12" /></RouterLink>
              <ProjectSettingsExplainer v-if="state === 'reading' || state === 'done'" id="backfill" :project-key="project.routeKey" :repository="source.repository" />
            </div>
          </div>
        </div>
      </template>
    </div>
    <p v-if="formOpen" class="feedback" :class="{ problem: feedback }" role="status" :aria-busy="busy">{{ feedback || (busy ? 'Linking the repository…' : '') }}</p>
  </SettingsCard>
  <FloatingPanel v-if="menuOpen" :anchor="moreButton ?? null" :width="340" :tallest="180" label="More for the repository" @close="restore => closeMenu(restore)"><div ref="menuContent" role="menu" aria-label="More for the repository"><button type="button" class="history-menu" role="menuitem" @click="startHistory('restart')"><AppIcon name="history" :size="14" /><span><b>Read history again…</b><small>Starts over on the server. Use it after GitHub was unreadable for a while.</small></span></button></div></FloatingPanel>
</template>

<style scoped>
#repository { --slot: 252px; --ds: 178px; }
.lock-line { display:flex; gap:7px; align-items:flex-start; margin-bottom:12px; font-size:12.5px; line-height:1.5; color:var(--ink-3); }.lock-line svg { flex:none; margin-top:2px; }
.repo-slot { display:grid; grid-template-rows:auto minmax(0,1fr); height:var(--slot); }
.empty { display:grid; grid-template-columns:44px minmax(0,1fr) auto; align-items:center; gap:12px 16px; min-height:var(--slot); padding:18px 18px 18px 16px; border-radius:16px; background:var(--surface-sunken); }
.empty-ic { display:grid; place-items:center; width:44px; height:44px; border-radius:50%; background:var(--surface-raised); box-shadow:inset 0 0 0 1px var(--line); color:var(--ink-2); }.empty b { font-size:14.5px; font-weight:650; }.empty p { margin-top:3px; font-size:13px; line-height:1.55; max-width:54ch; color:var(--ink-2); }
.repo-id { display:grid; grid-template-columns:36px minmax(0,1fr) auto; align-items:center; column-gap:12px; min-height:74px; padding-bottom:14px; }.repo-ic { display:grid; place-items:center; width:36px; height:36px; border-radius:10px; background:var(--surface-sunken); box-shadow:inset 0 0 0 1px var(--line); color:var(--ink-2); }.who { display:grid; gap:3px; min-width:0; }.who b { font:600 14px/1.3 var(--mono); overflow-wrap:anywhere; }.who small { display:flex; align-items:center; gap:6px; font-size:12px; color:var(--ink-3); }.who small svg { flex:none; }.mono { font-family:var(--mono); overflow-wrap:anywhere; }.who small .mono { font-size:11.5px; color:var(--ink-2); }.repo-acts { display:inline-flex; gap:4px; align-items:center; }
.dstat { display:grid; grid-template-columns:32px minmax(0,1fr); grid-template-rows:auto minmax(0,1fr) auto; column-gap:14px; min-height:0; padding:16px 18px 16px 16px; border-radius:16px; background:var(--surface-sunken); }.dstat.wait { background:var(--queue-wait-bg); box-shadow:inset 0 0 0 1px var(--queue-wait-line); }.ds-ic { display:grid; place-items:center; width:32px; height:32px; border-radius:50%; background:var(--surface-raised); box-shadow:inset 0 0 0 1px var(--line); color:var(--ink-2); }.ok .ds-ic,.run .ds-ic { background:var(--chip-teal-bg); box-shadow:inset 0 0 0 1px var(--chip-teal-line); color:var(--teal-ink); }.wait .ds-ic { background:color-mix(in srgb,var(--gold) 20%,var(--surface-raised)); color:var(--queue-wait-ink); box-shadow:inset 0 0 0 1px var(--queue-wait-line); }.ds-t { display:contents; }.ds-h { grid-column:2; padding-top:5px; }.ds-content { grid-column:1/-1; min-height:0; padding-left:46px; overflow:auto; scrollbar-gutter:stable; overscroll-behavior:contain; }.ds-content:focus-visible { outline:2px solid var(--teal); outline-offset:-2px; }.ds-h { font-size:14px; line-height:1.45; }.ds-h b { font-weight:650; }.ds-b { margin-top:6px; font-size:13px; line-height:1.55; color:var(--ink-2); max-width:68ch; }.fix { margin-top:8px; }.fix-r { display:grid; grid-template-columns:112px minmax(0,1fr); column-gap:12px; padding:2px 0; font-size:13px; line-height:1.55; color:var(--ink-2); }.fix-r b { color:var(--ink); font-weight:600; }.fix-l { padding-top:2px; font:500 10.5px/1.6 var(--mono); letter-spacing:.12em; text-transform:uppercase; color:var(--ink-3); }.fix :deep(.how) { margin-left:4px; }.ds-acts { grid-column:1/-1; padding-left:46px; display:flex; flex-wrap:wrap; gap:8px 10px; align-items:center; margin-top:auto; padding-top:12px; }.ds-acts .btn { text-decoration:none; }.checked { margin-left:4px; font-size:12px; color:var(--ink-3); }.link-btn { display:inline-flex; gap:3px; align-items:center; font-size:12.5px; color:var(--ink-2); }.narrow { display:none; }
.prog { position:relative; height:8px; margin-top:12px; border-radius:4px; background:var(--track); box-shadow:inset 0 0 0 1px var(--line); overflow:hidden; }.prog span { position:absolute; inset:0 auto 0 0; background:var(--teal); border-radius:4px; }.phases { display:flex; flex-wrap:wrap; gap:6px 18px; margin-top:6px; font-size:12.5px; color:var(--ink-3); }.phases span { display:inline-flex; align-items:center; gap:6px; }.phases .now { color:var(--ink); font-weight:600; }.facts { display:flex; flex-wrap:wrap; gap:6px 18px; margin:10px 0 0; padding:0; list-style:none; }.facts li { display:inline-flex; gap:7px; align-items:center; font-size:12.5px; color:var(--ink-2); }.facts b { color:var(--ink); font-weight:600; }
.slot-sk { display:grid; grid-template-rows:74px 1fr; min-height:var(--slot); }.sk-row { display:grid; grid-template-columns:36px 46%; gap:14px; align-items:center; }.sk-row .sk { height:30px; }.ds-sk { min-height:var(--ds); border-radius:16px; }
.link-form { display:grid; border-top:1px solid var(--line); }.rule { display:flex; gap:8px; margin:14px 0 2px; font-size:12.5px; color:var(--ink-2); }.rule svg { flex:none; }.lf-row { display:grid; grid-template-columns:210px minmax(0,1fr); align-items:center; column-gap:24px; min-height:66px; padding:12px 2px; border-bottom:1px solid var(--line); }.fl { display:grid; gap:2px; min-width:0; }.fl b { font-size:13.5px; font-weight:600; }.fl small { font-size:12.5px; line-height:1.45; color:var(--ink-3); }.pre-field { display:flex; align-items:center; min-width:0; height:36px; border:1px solid var(--glass-edge); border-radius:var(--radius-s); background:var(--field-bg); box-shadow:var(--field-inset),0 0 0 1px var(--line); }.pre { flex:none; padding-left:11px; font:500 12px/1 var(--mono); color:var(--ink-3); }.pre-field .field { flex:1; min-width:0; height:34px; min-height:0; padding:0 10px 0 1px; border:0; background:transparent; box-shadow:none; font:500 13px/1 var(--mono); }.lf-two { display:grid; grid-template-columns:minmax(0,1fr) minmax(0,1fr); gap:10px; }.lf-w { display:grid; gap:5px; min-width:0; }.lf-w>span { font-size:12px; font-weight:600; color:var(--ink-2); }.lf-w .field { height:36px; font:500 13px/1 var(--mono); }.lf-row select { max-width:260px; }.lf-acts { display:flex; justify-content:flex-end; gap:8px; padding-top:16px; }.feedback { min-height:18px; margin-top:8px; font-size:12px; line-height:1.5; color:var(--ink-3); overflow-wrap:anywhere; }.problem { color:var(--warn-ink); }.history-menu { display:flex; gap:10px; align-items:flex-start; width:100%; padding:12px; background:transparent; border:0; border-radius:8px; color:var(--ink); text-align:left; }.history-menu:hover { background:var(--row-hover); }.history-menu svg { flex:none; margin-top:2px; }.history-menu span { display:grid; gap:4px; }.history-menu b { font-size:13px; }.history-menu small { font-size:12px; line-height:1.5; color:var(--ink-2); }
.fix-r > span > svg { display:inline-block; vertical-align:middle; }
.ds-source { margin-top:6px; font-size:12px; line-height:1.5; color:var(--ink-3); }
@container project-settings-body (max-width:600px) {
  #repository { --slot:460px; --ds:0px; }.empty { grid-template-columns:44px minmax(0,1fr); }.empty>.btn { grid-column:1/-1; justify-self:start; }.repo-id { grid-template-columns:36px minmax(0,1fr); row-gap:10px; }.repo-acts { grid-column:2; }.dstat { column-gap:12px; align-content:start; padding:14px; }.ds-t { display:contents; }.ds-t>.ds-h { grid-column:2; align-self:center; }.ds-t>:not(.ds-h) { grid-column:1/-1; }.ds-content,.ds-acts { padding-left:0; }.ds-acts { margin-top:0; }.fix-r { display:block; padding:4px 0; }.fix-l { display:inline; margin-right:6px; }.wide { display:none; }.narrow { display:inline; }.lf-row { grid-template-columns:minmax(0,1fr); row-gap:10px; }.lf-two { grid-template-columns:minmax(0,1fr); }.lf-row select { max-width:none; }.lf-acts .btn { flex:1; }
}
@media (pointer:coarse), (max-width:600px) { .btn,.icon-btn,.link-btn,.history-menu { min-height:44px; }.icon-btn.sm { width:44px; height:44px; }.pre-field { height:46px; }.pre-field .field,.lf-w .field { height:44px; min-height:44px; }.lf-row select { min-height:44px; } }
</style>
