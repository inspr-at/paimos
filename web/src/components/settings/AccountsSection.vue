<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Settings › Accounts (AEON-1171, design AEON-1149): which logins agents use. The
// account list with Add an account and the low-quota warnings, then where accounts
// may work. Each row carries a small quota bar and one caption, with the legend
// once above the list (AEON-1172). Needs you lists only this part's items and is absent when nothing needs
// you. Without a paired computer the head offers Connect a computer instead of
// Add an account. Data, the docked panel and deep links are shared with Computers
// and Capacity.
import { computed, onBeforeUnmount, onMounted, ref, toRefs } from 'vue'
import { useAccountsSettings } from '../../lib/useAccountsSettings'
import AppIcon from '../AppIcon.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import AccountUseMatrix from './AccountUseMatrix.vue'
import AccountsPanel from './AccountsPanel.vue'
import AddAccountPanel from './AddAccountPanel.vue'
import NeedsYouList from './NeedsYouList.vue'
import QuotaBar from './QuotaBar.vue'
import QuotaLegend from './QuotaLegend.vue'
import QuotaWarningCard from './QuotaWarningCard.vue'

const ctx = useAccountsSettings('accounts')
const { capacity, accounts, machines, manage, add, open, account, thresholds, needs, allReady, show, actAttention, quota } = toRefs(ctx)
// The legend names Keep for you whenever the person keeps something (the same in the panel).
const keeps = computed(() => capacity.value.schedule.reserve !== 'off')
// "Choose where" for a new account opens its row in the matrix (AEON-1173).
const where = ref<InstanceType<typeof AccountUseMatrix> | null>(null)
onMounted(() => { ctx.bindWhere(id => where.value?.choose(id)) })
onBeforeUnmount(() => { ctx.bindWhere(null) })
</script>

<template>
  <AccountsPanel :ctx="ctx">
    <template #head><header class="page-head"><h2>Which logins agents use</h2><p>Quota belongs to the account: every computer signed in with it draws from the same quota.</p></header></template>
    <NeedsYouList :items="needs.accounts" hide-when-calm :aside="allReady ? 'Everything else is working.' : 'Based on the last available reports.'" @action="actAttention" />
    <section class="zone acc-zone" aria-labelledby="accounts-list-title"><div class="zone-head"><h3 id="accounts-list-title">Accounts <span class="count">{{ accounts.length }}</span></h3><p>Quota belongs to the account and is shared by its computers</p><button v-if="manage && machines.length" type="button" class="btn sm" :aria-expanded="add" @click="add = !add"><AppIcon name="plus" />Add an account</button><RouterLink v-else-if="manage && capacity.computersLoaded && capacity.computersState !== 'error'" class="btn sm" to="/agents/register-agent"><AppIcon name="monitor" />Connect a computer</RouterLink></div><AddAccountPanel v-if="add && machines.length" :machines="machines" /><div v-if="accounts.length" class="acc-lh" aria-hidden="true"><span class="eh">Account</span><QuotaLegend short :keep="keeps" /></div><ul class="list acc-list"><li v-for="a in accounts" :key="a.id"><button class="list-row acc" type="button" :data-account="a.records[0]?.id" :data-accounts="a.records.map(r => r.id).join(' ')" :aria-expanded="open && account?.id === a.id" :aria-current="open && account?.id === a.id" @click="show('account', a.id, $event)"><HarnessMark :harness="a.harness" /><span class="lr-who"><b>{{ a.vendor }}</b><small>{{ a.identity }}</small></span><span class="ar-q"><QuotaBar :bar="quota(a).bar" :label="quota(a).label" /><small class="cap" :class="quota(a).caption.tone" data-quota-caption><AppIcon v-if="quota(a).caption.icon" :name="quota(a).caption.icon || 'alert'" :size="12" />{{ quota(a).caption.text }}</small></span><AppIcon name="chevron-right" /></button></li></ul><p v-if="!accounts.length && capacity.loaded" class="empty">No vendor accounts are connected yet.</p><QuotaWarningCard @changed="thresholds = $event" /></section>
    <AccountUseMatrix ref="where" />
  </AccountsPanel>
</template>

<style scoped src="../../styles/accountsSettings.css"></style>
