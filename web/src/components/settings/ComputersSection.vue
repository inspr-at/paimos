<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Settings › Computers (AEON-1171, design AEON-1149): where agents run. Each cell of
// the sign-ins grid is one account on one computer; the list opens a computer's
// panel. Needs you lists the sign-ins with a problem and pending cleanup, and is
// absent when nothing needs you. Shared with Accounts and Capacity.
import { toRefs } from 'vue'
import { when } from '../../lib/capacity'
import { useAccountsSettings } from '../../lib/useAccountsSettings'
import { vClipTip } from '../../directives/clipTip'
import AppIcon from '../AppIcon.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import AccountsPanel from './AccountsPanel.vue'
import NeedsYouList from './NeedsYouList.vue'

const ctx = useAccountsSettings('computers')
const { agents, capacity, accounts, computers, manage, open, computer, retained, needs, allReady, show, actAttention, computerState, computerSummary, references, status, windowLabel } = toRefs(ctx)
</script>

<template>
  <AccountsPanel :ctx="ctx">
    <template #head><header class="page-head"><h2>Where agents run</h2><p>Each computer keeps its own sign-ins; a cell is one account signed in on one computer.</p></header></template>
    <NeedsYouList :items="needs.computers" hide-when-calm :show-calm="retained > 0" :aside="allReady ? 'Everything else is working.' : 'Based on the last available reports.'" @action="actAttention" />
      <section v-if="accounts.length && computers.length" class="zone grid-wrap" aria-labelledby="signin-grid-title">
        <div class="zone-head"><h3 id="signin-grid-title" class="eyebrow">Sign-ins at a glance</h3><p>Each cell is one account on one computer</p></div>
        <div class="matrix-scroll" tabindex="0" aria-label="Sign-ins table; scroll horizontally for more computers"><table class="matrix"><thead><tr><th scope="col" class="corner">Account · quota left</th><th v-for="c in computers" :key="c.computer_id!" scope="col"><button class="mx-head" type="button" @click="show('computer', c.computer_id!, $event)"><AppIcon name="monitor" /><b v-clip-tip>{{ c.computer_name }}</b><small>{{ c.computer_state === 'revoked' ? 'Removed' : c.host_capacity ? `${c.host_capacity.running} agents${c.host_capacity.policy.maximum_agents ? ` of ${c.host_capacity.policy.maximum_agents}` : ''}` : computerState(c) }}</small></button></th></tr></thead><tbody><tr v-for="a in accounts" :key="a.id"><th scope="row"><button class="mx-head" type="button" @click="show('account', a.id, $event)"><HarnessMark :harness="a.harness" /><b>{{ a.vendor }}</b><small>{{ a.windows[0] ? `${Math.round(a.windows[0].remaining_percent)}% · ${windowLabel(a.windows[0])}` : 'Quota not reported' }}</small></button></th><td v-for="c in computers" :key="c.computer_id!"><button v-for="s in references(a, c)" :key="s.enrollment.account_id" class="mx-cell" type="button" :aria-label="`${a.vendor} on ${c.computer_name}: ${status(c, s.enrollment.account_id)}`" @click="show('computer', c.computer_id!, $event, s.enrollment.account_id)"><span class="dot" :class="status(c, s.enrollment.account_id) === 'Ready' ? 'ok' : 'wait'"></span>{{ status(c, s.enrollment.account_id) }}</button><span v-if="!references(a, c).length" class="mx-none"><AppIcon name="minus" /><span class="sr-only">{{ a.vendor }} is not signed in on {{ c.computer_name }}</span></span></td></tr></tbody></table></div>
      </section>
    <section class="zone" aria-labelledby="computers-list-title"><div class="zone-head"><h3 id="computers-list-title">Computers <span class="count">{{ computers.length }}</span></h3><p>Sign-ins and capacity live with each computer</p><RouterLink v-if="manage" class="btn sm" to="/agents/register-agent"><AppIcon name="monitor" />Connect a computer</RouterLink></div><ul class="list"><li v-for="c in computers" :key="c.computer_id!"><button class="list-row" type="button" :data-computer="c.computer_id" :aria-expanded="open && computer?.computer_id === c.computer_id" :aria-current="open && computer?.computer_id === c.computer_id" @click="show('computer', c.computer_id!, $event)"><AppIcon name="monitor" /><span class="lr-who"><b v-clip-tip>{{ c.computer_name }}</b><small>{{ computerState(c) }}<template v-if="c.last_seen_at"> · last seen {{ when(c.last_seen_at, agents.now) }}</template></small></span><span class="st"><span class="dot" :class="c.computer_state === 'revoked' ? 'blocked' : c.computer_state === 'connected' && c.connectivity === 'online' && !c.host_capacity?.reason ? 'ok' : 'wait'"></span>{{ computerSummary(c) }}</span><AppIcon name="chevron-right" /></button></li></ul><p v-if="!computers.length && capacity.computersLoaded" class="empty">No paired computers yet.</p><p v-if="computers.length === 100" class="footnote">First 100 computers shown.</p></section>
    <p class="footnote"><AppIcon name="shield" /><span>Every sign-in stays on its computer. PAIMOS stores status and timestamps, never the vendor credential.</span></p>
  </AccountsPanel>
</template>

<style scoped src="../../styles/accountsSettings.css"></style>
