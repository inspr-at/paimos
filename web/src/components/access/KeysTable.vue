<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { keyExpiry, keyHint, scopeLabel } from '../../lib/access'
import { keyState, type AgentKey } from '../../lib/settings'
import { absoluteTime, relativeTime } from '../../lib/work'
import AppIcon from '../AppIcon.vue'

// Agent keys as the workspace settings showed them: name, prefix, scopes, last use
// and state. With `revocable`, an active key has a Revoke action (the caller
// confirms). Only a key's prefix is ever shown again.
defineProps<{ keys: AgentKey[]; revocable?: boolean; rotatable?: boolean; editable?: boolean; showName?: boolean }>()
const emit = defineEmits<{ revoke: [key: AgentKey]; rotate: [key: AgentKey]; edit: [key: AgentKey] }>()
const STATE: Record<string, string> = { active: 'Active', expired: 'Expired', revoked: 'Revoked' }
const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | undefined
onMounted(() => { timer = setInterval(() => { now.value = Date.now() }, 60_000) })
onUnmounted(() => { clearInterval(timer) })
</script>

<template>
  <div class="table-wrap">
    <table class="keys-table">
      <thead><tr><th v-if="showName" scope="col">Name</th><th scope="col">Key</th><th scope="col">Scopes</th><th scope="col">Last used</th><th scope="col">Expires</th><th scope="col">Status</th><th v-if="revocable || rotatable || editable" scope="col"><span class="sr-only">Actions</span></th></tr></thead>
      <tbody>
        <tr v-for="key in keys" :key="key.id" :class="keyState(key, now)">
          <th v-if="showName" scope="row">{{ key.name }}<span class="sub">Created {{ relativeTime(key.created_at, { long: true }) }}</span></th>
          <td class="mono">{{ keyHint(key.prefix) }}<span v-if="!showName" class="sub">Created {{ relativeTime(key.created_at, { long: true }) }}</span></td>
          <td data-label="Scopes"><span v-if="!key.scopes.length" class="muted" data-tip="A key without scopes can do nothing">None</span><span v-for="scope in key.scopes.slice(0, 4)" :key="scope" class="scope mono" :data-tip="scopeLabel(scope)">{{ scope }}</span><span v-if="key.scopes.length > 4" class="more" :data-tip="key.scopes.slice(4).join('\n')">and {{ key.scopes.length - 4 }} more</span></td>
          <td data-label="Last used"><time v-if="key.last_used_at" :datetime="key.last_used_at" :data-tip="absoluteTime(key.last_used_at)">{{ relativeTime(key.last_used_at, { long: true }) }}</time><span v-else class="muted">Never</span></td>
          <td data-label="Expires" class="expiry">
            <time v-if="key.expires_at" :datetime="key.expires_at" :data-tip="absoluteTime(key.expires_at)" :class="{ soon: keyExpiry(key, now).soon }">{{ keyExpiry(key, now).label }}</time>
            <span v-else class="muted">Never</span>
            <span v-if="keyExpiry(key, now).soon" class="sub">Rotate before expiry</span>
          </td>
          <td data-label="Status"><span class="state" :class="keyState(key, now)">{{ STATE[keyState(key, now)] }}</span></td>
          <td v-if="revocable || rotatable || editable" class="act">
            <div v-if="!key.revoked_at" class="actions">
              <button v-if="editable && keyState(key, now) === 'active'" type="button" class="btn sm ghost" :aria-label="`Edit scopes for key ${keyHint(key.prefix)}`" @click="emit('edit', key)"><AppIcon name="edit" :size="12" />Edit scopes</button>
              <button v-if="rotatable" type="button" class="btn sm ghost" :aria-label="`Rotate key ${keyHint(key.prefix)}`" @click="emit('rotate', key)"><AppIcon name="refresh" :size="12" />Rotate</button>
              <button v-if="revocable" type="button" class="btn sm ghost danger-text" :aria-label="`Revoke key ${keyHint(key.prefix)}`" @click="emit('revoke', key)"><AppIcon name="close" :size="12" />Revoke</button>
            </div>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.table-wrap { overflow-x: auto; margin: 0 -4px; padding: 0 4px; }
.keys-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.keys-table th, .keys-table td { padding: 9px 10px 9px 0; text-align: left; vertical-align: baseline; border-top: 1px solid var(--line); }
.keys-table thead th { padding-top: 0; border-top: 0; font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.keys-table tbody th { font-weight: 600; color: var(--ink); }
.sub { display: block; margin-top: 2px; font: 400 11.5px/1.4 var(--font); color: var(--ink-3); }
.scope { display: inline-flex; align-items: center; height: 20px; margin: 0 4px 4px 0; padding: 0 7px; border-radius: 6px; background: var(--surface-2); color: var(--ink-2); font-size: 11px; }
.muted { color: var(--ink-3); }
.more { font-size: 11.5px; color: var(--ink-3); white-space: nowrap; }
.state { display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: 999px; background: var(--surface-2); color: var(--ink-2); font-size: 11.5px; font-weight: 600; }
.state.active { background: rgba(47, 122, 90, .12); color: color-mix(in oklab, var(--ok), var(--ink) 35%); }
tr.revoked, tr.expired { color: var(--ink-2); }
.keys-table .act { text-align: right; padding-right: 0; }
.actions { display: flex; align-items: center; justify-content: flex-end; flex-wrap: wrap; gap: 4px; }
.actions svg { display: block; flex-shrink: 0; }
.expiry { white-space: nowrap; }
.soon { display: inline-block; padding: 2px 6px; border-radius: 6px; background: color-mix(in oklab, var(--warn) 12%, var(--surface)); color: var(--ink); }
.danger-text { color: var(--danger); }
@media (max-width: 600px) {
  /* Phones: each key reads as a small card, not a table that scrolls sideways. */
  .keys-table thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
  .keys-table, .keys-table tbody, .keys-table tr, .keys-table th, .keys-table td { display: block; }
  .keys-table tr { padding: 8px 0; border-top: 1px solid var(--line); }
  .keys-table th, .keys-table td { padding: 2px 0; border: 0; }
  /* Without the header row, each value says what it is. */
  .keys-table td[data-label] { display: grid; grid-template-columns: 76px minmax(0, 1fr); align-items: baseline; gap: 8px; }
  .keys-table td[data-label]::before { content: attr(data-label); font: 500 10.5px/1.6 var(--mono); letter-spacing: .1em; text-transform: uppercase; color: var(--ink-3); }
  .keys-table td[data-label] > * { justify-self: start; }
  .keys-table td[data-label] > .sub { grid-column: 2; }
  .actions { justify-content: flex-start; }
  .act .btn { height: 44px; }
}
</style>
