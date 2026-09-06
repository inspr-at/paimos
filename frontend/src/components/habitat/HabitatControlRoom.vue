<script setup lang="ts">
import { computed, inject, nextTick, onScopeDispose, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowUpRight, ChevronDown, ChevronRight, RefreshCw } from 'lucide-vue-next'
import { permissionsEpoch, permissionsEpochGeneration } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { useHabitat } from '@/composables/habitat/useHabitat'
import { PAIMOS6_COMMAND_CONTEXT_KEY } from '@/v6/commandPaletteContext'
import {
  humanize,
  workerRows,
  workerNeedsAttention,
  recentCommunication,
  type HabitatWorker,
} from './habitatModel'
import HabitatInspector from './HabitatInspector.vue'
import HabitatSetup from './HabitatSetup.vue'
import HabitatDeliveryEvidence from './HabitatDeliveryEvidence.vue'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const { t } = useI18n({
  useScope: 'local',
  messages: {
    en: {
      home: 'Your attention belongs here.',
      workers: 'Who is doing what, with which authority.',
      projects: 'A little distance. A clearer picture.',
      assign: 'Give the next generation a clear start.',
      homeDeck:
        'Decisions, handoffs and recovery come forward. Healthy background work stays quiet.',
      workersDeck:
        'From the instance orchestrator to each project and worker. Select a presence to inspect the evidence behind it.',
      projectsDeck:
        'Move between a single delivery and your portfolio. Progress remains grounded in trusted evidence.',
      assignDeck:
        'Bind an identity, choose a project, then start an owned worker with an explicit dispatch profile.',
    },
    de: {
      home: 'Hier ist deine Aufmerksamkeit gefragt.',
      workers: 'Wer arbeitet woran, mit welcher Befugnis.',
      projects: 'Etwas Abstand. Mehr Überblick.',
      assign: 'Ein klarer Start für die nächste Generation.',
      homeDeck:
        'Entscheidungen, Übergaben und Wiederherstellung stehen im Vordergrund. Gesunde Hintergrundarbeit bleibt ruhig.',
      workersDeck:
        'Vom Instanz-Orchestrator zu jedem Projekt und Worker. Wähle eine Präsenz, um ihre Nachweise zu prüfen.',
      projectsDeck:
        'Von einer Lieferung zum gesamten Portfolio. Fortschritt bleibt durch verlässliche Nachweise belegt.',
      assignDeck:
        'Identität binden, Projekt wählen und einen verwalteten Worker mit eindeutigem Ausführungsprofil starten.',
    },
  },
})
const view = computed(() =>
  ['workers', 'projects', 'assign'].includes(String(route.query.view))
    ? String(route.query.view)
    : 'home',
)
const projectId = computed(() => {
  const value = route.query.project
  return typeof value === 'string' &&
    /^[1-9]\d*$/.test(value) &&
    Number.isSafeInteger(Number(value))
    ? Number(value)
    : null
})
const zoom = computed(() =>
  typeof route.query.zoom === 'string' && /^[1-9]\d{0,63}$/.test(route.query.zoom)
    ? route.query.zoom
    : '10',
)
const zoomInput = ref(zoom.value)
const principal = computed(() => auth.user?.id ?? null)
const authority = computed(() =>
  JSON.stringify([
    permissionsEpochGeneration.value,
    permissionsEpoch.value,
    auth.user?.id,
    auth.user?.role,
    auth.user?.status,
    auth.allProjects,
    [...auth.accessibleProjects.entries()].sort(([a], [b]) => a - b),
  ]),
)
const habitat = useHabitat({ authority, principal, project: projectId, zoom })
const {
  snapshot,
  messageAttention,
  messageState,
  deliveries,
  state,
  deliveryState,
  refreshing,
  selectedId,
  selectedWorker,
  stale,
  now,
} = habitat
const expanded = ref(new Set<string>())
const projectSelection = ref<number | null>(null)
const inspector = ref<HTMLElement | null>(null)
const inspectorActions = ref<InstanceType<typeof HabitatInspector> | null>(null)
const announcement = ref('')
const selectedProject = computed(
  () =>
    snapshot.value?.project_coordination.find((p) => p.project.id === projectSelection.value) ??
    null,
)
const projects = computed(() => snapshot.value?.project_coordination ?? [])
const attentionWorkers = computed(
  () => snapshot.value?.fleet.workers.filter(workerNeedsAttention) ?? [],
)
const attentionDeliveries = computed(
  () =>
    deliveries.value?.deliveries.filter(
      (d) => d.attention.level >= 2 || ['blocked', 'stale', 'attention'].includes(d.health),
    ) ?? [],
)
const projectWorkers = (id: number) =>
  snapshot.value?.fleet.workers.filter((w) => w.project.id === id) ?? []
const projectDeliveryRows = (id: number) =>
  deliveries.value?.deliveries.filter((d) => d.lane.projectId === id) ?? []
const projectAggregate = (id: number) =>
  deliveries.value?.aggregates?.projects.find((p) => p.projectId === id)
const projectTotals = (id: number) => snapshot.value?.fleet.projects.find((p) => p.id === id)
const visibleRows = (id: number) => workerRows(projectWorkers(id), expanded.value)
const root = computed(() => snapshot.value?.instance_root)
const rootWorker = computed(() =>
  snapshot.value?.fleet.workers.find(
    (w) => w.harness_session_id === root.value?.active_generation.session_id,
  ),
)
const detail = computed(() => snapshot.value?.fleet.band === 'detail')
const consolidated = computed(() => ['aggregate', 'far'].includes(snapshot.value?.fleet.band ?? ''))
const bandCopy = computed(
  () =>
    ({
      detail: '1 · individual work and dispatch',
      overview: '10 · worker trees',
      aggregate: '100 · project clusters',
      far: '1000+ · portfolio and omissions',
    })[snapshot.value?.fleet.band ?? 'overview'],
)

watch(zoom, (value) => {
  zoomInput.value = value
})
watch(
  [authority, projectId, zoom],
  () => {
    projectSelection.value = null
    announcement.value = ''
    expanded.value = new Set()
  },
  { flush: 'sync' },
)
watch(
  () => snapshot.value?.fleet.workers,
  (workers) => {
    const focused =
      document.activeElement?.closest<HTMLElement>('[data-worker-id]')?.dataset.workerId
    if (focused && !workers?.some((w) => w.harness_session_id === focused))
      void nextTick(() => document.querySelector<HTMLElement>('#habitat-main')?.focus())
    if (detail.value && workers) expanded.value = new Set(workers.map((w) => w.harness_session_id))
  },
)
watch(selectedWorker, (worker, prior) => {
  if (!worker && prior) {
    announcement.value =
      'The selected worker is no longer in this authorized sample. Refresh or change detail to locate it.'
    if (inspector.value?.contains(document.activeElement))
      void nextTick(() => inspector.value?.focus())
  }
})
function selectWorker(worker: HabitatWorker) {
  selectedId.value = worker.harness_session_id
  projectSelection.value = null
  announcement.value = `Inspecting ${worker.agent.name}, ${worker.project.name}.`
}
function selectProject(id: number) {
  selectedId.value = null
  projectSelection.value = id
  announcement.value = 'Project selected. Details are in the inspector.'
}
function toggleWorker(id: string) {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}
function setZoom(value: string) {
  if (!/^[1-9]\d{0,63}$/.test(value)) {
    announcement.value = 'Enter a positive whole number, up to 64 digits.'
    return
  }
  void router.replace({ query: { ...route.query, zoom: value } })
}
function assign(project?: number) {
  void router.replace({
    query: {
      ...route.query,
      view: 'assign',
      project: project === undefined ? route.query.project : String(project),
    },
  })
}
function showWorkers(id?: number) {
  void router.replace({
    query: {
      ...route.query,
      view: 'workers',
      project: id === undefined ? route.query.project : String(id),
      zoom: '10',
    },
  })
}
const registerContext = inject(PAIMOS6_COMMAND_CONTEXT_KEY, null)
const noProductSession = computed(() => null)
registerContext?.({
  selectedSessionId: noProductSession,
  openTalk: () => {
    if (selectedWorker.value) {
      void inspectorActions.value?.openVoice()
      return
    }
    announcement.value =
      'Voice opens the product-session conversation. Worker controls remain in the inspector.'
    void router.replace({
      query: {
        ...route.query,
        view: 'sessions',
        project: route.query.project,
        talk: '1',
      },
    })
  },
  clearSession: () => {
    selectedId.value = null
  },
})
onScopeDispose(() => registerContext?.(null))
</script>

<template>
  <main id="habitat-main" class="habitat-main" tabindex="-1">
    <div class="habitat-context">
      <span
        >Habitat / {{ view === 'home' ? 'Needs you' : humanize(view)
        }}<template v-if="projectId"> / Project {{ projectId }}</template></span
      >
      <RouterLink v-if="projectId" :to="{ query: { ...route.query, project: undefined } }"
        >All authorized projects</RouterLink
      >
      <div class="habitat-zoom" aria-label="Semantic zoom">
        <span>Detail</span>
        <button
          v-for="value in ['1', '10', '100', '1000']"
          :key="value"
          type="button"
          :aria-pressed="zoom === value"
          @click="setZoom(value)"
        >
          {{ value === '1000' ? '1000+' : value }}
        </button>
        <label class="habitat-sr-only" for="habitat-zoom-value">Custom semantic zoom</label>
        <input
          id="habitat-zoom-value"
          v-model="zoomInput"
          inputmode="numeric"
          maxlength="64"
          @change="setZoom(zoomInput)"
          @keydown.enter="setZoom(zoomInput)"
        />
      </div>
      <button type="button" :disabled="refreshing" @click="habitat.refresh()">
        <RefreshCw :size="14" aria-hidden="true" />{{ refreshing ? 'Refreshing' : 'Refresh' }}
      </button>
    </div>
    <header class="habitat-hero">
      <div>
        <span class="habitat-eyebrow">{{
          view === 'home' ? 'Home · needs you' : `Habitat · ${humanize(view)}`
        }}</span>
        <h1>{{ t(view) }}</h1>
        <p>{{ t(`${view}Deck`) }}</p>
      </div>
      <button v-if="view !== 'assign'" type="button" class="habitat-primary" @click="assign()">
        Assign / start <ArrowUpRight :size="16" aria-hidden="true" />
      </button>
    </header>
    <p class="habitat-status" role="status" aria-live="polite">{{ announcement }}</p>
    <div v-if="state === 'loading'" class="habitat-loading" role="status">
      <span class="habitat-eyebrow">Connecting to your work</span>
      <h2>Reading the authorized projection…</h2>
      <p>Worker state will appear when the server provides its evidence.</p>
    </div>
    <div v-else-if="state !== 'ready'" class="habitat-card habitat-error" role="alert">
      <h2>
        {{
          state === 'unauthorized'
            ? 'This scope is not available to your account.'
            : 'The control room could not be refreshed.'
        }}
      </h2>
      <p>
        {{
          state === 'unauthorized'
            ? 'No worker data is retained. Check your project access or return to your authorized portfolio.'
            : 'Runtime health and current work are unknown. Check the connection and retry this read.'
        }}
      </p>
      <div class="habitat-actions">
        <button type="button" @click="habitat.refresh()">Retry</button
        ><RouterLink class="habitat-button" to="/">Open portfolio</RouterLink
        ><RouterLink class="habitat-button" to="/settings?tab=account">Account settings</RouterLink>
      </div>
    </div>
    <template v-else-if="snapshot">
      <div v-if="stale" class="habitat-error" role="status">
        This snapshot is stale. Controls are paused until a successful refresh confirms current
        ownership. <button type="button" @click="habitat.refresh()">Refresh evidence</button>
      </div>
      <div class="habitat-section-head habitat-muted">
        <span>{{ bandCopy }}</span
        ><span
          >Server snapshot · {{ new Date(snapshot.fleet.observed_at).toLocaleString() }} · refreshes
          every 15s</span
        >
      </div>
      <div class="habitat-workspace">
        <div class="habitat-stage habitat-stack">
          <HabitatSetup
            v-if="view === 'assign'"
            :projects="projects"
            :project-id="projectId"
            :authority="authority"
            :root="snapshot.instance_root"
            @refresh="habitat.refresh()"
          />
          <template v-else>
            <section class="habitat-card habitat-root">
              <span
                class="habitat-orb"
                :data-state="rootWorker?.liveness.state ?? 'unknown'"
                aria-hidden="true"
              ></span>
              <div>
                <span class="habitat-eyebrow">Instance orchestrator</span>
                <h2>
                  {{
                    root?.configured_identity?.display_label ?? 'A root identity is not configured.'
                  }}
                </h2>
                <span class="habitat-pill"
                  >{{ humanize(root?.active_generation.state) }} generation</span
                >
                <p>
                  {{ humanize(root?.active_generation.reason) }}. Binding revision
                  {{ root?.binding_revision }}.
                </p>
                <button v-if="rootWorker" type="button" @click="selectWorker(rootWorker)">
                  Inspect generation
                </button>
                <button v-else type="button" @click="assign()">
                  {{
                    root?.configured_identity ? 'Review recovery / start' : 'Set up orchestrator'
                  }}
                </button>
              </div>
            </section>
            <template v-if="view === 'home'">
              <section class="habitat-stack" aria-labelledby="habitat-needs-heading">
                <div class="habitat-section-head">
                  <h2 id="habitat-needs-heading">Needs you</h2>
                  <RouterLink :to="{ query: { ...route.query, view: 'sessions' } }"
                    >Unanswered messages &amp; decisions</RouterLink
                  >
                </div>
                <p v-if="messageState === 'loading'" class="habitat-muted">
                  Checking unanswered messages and decisions…
                </p>
                <article
                  v-for="row in messageAttention.filter(
                    (row) => row.totals && row.totals.exception_messages > 0,
                  )"
                  :key="`messages-${row.projectId}`"
                  class="habitat-card habitat-attention"
                >
                  <span class="habitat-eyebrow">Unanswered messages &amp; decisions</span>
                  <h3>
                    {{
                      projects.find((project) => project.project.id === row.projectId)?.project.name
                    }}
                  </h3>
                  <p>
                    {{ row.totals?.action_requests }} held action requests ·
                    {{ row.totals?.exception_messages }} exceptional messages ·
                    {{ row.totals?.attention_sessions }} product sessions need attention.
                  </p>
                  <RouterLink
                    class="habitat-button"
                    :to="{
                      query: { ...route.query, project: String(row.projectId), view: 'sessions' },
                    }"
                    >Review messages &amp; decisions</RouterLink
                  >
                </article>
                <div
                  v-if="messageAttention.some((row) => row.totals === null)"
                  class="habitat-card"
                >
                  <h3>Some message attention is unknown.</h3>
                  <p>
                    {{ messageAttention.filter((row) => row.totals === null).length }} project
                    message reads are unavailable. Open Product sessions or refresh to check
                    outstanding decisions.
                  </p>
                  <button type="button" @click="habitat.refresh()">Refresh message evidence</button>
                </div>
                <p v-if="messageState === 'ready'" class="habitat-muted">
                  Message attention checked for {{ messageAttention.length }} sampled projects;
                  {{ snapshot.coordination_bounds.total_projects - messageAttention.length }}
                  authorized projects are outside this message sample.
                </p>
                <div v-if="deliveryState === 'unavailable'" class="habitat-card">
                  <h3>Delivery attention is unavailable.</h3>
                  <p>
                    Worker evidence is separate. Unanswered delivery decisions cannot be counted
                    from this snapshot.
                  </p>
                  <button type="button" @click="habitat.refresh()">Retry delivery read</button>
                </div>
                <div
                  v-for="delivery in attentionDeliveries"
                  :key="delivery.id"
                  class="habitat-card habitat-attention"
                >
                  <span class="habitat-eyebrow">{{
                    humanize(delivery.attention.reason ?? delivery.health)
                  }}</span>
                  <h3>{{ delivery.issueKey }} · {{ delivery.title }}</h3>
                  <p>
                    {{ humanize(delivery.stage.key) }} ·
                    {{ humanize(delivery.freshness.state) }} evidence ·
                    {{ delivery.freshness.lastReportAt ?? 'Report time unknown' }}
                  </p>
                  <RouterLink
                    class="habitat-button"
                    :to="`/projects/${delivery.lane.projectId}/issues/${delivery.issueId}`"
                    >Review decision / evidence</RouterLink
                  >
                </div>
                <div
                  v-for="worker in attentionWorkers"
                  :key="worker.harness_session_id"
                  class="habitat-card habitat-attention"
                >
                  <span class="habitat-eyebrow">{{
                    worker.liveness.state === 'dead' ? 'Recovery' : 'Check evidence'
                  }}</span>
                  <h3>{{ worker.agent.name }} · {{ worker.project.name }}</h3>
                  <p>
                    {{ humanize(worker.liveness.state) }} · {{ humanize(worker.liveness.reason) }}.
                    Delivery: {{ humanize(worker.delivery_trust.reason) }}.
                  </p>
                  <button type="button" @click="selectWorker(worker)">Inspect &amp; recover</button>
                </div>
                <div
                  v-for="project in projects.filter((p) => p.coordinator.state !== 'resolved')"
                  :key="project.project.id"
                  class="habitat-card"
                >
                  <span class="habitat-eyebrow">Project coordination</span>
                  <h3>{{ project.project.name }}</h3>
                  <p>
                    {{ humanize(project.coordinator.reason) }}. A configured identity alone does not
                    prove that a worker is running.
                  </p>
                  <button type="button" @click="assign(project.project.id)">
                    Review project setup
                  </button>
                </div>
                <div
                  v-if="
                    !attentionWorkers.length &&
                    !attentionDeliveries.length &&
                    deliveryState === 'ready' &&
                    messageState === 'ready' &&
                    messageAttention.every(
                      (row) => row.totals !== null && row.totals.exception_messages === 0,
                    ) &&
                    projects.every((p) => p.coordinator.state === 'resolved')
                  "
                  class="habitat-card"
                >
                  <h3>No actionable transition in this sample.</h3>
                  <p>
                    Healthy work stays quiet here. Message decisions remain available in Product
                    sessions; omitted work is listed below.
                  </p>
                  <button type="button" @click="showWorkers()">See workers</button>
                </div>
              </section>
            </template>
            <section
              v-else-if="view === 'workers'"
              class="habitat-tree"
              aria-label="Worker hierarchy"
            >
              <p class="habitat-muted">
                Instance coordination → project trees. Indentation represents explicit same-project
                parent bindings.
              </p>
              <section
                v-for="project in projects"
                :key="project.project.id"
                class="habitat-project-tree"
              >
                <div class="habitat-section-head">
                  <h2>
                    {{ project.project.name }}
                    <small class="habitat-muted">{{ project.project.key }}</small>
                  </h2>
                  <button type="button" @click="selectProject(project.project.id)">
                    Inspect project
                  </button>
                </div>
                <p class="habitat-muted">
                  Coordinator: {{ humanize(project.coordinator.state) }} ·
                  {{
                    projectTotals(project.project.id)?.total_workers ??
                    (snapshot.fleet.totals.workers === 0 ? 0 : 'Unknown count of')
                  }}
                  retained workers<template
                    v-if="projectTotals(project.project.id)?.omitted_workers"
                  >
                    · {{ projectTotals(project.project.id)?.omitted_workers }} omitted</template
                  >
                </p>
                <template v-if="consolidated"
                  ><button type="button" @click="showWorkers(project.project.id)">
                    Expand {{ project.project.key }} workers
                  </button></template
                >
                <ol
                  v-else
                  class="habitat-worker-list"
                  :aria-label="`${project.project.name} worker tree`"
                >
                  <li
                    v-for="row in visibleRows(project.project.id)"
                    :key="row.worker.harness_session_id"
                    class="habitat-worker-row"
                    :style="{ '--depth': row.depth }"
                    :data-selected="selectedId === row.worker.harness_session_id"
                    :data-worker-id="row.worker.harness_session_id"
                  >
                    <button
                      v-if="row.children"
                      type="button"
                      class="habitat-expand"
                      :aria-expanded="expanded.has(row.worker.harness_session_id)"
                      :aria-label="`${expanded.has(row.worker.harness_session_id) ? 'Collapse' : 'Expand'} ${row.worker.agent.name} descendants`"
                      @click="toggleWorker(row.worker.harness_session_id)"
                    >
                      <ChevronDown
                        v-if="expanded.has(row.worker.harness_session_id)"
                        :size="16"
                      /><ChevronRight v-else :size="16" />
                    </button>
                    <button
                      type="button"
                      class="habitat-worker-select"
                      :aria-pressed="selectedId === row.worker.harness_session_id"
                      @click="selectWorker(row.worker)"
                    >
                      <span
                        class="habitat-orb"
                        :data-state="row.worker.liveness.state"
                        :data-recent="!stale && recentCommunication(row.worker, now)"
                        aria-hidden="true"
                      ></span>
                      <span
                        ><strong>{{ row.worker.agent.name }}</strong
                        ><small
                          >{{
                            row.worker.role === 'coordinator'
                              ? 'Project coordinator'
                              : row.worker.parent_harness_session_id
                                ? 'Subagent · explicit parent'
                                : 'Agent · no parent binding'
                          }}
                          · {{ humanize(row.worker.liveness.state) }}</small
                        ><small
                          >{{
                            row.worker.ticket?.key ??
                            (row.worker.ticket ? 'Ticket details unavailable' : 'No ticket binding')
                          }}
                          · {{ humanize(row.worker.work_shape) }}</small
                        ><small v-if="detail"
                          >{{ row.worker.harness }} ·
                          {{ row.worker.dispatch_profile?.model ?? 'Model unknown' }} ·
                          {{ row.worker.account_label }}</small
                        ><small v-if="row.hidden"
                          >{{ row.hidden }} sampled descendants collapsed</small
                        ><small
                          v-if="
                            row.worker.parent_harness_session_id && !row.worker.parent_in_sample
                          "
                          >Parent outside this sample</small
                        ><small v-if="recentCommunication(row.worker, now)"
                          >Message evidence in the last 60 seconds</small
                        ></span
                      >
                    </button>
                  </li>
                </ol>
                <div v-if="!projectWorkers(project.project.id).length">
                  <p class="habitat-muted">
                    No worker generations in this sample. A canonical agent and a running process
                    are separate resources.
                  </p>
                  <button type="button" @click="assign(project.project.id)">Set up a worker</button>
                </div>
              </section>
            </section>
            <section v-else class="habitat-stack" aria-label="Project portfolio">
              <div v-if="snapshot.fleet.band === 'far'" class="habitat-card">
                <span class="habitat-eyebrow">Portfolio · 1000+</span>
                <h2>{{ snapshot.coordination_bounds.total_projects }} authorized projects</h2>
                <p>
                  {{ snapshot.fleet.totals.workers }} retained workers.
                  {{ deliveries?.aggregates?.root.activeTotal ?? 'Unknown' }} active deliveries.
                </p>
                <p v-if="deliveries?.aggregates">
                  Trusted landing groups: {{ deliveries.aggregates.root.landing.within_4h }} within
                  4h · {{ deliveries.aggregates.root.landing.within_24h }} within 24h ·
                  {{ deliveries.aggregates.root.landing.within_3d }} within 3d ·
                  {{ deliveries.aggregates.root.landing.later }} later ·
                  {{ deliveries.aggregates.root.landing.range_only }} range only ·
                  {{ deliveries.aggregates.root.landing.suppressed_or_unknown }} unknown/suppressed.
                </p>
              </div>
              <div class="habitat-grid">
                <article v-for="project in projects" :key="project.project.id" class="habitat-card">
                  <span class="habitat-eyebrow">{{ project.project.key }} · project</span>
                  <h2>{{ project.project.name }}</h2>
                  <p>
                    {{
                      projectTotals(project.project.id)?.total_workers ??
                      (snapshot.fleet.totals.workers === 0 ? 0 : 'Unknown count of')
                    }}
                    retained workers · Coordinator {{ project.coordinator.state }}
                  </p>
                  <p v-if="!consolidated" class="habitat-muted">
                    {{ projectWorkers(project.project.id).filter(workerNeedsAttention).length }}
                    workers need evidence review in this sample.
                  </p>
                  <template v-if="projectAggregate(project.project.id)"
                    ><p>
                      {{ projectAggregate(project.project.id)!.counts.activeTotal }} active
                      deliveries ·
                      {{ projectAggregate(project.project.id)!.counts.flags.attention }} need
                      attention.
                    </p>
                    <p>
                      Landing evidence:
                      {{ projectAggregate(project.project.id)!.counts.landing.within_24h }} within
                      24h ·
                      {{
                        projectAggregate(project.project.id)!.counts.landing.suppressed_or_unknown
                      }}
                      unknown/suppressed.
                    </p>
                    <details v-if="consolidated">
                      <summary>Delivery stage counts</summary>
                      <p
                        v-for="(count, stage) in projectAggregate(project.project.id)!.counts
                          .currentStage"
                        :key="stage"
                      >
                        {{ humanize(stage) }} · {{ count }}
                      </p>
                    </details></template
                  >
                  <p v-else>
                    Portfolio progress / ETA: unknown. No trusted project aggregate is available.
                  </p>
                  <template v-if="!consolidated"
                    ><HabitatDeliveryEvidence
                      v-for="delivery in projectDeliveryRows(project.project.id).slice(
                        0,
                        detail ? 10 : 2,
                      )"
                      :key="delivery.id"
                      :delivery="delivery"
                      :fresh="!stale"
                      :detail="detail"
                    />
                    <p v-if="projectDeliveryRows(project.project.id).length > (detail ? 10 : 2)">
                      {{ projectDeliveryRows(project.project.id).length - (detail ? 10 : 2) }}
                      delivery rows collapsed. Inspect the project for more.
                    </p></template
                  >
                  <div class="habitat-actions">
                    <button type="button" @click="selectProject(project.project.id)">
                      Inspect project</button
                    ><button type="button" @click="showWorkers(project.project.id)">
                      Worker tree
                    </button>
                  </div>
                </article>
              </div>
            </section>
            <section v-if="!projects.length" class="habitat-card">
              <span class="habitat-eyebrow">Your next starting point</span>
              <h2>No authorized projects in this snapshot.</h2>
              <p>
                There is no work to visualize yet. Open the project workspace to create a project or
                check access, then refresh here.
              </p>
              <RouterLink class="habitat-button" to="/projects">Open project workspace</RouterLink>
            </section>
          </template>
          <div class="habitat-card habitat-muted">
            <strong>Scope &amp; evidence</strong>
            <p>
              {{ snapshot.coordination_bounds.sampled_projects }} of
              {{ snapshot.coordination_bounds.total_projects }} authorized projects ·
              {{ snapshot.coordination_bounds.omitted_projects }} projects omitted.
              {{ snapshot.fleet.totals.sampled_workers }} of
              {{ snapshot.fleet.totals.workers }} retained workers ·
              {{ snapshot.fleet.totals.omitted_workers }} workers omitted.
            </p>
            <small
              >Authoritative database · no remote cache · one terminal generation retained per
              agent. Recent communication is bounded metadata; it is not proof of a reply or active
              conversation.</small
            >
          </div>
        </div>
        <aside ref="inspector" class="habitat-inspector" aria-label="Inspector" tabindex="-1">
          <HabitatInspector
            ref="inspectorActions"
            :worker="selectedWorker"
            :project="selectedProject"
            :workers="snapshot.fleet.workers"
            :deliveries="deliveries?.deliveries ?? []"
            :fresh="!stale && state === 'ready'"
            :authority="authority"
            @refresh="habitat.refresh()"
            @select-worker="selectWorker"
            @assign="assign($event)"
          />
        </aside>
      </div>
    </template>
  </main>
</template>
