<!-- PAIMOS — Your Professional & Personal AI Project OS -->
<!-- Copyright (C) 2026 Markus Barta <markus@barta.com> -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, errMsg } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import type { User } from '@/types'
import type { HarnessDispatchProfile } from '@/services/orchestrationTypes'
import { parseDispatchProfiles } from '@/components/habitat/habitatSetup'
import {
  habitatAccountChoices,
  habitatChoiceId,
  loadHabitatRuntimes,
  type HabitatRuntime,
} from '@/components/habitat/habitatLifecycle'
import {
  decodeAgeCiphertext,
  downloadEncryptedCredential,
  hasExactKeys,
  ownObject,
  type EncryptedCredentialDownload,
} from './encryptedCredential'

type ProjectChoice = { id: number; key: string; name: string; status?: string }
type ActorRow = { issuer: string; subject: string; user_id: number | null }
type Limits = {
  max_input_bytes: number
  max_messages: number
  max_output_bytes: number
  max_event_bytes: number
  max_events: number
  max_timeout_ms: number
}
type CredentialSummary = {
  id: number
  name: string
  key_prefix: string
  created_at: string
  last_used_at: null
  scopes: string[]
  credential_kind: 'conversation_service'
}

const emit = defineEmits<{ created: [credential: CredentialSummary] }>()
const auth = useAuthStore()

const projects = ref<ProjectChoice[]>([])
const users = ref<User[]>([])
const runtimes = ref<HabitatRuntime[]>([])
const profiles = ref<HarnessDispatchProfile[]>([])
const projectID = ref<number | null>(null)
const runtimeID = ref('')
const accountID = ref('')
const profileID = ref('')
const loading = ref(false)
const creating = ref(false)
const error = ref('')
const ok = ref('')
const verified = ref(false)
const download = ref<EncryptedCredentialDownload | null>(null)
let loadGeneration = 0

const name = ref('')
const projectRef = ref('')
const expiresAt = ref('')
const ageRecipients = ref('')
const actors = ref<ActorRow[]>([{ issuer: '', subject: '', user_id: null }])
const limits = ref<Limits>({
  max_input_bytes: 128 * 1024,
  max_messages: 128,
  max_output_bytes: 256 * 1024,
  max_event_bytes: 8 * 1024,
  max_events: 512,
  max_timeout_ms: 180_000,
})

const limitFields: { key: keyof Limits; label: string; min: number; max: number }[] = [
  { key: 'max_input_bytes', label: 'Input bytes', min: 1, max: 128 * 1024 },
  { key: 'max_messages', label: 'Messages', min: 1, max: 128 },
  { key: 'max_output_bytes', label: 'Output bytes', min: 1, max: 256 * 1024 },
  { key: 'max_event_bytes', label: 'Event bytes', min: 1, max: 8 * 1024 },
  { key: 'max_events', label: 'Events', min: 2, max: 512 },
  { key: 'max_timeout_ms', label: 'Timeout (ms)', min: 1, max: 180_000 },
]

const selectedRuntime = computed(() => runtimes.value.find(runtime => runtime.id === runtimeID.value) ?? null)
const profileKeys = computed(() => new Set(profiles.value.map(profile => `${profile.id}@${profile.version}`)))
const accountOptions = computed(() => {
  const runtime = selectedRuntime.value
  if (!runtime || runtime.schema_version !== 4) return []
  const compatibleLabels = new Set(
    (runtime.account_scopes ?? [])
      .filter(scope => scope.profiles.some(profile => profileKeys.value.has(`${profile.id}@${profile.version}`)))
      .map(scope => scope.account_label),
  )
  return habitatAccountChoices(runtime).filter(choice =>
    compatibleLabels.has(choice.account_label) &&
    !!choice.account_key &&
    Number.isSafeInteger(choice.attachment_revision) &&
    Number(choice.attachment_revision) > 0,
  )
})
const selectedAccount = computed(() =>
  accountOptions.value.find(choice => habitatChoiceId(choice) === accountID.value) ?? null,
)
const profileOptions = computed(() => {
  const runtime = selectedRuntime.value
  const account = selectedAccount.value
  if (!runtime || !account) return []
  const advertised = new Set(
    (runtime.account_scopes ?? [])
      .find(scope => scope.account_label === account.account_label)
      ?.profiles.map(profile => `${profile.id}@${profile.version}`) ?? [],
  )
  return profiles.value.filter(profile => advertised.has(`${profile.id}@${profile.version}`))
})
const selectedProfile = computed(() =>
  profileOptions.value.find(profile => `${profile.id}@${profile.version}` === profileID.value) ?? null,
)

function parseProjects(value: unknown): ProjectChoice[] {
  if (!Array.isArray(value) || value.length > 1000) throw new Error('invalid project list')
  return value.map(candidate => {
    if (
      !ownObject(candidate) || !Number.isSafeInteger(candidate.id) || Number(candidate.id) <= 0 ||
      typeof candidate.key !== 'string' || typeof candidate.name !== 'string'
    ) throw new Error('invalid project list')
    return { id: Number(candidate.id), key: candidate.key, name: candidate.name, ...(typeof candidate.status === 'string' ? { status: candidate.status } : {}) }
  }).filter(project => project.status !== 'deleted')
}

function parseUsers(value: unknown): User[] {
  if (!Array.isArray(value) || value.length > 1000) throw new Error('invalid user list')
  return value.filter(candidate =>
    ownObject(candidate) && Number.isSafeInteger(candidate.id) && Number(candidate.id) > 0 &&
    typeof candidate.username === 'string' && candidate.status === 'active',
  ) as User[]
}

function parseCodexProfiles(value: unknown): HarnessDispatchProfile[] {
  if (!ownObject(value) || !Array.isArray(value.dispatch_profiles) || value.dispatch_profiles.length > 200) {
    throw new Error('invalid dispatch catalog')
  }
  const codex = value.dispatch_profiles.filter(candidate => ownObject(candidate) && candidate.harness === 'codex')
  return parseDispatchProfiles({ dispatch_profiles: codex })
}

async function loadInitialChoices() {
  if (!auth.isAdmin) return
  loading.value = true
  error.value = ''
  try {
    const [projectRows, userRows] = await Promise.all([
      api.get<unknown>('/projects'),
      api.get<unknown>('/users'),
    ])
    projects.value = parseProjects(projectRows)
    users.value = parseUsers(userRows)
  } catch (cause: unknown) {
    error.value = errMsg(cause, 'Enrollment choices are unavailable.')
  } finally {
    loading.value = false
  }
}

async function refreshRuntimeChoices(expectedProjectID: number): Promise<boolean> {
  const generation = ++loadGeneration
  loading.value = true
  try {
    const [runtimeRows, executionOptions] = await Promise.all([
      loadHabitatRuntimes(expectedProjectID),
      api.get<unknown>(`/ai/execution-options?project_id=${expectedProjectID}`),
    ])
    if (generation !== loadGeneration || projectID.value !== expectedProjectID) return false
    runtimes.value = runtimeRows
    profiles.value = parseCodexProfiles(executionOptions)
    return true
  } catch (cause: unknown) {
    if (generation === loadGeneration && projectID.value === expectedProjectID) {
      runtimes.value = []
      profiles.value = []
      error.value = errMsg(cause, 'Current Codex runtime choices are unavailable.')
    }
    return false
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}

watch(projectID, project => {
  loadGeneration++
  runtimeID.value = ''
  accountID.value = ''
  profileID.value = ''
  runtimes.value = []
  profiles.value = []
  verified.value = false
  error.value = ''
  if (project) void refreshRuntimeChoices(project)
})
watch(runtimeID, () => {
  accountID.value = ''
  profileID.value = ''
  verified.value = false
})
watch(accountID, () => {
  profileID.value = ''
  verified.value = false
})
watch(profileID, () => { verified.value = false })
watch([projectRef, actors], () => { verified.value = false }, { deep: true })

function addActor() {
  if (actors.value.length < 64) actors.value.push({ issuer: '', subject: '', user_id: null })
  verified.value = false
}

function removeActor(index: number) {
  if (actors.value.length > 1) actors.value.splice(index, 1)
  verified.value = false
}

function currentBindingSelection() {
  const project = projectID.value
  const runtime = selectedRuntime.value
  const account = selectedAccount.value
  const profile = selectedProfile.value
  if (
    !project || !projects.value.some(choice => choice.id === project) ||
    !runtime || !account || !profile || !account.attachment_revision
  ) return null
  return {
    project_id: project,
    host_id: runtime.machine_id,
    runtime_id: runtime.id,
    runtime_generation: runtime.generation,
    account_label: account.account_label,
    account_key: account.account_key,
    attachment_revision: account.attachment_revision,
    dispatch_profile_id: profile.id,
    dispatch_profile_version: profile.version,
  }
}

function sameBindingSelection(left: ReturnType<typeof currentBindingSelection>, right: ReturnType<typeof currentBindingSelection>) {
  return !!left && !!right && Object.keys(left).every(key =>
    left[key as keyof typeof left] === right[key as keyof typeof right],
  )
}

function printable(value: string, maximum: number) {
  return value === value.trim() && value.length > 0 && new TextEncoder().encode(value).length <= maximum && /^[\x20-\x7e]+$/.test(value)
}

function validateForm() {
  const binding = currentBindingSelection()
  if (!binding) return 'Select a current runtime, attached Codex account, and Codex profile.'
  if (!printable(name.value.trim(), 128) || !printable(projectRef.value.trim(), 128)) return 'Enter a valid credential name and explicit Aithema project reference.'
  const expiry = Date.parse(expiresAt.value.trim())
  if (!Number.isFinite(expiry) || expiry <= Date.now() || expiry > Date.now() + 366 * 24 * 60 * 60 * 1000) return 'Expiry must be a future RFC 3339 timestamp within 366 days.'
  const mappedActors = actors.value.map(actor => ({ issuer: actor.issuer.trim(), subject: actor.subject.trim(), user_id: Number(actor.user_id) }))
  if (mappedActors.length < 1 || mappedActors.length > 64 || mappedActors.some(actor =>
    !printable(actor.issuer, 256) || !printable(actor.subject, 256) || !Number.isSafeInteger(actor.user_id) || actor.user_id <= 0 ||
    !users.value.some(user => user.id === actor.user_id),
  )) return 'Complete every issuer, subject, and Paimos user mapping.'
  const identities = mappedActors.map(actor => `${actor.issuer}\0${actor.subject}`)
  if (new Set(identities).size !== identities.length) return 'Each issuer and subject pair must be unique.'
  for (const field of limitFields) {
    const value = Number(limits.value[field.key])
    if (!Number.isSafeInteger(value) || value < field.min || value > field.max) return `${field.label} must be a whole number from ${field.min} to ${field.max}.`
  }
  const recipients = ageRecipients.value.split(/\r?\n/).map(value => value.trim()).filter(Boolean)
  if (
    recipients.length < 1 || recipients.length > 8 ||
    recipients.some(value =>
      new TextEncoder().encode(value).length > 1024 ||
      !(/^(?:age1|ssh-ed25519 |ssh-rsa )/.test(value)),
    ) ||
    new Set(recipients).size !== recipients.length
  ) {
    return 'Add 1 to 8 unique public age recipients, one per line.'
  }
  if (!verified.value) return 'Verify the project reference and every actor mapping before enrollment.'
  return ''
}

type BindingSelection = NonNullable<ReturnType<typeof currentBindingSelection>>
type ExpectedEnrollment = BindingSelection & {
  name: string
  project_ref: string
  expires_at: string
  limits: Limits
}

function parseEnrollment(value: unknown, expected: ExpectedEnrollment) {
  const responseKeys = ['schema_version', 'id', 'name', 'key_prefix', 'expires_at', 'binding', 'credential_delivery', 'key_age_base64']
  const bindingKeys = ['binding_id', 'revision', 'project_id', 'host_id', 'project_ref', 'runtime_id', 'runtime_generation', 'account_key', 'attachment_revision', 'dispatch_profile_id', 'dispatch_profile_version', 'execution_policy_id', 'limits']
  const limitKeys = limitFields.map(field => field.key)
  if (!ownObject(value) || !hasExactKeys(value, responseKeys) || !ownObject(value.binding) || !hasExactKeys(value.binding, bindingKeys) || !ownObject(value.binding.limits) || !hasExactKeys(value.binding.limits, limitKeys)) {
    throw new Error('invalid encrypted enrollment response')
  }
  const binding = value.binding
  const responseLimits = binding.limits as Record<string, unknown>
  const ciphertext = decodeAgeCiphertext(value.key_age_base64)
  const bindingID = String(binding.binding_id ?? '')
  const exactBinding =
    binding.project_id === expected.project_id && binding.host_id === expected.host_id && binding.project_ref === expected.project_ref &&
    binding.runtime_id === expected.runtime_id && binding.runtime_generation === expected.runtime_generation &&
    binding.account_key === expected.account_key && binding.attachment_revision === expected.attachment_revision &&
    binding.dispatch_profile_id === expected.dispatch_profile_id && binding.dispatch_profile_version === expected.dispatch_profile_version
  const exactLimits = limitKeys.every(key => responseLimits[key] === expected.limits[key])
  if (
    value.schema_version !== 1 || !Number.isSafeInteger(value.id) || Number(value.id) <= 0 ||
    value.name !== expected.name || typeof value.key_prefix !== 'string' || !/^[A-Za-z0-9_]{1,64}$/.test(value.key_prefix) ||
    Date.parse(String(value.expires_at)) !== Date.parse(expected.expires_at) || value.credential_delivery !== 'age' || !ciphertext ||
    !/^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(bindingID) ||
    binding.revision !== 1 || binding.execution_policy_id !== 'aithema-conversation-v1' || !exactBinding || !exactLimits
  ) throw new Error('invalid encrypted enrollment response')
  return { id: Number(value.id), name: value.name as string, keyPrefix: value.key_prefix as string, bindingID, revision: 1, ciphertext }
}

function credentialFilename(credentialName: string, id: number) {
  const stem = credentialName.replace(/[^A-Za-z0-9._-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 64) || `conversation-service-${id}`
  return `${stem}-conversation-service.age`
}

function reviewedFormValues() {
  return {
    name: name.value.trim(),
    project_ref: projectRef.value.trim(),
    expires_at: expiresAt.value.trim(),
    actors: actors.value.map(actor => ({
      issuer: actor.issuer.trim(),
      subject: actor.subject.trim(),
      user_id: Number(actor.user_id),
    })),
    limits: { ...limits.value },
    age_recipients: ageRecipients.value.split(/\r?\n/).map(value => value.trim()).filter(Boolean),
  }
}

async function enroll() {
  if (!auth.isAdmin || creating.value) return
  error.value = ''
  ok.value = ''
  const problem = validateForm()
  if (problem) { error.value = problem; return }
  const reviewed = currentBindingSelection()!
  const reviewedForm = reviewedFormValues()
  const selectedProject = projectID.value!
  creating.value = true
  try {
    if (!await refreshRuntimeChoices(selectedProject)) throw new Error('runtime refresh failed')
    const refreshed = currentBindingSelection()
    if (!sameBindingSelection(reviewed, refreshed)) {
      verified.value = false
      error.value = 'The runtime, account attachment, or profile changed. Review the refreshed selection and verify it again.'
      return
    }
    if (!verified.value || JSON.stringify(reviewedFormValues()) !== JSON.stringify(reviewedForm)) {
      verified.value = false
      error.value = 'Enrollment fields changed during refresh. Review the current values and verify them again.'
      return
    }
    const payload = {
      schema_version: 1,
      name: reviewedForm.name,
      project_id: selectedProject,
      host_id: refreshed!.host_id,
      project_ref: reviewedForm.project_ref,
      runtime_id: refreshed!.runtime_id,
      runtime_generation: refreshed!.runtime_generation,
      account_key: refreshed!.account_key,
      attachment_revision: refreshed!.attachment_revision,
      dispatch_profile_id: refreshed!.dispatch_profile_id,
      dispatch_profile_version: refreshed!.dispatch_profile_version,
      actors: reviewedForm.actors,
      limits: reviewedForm.limits,
      expires_at: reviewedForm.expires_at,
      age_recipients: reviewedForm.age_recipients,
    }
    const raw = await api.post<unknown>('/auth/conversation-services', payload)
    const enrollment = parseEnrollment(raw, {
      ...refreshed!,
      name: reviewedForm.name,
      project_ref: reviewedForm.project_ref,
      expires_at: reviewedForm.expires_at,
      limits: reviewedForm.limits,
    })
    const ready = { filename: credentialFilename(enrollment.name, enrollment.id), ciphertext: Uint8Array.from(enrollment.ciphertext) }
    download.value = ready
    emit('created', {
      id: enrollment.id,
      name: enrollment.name,
      key_prefix: enrollment.keyPrefix,
      created_at: new Date().toISOString().slice(0, 19).replace('T', ' '),
      last_used_at: null,
      scopes: [],
      credential_kind: 'conversation_service',
    })
    ok.value = `Binding ${enrollment.bindingID} revision ${enrollment.revision} is ready. Encrypted download started.`
    name.value = ''
    ageRecipients.value = ''
    verified.value = false
    try {
      downloadEncryptedCredential(ready)
    } catch {
      ok.value = `Binding ${enrollment.bindingID} revision ${enrollment.revision} is ready. Use the download button to try again.`
    }
  } catch (cause: unknown) {
    if (!error.value) error.value = errMsg(cause, 'Failed to create a valid encrypted conversation credential.')
  } finally {
    creating.value = false
  }
}

onMounted(() => { void loadInitialChoices() })
</script>

<template>
  <div class="section" data-testid="conversation-enrollment">
    <div class="section-header">
      <h2 class="section-title">Aithema conversation service</h2>
      <p class="section-desc">Connect Aithema to one current attached Codex account. Paimos binds the selected runtime, profile, limits, and explicit actor mappings; the credential is delivered only as an age-encrypted file.</p>
    </div>
    <form class="card conversation-enrollment" @submit.prevent="enroll">
      <p class="secret-warning">Enter only public age recipients. Never give an agent passwords, cookies, API tokens, private keys, or other secrets.</p>

      <fieldset>
        <legend>Codex connection</legend>
        <div class="conversation-grid">
          <div class="field"><label for="conversation-name">Credential name</label><input id="conversation-name" v-model="name" maxlength="128" required /></div>
          <div class="field"><label for="conversation-project">Paimos project</label>
            <select id="conversation-project" v-model.number="projectID" :disabled="loading" required>
              <option :value="null" disabled>Select a project</option>
              <option v-for="project in projects" :key="project.id" :value="project.id">{{ project.key }} — {{ project.name }}</option>
            </select>
          </div>
          <div class="field"><label for="conversation-runtime">Current runtime</label>
            <select id="conversation-runtime" v-model="runtimeID" :disabled="!projectID || loading" required>
              <option value="" disabled>Select a runtime</option>
              <option v-for="runtime in runtimes" :key="runtime.id" :value="runtime.id">{{ runtime.machine_id }} · {{ runtime.id }}</option>
            </select>
          </div>
          <div class="field"><label for="conversation-account">Attached Codex account</label>
            <select id="conversation-account" v-model="accountID" :disabled="!selectedRuntime || loading" required>
              <option value="" disabled>Select an attached account</option>
              <option v-for="account in accountOptions" :key="habitatChoiceId(account)" :value="habitatChoiceId(account)">{{ account.label }} · {{ account.account_label }}</option>
            </select>
          </div>
          <div class="field"><label for="conversation-profile">Codex profile</label>
            <select id="conversation-profile" v-model="profileID" :disabled="!selectedAccount || loading" required>
              <option value="" disabled>Select a Codex profile</option>
              <option v-for="profile in profileOptions" :key="`${profile.id}@${profile.version}`" :value="`${profile.id}@${profile.version}`">{{ profile.id }}@{{ profile.version }} · {{ profile.model }} / {{ profile.effort }}</option>
            </select>
          </div>
          <div class="field"><label for="conversation-expiry">Expiry (RFC 3339, max 366 days)</label><input id="conversation-expiry" v-model="expiresAt" placeholder="2027-01-01T00:00:00Z" required /></div>
        </div>
        <p v-if="projectID && !loading && runtimes.length === 0" class="empty-hint">No current runtimes are available for this project.</p>
        <p v-else-if="selectedRuntime && !loading && accountOptions.length === 0" class="empty-hint">This runtime has no available attached Codex account with a current attachment revision.</p>
      </fieldset>

      <fieldset>
        <legend>Aithema identity mapping</legend>
        <div class="field"><label for="conversation-project-ref">Aithema project reference</label><input id="conversation-project-ref" v-model="projectRef" maxlength="128" required /></div>
        <div v-for="(actor, index) in actors" :key="index" class="actor-row">
          <div class="field"><label :for="`conversation-issuer-${index}`">Actor issuer</label><input :id="`conversation-issuer-${index}`" v-model="actor.issuer" maxlength="256" required /></div>
          <div class="field"><label :for="`conversation-subject-${index}`">Actor subject</label><input :id="`conversation-subject-${index}`" v-model="actor.subject" maxlength="256" required /></div>
          <div class="field"><label :for="`conversation-user-${index}`">Paimos user</label>
            <select :id="`conversation-user-${index}`" v-model.number="actor.user_id" required>
              <option :value="null" disabled>Select a user</option>
              <option v-for="user in users" :key="user.id" :value="user.id">{{ user.username }} (#{{ user.id }})</option>
            </select>
          </div>
          <button v-if="actors.length > 1" type="button" class="btn btn-ghost btn-sm danger actor-remove" :aria-label="`Remove actor mapping ${index + 1}`" @click="removeActor(index)">Remove</button>
        </div>
        <button type="button" class="btn btn-ghost btn-sm actor-add" :disabled="actors.length >= 64" @click="addActor">+ Add actor mapping</button>
      </fieldset>

      <fieldset>
        <legend>Conversation limits</legend>
        <div class="limit-grid">
          <div v-for="field in limitFields" :key="field.key" class="field">
            <label :for="`conversation-${field.key}`">{{ field.label }}</label>
            <input :id="`conversation-${field.key}`" v-model.number="limits[field.key]" type="number" :min="field.min" :max="field.max" step="1" required />
          </div>
        </div>
      </fieldset>

      <fieldset>
        <legend>Encrypted delivery</legend>
        <div class="field"><label for="conversation-age-recipients">Public age recipients <span class="field-hint">— one native age or supported SSH recipient per line, maximum 8</span></label><textarea id="conversation-age-recipients" v-model="ageRecipients" rows="3" required /></div>
      </fieldset>

      <label class="verification-row"><input v-model="verified" type="checkbox" /> I verified the Aithema project reference, each issuer/subject pair, and its selected Paimos user. No mapping was inferred from email.</label>
      <div v-if="error" class="form-error" role="alert">{{ error }}</div>
      <div v-if="ok" class="ok-banner" role="status">{{ ok }}</div>
      <div class="form-actions">
        <button type="submit" class="btn btn-primary btn-sm" :disabled="creating || loading">{{ creating ? 'Refreshing and enrolling…' : 'Create and download encrypted credential' }}</button>
        <button v-if="download" type="button" class="btn btn-ghost btn-sm" @click="downloadEncryptedCredential(download)">Download encrypted credential again</button>
      </div>
    </form>
  </div>
</template>

<style src="./settings-shared.css"></style>
<style scoped>
.conversation-enrollment { max-width: 900px; }
.conversation-enrollment fieldset { border: 0; border-top: 1px solid var(--border); margin: .2rem 0 0; padding: 1rem 0 0; }
.conversation-enrollment legend { padding: 0 .5rem 0 0; color: var(--text); font-size: 13px; font-weight: 700; }
.conversation-grid, .limit-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: .7rem 1rem; }
.limit-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); }
.actor-row { display: grid; grid-template-columns: 1fr 1fr 1fr auto; align-items: end; gap: .7rem; margin-top: .7rem; }
.actor-add { align-self: flex-start; margin-top: .7rem; }
.actor-remove { margin-bottom: 1px; }
.secret-warning { margin: 0; padding: .6rem .75rem; border: 1px solid var(--border); border-radius: var(--radius); background: var(--bg); color: var(--text); font-size: 13px; }
.verification-row { display: flex; align-items: flex-start; gap: .5rem; color: var(--text); font-size: 13px; line-height: 1.4; }
.verification-row input { margin-top: .15rem; }
.field-hint { font-size: 11px; font-weight: 400; color: var(--text-muted); }
.conversation-enrollment textarea { width: 100%; resize: vertical; font-family: 'DM Mono','Fira Code',monospace; font-size: 12px; }
@media (max-width: 720px) {
  .conversation-grid, .limit-grid, .actor-row { grid-template-columns: 1fr; }
  .actor-remove { justify-self: start; }
}
</style>
