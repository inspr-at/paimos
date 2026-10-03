<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, watch } from 'vue'
import { useServiceTiers } from '../../stores/serviceTiers'
import { TIER_NAME } from '../../lib/serviceTier'
import AppIcon from '../AppIcon.vue'
const tiers = useServiceTiers()
let timer: ReturnType<typeof setTimeout> | undefined
function hold() { clearTimeout(timer) }
function arm() { hold(); timer = setTimeout(() => { tiers.undo = null }, 8000) }
watch(() => tiers.undo, value => { hold(); if (value) arm() })
onBeforeUnmount(() => { hold(); tiers.undo = null })
</script>
<template>
  <Teleport to="body"><div v-if="tiers.undo" class="tier-toast" role="status" @pointerenter="hold" @pointerleave="arm" @focusin="hold" @focusout="arm">
    <span><b>{{ tiers.undo.name }}</b> <AppIcon name="arrow" :size="12" /> {{ TIER_NAME[tiers.undo.to] }} · {{ tiers.undo.price }}</span>
    <button type="button" class="btn sm ghost" :disabled="tiers.busy[tiers.undo.session]" @click="tiers.undoChange">Undo</button>
  </div></Teleport>
</template>
<style scoped>
.tier-toast{position:fixed;z-index:75;left:50%;bottom:20px;transform:translateX(-50%);display:grid;grid-template-columns:minmax(0,1fr) 58px;align-items:center;gap:10px;width:min(360px,calc(100vw - 16px));height:44px;padding:0 6px 0 14px;border-radius:12px;background:var(--surface-raised);box-shadow:var(--shadow-pop);color:var(--ink);font-size:13px}.tier-toast>span{white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.tier-toast>span>svg{vertical-align:middle}.tier-toast .btn{width:58px;height:32px}
</style>
