// SPDX-License-Identifier: AGPL-3.0-only
export function waitForHTTP(url: string, options?: {
  timeoutMs?: number
  intervalMs?: number
  fetch?: (url: string) => Promise<{ status: number }>
  sleep?: (ms: number) => Promise<void>
  now?: () => number
  deadline?: number
  kind?: 'web' | 'backend'
}): Promise<{ url: string, status: number }>

export type ProxyGate = (call: {
  connect: () => Promise<void> | void
  respond: () => Promise<void> | void
}) => Promise<'hold' | 'proxy'>

export function createBackendProxyGate(options: { check: () => Promise<boolean> }): ProxyGate

export function healthIsReady(url: string, fetchImpl?: (url: string) => Promise<{ status: number }>): Promise<boolean>

// off never dials. Every other mode returns one bounded health check per call.
export function proxyReadyCheck(
  env: Record<string, string | undefined>,
  url: string,
  fetchImpl?: (url: string) => Promise<{ status: number }>,
): () => Promise<boolean>

export function backendProxyBypass(
  gate: ProxyGate,
  req: { url?: string },
  // undefined: Vite's WebSocket upgrade calls bypass with no response.
  res: {
    writableEnded?: boolean
    statusCode: number
    setHeader: (name: string, value: string) => void
    end: (body?: string) => void
  } | undefined,
): Promise<string | undefined>
