<script setup lang="ts">
import { computed, onScopeDispose, ref, watch } from 'vue'
import { CircleDot, Inbox, Layers3, RadioTower, WifiOff } from 'lucide-vue-next'
import { useRoute, useRouter } from 'vue-router'

import { api, permissionsEpoch, permissionsEpochGeneration } from '@/api/client'
import Paimos6SessionCard from '@/components/v6/Paimos6SessionCard.vue'
import Paimos6TalkDoor from '@/components/v6/Paimos6TalkDoor.vue'
import { usePaimos6SessionHome } from '@/composables/v6/usePaimos6SessionHome'
import { useAuthStore } from '@/stores/auth'
import type { Project } from '@/types'
import { loadPaimos6SessionHome } from '@/v6/sessionHome'

interface ProjectOption { id: number; key: string; name: string }

const DEFAULT_STATUS_MESSAGE = 'Choose a session to target it. No mutation endpoint exists in this preview.'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const doorOpen = ref(false)
const statusMessage = ref(DEFAULT_STATUS_MESSAGE)
const statusBoundary = ref(0)
const projects = ref<ProjectOption[]>([])
const projectState = ref<'loading' | 'ready' | 'empty' | 'unavailable'>('loading')
const selectedProjectId = ref<number | null>(null)
let projectLoadVersion = 0
let projectController: AbortController | null = null

const principalId = computed(() => auth.user?.id ?? null)
const authorityKey = computed(() => JSON.stringify([
  permissionsEpochGeneration.value,
  permissionsEpoch.value,
  auth.user?.id ?? null,
  auth.user?.role ?? null,
  auth.user?.status ?? null,
  auth.allProjects,
  [...auth.accessibleProjects.entries()].sort(([left], [right]) => left - right),
]))
const deepLinkedSessionId = computed(() => {
  const raw = route.query.session
  if (raw === undefined) return null
  return typeof raw === 'string' && raw !== '' ? raw : '__invalid-session-query__'
})
const deepLinkedProjectId = computed(() => requestedProjectId())

async function replaceSessionQuery(id: string | null): Promise<void> {
  const query = {
    ...route.query,
    project: selectedProjectId.value === null ? route.query.project : String(selectedProjectId.value),
    session: id ?? undefined,
  }
  await router.replace({ query }).catch(() => {})
}

const home = usePaimos6SessionHome({
  principalId,
  authorityKey,
  projectId: selectedProjectId,
  deepLinkedProjectId,
  deepLinkedSessionId,
  load: loadPaimos6SessionHome,
  replaceSessionQuery,
})
const selectedSession = home.selectedSession
const selectedProject = computed(() => projects.value.find((project) => project.id === selectedProjectId.value) ?? null)

function resetAuxiliaryStatus() {
  // The status channel and talk door can both contain copies of session
  // metadata. Fence them with the same synchronous owner transitions as rows.
  statusBoundary.value += 1
  doorOpen.value = false
  statusMessage.value = DEFAULT_STATUS_MESSAGE
}

function publishDoorStatus(message: string) {
  statusMessage.value = message
}

function requestedProjectId(): number | null {
  const raw = route.query.project
  if (typeof raw !== 'string' || !/^[1-9]\d*$/.test(raw)) return null
  const id = Number(raw)
  return Number.isSafeInteger(id) ? id : null
}

function selectAvailableProject(requested: number | null) {
  const next = projects.value.find((project) => project.id === requested)?.id ?? projects.value[0]?.id ?? null
  selectedProjectId.value = next
  const routeProject = requestedProjectId()
  if (routeProject !== next) {
    void router.replace({
      query: {
        ...route.query,
        project: next === null ? undefined : String(next),
      },
    }).catch(() => {})
  }
}

async function loadProjects() {
  const key = authorityKey.value
  const principal = principalId.value
  const version = ++projectLoadVersion
  projectController?.abort()
  const controller = new AbortController()
  projectController = controller
  projects.value = []
  selectedProjectId.value = null
  projectState.value = 'loading'
  if (principal === null) return

  try {
    const response = await api.get<Project[]>('/projects?status=all', { signal: controller.signal })
    if (version !== projectLoadVersion || controller.signal.aborted
      || key !== authorityKey.value || principal !== principalId.value) return
    projects.value = response.map(({ id, key: projectKey, name }) => ({ id, key: projectKey, name }))
    projectState.value = projects.value.length === 0 ? 'empty' : 'ready'
    selectAvailableProject(requestedProjectId())
  } catch {
    if (version !== projectLoadVersion || controller.signal.aborted
      || key !== authorityKey.value || principal !== principalId.value) return
    projectState.value = 'unavailable'
  } finally {
    if (projectController === controller) projectController = null
  }
}

watch([authorityKey, selectedProjectId, home.selectedId], resetAuxiliaryStatus, { flush: 'sync' })

watch(authorityKey, () => {
  // Clear the project vocabulary before a new principal/permission request.
  projectLoadVersion += 1
  projectController?.abort()
  projects.value = []
  selectedProjectId.value = null
  projectState.value = 'loading'
  void loadProjects()
}, { immediate: true, flush: 'sync' })

watch(() => route.query.project, () => {
  // Preserve an atomic history/deep-link session value. The session owner
  // sees its URL project mismatch, waits through the synchronous row clear,
  // and validates only after this project's projection arrives.
  if (projectState.value === 'ready') selectAvailableProject(requestedProjectId())
}, { flush: 'sync' })

function changeProject(event: Event) {
  const id = Number((event.target as HTMLSelectElement).value)
  if (!projects.value.some((project) => project.id === id)) return
  // The assignment precedes navigation and synchronously clears home rows.
  selectedProjectId.value = id
  // Picker navigation has no session deep link; unlike history navigation it
  // intentionally clears the outgoing project selection.
  void router.replace({ query: { ...route.query, project: String(id), session: undefined } }).catch(() => {})
}

function selectSession(id: string) {
  if (!home.select(id)) return
  const session = home.sessions.value.find((candidate) => candidate.id === id)
  statusMessage.value = session
    ? `${session.title} selected. Target is ${session.agent}; nothing was sent.`
    : 'Selection unavailable.'
}

function clearSelection() {
  home.clearSelection()
  statusMessage.value = 'Selection cleared. Preview utterances would target Paimos; nothing was sent.'
}

function previewAction(label: string, id: string) {
  const session = home.sessions.value.find((candidate) => candidate.id === id)
  statusMessage.value = `${label} has no mutation endpoint yet for ${session?.title ?? 'this session'}. No request was sent.`
}

onScopeDispose(() => {
  projectLoadVersion += 1
  projectController?.abort()
})
</script>

<template>
  <div class="p6-preview-root">
  <main class="p6-home" aria-labelledby="p6-title" :inert="doorOpen || undefined">
    <section class="p6-intro">
      <div class="p6-source-framing" aria-label="Paimos sources">
        <span class="p6-source is-active"><CircleDot :size="12" aria-hidden="true" /> Paimos · active source</span>
        <span class="p6-source is-reserved">Pharos · reserved source</span>
        <span class="p6-source is-reserved">Janus · reserved source</span>
      </div>
      <div class="p6-title-row">
        <div>
          <p class="p6-kicker">Your agent loop, without the CRUD chrome</p>
          <h1 id="p6-title">Good morning. Here’s what needs you.</h1>
          <p class="p6-deck">
            Live, project-authorized product sessions. These are not relabelled 5.x issues, deliveries, runs, or harness sessions.
          </p>
        </div>
        <dl class="p6-glance" aria-label="Live session summary">
          <div><dt>Attention</dt><dd>{{ home.totals.value.attention }} <span>sessions</span></dd></div>
          <div><dt>Inbox</dt><dd>{{ home.totals.value.unread }} <span>unread</span></dd></div>
          <div><dt>Source</dt><dd><RadioTower :size="16" aria-hidden="true" /> live</dd></div>
        </dl>
      </div>
    </section>

    <section class="p6-sessions" aria-labelledby="p6-sessions-title">
      <div class="p6-section-head">
        <div>
          <p class="p6-section-kicker"><Layers3 :size="13" aria-hidden="true" /> Session home</p>
          <h2 id="p6-sessions-title">Near you now</h2>
          <label v-if="projects.length" class="p6-project-picker">
            Authorized project
            <select :value="selectedProjectId ?? ''" @change="changeProject">
              <option v-for="project in projects" :key="project.id" :value="project.id">
                {{ project.key }} · {{ project.name }}
              </option>
            </select>
          </label>
        </div>
        <div class="p6-selection-copy">
          <template v-if="selectedSession">
            <span>Selected agent target · <strong>{{ selectedSession.agent }}</strong></span>
            <button type="button" @click="clearSelection">Clear selection <kbd>Esc on card</kbd></button>
          </template>
          <span v-else>No selection · preview target <strong>Paimos</strong></span>
        </div>
      </div>

      <div v-if="projectState === 'loading' || home.state.value === 'loading'" class="p6-load-state" role="status">
        Loading authorized session home…
      </div>
      <div v-else-if="projectState === 'empty'" class="p6-load-state">
        No project is available to this principal.
      </div>
      <div v-else-if="projectState === 'unavailable' || home.state.value === 'unavailable'" class="p6-load-state is-unavailable" role="alert">
        Session home unavailable. Previously authorized rows have been cleared; no session data is shown.
      </div>
      <div v-else-if="home.state.value === 'empty'" class="p6-load-state">
        {{ selectedProject?.key ?? 'This project' }} has no product sessions yet.
      </div>
      <div v-else class="p6-session-grid">
        <Paimos6SessionCard
          v-for="session in home.sessions.value"
          :key="session.id"
          :session="session"
          :selected="home.selectedId.value === session.id"
          @select="selectSession"
          @clear="clearSelection"
          @action="previewAction"
        />
      </div>
    </section>

    <section class="p6-honesty" aria-labelledby="p6-honesty-title">
      <WifiOff :size="19" aria-hidden="true" />
      <div>
        <h2 id="p6-honesty-title">Read-only responsive web preview</h2>
        <p>
          Rows come from the strict, project-authorized session-home endpoint. Controls are capability truth only: no mutation endpoint exists yet. At 390px this remains mobile web—not a native client—and no push capability is claimed.
        </p>
      </div>
      <span>Web · no push</span>
    </section>

    <p class="p6-status" role="status" aria-live="polite" aria-atomic="true">
      <Inbox :size="13" aria-hidden="true" /> {{ statusMessage }}
    </p>

  </main>

  <Paimos6TalkDoor
      :key="statusBoundary"
      v-model:open="doorOpen"
      :target-agent="selectedSession?.agent ?? null"
      @status="publishDoorStatus"
    />
  </div>
</template>

<style scoped>
.p6-preview-root { min-height: 100%; }
.p6-home {
  width: min(1180px, calc(100% - 64px));
  margin: 0 auto;
  padding: 70px 0 44px;
}
.p6-intro { max-width: 1080px; margin: 0 auto; }
.p6-source-framing { display: flex; flex-wrap: wrap; gap: 7px; }
.p6-source { display: inline-flex; align-items: center; gap: 5px; padding: 5px 9px; border: 1px solid #d8e2dc; border-radius: 999px; font-size: 9.5px; font-weight: 700; letter-spacing: 0.055em; text-transform: uppercase; }
.p6-source.is-active { color: #2d6048; background: #edf6f0; }
.p6-source.is-reserved { border-style: dashed; color: #59655e; background: rgba(255, 255, 255, 0.5); }
.p6-title-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: end; gap: 70px; margin-top: 30px; }
.p6-kicker,
.p6-section-kicker { color: #5d7467; font-size: 10px; font-weight: 750; letter-spacing: 0.1em; text-transform: uppercase; }
.p6-title-row h1 { max-width: 720px; margin-top: 9px; font-family: "Bricolage Grotesque", "DM Sans", sans-serif; font-size: clamp(35px, 5vw, 59px); font-weight: 500; line-height: 1.04; letter-spacing: -0.055em; }
.p6-deck { max-width: 680px; margin-top: 18px; color: #66736c; font-size: 13px; line-height: 1.7; }
.p6-glance { display: grid; min-width: 250px; grid-template-columns: repeat(3, 1fr); gap: 0; padding-bottom: 5px; }
.p6-glance div { padding: 0 16px; border-left: 1px solid #dbe3de; }
.p6-glance dt { color: #59655e; font-size: 9px; font-weight: 700; letter-spacing: 0.07em; text-transform: uppercase; }
.p6-glance dd { display: flex; align-items: center; gap: 5px; margin-top: 5px; color: #31443a; font: 500 18px/1 "Bricolage Grotesque", sans-serif; }
.p6-glance dd span { color: #59655e; font: 500 9px/1 "DM Sans", sans-serif; }
.p6-sessions { margin-top: 78px; }
.p6-section-head { display: flex; align-items: end; justify-content: space-between; gap: 24px; margin-bottom: 17px; }
.p6-section-kicker { display: flex; align-items: center; gap: 6px; }
.p6-section-head h2 { margin-top: 4px; font-family: "Bricolage Grotesque", "DM Sans", sans-serif; font-size: 23px; font-weight: 600; letter-spacing: -0.035em; }
.p6-project-picker { display: flex; align-items: center; gap: 8px; margin-top: 10px; color: #59655e; font-size: 10px; font-weight: 700; letter-spacing: 0.04em; text-transform: uppercase; }
.p6-project-picker select { max-width: 280px; min-height: 30px; padding: 4px 28px 4px 8px; border: 1px solid #d7e0da; border-radius: 8px; color: #31443a; background: #fbfcfa; font: 600 11px/1.2 "DM Sans", sans-serif; text-transform: none; }
.p6-project-picker select:focus-visible { outline: 3px solid rgba(47, 107, 82, 0.3); outline-offset: 3px; }
.p6-selection-copy { display: flex; align-items: center; gap: 12px; color: #59655e; font-size: 10.5px; }
.p6-selection-copy strong { color: #315b47; font-family: "JetBrains Mono", monospace; font-size: 10px; }
.p6-selection-copy button { padding: 5px 8px; border: 1px solid #d7e0da; border-radius: 7px; color: #53645b; background: #fbfcfa; font-size: 10px; }
.p6-selection-copy button:hover { border-color: #aabdb1; }
.p6-selection-copy button:focus-visible { outline: 3px solid rgba(47, 107, 82, 0.3); outline-offset: 3px; }
.p6-selection-copy kbd { margin-left: 4px; color: #59655e; font: 500 9px/1 "JetBrains Mono", monospace; }
.p6-session-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 14px; }
.p6-load-state { padding: 36px 22px; border: 1px dashed #cad6cf; border-radius: 16px; color: #59655e; background: rgba(252, 253, 250, 0.7); font-size: 12px; text-align: center; }
.p6-load-state.is-unavailable { border-color: #ddc3b8; color: #784d3b; background: #fff8f4; }
.p6-honesty { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 14px; margin-top: 17px; padding: 16px 18px; border: 1px dashed #cad6cf; border-radius: 14px; color: #68756e; background: rgba(252, 253, 250, 0.58); }
.p6-honesty h2 { color: #4d5b53; font-size: 11px; font-weight: 700; }
.p6-honesty p { margin-top: 3px; font-size: 10.5px; line-height: 1.5; }
.p6-honesty > span { padding: 4px 7px; border: 1px solid #d5ded9; border-radius: 999px; font-size: 9px; font-weight: 700; text-transform: uppercase; }
.p6-status { display: flex; align-items: center; gap: 6px; min-height: 22px; margin-top: 14px; color: #59655e; font-size: 10px; }

@media (max-width: 940px) {
  .p6-home { width: min(100% - 36px, 760px); padding-top: 48px; }
  .p6-title-row { grid-template-columns: 1fr; gap: 28px; }
  .p6-glance { width: min(100%, 360px); }
  .p6-session-grid { grid-template-columns: 1fr; }
}

@media (max-width: 680px) {
  .p6-home { width: calc(100% - 28px); padding: 34px 0 86px; }
  .p6-source-framing { gap: 5px; }
  .p6-source { padding: 4px 7px; font-size: 8.5px; }
  .p6-title-row { margin-top: 22px; }
  .p6-title-row h1 { font-size: clamp(34px, 11vw, 44px); }
  .p6-deck { font-size: 12px; }
  .p6-glance { min-width: 0; }
  .p6-glance div { padding: 0 10px; }
  .p6-sessions { margin-top: 52px; }
  .p6-section-head { align-items: flex-start; flex-direction: column; }
  .p6-selection-copy { width: 100%; align-items: flex-start; justify-content: space-between; }
  .p6-selection-copy span { max-width: 220px; }
  .p6-honesty { grid-template-columns: auto 1fr; align-items: start; }
  .p6-honesty > span { grid-column: 2; justify-self: start; }
}
</style>
