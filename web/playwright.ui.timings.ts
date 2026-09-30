// SPDX-License-Identifier: AGPL-3.0-only
import { relative } from 'node:path'
import type { FullConfig, Reporter, TestCase, TestResult } from '@playwright/test/reporter'

// CI logs retain measured durations, including quarantine retries, so the next
// balance uses actual single-worker time rather than expanded declaration counts.
export default class Timings implements Reporter {
  private root = ''
  private durations = new Map<string, number>()
  onBegin(config: FullConfig) { this.root = config.rootDir }
  onTestEnd(test: TestCase, result: TestResult) {
    const file = relative(this.root, test.location.file).replaceAll('\\', '/')
    const key = file === 'ui-audit.spec.ts' ? `${file}::${test.title}` : file
    this.durations.set(key, (this.durations.get(key) ?? 0) + result.duration)
  }
  onEnd() {
    console.log(`AEON_UI_TIMINGS ${JSON.stringify(Object.fromEntries([...this.durations].sort()))}`)
  }
}
