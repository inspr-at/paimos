<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1054: on the pairing review, where the accounts about to connect may
// work, pre-filled by the workspace rule exactly as the server stamps them
// (trigger T3: "allow automatically" ticks every context except those set to
// "never"; "ask first" ticks none). Changes are made in the matrix afterwards.
import { computed, ref, watch } from 'vue'
import { can } from '../../lib/authz'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { useSession } from '../../stores/session'
import { PERMISSION, predictedForNewAccount, readMatrix, tickable, type Matrix } from '../../lib/accountUse'
import AppIcon from '../AppIcon.vue'

const session = useSession()
const allowed = computed(() => session.identity?.principal.kind === 'person' && can(PERMISSION))
const scope = useIdentityScope(() => allowed.value)
const reads = scope.lane()
const matrix = ref<Matrix | null>(null), failed = ref(false)
watch(() => scope.owner.value, owner => {
  matrix.value = null; failed.value = false
  if (owner) void reads.run(({ after, signal }) => after(readMatrix(signal), m => { matrix.value = m }), { failed: () => { failed.value = true } })
}, { immediate: true })

const rows = computed(() => {
  const m = matrix.value
  if (!m) return []
  const yes = new Set(predictedForNewAccount(m.rules, m.contexts).map(c => c.id))
  return tickable(m.contexts).map(c => ({ id: c.id, name: c.name, allowed: yes.has(c.id), never: c.new_accounts_override === 'deny' }))
})
const rule = computed(() => matrix.value?.rules.new_accounts === 'allow' ? 'allow automatically' : 'ask first')
</script>

<template>
  <section class="use-preview" aria-labelledby="pairing-use-title" data-pairing-use>
    <h3 id="pairing-use-title">Where these accounts may work</h3>
    <p v-if="!allowed" class="sub">The workspace rule decides. Owners and admins see and change it in Settings under Accounts and computers.</p>
    <p v-else-if="failed" class="sub">The workspace rule could not be read. It still applies; check the accounts in Settings under Accounts and computers after connecting.</p>
    <p v-else-if="!matrix" class="sub" role="status">Reading the workspace rule…</p>
    <template v-else>
      <p class="sub">New accounts: {{ rule }}. {{ rows.some(r => r.allowed) ? 'Each account starts with these ticks; change them in Settings under Accounts and computers.' : 'They start allowed nowhere until someone ticks them in Settings under Accounts and computers.' }}</p>
      <ul class="rows">
        <li v-for="r in rows" :key="r.id" :data-pairing-use-row="r.id"><AppIcon :name="r.allowed ? 'check' : 'minus'" :size="14" :class="r.allowed ? 'yes' : 'no'" /><span>{{ r.name }}</span><small>{{ r.allowed ? 'Allowed' : r.never ? 'Never for new accounts' : 'Not allowed' }}</small></li>
      </ul>
      <p v-if="matrix.next_context" class="sub">Only the first 200 contexts are shown.</p>
    </template>
  </section>
</template>

<style scoped>
.use-preview { margin-top: 18px; }
.use-preview h3 { font-size: 14px; font-weight: 600; }
.sub { margin-top: 4px; font-size: 12.5px; color: var(--ink-3); }
.rows { list-style: none; margin: 8px 0 0; padding: 0; border-top: 1px solid var(--line); }
.rows li { display: grid; grid-template-columns: 16px minmax(0, 1fr) auto; align-items: center; gap: 10px; min-height: 36px; border-bottom: 1px solid var(--line); font-size: 13px; }
.rows li span { overflow-wrap: anywhere; }
.rows small { font-size: 12px; color: var(--ink-3); }
.yes { color: var(--teal-ink); }
.no { color: var(--ink-3); }
</style>
