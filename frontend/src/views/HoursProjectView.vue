<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  Licensed under AGPL-3.0-only; see LICENSE.
-->
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { api, errMsg } from '@/api/client'
import type { Project } from '@/types'

interface TimeReportUser {
  user_id: number
  username: string
  hours: number
  entries: number
}

interface TimeReportIssue {
  issue_id: number
  issue_key: string
  title: string
  hours: number
  entries: number
}

interface TimeReport {
  total_hours: number
  total_entries: number
  by_user: TimeReportUser[]
  by_issue: TimeReportIssue[]
}

function isoDate(date: Date): string {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

function monthStart(date = new Date()): string {
  return isoDate(new Date(date.getFullYear(), date.getMonth(), 1))
}

function monthEnd(date = new Date()): string {
  return isoDate(new Date(date.getFullYear(), date.getMonth() + 1, 0))
}

const project = ref<Project | null>(null)
const report = ref<TimeReport | null>(null)
const fromDate = ref(monthStart())
const toDate = ref(monthEnd())
const loading = ref(true)
const error = ref('')

async function loadReport() {
  if (!project.value || !fromDate.value || !toDate.value) return
  loading.value = true
  error.value = ''
  try {
    const query = new URLSearchParams({ from: fromDate.value, to: toDate.value })
    report.value = await api.get<TimeReport>(`/projects/${project.value.id}/time-report?${query}`)
  } catch (cause) {
    report.value = null
    error.value = errMsg(cause, 'Failed to load project hours.')
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  try {
    const projects = await api.get<Project[]>('/projects')
    project.value = projects.find(item => item.key.toUpperCase() === 'AISP') ?? null
    if (!project.value) {
      error.value = 'AISP project is unavailable.'
      loading.value = false
      return
    }
    await loadReport()
  } catch (cause) {
    error.value = errMsg(cause, 'Failed to load AISP project.')
    loading.value = false
  }
})
</script>

<template>
  <Teleport defer to="#app-header-left">
    <span class="ah-title">Project Hours</span>
    <span class="ah-subtitle">Projektbuchungen · AISP grant evidence</span>
  </Teleport>

  <section class="project-hours">
    <div class="range-card">
      <div>
        <h1>AISP project hours</h1>
        <p>Read-only time entries rolled up by issue and person.</p>
      </div>
      <label>
        From
        <input v-model="fromDate" type="date">
      </label>
      <label>
        To
        <input v-model="toDate" type="date">
      </label>
      <button class="btn btn-primary btn-sm" :disabled="loading || !project" @click="loadReport">Load</button>
    </div>

    <p v-if="error" class="state state-error">{{ error }}</p>
    <p v-if="loading" class="state">Loading project hours…</p>

    <template v-else-if="report">
      <div class="summary-card">
        <strong>{{ report.total_hours.toFixed(1) }}h</strong>
        <span>{{ report.total_entries }} entries · {{ fromDate }} – {{ toDate }}</span>
      </div>

      <div class="report-sections">
        <section class="report-card">
          <h2>By issue</h2>
          <table>
            <thead><tr><th>Issue</th><th>Title</th><th>Hours</th><th>Entries</th></tr></thead>
            <tbody>
              <tr v-for="issue in report.by_issue" :key="issue.issue_id">
                <td><RouterLink :to="`/issues/${issue.issue_key}`">{{ issue.issue_key }}</RouterLink></td>
                <td>{{ issue.title }}</td>
                <td>{{ issue.hours.toFixed(1) }}h</td>
                <td>{{ issue.entries }}</td>
              </tr>
              <tr v-if="report.by_issue.length === 0"><td colspan="4" class="empty">No entries in this range.</td></tr>
            </tbody>
          </table>
        </section>

        <section class="report-card user-card">
          <h2>By person</h2>
          <table>
            <thead><tr><th>Person</th><th>Hours</th><th>Entries</th></tr></thead>
            <tbody>
              <tr v-for="user in report.by_user" :key="user.user_id">
                <td>{{ user.username }}</td>
                <td>{{ user.hours.toFixed(1) }}h</td>
                <td>{{ user.entries }}</td>
              </tr>
              <tr v-if="report.by_user.length === 0"><td colspan="3" class="empty">No entries in this range.</td></tr>
            </tbody>
          </table>
        </section>
      </div>
    </template>
  </section>
</template>

<style scoped>
.project-hours { display: flex; flex-direction: column; gap: 1rem; }
.range-card {
  display: flex; flex-wrap: wrap; align-items: flex-end; gap: .85rem;
  padding: 1rem 1.25rem; background: var(--bg-card); border: 1px solid var(--border);
  border-radius: 8px; box-shadow: var(--shadow);
}
.range-card > div { flex: 1 1 280px; }
.range-card h1 { margin: 0; font-size: 17px; }
.range-card p { margin: .2rem 0 0; color: var(--text-muted); font-size: 13px; }
.range-card label { display: flex; flex-direction: column; gap: .25rem; color: var(--text-muted); font-size: 11px; font-weight: 700; text-transform: uppercase; }
.range-card input { padding: .4rem .55rem; color: var(--text); background: var(--bg); border: 1px solid var(--border); border-radius: 6px; font: inherit; }
.summary-card { display: flex; align-items: baseline; gap: .7rem; padding: .85rem 1rem; background: var(--brand-blue-pale); border-radius: 8px; }
.summary-card strong { font-size: 20px; color: var(--brand-blue); }
.summary-card span { color: var(--text-muted); font-size: 12px; }
.report-sections { display: grid; grid-template-columns: minmax(0, 2fr) minmax(260px, 1fr); gap: 1rem; }
.report-card { overflow-x: auto; padding: 1rem; background: var(--bg-card); border: 1px solid var(--border); border-radius: 8px; box-shadow: var(--shadow); }
.report-card h2 { margin: 0 0 .75rem; font-size: 14px; }
table { width: 100%; border-collapse: collapse; font-size: 13px; }
th, td { padding: .55rem; border-bottom: 1px solid var(--border); text-align: left; }
th { color: var(--text-muted); font-size: 10px; text-transform: uppercase; letter-spacing: .04em; }
tbody tr:last-child td { border-bottom: 0; }
a { color: var(--brand-blue); font-weight: 700; text-decoration: none; }
.empty, .state { color: var(--text-muted); }
.state { margin: 0; }
.state-error { color: var(--danger, #b91c1c); }
@media (max-width: 800px) { .report-sections { grid-template-columns: 1fr; } }
</style>
