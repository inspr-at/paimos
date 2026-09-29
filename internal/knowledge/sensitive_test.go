// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"encoding/base64"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Synthetic credential shapes the round-2 review found missed. Assembled at
// run time; none is a real credential.
func credentialForms() map[string]string {
	return map[string]string{
		"short basic":          "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("demo:"+"fake")),
		"five char password":   "password=" + strings.Repeat("x", 5),
		"markdown bold label":  "**password**: " + "Synth" + "etic42!",
		"markdown colon bold":  "**Password:** " + "Synth" + "etic42!",
		"markdown code label":  "`password`: " + "Synth" + "etic42!",
		"quoted passphrase":    `password: "` + "test code words" + `"`,
		"single quoted phrase": `passphrase: '` + "correct horse battery" + `'`,
		"short mixed password": "password: " + "hunt" + "er2",
		"json secret":          `"client_secret": "` + "Zq8vT2mN" + "4kLp" + `"`,
		"key assignment":       "api_key=" + "abcd1234" + "efgh5678",
		"bearer":               "Authorization: Bearer " + "abcDEF1234" + "56ghiJKL789",
		"url credentials":      "https://deploy:" + "Hunter2x" + "@git.example.com/repo",
		"slashed base64":       "key k3J9/aZ8qL2mN7pX" + "4vB1cD6fG0hT5rY8wE2uI9oP here",
		"github token":         "leaked " + fakeGitHubToken(),
	}
}

func TestSensitiveCredentialForms(t *testing.T) {
	for name, text := range credentialForms() {
		t.Run(name, func(t *testing.T) {
			if !looksSensitive(text) {
				t.Error("not detected")
			}
		})
	}
}

// A label alone never triggers, and file names and paths are never
// credentials: the reviewer's false positives plus ordinary prose.
func TestSensitiveProseIsClean(t *testing.T) {
	prose := []string{
		"The token: refresh it before retrying.",
		"Password: rotation must happen before the deploy.",
		"Secret: never store credentials in comments.",
		"Use API_KEY=${API_KEY} in the config example.",
		"See web/src/components/knowledge/MethodLearningsV2.vue",
		"Check internal/knowledge/sensitive.go and Foo.vue before changing the detector.",
		"Rotate the API key before the release and update the vault entry.",
		"The password reset flow sends a token by mail; it expires after an hour.",
		"Never paste a secret into a ticket comment.",
		"Token refresh failed twice, so the agent retried with backoff.",
		"Keys live in 1Password, not in the repository.",
		"Password: see the vault entry for the staging database.",
		"The access token is short-lived and the refresh token lasts a week.",
		"Store the private key in the signing vault, never on the build host.",
		"Secret rotation: done for OPS-240 on Tuesday.",
		"Token: OPS-240 tracks the rotation.",
		"The key: keep secrets out of logs.",
		"Set token=${GITHUB_TOKEN} in the workflow, never the value itself.",
		"Passphrase prompts block the CI job; use an agent socket instead.",
		"Auth uses a Bearer token from Zitadel; Basic auth is disabled.",
		"Password: Must be rotated every 90 days.",
		"token: GITHUB_TOKEN from the CI secrets.",
		"Secret: short-lived, rotate it.",
		"api_key: see <your key> in the README.",
		"The password: ******** placeholder confused the reviewer.",
		"Secret key: rotated on 2026-09-29 after the incident.",
		"HTTPServerConfigurationManagerFactoryV2 handles bearer auth now.",
		"Moved web/src/components/knowledge/KnowledgeEntryPage.vue to the new layout.",
	}
	for _, text := range prose {
		if spans := sensitiveSpans(text); len(spans) > 0 {
			t.Errorf("flagged %q at %v", text, spans)
		}
	}
}

// Ranges are code point positions of the value, never the label.
func TestSensitiveRanges(t *testing.T) {
	text := "Grüße: password=" + "Winter2026!" + " then more"
	got := sensitiveRanges("text", text)
	if len(got) != 1 {
		t.Fatalf("ranges %v", got)
	}
	runes := []rune(text)
	if got[0].Field != "text" || string(runes[got[0].Start:got[0].End]) != "Winter2026!" {
		t.Fatalf("range %v", got[0])
	}
	if sensitiveRanges("title", "nothing here") == nil || len(sensitiveRanges("title", "nothing here")) != 0 {
		t.Fatal("clean text must give an empty list")
	}
}

// Bare random keys are still caught although paths and identifiers are
// exempt: the exemption must not open a hole for keys that happen to read
// like words. Seeded, so the result is stable.
func TestSensitiveRandomKeys(t *testing.T) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	r := rand.New(rand.NewPCG(288, 3))
	missed := 0
	for n := 32; n <= 64; n += 8 {
		for range 2000 {
			key := make([]byte, n)
			for i := range key {
				key[i] = alphabet[r.IntN(len(alphabet))]
			}
			if !looksSensitive("pasted " + string(key) + " into chat") {
				missed++
			}
		}
	}
	if missed > 5 {
		t.Fatalf("missed %d of 10000 random keys", missed)
	}
}

// Long identifiers and paths with capitals and digits read as code.
func TestSensitiveIdentifiersAreClean(t *testing.T) {
	for _, text := range []string{
		"HTTPServerConfigurationManagerFactoryV2",
		"internal/db/migrations/0952_learning_rule_drafts.sql",
		"web/src/components/knowledge/MethodLearningsV2",
		"TestRound2CredentialCopyPathsWithMarkdownLabels",
		"AeonWorktrees/aeon-288-learning-drafts/InternalKnowledge",
		"useMethodLearningsInboxForProjectV3WithDraftRules",
	} {
		if looksSensitive(text) {
			t.Errorf("flagged %q", text)
		}
	}
}

type sensitiveProblem struct {
	Code   string           `json:"code"`
	Error  string           `json:"error"`
	Ranges []SensitiveRange `json:"ranges"`
}

func eventConfirmed(t *testing.T, f fixture, id int64) string {
	t.Helper()
	var confirmed *string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT after->>'confirmed_not_sensitive' FROM events WHERE tenant_id=$1 AND id=$2`, f.a.TenantID, id).Scan(&confirmed); err != nil {
		t.Fatal(err)
	}
	if confirmed == nil {
		return ""
	}
	return *confirmed
}

// The four forms the round-2 review found: never nominated automatically; a
// person's own tag answers 409 with positions only, and a person's
// confirmation goes through and is recorded in the event.
func TestSensitiveCopyPaths(t *testing.T) {
	f := setup(t)
	h := rulesHandler(f)
	layer := rulesLayer(t, h, f.b, rules.Scope{Layer: "person", OwnerID: f.b.ID})
	set := rulesSet(t, h, f.b, layer.ID, "Sensitive cases")
	book := createEntry(t, f, f.b, map[string]any{"type": "runbook", "slug": "sensitive", "title": "Sensitive", "body": "Notes\n"})
	forms := credentialForms()
	for i, name := range []string{"short basic", "five char password", "markdown bold label", "quoted passphrase"} {
		text := forms[name]
		t.Run(name, func(t *testing.T) {
			host := addNode(t, f, "SENS-"+strconv.Itoa(i+1), "ticket", "Sensitive host", &f.project)
			auto := addComment(t, f, host, "Incident: "+text)
			if _, err := TagOnce(t.Context(), f.db.App); err != nil {
				t.Fatal(err)
			}
			if _, _, ok := nominationOfKey(t, f, commentLearningID(host, auto)); ok {
				t.Fatal("stored a credential candidate")
			}
			for _, action := range []string{"accept", "draft"} {
				body := "#process-learning " + text
				id := commentLearningID(host, addComment(t, f, host, body))
				request := map[string]any{"knowledge_id": book.ID}
				if action == "draft" {
					request = map[string]any{"layer_id": layer.ID, "set_id": set.ID}
				}
				w := call(t, f, f.b, "POST", "/api/knowledge/learnings/"+id+"/"+action, request)
				expect(t, w, 409)
				problem := decode[sensitiveProblem](t, w)
				learning := []rune(strings.TrimSpace(strings.TrimPrefix(body, "#process-learning")))
				if problem.Code != "learning_sensitive" || len(problem.Ranges) == 0 {
					t.Fatalf("%s problem %+v", action, problem)
				}
				for _, r := range problem.Ranges {
					if r.Field != "text" || r.Start < 0 || r.End <= r.Start || r.End > len(learning) {
						t.Fatalf("%s range %+v of %d", action, r, len(learning))
					}
				}
				if strings.Contains(w.Body.String(), strings.TrimPrefix(text, "Authorization: ")) {
					t.Fatalf("%s echoed the text", action)
				}
				if decisionOf(t, f, id) != "" {
					t.Fatalf("%s decided a refused learning", action)
				}
				request["confirm_not_sensitive"] = true
				w = call(t, f, f.b, "POST", "/api/knowledge/learnings/"+id+"/"+action, request)
				expect(t, w, 200)
				decision := decode[LearningDecision](t, w)
				if eventConfirmed(t, f, decision.EventID) != "true" {
					t.Fatalf("%s event does not record the confirmation", action)
				}
				expect(t, call(t, f, f.b, "POST", "/api/events/"+strconv.FormatInt(decision.EventID, 10)+"/undo", nil), 201)
				if decisionOf(t, f, id) != "" {
					t.Fatalf("%s undo left the decision", action)
				}
			}
		})
	}
}

// Normal text is copied without a warning, and nothing is recorded.
func TestSensitiveProseManual(t *testing.T) {
	f := setup(t)
	h := rulesHandler(f)
	layer := rulesLayer(t, h, f.b, rules.Scope{Layer: "person", OwnerID: f.b.ID})
	set := rulesSet(t, h, f.b, layer.ID, "Normal prose")
	book := createEntry(t, f, f.b, map[string]any{"type": "runbook", "slug": "prose", "title": "Prose", "body": "Notes\n"})
	for _, action := range []string{"accept", "draft"} {
		for _, text := range []string{"See web/src/components/knowledge/MethodLearningsV2.vue", "The token: refresh it before retrying."} {
			id := commentLearningID(f.ticket, addComment(t, f, f.ticket, "#process-learning "+text))
			request := map[string]any{"knowledge_id": book.ID}
			if action == "draft" {
				request = map[string]any{"layer_id": layer.ID, "set_id": set.ID}
			}
			w := call(t, f, f.b, "POST", "/api/knowledge/learnings/"+id+"/"+action, request)
			expect(t, w, 200)
			if got := eventConfirmed(t, f, decode[LearningDecision](t, w).EventID); got != "" {
				t.Fatalf("%s %q recorded a confirmation %q", action, text, got)
			}
			book = createEntry(t, f, f.b, map[string]any{"type": "runbook", "slug": "prose-" + action + strconv.Itoa(len(text)), "title": "Prose", "body": "Notes\n"})
		}
	}
}

// Only a person can confirm; an agent's confirmation is refused, and the
// flag must be a boolean.
func TestSensitiveConfirmNeedsPerson(t *testing.T) {
	f := setup(t)
	book := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "confirm", "title": "Confirm", "body": "Notes\n"})
	id := commentLearningID(f.ticket, addComment(t, f, f.ticket, "#process-learning password="+strings.Repeat("x", 5)))
	agent := tenant.Principal{TenantID: f.a.TenantID, Kind: tenant.Agent, Name: "Scout", Roles: []string{"owner"}, Scopes: []string{"knowledge.write"}}
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'agent',$2,$3) RETURNING id::text`, f.a.TenantID, agent.Name, agent.Roles).Scan(&agent.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.db, f.a.TenantID, agent.ID, "owner")
	for _, action := range []string{"accept", "draft"} {
		request := map[string]any{"knowledge_id": book.ID, "confirm_not_sensitive": true}
		if action == "draft" {
			request = map[string]any{"layer_id": book.ID, "set_id": book.ID, "confirm_not_sensitive": true}
		}
		w := call(t, f, agent, "POST", "/api/knowledge/learnings/"+id+"/"+action, request)
		expect(t, w, 403)
		if code(t, w) != "person_required" {
			t.Fatalf("agent %s %s", action, w.Body.String())
		}
	}
	expect(t, call(t, f, f.a, "POST", "/api/knowledge/learnings/"+id+"/accept", map[string]any{"knowledge_id": book.ID, "confirm_not_sensitive": "yes"}), 400)
	if decisionOf(t, f, id) != "" {
		t.Fatal("a refused confirmation decided the learning")
	}
}
