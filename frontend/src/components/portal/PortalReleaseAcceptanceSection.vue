<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { errMsg } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import {
  portalConfirmReleaseAcceptance,
  portalGetReleaseAcceptance,
  portalListReleaseRecords,
  type Acceptance,
} from '@/services/projectReleaseAcceptance'

const props = defineProps<{ projectId: number }>()
const auth = useAuthStore()
const loading = ref(true)
const error = ref('')
const busy = ref(false)
const acceptance = ref<Acceptance | null>(null)

const ownParty = computed(() => {
  const uid = auth.user?.id
  if (!uid || !acceptance.value) return null
  return acceptance.value.parties.find((p) => p.kind === 'linked_user' && p.user_id === uid) ?? null
})

function partyName(ref: string) {
  const party = acceptance.value?.parties.find((p) => p.party_ref === ref)
  return party?.display_name || party?.email || ref
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const list = await portalListReleaseRecords(props.projectId)
    if (!list.length) {
      acceptance.value = null
      return
    }
    acceptance.value = await portalGetReleaseAcceptance(props.projectId, list[0].id)
  } catch (e) {
    error.value = errMsg(e, 'Failed to load release acceptance.')
  } finally {
    loading.value = false
  }
}

async function confirm() {
  if (!acceptance.value || !ownParty.value) return
  busy.value = true
  error.value = ''
  try {
    acceptance.value = await portalConfirmReleaseAcceptance(props.projectId, acceptance.value.release.id, ownParty.value.party_ref)
  } catch (e) {
    error.value = errMsg(e, 'Could not confirm this release.')
  } finally {
    busy.value = false
  }
}

onMounted(() => { void load() })
</script>

<template>
  <section v-if="loading || error || acceptance" class="pra" data-testid="portal-release-acceptance">
    <h2>Release acceptance</h2>
    <p v-if="error" class="pra-error">{{ error }}</p>
    <template v-if="acceptance">
      <p class="pra-meta">{{ acceptance.release.release_ref }} · {{ acceptance.release.version }} · {{ acceptance.operating_mode_label }} · {{ acceptance.status }}</p>
      <p class="pra-note">{{ acceptance.offer_disclaimer }}</p>
      <ul class="pra-list">
        <li v-for="g in acceptance.disclosed_gaps" :key="g.gap_ref">{{ g.statement }}</li>
      </ul>
      <ul class="pra-list" data-testid="portal-confirmations">
        <li v-for="c in acceptance.confirmations" :key="c.party_ref + c.confirmed_at">
          {{ c.party_name || c.party_ref }} · {{ c.source_label || c.source }} · revision {{ c.acceptance_revision || acceptance.revision }}
        </li>
      </ul>
      <p v-if="acceptance.missing.confirmations.length" class="pra-meta">
        Still missing confirmation from:
        {{ acceptance.missing.confirmations.map(partyName).join(', ') }}
      </p>
      <button
        v-if="ownParty && acceptance.status !== 'accepted'"
        type="button"
        class="btn btn-sm"
        :disabled="busy"
        @click="confirm"
      >
        Confirm as {{ ownParty.display_name }}
      </button>
      <p v-else-if="!ownParty" class="pra-note">You can confirm only if you are a linked party on this release.</p>
    </template>
  </section>
</template>

<style scoped>
.pra {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: .8rem;
  display: flex;
  flex-direction: column;
  gap: .45rem;
  min-width: 0;
  margin: 0 0 1rem;
}
.pra h2 {
  margin: 0;
  font-size: 13px;
  text-transform: uppercase;
  letter-spacing: .04em;
}
.pra-meta, .pra-note { margin: 0; font-size: 12px; color: var(--text-muted); overflow-wrap: anywhere; }
.pra-error { color: var(--danger, #94513c); font-size: 12px; }
.pra-list { margin: 0; padding-left: 1.1rem; font-size: 12px; }
</style>
