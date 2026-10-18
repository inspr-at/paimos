# Severity rubric (applies to every slice)
- **critical**: exploitable security hole (auth bypass, cross-tenant read/write, RCE, secret exposure), or data loss/corruption reachable in normal use.
- **high**: likely production failure or security weakness with preconditions; broken invariant; race on a hot path; missing authz on a non-trivial endpoint; migration hazard.
- **medium**: architectural weakness that will cost real time (layering violation, duplicated core logic, god package), error handling that hides failures, flaky-by-design tests, perf issue at plausible scale.
- **low**: maintainability/style with a concrete cost (dead code, naming that misleads, missing docs on tricky code).
- **info**: observation, no action required.
Confidence: high = verified by reading the full path; medium = strong indication, one assumption; low = pattern match only.
Never report: formatting nits a linter/formatter fixes automatically; generated code; vendored code; known issues already ticketed (link them in related_tickets instead and only add new information).
Known tickets to check before reporting: AEON-535 (flaky tests/main CI), AEON-541 (layout shift), AEON-543 (ACP probe), AEON-544 (link flake), AEON-479/478 (capacity/continuation), AEON-460 (Touch ID), AEON-528 (strict GDPR later), AEON-537 (reach every harness).
