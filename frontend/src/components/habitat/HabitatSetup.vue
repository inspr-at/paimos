<script setup lang="ts">
import { computed, nextTick, onScopeDispose, ref, shallowRef, watch } from 'vue'
import { RouterLink, useRouter, useRoute } from 'vue-router'
import { api, ApiError } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import {
  setupAgents,
  setupDisplayLabelError,
  setupExpectedDeployment,
  type OrchestratorSetupAgent,
} from '@/v6/orchestratorSetup'
import type { HarnessDispatchProfile, OrchestrationRootV1 } from '@/services/orchestrationTypes'
import { parseRootConfig, parseDispatchProfiles, workerStartCommand } from './habitatSetup'
import HabitatLifecycle from './HabitatLifecycle.vue'
import { humanize, type HabitatProject, type HabitatWorker } from './habitatModel'
const props = defineProps<{
  projects: HabitatProject[]
  projectId: number | null
  authority: string
  workers?: HabitatWorker[]
  fresh?: boolean
  root: OrchestrationRootV1
}>()
const emit = defineEmits<{ refresh: [] }>()
const auth = useAuthStore()
const router = useRouter()
const route = useRoute()
const agents = ref<OrchestratorSetupAgent[]>([])
const profiles = ref<HarnessDispatchProfile[]>([])
const config = shallowRef<ReturnType<typeof parseRootConfig> | null>(null)
const state = ref<'loading' | 'ready' | 'unavailable'>('loading')
const agentKey = ref('')
const profileKey = ref('')
const label = ref('')
const cliInstance = ref('')
const deployment = ref<string | null>(null)
const ticket = ref('')
const shape = ref('ship')
const feedback = ref('')
const saving = ref(false)
const confirming = ref(false)
const confirmRef = ref<HTMLButtonElement | null>(null)
watch(confirming, (value) => {
  if (value) void nextTick(() => confirmRef.value?.focus())
})
let generation = 0
let controller: AbortController | null = null
const project = computed(
  () => props.projects.find((p) => p.project.id === props.projectId)?.project ?? null,
)
const selectedProfile = computed(
  () => profiles.value.find((p) => `${p.id}@${p.version}` === profileKey.value) ?? null,
)
const command = computed(() =>
  deployment.value && project.value && agents.value.some((a) => a.key === agentKey.value)
    ? workerStartCommand({
        instance: cliInstance.value,
        deployment: deployment.value,
        project: project.value.key,
        agent: agentKey.value,
        profile: selectedProfile.value,
        ticket: ticket.value,
        shape: shape.value,
      })
    : null,
)
const canSave = computed(
  () =>
    auth.isSuperAdmin &&
    config.value !== null &&
    project.value !== null &&
    state.value === 'ready' &&
    agents.value.some((a) => a.key === agentKey.value) &&
    !setupDisplayLabelError(label.value) &&
    !saving.value,
)
async function load() {
  const version = ++generation
  controller?.abort()
  controller = new AbortController()
  const signal = controller.signal
  state.value = 'loading'
  config.value = null
  agents.value = []
  profiles.value = []
  deployment.value = null
  agentKey.value = ''
  profileKey.value = ''
  label.value = ''
  feedback.value = ''
  confirming.value = false
  saving.value = false
  try {
    const [health, catalog, rawAgents, rawConfig] = await Promise.all([
      api.get<unknown>('/health', { signal }),
      api.get<unknown>('/ai/execution-options?dispatch_only=1', { signal }),
      props.projectId
        ? api.get<unknown>(`/projects/${props.projectId}/agents`, { signal })
        : Promise.resolve([]),
      auth.isSuperAdmin
        ? api.get<unknown>('/orchestrator/v1/config', { signal })
        : Promise.resolve(null),
    ])
    if (signal.aborted || version !== generation) return
    deployment.value = setupExpectedDeployment(health)
    profiles.value = parseDispatchProfiles(catalog)
    const choices = setupAgents(rawAgents, props.projectId ?? 0)
    if (!choices) throw new Error('invalid agent catalog')
    agents.value = choices
    config.value = auth.isSuperAdmin ? parseRootConfig(rawConfig) : null
    state.value = 'ready'
  } catch {
    if (!signal.aborted && version === generation) state.value = 'unavailable'
  }
}
watch(
  () => [props.authority, props.projectId],
  () => {
    cliInstance.value = ''
    ticket.value = ''
    void load()
  },
  { immediate: true, flush: 'sync' },
)
watch(
  () => [agentKey.value, label.value, props.root.binding_revision],
  () => {
    confirming.value = false
  },
)
async function save() {
  if (!canSave.value || !config.value || !project.value || !confirming.value) return
  const version = generation
  const revision = config.value.revision
  const signal = controller!.signal
  saving.value = true
  try {
    const result = parseRootConfig(
      await api.put<unknown>(
        '/orchestrator/v1/config',
        {
          expected_revision: revision,
          orchestrator: {
            project_id: project.value.id,
            key: agentKey.value,
            display_label: label.value,
          },
        },
        { signal },
      ),
    )
    if (signal.aborted || version !== generation) return
    if (result.revision !== revision + 1 || result.label !== label.value)
      throw new Error('invalid acknowledgement')
    config.value = result
    feedback.value =
      'Root identity saved. This does not start a process; inspect the live generation separately.'
    confirming.value = false
    emit('refresh')
  } catch (error) {
    if (signal.aborted || version !== generation) return
    feedback.value =
      error instanceof ApiError && error.status === 409
        ? 'The root binding changed. Refresh setup and review the newer revision before saving.'
        : 'No binding change is confirmed. Refresh setup to check authority and current state.'
    config.value = null
    confirming.value = false
  } finally {
    if (!signal.aborted && version === generation) saving.value = false
  }
}
function refreshSetup() {
  emit('refresh')
  void load()
}
async function copy() {
  if (!command.value) return
  try {
    await navigator.clipboard.writeText(command.value)
    feedback.value = 'Command copied. Nothing has been executed by the browser.'
  } catch {
    feedback.value = 'Clipboard unavailable. Select and copy the command below.'
  }
}
onScopeDispose(() => {
  generation++
  controller?.abort()
})
</script>
<template>
  <div class="habitat-stack" data-testid="habitat-setup">
    <section class="habitat-card">
      <span class="habitat-eyebrow">01 · Project &amp; agent</span>
      <h2>Project &amp; canonical agent</h2>
      <p>
        Agent definitions are project-owned roles. A definition does not prove that a process is
        running.
      </p>
      <label class="habitat-form"
        >Project<select
          :value="projectId ?? ''"
          @change="
            router.replace({
              query: {
                ...route.query,
                project: ($event.target as HTMLSelectElement).value || undefined,
              },
            })
          "
        >
          <option value="">Choose a project</option>
          <option v-for="row in projects" :key="row.project.id" :value="row.project.id">
            {{ row.project.key }} · {{ row.project.name }}
          </option>
        </select></label
      >
      <p v-if="state === 'loading'" role="status">Loading setup choices…</p>
      <div v-else-if="state === 'unavailable'" class="habitat-error">
        <p>Setup evidence is unavailable. No compatible agent or profile can be assumed.</p>
        <button type="button" @click="load">Retry setup</button>
      </div>
      <div v-else-if="project" class="habitat-form">
        <label
          >Canonical agent<select v-model="agentKey">
            <option value="">Choose an agent</option>
            <option v-for="agent in agents" :key="agent.key" :value="agent.key">
              {{ agent.key }}
            </option>
          </select></label
        >
        <p v-if="!agents.length">
          No canonical agents are available in this project. Create a project agent, then refresh
          these choices.
        </p>
        <div class="habitat-actions">
          <RouterLink class="habitat-button" :to="`/projects/${project.id}?tab=agents`"
            >Manage canonical agents</RouterLink
          ><button type="button" @click="load">Refresh choices</button>
        </div>
      </div>
    </section>
    <section class="habitat-card">
      <span class="habitat-eyebrow">02 · Instance identity</span>
      <h2>{{ root.configured_identity?.display_label ?? 'Bind the instance orchestrator' }}</h2>
      <p>
        {{ humanize(root.active_generation.state) }} generation ·
        {{ humanize(root.active_generation.reason) }}.
      </p>
      <p v-if="!auth.isSuperAdmin">
        A human super-admin must configure the instance root. You can inspect authorized workers and
        prepare a project start below.
      </p>
      <div v-else class="habitat-form">
        <label
          >Root display label<input
            v-model="label"
            maxlength="64"
            autocomplete="off"
            placeholder="Human-readable orchestrator name" /></label
        ><small v-if="label && setupDisplayLabelError(label)" class="habitat-danger">{{
          setupDisplayLabelError(label)
        }}</small>
        <p>
          Saving binds this instance to the explicitly selected project agent. It does not launch a
          process or adopt an existing worker.
        </p>
        <button v-if="!confirming" type="button" :disabled="!canSave" @click="confirming = true">
          Review root binding
        </button>
        <div v-else class="habitat-card" role="group" aria-label="Confirm root binding">
          <p>
            Bind {{ project?.key }} / {{ agentKey }} as “{{ label }}”, replacing revision
            {{ config?.revision }}?
          </p>
          <div class="habitat-actions">
            <button
              ref="confirmRef"
              type="button"
              class="habitat-primary"
              :disabled="!canSave"
              @click="save"
            >
              {{ saving ? 'Saving binding…' : 'Confirm root binding' }}</button
            ><button type="button" :disabled="saving" @click="confirming = false">Cancel</button>
          </div>
        </div>
      </div>
    </section>
    <section class="habitat-card">
      <span class="habitat-eyebrow">03 · Start / restart / repair</span>
      <h2>Start, assign or recover a worker</h2>
      <p>Start a worker, change an idle worker’s assignment, or recover a stopped generation.</p>
      <p>
        Choose an immutable profile and a runtime for a durable browser start or bounded repair. The
        guided CLI remains available when local execution cannot be reached.
      </p>
      <div class="habitat-form">
        <label
          >Profile for a new worker<select v-model="profileKey" :disabled="state !== 'ready'">
            <option value="">Choose one explicit profile</option>
            <option
              v-for="profile in profiles"
              :key="`${profile.id}@${profile.version}`"
              :value="`${profile.id}@${profile.version}`"
            >
              {{ profile.harness }} · {{ profile.model }} · {{ profile.effort }} ·
              {{ profile.id }}@{{ profile.version }}
            </option>
          </select></label
        >
        <dl v-if="selectedProfile" class="habitat-facts">
          <dt>Harness / model</dt>
          <dd>{{ selectedProfile.harness }} / {{ selectedProfile.model }}</dd>
          <dt>Effort</dt>
          <dd>{{ selectedProfile.effort }}</dd>
          <dt>Account</dt>
          <dd>Unknown until the local probe verifies it</dd>
          <dt>Machine</dt>
          <dd>Unknown until the authenticated reporter verifies it</dd>
          <dt>Workspace</dt>
          <dd>{{ selectedProfile.workspace_mode }} · selected and verified locally</dd>
        </dl>
        <p v-if="state === 'ready' && !profiles.length">
          No immutable profiles are available. An operator must configure a compatible dispatch
          profile.
        </p>
        <HabitatLifecycle
          :project-id="projectId"
          :agent="agentKey"
          :profile="selectedProfile"
          :authority="authority"
          :deployment="deployment"
          :workers="workers"
          :fresh="fresh"
          :selected-session-id="typeof route.query.worker === 'string' ? route.query.worker : ''"
          @refresh="emit('refresh')"
        />
        <h3>Guided CLI fallback</h3>
        <label
          >Ticket key (optional)<input
            v-model="ticket"
            :placeholder="project ? `${project.key}-123` : 'Choose a project first'"
            autocomplete="off" /></label
        ><label v-if="ticket"
          >Work shape<select v-model="shape">
            <option value="ship">Ship · verified product delivery</option>
            <option value="scout">Scout · investigation evidence</option>
          </select></label
        ><label
          >Local CLI instance alias<input
            v-model="cliInstance"
            maxlength="64"
            placeholder="Your configured CLI alias"
            autocomplete="off"
        /></label>
        <p v-if="deployment">
          Expected deployment: <strong>{{ deployment }}</strong
          >. Credentials and workspace paths stay on the local machine.
        </p>
        <p v-else>
          Deployment identity could not be verified. No command is generated. Ask the operator to
          enable matching deployment and agent-bus identities, then refresh.
        </p>
        <template v-if="command"
          ><code class="habitat-command" tabindex="0">{{ command }}</code
          ><button type="button" @click="copy">Copy guided start command</button>
          <p>
            Run it on the intended machine. The guide collects missing workspace and task choices
            locally. A stopped or ownership-lost generation must never be silently adopted.
          </p></template
        >
        <p v-else-if="deployment">
          Choose a project, canonical agent and profile, then enter your local instance alias.
          Ticket keys must belong to the selected project.
        </p>
        <button type="button" @click="refreshSetup">Refresh registration &amp; setup</button>
      </div>
    </section>
    <p class="habitat-status" role="status">{{ feedback }}</p>
  </div>
</template>
