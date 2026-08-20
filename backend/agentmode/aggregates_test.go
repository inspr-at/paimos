package agentmode

import (
	"testing"
	"time"
)

func TestAggregatesBoundariesOrderingAttentionAndInvariants(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	rows := []DeliveryRow{
		aggregateRow("delivery:4h", 2, nil, "verification", now.Add(4*time.Hour), 0, CountFlags{}),
		aggregateRow("delivery:24h", 1, int64PointerForTest(9), "qa", now.Add(24*time.Hour), 3, CountFlags{Blocked: 1}),
		aggregateRow("delivery:3d", 1, nil, "deployment", now.Add(72*time.Hour), 2, CountFlags{FailedNeedsRetry: 1}),
		aggregateRow("delivery:later", 1, int64PointerForTest(3), "implementation", now.Add(72*time.Hour+time.Nanosecond), 1, CountFlags{DeployedUnverified: 1, Unverified: 1}),
		aggregateRow("delivery:range", 2, int64PointerForTest(1), "specification", time.Time{}, 0, CountFlags{Unverified: 1}),
	}
	rows[4].Trust.OptimisticLandingAt = timePointerForTest(now.Add(time.Hour))
	rows[4].Trust.PessimisticLandingAt = timePointerForTest(now.Add(2 * time.Hour))
	rows[4].Trust.RangeOnly = true
	agg, err := BuildAggregates(rows, Filters{Attention: "all", Health: "all"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if agg.Root.ActiveTotal != 5 || agg.Root.Landing.Within4Hours != 1 || agg.Root.Landing.Within24Hours != 1 ||
		agg.Root.Landing.Within3Days != 1 || agg.Root.Landing.Later != 1 || agg.Root.Landing.RangeOnly != 1 {
		t.Fatalf("landing partitions: %+v", agg.Root.Landing)
	}
	if len(agg.Projects) != 2 || agg.Projects[0].ProjectID != 1 || agg.Projects[1].ProjectID != 2 ||
		len(agg.Projects[0].Lanes) != 3 || agg.Projects[0].Lanes[0].EpicID == nil ||
		*agg.Projects[0].Lanes[0].EpicID != 3 || *agg.Projects[0].Lanes[1].EpicID != 9 ||
		agg.Projects[0].Lanes[2].EpicID != nil {
		t.Fatalf("project/lane ordering: %+v", agg.Projects)
	}
	if agg.Attention.Total != 3 || len(agg.Attention.Items) != 3 || agg.Attention.Items[0].Level != 3 {
		t.Fatalf("attention: %+v", agg.Attention)
	}
	if agg.Root.Flags.DeployedUnverified > agg.Root.Flags.Unverified {
		t.Fatal("deployed-unverified invariant broken")
	}

	broken := agg
	broken.Root.ActiveTotal++
	if err := ValidateAggregates(broken, map[string]bool{}); err == nil {
		t.Fatal("invalid partition passed validation")
	}
}

func TestAttentionCapAndUnverifiedAloneIsNotAttention(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	rows := []DeliveryRow{aggregateRow("unverified-only", 1, nil, "verification", time.Time{}, 0, CountFlags{Unverified: 1})}
	for index := 0; index < MaxAttentionItems+3; index++ {
		row := aggregateRow("blocked:"+string(rune('a'+index)), 1, nil, "implementation", time.Time{}, 3, CountFlags{Blocked: 1})
		rows = append(rows, row)
	}
	agg, err := BuildAggregates(rows, Filters{Attention: "all", Health: "all"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if agg.Attention.Total != MaxAttentionItems+3 || len(agg.Attention.Items) != MaxAttentionItems ||
		agg.Root.Flags.Attention != MaxAttentionItems+3 || agg.Root.Flags.Unverified != 1 {
		t.Fatalf("cap/count mismatch: attention=%+v flags=%+v", agg.Attention, agg.Root.Flags)
	}
}

func aggregateRow(id string, project int64, epic *int64, stage string, landing time.Time, level int, flags CountFlags) DeliveryRow {
	row := DeliveryRow{DeliveryID: id, ProjectID: project, ProjectKey: "P", ProjectName: "Project",
		EpicID: epic, LaneKey: "", Stage: StageSummary{Key: stage}, active: true,
		Attention: RowAttention{Level: level}, attentionFlags: flags, Trust: SafeTrust{}, structuralIdentity: id}
	if epic == nil {
		row.LaneKey = "project:" + integerForTest(project) + "/ungrouped"
	} else {
		row.LaneKey = "project:" + integerForTest(project) + "/epic:" + integerForTest(*epic)
	}
	if !landing.IsZero() {
		row.Trust.LandingAt = timePointerForTest(landing)
		row.landingAt = timePointerForTest(landing)
	}
	if level > 0 {
		row.Attention.Reason = orderedTrueFlags(flags)[0]
	}
	return row
}

func integerForTest(value int64) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func int64PointerForTest(value int64) *int64        { return &value }
func timePointerForTest(value time.Time) *time.Time { return &value }
