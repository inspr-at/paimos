// SPDX-License-Identifier: AGPL-3.0-only

package demo

// Fictional products. Nothing here is a customer, a person, or a price from
// outside this demo.
const (
	issuer         = "https://demo.aeon.invalid"
	markerKey      = "LUMEN-1"
	markerField    = "demo_seed"
	markerComplete = "complete"
)

type projectSpec struct {
	key, route, title, body string
	epics                   []epicSpec
}

type epicSpec struct {
	key, title string
	tickets    []ticketSpec
}

type ticketSpec struct {
	key, title, state, priority, comment string
	pillEN, pillDE, benefitEN, benefitDE string
	// assignee is 0 (none), 1 (Ivo Quill) or 2 (Nia Frost).
	assignee int
	rich     bool
}

func projects() []projectSpec {
	return []projectSpec{
		{
			key: "LUMEN-1", route: "LUMEN", title: "Lumen Archive",
			body: "Fictional demo project. A reading room that lends lanterns and keeps a quiet catalog after dusk. Screenshot data only.",
			epics: []epicSpec{
				{key: "LE-1", title: "Reading room", tickets: lumenReading()},
				{key: "LE-2", title: "Lantern desk", tickets: lumenLanterns()},
				{key: "LE-3", title: "Quiet hours", tickets: lumenQuiet()},
				{key: "LE-4", title: "Catalog cards", tickets: lumenCards()},
			},
		},
		{
			key: "HARBOR-1", route: "HARBOR", title: "Harbor Ledger",
			body: "Fictional demo project. A harbormaster's ledger for invented berths, tides, and chandlery tabs. Screenshot data only.",
			epics: []epicSpec{
				{key: "HE-1", title: "Berth board", tickets: harborBerths()},
				{key: "HE-2", title: "Chandlery tabs", tickets: harborTabs()},
			},
		},
		{
			key: "NGLASS-1", route: "NGLASS", title: "North Glass",
			body: "Fictional demo project. A glassworks that anneals invented panes for the archive and the harbor. Screenshot data only.",
			epics: []epicSpec{
				{key: "NE-1", title: "Annealing floor", tickets: glassFloor()},
				{key: "NE-2", title: "Pane orders", tickets: glassOrders()},
			},
		},
	}
}

func lumenReading() []ticketSpec {
	return []ticketSpec{
		{key: "LT-1", title: "Write the reading-room brief", state: "in_progress", priority: "high", assignee: 1, rich: true, comment: "Fictional note: the brief stays in the demo tenant."},
		{key: "LT-2", title: "Mark the lantern hooks", state: "new", priority: "medium", assignee: 2},
		{key: "LT-3", title: "Oil the oak desks", state: "backlog", priority: "low", assignee: 1, comment: "Wait for the hooks. This comment is fictional."},
		{key: "LT-4", title: "Hang the dusk bell", state: "done", priority: "medium", assignee: 0,
			pillEN:    "Clear closing signal",
			pillDE:    "Klarer Hinweis zum Schließen",
			benefitEN: "The dusk bell makes closing time easy to notice.",
			benefitDE: "Die Abendglocke macht das Ende der Öffnungszeit leicht erkennbar.",
		},
	}
}

func lumenLanterns() []ticketSpec {
	return []ticketSpec{
		{key: "LT-5", title: "Count spare wicks", state: "in_progress", priority: "medium", assignee: 2},
		{key: "LT-6", title: "Label glass chimneys", state: "new", priority: "low", assignee: 1},
		{key: "LT-7", title: "Mend the brass collar", state: "backlog", priority: "high", assignee: 2, comment: "The collar is a prop."},
		{key: "LT-8", title: "Shelve unused globes", state: "cancelled", priority: "low", assignee: 0},
	}
}

func lumenQuiet() []ticketSpec {
	return []ticketSpec{
		{key: "LT-9", title: "Post quiet-hour hours", state: "done", priority: "medium", assignee: 1,
			pillEN:    "Quiet hours visible",
			pillDE:    "Ruhezeiten leicht finden",
			benefitEN: "Visitors can see when the reading room is quiet.",
			benefitDE: "Die Ruhezeiten im Leseraum sind leicht zu finden.",
		},
		{key: "LT-10", title: "Move the squeaky chair", state: "in_progress", priority: "low", assignee: 2},
		{key: "LT-11", title: "Test the felt door", state: "new", priority: "medium", assignee: 0},
		{key: "LT-12", title: "Write the whisper rule", state: "backlog", priority: "high", assignee: 1},
	}
}

func lumenCards() []ticketSpec {
	return []ticketSpec{
		{key: "LT-13", title: "Reprint faded cards", state: "new", priority: "medium", assignee: 2},
		{key: "LT-14", title: "Index the tide pamphlets", state: "in_progress", priority: "high", assignee: 1, comment: "Pamphlets are invented."},
		{key: "LT-15", title: "Retire duplicate cards", state: "done", priority: "low", assignee: 0,
			pillEN:    "A clearer catalog",
			pillDE:    "Ein übersichtlicher Katalog",
			benefitEN: "Each title has one catalog card, making books easier to find.",
			benefitDE: "Jeder Titel hat eine Katalogkarte, damit Bücher leichter zu finden sind.",
		},
		{key: "LT-16", title: "Stamp the fiction shelf", state: "backlog", priority: "medium", assignee: 2},
	}
}

func harborBerths() []ticketSpec {
	return []ticketSpec{
		{key: "HT-1", title: "Name the north berth", state: "new", priority: "high", assignee: 1},
		{key: "HT-2", title: "Paint berth numbers", state: "in_progress", priority: "medium", assignee: 2, comment: "Numbers are not a real quay."},
		{key: "HT-3", title: "Log a made-up tide", state: "backlog", priority: "low", assignee: 0},
		{key: "HT-4", title: "Coil the spare line", state: "done", priority: "low", assignee: 1,
			pillEN:    "Rope ready nearby",
			pillDE:    "Leine griffbereit verstaut",
			benefitEN: "The spare rope is ready to use and keeps the walkway clear.",
			benefitDE: "Die Ersatzleine ist griffbereit und der Gehweg bleibt frei.",
		},
		{key: "HT-5", title: "Check the gangway lamp", state: "new", priority: "medium", assignee: 2},
		{key: "HT-6", title: "Close the storm book", state: "cancelled", priority: "low", assignee: 0},
	}
}

func harborTabs() []ticketSpec {
	return []ticketSpec{
		{key: "HT-7", title: "Price fictional rope", state: "in_progress", priority: "high", assignee: 1},
		{key: "HT-8", title: "File the lamp oil tab", state: "new", priority: "medium", assignee: 2},
		{key: "HT-9", title: "Note a biscuit tin", state: "backlog", priority: "low", assignee: 0, comment: "The tin is not inventory."},
		{key: "HT-10", title: "Balance the chalk slate", state: "done", priority: "medium", assignee: 1,
			pillEN:    "Clear outstanding totals",
			pillDE:    "Offene Beträge überblicken",
			benefitEN: "The slate shows the amount still owed at a glance.",
			benefitDE: "Die Tafel zeigt auf einen Blick, welcher Betrag noch offen ist.",
		},
		{key: "HT-11", title: "Archive last month's ink", state: "new", priority: "low", assignee: 2},
		{key: "HT-12", title: "Stamp paid on a sample", state: "in_progress", priority: "medium", assignee: 0},
	}
}

func glassFloor() []ticketSpec {
	return []ticketSpec{
		{key: "NT-1", title: "Heat the annealer", state: "in_progress", priority: "high", assignee: 2},
		{key: "NT-2", title: "Sweep cullet into the bin", state: "new", priority: "low", assignee: 1},
		{key: "NT-3", title: "Record a fictional pour", state: "backlog", priority: "medium", assignee: 0, comment: "No furnace was lit."},
		{key: "NT-4", title: "Cool the sample pane", state: "done", priority: "medium", assignee: 2,
			pillEN:    "Sample ready safely",
			pillDE:    "Muster sicher bereit",
			benefitEN: "The cooled sample is ready for inspection.",
			benefitDE: "Das abgekühlte Muster steht für die Prüfung bereit.",
		},
		{key: "NT-5", title: "Label the north rack", state: "new", priority: "low", assignee: 1},
		{key: "NT-6", title: "Retire a cracked prop", state: "cancelled", priority: "low", assignee: 0},
	}
}

func glassOrders() []ticketSpec {
	return []ticketSpec{
		{key: "NT-7", title: "Cut a pane for Lumen", state: "in_progress", priority: "high", assignee: 1},
		{key: "NT-8", title: "Edge a harbor window", state: "new", priority: "medium", assignee: 2},
		{key: "NT-9", title: "Pack straw for transit", state: "backlog", priority: "low", assignee: 0},
		{key: "NT-10", title: "Write the pane ticket", state: "done", priority: "medium", assignee: 1,
			pillEN:    "Orders clearly identified",
			pillDE:    "Aufträge eindeutig zuordnen",
			benefitEN: "The label makes each pane easy to match to its order.",
			benefitDE: "Die Beschriftung erleichtert die Zuordnung jeder Scheibe zum Auftrag.",
		},
		{key: "NT-11", title: "Match a green tint", state: "new", priority: "medium", assignee: 2, comment: "The tint is a story."},
		{key: "NT-12", title: "Shelve the spare circle", state: "in_progress", priority: "low", assignee: 0},
	}
}

func richBody() string {
	return `Fictional reading-room brief for screenshots. No library, patron, or building described here is real.

## What the room is for

Visitors borrow a lantern, sit at an oak desk, and read tide pamphlets that exist only in this demo. The room closes when the dusk bell rings. See [[lumen-context]] and the procedure [[deploy-lumen]].

## Open questions

- Should the whisper rule live in [[review-bar]]?
- The glass chimneys come from [[north-glass]], which is another fictional project.
- Harbor rope prices stay in [[glass-ledger]] as an external name, not a system we operate.

## Sample card

` + "```" + `
LANTERN-014
chimney: clear
status: prop
` + "```" + `

This ticket is the rich context row for the demo. It is not a work item.
`
}

type knowledgeSpec struct {
	typ, slug, title, body string
	meta                   map[string]any
}

func knowledgeEntries() []knowledgeSpec {
	return []knowledgeSpec{
		{typ: "runbook", slug: "deploy-lumen", title: "Open the reading room", body: "Fictional procedure. Light the desk lamps, then hang the dusk bell. The whisper rule is [[review-bar]]. Context sits in [[lumen-context]]."},
		{typ: "runbook", slug: "incident-lumen", title: "If a lantern smokes", body: "Fictional incident note. Snuff the wick and open the felt door. Tell the harbor desk described in [[harbor-note]]."},
		{typ: "guideline", slug: "review-bar", title: "Whisper rule", body: "Fictional guideline. Read aloud only to point at a card. Related procedure: [[deploy-lumen]]."},
		{typ: "guideline", slug: "naming-lumen", title: "How cards are named", body: "Fictional naming rule. Prefix a prop with its shelf. The archive context is [[lumen-context]]."},
		{typ: "memory", slug: "lumen-context", title: "What Lumen is", body: "Fictional memory. Lumen Archive lends lanterns. It is not a customer. North Glass cuts its panes: [[north-glass]]."},
		{typ: "memory", slug: "harbor-note", title: "Harbor desk habit", body: "Fictional memory. The harbormaster writes tabs in chalk. See [[incident-lumen]] if a lantern fails during a delivery."},
		{typ: "external-system", slug: "glass-ledger", title: "Glass ledger name", body: "Fictional external name for a book that does not exist. The archive mentions it from [[lumen-context]].", meta: map[string]any{"url": "https://ledger.example.invalid/glass"}},
		{typ: "related-project", slug: "north-glass", title: "North Glass", body: "Fictional related project. It anneals panes for [[deploy-lumen]] and is not a separate company."},
	}
}
