<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import AppIcon from '@/components/AppIcon.vue'
import LoadingText from '@/components/LoadingText.vue'
import { errMsg, api } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import type { User } from '@/types'
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
const partyRows = ref<PartyDraft[]>([])
const gapRows = ref<{ gap_ref: string; statement: string }[]>([])
const previewSubject = ref('')
const previewBody = ref('')
const confirmSend = ref(false)
const selectedRecipients = ref<string[]>([])
const attestedParties = ref<string[]>([])
const confirmAttest = ref(false)
const sendRequestKey = ref(`send-${Date.now()}`)
const externalRaw = ref('')
const externalAttestation = ref('')
const policyRef = ref('policy_customer')
const policyUse = ref('Same approved baseline implementation updates only.')
const policyExpires = ref(defaultLocalExpiry())
const users = ref<User[]>([])
let partySeq = 0

type PartyDraft = {
  key: string
  party_ref: string
  kind: 'linked_user' | 'manual_email'
  user_id: number | null
  email: string
  display_name: string
  required: boolean
  delivery: boolean
  operator: boolean
  support: boolean
}

const ownParty = computed(() => {
  const uid = auth.user?.id
  if (!uid || !acceptance.value) return null
  return acceptance.value.parties.find((p) => p.kind === 'linked_user' && p.user_id === uid) ?? null
})

const linkedUsers = computed(() => users.value.filter((u) => u.status === 'active'))

const modes = [
  { value: 'customer_operated', label: 'customer-operated' },
  { value: 'agency_supported', label: 'agency-supported' },
  { value: 'agency_operated', label: 'provider-operated' },
] as const

function nextPartyRef() {
  partySeq += 1
  return `party_${partySeq}`
}

function emptyParty(): PartyDraft {
  return {
    key: nextPartyRef(),
    party_ref: '',
    kind: 'linked_user',
    user_id: null,
    email: '',
    display_name: '',
    required: true,
    delivery: false,
    operator: false,
    support: false,
  }
}

function onLinkedUser(row: PartyDraft) {
  const user = linkedUsers.value.find((u) => u.id === row.user_id)
  if (!user) return
  row.email = user.email || row.email
  row.display_name = user.nickname || `${user.first_name} ${user.last_name}`.trim() || user.username
}

function addParty() {
  partyRows.value = [...partyRows.value, emptyParty()]
}

function removeParty(key: string) {
  partyRows.value = partyRows.value.filter((p) => p.key !== key)
}

function addGap() {
  gapRows.value = [...gapRows.value, { gap_ref: `gap_${gapRows.value.length + 1}`, statement: '' }]
}

function removeGap(index: number) {
  gapRows.value = gapRows.value.filter((_, i) => i !== index)
}

function slugRef(prefix: string, label: string, fallback: string) {
  const slug = label.toLowerCase().replace(/[^a-z0-9]+/g, '_').replace(/^_|_$/g, '')
  return slug ? `${prefix}_${slug}` : fallback
}

function defaultLocalExpiry() {
  const d = new Date()
  d.setDate(d.getDate() + 90)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function localInputToRFC3339(value: string) {
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return d.toISOString()
}

function partyName(ref: string) {
  const party = acceptance.value?.parties.find((p) => p.party_ref === ref)
  return party?.display_name || party?.email || ref
}

function mailState(row: { display_state?: string; state: string }) {
  return row.display_state || row.state
}

const attestSummary = computed(() => {
  if (!attestedParties.value.length) {
    return 'None selected. Recipients of the recorded email are not consent. Linked members can still confirm themselves.'
  }
  const names = attestedParties.value.map((ref) => partyName(ref)).join(', ')
  return `${names} selected. Recipients of the recorded email are not consent. Linked members can still confirm themselves.`
})

const canAuthorizeSend = computed(() => {
  return !busy.value && confirmSend.value && !acceptance.value?.mail_in_flight && selectedRecipients.value.length > 0
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [list, workflow, policyList, userList] = await Promise.all([
      listReleaseRecords(props.projectId),
      getBaselineWorkflow(props.projectId).catch(() => null),
      listStandingPolicies(props.projectId).catch(() => []),
      api.get<User[]>('/users').catch(() => [] as User[]),
    ])
    records.value = list
    policies.value = policyList
    users.value = userList
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
  gapRows.value = (acc.disclosed_gaps ?? []).map((g) => ({ gap_ref: g.gap_ref, statement: g.statement }))
  partyRows.value = (acc.parties ?? []).map((p) => ({
    key: p.party_ref,
    party_ref: p.party_ref,
    kind: p.kind,
    user_id: p.user_id ?? null,
    email: p.email,
    display_name: p.display_name,
    required: (acc.required_party_refs ?? []).includes(p.party_ref),
    delivery: acc.delivery_party_ref === p.party_ref,
    operator: acc.operator_party_ref === p.party_ref,
    support: acc.support_party_ref === p.party_ref,
  }))
  if (!partyRows.value.length && props.canWrite) {
    const delivery = emptyParty()
    delivery.delivery = true
    delivery.display_name = 'Delivery'
    const operator = emptyParty()
    operator.operator = true
    operator.display_name = 'Customer'
    partyRows.value = [delivery, operator]
  }
  previewSubject.value = acc.preview_subject || ''
  previewBody.value = acc.preview_body || ''
  selectedRecipients.value = []
  attestedParties.value = []
  confirmAttest.value = false
  confirmSend.value = false
  if (!acc.mail_in_flight) {
    sendRequestKey.value = `send-${Date.now()}`
  }
}

function configureBody(): ConfigureRequest {
  const parties = partyRows.value.map((row, index) => {
    const party_ref = row.party_ref || slugRef('party', row.display_name, `party_${index + 1}`)
    row.party_ref = party_ref
    const roles = ['acceptance_party']
    if (row.delivery) roles.push('delivery_party')
    if (row.operator) roles.push('operator')
    if (row.support) roles.push('support')
    return {
      party_ref,
      kind: row.kind,
      user_id: row.kind === 'linked_user' ? row.user_id || undefined : undefined,
      email: row.email,
      display_name: row.display_name || party_ref,
      roles,
    }
  })
  const delivery = partyRows.value.find((p) => p.delivery)?.party_ref || parties[0]?.party_ref || ''
  const operator = partyRows.value.find((p) => p.operator)?.party_ref || parties[1]?.party_ref || delivery
  const support = partyRows.value.find((p) => p.support)?.party_ref
  return {
    expected_revision: acceptance.value?.revision,
    operating_mode: mode.value,
    agreement_ref: agreement.value,
    disclosed_gaps: gapRows.value.filter((g) => g.statement.trim()).map((g, i) => ({
      gap_ref: g.gap_ref || `gap_${i + 1}`,
      statement: g.statement.trim(),
    })),
    parties,
    delivery_party_ref: delivery,
    operator_party_ref: operator,
    support_party_ref: support || null,
    required_party_refs: partyRows.value.filter((p) => p.required).map((p) => p.party_ref).filter(Boolean),
  }
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
        <div v-if="canWrite" class="ra-stack" data-testid="gap-editor">
          <strong class="ra-meta">Disclosed gaps</strong>
          <div v-for="(gap, index) in gapRows" :key="index" class="ra-row">
            <input v-model="gap.gap_ref" class="ra-input" placeholder="label" aria-label="Gap label">
            <input v-model="gap.statement" class="ra-input" placeholder="What is not proven" aria-label="Gap statement">
            <button type="button" class="btn btn-sm" @click="removeGap(index)">Remove</button>
          </div>
          <button type="button" class="btn btn-sm" data-testid="add-gap" @click="addGap">Add gap</button>
        </div>
        <ul v-else class="ra-list">
          <li v-for="g in acceptance.disclosed_gaps" :key="g.gap_ref">{{ g.statement }}</li>
          <li v-if="!acceptance.disclosed_gaps.length">None disclosed.</li>
        </ul>
        <div v-if="canWrite" class="ra-stack" data-testid="party-editor">
          <strong class="ra-meta">Parties</strong>
          <div v-for="row in partyRows" :key="row.key" class="ra-party">
            <label>Name <input v-model="row.display_name" class="ra-input"></label>
            <label>
              Kind
              <select v-model="row.kind" class="ra-input">
                <option value="linked_user">Project member</option>
                <option value="manual_email">External email</option>
              </select>
            </label>
            <label v-if="row.kind === 'linked_user'">
              Member
              <select v-model.number="row.user_id" class="ra-input" @change="onLinkedUser(row)">
                <option :value="null">Select a member</option>
                <option v-for="u in linkedUsers" :key="u.id" :value="u.id">{{ u.username }}{{ u.email ? ` · ${u.email}` : '' }}</option>
              </select>
            </label>
            <label>
              Email
              <input v-model="row.email" class="ra-input" type="email" :placeholder="row.kind === 'manual_email' ? 'external@example.com' : ''">
            </label>
            <div class="ra-row">
              <label><input v-model="row.required" type="checkbox"> Required</label>
              <label><input v-model="row.delivery" type="checkbox"> Delivery</label>
              <label><input v-model="row.operator" type="checkbox"> Operator</label>
              <label><input v-model="row.support" type="checkbox"> Support</label>
            </div>
            <button type="button" class="btn btn-sm" @click="removeParty(row.key)">Remove party</button>
          </div>
          <button type="button" class="btn btn-sm" data-testid="add-party" @click="addParty">Add party</button>
        </div>
        <ul v-else class="ra-list">
          <li v-for="p in acceptance.parties" :key="p.party_ref">{{ p.display_name }} · {{ p.kind }} · {{ p.email }}</li>
        </ul>
        <button
          v-if="canWrite && acceptance.status !== 'accepted'"
          type="button"
          class="btn btn-sm"
          data-testid="save-arrangement"
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
          <li v-for="ref in acceptance.missing.confirmations" :key="'c-'+ref">Confirmation: {{ partyName(ref) }}</li>
          <li v-for="ref in acceptance.missing.email_coverage" :key="'e-'+ref">Sent email covering {{ partyName(ref) }}</li>
          <li v-if="!acceptance.missing.preview && !acceptance.missing.confirmations.length && !acceptance.missing.email_coverage.length">Nothing missing.</li>
        </ul>
        <ul class="ra-list" data-testid="confirmation-list">
          <li v-for="c in acceptance.confirmations" :key="c.party_ref + c.confirmed_at">
            {{ c.party_name || partyName(c.party_ref) }} · {{ c.source_label || c.source }} · revision {{ c.acceptance_revision || acceptance.revision }}
          </li>
        </ul>
        <ul v-if="acceptance.email_evidence.length" class="ra-list" data-testid="email-evidence">
          <li v-for="e in acceptance.email_evidence" :key="e.message_ref">
            {{ mailState(e) }} · {{ (e.recipient_names && e.recipient_names.length) ? e.recipient_names.join(', ') : e.recipient_party_refs.map(partyName).join(', ') }}
            · {{ e.recorded_at }}{{ e.sent_at ? ` · sent ${e.sent_at}` : '' }}
            · {{ e.source === 'platform_send' ? 'platform send' : 'recorded email' }}
          </li>
        </ul>
        <p v-if="acceptance.mail_recovery" class="ra-error" data-testid="mail-recovery">{{ acceptance.mail_recovery }}</p>
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
        <p v-else-if="!ownParty" class="ra-note">You can confirm on the platform only as a linked project member. External email parties need a recorded attestation.</p>
      </div>

      <div v-if="canWrite" class="ra-card">
        <label>Subject <input v-model="previewSubject" class="ra-input"></label>
        <label>Message <textarea v-model="previewBody" rows="4" class="ra-input" /></label>
        <div class="ra-row">
          <button type="button" class="btn btn-sm" data-testid="save-preview" :disabled="busy" @click="run(() => saveAcceptancePreview(projectId, acceptance!.release.id, previewSubject, previewBody))">
            Save preview
          </button>
        </div>
        <fieldset class="ra-stack" data-testid="recipient-picker">
          <legend class="ra-meta">Email recipients</legend>
          <label v-for="p in acceptance.parties" :key="'r-'+p.party_ref">
            <input v-model="selectedRecipients" type="checkbox" :value="p.party_ref" data-testid="recipient-party">
            {{ p.display_name }} · {{ p.email }}
          </label>
        </fieldset>
        <label class="ra-confirm">
          <input v-model="confirmSend" type="checkbox" data-testid="confirm-send">
          I authorize sending this reviewed message to these recipients. SMTP success is transport evidence, not consent.
        </label>
        <button
          type="button"
          class="btn btn-sm"
          data-testid="authorize-send"
          :disabled="!canAuthorizeSend"
          @click="run(() => authorizeAcceptanceSend(projectId, acceptance!.release.id, {
            request_key: sendRequestKey,
            recipient_party_refs: selectedRecipients,
            preview_revision: acceptance!.preview_revision,
            confirm_send: confirmSend,
          }))"
        >
          Authorize send
        </button>
        <p v-if="acceptance.mail_in_flight" class="ra-note">A send is already queued or in flight. Wait for it to finish; do not start another.</p>
        <label>Manual received email
          <textarea v-model="externalRaw" rows="4" class="ra-input" data-testid="external-raw" />
        </label>
        <label>Attestation (recorder, not sender identity)
          <input v-model="externalAttestation" class="ra-input" data-testid="external-attestation">
        </label>
        <fieldset class="ra-stack" data-testid="attest-picker">
          <legend class="ra-meta">Attest acceptance for</legend>
          <p class="ra-note" data-testid="attest-summary">{{ attestSummary }}</p>
          <label v-for="p in acceptance.parties" :key="'a-'+p.party_ref">
            <input v-model="attestedParties" type="checkbox" :value="p.party_ref" data-testid="attest-party">
            {{ p.display_name }}
          </label>
        </fieldset>
        <label class="ra-confirm">
          <input v-model="confirmAttest" type="checkbox" data-testid="confirm-attest">
          I attest, as the recording human, that these selected parties accepted this same release revision. This does not verify the sender.
        </label>
        <button
          type="button"
          class="btn btn-sm"
          data-testid="record-external"
          :disabled="busy || !externalRaw.trim() || !externalAttestation.trim() || (attestedParties.length > 0 && !confirmAttest)"
          @click="run(() => recordExternalAcceptanceEmail(projectId, acceptance!.release.id, {
            request_key: `ext-${Date.now()}`,
            recipient_party_refs: selectedRecipients,
            raw_message: externalRaw,
            attestation: externalAttestation,
            attested_party_refs: attestedParties,
            confirm_attest: confirmAttest,
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
        <label>Expires
          <input v-model="policyExpires" class="ra-input" type="datetime-local" data-testid="policy-expires">
        </label>
        <p class="ra-meta" data-testid="deployment-target">
          <template v-if="acceptance.deployment_target">Deployment target: {{ acceptance.deployment_target }}</template>
          <template v-else>{{ acceptance.target_unknown_reason || 'Deployment target is unknown. Standing policy cannot apply until an explicit target is bound.' }}</template>
        </p>
        <button
          type="button"
          class="btn btn-sm"
          data-testid="approve-policy"
          :disabled="busy || !acceptance.release.content_digest"
          @click="run(() => approveStandingPolicy(projectId, {
            policy_ref: policyRef,
            content_digest: acceptance!.release.content_digest,
            revision_seal: acceptance!.release.revision_seal,
            parties: partyRows.filter((p) => p.required).map((p) => p.party_ref).filter(Boolean),
            agreement_ref: agreement,
            gaps: gapRows.filter((g) => g.statement.trim()),
            bounded_use: policyUse,
            expires_at: localInputToRFC3339(policyExpires),
            release_channel: acceptance!.release.release_channel,
            artifact_digest: acceptance!.release.artifact_digest,
            target_ref: acceptance!.deployment_target || '',
            model_ref: 'customer_operated',
          }))"
        >
          Approve policy
        </button>
        <ul class="ra-list">
          <li v-for="p in policies" :key="p.id">
            {{ p.policy_ref }} · {{ p.revoked_at ? 'revoked' : 'active' }}
            <button v-if="!p.revoked_at" type="button" class="btn btn-sm" @click="run(() => revokeStandingPolicy(projectId, p.id))">Revoke</button>
            <button
              v-if="!p.revoked_at && ownParty"
              type="button"
              class="btn btn-sm"
              data-testid="apply-policy"
              :disabled="busy || !acceptance.deployment_target"
              @click="run(() => applyStandingPolicy(projectId, acceptance!.release.id, p.id))"
            >
              Apply to me
            </button>
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
.ra-stack, .ra-party {
  display: flex;
  flex-direction: column;
  gap: .45rem;
  min-width: 0;
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
