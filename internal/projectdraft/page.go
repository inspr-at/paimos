// SPDX-License-Identifier: AGPL-3.0-only
package projectdraft

import (
	"crypto/sha256"
	"encoding/base64"
	"html"
	"net/http"
	"strings"
)

// A refused handoff is a browser navigation, so it answers with a small page
// in the conversation's language (or the browser's, before the payload is
// read). The page reveals nothing about drafts and runs no script; its only
// style is allowed by hash.
type pageCopy struct{ title, hint string }

type pageKind int

const (
	pageUnavailable pageKind = iota
	pageUnreadable
	pageVersion
	pageTooLarge
	pageBusy
)

var pages = map[string]map[pageKind]pageCopy{
	"en": {
		pageUnavailable: {"Handing over from START is not available here.", "Ask the person who runs this Paimos to turn it on."},
		pageUnreadable:  {"This handoff could not be read.", "Start again from START."},
		pageVersion:     {"This handoff comes from a newer START.", "This Paimos reads handoff version 1. Start again once Paimos is updated."},
		pageTooLarge:    {"This conversation is too large to hand over.", "Shorten it in START, then start again."},
		pageBusy:        {"Too many handoffs right now.", "Wait a minute, then start again from START."},
	},
	"de": {
		pageUnavailable: {"Die Übergabe aus START ist hier nicht verfügbar.", "Frag die Person, die dieses Paimos betreibt, ob sie sie einschaltet."},
		pageUnreadable:  {"Diese Übergabe konnte nicht gelesen werden.", "Starte noch einmal in START."},
		pageVersion:     {"Diese Übergabe kommt aus einer neueren START-Version.", "Dieses Paimos liest Übergaben der Version 1. Starte noch einmal, sobald Paimos aktualisiert ist."},
		pageTooLarge:    {"Dieses Gespräch ist zu groß für die Übergabe.", "Kürze es in START und starte dann noch einmal."},
		pageBusy:        {"Gerade kommen zu viele Übergaben an.", "Warte eine Minute und starte dann noch einmal in START."},
	},
}

var pageLink = map[string]string{"en": "Go to Projects", "de": "Zu den Projekten"}

const pageStyle = `:root{color-scheme:light dark;--canvas:#f7f6f2;--ink:#203c3d;--ink-2:#4c6163;--teal:#0e6f6c;--on:#fff}` +
	`@media (prefers-color-scheme:dark){:root{--canvas:#102327;--ink:#edf4f0;--ink-2:#acc3c2;--teal:#a4e5df;--on:#102327}}` +
	`*{box-sizing:border-box}html,body{margin:0;height:100%}` +
	`body{display:grid;place-items:center;padding:24px 20px;background:var(--canvas);color:var(--ink);font:15px/1.5 -apple-system,"SF Pro Text",system-ui,"Segoe UI",Roboto,sans-serif}` +
	`main{display:grid;justify-items:center;gap:8px;max-width:30rem;text-align:center}svg{color:var(--teal);margin-bottom:4px}` +
	`h1{margin:0;font-size:17px;font-weight:600;text-wrap:balance}p{margin:0;color:var(--ink-2);font-size:14px;text-wrap:pretty}` +
	`a{display:inline-flex;align-items:center;justify-content:center;min-height:44px;margin-top:16px;padding:0 20px;border-radius:999px;background:var(--teal);color:var(--on);font-weight:600;text-decoration:none}` +
	`a:focus-visible{outline:2px solid var(--teal);outline-offset:3px}`

var pagePolicy = func() string {
	sum := sha256.Sum256([]byte(pageStyle))
	return "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}()

func pageLanguage(accept string) string {
	for _, part := range strings.Split(accept, ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		if tag == "de" || strings.HasPrefix(tag, "de-") {
			return "de"
		}
		if tag == "en" || strings.HasPrefix(tag, "en-") {
			return "en"
		}
	}
	return "en"
}

func writePage(w http.ResponseWriter, status int, lang string, kind pageKind) {
	if pages[lang] == nil {
		lang = "en"
	}
	text := pages[lang][kind]
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", pagePolicy)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<!doctype html><html lang="` + lang + `"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="referrer" content="no-referrer">` +
		`<title>PAIMOS</title><style>` + pageStyle + `</style></head><body><main>` +
		`<svg width="20" height="20" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="8" cy="8" r="6.2"/><path d="M8 4.6V8l2.2 1.4"/></svg>` +
		`<h1>` + html.EscapeString(text.title) + `</h1><p>` + html.EscapeString(text.hint) + `</p><a href="/">` + html.EscapeString(pageLink[lang]) + `</a></main></body></html>`))
}
