// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHarnessLeaseRequestsNeverFollowRedirects(t *testing.T) {
	lease := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var targetCalls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
			defer target.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(harnessWorkerLeaseHeader) != lease || r.Header.Get("Authorization") != "Bearer private-api-key" {
					t.Error("origin request lost protected credentials")
				}
				w.Header().Set("Location", target.URL+"/capture")
				w.WriteHeader(status)
			}))
			defer origin.Close()
			client := newClient(InstanceConfig{URL: origin.URL, APIKey: "private-api-key"})
			_, err := client.doForHarnessContext(context.Background(), http.MethodPost, "/worker", map[string]string{"worker_lease": lease}, "worker", lease)
			if err == nil || targetCalls.Load() != 0 {
				t.Fatalf("redirect err=%v target_calls=%d", err, targetCalls.Load())
			}
			if strings.Contains(err.Error(), lease) || strings.Contains(err.Error(), "private-api-key") {
				t.Fatal("redirect diagnostic leaked credentials")
			}
		})
	}
}
