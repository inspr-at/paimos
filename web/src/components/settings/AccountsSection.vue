<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { machinesForAdd, type AddMachine } from '../../lib/addAccount'
import { listPairingComputers, type PairingView } from '../../lib/agentPairing'
import { accountName } from '../../lib/accountCascade'
import type { AgentAccount } from '../../lib/agents'
import { can } from '../../lib/authz'
import { brand } from '../../lib/brand'
import { confirmAction } from '../../lib/confirm'
import { usePoller } from '../../lib/usePolledData'
import { useAgents } from '../../stores/agents'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import AccountsCard from '../agents/AccountsCard.vue'
import AddAccountPanel from './AddAccountPanel.vue'
import SettingsCard from './SettingsCard.vue'

// Settings → Accounts: the place behind "Manage accounts" on the Agents desk.
// Pausing an account and limits set by hand live here; sign-ins happen on the
// computer itself, and credentials never reach Aeon.
const agents = useAgents()
const session = useSession()
const route = useRoute()
const mayManage = computed(() => session.identity?.principal.kind === 'person' && can('account.manage'))
const open = ref(false)
const views = ref<PairingView[]>([])
const computersState = ref<'idle' | 'ready' | 'error'>('idle')
let computersTurn = 0

const machines = computed<AddMachine[]>(() => machinesForAdd(views.value))
const showHint = computed(() => !mayManage.value && agents.accountsState === 'ready')

async function loadComputers() {
  const turn = ++computersTurn
  try {
    const rows = await listPairingComputers()
    if (turn !== computersTurn) return
    views.value = rows
    computersState.value = 'ready'
  } catch {
    if (turn !== computersTurn) return
    if (computersState.value !== 'ready') computersState.value = 'error'
  }
}
async function refresh() {
  // A refresh already in flight may have started before this window existed.
  await agents.refreshAccounts()
  const jobs: Promise<unknown>[] = [agents.refreshAccounts()]
  if (mayManage.value) jobs.push(loadComputers())
  await Promise.all(jobs)
}
function onFocus() { void refresh() }
const poller = usePoller(() => refresh(), 20_000)

onMounted(() => {
  poller.start(true)
  window.addEventListener('focus', onFocus)
})
onBeforeUnmount(() => {
  computersTurn++
  poller.stop()
  window.removeEventListener('focus', onFocus)
})

watch(mayManage, allowed => {
  if (!allowed) { open.value = false; return }
  void loadComputers()
}, { immediate: true })
watch([() => route.hash, mayManage, machines], () => {
  if (route.hash === '#add-account' && mayManage.value && machines.value.length) open.value = true
}, { immediate: true })

async function setAccount(account: AgentAccount, state: AgentAccount['state']) {
  if (state === 'draining') {
    const ok = await confirmAction({ title: `Drain ${accountName(account)}?`, body: 'Running work finishes; no new runs start on this account until you resume it.', confirmLabel: 'Drain account' })
    if (!ok) return
  }
  await agents.setAccount(account, state)
}
async function refreshAfterAllowance() {
  await agents.refreshAccounts()
  await agents.refreshAccounts()
}
</script>

<template>
  <div id="add-account" class="add-account">
    <SettingsCard title="Accounts" icon="gauge" anchor="agent-accounts">
      <template #lead>The vendor accounts agents run on. Sign in on the computer itself; the password never leaves it.</template>
      <template v-if="mayManage && computersState === 'ready'" #aside>
        <RouterLink v-if="!machines.length" class="btn sm" to="/agents/register-agent"><AppIcon name="monitor" :size="14" />Connect your machine</RouterLink>
        <button v-else type="button" class="btn sm" :aria-expanded="open" aria-controls="add-account-panel" @click="open = !open"><AppIcon name="plus" :size="14" />Add an account</button>
      </template>
      <p v-if="mayManage && computersState === 'error'" class="add-hint" role="alert">Paired machines could not be loaded. <button type="button" class="btn sm" @click="loadComputers">Try again</button></p>
      <p v-else-if="showHint" class="add-hint">Sign-in happens on the machine; the password never reaches {{ brand.short_name }}.</p>
      <AddAccountPanel v-if="open && machines.length" :machines="machines" />
      <AccountsCard
        :accounts="agents.accounts" :state="agents.accountsUpdatedAt !== null ? 'ready' : agents.accountsState" :now="agents.now" :admin="agents.accountsState === 'ready'"
        :set="setAccount" @allowance-created="refreshAfterAllowance()"
      />
    </SettingsCard>
  </div>
</template>

<style scoped>
.add-account { scroll-margin-top: 20px; }
.add-hint { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin: 0 0 12px; color: var(--ink-2); font-size: 13px; line-height: 1.45; }
</style>
