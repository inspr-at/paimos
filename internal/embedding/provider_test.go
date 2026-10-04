// SPDX-License-Identifier: AGPL-3.0-only

package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestFromEnvAndHTTPProvider(t *testing.T) {
	t.Setenv("AEON_EMBEDDING_URL", "")
	t.Setenv("AEON_EMBEDDING_MODEL", "")
	t.Setenv("AEON_EMBEDDING_API_KEY", "")
	if p, err := FromEnv(); err != nil || p != nil {
		t.Fatalf("empty env %v %v", p, err)
	}
	t.Setenv("AEON_EMBEDDING_URL", "http://user:secret@example.test/embeddings")
	if _, err := FromEnv(); err == nil {
		t.Fatal("userinfo accepted")
	}
	t.Setenv("AEON_EMBEDDING_URL", "http://127.0.0.1/embeddings")
	t.Setenv("AEON_EMBEDDING_MODEL", "test-model")
	t.Setenv("AEON_EMBEDDING_API_KEY", "local-test")
	var sawAuth, sawModel bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer local-test" {
			sawAuth = true
		}
		var body embedRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model == "test-model" && len(body.Input) == 2 {
			sawModel = true
		}
		first, second := make([]float32, Dimensions), make([]float32, Dimensions)
		first[0], second[0] = 1, 2
		_ = json.NewEncoder(w).Encode(embedResponse{Data: []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}{
			{Index: 1, Embedding: second},
			{Index: 0, Embedding: first},
		}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AEON_EMBEDDING_URL", srv.URL)
	p, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if p.Model() != "test-model" {
		t.Fatal(p.Model())
	}
	got, err := p.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	first, second := make([]float32, Dimensions), make([]float32, Dimensions)
	first[0], second[0] = 1, 2
	if !sawAuth || !sawModel || !reflect.DeepEqual(got, [][]float32{first, second}) {
		t.Fatalf("auth %v model %v len %d", sawAuth, sawModel, len(got))
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	t.Cleanup(bad.Close)
	hp, err := NewHTTPProvider(HTTPConfig{URL: bad.URL, Model: "test-model", Client: bad.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hp.Embed(context.Background(), []string{"a"}); err == nil {
		t.Fatal("status accepted")
	}
}
