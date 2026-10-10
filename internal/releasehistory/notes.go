// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/ticketbenefits"
)

const SnapshotSchema = "aeon.release-note-snapshot.v1"
const MembershipSource = "journey_tickets.release_node_id"
const ManifestMembershipSource = "release-manifest-tickets"
const FieldSource = "nodes.fields"

// HistoricalFallback labels a git tag headline used only because member
// benefits were never captured.
const HistoricalFallback = "historical-tag-headline"

// BackfillLabel marks a snapshot an operator inserted after publication
// because none was stored when the release was published. captured_at on
// that row is the backfill time; released_at is the original release time.
const BackfillLabel = "backfilled"

type NoteSnapshot struct {
	Backfilled       bool         `json:"backfilled,omitempty"`
	ActorID          string       `json:"actor_principal_id,omitempty"`
	Schema           string       `json:"schema"`
	TenantID         string       `json:"tenant_id"`
	ProjectID        string       `json:"project_node_id"`
	ReleaseID        string       `json:"release_node_id"`
	Version          string       `json:"version"`
	VersionScheme    string       `json:"version_scheme"`
	Revision         int64        `json:"release_revision"`
	CapturedAt       time.Time    `json:"captured_at"`
	MembershipSource string       `json:"membership_source"`
	FieldSource      string       `json:"field_source"`
	Frozen           bool         `json:"frozen,omitempty"`
	Label            string       `json:"label,omitempty"`
	ReleasedAt       *time.Time   `json:"released_at,omitempty"`
	Tickets          []NoteTicket `json:"tickets"`
}
type NoteTicket struct {
	Group       string          `json:"group,omitempty"`
	ID          string          `json:"id"`
	Key         string          `json:"key"`
	Position    int             `json:"position"`
	UpdatedAt   *time.Time      `json:"updated_at"`
	Fields      json.RawMessage `json:"fields"`
	Unavailable string          `json:"unavailable"`
}
type NoteItem struct {
	Group     string `json:"group,omitempty"`
	ID        string `json:"id"`
	Key       string `json:"key"`
	PillEN    string `json:"pill_en"`
	PillDE    string `json:"pill_de"`
	BenefitEN string `json:"benefit_en"`
	BenefitDE string `json:"benefit_de"`
}
type Notes struct {
	Corrections         []NoteCorrection `json:"corrections,omitempty"`
	PublicItems         []TicketNote     `json:"public_items,omitempty"`
	Source              string           `json:"source"`
	Fallback            string           `json:"fallback,omitempty"`
	SHA256              string           `json:"snapshot_sha256"`
	CapturedAt          *time.Time       `json:"captured_at"`
	Revision            int64            `json:"release_revision"`
	Items               []NoteItem       `json:"items"`
	Gaps                []string         `json:"gaps"`
	Hidden              int              `json:"hidden"`
	WrittenAfterRelease bool             `json:"written_after_release,omitempty"`
}

func MissingNotes() *Notes {
	return &Notes{Source: "unavailable", Fallback: HistoricalFallback, Items: []NoteItem{}, Gaps: []string{"Release membership and bilingual ticket fields were not captured. Git mentions do not establish release membership."}}
}

var noteUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// NotesFromSnapshot verifies the export's identity and preserves exact source
// text. Invalid input fails the build; incomplete ticket content stays a gap.
// Hidden text is never copied into the product-wide history manifest.
func NotesFromSnapshot(raw []byte, version, source string) (*Notes, error) {
	if len(raw) > 16<<20 {
		return nil, fmt.Errorf("ticket snapshot exceeds 16 MiB")
	}
	var s NoteSnapshot
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return nil, fmt.Errorf("invalid ticket snapshot: %w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("ticket snapshot must contain one JSON object")
	}
	if s.Schema != SnapshotSchema || (s.Version != "" && s.Version != version) || !ValidVersion(version) || (s.Version != "" && !CalendarScheme(s.VersionScheme)) || (s.Version == "" && s.VersionScheme != "") || s.Revision < 1 || s.CapturedAt.IsZero() || s.Tickets == nil || (s.MembershipSource != MembershipSource && s.MembershipSource != ManifestMembershipSource) || s.FieldSource != FieldSource || !noteUUID.MatchString(s.TenantID) || !noteUUID.MatchString(s.ProjectID) || (s.MembershipSource == MembershipSource && !noteUUID.MatchString(s.ReleaseID)) {
		return nil, fmt.Errorf("ticket snapshot identity, version or provenance is incomplete")
	}
	if s.MembershipSource == ManifestMembershipSource && (s.ReleaseID != "" || s.Version == "" || !s.Backfilled || s.Label != BackfillLabel || !noteUUID.MatchString(s.ActorID)) {
		return nil, fmt.Errorf("manifest backfill identity or actor is incomplete")
	}
	if s.Label != "" && s.Label != BackfillLabel {
		return nil, fmt.Errorf("ticket snapshot label is not recognised")
	}
	if s.Label == BackfillLabel && (!s.Frozen || s.ReleasedAt == nil || s.ReleasedAt.IsZero()) {
		return nil, fmt.Errorf("backfilled ticket snapshot lacks its release time")
	}
	sum := sha256.Sum256(raw)
	out := &Notes{Source: source, SHA256: hex.EncodeToString(sum[:]), CapturedAt: &s.CapturedAt, Revision: s.Revision, Items: []NoteItem{}, Gaps: []string{}, WrittenAfterRelease: s.Label == BackfillLabel}
	if s.Version == "" {
		out.Gaps = append(out.Gaps, "The release had no assigned version when captured; its association with this build is declared by the tagged snapshot path.")
	}
	sort.Slice(s.Tickets, func(i, j int) bool {
		a, b := s.Tickets[i], s.Tickets[j]
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.ID < b.ID
	})
	seen := map[string][]byte{}
	keys := map[string]string{}
	for _, t := range s.Tickets {
		if t.Group != "" && t.Group != GroupFeatures && t.Group != GroupFixes && t.Group != GroupOther {
			return nil, fmt.Errorf("invalid snapshot ticket group")
		}
		if !noteUUID.MatchString(t.ID) {
			return nil, fmt.Errorf("invalid snapshot ticket identity")
		}
		canonical, _ := json.Marshal(t)
		if old, ok := seen[t.ID]; ok {
			if !bytes.Equal(old, canonical) {
				return nil, fmt.Errorf("conflicting duplicate snapshot ticket")
			}
			continue
		}
		seen[t.ID] = canonical
		if t.Key != "" {
			if id, ok := keys[t.Key]; ok && id != t.ID {
				return nil, fmt.Errorf("duplicate snapshot ticket key")
			}
			keys[t.Key] = t.ID
		}
		label := t.Key
		if label == "" {
			label = t.ID
		}
		if t.Unavailable != "" {
			if ticketbenefits.OmitFromReleaseNotes(t.Fields) {
				out.Hidden++
			} else {
				out.Gaps = append(out.Gaps, label+": "+t.Unavailable)
			}
			continue
		}
		if t.Key == "" || t.UpdatedAt == nil {
			return nil, fmt.Errorf("ticket snapshot lacks key or field timestamp")
		}
		var fields map[string]any
		if json.Unmarshal(t.Fields, &fields) != nil || fields == nil {
			out.Gaps = append(out.Gaps, label+": ticket fields unavailable")
			continue
		}
		for _, key := range []string{"hide_from_release_notes", "no_release_needed"} {
			if flag, present := fields[key]; present {
				if _, valid := flag.(bool); !valid {
					return nil, fmt.Errorf("invalid snapshot %s flag", key)
				}
			}
		}
		if ticketbenefits.OmitFromReleaseNotes(t.Fields) {
			out.Hidden++
			continue
		}
		issues := ticketbenefits.Issues(t.Fields)
		if len(issues) > 0 {
			out.Gaps = append(out.Gaps, label+": "+strings.Join(issues, "; "))
		}
		// Keep the captured language when a translation is missing. Completion
		// validation remains strict; historical readers must not discard it.
		text := func(key string) string { value, _ := fields[key].(string); return value }
		item := NoteItem{ID: t.ID, Key: t.Key, Group: t.Group, PillEN: text("pill_en"), PillDE: text("pill_de"), BenefitEN: text("benefit_en"), BenefitDE: text("benefit_de")}
		if strings.TrimSpace(item.PillEN+item.PillDE+item.BenefitEN+item.BenefitDE) != "" {
			out.Items = append(out.Items, item)
		}
	}
	return out, nil
}
