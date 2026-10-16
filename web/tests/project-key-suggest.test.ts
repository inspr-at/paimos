// SPDX-License-Identifier: AGPL-3.0-only
// Risk (AEON-1441): umlauts split German words, so the suggested key took
// letters that are not word starts ("für" became F and R). Both the START
// import and the ordinary New project sheet use this suggestion.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { projectKeyProblem, suggestProjectKey } from '../src/lib/projectSettings.ts'

test('German letters are spelt out before the key is suggested', () => {
  for (const [name, key] of [
    ['Vorbestell-App für die Bäckerei', 'VAFDB'],
    ['Übergabe Öffnungszeiten Straße', 'UOS'],
    ['Grüße', 'GRU'],
    ['Bär', 'BAER'],
    ['Website relaunch', 'WEB'],
    ['Kundenportal', 'KUN'],
    ['Plattform für gemeinsame Projektverwaltung und verlässliche Zusammenarbeit', 'PFGPUV'],
  ]) {
    assert.equal(suggestProjectKey(name), key, name)
    assert.equal(projectKeyProblem(suggestProjectKey(name)), '', name)
  }
  assert.equal(suggestProjectKey('  '), '')
})
