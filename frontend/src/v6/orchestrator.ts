/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 *
 * PAI-865 — strict ordinary orchestrator projection boundary.
 */

import { api } from '@/api/client'

export interface Paimos6OrchestratorWire {
  schema_version: 1
  revision: number
  orchestrator: { display_label: string } | null
  updated_at: string | null
}

export interface Paimos6OrchestratorProjection {
  revision: number
  displayLabel: string | null
  updatedAt: string | null
}

type UnknownRecord = Record<string, unknown>

export class Paimos6OrchestratorContractError extends Error {
  constructor() {
    super('invalid Paimos 6 orchestrator projection')
    this.name = 'Paimos6OrchestratorContractError'
  }
}

function record(value: unknown): UnknownRecord | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as UnknownRecord
    : null
}

function exactKeys(value: UnknownRecord, expected: readonly string[]): boolean {
  const actual = Object.keys(value)
  return actual.length === expected.length
    && expected.every((key) => Object.prototype.hasOwnProperty.call(value, key))
}

function isUnicodeScalarString(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const unit = value.charCodeAt(index)
    if (unit >= 0xd800 && unit <= 0xdbff) {
      const next = value.charCodeAt(index + 1)
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false
      index += 1
    } else if (unit >= 0xdc00 && unit <= 0xdfff) {
      return false
    }
  }
  return true
}

function displayLabel(value: unknown): string | null {
  if (typeof value !== 'string'
    || value.length === 0
    || value !== value.trim()
    || /[\u0000-\u001f\u007f]/.test(value)
    || !isUnicodeScalarString(value)
    || new TextEncoder().encode(value).byteLength > 64) return null
  return value
}

function canonicalTimestamp(value: unknown): string | null {
  if (typeof value !== 'string' || value !== value.trim()) return null
  const match = value.match(/^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?Z$/)
  if (!match) return null
  const [, yearRaw, monthRaw, dayRaw, hourRaw, minuteRaw, secondRaw] = match
  const year = Number(yearRaw)
  const month = Number(monthRaw)
  const day = Number(dayRaw)
  const hour = Number(hourRaw)
  const minute = Number(minuteRaw)
  const second = Number(secondRaw)
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const daysInMonth = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
  if (month < 1 || month > 12
    || day < 1 || day > daysInMonth[month - 1]
    || hour > 23 || minute > 59 || second > 59) return null
  return Number.isFinite(Date.parse(value)) ? value : null
}

export function parsePaimos6Orchestrator(value: unknown): Paimos6OrchestratorWire {
  const root = record(value)
  if (!root
    || !exactKeys(root, ['schema_version', 'revision', 'orchestrator', 'updated_at'])
    || root.schema_version !== 1
    || typeof root.revision !== 'number'
    || !Number.isSafeInteger(root.revision)
    || root.revision < 0) throw new Paimos6OrchestratorContractError()

  if (root.revision === 0) {
    if (root.orchestrator !== null || root.updated_at !== null) {
      throw new Paimos6OrchestratorContractError()
    }
    return {
      schema_version: 1,
      revision: 0,
      orchestrator: null,
      updated_at: null,
    }
  }

  const updatedAt = canonicalTimestamp(root.updated_at)
  if (updatedAt === null) throw new Paimos6OrchestratorContractError()
  if (root.orchestrator === null) {
    return {
      schema_version: 1,
      revision: root.revision,
      orchestrator: null,
      updated_at: updatedAt,
    }
  }

  const orchestrator = record(root.orchestrator)
  const label = orchestrator && exactKeys(orchestrator, ['display_label'])
    ? displayLabel(orchestrator.display_label)
    : null
  if (label === null) throw new Paimos6OrchestratorContractError()
  return {
    schema_version: 1,
    revision: root.revision,
    orchestrator: { display_label: label },
    updated_at: updatedAt,
  }
}

export async function loadPaimos6Orchestrator(signal?: AbortSignal): Promise<Paimos6OrchestratorProjection> {
  const wire = parsePaimos6Orchestrator(await api.get<unknown>('/orchestrator/v1', { signal }))
  return {
    revision: wire.revision,
    displayLabel: wire.orchestrator?.display_label ?? null,
    updatedAt: wire.updated_at,
  }
}
