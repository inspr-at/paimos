// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseContainerContractReferencesAndIdentityParameters(t *testing.T) {
	raw, e := os.ReadFile("../../api/openapi.yaml")
	if e != nil {
		t.Fatal(e)
	}
	var spec map[string]any
	if e = yaml.Unmarshal(raw, &spec); e != nil {
		t.Fatal(e)
	}
	components := spec["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	for _, name := range []string{"DeliveryReleaseRow", "DeliveryRelease", "DeliveryStatus", "DeliveryAdoptionJob", "DeliveryBatchRefusal"} {
		if schemas[name] == nil {
			t.Fatalf("missing schema %s", name)
		}
	}
	paths := spec["paths"].(map[string]any)
	checked := 0
	operations := map[string]bool{}
	for _, id := range strings.Fields("getProjectDelivery updateDeliveryDefaults verifyProjectDelivery listDeliveryAdoptions getDeliveryAdoptionReport requestDeliveryAdoption getDeliveryOverview listProjectReleases planProjectRelease getProjectRelease updateProjectRelease listProjectReleaseItems listProjectBacklog rankProjectRelease transitionProjectRelease cutProjectRelease publishProjectRelease closeProjectRelease placeShipsIn batchPlaceShipsIn") {
		operations[id] = true
	}
	var checkRefs func(any)
	checkRefs = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok && strings.HasPrefix(ref, "#/components/") {
				parts := strings.Split(strings.TrimPrefix(ref, "#/components/"), "/")
				if len(parts) != 2 {
					t.Fatalf("bad ref %s", ref)
				}
				set, ok := components[parts[0]].(map[string]any)
				if !ok || set[parts[1]] == nil {
					t.Fatalf("unresolved ref %s", ref)
				}
			}
			for _, child := range v {
				checkRefs(child)
			}
		case []any:
			for _, child := range v {
				checkRefs(child)
			}
		}
	}
	for path, value := range paths {
		item := value.(map[string]any)
		for method, value := range item {
			if method == "parameters" || method == "description" || method == "summary" {
				continue
			}
			operation, ok := value.(map[string]any)
			if !ok {
				continue
			}
			id, _ := operation["operationId"].(string)
			if !operations[id] {
				continue
			}
			checked++
			checkRefs(operation)
			security, _ := operation["security"].([]any)
			if len(security) == 0 {
				t.Fatalf("missing authentication %s %s", method, path)
			}
			parameters := map[string]bool{}
			for _, v := range []any{item["parameters"], operation["parameters"]} {
				if list, ok := v.([]any); ok {
					for _, p := range list {
						p := p.(map[string]any)
						if p["in"] == "path" && p["required"] == true {
							parameters[p["name"].(string)] = true
						}
					}
				}
			}
			for _, name := range []string{"projectId", "releaseId", "nodeId"} {
				if strings.Contains(path, "{"+name+"}") && !parameters[name] {
					t.Fatalf("missing path binding %s %s", method, path)
				}
			}
		}
	}
	if checked != len(operations) {
		t.Fatalf("only %d release operations linted", checked)
	}
	for name, value := range schemas {
		if strings.HasPrefix(name, "Delivery") {
			checkRefs(value)
		}
	}
}
