<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useSession } from '../stores/session'
import { createScope, scopeOwner } from '../lib/identityScope'
import { briefingDue, loadBriefingPreference, type BriefingPreference } from '../lib/morningBriefing'
import AppIcon from './AppIcon.vue'
const session = useSession()
const pref = ref<BriefingPreference | null>(null), ready = ref(false), now = ref(new Date())
const person = computed(() => session.identity?.principal.kind === 'person')
const scope = createScope(() => person.value ? scopeOwner(session.identity) : '')
const due = computed(() => ready.value && person.value && briefingDue(pref.value, now.value))
function load() {
  ready.value = false; pref.value = null
  void scope.run(({ after, signal }) => after(loadBriefingPreference(signal), value => { pref.value = value; ready.value = true }), { failed: () => { ready.value = false } })
}
watch(() => scopeOwner(session.identity), () => { scope.reset(); load() }, { immediate: true, flush: 'sync' })
const timer = setInterval(() => { now.value = new Date() }, 60_000)
onBeforeUnmount(() => { clearInterval(timer); scope.dispose() })
</script>
<template>
  <RouterLink v-if="person" to="/briefing" class="briefing-reminder" :class="{ due }"><AppIcon name="sun" :size="16" /><span>{{ due ? 'Your morning briefing is ready' : 'Morning briefing' }}</span><span v-if="due" class="detail">What finished · what needs you · usage</span><AppIcon name="chevron-right" :size="14" /></RouterLink>
</template>
<style scoped>
.briefing-reminder { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin: 0 0 18px; padding: 12px 14px; border: 1px solid var(--line); border-radius: 10px; color: var(--ink-2); font-size: 13px; }
.due { background: var(--surface-2); color: var(--ink); font-weight: 600; } .detail { margin-left: auto; color: var(--ink-3); font-size: 12px; font-weight: 400; }
.briefing-reminder:focus-visible { outline: none; box-shadow: var(--focus-ring); }
</style>
