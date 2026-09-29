// SPDX-License-Identifier: AGPL-3.0-only

// Package cli is the agent command line for the aeon binary.
//
// RunMessaging is the complete CLI constructor called from cmd/aeon. Run is
// available for callers that need the base command tree. This package does
// not mount an httpapi.Module or register a plugin manifest. Its commands
// call existing server modules. When argv[0] is paimos, the CLI reads
// ~/.paimos/config.yaml or PAIMOS_URL / PAIMOS_API_KEY.
//
// Served compatibility verbs (issue, project list, knowledge for memory, runbook, guideline,
// external-system and related-project, search, model resolve, onboard,
// session start, tell, listen, message target, message deliveries, anchors scan/verify, skill
// render, and sync check) talk only to the configured Aeon instance, except
// anchors scan/verify, which read and write the repo-side index
// .paimos/anchors.json and do not open a network connection. skill render
// builds the canonical agent artifact from the project node and its knowledge
// children, passes it through a harness adapter (claude-code, codex, grok, pi,
// cursor), and writes a file whose paimos-managed header lets sync check
// detect drift. run-agent watch executes local Claude work orders and reports
// evidence to Aeon. baseline-batch report-built resolves classic batch aliases
// to stage handoffs and records typed built evidence.
// harness run-heartbeat registers or resumes one session from a private state
// directory and posts known heartbeat fields while its owner process lives.
// Owner exit and SIGTERM mark that session stopped. A Claude transcript path
// posts cumulative usage to the existing session usage route.
// CP3 adds relation add, project create/show/update and resource reads, tag
// catalog commands, attachment upload/list/get/rm, declarative apply, schema,
// doctor, and authenticated curl. External-stage request/pull/report/result
// call Aeon's server-fenced stage handoff API. Classic one-time credentials,
// reporter registrations and launch admission cannot grant Aeon authority;
// those commands return exit 3 and point to first-party plugins and journey
// approvals. rules-compare is an offline one-time report of explicit doctrine
// files, an optional saved AR1 merge document and optional AEON-219 receipt
// hashes. It does not contact the network, wait, or replace instruction files.
// The coordinator wires the CLI constructor into cmd/aeon.
//
// This package exports no httpapi.Module and no plugins.Plugin. The nine
// starter kinds stay as they are. external_system and related_project are
// ensured through POST /api/kinds on first use (kind.created); the
// coordinator already mounts the nodes module that writes the entries.
package cli
