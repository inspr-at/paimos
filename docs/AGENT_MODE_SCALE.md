# Agent Mode measured scale budgets

PAI-811 defines latency and change-volume gates in addition to the hard
1,000-row snapshot limit and four-query Reader shape. These are regression
budgets for the deterministic local fixtures, not production SLOs: deployment
hardware, network distance, concurrent load, and database history still need
environment-specific monitoring.

| Surface | Deterministic load | Enforced budget | Executable evidence |
|---|---:|---:|---|
| Browser render | 100 delivery cards, aggregate lanes, one selected card | ≤ 2,000 ms from mount start through settled DOM | `AgentModeView.test.ts` — `scales to 100 deliveries…` |
| Reader query | 1,000 authorized delivery roots, including stage/trust/duration branches | ≤ 1,000 ms for the pinned four-query `Reader.Read` | `reader_test.go` — `TestReaderUsesOneClockInstantAtEverySupportedScale/roots_1000` |
| Snapshot API | 1,000 authorized deliveries | ≤ 2,000 ms for loopback HTTP, Reader projection, JSON encoding, transfer, and decode | `agent_mode_test.go` — `TestAgentModeDetailScopePrecedesScaleLimitAndOversizeSnapshotsAreExplicit` |
| Change ingestion | 1,000 committed changes for one active delivery | ≤ 5,000 ms for transaction begin, trigger-backed durable facts, and commit | `stream_test.go` — `TestStreamSubscribeRaceOverflowLostWakeRestartAndPermissionChanges/overflow_lost_wake_coalescing_and_restart` |
| Change recovery | the same 1,000-change backlog | ≤ 1,000 ms to detect the 512-event replay overflow and fail closed to resync | same stream test |

Reference measurement on 2026-08-27, using a local Apple M5 Max and the commands
below, was 121.4 ms render, 21.0 ms Reader query, 28.6 ms snapshot API,
1,029.4 ms change ingestion, and 30.0 ms overflow recovery. Three subsequent
local change-ingestion runs were 1,012.2–1,071.1 ms. The first GitHub Actions
Ubuntu 24.04 run measured 3,383.8 ms, so the portable change-ingestion budget is
5,000 ms; the original 2,000 ms ceiling described only the faster local machine
and could not serve as a cross-runner regression gate. These measurements
demonstrate margin against the enforced budgets; they are not percentiles or
production SLO evidence.

The tests log the measured elapsed value next to the budget on every verbose
run. Validate all budgets with:

```sh
cd backend
go test -v ./agentmode -run 'TestReaderUsesOneClockInstantAtEverySupportedScale|TestStreamSubscribeRaceOverflowLostWakeRestartAndPermissionChanges'
go test -v ./handlers -run TestAgentModeDetailScopePrecedesScaleLimitAndOversizeSnapshotsAreExplicit

cd ../frontend
npm test -- --run src/views/AgentModeView.test.ts -t 'scales to 100 deliveries'
```

The 1,000-row boundary is exact. A larger list returns an explicit invalid
request rather than partial truth. More than 512 unseen durable changes causes
an identity-free reset, after which the client reloads an authoritative
snapshot. The measured change-volume gate deliberately exercises 1,000 facts
to prove that overload recovery remains bounded well beyond that replay limit.

When changing a budget, update this document and its named assertion together,
record why the product expectation changed, and rerun the full backend,
frontend, build, end-to-end, and visual-baseline gates. Do not relax a budget
solely because an unexplained regression crossed it.
