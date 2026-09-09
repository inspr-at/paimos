<script setup lang="ts">
/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 */

import '@inspr/flow-shell'
import flowLogo from '@inspr/flow-shell/assets/inspr-logo.svg'

import { computed, nextTick, ref, watch } from 'vue'
import { useRouter } from 'vue-router'

import { instanceLabel } from '@/api/instance'
import { useBranding } from '@/composables/useBranding'
import { useFlowHost } from '@/composables/useFlowHost'
import { FLOW_OVERVIEW_LOCATION } from '@/services/flowHost'
import { formatDisplayVersion } from '@/utils/version'
import { publicURL, stripPublicBase } from '@/publicPath'

const CONSEQUENTIAL = new Set(['flow:start-intent', 'flow:review-batch', 'flow:save-proposal'])

type FlowShellElement = HTMLElement & {
  shellState?: unknown
  showNotice?: (text: string) => void
}

const props = defineProps<{
  projectId: number | null
  contentLayout?: 'document' | 'fill'
}>()

const emit = defineEmits<{
  active: [value: boolean]
}>()

const { brandName } = useBranding()
const router = useRouter()
const projectRef = computed(() => props.projectId)
const { state, active, notice, submitIntent } = useFlowHost(projectRef)
const logoSrc = flowLogo as string
const shellEl = ref<FlowShellElement | null>(null)
const useFillLayout = computed(() => props.contentLayout === 'fill')

watch(active, (value) => emit('active', value), { immediate: true })

watch(
  [state, brandName, instanceLabel, shellEl],
  () => {
    const shell = shellEl.value
    const current = state.value
    if (!shell || !current) return
    shell.shellState = {
      ...current,
      header: {
        ...current.header,
        appName: brandName.value || 'Paimos',
        instanceLabel: instanceLabel.value || current.header.instanceLabel || 'Paimos',
        version: formatDisplayVersion(__APP_VERSION__),
      },
    }
  },
  { immediate: true },
)

watch(notice, (text) => {
  if (text) shellEl.value?.showNotice?.(text)
})

async function onFlowIntent(event: Event) {
  const detail = (event as CustomEvent<{ type?: string; error?: string }>).detail
  if (!detail || detail.error) {
    if (detail?.error) notice.value = String(detail.error)
    return
  }
  const type = String(detail.type || '')
  if (type === 'flow:navigate-stage' || type === 'flow:toggle-map') return
  if (type === 'flow:header-identity' || type === 'flow:header-account') {
    await router.push('/settings?tab=account').catch(() => {})
    return
  }
  if (type === 'flow:header-project' && props.projectId) {
    await router.push({ path: `/projects/${props.projectId}`, query: { tab: 'overview' } }).catch(() => {})
    return
  }
  if (type === 'flow:health') {
    try {
      const response = await fetch(publicURL('/api/health'), { headers: { accept: 'application/json' } })
      const body = (await response.json().catch(() => ({}))) as { version?: string }
      notice.value = response.ok
        ? `Paimos health probe succeeded (${body.version || formatDisplayVersion(__APP_VERSION__)}). This is not delivery evidence.`
        : 'Paimos health probe failed. This is not delivery evidence.'
    } catch {
      notice.value = 'Paimos health probe failed. This is not delivery evidence.'
    }
    return
  }
  if (!CONSEQUENTIAL.has(type) && type !== 'flow:view-drafts') return
  const result = await submitIntent(type)
  if (!result || result.executed) return
  const location = result.location || (props.projectId ? FLOW_OVERVIEW_LOCATION(props.projectId) : '')
  if (!location) return
  const target = new URL(location, window.location.origin)
  await router.push(stripPublicBase(`${target.pathname}${target.search}${target.hash}`)).catch(() => {})
  await nextTick()
  document.getElementById('baseline-batch')?.scrollIntoView({ block: 'start' })
}
</script>

<template>
  <inspr-flow-shell
    v-if="active"
    ref="shellEl"
    class="paimos-flow-host"
    layout-mode="bounded"
    :content-layout="useFillLayout ? 'fill' : undefined"
    :logo-src="logoSrc"
    data-testid="paimos-flow-host"
    @flow-intent="onFlowIntent"
  >
    <div
      class="paimos-flow-toolbar"
      :data-flow-host-region="useFillLayout ? 'toolbar' : undefined"
    >
      <slot name="toolbar" />
    </div>
    <div
      class="paimos-flow-body"
      :data-flow-host-region="useFillLayout ? 'body' : undefined"
    >
      <slot />
    </div>
  </inspr-flow-shell>
  <template v-else>
    <slot name="toolbar" />
    <slot />
  </template>
</template>

<style scoped>
.paimos-flow-host {
  --ink: var(--paimos-ink, #203c3d);
  --muted: var(--text-muted, #596e70);
  --line: var(--border, #d8e3e9);
  --blue: var(--paimos-teal, #0e6f6c);
  --gold: var(--paimos-gold, #d69b31);
  --green: var(--paimos-teal, #0e6f6c);
  --glass: color-mix(in srgb, var(--paimos-ivory, #fffefa) 82%, transparent);
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
  min-width: 0;
  padding-bottom: var(--shell-footer-space);
}
.paimos-flow-toolbar,
.paimos-flow-body {
  min-width: 0;
}
.paimos-flow-body {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
.paimos-flow-host[content-layout='fill'] .paimos-flow-body {
  height: 100%;
}
</style>

<style>
[data-theme='night'] .paimos-flow-host {
  --ink: #edf4f0;
  --muted: #acc3c2;
  --line: #2a4a4d;
  --blue: #a4e5df;
  --glass: color-mix(in srgb, #183034 82%, transparent);
}
</style>
