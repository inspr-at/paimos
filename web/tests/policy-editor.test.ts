// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { policyJSON } from '../src/lib/policyEditor.ts'

test('response byte bounds precede JSON parsing',async()=>{
 await assert.rejects(policyJSON(new Response('"'+'x'.repeat(32)+'"'),8),/exceeds the editor limit/)
 assert.deepEqual(await policyJSON(new Response('{"ok":true}'),64),{ok:true})
})
