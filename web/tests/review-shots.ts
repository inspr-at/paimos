// SPDX-License-Identifier: AGPL-3.0-only
// Optional review screenshots. web/test-results is writable on a developer machine
// and on the Ubuntu CI runner. Pass an env override to keep a chosen directory.
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

export function reviewShots(name: string, override?: string): string {
  const dir = override && override.length > 0 ? override : join(process.cwd(), 'test-results', 'shots', name)
  mkdirSync(dir, { recursive: true })
  return dir
}
