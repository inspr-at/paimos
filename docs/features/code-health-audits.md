# Code health audits

AEON-571 defines the ongoing code-health workflow. Each run belongs to a child
ticket of that epic and produces ticket attachments, rather than committed
findings. Recurring work and queue dispatch belong to PAIMOS (AEON-573); until
that integration ships, the coordinator starts these runs manually.

| Run | Trigger | Scope |
| --- | --- | --- |
| Delta audit | Each published release | Files changed since the last audited release, by slice |
| Tool sweep | Weekly | Static analysis compared with the accepted tool baseline |
| Rolling deep read | Weekly | Two of nine slices in rotation; a complete rotation takes about five weeks |
| Full audit | Quarterly or on demand | All nine slices plus the tool sweep |

Readers use [the shared rubric](../../scripts/audit/rubric.md), record each assigned
file and findings in [the slice schema](../../scripts/audit/finding.schema.json),
and obtain independent verification from another model family. Check current
tickets before reporting known problems. Deduplicate against open findings by
the emitted `fingerprint` (files + theme + title, ignoring line-number churn,
title case and whitespace); link existing tickets in `related_tickets`.
Critical findings get an immediate ticket and operator notice; high findings
go to their theme's queued ticket; medium/low findings are batched by theme
with a weekly summary of new themes. This toolkit does not send notices,
create tickets, run models, schedule work or publish pages.

The tools in `scripts/audit/` use Python 3.10+ and the standard library, Git,
and Node for the optional renderer smoke check. They run offline, without
installing packages or invoking analysis tools. The renderer smoke check retains
its content assertions and a ten-second VM hang guard; it does not assert rendering
speed on shared runners. Supply locally collected,
sanitized diagnostics in the shared `T` manifest; never include credentials
or raw secret scanner output. Tool installation, advisory downloads and
analysis run separately on the approved build/test runner.

Start with an immutable snapshot and inspect `assigned` in the coverage
report to dispatch readers. The slice map uses repo-relative, case-sensitive
globs: `*` stays within one path component and `**` crosses directories.
`exclude` removes deliberate overlap (S9 excludes S8's views/components).
Unknown code extensions can be added to `code_extensions`/`code_names`;
new packages need explicit ownership. Ambiguous ownership and unassigned
code fail with exit 1, malformed inputs with exit 2. The map covers deploy
Compose, root/web embeds, web configuration and end-to-end tests as well as
the original nine areas. Shared harness/model observations in
`internal/modelreport/` belong to S2 (agent runtime), alongside their harness,
daemon and model-registry callers. Assigned context files also require manifest entries;
use `read`, `generated`, `vendored` or `not-code` with a nonnegative line count.
Account quota privacy policy and its tests (`internal/accountprivacy/**`)
belong to S1, alongside agent accounts and authorization.

```bash
AUDIT_DIR=tmp/code-health
AUDIT_SHA=$(git rev-parse HEAD)
mkdir -p "$AUDIT_DIR"
python3 -B scripts/audit/covcheck.py --sha "$AUDIT_SHA" --out "$AUDIT_DIR/assignment.json"
```

An assignment-only check proves ownership, not reading. Each reader writes
`S1.json` through `S9.json` with `slice`, `sha`, `coverage`, `findings` and
`good`. The coordinator then validates the actual manifests. For a rolling
read, pass `--slice S1 --slice S2` and just those two `--manifest` arguments.
For a delta audit, pass `--since <last-audited-commit>`; only changed files
still present at the new snapshot require coverage. Whole-tree ownership is
still enforced. Deleted paths do not need reading at the new snapshot.

```bash
# Full audit; all nine manifests must already exist.
coverage_args=()
merge_args=()
for manifest_file in "$AUDIT_DIR"/S[1-9].json; do
  coverage_args+=(--manifest "$manifest_file")
  merge_args+=(--manifest "$manifest_file")
done
python3 -B scripts/audit/covcheck.py --sha "$AUDIT_SHA" "${coverage_args[@]}" --out "$AUDIT_DIR/coverage.json"

# Weekly sweep: compare first. The checked-in baseline is an empty seed.
python3 -B scripts/audit/baseline.py compare --input "$AUDIT_DIR/T.json" \
  --baseline scripts/audit/tool-baseline.json --out "$AUDIT_DIR/T-new.json"

python3 -B scripts/audit/merge.py "${merge_args[@]}" --manifest "$AUDIT_DIR/T-new.json" \
  --reviews "$AUDIT_DIR/reviews.json" --release 'audited release label' --out "$AUDIT_DIR/merged.json"
python3 -B scripts/audit/groups.py --audit "$AUDIT_DIR/merged.json" --out "$AUDIT_DIR/grouped.json"
python3 -B scripts/audit/build.py --audit "$AUDIT_DIR/grouped.json" --out "$AUDIT_DIR/audit.html"
node scripts/audit/run.cjs "$AUDIT_DIR/audit.html"
just audit-check
```

Reviews are a JSON array of objects with `id`, `verdict`, nonempty `evidence`,
and optional `severity`, `note` and `pass`. Verdicts are `confirmed`, `partly`,
`refuted` or `duplicate:<finding-id>`; the prototype's `BEGIN-VERIFY`/`END-VERIFY`
JSONL envelope is also accepted. Review IDs must match candidates from the
same run, or use `T:<theme>` to verify a tool theme by sampling as in the
prototype. Individual tool verdicts take precedence over a theme verdict;
the tool table reports candidate counts and mixed verdicts per theme.
Missing reviews remain `unverified` in `dropped`; only confirmed or
partly confirmed findings appear on the page. Original severity and verification
evidence stay with each finding. Fingerprint duplicates retain merged locations,
related tickets and aliases, with each original candidate in `dropped`.
Mixed snapshots, conflicting reviews, duplicate IDs and invalid duplicate
targets fail rather than silently overwriting evidence.

`groups.py` derives themes from the retained findings. Optional
`--descriptions PATH` supplies a JSON object keyed by theme, with `title`,
`summary` and `tickets` for editorial copy; no fixed finding IDs are bundled.
The page keeps the prototype's search, severity filters, expandable themes,
coverage, tools, strengths and method. It calculates totals from input, escapes
audit text and uses local fonts. Identical inputs produce identical JSON/HTML;
there are no wall-clock timestamps or embedded claims about a previous audit.

Accept a baseline explicitly **after reviewing** the tool candidates:
`python3 -B scripts/audit/baseline.py accept --input tmp/code-health/T.json
--baseline scripts/audit/tool-baseline.json --out tmp/code-health/tool-baseline.json`.
It stores sorted SHA-256 fingerprints only, including the optional `tool`
producer identity (defaults to the theme), and retains known hashes across
clean sweeps. Later runs use the latest accepted attachment as `--baseline`;
acceptance is never implicit during comparison. Findings, reviews, reports
and accepted baseline attachments stay under ignored `tmp/` or outside the
checkout, and are attached to the run ticket. The stable living-page link,
trends and slice-coverage age are coordinator publication concerns.
