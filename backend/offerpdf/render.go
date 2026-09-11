// SPDX-License-Identifier: AGPL-3.0-only
// Package offerpdf prints the same bundled Vue offer component as the customer page.
package offerpdf

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

func Available() bool {
	if _, err := exec.LookPath("/usr/bin/env"); err != nil {
		return false
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"} {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	return false
}

func Render(ctx context.Context, offer any, publicURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	dir := os.Getenv("STATIC_DIR")
	if dir == "" {
		dir = "../frontend/dist"
	}
	// #nosec G703 -- STATIC_DIR is operator-owned startup configuration, never request or document input.
	if _, err := os.Stat(filepath.Join(dir, "offer-pdf.html")); err != nil {
		return nil, errors.New("pdf_assets_unavailable")
	}
	payload, err := json.Marshal(map[string]any{"offer": offer, "publicUrl": publicURL})
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	origin := "http://" + listener.Addr().String()
	// This server is local-only, contains no session credentials and serves only
	// the dedicated renderer/assets. No access logs or customer capability fetches.
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		if r.Host != listener.Addr().String() || r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		switch {
		case r.URL.Path == "/payload":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(payload)
		case r.URL.Path == "/offer-pdf.html" || strings.HasPrefix(r.URL.Path, "/assets/"):
			http.FileServer(http.Dir(dir)).ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	// Chromium/crashpad requires a writable HOME even with an explicit profile.
	// Keep all browser state private, temporary and outside the read-only rootfs.
	browserHome, err := os.MkdirTemp("", "paimos-offer-pdf-")
	if err != nil {
		return nil, errors.New("pdf_temp_unavailable")
	}
	defer os.RemoveAll(browserHome)
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.DisableGPU, chromedp.UserDataDir(filepath.Join(browserHome, "profile")))
	// A renderer must never inherit SMTP credentials or other service secrets.
	options = append(options, chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) {
		// chromedp merges the parent environment after this callback. Use env's
		// exec mode (no shell, no output) to clear it at the actual browser boundary.
		original := append([]string{}, cmd.Args...)
		cmd.Path = "/usr/bin/env"
		cmd.Args = append([]string{"env", "-i", "PATH=" + os.Getenv("PATH"), "HOME=" + browserHome, "XDG_CONFIG_HOME=" + filepath.Join(browserHome, "config"), "XDG_CACHE_HOME=" + filepath.Join(browserHome, "cache")}, original...)
		configureRendererProcess(cmd)
	}))
	alloc, closeAlloc := chromedp.NewExecAllocator(ctx, options...)
	defer closeAlloc()
	browser, closeBrowser := chromedp.NewContext(alloc)
	defer closeBrowser()
	var pdf []byte
	err = chromedp.Run(browser,
		chromedp.Navigate(origin+"/offer-pdf.html"),
		chromedp.WaitVisible(`[data-pdf-ready="true"]`, chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var e error
			pdf, _, e = page.PrintToPDF().WithPrintBackground(true).WithPreferCSSPageSize(true).WithDisplayHeaderFooter(false).Do(ctx)
			return e
		}),
	)
	if err != nil {
		return nil, errors.New("pdf_render_failed")
	}
	if len(pdf) > 20<<20 || !strings.HasPrefix(string(pdf), "%PDF-") {
		return nil, errors.New("pdf_invalid")
	}
	return pdf, nil
}
