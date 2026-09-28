<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import BizIcon from '../business/BizIcon.vue'
import RuleItem from './RuleItem.vue'
import RulesDialog from './RulesDialog.vue'
import {
  PUBLISH_NOTE_MAX, calendarVersion, listVersions, restoreSet, rulesMessage, utf8Length,
  type RuleSet, type RuleSnapshot,
} from '../../lib/rules'

// Published versions of one set, newest first. Any earlier version can be
// published again as a new version; the old one is never rewritten.
const props = defineProps<{ set: RuleSet; publishReason: string | null; dirty: boolean }>()
const emit = defineEmits<{ close: []; restored: [snapshot: RuleSnapshot] }>()
const versions = ref<RuleSnapshot[] | null>(null)
const error = ref('')
const restoring = ref('')
const note = ref('')
const busy = ref(false)

const when = (snapshot: RuleSnapshot) => {
  const date = new Date(snapshot.published_at)
  return Number.isNaN(date.getTime()) ? snapshot.version : date.toLocaleString(undefined, { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' })
}
onMounted(async () => {
  try { versions.value = (await listVersions(props.set.id)).versions } catch (cause) { error.value = rulesMessage(cause) }
})
function start(version: string) { restoring.value = version; note.value = ''; error.value = '' }
async function restore(snapshot: RuleSnapshot) {
  if (props.publishReason) { error.value = props.publishReason; return }
  if (props.dirty) { error.value = 'Save or cancel the edit of this set before restoring.'; return }
  const text = note.value.trim()
  if (utf8Length(text) > PUBLISH_NOTE_MAX) { error.value = 'A note can be at most 500 bytes.'; return }
  busy.value = true
  error.value = ''
  try {
    const restored = await restoreSet(props.set.id, { expected_revision: props.set.revision, version: snapshot.version, new_version: calendarVersion(), note: text })
    emit('restored', restored)
  } catch (cause) { error.value = rulesMessage(cause) } finally { busy.value = false }
}
</script>

<template>
  <RulesDialog :title="`${set.name} · history`" lede="Every published version stays as it was. Restoring publishes an earlier version again as a new one." size="side" :busy="busy" @close="emit('close')">
    <p v-if="error" class="error" role="alert"><BizIcon name="alert" :size="14" /><span>{{ error }}</span></p>
    <div v-if="!versions && !error" class="skeleton-block" role="status" aria-label="Loading versions"><span class="skeleton"></span><span class="skeleton"></span></div>
    <ol v-if="versions" class="versions">
      <li v-for="snapshot in versions" :key="snapshot.version" class="version">
        <div class="head">
          <span class="when">{{ when(snapshot) }}</span>
          <span v-if="snapshot.version === set.published_version" class="live">Live</span>
          <span class="count">{{ snapshot.rules.length }} {{ snapshot.rules.length === 1 ? 'rule' : 'rules' }}</span>
        </div>
        <p v-if="snapshot.note" class="note">{{ snapshot.note }}</p>
        <details class="rules">
          <summary><BizIcon name="chevron-right" :size="12" class="chev" />Rules in this version</summary>
          <ul><RuleItem v-for="rule in snapshot.rules" :key="rule.identity" :rule="rule" /></ul>
        </details>
        <div v-if="snapshot.version !== set.published_version" class="restore">
          <button v-if="restoring !== snapshot.version" type="button" class="btn sm" :disabled="!!publishReason" :data-tip="publishReason ?? undefined" @click="start(snapshot.version)"><BizIcon name="rollback" :size="13" />Restore as new version</button>
          <template v-else>
            <label class="fld">Note for the new version <span class="opt">optional</span>
              <textarea v-model="note" class="field" rows="2" maxlength="500" data-autofocus></textarea>
            </label>
            <div class="buttons">
              <button type="button" class="btn sm ghost" :disabled="busy" @click="restoring = ''">Cancel</button>
              <button type="button" class="btn sm primary" :disabled="busy" @click="restore(snapshot)">{{ busy ? 'Restoring…' : 'Restore' }}</button>
            </div>
          </template>
        </div>
        <p class="version-id" :data-tip="snapshot.sha256">{{ snapshot.version }}</p>
      </li>
    </ol>
  </RulesDialog>
</template>

<style scoped>
.versions { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 10px; }
.version { display: flex; flex-direction: column; gap: 8px; padding: 12px 14px; border-radius: 12px; box-shadow: 0 0 0 1px var(--line); }
.head { display: flex; align-items: center; gap: 8px; }
.when { font-weight: 650; font-size: 14px; }
.live { padding: 1px 8px; border-radius: 999px; background: var(--chip-teal-bg); color: var(--teal-ink); font-size: 11.5px; font-weight: 600; }
.count { margin-left: auto; color: var(--ink-3); font-size: 12.5px; }
.note { margin: 0; color: var(--ink-2); font-size: 13px; white-space: pre-wrap; overflow-wrap: anywhere; }
.rules summary { display: inline-flex; align-items: center; gap: 4px; color: var(--ink-3); font-size: 12.5px; font-weight: 600; cursor: pointer; list-style: none; }
.rules summary::-webkit-details-marker { display: none; }
.rules[open] .chev { transform: rotate(90deg); }
.rules ul { list-style: none; margin: 6px 0 0; padding: 0; }
.restore { display: flex; flex-direction: column; gap: 8px; align-items: flex-start; }
.fld { display: grid; gap: 4px; width: 100%; color: var(--ink-2); font-size: 12px; font-weight: 650; }
.fld textarea { height: auto; padding: 8px 12px; font-weight: 450; }
.opt { color: var(--ink-3); font-weight: 450; }
.buttons { display: flex; gap: 8px; align-self: flex-end; }
.version-id { margin: 0; color: var(--ink-3); font: 11px var(--mono); }
.error { display: flex; gap: 6px; align-items: flex-start; margin: 0; padding: 10px 12px; border-radius: 10px; background: var(--danger-bg); color: var(--danger); font-size: 13px; }
.skeleton-block { display: grid; gap: 8px; }
</style>
