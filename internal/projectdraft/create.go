// SPDX-License-Identifier: AGPL-3.0-only
package projectdraft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/knowledge"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	maxCreateBody = 64 << 10
	maxName       = 500
	knowledgeSlug = "from-start-conversation"
)

var (
	keyPattern  = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)
	uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	eventMeta   = json.RawMessage(`{"imported_from":"start"}`)
)

// CreateInput is what the person chose in the assistant. OpenPoints are the
// indices of the draft's open points that become work items.
type CreateInput struct {
	Name       string  `json:"name"`
	Key        string  `json:"key"`
	OwnerID    string  `json:"owner_id"`
	TargetDate *string `json:"target_date"`
	OpenPoints []int   `json:"open_points"`
}

// Created is the answer to a create, also to its retry by the same person.
type Created struct {
	Project  json.RawMessage `json:"project"`
	Tickets  int             `json:"tickets"`
	Replayed bool            `json:"replayed"`
}

type publicError struct {
	status  int
	message string
}

func (e *publicError) Error() string   { return e.message }
func (e *publicError) HTTPStatus() int { return e.status }

func invalid(message string) error { return &publicError{http.StatusUnprocessableEntity, message} }

func parseCreate(r *http.Request, w http.ResponseWriter) (CreateInput, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCreateBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return CreateInput{}, &publicError{http.StatusRequestEntityTooLarge, "The request is too large."}
	}
	var in CreateInput
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() {
		return in, &publicError{http.StatusBadRequest, "The request could not be read."}
	}
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "" || utf8.RuneCountInString(in.Name) > maxName || strings.ContainsRune(in.Name, 0):
		return in, invalid("Enter a name of up to 500 characters.")
	case !keyPattern.MatchString(in.Key):
		return in, invalid("2 to 10 letters or digits, starting with a letter.")
	case !uuidPattern.MatchString(in.OwnerID):
		return in, invalid("Choose an owner.")
	case len(in.OpenPoints) > maxItems:
		return in, invalid("Too many open points.")
	}
	if in.TargetDate != nil && *in.TargetDate != "" {
		if _, err := time.Parse(time.DateOnly, *in.TargetDate); err != nil {
			return in, invalid("Choose a valid target date.")
		}
	}
	seen := map[int]bool{}
	for _, index := range in.OpenPoints {
		if index < 0 || index >= maxItems || seen[index] {
			return in, invalid("The chosen open points do not match the handoff.")
		}
		seen[index] = true
	}
	return in, nil
}

// create consumes the draft and creates the project, its knowledge entry, the
// transcript attachment and the chosen work items in one transaction, through
// the ordinary guards. The transcript is staged first, outside it, as uploads
// are; the draft row is then fenced under the access-change lock, so a second
// person or a second tab gets the one gone answer and a retry by the same
// person gets the project it created. Events are appended last.
func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	p, digest, ok := m.person(w, r)
	if !ok {
		return
	}
	in, err := parseCreate(r, w)
	if err != nil {
		writeCreateError(w, r, err)
		return
	}
	var staged *attachments.Staged
	defer func() {
		if staged != nil {
			_ = staged.Close()
		}
	}()
	var payload *Payload
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		d, err := loadDraft(r.Context(), tx, digest, false)
		if err != nil || d.ConsumedBy != nil {
			return err
		}
		payload = &Payload{}
		return json.Unmarshal(d.Payload, payload)
	})
	if err == nil && payload != nil {
		for _, index := range in.OpenPoints {
			if index >= len(payload.OpenPoints) {
				err = invalid("The chosen open points do not match the handoff.")
			}
		}
		if err == nil && len(payload.Transcript) > 0 {
			staged, err = m.store.StageNamed(r.Context(), p.TenantID, strings.NewReader(transcriptText(*payload)), transcriptName(*payload))
		}
	}
	var out Created
	if err == nil {
		err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
			var err error
			out, err = m.createTx(r.Context(), tx, p, digest, in, staged)
			return err
		})
	}
	if err != nil {
		writeCreateError(w, r, err)
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, out)
}

func (m *Module) createTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, digest []byte, in CreateInput, staged *attachments.Staged) (Created, error) {
	// Tenant row and tree: the lock that serializes access changes and node
	// creation, so the permission checks below see the current grants.
	if err := authz.LockProjectWrite(ctx, tx, p.TenantID); err != nil {
		return Created{}, err
	}
	d, err := loadDraft(ctx, tx, digest, true)
	if err != nil {
		return Created{}, err
	}
	if d.ConsumedBy != nil {
		view, err := created(ctx, tx, p, d)
		if err != nil {
			return Created{}, err
		}
		return Created{Project: view.Project, Tickets: *view.Tickets, Replayed: true}, nil
	}
	var payload Payload
	if err := json.Unmarshal(d.Payload, &payload); err != nil {
		return Created{}, err
	}
	if len(payload.Transcript) > 0 && staged == nil {
		return Created{}, errGone
	}
	if err := requireOwner(ctx, tx, in.OwnerID); err != nil {
		return Created{}, err
	}
	words := copyFor(payload.Language)
	work := make([]string, 0, len(in.OpenPoints))
	for _, index := range in.OpenPoints {
		if index >= len(payload.OpenPoints) {
			return Created{}, invalid("The chosen open points do not match the handoff.")
		}
		work = append(work, strings.Join(strings.Fields(payload.OpenPoints[index]), " "))
	}
	targetDate := ""
	if in.TargetDate != nil {
		targetDate = *in.TargetDate
	}
	project, err := nodes.CreateImportedProjectTx(ctx, tx, p, nodes.ImportedProject{
		Title: in.Name, Key: in.Key, OwnerID: in.OwnerID, EndDate: targetDate, Work: work,
		WorkBody: fmt.Sprintf(words.workBody, payload.Source.Host),
	})
	if err != nil {
		return Created{}, err
	}
	// Node rows (the entry) before blob locks (the transcript), counter last.
	_, changes, err := knowledge.CreateImportedMemoryTx(ctx, tx, p, project.ProjectID, knowledgeSlug, words.entryTitle, knowledgeBody(payload, d.CreatedAt), in.Key, knowledgeMetadata(payload, d.CreatedAt))
	if err != nil {
		return Created{}, err
	}
	if staged != nil {
		_, change, err := attachments.AttachStagedTx(ctx, tx, p, project.ProjectID, transcriptName(payload), staged)
		if err != nil {
			return Created{}, err
		}
		changes = append(changes, change)
	}
	if _, err := tx.Exec(ctx, `UPDATE import_project_drafts SET payload=NULL, consumed_at=clock_timestamp(), consumed_by=$2::uuid, project_id=$3::uuid, ticket_count=$4,
	  expires_at=clock_timestamp()+make_interval(secs => $5) WHERE id=$1::uuid`, d.ID, p.ID, project.ProjectID, len(work), int(createdTTL.Seconds())); err != nil {
		return Created{}, err
	}
	// No further lock acquisition after the event counter.
	for _, e := range project.Events {
		e.Metadata = eventMeta
		if err := (nodes.SQLWriter{}).WriteEvent(ctx, tx, e); err != nil {
			return Created{}, err
		}
	}
	for _, c := range changes {
		c.Metadata = eventMeta
		if _, err := events.Append(ctx, tx, p, c); err != nil {
			return Created{}, err
		}
	}
	return Created{Project: project.Project, Tickets: len(work)}, nil
}

// requireOwner accepts an active person of this tenant; the project stores
// them as its assignee, which the node path canonicalizes again.
func requireOwner(ctx context.Context, tx pgx.Tx, id string) error {
	var kind, status string
	err := tx.QueryRow(ctx, `SELECT kind, status FROM principals WHERE id=$1::uuid`, id).Scan(&kind, &status)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (kind != "person" || status != "active") {
		return invalid("Choose an active person as the owner.")
	}
	return err
}

func writeCreateError(w http.ResponseWriter, r *http.Request, err error) {
	var status interface{ HTTPStatus() int }
	switch {
	case errors.Is(err, errGone):
		writeGone(w)
	case errors.Is(err, authz.ErrForbidden):
		httpapi.WriteError(w, http.StatusForbidden, "You may not create projects here.")
	case errors.As(err, &status) && status.HTTPStatus() < 500:
		httpapi.WriteError(w, status.HTTPStatus(), err.Error())
	default:
		httpapi.LogFailure(r, "draft_create", err)
		httpapi.WriteError(w, http.StatusInternalServerError, "The project was not created. Try again.")
	}
}
