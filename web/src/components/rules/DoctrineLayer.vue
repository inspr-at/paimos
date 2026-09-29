<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import BizIcon from '../business/BizIcon.vue'
import DoctrineFileCard from './DoctrineFileCard.vue'
import DoctrineSourceDialog from './DoctrineSourceDialog.vue'
import { can } from '../../lib/authz'
import { toast } from '../../lib/toast'
import { useSession } from '../../stores/session'
import {
  DoctrineError, doctrineMessage, getDoctrine, indexDoctrineSource, pinLine, repoName, stateLine,
  type DoctrineLayer, type DoctrineSource,
} from '../../lib/doctrine'

// The INSPR doctrine, read-only, as git holds it at the pinned release. Agents
// get it through their harness files, not from Aeon, so it has no switches
// here; changing it is a change in git. People who manage the workspace pick
// the repository and the pin.
const session = useSession()
const layer = ref<DoctrineLayer | null>(null)
const loadError = ref('')
const editing = ref<{ source?: DoctrineSource } | null>(null)
const reading = ref('')

const canManage = computed(() => session.identity?.principal.kind !== 'agent' && can('settings.manage'))
const sources = computed(() => layer.value?.sources ?? [])
const visible = computed(() => !!layer.value && (sources.value.length > 0 || canManage.value))

async function load() {
  loadError.value = ''
  try {
    layer.value = await getDoctrine()
  } catch (cause) {
    // A server without the doctrine layer answers 404: there is nothing to show.
    if (cause instanceof DoctrineError && cause.status === 404) return
    loadError.value = doctrineMessage(cause)
  }
}

async function reread(source: DoctrineSource) {
  if (reading.value) return
  reading.value = source.id
  try {
    layer.value = await indexDoctrineSource(source.id)
    const now = layer.value.sources.find(item => item.id === source.id)
    toast(now?.error ? 'The repository could not be read' : 'Read again', now?.error ? { tone: 'error' } : undefined)
  } catch (cause) {
    toast(doctrineMessage(cause), { tone: 'error' })
  } finally { reading.value = '' }
}

function saved(next: DoctrineLayer, message: string) {
  layer.value = next
  editing.value = null
  toast(message)
}

const skippedTip = (source: DoctrineSource) => source.skipped.map(item => `${item.path}: ${item.reason}`).join('\n')

onMounted(load)
</script>

<template>
  <section v-if="visible || loadError" class="doctrine" aria-labelledby="doctrine-title">
    <header class="head">
      <span class="mark" aria-hidden="true"><BizIcon name="book" :size="15" /></span>
      <div class="titles">
        <h3 id="doctrine-title">Doctrine</h3>
        <p>Read-only, as git holds it. Agents get it through their harness files.</p>
      </div>
      <button v-if="canManage && sources.length" type="button" class="btn sm ghost" @click="editing = {}"><BizIcon name="plus" :size="14" />Link repository</button>
    </header>

    <p v-if="loadError" class="quiet" role="status">Doctrine unavailable: {{ loadError }} <button type="button" class="link" @click="load">Try again</button></p>

    <div v-else-if="!sources.length" class="empty">
      <p class="quiet">No doctrine repository linked.</p>
      <button type="button" class="btn sm" @click="editing = {}"><BizIcon name="link" :size="14" />Link repository</button>
    </div>

    <div v-for="source in sources" :key="source.id" class="source">
      <div class="source-head">
        <div class="pin">
          <span class="repo">
            <BizIcon v-if="source.visibility === 'private'" name="lock" :size="13" class="private" data-tip="Private repository, read with a scoped read-only credential" />
            <a :href="source.url" target="_blank" rel="noopener noreferrer" :title="`${source.repository} at ${source.commit}`">{{ repoName(source.repository) }}</a>
          </span>
          <span class="pin-line" :title="source.commit">{{ pinLine(source) }}</span>
        </div>
        <span v-if="source.skipped.length" class="skipped" :data-tip="skippedTip(source)">{{ source.skipped.length }} not indexed</span>
        <button v-if="canManage" type="button" class="icon-btn sm flat" :aria-label="`Pin ${repoName(source.repository)}`" data-tip="Change pin" @click="editing = { source }"><BizIcon name="edit" :size="15" /></button>
      </div>
      <p v-if="stateLine(source)" class="quiet state" :role="source.state === 'failed' ? 'status' : undefined">
        {{ stateLine(source) }}
        <button v-if="canManage && source.state !== 'ready'" type="button" class="link" :disabled="!!reading" @click="reread(source)">{{ reading === source.id ? 'Reading…' : 'Read now' }}</button>
      </p>
      <p v-else-if="source.error" class="quiet state" role="status">
        Last read failed: {{ source.error }}
        <button v-if="canManage" type="button" class="link" :disabled="!!reading" @click="reread(source)">{{ reading === source.id ? 'Reading…' : 'Try again' }}</button>
      </p>
      <div v-if="source.files.length" class="files">
        <DoctrineFileCard v-for="file in source.files" :key="file.path" :file="file" />
      </div>
    </div>

    <DoctrineSourceDialog v-if="editing" :source="editing.source" @close="editing = null" @saved="saved" />
  </section>
</template>

<style scoped>
.doctrine { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
.head { display: flex; align-items: flex-start; gap: 12px; }
.mark { flex: none; display: grid; place-items: center; width: 24px; height: 24px; margin-top: 1px; border-radius: 50%; background: var(--surface-2); color: var(--ink-3); }
.titles { flex: 1; min-width: 0; }
.titles h3 { margin: 0; font-size: 15px; font-weight: 650; }
.titles p { margin: 2px 0 0; color: var(--ink-3); font-size: 13px; }
.quiet { margin: 0; color: var(--ink-3); font-size: 13px; line-height: 1.45; overflow-wrap: anywhere; }
.link { padding: 0; border: 0; background: none; color: var(--teal-ink); font: inherit; font-weight: 600; cursor: pointer; }
.link:disabled { opacity: .6; cursor: default; }
.link:focus-visible { box-shadow: var(--focus-ring); outline: none; border-radius: 4px; }
.empty { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 14px; margin-left: 36px; padding: 12px 14px; border-radius: 14px; background: var(--surface); box-shadow: 0 0 0 1px var(--line); }
.source { display: flex; flex-direction: column; gap: 6px; margin-left: 36px; min-width: 0; }
.source-head { display: flex; align-items: center; gap: 10px; min-width: 0; padding: 0 2px; }
.pin { flex: 1; display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 10px; min-width: 0; }
.repo { display: inline-flex; align-items: center; gap: 5px; min-width: 0; font-size: 14px; font-weight: 650; }
.repo a { color: inherit; text-decoration: none; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
@media (hover: hover) { .repo a:hover { color: var(--teal-ink); text-decoration: underline; text-underline-offset: 2px; } }
.repo a:focus-visible { box-shadow: var(--focus-ring); outline: none; border-radius: 4px; }
.private { flex: none; color: var(--ink-3); }
.pin-line { min-width: 0; color: var(--ink-3); font-size: 12.5px; font-variant-numeric: tabular-nums; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.skipped { flex: none; color: var(--ink-3); font-size: 12px; white-space: nowrap; }
.source-head .icon-btn { flex: none; color: var(--ink-3); }
.state { padding: 0 2px; }
.files { display: flex; flex-direction: column; border-radius: 14px; background: var(--surface); box-shadow: 0 0 0 1px var(--line); min-width: 0; }
.files > * + * { border-top: 1px solid var(--line); }
@media (max-width: 600px) {
  .mark { display: none; }
  .empty, .source { margin-left: 0; }
  .skipped { display: none; }
  .pin-line { white-space: normal; }
}
</style>
