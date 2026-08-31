<script setup lang="ts">
import type { Paimos6SessionZoomTotals, Paimos6ZoomBand } from '@/v6/sessionHomeZoom'

defineProps<{
  totals: Paimos6SessionZoomTotals
  band: Paimos6ZoomBand
  sampleLimit: number
  sampleTruncated: boolean
  sampledSessions: number
}>()
</script>

<template>
  <section class="p6-overview" aria-labelledby="p6-overview-title" style="min-width: 0; max-width: 100%; box-sizing: border-box;">
    <div class="p6-overview-copy">
      <p>Exception-first projection · {{ band }}</p>
      <h3 id="p6-overview-title">{{ totals.sessions }} authorized sessions</h3>
      <span>
        Showing {{ sampledSessions }} of {{ totals.sessions }}; bounded at {{ sampleLimit }}.
        {{ sampleTruncated ? 'The sample is truncated.' : 'The sample is complete.' }}
      </span>
    </div>
    <dl>
      <div><dt>Attention sessions</dt><dd>{{ totals.attention_sessions }}</dd></div>
      <div><dt>Exception targets</dt><dd>{{ totals.exception_targets }}</dd></div>
      <div><dt>Sampled exception targets</dt><dd>{{ totals.sampled_exception_targets }}</dd></div>
      <div><dt>Held messages</dt><dd>{{ totals.exception_messages }}</dd></div>
      <div><dt>Action requests</dt><dd>{{ totals.action_requests }}</dd></div>
      <div><dt>Unread</dt><dd>{{ totals.unread }}</dd></div>
    </dl>
  </section>
</template>

<style scoped>
.p6-overview {
  display: grid;
  min-width: 0;
  max-width: 100%;
  grid-template-columns: minmax(190px, 0.85fr) minmax(0, 1.5fr);
  gap: 18px;
  margin: 13px 0 17px;
  padding: 15px;
  border: 1px solid #d7e0da;
  border-radius: 14px;
  background: #f8faf7;
}
.p6-overview-copy { min-width: 0; }
.p6-overview-copy p { color: #5d7467; font-size: 9px; font-weight: 750; letter-spacing: 0.07em; text-transform: uppercase; }
.p6-overview-copy h3 { margin-top: 4px; color: #31443a; font: 600 18px/1.1 "Bricolage Grotesque", sans-serif; }
.p6-overview-copy span { display: block; margin-top: 7px; color: #68756e; font-size: 9.5px; line-height: 1.45; }
.p6-overview dl { display: grid; min-width: 0; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; }
.p6-overview dl div { min-width: 0; padding: 8px; border-left: 1px solid #d5dfd9; }
.p6-overview dt { overflow-wrap: anywhere; color: #68756e; font-size: 8.5px; line-height: 1.25; }
.p6-overview dd { margin-top: 4px; color: #315b47; font: 650 16px/1 "JetBrains Mono", monospace; }

@media (max-width: 680px) {
  .p6-overview { grid-template-columns: minmax(0, 1fr); }
  .p6-overview dl { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
</style>
