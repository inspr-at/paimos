/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 */

import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

import { isUsableFlowState } from './flowHost'

const FLOW_TARBALL_URL =
  'https://github.com/inspr-at/flow-shell/releases/download/v0.1.3/inspr-flow-shell-0.1.3.tgz'
const FLOW_TARBALL_SHA256 = '7f8ae73a31785ebff4029fc451c62918c4a1a6bccdba40f016c0749317810d67'
const frontendRoot = join(dirname(fileURLToPath(import.meta.url)), '../..')

describe('Flow host packaging', () => {
  it('pins public Flow 0.1.3 and the verified GitHub tarball digest', () => {
    const pkg = JSON.parse(readFileSync(join(frontendRoot, 'package.json'), 'utf8')) as {
      engines?: { node?: string }
      version: string
      dependencies: Record<string, string>
    }
    const lock = JSON.parse(readFileSync(join(frontendRoot, 'package-lock.json'), 'utf8')) as {
      packages: Record<string, { version?: string; resolved?: string; integrity?: string }>
    }
    expect(pkg.version).toBe('1.0.0')
    expect(pkg.engines?.node).toBe('>=24')
    expect(pkg.dependencies['@inspr/flow-shell']).toBe(FLOW_TARBALL_URL)
    const entry = lock.packages['node_modules/@inspr/flow-shell']
    expect(entry.version).toBe('0.1.3')
    expect(entry.resolved).toBe(FLOW_TARBALL_URL)
    expect(entry.integrity).toMatch(/^sha512-/)
    const hex = Buffer.from(entry.integrity!.slice('sha512-'.length), 'base64').toString('hex')
    const cache = execFileSync('npm', ['config', 'get', 'cache'], { encoding: 'utf8' }).trim()
    const cached = join(cache, '_cacache', 'content-v2', 'sha512', hex.slice(0, 2), hex.slice(2, 4), hex.slice(4))
    expect(existsSync(cached)).toBe(true)
    expect(createHash('sha256').update(readFileSync(cached)).digest('hex')).toBe(FLOW_TARBALL_SHA256)
  })

  it('rejects a frontend package placeholder as Flow identity', () => {
    expect(
      isUsableFlowState({
        header: { version: '1.0.0' },
        identityContext: { host_id: 'workspace', principal_kind: 'oidc_backed' },
      }),
    ).toBe(false)
  })
})
