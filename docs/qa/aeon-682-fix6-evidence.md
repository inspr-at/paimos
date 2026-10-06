# AEON-682 fix round 6 evidence

Merged origin/main (`4d7e7de34`) cleanly before implementing the CI fixes. The branch remains `work/aeon-682`; no push to origin, deployment, ticket write, browser session access, or model review was performed.

The hosted CI failure in run `37194328587` was an undeclared `POST /api/queue/{nodeId}/undo` route plus a failing unassignment browser regression. Undo now declares `nodes.read`, matching the other queue entry routes; the handler still checks current write authority and the caller's audited receipt inside the mutation transaction. Route matrix assertions cover allowed owner/member entry and denied customer entry, and the fail-closed regression requires the exact declaration and rejects anonymous entry.

The browser regression had expected visible “Unassigned” text, although the approved compact list shows a dash with an accessible “Unassigned” label. The corrected assertions require the exact dash, exact label and exact button accessible name. The spec also mocked PATCH even though list assignment uses the bulk endpoint. It now verifies the bulk request's target, captured revision and null assignment, preserves the other fields, and keeps every queue eligibility, no-reload, status, membership and stability assertion.

Bulk mutation responses were missing the current queue projection. They now load queue membership and idle-work evidence once each for the existing batch of at most 500 nodes, inside the mutation transaction. These are reads without new locks after the event counter; read errors fail the transaction. Regression subcases cover bulk unassignment, an ordinary bulk priority edit, direct queueing after each mutation, and retained membership after editing queued work. No endpoint, migration, ownership rule, approval flow or UI layout was added.

Five ticket Go regressions and eighteen ticket web regressions are explicitly classified as GATED-FULL. Strict native inventory validation passes for 100 Go cases in the queue/authz packages and all 42 cases in the three touched queue web test files. Existing classifications and unrelated manifest drift were left unchanged.

Validation:

- The exact route map from `7e75cdf4` fails the current `TestRouteDeclarationsFailClosed` assertion with `queue Undo entry permission: "", declared=false`; the corrected map passes that test, `TestRouteSourceCoverage` and `TestRealMuxRouteCoverage`.
- Remote pre-fix commit `0085cc69` fails both `TestStaleQueueAfterNodeMutation/bulk_unassign` and `/bulk_priority` for `QueueStale:false`. Its bulk handler is identical to `7e75cdf4` (`git diff` between those revisions for that file is empty).
- Final affected Go packages: remote-test.sh on approved mbp2606 at `354f0e05`, `internal/agentruns`, `internal/authz`, `internal/nodes`, `internal/workqueue`: all four passed (agentruns 37.709s, authz 17.991s, nodes 92.334s, workqueue 0.410s).
- Local, sequential touched files: work-queue.test.ts 6/6; work-queue-store.unit.test.ts 18/18; work-queue.spec.ts 18/18, isolated Chromium, workers=1, no retries. Existing stability guards pass, and the browser wrapper reports zero remaining processes.
- Twenty-four screenshots are outside git in `/private/tmp/claude-501/-Users-markus-Code-aithema/af3ab8bf-63f6-4fb5-bccf-086eb11c043e/scratchpad/aeon/shots/aeon-682-fix6/`, including `aeon-682-list-{390,1024,1440}-{light,dark}.png` and `aeon-682-ticket-{390,1024,1440}-{light,dark}.png` with the long German fixture.
- git diff --check passes. No migrations added.

Limits: the remote browser launcher refused with exit 3 because OPS-247 bootstrap is pending. Worker-common's TEST EVIDENCE rule permits the one-file local fallback used here. CI-equivalent Linux Chromium and complete hosted suites were not rerun; that requires the coordinator's later push, which this task forbids. The existing gate/policy-preview failure in run `37194328396` reports no trusted gate/verdict for the branch commit; the coordinator owns consolidated review, so no gate was run or bypassed.
