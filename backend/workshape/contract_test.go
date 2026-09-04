package workshape

import "testing"

func TestContractsAreClosedBoundedAndDistinct(t *testing.T) {
	ship, scout, unknown := For(Ship), For(Scout), For("legacy-value")
	if ship.Shape != Ship || ship.OutputKind != OutputDelivery || scout.Shape != Scout || scout.OutputKind != OutputInvestigationEvidence {
		t.Fatalf("shape contracts drifted: ship=%+v scout=%+v", ship, scout)
	}
	if unknown.Shape != Unknown || unknown.OutputKind != OutputUnclassified || len(unknown.StageApplicability) != 5 {
		t.Fatalf("legacy shape was inferred: %+v", unknown)
	}
	for _, contract := range []Contract{ship, scout, unknown} {
		if len(contract.StageApplicability) != 5 || len(contract.DefinitionOfDone) < 1 || len(contract.DefinitionOfDone) > 3 || len(contract.NonGoals) < 1 || len(contract.NonGoals) > 3 {
			t.Fatalf("contract is not bounded: %+v", contract)
		}
	}
	if ship.StageApplicability[1].Applicability != "required" || scout.StageApplicability[1].Applicability != "not_applicable" || scout.StageApplicability[4].Applicability != "required" {
		t.Fatalf("ship/scout applicability defaults are not distinct: ship=%+v scout=%+v", ship.StageApplicability, scout.StageApplicability)
	}
	ship.DefinitionOfDone[0] = "mutated"
	if For(Ship).DefinitionOfDone[0] == "mutated" {
		t.Fatal("callers can mutate global work-shape defaults")
	}
}

func TestPersistedShapeEnumIsClosed(t *testing.T) {
	if !ValidPersisted(Ship) || !ValidPersisted(Scout) {
		t.Fatal("closed persisted shapes rejected")
	}
	for _, value := range []string{"", Unknown, "research", "SHIP", " ship "} {
		if ValidPersisted(value) {
			t.Fatalf("invalid persisted shape %q accepted", value)
		}
	}
}
