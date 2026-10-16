// SPDX-License-Identifier: AGPL-3.0-only
// A conversation handed over from START (AEON-1441). The handoff answers with
// /new-project#draft=<capability>&lang=<de|en>: the fragment never reaches a
// server or proxy log. The router moves it into this tab's storage before any
// other rule runs, so no return path, redirect or report can copy it, and the
// assistant sends it only in a header.
import { api, APIError, type WorkNode } from './api.ts'

export type DraftLanguage = 'en' | 'de'
export interface HeldDraft { draft: string; lang: DraftLanguage | null }
export interface ImportDraft {
  status: 'ready' | 'created'
  language: DraftLanguage
  handed_over_at: string
  expires_at: string
  source?: { product: string; host: string }
  title: string
  summary: string
  facts: string[]
  open_points: string[]
  messages: number
  project?: WorkNode
  tickets?: number
}
export interface OwnerOption { id: string; name: string }
export interface DraftCreate { name: string; key: string; owner_id: string; target_date: string | null; open_points: number[] }
export interface DraftCreated { project: WorkNode; tickets: number; replayed: boolean }

const STORAGE = 'aeon.importDraft', HEADER = 'Aeon-Import-Draft'
const CAPABILITY = /^[A-Za-z0-9_-]{43}$/
const language = (value: unknown): DraftLanguage | null => value === 'de' || value === 'en' ? value : null

/** The capability in the assistant's address (#draft=…&lang=…, or the same as a query), validated. */
export function draftInAddress(hash: string, query: Record<string, unknown>): HeldDraft | null {
  const fragment = new URLSearchParams(hash.replace(/^#/, ''))
  const draft = fragment.get('draft') ?? (typeof query.draft === 'string' ? query.draft : null)
  if (!draft || !CAPABILITY.test(draft)) return null
  return { draft, lang: language(fragment.get('lang') ?? query.lang) }
}
export const carriesDraft = (hash: string, query: Record<string, unknown>) => /^#(draft|lang)=/.test(hash) || 'draft' in query || 'lang' in query

export function holdDraft(held: HeldDraft): void {
  try { sessionStorage.setItem(STORAGE, JSON.stringify(held)) } catch { /* Storage may be disabled: the sheet then says the handoff is gone. */ }
}
export function heldDraft(): HeldDraft | null {
  try {
    const value = JSON.parse(sessionStorage.getItem(STORAGE) ?? 'null') as Partial<HeldDraft> | null
    return value && typeof value.draft === 'string' && CAPABILITY.test(value.draft) ? { draft: value.draft, lang: language(value.lang) } : null
  } catch { return null }
}
export function dropDraft(): void {
  try { sessionStorage.removeItem(STORAGE) } catch { /* Storage may be disabled. */ }
}

async function answer<T>(response: Response): Promise<T> {
  const data = await response.json().catch(() => ({}))
  if (!response.ok) throw new APIError(response.status, typeof data?.error === 'string' ? data.error : `Request failed (${response.status})`, data && typeof data === 'object' ? data : {})
  return data as T
}
export const getImportDraft = (held: HeldDraft, signal?: AbortSignal) =>
  api('/import/project-draft', { headers: { [HEADER]: held.draft }, ...(signal ? { signal } : {}) }).then(answer<ImportDraft>)
export const createFromDraft = (held: HeldDraft, body: DraftCreate) =>
  api('/import/project-draft/create', { method: 'POST', headers: { [HEADER]: held.draft, 'Content-Type': 'application/json' }, body: JSON.stringify(body) }, 30_000).then(answer<DraftCreated>)

/** The page's language before the draft is read: the handoff's, else the browser's. */
export function draftLanguage(held: HeldDraft | null): DraftLanguage {
  return held?.lang ?? (typeof navigator !== 'undefined' && /^de\b/i.test(navigator.language) ? 'de' : 'en')
}

// The assistant's words follow the conversation's language; its content stays as sent.
export const IMPORT_COPY = {
  en: {
    title: 'New project from START', from: (host: string, when: string) => `From your conversation on ${host} · ${when}`, create: 'What you’ll create',
    name: 'Name', key: 'Key',
    keyHelp: (key: string) => key ? `Starts every ticket number: ${key}-1, ${key}-2. Suggested from the name; change it now if you like.` : 'Starts every ticket number. Suggested from the name; change it now if you like.',
    owner: 'Owner', you: 'you', date: 'Target date', optional: 'optional', noDate: 'No date', clearDate: 'Clear the target date', find: 'Find a person…', nobody: 'Nobody by that name.',
    tickets: 'Tickets', all: 'Create a ticket for each open point', count: (n: number, of: number) => n ? `${n} of ${of}` : 'none',
    along: 'What comes along', summary: 'Summary', showAll: 'Show all', showLess: 'Show less', facts: 'Facts', more: (n: number) => `+ ${n} more`, fewer: 'Show fewer',
    alongParts: (summary: boolean, facts: number, transcript: boolean) => [summary ? 'Summary' : '', facts ? `${facts} ${facts === 1 ? 'fact' : 'facts'}` : '', transcript ? 'transcript' : ''].filter(Boolean).join(', '),
    transcript: (n: number) => `Transcript attached · ${n} ${n === 1 ? 'message' : 'messages'}`,
    note: 'This becomes a knowledge entry “From the START conversation” in the new project.',
    cancel: 'Cancel', go: 'Create project', creating: 'Creating…', close: 'Close sheet',
    gone: 'This handoff has expired.', again: 'Start again from START.', toProjects: 'Go to Projects',
    denied: 'You may not create projects here.', deniedHint: 'Ask a workspace admin, or start again with someone who may.',
    readFailed: 'The handoff could not be read.', retry: 'Try again',
    failed: 'The project was not created. Try again.',
    created: (tickets: number) => tickets ? `Project created from START · ${tickets} ${tickets === 1 ? 'ticket' : 'tickets'}` : 'Project created from START',
  },
  de: {
    title: 'Neues Projekt aus START', from: (host: string, when: string) => `Aus deinem Gespräch auf ${host} · ${when}`, create: 'Was du anlegst',
    name: 'Name', key: 'Kürzel',
    keyHelp: (key: string) => key ? `Beginnt jede Ticketnummer: ${key}-1, ${key}-2. Aus dem Namen vorgeschlagen; ändere es jetzt, wenn du magst.` : 'Beginnt jede Ticketnummer. Aus dem Namen vorgeschlagen; ändere es jetzt, wenn du magst.',
    owner: 'Verantwortlich', you: 'du', date: 'Zieldatum', optional: 'optional', noDate: 'Kein Datum', clearDate: 'Zieldatum entfernen', find: 'Person suchen…', nobody: 'Niemand mit diesem Namen.',
    tickets: 'Tickets', all: 'Für jeden offenen Punkt ein Ticket anlegen', count: (n: number, of: number) => n ? `${n} von ${of}` : 'keine',
    along: 'Was mitkommt', summary: 'Aktuelle Einschätzung', showAll: 'Alles zeigen', showLess: 'Weniger zeigen', facts: 'Was wir konkret gesehen haben', more: (n: number) => `+ ${n} weitere`, fewer: 'Weniger zeigen',
    alongParts: (summary: boolean, facts: number, transcript: boolean) => [summary ? 'Einschätzung' : '', facts ? `${facts} ${facts === 1 ? 'Fakt' : 'Fakten'}` : '', transcript ? 'Gesprächsverlauf' : ''].filter(Boolean).join(', '),
    transcript: (n: number) => `Gesprächsverlauf angehängt · ${n} ${n === 1 ? 'Nachricht' : 'Nachrichten'}`,
    note: 'Daraus wird im neuen Projekt der Wissenseintrag „Aus dem START-Gespräch“.',
    cancel: 'Abbrechen', go: 'Projekt anlegen', creating: 'Wird angelegt…', close: 'Schließen',
    gone: 'Diese Übergabe ist abgelaufen.', again: 'Starte noch einmal in START.', toProjects: 'Zu den Projekten',
    denied: 'Du darfst hier keine Projekte anlegen.', deniedHint: 'Frag eine Admin-Person im Arbeitsbereich oder starte noch einmal mit jemandem, der es darf.',
    readFailed: 'Die Übergabe konnte nicht gelesen werden.', retry: 'Noch einmal',
    failed: 'Das Projekt wurde nicht angelegt. Versuche es noch einmal.',
    created: (tickets: number) => tickets ? `Projekt aus START angelegt · ${tickets} ${tickets === 1 ? 'Ticket' : 'Tickets'}` : 'Projekt aus START angelegt',
  },
} as const
export type ImportCopy = typeof IMPORT_COPY[DraftLanguage]

/** "16 Oct, 09:42" / "16. Okt., 09:42", in the reader's time zone. */
export function handedOverAt(lang: DraftLanguage, iso: string): string {
  return new Intl.DateTimeFormat(lang === 'de' ? 'de-DE' : 'en-GB', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }).format(new Date(iso))
}
/** A target date as the reader writes it: "30 Nov 2026" / "30. Nov. 2026". */
export function targetDateText(lang: DraftLanguage, value: string): string {
  const [year, month, day] = value.split('-').map(Number)
  return new Intl.DateTimeFormat(lang === 'de' ? 'de-DE' : 'en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' }).format(new Date(Date.UTC(year!, month! - 1, day!)))
}
