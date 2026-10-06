// SPDX-License-Identifier: AGPL-3.0-only

package httpapi

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/inspr-at/paimos/internal/brand"
)

// placeholderHTML is served when the binary has no web build; it carries the
// deployment's wordmark like every other surface.
func placeholderHTML(b brand.Brand) []byte {
	name := html.EscapeString(b.Wordmark)
	return []byte(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + name + `</title>
</head>
<body>
<p>` + name + `</p>
</body>
</html>
`)
}

func spaHandler(fsys fs.FS, b brand.Brand) http.Handler {
	if fsys == nil {
		page := placeholderHTML(b)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !allowRead(w, r) {
				return
			}
			writeHTML(w, r, page)
		})
	}
	files := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowRead(w, r) {
			return
		}
		if name, ok := staticName(r.URL.Path); ok && name != "index.html" {
			if f, err := fsys.Open(name); err == nil {
				st, statErr := f.Stat()
				f.Close()
				if statErr == nil && !st.IsDir() {
					// Vite fingerprints everything under assets/, so it never changes;
					// other files (favicon, brand) revalidate.
					if strings.HasPrefix(name, "assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					} else {
						w.Header().Set("Cache-Control", "no-cache")
					}
					files.ServeHTTP(w, r)
					return
				}
			}
		}
		body, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, r, body)
	})
}

func allowRead(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func staticName(urlPath string) (string, bool) {
	cleaned := path.Clean("/" + urlPath)
	if cleaned == "/" || cleaned == "." {
		return "", false
	}
	name := strings.TrimPrefix(cleaned, "/")
	if name == "" || name == ".." || strings.HasPrefix(name, "../") {
		return "", false
	}
	return name, true
}

func writeHTML(w http.ResponseWriter, r *http.Request, body []byte) {
	// Only the application bootstrap receives a nonce. API responses, public
	// documents and attachments retain their original restrictive policy.
	if bytes.Contains(body, []byte("__AEON_THEME_NONCE__")) {
		var random [24]byte
		if _, err := rand.Read(random[:]); err != nil {
			http.Error(w, "could not prepare application", http.StatusInternalServerError)
			return
		}
		nonce := base64.RawStdEncoding.EncodeToString(random[:])
		body = bytes.ReplaceAll(body, []byte("__AEON_THEME_NONCE__"), []byte(nonce))
		policy := w.Header().Get("Content-Security-Policy")
		w.Header().Set("Content-Security-Policy", policy+"; script-src 'self' 'nonce-"+nonce+"'; style-src 'self' 'nonce-"+nonce+"'")
	}
	// index.html names the fingerprinted bundles of this release: a stale copy
	// points a new tab at bundles a deploy removed (2026-09-25).
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}
