// SPDX-License-Identifier: AGPL-3.0-only
import { pathToFileURL } from 'node:url'
await import(pathToFileURL(process.argv[2]).href)
process.stdout.write(JSON.stringify(globalThis.__tierCases) + '\n')
