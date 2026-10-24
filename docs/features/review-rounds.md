<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Review rounds until ok

Delivery › Numbers shows the mean design rounds until the first recorded `ok`, with the gate mean alongside it. A ticket/kind without an `ok` stays open and never contributes a one-round success. Tickets with no review events contribute nothing. The tile follows Escaped defects; Expert also provides a weekly trend and a detail panel with the tickets needing most rounds and the rules cited most. Simple shows the value and its change only. The goal of 1.0 is a reference, independent of Arion targets. No reviewer, builder or model identity appears in the report.

`POST /api/outcomes/review-rounds` records an observation. It uses the existing `outcome.write` permission in the current ticket project, checked under tenant/tree fences. Agents must supply their own submitted authenticated `review_run_id`, matching the ticket, design/visual or gate/review kind, artifact head SHA and verdict. A person with that permission may record design artifacts and historical observations. Recording a number supplies no approval, review credential, merge permission or delivery authority.

```sh
aeon outcome review-round --ticket AEON-123 --kind design --round 2 \
  --verdict ok --at 2026-10-24T08:00:00Z --artifact <head-sha> \
  --reviewer-role design_reviewer --cited-rules U2,A4 \
  --review-run <submitted-run-uuid> --idempotency-key design-aeon-123-round-2
```

One immutable observation exists per tenant, ticket, kind and round. Identical requests replay even with another retry key or ticket alias; conflicting observations return 409. The append-only `outcome_review_rounds` extension and the common tenant event log (`outcome.review_round`) commit together. This expansion leaves the released AEON-286 table and its kind allowlist untouched. Reads use `GET /api/projects/{projectId}/delivery/review-rounds?window=7` with project `outcome.read`; no identity columns are returned.

The 7/30/90/180/365-day UTC calendar windows end at the reading time today. A completed kind is selected in the window containing its first-ok observation; an open kind is selected by its latest observation. The first `ok` is the lowest successful round, across lifetime history, so a first round before the selected window is retained. Each weekly mean follows the same rule. Ticket counts are distinct across both kinds; a ticket with an open selected kind is counted once as still open. Coverage starts with the earliest recorded review observation. Rules count once per cited rule per observation, with the previous window count available as a rising-count hint. Reads refuse more than 50000 events or a five-second deadline with 503 instead of publishing partial figures.

For the one-off backfill, use only the copy supplied for the merge:

```sh
aeon outcome import-design-rounds --file <supplied-copy.tsv> --dry-run
aeon outcome import-design-rounds --file <supplied-copy.tsv>
```

The TSV header is `key`, `repo`, `first look`, `rounds until ok`, `note`, separated by tabs. The importer validates the entire file before sending requests. It accepts at most 1 MiB, 1000 rows and 10000 outcomes, and requires a person with `outcome.write`. Positive rounds produce `changes` for each preceding round and `ok` for the final round. First look accepts RFC3339 or a UTC date; all generated rounds retain that timestamp because no later timestamps are present in the source. Rule IDs in notes are cited once, on the first observation; prose and names are discarded. Identical duplicate rows are coalesced; conflicting rows fail validation. A failed apply reports the completed prefix and can be retried safely. Backfill remains explicit; no home-directory source is read by the product.

The lens tooltip texts are a bundled snapshot of the AEON-1714 UI and accessibility lenses with German translations. Unknown future IDs say that their text is unavailable. Rule chips are neutral, keyboard reachable and link nowhere. The prepared German catalog follows the shared app language policy; the current application remains English (AEON-1609). A test-only component fixture exercises German layout without adding a product route or changing that policy. The UI follows the binding Tick specification and existing components; no attached Opus HTML was supplied (needs Opus design).
