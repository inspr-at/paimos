// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"errors"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

var (
	ErrPromotion           = &Conflict{"promotion", "Only a person may move an item earlier or rank an unranked item."}
	ErrHistoryCorrection   = &Conflict{"history_correction", "Changing released history requires a person with roles.manage."}
	ErrPublishedOrder      = &Conflict{"published_order", "Published releases keep their numbered order."}
	ErrProjectChanged      = &Conflict{"project_changed", "The item belongs to a different project; reload before moving it."}
	ErrRevisionChanged     = &Conflict{"revision_changed", "The record changed; reload before editing it."}
	ErrFrozen              = &Conflict{"frozen", "This release is frozen; adding or reordering work is closed, and cut scope stays fixed."}
	ErrEntryClosed         = &Conflict{"entry_closed", "A person with releases.write must move these items."}
	ErrReleaseClosed       = &Conflict{"release_closed", "This release no longer accepts items."}
	ErrReleaseCapacity     = &Conflict{"release_capacity", "A release holds at most 1,000 rows; move some out first."}
	ErrOpenReleaseCapacity = &Conflict{"open_release_capacity", "A project holds at most 50 non-terminal releases."}
	ErrPendingCapacity     = &Conflict{"pending_capacity", "Pending internal work and published scope hold at most 4,000 rows."}
)

// Position is read from Effective or constructed from the destination after
// its rows are locked. Empty release and rank identify the unranked tail.
type Position struct {
	ProjectID    string
	ItemID       string
	ReleaseID    string
	ReleaseRank  string
	ReleaseState string
	Rank         string
	CreatedAt    time.Time
}

func (p Position) class() (int, error) {
	if p.ProjectID == "" || p.ItemID == "" {
		return 0, errors.New("delivery position needs project and item identity")
	}
	if p.ReleaseID != "" {
		switch p.ReleaseState {
		case "released", "abandoned":
			return 0, ErrHistoryCorrection
		case "planned", "building", "frozen":
		default:
			return 0, errors.New("unknown release state")
		}
		if !ValidRank(p.ReleaseRank) || !ValidRank(p.Rank) {
			return 0, ErrInvalidRank
		}
		return 0, nil
	}
	if p.Rank != "" {
		if !ValidRank(p.Rank) {
			return 0, ErrInvalidRank
		}
		return 1, nil
	}
	if p.CreatedAt.IsZero() {
		return 0, errors.New("tail position needs creation time")
	}
	return 2, nil
}

// CheckAgentMove permits only equal or later delivery positions. Every item
// in a batch and undo must pass in the final locked write, before any change.
// It is a policy check, not permission or a transaction/CAS implementation.
func CheckAgentMove(before, after Position) error {
	if before.ProjectID != after.ProjectID {
		return ErrProjectChanged
	}
	if before.ItemID != after.ItemID {
		return errors.New("cannot compare different item identities")
	}
	b, err := before.class()
	if err != nil {
		return err
	}
	a, err := after.class()
	if err != nil {
		return err
	}
	comparison := a - b
	if comparison == 0 {
		switch a {
		case 0:
			comparison = strings.Compare(after.ReleaseRank, before.ReleaseRank)
			if comparison == 0 {
				if before.ReleaseID != after.ReleaseID {
					return errors.New("different releases share a rank")
				}
				comparison = strings.Compare(after.Rank, before.Rank)
			}
		case 1:
			comparison = strings.Compare(after.Rank, before.Rank)
		case 2:
			comparison = after.CreatedAt.Compare(before.CreatedAt)
		}
	}
	if comparison < 0 {
		return ErrPromotion
	}
	return nil
}

// CheckPrecondition checks the locked current project and revision. A missing
// ships_in row has revision 0. Writers must additionally predicate the final
// UPDATE on that revision (or INSERT only when absent); this grants no lock.
func CheckPrecondition(projectID string, revision int64, expectedProjectID string, expectedRevision int64) error {
	if projectID == "" || expectedProjectID == "" || revision < 0 || expectedRevision < 0 {
		return errors.New("project identity and nonnegative revision are required")
	}
	if projectID != expectedProjectID {
		return ErrProjectChanged
	}
	if revision != expectedRevision {
		return ErrRevisionChanged
	}
	return nil
}

type NumberedRank struct {
	Sequence int
	Rank     string
}

// CheckPublishedRank compares a proposed rank to the immediate lower/higher
// published sequences, including terminal releases. Internal releases are
// unrestricted. Conversion takes the next sequence and must rank after lower.
func CheckPublishedRank(sequence int, rank string, lower, higher *NumberedRank) error {
	if sequence < 1 || !ValidRank(rank) {
		return errors.New("published rank needs a positive sequence and valid key")
	}
	if lower != nil {
		if lower.Sequence < 1 || lower.Sequence >= sequence || !ValidRank(lower.Rank) {
			return errors.New("invalid lower published neighbour")
		}
		if rank <= lower.Rank {
			return ErrPublishedOrder
		}
	}
	if higher != nil {
		if higher.Sequence <= sequence || !ValidRank(higher.Rank) {
			return errors.New("invalid higher published neighbour")
		}
		if rank >= higher.Rank {
			return ErrPublishedOrder
		}
	}
	return nil
}

// CheckAdmission checks only a change INTO a release. Permissions, revision,
// history correction and promotion remain independent requirements. Callers
// sample their trusted clock after fences; reranking/removal is not admission.
func CheckAdmission(kind tenant.PrincipalKind, hasReleasesWrite bool, state string, closesAt *time.Time, now time.Time, adding bool) error {
	return checkAdmission(kind, hasReleasesWrite, state, closesAt, now, adding, false)
}

func checkAdmission(kind tenant.PrincipalKind, hasReleasesWrite bool, state string, closesAt *time.Time, now time.Time, adding, historyCorrection bool) error {
	if !adding {
		return nil
	}
	if state == "frozen" {
		return ErrFrozen
	}
	if state != "planned" && state != "building" && !(state == "released" && historyCorrection) {
		return ErrReleaseClosed
	}
	if now.IsZero() {
		return errors.New("admission needs the trusted post-fence clock")
	}
	if closesAt != nil && !now.Before(*closesAt) && (kind != tenant.Person || !hasReleasesWrite) {
		return ErrEntryClosed
	}
	return nil
}

// Admission describes the locked destination and the projected post-write
// counts, including tombstones. The clock must be sampled after the fences.
type Admission struct {
	State               string
	ClosesAt            *time.Time
	Now                 time.Time
	Adding              bool
	ReleaseRows         int
	NonTerminalReleases int
	PendingRows         int
}

// CheckHistoryCorrectionAdmission applies when either the source or destination
// is released. The principal kind and permission results must come from the
// final fenced transaction's current principal and RequireTx checks; this pure
// policy helper grants no authority. Corrections into another container use its
// actual state, deadline and projected counts. Removals still check authority
// and counts because they may increase the pending-internal total.
func CheckHistoryCorrectionAdmission(kind tenant.PrincipalKind, hasRolesManage, hasReleasesWrite bool, a Admission) error {
	if kind != tenant.Person || !hasRolesManage {
		return ErrHistoryCorrection
	}
	if err := checkAdmission(kind, hasReleasesWrite, a.State, a.ClosesAt, a.Now, a.Adding, true); err != nil {
		return err
	}
	return CheckCounts(a.ReleaseRows, a.NonTerminalReleases, a.PendingRows)
}

// CheckCounts uses projected counts, including tombstones, not live counts.
// Count queries must stop at cap+1; they must not allocate a whole population.
func CheckCounts(releaseRows, nonTerminalReleases, pendingRows int) error {
	if releaseRows < 0 || nonTerminalReleases < 0 || pendingRows < 0 {
		return errors.New("delivery counts cannot be negative")
	}
	if releaseRows > 1000 {
		return ErrReleaseCapacity
	}
	if nonTerminalReleases > 50 {
		return ErrOpenReleaseCapacity
	}
	if pendingRows > 4000 {
		return ErrPendingCapacity
	}
	return nil
}
