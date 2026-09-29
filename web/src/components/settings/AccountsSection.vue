<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onMounted } from 'vue'
import { accountName } from '../../lib/accountCascade'
import type { AgentAccount } from '../../lib/agents'
import { confirmAction } from '../../lib/confirm'
import { useAgents } from '../../stores/agents'
import AccountsCard from '../agents/AccountsCard.vue'
import SettingsCard from './SettingsCard.vue'

// Settings → Accounts: the place behind "Manage accounts" on the Agents desk.
// Pausing an account and limits set by hand live here; sign-ins happen on the
// computer itself, and credentials never reach Aeon.
const agents = useAgents()
onMounted(() => { void agents.refreshAccounts() })
async function setAccount(account: AgentAccount, state: AgentAccount['state']) {
  if (state === 'draining') {
    const ok = await confirmAction({ title: `Drain ${accountName(account)}?`, body: 'Running work finishes; no new runs start on this account until you resume it.', confirmLabel: 'Drain account' })
    if (!ok) return
  }
  await agents.setAccount(account, state)
}
async function refresh() {
  // A refresh already in flight may have started before this window existed.
  await agents.refreshAccounts()
  await agents.refreshAccounts()
}
</script>

<template>
  <SettingsCard title="Accounts" icon="gauge" anchor="agent-accounts">
    <template #lead>The vendor accounts agents run on. Sign in on the computer itself; the password never leaves it.</template>
    <AccountsCard
      :accounts="agents.accounts" :state="agents.accountsUpdatedAt !== null ? 'ready' : agents.accountsState" :now="agents.now" :admin="agents.accountsState === 'ready'"
      :set="setAccount" @allowance-created="refresh()"
    />
  </SettingsCard>
</template>
