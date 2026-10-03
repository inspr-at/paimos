// SPDX-License-Identifier: AGPL-3.0-only

package demo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

type journeyView struct {
	Revision int64  `json:"revision"`
	Stage    string `json:"stage"`
	Next     struct {
		Key       string  `json:"key"`
		Available bool    `json:"available"`
		Reason    string  `json:"reason"`
		Approval  *string `json:"approval_request_id"`
	} `json:"next_action"`
	RequirementsScope string  `json:"requirements_approval_scope"`
	CurrentReleaseID  *string `json:"current_release_id"`
}

type walkerView struct {
	Revision int64 `json:"revision"`
}

func (s *seeder) journeyView(project string) (journeyView, error) {
	var view journeyView
	err := s.api.do(s.admin, "", http.MethodGet, "/api/projects/"+project+"/journey", nil, http.StatusOK, &view, nil)
	return view, err
}

func (s *seeder) act(project string, body map[string]any) (journeyView, error) {
	var view journeyView
	err := s.api.do(s.admin, "", http.MethodPost, "/api/projects/"+project+"/journey/actions", body, http.StatusOK, &view, nil)
	if err != nil {
		return view, err
	}
	return view, nil
}

func (s *seeder) journey() error {
	project := s.lumenID
	if _, err := s.journeyView(project); err != nil {
		return err
	}
	var ignored journeyView
	if err := s.api.do(s.admin, "", http.MethodPut, "/api/projects/"+project+"/journey/profile", map[string]any{"profile": "personal", "expected_revision": 1}, http.StatusOK, &ignored, nil); err != nil {
		return fmt.Errorf("journey profile: %w", err)
	}
	if err := s.brief(project); err != nil {
		return err
	}
	view, err := s.journeyView(project)
	if err != nil {
		return err
	}
	view, err = s.act(project, map[string]any{"action": "confirm_brief", "expected_revision": view.Revision, "idempotency_key": "demo-confirm-brief"})
	if err != nil {
		return fmt.Errorf("confirm brief: %w", err)
	}
	if view.Stage != "requirements" {
		return fmt.Errorf("after brief, stage %s next %s (%s)", view.Stage, view.Next.Key, view.Next.Reason)
	}
	if err := s.api.do(s.admin, "", http.MethodPost, "/api/projects/"+project+"/requirements", map[string]any{
		"kind": "functional", "title": "Keep the reading room quiet after dusk",
		"body":              "Fictional requirement. The dusk bell ends the day. Screenshot copy only.",
		"expected_revision": view.Revision,
		"idempotency_key":   "demo-requirement",
	}, http.StatusCreated, nil, nil); err != nil {
		return fmt.Errorf("requirement: %w", err)
	}
	if err := s.agree(project, "demo-agree-1"); err != nil {
		return err
	}
	view, err = s.journeyView(project)
	if err != nil {
		return err
	}
	view, err = s.act(project, map[string]any{"action": "open_first_release", "expected_revision": view.Revision, "idempotency_key": "demo-open-release"})
	if err != nil {
		return fmt.Errorf("open release: %w", err)
	}
	if view.CurrentReleaseID == nil || *view.CurrentReleaseID == "" {
		return fmt.Errorf("release was not opened, stage %s next %s", view.Stage, view.Next.Key)
	}
	release := *view.CurrentReleaseID
	if err := s.releaseTickets(project, release); err != nil {
		return err
	}
	if err := s.agree(project, "demo-agree-2"); err != nil {
		return err
	}
	approval, err := s.grant(s.scribe, s.scribeKey, "journey.build", release, "Fictional demo: start the Lumen build.")
	if err != nil {
		return err
	}
	view, err = s.journeyView(project)
	if err != nil {
		return err
	}
	view, err = s.act(project, map[string]any{
		"action": "start_build", "expected_revision": view.Revision, "idempotency_key": "demo-start-build",
		"approval_request_id": approval, "release_id": release,
	})
	if err != nil {
		return fmt.Errorf("start build: %w", err)
	}
	if view.Stage != "build" {
		return fmt.Errorf("journey stage %s, next %s available %v (%s)", view.Stage, view.Next.Key, view.Next.Available, view.Next.Reason)
	}
	return nil
}

func (s *seeder) brief(project string) error {
	note := "Fictional brief source for the archive demo."
	sum := sha256.Sum256([]byte(note))
	var source idBody
	if err := s.api.do(s.admin, "", http.MethodPost, "/api/projects/"+project+"/intake/sources", map[string]any{
		"kind": "note", "label": "Fictional dusk note", "locator": "note:lumen-brief",
		"content_sha256": hex.EncodeToString(sum[:]), "idempotency_key": "demo-brief-source",
	}, http.StatusCreated, &source, nil); err != nil {
		return fmt.Errorf("intake source: %w", err)
	}
	if _, err := s.grant(s.scribe, s.scribeKey, "intake.write", project, "Fictional demo: let the scribe file the Lumen brief."); err != nil {
		return err
	}
	var draft idBody
	if err := s.api.do(s.scribe, s.scribeKey, http.MethodPost, "/api/projects/"+project+"/intake/drafts", map[string]any{
		"kind": "brief", "title": "A quiet reading room",
		"body":            "The archive wants a reading room that stays quiet after dusk. This text is fictional demo copy.",
		"base_event_id":   0,
		"citations":       []map[string]string{{"source_id": source.ID, "locator": "paragraph-1"}},
		"idempotency_key": "demo-brief",
	}, http.StatusCreated, &draft, nil); err != nil {
		return fmt.Errorf("intake draft: %w", err)
	}
	if err := s.api.do(s.admin, "", http.MethodPost, "/api/projects/"+project+"/intake/drafts/"+draft.ID+"/accept", map[string]any{
		"expected_base_event_id": 0,
	}, http.StatusOK, nil, nil); err != nil {
		return fmt.Errorf("accept brief: %w", err)
	}
	return nil
}

// pendingJourney leaves a live, revision-bound requirements gate on a second
// project. The existing Lumen build and its applied approvals stay intact.
func (s *seeder) pendingJourney() error {
	project := s.ids["NGLASS-1"]
	view, err := s.journeyView(project)
	if err != nil {
		return err
	}
	if err := s.api.do(s.admin, "", http.MethodPut, "/api/projects/"+project+"/journey/profile", map[string]any{
		"profile": "personal", "expected_revision": view.Revision,
	}, http.StatusOK, nil, nil); err != nil {
		return err
	}
	if err := s.brief(project); err != nil {
		return err
	}
	view, err = s.journeyView(project)
	if err != nil {
		return err
	}
	view, err = s.act(project, map[string]any{
		"action": "confirm_brief", "expected_revision": view.Revision, "idempotency_key": "demo-glass-confirm-brief",
	})
	if err != nil {
		return err
	}
	if err := s.api.do(s.admin, "", http.MethodPost, "/api/projects/"+project+"/requirements", map[string]any{
		"kind": "functional", "title": "Fit quiet glass panes in the reading room",
		"body":              "Fictional glass specification for the archive. A person must agree it before work starts.",
		"expected_revision": view.Revision, "idempotency_key": "demo-glass-requirement",
	}, http.StatusCreated, nil, nil); err != nil {
		return err
	}
	view, err = s.journeyView(project)
	if err != nil {
		return err
	}
	if view.Stage != "requirements" || view.RequirementsScope == "" {
		return fmt.Errorf("North Glass has no live requirements gate")
	}
	return s.api.do(s.scribe, s.scribeKey, http.MethodPost, "/api/approvals", map[string]any{
		"scope": view.RequirementsScope, "resource_kind": "node", "resource_id": project,
		"rationale":  "Lumen Scribe asks a person to agree the fictional North Glass pane requirements.",
		"expires_at": time.Now().Add(72 * time.Hour).UTC(),
	}, http.StatusCreated, nil, nil)
}

func (s *seeder) agree(project, key string) error {
	view, err := s.journeyView(project)
	if err != nil {
		return err
	}
	if view.RequirementsScope == "" {
		return fmt.Errorf("requirements scope is empty at stage %s", view.Stage)
	}
	approval, err := s.grant(s.scribe, s.scribeKey, view.RequirementsScope, project, "Fictional demo: agree the Lumen requirements ("+key+").")
	if err != nil {
		return err
	}
	view, err = s.journeyView(project)
	if err != nil {
		return err
	}
	if err := s.api.do(s.admin, "", http.MethodPost, "/api/projects/"+project+"/requirements/agree", map[string]any{
		"expected_revision": view.Revision, "approval_request_id": approval, "idempotency_key": key,
	}, http.StatusOK, nil, nil); err != nil {
		return fmt.Errorf("agree %s: %w", key, err)
	}
	return nil
}

func (s *seeder) releaseTickets(project, release string) error {
	var walker walkerView
	if err := s.api.do(s.admin, "", http.MethodGet, "/api/projects/"+project+"/releases/"+release+"/walker", nil, http.StatusOK, &walker, nil); err != nil {
		return err
	}
	titles := []string{"Hang the release lantern", "Stamp the release card", "Oil the release desk"}
	rev := walker.Revision
	for i, title := range titles {
		var next walkerView
		if err := s.api.do(s.admin, "", http.MethodPost, "/api/projects/"+project+"/releases/"+release+"/tickets", map[string]any{
			"title": title, "included": true, "expected_revision": rev, "idempotency_key": fmt.Sprintf("demo-release-ticket-%d", i+1),
		}, http.StatusOK, &next, nil); err != nil {
			return fmt.Errorf("release ticket %d: %w", i+1, err)
		}
		rev = next.Revision
	}
	return nil
}

func (s *seeder) grant(agent tenant.Principal, token, scope, resource, rationale string) (string, error) {
	var created idBody
	err := s.api.do(agent, token, http.MethodPost, "/api/approvals", map[string]any{
		"scope": scope, "resource_kind": "node", "resource_id": resource,
		"rationale": rationale, "expires_at": time.Now().Add(72 * time.Hour).UTC(),
	}, http.StatusCreated, &created, nil)
	if err != nil {
		return "", fmt.Errorf("propose %s: %w", scope, err)
	}
	if err := s.api.do(s.admin, "", http.MethodPost, "/api/approvals/"+created.ID+"/decision", map[string]any{
		"decision": "approved",
	}, http.StatusOK, nil, nil); err != nil {
		return "", fmt.Errorf("approve %s: %w", scope, err)
	}
	return created.ID, nil
}
