// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type RoutineTemplateCopy struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Goal        string   `json:"goal"`
	Criteria    []string `json:"acceptance_criteria"`
	Pill        string   `json:"pill"`
	Benefit     string   `json:"benefit"`
}

type RoutineTemplatePreset struct {
	ID                    string              `json:"id"`
	Version               string              `json:"version"`
	EN                    RoutineTemplateCopy `json:"en"`
	DE                    RoutineTemplateCopy `json:"de"`
	Scope                 string              `json:"scope"`
	RequiredConfiguration []string            `json:"required_configuration"`
	Trigger               Trigger             `json:"trigger"`
	Role                  string              `json:"role"`
	AllowedActions        []string            `json:"allowed_actions"`
	RuntimeRequirements   RuntimeRequirements `json:"runtime_requirements"`
	Budget                DefinitionBudget    `json:"budget"`
	Paused                bool                `json:"paused"`
	ExecuteConsent        bool                `json:"execute_consent"`
}

// References belong to the installing tenant. A template never embeds an
// operator's UUID, account, host, model pin or production configuration.
type RoutineTemplateConfig struct {
	ProjectID        string            `json:"project_id"`
	ParentID         string            `json:"parent_id"`
	OwnerPrincipalID string            `json:"owner_principal_id"`
	WorkKindID       string            `json:"work_kind_id"`
	Language         string            `json:"language,omitempty"`
	SourceProjectID  string            `json:"source_project_id,omitempty"`
	Sources          []SourceReference `json:"sources,omitempty"`
	Tags             []string          `json:"tags,omitempty"`
	Trigger          *Trigger          `json:"trigger,omitempty"`
	Budget           *DefinitionBudget `json:"budget,omitempty"`
}

type RoutineTemplatePreview struct {
	Input   Input `json:"input"`
	Preview struct {
		Times           []time.Time      `json:"times"`
		TriggerKind     string           `json:"trigger_kind"`
		ExecutionPolicy *ExecutionPolicy `json:"execution_policy"`
	} `json:"preview"`
}

// These are immutable v1 definitions. Behaviour/copy changes require a new
// version while retaining old versions for reproducible preview and retry.
// Fresh values on every call prevent a consumer from mutating the catalogue.
func routineTemplates() []RoutineTemplatePreset {
	out := []RoutineTemplatePreset{
		{
			ID: "weekly-cleanup", Role: "build",
			Trigger: Trigger{Kind: "time", RRULE: "FREQ=WEEKLY;BYDAY=MO", TimeOfDay: "09:00", Timezone: "Europe/Vienna"},
			EN: RoutineTemplateCopy{
				Name: "Weekly cleanup", Title: "Weekly cleanup {{date}} · {{occurrence}}",
				Goal:        "Inspect current repository health, deduplicate verified findings and perform already permitted reversible local cleanup in the assigned workspace. Run affected checks and produce a local commit or draft PR when needed.",
				Description: "Project routine owned by a canonical person administrator. Read the supplied repository health and policy sources. Output: a work leaf with routine provenance, evidence-linked findings, a bounded knowledge report and, when needed, a local commit or draft PR. Use role build and the stronger existing route for security paths; use a qualified native Codex builder on an owner-allowed registered Mac. Already permitted reversible local work needs no extra human gate. Critical changes follow the effective evaluation policy; coordinator different-family review, CI and repository/person shipping gates remain. Secret access, changes to own rules or gates, force push and automatic deployment are prohibited. Test deletion, skips or weakened assertions require an accepted person-approved note. Log actions, approvals, costs and unknown results; an unchanged repository is an honest no-change result.",
				Criteria: []string{
					"Each finding links concrete evidence and reuses an existing work item when the finding is already tracked.",
					"Only already permitted reversible cleanup is performed locally; affected normal checks pass before a local commit or draft PR is reported as ready.",
					"Coordinator different-family review and current repository/person pipeline gates remain; no automatic deployment or own-policy edit occurs.",
					"Tests are retained unless an accepted person-approved note authorizes deletion or weakening; actions, cost, approvals and unresolved effects are logged, including honest no-change completion.",
				},
				Pill: "Repository health checked", Benefit: "Repository findings and permitted cleanup are recorded with evidence and the required checks.",
			},
			DE: RoutineTemplateCopy{
				Name: "Wöchentliche Bereinigung", Title: "Wöchentliche Bereinigung {{date}} · {{occurrence}}",
				Goal:        "Den aktuellen Repository-Zustand prüfen, belegte Befunde deduplizieren und bereits erlaubte reversible lokale Bereinigungen im zugewiesenen Arbeitsbereich ausführen. Betroffene Prüfungen ausführen und bei Bedarf einen lokalen Commit oder Entwurfs-PR erstellen.",
				Description: "Projektroutine im Besitz einer kanonischen administrierenden Person. Die bereitgestellten Quellen zum Repository-Zustand und zu den Regeln lesen. Ausgabe: ein Arbeitsblatt mit Routinenherkunft, belegte Befunde, ein begrenzter Wissensbericht und bei Bedarf ein lokaler Commit oder Entwurfs-PR. Rolle build verwenden; für Sicherheitspfade bleibt die stärkere bestehende Route erhalten. Der native Codex-Builder benötigt einen qualifizierten, registrierten und vom Eigentümer zugelassenen Mac. Bereits erlaubte reversible lokale Arbeit benötigt keine zusätzliche menschliche Freigabe. Kritische Änderungen folgen der wirksamen Bewertungsrichtlinie; Koordinator-Review durch eine andere Modellfamilie, CI sowie Repository- und Personenfreigaben im Lieferprozess bleiben erhalten. Geheimniszugriff, Änderungen an eigenen Regeln oder Freigaben, Force-Push und automatische Bereitstellung sind untersagt. Testlöschungen, übersprungene Tests und abgeschwächte Assertions benötigen eine akzeptierte personenbezogene Freigabenotiz. Aktionen, Freigaben, Kosten und unbekannte Ergebnisse protokollieren; ein unverändertes Repository wird ehrlich als Ergebnis ohne Änderung gemeldet.",
				Criteria: []string{
					"Jeder Befund enthält konkrete Belege; bereits erfasste Befunde verwenden den vorhandenen Arbeitseintrag.",
					"Lokal erfolgen nur bereits erlaubte reversible Bereinigungen; betroffene reguläre Prüfungen bestehen vor der Bereitschaftsmeldung für Commit oder Entwurfs-PR.",
					"Koordinator-Review durch eine andere Modellfamilie sowie bestehende Repository- und Personenfreigaben bleiben erhalten; keine automatische Bereitstellung oder Änderung eigener Regeln.",
					"Tests bleiben ohne akzeptierte personenbezogene Freigabenotiz erhalten; Aktionen, Kosten, Freigaben und offene Wirkungen werden einschließlich eines ehrlichen Ergebnisses ohne Änderung protokolliert.",
				},
				Pill: "Repository-Zustand geprüft", Benefit: "Repository-Befunde und erlaubte Bereinigungen werden mit Belegen und den erforderlichen Prüfungen dokumentiert.",
			},
		},
		{
			ID: "test-review", Role: "build-hard",
			Trigger: Trigger{Kind: "time", RRULE: "FREQ=WEEKLY;BYDAY=TU", TimeOfDay: "09:00", Timezone: "Europe/Vienna"},
			EN: RoutineTemplateCopy{
				Name: "Test review", Title: "Test review {{date}} · {{occurrence}}",
				Goal:        "Map real risks and existing guards using the current test manifests, agent-test-policy and accepted AEON-678/679 decisions. Identify essential core versus gated-full/nightly coverage and draft deduplicated improvements.",
				Description: "Project routine owned by a canonical person administrator. Supply the current test-policy knowledge sources. Output: a work leaf with routine provenance, risk map, bounded knowledge/report drafts and deduplicated proposals. Use build-hard for native Codex analysis/build on a qualified registered Mac; the coordinator owns the different-family gate. PR and pipeline requests are allowed only for already approved changes. Test deletion, reclassification, skips and weakened assertions require an accepted person-approved note linked to the change. Preserve current manifests and policy until acceptance; never approve the routine's own recommendation or edit its own policy. No bulk deletion, learning, force push or automatic deployment. Log retained risk, existing guard, decisions, checks, actions and cost.",
				Criteria: []string{
					"Every proposed removal or reclassification names the retained risk and its existing guard and is deduplicated against current work.",
					"Current manifests, assertions and policy are preserved until an accepted person-approved note authorizes the specific change; the routine cannot approve itself.",
					"Only approved small changes reach a PR or pipeline request, validated through normal tiers and coordinator different-family review.",
					"The run reports a risk map and proposals with evidence, decisions, checks and costs; it performs no v1 learning or automatic deployment.",
				},
				Pill: "Test risks mapped", Benefit: "Test coverage proposals identify the risk and existing guard before any approved change.",
			},
			DE: RoutineTemplateCopy{
				Name: "Testprüfung", Title: "Testprüfung {{date}} · {{occurrence}}",
				Goal:        "Reale Risiken und bestehende Sicherungen anhand der aktuellen Testmanifeste, agent-test-policy und akzeptierten Entscheidungen AEON-678/679 zuordnen. Essenziellen Kern von gated-full/nightly unterscheiden und deduplizierte Verbesserungen entwerfen.",
				Description: "Projektroutine im Besitz einer kanonischen administrierenden Person. Aktuelle Wissensquellen zur Testrichtlinie bereitstellen. Ausgabe: ein Arbeitsblatt mit Routinenherkunft, Risikokarte, begrenzte Wissens- und Berichtsentwürfe sowie deduplizierte Vorschläge. Rolle build-hard für native Codex-Analyse und Umsetzung auf einem qualifizierten registrierten Mac; das Review durch eine andere Modellfamilie liegt beim Koordinator. PR- und Lieferprozess-Anfragen sind nur für bereits genehmigte Änderungen erlaubt. Testlöschung, Neueinstufung, übersprungene Tests und abgeschwächte Assertions benötigen eine akzeptierte personenbezogene Freigabenotiz zur jeweiligen Änderung. Aktuelle Manifeste und Regeln bleiben bis zur Annahme erhalten; eigene Empfehlungen dürfen nicht selbst genehmigt und eigene Regeln nicht geändert werden. Keine Massenlöschung, Lernfunktion, Force-Push oder automatische Bereitstellung. Erhaltenes Risiko, bestehende Sicherung, Entscheidungen, Prüfungen, Aktionen und Kosten protokollieren.",
				Criteria: []string{
					"Jede vorgeschlagene Entfernung oder Neueinstufung benennt das erhaltene Risiko und seine bestehende Sicherung und wird mit aktuellen Arbeitseinträgen dedupliziert.",
					"Aktuelle Manifeste, Assertions und Regeln bleiben bis zur akzeptierten personenbezogenen Freigabenotiz für die konkrete Änderung erhalten; keine Selbstfreigabe.",
					"Nur genehmigte kleine Änderungen führen zu einem PR oder einer Lieferprozess-Anfrage, geprüft durch reguläre Teststufen und Koordinator-Review einer anderen Modellfamilie.",
					"Der Lauf meldet Risikokarte und Vorschläge mit Belegen, Entscheidungen, Prüfungen und Kosten; keine Lernfunktion in v1 oder automatische Bereitstellung.",
				},
				Pill: "Testrisiken zugeordnet", Benefit: "Vorschläge zur Testabdeckung benennen Risiko und bestehende Sicherung vor einer genehmigten Änderung.",
			},
		},
		{
			ID: "website-release-update", Role: "build-hard",
			Trigger: Trigger{Kind: "event", Event: "release.published", EventStart: "now"},
			EN: RoutineTemplateCopy{
				Name: "Website release update", Title: "Website draft for {{release_name}} {{release_version}} · {{occurrence}}",
				Goal:        "Inspect the exact authorized PAIMOS publication, codename, canonical version and approved release copy. Compare live sites and source at the deployed SHA against the supplied INSPR-541/RUN-12 and GUI-22 sources. Draft only mechanical release-pin and evidence-backed planned-to-live corrections in the owner-approved site workspace.",
				Description: "Install explicitly in the INSPR output/definition project under the supplied INSPR-521 parent, owned by its responsible project owner/person; subscribe to the supplied AEON source project in the same tenant. Reuse the existing INSPR-541 weekly audit as a companion without overwriting or duplicating it. Output: a work leaf with routine provenance, per-site report and deduplicated findings, run-bound screenshot artifacts and a scoped draft PR through the action catalogue. Deduplicate by URL and claim. Resolve scout for crawl/extract, build-hard for judgement and review-gate through the coordinator. Codex builds; the qualified native Claude capture broker handles read-only Chrome outside the Codex sandbox on an owner-allowed registered Mac. Bound captures to approved domains, pages, bytes and time. Publication is evidence, not approval for unsupported product claims. Preserve the current house vocabulary and retired-feature boundaries. Observe/draft phase remains. No autonomous site deployment: the target/head-bound owner Decision Desk GO and OPS deployment/live-check remain required; a merged draft alone is never live. Log source project/release ID/version, actions, costs and approvals. Unsupported runtime or budget qualification must visibly refuse start.",
				Criteria: []string{
					"Each authorized publication retains a distinct eventual receipt even during overlap; publication replay and manual selection share the existing release receipt identity.",
					"The report identifies source project, release ID/version and per-site evidence; URL/claim deduplication reuses findings, and screenshot artifacts and draft PR bind to the run.",
					"Claims match released evidence and approved copy; only mechanical authorized site drafts are made, without overwriting the companion weekly audit.",
					"Coordinator review and target/head-bound owner Decision Desk GO precede OPS deployment/live-check; this routine never deploys or reports a merged draft as live.",
				},
				Pill: "Website release drafted", Benefit: "Website release evidence and proposed corrections are prepared for owner review and the existing site delivery process.",
			},
			DE: RoutineTemplateCopy{
				Name: "Website-Aktualisierung zur Veröffentlichung", Title: "Website-Entwurf für {{release_name}} {{release_version}} · {{occurrence}}",
				Goal:        "Die genaue autorisierte PAIMOS-Veröffentlichung, den Codenamen, die kanonische Version und genehmigte Veröffentlichungstexte prüfen. Live-Websites und Quellen am bereitgestellten SHA mit den Quellen INSPR-541/RUN-12 und GUI-22 vergleichen. Nur mechanische Versionsanpassungen und belegte Änderungen von geplant zu live im genehmigten Website-Arbeitsbereich entwerfen.",
				Description: "Explizit im INSPR-Ausgabe- und Definitionsprojekt unter dem bereitgestellten Elternknoten INSPR-521 installieren, im Besitz der verantwortlichen Projekteigentümerperson; das bereitgestellte AEON-Quellprojekt im selben Mandanten abonnieren. Die bestehende wöchentliche Prüfung INSPR-541 als Begleitung weiterverwenden, ohne Überschreiben oder Duplizieren. Ausgabe: ein Arbeitsblatt mit Routinenherkunft, Bericht je Website und deduplizierten Befunden, laufgebundene Screenshot-Artefakte und ein eingegrenzter Entwurfs-PR über den Aktionskatalog. Nach URL und Aussage deduplizieren. Rollen scout für Erfassung, build-hard für Bewertung und review-gate über den Koordinator auflösen. Codex setzt um; der qualifizierte native Claude-Erfassungsbroker bedient Chrome ausschließlich lesend außerhalb der Codex-Sandbox auf einem zugelassenen registrierten Mac. Erfassung auf genehmigte Domains, Seiten, Bytes und Zeit begrenzen. Eine Veröffentlichung belegt Fakten, genehmigt aber keine unbelegten Produktversprechen. Aktuelles Hausvokabular und Grenzen stillgelegter Funktionen erhalten. Die Beobachtungs- und Entwurfsphase bleibt bestehen. Keine autonome Website-Bereitstellung: eine ziel- und headgebundene GO-Entscheidung der Eigentümerperson im Decision Desk sowie OPS-Bereitstellung und Live-Prüfung bleiben erforderlich; ein gemergter Entwurf gilt nicht als live. Quellprojekt, Veröffentlichungs-ID/Version, Aktionen, Kosten und Freigaben protokollieren. Fehlende Laufzeit- oder Budgetqualifikation verweigert den Start sichtbar.",
				Criteria: []string{
					"Jede autorisierte Veröffentlichung behält auch bei Überlappung einen eigenen späteren Beleg; Wiederholung und manuelle Auswahl verwenden dieselbe bestehende Veröffentlichungsidentität.",
					"Der Bericht enthält Quellprojekt, Veröffentlichungs-ID/Version und Belege je Website; URL/Aussage-Deduplizierung verwendet bestehende Befunde, Screenshot-Artefakte und Entwurfs-PR sind an den Lauf gebunden.",
					"Aussagen entsprechen veröffentlichten Belegen und genehmigten Texten; nur mechanische autorisierte Website-Entwürfe, ohne Überschreiben der begleitenden Wochenprüfung.",
					"Koordinator-Review und ziel- und headgebundenes GO der Eigentümerperson im Decision Desk gehen OPS-Bereitstellung und Live-Prüfung voraus; diese Routine stellt niemals bereit und meldet keinen gemergten Entwurf als live.",
				},
				Pill: "Website-Version vorbereitet", Benefit: "Website-Belege und vorgeschlagene Korrekturen werden für die Eigentümerprüfung und den bestehenden Lieferprozess vorbereitet.",
			},
		},
	}
	for i := range out {
		p := &out[i]
		p.Version, p.Scope, p.Paused = "1", "project", true
		p.RequiredConfiguration = []string{"project_id", "parent_id", "owner_principal_id", "work_kind_id"}
		p.AllowedActions = []string{"work.create", "work.update", "knowledge.write", "pr.open", "pipeline.request"}
		p.Budget = DefinitionBudget{Mode: "off"}
		p.RuntimeRequirements = RuntimeRequirements{NeedsNativeHost: true, RuntimeClass: "native_coding"}
		if p.ID == "website-release-update" {
			p.RequiredConfiguration = append(p.RequiredConfiguration, "source_project_id", "sources")
			p.RuntimeRequirements.NeedsBrowser = true
			p.RuntimeRequirements.RuntimeClass = "native_browser"
		}
	}
	return out
}

func routineTemplate(id, version string) (RoutineTemplatePreset, error) {
	for _, p := range routineTemplates() {
		if p.ID == id && p.Version == version {
			return p, nil
		}
	}
	return RoutineTemplatePreset{}, workorders.Fail(404, "unknown routine template version")
}

func (p RoutineTemplatePreset) input(c RoutineTemplateConfig) (Input, error) {
	if len(c.Sources) > 32 || len(c.Tags) > 50 {
		return Input{}, workorders.Fail(400, "template configuration exceeds limits")
	}
	copy := p.EN
	switch c.Language {
	case "", "en":
	case "de":
		copy = p.DE
	default:
		return Input{}, workorders.Fail(400, "template language must be en or de")
	}
	hidden := true
	in := Input{ProjectID: c.ProjectID, ParentID: c.ParentID,
		Template: Template{Name: copy.Name + " (v" + p.Version + ")", Title: copy.Title, Description: copy.Description, Criteria: copy.Criteria,
			EstimateHours: 2, Priority: "medium", Tags: append([]string{}, c.Tags...), Type: "work", PillEN: p.EN.Pill, PillDE: p.DE.Pill,
			BenefitEN: p.EN.Benefit, BenefitDE: p.DE.Benefit, HideFromReleaseNotes: &hidden},
		Trigger: p.Trigger, OverlapPolicy: "skip", CatchUpPolicy: "one",
		Definition: &Definition{Scope: DefinitionScope{Kind: "project", ProjectID: c.ProjectID}, OwnerPrincipalID: c.OwnerPrincipalID,
			Assignment: &Assignment{Goal: copy.Goal, Role: p.Role, WorkKindID: c.WorkKindID, Sources: append([]SourceReference{}, c.Sources...),
				AllowedActions: p.AllowedActions, RuntimeRequirements: p.RuntimeRequirements, Budget: p.Budget}},
	}
	if c.Budget != nil {
		in.Definition.Assignment.Budget = *c.Budget
	}
	if p.ID == "website-release-update" {
		if !workorders.UUID(c.SourceProjectID) || strings.EqualFold(c.SourceProjectID, c.ProjectID) || c.Trigger != nil {
			return Input{}, workorders.Fail(400, "website template requires a distinct source project and its fixed release trigger")
		}
		knowledge := false
		for _, s := range c.Sources {
			knowledge = knowledge || s.Kind == "knowledge"
		}
		if !knowledge {
			return Input{}, workorders.Fail(400, "website template requires accepted site-process knowledge sources")
		}
		in.Trigger.Filter = &EventFilter{ProjectIDs: []string{c.SourceProjectID}}
	} else {
		if c.SourceProjectID != "" || c.Trigger != nil && c.Trigger.Kind != "time" {
			return Input{}, workorders.Fail(400, "scheduled templates require a time trigger and no release source project")
		}
		if c.Trigger != nil {
			in.Trigger = *c.Trigger
		}
	}
	for i := range in.Template.Tags {
		in.Template.Tags[i] = strings.ToLower(in.Template.Tags[i])
	}
	sort.Strings(in.Template.Tags)
	for i := range in.Definition.Assignment.Sources {
		in.Definition.Assignment.Sources[i].ID = strings.ToLower(in.Definition.Assignment.Sources[i].ID)
	}
	sort.Slice(in.Definition.Assignment.Sources, func(i, j int) bool {
		return in.Definition.Assignment.Sources[i].ID < in.Definition.Assignment.Sources[j].ID
	})
	return in, nil
}

func (m *Module) mountTemplates(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/recurrences/templates", m.listTemplates)
	mux.HandleFunc("POST /api/recurrences/templates/{templateId}/{templateVersion}/preview", m.previewTemplate)
	mux.HandleFunc("POST /api/recurrences/templates/{templateId}/{templateVersion}/apply", m.applyTemplate)
}
func (m *Module) listTemplates(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": routineTemplates()})
}
func templateRequest(w http.ResponseWriter, r *http.Request) (RoutineTemplatePreset, Input, bool) {
	p, err := routineTemplate(r.PathValue("templateId"), r.PathValue("templateVersion"))
	if err != nil {
		reply(w, 404, nil, err)
		return p, Input{}, false
	}
	var c RoutineTemplateConfig
	if !decode(w, r, &c) {
		return p, Input{}, false
	}
	in, err := p.input(c)
	if err != nil {
		reply(w, 400, nil, err)
		return p, in, false
	}
	return p, in, true
}

func (m *Module) previewTemplate(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	_, in, ok := templateRequest(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var out RoutineTemplatePreview
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		now, err := m.clock(ctx, tx)
		if err != nil {
			return err
		}
		if err = in.normalize(now); err != nil {
			return workorders.Fail(400, err.Error())
		}
		if err = authorizeDefinition(ctx, tx, p, in); err != nil {
			return err
		}
		if err = validateTarget(ctx, tx, in, false); err != nil {
			return err
		}
		out.Input = in
		out.Preview.TriggerKind = in.Trigger.Kind
		out.Preview.Times, err = Preview(in.Trigger, now, 4)
		if err != nil {
			return err
		}
		item := Recurrence{Input: in}
		err = m.projectExecutionPolicy(ctx, tx, p.TenantID, &item)
		out.Preview.ExecutionPolicy = item.ExecutionPolicy
		return err
	})
	reply(w, 200, out, err)
}

// A UUIDv8 uses a SHA-256-derived tenant/version/config identity. The existing
// recurrence PK and tenant fence provide durable concurrent retry safety without
// a new installation table. An implicit calendar start never changes the key.
func templateRecurrenceID(tenantID string, preset RoutineTemplatePreset, in Input, implicitStart bool) string {
	if implicitStart {
		in.Trigger.StartDate = ""
	}
	raw, _ := json.Marshal(struct {
		Namespace, Tenant, Template, Version string
		Input                                Input
	}{"aeon-routine-template-v1", tenantID, preset.ID, preset.Version, in})
	h := sha256.Sum256(raw)
	h[6], h[8] = (h[6]&0x0f)|0x80, (h[8]&0x3f)|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

func (m *Module) applyTemplate(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	preset, in, ok := templateRequest(w, r)
	if !ok {
		return
	}
	implicitStart := in.Trigger.StartDate == ""
	var out Recurrence
	status := 201
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		if _, err := lock(ctx, tx, p.TenantID, false); err != nil {
			return err
		}
		now, err := m.clock(ctx, tx)
		if err != nil {
			return err
		}
		if err = in.normalize(now); err != nil {
			return workorders.Fail(400, err.Error())
		}
		if err = authorizeDefinition(ctx, tx, p, in); err != nil {
			return err
		}
		if err = validateTarget(ctx, tx, in, true); err != nil {
			return err
		}
		id := templateRecurrenceID(p.TenantID, preset, in, implicitStart)
		var retired bool
		err = tx.QueryRow(ctx, `SELECT retired_at IS NOT NULL FROM recurrences WHERE id=$1 FOR UPDATE`, id).Scan(&retired)
		if err == nil {
			if retired {
				return workorders.Fail(409, "template configuration was retired; change configuration explicitly to create another routine")
			}
			out, err = load(ctx, tx, id, false)
			if err != nil {
				return err
			}
			// A replay returns the current record; it never restores the template,
			// resets consent or substitutes the installer's former target rights.
			if err = authorizeDefinition(ctx, tx, p, out.Input); err != nil {
				return err
			}
			if err = validateTarget(ctx, tx, out.Input, true); err != nil {
				return err
			}
			status = 200
			return m.projectExecutionPolicy(ctx, tx, p.TenantID, &out)
		}
		if err != pgx.ErrNoRows {
			return err
		}
		out, err = m.insertRecurrenceTx(ctx, tx, p, in, now, id)
		return err
	})
	reply(w, status, out, m.recordGuardrailDenial(ctx, p, err))
}
