// SPDX-License-Identifier: AGPL-3.0-only

// Package business defines the R4 contract for first-party business plugins.
// This is a contract handoff, not an implementation package. api/openapi.yaml
// and migrations 0400-0403 are authoritative. R1 nodes remain the single work
// tree, R2 runs supply optional agent time, and R3 owns the compiled plugin
// registry. No code or data was read from business-owned repositories to design
// these public AGPL domain types.
//
// The four plugin IDs are business_costs, business_crm, business_quotes and
// business_hours. Each builder supplies a compiled plugins.Plugin with an
// exact digest, owner, version and declared node kinds/views/workflow steps.
// The coordinator registers all four in plugins.NewRegistry before Seal and
// mounts the resulting module. It never loads a plugin from tenant data. The
// tenant admin enables each with PUT /plugins/{pluginId}/installation using the
// compiled digest and an explicit permission subset. Plugin-specific paths
// fail closed when disabled, unpinned, digest-mismatched or under-granted.
// Quotes and hours additionally require business_costs enabled; quotes also
// require business_crm. Generic R1 nodes remain visible through R1 read APIs.
// Registry checks happen inside the same db.InTenant transaction as business
// writes, including a recheck after a row lock on a contested decision. Existing
// R3 fence permissions are the vocabulary: nodes.contribute, views.provide,
// steps.evaluate, steps.request and steps.apply. The manifests declare only
// needed permissions. The business host uses plugins.Enabled for declared
// operations; it verifies observed facts and person authority itself before
// writing. A plugin result, view or PDF never grants authority.
//
// Manifest declarations and host gates:
//   - business_costs: cost_unit kind, cost_units view, cost_rate_change step;
//     observed_state plus person_decision (tenant admin) gates. The step binds
//     steps.apply and the view/kind bind views.provide/nodes.contribute.
//   - business_crm: organisation/contact kinds, crm view, crm_bind step;
//     observed_state plus person_decision (tenant admin) gates. Graph links
//     customer_of (organisation -> project/quote) and contact_for (contact ->
//     organisation/project/quote) use R1 node_relations, checked for kind and
//     direction by the CRM handler. Contact-principal binding is explicit.
//   - business_quotes: quote kind, quotes view, quote_issue and quote_accept
//     steps; observed_state plus person_decision gates. The issue gate checks
//     the current version, recipient and frozen digest. The accept gate checks
//     the signed-in customer person, bound contact, issued current version and
//     the exact digest in the request. This customer approval is its own event
//     and immutable projection; R2 approval_requests are agent-proposed grants
//     and cannot express a customer acceptance. An agent cannot accept.
//   - business_hours: hours view, time_entry (observed_state gate) and
//     period_approve (observed_state plus person_decision gates) steps. The
//     latter is an authenticated
//     admin person's approval of an exact period-entry digest, not an R2
//     agent permission grant. Telemetry cannot approve its own time.
//
// No business plugin declares a runtime integration or background job in R4.
// All operations also check the caller role, referenced resource kinds and
// tenant, expected revision or digest, and a matching installation. Every
// query (including reads, export rendering, recursive totals and import target
// queries) runs through db.InTenant. Every change appends an R1 tenant event
// in its transaction; a no-op replay makes no new event. The host must not
// treat an R3 manifest gate list as proof that the gate was satisfied.
//
// Cost units are tenant-configured nodes of slug cost_unit, including classic
// PPM cost_unit nodes already imported with their original keys. The plugin
// supplies a JSON field schema for display metadata. cost_unit_rates holds
// effective-dated internal and bill rates by unit and ISO currency. An admin
// serializes and rejects overlapping intervals per cost unit/unit/currency;
// a new price creates a new rate row and event; the previous row's end date
// may close at the new start with its own event, but its amounts never change.
// Quotes and hours snapshot their chosen rate and currency. There is
// no exchange-rate conversion: totals group by currency. A rate table is a
// pricing source, not an invoice, payroll ledger or accounting system.
//
// CRM organisations and contacts are R1 nodes with tenant-owned labels,
// schemas and keys. Existing POST /nodes, PATCH /nodes and /relations provide
// general editing and graph linkage; R4 only adds explicit contact-principal
// binding. An organisation can be the customer_of a project or quote. A
// contact_for link relates a contact to its organisation or a project/quote.
// The handler checks live kinds and link direction; the SQL relation type
// constraint is expanded in 0401. Neither email equality nor a contact node
// alone grants the ability to accept an offer. R4 includes no email sync.
//
// A quote is one R1 quote node plus business_quotes projection. Creating it
// links an existing project and customer organisation. Each quote_versions row
// and ordered quote_line_items are a complete frozen offer: recipient contact,
// currency, Markdown terms, quantity, unit, cost unit, bill-rate snapshot,
// net amount, tax and total. The version digest covers the canonical offer
// and ordered lines. The handler checks line arithmetic with decimal math,
// that all cost units and recipient/customer links are live, and that an
// effective rate exists on the version creation's UTC date for each line's
// unit and requested currency. expected_revision fences concurrent creates.
// Issue is a separate event/projection. Acceptance is allowed only for the
// current issued version and its exact digest by a person principal bound to
// that version's recipient contact; the contact must still be contact_for the
// customer organisation. The issue and acceptance projections reference
// unique, actor-matched tenant events. A new version never edits an earlier
// accepted one.
// Markdown and PDF are deterministic renderings of that frozen version. The
// export endpoint does not create an independent mutable file or approval.
// Versions and lines are immutable; the handler must prevent line insertion
// after issuance and must create all lines before the issue transaction.
//
// Each time entry belongs to one principal, node, open period and cost unit;
// it snapshots its hour rate for the entry start's UTC date and requested
// currency, plus start/end and whole-second duration.
// A person creates own manual entries; a tenant admin may record another
// person's time with an event naming both. Agent time is derived from a
// terminal R2 agent_run: one entry per run, with its agent principal, work-
// order node and persisted started_at/ended_at. Heartbeats, model cost and
// token usage are not billable hours. No run with missing terminal times is
// eligible. Time entries cannot be edited; corrections require a new entry
// and an explicit reversal event/projection in a later build if needed.
// The R4 API intentionally closes approved periods; it does not silently
// correct approved entries. Approval locks the period, hashes ordered entry
// values, checks expected revision/digest, and records the deciding person and
// event. Subtree totals recursively walk live R1 descendants, add each entry
// once, and return duration plus amounts separately per currency. The
// approved_only filter reads only sealed periods. No aggregate cache is an
// authority in R4.
//
// Second-tenant bootstrap is an operator-only CLI and ordinary tenant-admin
// API sequence, not a migration that seeds a business tenant automatically:
//  1. paimos tenant create --slug example-business --name Example Business
//  2. paimos tenant principal bind-oidc --tenant example-business --issuer <issuer>
//     --subject <operator-subject> --name <name> --role admin
//  3. GET /auth/login?tenant=example-business, then GET /me; the signed OIDC state
//     fixes the tenant and issuer+subject must resolve to the mapped principal.
//  4. GET /plugins to obtain the four compiled manifest digests. PUT
//     /plugins/business_costs/installation,
//     /plugins/business_crm/installation,
//     /plugins/business_quotes/installation and
//     /plugins/business_hours/installation with
//     enabled=true, each exact digest and its manifest permission subset.
//  5. GET /kinds, then POST /kinds for each missing manifest kind:
//     cost_unit (Cost unit, CU), organisation (Organisation, ORG), contact
//     (Contact, CON), quote (Quote, QUO). Each call includes slug, label,
//     short_prefix, icon, allowed_child_kinds and field_schema. Imported
//     cost_unit already present is reused, never rekeyed.
//  6. For a customer who will approve an offer, provision a tenant person
//     with paimos tenant principal bind-oidc --tenant example-business --issuer
//     <issuer> --subject <customer-subject> --name <name> --role customer;
//     create a contact node via POST /nodes and a contact_for organisation
//     link via POST /relations, then POST /crm/contacts/{contactId}/principals.
//
// Tenant creation allocates the UUID before db.InTenant and inserts tenants
// inside that transaction; kind-seeding triggers retain their own setting.
// OIDC identity upsert, principal binding and membership lookup use a target
// tenant transaction. The R0 login flow currently assumes one bootstrap slug;
// the bootstrap builder must bind the requested slug into signed OIDC state,
// resolve membership by issuer+subject, and keep a session in one tenant.
// Email alone cannot choose or join a tenant. Shared identities may have one
// principal per tenant. All CLI changes write a tenant event by the mapped
// operator principal after binding; initial tenant/principal creation uses an
// auditable bootstrap actor established in that target tenant transaction.
//
// PMA import is design only. Extend importer.Source with an explicit source
// instance identifier and a pm-example-business adapter using the same read-only
// snapshot contract; --tenant example-business selects the target. Preserve each
// classic key exactly and namespace idempotency by tenant and source instance,
// so an identical numeric source ID from PPM and PMA cannot merge records.
// Map PMA cost_unit issues to the tenant cost_unit kind and retain all fetched
// fields and historical records under fields.classic/events as the current
// PPM importer does. Dry-run reports unmapped and skipped records without
// writes; live import uses db.InTenant, event append and per-tenant/source
// advisory lock. It must reject a mismatched source ID on rerun. Reading or
// running pm-example-business is an operator trust-context gate: this contract
// worker does not access it, and execution waits for Markus's explicit
// instruction with source access and target tenant confirmed. No business-
// owned code is copied into this public repository.
//
// Parallel build packages, with disjoint file ownership:
//
//	A. Cost units: internal/business/costunits/*.go and
//	   web/src/views/business/CostUnitsView.vue; owns rate endpoints and the
//	   business_costs manifest constructor. Consumes 0400.
//	B. CRM: internal/business/crm/*.go, internal/relations R4 type support and
//	   web/src/views/business/CRMView.vue; owns contact binding, graph kind
//	   checks and business_crm manifest constructor. Consumes 0401.
//	C. Quotes: internal/business/quotes/*.go and
//	   web/src/views/business/QuotesView.vue; owns quote
//	   endpoints, rendering and business_quotes manifest constructor. Consumes
//	   0402; no other builder edits its Go package or view files.
//	D. Hours: internal/business/hours/*.go and
//	   web/src/views/business/HoursView.vue; owns time APIs,
//	   telemetry conversion and business_hours manifest constructor. Consumes
//	   0403.
//	E. Tenant/PMA: internal/tenantbootstrap/*.go, internal/auth/* tenant
//	   selection, internal/importer/* PMA adapter, and associated tests;
//	   owns CLI behavior design but does not edit cmd/aeon itself.
//	F. Business shell: web/src/views/business/BusinessHome.vue and
//	   web/src/components/business/*; owns navigation/presentation shared by
//	   the views, never edits another package's view or Go code.
//
// Contract worker owns api/openapi.yaml, migrations 0400-0403, this doc.go and
// contract_test.go.
// Builders consume the contract and do not independently edit shared OpenAPI
// or migration files. Each builder's manifest constructor is registered by the
// coordinator; none edits internal/plugins/builtin.go. Coordinator alone
// reconciles shared contract changes, wires cmd/aeon and web/src/router.ts,
// and performs release-wide QA. Builders test tenant isolation, installation
// closure, gate failures, event atomicity, retries and financial edge cases.
//
// QP1/AEON-82 quote port (migration 0600) adds generic commercial document
// code, not a tenant's company identity, legal wording, logo or bank data.
// The coordinator registers quotes.ManifestPlugin before Seal and mounts
// quotes.New(pool, registry), which returns httpapi.Module. This package does
// not wire cmd/aeon, internal/plugins/builtin.go or web/src/router.ts.
// business_quotes is the lifecycle/number projection; quote_drafts is the
// mutable full document; quote_versions and quote_version_snapshots are sealed
// commercial evidence. R4 rows keep digest_mode=r4-v1 and pricing_mode=rate-4.
// New document versions use digest_mode=document-v1 and pricing_mode=
// cent-half-up-v1. No old version totals or digests are recomputed or rewritten.
// A commercial offer number AYYMMDD-NN is allocated on creation and never
// reused. KYYMM<unpadded counter> is allocated once on a customer's first
// numbered quote. The calendar day comes from quote_settings.numbering_time_zone
// (explicit IANA zone; Europe/Vienna for classic-compatible tenants), never
// the server timezone. The draft currency comes from settings.default_currency.
// Settings and SMTP confirmation default to absent/off;
// only a tenant admin can configure future-offer content with revision CAS.
// P2's CRM number-conversion adapter calls quotes.ReformatLegacyCustomerNumber
// with its authenticated admin principal; the P1 service locks the customer
// and related quotes, rejects any nondraft, updates draft customer numbers and
// revisions, and appends events atomically. It never renumbers issued history.
// No offer is emailed by finalization.
//
// P3 document JSON schema v1 (exact wire names; OpenAPI QuoteDocument):
//
//	schema_version=1, minimum_writer_version=1; title, subtitle, project_ref,
//	offer_date and valid_until (YYYY-MM-DD local calendar dates), currency
//	(three uppercase letters); sender, recipient, legal and layout objects;
//	sections and positions ordered arrays; net_total_cents server-computed.
//
// sender is a tenant-settings snapshot with company, street, postal_code,
// city, country, register_no, register_court, email, phone, website, uid,
// bank_name, iban, bic, contact_person, plus optional file/hash references.
// recipient is a frozen name, address, contact, country, customer_no, email
// and optional contact_node_id. legal holds intro, accept_text and vat_note;
// these are prose, not an inferred tax rate. layout holds neutral presentation
// values including logo_width_mm/logo_offset_mm as exact decimal strings and
// optional asset references. All business words and assets come from tenant
// data; no migration, fixture or source constant supplies them.
// Each section is {id,heading,body,nodes}; each prose node is
// {id,kind,text,depth?,marker?,numbering?,list_start?,list_continue?,
// section_bound?,glyph?,marker_x_mm?,marker_y_mm?,text_start_mm?,marks?}.
// IDs are UUIDs unique within one document and remain stable on edits and
// reorders. Splitting a node creates a new ID on one side; merging retains
// one ID. Duplicating a quote remaps all section, prose and position IDs.
// kind is paragraph|item; marker is disc|circle|square|dash|decimal;
// numbering is absent or outline for multilevel generated numbers. depth is
// 0..5. list_start is 0 or 1..9999; a positive start excludes continue.
// list_continue resumes the prior compatible numbering, even across prose;
// section_bound prefixes outline numbering with the owning section number.
// The generated marker is never inserted into text. Optical offsets are
// signed decimal millimetre strings, not binary floats. marks are ordered,
// disjoint {start,end,bold?,italic?} UTF-16 half-open ranges. Offsets must be
// valid surrogate boundaries; neither flag false/absent on both is invalid.
// A plain body with empty nodes is retained for old content.
// Each position is {id,pricing_source,short_text,long_text,quantity,
// unit_label,unit_price_cents,total_cents,currency,cost_unit_node_id?,
// rate_unit?}. quantity is an exact decimal string with at most two places;
// unit_price_cents and total_cents are integers. Manual positions supply a
// cent price and arbitrary bounded display unit. cost_unit positions identify
// a live rate and hour|day|item rate_unit; their effective rate is frozen on
// issue. The server recomputes every total with half-up cent rounding; client
// totals never grant authority. A version holds the exact source mode, rate
// snapshots, sender/recipient/legal/layout, number, date, validity_time_zone
// and document digest. Later settings changes never change the expiry day.
//
// Draft PATCH requires If-Match "qd-<draft_revision>" and writer_version plus
// UUID client_session_id/mutation_id. Missing/stale preconditions fail 428/412.
// Writer 2 preserves inline marks. A draft gains minimum_writer_version=2 on
// its first marked save and keeps that floor after formatting is removed;
// unformatted writer-1 drafts remain writable by older clients.
// Receipts are durable with no silent expiry; exact replay acknowledges the
// original result revision, while a reused ID with changed payload conflicts.
// The aggregate business_quotes.revision fences lifecycle/visibility and is
// distinct from draft_revision and immutable version. Draft reads and mutation
// receipts expose both revision axes to P6. Events expose IDs,
// revision and mutation metadata, never draft contents or capability secrets.
// Finalize checks both revisions and the saved draft hash under the quote lock,
// freezes it and issues in one transaction. Duplicate creates a new draft and
// number; archive is independent of issue/acceptance and never deletes history.
// POST /quotes/{id}/draft/branch explicitly reopens a sealed document as a
// new mutable draft on the same quote, with base_version and new revisions;
// the prior immutable version and decision evidence are never edited.
// Classic draft/sent/accepted map to draft/issued/accepted; expired derives
// from frozen valid_until in the configured local day and remains stored as
// issued. Historical declined is import-only evidence; void stays distinct.
// P5 owns public capability routes and confirmation jobs. Its acceptance
// transaction must lock the same quote row and use one common decision guard
// with authenticated acceptance; capability identity is never a bound person.
// The public-link projection stores only a verifier hash, frozen target digest
// and required quote.public_link_created / quote.public_link_revoked event IDs.
// The public-acceptance projection requires quote.accepted_public evidence;
// a unique quote_decisions row arbitrates both acceptance channels. P5 must
// keep audit metadata out of general events and must use a tenant service
// actor rather than inventing an authenticated customer principal.
package business
