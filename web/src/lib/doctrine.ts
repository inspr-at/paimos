// SPDX-License-Identifier: AGPL-3.0-only
// The INSPR doctrine as a git-backed, read-only rule layer (AEON-318). Git is
// the source of truth: Aeon shows each configured repository at its pinned
// commit and never edits it. Every rule's text is the exact bytes of its lines
// at that commit and links there; TL;DRs are read from git, never written here.
import { api, RequestFailure } from './api.ts'
import { sessionGone } from './authz.ts'

export interface DoctrineTldr { en: string; de?: string; check?: boolean }
export interface DoctrineRule {
  key: string
  identity: string
  set: string
  heading_path: string
  anchor: string
  start_line: number
  end_line: number
  /** The exact bytes of lines start_line..end_line at the pinned commit. */
  source: string
  sha256: string
  text: string
  strength: 'normal' | 'locked'
  url: string
  tldr?: DoctrineTldr
}
export interface DoctrineSet { set: string; title: string; anchor: string; tldr?: DoctrineTldr }
export interface DoctrineSidecar { path: string; url: string; problem?: string; unmatched?: string[] }
export interface DoctrineFile {
  path: string
  blob_sha: string
  sha256: string
  bytes: number
  kind: string
  layer: string
  url: string
  tldr?: DoctrineTldr
  sets: DoctrineSet[]
  rules: DoctrineRule[]
  sidecar?: DoctrineSidecar
  problem?: string
}
export interface DoctrineSkip { path: string; reason: string }
export type DoctrineState = 'ready' | 'not_indexed' | 'failed'
export interface DoctrineSource {
  id: string
  repository: string
  visibility: 'public' | 'private'
  ref?: string
  commit: string
  committed_at?: string
  pinned_at: string
  paths: string[]
  credential_ref?: string
  url: string
  state: DoctrineState
  error?: string
  indexed_at?: string
  files: DoctrineFile[]
  skipped: DoctrineSkip[]
}
export interface DoctrineLayer { sources: DoctrineSource[] }
export interface DoctrineSourceInput {
  repository?: string
  visibility: 'public' | 'private'
  ref?: string
  commit?: string
  paths?: string[]
  credential_ref?: string
}

export class DoctrineError extends Error {
  readonly status: number
  readonly code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

async function send(path: string, method = 'GET', body?: unknown): Promise<DoctrineLayer> {
  const response = await api(path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (response.status === 401) sessionGone()
  if (!response.ok) {
    const failure = await response.json().catch(() => ({})) as { error?: string; code?: string }
    throw new DoctrineError(response.status, failure.code ?? '', failure.error || `Request failed (${response.status})`)
  }
  const layer = await response.json() as DoctrineLayer
  return { sources: layer.sources ?? [] }
}

export const getDoctrine = () => send('/rules/doctrine')
export const addDoctrineSource = (input: DoctrineSourceInput) => send('/rules/doctrine/sources', 'POST', input)
export const pinDoctrineSource = (id: string, input: DoctrineSourceInput) => send(`/rules/doctrine/sources/${encodeURIComponent(id)}`, 'PUT', input)
export const removeDoctrineSource = (id: string) => send(`/rules/doctrine/sources/${encodeURIComponent(id)}`, 'DELETE')
export const indexDoctrineSource = (id: string) => send(`/rules/doctrine/sources/${encodeURIComponent(id)}/index`, 'POST')

/** What a failed doctrine request tells a person. A write that timed out may still land. */
export function doctrineMessage(error: unknown): string {
  if (error instanceof RequestFailure && error.kind === 'timeout') return 'Still reading the repository. Reload in a moment to see the result.'
  if (error instanceof DoctrineError) {
    if (error.code === 'forbidden') return 'You do not have permission for that.'
    return error.message
  }
  return error instanceof Error ? error.message : 'The doctrine request failed.'
}

// ---------- Display helpers ----------

export const shortSha = (sha: string) => sha.slice(0, 7)
export const repoName = (repository: string) => repository.slice(repository.indexOf('/') + 1)
export const fileName = (path: string) => path.slice(path.lastIndexOf('/') + 1)

const day = (value: string) => new Date(value).toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' })

/** "v260922101217.0.0 · 21b8140 · pinned since 22 Sep 2026" — the release first when there is one. */
export function pinLine(source: Pick<DoctrineSource, 'ref' | 'commit' | 'pinned_at'>): string {
  const parts = source.ref ? [source.ref, shortSha(source.commit)] : [`commit ${shortSha(source.commit)}`]
  parts.push(`pinned since ${day(source.pinned_at)}`)
  return parts.join(' · ')
}

/** "L7" or "L7–9": the lines a rule covers, as its link reads. */
export const lineLabel = (rule: Pick<DoctrineRule, 'start_line' | 'end_line'>) =>
  rule.end_line > rule.start_line ? `L${rule.start_line}–${rule.end_line}` : `L${rule.start_line}`

/** A rule's exact source without the trailing line break, which pre-wrap would show as an empty line. */
export const sourceText = (rule: Pick<DoctrineRule, 'source'>) => rule.source.replace(/(\r\n|\r|\n)$/, '')

/** The last heading of a set title: the one a reader sees above its rules. */
export function setHeading(title: string): string {
  const cut = title.lastIndexOf(' / ')
  return cut < 0 ? title : title.slice(cut + 3)
}

export interface RuleGroup { set: DoctrineSet; rules: DoctrineRule[] }
/** Rules grouped under their heading, in document order. */
export function groupRules(file: Pick<DoctrineFile, 'sets' | 'rules'>): RuleGroup[] {
  const groups = new Map<string, RuleGroup>()
  for (const set of file.sets) groups.set(set.set, { set, rules: [] })
  for (const rule of file.rules) {
    let group = groups.get(rule.set)
    if (!group) {
      group = { set: { set: rule.set, title: rule.set, anchor: rule.anchor }, rules: [] }
      groups.set(rule.set, group)
    }
    group.rules.push(rule)
  }
  return [...groups.values()].filter(group => group.rules.length)
}

/** "18 rules · 5 locked" */
export function fileSummary(file: Pick<DoctrineFile, 'rules'>): string {
  const count = file.rules.length
  const locked = file.rules.filter(rule => rule.strength === 'locked').length
  const parts = [`${count} ${count === 1 ? 'rule' : 'rules'}`]
  if (locked) parts.push(`${locked} locked`)
  return parts.join(' · ')
}

/** One line for a source that has nothing to list yet, or null when it has files. */
export function stateLine(source: Pick<DoctrineSource, 'state' | 'error' | 'files'>): string | null {
  if (source.state === 'failed') return `Could not read this commit: ${source.error || 'the repository did not answer'}`
  if (source.state === 'not_indexed') return 'Not read yet.'
  if (!source.files.length) return 'No doctrine file matches the configured paths at this commit.'
  return null
}

/** Paths as the form edits them: one pattern per line. */
export const pathsText = (paths: string[]) => paths.join('\n')
export const parsePaths = (text: string) => [...new Set(text.split(/[\n,]/).map(part => part.trim()).filter(Boolean))]

const SHA = /^[0-9a-f]{40}$/
/** The pin a person typed: a full commit SHA, or a tag or branch to resolve. */
export function pinInput(value: string): Pick<DoctrineSourceInput, 'ref' | 'commit'> {
  const pin = value.trim()
  return SHA.test(pin) ? { commit: pin } : { ref: pin }
}
