<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Settings › Capacity (AEON-1171, design AEON-1149): how much agents may use, and
// when. The one-time plan card is this part's only Needs-you item; below it the
// work week, nights and Keep for you. #when (and the older #capacity-and-load)
// focuses the pacing editors. Shared with Accounts and Computers.
import { nextTick, onBeforeUnmount, onMounted, ref, toRefs, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAccountsSettings } from '../../lib/useAccountsSettings'
import AccountsPanel from './AccountsPanel.vue'
import PacingSettings from './PacingSettings.vue'

const ctx = useAccountsSettings('capacity')
const { manage, owner, planCard, planBusy, confirmPlan, cardSecondary } = toRefs(ctx)
const route = useRoute(), router = useRouter()
const pacing = ref<InstanceType<typeof PacingSettings>>()
onMounted(() => { ctx.bindPacing(() => pacing.value?.reveal()) })
onBeforeUnmount(() => { ctx.bindPacing(null) })
// The Agents page's pacing line and the old Accounts and computers link land here (AEON-721).
watch(() => route.hash, async hash => {
  if (hash !== '#when' && hash !== '#capacity-and-load') return
  await nextTick(); pacing.value?.reveal(); void router.replace({ path: route.path, query: route.query, hash: '' })
}, { immediate: true })
</script>

<template>
  <AccountsPanel :ctx="ctx">
    <template #head>
      <header class="page-head"><h2>How much agents may use, and when</h2><p>Your plan for every account you can use. Agents pace each account so its week lasts; the Agents dial shows it live.</p></header>
      <div v-if="planCard" class="plan-card" role="region" aria-label="The plan">
        <p><b>{{ planCard.title }}</b> {{ planCard.text }}</p>
        <span class="pc-actions">
          <button class="btn" type="button" :disabled="planBusy" @click="cardSecondary">{{ planCard.secondary }}</button>
          <button class="btn primary" type="button" :disabled="planBusy" @click="confirmPlan('auto')">{{ planCard.primary }}</button>
        </span>
      </div>
    </template>
    <PacingSettings ref="pacing" :manage="manage" :owner-key="owner" />
  </AccountsPanel>
</template>

<style scoped src="../../styles/accountsSettings.css"></style>
