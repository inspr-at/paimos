# Nightly CI evidence and reporting

Nightly full executes the native Go, Node, Vitest and browser catalogue. A green
result means the selected cases and runners completed successfully; missing,
partial or mismatched reports remain failures. Strict native inventory checks
run in the Go static job and before the web build in both CI and nightly.

Each tier runner writes a bounded `*-failures.json` artifact alongside its
existing measurements. It names failed case keys, incomplete runners, the tested
commit, GitHub run and attempt. A runner failure without a complete case result
is reported explicitly. These artifacts do not change test selection, assertions,
retry policy or the required release gates.

The existing PAIMOS reporter can inspect a completed red run:

```sh
node scripts/nightly-ticket.mjs --run-id RUN_ID --evidence DOWNLOADED_ARTIFACT_DIRECTORY
```

The default invocation is read-only. Delivery uses the reporter's existing
absolute CLI path and shared state directory:

```sh
node scripts/nightly-ticket.mjs --run-id RUN_ID --evidence DOWNLOADED_ARTIFACT_DIRECTORY \
  --paimos EXISTING_CLI_PATH --state-directory EXISTING_STATE_DIRECTORY --write
```

The reporter verifies the `ppm` INSPR account, repository, workflow, main branch,
commit and attempt, then reads the complete bounded job inventory. Green,
cancelled and foreign runs create no work. It creates one hidden AEON bug per
red run, with failed jobs, exact case evidence when available, and an explicit
note when artifacts are missing. Tracker search and a per-run lock prevent
concurrent duplicate creation; partial search, an existing lock or an uncertain
write returns an error. No additional credential belongs in GitHub Actions.

OPS owns activation in the existing reporter, runner capacity and its budget
wiring (OPS-257). The AEON lead, `aeon-lead-claude`, owns product and test fixes and posts the
`nightly_green` outcome seven days after deployment. Activation and the observed
seven-day outcome are release follow-ups, not claims made by a local test run.

The AEON-1018 baseline is four failed scheduled runs from 2026-10-05 through
2026-10-08: 0/4 green. The handoff records failing-before and passing-after
checks, preserved risk mappings and the tested commit. Hosted CI remains the
proof that the deployed nightly lane is green.

Worker verification on 2026-10-09 at source commit `1715191df` passed all 41
checks in `ci-static --merge-main` and all 47 browser files implicated by the
historical failures: 696 passing tests and 24 unchanged optional skips. Browser
files ran individually with one worker on mbp2606; the backup/restore drill used
the approved remote Go runner. The handoff retains each tested SHA, failed-before
log, passing result and mapping for updated UI assertions.
