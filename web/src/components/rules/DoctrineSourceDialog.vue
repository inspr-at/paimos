<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import RulesDialog from './RulesDialog.vue'
import {
  addDoctrineSource, doctrineMessage, parsePaths, pathsText, pinDoctrineSource, pinInput, removeDoctrineSource, repoName,
  type DoctrineLayer, type DoctrineSource,
} from '../../lib/doctrine'

// Which repository is indexed at which pin. This edits Aeon's configuration
// only; the doctrine itself changes in git. A private repository names a
// read-only credential that lives on the server; Aeon never sees its value.
const props = defineProps<{ source?: DoctrineSource }>()
const emit = defineEmits<{ close: []; saved: [layer: DoctrineLayer, message: string] }>()
const id = useId()
const repository = ref(props.source?.repository ?? '')
const visibility = ref<'public' | 'private'>(props.source?.visibility ?? 'public')
const pin = ref(props.source?.ref || props.source?.commit || '')
const paths = ref(pathsText(props.source?.paths ?? ['docs/AGENTS-*.md']))
const credential = ref(props.source?.credential_ref ?? '')
const busy = ref(false)
const error = ref('')
const confirmRemove = ref(false)

const title = computed(() => props.source ? `Pin ${repoName(props.source.repository)}` : 'Link a doctrine repository')
const lede = computed(() => props.source ? `${props.source.repository}, read from git at this pin and never changed here.` : 'The doctrine is read from git at this pin and never changed here.')
const ready = computed(() => !!repository.value.trim() && !!pin.value.trim() && (visibility.value === 'public' || !!credential.value.trim()))

async function save() {
  if (!ready.value || busy.value) return
  busy.value = true
  error.value = ''
  const input = {
    repository: repository.value.trim(), visibility: visibility.value, ...pinInput(pin.value),
    paths: parsePaths(paths.value), credential_ref: credential.value.trim() || undefined,
  }
  try {
    const layer = props.source ? await pinDoctrineSource(props.source.id, input) : await addDoctrineSource(input)
    emit('saved', layer, props.source ? 'Pin saved' : 'Repository linked')
  } catch (cause) {
    error.value = doctrineMessage(cause)
  } finally { busy.value = false }
}

async function remove() {
  if (!props.source || busy.value) return
  if (!confirmRemove.value) { confirmRemove.value = true; return }
  busy.value = true
  error.value = ''
  try {
    emit('saved', await removeDoctrineSource(props.source.id), 'Repository unlinked')
  } catch (cause) {
    error.value = doctrineMessage(cause)
  } finally { busy.value = false }
}
</script>

<template>
  <RulesDialog :title="title" :lede="lede" :busy="busy" @close="emit('close')">
    <form :id="`${id}-form`" class="form" @submit.prevent="save">
      <label v-if="!source" class="row">
        <span class="label">Repository</span>
        <input v-model="repository" class="field mono" placeholder="inspr-at/inspr-modules" autocomplete="off" spellcheck="false" data-autofocus>
      </label>
      <div class="row">
        <span :id="`${id}-vis`" class="label">Visibility</span>
        <div class="seg" role="radiogroup" :aria-labelledby="`${id}-vis`">
          <button type="button" role="radio" :aria-checked="visibility === 'public'" @click="visibility = 'public'">Public</button>
          <button type="button" role="radio" :aria-checked="visibility === 'private'" @click="visibility = 'private'">Private</button>
        </div>
      </div>
      <label class="row">
        <span class="label">Release</span>
        <input v-model="pin" class="field mono" placeholder="Tag or full commit SHA" autocomplete="off" spellcheck="false" :data-autofocus="source ? '' : undefined">
      </label>
      <label v-if="visibility === 'private' || credential" class="row">
        <span class="label">Credential</span>
        <input v-model="credential" class="field mono" placeholder="doctrine-private-read" autocomplete="off" spellcheck="false" :aria-describedby="`${id}-cred`">
        <span :id="`${id}-cred`" class="hint">Name of a read-only token on the server. The token itself is never stored here.</span>
      </label>
      <label class="row">
        <span class="label">Files</span>
        <textarea v-model="paths" class="field mono paths" rows="2" spellcheck="false" :aria-describedby="`${id}-paths`"></textarea>
        <span :id="`${id}-paths`" class="hint">One path pattern per line.</span>
      </label>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
    </form>
    <template #footer>
      <button v-if="source" type="button" class="btn sm ghost remove" :disabled="busy" @click="remove">{{ confirmRemove ? 'Unlink for sure' : 'Unlink' }}</button>
      <button type="button" class="btn sm ghost" :disabled="busy" @click="emit('close')">Cancel</button>
      <button type="submit" :form="`${id}-form`" class="btn sm primary" :disabled="busy || !ready">{{ busy ? 'Reading git…' : source ? 'Save pin' : 'Link repository' }}</button>
    </template>
  </RulesDialog>
</template>

<style scoped>
.form { display: flex; flex-direction: column; gap: 14px; }
.row { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.label { color: var(--ink-2); font-size: 12.5px; font-weight: 600; }
.row .seg { align-self: flex-start; }
.mono { font-family: var(--mono); font-size: 13px; }
.paths { min-height: 60px; resize: vertical; padding-top: 8px; padding-bottom: 8px; line-height: 1.5; }
.hint { color: var(--ink-3); font-size: 12px; line-height: 1.4; }
.error { margin: 0; color: var(--danger); font-size: 12.5px; }
.remove { margin-right: auto; color: var(--danger); }
</style>
