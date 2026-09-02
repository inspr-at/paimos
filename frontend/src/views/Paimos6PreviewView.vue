<script setup lang="ts">
import { computed, inject, onMounted, onScopeDispose, ref, watch } from 'vue'
import { Check, Clipboard, Inbox, Layers3, RadioTower, RefreshCw, WifiOff } from 'lucide-vue-next'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { api, permissionsEpoch, permissionsEpochGeneration } from '@/api/client'
import Paimos6SessionCard from '@/components/v6/Paimos6SessionCard.vue'
import Paimos6KnowledgeCompact from '@/components/v6/Paimos6KnowledgeCompact.vue'
import Paimos6SourceRail from '@/components/v6/Paimos6SourceRail.vue'
import Paimos6TalkDoor from '@/components/v6/Paimos6TalkDoor.vue'
import Paimos6ZoomControl from '@/components/v6/Paimos6ZoomControl.vue'
import Paimos6ZoomOverview from '@/components/v6/Paimos6ZoomOverview.vue'
import { usePaimos6Orchestrator } from '@/composables/v6/usePaimos6Orchestrator'
import { usePaimos6Voice } from '@/composables/v6/usePaimos6Voice'
import { usePaimos6SessionZoom } from '@/composables/v6/usePaimos6SessionZoom'
import { useAuthStore } from '@/stores/auth'
import type { Project } from '@/types'
import {
  orchestratorSetupCommand,
  setupAgents,
  setupInstanceName,
  type OrchestratorSetupAgent,
} from '@/v6/orchestratorSetup'
import { canonicalPaimos6Zoom, loadPaimos6SessionZoom } from '@/v6/sessionHomeZoom'
import { PAIMOS6_COMMAND_CONTEXT_KEY } from '@/v6/commandPaletteContext'
import {
  loadStructuredKnowledgeSnapshot,
  type StructuredKnowledgeSnapshot,
} from '@/v6/structuredKnowledge'

interface ProjectOption {
  id: number
  key: string
  name: string
}

const DEFAULT_STATUS_MESSAGE =
  'Choose a session to target it for voice delivery, or leave it clear to talk to Paimos.'
const auth = useAuthStore()
const { locale } = useI18n()
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
const knowledgeSnapshot = ref<StructuredKnowledgeSnapshot | null>(null)
const knowledgeState = ref<'loading' | 'ready' | 'empty' | 'unavailable'>('loading')
let knowledgeLoadVersion = 0
let knowledgeController: AbortController | null = null
const orchestratorSetupState = ref<'idle' | 'loading' | 'ready' | 'empty' | 'unavailable'>('idle')
const orchestratorSetupAgents = ref<OrchestratorSetupAgent[]>([])
const orchestratorSetupInstance = ref<string | null>(null)
const orchestratorSetupAgentKey = ref('')
const orchestratorSetupLabel = ref('')
const orchestratorSetupFeedback = ref('')
let orchestratorSetupLoadVersion = 0
let orchestratorSetupController: AbortController | null = null

const principalId = computed(() => auth.user?.id ?? null)
const authorityKey = computed(() =>
  JSON.stringify([
    permissionsEpochGeneration.value,
    permissionsEpoch.value,
    auth.user?.id ?? null,
    auth.user?.role ?? null,
    auth.user?.status ?? null,
    auth.allProjects,
    [...auth.accessibleProjects.entries()].sort(([left], [right]) => left - right),
  ]),
)
const deepLinkedSessionId = computed(() => {
  const raw = route.query.session
  if (raw === undefined) return null
  return typeof raw === 'string' && raw !== '' ? raw : '__invalid-session-query__'
})
const deepLinkedProjectId = computed(() => requestedProjectId())
const zoomResolution = computed(() => canonicalPaimos6Zoom(route.query.zoom))
const zoom = computed(() => zoomResolution.value.zoom)

async function replaceSessionQuery(id: string | null): Promise<void> {
  const query = {
    ...route.query,
    project:
      selectedProjectId.value === null ? route.query.project : String(selectedProjectId.value),
    session: id ?? undefined,
  }
  await router.replace({ query }).catch(() => {})
}

const home = usePaimos6SessionZoom({
  principalId,
  authorityKey,
  projectId: selectedProjectId,
  deepLinkedProjectId,
  deepLinkedSessionId,
  zoom,
  load: loadPaimos6SessionZoom,
  replaceSessionQuery,
})
const selectedSession = home.selectedSession
const voiceSelection = computed(() => {
  const selected = selectedSession.value
  if (!selected) return null
  return {
    productSessionId: selected.id,
    revision: selected.revision,
    destination: selected.agent,
    available: selected.mode !== 'unavailable' && selected.mode !== 'paimos',
  }
})
const voice = usePaimos6Voice({
  principalId,
  authorityKey,
  projectId: selectedProjectId,
  selection: voiceSelection,
  locale,
})
const selectedOutsideSample = computed(
  () =>
    selectedSession.value !== null &&
    !home.sessions.value.some((session) => session.id === selectedSession.value?.id),
)
const registerCommandContext = inject(PAIMOS6_COMMAND_CONTEXT_KEY, null)
const selectedProject = computed(
  () => projects.value.find((project) => project.id === selectedProjectId.value) ?? null,
)
const orchestratorProjectKey = computed(() => selectedProject.value?.key ?? null)
const orchestrator = usePaimos6Orchestrator({
  principalId,
  authorityKey,
  projectKey: orchestratorProjectKey,
})
const orchestratorSetupEligible = computed(
  () =>
    auth.isSuperAdmin &&
    selectedProject.value !== null &&
    orchestrator.state.value === 'ready' &&
    orchestrator.projection.value?.displayLabel === null,
)
const visibleOrchestratorSetupCommand = computed(() => {
  if (
    !orchestratorSetupEligible.value ||
    orchestratorSetupState.value !== 'ready' ||
    orchestratorSetupInstance.value === null ||
    selectedProject.value === null ||
    !orchestratorSetupAgents.value.some(({ key }) => key === orchestratorSetupAgentKey.value)
  )
    return null
  return orchestratorSetupCommand({
    instance: orchestratorSetupInstance.value,
    project: selectedProject.value.key,
    agent: orchestratorSetupAgentKey.value,
    displayLabel: orchestratorSetupLabel.value,
  })
})

function clearOrchestratorSetup() {
  orchestratorSetupLoadVersion += 1
  orchestratorSetupController?.abort()
  orchestratorSetupController = null
  orchestratorSetupState.value = 'idle'
  orchestratorSetupAgents.value = []
  orchestratorSetupInstance.value = null
  orchestratorSetupAgentKey.value = ''
  orchestratorSetupLabel.value = ''
  orchestratorSetupFeedback.value = ''
}

watch(
  [orchestratorSetupEligible, selectedProjectId, authorityKey],
  () => {
    clearOrchestratorSetup()
    if (!orchestratorSetupEligible.value || selectedProjectId.value === null) return
    const projectId = selectedProjectId.value
    const authority = authorityKey.value
    const version = ++orchestratorSetupLoadVersion
    const controller = new AbortController()
    orchestratorSetupController = controller
    orchestratorSetupState.value = 'loading'
    void Promise.all([
      api.get<unknown>('/health', { signal: controller.signal }),
      api.get<unknown>(`/projects/${projectId}/agents`, { signal: controller.signal }),
    ])
      .then(([health, rawAgents]) => {
        if (
          version !== orchestratorSetupLoadVersion ||
          controller.signal.aborted ||
          projectId !== selectedProjectId.value ||
          authority !== authorityKey.value ||
          !orchestratorSetupEligible.value
        )
          return
        const instance = setupInstanceName(health)
        const agents = setupAgents(rawAgents, projectId)
        if (instance === null || agents === null) {
          orchestratorSetupState.value = 'unavailable'
          return
        }
        orchestratorSetupInstance.value = instance
        orchestratorSetupAgents.value = agents
        orchestratorSetupState.value = agents.length === 0 ? 'empty' : 'ready'
      })
      .catch(() => {
        if (
          version !== orchestratorSetupLoadVersion ||
          controller.signal.aborted ||
          projectId !== selectedProjectId.value ||
          authority !== authorityKey.value ||
          !orchestratorSetupEligible.value
        )
          return
        orchestratorSetupState.value = 'unavailable'
      })
      .finally(() => {
        if (orchestratorSetupController === controller) orchestratorSetupController = null
      })
  },
  { immediate: true, flush: 'sync' },
)

watch([orchestratorSetupAgentKey, orchestratorSetupLabel], () => {
  orchestratorSetupFeedback.value = ''
})

async function copyOrchestratorSetupCommand() {
  const command = visibleOrchestratorSetupCommand.value
  if (command === null || !auth.isSuperAdmin) return
  try {
    if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable')
    await navigator.clipboard.writeText(command)
    orchestratorSetupFeedback.value =
      'Command copied. Paste it into Terminal, run it, then refresh the binding.'
  } catch {
    orchestratorSetupFeedback.value =
      'Could not copy. Select the full visible command and copy it manually.'
  }
}

function refreshOrchestratorBinding() {
  if (!auth.isSuperAdmin) return
  orchestratorSetupFeedback.value = 'Checking the binding…'
  orchestrator.reload()
}

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

function startVoice() {
  void voice.start()
}

function retryVoice() {
  void voice.retry()
}

function requestedProjectId(): number | null {
  const raw = route.query.project
  if (typeof raw !== 'string' || !/^[1-9]\d*$/.test(raw)) return null
  const id = Number(raw)
  return Number.isSafeInteger(id) ? id : null
}

function selectAvailableProject(requested: number | null) {
  const next =
    projects.value.find((project) => project.id === requested)?.id ?? projects.value[0]?.id ?? null
  selectedProjectId.value = next
  const routeProject = requestedProjectId()
  if (routeProject !== next) {
    void router
      .replace({
        query: {
          ...route.query,
          project: next === null ? undefined : String(next),
        },
      })
      .catch(() => {})
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
    if (
      version !== projectLoadVersion ||
      controller.signal.aborted ||
      key !== authorityKey.value ||
      principal !== principalId.value
    )
      return
    projects.value = response.map(({ id, key: projectKey, name }) => ({
      id,
      key: projectKey,
      name,
    }))
    projectState.value = projects.value.length === 0 ? 'empty' : 'ready'
    selectAvailableProject(requestedProjectId())
  } catch {
    if (
      version !== projectLoadVersion ||
      controller.signal.aborted ||
      key !== authorityKey.value ||
      principal !== principalId.value
    )
      return
    projectState.value = 'unavailable'
  } finally {
    if (projectController === controller) projectController = null
  }
}

watch([authorityKey, selectedProjectId, zoom, home.selectedId], resetAuxiliaryStatus, {
  flush: 'sync',
})

watch(
  [authorityKey, selectedProjectId],
  () => {
    const version = ++knowledgeLoadVersion
    knowledgeController?.abort()
    knowledgeSnapshot.value = null
    knowledgeState.value = 'loading'
    const principal = principalId.value
    const projectId = selectedProjectId.value
    const authority = authorityKey.value
    if (principal === null || projectId === null) return
    const controller = new AbortController()
    knowledgeController = controller
    void loadStructuredKnowledgeSnapshot(projectId, controller.signal)
      .then((snapshot) => {
        if (
          version !== knowledgeLoadVersion ||
          controller.signal.aborted ||
          principal !== principalId.value ||
          projectId !== selectedProjectId.value ||
          authority !== authorityKey.value
        )
          return
        knowledgeSnapshot.value = snapshot
        knowledgeState.value =
          snapshot.entries.length || snapshot.legacy.length || snapshot.proposals.length
            ? 'ready'
            : 'empty'
      })
      .catch(() => {
        if (
          version !== knowledgeLoadVersion ||
          controller.signal.aborted ||
          principal !== principalId.value ||
          projectId !== selectedProjectId.value ||
          authority !== authorityKey.value
        )
          return
        knowledgeState.value = 'unavailable'
      })
      .finally(() => {
        if (knowledgeController === controller) knowledgeController = null
      })
  },
  { immediate: true, flush: 'sync' },
)

watch(
  () => route.query.zoom,
  () => {
    if (!zoomResolution.value.replace) return
    void router.replace({ query: { ...route.query, zoom: '10' } }).catch(() => {})
  },
  { immediate: true, flush: 'sync' },
)

watch(
  authorityKey,
  () => {
    // Clear the project vocabulary before a new principal/permission request.
    projectLoadVersion += 1
    projectController?.abort()
    knowledgeLoadVersion += 1
    knowledgeController?.abort()
    projects.value = []
    selectedProjectId.value = null
    projectState.value = 'loading'
    void loadProjects()
  },
  { immediate: true, flush: 'sync' },
)

watch(
  () => route.query.project,
  () => {
    // Preserve an atomic history/deep-link session value. The session owner
    // sees its URL project mismatch, waits through the synchronous row clear,
    // and validates only after this project's projection arrives.
    if (projectState.value === 'ready') selectAvailableProject(requestedProjectId())
  },
  { flush: 'sync' },
)

function changeProject(event: Event) {
  const id = Number((event.target as HTMLSelectElement).value)
  if (!projects.value.some((project) => project.id === id)) return
  // The assignment precedes navigation and synchronously clears home rows.
  selectedProjectId.value = id
  // Picker navigation has no session deep link; unlike history navigation it
  // intentionally clears the outgoing project selection.
  void router
    .replace({ query: { ...route.query, project: String(id), session: undefined } })
    .catch(() => {})
}

function changeZoom(value: string) {
  if (value === zoom.value && route.query.zoom === value) return
  void router.push({ query: { ...route.query, zoom: value } }).catch(() => {})
}

function selectSession(id: string) {
  // Capture current authorized union truth before URL publication. A router
  // implementation may publish the query synchronously, which fences rows.
  const session =
    home.sessions.value.find((candidate) => candidate.id === id) ??
    (selectedSession.value?.id === id ? selectedSession.value : null)
  if (!home.select(id)) return
  statusMessage.value = session
    ? `${session.title} selected. Target is ${session.agent}; nothing was sent.`
    : 'Selection unavailable.'
}

function clearSelection() {
  home.clearSelection()
  statusMessage.value = `Selection cleared. Preview target is ${orchestrator.identityLabel.value}; ${orchestrator.statusText.value}. Nothing was sent.`
}

function previewAction(label: string, id: string) {
  const session =
    home.sessions.value.find((candidate) => candidate.id === id) ??
    (selectedSession.value?.id === id ? selectedSession.value : null)
  statusMessage.value = `${label} has no mutation endpoint yet for ${session?.title ?? 'this session'}. No request was sent.`
}

onMounted(() =>
  registerCommandContext?.({
    selectedSessionId: home.selectedId,
    openTalk: () => {
      doorOpen.value = true
    },
    clearSession: clearSelection,
  }),
)

onScopeDispose(() => {
  registerCommandContext?.(null)
  projectLoadVersion += 1
  projectController?.abort()
  knowledgeLoadVersion += 1
  knowledgeController?.abort()
  clearOrchestratorSetup()
})
</script>

<template>
  <div class="p6-preview-root">
    <main class="p6-home" aria-labelledby="p6-title" :inert="doorOpen || undefined">
      <section class="p6-intro">
        <Paimos6SourceRail />
        <div class="p6-title-row">
          <div>
            <p class="p6-kicker">Your agent loop, without the CRUD chrome</p>
            <h1 id="p6-title">Good morning. Here’s what needs you.</h1>
            <p class="p6-deck">
              Live, project-authorized product sessions. These are not relabelled 5.x issues,
              deliveries, runs, or harness sessions.
            </p>
          </div>
          <dl class="p6-glance" aria-label="Live session summary">
            <div>
              <dt>Attention</dt>
              <dd>{{ home.totals.value.attention_sessions }} <span>sessions</span></dd>
            </div>
            <div>
              <dt>Inbox</dt>
              <dd>{{ home.totals.value.unread }} <span>unread</span></dd>
            </div>
            <div>
              <dt>Source</dt>
              <dd><RadioTower :size="16" aria-hidden="true" /> live</dd>
            </div>
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
              <span
                >Selected agent target · <strong>{{ selectedSession.agent }}</strong></span
              >
              <button type="button" @click="clearSelection">
                Clear selection <kbd>Esc on card</kbd>
              </button>
            </template>
            <span v-else
              >No selection · preview target
              <strong>{{ orchestrator.identityLabel.value }}</strong></span
            >
            <span class="p6-orchestrator-projection">{{ orchestrator.statusText.value }}</span>
          </div>
        </div>

        <div v-if="projectState === 'ready' && selectedProjectId !== null" class="p6-zoom-panel">
          <Paimos6ZoomControl
            :model-value="zoom"
            :band="home.band.value"
            @update:model-value="changeZoom"
          />
        </div>

        <div
          v-if="projectState === 'loading' || home.state.value === 'loading'"
          class="p6-load-state"
          role="status"
        >
          Loading authorized semantic-zoom projection…
        </div>
        <div v-else-if="projectState === 'empty'" class="p6-load-state">
          No project is available to this principal.
        </div>
        <div
          v-else-if="projectState === 'unavailable' || home.state.value === 'unavailable'"
          class="p6-load-state is-unavailable"
          role="alert"
        >
          Session home unavailable. Previously authorized rows have been cleared; no session data is
          shown.
        </div>
        <div v-else-if="home.state.value === 'empty'" class="p6-empty-state">
          <div class="p6-empty-state-copy">
            <p class="p6-empty-kicker">No Paimos product sessions</p>
            <h3>
              {{ selectedProject?.key ?? 'This project' }} has no product-session records yet.
            </h3>
            <p>
              Paimos has no product-session record to show. Local Codex or Claude processes started
              outside Paimos stay unmanaged, and Paimos does not adopt them retroactively.
            </p>
          </div>
          <div class="p6-empty-binding">
            <p class="p6-empty-kicker">Orchestrator binding</p>
            <p v-if="orchestrator.state.value === 'loading'">Checking the instance orchestrator…</p>
            <p
              v-else-if="
                orchestrator.state.value === 'unavailable' || orchestrator.state.value === 'idle'
              "
            >
              Binding status is unavailable. No orchestrator is inferred.
            </p>
            <p v-else-if="orchestrator.projection.value?.displayLabel">
              Configured as <strong>{{ orchestrator.projection.value.displayLabel }}</strong
              >. No Paimos product-session records are present yet.
            </p>
            <template v-else>
              <p>
                Not configured. This is separate from the project having no product-session records.
              </p>
              <div v-if="auth.isSuperAdmin" class="p6-orchestrator-setup">
                <p v-if="orchestratorSetupState === 'loading'" role="status">
                  Loading the authorized setup choices…
                </p>
                <p v-else-if="orchestratorSetupState === 'empty'" role="status">
                  This project has no canonical agents to choose from.
                </p>
                <p v-else-if="orchestratorSetupState === 'unavailable'" role="alert">
                  Setup choices are unavailable. No command was generated.
                </p>
                <template v-else-if="orchestratorSetupState === 'ready'">
                  <label>
                    Canonical agent
                    <select v-model="orchestratorSetupAgentKey">
                      <option value="">Choose an agent…</option>
                      <option
                        v-for="agent in orchestratorSetupAgents"
                        :key="agent.key"
                        :value="agent.key"
                      >
                        {{ agent.key }}
                      </option>
                    </select>
                  </label>
                  <label>
                    Display label
                    <input
                      v-model="orchestratorSetupLabel"
                      type="text"
                      maxlength="64"
                      autocomplete="off"
                      placeholder="For example: Primary orchestrator"
                    />
                  </label>
                  <p class="p6-setup-note">
                    The browser only copies this command. Terminal uses your existing credentials
                    for instance <strong>{{ orchestratorSetupInstance }}</strong
                    >.
                  </p>
                  <code class="p6-setup-command" tabindex="0">{{
                    visibleOrchestratorSetupCommand ??
                    'Choose an agent and enter a valid display label.'
                  }}</code>
                  <div class="p6-setup-actions">
                    <button
                      type="button"
                      :disabled="visibleOrchestratorSetupCommand === null"
                      @click="copyOrchestratorSetupCommand"
                    >
                      <Check
                        v-if="orchestratorSetupFeedback.startsWith('Command copied')"
                        :size="14"
                      />
                      <Clipboard v-else :size="14" />
                      Copy command
                    </button>
                    <button type="button" @click="refreshOrchestratorBinding">
                      <RefreshCw :size="14" />
                      I ran it — refresh
                    </button>
                  </div>
                  <p
                    v-if="orchestratorSetupFeedback"
                    class="p6-setup-feedback"
                    :role="orchestratorSetupFeedback.startsWith('Could not') ? 'alert' : 'status'"
                  >
                    {{ orchestratorSetupFeedback }}
                  </p>
                </template>
              </div>
              <p v-else class="p6-empty-operator-note">
                A super admin can copy an explicit terminal setup command here.
              </p>
            </template>
          </div>
        </div>
        <div v-else>
          <Paimos6ZoomOverview
            :totals="home.totals.value"
            :band="home.band.value"
            :sample-limit="home.sampleLimit.value"
            :sample-truncated="home.sampleTruncated.value"
            :sampled-sessions="home.sessions.value.length"
          />
          <section
            v-if="selectedOutsideSample && selectedSession"
            class="p6-pinned"
            aria-labelledby="p6-pinned-title"
          >
            <p id="p6-pinned-title">Selected outside this bounded sample</p>
            <Paimos6SessionCard
              :session="selectedSession"
              selected
              @select="selectSession"
              @clear="clearSelection"
              @action="previewAction"
            />
          </section>
          <div class="p6-sample-label">
            Exception-first sample · {{ home.sessions.value.length }} visible
          </div>
          <div class="p6-session-grid">
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
        </div>
      </section>

      <Paimos6KnowledgeCompact :state="knowledgeState" :snapshot="knowledgeSnapshot" />

      <section class="p6-honesty" aria-labelledby="p6-honesty-title">
        <WifiOff :size="19" aria-hidden="true" />
        <div>
          <h2 id="p6-honesty-title">Responsive web home</h2>
          <p>
            Rows come from the strict, project-authorized semantic-zoom endpoint. The
            exception-first sample is bounded while totals and a separately hydrated selection stay
            authoritative. Voice commits only a finalized transcript through the selected session
            contract; other controls remain capability previews. At 390px this remains mobile
            web—not a native client—and no push capability is claimed.
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
      :orchestrator-label="orchestrator.identityLabel.value"
      :orchestrator-status="orchestrator.statusText.value"
      :voice-state="voice.state.value"
      :voice-message="voice.message.value"
      :voice-supported="voice.supported.value"
      :voice-can-retry="voice.canRetry.value"
      @status="publishDoorStatus"
      @voice-start="startVoice"
      @voice-finish="voice.finish()"
      @voice-cancel="voice.cancel()"
      @voice-retry="retryVoice"
    />
  </div>
</template>

<style scoped>
.p6-preview-root {
  min-height: 100%;
}
.p6-home {
  width: min(1180px, calc(100% - 64px));
  margin: 0 auto;
  padding: 70px 0 44px;
}
.p6-intro {
  max-width: 1080px;
  margin: 0 auto;
}
.p6-title-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: end;
  gap: 70px;
  margin-top: 30px;
}
.p6-kicker,
.p6-section-kicker {
  color: #5d7467;
  font-size: 10px;
  font-weight: 750;
  letter-spacing: 0.1em;
  text-transform: uppercase;
}
.p6-title-row h1 {
  max-width: 720px;
  margin-top: 9px;
  font-family: 'Bricolage Grotesque', 'DM Sans', sans-serif;
  font-size: clamp(35px, 5vw, 59px);
  font-weight: 500;
  line-height: 1.04;
  letter-spacing: -0.055em;
}
.p6-deck {
  max-width: 680px;
  margin-top: 18px;
  color: #66736c;
  font-size: 13px;
  line-height: 1.7;
}
.p6-glance {
  display: grid;
  min-width: 250px;
  grid-template-columns: repeat(3, 1fr);
  gap: 0;
  padding-bottom: 5px;
}
.p6-glance div {
  padding: 0 16px;
  border-left: 1px solid #dbe3de;
}
.p6-glance dt {
  color: #59655e;
  font-size: 9px;
  font-weight: 700;
  letter-spacing: 0.07em;
  text-transform: uppercase;
}
.p6-glance dd {
  display: flex;
  align-items: center;
  gap: 5px;
  margin-top: 5px;
  color: #31443a;
  font:
    500 18px/1 'Bricolage Grotesque',
    sans-serif;
}
.p6-glance dd span {
  color: #59655e;
  font:
    500 9px/1 'DM Sans',
    sans-serif;
}
.p6-sessions {
  margin-top: 78px;
}
.p6-section-head {
  display: flex;
  align-items: end;
  justify-content: space-between;
  gap: 24px;
  margin-bottom: 17px;
}
.p6-section-kicker {
  display: flex;
  align-items: center;
  gap: 6px;
}
.p6-section-head h2 {
  margin-top: 4px;
  font-family: 'Bricolage Grotesque', 'DM Sans', sans-serif;
  font-size: 23px;
  font-weight: 600;
  letter-spacing: -0.035em;
}
.p6-project-picker {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
  color: #59655e;
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.04em;
  text-transform: uppercase;
}
.p6-project-picker select {
  max-width: 280px;
  min-height: 30px;
  padding: 4px 28px 4px 8px;
  border: 1px solid #d7e0da;
  border-radius: 8px;
  color: #31443a;
  background: #fbfcfa;
  font:
    600 11px/1.2 'DM Sans',
    sans-serif;
  text-transform: none;
}
.p6-project-picker select:focus-visible {
  outline: 3px solid rgba(47, 107, 82, 0.3);
  outline-offset: 3px;
}
.p6-selection-copy {
  display: flex;
  align-items: center;
  gap: 12px;
  color: #59655e;
  font-size: 10.5px;
}
.p6-selection-copy strong {
  color: #315b47;
  font-family: 'JetBrains Mono', monospace;
  font-size: 10px;
}
.p6-selection-copy button {
  padding: 5px 8px;
  border: 1px solid #d7e0da;
  border-radius: 7px;
  color: #53645b;
  background: #fbfcfa;
  font-size: 10px;
}
.p6-selection-copy button:hover {
  border-color: #aabdb1;
}
.p6-selection-copy button:focus-visible {
  outline: 3px solid rgba(47, 107, 82, 0.3);
  outline-offset: 3px;
}
.p6-selection-copy kbd {
  margin-left: 4px;
  color: #59655e;
  font:
    500 9px/1 'JetBrains Mono',
    monospace;
}
.p6-zoom-panel {
  display: grid;
  max-width: 560px;
  margin: 0 0 17px;
}
.p6-pinned {
  max-width: 380px;
  margin: 0 0 16px;
  padding: 10px;
  border: 1px solid #9db9a9;
  border-radius: 15px;
  background: #f1f7f3;
}
.p6-pinned > p,
.p6-sample-label {
  margin-bottom: 8px;
  color: #4c6959;
  font-size: 9px;
  font-weight: 750;
  letter-spacing: 0.065em;
  text-transform: uppercase;
}
.p6-session-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 14px;
}
.p6-load-state {
  padding: 36px 22px;
  border: 1px dashed #cad6cf;
  border-radius: 16px;
  color: #59655e;
  background: rgba(252, 253, 250, 0.7);
  font-size: 12px;
  text-align: center;
}
.p6-load-state.is-unavailable {
  border-color: #ddc3b8;
  color: #784d3b;
  background: #fff8f4;
}
.p6-empty-state {
  display: grid;
  grid-template-columns: minmax(0, 1.35fr) minmax(250px, 0.65fr);
  gap: 14px;
}
.p6-empty-state-copy,
.p6-empty-binding {
  padding: 22px;
  border: 1px dashed #cad6cf;
  border-radius: 16px;
  color: #59655e;
  background: rgba(252, 253, 250, 0.7);
}
.p6-empty-state-copy h3 {
  margin-top: 6px;
  color: #31443a;
  font:
    600 18px/1.25 'Bricolage Grotesque',
    'DM Sans',
    sans-serif;
  letter-spacing: -0.025em;
}
.p6-empty-state-copy > p:last-child,
.p6-empty-binding > p:not(.p6-empty-kicker) {
  margin-top: 8px;
  font-size: 11.5px;
  line-height: 1.55;
}
.p6-empty-kicker {
  color: #5d7467;
  font-size: 9px;
  font-weight: 750;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}
.p6-empty-binding strong {
  color: #315b47;
}
.p6-orchestrator-setup {
  display: grid;
  gap: 10px;
  margin-top: 14px;
}
.p6-orchestrator-setup label {
  display: grid;
  gap: 5px;
  color: #42584c;
  font-size: 10px;
  font-weight: 700;
}
.p6-orchestrator-setup select,
.p6-orchestrator-setup input {
  min-height: 36px;
  width: 100%;
  box-sizing: border-box;
  padding: 0 10px;
  border: 1px solid #b9c9bf;
  border-radius: 8px;
  color: #263b30;
  background: #fff;
  font:
    500 12px/1.3 'DM Sans',
    sans-serif;
}
.p6-setup-note {
  font-size: 10.5px;
  line-height: 1.45;
}
.p6-setup-command {
  display: block;
  overflow-wrap: anywhere;
  padding: 10px;
  border: 1px solid #d0ddd5;
  border-radius: 8px;
  color: #294638;
  background: #edf4ef;
  font:
    500 10px/1.55 'JetBrains Mono',
    monospace;
  user-select: all;
}
.p6-setup-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.p6-setup-actions button {
  display: inline-flex;
  min-height: 36px;
  align-items: center;
  gap: 6px;
  padding: 0 11px;
  border: 1px solid #9db9a9;
  border-radius: 9px;
  color: #315b47;
  background: #f1f7f3;
  font-size: 10.5px;
  font-weight: 700;
  cursor: pointer;
}
.p6-setup-actions button:hover:not(:disabled) {
  border-color: #6f967f;
  background: #e8f2eb;
}
.p6-setup-actions button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}
.p6-setup-actions button:focus-visible,
.p6-orchestrator-setup select:focus-visible,
.p6-orchestrator-setup input:focus-visible,
.p6-setup-command:focus-visible {
  outline: 2px solid #315e49;
  outline-offset: 2px;
}
.p6-setup-feedback {
  font-size: 10.5px;
  line-height: 1.45;
}
.p6-empty-binding > p.p6-empty-operator-note {
  color: #59655e;
  font-size: 11.5px;
}
.p6-honesty {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 14px;
  margin-top: 17px;
  padding: 16px 18px;
  border: 1px dashed #cad6cf;
  border-radius: 14px;
  color: #68756e;
  background: rgba(252, 253, 250, 0.58);
}
.p6-honesty h2 {
  color: #4d5b53;
  font-size: 11px;
  font-weight: 700;
}
.p6-honesty p {
  margin-top: 3px;
  font-size: 10.5px;
  line-height: 1.5;
}
.p6-honesty > span {
  padding: 4px 7px;
  border: 1px solid #d5ded9;
  border-radius: 999px;
  font-size: 9px;
  font-weight: 700;
  text-transform: uppercase;
}
.p6-status {
  display: flex;
  align-items: center;
  gap: 6px;
  min-height: 22px;
  margin-top: 14px;
  color: #59655e;
  font-size: 10px;
}

@media (max-width: 940px) {
  .p6-home {
    width: min(100% - 36px, 760px);
    padding-top: 48px;
  }
  .p6-title-row {
    grid-template-columns: 1fr;
    gap: 28px;
  }
  .p6-glance {
    width: min(100%, 360px);
  }
  .p6-session-grid {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 680px) {
  .p6-home {
    width: calc(100% - 28px);
    padding: 34px 0 86px;
  }
  .p6-title-row {
    margin-top: 22px;
  }
  .p6-title-row h1 {
    font-size: clamp(34px, 11vw, 44px);
  }
  .p6-deck {
    font-size: 12px;
  }
  .p6-glance {
    min-width: 0;
  }
  .p6-glance div {
    padding: 0 10px;
  }
  .p6-sessions {
    margin-top: 52px;
  }
  .p6-section-head {
    align-items: flex-start;
    flex-direction: column;
  }
  .p6-selection-copy {
    width: 100%;
    align-items: flex-start;
    justify-content: space-between;
  }
  .p6-selection-copy span {
    max-width: 220px;
  }
  .p6-zoom-panel,
  .p6-pinned {
    width: 100%;
    max-width: 100%;
    box-sizing: border-box;
  }
  .p6-empty-state {
    grid-template-columns: 1fr;
  }
  .p6-empty-state-copy,
  .p6-empty-binding {
    padding: 18px;
  }
  .p6-honesty {
    grid-template-columns: auto 1fr;
    align-items: start;
  }
  .p6-honesty > span {
    grid-column: 2;
    justify-self: start;
  }
}
</style>
