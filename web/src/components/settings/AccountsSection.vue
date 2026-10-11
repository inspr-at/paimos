<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Settings › Accounts (AEON-1171, design AEON-1149): which logins agents use. The
// account list with Add an account and the low-quota warnings, then where accounts
// may work. Needs you lists only this part's items and is absent when nothing needs
// you. Without a paired computer the head offers Connect a computer instead of
// Add an account. Data, the docked panel and deep links are shared with Computers
// and Capacity.
import { toRefs } from 'vue'
import { useAccountsSettings } from '../../lib/useAccountsSettings'
import AppIcon from '../AppIcon.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import AccountUseMatrix from './AccountUseMatrix.vue'
import AccountsPanel from './AccountsPanel.vue'
import AddAccountPanel from './AddAccountPanel.vue'
import NeedsYouList from './NeedsYouList.vue'
import QuotaWarningCard from './QuotaWarningCard.vue'

const ctx = useAccountsSettings('accounts')
const { capacity, accounts, machines, manage, add, open, account, thresholds, needs, allReady, show, actAttention, accountSummary, accountTone } = toRefs(ctx)
</script>

<template>
  <AccountsPanel :ctx="ctx">
    <template #head><header class="page-head"><h2>Which logins agents use</h2><p>Quota belongs to the account: every computer signed in with it draws from the same quota.</p></header></template>
    <NeedsYouList :items="needs.accounts" hide-when-calm :aside="allReady ? 'Everything else is working.' : 'Based on the last available reports.'" @action="actAttention" />
    <section class="zone" aria-labelledby="accounts-list-title"><div class="zone-head"><h3 id="accounts-list-title">Accounts <span class="count">{{ accounts.length }}</span></h3><p>Quota belongs to the account and is shared by its computers</p><button v-if="manage && machines.length" type="button" class="btn sm" :aria-expanded="add" @click="add = !add"><AppIcon name="plus" />Add an account</button><RouterLink v-else-if="manage && capacity.computersLoaded && capacity.computersState !== 'error'" class="btn sm" to="/agents/register-agent"><AppIcon name="monitor" />Connect a computer</RouterLink></div><AddAccountPanel v-if="add && machines.length" :machines="machines" /><ul class="list"><li v-for="a in accounts" :key="a.id"><button class="list-row" type="button" :data-account="a.records[0]?.id" :data-accounts="a.records.map(r => r.id).join(' ')" :aria-expanded="open && account?.id === a.id" :aria-current="open && account?.id === a.id" @click="show('account', a.id, $event)"><HarnessMark :harness="a.harness" /><span class="lr-who"><b>{{ a.vendor }}</b><small>{{ a.identity }}</small></span><span class="st"><span class="dot" :class="accountTone(a)"></span>{{ accountSummary(a) }}</span><AppIcon name="chevron-right" /></button></li></ul><p v-if="!accounts.length && capacity.loaded" class="empty">No vendor accounts are connected yet.</p><QuotaWarningCard @changed="thresholds = $event" /></section>
    <AccountUseMatrix />
  </AccountsPanel>
</template>

<style scoped src="../../styles/accountsSettings.css"></style>
