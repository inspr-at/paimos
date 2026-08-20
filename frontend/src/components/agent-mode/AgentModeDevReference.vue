<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  AGPL-3.0-only — see LICENSE.

  PAI-805 — DEV-ONLY fixture-backed Agent Mode reference.
  Route: /dev/agent-mode?n=10&state=ready   (DEV builds only)
    n      1 | 10 | 100                      fixture size
    state  ready | empty | offline | forbidden | not-found | error
  The live chip says "Fixture data" so nobody mistakes it for production.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

import AgentModeView from '@/views/AgentModeView.vue'
import { AgentModeLoadError, type AgentModeSnapshotLoader } from '@/services/agentMode'
import { FIXTURE_BASE_TIME, makeFixtureSnapshot } from '@/services/agentModeFixtures'
import { normalizeWireSnapshot, type WireSnapshot } from '@/services/agentModeTransport'

const route = useRoute()

const ISO_RE = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/

/** Shift every fixture timestamp so "now" in the fixture equals real now. */
function rebase(wire: WireSnapshot, nowMs: number): WireSnapshot {
  const delta = nowMs - Date.parse(FIXTURE_BASE_TIME)
  return JSON.parse(
    JSON.stringify(wire, (_key, value) =>
      typeof value === 'string' && ISO_RE.test(value) ? new Date(Date.parse(value) + delta).toISOString() : value,
    ),
  ) as WireSnapshot
}

const count = computed(() => {
  const n = Number(route.query.n)
  return n === 1 || n === 100 ? n : 10
})
const state = computed(() => String(route.query.state ?? 'ready'))

const loader = computed<AgentModeSnapshotLoader>(() => async () => {
  await new Promise((r) => setTimeout(r, 250))
  switch (state.value) {
    case 'empty':
      return normalizeWireSnapshot({ server_time: new Date().toISOString(), revision: 'fx-empty', deliveries: [] }, Date.now())
    case 'offline':
      throw new AgentModeLoadError('offline', 'fixture: network unreachable', 0)
    case 'forbidden':
      throw new AgentModeLoadError('forbidden', 'fixture: forbidden', 403)
    case 'not-found':
      throw new AgentModeLoadError('not-found', 'fixture: not found', 404)
    case 'error':
      throw new AgentModeLoadError('error', 'fixture: internal error', 500)
    default:
      return normalizeWireSnapshot(rebase(makeFixtureSnapshot(count.value), Date.now()), Date.now())
  }
})
</script>

<template>
  <AgentModeView :key="`${count}-${state}`" :loader="loader" source-label="Fixture data" />
</template>
