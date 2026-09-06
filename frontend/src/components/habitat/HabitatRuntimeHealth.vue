<script setup lang="ts">
import { computed, onScopeDispose, ref, shallowRef, watch } from 'vue'
import { loadRuntimeHealth, type RuntimeHealthPage } from './habitatRuntimeHealth'
import { humanize } from './habitatModel'
const props = defineProps<{ projectIds: number[]; authority: string; fresh: boolean }>()
const pages = shallowRef<RuntimeHealthPage[]>([])
const unavailable = ref(false),
  loading = ref(false),
  observed = ref(0),
  now = ref(Date.now())
let generation = 0
let controller: AbortController | null = null
const sampledIds = computed(() => [...new Set(props.projectIds)].slice(0, 10))
const reports = computed(() => [
  ...new Map(
    pages.value.flatMap((page) => page.runtimes).map((runtime) => [runtime.runtime_id, runtime]),
  ).values(),
])
const stale = computed(() => !props.fresh || now.value - observed.value > 45000)
const issues = computed(() =>
  reports.value.filter(
    (runtime) =>
      runtime.status === 'offline' ||
      Date.parse(runtime.expires_at) <= now.value ||
      runtime.layers.some(
        (layer) =>
          layer.state === 'unhealthy' || layer.status === 'stale' || layer.status === 'offline',
      ),
  ),
)
const unknown = computed(
  () =>
    unavailable.value ||
    reports.value.some((runtime) =>
      runtime.layers.some((layer) => layer.state === 'unknown' || layer.status === 'unknown'),
    ),
)
const summary = computed(() =>
  loading.value && !observed.value
    ? 'Checking connections…'
    : stale.value
      ? 'Connection status is unconfirmed.'
      : unavailable.value
        ? 'Some connection health is unavailable.'
        : !reports.value.length
          ? 'No runtime reports in this view.'
          : issues.value.length
            ? `${issues.value.length} ${issues.value.length === 1 ? 'runtime needs' : 'runtimes need'} a check.`
            : unknown.value
              ? 'Some connections have not reported.'
              : 'Reported connections are responding.',
)
async function refresh(clear = false) {
  const version = ++generation
  controller?.abort()
  controller = new AbortController()
  const signal = controller.signal
  if (clear) {
    pages.value = []
    unavailable.value = false
    observed.value = 0
  }
  loading.value = true
  const results = await Promise.allSettled(
    sampledIds.value.map((id) => loadRuntimeHealth(id, signal)),
  )
  if (version !== generation || signal.aborted) return
  pages.value = results.flatMap((result) => (result.status === 'fulfilled' ? [result.value] : []))
  unavailable.value = results.some((result) => result.status === 'rejected')
  observed.value = Date.now()
  now.value = Date.now()
  loading.value = false
}
watch(
  () => [props.authority, sampledIds.value.join(',')],
  () => void refresh(true),
  { immediate: true, flush: 'sync' },
)
const timer = setInterval(() => {
  now.value = Date.now()
  if (!document.hidden && !loading.value) void refresh()
}, 15000)
onScopeDispose(() => {
  generation++
  controller?.abort()
  clearInterval(timer)
})
</script>
<template>
  <section class="habitat-health" aria-label="Runtime connections">
    <div class="habitat-section-head"><h2>Connections</h2></div>
    <p class="habitat-health-summary" :data-warning="issues.length > 0 || unknown || stale">
      {{ summary }}
    </p>
    <details>
      <summary>Connection details</summary>
      <p v-if="projectIds.length > sampledIds.length">
        Showing health for {{ sampledIds.length }} of {{ projectIds.length }} projects in this view.
      </p>
      <div v-for="runtime in reports" :key="runtime.runtime_id" class="habitat-runtime-health">
        <h3>{{ runtime.machine_id }}</h3>
        <p>
          {{
            stale || Date.parse(runtime.expires_at) <= now
              ? 'Current connection unconfirmed'
              : humanize(runtime.status)
          }}
        </p>
        <dl class="habitat-facts">
          <template v-for="layer in runtime.layers" :key="layer.layer"
            ><dt>
              {{ layer.layer === 'reporter' ? 'Reporter' : `${humanize(layer.layer)} listener` }}
            </dt>
            <dd>
              {{ stale ? 'Unconfirmed' : humanize(layer.status) }} · {{ humanize(layer.state)
              }}<small v-if="layer.reason !== 'recovered'">{{ humanize(layer.reason) }}</small>
            </dd></template
          >
        </dl>
      </div>
      <button type="button" :disabled="loading" @click="refresh()">Refresh connections</button>
    </details>
  </section>
</template>
