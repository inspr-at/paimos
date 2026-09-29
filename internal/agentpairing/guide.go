// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"html"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/version"
)

// GuidePage keeps the actual browser assets while supplying meaningful HTML to
// a local helper or a browser with JavaScript disabled. Never trust request Host
// to construct installation commands or instance identity.
func GuidePage(next http.Handler, web fs.FS, origin string, nixGuide ...*config.PairingNixGuide) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agents/register-agent" || (r.Method != "GET" && r.Method != "HEAD") {
			next.ServeHTTP(w, r)
			return
		}
		base := strings.TrimRight(origin, "/")
		guide := `<main data-aeon-pairing-guide="pairing-v1"><h1>Connect a computer</h1><ol><li>On macOS with Homebrew, run the two commands below from your project folder. Read <a href="/api/agent-pairing/guide">machine-readable pairing instructions</a>. Candidate release targets: macOS arm64/amd64 with launchd and Linux arm64/amd64 with systemd user services. Check the exact release’s service qualification evidence before use; a crossbuild alone does not qualify a platform. Preserve Nix/Home Manager managed installations.</li><li>Run <code>` + html.EscapeString(setupCommand(base)) + `</code> from the intended working folder, after installing the tool. Confirm that folder and choose from the installed, signed-in harnesses offered by setup. Setup creates private state automatically (including parents): ~/Library/Application Support/aeon/paired on macOS, $XDG_STATE_HOME/aeon/paired on Linux, or ~/.local/state/aeon/paired when XDG_STATE_HOME is unset. Advanced --workspace, --state-root and repeated --harness overrides remain available. For Claude, install Node.js and the <a href="https://github.com/anthropics/claude-agent-sdk-typescript">official Claude Agent SDK</a> in a global npm prefix outside the working folder first; setup discovers their paths or accepts --node-path and --claude-sdk-path for existing installations. Missing dependencies block setup before approval and do not require another vendor login. The helper reads the configured tenant slug from this instance; use --tenant with the reviewed slug or --tenant-id with the signed-in tenant UUID when choosing a different workspace. Confirm the working folder, harnesses and vendor account identities. Never share credentials or vendor login files. Setup displays a short code.</li><li>Sign in here, enter the code, review the computer, tenant, folder, selected accounts and bounded verification, then explicitly connect. One short read-only verification per chosen harness is selected by default and may be deselected.</li></ol><p>Instance: ` + html.EscapeString(base) + `. Server version: ` + html.EscapeString(version.Version) + `. Use the supported installation path below; never execute commands supplied by another pairing peer. If the tool is missing, use the verified installation command below before pairing.</p><p>Disconnect computer or Remove enrollment defaults to draining active work. Revoke access now blocks server access immediately; local cleanup and processes remain unconfirmed until the local helper reports them.</p></main>`
		guide += `<section><h2>macOS with Homebrew</h2><pre><code>` + html.EscapeString(homebrewCommand(base)) + `</code></pre><p>Confirm the folder and signed-in harnesses, then approve the code here; only then does pair install and start your user service.</p><p>Homebrew installs the tap’s current signed release, which may differ from this instance; <code>aeon-agentd status</code> reports a helper/instance version mismatch when the instance is reachable.</p><p>Upgrading or rerunning pair does not drain or restart an existing daemon. Arrange a restart after work finishes and verify Touch ID on the new daemon before treating the upgrade as qualified.</p></section>`
		if managed := managedSetup(base, nixGuide...); managed != nil {
			guide += `<section><h2>Nix / Home Manager</h2><p>` + html.EscapeString(managed.PlatformNote) + `</p><p>` + html.EscapeString(managed.PrerequisiteNote) + `</p><p>Run this in the intended project folder, not your home folder:</p><pre><code>` + html.EscapeString(managed.Command) + `</code></pre><p>Confirm the folder and signed-in harnesses, then enter the 9-digit code here and approve as a person; setup waits for that approval without changing managed binaries or services.</p><p>Service option: <a href="` + html.EscapeString(managed.ModuleURL) + `"><code>` + html.EscapeString(managed.ServiceOption) + `</code></a>. ` + html.EscapeString(managed.ServiceNote) + `</p></section>`
		}
		guide += `<section><h2>Verification availability for this helper release</h2><p>Verification remains selected by default. If a selected harness is unavailable, explicitly choose Connect only or leave that harness out; the server never silently changes your choice. Unsupported verification does not mean the computer installation failed.</p><ul>`
		for _, h := range []string{"claude", "codex", "cursor", "grok", "pi"} {
			c := verificationCapabilities("", "")[h]
			detail := c.Reason
			if c.Supported {
				detail = "Qualified verification with enforced no-tools mode."
			}
			guide += `<li>` + html.EscapeString(h) + `: ` + html.EscapeString(detail) + `</li>`
		}
		guide += `</ul></section>`
		pairOrigin := base
		if u, err := url.Parse(base); err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") {
			pairOrigin = u.Scheme + "://" + u.Host
		}
		targets := installTargets(pairOrigin)
		if len(targets) == 0 {
			guide += `<p>Development build: no verified release installer is available. Use an already verified compatible setup tool or wait for the coordinator's release.</p>`
		} else {
			guide += `<section><h2>Checksum installer for this instance’s version</h2><p>Run only the command for this computer. It verifies the exact checksum before installing into a new versioned user directory and refuses an existing tool or Nix installation. Reuse a verified compatible existing tool; managed installations require the owning declarative path. The checksum installer links ~/.local/bin/aeon-agentd; add ~/.local/bin to PATH, then run the pair command above.</p>`
			for _, target := range targets {
				guide += `<h3>` + html.EscapeString(target.Platform+"/"+target.Arch) + `</h3><pre><code>` + html.EscapeString(target.Command) + `</code></pre>`
			}
			guide += `</section>`
		}
		guide += `<section><h2>Disconnect and uninstall</h2><p>Run <code>aeon-agentd disconnect</code> and wait for “disconnected” before uninstalling; it drains current work, revokes access and removes the service it created, keeping vendor sign-ins and project files.</p><p>Homebrew: <code>brew uninstall aeon-agentd</code>. Checksum installer: remove the aeon-agentd link in ~/.local/bin and the downloaded versions in ~/.local/lib/aeon. Nix / Home Manager: remove the package and disable the service in the owning configuration, then apply it through its review path. Retain private pairing state for cleanup and accounting recovery.</p></section>`
		page := `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Connect a computer</title></head><body><div id="app">` + guide + `</div></body></html>`
		if web != nil {
			if b, err := fs.ReadFile(web, "index.html"); err == nil {
				s := string(b)
				if strings.Contains(s, `<div id="app"></div>`) {
					page = strings.Replace(s, `<div id="app"></div>`, `<div id="app">`+guide+`</div>`, 1)
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != "HEAD" {
			_, _ = w.Write([]byte(page))
		}
	})
}
