// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const maxSafeInteger = 9007199254740991

var attentionFlagOrder = []string{
	"blocked", "waiting_needs_input", "failed_needs_retry", "stale_no_signal",
	"unknown_reporter", "deployed_unverified", "unverified",
}

func BuildAggregates(rows []DeliveryRow, filters Filters, calculatedAt time.Time) (Aggregates, error) {
	calculatedAt = calculatedAt.UTC()
	if calculatedAt.IsZero() {
		return Aggregates{}, fmt.Errorf("%w: aggregate clock is zero", ErrInvariant)
	}
	rows = append([]DeliveryRow(nil), rows...)
	sortRows(rows)
	agg := Aggregates{
		SchemaVersion: AggregatesVersion, CalculatedAt: calculatedAt,
		Projects: []ProjectAggregate{}, Attention: AttentionAggregate{Items: []AttentionItem{}},
	}
	type laneBuilder struct {
		row DeliveryRow
		set CountSet
	}
	type projectBuilder struct {
		row   DeliveryRow
		set   CountSet
		lanes map[string]*laneBuilder
	}
	projects := map[int64]*projectBuilder{}
	activeIDs := map[string]bool{}
	var earliest *time.Time
	for _, row := range rows {
		if !row.active {
			return Aggregates{}, fmt.Errorf("%w: terminal row entered active aggregates", ErrInvariant)
		}
		if activeIDs[row.DeliveryID] {
			return Aggregates{}, fmt.Errorf("%w: duplicate delivery row", ErrInvariant)
		}
		activeIDs[row.DeliveryID] = true
		addRowToCountSet(&agg.Root, row, calculatedAt)
		project := projects[row.ProjectID]
		if project == nil {
			project = &projectBuilder{row: row, lanes: map[string]*laneBuilder{}}
			projects[row.ProjectID] = project
		} else if project.row.ProjectKey != row.ProjectKey || project.row.ProjectName != row.ProjectName {
			return Aggregates{}, fmt.Errorf("%w: project metadata is inconsistent", ErrInvariant)
		}
		addRowToCountSet(&project.set, row, calculatedAt)
		lane := project.lanes[row.LaneKey]
		if lane == nil {
			lane = &laneBuilder{row: row}
			project.lanes[row.LaneKey] = lane
		} else if !sameLane(lane.row, row) {
			return Aggregates{}, fmt.Errorf("%w: lane identity is inconsistent", ErrInvariant)
		}
		addRowToCountSet(&lane.set, row, calculatedAt)
		if row.nextRefresh != nil && row.nextRefresh.After(calculatedAt) && (earliest == nil || row.nextRefresh.Before(*earliest)) {
			copy := row.nextRefresh.UTC()
			earliest = &copy
		}
		if row.Attention.Level > 0 {
			agg.Attention.Items = append(agg.Attention.Items, AttentionItem{
				DeliveryID: row.DeliveryID, Level: row.Attention.Level,
				PrimaryReason: row.Attention.Reason, Flags: orderedTrueFlags(row.attentionFlags),
				Since: formatOptionalTime(row.attentionSince),
			})
		}
	}
	agg.NextRefreshAt = earliest

	projectIDs := make([]int64, 0, len(projects))
	for id := range projects {
		projectIDs = append(projectIDs, id)
	}
	sort.Slice(projectIDs, func(i, j int) bool { return projectIDs[i] < projectIDs[j] })
	for _, id := range projectIDs {
		builder := projects[id]
		project := ProjectAggregate{ProjectID: id, ProjectKey: builder.row.ProjectKey,
			ProjectName: builder.row.ProjectName, Counts: builder.set, Lanes: []LaneAggregate{}}
		lanes := make([]*laneBuilder, 0, len(builder.lanes))
		for _, lane := range builder.lanes {
			lanes = append(lanes, lane)
		}
		sort.Slice(lanes, func(i, j int) bool {
			left, right := lanes[i].row, lanes[j].row
			if (left.EpicID == nil) != (right.EpicID == nil) {
				return left.EpicID != nil // grouped before Ungrouped
			}
			if left.EpicID != nil && *left.EpicID != *right.EpicID {
				return *left.EpicID < *right.EpicID
			}
			return left.LaneKey < right.LaneKey
		})
		for _, lane := range lanes {
			project.Lanes = append(project.Lanes, LaneAggregate{LaneKey: lane.row.LaneKey,
				EpicID: lane.row.EpicID, EpicKey: lane.row.EpicKey, EpicTitle: lane.row.EpicTitle, Counts: lane.set})
		}
		agg.Projects = append(agg.Projects, project)
	}

	sort.Slice(agg.Attention.Items, func(i, j int) bool {
		left, right := agg.Attention.Items[i], agg.Attention.Items[j]
		if left.Level != right.Level {
			return left.Level > right.Level
		}
		if attentionReasonRank(left.PrimaryReason) != attentionReasonRank(right.PrimaryReason) {
			return attentionReasonRank(left.PrimaryReason) < attentionReasonRank(right.PrimaryReason)
		}
		if (left.Since == nil) != (right.Since == nil) {
			return left.Since != nil
		}
		if left.Since != nil && *left.Since != *right.Since {
			return *left.Since < *right.Since
		}
		return left.DeliveryID < right.DeliveryID
	})
	agg.Attention.Total = len(agg.Attention.Items)
	if len(agg.Attention.Items) > MaxAttentionItems {
		agg.Attention.Items = append([]AttentionItem(nil), agg.Attention.Items[:MaxAttentionItems]...)
	}
	agg.StructuralRevision = structuralRevision(filters, rows, agg.Projects)
	agg.ClassificationRevision = classificationRevision(agg, rows)
	if err := ValidateAggregates(agg, activeIDs); err != nil {
		return Aggregates{}, err
	}
	return agg, nil
}

func addRowToCountSet(set *CountSet, row DeliveryRow, calculatedAt time.Time) {
	set.ActiveTotal++
	switch row.Stage.Key {
	case "specification":
		set.CurrentStage.Specification++
	case "implementation":
		set.CurrentStage.Implementation++
	case "qa":
		set.CurrentStage.QA++
	case "deployment":
		set.CurrentStage.Deployment++
	case "verification":
		set.CurrentStage.Verification++
	default:
		set.CurrentStage.Unknown++
	}
	if row.Trust.Suppression != "" || (row.Trust.LandingAt == nil && row.Trust.OptimisticLandingAt == nil && row.Trust.PessimisticLandingAt == nil) {
		set.Landing.SuppressedOrUnknown++
	} else if row.Trust.LandingAt == nil || row.Trust.RangeOnly {
		set.Landing.RangeOnly++
	} else {
		remaining := row.Trust.LandingAt.Sub(calculatedAt)
		switch {
		case remaining <= 4*time.Hour:
			set.Landing.Within4Hours++
		case remaining <= 24*time.Hour:
			set.Landing.Within24Hours++
		case remaining <= 72*time.Hour:
			set.Landing.Within3Days++
		default:
			set.Landing.Later++
		}
	}
	set.Flags.Attention += boolCount(row.Attention.Level > 0)
	set.Flags.WaitingNeedsInput += boolCount(row.attentionFlags.WaitingNeedsInput > 0)
	set.Flags.Blocked += boolCount(row.attentionFlags.Blocked > 0)
	set.Flags.StaleNoSignal += boolCount(row.attentionFlags.StaleNoSignal > 0)
	set.Flags.FailedNeedsRetry += boolCount(row.attentionFlags.FailedNeedsRetry > 0)
	set.Flags.DeployedUnverified += boolCount(row.attentionFlags.DeployedUnverified > 0)
	set.Flags.Unverified += boolCount(row.attentionFlags.Unverified > 0)
	set.Flags.UnknownReporter += boolCount(row.attentionFlags.UnknownReporter > 0)
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func sameLane(a, b DeliveryRow) bool {
	if a.ProjectID != b.ProjectID || a.LaneKey != b.LaneKey || (a.EpicID == nil) != (b.EpicID == nil) {
		return false
	}
	if a.EpicID == nil {
		return true
	}
	return *a.EpicID == *b.EpicID && optionalStringEqual(a.EpicKey, b.EpicKey) && optionalStringEqual(a.EpicTitle, b.EpicTitle)
}

func optionalStringEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func orderedTrueFlags(flags CountFlags) []string {
	values := map[string]int{
		"blocked": flags.Blocked, "waiting_needs_input": flags.WaitingNeedsInput,
		"failed_needs_retry": flags.FailedNeedsRetry, "stale_no_signal": flags.StaleNoSignal,
		"unknown_reporter": flags.UnknownReporter, "deployed_unverified": flags.DeployedUnverified,
		"unverified": flags.Unverified,
	}
	out := []string{}
	for _, key := range attentionFlagOrder {
		if values[key] > 0 {
			out = append(out, key)
		}
	}
	return out
}

func attentionReasonRank(reason string) int {
	for i, candidate := range append(attentionFlagOrder, "other") {
		if reason == candidate {
			return i
		}
	}
	return len(attentionFlagOrder)
}

func sortRows(rows []DeliveryRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if left.Attention.Level != right.Attention.Level {
			return left.Attention.Level > right.Attention.Level
		}
		if (left.landingAt == nil) != (right.landingAt == nil) {
			return left.landingAt != nil
		}
		if left.landingAt != nil && !left.landingAt.Equal(*right.landingAt) {
			return left.landingAt.Before(*right.landingAt)
		}
		return left.DeliveryID < right.DeliveryID
	})
}

func structuralRevision(filters Filters, rows []DeliveryRow, projects []ProjectAggregate) string {
	h := sha256.New()
	_, _ = h.Write([]byte("agent-mode-structural-v1\x00"))
	fingerprint := filters.CanonicalFingerprint()
	_, _ = h.Write(fingerprint[:])
	structuralRows := append([]DeliveryRow(nil), rows...)
	sort.Slice(structuralRows, func(i, j int) bool { return structuralRows[i].DeliveryID < structuralRows[j].DeliveryID })
	for _, row := range structuralRows {
		identity := row.structuralIdentity
		if identity == "" {
			identity = strings.Join([]string{row.DeliveryID, row.DeliveryRevision, row.TrustRevision, row.LaneKey}, "\x00")
		}
		_, _ = h.Write([]byte(identity))
		_, _ = h.Write([]byte{0})
	}
	for _, project := range projects {
		for _, lane := range project.Lanes {
			_, _ = h.Write([]byte(lane.LaneKey))
			_, _ = h.Write([]byte{0})
		}
	}
	return "am_s1_" + hex.EncodeToString(h.Sum(nil))
}

func classificationRevision(agg Aggregates, rows []DeliveryRow) string {
	presentationOrder := make([]string, len(rows))
	for index := range rows {
		presentationOrder[index] = rows[index].DeliveryID
	}
	payload := struct {
		Structural        string
		PresentationOrder []string
		Root              CountSet
		Projects          []ProjectAggregate
		Attention         AttentionAggregate
	}{agg.StructuralRevision, presentationOrder, agg.Root, agg.Projects, agg.Attention}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(append([]byte("agent-mode-classification-v1\x00"), raw...))
	return "am_c1_" + hex.EncodeToString(sum[:])
}

func ValidateAggregates(agg Aggregates, activeIDs map[string]bool) error {
	if agg.SchemaVersion != AggregatesVersion || agg.CalculatedAt.IsZero() ||
		!strings.HasPrefix(agg.StructuralRevision, "am_s1_") || !strings.HasPrefix(agg.ClassificationRevision, "am_c1_") {
		return fmt.Errorf("%w: malformed aggregate header", ErrInvariant)
	}
	if err := validateCountSet(agg.Root); err != nil {
		return err
	}
	projectSeen, laneSeen := map[int64]bool{}, map[string]bool{}
	var projectSum CountSet
	lastProject := int64(0)
	for index, project := range agg.Projects {
		if project.ProjectID <= 0 || projectSeen[project.ProjectID] || (index > 0 && project.ProjectID <= lastProject) {
			return fmt.Errorf("%w: duplicate or unordered aggregate project", ErrInvariant)
		}
		projectSeen[project.ProjectID], lastProject = true, project.ProjectID
		if err := validateCountSet(project.Counts); err != nil {
			return err
		}
		var laneSum CountSet
		for _, lane := range project.Lanes {
			if lane.LaneKey == "" || laneSeen[lane.LaneKey] {
				return fmt.Errorf("%w: duplicate aggregate lane", ErrInvariant)
			}
			laneSeen[lane.LaneKey] = true
			if err := validateCountSet(lane.Counts); err != nil {
				return err
			}
			laneSum = addCountSets(laneSum, lane.Counts)
		}
		if laneSum != project.Counts {
			return fmt.Errorf("%w: project-to-lane count mismatch", ErrInvariant)
		}
		projectSum = addCountSets(projectSum, project.Counts)
	}
	if projectSum != agg.Root {
		return fmt.Errorf("%w: root-to-project count mismatch", ErrInvariant)
	}
	attentionSeen := map[string]bool{}
	if agg.Attention.Total < len(agg.Attention.Items) || len(agg.Attention.Items) > MaxAttentionItems || agg.Attention.Total > agg.Root.ActiveTotal {
		return fmt.Errorf("%w: invalid attention cardinality", ErrInvariant)
	}
	for _, item := range agg.Attention.Items {
		if item.DeliveryID == "" || attentionSeen[item.DeliveryID] || !activeIDs[item.DeliveryID] || item.Level < 1 || item.Level > 3 {
			return fmt.Errorf("%w: invalid attention reference", ErrInvariant)
		}
		attentionSeen[item.DeliveryID] = true
	}
	return nil
}

func validateCountSet(set CountSet) error {
	values := []int{set.ActiveTotal, set.CurrentStage.Specification, set.CurrentStage.Implementation,
		set.CurrentStage.QA, set.CurrentStage.Deployment, set.CurrentStage.Verification, set.CurrentStage.Unknown,
		set.Landing.Within4Hours, set.Landing.Within24Hours, set.Landing.Within3Days, set.Landing.Later,
		set.Landing.RangeOnly, set.Landing.SuppressedOrUnknown, set.Flags.Attention,
		set.Flags.WaitingNeedsInput, set.Flags.Blocked, set.Flags.StaleNoSignal, set.Flags.FailedNeedsRetry,
		set.Flags.DeployedUnverified, set.Flags.Unverified, set.Flags.UnknownReporter}
	for _, value := range values {
		if value < 0 || value > maxSafeInteger {
			return fmt.Errorf("%w: aggregate count is not a safe integer", ErrInvariant)
		}
	}
	stageTotal := set.CurrentStage.Specification + set.CurrentStage.Implementation + set.CurrentStage.QA +
		set.CurrentStage.Deployment + set.CurrentStage.Verification + set.CurrentStage.Unknown
	landingTotal := set.Landing.Within4Hours + set.Landing.Within24Hours + set.Landing.Within3Days +
		set.Landing.Later + set.Landing.RangeOnly + set.Landing.SuppressedOrUnknown
	if stageTotal != set.ActiveTotal || landingTotal != set.ActiveTotal {
		return fmt.Errorf("%w: aggregate partitions do not equal active_total", ErrInvariant)
	}
	for _, flag := range []int{set.Flags.Attention, set.Flags.WaitingNeedsInput, set.Flags.Blocked,
		set.Flags.StaleNoSignal, set.Flags.FailedNeedsRetry, set.Flags.DeployedUnverified,
		set.Flags.Unverified, set.Flags.UnknownReporter} {
		if flag > set.ActiveTotal {
			return fmt.Errorf("%w: aggregate flag exceeds active_total", ErrInvariant)
		}
	}
	if set.Flags.DeployedUnverified > set.Flags.Unverified {
		return fmt.Errorf("%w: deployed_unverified exceeds unverified", ErrInvariant)
	}
	return nil
}

func addCountSets(a, b CountSet) CountSet {
	a.ActiveTotal += b.ActiveTotal
	a.CurrentStage.Specification += b.CurrentStage.Specification
	a.CurrentStage.Implementation += b.CurrentStage.Implementation
	a.CurrentStage.QA += b.CurrentStage.QA
	a.CurrentStage.Deployment += b.CurrentStage.Deployment
	a.CurrentStage.Verification += b.CurrentStage.Verification
	a.CurrentStage.Unknown += b.CurrentStage.Unknown
	a.Landing.Within4Hours += b.Landing.Within4Hours
	a.Landing.Within24Hours += b.Landing.Within24Hours
	a.Landing.Within3Days += b.Landing.Within3Days
	a.Landing.Later += b.Landing.Later
	a.Landing.RangeOnly += b.Landing.RangeOnly
	a.Landing.SuppressedOrUnknown += b.Landing.SuppressedOrUnknown
	a.Flags.Attention += b.Flags.Attention
	a.Flags.WaitingNeedsInput += b.Flags.WaitingNeedsInput
	a.Flags.Blocked += b.Flags.Blocked
	a.Flags.StaleNoSignal += b.Flags.StaleNoSignal
	a.Flags.FailedNeedsRetry += b.Flags.FailedNeedsRetry
	a.Flags.DeployedUnverified += b.Flags.DeployedUnverified
	a.Flags.Unverified += b.Flags.Unverified
	a.Flags.UnknownReporter += b.Flags.UnknownReporter
	return a
}

func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}
