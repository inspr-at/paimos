import { describe, expect, it, vi } from 'vitest'

import { api } from '@/api/client'
import {
  loadPaimos6Orchestrator,
  parsePaimos6Orchestrator,
  Paimos6OrchestratorContractError,
} from './orchestrator'

const configured = {
  schema_version: 1,
  revision: 3,
  orchestrator: { display_label: 'aMY / Primary' },
  updated_at: '2026-08-31T12:00:00.000Z',
}

describe('Paimos 6 ordinary orchestrator projection (PAI-865)', () => {
  it('preserves the exact configured display label and accepts first-class unset', () => {
    expect(parsePaimos6Orchestrator(configured)).toEqual(configured)
    expect(parsePaimos6Orchestrator({
      schema_version: 1,
      revision: 0,
      orchestrator: null,
      updated_at: null,
    })).toEqual({
      schema_version: 1,
      revision: 0,
      orchestrator: null,
      updated_at: null,
    })
    expect(parsePaimos6Orchestrator({
      ...configured,
      orchestrator: { display_label: 'é'.repeat(32) },
    }).orchestrator?.display_label).toBe('é'.repeat(32))
  })

  it.each([
    ['extra root field', { ...configured, project_id: 42 }],
    ['unsafe revision', { ...configured, revision: Number.MAX_SAFE_INTEGER + 1 }],
    ['negative revision', { ...configured, revision: -1 }],
    ['extra identity field', { ...configured, orchestrator: { display_label: 'Amy', key: 'amy' } }],
    ['trimmed label mismatch', { ...configured, orchestrator: { display_label: ' Amy' } }],
    ['empty label', { ...configured, orchestrator: { display_label: '' } }],
    ['control label', { ...configured, orchestrator: { display_label: 'Amy\nPrimary' } }],
    ['overlong UTF-8 label', { ...configured, orchestrator: { display_label: 'é'.repeat(33) } }],
    ['invalid Unicode scalar', { ...configured, orchestrator: { display_label: '\ud800' } }],
    ['configured without timestamp', { ...configured, updated_at: null }],
    ['unset with timestamp', { ...configured, orchestrator: null }],
    ['impossible timestamp', { ...configured, updated_at: '2026-02-31T12:00:00Z' }],
    ['noncanonical timestamp', { ...configured, updated_at: '2026-08-31 12:00:00Z' }],
  ])('rejects %s', (_label, value) => {
    expect(() => parsePaimos6Orchestrator(value)).toThrow(Paimos6OrchestratorContractError)
  })

  it('loads only the ordinary same-instance UI projection endpoint', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue(configured as never)
    const controller = new AbortController()
    await expect(loadPaimos6Orchestrator(controller.signal)).resolves.toEqual({
      revision: 3,
      displayLabel: 'aMY / Primary',
      updatedAt: '2026-08-31T12:00:00.000Z',
    })
    expect(get).toHaveBeenCalledWith('/orchestrator/v1', { signal: controller.signal })
  })
})
