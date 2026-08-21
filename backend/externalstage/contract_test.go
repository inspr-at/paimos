package externalstage

import (
	"reflect"
	"strings"
	"testing"
)

func TestV1RouteAndMediaContractIsLiteralAndClosed(t *testing.T) {
	if ContractMajor != 1 || MediaTypeV1 != "application/vnd.paimos.external-stage.v1+json" ||
		SecretMediaTypeV1 != "application/vnd.paimos.external-stage-secret.v1" ||
		HandoffSecretHeader != "X-PAIMOS-Handoff-Secret" || OneTimeSecretBytes != 32 {
		t.Fatalf("v1 constants drifted: major=%d media=%q secret_media=%q header=%q bytes=%d",
			ContractMajor, MediaTypeV1, SecretMediaTypeV1, HandoffSecretHeader, OneTimeSecretBytes)
	}
	if len(Routes) != 7 {
		t.Fatalf("routes=%d want 7", len(Routes))
	}
	seen := map[string]bool{}
	for _, route := range Routes {
		key := route.Method + " " + route.Path
		if seen[key] {
			t.Fatalf("duplicate route %s", key)
		}
		seen[key] = true
		if strings.Contains(route.Path, "{action}") {
			t.Fatalf("caller-selected action route is forbidden: %s", route.Path)
		}
		if route.Audience != "internal" && route.Audience != "external" {
			t.Fatalf("route %s has open audience %q", key, route.Audience)
		}
	}
}

func TestHandoffLifecycleStaysSeparateAndExact(t *testing.T) {
	want := []string{"issued", "accepted", "active", "waiting", "blocked", "succeeded", "failed"}
	if !reflect.DeepEqual(HandoffStates, want) {
		t.Fatalf("handoff states=%v want %v", HandoffStates, want)
	}
}

func TestSecretNeverAppearsInJSONDTOs(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(CreateHandoffRequest{}), reflect.TypeOf(CredentialEpochRequest{}),
		reflect.TypeOf(RevokeHandoffRequest{}), reflect.TypeOf(HandoffMetadata{}),
		reflect.TypeOf(PullResponse{}), reflect.TypeOf(AcceptRequest{}),
		reflect.TypeOf(ReportRequest{}), reflect.TypeOf(ReportReceipt{}),
	}
	for _, typ := range types {
		for index := 0; index < typ.NumField(); index++ {
			name := strings.Split(typ.Field(index).Tag.Get("json"), ",")[0]
			if strings.Contains(name, "secret") || strings.Contains(name, "token") || strings.Contains(name, "credential_value") {
				t.Fatalf("%s exposes forbidden JSON field %q", typ.Name(), name)
			}
		}
	}
}

func TestJanusEvidenceHasOnlyValueFreeFields(t *testing.T) {
	want := []string{"authorized", "credential_ready", "kind", "observed_at", "result"}
	typ := reflect.TypeOf(JanusEvidence{})
	got := make([]string, 0, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		got = append(got, strings.Split(typ.Field(index).Tag.Get("json"), ",")[0])
	}
	// Source order is itself reviewable; compare by membership without adding a
	// runtime sort dependency to the contract package.
	for _, field := range want {
		found := false
		for _, candidate := range got {
			found = found || candidate == field
		}
		if !found {
			t.Errorf("Janus evidence field %q missing; got %v", field, got)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("Janus evidence gained a field: %v", got)
	}
}
