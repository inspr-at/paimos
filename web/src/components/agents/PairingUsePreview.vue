<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1054: on the pairing review, where the accounts about to connect may
// work, pre-filled by the workspace rule exactly as the server stamps them
// (trigger T3: "allow automatically" ticks every context except those set to
// "never"; "ask first" ticks none). Changes are made in the matrix afterwards.
// AEON-1502: one compact Details row (label and value) in the review's list.
import { computed, ref, watch } from 'vue'
import { can } from '../../lib/authz'
import { brand } from '../../lib/brand'
import { pairingCopy } from '../../lib/attachCopy'
import type { DisplayLanguage } from '../../lib/displayLanguage'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { useSession } from '../../stores/session'
import { PERMISSION, predictedForNewAccount, readMatrix, tickable, type Matrix } from '../../lib/accountUse'

const props = withDefaults(defineProps<{ language?: DisplayLanguage }>(), { language: 'en' })
const words = computed(() => pairingCopy(props.language, brand.value.short_name))
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
// At most three names; the rest is a count, and the first page of contexts is all that was read.
// Nowhere is said only when "ask first" or a complete read proves it; unread contexts may allow them.
const value = computed(() => {
  if (!allowed.value) return words.value.whereRule
  if (failed.value) return words.value.whereFailed
  const m = matrix.value
  if (!m) return words.value.whereReading
  const yes = rows.value.filter(r => r.allowed).map(r => r.name)
  if (!yes.length) return m.rules.new_accounts === 'allow' && m.next_context ? words.value.whereNoneRead(m.contexts.length) : words.value.whereNowhere
  return words.value.whereSome(yes.slice(0, 3), Math.max(0, yes.length - 3), !!m.next_context)
})
</script>

<template>
  <div class="row" data-pairing-use>
    <dt>{{ words.where }}</dt>
    <dd :role="allowed && !failed && !matrix ? 'status' : undefined" :data-pairing-use-allowed="rows.filter(r => r.allowed).map(r => r.id).join(' ') || undefined">{{ value }}</dd>
  </div>
</template>
