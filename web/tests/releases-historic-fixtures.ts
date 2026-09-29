// SPDX-License-Identifier: AGPL-3.0-only
// Releases 102 and 105 as /api/releases serves them (AEON-305). The commits are
// the real ones (git log --no-merges between the tags). The server has grouped
// them and linked each visible ticket's pill and benefit; bug-tagged tickets
// are fixes. Release 102's notes were backfilled after the release (a
// release_manifest_note_snapshots row, labelled backfilled); 105 has no capture,
// so it carries the historical-tag-headline fallback.
import type { History } from './releases-fixtures'

type Note = { key: string; pill_en: string; pill_de: string; benefit_en: string; benefit_de: string }
const note = (key: string, pill_en: string, pill_de: string, benefit_en: string, benefit_de: string): Note => ({ key, pill_en, pill_de, benefit_en, benefit_de })
// Ticket text as written at Done; hidden tickets have none.
export const NOTES: Record<string, Note> = {
  'AEON-211': note('AEON-211', 'Deploy target on screen', 'Deploy-Ziel im Blick', 'Every deploy approval names the server it goes to, so nothing is approved blind.', 'Jede Deploy-Freigabe nennt den Zielserver, damit nichts blind freigegeben wird.'),
  'AEON-218': note('AEON-218', 'Rate agent work', 'Agentenarbeit bewerten', 'Rate a delivery from 1 to 5 with tags and a comment, or send it back for rework.', 'Bewerte eine Lieferung von 1 bis 5 mit Tags und Kommentar oder schicke sie zur Nacharbeit zurück.'),
  'AEON-219': note('AEON-219', 'Instruction provenance', 'Herkunft der Anweisungen', 'Each session records which AGENTS.md and rule versions it ran with.', 'Jede Sitzung hält fest, mit welchen AGENTS.md- und Regelversionen sie lief.'),
  'AEON-249': note('AEON-249', 'Merged rules at start', 'Zusammengeführte Regeln beim Start', 'Sessions start with one merged rule file, and a stale cache is marked instead of trusted.', 'Sitzungen starten mit einer zusammengeführten Regeldatei; ein veralteter Cache wird markiert statt verwendet.'),
  'AEON-276': note('AEON-276', 'Read on every device', 'Gelesen auf allen Geräten', 'What you have read in a session chat stays read on your other devices.', 'Was du in einem Sitzungschat gelesen hast, bleibt auf deinen anderen Geräten gelesen.'),
  'AEON-293': note('AEON-293', 'Readable risk chip', 'Lesbarer Risiko-Chip', 'The high-risk chip on the deploy journey is easy to read in light mode.', 'Der Hochrisiko-Chip in der Deploy-Journey ist im hellen Modus gut lesbar.'),
  'AEON-291': note('AEON-291', 'Dead sessions tidy up', 'Tote Sitzungen räumen auf', 'Sessions whose agent is gone end on their own, and the session menu offers only what works.', 'Sitzungen ohne Agent enden von selbst, und das Sitzungsmenü bietet nur, was funktioniert.'),
  'AEON-251': note('AEON-251', 'Rules shadow check', 'Regel-Schattenvergleich', 'Aeon compares the instructions an agent loaded with the merged rule file and shows where they differ.', 'Aeon vergleicht die geladenen Anweisungen eines Agenten mit der zusammengeführten Regeldatei und zeigt Abweichungen.'),
  'AEON-302': note('AEON-302', 'Honest project counts', 'Richtige Projektzahlen', 'Project cards count open and blocked tickets too, so the numbers match the list.', 'Projektkarten zählen auch offene und blockierte Tickets, damit die Zahlen zur Liste passen.'),
  'AEON-303': note('AEON-303', 'Clear Done check', 'Klare Done-Prüfung', 'Moving a ticket to Done without release-note texts asks for them instead of showing a raw error.', 'Wer ein Ticket ohne Release-Note-Texte auf Done setzt, wird danach gefragt, statt einen rohen Fehler zu sehen.'),
  'AEON-304': note('AEON-304', 'Session list on phones', 'Sitzungsliste am Handy', 'The agents session list fits phones: no floating time column and no orphan menu row.', 'Die Sitzungsliste passt aufs Handy: keine schwebende Zeitspalte, keine verwaiste Menüzeile.'),
}
// Bug-tagged tickets (AEON-293, 302, 303, 304) group as fixes; AEON-278 is
// hidden and AEON-285 is still open without text, so theirs stay other.
const BUGS = new Set(['AEON-293', 'AEON-302', 'AEON-303', 'AEON-304'])

// [commit, subject, authored at], newest first as the manifest lists them.
const STABLE102: [string, string, string][] = [
  ["0267ed0711c2314405aa68f0bb4a7f3d9ac4e55c", "AEON-211: reserve stable102 deploy targets on screen, provenance, votes, read markers, rules cap, pin artifacts, contrast", "2026-09-29T10:22:13+02:00"],
  ["7f04c207a09f979cb7566f9531fcb66bb2dcac5d", "AEON-278: quote flow descriptions merged in from 258, 218, 219 and 276", "2026-09-29T10:18:15+02:00"],
  ["7ea11e6a164a33fad3690d899a9fac9e9ee08d3c", "AEON-211: regenerate contract pins after merging 278 quoting with journey/1.3", "2026-09-29T10:01:35+02:00"],
  ["613d94ea192f756ac49c6e7b198c82e60c3aa286", "P0.x: offer Needs rework only on stopped ticket sessions (AEON-218)", "2026-09-29T09:56:40+02:00"],
  ["17608e52ed9308fb8318e3b53bdf1d1967987a4d", "P0.x: keep rework focus in the form and off live sessions (AEON-218)", "2026-09-29T09:47:59+02:00"],
  ["2c36db84b01b64f3baac943d8fce215949a8aacb", "P0.x: let a project guest rate a visible delivery (AEON-218)", "2026-09-29T09:47:56+02:00"],
  ["8f2aad45ff4605bb91560f87031659821837530c", "P0.x: quote remaining harness OpenAPI flow descriptions (AEON-278)", "2026-09-29T09:46:51+02:00"],
  ["e9c460bfea447621ea59142af57ab53be0032578", "P0.x: preserve strict handoff bytes with optional deployment targets (AEON-211)", "2026-09-29T09:34:26+02:00"],
  ["6edc625b2f6f74954401cf6c7b9ea51d9dcd803e", "P0.x: lift high-risk chip contrast on the deploy journey (AEON-293)", "2026-09-29T09:30:23+02:00"],
  ["72c874b1febbbafa00e9823eb495062f8f9f633d", "P0.x: mark a delivery for rework by exception (AEON-218)", "2026-09-29T09:21:52+02:00"],
  ["bb7b8a27b5ef58649a2f55fac30c2af54d2e4761", "P0.x: quote OpenAPI flow descriptions so contract pins drop parse artifacts (AEON-278)", "2026-09-29T09:15:59+02:00"],
  ["9e0e059c97c1b3f2dfcce7e5eedf6735b7979dc1", "P0.x: attribute served rule sets and keep partial provenance (AEON-219)", "2026-09-29T09:14:55+02:00"],
  ["c00655882bdd713a2c19cddbff318e9692761742", "P0.x: let harness.read agents query instruction provenance (AEON-219)", "2026-09-29T09:14:50+02:00"],
  ["afb53e7406e46cf26b07a3cffab5263450dcaa4b", "P0.x: coalesce session read markers and refresh them on focus (AEON-276)", "2026-09-29T09:14:11+02:00"],
  ["2fb0e9fe48a7becc7c2d09f9d0edb3e660b700b1", "P0.x: cascade session read markers and reject foreign events (AEON-276)", "2026-09-29T09:14:08+02:00"],
  ["8d667d61a20e505b3597ec50ea57effda0e57047", "P0.x: show optional deployment targets without tightening approvals (AEON-211)", "2026-09-29T09:03:21+02:00"],
  ["3d1a4e399efb196807ac18c90a77508a7785c3d2", "P0.x: stop reading rule snapshots once a merged file crosses its store cap (AEON-249)", "2026-09-29T09:01:54+02:00"],
  ["376bf7617888383422a0fa8b7e4961ab8a1345dc", "P0.x: rate agent deliveries (AEON-218)", "2026-09-29T08:50:25+02:00"],
  ["4b70819c9596697158aa289ac7347a145bbc245b", "P0.x: mark an unreachable rules cache stale and cap merged loads (AEON-249)", "2026-09-29T08:42:46+02:00"],
  ["4fb3b7d6d912258d740b262d8caa6bbc9b75fc12", "P0.x: record instruction provenance from receipts and heartbeat (AEON-219)", "2026-09-29T08:40:46+02:00"],
  ["9351ce733a05bd5409970ec727b3b0c69d174dcc", "P0.x: server-side per-person session read marker (AEON-276)", "2026-09-29T08:28:55+02:00"],
]
const STABLE105: [string, string, string][] = [
  ["5a622e28b5c16ecabe45449a14233f1f13dc3718", "AEON-291: reserve stable105 dead sessions, phone session list, done dialog, project counts, shadow comparison, signed agentd", "2026-09-29T13:38:54+02:00"],
  ["0fe0ef07a25200ef4330b2a398da663fd55b2538", "AEON-304: phone state line shows the listening cue as its icon", "2026-09-29T13:38:23+02:00"],
  ["9682a8ebee314eea444ffd987a0d224a2e97bac3", "AEON-304: keep the listening cue under the state label on phones", "2026-09-29T13:10:37+02:00"],
  ["c840955e5c98ad57f9464aa3e2f205bdac098c94", "AEON-251: renumber rules comparison gaps migration to 0937 (release 8 takes 0935)", "2026-09-29T11:44:23+02:00"],
  ["12059dde216c9127592d4d43bbcb47e40637eb89", "P0.x: fail signing cleanup on keychain search-list query errors; assert both path guards first (AEON-285)", "2026-09-29T11:29:50+02:00"],
  ["4856a16969d80a415641ee47d07eeae8988597a9", "P0.11: keep node 422 bodies compatible with ApiError (AEON-303)", "2026-09-29T11:25:42+02:00"],
  ["0cbcc3caa154c08cece2607ecf6e64931e761bf8", "P0.x: harden signing cleanup; parse the release workflow in the guard test (AEON-285)", "2026-09-29T11:24:17+02:00"],
  ["14d5ad7bb54367b591c537c5dcf67ee7c21840b7", "P0.x: keep unknown states last and rank normalised spellings (AEON-302)", "2026-09-29T11:22:16+02:00"],
  ["b80f4d3fd78b56fb4e4d66e9f5b315a97d16b809", "P0.x: always remove the signing keychain; guard every workflow and the tag-only trigger (AEON-285)", "2026-09-29T11:17:50+02:00"],
  ["b686f35a9c2043414a9a938fc0502a9644485945", "P0.10: finish the done-gate review (AEON-303)", "2026-09-29T11:16:45+02:00"],
  ["854c59456f557ac242e3f6742db423b90a720c02", "P0.x: parse ordinary Claude imports and cap Codex on raw bytes (AEON-251)", "2026-09-29T11:15:41+02:00"],
  ["d13989118ce52ad5394793dd881c4b52953e99e7", "P0.x: sort status in workflow order (AEON-302)", "2026-09-29T11:09:48+02:00"],
  ["2082075ffeab0ee1912f5f4577f5fe2c8f44ab6b", "P0.x: count progress out of open, doing and done (AEON-302)", "2026-09-29T11:09:48+02:00"],
  ["0959b887c39cf6430b1a7dea87a4099fb9eb3c15", "P0.x: keep local conflicts off the merged presence count (AEON-251)", "2026-09-29T10:53:02+02:00"],
  ["c336c0960b11b914e785b01cc33fbda218cdeb9c", "P0.x: control rights per session project, shared by row and panel (AEON-291)", "2026-09-29T10:52:45+02:00"],
  ["28a50e924b0cfea774cb2649d137ee6ce9e1acd6", "P0.x: keep a lead's 44 px worker toggles inside its row on phones (AEON-304)", "2026-09-29T10:52:26+02:00"],
  ["bcae6a2713c683af1b1abe4bad680fa1004d6d56", "P0.x: apply Codex child-over-root precedence before the diff (AEON-251)", "2026-09-29T10:48:20+02:00"],
  ["84c229b43ab1cc359c562307edfde9fbaf0988e8", "P0.x: compare the Codex instruction prefix inside the byte cap (AEON-251)", "2026-09-29T10:47:12+02:00"],
  ["174f820cb142a3fcee8846ecb5fd85e169c3cd08", "P0.x: discover CLAUDE.local.md before the AGENTS.md fallback (AEON-251)", "2026-09-29T10:46:15+02:00"],
  ["bb7c85f4324d5f262d234dc19b5d9be462e03570", "P0.x: compact phone session rows, inline heartbeat, tree through avatars (AEON-304)", "2026-09-29T10:45:41+02:00"],
  ["0b79a6bfa35e4d7a48eba3dc9abaec10c3ff2f57", "P0.x: follow bounded Claude imports and upload the omission (AEON-251)", "2026-09-29T10:45:03+02:00"],
  ["85f9f363b04cc86108968d4af27552b4b5e26b67", "P0.9: ask for the user benefit before Done (AEON-303)", "2026-09-29T10:43:28+02:00"],
  ["0d912212a5dfe8e5585e5f2795d7f9b2b3a1699e", "P0.x: one managed-control eligibility for row and panel (AEON-291)", "2026-09-29T10:43:24+02:00"],
  ["f56b6b37ba9bdf0570ab5f1137427a2d7f4d398e", "P0.x: hide closed work with the same status buckets as the counts (AEON-302)", "2026-09-29T10:38:06+02:00"],
  ["3017fa7f970d3f1ec94c5d3b3c2bcf09bde1ee05", "P0.x: sign and notarize darwin paimos-agentd in the release workflow (AEON-285)", "2026-09-29T10:34:13+02:00"],
  ["477b272415c6dcdc515c0d4882ce1002900cda4f", "P0.x: bind Mac confirmation to the expected Developer ID team (AEON-285)", "2026-09-29T10:34:05+02:00"],
  ["e0cddeec5ade85cdb839a0291ebb273b30ac8d78", "P0.x: honest Undo, History paging, 44 px toast targets (AEON-291)", "2026-09-29T10:28:20+02:00"],
  ["5b58cf61904bb7e50c199c9e0c79ee318e0d5d61", "P0.x: row Interrupt/Stop use managed controls for managed_control_v1 (AEON-291)", "2026-09-29T10:25:51+02:00"],
  ["8b2d6a5ec50e37ce4186f497065eef3c39db56c5", "P0.x: review fixes: revive fence, legacy cursors, undoable (AEON-291)", "2026-09-29T10:24:22+02:00"],
  ["ed8404825d47ec30f9f98ef83305930c71f558c6", "P0.x: show watch-off copy only for Mac confirmation (AEON-285)", "2026-09-29T10:24:17+02:00"],
  ["b714baf4b45d5a3d7b566d73d7b672da3262c881", "P0.x: pin release artifact actions to v4 commits (AEON-285)", "2026-09-29T10:24:17+02:00"],
  ["90a5d8657ae7f826f584bfaf5bd2baa859331a8b", "P0.x: accept installed paimos-agentd and aeon-agentd names (AEON-285)", "2026-09-29T10:24:10+02:00"],
  ["a8d707fb4a38c1022b77ac7a133fe281f50cbf1c", "P0.x: show open and blocked as their own states (AEON-302)", "2026-09-29T10:21:48+02:00"],
  ["71a8ca710d0a068d69eee23d19a72e7496f9424e", "P0.x: count open, blocked and unknown work on project cards (AEON-302)", "2026-09-29T10:21:30+02:00"],
  ["77cc56f7a665ae09d152dc567044709789e3b6d7", "P0.x: mirror remove event_id into the harness module contract (AEON-291)", "2026-09-29T10:12:16+02:00"],
  ["ff8a9e8cc612ee15ef496cb8cd8f04506a687fa4", "P0.x: compare loaded harness instructions with merged rules (AEON-251)", "2026-09-29T10:11:52+02:00"],
  ["405be461bf860a786d58715d848de79912eb3f94", "P0.x: session menu offers only what works; bin, undo and History (AEON-291)", "2026-09-29T09:56:53+02:00"],
  ["04aa12ba2ee9d0b961ab6d16aa1090e8b4d0d86b", "P0.x: build darwin agentd with LocalAuthentication and report capability (AEON-285)", "2026-09-29T09:52:22+02:00"],
  ["29b526da96f13cf32c5e1548d6a837f78a7baa1c", "P0.x: lost contact is an ended session, not a problem (AEON-291)", "2026-09-29T09:35:53+02:00"],
  ["9dbc1de8be535ad232fe84e449404e4a4ef8dd98", "P0.x: lost-contact sweeper, revive, 24 h view and removal undo (AEON-291)", "2026-09-29T09:31:52+02:00"],
]

const KEY = /\b[A-Z][A-Z0-9]{1,9}-[1-9]\d{0,6}\b/g
// What the server adds when it serves the history: the group and linked notes.
function served([commit, subject, at]: [string, string, string]) {
  const tickets = [...new Set(subject.match(KEY) ?? [])]
  const group = tickets.some(t => BUGS.has(t)) ? 'fixes' : tickets.some(t => NOTES[t]) ? 'features' : 'other'
  const linked = group === 'other' ? [] : tickets.filter(t => NOTES[t]).map(t => NOTES[t]!)
  return { commit, subject, type: 'other', scope: '', tickets, at: new Date(at).toISOString(), group, ...(linked.length ? { linked_tickets: linked } : {}) }
}
const evidence = (repository: string, version: string, commit: string) => ({
  source_commit: commit, source_url: `https://github.com/${repository}/commit/${commit}`,
  image: { reference: `ghcr.io/${repository}:${version}`, digest: `sha256:${commit}${commit.slice(0, 24)}` },
  ci: null, release_run: null, release_url: `https://github.com/${repository}/releases/tag/v${version}`, unavailable: [],
})

export function historicHistory(repository = 'inspr-at/aeon'): History {
  const v105 = '260929113854.0.0', v102 = '260929082208.0.0'
  const release105 = {
    version: v105, tag: `v${v105}`, release_channel: 'stable', release_sequence: 105, state: 'published',
    reserved_at: '2026-09-29T11:38:54Z', tagged_at: '2026-09-29T11:52:33Z', published_at: '2026-09-29T12:01:10Z',
    headline: `release: v${v105} (AEON-291)`, tickets: ['AEON-291'], changes: STABLE105.map(served), changes_omitted: 0,
    evidence: evidence(repository, v105, STABLE105[0]![0]),
    notes: { source: 'unavailable', fallback: 'historical-tag-headline', snapshot_sha256: '', captured_at: null, release_revision: 0, items: [], hidden: 0,
      gaps: ['Release membership and bilingual ticket fields were not captured. Git mentions do not establish release membership.'] },
  }
  const members = ['AEON-211', 'AEON-218', 'AEON-219', 'AEON-249', 'AEON-276', 'AEON-293']
  const release102 = {
    version: v102, tag: `v${v102}`, release_channel: 'stable', release_sequence: 102, state: 'published',
    reserved_at: '2026-09-29T08:22:08Z', tagged_at: '2026-09-29T08:31:11Z', published_at: '2026-09-29T08:39:40Z',
    headline: 'stable102', tickets: [], changes: STABLE102.map(served), changes_omitted: 0,
    evidence: evidence(repository, v102, STABLE102[0]![0]),
    notes: { source: 'database-snapshot', snapshot_sha256: 'c1'.repeat(32), captured_at: '2026-09-29T12:40:00Z', release_revision: 1, written_after_release: true,
      items: members.map((key, i) => ({ id: `00000000-0000-4000-8000-${String(i + 1).padStart(12, '0')}`, ...NOTES[key]! })), gaps: [], hidden: 1 },
  }
  return {
    schema: 'inspr.release-history.v1', product: 'PAIMOS AEON', repository, version_scheme: 'inspr-calendar-v2', generated_at: '2026-09-29T11:52:40Z',
    source: 'git+github', current: v105, live_since: '2026-09-29T12:03:00Z', releases: [release105, release102],
  } as unknown as History
}
