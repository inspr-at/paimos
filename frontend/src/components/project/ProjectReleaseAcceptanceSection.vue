<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import LoadingText from '@/components/LoadingText.vue'
import { errMsg } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { getBaselineWorkflow } from '@/services/projectBaselineBatch'
import {
  applyStandingPolicy,
  approveStandingPolicy,
  authorizeAcceptanceSend,
  configureReleaseAcceptance,
  confirmReleaseAcceptance,
  downloadAcceptanceEvidence,
  getReleaseAcceptance,
  listReleaseRecords,
  listStandingPolicies,
  mintReleaseRecord,
  recordExternalAcceptanceEmail,
  revokeStandingPolicy,
  saveAcceptancePreview,
  type Acceptance,
  type ConfigureRequest,
  type Party,
  type StandingPolicy,
} from '@/services/projectReleaseAcceptance'

const props = defineProps<{
  projectId: number
  canWrite: boolean
}>()

const auth = useAuthStore()
const loading = ref(true)
const error = ref('')
const busy = ref(false)
const records = ref<{ id: number; batch_id: number; release_ref: string; state: string }[]>([])
const selectedId = ref(0)
const acceptance = ref<Acceptance | null>(null)
const policies = ref<StandingPolicy[]>([])
const batches = ref<{ id: number; batch_key: string }[]>([])
const mintBatchId = ref(0)

const mode = ref('customer_operated')
const agreement = ref('')
const gapsText = ref('')
const partiesText = ref('')
const deliveryRef = ref('party_delivery')
const operatorRef = ref('party_operator')
const requiredText = ref('party_delivery, party_operator')
const previewSubject = ref('')
const previewBody = ref('')
const confirmSend = ref(false)
const recipientsText = ref('')
const externalRaw = ref('')
const externalAttestation = ref('')
const policyRef = ref('policy_customer')
const policyUse = ref('Same approved baseline implementation updates only.')
const policyExpires = ref('2026-12-01T00:00:00Z')

const ownParty = computed(() => {
  const uid = auth.user?.id
  if (!uid || !acceptance.value) return null
  return acceptance.value.parties.find((p) => p.kind === 'linked_user' && p.user_id === uid) ?? null
})

const modes = [
  { value: 'customer_operated', label: 'customer-operated' },
  { value: 'agency_supported', label: 'agency-supported' },
  { value: 'agency_operated', label: 'provider-operated' },
] as const

function parseGaps(text: string) {
  return text.split('\n').map((line) => line.trim()).filter(Boolean).map((line) => {
    const [ref, ...rest] = line.split('|')
    return { gap_ref: ref.trim(), statement: rest.join('|').trim() || ref.trim() }
  }).filter((g) => g.gap_ref)
}

function parseParties(text: string): ConfigureRequest['parties'] {
  return text.split('\n').map((line) => line.trim()).filter(Boolean).map((line) => {
    const [party_ref, kind, user, email, name, roles] = line.split('|').map((p) => p.trim())
    return {
      party_ref,
      kind: (kind === 'manual_email' ? 'manual_email' : 'linked_user') as Party['kind'],
      user_id: user ? Number(user) : undefined,
      email,
      display_name: name || party_ref,
      roles: (roles || 'acceptance_party').split(',').map((r) => r.trim()).filter(Boolean),
    }
  }).filter((p) => p.party_ref && p.email)
}

function refs(text: string) {
  return text.split(/[,\s]+/).map((s) => s.trim()).filter(Boolean)
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [list, workflow, policyList] = await Promise.all([
      listReleaseRecords(props.projectId),
      getBaselineWorkflow(props.projectId).catch(() => null),
      listStandingPolicies(props.projectId).catch(() => []),
    ])
    records.value = list
    policies.value = policyList
    batches.value = (workflow?.batches ?? []).map((b) => ({ id: b.id, batch_key: b.batch_key }))
    if (workflow?.active_batch) {
      mintBatchId.value = workflow.active_batch.id
    } else if (batches.value[0]) {
      mintBatchId.value = batches.value[0].id
    }
    if (!selectedId.value && list[0]) selectedId.value = list[0].id
    if (selectedId.value) {
      acceptance.value = await getReleaseAcceptance(props.projectId, selectedId.value)
      syncForm(acceptance.value)
    } else {
      acceptance.value = null
    }
  } catch (e) {
    error.value = errMsg(e, 'Failed to load release acceptance.')
  } finally {
    loading.value = false
  }
}

function syncForm(acc: Acceptance) {
  mode.value = acc.operating_mode || 'customer_operated'
  agreement.value = acc.agreement_ref || acc.defaults.agreement_ref || ''
  gapsText.value = (acc.disclosed_gaps ?? []).map((g) => `${g.gap_ref} | ${g.statement}`).join('\n')
  partiesText.value = (acc.parties ?? []).map((p) =>
    [p.party_ref, p.kind, p.user_id ?? '', p.email, p.display_name, (p.roles ?? []).join(',')].join(' | '),
  ).join('\n')
  deliveryRef.value = acc.delivery_party_ref || deliveryRef.value
  operatorRef.value = acc.operator_party_ref || operatorRef.value
  requiredText.value = (acc.required_party_refs ?? []).join(', ')
  previewSubject.value = acc.preview_subject || ''
  previewBody.value = acc.preview_body || ''
  recipientsText.value = (acc.parties ?? []).map((p) => p.party_ref).join(', ')
}

async function run(fn: () => Promise<Acceptance | StandingPolicy | void>) {
  busy.value = true
  error.value = ''
  try {
    const out = await fn()
    if (out && 'release' in out) {
      acceptance.value = out
      selectedId.value = out.release.id
      syncForm(out)
    }
    await load()
  } catch (e) {
    error.value = errMsg(e, 'Release acceptance action failed.')
  } finally {
    busy.value = false
  }
}

function configureBody(): ConfigureRequest {
  return {
    expected_revision: acceptance.value?.revision,
    operating_mode: mode.value,
    agreement_ref: agreement.value,
    disclosed_gaps: parseGaps(gapsText.value),
    parties: parseParties(partiesText.value),
    delivery_party_ref: deliveryRef.value,
    operator_party_ref: operatorRef.value,
    required_party_refs: refs(requiredText.value),
  }
}

watch(() => props.projectId, () => { void load() })
watch(selectedId, async (id) => {
  if (!id) return
  try {
    acceptance.value = await getReleaseAcceptance(props.projectId, id)
    syncForm(acceptance.value)
  } catch (e) {
    error.value = errMsg(e, 'Failed to load release.')
  }
})
onMounted(() => { void load() })
</script>

<template>
  <section class="ra" data-testid="release-acceptance">
    <header class="ra-head">
      <AppIcon name="package-check" :size="14" />
      <h3>Release acceptance</h3>
    </header>
    <p class="ra-note">
      Go-live acceptance for one built release. Deployment stays separate and is never inferred from accepted state.
    </p>
    <p v-if="acceptance" class="ra-disclaimer" data-testid="offer-disclaimer">{{ acceptance.offer_disclaimer }}</p>
    <p v-else class="ra-disclaimer" data-testid="offer-disclaimer">
      Provider-operated is representable here. It is not an offer that Augmentoring will operate the service.
    </p>
    <LoadingText v-if="loading" />
    <p v-if="error" class="ra-error">{{ error }}</p>

    <div v-if="canWrite && batches.length" class="ra-card ra-row">
      <label>
        Mint from built batch
        <select v-model.number="mintBatchId" class="ra-input">
          <option v-for="b in batches" :key="b.id" :value="b.id">{{ b.batch_key }} (#{{ b.id }})</option>
        </select>
      </label>
      <button type="button" class="btn btn-sm" :disabled="busy || !mintBatchId" @click="run(() => mintReleaseRecord(projectId, mintBatchId))">
        Mint release record
      </button>
    </div>

    <div v-if="records.length" class="ra-card">
      <label>
        Release
        <select v-model.number="selectedId" class="ra-input">
          <option v-for="r in records" :key="r.id" :value="r.id">{{ r.release_ref }} · {{ r.state }}</option>
        </select>
      </label>
    </div>
    <p v-else class="ra-meta">No release record yet. Mint from a baseline batch after its built receipt exists.</p>

    <template v-if="acceptance">
      <div class="ra-card">
        <p class="ra-meta" data-testid="release-identity">
          {{ acceptance.release.release_ref }} · {{ acceptance.release.version }} ·
          {{ acceptance.release.artifact_digest }} · {{ acceptance.status }} · revision {{ acceptance.revision }}
        </p>
        <p class="ra-meta">Arrangement: {{ acceptance.operating_mode_label || 'unset' }}</p>
        <fieldset class="ra-modes">
          <legend>Operating arrangement</legend>
          <label v-for="item in modes" :key="item.value">
            <input v-model="mode" type="radio" :value="item.value" :disabled="!canWrite || acceptance.status === 'accepted'">
            {{ item.label }}
          </label>
        </fieldset>
        <p class="ra-note">A knowledgeable customer may proceed with disclosed gaps. Missing operator or backup/restore is a disclosure, not a universal legal requirement. No agency consent is inferred.</p>
        <label v-if="canWrite">
          Agreement reference
          <input v-model="agreement" class="ra-input" :placeholder="acceptance.defaults.agreement_ref || 'agreement'">
        </label>
        <label v-if="canWrite">
          Disclosed gaps (ref | statement)
          <textarea v-model="gapsText" rows="3" class="ra-input" />
        </label>
        <ul v-else class="ra-list">
          <li v-for="g in acceptance.disclosed_gaps" :key="g.gap_ref">{{ g.gap_ref }}: {{ g.statement }}</li>
          <li v-if="!acceptance.disclosed_gaps.length">None disclosed.</li>
        </ul>
        <label v-if="canWrite">
          Parties (ref | kind | user_id | email | name | roles)
          <textarea v-model="partiesText" rows="3" class="ra-input" />
        </label>
        <ul v-else class="ra-list">
          <li v-for="p in acceptance.parties" :key="p.party_ref">{{ p.display_name }} · {{ p.party_ref }} · {{ p.kind }}</li>
        </ul>
        <div v-if="canWrite" class="ra-row">
          <label>Delivery <input v-model="deliveryRef" class="ra-input"></label>
          <label>Operator <input v-model="operatorRef" class="ra-input"></label>
          <label>Required <input v-model="requiredText" class="ra-input"></label>
        </div>
        <button
          v-if="canWrite && acceptance.status !== 'accepted'"
          type="button"
          class="btn btn-sm"
          :disabled="busy"
          @click="run(() => configureReleaseAcceptance(projectId, acceptance!.release.id, configureBody()))"
        >
          Save arrangement
        </button>
      </div>

      <div class="ra-card">
        <strong class="ra-meta">Missing</strong>
        <ul class="ra-list" data-testid="missing-list">
          <li v-if="acceptance.missing.preview">Reviewable message</li>
          <li v-for="ref in acceptance.missing.confirmations" :key="'c-'+ref">Confirmation: {{ ref }}</li>
          <li v-for="ref in acceptance.missing.email_coverage" :key="'e-'+ref">Sent email covering {{ ref }}</li>
          <li v-if="!acceptance.missing.preview && !acceptance.missing.confirmations.length && !acceptance.missing.email_coverage.length">Nothing missing.</li>
        </ul>
        <ul class="ra-list">
          <li v-for="c in acceptance.confirmations" :key="c.party_ref + c.confirmed_at">
            {{ c.party_ref }} · {{ c.source }} · actor {{ c.actor_user_id }}
          </li>
        </ul>
        <button
          v-if="ownParty && acceptance.status !== 'accepted'"
          type="button"
          class="btn btn-sm"
          data-testid="confirm-own"
          :disabled="busy"
          @click="run(() => confirmReleaseAcceptance(projectId, acceptance!.release.id, ownParty!.party_ref))"
        >
          Confirm as {{ ownParty.display_name }}
        </button>
      </div>

      <div v-if="canWrite" class="ra-card">
        <label>Subject <input v-model="previewSubject" class="ra-input"></label>
        <label>Message <textarea v-model="previewBody" rows="4" class="ra-input" /></label>
        <div class="ra-row">
          <button type="button" class="btn btn-sm" :disabled="busy" @click="run(() => saveAcceptancePreview(projectId, acceptance!.release.id, previewSubject, previewBody))">
            Save preview
          </button>
        </div>
        <label>Recipients <input v-model="recipientsText" class="ra-input"></label>
        <label class="ra-confirm">
          <input v-model="confirmSend" type="checkbox">
          I authorize sending this reviewed message to these recipients. SMTP success is transport evidence, not consent.
        </label>
        <button
          type="button"
          class="btn btn-sm"
          :disabled="busy || !confirmSend"
          @click="run(() => authorizeAcceptanceSend(projectId, acceptance!.release.id, {
            request_key: `send-${Date.now()}`,
            recipient_party_refs: refs(recipientsText),
            preview_revision: acceptance!.preview_revision,
            confirm_send: confirmSend,
          }))"
        >
          Authorize send
        </button>
        <label>Manual received email
          <textarea v-model="externalRaw" rows="4" class="ra-input" />
        </label>
        <label>Attestation (recorder, not sender identity)
          <input v-model="externalAttestation" class="ra-input">
        </label>
        <button
          type="button"
          class="btn btn-sm"
          :disabled="busy || !externalRaw.trim() || !externalAttestation.trim()"
          @click="run(() => recordExternalAcceptanceEmail(projectId, acceptance!.release.id, {
            request_key: `ext-${Date.now()}`,
            recipient_party_refs: refs(recipientsText),
            raw_message: externalRaw,
            attestation: externalAttestation,
            attested_party_refs: refs(recipientsText),
          }))"
        >
          Record external email
        </button>
      </div>

      <div class="ra-card ra-row">
        <button type="button" class="btn btn-sm" @click="downloadAcceptanceEvidence(projectId, acceptance.release.id, 'json')">JSON</button>
        <button type="button" class="btn btn-sm" @click="downloadAcceptanceEvidence(projectId, acceptance.release.id, 'eml')">EML</button>
        <button type="button" class="btn btn-sm" @click="downloadAcceptanceEvidence(projectId, acceptance.release.id, 'html')">Printable HTML</button>
      </div>

      <div v-if="canWrite && mode === 'customer_operated'" class="ra-card">
        <strong class="ra-meta">Standing policy (customer-operated only; does not send mail)</strong>
        <label>Policy ref <input v-model="policyRef" class="ra-input"></label>
        <label>Bounded use <input v-model="policyUse" class="ra-input"></label>
        <label>Expires <input v-model="policyExpires" class="ra-input"></label>
        <button
          type="button"
          class="btn btn-sm"
          :disabled="busy || !acceptance.release.content_digest"
          @click="run(() => approveStandingPolicy(projectId, {
            policy_ref: policyRef,
            content_digest: acceptance!.release.content_digest,
            revision_seal: acceptance!.release.revision_seal,
            parties: refs(requiredText),
            agreement_ref: agreement,
            gaps: parseGaps(gapsText),
            bounded_use: policyUse,
            expires_at: policyExpires,
            release_channel: acceptance!.release.release_channel,
            artifact_digest: acceptance!.release.artifact_digest,
          }))"
        >
          Approve policy
        </button>
        <ul class="ra-list">
          <li v-for="p in policies" :key="p.id">
            {{ p.policy_ref }} · {{ p.revoked_at ? 'revoked' : 'active' }}
            <button v-if="!p.revoked_at" type="button" class="btn btn-sm" @click="run(() => revokeStandingPolicy(projectId, p.id))">Revoke</button>
            <button v-if="!p.revoked_at && ownParty" type="button" class="btn btn-sm" @click="run(() => applyStandingPolicy(projectId, acceptance!.release.id, p.id))">Apply to me</button>
          </li>
        </ul>
      </div>
    </template>
  </section>
</template>

<style scoped>
.ra {
  display: flex;
  flex-direction: column;
  gap: .7rem;
  min-width: 0;
  max-width: 100%;
}
.ra-head {
  display: flex;
  align-items: center;
  gap: .4rem;
  flex-wrap: wrap;
}
.ra-head h3 {
  margin: 0;
  font-size: 13px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: .04em;
}
.ra-note, .ra-meta, .ra-disclaimer {
  font-size: 12px;
  color: var(--text-muted);
  overflow-wrap: anywhere;
  margin: 0;
}
.ra-error { color: var(--danger, #94513c); font-size: 12px; }
.ra-card {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: .8rem;
  display: flex;
  flex-direction: column;
  gap: .55rem;
  min-width: 0;
}
.ra-row {
  display: flex;
  flex-wrap: wrap;
  gap: .45rem;
  align-items: center;
}
.ra-input {
  width: 100%;
  min-width: 0;
}
.ra-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: .3rem;
  font-size: 12px;
}
.ra-modes {
  border: 0;
  padding: 0;
  display: flex;
  flex-wrap: wrap;
  gap: .6rem;
  font-size: 12px;
}
.ra-confirm {
  display: flex;
  gap: .4rem;
  align-items: flex-start;
  font-size: 12px;
}
@media (max-width: 720px) {
  .ra-row, .ra-modes { flex-direction: column; align-items: stretch; }
}
</style>
