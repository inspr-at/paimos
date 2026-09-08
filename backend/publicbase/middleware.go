// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package publicbase

import (
	"bytes"
	"fmt"
	"net/http"
)

// RejectOutsideAndStrip 404s requests that are not on the configured
// segment-boundary mount, then rewrites Path/RawPath so downstream chi
// routes stay `/api` and `/*`. Empty prefix is a no-op.
//
// Classification and access logging must run *before* this middleware so
// leftover root-shaped control URLs are still classified while the original
// request path is intact. This middleware never reads forwarded headers.
func RejectOutsideAndStrip(prefix Path) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if prefix.Empty() {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r == nil || r.URL == nil {
				http.NotFound(w, r)
				return
			}
			stripped, ok := prefix.Strip(r.URL.Path)
			if !ok {
				http.NotFound(w, r)
				return
			}
			cloned := r.Clone(r.Context())
			u := *r.URL
			u.Path = stripped
			if r.URL.RawPath != "" {
				if rawStripped, ok := prefix.Strip(r.URL.RawPath); ok {
					u.RawPath = rawStripped
				} else {
					http.NotFound(w, r)
					return
				}
			}
			cloned.URL = &u
			next.ServeHTTP(w, cloned)
		})
	}
}

// InjectIndexBootstrap inserts the app-owned SPA bootstrap. The prefix is
// taken from process configuration, never from the request.
func InjectIndexBootstrap(html []byte, prefix Path) []byte {
	href := prefix.BaseHref()
	snippet := []byte(fmt.Sprintf(
		`<base href="%s"><script>window.__PAIMOS_PUBLIC_BASE_PATH__=%q;</script>`,
		href, prefix.String(),
	))
	lower := bytes.ToLower(html)
	head := bytes.Index(lower, []byte("<head"))
	if head < 0 {
		out := make([]byte, 0, len(snippet)+len(html))
		out = append(out, snippet...)
		return append(out, html...)
	}
	relEnd := bytes.IndexByte(html[head:], '>')
	if relEnd < 0 {
		out := make([]byte, 0, len(snippet)+len(html))
		out = append(out, snippet...)
		return append(out, html...)
	}
	insertAt := head + relEnd + 1
	out := make([]byte, 0, len(html)+len(snippet))
	out = append(out, html[:insertAt]...)
	out = append(out, snippet...)
	out = append(out, html[insertAt:]...)
	return out
}
