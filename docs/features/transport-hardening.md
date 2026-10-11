# Transport hardening (AEON-1229)

HTTP body reads use `httpapi.ReadRequestBody`: a route-specific byte cap,
`MaxBytesReader`, and the smaller of the route timeout and the existing request
deadline. Reads finish before transaction admission. A failed read retains the
transport deadline and engages the AEON-1228 connection guard, including read
failures with a still-live request context. Only a complete read clears the
deadline and permits keep-alive reuse. Unsupported writers use a bounded,
closed and joined reader; request bodies must support Close interrupting Read.
A cancelled transport read is given up to one second to finish before an
explicit interrupt, preserving the existing failed-read drain protocol.

The AST ownership test forbids production HTTP read-deadline calls outside the
helper. The raw Unix hook-note socket has a separate, explicit exception.
Existing JSON validation, permission checks and final transaction fences remain
at their original owners. The byte caps of the decoders still apply after
buffering. Delivery route buffering uses an outer 1 MiB cap (its decoders have
smaller caps); webhooks use 2 MiB and five seconds.

`GET /api/health` remains liveness. `GET /api/ready` checks database acquisition
and ping, then a read-only authentication canary within the same 500 ms total
budget. The canary exercises session lookup admission and bootstrap-tenant key
reads through the authentication transaction path. It reads no credentials,
mints no keys and renews no session or agent activity. It is a structural auth
canary; it does not prove an actual browser/session/key sign-in or the proxy
path. OPS-307 owns those authenticated synthetic probes.

A shared authentication canary failure must persist for at least 15 seconds
and three observations before readiness returns 503 with
`authentication_unavailable`. Recovery requires 30 seconds and three successes.
Observations count at most once per second; gaps over 30 seconds reset the
observation window. Cancelled probe callers do not change process health.
Invalid credentials and individual tenant handler failures cannot trip global
readiness. Probes continue during traffic removal, enabling recovery without
client traffic. Drain and database failures retain their existing immediate
readiness behavior. OPS should probe at least every five seconds.

Counters are process-local and reset on restart. `Server.Metrics.Snapshot()`
returns API requests by route template, status class and authenticated lookup;
its second result is cancelled arrivals. `db.AcquireErrors(pool)` returns pool
acquisition errors by a fixed class. Existing pgx tracing, permits and retained
connection ownership are preserved. Metrics are available to trusted
in-process exporters and structured logs; no cross-tenant metrics endpoint is
exposed to tenant API callers.

PHAROS can ingest the structured log increments as these Prometheus counters:

| Metric | Log selection / value | Labels |
| --- | --- | --- |
| `aeon_http_requests_total` | `msg=request`, increment `request_count` | instance, route, status_class, authenticated |
| `aeon_http_cancelled_on_arrival_total` | `msg=request`, `connection_context=cancelled_on_arrival`, increment 1 | instance |
| `aeon_pool_acquire_errors_total` | `msg=pool acquire failed`, increment `pool_acquire_error_count` | instance, error_class |

`requests_total`, `cancelled_on_arrival_total` and `pool_acquire_errors_total`
also carry cumulative values for diagnostics. Ingest the unit increments,
not repeated cumulative values as increments. Counters require reliable log
collection; missing logs are unknown, never evidence of availability. Route
labels are registered templates or `unmatched`/`other`, never record IDs or
capabilities. Error classes contain no driver text or connection strings.
`conn_id` is a random server-generated ID stable over one accepted connection;
it appears beside `request_id` in API logs. Neither ID belongs in metric labels.

## SLO and OPS alert handoff

The authenticated interactive API availability objective is **99.5% over a
rolling 30 days**, with a 0.5% error budget. This is an objective, not a claim
that a released instance has already achieved it. Eligible requests are
completed authenticated lookups/handlers with 2xx, 3xx or 5xx responses.
Correct 4xx refusals and confirmed client cancellations are excluded. Exclude
health/readiness/version, webhook ingestion, SSE/live streams and background
work from this interactive denominator. OPS must maintain the explicit route
allowlist from the deployed contract; it must not guess eligibility from a URL.
Authentication lookup 5xx count as failures even when principal resolution
could not complete. Cancelled-on-arrival responses are tracked separately:
validation is intentionally skipped on that broken transport, so they cannot
be called successfully authenticated. HTTP counters alone cannot distinguish a
late confirmed client cancellation; PHAROS must correlate cancellation facts
or conservatively count the 5xx. Pool errors are diagnostics, not additional
request failures in the denominator.

PHAROS should derive `aeon_api_slo_requests_total{outcome="good|bad"}` from the
eligible HTTP increments, attach deployment labels, and install the following
Prometheus rules in OPS. The rules are handoff templates, not deployed here.
For each window `W` in `5m, 30m, 1h, 6h, 3d`, record
`aeon:api_error_ratio:W = sum by (deployment) (rate(aeon_api_slo_requests_total{outcome="bad"}[W])) / sum by (deployment) (rate(aeon_api_slo_requests_total[W]))`.
For `1h, 6h, 3d`, also record
`aeon:api_samples:W = sum by (deployment) (increase(aeon_api_slo_requests_total[W]))`.
Zero traffic has no ratio; use synthetic probe failure and telemetry-absence
alerts when traffic is too low to measure a rate.

```yaml
groups:
  - name: aeon-api-availability
    interval: 15s
    rules:
      - alert: AeonAPIErrorBudgetFastBurn
        expr: >-
          (aeon:api_error_ratio:1h > 14.4 * 0.005)
          and (aeon:api_error_ratio:5m > 14.4 * 0.005)
          and (aeon:api_samples:1h >= 100)
        for: 30s
        labels: {severity: page}
        annotations:
          summary: "Authenticated API burns 2% of its 30-day error budget per hour"
      - alert: AeonAPIErrorBudgetSlowBurn
        expr: >-
          (aeon:api_error_ratio:6h > 6 * 0.005)
          and (aeon:api_error_ratio:30m > 6 * 0.005)
          and (aeon:api_samples:6h >= 100)
        for: 2m
        labels: {severity: page}
        annotations:
          summary: "Authenticated API burns 5% of its 30-day error budget per six hours"
      - alert: AeonAPIErrorBudgetLongBurn
        expr: >-
          (aeon:api_error_ratio:3d > 0.005)
          and (aeon:api_error_ratio:6h > 0.005)
          and (aeon:api_samples:3d >= 100)
        for: 15m
        labels: {severity: ticket}
        annotations:
          summary: "Authenticated API burns at least 10% of its budget per three days"
      - alert: AeonCancelledConnectionArrival
        expr: sum by (instance) (increase(aeon_http_cancelled_on_arrival_total[1m])) > 0
        labels: {severity: warning}
        annotations:
          summary: "A request arrived on a cancelled connection; correlate conn_id and request_id"
```

The fast/slow/long thresholds are respectively 7.2%, 3%, and 0.5% errors.
These budget alerts complement OPS-307's 60-second authenticated synthetic
failure detection. On an alert, correlate the route/status rate, cancelled
arrivals, pool error classes and connection IDs; check `/api/ready` separately
from `/api/health`. A healthy Ping does not clear authentication canary failure.
OPS must validate recording rules, route eligibility, log coverage, probe
cadence, alert delivery and recovery in its own rollout. No production probe,
alert deployment, restart or new credential is part of this change.
