<!--
  PAIMOS — Your Professional & Personal AI Project OS
  Copyright (C) 2026 Markus Barta <markus@barta.com>
  Licensed under AGPL-3.0-only; see LICENSE.
-->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, errMsg } from '@/api/client'
import { useConfirm } from '@/composables/useConfirm'
import { useAuthStore } from '@/stores/auth'
import type { Issue, User } from '@/types'

const PEOPLE = ['Markus', 'David'] as const
const WEEKLY_TARGET = 38.5
const ASSUMED_DAY_HOURS = WEEKLY_TARGET / 5

interface WeekDayRow {
  date: string
  hours: number
  entries: number
  assumed: boolean
}

interface WeekUserRow {
  user_id: number
  username: string
  days: WeekDayRow[]
}

interface WeekResponse {
  from: string
  to: string
  users: WeekUserRow[]
}

const auth = useAuthStore()
const { confirm } = useConfirm()
const users = ref<User[]>([])
const report = ref<WeekResponse>({ from: '', to: '', users: [] })
const loading = ref(true)
const filing = ref(false)
const error = ref('')
const notice = ref('')
const drafts = ref<Record<string, number>>({})

function isoDate(date: Date): string {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

function dateFromISO(value: string): Date {
  return new Date(`${value}T12:00:00`)
}

function addDays(value: string, amount: number): string {
  const date = dateFromISO(value)
  date.setDate(date.getDate() + amount)
  return isoDate(date)
}

function mondayFor(date: Date): string {
  const monday = new Date(date.getFullYear(), date.getMonth(), date.getDate(), 12)
  const weekday = monday.getDay() || 7
  monday.setDate(monday.getDate() - weekday + 1)
  return isoDate(monday)
}

function isoWeek(value: string): number {
  const date = dateFromISO(value)
  const utc = new Date(Date.UTC(date.getFullYear(), date.getMonth(), date.getDate()))
  const weekday = utc.getUTCDay() || 7
  utc.setUTCDate(utc.getUTCDate() + 4 - weekday)
  const yearStart = new Date(Date.UTC(utc.getUTCFullYear(), 0, 1))
  return Math.ceil((((utc.getTime() - yearStart.getTime()) / 86400000) + 1) / 7)
}

const weekStart = ref(mondayFor(new Date()))
const days = computed(() => Array.from({ length: 7 }, (_, index) => {
  const date = addDays(weekStart.value, index)
  return {
    date,
    short: dateFromISO(date).toLocaleDateString(undefined, { weekday: 'short' }),
    label: dateFromISO(date).toLocaleDateString(undefined, { month: 'short', day: 'numeric' }),
    weekday: index < 5,
  }
}))
const weekEnd = computed(() => addDays(weekStart.value, 6))
const weekLabel = computed(() => `ISO week ${isoWeek(weekStart.value)}`)

const people = computed(() => PEOPLE.map(username => ({
  username,
  user: users.value.find(user => user.username.toLowerCase() === username.toLowerCase()),
})))

function reportUser(userID: number | undefined): WeekUserRow | undefined {
  return userID == null ? undefined : report.value.users.find(row => row.user_id === userID)
}

function dayRow(userID: number | undefined, date: string): WeekDayRow | undefined {
  return reportUser(userID)?.days.find(day => day.date === date)
}

function draftKey(userID: number, date: string): string {
  return `${userID}:${date}`
}

function bookedTotal(userID: number | undefined): number {
  return reportUser(userID)?.days.reduce((total, day) => total + day.hours, 0) ?? 0
}

function canFileFor(userID: number | undefined): boolean {
  if (userID == null || reportUser(userID) == null) return false
  return auth.isSuperAdmin || userID === auth.user?.id
}

function resetDrafts() {
  const next: Record<string, number> = {}
  for (const person of people.value) {
    if (!person.user || !reportUser(person.user.id)) continue
    for (const day of days.value) {
      if (day.weekday && (dayRow(person.user.id, day.date)?.hours ?? 0) === 0) {
        next[draftKey(person.user.id, day.date)] = ASSUMED_DAY_HOURS
      }
    }
  }
  drafts.value = next
}

async function loadWeek() {
  loading.value = true
  error.value = ''
  try {
    const query = new URLSearchParams({
      from: weekStart.value,
      to: weekEnd.value,
      usernames: PEOPLE.join(','),
    })
    report.value = await api.get<WeekResponse>(`/time-entries/week?${query}`)
    resetDrafts()
  } catch (cause) {
    report.value = { from: weekStart.value, to: weekEnd.value, users: [] }
    error.value = errMsg(cause, 'Failed to load weekly hours.')
  } finally {
    loading.value = false
  }
}

async function changeWeek(amount: number) {
  weekStart.value = addDays(weekStart.value, amount * 7)
  notice.value = ''
  await loadWeek()
}

interface FilingCandidate {
  userID: number
  date: string
  hours: number
}

const filingCandidates = computed<FilingCandidate[]>(() => {
  const candidates: FilingCandidate[] = []
  for (const person of people.value) {
    if (!person.user || !canFileFor(person.user.id)) continue
    for (const day of days.value) {
      const hours = drafts.value[draftKey(person.user.id, day.date)] ?? 0
      if (day.weekday && (dayRow(person.user.id, day.date)?.hours ?? 0) === 0 && hours > 0) {
        candidates.push({ userID: person.user.id, date: day.date, hours })
      }
    }
  }
  return candidates
})

async function fileWeek() {
  const candidates = filingCandidates.value
  if (candidates.length === 0) return
  const accepted = await confirm({
    title: 'File weekly hours?',
    message: `Create ${candidates.length} manual time entries on AISP-3? Existing booked days will not be changed.`,
    confirmLabel: 'File week',
  })
  if (!accepted) return

  filing.value = true
  error.value = ''
  notice.value = ''
  try {
    const issue = await api.get<Issue>('/issues/AISP-3')
    for (const candidate of candidates) {
      const stamp = `${candidate.date}T12:00:00Z`
      const body: Record<string, unknown> = {
        override: candidate.hours,
        started_at: stamp,
        stopped_at: stamp,
        comment: 'Assumed working hours filed from the weekly hours view',
      }
      if (candidate.userID !== auth.user?.id) body.user_id = candidate.userID
      await api.post(`/issues/${issue.id}/time-entries`, body)
    }
    notice.value = `${candidates.length} time entries filed.`
    await loadWeek()
  } catch (cause) {
    error.value = errMsg(cause, 'Failed to file weekly hours.')
  } finally {
    filing.value = false
  }
}

onMounted(async () => {
  try {
    users.value = await api.get<User[]>('/users')
  } catch (cause) {
    error.value = errMsg(cause, 'Failed to load users.')
  }
  await loadWeek()
})
</script>

<template>
  <Teleport defer to="#app-header-left">
    <span class="ah-title">Weekly Hours</span>
    <span class="ah-subtitle">Zeitbuchungen · Arbeitszeiterfassung</span>
  </Teleport>

  <section class="hours-page">
    <div class="hours-toolbar">
      <div>
        <h1>{{ weekLabel }}</h1>
        <p>{{ weekStart }} – {{ weekEnd }} · Monday to Sunday</p>
      </div>
      <div class="toolbar-actions">
        <button class="btn btn-ghost btn-sm" aria-label="Previous week" @click="changeWeek(-1)">←</button>
        <button class="btn btn-ghost btn-sm" @click="changeWeek(0)">Reload</button>
        <button class="btn btn-ghost btn-sm" aria-label="Next week" @click="changeWeek(1)">→</button>
        <button
          class="btn btn-primary btn-sm file-week"
          :disabled="filing || filingCandidates.length === 0"
          @click="fileWeek"
        >
          {{ filing ? 'Filing…' : 'File week' }}
        </button>
      </div>
    </div>

    <p v-if="error" class="state state-error">{{ error }}</p>
    <p v-if="notice" class="state state-success">{{ notice }}</p>
    <p v-if="loading" class="state">Loading weekly hours…</p>

    <div v-else class="hours-table-wrap">
      <table class="hours-table">
        <thead>
          <tr>
            <th>Person</th>
            <th v-for="day in days" :key="day.date">
              {{ day.short }}
              <span>{{ day.label }}</span>
            </th>
            <th>Weekly total</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="person in people" :key="person.username">
            <th class="person-cell">
              {{ person.username }}
              <span v-if="!person.user || !reportUser(person.user.id)" class="unavailable">Unavailable</span>
            </th>
            <td v-for="day in days" :key="day.date">
              <template v-if="person.user && reportUser(person.user.id)">
                <template v-if="day.weekday && (dayRow(person.user.id, day.date)?.hours ?? 0) === 0">
                  <input
                    v-model.number="drafts[draftKey(person.user.id, day.date)]"
                    class="hours-input"
                    type="number"
                    min="0"
                    max="24"
                    step="0.1"
                    :aria-label="`${person.username} ${day.date} assumed hours`"
                    :disabled="!canFileFor(person.user.id)"
                  >
                  <span class="assumed-label">assumed</span>
                </template>
                <template v-else>
                  <span class="hours-value">{{ (dayRow(person.user.id, day.date)?.hours ?? 0).toFixed(1) }}h</span>
                  <span v-if="dayRow(person.user.id, day.date)?.assumed" class="assumed-label">assumed</span>
                </template>
              </template>
              <span v-else>—</span>
            </td>
            <td class="total-cell">
              <template v-if="person.user && reportUser(person.user.id)">
                <strong>{{ bookedTotal(person.user.id).toFixed(1) }}h booked</strong>
                <span>/ {{ WEEKLY_TARGET.toFixed(1) }}h target</span>
              </template>
              <span v-else>Unavailable</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.hours-page { display: flex; flex-direction: column; gap: 1rem; }
.hours-toolbar {
  display: flex; align-items: flex-end; justify-content: space-between; gap: 1rem;
  padding: 1rem 1.25rem; background: var(--bg-card); border: 1px solid var(--border);
  border-radius: 8px; box-shadow: var(--shadow);
}
.hours-toolbar h1 { margin: 0; font-size: 17px; }
.hours-toolbar p { margin: .2rem 0 0; color: var(--text-muted); font-size: 13px; }
.toolbar-actions { display: flex; flex-wrap: wrap; gap: .4rem; }
.hours-table-wrap { overflow-x: auto; border: 1px solid var(--border); border-radius: 8px; background: var(--bg-card); }
.hours-table { width: 100%; min-width: 920px; border-collapse: collapse; font-size: 13px; }
.hours-table th, .hours-table td { padding: .75rem; border-bottom: 1px solid var(--border); text-align: center; vertical-align: top; }
.hours-table thead th { color: var(--text-muted); font-size: 11px; text-transform: uppercase; letter-spacing: .04em; }
.hours-table thead th span { display: block; margin-top: .15rem; font-weight: 400; text-transform: none; letter-spacing: 0; }
.hours-table tbody tr:last-child th, .hours-table tbody tr:last-child td { border-bottom: 0; }
.person-cell { min-width: 110px; text-align: left !important; font-size: 14px; }
.unavailable, .assumed-label { display: block; margin-top: .2rem; color: var(--text-muted); font-size: 10px; font-weight: 500; text-transform: uppercase; letter-spacing: .04em; }
.assumed-label { color: var(--brand-blue); }
.hours-input { width: 58px; padding: .3rem; text-align: right; color: var(--text); background: var(--bg); border: 1px solid var(--border); border-radius: 5px; }
.hours-value { display: block; font-variant-numeric: tabular-nums; }
.total-cell { min-width: 145px; text-align: left !important; }
.total-cell strong, .total-cell span { display: block; }
.total-cell span { margin-top: .15rem; color: var(--text-muted); font-size: 11px; }
.state { margin: 0; color: var(--text-muted); }
.state-error { color: var(--danger, #b91c1c); }
.state-success { color: var(--success, #15803d); }
@media (max-width: 720px) {
  .hours-toolbar { align-items: stretch; flex-direction: column; }
}
</style>
