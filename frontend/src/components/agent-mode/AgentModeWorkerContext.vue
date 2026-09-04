<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-907 — Explicit, on-demand consumer of the authorized bounded worker
  fleet. No workspace path, account credential, quota, or vendor payload is
  requested or rendered.
-->
<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import {
  loadWorkerFleetTicket,
  type WorkerFleetContext,
  type WorkerFleetTicketContext,
} from '@/services/workerFleet'

const props = defineProps<{
  projectId: number
  ticketId: number
  loader?: (projectId: number, ticketId: number) => Promise<WorkerFleetTicketContext>
}>()

const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const workers = ref<WorkerFleetContext[]>([])
const truncated = ref(false)
const { t } = useI18n()
let requestGeneration = 0

watch([() => props.projectId, () => props.ticketId], () => {
  requestGeneration += 1
  workers.value = []
  truncated.value = false
  state.value = 'idle'
})

async function load() {
  if (state.value === 'loading') return
  const generation = ++requestGeneration
  const projectId = props.projectId
  const ticketId = props.ticketId
  state.value = 'loading'
  workers.value = []
  truncated.value = false
  try {
    const result = await (props.loader ?? loadWorkerFleetTicket)(projectId, ticketId)
    if (
      generation !== requestGeneration ||
      projectId !== props.projectId ||
      ticketId !== props.ticketId
    )
      return
    workers.value = result.workers
    truncated.value = result.sampleTruncated
    state.value = 'ready'
  } catch {
    if (
      generation !== requestGeneration ||
      projectId !== props.projectId ||
      ticketId !== props.ticketId
    )
      return
    // Never retain or invent worker provenance after an authorization,
    // transport, or contract failure.
    workers.value = []
    state.value = 'error'
  }
}
</script>

<template>
  <section class="am-worker-context" aria-labelledby="am-worker-context-title">
    <div class="am-worker-context__head">
      <div>
        <h3 id="am-worker-context-title">{{ t('agentMode.workerContext.title') }}</h3>
        <p>{{ t('agentMode.workerContext.subtitle') }}</p>
      </div>
      <button type="button" :disabled="state === 'loading'" @click="load">
        {{
          state === 'loading'
            ? t('agentMode.workerContext.action.loading')
            : state === 'idle'
              ? t('agentMode.workerContext.action.show')
              : t('agentMode.workerContext.action.refresh')
        }}
      </button>
    </div>

    <p v-if="state === 'error'" role="status">
      {{ t('agentMode.workerContext.error') }}
    </p>
    <p v-else-if="state === 'ready' && workers.length === 0" role="status">
      {{ t('agentMode.workerContext.empty')
      }}<template v-if="truncated"> {{ t('agentMode.workerContext.truncated') }}</template>
    </p>
    <div v-else-if="state === 'ready'" class="am-worker-context__workers">
      <article
        v-for="worker in workers"
        :key="worker.harnessSessionId"
        class="am-worker-context__worker"
      >
        <div class="am-worker-context__identity">
          <strong>{{ worker.agentName }}</strong>
          <span class="am-worker-context__shape" :data-shape="worker.shape">{{
            t(`agentMode.workerContext.shape.${worker.shape}`)
          }}</span>
        </div>
        <dl>
          <div>
            <dt>{{ t('agentMode.workerContext.field.machine') }}</dt>
            <dd>{{ worker.machineId ?? t('agentMode.workerContext.unknown') }}</dd>
          </div>
          <div>
            <dt>{{ t('agentMode.workerContext.field.management') }}</dt>
            <dd>{{ t(`agentMode.workerContext.management.${worker.managementMode}`) }}</dd>
          </div>
          <div>
            <dt>{{ t('agentMode.workerContext.field.provenanceTrust') }}</dt>
            <dd>
              {{ t(`agentMode.workerContext.provenanceTrust.${worker.runtimeProvenanceTrust}`) }}
            </dd>
          </div>
          <div>
            <dt>{{ t('agentMode.workerContext.field.workspace') }}</dt>
            <dd>
              {{
                worker.workspace
                  ? `${t(`agentMode.workerContext.workspaceKind.${worker.workspace.kind}`)} · ${t(`agentMode.workerContext.workspaceMode.${worker.workspace.mode}`)}`
                  : t('agentMode.workerContext.unknown')
              }}
            </dd>
          </div>
          <div>
            <dt>{{ t('agentMode.workerContext.field.dispatch') }}</dt>
            <dd>
              {{
                worker.dispatch
                  ? `${worker.dispatch.model} · ${t(`agentMode.workerContext.effort.${worker.dispatch.effort}`)}`
                  : t('agentMode.workerContext.unknown')
              }}
            </dd>
          </div>
          <div>
            <dt>{{ t('agentMode.workerContext.field.account') }}</dt>
            <dd>{{ t(`agentMode.workerContext.account.${worker.accountLabel}`) }}</dd>
          </div>
          <div>
            <dt>{{ t('agentMode.workerContext.field.output') }}</dt>
            <dd>{{ t(`agentMode.workerContext.output.${worker.outputKind}`) }}</dd>
          </div>
        </dl>
        <p v-if="worker.shape === 'scout'" class="am-worker-context__note">
          {{ t('agentMode.workerContext.scoutNote') }}
        </p>
        <p v-else-if="worker.shape === 'unknown'" class="am-worker-context__note">
          {{ t('agentMode.workerContext.unknownNote') }}
        </p>
        <p v-if="worker.runtimeProvenanceTrust === 'untrusted'" class="am-worker-context__note">
          {{ t('agentMode.workerContext.untrustedNote') }}
        </p>
      </article>
      <p v-if="truncated" class="am-worker-context__note">
        {{ t('agentMode.workerContext.truncated') }}
      </p>
    </div>
  </section>
</template>

<style scoped>
.am-worker-context {
  padding: 14px;
  border: 1px solid var(--am-line);
  border-radius: 10px;
  background: var(--am-surface);
}
.am-worker-context__head {
  display: flex;
  align-items: start;
  justify-content: space-between;
  gap: 12px;
}
.am-worker-context h3 {
  margin: 0;
  font-size: 13px;
  color: var(--am-ink);
}
.am-worker-context p {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--am-muted);
}
.am-worker-context button {
  min-height: 34px;
  padding: 6px 11px;
  border: 1px solid var(--am-line);
  border-radius: 8px;
  background: transparent;
  color: var(--am-ink);
  font-size: 12px;
}
.am-worker-context button:focus-visible {
  outline: 2px solid var(--am-focus);
  outline-offset: 2px;
}
.am-worker-context__workers {
  display: grid;
  gap: 9px;
  margin-top: 12px;
}
.am-worker-context__worker {
  padding: 10px;
  border: 1px solid var(--am-line);
  border-radius: 8px;
}
.am-worker-context__identity {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.am-worker-context__shape {
  padding: 2px 7px;
  border-radius: 999px;
  background: var(--am-surface-raised, var(--am-line));
  font:
    10px 'JetBrains Mono',
    ui-monospace,
    monospace;
  text-transform: capitalize;
}
.am-worker-context dl {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
  gap: 8px;
  margin: 10px 0 0;
}
.am-worker-context dl div {
  min-width: 0;
}
.am-worker-context dt {
  font-size: 10px;
  color: var(--am-muted);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.am-worker-context dd {
  overflow-wrap: anywhere;
  margin: 2px 0 0;
  font:
    11px 'JetBrains Mono',
    ui-monospace,
    monospace;
  color: var(--am-ink);
}
.am-worker-context .am-worker-context__note {
  margin-top: 9px;
  color: var(--am-ink);
}
@media (max-width: 560px) {
  .am-worker-context__head {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>
