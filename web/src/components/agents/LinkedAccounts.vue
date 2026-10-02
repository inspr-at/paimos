<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { listAccountLinks, unlinkAccount, type AccountLinkReview } from '../../lib/accountLink'
import { harnessLabel } from '../../lib/agentState'
import { can } from '../../lib/authz'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import HarnessMark from './HarnessMark.vue'

const props = defineProps<{ revision?: number }>()
const session = useSession()
const allowed = computed(() => session.identity?.principal.kind === 'person' && can('profile.write') && !session.requiresSignIn)
const scope = computed(() => `${session.identity?.tenant.id ?? ''}:${session.identity?.principal.id ?? ''}:${allowed.value}`)
const rows = ref<AccountLinkReview[]>([])
const error = ref('')
const loading = ref(false)
const busy = ref('')
let generation = 0
let controller: AbortController | undefined
function current(row: AccountLinkReview) { return row.person_id === session.identity?.principal.id && row.tenant_id === session.identity?.tenant.id && row.state === 'linked' }
async function load() {
  const turn = ++generation
  controller?.abort(); controller = new AbortController()
  rows.value = []; error.value = ''; busy.value = ''
  if (!allowed.value) return
  const owner = scope.value
  loading.value = true
  try {
    const result = await listAccountLinks(controller.signal)
    if (turn === generation && owner === scope.value) rows.value = result.filter(current)
  } catch (cause) { if (turn === generation && owner === scope.value) error.value = cause instanceof Error ? cause.message : 'Linked accounts are unavailable.' }
  finally { if (turn === generation) loading.value = false }
}
watch([scope, () => props.revision], () => void load(), { immediate: true })
async function unlink(row: AccountLinkReview) {
  if (!allowed.value || !current(row) || busy.value || loading.value) return
  const turn = generation
  const owner = scope.value
  busy.value = row.account_id; error.value = ''
  try {
    await unlinkAccount(row, controller?.signal)
    if (turn === generation && owner === scope.value) rows.value = rows.value.filter(item => item.account_id !== row.account_id)
  } catch (cause) { if (turn === generation && owner === scope.value) error.value = cause instanceof Error ? cause.message : 'The account could not be unlinked.' }
  finally { if (turn === generation) busy.value = '' }
}
onBeforeUnmount(() => { generation++; controller?.abort() })
</script>

<template>
  <section v-if="allowed" id="linked-accounts" class="linked-accounts" aria-label="Your linked accounts">
    <h2>Your linked accounts</h2>
    <p v-if="loading" class="hint" role="status">Loading…</p>
    <p v-else-if="!rows.length && !error" class="hint">No accounts linked to you yet. <RouterLink to="/link">Enter an account code</RouterLink></p>
    <ul v-else-if="rows.length">
      <li v-for="row in rows" :key="row.account_id">
        <HarnessMark :harness="row.harness" :size="18" />
        <div class="account"><span>{{ harnessLabel(row.harness) }} · {{ row.account_label }}</span><small>{{ row.computer_name }}</small></div>
        <button type="button" class="btn sm" :disabled="!!busy" :aria-label="`Unlink ${harnessLabel(row.harness)} (${row.account_label})`" @click="unlink(row)"><AppIcon name="link" :size="14" />{{ busy === row.account_id ? 'Unlinking…' : 'Unlink' }}</button>
      </li>
    </ul>
    <p v-if="error" class="error" role="alert">{{ error }} <button v-if="!busy" class="btn sm" type="button" @click="load">Refresh accounts</button></p>
  </section>
</template>

<style scoped>
.linked-accounts { margin-top: 30px; scroll-margin-top: 24px; }
h2 { margin: 0 0 12px; font-size: 15px; font-weight: 650; }
.hint { margin: 0; color: var(--ink-2); font-size: 13px; line-height: 1.5; }
ul { margin: 0; padding: 0; list-style: none; display: grid; gap: 8px; }
li { display: flex; align-items: center; gap: 10px; padding: 12px; border: 1px solid var(--line); border-radius: var(--radius, 9px); background: var(--surface); }
.account { display: grid; gap: 3px; flex: 1; min-width: 0; font-size: 13px; overflow-wrap: anywhere; }
small { color: var(--ink-3); font-size: 12px; }
.btn { flex-shrink: 0; }
.error { color: var(--danger); font-size: 13px; line-height: 1.5; }
@media (max-width: 420px) { li { flex-wrap: wrap; } .account { flex-basis: calc(100% - 30px); } li .btn { margin-left: 28px; } }
</style>
