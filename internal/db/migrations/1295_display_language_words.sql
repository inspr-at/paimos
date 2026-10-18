-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-998: additive bilingual words. Existing English wording and user edits
-- remain untouched. This repo uses forward-only SQL migrations, no down phase.
SET LOCAL lock_timeout = '5s';
ALTER TABLE work_kinds ADD COLUMN words_de jsonb;
CREATE TABLE work_display_words (
 tenant_id uuid NOT NULL,
 subject_kind text NOT NULL,
 slug text NOT NULL,
 words_en jsonb NOT NULL,
 words_de jsonb NOT NULL,
 PRIMARY KEY (tenant_id, subject_kind, slug)
);
CREATE FUNCTION aeon_builtin_display_words() RETURNS TABLE(subject_kind text,slug text,words_en jsonb,words_de jsonb)
LANGUAGE sql IMMUTABLE AS $$
 SELECT v.subject_kind,v.slug,v.words_en::jsonb,v.words_de::jsonb FROM (VALUES
 ('work-kind', 'design', '{"label": "UI design", "hint": "Screens and interaction, designed as an HTML mock before any code.", "examples": ["A settings screen", "A ticket detail layout"]}', '{"label": "UI-Design", "hint": "Bildschirme und Interaktion, vor dem Code als HTML-Entwurf gestaltet.", "examples": ["Ein Einstellungsbildschirm", "Die Detailansicht eines Tickets"]}'),
 ('work-kind', 'frontend', '{"label": "Frontend build", "hint": "Vue code that implements an approved design.", "examples": ["A Vue component", "A board interaction"]}', '{"label": "Frontend-Build", "hint": "Vue-Code, der einen freigegebenen Entwurf umsetzt.", "examples": ["Eine Vue-Komponente", "Eine Interaktion auf dem Board"]}'),
 ('work-kind', 'backend', '{"label": "Backend build", "hint": "Go code, APIs, SQL and migrations behind the app.", "examples": ["An API handler", "A SQL migration"]}', '{"label": "Backend-Build", "hint": "Go-Code, APIs, SQL und Migrationen hinter der App.", "examples": ["Ein API-Handler", "Eine SQL-Migration"]}'),
 ('work-kind', 'full-stack', '{"label": "Full stack", "hint": "Both ends in one change.", "examples": ["An API and its screen"]}', '{"label": "Full Stack", "hint": "Frontend und Backend in einer Änderung.", "examples": ["Eine API und ihr Bildschirm"]}'),
 ('work-kind', 'infra', '{"label": "Infrastructure", "hint": "CI, Nix, hosting and deployments.", "examples": ["A CI check", "A Nix change"]}', '{"label": "Infrastruktur", "hint": "CI, Nix, Hosting und Bereitstellungen.", "examples": ["Eine CI-Prüfung", "Eine Nix-Änderung"]}'),
 ('work-kind', 'docs', '{"label": "Docs and copy", "hint": "Documentation and the words in PAIMOS.", "examples": ["A guide", "Release copy"]}', '{"label": "Dokumentation und Texte", "hint": "Dokumentation und die Texte in PAIMOS.", "examples": ["Eine Anleitung", "Release-Texte"]}'),
 ('work-kind', 'security', '{"label": "Security", "hint": "Permissions and isolation, with a security review.", "examples": ["An authorization check", "Tenant isolation"]}', '{"label": "Sicherheit", "hint": "Berechtigungen und Isolation mit einer Sicherheitsprüfung.", "examples": ["Eine Berechtigungsprüfung", "Mandantenisolation"]}'),
 ('work-kind', 'review', '{"label": "Reviews", "hint": "Checks finished work before it merges.", "examples": ["A code review"]}', '{"label": "Prüfungen", "hint": "Prüft fertige Arbeit vor dem Zusammenführen.", "examples": ["Eine Codeprüfung"]}'),
 ('work-kind', 'other', '{"label": "Everything else", "hint": "Any ticket no other kind describes, and every kind without its own column.", "examples": ["Tidy up a script", "A one-off data fix"]}', '{"label": "Alles andere", "hint": "Tickets, die keine andere Art beschreibt, und jede Art ohne eigene Spalte.", "examples": ["Ein Skript aufräumen", "Eine einmalige Datenkorrektur"]}'),
 ('situation', 'first', '{"label": "First build", "hint": "The first implementation of a ticket.", "examples": ["Build an approved screen"]}', '{"label": "Erster Build", "hint": "Die erste Umsetzung eines Tickets.", "examples": ["Einen freigegebenen Bildschirm umsetzen"]}'),
 ('situation', 'small', '{"label": "Small work", "hint": "Work at or below the small-work hours limit thinks one step less.", "examples": ["A small wording change"]}', '{"label": "Kleine Arbeit", "hint": "Arbeit bis zur Stundengrenze für kleine Arbeit denkt eine Stufe weniger.", "examples": ["Eine kleine Textänderung"]}'),
 ('situation', 'fix', '{"label": "Fix rounds", "hint": "The agent fixes findings from a review, up to the fix-round limit.", "examples": ["Fix a permission check"]}', '{"label": "Korrekturrunden", "hint": "Der Agent behebt Prüfbefunde bis zur Grenze für Korrekturrunden.", "examples": ["Eine Berechtigungsprüfung korrigieren"]}'),
 ('situation', 'stuck', '{"label": "Stuck", "hint": "Review still fails after the fix-round limit; another model family takes over.", "examples": ["Switch family after repeated findings"]}', '{"label": "Festgefahren", "hint": "Die Prüfung scheitert nach der Rundengrenze weiter; eine andere Modellfamilie übernimmt.", "examples": ["Nach wiederholten Befunden die Modellfamilie wechseln"]}'),
 ('situation', 'review', '{"label": "Review gate", "hint": "Checks finished work with a model from another family.", "examples": ["Review a completed change"]}', '{"label": "Prüf-Gate", "hint": "Prüft fertige Arbeit mit einem Modell aus einer anderen Familie.", "examples": ["Eine fertige Änderung prüfen"]}'),
 ('situation', 'concept', '{"label": "Concept on request", "hint": "Someone asks for a concept; PAIMOS never writes one on its own.", "examples": ["Compare implementation options"]}', '{"label": "Konzept auf Anfrage", "hint": "Jemand bittet um ein Konzept; PAIMOS schreibt nie von sich aus eines.", "examples": ["Umsetzungsoptionen vergleichen"]}')
 ) v(subject_kind,slug,words_en,words_de);
$$;
-- Seed the new table before FORCE RLS: no existing table or user text is changed.
INSERT INTO work_display_words(tenant_id,subject_kind,slug,words_en,words_de)
 SELECT t.id,w.* FROM tenants t CROSS JOIN aeon_builtin_display_words() w;
ALTER TABLE work_display_words ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_display_words FORCE ROW LEVEL SECURITY;
CREATE POLICY work_display_words_tenant ON work_display_words
 USING (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)
 WITH CHECK (tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid);
CREATE FUNCTION aeon_seed_tenant_display_words() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE prior text := current_setting('aeon.tenant_id',true);
BEGIN
 PERFORM set_config('aeon.tenant_id',NEW.id::text,true);
 INSERT INTO work_display_words(tenant_id,subject_kind,slug,words_en,words_de)
 SELECT NEW.id,w.* FROM aeon_builtin_display_words() w;
 PERFORM set_config('aeon.tenant_id',coalesce(prior,''),true);
 RETURN NEW;
END;
$$;
CREATE TRIGGER tenants_display_words AFTER INSERT ON tenants
 FOR EACH ROW EXECUTE FUNCTION aeon_seed_tenant_display_words();
