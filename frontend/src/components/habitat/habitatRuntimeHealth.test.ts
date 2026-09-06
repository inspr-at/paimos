import { describe, expect, it } from 'vitest'
import { parseRuntimeHealth } from './habitatRuntimeHealth'
const uuid = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`
const page = () => ({
  schema_version: 1,
  observed_at: '2026-09-06T10:00:00Z',
  runtimes: [
    {
      runtime_id: uuid(1),
      runtime_generation: uuid(2),
      machine_id: 'test-machine',
      expires_at: '2026-09-06T10:02:00Z',
      status: 'fresh',
      layers: ['reporter', 'primary', 'fallback', 'attention'].map((layer) => ({
        layer,
        state: 'healthy',
        status: 'fresh',
        reason: 'recovered',
        failure_count: 0,
        updated_at: '2026-09-06T10:00:00Z',
      })),
    },
  ],
})
describe('Runtime health boundary', () => {
  it('keeps fresh, stale, and offline health separate from missing reports', () => {
    const report = page()
    expect(parseRuntimeHealth(report).runtimes[0].layers[0].status).toBe('fresh')
    report.runtimes[0].layers[0].status = 'stale'
    expect(parseRuntimeHealth(report).runtimes[0].layers[0].status).toBe('stale')
    report.runtimes[0].status = 'offline'
    report.runtimes[0].layers = report.runtimes[0].layers.map((row) => ({
      ...row,
      status: 'offline',
    }))
    expect(parseRuntimeHealth(report).runtimes[0].status).toBe('offline')
    const missing = {
      ...report,
      runtimes: [
        {
          ...report.runtimes[0],
          layers: report.runtimes[0].layers.map(({ updated_at: _updated, ...row }) => ({
            ...row,
            state: 'unknown',
            reason: 'not_reported',
          })),
        },
      ],
    }
    expect(parseRuntimeHealth(missing).runtimes[0].layers[0].state).toBe('unknown')
  })
  it('rejects extra/private fields, duplicate layers, unknown states, and invented healthy reports', () => {
    const valid = page()
    for (const layer of [
      { ...valid.runtimes[0].layers[0], private_path: '/fixture-canary' },
      { ...valid.runtimes[0].layers[0], state: 'ready' },
      { ...valid.runtimes[0].layers[0], failure_count: 11 },
      { ...valid.runtimes[0].layers[0], reason: 'not_reported' },
      { ...valid.runtimes[0].layers[0], updated_at: null },
    ])
      expect(() =>
        parseRuntimeHealth({
          ...valid,
          runtimes: [
            { ...valid.runtimes[0], layers: [layer, ...valid.runtimes[0].layers.slice(1)] },
          ],
        }),
      ).toThrow()
    expect(() =>
      parseRuntimeHealth({ ...valid, runtimes: [valid.runtimes[0], valid.runtimes[0]] }),
    ).toThrow()
    expect(() =>
      parseRuntimeHealth({
        ...valid,
        runtimes: [{ ...valid.runtimes[0], layers: Array(4).fill(valid.runtimes[0].layers[0]) }],
      }),
    ).toThrow()
  })
})
